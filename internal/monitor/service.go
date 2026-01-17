package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/fushengyk/arbix/internal/config"
	"github.com/fushengyk/arbix/internal/domain"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// Service defines the monitor service
type Service struct {
	cfg    *config.Config
	js     nats.JetStreamContext
	logger *zap.SugaredLogger

	ctx    context.Context
	cancel context.CancelFunc

	// State
	mu              sync.RWMutex
	priceHistory    map[string][]pricePoint         // Key: exchange:market:symbol
	fundingCache    map[string]domain.FundingEvent  // Key: exchange:symbol
	volatilityState map[string]map[string]ruleState // Strategy:Symbol -> RuleName -> State
	symbolMaxState  map[string]symbolGlobalState    // Strategy:Symbol -> GlobalState (for dedup)
	precisionCache  map[string]int                  // Symbol -> PricePrecision

	notifier *Notifier
}

type pricePoint struct {
	Time  int64
	Price float64
}

type ruleState struct {
	LastTriggerTime  int64
	LastTriggerPrice float64
	LastTriggerPct   float64
}

type symbolGlobalState struct {
	MaxPct          float64
	CooldownEndTime int64
}

// NewService creates a new monitor service
func NewService(cfg *config.Config, js nats.JetStreamContext, logger *zap.SugaredLogger) (*Service, error) {
	ctx, cancel := context.WithCancel(context.Background())

	notifier := NewNotifier(cfg, logger)

	return &Service{
		cfg:             cfg,
		js:              js,
		logger:          logger,
		ctx:             ctx,
		cancel:          cancel,
		priceHistory:    make(map[string][]pricePoint),
		fundingCache:    make(map[string]domain.FundingEvent),
		volatilityState: make(map[string]map[string]ruleState),
		symbolMaxState:  make(map[string]symbolGlobalState),
		precisionCache:  make(map[string]int),
		notifier:        notifier,
	}, nil
}

func (s *Service) Start() error {
	s.logger.Info("🛡️ Starting Monitor Service...")

	// Log loaded strategies
	if len(s.cfg.Strategies) == 0 {
		s.logger.Warn("⚠️ No strategies configured!")
	} else {
		for _, strat := range s.cfg.Strategies {
			s.logger.Infof("📋 Strategy [%s]: %d rules → channels %v", strat.Name, len(strat.Rules), strat.Channels)
		}
	}

	// Subscribe to market data
	sub, err := s.js.Subscribe("market.>", func(msg *nats.Msg) {
		s.handleMessage(msg)
		msg.Ack()
	}, nats.Durable("MONITOR_PROCESSOR_V4"))

	if err != nil {
		return fmt.Errorf("subscribe market: %w", err)
	}

	// Subscribe to metadata (for price precision)
	metaSub, err := s.js.Subscribe("metadata.>", func(msg *nats.Msg) {
		s.handleMetadata(msg)
		msg.Ack()
	}, nats.Durable("MONITOR_METADATA_V2"))

	if err != nil {
		s.logger.Warnf("Failed to subscribe to metadata: %v", err)
	} else {
		s.logger.Info("✅ Subscribed to metadata.>")
	}

	s.logger.Info("✅ Monitor Service Listening on market.>")

	go func() {
		<-s.ctx.Done()
		sub.Unsubscribe()
		if metaSub != nil {
			metaSub.Unsubscribe()
		}
	}()

	return nil
}

func (s *Service) Stop() {
	s.logger.Info("🛑 Stopping Monitor Service...")
	s.cancel()
}

func (s *Service) handleMetadata(msg *nats.Msg) {
	var meta domain.SymbolMetadataEvent
	if err := json.Unmarshal(msg.Data, &meta); err != nil {
		return
	}
	s.processMetadata(meta)
}

func (s *Service) processMetadata(event domain.SymbolMetadataEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.precisionCache[event.Symbol] = event.PricePrecision
}

func (s *Service) handleMessage(msg *nats.Msg) {
	// Try Kline
	var kline domain.KlineEvent
	if err := json.Unmarshal(msg.Data, &kline); err == nil && kline.OpenTime > 0 {
		s.processKline(kline)
		return
	}

	// Try Funding
	var funding domain.FundingEvent
	if err := json.Unmarshal(msg.Data, &funding); err == nil && funding.Rate != 0 {
		s.processFunding(funding)
		return
	}
}

func (s *Service) processFunding(event domain.FundingEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%s:%s", event.Exchange, event.Symbol)
	s.fundingCache[key] = event
}

func (s *Service) getPrecisionUnsafe(symbol string) int {
	if p, ok := s.precisionCache[symbol]; ok {
		return p
	}
	return -1
}

func (s *Service) processKline(event domain.KlineEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Update History
	key := fmt.Sprintf("%s:%s:%s", event.Exchange, event.Market, event.Symbol)
	s.updateHistory(key, event)

	// 2. Check all strategies
	for i := range s.cfg.Strategies {
		s.checkStrategy(&s.cfg.Strategies[i], event)
	}
}

func (s *Service) updateHistory(key string, event domain.KlineEvent) {
	history := s.priceHistory[key]
	history = append(history, pricePoint{Time: event.OpenTime, Price: event.Close})

	// Keep 65 mins of data
	cutoff := event.OpenTime - (65 * 60 * 1000)
	startIdx := 0
	for i, p := range history {
		if p.Time >= cutoff {
			startIdx = i
			break
		}
	}
	if startIdx > 0 {
		history = history[startIdx:]
	}
	s.priceHistory[key] = history
}

func (s *Service) checkStrategy(strategy *config.StrategyConfig, event domain.KlineEvent) {
	historyKey := fmt.Sprintf("%s:%s:%s", event.Exchange, event.Market, event.Symbol)
	history := s.priceHistory[historyKey]

	if len(history) < 2 {
		return
	}

	// Check each rule in the strategy
	for _, rule := range strategy.Rules {
		targetTime := event.OpenTime - rule.Window.Milliseconds()

		// Find min and max price within the window
		var minPrice, maxPrice float64
		hasData := false

		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Time < targetTime {
				break // Outside window
			}
			price := history[i].Price
			if !hasData {
				minPrice = price
				maxPrice = price
				hasData = true
			} else {
				if price < minPrice {
					minPrice = price
				}
				if price > maxPrice {
					maxPrice = price
				}
			}
		}

		if !hasData || minPrice == 0 || maxPrice == 0 {
			continue
		}

		// Calculate max pump (from min) and max dump (from max)
		pumpPct := (event.Close - minPrice) / minPrice * 100 // Positive = pump
		dumpPct := (event.Close - maxPrice) / maxPrice * 100 // Negative = dump

		// Use the larger absolute change
		var pct float64
		var basePrice float64
		if math.Abs(pumpPct) >= math.Abs(dumpPct) {
			pct = pumpPct
			basePrice = minPrice
		} else {
			pct = dumpPct
			basePrice = maxPrice
		}

		absPct := math.Abs(pct)

		if absPct >= rule.Threshold {
			// Smart deduplication: check if this symbol already triggered higher pct during cooldown
			stateKey := fmt.Sprintf("%s:%s", strategy.Name, event.Symbol)
			globalState := s.symbolMaxState[stateKey]
			now := time.Now().UnixMilli()

			// Reset maxPct if cooldown has expired
			if now > globalState.CooldownEndTime {
				globalState.MaxPct = 0
			}

			// Apply ExpansionFactor to cross-rule deduplication
			factor := strategy.ExpansionFactor
			if factor == 0 {
				factor = 1.2
			}

			// Only trigger if current pct >= maxPct * factor during cooldown
			if absPct >= globalState.MaxPct*factor {
				s.handleTrigger(strategy, rule, event, basePrice, event.Close, pct)
				break // Only one alert per check - first matching rule wins
			}
		}
	}
}

func (s *Service) handleTrigger(strategy *config.StrategyConfig, rule config.RuleConfig, event domain.KlineEvent, start, end, pct float64) {
	// Unique key for this strategy+symbol+rule
	stateKey := fmt.Sprintf("%s:%s", strategy.Name, event.Symbol)
	if _, ok := s.volatilityState[stateKey]; !ok {
		s.volatilityState[stateKey] = make(map[string]ruleState)
	}
	state := s.volatilityState[stateKey][rule.Name]

	now := time.Now().UnixMilli()

	inCooldown := (now - state.LastTriggerTime) < rule.Cooldown.Milliseconds()
	shouldNotify := false

	if !inCooldown {
		shouldNotify = true
	} else {
		// Expansion Check
		factor := strategy.ExpansionFactor
		if factor == 0 {
			factor = 1.2
		}

		isExpansion := math.Abs(pct) >= math.Abs(state.LastTriggerPct)*factor

		if isExpansion {
			shouldNotify = true
		}
	}

	if shouldNotify {
		// Update state
		s.volatilityState[stateKey][rule.Name] = ruleState{
			LastTriggerTime:  now,
			LastTriggerPrice: end,
			LastTriggerPct:   pct,
		}

		// Update global max state for deduplication
		absPct := math.Abs(pct)
		newCooldownEnd := now + rule.Cooldown.Milliseconds()
		currentState := s.symbolMaxState[stateKey]

		// Only extend cooldown, never shorten
		if newCooldownEnd < currentState.CooldownEndTime {
			newCooldownEnd = currentState.CooldownEndTime
		}

		s.symbolMaxState[stateKey] = symbolGlobalState{
			MaxPct:          absPct,
			CooldownEndTime: newCooldownEnd,
		}

		// Enrich data
		otherMarket, funding := s.getCrossMarketData(event, rule)

		// Build payload
		payload := AlertPayload{
			StrategyName: strategy.Name,
			RuleName:     rule.Name,
			Window:       rule.Window.String(),
			Symbol:       event.Symbol,
			TriggerTime:  time.Now(),

			BaseMarket: MarketInfo{
				Type:       string(event.Market),
				PriceStart: start,
				PriceEnd:   end,
				ChangePct:  pct,
			},

			HasOther:   otherMarket != nil,
			HasFunding: funding != nil,

			Precision: s.getPrecisionUnsafe(event.Symbol),
		}

		if otherMarket != nil {
			payload.OtherMarket = *otherMarket
			var spotP, futP float64
			if event.Market == domain.MarketSpot {
				spotP = end
				futP = otherMarket.PriceEnd
			} else {
				spotP = otherMarket.PriceEnd
				futP = end
			}
			if spotP != 0 {
				payload.SpreadPct = (futP - spotP) / spotP * 100
			}
		}

		if funding != nil {
			payload.FundingRate = funding.Rate
			payload.NextFunding = funding.NextFunding
		}

		// Send to strategy's channels
		go s.notifier.Send(payload, strategy.Channels)
	}
}

func (s *Service) getCrossMarketData(event domain.KlineEvent, rule config.RuleConfig) (*MarketInfo, *domain.FundingEvent) {
	var otherType domain.MarketType
	if event.Market == domain.MarketSpot {
		otherType = domain.MarketFuturePerm
	} else {
		otherType = domain.MarketSpot
	}

	key := fmt.Sprintf("%s:%s:%s", event.Exchange, otherType, event.Symbol)
	hist := s.priceHistory[key]

	var otherInfo *MarketInfo
	if len(hist) >= 2 {
		latest := hist[len(hist)-1]
		targetTime := event.OpenTime - rule.Window.Milliseconds()
		var prevPrice float64

		for i := len(hist) - 1; i >= 0; i-- {
			if hist[i].Time <= targetTime {
				prevPrice = hist[i].Price
				break
			}
		}

		if prevPrice != 0 {
			changePct := (latest.Price - prevPrice) / prevPrice * 100
			otherInfo = &MarketInfo{
				Type:       string(otherType),
				PriceStart: prevPrice,
				PriceEnd:   latest.Price,
				ChangePct:  changePct,
			}
		} else if len(hist) >= 1 {
			// Fallback: use earliest available price when exact time not found
			prevPrice = hist[0].Price
			changePct := (latest.Price - prevPrice) / prevPrice * 100
			otherInfo = &MarketInfo{
				Type:       string(otherType),
				PriceStart: prevPrice,
				PriceEnd:   latest.Price,
				ChangePct:  changePct,
			}
		} else {
			otherInfo = &MarketInfo{
				Type:     string(otherType),
				PriceEnd: latest.Price,
			}
		}
	}

	fKey := fmt.Sprintf("%s:%s", event.Exchange, event.Symbol)
	funding, ok := s.fundingCache[fKey]
	var fPtr *domain.FundingEvent
	if ok {
		fPtr = &funding
	}

	return otherInfo, fPtr
}

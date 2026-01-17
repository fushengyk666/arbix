package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fushengyk/arbix/internal/config"
	"github.com/fushengyk/arbix/internal/domain"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// Collector implements domain.MarketDataCollector for Binance
type Collector struct {
	cfg     config.BinanceConfig
	handler domain.MarketDataHandler
	logger  *zap.SugaredLogger

	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.RWMutex
	clients     []*wsClient
	lastSymbols map[string]bool

	// Statistics (per minute)
	stats collectorStats
}

// collectorStats tracks message statistics
type collectorStats struct {
	msgRecv     atomic.Uint64
	msgParsed   atomic.Uint64
	msgFailed   atomic.Uint64
	lastMsgRecv uint64
	lastParsed  uint64
	lastFailed  uint64

	mu          sync.Mutex
	spotSymbols map[string]bool
	futSymbols  map[string]bool
	fundSymbols map[string]bool
}

// NewCollector creates a new Binance market data collector
func NewCollector(cfg config.BinanceConfig, handler domain.MarketDataHandler) (domain.MarketDataCollector, error) {
	logger, _ := zap.NewProduction()
	return newCollector(cfg, handler, logger.Sugar()), nil
}

// NewCollectorWithLogger creates a collector with a specific logger
func NewCollectorWithLogger(cfg config.BinanceConfig, handler domain.MarketDataHandler, logger *zap.SugaredLogger) *Collector {
	return newCollector(cfg, handler, logger)
}

func newCollector(cfg config.BinanceConfig, handler domain.MarketDataHandler, logger *zap.SugaredLogger) *Collector {
	ctx, cancel := context.WithCancel(context.Background())
	return &Collector{
		cfg:         cfg,
		handler:     handler,
		logger:      logger,
		ctx:         ctx,
		cancel:      cancel,
		lastSymbols: make(map[string]bool),
	}
}

func (c *Collector) Name() domain.ExchangeID {
	return domain.ExchangeBinance
}

func (c *Collector) Start(ctx context.Context) error {
	go c.runStatsLogger()
	go c.runDiscovery()
	return nil
}

func (c *Collector) Stop() error {
	c.logger.Info("[Binance] Stopping collector...")
	c.cancel()

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, client := range c.clients {
		client.Stop()
	}
	return nil
}

func (c *Collector) Subscribe(symbols []*domain.Symbol) error {
	return c.Refresh(symbols)
}

// runDiscovery periodically fetches exchange info and updates subscriptions
func (c *Collector) runDiscovery() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	c.logger.Info("[Binance] Starting symbol discovery (1m interval)...")

	// Initial fetch
	if err := c.refreshSymbols(); err != nil {
		c.logger.Errorf("[Binance] Initial discovery failed: %v", err)
	}

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if err := c.refreshSymbols(); err != nil {
				c.logger.Errorf("[Binance] Discovery failed: %v", err)
			}
		}
	}
}

func (c *Collector) refreshSymbols() error {
	start := time.Now()
	var newSymbols []*domain.Symbol
	var allMetadata []domain.SymbolMetadataEvent

	// Fetch Spot
	if c.cfg.Markets.SpotEnabled {
		spot, metadata, err := c.fetchSpotSymbols()
		if err != nil {
			return fmt.Errorf("fetch spot: %w", err)
		}
		newSymbols = append(newSymbols, spot...)
		allMetadata = append(allMetadata, metadata...)
	}

	// Fetch Futures
	if c.cfg.Markets.FuturesEnabled {
		fut, metadata, err := c.fetchFuturesSymbols()
		if err != nil {
			return fmt.Errorf("fetch futures: %w", err)
		}
		newSymbols = append(newSymbols, fut...)
		allMetadata = append(allMetadata, metadata...)
	}

	if len(newSymbols) == 0 {
		return fmt.Errorf("no symbols found")
	}

	// Publish metadata events
	for _, m := range allMetadata {
		if c.handler != nil {
			c.handler.OnMetadata(m)
		}
	}

	// Build new map and check for changes
	c.mu.Lock()
	changed := false
	currentMap := make(map[string]bool)

	for _, s := range newSymbols {
		key := fmt.Sprintf("%s:%s", s.MarketType, s.RawSymbol)
		currentMap[key] = true
		if !c.lastSymbols[key] {
			changed = true
		}
	}

	if !changed && len(currentMap) != len(c.lastSymbols) {
		changed = true
	}

	if changed {
		c.lastSymbols = currentMap
	}
	c.mu.Unlock()

	if !changed {
		return nil
	}

	c.logger.Infof("[Binance] Symbol change detected! Active: %d (Fetch: %v)", len(newSymbols), time.Since(start))
	return c.Refresh(newSymbols)
}

type exchangeInfoResp struct {
	Symbols []struct {
		Symbol         string `json:"symbol"`
		Status         string `json:"status"`
		QuoteAsset     string `json:"quoteAsset"`
		PricePrecision int    `json:"quotePrecision"`
	} `json:"symbols"`
}

func (c *Collector) fetchSpotSymbols() ([]*domain.Symbol, []domain.SymbolMetadataEvent, error) {
	resp, err := http.Get(c.cfg.RestBaseURL + "/api/v3/exchangeInfo")
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("bad status: %s", resp.Status)
	}

	var info exchangeInfoResp
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, nil, err
	}

	var parsed []*domain.Symbol
	var metadata []domain.SymbolMetadataEvent
	now := time.Now().UnixMilli()

	for _, s := range info.Symbols {
		if s.Status == "TRADING" && s.QuoteAsset == "USDT" {
			parsed = append(parsed, &domain.Symbol{
				Exchange:   domain.ExchangeBinance,
				RawSymbol:  s.Symbol,
				BaseAsset:  strings.TrimSuffix(s.Symbol, "USDT"),
				QuoteAsset: "USDT",
				MarketType: domain.MarketSpot,
			})
			metadata = append(metadata, domain.SymbolMetadataEvent{
				Exchange:       domain.ExchangeBinance,
				Symbol:         s.Symbol,
				Market:         domain.MarketSpot,
				PricePrecision: s.PricePrecision,
				Timestamp:      now,
			})
		}
	}

	return parsed, metadata, nil
}

func (c *Collector) fetchFuturesSymbols() ([]*domain.Symbol, []domain.SymbolMetadataEvent, error) {
	url := c.cfg.FuturesRest + "/fapi/v1/exchangeInfo"
	resp, err := http.Get(url)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("bad status: %s", resp.Status)
	}

	var info exchangeInfoResp
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, nil, err
	}

	var parsed []*domain.Symbol
	var metadata []domain.SymbolMetadataEvent
	now := time.Now().UnixMilli()

	for _, s := range info.Symbols {
		if s.Status == "TRADING" && s.QuoteAsset == "USDT" {
			parsed = append(parsed, &domain.Symbol{
				Exchange:   domain.ExchangeBinance,
				RawSymbol:  s.Symbol,
				BaseAsset:  strings.TrimSuffix(s.Symbol, "USDT"),
				QuoteAsset: "USDT",
				MarketType: domain.MarketFuturePerm,
			})
			metadata = append(metadata, domain.SymbolMetadataEvent{
				Exchange:       domain.ExchangeBinance,
				Symbol:         s.Symbol,
				Market:         domain.MarketFuturePerm,
				PricePrecision: s.PricePrecision,
				Timestamp:      now,
			})
		}
	}

	return parsed, metadata, nil
}

// Refresh implements hot-swap subscription update
func (c *Collector) Refresh(symbols []*domain.Symbol) error {
	c.logger.Infof("[Binance] Refreshing subscriptions for %d symbols...", len(symbols))

	// Build stream lists
	spotStreams, futuresStreams, fundingStreams := c.buildStreamLists(symbols)

	c.logger.Infof("[Binance] Streams: Spot=%d, Futures=%d, Funding=%d",
		len(spotStreams), len(futuresStreams), len(fundingStreams))

	// Create new WebSocket clients
	newClients := c.createClients(spotStreams, futuresStreams, fundingStreams)

	c.logger.Infof("[Binance] Starting %d WebSocket clients...", len(newClients))

	// Start clients with connection stagger
	c.startClients(newClients)

	// Hot swap: replace old clients with new ones
	c.mu.Lock()
	oldClients := c.clients
	c.clients = newClients
	c.mu.Unlock()

	// Cleanup old clients in background
	if len(oldClients) > 0 {
		go c.stopClients(oldClients)
	}

	c.logger.Info("[Binance] Refresh complete")
	return nil
}

// buildStreamLists creates stream subscription lists from symbols
func (c *Collector) buildStreamLists(symbols []*domain.Symbol) (spot, futures, funding []string) {
	for _, sym := range symbols {
		stream := strings.ToLower(sym.RawSymbol)

		switch sym.MarketType {
		case domain.MarketSpot:
			if c.cfg.Markets.SpotEnabled {
				spot = append(spot, url.QueryEscape(stream+"@kline_1m"))
			}
		case domain.MarketFuturePerm:
			if c.cfg.Markets.FuturesEnabled {
				futures = append(futures, url.QueryEscape(stream+"@kline_1m"))
				funding = append(funding, url.QueryEscape(stream+"@markPrice"))
			}
		}
	}
	return
}

// createClients creates WebSocket clients for all stream types
func (c *Collector) createClients(spot, futures, funding []string) []*wsClient {
	var clients []*wsClient
	maxStreams := c.cfg.WebSocket.MaxStreamsPerConn
	if maxStreams == 0 {
		maxStreams = 50
	}

	// Helper to chunk streams into multiple clients
	createBatch := func(baseURL string, streams []string, handler func([]byte), prefix string) {
		for i := 0; i < len(streams); i += maxStreams {
			end := min(i+maxStreams, len(streams))
			chunk := streams[i:end]
			name := fmt.Sprintf("%s-%d", prefix, i/maxStreams)
			client := newWSClient(baseURL, chunk, handler, c.logger, name, c.cfg.WebSocket)
			clients = append(clients, client)
		}
	}

	createBatch(c.cfg.WSBaseURL, spot, c.handleSpotMessage, "Spot")
	createBatch(c.cfg.FuturesWS, futures, c.handleFuturesMessage, "Futures")
	createBatch(c.cfg.FuturesWS, funding, c.handleFundingMessage, "Funding")

	return clients
}

// startClients starts clients with staggered connections
func (c *Collector) startClients(clients []*wsClient) {
	stagger := c.cfg.WebSocket.ConnectStagger
	if stagger == 0 {
		stagger = 200 * time.Millisecond
	}

	for i, client := range clients {
		if i > 0 {
			time.Sleep(stagger)
		}
		client.Start()

		if (i+1)%10 == 0 {
			c.logger.Infof("[Binance] Started %d/%d clients", i+1, len(clients))
		}
	}
}

// stopClients gracefully stops old clients
func (c *Collector) stopClients(clients []*wsClient) {
	c.logger.Infof("[Binance] Closing %d old clients...", len(clients))
	for _, client := range clients {
		client.Stop()
	}
}

// --- Message Processing ---

func (c *Collector) handleSpotMessage(msg []byte) {
	c.processMessage(msg, domain.MarketSpot, false)
}

func (c *Collector) handleFuturesMessage(msg []byte) {
	c.processMessage(msg, domain.MarketFuturePerm, false)
}

func (c *Collector) handleFundingMessage(msg []byte) {
	c.processMessage(msg, domain.MarketFuturePerm, true)
}

// combinedStreamPayload represents Binance combined stream message format
type combinedStreamPayload struct {
	Stream string          `json:"stream"`
	Data   json.RawMessage `json:"data"`
}

func (c *Collector) processMessage(msg []byte, marketType domain.MarketType, isFunding bool) {
	c.stats.msgRecv.Add(1)

	if len(msg) == 0 {
		c.stats.msgFailed.Add(1)
		return
	}

	// Check for API error response
	if c.isErrorResponse(msg) {
		return
	}

	var payload combinedStreamPayload
	if err := json.Unmarshal(msg, &payload); err != nil {
		c.stats.msgFailed.Add(1)
		return
	}

	if payload.Data == nil {
		c.stats.msgFailed.Add(1)
		return
	}

	if isFunding {
		c.parseFunding(payload.Data)
	} else {
		c.parseKline(payload.Data, marketType)
	}
}

// isErrorResponse checks if message is an API error
func (c *Collector) isErrorResponse(msg []byte) bool {
	if !strings.Contains(string(msg), `"code":`) {
		return false
	}

	var errResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(msg, &errResp); err == nil && errResp.Code != 0 {
		c.logger.Errorf("[Binance] API Error: code=%d msg=%s", errResp.Code, errResp.Msg)
		c.stats.msgFailed.Add(1)
		return true
	}
	return false
}

// klineData represents Binance kline event data
// Note: Must include all fields to prevent json decoder case-insensitive matching issues
type klineData struct {
	Symbol string `json:"s"`
	Kline  struct {
		OpenTime     int64  `json:"t"` // Kline start time
		CloseTime    int64  `json:"T"` // Kline close time (uppercase T)
		Open         string `json:"o"`
		High         string `json:"h"`
		Low          string `json:"l"` // Low price (lowercase l)
		Close        string `json:"c"`
		Volume       string `json:"v"`
		QuoteVolume  string `json:"q"`
		IsClosed     bool   `json:"x"`
		Trades       int64  `json:"n"`
		FirstTradeID int64  `json:"f"`
		LastTradeID  int64  `json:"L"` // Last trade ID (uppercase L)
	} `json:"k"`
}

func (c *Collector) parseKline(data []byte, marketType domain.MarketType) {
	var event klineData
	if err := json.Unmarshal(data, &event); err != nil {
		c.stats.msgFailed.Add(1)
		return
	}

	if event.Symbol == "" {
		c.stats.msgFailed.Add(1)
		return
	}

	open, _ := strconv.ParseFloat(event.Kline.Open, 64)
	high, _ := strconv.ParseFloat(event.Kline.High, 64)
	low, _ := strconv.ParseFloat(event.Kline.Low, 64)
	closePrice, _ := strconv.ParseFloat(event.Kline.Close, 64)
	volume, _ := strconv.ParseFloat(event.Kline.Volume, 64)
	quoteVol, _ := strconv.ParseFloat(event.Kline.QuoteVolume, 64)

	c.stats.msgParsed.Add(1)
	c.recordSymbol(event.Symbol, marketType)

	ke := domain.KlineEvent{
		Exchange:    domain.ExchangeBinance,
		Symbol:      event.Symbol,
		Market:      marketType,
		OpenTime:    event.Kline.OpenTime,
		Open:        open,
		High:        high,
		Low:         low,
		Close:       closePrice,
		Volume:      volume,
		QuoteVolume: quoteVol,
		IsClosed:    event.Kline.IsClosed,
		Timestamp:   time.Now().UnixMilli(),
	}

	if c.handler != nil {
		c.handler.OnKline(ke)
	}
}

// fundingData represents Binance mark price event data
type fundingData struct {
	Symbol      string `json:"s"`
	FundingRate string `json:"r"`
	NextFunding int64  `json:"T"`
}

func (c *Collector) parseFunding(data []byte) {
	var event fundingData
	if err := json.Unmarshal(data, &event); err != nil {
		c.stats.msgFailed.Add(1)
		return
	}

	if event.Symbol == "" {
		c.stats.msgFailed.Add(1)
		return
	}

	rate, _ := strconv.ParseFloat(event.FundingRate, 64)

	c.stats.msgParsed.Add(1)
	c.recordFunding(event.Symbol)

	fe := domain.FundingEvent{
		Exchange:    domain.ExchangeBinance,
		Symbol:      event.Symbol,
		Market:      domain.MarketFuturePerm,
		Rate:        rate,
		NextFunding: event.NextFunding,
		Timestamp:   time.Now().UnixMilli(),
	}

	if c.handler != nil {
		c.handler.OnFunding(fe)
	}
}

// --- Statistics ---

func (c *Collector) recordSymbol(symbol string, marketType domain.MarketType) {
	c.stats.mu.Lock()
	defer c.stats.mu.Unlock()

	switch marketType {
	case domain.MarketSpot:
		if c.stats.spotSymbols == nil {
			c.stats.spotSymbols = make(map[string]bool)
		}
		c.stats.spotSymbols[symbol] = true
	case domain.MarketFuturePerm:
		if c.stats.futSymbols == nil {
			c.stats.futSymbols = make(map[string]bool)
		}
		c.stats.futSymbols[symbol] = true
	}
}

func (c *Collector) recordFunding(symbol string) {
	c.stats.mu.Lock()
	defer c.stats.mu.Unlock()

	if c.stats.fundSymbols == nil {
		c.stats.fundSymbols = make(map[string]bool)
	}
	c.stats.fundSymbols[symbol] = true
}

func (c *Collector) runStatsLogger() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.logStats()
		}
	}
}

func (c *Collector) logStats() {
	// Calculate deltas
	recv := c.stats.msgRecv.Load()
	parsed := c.stats.msgParsed.Load()
	failed := c.stats.msgFailed.Load()

	deltaRecv := recv - c.stats.lastMsgRecv
	deltaParsed := parsed - c.stats.lastParsed
	deltaFailed := failed - c.stats.lastFailed

	c.stats.lastMsgRecv = recv
	c.stats.lastParsed = parsed
	c.stats.lastFailed = failed

	// Get symbol counts and reset
	c.stats.mu.Lock()
	spotCount := len(c.stats.spotSymbols)
	futCount := len(c.stats.futSymbols)
	fundCount := len(c.stats.fundSymbols)

	c.stats.spotSymbols = make(map[string]bool)
	c.stats.futSymbols = make(map[string]bool)
	c.stats.fundSymbols = make(map[string]bool)
	c.stats.mu.Unlock()

	// Count subscriptions
	var spotSub, futSub, fundSub int
	c.mu.RLock()
	clientCount := len(c.clients)
	for _, client := range c.clients {
		if len(client.streams) == 0 {
			continue
		}
		first := client.streams[0]
		if strings.Contains(first, "markPrice") {
			fundSub += len(client.streams)
		} else if strings.Contains(client.baseURL, "fstream") {
			futSub += len(client.streams)
		} else {
			spotSub += len(client.streams)
		}
	}
	c.mu.RUnlock()

	// Calculate coverage percentage
	pct := func(count, total int) float64 {
		if total == 0 {
			return 0
		}
		return float64(count) / float64(total) * 100
	}

	c.logger.Infof("[Binance 1min] WS:%d | Recv:%d OK:%d Fail:%d | Spot:%d/%d(%.0f%%) Fut:%d/%d(%.0f%%) Fund:%d/%d(%.0f%%)",
		clientCount, deltaRecv, deltaParsed, deltaFailed,
		spotCount, spotSub, pct(spotCount, spotSub),
		futCount, futSub, pct(futCount, futSub),
		fundCount, fundSub, pct(fundCount, fundSub),
	)
}

// --- WebSocket Client ---

type wsClient struct {
	baseURL string
	streams []string
	handler func([]byte)
	logger  *zap.SugaredLogger
	name    string
	wsCfg   config.WebSocketConfig

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func newWSClient(baseURL string, streams []string, handler func([]byte), logger *zap.SugaredLogger, name string, wsCfg config.WebSocketConfig) *wsClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &wsClient{
		baseURL: baseURL,
		streams: streams,
		handler: handler,
		logger:  logger,
		name:    name,
		wsCfg:   wsCfg,
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
}

func (c *wsClient) Start() {
	go c.run()
}

func (c *wsClient) Stop() {
	c.cancel()
	<-c.done
}

func (c *wsClient) run() {
	defer close(c.done)

	backoff := c.getBackoff()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		err := c.connectAndRead()

		select {
		case <-c.ctx.Done():
			return
		default:
			if err != nil {
				c.logger.Warnf("[%s] Disconnected: %v. Retry in %v", c.name, err, backoff.current)
			}
			c.waitWithBackoff(&backoff)
		}
	}
}

type backoffState struct {
	current time.Duration
	min     time.Duration
	max     time.Duration
}

func (c *wsClient) getBackoff() backoffState {
	minB := c.wsCfg.ReconnectDelay
	if minB == 0 {
		minB = time.Second
	}
	maxB := c.wsCfg.MaxReconnectDelay
	if maxB == 0 {
		maxB = 30 * time.Second
	}
	return backoffState{current: minB, min: minB, max: maxB}
}

func (c *wsClient) waitWithBackoff(b *backoffState) {
	select {
	case <-time.After(b.current):
		b.current *= 2
		if b.current > b.max {
			b.current = b.max
		}
	case <-c.ctx.Done():
	}
}

func (c *wsClient) connectAndRead() error {
	url := c.buildURL()

	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second

	conn, resp, err := dialer.Dial(url, c.headers())
	if err != nil {
		status := ""
		if resp != nil {
			status = resp.Status
		}
		return fmt.Errorf("dial: %v (status: %s)", err, status)
	}
	defer conn.Close()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if c.handler != nil {
			c.handler(msg)
		}
	}
}

func (c *wsClient) buildURL() string {
	streams := strings.Join(c.streams, "/")
	return fmt.Sprintf("%s/stream?streams=%s", c.baseURL, streams)
}

func (c *wsClient) headers() http.Header {
	h := http.Header{}
	h.Add("User-Agent", "Arbix/2.0")
	return h
}

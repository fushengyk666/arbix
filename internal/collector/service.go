package collector

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/fushengyk/arbix/internal/binance"
	"github.com/fushengyk/arbix/internal/config"
	"github.com/fushengyk/arbix/internal/domain"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// Service manages market data collection
type Service struct {
	cfg    *config.Config
	js     nats.JetStreamContext
	logger *zap.SugaredLogger

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.RWMutex
	collector *binance.Collector
}

// NewService creates a new collector service
func NewService(cfg *config.Config, js nats.JetStreamContext, logger *zap.SugaredLogger) (*Service, error) {
	ctx, cancel := context.WithCancel(context.Background())

	return &Service{
		cfg:    cfg,
		js:     js,
		logger: logger,
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

// Start initializes and starts the Binance collector
func (s *Service) Start() error {
	s.logger.Info("📊 Starting Collector Service...")

	// Create NATS handler
	handler := &natsHandler{
		js:     s.js,
		logger: s.logger,
	}

	// Create Binance collector with config
	collector := binance.NewCollectorWithLogger(s.cfg.Binance, handler, s.logger)

	if err := collector.Start(s.ctx); err != nil {
		return err
	}

	s.mu.Lock()
	s.collector = collector
	s.mu.Unlock()

	s.logger.Info("✅ Started Binance collector")
	return nil
}

// Stop gracefully shuts down the collector
func (s *Service) Stop() {
	s.logger.Info("🛑 Stopping Collector Service...")
	s.cancel()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.collector != nil {
		if err := s.collector.Stop(); err != nil {
			s.logger.Errorf("Error stopping collector: %v", err)
		}
	}
}

// natsHandler publishes market events to NATS
type natsHandler struct {
	js     nats.JetStreamContext
	logger *zap.SugaredLogger
}

func (h *natsHandler) OnKline(event domain.KlineEvent) error {
	subject := domain.SubjectKline(event.Exchange, strings.ToLower(event.Symbol))
	data, _ := json.Marshal(event)
	_, err := h.js.Publish(subject, data)
	return err
}

func (h *natsHandler) OnFunding(event domain.FundingEvent) error {
	subject := domain.SubjectFunding(event.Exchange, strings.ToLower(event.Symbol))
	data, _ := json.Marshal(event)
	_, err := h.js.Publish(subject, data)
	return err
}

func (h *natsHandler) OnMetadata(event domain.SymbolMetadataEvent) error {
	subject := domain.SubjectMetadata(event.Exchange, strings.ToLower(event.Symbol))
	data, _ := json.Marshal(event)
	_, err := h.js.Publish(subject, data)
	return err
}

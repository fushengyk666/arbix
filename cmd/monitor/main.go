package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/fushengyk/arbix/internal/config"
	"github.com/fushengyk/arbix/internal/domain"
	"github.com/fushengyk/arbix/internal/monitor"
	"github.com/fushengyk/arbix/internal/natsutil"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

func main() {
	// Parse flags
	configPath := flag.String("config", "configs/config.yaml", "Path to config file")
	flag.Parse()

	// Logger
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	sugar := logger.Sugar()

	sugar.Info("🛡️ Starting Monitor Service v3...")

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		sugar.Fatalf("❌ Failed to load config: %v", err)
	}

	// NATS
	nc, err := nats.Connect(cfg.NATS.URL,
		nats.Name("Arbix Monitor Service"),
		nats.ReconnectWait(cfg.NATS.ReconnectWait),
		nats.MaxReconnects(cfg.NATS.MaxReconnects),
	)
	if err != nil {
		sugar.Fatalf("❌ Failed to connect to NATS: %v", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		sugar.Fatalf("❌ Failed to create JetStream context: %v", err)
	}
	sugar.Info("✅ Connected to NATS JetStream")

	// Ensure streams
	natsutil.EnsureStream(js, domain.StreamMarket, domain.StreamMarketSubjects, sugar)

	// Create and start service
	svc, err := monitor.NewService(cfg, js, sugar)
	if err != nil {
		sugar.Fatalf("❌ Failed to create monitor service: %v", err)
	}

	if err := svc.Start(); err != nil {
		sugar.Fatalf("❌ Failed to start monitor: %v", err)
	}

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	sugar.Info("🛑 Shutting down Monitor Service...")
	svc.Stop()
}

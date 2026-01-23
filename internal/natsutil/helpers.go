package natsutil

import (
	"time"

	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// EnsureStream creates a NATS JetStream stream if it doesn't exist
func EnsureStream(js nats.JetStreamContext, name string, subjects []string, logger *zap.SugaredLogger) {
	stream, err := js.StreamInfo(name)
	if stream != nil && err == nil {
		return
	}

	_, err = js.AddStream(&nats.StreamConfig{
		Name:     name,
		Subjects: subjects,
		Storage:  nats.FileStorage,
		Replicas: 1,
		MaxAge:   6 * time.Hour,   // Auto-cleanup messages older than 6 hours
		Discard:  nats.DiscardOld, // When limit reached, discard oldest messages
	})
	if err != nil {
		logger.Errorf("Failed to create stream %s: %v", name, err)
	} else {
		logger.Infof("✅ Created stream: %s", name)
	}
}

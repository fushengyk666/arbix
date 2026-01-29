package natsutil

import (
	"time"

	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// EnsureStream creates or updates a NATS JetStream stream
func EnsureStream(js nats.JetStreamContext, name string, subjects []string, logger *zap.SugaredLogger) {
	config := &nats.StreamConfig{
		Name:     name,
		Subjects: subjects,
		Storage:  nats.FileStorage,
		Replicas: 1,
		MaxAge:   6 * time.Hour,   // Auto-cleanup messages older than 6 hours
		Discard:  nats.DiscardOld, // When limit reached, discard oldest messages
	}

	stream, err := js.StreamInfo(name)
	if stream != nil && err == nil {
		// Stream exists, update its configuration
		_, err = js.UpdateStream(config)
		if err != nil {
			logger.Errorf("Failed to update stream %s: %v", name, err)
		} else {
			logger.Infof("✅ Updated stream: %s", name)
		}
		return
	}

	// Stream doesn't exist, create it
	_, err = js.AddStream(config)
	if err != nil {
		logger.Errorf("Failed to create stream %s: %v", name, err)
	} else {
		logger.Infof("✅ Created stream: %s", name)
	}
}

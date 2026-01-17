package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration structure
type Config struct {
	NATS       NATSConfig               `yaml:"nats"`
	Binance    BinanceConfig            `yaml:"binance"`
	Channels   map[string]ChannelConfig `yaml:"channels"`
	Strategies []StrategyConfig         `yaml:"strategies"`
}

// NATSConfig holds NATS connection settings
type NATSConfig struct {
	URL           string        `yaml:"url"`
	ReconnectWait time.Duration `yaml:"reconnect_wait"`
	MaxReconnects int           `yaml:"max_reconnects"`
}

// BinanceConfig holds Binance exchange configuration
type BinanceConfig struct {
	RestBaseURL string          `yaml:"rest_base_url"`
	WSBaseURL   string          `yaml:"ws_base_url"`
	FuturesRest string          `yaml:"futures_rest_url"`
	FuturesWS   string          `yaml:"futures_ws_url"`
	WebSocket   WebSocketConfig `yaml:"websocket"`
	Markets     MarketsConfig   `yaml:"markets"`
}

// WebSocketConfig holds WebSocket connection settings
type WebSocketConfig struct {
	MaxStreamsPerConn int           `yaml:"max_streams_per_conn"`
	ReconnectDelay    time.Duration `yaml:"reconnect_delay"`
	MaxReconnectDelay time.Duration `yaml:"max_reconnect_delay"`
	ConnectStagger    time.Duration `yaml:"connect_stagger"`
}

// MarketsConfig holds market filter settings
type MarketsConfig struct {
	QuoteAssets    []string `yaml:"quote_assets"`
	SpotEnabled    bool     `yaml:"spot_enabled"`
	FuturesEnabled bool     `yaml:"futures_enabled"`
}

// ChannelConfig defines a notification channel
type ChannelConfig struct {
	Type      string `yaml:"type"` // telegram, webhook
	Enabled   bool   `yaml:"enabled"`
	EnvPrefix string `yaml:"env_prefix"` // prefix for env vars: {PREFIX}_TOKEN, etc.
}

// StrategyConfig defines a monitoring strategy with rules and channels
type StrategyConfig struct {
	Name            string       `yaml:"name"`
	Channels        []string     `yaml:"channels"` // channel names to send alerts to
	ExpansionFactor float64      `yaml:"expansion_factor"`
	Rules           []RuleConfig `yaml:"rules"`
}

// RuleConfig defines a single monitoring rule
type RuleConfig struct {
	Name      string        `yaml:"name"`
	Window    time.Duration `yaml:"window"`
	Threshold float64       `yaml:"threshold"`
	Cooldown  time.Duration `yaml:"cooldown"`
}

// ResolvedChannel contains fully resolved channel config with env values
type ResolvedChannel struct {
	Name    string
	Type    string // telegram, webhook
	Enabled bool

	// Telegram fields
	Token    string
	ChatID   string
	ThreadID string

	// Webhook fields
	URL     string
	Method  string // GET, POST
	Body    string // body template for POST
	Headers string // JSON headers
}

// Load reads configuration from YAML file and environment variables
func Load(path string) (*Config, error) {
	cfg := defaultConfig()

	// Read YAML file if exists
	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			if err := yaml.Unmarshal(data, cfg); err != nil {
				return nil, err
			}
		}
	}

	// Override with environment variables
	cfg.applyEnvOverrides()

	return cfg, nil
}

// ResolveChannel resolves a channel config by reading env vars
func (c *Config) ResolveChannel(name string) *ResolvedChannel {
	ch, ok := c.Channels[name]
	if !ok || !ch.Enabled {
		return nil
	}

	prefix := ch.EnvPrefix
	if prefix == "" {
		return nil
	}

	resolved := &ResolvedChannel{
		Name:    name,
		Type:    ch.Type,
		Enabled: ch.Enabled,
	}

	switch ch.Type {
	case "telegram":
		resolved.Token = os.Getenv(prefix + "_TOKEN")
		resolved.ChatID = os.Getenv(prefix + "_CHAT_ID")
		resolved.ThreadID = os.Getenv(prefix + "_THREAD_ID")
		if resolved.Token == "" || resolved.ChatID == "" {
			return nil // missing required fields
		}

	case "webhook":
		resolved.URL = os.Getenv(prefix + "_URL")
		resolved.Method = os.Getenv(prefix + "_METHOD")
		resolved.Body = os.Getenv(prefix + "_BODY")
		resolved.Headers = os.Getenv(prefix + "_HEADERS")
		if resolved.URL == "" {
			return nil // missing required field
		}
		if resolved.Method == "" {
			resolved.Method = "POST" // default
		}
	}

	return resolved
}

// defaultConfig returns configuration with sensible defaults
func defaultConfig() *Config {
	return &Config{
		NATS: NATSConfig{
			URL:           "nats://localhost:4222",
			ReconnectWait: 2 * time.Second,
			MaxReconnects: 10,
		},
		Binance: BinanceConfig{
			RestBaseURL: "https://api.binance.com",
			WSBaseURL:   "wss://stream.binance.com:9443",
			FuturesRest: "https://fapi.binance.com",
			FuturesWS:   "wss://fstream.binance.com",
			WebSocket: WebSocketConfig{
				MaxStreamsPerConn: 50,
				ReconnectDelay:    time.Second,
				MaxReconnectDelay: 30 * time.Second,
				ConnectStagger:    200 * time.Millisecond,
			},
			Markets: MarketsConfig{
				QuoteAssets:    []string{"USDT"},
				SpotEnabled:    true,
				FuturesEnabled: true,
			},
		},
		Channels:   make(map[string]ChannelConfig),
		Strategies: []StrategyConfig{},
	}
}

// applyEnvOverrides applies environment variable overrides
func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("NATS_URL"); v != "" {
		c.NATS.URL = v
	}
}

package domain

import (
	"context"
)

// ExchangeID represents a supported exchange
type ExchangeID string

const (
	ExchangeBinance ExchangeID = "binance"
)

// MarketType defines the type of market
type MarketType string

const (
	MarketSpot       MarketType = "spot"
	MarketFuturePerm MarketType = "future_perp"
)

// Symbol represents a tradeable pair on an exchange
type Symbol struct {
	Exchange   ExchangeID `json:"exchange"`
	MarketType MarketType `json:"market_type"`
	RawSymbol  string     `json:"raw_symbol"`
	BaseAsset  string     `json:"base_asset"`
	QuoteAsset string     `json:"quote_asset"`
}

// KlineEvent represents a standard OHLCV candle
type KlineEvent struct {
	Exchange    ExchangeID `json:"ex"`
	Symbol      string     `json:"s"`
	Market      MarketType `json:"m"`
	OpenTime    int64      `json:"t"`
	Open        float64    `json:"o"`
	High        float64    `json:"h"`
	Low         float64    `json:"l"`
	Close       float64    `json:"c"`
	Volume      float64    `json:"v"`
	QuoteVolume float64    `json:"q"`
	IsClosed    bool       `json:"x"`
	Timestamp   int64      `json:"T"`
}

// FundingEvent represents funding rate data
type FundingEvent struct {
	Exchange    ExchangeID `json:"ex"`
	Symbol      string     `json:"s"`
	Market      MarketType `json:"m"`
	Rate        float64    `json:"r"`
	NextFunding int64      `json:"n"`
	Timestamp   int64      `json:"T"`
}

// SymbolMetadataEvent contains symbol precision info from exchangeInfo
type SymbolMetadataEvent struct {
	Exchange       ExchangeID `json:"ex"`
	Symbol         string     `json:"s"`
	Market         MarketType `json:"m"`
	PricePrecision int        `json:"pp"` // Number of decimal places for price
	TickSize       float64    `json:"ts"` // Minimum price movement
	Timestamp      int64      `json:"T"`
}

// MarketDataCollector collects real-time market data
type MarketDataCollector interface {
	Name() ExchangeID
	Start(ctx context.Context) error
	Stop() error
	Subscribe(symbols []*Symbol) error
	Refresh(symbols []*Symbol) error
}

// MarketDataHandler handles incoming market data events
type MarketDataHandler interface {
	OnKline(event KlineEvent) error
	OnFunding(event FundingEvent) error
	OnMetadata(event SymbolMetadataEvent) error
}

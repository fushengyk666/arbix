package domain

import (
	"fmt"
	"strings"
)

// NATS Subject constants
const (
	SubjectPrefixMarket   = "market"
	SubjectPrefixMetadata = "metadata"
)

// Subject builders for market data
func SubjectKline(exchange ExchangeID, symbol string) string {
	return fmt.Sprintf("%s.%s.kline.%s", SubjectPrefixMarket, exchange, strings.ToLower(symbol))
}

func SubjectFunding(exchange ExchangeID, symbol string) string {
	return fmt.Sprintf("%s.%s.funding.%s", SubjectPrefixMarket, exchange, strings.ToLower(symbol))
}

// Subject builder for metadata
func SubjectMetadata(exchange ExchangeID, symbol string) string {
	return fmt.Sprintf("%s.%s.%s", SubjectPrefixMetadata, exchange, strings.ToLower(symbol))
}

// Subject wildcard patterns for subscriptions
func SubjectPatternAllMarket(exchange ExchangeID) string {
	return fmt.Sprintf("%s.%s.>", SubjectPrefixMarket, exchange)
}

func SubjectPatternAllMetadata() string {
	return fmt.Sprintf("%s.>", SubjectPrefixMetadata)
}

// Stream names
const (
	StreamMarket   = "MARKET"
	StreamMetadata = "METADATA"
)

// Stream subject patterns
var (
	StreamMarketSubjects   = []string{"market.>"}
	StreamMetadataSubjects = []string{"metadata.>"}
)

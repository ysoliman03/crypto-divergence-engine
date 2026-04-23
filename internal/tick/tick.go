package tick

import "time"

// Tick is the common wire format produced by every ingester and consumed by every detector.
// All ingesters must normalize to this schema — field names, timestamp precision, and symbol
// convention (e.g. "BTC-USD") must be identical regardless of source exchange.
type Tick struct {
	Symbol    string    `json:"symbol"`    // normalized pair, e.g. "BTC-USD"
	Exchange  string    `json:"exchange"`  // source exchange, e.g. "binance"
	Price     float64   `json:"price"`     // last trade price
	Volume    float64   `json:"volume"`    // trade volume in base currency
	Side      string    `json:"side"`      // "buy" (hit ask) or "sell" (hit bid)
	TradeID   string    `json:"trade_id"`  // exchange-assigned trade identifier
	Timestamp  time.Time `json:"timestamp"`   // exchange-reported trade time (UTC)
	ReceivedAt time.Time `json:"received_at"` // local ingester receipt time (UTC), used to compute bus lag
}

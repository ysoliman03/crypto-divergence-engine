package ingester

import (
	"context"

	"github.com/youssef/divergence-engine/internal/tick"
)

// ExchangeAdapter is implemented once per exchange. It handles the exchange-specific
// WebSocket protocol, message format, and symbol normalization, and emits normalized
// ticks onto the provided channel. The shared ingester plumbing (reconnection, Redis
// publishing, backpressure, context cancellation) lives outside this interface so it
// doesn't have to be reimplemented per exchange.
//
// Adding a new exchange = one new file implementing this interface + one new entry in
// docker-compose.yml. Nothing else changes.
type ExchangeAdapter interface {
	// Name returns the exchange identifier written into each Tick.Exchange field.
	Name() string

	// Subscribe connects to the exchange WebSocket, parses incoming trade messages,
	// and sends normalized Ticks onto out until ctx is cancelled or a fatal error
	// occurs. Reconnection on transient errors is handled by the caller.
	Subscribe(ctx context.Context, symbols []string, out chan<- tick.Tick) error
}

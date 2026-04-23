package bus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
	"github.com/youssef/divergence-engine/internal/tick"
)

const (
	StreamTicks  = "ticks"
	StreamAlerts = "alerts"
)

// Publish serializes t as JSON and appends it to stream.
// MaxLen ~100k entries with APPROX trim keeps memory bounded without blocking writers.
func Publish(ctx context.Context, rdb *redis.Client, stream string, t tick.Tick) error {
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		MaxLen: 100_000,
		Approx: true,
		Values: map[string]any{"data": string(data)},
	}).Err()
}

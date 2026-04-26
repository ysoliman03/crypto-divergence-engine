package bus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
	"github.com/youssef/divergence-engine/internal/alert"
	"github.com/youssef/divergence-engine/internal/tick"
)

const (
	StreamTicks       = "ticks"
	StreamAlerts      = "alerts"
	StreamAlertsClean = "alerts-clean" // deduplicated alerts produced by the aggregator
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

// PublishAlertClean serializes a to JSON and appends it to the clean alerts stream.
func PublishAlertClean(ctx context.Context, rdb *redis.Client, a alert.Alert) error {
	data, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamAlertsClean,
		MaxLen: 10_000,
		Approx: true,
		Values: map[string]any{"data": string(data)},
	}).Err()
}

// PublishAlert serializes a to JSON and appends it to the alerts stream.
func PublishAlert(ctx context.Context, rdb *redis.Client, a alert.Alert) error {
	data, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamAlerts,
		MaxLen: 10_000,
		Approx: true,
		Values: map[string]any{"data": string(data)},
	}).Err()
}

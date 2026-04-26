package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/youssef/divergence-engine/internal/alert"
	"github.com/youssef/divergence-engine/internal/bus"
	"github.com/youssef/divergence-engine/internal/tick"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	log.Printf("listening on streams %q and %q (Ctrl+C to stop)...", bus.StreamTicks, bus.StreamAlertsClean)

	ticksID := "$"
	alertsID := "$"

	for {
		// XREAD supports multiple streams in one blocking call.
		streams, err := rdb.XRead(ctx, &redis.XReadArgs{
			Streams: []string{bus.StreamTicks, bus.StreamAlertsClean, ticksID, alertsID},
			Block:   0,
			Count:   100,
		}).Result()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("xread: %v", err)
			continue
		}

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				switch stream.Stream {
				case bus.StreamTicks:
					ticksID = msg.ID
					printTick(msg)
				case bus.StreamAlertsClean:
					alertsID = msg.ID
					printAlert(msg)
				}
			}
		}
	}
}

func printTick(msg redis.XMessage) {
	data, ok := msg.Values["data"].(string)
	if !ok {
		return
	}
	var t tick.Tick
	if err := json.Unmarshal([]byte(data), &t); err != nil {
		return
	}
	lag := t.ReceivedAt.Sub(t.Timestamp).Round(time.Millisecond)
	fmt.Printf("TICK   [%s] %s  %-10s  price=%-12.2f  vol=%.6f  side=%s  lag=%s\n",
		t.Exchange, t.Timestamp.Format("15:04:05.000"), t.Symbol,
		t.Price, t.Volume, t.Side, lag)
}

func printAlert(msg redis.XMessage) {
	data, ok := msg.Values["data"].(string)
	if !ok {
		return
	}
	var a alert.Alert
	if err := json.Unmarshal([]byte(data), &a); err != nil {
		return
	}
	fmt.Printf("ALERT  [%s] %-10s  detector=%s  %s  (val=%.4f threshold=%.4f)\n",
		a.Severity, a.Symbol, a.Detector, a.Message, a.Value, a.Threshold)
}

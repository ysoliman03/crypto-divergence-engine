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

	log.Printf("listening on stream %q — waiting for ticks (Ctrl+C to stop)...", bus.StreamTicks)

	lastID := "$" // only show ticks that arrive after we start
	for {
		streams, err := rdb.XRead(ctx, &redis.XReadArgs{
			Streams: []string{bus.StreamTicks, lastID},
			Block:   0, // block until at least one message arrives
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
				lastID = msg.ID

				data, ok := msg.Values["data"].(string)
				if !ok {
					continue
				}

				var t tick.Tick
				if err := json.Unmarshal([]byte(data), &t); err != nil {
					log.Printf("unmarshal: %v", err)
					continue
				}

				lag := t.ReceivedAt.Sub(t.Timestamp).Round(time.Millisecond)
				fmt.Printf("[%s] %s  %-10s  price=%-12.2f  vol=%.6f  side=%s  lag=%s\n",
					t.Exchange,
					t.Timestamp.Format("15:04:05.000"),
					t.Symbol,
					t.Price,
					t.Volume,
					t.Side,
					lag,
				)
			}
		}
	}
}

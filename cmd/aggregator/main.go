package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/youssef/divergence-engine/internal/alert"
	"github.com/youssef/divergence-engine/internal/bus"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := getenv("REDIS_ADDR", "localhost:6379")
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	dd := newDeduper(time.Minute)
	go dd.cleanupLoop(ctx, 5*time.Minute)

	log.Printf("aggregator started, dedup window=1m, reading from %q", bus.StreamAlerts)

	lastID := "$"
	for {
		streams, err := rdb.XRead(ctx, &redis.XReadArgs{
			Streams: []string{bus.StreamAlerts, lastID},
			Block:   500 * time.Millisecond,
			Count:   100,
		}).Result()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err == redis.Nil {
				continue
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

				var a alert.Alert
				if err := json.Unmarshal([]byte(data), &a); err != nil {
					log.Printf("unmarshal: %v", err)
					continue
				}

				if dd.isDup(a) {
					continue
				}

				log.Printf("[%s] %-10s  %s  %s", a.Severity, a.Symbol, a.Detector, a.Message)

				if err := bus.PublishAlertClean(ctx, rdb, a); err != nil {
					log.Printf("publish: %v", err)
				}
			}
		}
	}
}

// deduper suppresses duplicate alerts within a rolling time window.
// Key: detector + symbol + timestamp truncated to window.
type deduper struct {
	mu     sync.Mutex
	cache  map[string]time.Time
	window time.Duration
}

func newDeduper(window time.Duration) *deduper {
	return &deduper{cache: make(map[string]time.Time), window: window}
}

func (d *deduper) isDup(a alert.Alert) bool {
	key := fmt.Sprintf("%s:%s:%d", a.Detector, a.Symbol, a.Timestamp.Truncate(d.window).Unix())
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.cache[key]; ok {
		return true
	}
	d.cache[key] = time.Now()
	return false
}

// cleanupLoop removes expired entries so the cache doesn't grow unbounded.
func (d *deduper) cleanupLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			cutoff := time.Now().Add(-2 * d.window)
			d.mu.Lock()
			for k, seen := range d.cache {
				if seen.Before(cutoff) {
					delete(d.cache, k)
				}
			}
			d.mu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

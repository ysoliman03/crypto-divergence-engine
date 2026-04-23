package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/youssef/divergence-engine/internal/bus"
	"github.com/youssef/divergence-engine/internal/ingester"
	"github.com/youssef/divergence-engine/internal/tick"
)

const (
	channelSize = 1000
	maxBackoff  = 30 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := getenv("REDIS_ADDR", "localhost:6379")
	symbols := strings.Split(getenv("SYMBOLS", "BTC-USDT"), ",")

	rdb := redis.NewClient(&redis.Options{Addr: addr, PoolSize: 4})
	defer rdb.Close()

	adapter := ingester.NewBinanceAdapter()
	ch := make(chan tick.Tick, channelSize)

	var wg sync.WaitGroup

	// Subscribe goroutine: calls adapter.Subscribe and reconnects on transient errors.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ch) // signals the publish goroutine to drain and exit
		backoff := time.Second
		for {
			log.Printf("connecting to %s (symbols: %v)", adapter.Name(), symbols)
			err := adapter.Subscribe(ctx, symbols, ch)
			if ctx.Err() != nil {
				return
			}
			log.Printf("subscribe error: %v — reconnecting in %s", err, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff = min(backoff*2, maxBackoff)
		}
	}()

	// Publish goroutine: drains ch and writes each tick to Redis Stream.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for t := range ch {
			if err := bus.Publish(ctx, rdb, bus.StreamTicks, t); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("publish error: %v", err)
			}
		}
	}()

	<-ctx.Done()
	log.Println("shutdown signal received, draining...")
	wg.Wait()
	log.Println("stopped")
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

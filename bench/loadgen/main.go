// loadgen publishes synthetic ticks at a configurable rate and reports
// detector consumer-group throughput once per second.
//
// Usage:
//
//	go run ./bench/loadgen -rate 500 -duration 30s -redis localhost:6379
//
// Output (CSV-style):
//
//	sec | pub/s | consumed/s | pending | lag_ms
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/youssef/divergence-engine/internal/bus"
	"github.com/youssef/divergence-engine/internal/tick"
)

var symbols = []string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}
var exchanges = []string{"binance"}
var sides = []string{"buy", "sell"}

func main() {
	rate := flag.Int("rate", 200, "ticks per second to publish")
	dur := flag.Duration("duration", 30*time.Second, "how long to run")
	addr := flag.String("redis", envOr("REDIS_ADDR", "localhost:6379"), "Redis address")
	group := flag.String("group", "detectors", "consumer group to monitor")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rdb := redis.NewClient(&redis.Options{Addr: *addr})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping: %v", err)
	}

	log.Printf("loadgen: rate=%d/s  duration=%s  group=%q", *rate, *dur, *group)
	fmt.Printf("%-6s  %-10s  %-12s  %-10s  %-10s\n", "sec", "pub/s", "consumed/s", "pending", "lag_ms")
	fmt.Println("------  ----------  ------------  ----------  ----------")

	ticker := time.NewTicker(time.Second / time.Duration(*rate))
	defer ticker.Stop()

	deadline := time.After(*dur)
	report := time.NewTicker(time.Second)
	defer report.Stop()

	var (
		totalPub      int64
		totalConsumed int64
		lastConsumed  int64
		pubThisSec    int
		sec           int
	)

	// seed repeatable prices per symbol
	prices := map[string]float64{
		"BTC-USDT": 65000.0,
		"ETH-USDT": 3200.0,
		"SOL-USDT": 170.0,
	}

	for {
		select {
		case <-ctx.Done():
			printSummary(totalPub, totalConsumed, sec)
			return

		case <-deadline:
			// drain one more report tick
			<-report.C
			sec++
			consumed, pending, lag := queryGroup(ctx, rdb, bus.StreamTicks, *group)
			thisSecConsumed := consumed - lastConsumed
			lastConsumed = consumed
			totalConsumed = consumed
			fmt.Printf("%-6d  %-10d  %-12d  %-10d  %-10.1f\n",
				sec, pubThisSec, thisSecConsumed, pending, lag)
			printSummary(totalPub, totalConsumed, sec)
			return

		case <-report.C:
			sec++
			consumed, pending, lag := queryGroup(ctx, rdb, bus.StreamTicks, *group)
			thisSecConsumed := consumed - lastConsumed
			lastConsumed = consumed
			totalConsumed = consumed
			fmt.Printf("%-6d  %-10d  %-12d  %-10d  %-10.1f\n",
				sec, pubThisSec, thisSecConsumed, pending, lag)
			pubThisSec = 0

		case <-ticker.C:
			sym := symbols[rand.Intn(len(symbols))]
			// small random walk
			prices[sym] *= 1 + (rand.Float64()-0.5)*0.0002
			t := tick.Tick{
				Symbol:     sym,
				Exchange:   exchanges[0],
				Price:      prices[sym],
				Volume:     rand.Float64() * 2,
				Side:       sides[rand.Intn(len(sides))],
				TradeID:    fmt.Sprintf("bench-%d", totalPub),
				Timestamp:  time.Now(),
				ReceivedAt: time.Now(),
			}
			if err := bus.Publish(ctx, rdb, bus.StreamTicks, t); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("publish: %v", err)
				continue
			}
			totalPub++
			pubThisSec++
		}
	}
}

// queryGroup reads XINFO GROUPS and returns (totalDelivered, pendingCount, estimatedLagMs).
// pendingCount is the number of messages delivered but not yet ACKed.
// estimatedLagMs is derived from the last-delivered-id timestamp.
func queryGroup(ctx context.Context, rdb *redis.Client, stream, group string) (delivered, pending int64, lagMs float64) {
	groups, err := rdb.XInfoGroups(ctx, stream).Result()
	if err != nil {
		return 0, 0, 0
	}
	for _, g := range groups {
		if g.Name != group {
			continue
		}
		delivered = g.EntriesRead
		pending = g.Pending

		// Derive lag from the last-delivered message ID (millisecond timestamp prefix).
		if g.LastDeliveredID != "" && g.LastDeliveredID != "0-0" {
			var ms int64
			fmt.Sscanf(g.LastDeliveredID, "%d-", &ms)
			if ms > 0 {
				lagMs = float64(time.Now().UnixMilli() - ms)
			}
		}
		return
	}
	return
}

func printSummary(pub, consumed int64, secs int) {
	fmt.Println("------  ----------  ------------  ----------  ----------")
	fmt.Printf("TOTAL   pub=%-10d  consumed=%-10d  secs=%d\n", pub, consumed, secs)
	if secs > 0 {
		fmt.Printf("AVG     pub/s=%-8.1f  consumed/s=%-8.1f\n",
			float64(pub)/float64(secs), float64(consumed)/float64(secs))
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/youssef/divergence-engine/internal/bus"
	"github.com/youssef/divergence-engine/internal/detectors"
	"github.com/youssef/divergence-engine/internal/tick"
)

const (
	consumerGroup  = "detectors"
	blockDuration  = 2 * time.Second
	batchSize      = 100
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := getenv("REDIS_ADDR", "localhost:6379")
	workerID := getenv("WORKER_ID", mustHostname())

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	// Create the consumer group and stream if they don't exist yet.
	// "0" means the group will process all messages in the stream from the start.
	err := rdb.XGroupCreateMkStream(ctx, bus.StreamTicks, consumerGroup, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		log.Fatalf("create consumer group: %v", err)
	}

	registry := detectors.NewRegistry(
		detectors.NewVolumeSpikeDetector(),
		detectors.NewOrderFlowImbalanceDetector(),
		detectors.NewCorrelationBreakdownDetector(),
	)

	log.Printf("detector worker %q starting, group %q", workerID, consumerGroup)

	for {
		streams, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    consumerGroup,
			Consumer: workerID,
			Streams:  []string{bus.StreamTicks, ">"},
			Count:    batchSize,
			Block:    blockDuration,
		}).Result()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err == redis.Nil {
				continue // block timeout, no messages — loop again
			}
			log.Printf("xreadgroup: %v", err)
			continue
		}

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				process(ctx, rdb, registry, msg)
			}
		}
	}
}

func process(ctx context.Context, rdb *redis.Client, reg *detectors.Registry, msg redis.XMessage) {
	// Always ACK, even on parse failure, so bad messages don't block the group.
	defer rdb.XAck(ctx, bus.StreamTicks, consumerGroup, msg.ID)

	data, ok := msg.Values["data"].(string)
	if !ok {
		return
	}

	var t tick.Tick
	if err := json.Unmarshal([]byte(data), &t); err != nil {
		log.Printf("unmarshal tick: %v", err)
		return
	}

	alerts := reg.OnTick(ctx, t)
	for _, a := range alerts {
		if err := bus.PublishAlert(ctx, rdb, a); err != nil {
			log.Printf("publish alert: %v", err)
		}
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "worker-unknown"
	}
	return h
}

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "embed"

	"github.com/redis/go-redis/v9"
	"github.com/youssef/divergence-engine/internal/bus"
)

//go:embed index.html
var indexHTML []byte

// hub broadcasts SSE messages to all connected browser clients.
type hub struct {
	mu      sync.Mutex
	clients map[chan string]struct{}
}

func newHub() *hub {
	return &hub{clients: make(map[chan string]struct{})}
}

func (h *hub) subscribe() chan string {
	ch := make(chan string, 64)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan string) {
	h.mu.Lock()
	delete(h.clients, ch)
	close(ch)
	h.mu.Unlock()
}

func (h *hub) broadcast(event, data string) {
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- msg:
		default: // slow client — drop rather than block
		}
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := getenv("REDIS_ADDR", "localhost:6379")
	port := getenv("PORT", "8080")

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	h := newHub()
	go readStreams(ctx, rdb, h)

	sm := newSessionManager(h)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write(indexHTML)
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w, r, h)
	})
	mux.HandleFunc("/api/backtest", handleBacktest)
	mux.HandleFunc("/api/backtest/custom", handleCustomBacktest)
	mux.HandleFunc("/api/live-strategy/start", sm.handleStart)
	mux.HandleFunc("/api/live-strategy/stop", sm.handleStop)
	mux.HandleFunc("/api/live-strategy/sessions", sm.handleList)
	mux.HandleFunc("/api/live-btc", handleLiveBTC)

	srv := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	log.Printf("dashboard → http://localhost:%s", port)
	srv.ListenAndServe()
}

func readStreams(ctx context.Context, rdb *redis.Client, h *hub) {
	ticksID := "$"
	alertsID := "$"

	for {
		streams, err := rdb.XRead(ctx, &redis.XReadArgs{
			Streams: []string{bus.StreamTicks, bus.StreamAlertsClean, ticksID, alertsID},
			Block:   100 * time.Millisecond,
			Count:   50,
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
				data, ok := msg.Values["data"].(string)
				if !ok {
					continue
				}
				switch stream.Stream {
				case bus.StreamTicks:
					ticksID = msg.ID
					h.broadcast("tick", data)
				case bus.StreamAlertsClean:
					alertsID = msg.ID
					h.broadcast("alert", data)
				}
			}
		}
	}
}

func serveSSE(w http.ResponseWriter, r *http.Request, h *hub) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ch := h.subscribe()
	defer h.unsubscribe(ch)

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, msg)
			flusher.Flush()
		case <-r.Context().Done():
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

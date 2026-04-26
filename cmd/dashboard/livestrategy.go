package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type liveSession struct {
	mu sync.Mutex

	Symbol   string `json:"symbol"`
	Strategy string `json:"strategy"`
	Interval string `json:"interval"`

	Position   string    `json:"position"` // "flat" | "long"
	EntryPrice float64   `json:"entryPrice"`
	EntryTime  time.Time `json:"entryTime"`

	cash     float64 // realized value (starts at 10 000)
	shares   float64 // units held when long
	notional float64 // always 10 000

	CurrentPrice float64   `json:"currentPrice"`
	PnL          float64   `json:"pnl"` // % since session start
	Trades       int       `json:"trades"`
	Signal       string    `json:"signal"`
	StartTime    time.Time `json:"startTime"`

	cancel context.CancelFunc
}

func (s *liveSession) totalValue() float64 {
	if s.Position == "long" {
		return s.shares * s.CurrentPrice
	}
	return s.cash
}

type liveSnapshot struct {
	Symbol       string    `json:"symbol"`
	Strategy     string    `json:"strategy"`
	Interval     string    `json:"interval"`
	Position     string    `json:"position"`
	EntryPrice   float64   `json:"entryPrice"`
	CurrentPrice float64   `json:"currentPrice"`
	PnL          float64   `json:"pnl"`
	Trades       int       `json:"trades"`
	Signal       string    `json:"signal"`
	StartTime    time.Time `json:"startTime"`
	TotalValue   float64   `json:"totalValue"`
}

func (s *liveSession) snapshot() liveSnapshot {
	return liveSnapshot{
		Symbol:       s.Symbol,
		Strategy:     s.Strategy,
		Interval:     s.Interval,
		Position:     s.Position,
		EntryPrice:   s.EntryPrice,
		CurrentPrice: s.CurrentPrice,
		PnL:          s.PnL,
		Trades:       s.Trades,
		Signal:       s.Signal,
		StartTime:    s.StartTime,
		TotalValue:   s.totalValue(),
	}
}

type sessionManager struct {
	mu       sync.Mutex
	sessions map[string]*liveSession // key: symbol
	hub      *hub
}

func newSessionManager(h *hub) *sessionManager {
	return &sessionManager{
		sessions: make(map[string]*liveSession),
		hub:      h,
	}
}

func (sm *sessionManager) start(symbol, strategy, interval string) {
	sm.mu.Lock()
	if existing, ok := sm.sessions[symbol]; ok {
		existing.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	sess := &liveSession{
		Symbol:    symbol,
		Strategy:  strategy,
		Interval:  interval,
		Position:  "flat",
		cash:      10000,
		notional:  10000,
		StartTime: time.Now(),
		cancel:    cancel,
	}
	sm.sessions[symbol] = sess
	sm.mu.Unlock()

	go sm.runSession(ctx, sess)
}

func (sm *sessionManager) stop(symbol string) {
	sm.mu.Lock()
	sess, ok := sm.sessions[symbol]
	if ok {
		sess.cancel()
		delete(sm.sessions, symbol)
	}
	sm.mu.Unlock()
	if ok {
		sm.hub.broadcast("strategy_remove", fmt.Sprintf(`{"symbol":%q}`, symbol))
	}
}

func (sm *sessionManager) listSnapshots() []liveSnapshot {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	out := make([]liveSnapshot, 0, len(sm.sessions))
	for _, s := range sm.sessions {
		s.mu.Lock()
		out = append(out, s.snapshot())
		s.mu.Unlock()
	}
	return out
}

func (sm *sessionManager) runSession(ctx context.Context, sess *liveSession) {
	sm.tick(ctx, sess)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			sm.tick(ctx, sess)
		case <-ctx.Done():
			return
		}
	}
}

func (sm *sessionManager) tick(_ context.Context, sess *liveSession) {
	closes, err := fetchRecentCloses(sess.Symbol, sess.Interval, 200)
	if err != nil {
		log.Printf("live %s/%s: %v", sess.Symbol, sess.Strategy, err)
		return
	}
	if len(closes) < 2 {
		return
	}

	signal := computeLiveSignal(sess.Strategy, closes)
	currentPrice := closes[len(closes)-1]

	sess.mu.Lock()
	sess.CurrentPrice = currentPrice
	sess.Signal = signal

	switch {
	case signal == "buy" && sess.Position == "flat":
		sess.Position = "long"
		sess.EntryPrice = currentPrice
		sess.EntryTime = time.Now()
		sess.shares = sess.cash / currentPrice
		sess.cash = 0
		sess.Trades++
	case signal == "sell" && sess.Position == "long":
		sess.cash = sess.shares * currentPrice
		sess.shares = 0
		sess.Position = "flat"
		sess.Trades++
	}
	sess.PnL = (sess.totalValue() - sess.notional) / sess.notional * 100

	snap := sess.snapshot()
	sess.mu.Unlock()

	data, _ := json.Marshal(snap)
	sm.hub.broadcast("strategy_tick", string(data))
}

// computeLiveSignal returns "buy" (should be long), "sell" (should be flat), or "hold".
// State-based — uses current indicator state, not crossover events.
func computeLiveSignal(strategy string, closes []float64) string {
	switch strategy {
	case "sma_crossover":
		return liveSignalCrossover(closes, 9, 21, false)
	case "ema_crossover":
		return liveSignalCrossover(closes, 9, 21, true)
	case "rsi_mean_reversion":
		return liveSignalRSI(closes, 14, 30, 70)
	case "bollinger_bands":
		return liveSignalBollinger(closes, 20, 2.0)
	case "breakout":
		return liveSignalBreakout(closes, 20)
	}
	return "hold"
}

func liveSignalCrossover(closes []float64, fast, slow int, useEMA bool) string {
	n := len(closes)
	if n < slow+1 {
		return "hold"
	}
	var f, s float64
	if useEMA {
		fe := computeEMA(closes, fast)
		se := computeEMA(closes, slow)
		f, s = fe[n-1], se[n-1]
	} else {
		f = barSMA(closes, n, fast)
		s = barSMA(closes, n, slow)
	}
	if f > s {
		return "buy"
	}
	return "sell"
}

func liveSignalRSI(closes []float64, period int, oversold, overbought float64) string {
	rsi := computeRSI(closes, period)
	for i := len(rsi) - 1; i >= 0; i-- {
		if rsi[i] == 0 {
			continue
		}
		if rsi[i] < oversold {
			return "buy"
		}
		if rsi[i] > overbought {
			return "sell"
		}
		return "hold"
	}
	return "hold"
}

func liveSignalBollinger(closes []float64, period int, stdDevs float64) string {
	upper, middle, lower := computeBollinger(closes, period, stdDevs)
	n := len(closes)
	last := closes[n-1]
	if lower[n-1] > 0 && last < lower[n-1] {
		return "buy"
	}
	if upper[n-1] > 0 && last > middle[n-1] {
		return "sell"
	}
	return "hold"
}

func liveSignalBreakout(closes []float64, lookback int) string {
	n := len(closes)
	if n < lookback+1 {
		return "hold"
	}
	maxClose := closes[n-lookback-1]
	for i := n - lookback; i < n-1; i++ {
		if closes[i] > maxClose {
			maxClose = closes[i]
		}
	}
	if closes[n-1] > maxClose {
		return "buy"
	}
	return "hold"
}

func fetchRecentCloses(symbol, interval string, limit int) ([]float64, error) {
	sym := strings.ReplaceAll(symbol, "-", "")
	url := fmt.Sprintf(
		"https://api.binance.com/api/v3/klines?symbol=%s&interval=%s&limit=%d",
		sym, interval, limit,
	)
	resp, err := binanceClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("binance %d", resp.StatusCode)
	}

	var raw [][]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	closes := make([]float64, 0, len(raw))
	for _, bar := range raw {
		if len(bar) < 5 {
			continue
		}
		var s string
		if json.Unmarshal(bar[4], &s) != nil {
			continue
		}
		c, _ := strconv.ParseFloat(s, 64)
		closes = append(closes, c)
	}
	return closes, nil
}

func (sm *sessionManager) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Symbols  []string `json:"symbols"`
		Strategy string   `json:"strategy"`
		Interval string   `json:"interval"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Symbols) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Interval == "" {
		req.Interval = "1m"
	}
	for _, sym := range req.Symbols {
		sm.start(sym, req.Strategy, req.Interval)
	}
	w.WriteHeader(http.StatusOK)
}

func (sm *sessionManager) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Symbol string `json:"symbol"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sm.stop(req.Symbol)
	w.WriteHeader(http.StatusOK)
}

func (sm *sessionManager) handleList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sm.listSnapshots())
}

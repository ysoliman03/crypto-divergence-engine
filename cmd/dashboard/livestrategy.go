package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var sessionIDCounter int64

func newSessionID() string {
	n := atomic.AddInt64(&sessionIDCounter, 1)
	return fmt.Sprintf("s%d", n)
}

type EquityPoint struct {
	T int64   `json:"t"` // unix ms
	V float64 `json:"v"` // P&L %
}

type TradeRecord struct {
	T     int64   `json:"t"`     // unix ms
	Kind  string  `json:"kind"`  // "buy" | "sell"
	Price float64 `json:"price"`
	PnL   float64 `json:"pnl"` // cumulative P&L at this trade
}

type liveSession struct {
	mu sync.Mutex

	ID       string
	Symbol   string
	Strategy string
	Interval string
	Code     string // Python code for custom strategy

	Position   string
	EntryPrice float64
	EntryTime  time.Time

	cash     float64
	shares   float64
	notional float64

	CurrentPrice float64
	PnL          float64
	Trades       int
	Signal       string
	StartTime    time.Time

	TradeHistory []TradeRecord
	EquityCurve  []EquityPoint

	cancel context.CancelFunc
}

func (s *liveSession) totalValue() float64 {
	if s.Position == "long" {
		return s.shares * s.CurrentPrice
	}
	return s.cash
}

type liveSnapshot struct {
	ID           string        `json:"id"`
	Symbol       string        `json:"symbol"`
	Strategy     string        `json:"strategy"`
	Interval     string        `json:"interval"`
	Position     string        `json:"position"`
	EntryPrice   float64       `json:"entryPrice"`
	CurrentPrice float64       `json:"currentPrice"`
	PnL          float64       `json:"pnl"`
	Trades       int           `json:"trades"`
	Signal       string        `json:"signal"`
	StartTime    time.Time     `json:"startTime"`
	TotalValue   float64       `json:"totalValue"`
	TradeHistory []TradeRecord `json:"tradeHistory"`
	EquityCurve  []EquityPoint `json:"equityCurve"`
}

func (s *liveSession) snapshot() liveSnapshot {
	ec := make([]EquityPoint, len(s.EquityCurve))
	copy(ec, s.EquityCurve)
	th := make([]TradeRecord, len(s.TradeHistory))
	copy(th, s.TradeHistory)
	return liveSnapshot{
		ID:           s.ID,
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
		TradeHistory: th,
		EquityCurve:  ec,
	}
}

type sessionManager struct {
	mu       sync.Mutex
	sessions map[string]*liveSession // key: ID
	hub      *hub
}

func newSessionManager(h *hub) *sessionManager {
	return &sessionManager{
		sessions: make(map[string]*liveSession),
		hub:      h,
	}
}

func (sm *sessionManager) start(symbol, strategy, interval, code string) string {
	id := newSessionID()
	ctx, cancel := context.WithCancel(context.Background())
	sess := &liveSession{
		ID:        id,
		Symbol:    symbol,
		Strategy:  strategy,
		Interval:  interval,
		Code:      code,
		Position:  "flat",
		cash:      10000,
		notional:  10000,
		StartTime: time.Now(),
		cancel:    cancel,
	}
	sm.mu.Lock()
	sm.sessions[id] = sess
	sm.mu.Unlock()
	go sm.runSession(ctx, sess)
	return id
}

func (sm *sessionManager) stop(id string) {
	sm.mu.Lock()
	sess, ok := sm.sessions[id]
	if ok {
		sess.cancel()
		delete(sm.sessions, id)
	}
	sm.mu.Unlock()
	if ok {
		data, _ := json.Marshal(map[string]string{"id": id})
		sm.hub.broadcast("strategy_remove", string(data))
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
	var (
		closes []float64
		err    error
	)

	if sess.Strategy == "custom" {
		// Need full OHLCV for custom Python strategies.
		bars, ferr := fetchRecentBars(sess.Symbol, sess.Interval, 200)
		if ferr != nil {
			log.Printf("live %s/custom: %v", sess.Symbol, ferr)
			return
		}
		closes = make([]float64, len(bars))
		for i, b := range bars {
			closes[i] = b.Close
		}
		sess.mu.Lock()
		signal := computeCustomLiveSignal(sess.Code, bars)
		currentPrice := closes[len(closes)-1]
		sm.applySignal(sess, signal, currentPrice)
		sess.mu.Unlock()
	} else {
		closes, err = fetchRecentCloses(sess.Symbol, sess.Interval, 200)
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
		sm.applySignal(sess, signal, currentPrice)
		sess.mu.Unlock()
	}

	sess.mu.Lock()
	snap := sess.snapshot()
	sess.mu.Unlock()

	data, _ := json.Marshal(snap)
	sm.hub.broadcast("strategy_tick", string(data))
}

// applySignal updates session state given a new signal and price. Caller must hold sess.mu.
func (sm *sessionManager) applySignal(sess *liveSession, signal string, currentPrice float64) {
	sess.CurrentPrice = currentPrice
	sess.Signal = signal

	now := time.Now().UnixMilli()
	switch {
	case signal == "buy" && sess.Position == "flat":
		sess.Position = "long"
		sess.EntryPrice = currentPrice
		sess.EntryTime = time.Now()
		sess.shares = sess.cash / currentPrice
		sess.cash = 0
		sess.Trades++
		pnl := (sess.totalValue() - sess.notional) / sess.notional * 100
		sess.TradeHistory = append(sess.TradeHistory, TradeRecord{T: now, Kind: "buy", Price: currentPrice, PnL: pnl})
	case signal == "sell" && sess.Position == "long":
		sess.cash = sess.shares * currentPrice
		sess.shares = 0
		sess.Position = "flat"
		sess.Trades++
		pnl := (sess.totalValue() - sess.notional) / sess.notional * 100
		sess.TradeHistory = append(sess.TradeHistory, TradeRecord{T: now, Kind: "sell", Price: currentPrice, PnL: pnl})
	}

	sess.PnL = (sess.totalValue() - sess.notional) / sess.notional * 100
	sess.EquityCurve = append(sess.EquityCurve, EquityPoint{T: now, V: sess.PnL})
	if len(sess.EquityCurve) > 2000 {
		sess.EquityCurve = sess.EquityCurve[len(sess.EquityCurve)-2000:]
	}
}

// computeCustomLiveSignal runs the Python harness and returns the final position as a signal.
func computeCustomLiveSignal(code string, bars []ohlcvBar) string {
	if _, err := exec.LookPath("python3"); err != nil {
		return "hold"
	}
	n := len(bars)
	dates := make([]string, n)
	closes := make([]float64, n)
	opens := make([]float64, n)
	highs := make([]float64, n)
	lows := make([]float64, n)
	vols := make([]float64, n)
	for i, b := range bars {
		dates[i] = b.Date
		closes[i] = b.Close
		opens[i] = b.Open
		highs[i] = b.High
		lows[i] = b.Low
		vols[i] = b.Volume
	}
	inputData, _ := json.Marshal(map[string]any{
		"dates": dates, "closes": closes, "opens": opens,
		"highs": highs, "lows": lows, "volumes": vols,
	})
	script := strings.Replace(pythonHarness, "{USER_CODE}", code, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-c", script)
	cmd.Stdin = bytes.NewReader(inputData)
	out, err := cmd.Output()
	if err != nil {
		return "hold"
	}
	var result struct {
		FinalPosition int    `json:"final_position"`
		Error         string `json:"error"`
	}
	if json.Unmarshal(out, &result) != nil || result.Error != "" {
		return "hold"
	}
	if result.FinalPosition == 1 {
		return "buy"
	}
	return "sell"
}

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

func fetchRecentBars(symbol, interval string, limit int) ([]ohlcvBar, error) {
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
	pf := func(m json.RawMessage) float64 {
		var s string
		json.Unmarshal(m, &s)
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	bars := make([]ohlcvBar, 0, len(raw))
	for _, bar := range raw {
		if len(bar) < 6 {
			continue
		}
		var ts int64
		json.Unmarshal(bar[0], &ts)
		bars = append(bars, ohlcvBar{
			Date:   time.UnixMilli(ts).UTC().Format("2006-01-02 15:04"),
			Open:   pf(bar[1]), High: pf(bar[2]), Low: pf(bar[3]),
			Close:  pf(bar[4]), Volume: pf(bar[5]),
		})
	}
	return bars, nil
}

// handleLiveBTC returns BTC % change from a given start time, used for chart comparison.
func handleLiveBTC(w http.ResponseWriter, r *http.Request) {
	fromStr := r.URL.Query().Get("from")
	interval := r.URL.Query().Get("interval")
	if interval == "" {
		interval = "1m"
	}
	fromMs, err := strconv.ParseInt(fromStr, 10, 64)
	if err != nil || fromMs <= 0 {
		http.Error(w, "bad from param", http.StatusBadRequest)
		return
	}
	url := fmt.Sprintf(
		"https://api.binance.com/api/v3/klines?symbol=BTCUSDT&interval=%s&startTime=%d&limit=500",
		interval, fromMs,
	)
	resp, err := binanceClient.Get(url)
	if err != nil {
		http.Error(w, "fetch failed", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var raw [][]json.RawMessage
	if json.Unmarshal(body, &raw) != nil || len(raw) == 0 {
		writeJSON(w, []any{})
		return
	}

	type point struct {
		T int64   `json:"t"`
		V float64 `json:"v"`
	}
	var basePrice float64
	points := make([]point, 0, len(raw))
	for _, bar := range raw {
		if len(bar) < 5 {
			continue
		}
		var ts int64
		json.Unmarshal(bar[0], &ts)
		var s string
		json.Unmarshal(bar[4], &s)
		c, _ := strconv.ParseFloat(s, 64)
		if basePrice == 0 {
			basePrice = c
		}
		if basePrice > 0 {
			points = append(points, point{T: ts, V: (c - basePrice) / basePrice * 100})
		}
	}
	writeJSON(w, points)
}

// ── HTTP handlers ──────────────────────────────────────────────────────────

func (sm *sessionManager) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Symbols  []string `json:"symbols"`
		Strategy string   `json:"strategy"`
		Interval string   `json:"interval"`
		Code     string   `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Symbols) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Interval == "" {
		req.Interval = "1m"
	}
	ids := make([]string, 0, len(req.Symbols))
	for _, sym := range req.Symbols {
		ids = append(ids, sm.start(sym, req.Strategy, req.Interval, req.Code))
	}
	writeJSON(w, map[string][]string{"ids": ids})
}

func (sm *sessionManager) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sm.stop(req.ID)
	w.WriteHeader(http.StatusOK)
}

func (sm *sessionManager) handleList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sm.listSnapshots())
}

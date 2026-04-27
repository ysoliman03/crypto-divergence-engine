package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var binanceClient = &http.Client{Timeout: 30 * time.Second}

type BacktestRequest struct {
	Symbol      string             `json:"symbol"`
	Interval    string             `json:"interval"`
	FromDate    string             `json:"fromDate"`
	ToDate      string             `json:"toDate"`
	ForwardDays int                `json:"forwardDays"`
	Strategy    string             `json:"strategy"`
	Params      map[string]float64 `json:"params"`
}

func (r *BacktestRequest) param(key string, fallback float64) float64 {
	if v, ok := r.Params[key]; ok {
		return v
	}
	return fallback
}

type SignalRow struct {
	Date          string  `json:"date"`
	Price         float64 `json:"price"`
	Type          string  `json:"type"`
	ForwardReturn float64 `json:"forwardReturn"`
	HasForward    bool    `json:"hasForward"`
}

type BacktestResult struct {
	Signals     []SignalRow `json:"signals"`
	StratReturn float64    `json:"stratReturn"`
	WinRate     float64    `json:"winRate"`
	Correlation float64    `json:"correlation"`
	NumSignals  int        `json:"numSignals"`
	BarsUsed    int        `json:"barsUsed"`
	Error       string     `json:"error,omitempty"`
}

type tradeEvent struct {
	barIdx int
	date   string
	price  float64
	kind   string // "buy" or "sell"
}

func handleBacktest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req BacktestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, BacktestResult{Error: "invalid request"})
		return
	}
	if req.ForwardDays <= 0 {
		req.ForwardDays = 5
	}
	if req.Interval == "" {
		req.Interval = "1d"
	}
	if req.Strategy == "" {
		req.Strategy = "sma_crossover"
	}
	if req.Params == nil {
		req.Params = map[string]float64{}
	}

	from, err := time.Parse("2006-01-02", req.FromDate)
	if err != nil {
		writeJSON(w, BacktestResult{Error: "invalid from date"})
		return
	}
	to := time.Now()
	if req.ToDate != "" {
		if t, err := time.Parse("2006-01-02", req.ToDate); err == nil {
			to = t.Add(24 * time.Hour)
		}
	}

	closes, dates, err := fetchKlines(req.Symbol, req.Interval, from, to)
	if err != nil {
		writeJSON(w, BacktestResult{Error: "data fetch failed: " + err.Error()})
		return
	}

	var result BacktestResult
	switch req.Strategy {
	case "sma_crossover":
		result = runCrossover(closes, dates, int(req.param("fastPeriod", 9)), int(req.param("slowPeriod", 21)), req.ForwardDays, false)
	case "ema_crossover":
		result = runCrossover(closes, dates, int(req.param("fastPeriod", 9)), int(req.param("slowPeriod", 21)), req.ForwardDays, true)
	case "rsi_mean_reversion":
		result = runRSI(closes, dates, int(req.param("period", 14)), req.param("oversold", 30), req.param("overbought", 70), req.ForwardDays)
	case "bollinger_bands":
		result = runBollinger(closes, dates, int(req.param("period", 20)), req.param("stdDevs", 2.0), req.ForwardDays)
	case "breakout":
		result = runBreakout(closes, dates, int(req.param("lookback", 20)), int(req.param("holdBars", 10)), req.ForwardDays)
	default:
		writeJSON(w, BacktestResult{Error: "unknown strategy: " + req.Strategy})
		return
	}

	writeJSON(w, result)
}

// computeResult converts a list of trade events into a full BacktestResult with all stats.
func computeResult(events []tradeEvent, closes []float64, n, warmup, forwardDays int) BacktestResult {
	signals := make([]SignalRow, len(events))
	for i, e := range events {
		row := SignalRow{Date: e.date, Price: e.price, Type: e.kind}
		if e.barIdx+forwardDays < n {
			row.ForwardReturn = (closes[e.barIdx+forwardDays] - closes[e.barIdx]) / closes[e.barIdx] * 100
			row.HasForward = true
		}
		signals[i] = row
	}

	stratReturn := 1.0
	for i := 0; i+1 < len(events); i += 2 {
		if events[i].kind == "buy" && events[i+1].kind == "sell" {
			stratReturn *= 1 + (events[i+1].price-events[i].price)/events[i].price
		}
	}
	if len(events)%2 == 1 && events[len(events)-1].kind == "buy" {
		stratReturn *= 1 + (closes[n-1]-events[len(events)-1].price)/events[len(events)-1].price
	}

	wins, buyN := 0, 0
	for _, s := range signals {
		if s.Type == "buy" && s.HasForward {
			buyN++
			if s.ForwardReturn > 0 {
				wins++
			}
		}
	}
	winRate := 0.0
	if buyN > 0 {
		winRate = float64(wins) / float64(buyN) * 100
	}

	indicator := make([]float64, n)
	for _, e := range events {
		if e.kind == "buy" {
			indicator[e.barIdx] = 1
		}
	}
	xs := make([]float64, 0, n)
	ys := make([]float64, 0, n)
	for i := warmup; i+forwardDays < n; i++ {
		fwd := (closes[i+forwardDays] - closes[i]) / closes[i] * 100
		xs = append(xs, indicator[i])
		ys = append(ys, fwd)
	}

	return BacktestResult{
		Signals:     signals,
		StratReturn: (stratReturn - 1) * 100,
		WinRate:     winRate,
		Correlation: btPearson(xs, ys),
		NumSignals:  len(signals),
		BarsUsed:    n,
	}
}

func runCrossover(closes []float64, dates []time.Time, fast, slow, forwardDays int, useEMA bool) BacktestResult {
	n := len(closes)
	if n < slow+2 {
		return BacktestResult{Error: fmt.Sprintf("not enough data: need %d bars, got %d", slow+2, n)}
	}

	var fastMA, slowMA []float64
	if useEMA {
		fastMA = computeEMA(closes, fast)
		slowMA = computeEMA(closes, slow)
	} else {
		fastMA = make([]float64, n)
		slowMA = make([]float64, n)
		for i := slow; i < n; i++ {
			fastMA[i] = barSMA(closes, i+1, fast)
			slowMA[i] = barSMA(closes, i+1, slow)
		}
	}

	var events []tradeEvent
	inPosition := false
	for i := slow + 1; i < n; i++ {
		prevAbove := fastMA[i-1] > slowMA[i-1]
		currAbove := fastMA[i] > slowMA[i]
		if !prevAbove && currAbove && !inPosition {
			inPosition = true
			events = append(events, tradeEvent{i, dates[i].Format("2006-01-02"), closes[i], "buy"})
		} else if prevAbove && !currAbove && inPosition {
			inPosition = false
			events = append(events, tradeEvent{i, dates[i].Format("2006-01-02"), closes[i], "sell"})
		}
	}
	return computeResult(events, closes, n, slow+1, forwardDays)
}

func runRSI(closes []float64, dates []time.Time, period int, oversold, overbought float64, forwardDays int) BacktestResult {
	n := len(closes)
	if n < period+2 {
		return BacktestResult{Error: fmt.Sprintf("not enough data: need %d bars, got %d", period+2, n)}
	}

	rsi := computeRSI(closes, period)

	var events []tradeEvent
	inPosition := false
	for i := period + 1; i < n; i++ {
		if rsi[i] == 0 {
			continue
		}
		if rsi[i] < oversold && !inPosition {
			inPosition = true
			events = append(events, tradeEvent{i, dates[i].Format("2006-01-02"), closes[i], "buy"})
		} else if rsi[i] > overbought && inPosition {
			inPosition = false
			events = append(events, tradeEvent{i, dates[i].Format("2006-01-02"), closes[i], "sell"})
		}
	}
	return computeResult(events, closes, n, period+1, forwardDays)
}

func runBollinger(closes []float64, dates []time.Time, period int, stdDevs float64, forwardDays int) BacktestResult {
	n := len(closes)
	if n < period+1 {
		return BacktestResult{Error: fmt.Sprintf("not enough data: need %d bars, got %d", period+1, n)}
	}

	_, middle, lower := computeBollinger(closes, period, stdDevs)

	var events []tradeEvent
	inPosition := false
	for i := period; i < n; i++ {
		if lower[i] == 0 {
			continue
		}
		if closes[i] < lower[i] && !inPosition {
			inPosition = true
			events = append(events, tradeEvent{i, dates[i].Format("2006-01-02"), closes[i], "buy"})
		} else if closes[i] > middle[i] && inPosition {
			inPosition = false
			events = append(events, tradeEvent{i, dates[i].Format("2006-01-02"), closes[i], "sell"})
		}
	}
	return computeResult(events, closes, n, period, forwardDays)
}

func runBreakout(closes []float64, dates []time.Time, lookback, holdBars, forwardDays int) BacktestResult {
	n := len(closes)
	if n < lookback+1 {
		return BacktestResult{Error: fmt.Sprintf("not enough data: need %d bars, got %d", lookback+1, n)}
	}

	var events []tradeEvent
	inPosition := false
	entryBar := 0

	for i := lookback; i < n; i++ {
		if inPosition {
			if i >= entryBar+holdBars {
				inPosition = false
				events = append(events, tradeEvent{i, dates[i].Format("2006-01-02"), closes[i], "sell"})
			}
			continue
		}
		maxClose := closes[i-lookback]
		for j := i - lookback + 1; j < i; j++ {
			if closes[j] > maxClose {
				maxClose = closes[j]
			}
		}
		if closes[i] > maxClose {
			inPosition = true
			entryBar = i
			events = append(events, tradeEvent{i, dates[i].Format("2006-01-02"), closes[i], "buy"})
		}
	}
	return computeResult(events, closes, n, lookback, forwardDays)
}

func computeEMA(prices []float64, period int) []float64 {
	n := len(prices)
	ema := make([]float64, n)
	if n < period {
		return ema
	}
	k := 2.0 / float64(period+1)
	var seed float64
	for i := 0; i < period; i++ {
		seed += prices[i]
	}
	ema[period-1] = seed / float64(period)
	for i := period; i < n; i++ {
		ema[i] = prices[i]*k + ema[i-1]*(1-k)
	}
	return ema
}

func computeRSI(prices []float64, period int) []float64 {
	n := len(prices)
	rsi := make([]float64, n)
	if n < period+1 {
		return rsi
	}

	var avgGain, avgLoss float64
	for i := 1; i <= period; i++ {
		d := prices[i] - prices[i-1]
		if d > 0 {
			avgGain += d
		} else {
			avgLoss -= d
		}
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)

	toRSI := func(g, l float64) float64 {
		if l == 0 {
			return 100
		}
		return 100 - 100/(1+g/l)
	}
	rsi[period] = toRSI(avgGain, avgLoss)

	for i := period + 1; i < n; i++ {
		d := prices[i] - prices[i-1]
		gain, loss := 0.0, 0.0
		if d > 0 {
			gain = d
		} else {
			loss = -d
		}
		avgGain = (avgGain*float64(period-1) + gain) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + loss) / float64(period)
		rsi[i] = toRSI(avgGain, avgLoss)
	}
	return rsi
}

func computeBollinger(prices []float64, period int, stdDevs float64) (upper, middle, lower []float64) {
	n := len(prices)
	upper = make([]float64, n)
	middle = make([]float64, n)
	lower = make([]float64, n)
	for i := period - 1; i < n; i++ {
		sma := barSMA(prices, i+1, period)
		middle[i] = sma
		var variance float64
		for j := i - period + 1; j <= i; j++ {
			d := prices[j] - sma
			variance += d * d
		}
		std := math.Sqrt(variance / float64(period))
		upper[i] = sma + stdDevs*std
		lower[i] = sma - stdDevs*std
	}
	return
}

func fetchKlines(symbol, interval string, from, to time.Time) ([]float64, []time.Time, error) {
	url := fmt.Sprintf(
		"https://api.binance.com/api/v3/klines?symbol=%s&interval=%s&startTime=%d&endTime=%d&limit=1000",
		symbol, interval, from.UnixMilli(), to.UnixMilli(),
	)
	resp, err := binanceClient.Get(url)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Msg string `json:"msg"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Msg != "" {
			return nil, nil, fmt.Errorf("%s", apiErr.Msg)
		}
		return nil, nil, fmt.Errorf("binance returned %d", resp.StatusCode)
	}

	var raw [][]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, fmt.Errorf("parse response: %w", err)
	}

	closes := make([]float64, 0, len(raw))
	dates := make([]time.Time, 0, len(raw))
	for _, bar := range raw {
		if len(bar) < 5 {
			continue
		}
		var openTimeMs int64
		if err := json.Unmarshal(bar[0], &openTimeMs); err != nil {
			continue
		}
		var closeStr string
		if err := json.Unmarshal(bar[4], &closeStr); err != nil {
			continue
		}
		c, err := strconv.ParseFloat(closeStr, 64)
		if err != nil {
			continue
		}
		closes = append(closes, c)
		dates = append(dates, time.UnixMilli(openTimeMs))
	}
	return closes, dates, nil
}

func barSMA(prices []float64, end, period int) float64 {
	if end < period {
		return 0
	}
	var sum float64
	for i := end - period; i < end; i++ {
		sum += prices[i]
	}
	return sum / float64(period)
}

func btPearson(xs, ys []float64) float64 {
	n := len(xs)
	if n < 2 {
		return 0
	}
	var mX, mY float64
	for i := range n {
		mX += xs[i]
		mY += ys[i]
	}
	mX /= float64(n)
	mY /= float64(n)
	var num, dX, dY float64
	for i := range n {
		dx := xs[i] - mX
		dy := ys[i] - mY
		num += dx * dy
		dX += dx * dx
		dY += dy * dy
	}
	if dX == 0 || dY == 0 {
		return 0
	}
	return num / math.Sqrt(dX*dY)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ── Custom Python strategy ──────────────────────────────────────────────────

type ohlcvBar struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

// pythonHarness wraps the user's on_bar function and drives it bar-by-bar.
// {USER_CODE} is replaced with the user's code before execution.
const pythonHarness = `
import json, sys, traceback

{USER_CODE}

try:
    data   = json.load(sys.stdin)
    dates   = data['dates']
    closes  = data['closes']
    opens   = data['opens']
    highs   = data['highs']
    lows    = data['lows']
    volumes = data['volumes']

    memory   = {}
    position = 0
    events   = []

    for i in range(len(closes)):
        try:
            signal = on_bar(i, closes, opens, highs, lows, volumes, dates, position, memory)
        except Exception as e:
            sys.stdout.write(json.dumps({'error': 'bar %d (%s): %s' % (i, dates[i], str(e))}))
            sys.exit(1)

        if signal == 'buy' and position == 0:
            position = 1
            events.append({'barIdx': i, 'date': dates[i], 'price': closes[i], 'kind': 'buy'})
        elif signal == 'sell' and position == 1:
            position = 0
            events.append({'barIdx': i, 'date': dates[i], 'price': closes[i], 'kind': 'sell'})

    sys.stdout.write(json.dumps({'events': events, 'final_position': position}))

except Exception:
    sys.stdout.write(json.dumps({'error': traceback.format_exc()}))
    sys.exit(1)
`

func handleCustomBacktest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Symbol      string `json:"symbol"`
		Interval    string `json:"interval"`
		FromDate    string `json:"fromDate"`
		ToDate      string `json:"toDate"`
		ForwardDays int    `json:"forwardDays"`
		Code        string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, BacktestResult{Error: "invalid request"})
		return
	}
	if req.ForwardDays <= 0 {
		req.ForwardDays = 5
	}

	if _, err := exec.LookPath("python3"); err != nil {
		writeJSON(w, BacktestResult{Error: "python3 not found — install it or rebuild the container"})
		return
	}

	from, err := time.Parse("2006-01-02", req.FromDate)
	if err != nil {
		writeJSON(w, BacktestResult{Error: "invalid from date"})
		return
	}
	to := time.Now()
	if req.ToDate != "" {
		if t, err := time.Parse("2006-01-02", req.ToDate); err == nil {
			to = t.Add(24 * time.Hour)
		}
	}

	bars, err := fetchFullKlines(req.Symbol, req.Interval, from, to)
	if err != nil {
		writeJSON(w, BacktestResult{Error: "data fetch failed: " + err.Error()})
		return
	}

	// Build per-column arrays for the Python harness.
	n := len(bars)
	dates := make([]string, n)
	closes := make([]float64, n)
	opens := make([]float64, n)
	highs := make([]float64, n)
	lows := make([]float64, n)
	volumes := make([]float64, n)
	for i, b := range bars {
		dates[i] = b.Date
		closes[i] = b.Close
		opens[i] = b.Open
		highs[i] = b.High
		lows[i] = b.Low
		volumes[i] = b.Volume
	}

	inputData, _ := json.Marshal(map[string]any{
		"dates":   dates,
		"closes":  closes,
		"opens":   opens,
		"highs":   highs,
		"lows":    lows,
		"volumes": volumes,
	})

	script := strings.Replace(pythonHarness, "{USER_CODE}", req.Code, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "python3", "-c", script)
	cmd.Stdin = bytes.NewReader(inputData)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			writeJSON(w, BacktestResult{Error: "strategy timed out after 20 seconds — check for infinite loops"})
			return
		}
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			writeJSON(w, BacktestResult{Error: string(exitErr.Stderr)})
			return
		}
	}

	var pyResult struct {
		Events []struct {
			BarIdx int     `json:"barIdx"`
			Date   string  `json:"date"`
			Price  float64 `json:"price"`
			Kind   string  `json:"kind"`
		} `json:"events"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &pyResult); err != nil {
		writeJSON(w, BacktestResult{Error: "could not parse strategy output — check your code returns nothing from on_bar except \"buy\", \"sell\", or None"})
		return
	}
	if pyResult.Error != "" {
		writeJSON(w, BacktestResult{Error: pyResult.Error})
		return
	}

	events := make([]tradeEvent, len(pyResult.Events))
	for i, e := range pyResult.Events {
		events[i] = tradeEvent{barIdx: e.BarIdx, date: e.Date, price: e.Price, kind: e.Kind}
	}

	result := computeResult(events, closes, n, 0, req.ForwardDays)
	writeJSON(w, result)
}

func fetchFullKlines(symbol, interval string, from, to time.Time) ([]ohlcvBar, error) {
	url := fmt.Sprintf(
		"https://api.binance.com/api/v3/klines?symbol=%s&interval=%s&startTime=%d&endTime=%d&limit=1000",
		symbol, interval, from.UnixMilli(), to.UnixMilli(),
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
		var apiErr struct{ Msg string `json:"msg"` }
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Msg != "" {
			return nil, fmt.Errorf("%s", apiErr.Msg)
		}
		return nil, fmt.Errorf("binance returned %d", resp.StatusCode)
	}

	var raw [][]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	parseFloat := func(msg json.RawMessage) float64 {
		var s string
		if json.Unmarshal(msg, &s) == nil {
			v, _ := strconv.ParseFloat(s, 64)
			return v
		}
		return 0
	}

	bars := make([]ohlcvBar, 0, len(raw))
	for _, bar := range raw {
		if len(bar) < 6 {
			continue
		}
		var openTimeMs int64
		if err := json.Unmarshal(bar[0], &openTimeMs); err != nil {
			continue
		}
		bars = append(bars, ohlcvBar{
			Date:   time.UnixMilli(openTimeMs).Format("2006-01-02"),
			Open:   parseFloat(bar[1]),
			High:   parseFloat(bar[2]),
			Low:    parseFloat(bar[3]),
			Close:  parseFloat(bar[4]),
			Volume: parseFloat(bar[5]),
		})
	}
	return bars, nil
}

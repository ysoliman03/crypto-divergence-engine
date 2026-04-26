// This file knows everything about Binance and nothing about Redis. 
// Its one job: connect to Binance, read trade messages, convert them into Tick structs.
package ingester

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/youssef/divergence-engine/internal/tick"
)

// BinanceAdapter implements ExchangeAdapter for Binance's public trade stream.
type BinanceAdapter struct{}

func NewBinanceAdapter() *BinanceAdapter { return &BinanceAdapter{} }

func (b *BinanceAdapter) Name() string { return "binance" }

// binanceTrade is the payload inside Binance's combined stream envelope.
type binanceTrade struct {
	Symbol    string `json:"s"`
	TradeID   int64  `json:"t"`
	Price     string `json:"p"`
	Quantity  string `json:"q"`
	TradeTime int64  `json:"T"` // ms since epoch
	IsMaker   bool   `json:"m"` // true = buyer is maker → seller was the aggressor
}

func (b *BinanceAdapter) Subscribe(ctx context.Context, symbols []string, out chan<- tick.Tick) error {
	streams := make([]string, len(symbols))
	for i, s := range symbols {
		streams[i] = strings.ToLower(strings.ReplaceAll(s, "-", "")) + "@trade"
	}
	url := "wss://stream.binance.com:9443/stream?streams=" + strings.Join(streams, "/")

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Unblock ReadMessage when ctx is cancelled so the goroutine exits cleanly.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	var envelope struct {
		Data binanceTrade `json:"data"`
	}

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}

		if err := json.Unmarshal(msg, &envelope); err != nil {
			continue
		}

		t := normalizeBinanceTrade(envelope.Data)

		select {
		case out <- t:
		default: // channel full: drop tick rather than block the WebSocket reader
		}
	}
}

func normalizeBinanceTrade(m binanceTrade) tick.Tick {
	price, _ := strconv.ParseFloat(m.Price, 64)
	volume, _ := strconv.ParseFloat(m.Quantity, 64)

	side := "buy"
	if m.IsMaker {
		side = "sell"
	}

	return tick.Tick{
		Symbol:     normalizeSymbol(m.Symbol),
		Exchange:   "binance",
		Price:      price,
		Volume:     volume,
		Side:       side,
		TradeID:    strconv.FormatInt(m.TradeID, 10),
		Timestamp:  time.UnixMilli(m.TradeTime).UTC(),
		ReceivedAt: time.Now().UTC(),
	}
}

// normalizeSymbol converts Binance's concatenated format to dash-separated.
// BTCUSDT → BTC-USDT, ETHBTC → ETH-BTC
// Quotes are checked longest-first to avoid partial matches (USDT before BTC).
func normalizeSymbol(s string) string {
	quotes := []string{"USDT", "BUSD", "USDC", "BTC", "ETH", "BNB"}
	s = strings.ToUpper(s)
	for _, q := range quotes {
		if strings.HasSuffix(s, q) {
			return s[:len(s)-len(q)] + "-" + q
		}
	}
	return s
}

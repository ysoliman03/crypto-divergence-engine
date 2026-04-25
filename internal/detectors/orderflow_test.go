package detectors

import (
	"context"
	"testing"
	"time"

	"github.com/youssef/divergence-engine/internal/tick"
)

// testOFI returns a detector with warmup and cooldown disabled for unit testing.
func testOFI() *OrderFlowImbalanceDetector {
	return &OrderFlowImbalanceDetector{
		window:         10 * time.Second,
		upperThreshold: 0.8,
		lowerThreshold: 0.2,
		minVolume:      0.001,
		minTicks:       10,
		minHistory:     0, // no warmup
		cooldown:       0, // no cooldown
	}
}

func makeTick(symbol, side string, volume float64, at time.Time) tick.Tick {
	return tick.Tick{
		Symbol:    symbol,
		Exchange:  "binance",
		Side:      side,
		Volume:    volume,
		Timestamp: at,
	}
}

func TestOFI_BuyImbalanceFires(t *testing.T) {
	d := testOFI()
	ctx := context.Background()
	base := time.Now()

	var got int
	for i := range 20 {
		alerts := d.OnTick(ctx, makeTick("BTC-USDT", "buy", 0.1, base.Add(time.Duration(i)*200*time.Millisecond)))
		got += len(alerts)
	}

	if got == 0 {
		t.Fatal("expected at least one buy imbalance alert, got none")
	}
}

func TestOFI_SellImbalanceFires(t *testing.T) {
	d := testOFI()
	ctx := context.Background()
	base := time.Now()

	var got int
	for i := range 20 {
		alerts := d.OnTick(ctx, makeTick("BTC-USDT", "sell", 0.1, base.Add(time.Duration(i)*200*time.Millisecond)))
		got += len(alerts)
	}

	if got == 0 {
		t.Fatal("expected at least one sell imbalance alert, got none")
	}
}

func TestOFI_BalancedFlowNoAlert(t *testing.T) {
	d := testOFI()
	ctx := context.Background()
	base := time.Now()

	for i := range 40 {
		side := "buy"
		if i%2 == 1 {
			side = "sell"
		}
		alerts := d.OnTick(ctx, makeTick("BTC-USDT", side, 0.1, base.Add(time.Duration(i)*200*time.Millisecond)))
		if len(alerts) > 0 {
			t.Fatalf("expected no alerts for balanced flow, got %+v", alerts)
		}
	}
}

func TestOFI_IndependentPerSymbol(t *testing.T) {
	d := testOFI()
	ctx := context.Background()
	base := time.Now()

	// BTC is all buys, ETH is all sells — both should alert independently.
	var btcAlerts, ethAlerts int
	for i := range 20 {
		at := base.Add(time.Duration(i) * 200 * time.Millisecond)
		btcAlerts += len(d.OnTick(ctx, makeTick("BTC-USDT", "buy", 0.1, at)))
		ethAlerts += len(d.OnTick(ctx, makeTick("ETH-USDT", "sell", 0.1, at)))
	}

	if btcAlerts == 0 {
		t.Error("expected BTC-USDT buy imbalance alert")
	}
	if ethAlerts == 0 {
		t.Error("expected ETH-USDT sell imbalance alert")
	}
}

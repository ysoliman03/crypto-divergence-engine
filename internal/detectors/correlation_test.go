package detectors

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/youssef/divergence-engine/internal/alert"
	"github.com/youssef/divergence-engine/internal/state"
	"github.com/youssef/divergence-engine/internal/tick"
)

// ── pearson unit tests ────────────────────────────────────────────────────────

func TestPearson_PerfectPositive(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5}
	ys := []float64{2, 4, 6, 8, 10} // ys = 2*xs
	got := pearson(xs, ys)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("expected 1.0, got %.6f", got)
	}
}

func TestPearson_PerfectNegative(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5}
	ys := []float64{10, 8, 6, 4, 2}
	got := pearson(xs, ys)
	if math.Abs(got+1.0) > 1e-9 {
		t.Errorf("expected -1.0, got %.6f", got)
	}
}

func TestPearson_Uncorrelated(t *testing.T) {
	// These two binary sequences are orthogonal: their dot product (centered) is 0.
	xs := []float64{0, 1, 0, 1, 0, 1, 0, 1} // alternating
	ys := []float64{0, 0, 1, 1, 0, 0, 1, 1} // block pattern
	got := pearson(xs, ys)
	if math.Abs(got) > 1e-9 {
		t.Errorf("expected exactly 0, got %.6f", got)
	}
}

func TestPearson_InsufficientData(t *testing.T) {
	if got := pearson([]float64{1}, []float64{1}); got != 0 {
		t.Errorf("expected 0 for n<2, got %f", got)
	}
	if got := pearson(nil, nil); got != 0 {
		t.Errorf("expected 0 for empty slices, got %f", got)
	}
}

// ── detector integration tests ────────────────────────────────────────────────

// testCorrDetector returns a detector wired for fast testing:
// no warmup, no cooldown, sample on every tick, alert after 10 samples.
func testCorrDetector() *CorrelationBreakdownDetector {
	pairs := []symbolPair{{a: "BTC-USDT", b: "ETH-USDT"}}
	states := map[string]*corrState{
		pairKey("BTC-USDT", "ETH-USDT"): {
			returnsA: state.NewRingBuffer(1000),
			returnsB: state.NewRingBuffer(1000),
		},
	}
	return &CorrelationBreakdownDetector{
		pairs:          pairs,
		mu:             sync.Mutex{},
		pairStates:     states,
		shortSamples:   10,
		minSamples:     10,
		minBaseline:    0.5,
		dropThreshold:  0.4,
		sampleInterval: 0, // sample on every tick pair
		minHistory:     0,
		cooldown:       0,
	}
}

func feedPair(d *CorrelationBreakdownDetector, priceA, priceB float64, at time.Time) {
	ctx := context.Background()
	d.OnTick(ctx, tick.Tick{Symbol: "BTC-USDT", Price: priceA, Timestamp: at})
	d.OnTick(ctx, tick.Tick{Symbol: "ETH-USDT", Price: priceB, Timestamp: at.Add(time.Millisecond)})
}

func TestCorrelation_BreakdownFires(t *testing.T) {
	d := testCorrDetector()
	ctx := context.Background()
	base := time.Now()

	// Build baseline: strongly correlated series (both move together).
	for i := range 30 {
		p := 100.0 + float64(i)*0.1
		feedPair(d, p, p*0.05, base.Add(time.Duration(i)*time.Second))
	}

	// Now inject a breakdown: A drops, B stays flat.
	var alerts []alert.Alert
	for i := range 15 {
		at := base.Add(30*time.Second + time.Duration(i)*time.Second)
		pA := 103.0 - float64(i)*0.5 // A falling
		pB := 5.15                    // B flat
		d.OnTick(ctx, tick.Tick{Symbol: "BTC-USDT", Price: pA, Timestamp: at})
		r := d.OnTick(ctx, tick.Tick{Symbol: "ETH-USDT", Price: pB, Timestamp: at.Add(time.Millisecond)})
		alerts = append(alerts, r...)
	}

	if len(alerts) == 0 {
		t.Fatal("expected correlation breakdown alert, got none")
	}
	if alerts[0].Detector != "correlation_breakdown" {
		t.Errorf("wrong detector: %q", alerts[0].Detector)
	}
}

func TestCorrelation_NoAlertWhenSustained(t *testing.T) {
	d := testCorrDetector()
	ctx := context.Background()
	base := time.Now()

	// Feed a long sustained correlated series — no breakdown.
	for i := range 60 {
		p := 100.0 + float64(i)*0.1
		at := base.Add(time.Duration(i) * time.Second)
		d.OnTick(ctx, tick.Tick{Symbol: "BTC-USDT", Price: p, Timestamp: at})
		r := d.OnTick(ctx, tick.Tick{Symbol: "ETH-USDT", Price: p * 0.05, Timestamp: at.Add(time.Millisecond)})
		if len(r) > 0 {
			t.Fatalf("unexpected alert on sustained correlation: %+v", r)
		}
	}
}

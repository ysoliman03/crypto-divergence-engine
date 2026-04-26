package detectors

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/youssef/divergence-engine/internal/alert"
	"github.com/youssef/divergence-engine/internal/state"
	"github.com/youssef/divergence-engine/internal/tick"
)

// symbolPair is an ordered pair of symbols whose correlation is tracked.
type symbolPair struct{ a, b string }

func pairKey(a, b string) string { return a + ":" + b }

type corrState struct {
	returnsA *state.RingBuffer // log-returns for symbol A, sampled at sampleInterval
	returnsB *state.RingBuffer // log-returns for symbol B, same cadence

	// Snapshot prices at the last sample point.
	snapshotPriceA float64
	snapshotPriceB float64
	snapshotAt     time.Time

	// Latest prices received (may be ahead of snapshot).
	latestPriceA float64
	latestPriceB float64

	// True once the symbol has ticked since the last sample was taken.
	updatedA bool
	updatedB bool

	firstSeen time.Time
	lastAlert time.Time
}

// CorrelationBreakdownDetector alerts when the short-term rolling correlation
// between a symbol pair drops significantly below its long-term baseline.
//
// Returns for both symbols are sampled at sampleInterval and stored in ring
// buffers. The short-term correlation uses the last shortSamples entries;
// the baseline uses all entries. Both are computed via pearson(), which runs
// Welford's online algorithm to avoid catastrophic cancellation in the naive
// sum-of-squares formula.
type CorrelationBreakdownDetector struct {
	pairs         []symbolPair
	mu            sync.Mutex
	pairStates    map[string]*corrState

	shortSamples   int           // entries in the short-term window
	minSamples     int           // minimum entries before alerting
	minBaseline    float64       // don't alert if baseline corr is below this
	dropThreshold  float64       // alert when shortCorr < baseline - dropThreshold
	sampleInterval time.Duration // how often to record a return sample
	minHistory     time.Duration // warmup before alerting
	cooldown       time.Duration
}

func NewCorrelationBreakdownDetector() *CorrelationBreakdownDetector {
	pairs := []symbolPair{
		{a: "BTC-USDT", b: "ETH-USDT"},
		{a: "BTC-USDT", b: "SOL-USDT"},
	}
	states := make(map[string]*corrState, len(pairs))
	for _, p := range pairs {
		states[pairKey(p.a, p.b)] = &corrState{
			returnsA: state.NewRingBuffer(20_000),
			returnsB: state.NewRingBuffer(20_000),
		}
	}
	return &CorrelationBreakdownDetector{
		pairs:          pairs,
		pairStates:     states,
		shortSamples:   720,              // ~1 hour at one sample per 5 seconds
		minSamples:     30,               // need at least 30 pairs before alerting
		minBaseline:    0.5,              // only alert on pairs that are usually correlated
		dropThreshold:  0.4,              // short corr must drop 0.4 below baseline
		sampleInterval: 5 * time.Second,
		minHistory:     10 * time.Minute,
		cooldown:       5 * time.Minute,
	}
}

func (d *CorrelationBreakdownDetector) Name() string { return "correlation_breakdown" }

func (d *CorrelationBreakdownDetector) OnTick(ctx context.Context, t tick.Tick) []alert.Alert {
	var result []alert.Alert

	d.mu.Lock()
	defer d.mu.Unlock()

	for _, pair := range d.pairs {
		s, ok := d.pairStates[pairKey(pair.a, pair.b)]
		if !ok {
			continue
		}

		// Update the latest price for whichever symbol this tick belongs to.
		switch t.Symbol {
		case pair.a:
			s.latestPriceA = t.Price
			s.updatedA = true
		case pair.b:
			s.latestPriceB = t.Price
			s.updatedB = true
		default:
			continue
		}

		// Can't do anything until we have at least one price for each symbol.
		if s.latestPriceA == 0 || s.latestPriceB == 0 {
			continue
		}

		// First time both prices are available: set the initial snapshot.
		if s.snapshotAt.IsZero() {
			s.snapshotPriceA = s.latestPriceA
			s.snapshotPriceB = s.latestPriceB
			s.snapshotAt = t.Timestamp
			s.firstSeen = t.Timestamp
			s.updatedA = false
			s.updatedB = false
			continue
		}

		// Only sample once both symbols have ticked and the interval has elapsed.
		if !s.updatedA || !s.updatedB {
			continue
		}
		if t.Timestamp.Sub(s.snapshotAt) < d.sampleInterval {
			continue
		}

		// Compute log-returns relative to the snapshot prices.
		if s.snapshotPriceA > 0 && s.snapshotPriceB > 0 {
			ra := math.Log(s.latestPriceA / s.snapshotPriceA)
			rb := math.Log(s.latestPriceB / s.snapshotPriceB)
			s.returnsA.Push(t.Timestamp, ra)
			s.returnsB.Push(t.Timestamp, rb)
		}

		s.snapshotPriceA = s.latestPriceA
		s.snapshotPriceB = s.latestPriceB
		s.snapshotAt = t.Timestamp
		s.updatedA = false
		s.updatedB = false

		// Warmup and minimum-samples guards.
		if t.Timestamp.Sub(s.firstSeen) < d.minHistory {
			continue
		}
		n := s.returnsA.Len()
		if n < d.minSamples {
			continue
		}

		// Baseline correlation: all samples in the buffer.
		baseA := s.returnsA.Slice(n)
		baseB := s.returnsB.Slice(n)
		baselineCorr := pearson(baseA, baseB)

		if baselineCorr < d.minBaseline {
			continue // pair isn't strongly correlated — don't alert
		}

		// Short-term correlation: most recent shortSamples entries.
		shortA := s.returnsA.Slice(d.shortSamples)
		shortB := s.returnsB.Slice(d.shortSamples)
		shortCorr := pearson(shortA, shortB)

		if shortCorr >= baselineCorr-d.dropThreshold {
			continue
		}
		if t.Timestamp.Sub(s.lastAlert) < d.cooldown {
			continue
		}
		s.lastAlert = t.Timestamp

		result = append(result, alert.Alert{
			Detector:  d.Name(),
			Symbol:    pairKey(pair.a, pair.b),
			Exchange:  "",
			Severity:  alert.SeverityWarning,
			Message:   fmt.Sprintf("%s/%s correlation breakdown: short=%.2f baseline=%.2f", pair.a, pair.b, shortCorr, baselineCorr),
			Value:     shortCorr,
			Threshold: baselineCorr - d.dropThreshold,
			Timestamp: t.Timestamp,
		})
	}

	return result
}

// pearson computes the Pearson correlation coefficient of xs and ys.
//
// Uses Welford's online algorithm extended to pairs: instead of accumulating
// raw sums (which suffer catastrophic cancellation for nearly-equal values),
// it maintains running means and updates M2X, M2Y, and C (the cross-product
// term for covariance) incrementally. The result is numerically stable even
// for long series with small variance.
func pearson(xs, ys []float64) float64 {
	n := len(xs)
	if len(ys) < n {
		n = len(ys)
	}
	if n < 2 {
		return 0
	}

	var count, meanX, meanY, C, M2X, M2Y float64
	for i := range n {
		count++
		dx := xs[i] - meanX
		meanX += dx / count
		dy := ys[i] - meanY
		meanY += dy / count
		C += dx * (ys[i] - meanY)   // cross-product: pre-update dx, post-update dy
		M2X += dx * (xs[i] - meanX) // Welford variance accumulator for X
		M2Y += dy * (ys[i] - meanY) // Welford variance accumulator for Y
	}

	if M2X <= 0 || M2Y <= 0 {
		return 0
	}
	return C / math.Sqrt(M2X*M2Y)
}

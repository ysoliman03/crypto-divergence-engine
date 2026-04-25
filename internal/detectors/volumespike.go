package detectors

import (
	"context"
	"fmt"
	"time"

	"github.com/youssef/divergence-engine/internal/alert"
	"github.com/youssef/divergence-engine/internal/state"
	"github.com/youssef/divergence-engine/internal/tick"
)

const (
	// minHistory is how long we collect data before trusting the baseline.
	// Without this, the long window is nearly empty at startup and the ratio
	// is always enormous, causing false alerts on every tick.
	minHistory = 5 * time.Minute

	// alertCooldown prevents the same alert firing on every tick once triggered.
	// Deduplication is the aggregator's job (Phase 6); this just keeps the output readable.
	alertCooldown = 30 * time.Second
)

type vsState struct {
	short     *state.RingBuffer
	long      *state.RingBuffer
	firstSeen time.Time // when we first saw a tick for this symbol
	lastAlert time.Time // when we last fired an alert for this symbol
}

// VolumeSpikeDetector alerts when the per-second trade volume over the last
// shortWindow exceeds multiplier × the per-second baseline over longWindow.
type VolumeSpikeDetector struct {
	symbols     state.ShardedMap[*vsState]
	shortWindow time.Duration
	longWindow  time.Duration
	multiplier  float64
}

func NewVolumeSpikeDetector() *VolumeSpikeDetector {
	return &VolumeSpikeDetector{
		shortWindow: 60 * time.Second,
		longWindow:  time.Hour,
		multiplier:  3.0,
	}
}

func (d *VolumeSpikeDetector) Name() string { return "volume_spike" }

func (d *VolumeSpikeDetector) OnTick(ctx context.Context, t tick.Tick) []alert.Alert {
	var result []alert.Alert

	d.symbols.Use(t.Symbol,
		func() *vsState {
			return &vsState{
				short: state.NewRingBuffer(5_000),
				long:  state.NewRingBuffer(100_000),
			}
		},
		func(s *vsState) {
			if s.firstSeen.IsZero() {
				s.firstSeen = t.Timestamp
			}
			s.short.Push(t.Timestamp, t.Volume)
			s.long.Push(t.Timestamp, t.Volume)

			// Wait until we have enough history to compute a meaningful baseline.
			if t.Timestamp.Sub(s.firstSeen) < minHistory {
				return
			}

			shortVol := s.short.SumSince(t.Timestamp, d.shortWindow)
			longVol := s.long.SumSince(t.Timestamp, d.longWindow)
			if longVol == 0 {
				return
			}

			shortRate := shortVol / d.shortWindow.Seconds()
			longRate := longVol / d.longWindow.Seconds()
			threshold := longRate * d.multiplier

			if shortRate > threshold && t.Timestamp.Sub(s.lastAlert) >= alertCooldown {
				s.lastAlert = t.Timestamp
				result = append(result, alert.Alert{
					Detector:  d.Name(),
					Symbol:    t.Symbol,
					Exchange:  t.Exchange,
					Severity:  alert.SeverityWarning,
					Message:   fmt.Sprintf("volume spike: %.1f× baseline rate", shortRate/longRate),
					Value:     shortRate,
					Threshold: threshold,
					Timestamp: t.Timestamp,
				})
			}
		},
	)

	return result
}

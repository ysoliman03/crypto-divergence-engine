package detectors

import (
	"context"
	"fmt"
	"time"

	"github.com/youssef/divergence-engine/internal/alert"
	"github.com/youssef/divergence-engine/internal/state"
	"github.com/youssef/divergence-engine/internal/tick"
)

type vsState struct {
	short *state.RingBuffer // rolling window for current rate
	long  *state.RingBuffer // rolling window for baseline rate
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
			s.short.Push(t.Timestamp, t.Volume)
			s.long.Push(t.Timestamp, t.Volume)

			shortVol := s.short.SumSince(t.Timestamp, d.shortWindow)
			longVol := s.long.SumSince(t.Timestamp, d.longWindow)

			if longVol == 0 {
				return // not enough history yet
			}

			// Compare per-second rates to normalize the different window lengths.
			shortRate := shortVol / d.shortWindow.Seconds()
			longRate := longVol / d.longWindow.Seconds()
			threshold := longRate * d.multiplier

			if shortRate > threshold {
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

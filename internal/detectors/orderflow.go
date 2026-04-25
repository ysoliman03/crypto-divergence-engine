package detectors

import (
	"context"
	"fmt"
	"time"

	"github.com/youssef/divergence-engine/internal/alert"
	"github.com/youssef/divergence-engine/internal/state"
	"github.com/youssef/divergence-engine/internal/tick"
)

type ofiState struct {
	buys       *state.RingBuffer // aggressive buy volume (side == "buy")
	sells      *state.RingBuffer // aggressive sell volume (side == "sell")
	totalTicks int               // total ticks seen, used for minTicks warmup
	firstSeen  time.Time
	lastAlert  time.Time
}

// OrderFlowImbalanceDetector alerts when aggressive buying or selling dominates
// a rolling window. A ratio above upperThreshold means buyers are consistently
// crossing the spread; below lowerThreshold means sellers are.
type OrderFlowImbalanceDetector struct {
	symbols        state.ShardedMap[*ofiState]
	window         time.Duration
	upperThreshold float64 // ratio above this → buy imbalance
	lowerThreshold float64 // ratio below this → sell imbalance
	minVolume      float64 // ignore windows with too little total volume
	minTicks       int     // minimum ticks before ratio is meaningful (avoids 1-tick false positives)
	minHistory     time.Duration
	cooldown       time.Duration
}

func NewOrderFlowImbalanceDetector() *OrderFlowImbalanceDetector {
	return &OrderFlowImbalanceDetector{
		window:         60 * time.Second,
		upperThreshold: 0.65,
		lowerThreshold: 0.35,
		minVolume:      0.01,
		minTicks:       10,
		minHistory:     60 * time.Second, // one full window before trusting the ratio
		cooldown:       30 * time.Second,
	}
}

func (d *OrderFlowImbalanceDetector) Name() string { return "order_flow_imbalance" }

func (d *OrderFlowImbalanceDetector) OnTick(ctx context.Context, t tick.Tick) []alert.Alert {
	var result []alert.Alert

	d.symbols.Use(t.Symbol,
		func() *ofiState {
			return &ofiState{
				buys:  state.NewRingBuffer(5_000),
				sells: state.NewRingBuffer(5_000),
			}
		},
		func(s *ofiState) {
			if s.firstSeen.IsZero() {
				s.firstSeen = t.Timestamp
			}
			s.totalTicks++

			switch t.Side {
			case "buy":
				s.buys.Push(t.Timestamp, t.Volume)
			case "sell":
				s.sells.Push(t.Timestamp, t.Volume)
			}

			if s.totalTicks < d.minTicks {
				return
			}
			if t.Timestamp.Sub(s.firstSeen) < d.minHistory {
				return
			}

			buyVol := s.buys.SumSince(t.Timestamp, d.window)
			sellVol := s.sells.SumSince(t.Timestamp, d.window)
			total := buyVol + sellVol

			if total < d.minVolume {
				return
			}

			ratio := buyVol / total

			var msg string
			var threshold float64
			switch {
			case ratio >= d.upperThreshold:
				msg = fmt.Sprintf("buy imbalance: %.0f%% of volume is aggressive buying", ratio*100)
				threshold = d.upperThreshold
			case ratio <= d.lowerThreshold:
				msg = fmt.Sprintf("sell imbalance: %.0f%% of volume is aggressive selling", (1-ratio)*100)
				threshold = d.lowerThreshold
			default:
				return
			}

			if t.Timestamp.Sub(s.lastAlert) < d.cooldown {
				return
			}
			s.lastAlert = t.Timestamp

			result = append(result, alert.Alert{
				Detector:  d.Name(),
				Symbol:    t.Symbol,
				Exchange:  t.Exchange,
				Severity:  alert.SeverityWarning,
				Message:   msg,
				Value:     ratio,
				Threshold: threshold,
				Timestamp: t.Timestamp,
			})
		},
	)

	return result
}

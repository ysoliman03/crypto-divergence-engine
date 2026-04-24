package detectors

import (
	"context"

	"github.com/youssef/divergence-engine/internal/alert"
	"github.com/youssef/divergence-engine/internal/tick"
)

// Detector is implemented once per detection algorithm. The worker calls
// OnTick for every tick it receives and collects any alerts produced.
// Implementations must be safe for concurrent use if the worker fans out
// detector calls across goroutines.
type Detector interface {
	Name() string
	OnTick(ctx context.Context, t tick.Tick) []alert.Alert
}

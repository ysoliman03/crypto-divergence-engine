package detectors

import (
	"context"

	"github.com/youssef/divergence-engine/internal/alert"
	"github.com/youssef/divergence-engine/internal/tick"
)

// Registry runs a fixed set of detectors against each tick and collects alerts.
type Registry struct {
	dd []Detector
}

func NewRegistry(dd ...Detector) *Registry {
	return &Registry{dd: dd}
}

func (r *Registry) OnTick(ctx context.Context, t tick.Tick) []alert.Alert {
	var all []alert.Alert
	for _, d := range r.dd {
		all = append(all, d.OnTick(ctx, t)...)
	}
	return all
}

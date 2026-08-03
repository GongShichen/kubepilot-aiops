package injector

import (
	"context"
	"fmt"
	"sync"

	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
)

type Injector interface {
	Preflight(context.Context, scenarios.Scenario) error
	RestoreBaseline(context.Context, scenarios.Scenario) error
	Inject(context.Context, scenarios.Scenario) error
	Cleanup(context.Context, scenarios.Scenario) error
	Healthy(context.Context, scenarios.Scenario) error
}
type Registry struct{ handlers map[string]Injector }

func NewRegistry() *Registry                         { return &Registry{handlers: map[string]Injector{}} }
func (r *Registry) Register(name string, h Injector) { r.handlers[name] = h }
func (r *Registry) Get(name string) (Injector, error) {
	h, ok := r.handlers[name]
	if !ok {
		return nil, fmt.Errorf("injector %q is not registered", name)
	}
	return h, nil
}

type DryRun struct {
	mu     sync.Mutex
	active string
}

func (d *DryRun) Preflight(context.Context, scenarios.Scenario) error { return nil }
func (d *DryRun) RestoreBaseline(context.Context, scenarios.Scenario) error {
	d.mu.Lock()
	d.active = ""
	d.mu.Unlock()
	return nil
}
func (d *DryRun) Inject(_ context.Context, s scenarios.Scenario) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.active != "" {
		return fmt.Errorf("scenario %s is already active", d.active)
	}
	d.active = s.ID
	return nil
}
func (d *DryRun) Cleanup(_ context.Context, s scenarios.Scenario) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.active == s.ID {
		d.active = ""
	}
	return nil
}
func (d *DryRun) Healthy(context.Context, scenarios.Scenario) error { return nil }

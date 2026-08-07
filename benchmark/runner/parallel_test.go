package runner

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/injector"
	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type concurrencyProbe struct {
	mu      sync.Mutex
	active  int
	maximum int
	seen    map[string]bool
}

type parallelInjector struct {
	probe *concurrencyProbe
}

func (*parallelInjector) Preflight(context.Context, scenarios.Scenario) error       { return nil }
func (*parallelInjector) RestoreBaseline(context.Context, scenarios.Scenario) error { return nil }
func (*parallelInjector) Cleanup(context.Context, scenarios.Scenario) error         { return nil }
func (*parallelInjector) Healthy(context.Context, scenarios.Scenario) error         { return nil }
func (i *parallelInjector) Inject(ctx context.Context, scenario scenarios.Scenario) error {
	i.probe.mu.Lock()
	i.probe.active++
	if i.probe.active > i.probe.maximum {
		i.probe.maximum = i.probe.active
	}
	i.probe.seen[scenario.Namespace] = true
	i.probe.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(15 * time.Millisecond):
	}
	i.probe.mu.Lock()
	i.probe.active--
	i.probe.mu.Unlock()
	return ctx.Err()
}

type parallelClient struct{}

func (*parallelClient) Create(_ context.Context, scenario scenarios.Scenario) (*domain.Incident, error) {
	return &domain.Incident{ID: scenario.ID, Status: domain.StatusNeedsAttention, Namespace: scenario.Namespace, Investigation: completedParallelInvestigation()}, nil
}
func (*parallelClient) Get(_ context.Context, id string) (*domain.Incident, error) {
	return &domain.Incident{ID: id, Status: domain.StatusNeedsAttention, Investigation: completedParallelInvestigation()}, nil
}
func (*parallelClient) Approve(context.Context, *domain.Incident) error { return nil }

func completedParallelInvestigation() *domain.Investigation {
	return &domain.Investigation{Architecture: "test-runtime", CompletedAt: time.Now().UTC()}
}

func TestParallelRunnerUsesStableIsolatedShardsAndGlobalGate(t *testing.T) {
	probe := &concurrencyProbe{seen: map[string]bool{}}
	gate, err := NewConcurrencyGate(2)
	if err != nil {
		t.Fatal(err)
	}
	workers := make([]ParallelWorker, 4)
	for index := range workers {
		registry := injector.NewRegistry()
		registry.Register("service_fault", &parallelInjector{probe: probe})
		namespace := fmt.Sprintf("kubepilot-benchmark-worker-%02d", index+1)
		workers[index] = ParallelWorker{
			ID:        fmt.Sprintf("worker-%02d", index+1),
			Namespace: namespace,
			Runner:    &Runner{Registry: registry, Client: &parallelClient{}, Gate: gate, PollInterval: time.Millisecond},
		}
	}
	items := make([]scenarios.Scenario, 24)
	for index := range items {
		items[index] = scenarios.Scenario{ID: fmt.Sprintf("case-%02d", index), Seed: int64(100 + index), Repetition: 1, Injector: "service_fault", Timeouts: scenarios.Timeouts{FaultVisible: time.Second}}
	}
	results, err := (ParallelRunner{Workers: workers}).Run(context.Background(), items)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(items) {
		t.Fatalf("results=%d, want %d", len(results), len(items))
	}
	for index, result := range results {
		if result.CaseID != items[index].ID {
			t.Fatalf("result order changed at %d: %s", index, result.CaseID)
		}
		workerIndex := StableWorkerIndex(items[index], len(workers))
		if result.Namespace != workers[workerIndex].Namespace || result.WorkerID != workers[workerIndex].ID {
			t.Fatalf("case %s assigned to worker=%s namespace=%s", result.CaseID, result.WorkerID, result.Namespace)
		}
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.maximum != 2 {
		t.Fatalf("maximum concurrency=%d, want global gate limit 2", probe.maximum)
	}
	if len(probe.seen) < 2 {
		t.Fatalf("cases did not use isolated namespaces: %#v", probe.seen)
	}
}

func TestStableWorkerIndexIgnoresDiagnosisStrategy(t *testing.T) {
	item := scenarios.Scenario{ID: "payment-memory-leak", Seed: 20260805, Repetition: 2}
	want := StableWorkerIndex(item, 4)
	for _, strategy := range []string{"direct", "rag", "react", "kubepilot"} {
		copy := item
		copy.Description = strategy
		if got := StableWorkerIndex(copy, 4); got != want {
			t.Fatalf("strategy %s changed paired shard: got %d want %d", strategy, got, want)
		}
	}
}

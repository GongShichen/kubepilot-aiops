package runner

import (
	"context"
	"errors"
	"github.com/kubepilot-aiops/kubepilot/benchmark/injector"
	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"testing"
	"time"
)

type fakeClient struct{ in *domain.Incident }

func (f *fakeClient) Create(context.Context, scenarios.Scenario) (*domain.Incident, error) {
	return f.in, nil
}
func (f *fakeClient) Get(context.Context, string) (*domain.Incident, error) { return f.in, nil }
func (f *fakeClient) Approve(context.Context, *domain.Incident) error       { return nil }
func TestRunner(t *testing.T) {
	reg := injector.NewRegistry()
	dry := &injector.DryRun{}
	reg.Register("service_fault", dry)
	s := scenarios.Scenario{ID: "x", Category: "cpu", Variant: "busy_loop", Service: "payment-service", Target: "payment-service", Namespace: "kubepilot-benchmark", Injector: "service_fault", Timeouts: scenarios.Timeouts{FaultVisible: time.Second, Diagnosis: time.Second, Recovery: time.Second}, GroundTruth: scenarios.GroundTruth{RootCauseCategory: "cpu", Service: "payment-service", Resource: "payment-service", RequiredEvidence: []string{"cpu"}}}
	client := &fakeClient{in: &domain.Incident{ID: "i", Status: domain.StatusNeedsAttention, RootCauseCategory: "cpu", RootCauseVariant: "busy_loop", RootCauseService: "payment-service", RootCauseResource: "payment-service", Service: "payment-service", Resource: "payment-service", RootCauseEvidenceIDs: []string{"e1"}, Evidence: []domain.Evidence{{ID: "e1", Kind: "cpu"}}}}
	items := (&Runner{Registry: reg, Client: client, PollInterval: time.Millisecond}).Run(context.Background(), []scenarios.Scenario{s})
	if len(items) != 1 || items[0].Status != "passed" {
		t.Fatalf("%#v", items)
	}
}

type cleanupContextInjector struct {
	cleanupContextUsable bool
}

func (f *cleanupContextInjector) Preflight(context.Context, scenarios.Scenario) error { return nil }
func (f *cleanupContextInjector) Inject(context.Context, scenarios.Scenario) error    { return nil }
func (f *cleanupContextInjector) Cleanup(context.Context, scenarios.Scenario) error   { return nil }
func (f *cleanupContextInjector) Healthy(context.Context, scenarios.Scenario) error   { return nil }
func (f *cleanupContextInjector) RestoreBaseline(ctx context.Context, _ scenarios.Scenario) error {
	f.cleanupContextUsable = ctx.Err() == nil
	return nil
}

type timeoutClient struct{}

func (*timeoutClient) Create(ctx context.Context, _ scenarios.Scenario) (*domain.Incident, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*timeoutClient) Get(context.Context, string) (*domain.Incident, error) {
	return nil, errors.New("unexpected get")
}
func (*timeoutClient) Approve(context.Context, *domain.Incident) error { return nil }

func TestRunnerCleanupUsesFreshContextAfterCaseTimeout(t *testing.T) {
	reg := injector.NewRegistry()
	probe := &cleanupContextInjector{}
	reg.Register("service_fault", probe)
	s := scenarios.Scenario{
		ID: "timeout", Namespace: "kubepilot-benchmark", Service: "gateway-service",
		Target: "gateway-service", Injector: "service_fault",
		Timeouts: scenarios.Timeouts{FaultVisible: time.Nanosecond},
	}
	result := (&Runner{Registry: reg, Client: &timeoutClient{}}).Run(context.Background(), []scenarios.Scenario{s})
	if len(result) != 1 {
		t.Fatalf("expected one result, got %d", len(result))
	}
	if !probe.cleanupContextUsable {
		t.Fatal("cleanup inherited an expired case context")
	}
	if result[0].Status == "cleanup_failed" {
		t.Fatalf("cleanup should succeed with a fresh context: %#v", result[0])
	}
}

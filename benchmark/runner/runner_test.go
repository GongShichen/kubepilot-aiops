package runner

import (
	"context"
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

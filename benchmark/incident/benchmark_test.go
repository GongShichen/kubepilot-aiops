package incident

import (
	"context"
	"github.com/kubepilot-aiops/kubepilot/benchmark/evaluator"
	"testing"
	"time"
)

type fakeAgent struct {
	obs      Observation
	input    Input
	approved bool
}

type fakeInjector struct{ prepared, injected, cleaned, healthy int }

func (f *fakeInjector) Prepare(context.Context, Case) error { f.prepared++; return nil }
func (f *fakeInjector) Inject(context.Context, Case) error  { f.injected++; return nil }
func (f *fakeInjector) Cleanup(context.Context, Case) error { f.cleaned++; return nil }
func (f *fakeInjector) Healthy(context.Context, Case) error { f.healthy++; return nil }

type fakeKnowledge struct {
	observed int
	last     Observation
}

func (f *fakeKnowledge) Isolated() bool { return true }

type unsafeKnowledge struct{}

func (unsafeKnowledge) ObserveResolved(context.Context, Observation) error { return nil }

func (f *fakeKnowledge) ObserveResolved(_ context.Context, o Observation) error {
	f.observed++
	f.last = o
	return nil
}

func (f *fakeAgent) Create(_ context.Context, in Input) (string, error) {
	f.input = in
	now := time.Now()
	f.obs = Observation{ID: "i", Status: StatusAwaitingApproval, CreatedAt: now, DiagnosedAt: now, RootCause: "memory_leak", Category: "memory", Service: "payment-service", Resource: "payment-service", EvidenceIDs: []string{"e1"}}
	return "i", nil
}
func (f *fakeAgent) Get(_ context.Context, _ string) (Observation, error) {
	if f.approved {
		f.obs.Status = StatusResolved
		f.obs.VerificationOK = true
		f.obs.RecoveryAction = "restart_pod"
		f.obs.RecoveryTarget = "payment-service"
	}
	return f.obs, nil
}
func (f *fakeAgent) Approve(_ context.Context, _ string) error { f.approved = true; return nil }
func TestRunnerKeepsExpectedLabelsOutOfAgentInput(t *testing.T) {
	f := &fakeAgent{}
	c := Case{ID: "c1", Category: "memory", Input: Input{Namespace: "kubepilot-demo", Service: "payment-service", Summary: "memory anomaly"}, Expected: evaluator.Expected{RootCause: "memory_leak", Category: "memory", Service: "payment-service", Resource: "payment-service", EvidenceIDs: []string{"e1"}, RecoveryAction: "restart_pod", RecoveryTarget: "payment-service"}}
	r := Runner{Agent: f, Config: Config{AutoApprove: true, PollInterval: time.Millisecond}}
	got := r.Run(context.Background(), []Case{c})
	if len(got) != 1 || got[0].Status != "passed" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if f.input.Summary != "memory anomaly" {
		t.Fatalf("unexpected input: %+v", f.input)
	}
	if err := ValidateInput(f.input); err != nil {
		t.Fatal(err)
	}
}

func TestFullPipelineUsesObservedKnowledgeOnly(t *testing.T) {
	f := &fakeAgent{}
	injector := &fakeInjector{}
	knowledge := &fakeKnowledge{}
	c := Case{ID: "c1", Category: "memory", Input: Input{Namespace: "kubepilot-demo", Service: "payment-service", Summary: "memory anomaly"}, Expected: evaluator.Expected{RootCause: "memory_leak", Category: "memory", Service: "payment-service", Resource: "payment-service", EvidenceIDs: []string{"e1"}, RecoveryAction: "restart_pod", RecoveryTarget: "payment-service"}}
	r := Runner{Agent: f, Config: Config{AutoApprove: true, PollInterval: time.Millisecond, FaultInjector: injector, Knowledge: knowledge}}
	results := r.Run(context.Background(), []Case{c})
	if len(results) != 1 || results[0].Status != "passed" {
		t.Fatalf("pipeline failed: %+v", results)
	}
	if injector.prepared != 1 || injector.injected != 1 || injector.cleaned != 1 || injector.healthy == 0 {
		t.Fatalf("fault lifecycle not completed: %+v", injector)
	}
	if knowledge.observed != 1 || knowledge.last.RootCause != "memory_leak" {
		t.Fatalf("knowledge did not receive observed result: %+v", knowledge)
	}
	if got := Summarize(results); got.RootCauseAccuracy != 1 || got.VerificationSuccess != 1 {
		t.Fatalf("unexpected summary: %+v", got)
	}
}

func TestRunnerRejectsUnisolatedKnowledgeSink(t *testing.T) {
	f := &fakeAgent{}
	c := Case{ID: "c1", Input: Input{Summary: "observation"}, Expected: evaluator.Expected{RootCause: "memory_leak"}}
	r := Runner{Agent: f, Config: Config{AutoApprove: true, PollInterval: time.Millisecond, Knowledge: unsafeKnowledge{}}}
	results := r.Run(context.Background(), []Case{c})
	if len(results) != 1 || results[0].Error == "" || results[0].Status == "passed" {
		t.Fatalf("unisolated sink was accepted: %+v", results)
	}
}

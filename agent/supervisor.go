package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

type Supervisor struct {
	runnable compose.Runnable[*WorkflowState, *WorkflowState]
}
type SupervisorDeps struct {
	Collectors map[string]Collector
	Historical Collector
	Diagnosis  *DiagnosisAgent
	Recovery   *RecoveryAgent
}

func NewSupervisor(ctx context.Context, deps SupervisorDeps) (*Supervisor, error) {
	g := compose.NewGraph[*WorkflowState, *WorkflowState]()
	add := func(name string, fn func(context.Context, *WorkflowState) (*WorkflowState, error)) error {
		wrapped := func(ctx context.Context, state *WorkflowState) (*WorkflowState, error) {
			ctx, span := otel.Tracer("kubepilot/eino").Start(ctx, name)
			defer span.End()
			out, err := fn(ctx, state)
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			}
			return out, err
		}
		return g.AddLambdaNode(name, compose.InvokableLambda(wrapped))
	}
	if err := add("incident_intake", passStatus(domain.StatusCorrelating)); err != nil {
		return nil, err
	}
	if err := add("alert_correlator", passStatus(domain.StatusCollecting)); err != nil {
		return nil, err
	}
	if err := add("evidence_planner", func(_ context.Context, s *WorkflowState) (*WorkflowState, error) { return s, nil }); err != nil {
		return nil, err
	}
	if err := add("evidence_collection", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		if diagnosisMethod(s.Incident) != domain.DiagnosisMethodKubePilot {
			s.Incident.Evidence = append(s.Incident.Evidence, domain.Evidence{
				ID: "incident-context-" + s.Incident.ID, Source: "incident", Kind: "incident_context",
				Summary:    fmt.Sprintf("severity=%s service=%s namespace=%s resource=%s summary=%s", s.Incident.Severity, s.Incident.Service, s.Incident.Namespace, s.Incident.Resource, s.Incident.Summary),
				ObservedAt: time.Now().UTC(),
			})
			s.Incident.Status = domain.StatusDiagnosing
			return s, nil
		}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for name, c := range deps.Collectors {
			name, c := name, c
			wg.Add(1)
			go func() {
				defer wg.Done()
				ev, err := c.Collect(ctx, s.Incident)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					s.Errors = append(s.Errors, name+": "+err.Error())
					return
				}
				s.Incident.Evidence = append(s.Incident.Evidence, ev...)
			}()
		}
		wg.Wait()
		s.Incident.Status = domain.StatusDiagnosing
		return s, nil
	}); err != nil {
		return nil, err
	}
	if err := add("evidence_merger", func(_ context.Context, s *WorkflowState) (*WorkflowState, error) {
		if len(s.Incident.Evidence) == 0 {
			return s, fmt.Errorf("no evidence collected")
		}
		return s, nil
	}); err != nil {
		return nil, err
	}
	if err := add("historical_retriever", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		if diagnosisMethod(s.Incident) == domain.DiagnosisMethodLLMOnly || deps.Historical == nil {
			return s, nil
		}
		ev, err := deps.Historical.Collect(ctx, s.Incident)
		if err != nil {
			s.Errors = append(s.Errors, "historical: "+err.Error())
			return s, nil
		}
		s.Incident.Evidence = append(s.Incident.Evidence, ev...)
		return s, nil
	}); err != nil {
		return nil, err
	}
	if err := add("hypothesis_generator", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		if deps.Diagnosis == nil {
			return s, fmt.Errorf("diagnosis model unavailable")
		}
		if err := deps.Diagnosis.Run(ctx, s.Incident); err != nil {
			return s, err
		}
		return s, nil
	}); err != nil {
		return nil, err
	}
	if err := add("hypothesis_verifier", func(_ context.Context, s *WorkflowState) (*WorkflowState, error) {
		if s.Incident.Confidence < 0.8 {
			s.Incident.Status = domain.StatusNeedsAttention
			return s, nil
		}
		return s, nil
	}); err != nil {
		return nil, err
	}
	if err := add("root_cause_agent", func(_ context.Context, s *WorkflowState) (*WorkflowState, error) { return s, nil }); err != nil {
		return nil, err
	}
	if err := add("recovery_agent", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		if s.Incident.Status == domain.StatusNeedsAttention {
			return s, nil
		}
		if err := deps.Recovery.Propose(ctx, s.Incident); err != nil {
			return s, err
		}
		s.Incident.Status = domain.StatusAwaitingApproval
		s.Incident.UpdatedAt = time.Now().UTC()
		return s, nil
	}); err != nil {
		return nil, err
	}
	if err := add("incident_finalizer", func(_ context.Context, s *WorkflowState) (*WorkflowState, error) { return s, nil }); err != nil {
		return nil, err
	}
	if err := add("approval_interrupt", func(_ context.Context, s *WorkflowState) (*WorkflowState, error) { return s, nil }); err != nil {
		return nil, err
	}
	if err := add("action_executor", func(_ context.Context, s *WorkflowState) (*WorkflowState, error) { return s, nil }); err != nil {
		return nil, err
	}
	if err := add("verification_agent", func(_ context.Context, s *WorkflowState) (*WorkflowState, error) { return s, nil }); err != nil {
		return nil, err
	}
	nodes := []string{"incident_intake", "alert_correlator", "evidence_planner", "evidence_collection", "evidence_merger", "historical_retriever", "hypothesis_generator", "hypothesis_verifier", "root_cause_agent", "recovery_agent", "approval_interrupt", "action_executor", "verification_agent", "incident_finalizer"}
	if err := g.AddEdge(compose.START, nodes[0]); err != nil {
		return nil, err
	}
	for i := 0; i < len(nodes)-1; i++ {
		if err := g.AddEdge(nodes[i], nodes[i+1]); err != nil {
			return nil, err
		}
	}
	if err := g.AddEdge(nodes[len(nodes)-1], compose.END); err != nil {
		return nil, err
	}
	run, err := g.Compile(ctx, compose.WithGraphName("kubepilot-supervisor"))
	if err != nil {
		return nil, err
	}
	return &Supervisor{runnable: run}, nil
}

func diagnosisMethod(incident *domain.Incident) string {
	if incident.DiagnosisMethod == "" {
		return domain.DiagnosisMethodKubePilot
	}
	return incident.DiagnosisMethod
}
func passStatus(status domain.IncidentStatus) func(context.Context, *WorkflowState) (*WorkflowState, error) {
	return func(_ context.Context, s *WorkflowState) (*WorkflowState, error) {
		s.Incident.Status = status
		s.Incident.UpdatedAt = time.Now().UTC()
		return s, nil
	}
}
func (s *Supervisor) Run(ctx context.Context, in *domain.Incident) (*WorkflowState, error) {
	return s.runnable.Invoke(ctx, &WorkflowState{Incident: in})
}

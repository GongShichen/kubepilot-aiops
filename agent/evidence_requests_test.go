package agent

import (
	"reflect"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestEvidenceRequestValidationNormalizesScopeWindowAndBoundaries(t *testing.T) {
	if _, err := validateEvidenceRequest(nil, domain.EvidenceRequest{}, "metric", nil); err == nil {
		t.Fatal("nil incident was accepted")
	}
	incident := &domain.Incident{Namespace: "team-a", Service: "checkout", Resource: "checkout"}
	request := domain.EvidenceRequest{Source: "log", Targets: []domain.ResourceRef{{Service: "checkout"}}}
	if _, err := validateEvidenceRequest(incident, request, "metric", nil); err == nil {
		t.Fatal("mismatched collector source was accepted")
	}
	request.Source = "metric"
	validated, err := validateEvidenceRequest(incident, request, "metric", map[string]bool{"checkout": true})
	if err != nil {
		t.Fatal(err)
	}
	if validated.Targets[0].Namespace != "team-a" || !validated.WindowStart.Before(validated.WindowEnd) {
		t.Fatalf("scope/window were not normalized: %+v", validated)
	}
	copy := requestTargetIncident(incident, validated)
	if copy.Namespace != "team-a" || copy.Service != "checkout" || copy.EvidenceStartAt.IsZero() {
		t.Fatalf("target incident was not scoped: %+v", copy)
	}
	disallowed := validated
	disallowed.Targets = []domain.ResourceRef{{Namespace: "team-a", Service: "database", Resource: "database"}}
	if _, err = validateEvidenceRequest(incident, disallowed, "metric", map[string]bool{"checkout": true}); err == nil {
		t.Fatal("non-neighbor target was accepted")
	}
	if got := stringSlice([]any{"redis", 1, "mysql"}); len(got) != 2 || got[0] != "redis" || got[1] != "mysql" {
		t.Fatalf("generic dependency list was not projected: %v", got)
	}
	if got := stringSlice(time.Now()); got != nil {
		t.Fatalf("unsupported dependency value was projected: %v", got)
	}
}

func TestDependencyInvestigationTargetsDiscoveredOneHopServices(t *testing.T) {
	incident := &domain.Incident{Namespace: "team-a", Service: "checkout", Resource: "checkout", CreatedAt: time.Now().Add(-time.Minute)}
	evidence := []domain.Evidence{{
		Source: "kubernetes", Facts: map[string]any{"discovered_dependencies": []any{"redis", "payment"}},
	}}
	policies := []domain.InvestigationPolicy{{ObservationKind: "dependency_availability"}}
	requests := evidenceRequestsForPolicies(incident, policies, evidence)
	if len(requests) != 4 { // metric + topology for each discovered dependency
		t.Fatalf("dependency policy did not compile one-hop requests: %+v", requests)
	}
	for _, request := range requests {
		if len(request.Targets) != 1 || request.Targets[0].Service == "checkout" || request.Targets[0].Namespace != "team-a" {
			t.Fatalf("dependency request escaped one-hop scope: %+v", request)
		}
		if err := func() error {
			_, err := validateEvidenceRequest(incident, request, request.Source, allowedEvidenceTargets(incident, evidence))
			return err
		}(); err != nil {
			t.Fatalf("server-compiled one-hop request was rejected: %v", err)
		}
	}
}

func TestServerDependencyExplorationIsBoundedToObservedOneHopNeed(t *testing.T) {
	incident := &domain.Incident{Namespace: "team-a", Service: "checkout", Resource: "checkout", CreatedAt: time.Now().Add(-time.Minute)}
	evidence := []domain.Evidence{{Source: "kubernetes", Facts: map[string]any{"discovered_dependencies": []string{"redis", "payment"}}}}
	assertions := []domain.StateAssertion{{Property: "application_errors", State: "abnormal", Status: domain.StateAssertionActive}}
	requests := serverDependencyExplorationRequests(incident, evidence, assertions)
	if len(requests) != 2 {
		t.Fatalf("bounded dependency fallback did not create topology requests: %+v", requests)
	}
	for _, request := range requests {
		if request.Source != "topology" || len(request.Targets) != 1 || request.Targets[0].Service == "checkout" || !reflect.DeepEqual(request.SignalKinds, []string{"dependency_availability"}) {
			t.Fatalf("dependency fallback escaped its server boundary: %+v", request)
		}
	}
	if got := serverDependencyExplorationRequests(incident, evidence, []domain.StateAssertion{{Property: "request_latency", State: "normal", Status: domain.StateAssertionActive}}); len(got) != 0 {
		t.Fatalf("healthy observations triggered dependency exploration: %+v", got)
	}
}

package reasoning

import (
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestBuildStateAssertionsPreservesScopeIndependenceAndLifecycle(t *testing.T) {
	now := time.Now().UTC()
	incident := &domain.Incident{Namespace: "team-a", Service: "checkout", Resource: "checkout"}
	evidence := []domain.Evidence{{
		ID: "metric", Source: "prometheus", Namespace: "team-a", Service: "checkout", Resource: "checkout",
		Signals: []domain.EvidenceSignal{{ID: "cpu", Signal: "cpu_pressure", Direction: "abnormal", Strength: .95, Reliability: .9, Independence: .5, ObservedAt: now}},
	}}
	assertions := BuildStateAssertions(incident, evidence, nil, now)
	if len(assertions) != 1 || assertions[0].Property != "cpu_pressure" || assertions[0].Subject != "checkout" || assertions[0].Status != domain.StateAssertionActive {
		t.Fatalf("state assertion did not preserve server signal scope: %+v", assertions)
	}
	if assertions[0].Confidence != .95*.9*.5 {
		t.Fatalf("signal independence was not reflected in assertion confidence: %+v", assertions[0])
	}
	previous := []domain.StateAssertion{{ID: "old", Subject: "checkout", Property: "memory_pressure", State: "abnormal", Confidence: .8, FirstSeen: now.Add(-20 * time.Minute), LastSeen: now.Add(-20 * time.Minute), Status: domain.StateAssertionActive}}
	stale := BuildStateAssertions(incident, nil, previous, now)
	if len(stale) != 1 || stale[0].Status != domain.StateAssertionStale {
		t.Fatalf("unrefreshed assertion did not become stale: %+v", stale)
	}
	contradicted := BuildStateAssertions(incident, []domain.Evidence{{Signals: []domain.EvidenceSignal{{ID: "normal", Signal: "memory_pressure", Direction: "normal", Strength: 1, Reliability: 1, ObservedAt: now}}}}, previous, now)
	if len(contradicted) != 1 || contradicted[0].Status != domain.StateAssertionContradicted {
		t.Fatalf("normal observation did not contradict active assertion: %+v", contradicted)
	}
}

func TestBuildStateAssertionsDoesNotContradictMixedSignalsInFirstCollectionRound(t *testing.T) {
	now := time.Now().UTC()
	incident := &domain.Incident{Service: "checkout", Resource: "checkout"}
	evidence := []domain.Evidence{{
		ID: "metric", Source: "prometheus", Service: "checkout", Resource: "checkout",
		Signals: []domain.EvidenceSignal{
			{ID: "pressure", Signal: "cpu_pressure", Direction: "abnormal", Strength: .95, Reliability: .95, ObservedAt: now},
			{ID: "healthy-sample", Signal: "cpu_pressure", Direction: "normal", Reliability: .95, ObservedAt: now.Add(time.Millisecond)},
			{ID: "throttling", Signal: "cpu_throttling", Direction: "abnormal", Strength: 1, Reliability: .95, ObservedAt: now.Add(2 * time.Millisecond)},
		},
	}}
	assertions := BuildStateAssertions(incident, evidence, nil, now.Add(time.Second))
	if len(assertions) != 2 {
		t.Fatalf("decisive throttle signal was not retained as an independent assertion: %+v", assertions)
	}
	byProperty := map[string]domain.StateAssertion{}
	for _, assertion := range assertions {
		byProperty[assertion.Property] = assertion
	}
	pressure, throttle := byProperty["cpu_pressure"], byProperty["cpu_throttling"]
	if pressure.State != "abnormal" || pressure.Status != domain.StateAssertionActive || throttle.State != "abnormal" || throttle.Status != domain.StateAssertionActive {
		t.Fatalf("mixed first-round signals incorrectly closed the abnormal state: %+v", assertions)
	}
	if len(pressure.SupportingSignalIDs) != 1 || len(pressure.ContradictingSignalIDs) != 1 || len(throttle.SupportingSignalIDs) != 1 {
		t.Fatalf("signal audit was not retained: pressure=%+v throttle=%+v", pressure, throttle)
	}
}

func TestEndpointUnavailableBuildsDependencyAvailabilityAssertion(t *testing.T) {
	now := time.Now().UTC()
	incident := &domain.Incident{Service: "checkout", Resource: "checkout"}
	assertions := BuildStateAssertions(incident, []domain.Evidence{{
		ID: "dependency-endpoint", Source: "kubernetes", Service: "cache", Resource: "cache",
		Signals: []domain.EvidenceSignal{{
			ID: "endpoint-unavailable", Signal: "endpoint_unavailable", Direction: "abnormal", Strength: 1, Reliability: .95, ObservedAt: now,
		}},
	}}, nil, now)
	if len(assertions) != 1 || assertions[0].Subject != "cache" || assertions[0].Property != "dependency_availability" || assertions[0].State != "abnormal" {
		t.Fatalf("endpoint availability state was not preserved: %+v", assertions)
	}
}

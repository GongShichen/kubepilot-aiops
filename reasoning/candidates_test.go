package reasoning

import (
	"reflect"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestCandidateGenerationDoesNotRefuteMetricChangeWithItsBaseline(t *testing.T) {
	incident := &domain.Incident{Namespace: "team", Service: "checkout", Resource: "checkout"}
	evidence := []domain.Evidence{
		{ID: "baseline", Source: "prometheus", Type: "cpu", Signals: []domain.EvidenceSignal{{ID: "baseline-signal", EvidenceID: "baseline", Signal: "cpu_pressure", Direction: "normal"}}},
		{ID: "change", Source: "prometheus", Type: "cpu_change", CausalNodeIDs: []string{"cpu_demand", "cpu_saturation"}, Signals: []domain.EvidenceSignal{{ID: "change-signal", EvidenceID: "change", Signal: "cpu_pressure", Direction: "abnormal", Strength: 1}}},
		{ID: "throttling", Source: "prometheus", Type: "cpu_throttling", Signals: []domain.EvidenceSignal{{ID: "throttling-signal", EvidenceID: "throttling", Signal: "cpu_throttling", Direction: "normal"}}},
	}
	assertions := []domain.StateAssertion{{
		Subject: "checkout", Property: "cpu_pressure", State: "abnormal", Status: domain.StateAssertionActive,
		SupportingSignalIDs: []string{"change-signal"}, ContradictingSignalIDs: []string{"baseline-signal", "throttling-signal"},
	}}
	candidates := GenerateDeterministicCandidates(incident, assertions, evidence, cpuSaturationPattern())
	if len(candidates) != 1 || candidates[0].Category != "cpu" {
		t.Fatalf("cpu candidate was not generated: %+v", candidates)
	}
	if len(candidates[0].ContradictingEvidenceIDs) != 0 {
		t.Fatalf("metric baseline or unrelated signal incorrectly refuted change candidate: %+v", candidates[0])
	}
}

func TestCandidateGenerationAccumulatesIndependentAssertionsInOneCategory(t *testing.T) {
	incident := &domain.Incident{Namespace: "team", Service: "checkout", Resource: "checkout"}
	evidence := []domain.Evidence{
		{ID: "cpu", Source: "prometheus", Type: "cpu_change", CausalNodeIDs: []string{"cpu_demand", "cpu_saturation"}, Signals: []domain.EvidenceSignal{{ID: "cpu-signal", EvidenceID: "cpu", Signal: "cpu_pressure", Direction: "abnormal", Strength: 1}}},
		{ID: "throttle", Source: "prometheus", Type: "cpu_throttling", CausalNodeIDs: []string{"cpu_saturation"}, Signals: []domain.EvidenceSignal{{ID: "throttle-signal", EvidenceID: "throttle", Signal: "cpu_throttling", Direction: "abnormal", Strength: 1}}},
	}
	assertions := []domain.StateAssertion{
		{Subject: "checkout", Property: "cpu_pressure", State: "abnormal", Status: domain.StateAssertionActive, SupportingSignalIDs: []string{"cpu-signal"}},
		{Subject: "checkout", Property: "cpu_pressure", State: "abnormal", Status: domain.StateAssertionActive, SupportingSignalIDs: []string{"throttle-signal"}},
	}
	candidates := GenerateDeterministicCandidates(incident, assertions, evidence, cpuSaturationPattern())
	if len(candidates) != 1 || !reflect.DeepEqual(candidates[0].SupportingEvidenceIDs, []string{"cpu", "throttle"}) {
		t.Fatalf("same-category support was reset instead of accumulated: %+v", candidates)
	}
}

func TestCandidateGenerationUsesObservedServerPatternPath(t *testing.T) {
	incident := &domain.Incident{Namespace: "team", Service: "checkout", Resource: "checkout"}
	pattern := domain.CausalPattern{
		ID: "resource-pressure", Category: "cpu", Cause: "resource pressure", Status: "active",
		Nodes: []domain.CausalNode{{ID: "demand", Type: "cause"}, {ID: "saturation", Type: "mechanism"}, {ID: "latency", Type: "symptom"}},
		Edges: []domain.CausalEdge{{From: "demand", To: "saturation"}, {From: "saturation", To: "latency"}},
	}
	evidence := []domain.Evidence{{
		ID: "cpu", Source: "prometheus", Type: "cpu", CausalNodeIDs: []string{"obs:cpu", "demand", "saturation", "latency"},
		Signals: []domain.EvidenceSignal{{ID: "cpu-signal", EvidenceID: "cpu", Signal: "cpu_pressure", Direction: "abnormal", Strength: 1}},
	}}
	assertions := []domain.StateAssertion{{Subject: "checkout", Property: "cpu_pressure", State: "abnormal", Status: domain.StateAssertionActive, SupportingSignalIDs: []string{"cpu-signal"}}}
	candidates := GenerateDeterministicCandidates(incident, assertions, evidence, []domain.CausalPattern{pattern})
	if len(candidates) != 1 || !reflect.DeepEqual(candidates[0].ExpectedCausalNodeIDs, []string{"demand", "saturation", "latency"}) {
		t.Fatalf("candidate did not use the server causal graph path: %+v", candidates)
	}
}

func TestCandidateGenerationUsesObservedMechanismToSymptomSubpath(t *testing.T) {
	incident := &domain.Incident{Namespace: "team", Service: "checkout", Resource: "checkout"}
	pattern := domain.CausalPattern{
		ID: "dependency", Category: "database", Cause: "database dependency failure", Status: "active",
		Nodes: []domain.CausalNode{
			{ID: "dependency_fault", Type: "cause", Signals: []string{"connection_pressure"}},
			{ID: "database_error", Type: "mechanism", Signals: []string{"trace_error"}},
			{ID: "dependency_propagation", Type: "mechanism", Signals: []string{"dependency_unavailable"}},
			{ID: "request_failure", Type: "symptom", Signals: []string{"request_latency"}},
		},
		Edges: []domain.CausalEdge{
			{From: "dependency_fault", To: "database_error"},
			{From: "database_error", To: "dependency_propagation"},
			{From: "database_error", To: "request_failure"},
			{From: "dependency_propagation", To: "request_failure"},
		},
	}
	evidence := []domain.Evidence{
		{ID: "db", Source: "loki", CausalNodeIDs: []string{"database_error"}, Signals: []domain.EvidenceSignal{{ID: "db-signal", EvidenceID: "db", Signal: "trace_error", Direction: "abnormal", Strength: 1}}},
		{ID: "request", Source: "jaeger", CausalNodeIDs: []string{"request_failure"}, Signals: []domain.EvidenceSignal{{ID: "request-signal", EvidenceID: "request", Signal: "request_latency", Direction: "abnormal", Strength: 1}}},
	}
	assertions := []domain.StateAssertion{{Subject: "checkout", Property: "connection_pressure", State: "abnormal", Status: domain.StateAssertionActive, SupportingSignalIDs: []string{"db-signal"}}}
	candidates := GenerateDeterministicCandidates(incident, assertions, evidence, []domain.CausalPattern{pattern})
	if len(candidates) != 1 || !reflect.DeepEqual(candidates[0].ExpectedCausalNodeIDs, []string{"database_error", "request_failure"}) {
		t.Fatalf("candidate did not select the complete observed causal subpath: %+v", candidates)
	}
}

func TestCandidateGenerationUsesObservedMechanismLabel(t *testing.T) {
	incident := &domain.Incident{Namespace: "team", Service: "checkout", Resource: "checkout"}
	pattern := domain.CausalPattern{
		ID: "deployment", Category: "deployment", Cause: "generic rollout failure", Status: "active",
		Nodes: []domain.CausalNode{
			{ID: "rollout", Type: "cause", Signals: []string{"deployment_change"}},
			{ID: "pod_failure", Type: "mechanism", Signals: []string{"image_pull_failure"}},
			{ID: "unavailable", Type: "symptom", Signals: []string{"workload_unavailable"}},
		},
		Edges: []domain.CausalEdge{{From: "rollout", To: "pod_failure"}, {From: "pod_failure", To: "unavailable"}},
	}
	evidence := []domain.Evidence{
		{ID: "image", Source: "kubernetes", CausalNodeIDs: []string{"rollout", "pod_failure"}, Signals: []domain.EvidenceSignal{{ID: "image-signal", EvidenceID: "image", Signal: "image_pull_failure", Direction: "abnormal", Strength: 1}}},
		{ID: "availability", Source: "kubernetes", CausalNodeIDs: []string{"unavailable"}, Signals: []domain.EvidenceSignal{{ID: "availability-signal", EvidenceID: "availability", Signal: "workload_unavailable", Direction: "abnormal", Strength: 1}}},
	}
	assertions := []domain.StateAssertion{{Subject: "checkout", Property: "workload_health", State: "abnormal", Status: domain.StateAssertionActive, SupportingSignalIDs: []string{"image-signal"}}}
	candidates := GenerateDeterministicCandidates(incident, assertions, evidence, []domain.CausalPattern{pattern})
	if len(candidates) != 1 || candidates[0].Variant != "image_pull_failure" || candidates[0].Cause != "container image acquisition failure prevents workload startup" {
		t.Fatalf("candidate did not retain its observed mechanism identity: %+v", candidates)
	}
}

func TestCandidatePatternSelectionPrefersObservedDirectMechanism(t *testing.T) {
	incident := &domain.Incident{Namespace: "team", Service: "checkout", Resource: "checkout"}
	patterns := []domain.CausalPattern{
		{
			ID: "memory-oom", Category: "memory", Cause: "memory exhaustion", Status: "active",
			Nodes: []domain.CausalNode{{ID: "growth", Type: "cause", Signals: []string{"memory_growth"}}, {ID: "oom", Type: "mechanism", Signals: []string{"oom_killed"}}, {ID: "restart", Type: "symptom", Signals: []string{"pod_restarts"}}},
			Edges: []domain.CausalEdge{{From: "growth", To: "oom"}, {From: "oom", To: "restart"}},
		},
		{
			ID: "memory-pressure", Category: "memory", Cause: "memory pressure", Status: "active",
			Nodes: []domain.CausalNode{{ID: "growth", Type: "cause", Signals: []string{"memory_growth"}}, {ID: "pressure", Type: "mechanism", Signals: []string{"memory_pressure"}}, {ID: "request", Type: "symptom", Signals: []string{"request_latency"}}},
			Edges: []domain.CausalEdge{{From: "growth", To: "pressure"}, {From: "pressure", To: "request"}},
		},
	}
	evidence := []domain.Evidence{
		{ID: "growth", Source: "prometheus", Signals: []domain.EvidenceSignal{{ID: "growth-signal", EvidenceID: "growth", Signal: "memory_growth", Direction: "abnormal", Strength: .9}}},
		{ID: "pressure", Source: "prometheus", Signals: []domain.EvidenceSignal{{ID: "pressure-signal", EvidenceID: "pressure", Signal: "memory_pressure", Direction: "abnormal", Strength: .9}}},
		{ID: "restart", Source: "kubernetes", Signals: []domain.EvidenceSignal{{ID: "restart-signal", EvidenceID: "restart", Signal: "pod_restarts", Direction: "abnormal", Strength: .9}}},
	}
	assertions := []domain.StateAssertion{{Subject: "checkout", Property: "memory_pressure", State: "abnormal", Status: domain.StateAssertionActive, SupportingSignalIDs: []string{"growth-signal", "pressure-signal", "restart-signal"}}}
	candidates := GenerateDeterministicCandidates(incident, assertions, evidence, patterns)
	if len(candidates) != 1 || candidates[0].Variant != "memory_leak" {
		t.Fatalf("observed memory-pressure mechanism was not preferred over unobserved OOM: %+v", candidates)
	}
}

func TestCandidateMechanismLabelsPreserveSpecificServerSignatures(t *testing.T) {
	incident := &domain.Incident{Namespace: "team", Service: "checkout", Resource: "checkout"}
	patterns := []domain.CausalPattern{
		{ID: "pool", Category: "database", Cause: "database pool issue", Status: "active", Nodes: []domain.CausalNode{{ID: "pool", Type: "mechanism", Signals: []string{"connection_pressure"}}, {ID: "failure", Type: "symptom", Signals: []string{"request_latency"}}}, Edges: []domain.CausalEdge{{From: "pool", To: "failure"}}},
		{ID: "dependency", Category: "dependency", Cause: "upstream unavailable", Status: "active", Nodes: []domain.CausalNode{{ID: "dependency", Type: "mechanism", Signals: []string{"endpoint_unavailable"}}, {ID: "failure", Type: "symptom", Signals: []string{"request_latency"}}}, Edges: []domain.CausalEdge{{From: "dependency", To: "failure"}}},
	}
	evidence := []domain.Evidence{
		{ID: "pool", Source: "loki", Signals: []domain.EvidenceSignal{{ID: "pool-signal", EvidenceID: "pool", Signal: "connection_pressure", Direction: "abnormal", Strength: .95}}},
		{ID: "endpoint", Source: "kubernetes", Signals: []domain.EvidenceSignal{{ID: "endpoint-signal", EvidenceID: "endpoint", Signal: "endpoint_unavailable", Direction: "abnormal", Strength: 1}}},
		{ID: "request", Source: "prometheus", Signals: []domain.EvidenceSignal{{ID: "request-signal", EvidenceID: "request", Signal: "request_latency", Direction: "abnormal", Strength: .9}}},
	}
	assertions := []domain.StateAssertion{
		{Subject: "checkout", Property: "connection_pressure", State: "abnormal", Status: domain.StateAssertionActive, SupportingSignalIDs: []string{"pool-signal"}},
		{Subject: "checkout", Property: "dependency_availability", State: "abnormal", Status: domain.StateAssertionActive, SupportingSignalIDs: []string{"endpoint-signal"}},
	}
	candidates := GenerateDeterministicCandidates(incident, assertions, evidence, patterns)
	variants := map[string]string{}
	for _, candidate := range candidates {
		variants[candidate.Category] = candidate.Variant
	}
	if variants["database"] != "connection_pool_exhaustion" || variants["dependency"] != "dependency_unavailable" {
		t.Fatalf("specific operational signatures were lost: %+v", candidates)
	}
}

func TestCandidateMechanismLabelDoesNotMistakeMixedRequestSamplesForBusyLoop(t *testing.T) {
	pattern := domain.CausalPattern{
		ID: "cpu", Category: "cpu", Cause: "CPU saturation", Status: "active",
		Nodes: []domain.CausalNode{
			{ID: "demand", Type: "cause", Signals: []string{"request_rate"}},
			{ID: "saturation", Type: "mechanism", Signals: []string{"cpu_pressure", "cpu_throttling"}},
		},
	}
	evidence := []domain.Evidence{
		{ID: "throttle", Source: "prometheus", Signals: []domain.EvidenceSignal{{Signal: "cpu_throttling", Direction: "abnormal", Strength: 1}}},
		{ID: "pressure", Source: "prometheus", Signals: []domain.EvidenceSignal{{Signal: "cpu_pressure", Direction: "abnormal", Strength: .2}}},
		{ID: "rate-current", Source: "prometheus", Signals: []domain.EvidenceSignal{{Signal: "request_rate", Direction: "abnormal", Strength: .8}}},
		{ID: "rate-baseline", Source: "prometheus", Signals: []domain.EvidenceSignal{{Signal: "request_rate", Direction: "normal"}}},
	}
	variant, _ := candidateMechanismLabel(pattern, evidence, "cpu_saturation", "fallback")
	if variant != "cpu_quota_pressure" {
		t.Fatalf("mixed request-rate samples misclassified quota pressure: variant=%q", variant)
	}
}

func TestDependencyCandidateRequiresObservedTargetAndNamesIt(t *testing.T) {
	incident := &domain.Incident{Namespace: "team", Service: "checkout", Resource: "checkout"}
	pattern := domain.CausalPattern{
		ID: "dependency", Category: "dependency", Cause: "upstream unavailable", Status: "active",
		Nodes: []domain.CausalNode{
			{ID: "dependency_target", Type: "cause", Signals: []string{"endpoint_unavailable"}},
			{ID: "dependency_endpoint_failure", Type: "mechanism", Signals: []string{"connection_timeout"}},
			{ID: "request_failure", Type: "symptom", Signals: []string{"request_latency"}},
		},
		Edges: []domain.CausalEdge{{From: "dependency_target", To: "dependency_endpoint_failure"}, {From: "dependency_endpoint_failure", To: "request_failure"}},
	}
	callerOnly := []domain.Evidence{
		{ID: "timeout", Source: "loki", Service: "checkout", Signals: []domain.EvidenceSignal{{ID: "timeout-signal", EvidenceID: "timeout", Signal: "connection_timeout", Direction: "abnormal", Strength: 1}}},
		{ID: "latency", Source: "prometheus", Service: "checkout", Signals: []domain.EvidenceSignal{{ID: "latency-signal", EvidenceID: "latency", Signal: "request_latency", Direction: "abnormal", Strength: 1}}},
	}
	if candidates := GenerateDeterministicCandidates(incident, nil, callerOnly, []domain.CausalPattern{pattern}); len(candidates) != 0 {
		t.Fatalf("caller-side timeout alone created an ambiguous dependency candidate: %+v", candidates)
	}
	targeted := append(callerOnly, domain.Evidence{ID: "target", Source: "kubernetes", Service: "cache", Resource: "cache", Signals: []domain.EvidenceSignal{{ID: "target-signal", EvidenceID: "target", Signal: "endpoint_unavailable", Direction: "abnormal", Strength: 1}}})
	candidates := GenerateDeterministicCandidates(incident, nil, targeted, []domain.CausalPattern{pattern})
	if len(candidates) != 1 || candidates[0].Variant != "cache_unavailable" || candidates[0].Cause != "upstream dependency cache has no ready endpoints" {
		t.Fatalf("observed dependency target was not retained in the diagnosis: %+v", candidates)
	}
}

func TestDependencyObservationCoverageUsesEndpointAvailabilityAndImpact(t *testing.T) {
	coverage := observationCoverage(domain.HypothesisDraft{Category: "dependency"}, []domain.StateAssertion{
		{Subject: "cache", Property: "dependency_availability", State: "abnormal", Status: domain.StateAssertionActive},
		{Subject: "checkout", Property: "request_latency", State: "abnormal", Status: domain.StateAssertionActive},
	})
	if coverage != 1 {
		t.Fatalf("dependency observation coverage=%v, want 1", coverage)
	}
}

func TestCandidateAdmissionRejectsDownstreamSharedMechanism(t *testing.T) {
	incident := &domain.Incident{Namespace: "team", Service: "checkout", Resource: "checkout"}
	memoryOOM := domain.CausalPattern{
		ID: "memory-oom", Category: "memory", Cause: "memory exhaustion", Status: "active",
		Nodes: []domain.CausalNode{
			{ID: "memory_growth", Type: "cause", Signals: []string{"memory_growth"}},
			{ID: "oom_kill", Type: "mechanism", Signals: []string{"oom_killed"}},
			{ID: "restart_unavailable", Type: "mechanism", Signals: []string{"workload_unavailable"}},
			{ID: "request_failure", Type: "symptom", Signals: []string{"request_latency"}},
		},
		Edges: []domain.CausalEdge{{From: "memory_growth", To: "oom_kill"}, {From: "oom_kill", To: "restart_unavailable"}, {From: "restart_unavailable", To: "request_failure"}},
	}
	deployment := domain.CausalPattern{
		ID: "deployment", Category: "deployment", Cause: "rollout failure", Status: "active",
		Nodes: []domain.CausalNode{
			{ID: "rollout", Type: "cause", Signals: []string{"deployment_change"}},
			{ID: "pod_failure", Type: "mechanism", Signals: []string{"image_pull_failure"}},
			{ID: "unavailable", Type: "mechanism", Signals: []string{"workload_unavailable"}},
			{ID: "request_failure", Type: "symptom", Signals: []string{"request_latency"}},
		},
		Edges: []domain.CausalEdge{{From: "rollout", To: "pod_failure"}, {From: "pod_failure", To: "unavailable"}, {From: "unavailable", To: "request_failure"}},
	}
	evidence := []domain.Evidence{{
		ID: "workload", Source: "kubernetes", Signals: []domain.EvidenceSignal{
			{ID: "image", EvidenceID: "workload", Signal: "image_pull_failure", Direction: "abnormal", Strength: 1},
			{ID: "unavailable", EvidenceID: "workload", Signal: "workload_unavailable", Direction: "abnormal", Strength: 1},
		},
	}}
	assertions := []domain.StateAssertion{{Subject: "checkout", Property: "workload_health", State: "abnormal", Status: domain.StateAssertionActive, SupportingSignalIDs: []string{"image", "unavailable"}}}
	candidates := GenerateDeterministicCandidates(incident, assertions, evidence, []domain.CausalPattern{memoryOOM, deployment})
	if len(candidates) != 1 || candidates[0].Category != "deployment" || candidates[0].Variant != "image_pull_failure" {
		t.Fatalf("downstream shared mechanism created a false upstream candidate: %+v", candidates)
	}
}

func TestPatternSymptomAloneDoesNotCreateCandidate(t *testing.T) {
	incident := &domain.Incident{Namespace: "team", Service: "checkout", Resource: "checkout"}
	pattern := domain.CausalPattern{
		ID: "dependency", Category: "database", Cause: "dependency failure", Status: "active",
		Nodes: []domain.CausalNode{{ID: "cause", Type: "cause"}, {ID: "failure", Type: "symptom"}},
		Edges: []domain.CausalEdge{{From: "cause", To: "failure"}},
	}
	evidence := []domain.Evidence{{ID: "timeout", Source: "loki", CausalNodeIDs: []string{"obs:timeout", "failure"}}}
	if candidates := GenerateDeterministicCandidates(incident, nil, evidence, []domain.CausalPattern{pattern}); len(candidates) != 0 {
		t.Fatalf("symptom-only evidence created a root-cause candidate: %+v", candidates)
	}
}

func TestCandidateGenerationDoesNotRefuteActiveAssertionWithMixedSnapshotSignals(t *testing.T) {
	incident := &domain.Incident{Namespace: "team", Service: "checkout", Resource: "checkout"}
	evidence := []domain.Evidence{
		{ID: "abnormal", Source: "prometheus", Type: "cpu", CausalNodeIDs: []string{"cpu_demand", "cpu_saturation"}, Signals: []domain.EvidenceSignal{{ID: "abnormal-signal", EvidenceID: "abnormal", Signal: "cpu_pressure", Direction: "abnormal", Strength: 1}}},
		{ID: "normal", Source: "prometheus", Type: "cpu", Signals: []domain.EvidenceSignal{{ID: "normal-signal", EvidenceID: "normal", Signal: "cpu_pressure", Direction: "normal"}}},
	}
	assertions := []domain.StateAssertion{{
		Subject: "checkout", Property: "cpu_pressure", State: "abnormal", Status: domain.StateAssertionActive,
		SupportingSignalIDs: []string{"abnormal-signal"}, ContradictingSignalIDs: []string{"normal-signal"},
	}}
	candidates := GenerateDeterministicCandidates(incident, assertions, evidence, cpuSaturationPattern())
	if len(candidates) != 1 || len(candidates[0].ContradictingEvidenceIDs) != 0 {
		t.Fatalf("mixed same-round samples incorrectly refuted active assertion: %+v", candidates)
	}
}

func TestCandidateGenerationRejectsUnpatternedAssertion(t *testing.T) {
	incident := &domain.Incident{Namespace: "team", Service: "checkout", Resource: "checkout"}
	evidence := []domain.Evidence{{
		ID: "embedded-timeout", Source: "kubernetes", Type: "workload_state",
		Signals: []domain.EvidenceSignal{{ID: "connection", EvidenceID: "embedded-timeout", Signal: "connection_pressure", Direction: "abnormal", Strength: 1}},
	}}
	assertions := []domain.StateAssertion{{
		Subject: "checkout", Property: "connection_pressure", State: "abnormal", Status: domain.StateAssertionActive,
		SupportingSignalIDs: []string{"connection"},
	}}
	if candidates := GenerateDeterministicCandidates(incident, assertions, evidence, cpuSaturationPattern()); len(candidates) != 0 {
		t.Fatalf("unpatterned assertion created an executable candidate: %+v", candidates)
	}
}

func TestCandidateAdmissionRequiresServerObservedRolloutAndApplicationFailure(t *testing.T) {
	incident := &domain.Incident{Namespace: "team", Service: "checkout", Resource: "checkout"}
	pattern := domain.CausalPattern{
		ID: "deployment-app-regression", Category: "deployment", Cause: "rollout application regression", Status: "active",
		RequiredAdmissionNodeIDs: []string{"rollout", "application_failure"},
		Nodes: []domain.CausalNode{
			{ID: "rollout", Type: "cause", Signals: []string{"deployment_change"}},
			{ID: "application_failure", Type: "mechanism", Signals: []string{"trace_error"}},
			{ID: "request_failure", Type: "symptom", Signals: []string{"request_latency"}},
		},
		Edges: []domain.CausalEdge{{From: "rollout", To: "application_failure"}, {From: "application_failure", To: "request_failure"}},
	}
	appOnly := []domain.Evidence{{ID: "app", Source: "jaeger", Signals: []domain.EvidenceSignal{{ID: "app-signal", EvidenceID: "app", Signal: "trace_error", Direction: "abnormal", Strength: 1}}}}
	if candidates := GenerateDeterministicCandidates(incident, nil, appOnly, []domain.CausalPattern{pattern}); len(candidates) != 0 {
		t.Fatalf("application failure alone admitted a rollout root cause: %+v", candidates)
	}
	evidence := append(appOnly,
		domain.Evidence{ID: "rollout", Source: "kubernetes", Signals: []domain.EvidenceSignal{{ID: "rollout-signal", EvidenceID: "rollout", Signal: "deployment_change", Direction: "observed"}}},
		domain.Evidence{ID: "latency", Source: "prometheus", Signals: []domain.EvidenceSignal{{ID: "latency-signal", EvidenceID: "latency", Signal: "request_latency", Direction: "abnormal", Strength: 1}}},
	)
	candidates := GenerateDeterministicCandidates(incident, nil, evidence, []domain.CausalPattern{pattern})
	if len(candidates) != 1 || !reflect.DeepEqual(candidates[0].ExpectedCausalNodeIDs, []string{"rollout", "application_failure", "request_failure"}) {
		t.Fatalf("paired rollout/app failure did not form causal path: %+v", candidates)
	}
}

func TestDatabaseEndpointCandidateKeepsObservedTargetIdentity(t *testing.T) {
	incident := &domain.Incident{Namespace: "team", Service: "checkout", Resource: "checkout"}
	pattern := domain.CausalPattern{
		ID: "database-endpoint", Category: "database", Cause: "database endpoint unavailable", Status: "active",
		RequiredAdmissionNodeIDs: []string{"endpoint", "connection"},
		Nodes: []domain.CausalNode{
			{ID: "endpoint", Type: "cause", Signals: []string{"database_endpoint_unavailable"}},
			{ID: "connection", Type: "mechanism", Signals: []string{"connection_timeout"}},
			{ID: "request", Type: "symptom", Signals: []string{"request_latency"}},
		},
		Edges: []domain.CausalEdge{{From: "endpoint", To: "connection"}, {From: "connection", To: "request"}},
	}
	evidence := []domain.Evidence{
		{ID: "database", Source: "kubernetes", Service: "accounts-db", Signals: []domain.EvidenceSignal{{ID: "endpoint-signal", EvidenceID: "database", Signal: "database_endpoint_unavailable", Direction: "abnormal", Strength: 1}}},
		{ID: "timeout", Source: "loki", Service: "checkout", Signals: []domain.EvidenceSignal{{ID: "timeout-signal", EvidenceID: "timeout", Signal: "connection_timeout", Direction: "abnormal", Strength: 1}}},
		{ID: "latency", Source: "prometheus", Service: "checkout", Signals: []domain.EvidenceSignal{{ID: "latency-signal", EvidenceID: "latency", Signal: "request_latency", Direction: "abnormal", Strength: 1}}},
	}
	candidates := GenerateDeterministicCandidates(incident, nil, evidence, []domain.CausalPattern{pattern})
	if len(candidates) != 1 || candidates[0].Variant != "accounts_db_unavailable" {
		t.Fatalf("database target identity was not retained: %+v", candidates)
	}
}

func cpuSaturationPattern() []domain.CausalPattern {
	return []domain.CausalPattern{{
		ID: "cpu-saturation", Category: "cpu", Cause: "container CPU saturation", Status: "active",
		Nodes: []domain.CausalNode{{ID: "cpu_demand", Type: "cause"}, {ID: "cpu_saturation", Type: "mechanism"}, {ID: "latency_error", Type: "symptom"}},
		Edges: []domain.CausalEdge{{From: "cpu_demand", To: "cpu_saturation"}, {From: "cpu_saturation", To: "latency_error"}},
	}}
}

package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type cognitiveDiagnosisMode struct {
	RuleOnly  bool
	Cognitive bool
	Active    bool
}

func diagnosisArchitecture(method string) string {
	normalized, ok := domain.NormalizeDiagnosisMethod(method)
	if !ok {
		return domain.WorkflowRuntimeName
	}
	switch normalized {
	case domain.DiagnosisMethodRuleOnly:
		return "eino-rule-diagnosis-runtime"
	case domain.DiagnosisMethodEvidence:
		return "eino-evidence-diagnosis-runtime"
	default:
		return domain.WorkflowRuntimeName
	}
}

func serverInvestigationPlan(incident *domain.Incident) domain.InvestigationPlan {
	plan := domain.InvestigationPlan{
		Objective:      "collect current server-owned evidence and deterministically test bounded incident candidates",
		StopConditions: []string{"no positive diagnostic value", "duplicate request", "no new evidence", "collector unavailable"},
		RoundLimit:     2, CreatedAt: time.Now().UTC(),
	}
	for _, source := range []string{"metric", "log", "trace", "topology"} {
		plan.Tasks = append(plan.Tasks, domain.WorkerTask{ID: "server-" + source, Source: source, Question: "server compiled current incident collection", Required: true, Request: defaultEvidenceRequest(incident, source)})
	}
	return plan
}

func uniqueEvidenceRequests(requests []domain.EvidenceRequest, seen map[string]bool) []domain.EvidenceRequest {
	var out []domain.EvidenceRequest
	for _, request := range requests {
		fingerprint := evidenceRequestFingerprint(request)
		if seen[fingerprint] {
			continue
		}
		seen[fingerprint] = true
		out = append(out, request)
	}
	return out
}

func collectEvidenceRequests(ctx context.Context, incident *domain.Incident, collectors map[string]Collector, requests []domain.EvidenceRequest, allowed map[string]bool) ([]domain.Evidence, []string) {
	type result struct {
		evidence []domain.Evidence
		err      error
		source   string
	}
	results := make(chan result, len(requests))
	var group sync.WaitGroup
	for _, request := range requests {
		source := canonicalWorkerSource(request.Source)
		collector := collectors[source]
		if collector == nil && source == "topology" {
			collector = collectors["kubernetes"]
		}
		if collector == nil {
			results <- result{source: source, err: fmt.Errorf("collector unavailable")}
			continue
		}
		request := request
		group.Add(1)
		go func() {
			defer group.Done()
			validated, err := validateEvidenceRequest(incident, request, source, allowed)
			if err != nil {
				results <- result{source: source, err: err}
				return
			}
			items, err := collector.Collect(ctx, requestTargetIncident(incident, validated), validated)
			results <- result{source: source, evidence: items, err: err}
		}()
	}
	group.Wait()
	close(results)
	var evidence []domain.Evidence
	var infrastructure []string
	for result := range results {
		if result.err != nil {
			// Preserve the server-side collection failure in the investigation
			// audit. Collapsing every error into an opaque source label makes a
			// missing required modality indistinguishable from an empty but
			// successful observation, and prevents the runtime from safely
			// deciding whether another collection round is useful.
			infrastructure = append(infrastructure, fmt.Sprintf("%s evidence unavailable: %v", result.source, result.err))
			continue
		}
		evidence = append(evidence, normalizeCollectedEvidence(incident, result.evidence)...)
	}
	sort.Strings(infrastructure)
	return evidence, infrastructure
}

// serverWorkerFindings records deterministic collector outcomes without
// asking an LLM to summarize or reinterpret observations. The Evidence IDs
// reference the normalized facts shared by every later diagnosis node.
func serverWorkerFindings(requests []domain.EvidenceRequest, evidence []domain.Evidence, infrastructure []string, round int) []domain.WorkerFinding {
	bySource := map[string][]string{}
	for _, item := range evidence {
		source := workerSourceForEvidence(item.Source)
		bySource[source] = append(bySource[source], item.ID)
	}
	for source := range bySource {
		sort.Strings(bySource[source])
	}
	findings := make([]domain.WorkerFinding, 0, len(requests))
	now := time.Now().UTC()
	for _, request := range requests {
		source := canonicalWorkerSource(request.Source)
		unknowns := []string{}
		for _, problem := range infrastructure {
			if strings.HasPrefix(problem, source+" ") {
				unknowns = append(unknowns, problem)
			}
		}
		ids := append([]string(nil), bySource[source]...)
		summary := fmt.Sprintf("server collector returned %d normalized evidence records", len(ids))
		if len(unknowns) > 0 {
			summary = "server collector did not return usable evidence"
		}
		findings = append(findings, domain.WorkerFinding{
			TaskID: fmt.Sprintf("server-%s-round-%d", source, round), Worker: "evidence_collection", Source: source,
			Summary: summary, EvidenceIDs: ids, Unknowns: unknowns, CompletedAt: now,
		})
	}
	return findings
}

func workerSourceForEvidence(source domain.EvidenceSource) string {
	switch strings.ToLower(string(source)) {
	case "prometheus":
		return "metric"
	case "loki":
		return "log"
	case "jaeger":
		return "trace"
	case "kubernetes":
		return "topology"
	default:
		return strings.ToLower(string(source))
	}
}

func collectSignals(evidence []domain.Evidence) []domain.EvidenceSignal {
	var out []domain.EvidenceSignal
	for _, item := range evidence {
		out = append(out, item.Signals...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func deterministicFalsification(drafts []domain.HypothesisDraft, assertions []domain.StateAssertion) []domain.FalsificationResult {
	byProperty := map[string][]domain.StateAssertion{}
	for _, assertion := range assertions {
		byProperty[assertion.Property] = append(byProperty[assertion.Property], assertion)
	}
	results := make([]domain.FalsificationResult, 0, len(drafts))
	for _, draft := range drafts {
		result := domain.FalsificationResult{CandidateID: draft.ID}
		for _, property := range expectedObservationPropertiesForCategory(draft.Category) {
			items := byProperty[property]
			if len(items) == 0 {
				result.MissingObservationKinds = append(result.MissingObservationKinds, property)
				continue
			}
			for _, assertion := range items {
				if assertion.Status == domain.StateAssertionContradicted || assertion.State == "normal" {
					result.ContradictingAssertionIDs = append(result.ContradictingAssertionIDs, assertion.ID)
				} else {
					result.SupportingAssertionIDs = append(result.SupportingAssertionIDs, assertion.ID)
				}
			}
		}
		results = append(results, result)
	}
	return results
}

func deterministicPairwise(verified []domain.VerifiedHypothesis, assertions []domain.StateAssertion) []domain.PairwiseFalsification {
	if len(verified) < 2 {
		return nil
	}
	ordered := append([]domain.VerifiedHypothesis(nil), verified...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ObjectiveScore > ordered[j].ObjectiveScore })
	limit := len(ordered)
	if limit > 3 {
		limit = 3
	}
	var out []domain.PairwiseFalsification
	for i := 0; i < limit; i++ {
		for j := i + 1; j < limit; j++ {
			left, right := ordered[i], ordered[j]
			leftExpected := stringSet(expectedObservationPropertiesForCategory(left.Draft.Category))
			rightExpected := stringSet(expectedObservationPropertiesForCategory(right.Draft.Category))
			result := domain.PairwiseFalsification{PreferredCandidateID: left.Draft.ID, OtherCandidateID: right.Draft.ID, Result: "inconclusive"}
			for _, assertion := range assertions {
				if assertion.Status != domain.StateAssertionActive || assertion.State != "abnormal" || !leftExpected[assertion.Property] || rightExpected[assertion.Property] {
					continue
				}
				result.DiscriminatingAssertionIDs = append(result.DiscriminatingAssertionIDs, assertion.ID)
			}
			if len(result.DiscriminatingAssertionIDs) > 0 {
				result.Result = "preferred_by_discriminating_observation"
			}
			out = append(out, result)
		}
	}
	return out
}

func expectedObservationPropertiesForCategory(category string) []string {
	switch category {
	case "cpu":
		return []string{"cpu_pressure", "cpu_throttling", "request_latency", "application_errors"}
	case "memory":
		return []string{"memory_pressure", "memory_growth", "pod_restarts", "application_errors"}
	case "database":
		return []string{"connection_pressure", "dependency_availability", "request_latency", "trace_error", "application_errors"}
	case "network":
		return []string{"network_connectivity", "dependency_availability", "trace_error", "application_errors"}
	case "deployment":
		return []string{"workload_health", "pod_restarts", "application_errors"}
	case "dependency":
		return []string{"dependency_availability", "network_connectivity", "trace_error", "application_errors"}
	default:
		return nil
	}
}

func stringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func evidenceRequestsForPolicies(incident *domain.Incident, policies []domain.InvestigationPolicy, evidence []domain.Evidence) []domain.EvidenceRequest {
	allowed := allowedEvidenceTargets(incident, evidence)
	var requests []domain.EvidenceRequest
	for _, policy := range policies {
		for _, source := range sourcesForObservation(policy.ObservationKind) {
			for _, target := range evidenceTargetsForObservation(incident, evidence, policy.ObservationKind) {
				request := defaultEvidenceRequest(incident, source)
				request.Targets = []domain.ResourceRef{target}
				request.SignalKinds = []string{policy.ObservationKind}
				if !allowed[resourceIdentity(target.Service, target.Resource)] {
					continue
				}
				requests = append(requests, request)
			}
		}
	}
	return requests
}

// evidenceTargetsForObservation expands only dependency-facing observations
// to server-discovered, one-hop services. The first collection round already
// covers the incident workload; querying the same target again cannot answer
// whether one of its declared dependencies is unavailable. Every returned
// target is subsequently checked by validateEvidenceRequest.
func evidenceTargetsForObservation(incident *domain.Incident, evidence []domain.Evidence, observation string) []domain.ResourceRef {
	if incident == nil {
		return nil
	}
	current := domain.ResourceRef{Namespace: incident.Namespace, Service: incident.Service, Resource: incident.Resource}
	observation = strings.ToLower(strings.TrimSpace(observation))
	if observation != "dependency_availability" && observation != "network_connectivity" {
		return []domain.ResourceRef{current}
	}
	seen := map[string]bool{resourceIdentity(current.Service, current.Resource): true}
	var targets []domain.ResourceRef
	for _, item := range evidence {
		facts := item.Facts
		if len(facts) == 0 {
			facts = item.Content
		}
		for _, key := range []string{"discovered_dependencies", "dependencies"} {
			for _, dependency := range stringSlice(facts[key]) {
				identity := resourceIdentity(dependency, dependency)
				if identity == "" || seen[identity] {
					continue
				}
				seen[identity] = true
				targets = append(targets, domain.ResourceRef{Namespace: incident.Namespace, Service: dependency, Resource: dependency})
			}
		}
	}
	if len(targets) == 0 {
		return []domain.ResourceRef{current}
	}
	sort.SliceStable(targets, func(i, j int) bool {
		return resourceIdentity(targets[i].Service, targets[i].Resource) < resourceIdentity(targets[j].Service, targets[j].Resource)
	})
	return targets
}

// serverDependencyExplorationRequests is the deterministic bounded supplement
// for the Active Diagnosis baseline. When the incident workload has current
// request-failure observations but no observation of the availability of a
// topology-discovered dependency, the most discriminating safe next step is
// to inspect that dependency's Kubernetes endpoint state. This is not a
// free-form query and does not create a candidate: it is bounded to one hop,
// runs at most once in the existing second round, and remains subject to the
// same request fingerprint and scope checks as every cognitive proposal.
func serverDependencyExplorationRequests(incident *domain.Incident, evidence []domain.Evidence, assertions []domain.StateAssertion) []domain.EvidenceRequest {
	if incident == nil || !hasDownstreamFailureAssertion(assertions) || hasActiveAssertion(assertions, "dependency_availability") {
		return nil
	}
	targets := evidenceTargetsForObservation(incident, evidence, "dependency_availability")
	if len(targets) == 0 || (len(targets) == 1 && resourceIdentity(targets[0].Service, targets[0].Resource) == resourceIdentity(incident.Service, incident.Resource)) {
		return nil
	}
	requests := make([]domain.EvidenceRequest, 0, len(targets))
	for _, target := range targets {
		request := defaultEvidenceRequest(incident, "topology")
		request.Targets = []domain.ResourceRef{target}
		request.SignalKinds = []string{"dependency_availability"}
		requests = append(requests, request)
	}
	return requests
}

func hasDownstreamFailureAssertion(assertions []domain.StateAssertion) bool {
	for _, assertion := range assertions {
		if assertion.Status != domain.StateAssertionActive || assertion.State != "abnormal" {
			continue
		}
		switch assertion.Property {
		case "request_latency", "application_errors", "trace_error":
			return true
		}
	}
	return false
}

func hasActiveAssertion(assertions []domain.StateAssertion, property string) bool {
	for _, assertion := range assertions {
		if assertion.Property == property && assertion.Status == domain.StateAssertionActive && assertion.State == "abnormal" {
			return true
		}
	}
	return false
}

func sourcesForObservation(kind string) []string {
	switch strings.ToLower(kind) {
	case "cpu_pressure", "cpu_throttling", "memory_pressure", "memory_growth", "connection_pressure":
		return []string{"metric", "topology"}
	case "request_latency", "trace_error":
		return []string{"metric", "trace"}
	case "application_errors":
		return []string{"log", "trace"}
	case "network_connectivity", "dependency_availability", "workload_health", "pod_restarts":
		return []string{"topology", "metric"}
	case "recent_diff", "thread_dump", "profile":
		// No collector is authorized for these optional capabilities in the MVP.
		return nil
	default:
		return nil
	}
}

func applyTieBreakingPreference(result *domain.ArbitrationResult, verified []domain.VerifiedHypothesis, preferences []domain.TieBreakingPreference) {
	if result == nil || len(preferences) == 0 {
		return
	}
	if result.SelectedHypothesisID == "" {
		return
	}
	scores := map[string]float64{}
	for _, item := range verified {
		scores[item.Draft.ID] = item.ObjectiveScore
	}
	for _, preference := range preferences {
		if preference.PreferredCandidateID != result.SelectedHypothesisID && preference.OtherCandidateID != result.SelectedHypothesisID {
			continue
		}
		if abs(scores[preference.PreferredCandidateID]-scores[preference.OtherCandidateID]) > .10 {
			continue
		}
		// This is deliberately presentation-only. Objective ordering, selected
		// candidate, confidence margin, and every recovery gate remain intact.
		result.DisplayHypothesisID = preference.PreferredCandidateID
		return
	}
}

func recoveryPermission(verified []domain.VerifiedHypothesis, arbitration domain.ArbitrationResult) *domain.RecoveryPermission {
	permission := &domain.RecoveryPermission{Level: "escalate", Reason: arbitration.Reason}
	for _, item := range verified {
		if item.Draft.ID != arbitration.SelectedHypothesisID {
			continue
		}
		permission.ObjectiveDiagnosisConfidence = item.ObjectiveScore
		permission.DiagnosisStability = 1
		// Action safety and verification are only knowable once the existing
		// recovery proposal and dry-run nodes run. Keep this diagnosis-stage
		// permission non-authoritative and require the later safety controller.
		permission.Level = "requires_recovery_assessment"
		permission.Allowed = arbitration.Accepted
		permission.Reason = "objective diagnosis accepted; action safety and verification remain server-gated"
		return permission
	}
	return permission
}

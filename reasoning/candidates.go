package reasoning

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// GenerateDeterministicCandidates builds the closed, auditable candidate
// universe from live state assertions. It has no historical text or model
// input; unknown mechanisms are represented separately by the runtime.
func GenerateDeterministicCandidates(incident *domain.Incident, assertions []domain.StateAssertion, evidence []domain.Evidence, patternSets ...[]domain.CausalPattern) []domain.HypothesisDraft {
	signalEvidence := map[string]string{}
	for _, item := range evidence {
		for _, signal := range item.Signals {
			signalEvidence[signal.ID] = item.ID
		}
	}
	byCategory := map[string]domain.HypothesisDraft{}
	for _, assertion := range assertions {
		if assertion.Status != domain.StateAssertionActive || assertion.State != "abnormal" {
			continue
		}
		category, variant, cause := candidateForProperty(assertion.Property)
		if category == "" {
			continue
		}
		candidate := byCategory[category]
		if candidate.Category == "" {
			candidate = domain.HypothesisDraft{
				Category: category, Variant: variant, Cause: cause,
				Service: incident.Service, Resource: incident.Resource, PriorProbability: .20, RequireCausalMechanism: true,
			}
		}
		for _, signalID := range assertion.SupportingSignalIDs {
			if evidenceID := signalEvidence[signalID]; evidenceID != "" {
				candidate.SupportingEvidenceIDs = appendUniqueString(candidate.SupportingEvidenceIDs, evidenceID)
				candidate.ExpectedCausalNodeIDs = appendUniqueString(candidate.ExpectedCausalNodeIDs, "obs:"+evidenceID)
			}
		}
		byCategory[category] = candidate
	}
	patterns := flattenCandidatePatterns(patternSets...)
	// Active causal patterns can create a candidate only when a server-observed
	// cause or mechanism node is present. A symptom such as a generic timeout
	// is intentionally insufficient: it may be shared by several categories.
	for _, pattern := range patterns {
		if pattern.Status != "active" || pattern.Category == "" {
			continue
		}
		support, triggers := patternEvidence(pattern, evidence)
		if len(support) == 0 || !triggers {
			continue
		}
		candidate := byCategory[pattern.Category]
		if candidate.Category == "" {
			candidate = domain.HypothesisDraft{
				Category: pattern.Category, Variant: candidateVariant(pattern), Cause: pattern.Cause,
				Service: incident.Service, Resource: incident.Resource, PriorProbability: .20, RequireCausalMechanism: true,
			}
		}
		for _, evidenceID := range support {
			candidate.SupportingEvidenceIDs = appendUniqueString(candidate.SupportingEvidenceIDs, evidenceID)
		}
		byCategory[pattern.Category] = candidate
	}
	out := make([]domain.HypothesisDraft, 0, len(byCategory))
	for category, candidate := range byCategory {
		if len(candidate.SupportingEvidenceIDs) == 0 {
			continue
		}
		// An abnormal StateAssertion is an observation, not a root-cause
		// admission.  It must be bound to an active server causal pattern whose
		// cause or mechanism is currently observed.  In particular, do not leave
		// an `obs:<evidence>` fallback path behind when an embedded or ambiguous
		// signal (for example a registry transport error) has no matching
		// application causal mechanism.
		pattern, ok := bestCandidatePattern(category, candidate.SupportingEvidenceIDs, evidence, patterns)
		if !ok {
			continue
		}
		support, triggers := patternEvidence(pattern, evidence)
		if !triggers || len(support) == 0 {
			continue
		}
		// Score only evidence attributed to the selected causal pattern.  The
		// assertion may contain other same-category telemetry from the collection
		// scope, but that telemetry cannot silently become support for a distinct
		// mechanism.
		candidate.SupportingEvidenceIDs = support
		candidate.Variant = candidateVariant(pattern)
		candidate.Cause = pattern.Cause
		// The candidate's causal explanation is the strongest currently
		// observed server-graph path, not always the longest path declared by
		// the pattern. A pattern can contain alternative or optional stages; an
		// unobserved upstream stage must not make a fully observed decisive
		// mechanism→symptom path look incomplete. The selected path is still
		// made solely from canonical node IDs and declared graph edges.
		candidate.ExpectedCausalNodeIDs = observedPatternPath(pattern, evidence)
		if len(candidate.ExpectedCausalNodeIDs) == 0 {
			candidate.ExpectedCausalNodeIDs = patternPath(pattern)
		}
		candidate.Variant, candidate.Cause = candidateMechanismLabel(pattern, evidence, candidate.Variant, candidate.Cause)
		candidate.ExpectedCausalPath = append([]string(nil), candidate.ExpectedCausalNodeIDs...)
		digest := sha256.Sum256([]byte(strings.Join([]string{incident.Namespace, candidate.Service, candidate.Resource, candidate.Category, candidate.Variant}, "|")))
		candidate.ID = "candidate-" + hex.EncodeToString(digest[:12])
		if len(candidate.ExpectedCausalPath) == 0 {
			candidate.ExpectedCausalPath = append([]string(nil), candidate.ExpectedCausalNodeIDs...)
		}
		out = append(out, candidate)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func flattenCandidatePatterns(groups ...[]domain.CausalPattern) []domain.CausalPattern {
	var out []domain.CausalPattern
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

func candidateVariant(pattern domain.CausalPattern) string {
	return strings.ReplaceAll(strings.TrimSpace(pattern.ID), "-", "_")
}

// candidateMechanismLabel makes the deterministic candidate name reflect the
// concrete server-observed mechanism, when one is available. It is a fixed
// operational signal taxonomy, not a benchmark label map: no case identity,
// service name, free-form text, or model output participates. The generic
// pattern label remains the safe fallback when telemetry cannot distinguish a
// concrete mechanism.
func candidateMechanismLabel(pattern domain.CausalPattern, evidence []domain.Evidence, fallbackVariant, fallbackCause string) (string, string) {
	abnormal, normal, strength := patternSignalDirections(pattern, evidence)
	// A database endpoint is classified by the server from its declared
	// protocol surface (not its Service name). Preserve the target identity
	// that Kubernetes actually observed so this is distinguishable from an
	// application-side connection pool failure.
	if pattern.Category == "database" && abnormal["database_endpoint_unavailable"] {
		if target := observedDependencyTarget(pattern, evidence); target != "" {
			return target + "_unavailable", fmt.Sprintf("database endpoint %s has no ready backends", target)
		}
		return "database_endpoint_unavailable", "a server-classified database endpoint has no ready backends"
	}
	// A dependency candidate is scoped to the incident workload but is admitted
	// from a server-observed one-hop dependency endpoint.  Name that mechanism
	// explicitly instead of leaking the transport-level endpoint symptom into
	// the diagnosis label.
	if pattern.Category == "dependency" && (abnormal["dependency_unavailable"] || abnormal["endpoint_unavailable"]) {
		if target := observedDependencyTarget(pattern, evidence); target != "" {
			return target + "_unavailable", fmt.Sprintf("upstream dependency %s has no ready endpoints", target)
		}
		return "dependency_unavailable", "an upstream service dependency has no ready endpoints"
	}
	// A high, parser-derived connection-pressure signal is a bounded capacity
	// observation, not merely a generic database warning.  Preserve the more
	// specific operational mechanism when the source has already established
	// that level of pressure.
	if abnormal["connection_pressure"] && strength["connection_pressure"] >= .90 {
		return "connection_pool_exhaustion", "the local connection pool is exhausted and cannot admit requests"
	}
	// Sustained limit-normalised growth together with pressure is the
	// deterministic memory-leak signature in the runtime ontology.  A memory
	// pressure signal on its own deliberately remains the broader pattern.
	if abnormal["memory_growth"] && abnormal["memory_pressure"] {
		return "memory_leak", "sustained application memory growth is exhausting the workload limit"
	}
	// CFS throttling is a server-derived scheduling observation. When it is
	// sustained but the collector has not separately established high CPU-limit
	// utilisation, name the observed quota-pressure mechanism rather than
	// guessing whether demand came from traffic or a code path.
	if abnormal["cpu_throttling"] && strength["cpu_throttling"] >= .50 && strength["cpu_pressure"] < .60 {
		return "cpu_quota_pressure", "sustained CFS throttling is constraining workload CPU capacity"
	}
	// CPU pressure without a matching request-rate increase is the bounded
	// signature of application execution spinning rather than traffic-driven
	// load.  This remains an evidence label only; recovery is still governed by
	// the unchanged objective gates.
	if abnormal["cpu_pressure"] && normal["request_rate"] && !abnormal["request_rate"] {
		return "busy_loop", "application execution is spinning and saturating CPU without a matching request-rate increase"
	}
	for _, candidate := range []struct {
		signal  string
		variant string
		cause   string
	}{
		{"service_selector_mismatch", "service_selector_mismatch", "the Service selector matches no workload pods"},
		{"configured_endpoint_unresolvable", "configured_endpoint_unresolvable", "workload configuration points to a missing cluster-local Service"},
		{"network_policy_denial", "network_policy_denial", "network policy denial blocks a service dependency path"},
		{"image_reference_unresolvable", "unresolvable_image_reference", "the deployment references a container image that cannot be resolved"},
		{"image_pull_failure", "image_pull_failure", "container image acquisition failure prevents workload startup"},
		{"oom_killed", "oom_termination", "out-of-memory termination interrupts the workload"},
		{"connection_pressure", "connection_pool_pressure", "database connection pool pressure or exhaustion disrupts requests"},
		{"database_error", "database_error", "database operation failure disrupts dependent requests"},
		{"dependency_unavailable", "dependency_unavailable", "a required service dependency is unavailable"},
		{"endpoint_unavailable", "endpoint_unavailable", "a required service endpoint has no available backends"},
		{"connection_refused", "connection_refused", "a required service dependency refuses connections"},
		{"connection_timeout", "connection_timeout", "a required service dependency times out"},
		{"network_unreachable", "network_unreachable", "a required service dependency is unreachable"},
		{"crash_loop", "crash_loop", "the workload repeatedly crashes during startup"},
		{"probe_failure", "probe_failure", "workload health probes fail"},
	} {
		if abnormal[candidate.signal] {
			return candidate.variant, candidate.cause
		}
	}
	return fallbackVariant, fallbackCause
}

// observedDependencyTarget returns a server-observed upstream identity only
// when the dependency pattern's own cause node is present. It does not use
// incident names, logs, or model text: a connection error observed at the
// caller is an effect and cannot identify which dependency is unavailable.
func observedDependencyTarget(pattern domain.CausalPattern, evidence []domain.Evidence) string {
	admission := candidateAdmissionNodes(pattern)
	if len(admission) == 0 {
		return ""
	}
	nodes := map[string]domain.CausalNode{}
	for _, node := range pattern.Nodes {
		nodes[node.ID] = node
	}
	targets := map[string]bool{}
	for _, item := range evidence {
		for nodeID := range admission {
			node, ok := nodes[nodeID]
			if !ok || !evidenceMatchesCausalNode(item, node) {
				continue
			}
			if target := normalizeCausalNodeID(item.Service); target != "" {
				targets[target] = true
			}
		}
	}
	if len(targets) == 0 {
		return ""
	}
	values := make([]string, 0, len(targets))
	for target := range targets {
		values = append(values, target)
	}
	sort.Strings(values)
	return values[0]
}

func patternSignalDirections(pattern domain.CausalPattern, evidence []domain.Evidence) (map[string]bool, map[string]bool, map[string]float64) {
	allowed := map[string]map[string]bool{}
	for _, node := range pattern.Nodes {
		for _, signal := range node.Signals {
			signal = strings.ToLower(strings.TrimSpace(signal))
			if signal == "" {
				continue
			}
			if allowed[signal] == nil {
				allowed[signal] = map[string]bool{}
			}
			if node.Source != "" {
				allowed[signal][strings.ToLower(node.Source)] = true
			}
		}
	}
	abnormal, normal, strength := map[string]bool{}, map[string]bool{}, map[string]float64{}
	for _, item := range evidence {
		for _, signal := range item.Signals {
			name := strings.ToLower(strings.TrimSpace(signal.Signal))
			sources, known := allowed[name]
			if !known || (len(sources) > 0 && !sources[strings.ToLower(item.Source)]) {
				continue
			}
			switch signal.Direction {
			case "abnormal":
				abnormal[name] = true
				if signal.Strength > strength[name] {
					strength[name] = signal.Strength
				}
			case "normal":
				normal[name] = true
			}
		}
	}
	return abnormal, normal, strength
}

// patternEvidence returns the current evidence attributed to a pattern and
// whether the evidence includes a cause or mechanism. Pattern symptoms alone
// are deliberately not permitted to create a root-cause candidate.
func patternEvidence(pattern domain.CausalPattern, evidence []domain.Evidence) ([]string, bool) {
	admissionNodes := candidateAdmissionNodes(pattern)
	var ids []string
	observed := map[string]bool{}
	for _, item := range evidence {
		matched := false
		for _, node := range pattern.Nodes {
			if !evidenceMatchesCausalNode(item, node) {
				continue
			}
			matched = true
			observed[node.ID] = true
		}
		if matched {
			ids = appendUniqueString(ids, item.ID)
		}
	}
	triggers := false
	for nodeID := range admissionNodes {
		if observed[nodeID] {
			triggers = true
			break
		}
	}
	if !triggers {
		return ids, false
	}
	// Some mechanisms are only diagnostic as a pair. For example, a rollout
	// event is ordinary on its own and an application error can have many
	// causes; a causal graph may require both nodes without handing that
	// conjunction to a model or textual matcher.
	for _, nodeID := range pattern.RequiredAdmissionNodeIDs {
		if !observed[nodeID] {
			return ids, false
		}
	}
	return ids, true
}

// candidateAdmissionNodes identifies the causal observations that can create
// a root-cause candidate. A downstream mechanism (for example an unavailable
// workload after a failed rollout) is a consequence, not evidence that every
// upstream pattern ending in unavailability occurred. Admission therefore
// accepts root causes and mechanisms directly caused by a root cause. This is
// a graph property, not a signal-name allowlist, and keeps shared symptom
// signals from leaking across causal patterns.
func candidateAdmissionNodes(pattern domain.CausalPattern) map[string]bool {
	nodes := map[string]domain.CausalNode{}
	incoming := map[string][]string{}
	for _, node := range pattern.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range pattern.Edges {
		if nodes[edge.From].ID == "" || nodes[edge.To].ID == "" {
			continue
		}
		incoming[edge.To] = append(incoming[edge.To], edge.From)
	}
	admission := map[string]bool{}
	for _, node := range pattern.Nodes {
		typeName := strings.ToLower(strings.TrimSpace(node.Type))
		if typeName == "cause" && len(incoming[node.ID]) == 0 {
			admission[node.ID] = true
			continue
		}
		if typeName != "mechanism" {
			continue
		}
		for _, parentID := range incoming[node.ID] {
			if strings.EqualFold(strings.TrimSpace(nodes[parentID].Type), "cause") {
				admission[node.ID] = true
				break
			}
		}
	}
	// Remote dependency failures require an observation of the dependency
	// target when the graph models one. A timeout at the caller is a shared
	// downstream symptom: it can result from policy denial, service discovery,
	// or a genuinely unavailable dependency. Requiring the explicit target
	// avoids promoting that ambiguous symptom to a root cause while preserving
	// mechanism-only patterns that deliberately have no cause node.
	if pattern.Category == "dependency" {
		causes := map[string]bool{}
		for _, node := range pattern.Nodes {
			if strings.EqualFold(strings.TrimSpace(node.Type), "cause") && len(incoming[node.ID]) == 0 {
				causes[node.ID] = true
			}
		}
		if len(causes) > 0 {
			return causes
		}
	}
	// A mechanism-only pattern remains useful when it deliberately models a
	// direct operational failure without an explicit cause node.
	if len(admission) == 0 {
		for _, node := range pattern.Nodes {
			if strings.EqualFold(strings.TrimSpace(node.Type), "mechanism") {
				admission[node.ID] = true
			}
		}
	}
	return admission
}

func evidenceMatchesCausalNode(item domain.Evidence, node domain.CausalNode) bool {
	if node.Source != "" && !strings.EqualFold(strings.TrimSpace(node.Source), strings.TrimSpace(item.Source)) {
		return false
	}
	allowed := map[string]bool{}
	for _, signal := range node.Signals {
		signal = strings.ToLower(strings.TrimSpace(signal))
		if signal != "" {
			allowed[signal] = true
		}
	}
	if len(allowed) == 0 {
		// Compatibility for legacy server-owned patterns that predate typed
		// signal mappings. Production patterns carry Signals; this fallback is
		// intentionally limited to an exact canonical node ID and never looks
		// at summary text or facts.
		for _, nodeID := range item.CausalNodeIDs {
			if nodeID == node.ID {
				return true
			}
		}
		return false
	}
	for _, signal := range item.Signals {
		// An observed lifecycle/configuration fact is accepted only as a causal
		// *cause* node. It is never anomalous support by itself, and mechanisms
		// still require an abnormal server signal.
		if (signal.Direction == "abnormal" || (signal.Direction == "observed" && strings.EqualFold(strings.TrimSpace(node.Type), "cause"))) && allowed[strings.ToLower(strings.TrimSpace(signal.Signal))] {
			return true
		}
	}
	// Legacy stored evidence can carry canonical server node IDs but no typed
	// signals. Preserve that explicit server contract only when there are no
	// signals to evaluate; a present-but-unrelated signal must never be
	// promoted by a shared node name.
	if len(item.Signals) == 0 {
		for _, nodeID := range item.CausalNodeIDs {
			if nodeID == node.ID {
				return true
			}
		}
	}
	return false
}

func bestCandidatePattern(category string, supporting []string, evidence []domain.Evidence, patterns []domain.CausalPattern) (domain.CausalPattern, bool) {
	support := map[string]bool{}
	for _, id := range supporting {
		support[id] = true
	}
	var selected domain.CausalPattern
	selectedDecisive, selectedScore := -1, -1
	for _, pattern := range patterns {
		if pattern.Status != "active" || pattern.Category != category {
			continue
		}
		ids, triggers := patternEvidence(pattern, evidence)
		if !triggers {
			continue
		}
		decisive := observedAdmissionNodeCount(pattern, evidence)
		score := 0
		for _, id := range ids {
			if support[id] {
				score++
			}
		}
		if decisive > selectedDecisive || (decisive == selectedDecisive && (score > selectedScore || (score == selectedScore && (selected.ID == "" || pattern.ID < selected.ID)))) {
			selected, selectedDecisive, selectedScore = pattern, decisive, score
		}
	}
	return selected, selectedScore >= 0
}

// observedAdmissionNodeCount prefers the pattern whose own direct causal
// observations are present.  It prevents a downstream restart from making an
// unobserved OOM path look more specific than an observed memory-pressure
// path merely because the former contains more symptom evidence.
func observedAdmissionNodeCount(pattern domain.CausalPattern, evidence []domain.Evidence) int {
	observed := map[string]bool{}
	for _, item := range evidence {
		for _, node := range pattern.Nodes {
			if evidenceMatchesCausalNode(item, node) {
				observed[node.ID] = true
			}
		}
	}
	count := 0
	for nodeID := range candidateAdmissionNodes(pattern) {
		if observed[nodeID] {
			count++
		}
	}
	return count
}

// patternPath deterministically selects a root-to-leaf path from the
// server-owned causal graph. It never infers an edge from natural language or
// from a model response; branching graphs use the longest lexicographically
// stable path for a repeatable audit trail.
func patternPath(pattern domain.CausalPattern) []string {
	if len(pattern.Nodes) == 0 {
		return nil
	}
	nodes := map[string]bool{}
	incoming := map[string]int{}
	next := map[string][]string{}
	for _, node := range pattern.Nodes {
		nodes[node.ID] = true
	}
	for _, edge := range pattern.Edges {
		if !nodes[edge.From] || !nodes[edge.To] {
			continue
		}
		next[edge.From] = append(next[edge.From], edge.To)
		incoming[edge.To]++
	}
	var roots []string
	for node := range nodes {
		if incoming[node] == 0 {
			roots = append(roots, node)
		}
	}
	if len(roots) == 0 {
		for node := range nodes {
			roots = append(roots, node)
		}
	}
	sort.Strings(roots)
	for node := range next {
		sort.Strings(next[node])
	}
	var candidates [][]string
	var walk func(string, []string, map[string]bool)
	walk = func(node string, path []string, seen map[string]bool) {
		if seen[node] {
			return
		}
		seen = cloneCandidateSeen(seen)
		seen[node] = true
		path = append(path, node)
		if len(next[node]) == 0 {
			candidates = append(candidates, path)
			return
		}
		for _, child := range next[node] {
			walk(child, path, seen)
		}
	}
	for _, root := range roots {
		walk(root, nil, map[string]bool{})
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if len(candidates[i]) == len(candidates[j]) {
			return strings.Join(candidates[i], "\x00") < strings.Join(candidates[j], "\x00")
		}
		return len(candidates[i]) > len(candidates[j])
	})
	return candidates[0]
}

// observedPatternPath selects the strongest fully observed causal subpath.
// It starts at a server-observed cause or mechanism, follows only declared
// edges whose target nodes are observed, and requires an actual edge. This
// preserves the causal admission contract while allowing a canonical pattern
// to encode optional or alternative stages without forcing every candidate to
// prove an unrelated, unobserved branch.
func observedPatternPath(pattern domain.CausalPattern, evidence []domain.Evidence) []string {
	observed := map[string]bool{}
	nodes := map[string]domain.CausalNode{}
	next := map[string][]string{}
	for _, node := range pattern.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range pattern.Edges {
		if nodes[edge.From].ID == "" || nodes[edge.To].ID == "" {
			continue
		}
		next[edge.From] = append(next[edge.From], edge.To)
	}
	for _, item := range evidence {
		for _, node := range pattern.Nodes {
			if evidenceMatchesCausalNode(item, node) {
				observed[node.ID] = true
			}
		}
	}
	for node := range next {
		sort.Strings(next[node])
	}
	var paths [][]string
	var walk func(string, []string, map[string]bool)
	walk = func(nodeID string, path []string, seen map[string]bool) {
		if seen[nodeID] || !observed[nodeID] {
			return
		}
		seen = cloneCandidateSeen(seen)
		seen[nodeID] = true
		path = append(path, nodeID)
		expanded := false
		for _, child := range next[nodeID] {
			if observed[child] && !seen[child] {
				expanded = true
				walk(child, path, seen)
			}
		}
		if !expanded && len(path) >= 2 {
			paths = append(paths, path)
		}
	}
	for nodeID := range candidateAdmissionNodes(pattern) {
		if !observed[nodeID] {
			continue
		}
		walk(nodeID, nil, map[string]bool{})
	}
	if len(paths) == 0 {
		return nil
	}
	sort.SliceStable(paths, func(i, j int) bool {
		leftTerminal := strings.EqualFold(nodes[paths[i][len(paths[i])-1]].Type, "symptom")
		rightTerminal := strings.EqualFold(nodes[paths[j][len(paths[j])-1]].Type, "symptom")
		if leftTerminal != rightTerminal {
			return leftTerminal
		}
		if len(paths[i]) != len(paths[j]) {
			return len(paths[i]) > len(paths[j])
		}
		return strings.Join(paths[i], "\x00") < strings.Join(paths[j], "\x00")
	})
	return append([]string(nil), paths[0]...)
}

func cloneCandidateSeen(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in)+1)
	for key, value := range in {
		out[key] = value
	}
	return out
}

func candidateForProperty(property string) (category, variant, cause string) {
	switch property {
	case "cpu_pressure":
		return "cpu", "cpu_saturation", "container CPU saturation"
	case "memory_pressure":
		return "memory", "memory_pressure", "container memory pressure"
	case "connection_pressure", "dependency_availability":
		return "database", "connection_exhaustion", "dependency connection exhaustion"
	case "network_connectivity":
		return "network", "network_unavailable", "network connectivity failure"
	case "workload_health", "pod_restarts":
		return "deployment", "workload_regression", "workload configuration or deployment regression"
	default:
		return "", "", ""
	}
}

func appendUniqueString(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func observationCoverage(draft domain.HypothesisDraft, assertions []domain.StateAssertion) float64 {
	expected := expectedObservationProperties(draft.Category)
	if len(expected) == 0 {
		return 0
	}
	seen := map[string]bool{}
	for _, assertion := range assertions {
		if assertion.Status != domain.StateAssertionActive || assertion.State != "abnormal" {
			continue
		}
		for _, property := range expected {
			if assertion.Property == property {
				seen[property] = true
			}
		}
	}
	return clamp(float64(len(seen)) / float64(len(expected)))
}

func expectedObservationProperties(category string) []string {
	switch category {
	case "cpu":
		return []string{"cpu_pressure", "request_latency"}
	case "memory":
		return []string{"memory_pressure", "pod_restarts"}
	case "database":
		return []string{"connection_pressure", "request_latency"}
	case "network":
		return []string{"network_connectivity", "dependency_availability"}
	case "deployment":
		return []string{"workload_health", "pod_restarts"}
	case "dependency":
		return []string{"dependency_availability", "request_latency"}
	default:
		return nil
	}
}

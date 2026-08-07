package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func defaultEvidenceRequest(incident *domain.Incident, source string) domain.EvidenceRequest {
	end := time.Now().UTC()
	start := incident.EvidenceStartAt
	if start.IsZero() || !start.Before(end) {
		start = incident.CreatedAt.Add(-5 * time.Minute)
	}
	if start.IsZero() || !start.Before(end) {
		start = end.Add(-5 * time.Minute)
	}
	return domain.EvidenceRequest{
		Source:      source,
		Targets:     []domain.ResourceRef{{Namespace: incident.Namespace, Service: incident.Service, Resource: incident.Resource}},
		WindowStart: start, WindowEnd: end,
	}
}

// normalizeCollectedEvidence applies the Evidence contract at the collection
// boundary. Every downstream node receives stable identifiers, attributed
// scope, and a shared Facts representation before it can rank or reason.
func normalizeCollectedEvidence(incident *domain.Incident, items []domain.Evidence) []domain.Evidence {
	out := append([]domain.Evidence(nil), items...)
	for index := range out {
		normalizeEvidence(&out[index], incident)
	}
	return out
}

func validateEvidenceRequest(incident *domain.Incident, request domain.EvidenceRequest, source string, allowedTargets map[string]bool) (domain.EvidenceRequest, error) {
	if incident == nil {
		return request, fmt.Errorf("incident is required")
	}
	if strings.TrimSpace(request.Source) == "" {
		request.Source = source
	}
	if canonicalWorkerSource(request.Source) != canonicalWorkerSource(source) {
		return request, fmt.Errorf("collector source %q does not match request source %q", source, request.Source)
	}
	if len(request.Targets) == 0 {
		request.Targets = defaultEvidenceRequest(incident, source).Targets
	}
	for index := range request.Targets {
		target := request.Targets[index]
		if target.Namespace == "" {
			target.Namespace = incident.Namespace
		}
		if target.Namespace != incident.Namespace {
			return request, fmt.Errorf("evidence target namespace %q is outside incident namespace %q", target.Namespace, incident.Namespace)
		}
		identity := resourceIdentity(target.Service, target.Resource)
		if len(allowedTargets) > 0 && !allowedTargets[identity] {
			return request, fmt.Errorf("evidence target %q is outside the incident service and one-hop topology", identity)
		}
		request.Targets[index] = target
	}
	if request.WindowEnd.IsZero() {
		request.WindowEnd = time.Now().UTC()
	}
	if request.WindowStart.IsZero() || !request.WindowStart.Before(request.WindowEnd) {
		request.WindowStart = request.WindowEnd.Add(-5 * time.Minute)
	}
	request.SignalKinds = normalizeStrings(request.SignalKinds)
	request.HypothesisIDs = normalizeStrings(request.HypothesisIDs)
	return request, nil
}

func requestTargetIncident(incident *domain.Incident, request domain.EvidenceRequest) *domain.Incident {
	copy := *incident
	if len(request.Targets) > 0 {
		target := request.Targets[0]
		copy.Namespace = firstNonEmpty(target.Namespace, incident.Namespace)
		copy.Service = firstNonEmpty(target.Service, incident.Service)
		copy.Resource = firstNonEmpty(target.Resource, target.Service, incident.Resource)
	}
	copy.EvidenceStartAt = request.WindowStart
	return &copy
}

func evidenceRequestFingerprint(request domain.EvidenceRequest) string {
	request.SignalKinds = normalizeStrings(request.SignalKinds)
	// Hypothesis attribution does not change the observation query and must not
	// make an otherwise identical collection look new.
	request.HypothesisIDs = nil
	request.Source = canonicalWorkerSource(request.Source)
	sort.SliceStable(request.Targets, func(i, j int) bool {
		return resourceIdentity(request.Targets[i].Service, request.Targets[i].Resource) < resourceIdentity(request.Targets[j].Service, request.Targets[j].Resource)
	})
	raw, _ := json.Marshal(request)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func canonicalWorkerSource(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "kubernetes" {
		return "topology"
	}
	return source
}

func resourceIdentity(service, resource string) string {
	return strings.ToLower(firstNonEmpty(service, resource))
}

func normalizeStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

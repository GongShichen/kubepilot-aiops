package reasoning

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// BuildStateAssertions deterministically turns normalized server signals into
// live diagnostic facts. It intentionally has no model dependency: LLMs may
// interpret assertions but never create or alter them.
func BuildStateAssertions(incident *domain.Incident, evidence []domain.Evidence, previous []domain.StateAssertion, now time.Time) []domain.StateAssertion {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	groups := map[string]*domain.StateAssertion{}
	priorLastSeen := map[string]time.Time{}
	observedAbnormalSincePrior := map[string]bool{}
	observedNormalSincePrior := map[string]bool{}
	for _, assertion := range previous {
		copy := assertion
		key := assertionKey(assertion.Subject, assertion.Property)
		groups[key] = &copy
		priorLastSeen[key] = assertion.LastSeen
	}
	for _, item := range evidence {
		for _, signal := range item.Signals {
			property := assertionProperty(signal)
			if property == "" {
				continue
			}
			subject := assertionSubject(incident, item, signal)
			key := assertionKey(subject, property)
			assertion := groups[key]
			if assertion == nil {
				assertion = &domain.StateAssertion{ID: stableAssertionID(subject, property), Subject: subject, Property: property, State: "normal", FirstSeen: signal.ObservedAt, Status: domain.StateAssertionActive}
				if assertion.FirstSeen.IsZero() {
					assertion.FirstSeen = now
				}
				groups[key] = assertion
			}
			observedAt := signal.ObservedAt
			if observedAt.IsZero() {
				observedAt = now
			}
			if assertion.LastSeen.IsZero() || observedAt.After(assertion.LastSeen) {
				assertion.LastSeen = observedAt
			}
			prior, hadPrior := priorLastSeen[key]
			observedSincePrior := !hadPrior || prior.IsZero() || observedAt.After(prior)
			if signal.Direction == "abnormal" {
				confidence := signal.Strength * signal.Reliability * nonZero(signal.Independence, 1)
				if confidence >= assertion.Confidence {
					assertion.State = "abnormal"
					assertion.Confidence = confidence
				}
				assertion.SupportingSignalIDs = appendUniqueID(assertion.SupportingSignalIDs, signal.ID)
				assertion.Status = domain.StateAssertionActive
				if observedSincePrior {
					observedAbnormalSincePrior[key] = true
				}
			} else if signal.Direction == "normal" {
				assertion.ContradictingSignalIDs = appendUniqueID(assertion.ContradictingSignalIDs, signal.ID)
				if observedSincePrior {
					observedNormalSincePrior[key] = true
				}
			}
		}
	}
	out := make([]domain.StateAssertion, 0, len(groups))
	for key, assertion := range groups {
		if assertion.LastSeen.IsZero() {
			assertion.LastSeen = assertion.FirstSeen
		}
		// A normal observation only closes an existing abnormal assertion after
		// a subsequent collection round has failed to reproduce that abnormal
		// state. Mixed signal families from one collection pass (for example CPU
		// usage plus throttling) are independent observations, not immediate
		// proof that an abnormal assertion is false.
		if prior, hadPrior := priorLastSeen[key]; hadPrior && !prior.IsZero() && assertion.State == "abnormal" && observedNormalSincePrior[key] && !observedAbnormalSincePrior[key] {
			assertion.Status = domain.StateAssertionContradicted
		}
		if assertion.Status == domain.StateAssertionActive && assertion.LastSeen.Before(now.Add(-10*time.Minute)) {
			assertion.Status = domain.StateAssertionStale
		}
		out = append(out, *assertion)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Subject == out[j].Subject {
			return out[i].Property < out[j].Property
		}
		return out[i].Subject < out[j].Subject
	})
	return out
}

func assertionProperty(signal domain.EvidenceSignal) string {
	name := strings.ToLower(signal.Signal)
	switch {
	case strings.Contains(name, "cpu_throttl"):
		return "cpu_throttling"
	case strings.Contains(name, "cpu"):
		return "cpu_pressure"
	case strings.Contains(name, "memory_growth"):
		return "memory_growth"
	case strings.Contains(name, "memory"):
		return "memory_pressure"
	case strings.Contains(name, "trace_error"):
		return "trace_error"
	case strings.Contains(name, "latency"):
		return "request_latency"
	case strings.Contains(name, "error"), strings.Contains(name, "log"):
		return "application_errors"
	case strings.Contains(name, "restart"):
		return "pod_restarts"
	case strings.Contains(name, "connection"):
		return "connection_pressure"
	case strings.Contains(name, "endpoint"), strings.Contains(name, "dependency"):
		// Endpoint and dependency-unavailable signals are observations of a
		// concrete upstream availability state. Preserve that state rather than
		// dropping it merely because the signal name does not contain the word
		// "availability" (for example endpoint_unavailable).
		return "dependency_availability"
	case strings.Contains(name, "availability"):
		return "dependency_availability"
	case strings.Contains(name, "network"):
		return "network_connectivity"
	case strings.Contains(name, "workload"):
		return "workload_health"
	default:
		return ""
	}
}

func assertionSubject(incident *domain.Incident, item domain.Evidence, signal domain.EvidenceSignal) string {
	for _, value := range []string{signal.Resource, item.Resource, signal.Service, item.Service} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	if incident != nil {
		return firstNonBlank(incident.Resource, incident.Service, "incident")
	}
	return "incident"
}

func assertionKey(subject, property string) string {
	return strings.ToLower(strings.TrimSpace(subject)) + "|" + strings.ToLower(strings.TrimSpace(property))
}

func stableAssertionID(subject, property string) string {
	digest := sha256.Sum256([]byte(assertionKey(subject, property)))
	return "assertion-" + hex.EncodeToString(digest[:12])
}

func appendUniqueID(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func nonZero(value, fallback float64) float64 {
	if value == 0 {
		return fallback
	}
	return value
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

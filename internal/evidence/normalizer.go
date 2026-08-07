package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	projectionEmpty = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "evidence_projection_empty_total",
		Help: "Evidence records whose source payload could not be projected into canonical facts.",
	})
	missingID = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "evidence_missing_id_total",
		Help: "Evidence records received without an ID before server normalization.",
	})
)

func init() {
	prometheus.MustRegister(projectionEmpty, missingID)
}

// Normalize turns the backward-compatible Content/Data aliases into one
// canonical representation before evidence is ranked or shown to a model.
func Normalize(incident *domain.Incident, request domain.EvidenceRequest, items []domain.Evidence) []domain.Evidence {
	out := make([]domain.Evidence, 0, len(items))
	for _, item := range items {
		facts := mergeFacts(item.Content, item.Data, item.Facts)
		facts = enrichStructuredLogFacts(item, facts)
		if len(facts) == 0 && (len(item.Content) > 0 || len(item.Data) > 0 || len(item.Facts) > 0) {
			projectionEmpty.Inc()
		}
		item.Facts = facts
		item.Content = cloneMap(facts)
		item.Data = nil
		if item.Type == "" {
			item.Type = item.Kind
		}
		if item.Kind == "" {
			item.Kind = item.Type
		}
		if item.Timestamp.IsZero() {
			item.Timestamp = item.ObservedAt
		}
		if item.ObservedAt.IsZero() {
			item.ObservedAt = item.Timestamp
		}
		if item.ObservedAt.IsZero() {
			item.ObservedAt = time.Now().UTC()
			item.Timestamp = item.ObservedAt
		}
		applyScope(incident, request, &item)
		if strings.TrimSpace(item.ID) == "" {
			missingID.Inc()
			item.ID = StableID(item)
		}
		out = append(out, item)
	}
	return out
}

// enrichStructuredLogFacts preserves fields emitted by JSON loggers. Loki
// labels are frequently incomplete (for example, level is not promoted to a
// label), while the line itself contains the authoritative severity and error
// signature.  Projection is source-gated and only fills absent or empty
// fields, so collector-provided structured facts remain authoritative.
func enrichStructuredLogFacts(item domain.Evidence, facts map[string]any) map[string]any {
	source := strings.ToLower(strings.TrimSpace(string(item.Source)))
	if source != "loki" && source != "log" {
		return facts
	}
	line := strings.TrimSpace(item.Summary)
	if !strings.HasPrefix(line, "{") {
		return facts
	}
	var structured map[string]any
	if err := json.Unmarshal([]byte(line), &structured); err != nil || len(structured) == 0 {
		return facts
	}
	if facts == nil {
		facts = map[string]any{}
	}
	for key, value := range structured {
		current, exists := facts[key]
		if !exists || isEmptyFact(current) {
			facts[key] = value
		}
	}
	return facts
}

func isEmptyFact(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) == ""
}

func applyScope(incident *domain.Incident, request domain.EvidenceRequest, item *domain.Evidence) {
	if incident == nil {
		return
	}
	target := domain.ResourceRef{Namespace: incident.Namespace, Service: incident.Service, Resource: incident.Resource}
	if len(request.Targets) > 0 {
		target = request.Targets[0]
	}
	if item.Namespace == "" {
		item.Namespace = target.Namespace
	}
	if item.Service == "" {
		item.Service = target.Service
	}
	if item.Resource == "" {
		item.Resource = target.Resource
	}
	if item.WindowStart.IsZero() {
		item.WindowStart = request.WindowStart
	}
	if item.WindowEnd.IsZero() {
		item.WindowEnd = request.WindowEnd
	}
}

// StableID derives the only logical Evidence identifier used across the
// runtime. It intentionally excludes collection timestamps so a repeated
// observation is deduplicated instead of becoming a fresh fact on each pass.
func StableID(item domain.Evidence) string {
	payload := struct {
		Source, Kind, Namespace, Service, Resource, TraceID, TemplateID, Summary string
		Facts                                                                    map[string]any
	}{item.Source, first(item.Type, item.Kind), item.Namespace, item.Service, item.Resource, item.TraceID, item.TemplateID, item.Summary, item.Facts}
	raw, _ := json.Marshal(payload)
	digest := sha256.Sum256(raw)
	source := strings.ToLower(strings.TrimSpace(item.Source))
	if source == "" {
		source = "unknown"
	}
	return source + "-" + hex.EncodeToString(digest[:12])
}

// Views creates a common bounded representation. Each oversized field is
// truncated independently; one large record never prevents later records from
// being considered.
func Views(items []domain.Evidence, maximumBytes, perFieldBytes, maximumItems int) []domain.EvidenceView {
	if perFieldBytes <= 0 {
		perFieldBytes = 2048
	}
	if maximumItems <= 0 {
		maximumItems = len(items)
	}
	out := make([]domain.EvidenceView, 0, min(len(items), maximumItems))
	for _, item := range items {
		if len(out) >= maximumItems {
			break
		}
		view := View(item, perFieldBytes)
		trial := append(append([]domain.EvidenceView(nil), out...), view)
		raw, _ := json.Marshal(trial)
		if maximumBytes > 0 && len(raw) > maximumBytes {
			// Never discard the sole fact carrier. Reduce facts deterministically
			// by field and record every omitted field instead; a large record must
			// not starve later observations or turn a worker view into prose only.
			var truncated []string
			view.Facts, truncated = boundedFacts(view.Facts, minPositive(perFieldBytes, 256), 3)
			for _, field := range truncated {
				view.TruncatedFields = appendUnique(view.TruncatedFields, field)
			}
			trial = append(append([]domain.EvidenceView(nil), out...), view)
			raw, _ = json.Marshal(trial)
			if len(raw) > maximumBytes {
				view.Facts, truncated = boundedFacts(view.Facts, minPositive(perFieldBytes, 64), 1)
				for _, field := range truncated {
					view.TruncatedFields = appendUnique(view.TruncatedFields, field)
				}
				trial = append(append([]domain.EvidenceView(nil), out...), view)
				raw, _ = json.Marshal(trial)
				if len(raw) > maximumBytes {
					continue
				}
			}
		}
		out = append(out, view)
	}
	return out
}

func View(item domain.Evidence, perFieldBytes int) domain.EvidenceView {
	facts := mergeFacts(item.Content, item.Data, item.Facts)
	bounded, truncatedFields := boundedFacts(facts, perFieldBytes, 0)
	truncated := append([]string(nil), item.TruncatedFields...)
	for _, field := range truncatedFields {
		truncated = appendUnique(truncated, field)
	}
	observed := item.ObservedAt
	if observed.IsZero() {
		observed = item.Timestamp
	}
	return domain.EvidenceView{
		ID: item.ID, Source: item.Source, Kind: first(item.Type, item.Kind),
		Namespace: item.Namespace, Service: item.Service, Resource: item.Resource,
		ObservedAt: observed, Summary: truncateUTF8(item.Summary, 512), Facts: bounded,
		TruncatedFields: truncated, CausalNodeIDs: append([]string(nil), item.CausalNodeIDs...),
		ContextRelevance: item.RelevanceScore, AnomalyScore: item.AnomalyScore,
	}
}

// boundedFacts truncates individual values first, then deterministically
// retains at most maxFields fields. maxFields <= 0 keeps every field.
func boundedFacts(facts map[string]any, perFieldBytes, maxFields int) (map[string]any, []string) {
	if len(facts) == 0 {
		return nil, nil
	}
	bounded := make(map[string]any, len(facts))
	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var truncated []string
	for index, key := range keys {
		if maxFields > 0 && index >= maxFields {
			truncated = append(truncated, key)
			continue
		}
		value := facts[key]
		raw, err := json.Marshal(value)
		if err != nil {
			bounded[key] = fmt.Sprint(value)
			continue
		}
		if perFieldBytes > 0 && len(raw) > perFieldBytes {
			bounded[key] = map[string]any{"bounded_preview": truncateUTF8(string(raw), perFieldBytes), "original_bytes": len(raw)}
			truncated = append(truncated, key)
			continue
		}
		bounded[key] = value
	}
	return bounded, truncated
}

func minPositive(first, fallback int) int {
	if first > 0 && first < fallback {
		return first
	}
	return fallback
}

func mergeFacts(maps ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, values := range maps {
		for key, value := range values {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func truncateUTF8(value string, maximumBytes int) string {
	if maximumBytes <= 0 || len(value) <= maximumBytes {
		return value
	}
	end := maximumBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func appendUnique(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "unknown"
}

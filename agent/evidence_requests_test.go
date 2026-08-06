package agent

import (
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

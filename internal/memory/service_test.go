package memory

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type historicalFixture struct {
	semantic []domain.RetrievalCandidate
	lexical  []domain.RetrievalCandidate
	err      error
}

func (fixture historicalFixture) Semantic(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error) {
	if fixture.err != nil {
		return nil, fixture.err
	}
	return append([]domain.RetrievalCandidate(nil), fixture.semantic...), nil
}

func (fixture historicalFixture) Lexical(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error) {
	return append([]domain.RetrievalCandidate(nil), fixture.lexical...), nil
}

type causalFixture []domain.CausalPattern

func (fixture causalFixture) ListCausalPatterns(context.Context, string) ([]domain.CausalPattern, error) {
	return append([]domain.CausalPattern(nil), fixture...), nil
}

type failingCausalFixture struct{}

func (failingCausalFixture) ListCausalPatterns(context.Context, string) ([]domain.CausalPattern, error) {
	return nil, errors.New("causal unavailable")
}

type auditFixture struct{ events []domain.MemoryAccessEvent }

func (fixture *auditFixture) RecordMemoryAccess(_ context.Context, event domain.MemoryAccessEvent) error {
	fixture.events = append(fixture.events, event)
	return nil
}

type writerFixture struct {
	inputs []domain.IncidentLearningInput
}

func (fixture *writerFixture) WriteVerifiedIncident(_ context.Context, input domain.IncidentLearningInput) error {
	fixture.inputs = append(fixture.inputs, input)
	return nil
}

func TestEpisodicMemoryDoesNotCrossClusterOrNamespace(t *testing.T) {
	reader := historicalFixture{semantic: []domain.RetrievalCandidate{
		{IncidentID: "matching", Cluster: "cluster-a", Namespace: "team-a", Service: "payment"},
		{IncidentID: "wrong-cluster", Cluster: "cluster-b", Namespace: "team-a", Service: "payment"},
		{IncidentID: "wrong-namespace", Cluster: "cluster-a", Namespace: "team-b", Service: "payment"},
	}}
	service := Service{Historical: reader}
	items, err := service.Read(context.Background(), domain.MemoryQuery{IncidentID: "current", Kind: domain.MemoryEpisodic, Scope: domain.MemoryScope{Cluster: "cluster-a", Namespace: "team-a"}, Terms: []string{"payment"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "matching" {
		t.Fatalf("cross-scope episodic result leaked: %+v", items)
	}
}

func TestSemanticAndProceduralMemoryEnforceScope(t *testing.T) {
	service := Service{
		Causal: causalFixture{
			{ID: "global-curated", Cause: "redis timeout", Status: "active", Confidence: .8},
			{ID: "matching", Cluster: "cluster-a", Namespace: "team-a", Cause: "redis timeout", Status: "active", Confidence: .9},
			{ID: "other-tenant", Cluster: "cluster-a", Namespace: "team-b", Cause: "redis timeout", Status: "active", Confidence: 1},
		},
		Procedures: []domain.MemoryResult{
			{ID: "matching-skill", Scope: domain.MemoryScope{Cluster: "cluster-a", Namespace: "team-a"}, Summary: "redis recovery runbook", Score: 1},
			{ID: "other-skill", Scope: domain.MemoryScope{Cluster: "cluster-b", Namespace: "team-a"}, Summary: "redis recovery runbook", Score: 1},
		},
	}
	scope := domain.MemoryScope{Cluster: "cluster-a", Namespace: "team-a"}
	semantic, err := service.Read(context.Background(), domain.MemoryQuery{Kind: domain.MemorySemantic, Scope: scope, Terms: []string{"redis"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(semantic) != 2 || containsMemory(semantic, "other-tenant") {
		t.Fatalf("semantic scope filter failed: %+v", semantic)
	}
	procedural, err := service.Read(context.Background(), domain.MemoryQuery{Kind: domain.MemoryProcedural, Scope: scope, Terms: []string{"redis"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(procedural) != 1 || procedural[0].ID != "matching-skill" {
		t.Fatalf("procedural scope filter failed: %+v", procedural)
	}
}

func TestMemoryAuditWriterAndRecencyDecay(t *testing.T) {
	recorder := &auditFixture{}
	writer := &writerFixture{}
	service := Service{Recorder: recorder, Writer: writer}
	event := domain.MemoryAccessEvent{IncidentID: "incident", Kind: domain.MemoryEpisodic}
	if err := service.RecordAccess(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	incident := &domain.Incident{ID: "incident"}
	if err := service.WriteVerifiedIncident(context.Background(), domain.IncidentLearningInput{Incident: incident, Source: "resolved-incident"}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.events) != 1 || len(writer.inputs) != 1 || writer.inputs[0].Incident.ID != incident.ID {
		t.Fatalf("memory audit/write delegation failed: recorder=%+v writer=%+v", recorder.events, writer.inputs)
	}
	score := recencyAdjusted(1, time.Now().Add(-90*24*time.Hour), 90*24*time.Hour)
	if math.Abs(score-.5) > .01 {
		t.Fatalf("ninety-day half-life score=%f, want approximately 0.5", score)
	}
}

func TestMemoryEmptyBoundariesAndDependencyFailures(t *testing.T) {
	service := Service{}
	for _, kind := range []domain.MemoryKind{domain.MemoryWorking, domain.MemoryEpisodic, domain.MemorySemantic, domain.MemoryProcedural, "unknown"} {
		items, err := service.Read(context.Background(), domain.MemoryQuery{Kind: kind, Limit: 100})
		if err != nil || len(items) != 0 {
			t.Fatalf("empty %s memory returned items=%+v err=%v", kind, items, err)
		}
	}
	if err := service.RecordAccess(context.Background(), domain.MemoryAccessEvent{}); err != nil {
		t.Fatal(err)
	}
	if err := service.WriteVerifiedIncident(context.Background(), domain.IncidentLearningInput{}); err != nil {
		t.Fatal(err)
	}
	service.Historical = historicalFixture{err: errors.New("history unavailable")}
	if _, err := service.Read(context.Background(), domain.MemoryQuery{Kind: domain.MemoryEpisodic}); err == nil {
		t.Fatal("episodic dependency failure was hidden")
	}
	service.Causal = failingCausalFixture{}
	if _, err := service.Read(context.Background(), domain.MemoryQuery{Kind: domain.MemorySemantic}); err == nil {
		t.Fatal("semantic dependency failure was hidden")
	}
}

func TestMemoryLimitOrderingFilteringAndRecencyEdges(t *testing.T) {
	service := Service{Procedures: []domain.MemoryResult{
		{ID: "b", Summary: "generic database runbook", Score: .5},
		{ID: "a", Summary: "generic database runbook", Score: .5},
		{ID: "c", Summary: "network runbook", Score: 1},
	}}
	items, err := service.Read(context.Background(), domain.MemoryQuery{Kind: domain.MemoryProcedural, Terms: []string{"database"}, Limit: 1})
	if err != nil || len(items) != 1 || items[0].ID != "b" {
		t.Fatalf("procedural limit/filter failed: items=%+v err=%v", items, err)
	}
	if score := recencyAdjusted(.7, time.Time{}, time.Hour); score != .7 {
		t.Fatalf("zero timestamp changed score: %f", score)
	}
	if score := recencyAdjusted(.7, time.Now().Add(time.Hour), time.Hour); score != .7 {
		t.Fatalf("future timestamp changed score: %f", score)
	}
	if hasTermOverlap("db", "database") || hasTermOverlap("redis", "network") {
		t.Fatal("term overlap accepted a short or unrelated term")
	}
	if max(1, 2) != 2 || max(2, 1) != 2 {
		t.Fatal("max helper returned the wrong bound")
	}
}

func containsMemory(items []domain.MemoryResult, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

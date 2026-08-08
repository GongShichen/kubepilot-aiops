package retrieval

import (
	"context"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestSkillRetrieverSelectsRelevantFrozenSkillWithoutActivatingIt(t *testing.T) {
	retriever := NewSkillHybridRetriever(nil, nil)
	documents := []domain.SkillSearchDocument{
		{ID: "investigate-metrics", Version: "1", ContentHash: "metric-hash", Description: "investigate metric trends and resource saturation", OutputContract: "evidence-request", CompatiblePhases: []domain.BrainPhase{domain.BrainPhaseInvestigation}},
		{ID: "investigate-logs", Version: "1", ContentHash: "log-hash", Description: "inspect application error log templates", OutputContract: "evidence-request", CompatiblePhases: []domain.BrainPhase{domain.BrainPhaseInvestigation}},
	}
	result, err := retriever.Search(context.Background(), domain.SkillRetrievalQuery{IncidentID: "incident-a", Phase: domain.BrainPhaseInvestigation, Text: "memory metric saturation trend", Documents: documents, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].ID != "investigate-metrics" || result.Results[0].ContentHash != "metric-hash" {
		t.Fatalf("unexpected Skill retrieval: %+v", result)
	}
	if result.QueryHash == "" || result.SnapshotHash == "" {
		t.Fatalf("Skill retrieval is not replayable: %+v", result)
	}
}

func TestSkillRetrieverDoesNotExposePhaseCompatibleButIrrelevantSkill(t *testing.T) {
	retriever := NewSkillHybridRetriever(nil, nil)
	result, err := retriever.Search(context.Background(), domain.SkillRetrievalQuery{
		IncidentID: "incident-b",
		Phase:      domain.BrainPhaseInvestigation,
		Text:       "database connection latency",
		Documents: []domain.SkillSearchDocument{{
			ID: "inspect-kubernetes", Version: "1", ContentHash: "k8s-hash",
			Description:      "inspect pod rollout image and scheduling state",
			CompatiblePhases: []domain.BrainPhase{domain.BrainPhaseInvestigation},
		}},
		Limit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("phase compatibility alone exposed an irrelevant optional Skill: %+v", result.Results)
	}
}

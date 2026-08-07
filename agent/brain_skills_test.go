package agent

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestBrainSkillResolverPinsCompleteBundlesAndDependencies(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	if len(resolver.SnapshotHash()) != 64 {
		t.Fatalf("invalid skill snapshot hash %q", resolver.SnapshotHash())
	}
	resolved, err := resolver.Resolve(domain.BrainPhaseInvestigation, []SkillRequest{{SkillID: "investigate-metrics", Reason: "separate resource pressure", Trigger: "HYPOTHESIS_CONFLICT", RequestedBy: "BRAIN", RequestedTurn: "turn-2"}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{"brain-kernel": false, "form-hypotheses": false, "select-tools": false, "investigate-metrics": false}
	for _, ref := range resolved.Refs {
		if _, ok := wanted[ref.ID]; ok {
			wanted[ref.ID] = true
		}
	}
	for id, found := range wanted {
		if !found {
			t.Fatalf("resolved skill set is missing %s: %+v", id, resolved.Refs)
		}
	}
	if !resolved.AllowedCategories[domain.BrainToolEvidence] || !resolved.AllowedCategories[domain.BrainToolReasoning] {
		t.Fatalf("unexpected effective categories: %+v", resolved.AllowedCategories)
	}
	reference, err := resolved.ReadReference("investigate-metrics", "metric-signals.md")
	if err != nil || reference == "" {
		t.Fatalf("active reference unavailable: %q %v", reference, err)
	}
}

func TestBrainSkillResolverRejectsPhaseAndBudgetWithoutExpandingAuthority(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(domain.BrainPhaseInvestigation, []SkillRequest{{SkillID: "plan-recovery", Reason: "try recovery", Trigger: "MODEL_REQUEST", RequestedBy: "BRAIN"}, {SkillID: "investigate-metrics", Reason: "need metrics", Trigger: "MODEL_REQUEST", RequestedBy: "BRAIN"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	rejected := map[string]string{}
	for _, event := range resolved.Activations {
		if event.Status == "REJECTED" {
			rejected[event.SkillID] = event.RejectedReason
		}
	}
	if rejected["plan-recovery"] != "incompatible_phase" || rejected["investigate-metrics"] != "optional_skill_budget_exhausted" {
		t.Fatalf("unexpected rejections: %+v", rejected)
	}
	if resolved.AllowedCategories[domain.BrainToolRecovery] {
		t.Fatal("recovery authority leaked into investigation")
	}
}

func TestBrainSkillResolverNoOptionalSkillsAblationRejectsBrainRequests(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(domain.BrainPhaseInvestigation, []SkillRequest{{SkillID: "investigate-metrics", Reason: "need metrics", Trigger: "MODEL_REQUEST", RequestedBy: "BRAIN"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range resolved.Refs {
		if ref.ID == "investigate-metrics" {
			t.Fatalf("optional skill loaded while the ablation budget was zero: %+v", resolved.Refs)
		}
	}
	found := false
	for _, event := range resolved.Activations {
		if event.SkillID == "investigate-metrics" && event.Status == "REJECTED" && event.RejectedReason == "optional_skill_budget_exhausted" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing optional-skill rejection audit: %+v", resolved.Activations)
	}
}

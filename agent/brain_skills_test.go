package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestBrainSkillsProvideValidStructuredOutputExamples(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	for id, pkg := range resolver.packages {
		for _, section := range []string{"## Preconditions", "## Server-Owned Inputs", "## Procedure", "## Allowed Tools", "## Required IDs", "## Output Contract", "## Output Example", "## Stop & Failure Conditions", "## Handoff"} {
			if !strings.Contains(pkg.Content, section) {
				t.Fatalf("brain skill %s is missing required contract section %q", id, section)
			}
		}
		marker := "## Output Example"
		if !strings.Contains(pkg.Content, marker) {
			t.Fatalf("brain skill %s has no output example", id)
		}
		section := strings.SplitN(pkg.Content, marker, 2)[1]
		if next := strings.Index(section, "\n## "); next >= 0 {
			section = section[:next]
		}
		blocks := strings.Split(section, "```json\n")
		valid := 0
		for _, block := range blocks[1:] {
			end := strings.Index(block, "\n```")
			if end < 0 {
				t.Fatalf("brain skill %s has an unterminated JSON output example", id)
			}
			var example struct {
				Tool      string         `json:"tool"`
				Arguments map[string]any `json:"arguments"`
			}
			if err = json.Unmarshal([]byte(block[:end]), &example); err != nil {
				t.Fatalf("brain skill %s has invalid JSON output example: %v", id, err)
			}
			if strings.TrimSpace(example.Tool) == "" || strings.TrimSpace(stringValue(example.Arguments["intent"])) == "" {
				t.Fatalf("brain skill %s output example is missing tool or intent", id)
			}
			if values, ok := example.Arguments["expected_observation"].([]any); !ok || len(values) == 0 {
				t.Fatalf("brain skill %s output example is missing expected_observation", id)
			}
			valid++
		}
		if valid == 0 {
			t.Fatalf("brain skill %s has no JSON output example", id)
		}
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func TestBrainSkillResolverPinsCompleteBundlesAndDependencies(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	if len(resolver.SnapshotHash()) != 64 {
		t.Fatalf("invalid skill snapshot hash %q", resolver.SnapshotHash())
	}
	resolved, err := resolver.Resolve(domain.BrainPhaseInvestigation, []SkillRequest{{SkillID: "investigate-metrics", Reason: "separate resource pressure", Trigger: "HYPOTHESIS_CONFLICT", RequestedBy: "BRAIN", RequestedTurn: "turn-2"}}, 2, "turn-3")
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
	for _, activation := range resolved.Activations {
		if activation.SkillID == "" || activation.Version == "" || activation.ContentHash == "" || activation.Reason == "" || activation.Trigger == "" || activation.RequestedBy == "" || activation.RequestedTurn == "" || activation.Status == "" {
			t.Fatalf("resolved Skill activation has incomplete audit identity: %+v", activation)
		}
		if activation.Reason == "mandatory phase procedure" && activation.RequestedTurn != "turn-3" {
			t.Fatalf("mandatory Skill activation was not bound to its model Turn: %+v", activation)
		}
	}
	reference, err := resolved.ReadReference("investigate-metrics", "metric-signals.md")
	if err != nil || reference == "" {
		t.Fatalf("active reference unavailable: %q %v", reference, err)
	}
}

func TestBrainSkillResolverExposesFrozenRetrievalDocumentsWithoutActivation(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	catalog := resolver.SkillDocuments(domain.BrainPhaseInvestigation)
	wanted := map[string]bool{"investigate-metrics": false, "investigate-logs": false, "investigate-traces": false, "inspect-kubernetes": false, "select-tools": false}
	for _, entry := range catalog {
		if entry.ID == "" || entry.Version == "" || entry.Description == "" || entry.OutputContract == "" || len(entry.AllowedToolCategories) == 0 {
			t.Fatalf("optional Skill catalog entry is incomplete: %+v", entry)
		}
		if _, ok := wanted[entry.ID]; ok {
			wanted[entry.ID] = true
		}
	}
	for id, found := range wanted {
		if !found {
			t.Fatalf("phase-compatible optional Skill %s is undiscoverable: %+v", id, catalog)
		}
	}
	if len(resolver.SkillDocuments(domain.BrainPhaseIntake)) != 0 {
		t.Fatal("INTAKE unexpectedly advertised optional Skills")
	}
}

func TestBrainSkillResolverRejectsPhaseAndBudgetWithoutExpandingAuthority(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(domain.BrainPhaseInvestigation, []SkillRequest{{SkillID: "plan-recovery", Reason: "try recovery", Trigger: "MODEL_REQUEST", RequestedBy: "BRAIN"}, {SkillID: "investigate-metrics", Reason: "need metrics", Trigger: "MODEL_REQUEST", RequestedBy: "BRAIN"}}, 1, "turn-rejected")
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
	resolved, err := resolver.Resolve(domain.BrainPhaseInvestigation, []SkillRequest{{SkillID: "investigate-metrics", Reason: "need metrics", Trigger: "MODEL_REQUEST", RequestedBy: "BRAIN"}}, 0, "turn-ablation")
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

func TestBrainSkillResolverRequiresTurnAndAuditsUnknownSkillAgainstCatalog(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.Resolve(domain.BrainPhaseInvestigation, nil, 2, ""); err == nil || !strings.Contains(err.Error(), "activation turn is required") {
		t.Fatalf("resolver accepted a Skill bundle without a Turn identity: %v", err)
	}
	resolved, err := resolver.Resolve(domain.BrainPhaseInvestigation, []SkillRequest{{
		SkillID: "not-in-frozen-catalog", Reason: "request a nonexistent capability", Trigger: "MODEL_REQUEST", RequestedBy: "BRAIN",
	}}, 2, "turn-unknown")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, activation := range resolved.Activations {
		if activation.SkillID != "not-in-frozen-catalog" {
			continue
		}
		found = true
		if activation.Status != "REJECTED" || activation.RejectedReason != "unknown_skill" || activation.Version != "catalog-v1-unresolved" || activation.ContentHash != resolver.SnapshotHash() || activation.RequestedTurn != "turn-unknown" {
			t.Fatalf("unknown Skill rejection is not bound to its Turn and frozen catalog: %+v", activation)
		}
	}
	if !found {
		t.Fatalf("unknown Skill request produced no rejection audit: %+v", resolved.Activations)
	}
}

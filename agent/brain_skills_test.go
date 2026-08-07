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

package agent

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentProductionCodeHasNoManualEinoToolCallOrBenchmarkDependency(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	root := filepath.Dir(current)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), "_test.go") || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		for _, imported := range file.Imports {
			if strings.Contains(strings.Trim(imported.Path.Value, `"`), "/benchmark") {
				t.Fatalf("Agent runtime imports benchmark code: %s", path)
			}
		}
		full, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		ast.Inspect(full, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "ToolCall" {
				return true
			}
			if identifier, ok := selector.X.(*ast.Ident); ok && identifier.Name == "schema" {
				t.Errorf("Agent runtime manually constructs schema.ToolCall in %s", path)
			}
			return true
		})
	}
}

func TestSkillsArePinnedAndDoNotEncodeHiddenWorkflow(t *testing.T) {
	cases := []struct{ agent, path string }{
		{SupervisorAgentName, "internal/agent/skills/supervisor/SKILL.md"},
		{DiagnosisAgentName, "internal/agent/skills/diagnosis/SKILL.md"},
		{RecoveryAgentName, "internal/agent/skills/recovery/SKILL.md"},
	}
	hashes := map[string]bool{}
	for _, item := range cases {
		skill, err := loadAgentSkill(resolveProjectFile(item.path), item.agent)
		if err != nil {
			t.Fatal(err)
		}
		if len(skill.Hash) != 64 || hashes[skill.Hash] {
			t.Fatalf("invalid or duplicate Skill hash for %s", item.agent)
		}
		hashes[skill.Hash] = true
		lower := strings.ToLower(skill.Content)
		for _, forbidden := range []string{"must call query_", "always call query_", "first call query_", "ground_truth", "scenario_id", "case_id"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("Skill %s contains hidden workflow content %q", item.agent, forbidden)
			}
		}
	}
}

func TestRecoveryAgentCannotDiscoverMutationTools(t *testing.T) {
	tools, err := buildConstrainedRecoveryTools(constrainedToolDeps{})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range tools {
		info, infoErr := candidate.Info(context.Background())
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		for _, forbidden := range []string{"restart_workload", "scale_deployment", "rollback_deployment", "verify_"} {
			if info.Name == forbidden || strings.HasPrefix(info.Name, forbidden) {
				t.Fatalf("Recovery Agent can discover side-effect capability %q", info.Name)
			}
		}
	}
}

func TestForbiddenToolIntentProtectsEvaluationAndServerContext(t *testing.T) {
	if forbiddenToolIntent(DiagnosisAgentName, `{"ground_truth":"answer"}`) == "" {
		t.Fatal("evaluation answer access was not rejected")
	}
	if forbiddenToolIntent(RecoveryAgentName, `{"execution_context":{"approval_id":"forged"}}`) == "" {
		t.Fatal("server execution context override was not rejected")
	}
	if code := forbiddenToolIntent(DiagnosisAgentName, `{"hypothesis_id":"h1"}`); code != "" {
		t.Fatalf("ordinary diagnosis input was rejected: %s", code)
	}
}

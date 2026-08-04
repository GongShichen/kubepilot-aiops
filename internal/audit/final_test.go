package audit

import (
	"bytes"
	"context"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/execution"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate audit package")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "../.."))
}

func productionAgentFiles(t *testing.T) []string {
	t.Helper()
	root := filepath.Join(repoRoot(t), "agent")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		files = append(files, filepath.Join(root, entry.Name()))
	}
	return files
}

func TestAgentRuntimeHasNoBenchmarkDependency(t *testing.T) {
	for _, path := range productionAgentFiles(t) {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			if strings.Contains(strings.Trim(imported.Path.Value, `"`), "/benchmark") {
				t.Fatalf("agent runtime imports benchmark package: %s", path)
			}
		}
	}
}

func TestRecoveryAgentSurfaceHasNoMutationOrVerificationCapability(t *testing.T) {
	path := filepath.Join(repoRoot(t), "agent", "constrained_tools.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "buildConstrainedRecoveryTools" {
			body = function.Body
			break
		}
	}
	if body == nil {
		t.Fatal("recovery tool builder is missing")
	}
	var source bytes.Buffer
	if err := format.Node(&source, token.NewFileSet(), body); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"restart_workload", "scale_deployment", "rollback_deployment", "verify_kubernetes_health", "verify_prometheus_recovery", "verify_loki_recovery", "verify_trace_recovery"} {
		if strings.Contains(source.String(), forbidden) {
			t.Fatalf("Recovery Agent tool surface contains forbidden capability %q", forbidden)
		}
	}
}

func TestSafetyFeedbackCannotPrescribeToolOrRecoveryAnswer(t *testing.T) {
	knownTools := []string{"query_prometheus_evidence", "retrieve_semantic_incidents", "restart_workload"}
	feedback := safety.Repairable(domain.SafetyScopeDiagnosis, "insufficient_evidence", "more independent observations are required", nil, []string{"query_prometheus_evidence"}, 1)
	if safety.ValidateFeedback(feedback, knownTools) {
		t.Fatal("feedback containing a concrete tool must be rejected")
	}
	feedback = safety.Repairable(domain.SafetyScopeRecoveryProposal, "proposal_incomplete", "the proposal lacks a safe rollback description", nil, []string{"choose rollback_deployment"}, 1)
	if safety.ValidateFeedback(feedback, knownTools) {
		t.Fatal("feedback containing a concrete recovery capability must be rejected")
	}
	feedback = safety.Repairable(domain.SafetyScopeDiagnosis, "insufficient_evidence", "independent observations are required", nil, []string{"more evidence is required to test the current hypothesis"}, 1)
	if !safety.ValidateFeedback(feedback, knownTools) {
		t.Fatal("answer-neutral feedback was rejected")
	}
}

func TestActionExecutorRequiresServerApprovalValidator(t *testing.T) {
	called := 0
	backend := mutationBackendFunc(func(context.Context, *domain.Incident, domain.RecoveryProposal) error {
		called++
		return nil
	})
	executor, err := execution.NewActionExecutor(context.Background(), backend, func(incident *domain.Incident, proposal domain.RecoveryProposal) error {
		if incident.ExecutionContext == nil || incident.ExecutionContext.ApprovalID == "" {
			return os.ErrPermission
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = executor.Execute(context.Background(), &domain.Incident{ID: "audit", Namespace: "kubepilot-demo"}, domain.RecoveryProposal{ID: "proposal", Action: domain.ActionRestartPod, Target: "kubepilot-demo/gateway"})
	if err == nil || called != 0 {
		t.Fatalf("mutation executed without server approval: err=%v calls=%d", err, called)
	}
}

type mutationBackendFunc func(context.Context, *domain.Incident, domain.RecoveryProposal) error

func (f mutationBackendFunc) Execute(ctx context.Context, incident *domain.Incident, proposal domain.RecoveryProposal) error {
	return f(ctx, incident, proposal)
}

func TestRetrievalHasNoLegacyProductionAliases(t *testing.T) {
	root := filepath.Join(repoRoot(t), "retrieval")
	var found []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, legacy := range []string{"type HybridRetriever", "type HybridFacade"} {
			if strings.Contains(string(data), legacy) {
				found = append(found, path+": "+legacy)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("legacy retrieval production aliases remain: %s", strings.Join(found, "; "))
	}
}

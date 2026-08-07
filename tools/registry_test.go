package tools

import (
	"context"
	"testing"
	"time"
)

func TestRegistryRequiresApprovalMiddlewareForActions(t *testing.T) {
	capability, err := NewCapability("restart_workload", "restart", func(context.Context, struct {
		Target string `json:"target"`
	}) (string, error) {
		return "ok", nil
	}, Registration{Category: CategoryAction, AllowedNodes: []string{NodeActionExecutor}, Timeout: time.Second, MaxArgumentBytes: 100, MaxOutputBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	err = registry.Register(context.Background(), capability)
	if err == nil {
		t.Fatal("action tool without approval middleware was accepted")
	}
}

func TestRegistryEnforcesNodeAllowlistAndBounds(t *testing.T) {
	meta := Registration{Category: CategoryObservability, AllowedNodes: []string{NodeDiagnosisReact}, Timeout: time.Second, MaxArgumentBytes: 32, MaxOutputBytes: 32}
	capability, err := NewCapability("query_evidence", "query", func(context.Context, struct {
		Value string `json:"value"`
	}) (string, error) {
		return "bounded", nil
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err = registry.Register(context.Background(), capability); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.ToolsForNode(NodeActionExecutor); err == nil {
		t.Fatal("tool escaped its node allowlist")
	}
	config, err := registry.ToolsNodeConfig(NodeDiagnosisReact, false)
	if err != nil || len(config.Tools) != 1 || config.ExecuteSequentially {
		t.Fatalf("tools=%d sequential=%t err=%v", len(config.Tools), config.ExecuteSequentially, err)
	}
	invokable, err := registry.InvokableForNode(context.Background(), NodeDiagnosisReact, "query_evidence")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = invokable.InvokableRun(context.Background(), `{"value":"012345678901234567890123456789012"}`); err == nil {
		t.Fatal("registered capability bypassed argument bound")
	}
}

func TestRegistryValidatesToolArgumentsBeforeTypedInvocation(t *testing.T) {
	type input struct {
		Intent string `json:"intent" jsonschema:"required"`
		Count  int    `json:"count" jsonschema:"required,minimum=1"`
	}
	capability, err := NewCapability("typed_tool", "typed", func(context.Context, input) (string, error) {
		return "ok", nil
	}, Registration{Category: CategoryReasoning, AllowedNodes: []string{NodeBrainReasoning}, Timeout: time.Second, MaxArgumentBytes: 1024, MaxOutputBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err = registry.Register(context.Background(), capability); err != nil {
		t.Fatal(err)
	}
	for name, arguments := range map[string]string{
		"invalid_json": `{"intent":"investigate" "count":1}`,
		"wrong_type":   `{"intent":"investigate","count":"one"}`,
		"missing":      `{"intent":"investigate"}`,
		"trailing":     `{"intent":"investigate","count":1} {}`,
	} {
		if err = registry.ValidateArgumentsForNode(NodeBrainReasoning, "typed_tool", arguments); err == nil {
			t.Fatalf("%s arguments passed pre-invocation validation", name)
		}
	}
	if err = registry.ValidateArgumentsForNode(NodeBrainReasoning, "typed_tool", `{"intent":"investigate","count":2}`); err != nil {
		t.Fatalf("valid typed arguments were rejected: %v", err)
	}
	if err = registry.ValidateArgumentsForNode(NodeBrainReasoning, "unknown_tool", `{"intent":"investigate"}`); err != nil {
		t.Fatalf("valid JSON for an unknown tool did not reach UnknownToolsHandler: %v", err)
	}
}

func TestCapabilityRegistrationIsImmutable(t *testing.T) {
	meta := Registration{Category: CategoryReasoning, AllowedNodes: []string{NodeDiagnosisReact}, Timeout: time.Second, MaxArgumentBytes: 100, MaxOutputBytes: 100}
	capability, err := NewCapability("reason", "reason", func(context.Context, struct{}) (string, error) { return "ok", nil }, meta)
	if err != nil {
		t.Fatal(err)
	}
	meta.AllowedNodes[0] = NodeRecoveryReact
	registered := capability.Registration()
	if len(registered.AllowedNodes) != 1 || registered.AllowedNodes[0] != NodeDiagnosisReact {
		t.Fatalf("caller mutated capability registration: %+v", registered)
	}
}

func TestRegistryRejectsDuplicateCapabilityNamesAcrossNodes(t *testing.T) {
	first, err := NewCapability("same_name", "first", func(context.Context, struct{}) (string, error) { return "first", nil }, Registration{Category: CategoryReasoning, AllowedNodes: []string{NodeDiagnosisReact}, Timeout: time.Second, MaxArgumentBytes: 100, MaxOutputBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewCapability("same_name", "second", func(context.Context, struct{}) (string, error) { return "second", nil }, Registration{Category: CategoryDecision, AllowedNodes: []string{NodeSupervisorReact}, Timeout: time.Second, MaxArgumentBytes: 100, MaxOutputBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err = registry.RegisterAll(context.Background(), first, second); err == nil {
		t.Fatal("duplicate capability name was accepted across nodes")
	}
	if _, err = registry.ToolsForNode(NodeDiagnosisReact); err == nil {
		t.Fatal("failed RegisterAll partially mutated the registry")
	}
}

func TestRegistryRejectsUnknownCategoryAndDuplicateNode(t *testing.T) {
	for name, meta := range map[string]Registration{
		"unknown_category": {Category: ToolCategory("unknown"), AllowedNodes: []string{NodeDiagnosisReact}, Timeout: time.Second, MaxArgumentBytes: 100, MaxOutputBytes: 100},
		"duplicate_node":   {Category: CategoryReasoning, AllowedNodes: []string{NodeDiagnosisReact, NodeDiagnosisReact}, Timeout: time.Second, MaxArgumentBytes: 100, MaxOutputBytes: 100},
	} {
		capability, err := NewCapability(name, name, func(context.Context, struct{}) (string, error) { return "ok", nil }, meta)
		if err != nil {
			t.Fatal(err)
		}
		if err = NewRegistry().Register(context.Background(), capability); err == nil {
			t.Fatalf("invalid registration %s was accepted", name)
		}
	}
}

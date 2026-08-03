package tools

import (
	"context"
	"testing"
	"time"

	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

func TestRegistryRequiresApprovalMiddlewareForActions(t *testing.T) {
	candidate, err := toolutils.InferTool("restart_workload", "restart", func(context.Context, struct {
		Target string `json:"target"`
	}) (string, error) { return "ok", nil })
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	err = registry.Register(context.Background(), candidate, Registration{Category: CategoryAction, AllowedNodes: []string{"action_tools_node"}, Timeout: time.Second, MaxArgumentBytes: 100, MaxOutputBytes: 100})
	if err == nil {
		t.Fatal("action tool without approval middleware was accepted")
	}
}

func TestRegistryEnforcesNodeAllowlistAndBounds(t *testing.T) {
	candidate, err := toolutils.InferTool("query_evidence", "query", func(context.Context, struct {
		Value string `json:"value"`
	}) (string, error) { return "bounded", nil })
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	meta := Registration{Category: CategoryObservability, AllowedNodes: []string{"evidence_tools_node"}, Timeout: time.Second, MaxArgumentBytes: 32, MaxOutputBytes: 32}
	if err = registry.Register(context.Background(), candidate, meta); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.ToolsForNode("action_tools_node"); err == nil {
		t.Fatal("tool escaped its node allowlist")
	}
	items, err := registry.ToolsForNode("evidence_tools_node")
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
}

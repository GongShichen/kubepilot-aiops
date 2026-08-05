package agent

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
)

func registeredCapabilitiesForTest(t *testing.T, node string, capabilities []captools.Capability) []tool.BaseTool {
	t.Helper()
	registry := captools.NewRegistry()
	if err := registry.RegisterAll(context.Background(), capabilities...); err != nil {
		t.Fatal(err)
	}
	items, err := registry.ToolsForNode(node)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
)

type ToolCategory string

const (
	CategoryIncident      ToolCategory = "incident"
	CategoryObservability ToolCategory = "observability"
	CategoryDecision      ToolCategory = "decision"
	CategoryDryRun        ToolCategory = "dry_run"
	CategoryAction        ToolCategory = "action"
	CategoryVerification  ToolCategory = "verification"
)

type Registration struct {
	Category           ToolCategory
	AllowedNodes       []string
	Timeout            time.Duration
	MaxArgumentBytes   int
	MaxOutputBytes     int
	ApprovalMiddleware bool
}

type registeredTool struct {
	tool tool.BaseTool
	meta Registration
}

type boundedTool struct {
	tool.InvokableTool
	meta Registration
}

func (t boundedTool) InvokableRun(ctx context.Context, arguments string, opts ...tool.Option) (string, error) {
	if len(arguments) > t.meta.MaxArgumentBytes {
		return "", fmt.Errorf("tool arguments exceed %d bytes", t.meta.MaxArgumentBytes)
	}
	toolCtx, cancel := context.WithTimeout(ctx, t.meta.Timeout)
	defer cancel()
	output, err := t.InvokableTool.InvokableRun(toolCtx, arguments, opts...)
	if err != nil {
		return "", err
	}
	if len(output) > t.meta.MaxOutputBytes {
		return "", fmt.Errorf("tool output exceeds %d bytes", t.meta.MaxOutputBytes)
	}
	return output, nil
}

// Registry is the single capability catalog for both ADK agents and Graph
// ToolsNodes. It rejects unbounded or incorrectly exposed mutation tools.
type Registry struct {
	mu    sync.RWMutex
	items map[string]registeredTool
}

func NewRegistry() *Registry { return &Registry{items: map[string]registeredTool{}} }

func (r *Registry) Register(ctx context.Context, candidate tool.BaseTool, meta Registration) error {
	if candidate == nil {
		return fmt.Errorf("tool is required")
	}
	info, err := candidate.Info(ctx)
	if err != nil {
		return err
	}
	if info == nil || info.Name == "" || info.ParamsOneOf == nil {
		return fmt.Errorf("tool schema and unique name are required")
	}
	if meta.Category == "" || len(meta.AllowedNodes) == 0 || meta.Timeout <= 0 || meta.MaxArgumentBytes <= 0 || meta.MaxOutputBytes <= 0 {
		return fmt.Errorf("tool %s has incomplete category, allowlist, timeout, or size limits", info.Name)
	}
	if meta.Category == CategoryAction && !meta.ApprovalMiddleware {
		return fmt.Errorf("action tool %s requires approval middleware", info.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[info.Name]; exists {
		return fmt.Errorf("duplicate tool %q", info.Name)
	}
	invokable, ok := candidate.(tool.InvokableTool)
	if !ok {
		return fmt.Errorf("tool %s must be invokable", info.Name)
	}
	r.items[info.Name] = registeredTool{tool: boundedTool{InvokableTool: invokable, meta: meta}, meta: meta}
	return nil
}

func (r *Registry) ToolsForNode(node string) ([]tool.BaseTool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0)
	for name, item := range r.items {
		for _, allowed := range item.meta.AllowedNodes {
			if allowed == node {
				names = append(names, name)
				break
			}
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("node %q has no registered tools", node)
	}
	sort.Strings(names)
	out := make([]tool.BaseTool, 0, len(names))
	for _, name := range names {
		out = append(out, r.items[name].tool)
	}
	return out, nil
}

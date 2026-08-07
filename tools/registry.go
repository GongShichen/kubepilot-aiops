package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	validationjsonschema "github.com/google/jsonschema-go/jsonschema"
)

type ToolCategory string

const (
	CategoryIncident      ToolCategory = "incident"
	CategoryObservability ToolCategory = "observability"
	CategoryRetrieval     ToolCategory = "retrieval"
	CategoryReasoning     ToolCategory = "reasoning"
	CategoryAgent         ToolCategory = "agent"
	CategoryDecision      ToolCategory = "decision"
	CategoryDryRun        ToolCategory = "dry_run"
	CategoryAction        ToolCategory = "action"
	CategoryVerification  ToolCategory = "verification"
)

const (
	NodeAlertCorrelation = "alert_correlation"
	NodeDiagnosisReact   = "diagnosis_react"
	NodeRecoveryReact    = "recovery_react"
	NodeSupervisorReact  = "supervisor_react"
	NodeActionExecutor   = "deterministic_action_executor"
	NodeBrainEvidence    = "brain_evidence_tools"
	NodeBrainRetrieval   = "brain_retrieval_tools"
	NodeBrainReasoning   = "brain_reasoning_tools"
	NodeBrainRecovery    = "brain_recovery_tools"
	NodeBrainControl     = "brain_control_tools"
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
	tool           tool.BaseTool
	capability     Capability
	meta           Registration
	argumentSchema *validationjsonschema.Resolved
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

func (r *Registry) Register(ctx context.Context, capability Capability) error {
	return r.RegisterAll(ctx, capability)
}

func prepareCapability(ctx context.Context, capability Capability) (string, registeredTool, error) {
	if capability == nil {
		return "", registeredTool{}, fmt.Errorf("capability is required")
	}
	candidate := capability.EinoTool()
	meta := capability.Registration()
	if candidate == nil {
		return "", registeredTool{}, fmt.Errorf("capability Eino tool is required")
	}
	info, err := candidate.Info(ctx)
	if err != nil {
		return "", registeredTool{}, err
	}
	if info == nil || info.Name == "" || info.ParamsOneOf == nil {
		return "", registeredTool{}, fmt.Errorf("tool schema and unique name are required")
	}
	if err = validateRegistration(info.Name, meta); err != nil {
		return "", registeredTool{}, err
	}
	argumentSchema, err := compileArgumentSchema(info)
	if err != nil {
		return "", registeredTool{}, fmt.Errorf("compile tool %s argument schema: %w", info.Name, err)
	}
	invokable, ok := candidate.(tool.InvokableTool)
	if !ok {
		return "", registeredTool{}, fmt.Errorf("tool %s must be invokable", info.Name)
	}
	return info.Name, registeredTool{tool: boundedTool{InvokableTool: invokable, meta: meta}, capability: capability, meta: cloneRegistration(meta), argumentSchema: argumentSchema}, nil
}

func compileArgumentSchema(info *schema.ToolInfo) (*validationjsonschema.Resolved, error) {
	if info == nil || info.ParamsOneOf == nil {
		return nil, fmt.Errorf("tool schema is required")
	}
	einoSchema, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(einoSchema)
	if err != nil {
		return nil, err
	}
	var validationSchema validationjsonschema.Schema
	if err = json.Unmarshal(raw, &validationSchema); err != nil {
		return nil, err
	}
	return validationSchema.Resolve(nil)
}

func (r *Registry) RegisterAll(ctx context.Context, capabilities ...Capability) error {
	prepared := make(map[string]registeredTool, len(capabilities))
	for _, capability := range capabilities {
		name, item, err := prepareCapability(ctx, capability)
		if err != nil {
			return err
		}
		if _, exists := prepared[name]; exists {
			return fmt.Errorf("duplicate tool %q", name)
		}
		prepared[name] = item
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range prepared {
		if _, exists := r.items[name]; exists {
			return fmt.Errorf("duplicate tool %q", name)
		}
	}
	for name, item := range prepared {
		r.items[name] = item
	}
	return nil
}

func validateRegistration(name string, meta Registration) error {
	switch meta.Category {
	case CategoryIncident, CategoryObservability, CategoryRetrieval, CategoryReasoning, CategoryAgent, CategoryDecision, CategoryDryRun, CategoryAction, CategoryVerification:
	default:
		return fmt.Errorf("tool %s has unknown category %q", name, meta.Category)
	}
	if len(meta.AllowedNodes) == 0 || meta.Timeout <= 0 || meta.MaxArgumentBytes <= 0 || meta.MaxOutputBytes <= 0 {
		return fmt.Errorf("tool %s has incomplete allowlist, timeout, or size limits", name)
	}
	seen := make(map[string]bool, len(meta.AllowedNodes))
	for _, node := range meta.AllowedNodes {
		if node == "" {
			return fmt.Errorf("tool %s has an empty allowed node", name)
		}
		if seen[node] {
			return fmt.Errorf("tool %s repeats allowed node %q", name, node)
		}
		seen[node] = true
	}
	if meta.Category == CategoryAction && !meta.ApprovalMiddleware {
		return fmt.Errorf("action tool %s requires approval middleware", name)
	}
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

func (r *Registry) ToolInfosForNode(ctx context.Context, node string) ([]*schema.ToolInfo, error) {
	items, err := r.ToolsForNode(node)
	if err != nil {
		return nil, err
	}
	infos := make([]*schema.ToolInfo, 0, len(items))
	for _, item := range items {
		info, infoErr := item.Info(ctx)
		if infoErr != nil {
			return nil, infoErr
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// ValidateArgumentsForNode performs the same JSON shape validation exposed to
// the model before Eino's ToolsNode attempts its typed Go unmarshalling. This
// keeps provider formatting errors inside the Agent protocol: callers can
// return a non-empty Tool status for every ToolCall instead of turning a single
// malformed argument object into a graph-level NodeRunError.
//
// Unknown tool names deliberately pass schema validation after valid JSON so
// ToolsNode's UnknownToolsHandler can return the normal category constraint.
func (r *Registry) ValidateArgumentsForNode(node, name, arguments string) error {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return fmt.Errorf("arguments must be one JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(arguments))
	var instance any
	if err := decoder.Decode(&instance); err != nil {
		return fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("arguments contain more than one JSON value")
	} else if err != io.EOF {
		return fmt.Errorf("arguments have invalid trailing data: %w", err)
	}

	r.mu.RLock()
	item, exists := r.items[name]
	r.mu.RUnlock()
	if !exists || !registrationAllowsNode(item.meta, node) {
		return nil
	}
	if len(arguments) > item.meta.MaxArgumentBytes {
		return fmt.Errorf("arguments exceed %d bytes", item.meta.MaxArgumentBytes)
	}
	if item.argumentSchema == nil {
		return fmt.Errorf("argument schema is unavailable")
	}
	if err := item.argumentSchema.Validate(instance); err != nil {
		return fmt.Errorf("arguments do not match the exposed schema: %w", err)
	}
	return nil
}

func registrationAllowsNode(meta Registration, node string) bool {
	for _, allowed := range meta.AllowedNodes {
		if allowed == node {
			return true
		}
	}
	return false
}

// SchemaHash freezes the exact model-visible schemas and registration policy
// for an Execution Snapshot. Handler implementation details are represented by
// the source hash recorded by the deployment.
func (r *Registry) SchemaHash(ctx context.Context, nodes ...string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	type schemaRecord struct {
		Node         string           `json:"node"`
		Name         string           `json:"name"`
		Info         *schema.ToolInfo `json:"info"`
		Registration Registration     `json:"registration"`
	}
	records := []schemaRecord{}
	seen := map[string]bool{}
	for _, node := range nodes {
		for name, item := range r.items {
			allowed := false
			for _, candidate := range item.meta.AllowedNodes {
				if candidate == node {
					allowed = true
					break
				}
			}
			key := node + "\x00" + name
			if !allowed || seen[key] {
				continue
			}
			seen[key] = true
			info, err := item.tool.Info(ctx)
			if err != nil {
				return "", err
			}
			records = append(records, schemaRecord{Node: node, Name: name, Info: info, Registration: item.meta})
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Node != records[j].Node {
			return records[i].Node < records[j].Node
		}
		return records[i].Name < records[j].Name
	})
	raw, err := json.Marshal(records)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// ToolsNodeConfig is the single bridge from KubePilot capabilities to Eino
// ADK/ToolsNode configuration.
func (r *Registry) ToolsNodeConfig(node string, executeSequentially bool) (compose.ToolsNodeConfig, error) {
	items, err := r.ToolsForNode(node)
	if err != nil {
		return compose.ToolsNodeConfig{}, err
	}
	return compose.ToolsNodeConfig{Tools: items, ExecuteSequentially: executeSequentially}, nil
}

// InvokableForNode resolves a deterministic capability through the same node
// allowlist used by Agent ToolsNodes.
func (r *Registry) InvokableForNode(ctx context.Context, node, name string) (tool.InvokableTool, error) {
	items, err := r.ToolsForNode(node)
	if err != nil {
		return nil, err
	}
	for _, candidate := range items {
		info, infoErr := candidate.Info(ctx)
		if infoErr != nil {
			return nil, infoErr
		}
		if info.Name != name {
			continue
		}
		invokable, ok := candidate.(tool.InvokableTool)
		if !ok {
			return nil, fmt.Errorf("capability %s is not invokable", name)
		}
		return invokable, nil
	}
	return nil, fmt.Errorf("capability %q is not registered for node %q", name, node)
}

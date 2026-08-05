package safety

import (
	"fmt"
	"sync"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// BudgetController atomically enforces each Agent's independent iteration,
// ToolCall, token, and correction budgets. Tool cost and Incident totals are
// retained only as telemetry and never reject an Agent action.
type BudgetController struct {
	mu    sync.Mutex
	state *domain.AgentBudgetState
	costs map[string]int
}

func NewBudgetController(state *domain.AgentBudgetState, limits map[string]domain.AgentBudget, costs map[string]int) *BudgetController {
	if state == nil {
		state = &domain.AgentBudgetState{}
	}
	if state.Limits == nil {
		state.Limits = cloneLimits(limits)
	}
	if state.Usage == nil {
		state.Usage = map[string]domain.AgentBudgetUsage{}
	}
	return &BudgetController{state: state, costs: cloneCosts(costs)}
}

func (b *BudgetController) State() *domain.AgentBudgetState {
	b.mu.Lock()
	defer b.mu.Unlock()
	copyState := *b.state
	copyState.Limits = cloneLimits(b.state.Limits)
	copyState.Usage = make(map[string]domain.AgentBudgetUsage, len(b.state.Usage))
	for key, value := range b.state.Usage {
		copyState.Usage[key] = value
	}
	return &copyState
}

func (b *BudgetController) ReserveTool(agent, name string) (domain.AgentBudgetUsage, error) {
	return b.ReserveTools(agent, []string{name})
}

// ReserveTools charges one model-produced parallel batch atomically. If the
// complete batch does not fit, no member is allowed to execute.
func (b *BudgetController) ReserveTools(agent string, names []string) (domain.AgentBudgetUsage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	limit, ok := b.state.Limits[agent]
	if !ok {
		return domain.AgentBudgetUsage{}, fmt.Errorf("agent %q has no budget", agent)
	}
	totalCost := 0
	for _, name := range names {
		cost := b.costs[name]
		if cost <= 0 {
			cost = 1
		}
		totalCost += cost
	}
	usage := b.state.Usage[agent]
	count := len(names)
	if count == 0 {
		return usage, nil
	}
	if usage.ToolUses+count > limit.MaxToolUses {
		return usage, ErrBudgetExceeded{Agent: agent, Tool: names[0]}
	}
	usage.ToolUses += count
	usage.ToolCost += totalCost
	b.state.Usage[agent] = usage
	b.state.IncidentUses += count
	b.state.IncidentCost += totalCost
	return usage, nil
}

func (b *BudgetController) AddIteration(agent string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	limit, ok := b.state.Limits[agent]
	if !ok {
		return fmt.Errorf("agent %q has no budget", agent)
	}
	usage := b.state.Usage[agent]
	if usage.Iterations+1 > limit.MaxIterations {
		return ErrBudgetExceeded{Agent: agent, Resource: "iterations"}
	}
	usage.Iterations++
	b.state.Usage[agent] = usage
	return nil
}

func (b *BudgetController) AddTokens(agent string, tokens int) error {
	if tokens <= 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	limit, ok := b.state.Limits[agent]
	if !ok {
		return fmt.Errorf("agent %q has no budget", agent)
	}
	usage := b.state.Usage[agent]
	if usage.Tokens+tokens > limit.MaxTokens {
		return ErrBudgetExceeded{Agent: agent, Resource: "tokens"}
	}
	usage.Tokens += tokens
	b.state.Usage[agent] = usage
	b.state.IncidentTokens += tokens
	return nil
}

func (b *BudgetController) UseCorrection(agent string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	limit, ok := b.state.Limits[agent]
	if !ok {
		return 0, fmt.Errorf("agent %q has no budget", agent)
	}
	usage := b.state.Usage[agent]
	if usage.Corrections+1 > limit.MaxCorrections {
		return 0, ErrBudgetExceeded{Agent: agent, Resource: "corrections"}
	}
	usage.Corrections++
	b.state.Usage[agent] = usage
	return limit.MaxCorrections - usage.Corrections, nil
}

func (b *BudgetController) RemainingTools(agent string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	limit := b.state.Limits[agent]
	usage := b.state.Usage[agent]
	remaining := limit.MaxToolUses - usage.ToolUses
	if remaining < 0 {
		return 0
	}
	return remaining
}

// KnownTools returns policy names only; callers use it to ensure Safety
// feedback does not prescribe a concrete next tool.
func (b *BudgetController) KnownTools() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.costs))
	for name := range b.costs {
		out = append(out, name)
	}
	return out
}

type ErrBudgetExceeded struct {
	Agent    string
	Tool     string
	Resource string
}

func (e ErrBudgetExceeded) Error() string {
	resource := e.Resource
	if resource == "" {
		resource = "tool count"
	}
	if e.Tool != "" {
		return fmt.Sprintf("%s budget exhausted for agent %s before tool %s", resource, e.Agent, e.Tool)
	}
	return fmt.Sprintf("%s budget exhausted for agent %s", resource, e.Agent)
}

func cloneLimits(in map[string]domain.AgentBudget) map[string]domain.AgentBudget {
	out := make(map[string]domain.AgentBudget, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneCosts(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

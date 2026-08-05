package tools

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

// Capability is the only KubePilot tool contract. Every model-visible,
// deterministic, or nested-Agent capability carries its Eino implementation
// and its server-owned registration policy through this interface.
type Capability interface {
	EinoTool() tool.BaseTool
	Registration() Registration
}

// Definition composes an Eino tool with immutable KubePilot policy metadata.
// Go uses composition instead of inheritance; all capabilities share this
// concrete representation before entering Registry.
type Definition struct {
	tool         tool.BaseTool
	registration Registration
}

var _ Capability = Definition{}

func (d Definition) EinoTool() tool.BaseTool { return d.tool }

func (d Definition) Registration() Registration { return cloneRegistration(d.registration) }

// NewCapability infers the Eino schema from a typed handler and returns the
// canonical KubePilot capability representation.
func NewCapability[I, O any](name, description string, handler func(context.Context, I) (O, error), registration Registration) (Capability, error) {
	if handler == nil {
		return nil, fmt.Errorf("capability %q handler is required", name)
	}
	candidate, err := toolutils.InferTool(name, description, handler)
	if err != nil {
		return nil, err
	}
	return WrapCapability(candidate, registration)
}

// WrapCapability brings framework-created Eino tools, including ADK
// AgentTools, through the same policy and registry path as typed tools.
func WrapCapability(candidate tool.BaseTool, registration Registration) (Capability, error) {
	if candidate == nil {
		return nil, fmt.Errorf("Eino tool is required")
	}
	return Definition{tool: candidate, registration: cloneRegistration(registration)}, nil
}

func cloneRegistration(in Registration) Registration {
	out := in
	out.AllowedNodes = append([]string(nil), in.AllowedNodes...)
	return out
}

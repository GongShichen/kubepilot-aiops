package agent

import (
	"context"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type Collector interface {
	Collect(context.Context, *domain.Incident) ([]domain.Evidence, error)
}
type WorkflowState struct {
	Incident *domain.Incident `json:"incident"`
	Errors   []string         `json:"errors,omitempty"`
}
type Executor interface {
	Execute(context.Context, *domain.Incident, domain.RecoveryProposal) error
	Verify(context.Context, *domain.Incident) (domain.Verification, error)
}

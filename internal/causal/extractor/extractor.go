package extractor

import (
	"fmt"
	"strings"

	knowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// Propose extracts a server-owned candidate. It deliberately has no store
// access; persistence is only possible after Validator.Validate succeeds.
func Propose(in *domain.Incident) (knowledge.Proposal, bool) {
	return knowledge.ProposalFromIncident(in)
}

func ValidateText(proposal knowledge.Proposal) error {
	if strings.TrimSpace(proposal.Pattern.Cause) == "" || len(proposal.Pattern.CausalGraph.Nodes) < 2 {
		return fmt.Errorf("causal proposal requires a cause and a complete graph")
	}
	return nil
}

package topology

import (
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	internaltopology "github.com/kubepilot-aiops/kubepilot/internal/retrieval/topology"
)

func Build(incident *domain.Incident, evidence []domain.Evidence) ServiceGraph {
	return internaltopology.Build(incident, evidence)
}

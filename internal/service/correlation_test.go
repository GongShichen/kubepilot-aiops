package service

import (
	"context"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/agent"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type correlationRepository struct{}

func (correlationRepository) Get(context.Context, string) (*domain.Incident, error) {
	return &domain.Incident{}, nil
}
func (correlationRepository) List(context.Context, int, int) ([]domain.Incident, error) {
	return nil, nil
}

func TestEinoCorrelatorSelectsCandidateCapabilityByName(t *testing.T) {
	correlator, err := NewEinoCorrelator(context.Background(), correlationRepository{}, &agent.AgentRegistry{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := correlator.candidateTool.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "find_incident_candidates" {
		t.Fatalf("wrong correlation capability selected: %s", info.Name)
	}
}

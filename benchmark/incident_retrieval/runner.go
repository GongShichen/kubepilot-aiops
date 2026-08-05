package incident_retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
)

type RunnerConfig struct {
	Dataset   Dataset
	Engine    *retrieval.IncidentRetrievalEngine
	OutputDir string
	Progress  func(current, total int)
}

// Run executes the incident retrieval evaluation exclusively through the
// production IncidentRetrievalEngine. This package owns execution and scoring;
// it contains no retrieval or ranking implementation.
func Run(ctx context.Context, cfg RunnerConfig) (Report, error) {
	if cfg.Engine == nil {
		return Report{}, fmt.Errorf("production incident retrieval engine is required")
	}
	if err := cfg.Dataset.Validate(); err != nil {
		return Report{}, err
	}
	observations := make([]Observation, 0, len(cfg.Dataset.Incidents)*len(AblationStrategies))
	for index, query := range cfg.Dataset.Incidents {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		started := time.Now()
		stages, err := cfg.Engine.RunPipeline(ctx, query.Features(), retrieval.PipelineConfig{CandidateTopK: 100, ReasoningTopK: 20, FinalTopK: 20})
		if err != nil {
			return Report{}, fmt.Errorf("production incident retrieval for %s: %w", query.IncidentID, err)
		}
		elapsed := time.Since(started)
		stageResults := map[string][]domain.RetrievalCandidate{
			StrategySemantic:                stages.Semantic,
			StrategySemanticLexical:         stages.SemanticLexical,
			StrategySemanticLexicalTopology: stages.Topology,
			StrategySemanticLexicalCausal:   stages.Causal,
			StrategyFull:                    stages.Final,
		}
		for _, strategy := range AblationStrategies {
			observations = append(observations, Observation{
				QueryID: query.IncidentID, Strategy: strategy,
				RankedIDs: candidateIDs(stageResults[strategy]), Latency: elapsed,
			})
		}
		if cfg.Progress != nil {
			cfg.Progress(index+1, len(cfg.Dataset.Incidents))
		}
	}
	report := Evaluate(cfg.Dataset, observations)
	if cfg.OutputDir != "" {
		if err := os.MkdirAll(cfg.OutputDir, 0o750); err != nil {
			return Report{}, err
		}
		payload, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(cfg.OutputDir+"/incident_retrieval_report.json", payload, 0o640); err != nil {
			return Report{}, err
		}
	}
	return report, nil
}

func candidateIDs(candidates []domain.RetrievalCandidate) []string {
	ids := make([]string, len(candidates))
	for index, candidate := range candidates {
		ids[index] = candidate.IncidentID
	}
	return ids
}

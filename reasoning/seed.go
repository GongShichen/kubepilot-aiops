package reasoning

import (
	"fmt"
	"os"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"gopkg.in/yaml.v3"
)

type PatternSeed struct {
	Version  int                    `yaml:"version"`
	Patterns []domain.CausalPattern `yaml:"patterns"`
}

func LoadPatternSeed(path string) (PatternSeed, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return PatternSeed{}, err
	}
	var seed PatternSeed
	if err = yaml.Unmarshal(raw, &seed); err != nil {
		return PatternSeed{}, fmt.Errorf("decode causal pattern seed: %w", err)
	}
	seen := map[string]bool{}
	for i := range seed.Patterns {
		pattern := &seed.Patterns[i]
		if pattern.ID == "" || pattern.Category == "" || pattern.Cause == "" || len(pattern.Nodes) == 0 || len(pattern.Edges) == 0 {
			return PatternSeed{}, fmt.Errorf("invalid causal pattern at index %d", i)
		}
		if seen[pattern.ID] {
			return PatternSeed{}, fmt.Errorf("duplicate causal pattern %s", pattern.ID)
		}
		seen[pattern.ID] = true
		if pattern.Status == "" {
			pattern.Status = "active"
		}
		if pattern.Version == 0 {
			pattern.Version = seed.Version
		}
	}
	return seed, nil
}

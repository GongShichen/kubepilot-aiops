// Package suite defines the benchmark contract and orchestration metadata.
// Runtime execution is injected by callers so the suite cannot call internal
// Agent or Kubernetes functions directly.
package suite

import (
	"fmt"
	"sort"

	"github.com/kubepilot-aiops/kubepilot/benchmark/manifests"
)

const Name = "KubePilot Autonomous SRE Benchmark"

var Components = []string{
	"log_retrieval",
	"incident_retrieval",
	"diagnosis",
	"recovery",
	"agent_behavior",
	"knowledge_evolution",
}

type Section struct {
	Name    string `json:"name"`
	Dataset string `json:"dataset"`
	Status  string `json:"status"`
	Metrics any    `json:"metrics,omitempty"`
}

type Report struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Sections  []Section `json:"sections"`
	Ablations []Section `json:"ablations,omitempty"`
}

func ValidateManifest(path string) (manifests.Manifest, string, error) {
	m, hash, err := manifests.Load(path)
	if err != nil {
		return manifests.Manifest{}, "", err
	}
	if m.Version != "autonomous-sre" {
		return manifests.Manifest{}, "", fmt.Errorf("manifest version %q is not autonomous-sre", m.Version)
	}
	return m, hash, nil
}

func ComponentNames() []string {
	out := append([]string(nil), Components...)
	sort.Strings(out)
	return out
}

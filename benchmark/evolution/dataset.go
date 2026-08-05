package evolution

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type ResolvedIncident struct {
	IncidentID      string   `yaml:"incident_id" json:"incident_id"`
	Status          string   `yaml:"status" json:"status"`
	Service         string   `yaml:"service" json:"service"`
	Namespace       string   `yaml:"namespace" json:"namespace"`
	CausalPath      []string `yaml:"causal_path" json:"causal_path"`
	EvidenceSources []string `yaml:"evidence_sources" json:"evidence_sources"`
	TopologyPath    []string `yaml:"topology_path" json:"topology_path"`
	RootCause       string   `yaml:"root_cause" json:"-"`
}

type ResolvedDataset struct {
	Version   string             `yaml:"version" json:"version"`
	Incidents []ResolvedIncident `yaml:"incidents" json:"incidents"`
}

func LoadResolved(path string) (ResolvedDataset, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ResolvedDataset{}, err
	}
	var dataset ResolvedDataset
	if err = yaml.Unmarshal(b, &dataset); err != nil {
		return ResolvedDataset{}, err
	}
	if dataset.Version == "" || len(dataset.Incidents) == 0 {
		return ResolvedDataset{}, fmt.Errorf("resolved incident dataset requires version and incidents")
	}
	seen := map[string]bool{}
	for _, incident := range dataset.Incidents {
		if incident.Status != "RESOLVED" || incident.IncidentID == "" || seen[incident.IncidentID] {
			return ResolvedDataset{}, fmt.Errorf("invalid resolved incident %q", incident.IncidentID)
		}
		if len(incident.EvidenceSources) == 0 || len(incident.CausalPath) == 0 {
			return ResolvedDataset{}, fmt.Errorf("resolved incident %q lacks causal/evidence data", incident.IncidentID)
		}
		seen[incident.IncidentID] = true
	}
	return dataset, nil
}

// Package incident_retrieval contains the structured historical-incident
// benchmark. It is deliberately separate from log/template retrieval.
package incident_retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Incident struct {
	IncidentID       string            `yaml:"incident_id" json:"incident_id"`
	Category         string            `yaml:"category,omitempty" json:"category,omitempty"`
	Service          string            `yaml:"service" json:"service"`
	Namespace        string            `yaml:"namespace" json:"namespace"`
	Symptoms         []string          `yaml:"symptoms" json:"symptoms"`
	Metrics          []string          `yaml:"metrics" json:"metrics"`
	Logs             []string          `yaml:"logs" json:"logs"`
	Traces           []string          `yaml:"traces" json:"traces"`
	KubernetesEvents []string          `yaml:"kubernetes_events" json:"kubernetes_events"`
	TopologyGraph    TopologyGraph     `yaml:"topology_graph" json:"topology_graph"`
	CausalFeatures   []string          `yaml:"causal_features" json:"causal_features"`
	RootCause        string            `yaml:"root_cause" json:"-"`
	RelatedIncidents []string          `yaml:"related_incidents" json:"-"`
	GroundTruth      GroundTruth       `yaml:"ground_truth,omitempty" json:"-"`
	Metadata         map[string]string `yaml:"metadata,omitempty" json:"metadata,omitempty"`
}

// GroundTruth is evaluator-only. It is deliberately excluded from
// AgentContext and from all JSON representations used by retrieval.
type GroundTruth struct {
	RootCause      string   `yaml:"root_cause" json:"-"`
	CausalPath     []string `yaml:"causal_path" json:"-"`
	RecoveryAction string   `yaml:"recovery_action" json:"-"`
	EvidenceIDs    []string `yaml:"evidence_ids" json:"-"`
}

type TopologyGraph struct {
	Nodes []TopologyNode `yaml:"nodes" json:"nodes"`
	Edges []TopologyEdge `yaml:"edges" json:"edges"`
}

type TopologyNode struct {
	ID   string `yaml:"id" json:"id"`
	Type string `yaml:"type" json:"type"`
}

type TopologyEdge struct {
	Source string  `yaml:"source" json:"source"`
	Target string  `yaml:"target" json:"target"`
	Type   string  `yaml:"type" json:"type"`
	Weight float64 `yaml:"weight,omitempty" json:"weight,omitempty"`
}

// AgentContext returns the observation-only request. RootCause and
// RelatedIncidents are evaluator-only and cannot leak into an Agent prompt.
func (i Incident) AgentContext() map[string]any {
	return map[string]any{
		"incident_id":       i.IncidentID,
		"service":           i.Service,
		"namespace":         i.Namespace,
		"symptoms":          append([]string(nil), i.Symptoms...),
		"metrics":           append([]string(nil), i.Metrics...),
		"logs":              append([]string(nil), i.Logs...),
		"traces":            append([]string(nil), i.Traces...),
		"kubernetes_events": append([]string(nil), i.KubernetesEvents...),
		"topology_graph":    i.TopologyGraph,
		"causal_features":   append([]string(nil), i.CausalFeatures...),
		"metadata":          cloneMetadata(i.Metadata),
	}
}

type Dataset struct {
	Version   string     `yaml:"version" json:"version"`
	Incidents []Incident `yaml:"incidents" json:"incidents"`
	Hash      string     `json:"hash"`
}

func Load(path string) (Dataset, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Dataset{}, err
	}
	var dataset Dataset
	if err = yaml.Unmarshal(b, &dataset); err != nil {
		return Dataset{}, err
	}
	sum := sha256.Sum256(b)
	dataset.Hash = hex.EncodeToString(sum[:])
	if err = dataset.Validate(); err != nil {
		return Dataset{}, err
	}
	dataset = normalizeGroundTruth(dataset)
	return dataset, nil
}

// LoadExpanded loads the checked-in structured fixture and deterministically
// expands it to the requested query count. The expansion is part of the
// evaluator dataset preparation; generated records never enter AgentContext.
func LoadExpanded(path string, count int) (Dataset, error) {
	d, err := Load(path)
	if err != nil {
		return Dataset{}, err
	}
	if count <= len(d.Incidents) {
		return d, nil
	}
	return Expand(d, count), nil
}

// Expand creates stable, category-balanced structured incidents from the
// audited seed fixture. This keeps the repository compact while making the
// formal retrieval suite large and reproducible.
func Expand(d Dataset, count int) Dataset {
	if count <= len(d.Incidents) {
		return d
	}
	out := Dataset{Version: d.Version, Incidents: append([]Incident(nil), d.Incidents...), Hash: d.Hash}
	seedCount := len(out.Incidents)
	categoryOrder := []string{"memory", "database", "network", "deployment", "cpu"}
	categoryCounts := out.CategoryCounts()
	templates := map[string]Incident{}
	for _, incident := range out.Incidents {
		if _, exists := templates[incident.Category]; !exists {
			templates[incident.Category] = incident
		}
	}
	for i := range out.Incidents {
		out.Incidents[i] = normalizeIncident(out.Incidents[i])
	}
	for i := len(out.Incidents); i < count; i++ {
		category := categoryOrder[0]
		for _, candidate := range categoryOrder[1:] {
			if categoryCounts[candidate] < categoryCounts[category] {
				category = candidate
			}
		}
		base, ok := templates[category]
		if !ok {
			base = out.Incidents[i%seedCount]
		}
		id := fmt.Sprintf("incident-%s-%03d", category, i+1)
		base.IncidentID = id
		base.Category = category
		base.Metadata = cloneMetadata(base.Metadata)
		if base.Metadata == nil {
			base.Metadata = map[string]string{}
		}
		base.Metadata["dataset_record"] = "generated"
		base.Metadata["category"] = category
		if len(base.TopologyGraph.Nodes) > 0 {
			for n := range base.TopologyGraph.Nodes {
				if base.TopologyGraph.Nodes[n].Type == "service" {
					base.TopologyGraph.Nodes[n].ID = base.Service
				}
			}
		}
		base.RelatedIncidents = nil
		out.Incidents = append(out.Incidents, base)
		categoryCounts[category]++
	}
	for i := range out.Incidents {
		out.Incidents[i] = normalizeIncident(out.Incidents[i])
		for j := i - 1; j >= 0 && len(out.Incidents[i].RelatedIncidents) < 3; j-- {
			if out.Incidents[j].Category == out.Incidents[i].Category {
				out.Incidents[i].RelatedIncidents = append(out.Incidents[i].RelatedIncidents, out.Incidents[j].IncidentID)
			}
		}
	}
	return out
}

func (d Dataset) Validate() error {
	if d.Version == "" {
		return fmt.Errorf("dataset version is required")
	}
	if len(d.Incidents) == 0 {
		return fmt.Errorf("incident dataset is empty")
	}
	seen := map[string]bool{}
	for _, incident := range d.Incidents {
		if incident.IncidentID == "" || seen[incident.IncidentID] {
			return fmt.Errorf("invalid or duplicate incident_id %q", incident.IncidentID)
		}
		seen[incident.IncidentID] = true
		if incident.Service == "" || incident.Namespace == "" {
			return fmt.Errorf("incident %q requires service and namespace", incident.IncidentID)
		}
		if incident.RootCause == "" && incident.GroundTruth.RootCause == "" {
			return fmt.Errorf("incident %q requires evaluator root_cause", incident.IncidentID)
		}
	}
	return nil
}

type Query struct {
	ID      string         `json:"id"`
	Context map[string]any `json:"context"`
}

func (d Dataset) Queries() []Query {
	queries := make([]Query, 0, len(d.Incidents))
	for _, incident := range d.Incidents {
		queries = append(queries, Query{ID: incident.IncidentID, Context: incident.AgentContext()})
	}
	return queries
}

func cloneMetadata(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (d Dataset) IncidentIDs() []string {
	ids := make([]string, 0, len(d.Incidents))
	for _, incident := range d.Incidents {
		ids = append(ids, incident.IncidentID)
	}
	sort.Strings(ids)
	return ids
}

func normalizeGroundTruth(d Dataset) Dataset {
	for i := range d.Incidents {
		d.Incidents[i] = normalizeIncident(d.Incidents[i])
	}
	return d
}

func normalizeIncident(in Incident) Incident {
	if in.RootCause == "" {
		in.RootCause = in.GroundTruth.RootCause
	}
	if in.GroundTruth.RootCause == "" {
		in.GroundTruth.RootCause = in.RootCause
	}
	if in.Category == "" {
		in.Category = inferCategory(in.RootCause, 0)
	}
	if in.Metadata == nil {
		in.Metadata = map[string]string{}
	}
	in.Metadata["category"] = in.Category
	return in
}

func inferCategory(root string, index int) string {
	lower := strings.ToLower(root)
	for _, category := range []string{"memory", "database", "network", "deployment", "cpu"} {
		if strings.Contains(lower, category) {
			return category
		}
	}
	return []string{"memory", "database", "network", "deployment", "cpu"}[index%5]
}

// CategoryCounts is used by manifests and reports without exposing truth to
// the Agent.
func (d Dataset) CategoryCounts() map[string]int {
	out := map[string]int{}
	for _, in := range d.Incidents {
		category := in.Category
		if category == "" {
			category = inferCategory(in.RootCause, len(out))
		}
		out[category]++
	}
	return out
}

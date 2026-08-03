package history

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/kubepilot-aiops/kubepilot/retrieval"
	"gopkg.in/yaml.v3"
)

// Catalog is a versioned, held-out corpus of previously resolved incidents.
// It is deliberately independent from benchmark/incidents.yaml: benchmark
// scenarios and their ground truth must never be used to seed Agent memory.
type Catalog struct {
	Version   string   `yaml:"version"`
	Namespace string   `yaml:"namespace"`
	Services  []string `yaml:"services"`
	Incidents []Entry  `yaml:"incidents"`
}

type Entry struct {
	ID           string   `yaml:"id"`
	Category     string   `yaml:"category"`
	Variant      string   `yaml:"variant"`
	Observations []string `yaml:"observations"`
	RootCause    string   `yaml:"root_cause"`
	Recovery     string   `yaml:"recovery"`
}

type SeedDocument struct {
	Document retrieval.Document
	Text     string
}

func Load(path string) (Catalog, []SeedDocument, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, nil, "", err
	}
	var catalog Catalog
	if err := yaml.Unmarshal(b, &catalog); err != nil {
		return Catalog{}, nil, "", err
	}
	if err := validate(catalog); err != nil {
		return Catalog{}, nil, "", err
	}
	var docs []SeedDocument
	for _, service := range catalog.Services {
		for _, item := range catalog.Incidents {
			id := fmt.Sprintf("history-%s-%s", service, item.ID)
			docs = append(docs, SeedDocument{
				Document: retrieval.Document{
					ID: id, Service: service, Namespace: catalog.Namespace,
					Category: item.Category, Template: item.Variant,
					RootCause: item.RootCause, Recovery: item.Recovery,
				},
				Text: strings.Join(append([]string{service, catalog.Namespace}, item.Observations...), "\n"),
			})
		}
	}
	sum := sha256.Sum256(b)
	return catalog, docs, hex.EncodeToString(sum[:]), nil
}

func validate(c Catalog) error {
	if c.Version == "" || c.Namespace == "" || len(c.Services) == 0 || len(c.Incidents) == 0 {
		return fmt.Errorf("history catalog header is incomplete")
	}
	seen := map[string]bool{}
	for _, item := range c.Incidents {
		if item.ID == "" || item.Category == "" || item.Variant == "" || item.RootCause == "" || item.Recovery == "" || len(item.Observations) < 2 {
			return fmt.Errorf("history incident %q is incomplete", item.ID)
		}
		if seen[item.ID] {
			return fmt.Errorf("duplicate history incident %q", item.ID)
		}
		seen[item.ID] = true
	}
	return nil
}

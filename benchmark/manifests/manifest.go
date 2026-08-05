package manifests

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Manifest struct {
	Version         string                 `yaml:"version" json:"version"`
	CodeCommit      string                 `yaml:"code_commit" json:"code_commit"`
	Model           Model                  `yaml:"model" json:"model"`
	EmbeddingModel  string                 `yaml:"embedding_model" json:"embedding_model"`
	Reranker        Reranker               `yaml:"reranker" json:"reranker"`
	SkillHash       string                 `yaml:"skill_hash" json:"skill_hash"`
	RetrievalConfig map[string]any         `yaml:"retrieval_config" json:"retrieval_config"`
	BudgetConfig    map[string]any         `yaml:"budget_config" json:"budget_config"`
	DatasetVersion  string                 `yaml:"dataset_version" json:"dataset_version"`
	Datasets        map[string]DatasetSpec `yaml:"datasets" json:"datasets,omitempty"`
}

type DatasetSpec struct {
	Path           string         `yaml:"path" json:"path"`
	Size           int            `yaml:"size" json:"size"`
	CategoryCounts map[string]int `yaml:"category_counts,omitempty" json:"category_counts,omitempty"`
	GroundTruth    string         `yaml:"ground_truth" json:"ground_truth"`
}
type Model struct {
	Protocol   string `yaml:"protocol" json:"protocol"`
	Name       string `yaml:"name" json:"name"`
	ConfigHash string `yaml:"config_hash" json:"config_hash"`
}
type Reranker struct {
	Protocol   string `yaml:"protocol" json:"protocol"`
	Name       string `yaml:"name" json:"name"`
	ConfigHash string `yaml:"config_hash" json:"config_hash"`
}

func Load(path string) (Manifest, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, "", err
	}
	var m Manifest
	if err = yaml.Unmarshal(b, &m); err != nil {
		return Manifest{}, "", err
	}
	if err = m.Validate(); err != nil {
		return Manifest{}, "", err
	}
	sum := sha256.Sum256(b)
	return m, hex.EncodeToString(sum[:]), nil
}
func (m Manifest) Validate() error {
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("manifest version is required")
	}
	if strings.TrimSpace(m.DatasetVersion) == "" {
		return fmt.Errorf("dataset_version is required")
	}
	if m.RetrievalConfig == nil {
		return fmt.Errorf("retrieval_config is required")
	}
	if m.BudgetConfig == nil {
		return fmt.Errorf("budget_config is required")
	}
	return nil
}

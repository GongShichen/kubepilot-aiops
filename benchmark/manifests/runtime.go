package manifests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RuntimeManifest is the redacted environment snapshot for a full run. It
// deliberately stores endpoint hashes only; API keys and Authorization values
// are never read into the serialized structure.
type RuntimeManifest struct {
	BenchmarkVersion string          `json:"benchmark_version"`
	CodeCommit       string          `json:"code_commit"`
	Model            Model           `json:"model"`
	EmbeddingModel   string          `json:"embedding_model"`
	Reranker         RuntimeReranker `json:"reranker"`
	SkillHash        string          `json:"skill_hash"`
	RetrievalConfig  map[string]any  `json:"retrieval_config"`
	BudgetConfig     map[string]any  `json:"budget_config"`
	DatasetVersion   string          `json:"dataset_version"`
	Timestamp        time.Time       `json:"timestamp"`
}

type RuntimeReranker struct {
	Protocol     string `json:"protocol"`
	Model        string `json:"model"`
	Configured   bool   `json:"configured"`
	EndpointHash string `json:"endpoint_hash,omitempty"`
	ConfigHash   string `json:"config_hash"`
}

func RuntimeFromEnv(base Manifest, commit string, now time.Time) RuntimeManifest {
	chatEndpoint := strings.TrimRight(os.Getenv("CHAT_BASE_URL"), "/") + "/" + strings.TrimLeft(getenv("CHAT_API_PATH", "/chat/completions"), "/")
	chatHash := hashString(strings.Join([]string{getenv("CHAT_PROTOCOL", "openai-compatible"), os.Getenv("CHAT_MODEL"), chatEndpoint, getenv("CHAT_TIMEOUT", "60s"), getenv("CHAT_MAX_TOKENS", "")}, "\x00"))
	rerankerEndpoint := strings.TrimRight(os.Getenv("RERANKER_BASE_URL"), "/") + "/" + strings.TrimLeft(getenv("RERANKER_API_PATH", "/reranks"), "/")
	rerankerConfigured := strings.EqualFold(getenv("RERANKER_ENABLED", "false"), "true") && os.Getenv("RERANKER_BASE_URL") != "" && os.Getenv("RERANKER_MODEL") != ""
	rererankerConfig := hashString(strings.Join([]string{getenv("RERANKER_PROTOCOL", "openai-compatible"), os.Getenv("RERANKER_MODEL"), rerankerEndpoint, getenv("RERANKER_TIMEOUT", "30s")}, "\x00"))
	return RuntimeManifest{
		BenchmarkVersion: base.Version,
		CodeCommit:       commit,
		Model:            Model{Protocol: getenv("CHAT_PROTOCOL", "openai-compatible"), Name: os.Getenv("CHAT_MODEL"), ConfigHash: firstNonEmpty(os.Getenv("MODEL_CONFIG_HASH"), chatHash)},
		EmbeddingModel:   os.Getenv("EMBEDDING_MODEL"),
		Reranker:         RuntimeReranker{Protocol: getenv("RERANKER_PROTOCOL", "openai-compatible"), Model: os.Getenv("RERANKER_MODEL"), Configured: rerankerConfigured, EndpointHash: hashString(rerankerEndpoint), ConfigHash: rererankerConfig},
		SkillHash:        firstNonEmpty(os.Getenv("SKILL_SNAPSHOT_HASH"), hashFiles([]string{"internal/agent/skills/supervisor/SKILL.md", "internal/agent/skills/diagnosis/SKILL.md", "internal/agent/skills/recovery/SKILL.md"})),
		RetrievalConfig:  base.RetrievalConfig,
		BudgetConfig:     base.BudgetConfig,
		DatasetVersion:   base.DatasetVersion,
		Timestamp:        now.UTC(),
	}
}

func WriteRuntime(path string, runtime RuntimeManifest) error {
	if path == "" {
		return fmt.Errorf("runtime manifest path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(runtime, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o640)
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func hashFiles(paths []string) string {
	h := sha256.New()
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		_, _ = h.Write([]byte(path))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

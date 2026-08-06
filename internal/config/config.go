package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr           string
	APIToken           string
	WebhookToken       string
	DatabaseURL        string
	RedisURL           string
	Chat               ChatConfig
	Embedding          EmbeddingConfig
	PrometheusURL      string
	LokiURL            string
	JaegerURL          string
	MilvusAddress      string
	HistoryCollection  string
	LogIndexCollection string
	LogIndexerInterval time.Duration
	BusinessProbeURL   string
	Drain3URL          string
	Drain3Token        string
	Kubeconfig         string
	AllowedNamespaces  []string
	ConfigEnvFile      string
	ConfigReloadEvery  time.Duration
	ConfigRetryEvery   time.Duration
	Reasoning          ReasoningConfig
	Reranker           RerankerConfig
	AgentBudgets       AgentBudgetsConfig
}

type ReasoningConfig struct {
	SemanticTopK                 int
	LexicalTopK                  int
	TopologyTopK                 int
	RRFK                         int
	RerankTopK                   int
	ModelEvidenceMaxItems        int
	ModelContextMaxBytes         int
	CausalAutoActivateConfidence float64
	CausalLearningNamespaces     []string
	CausalPatternFile            string
	CausalPatternDirectory       string
	RankingPolicyFile            string
	ToolCostFile                 string
}

type RerankerConfig struct {
	Enabled          bool
	Protocol         string
	BaseURL          string
	APIPath          string
	APIKey           string
	Model            string
	Timeout          time.Duration
	MaxRetries       int
	MaxDocumentBytes int
	MaxPayloadBytes  int
}

type AgentBudgetConfig struct {
	MaxIterations  int
	MaxToolUses    int
	MaxTokens      int
	MaxCorrections int
}

type AgentBudgetsConfig struct {
	Supervisor AgentBudgetConfig
	Diagnosis  AgentBudgetConfig
	Recovery   AgentBudgetConfig
}

type ChatConfig struct {
	Protocol                 string
	BaseURL                  string
	APIPath                  string
	APIKey                   string
	Model                    string
	Timeout                  time.Duration
	MaxTokens                int
	Temperature              float64
	ReasoningEffort          string
	MaxRetries               int
	Concurrency              int
	InputPricePerMillion     float64
	OutputPricePerMillion    float64
	ReasoningPricePerMillion float64
}

type EmbeddingConfig struct {
	BaseURL         string
	APIPath         string
	APIKey          string
	Model           string
	Dimensions      int
	BatchSize       int
	Concurrency     int
	Timeout         time.Duration
	RequestInterval time.Duration
	MaxRetries      int
}

func Load() (Config, error) {
	chatTimeout, err := duration("CHAT_TIMEOUT", 60*time.Second)
	if err != nil {
		return Config{}, err
	}
	embedTimeout, err := duration("EMBEDDING_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	embedRequestInterval, err := duration("EMBEDDING_REQUEST_INTERVAL", time.Second)
	if err != nil {
		return Config{}, err
	}
	configReloadEvery, err := duration("CONFIG_RELOAD_INTERVAL", 2*time.Second)
	if err != nil {
		return Config{}, err
	}
	configRetryEvery, err := duration("CONFIG_RELOAD_RETRY_INTERVAL", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	logIndexerInterval, err := duration("LOG_INDEXER_INTERVAL", 2*time.Second)
	if err != nil {
		return Config{}, err
	}
	maxTokens, err := integer("CHAT_MAX_TOKENS", 8192)
	if err != nil {
		return Config{}, err
	}
	maxRetries, err := integer("CHAT_MAX_RETRIES", 3)
	if err != nil {
		return Config{}, err
	}
	chatConcurrency, err := integer("CHAT_CONCURRENCY", 4)
	if err != nil {
		return Config{}, err
	}
	dimensions, err := integer("EMBEDDING_DIMENSIONS", 1024)
	if err != nil {
		return Config{}, err
	}
	embeddingBatchSize, err := integer("EMBEDDING_BATCH_SIZE", 10)
	if err != nil {
		return Config{}, err
	}
	embeddingConcurrency, err := integer("EMBEDDING_CONCURRENCY", 1)
	if err != nil {
		return Config{}, err
	}
	embeddingRetries, err := integer("EMBEDDING_MAX_RETRIES", 3)
	if err != nil {
		return Config{}, err
	}
	temperature, err := decimal("CHAT_TEMPERATURE", 0)
	if err != nil {
		return Config{}, err
	}
	inputPrice, err := decimal("CHAT_INPUT_PRICE_PER_MILLION", 0)
	if err != nil {
		return Config{}, err
	}
	outputPrice, err := decimal("CHAT_OUTPUT_PRICE_PER_MILLION", 0)
	if err != nil {
		return Config{}, err
	}
	reasoningPrice, err := decimal("CHAT_REASONING_PRICE_PER_MILLION", 0)
	if err != nil {
		return Config{}, err
	}
	semanticTopK, err := integer("RETRIEVAL_SEMANTIC_TOP_K", 50)
	if err != nil {
		return Config{}, err
	}
	lexicalTopK, err := integer("RETRIEVAL_LEXICAL_TOP_K", 50)
	if err != nil {
		return Config{}, err
	}
	topologyTopK, err := integer("RETRIEVAL_TOPOLOGY_TOP_K", 50)
	if err != nil {
		return Config{}, err
	}
	rrfK, err := integer("RETRIEVAL_RRF_K", 60)
	if err != nil {
		return Config{}, err
	}
	rerankTopK, err := integer("RETRIEVAL_RERANK_TOP_K", 5)
	if err != nil {
		return Config{}, err
	}
	maxEvidence, err := integer("MODEL_EVIDENCE_MAX_ITEMS", 12)
	if err != nil {
		return Config{}, err
	}
	maxContextBytes, err := integer("MODEL_CONTEXT_MAX_BYTES", 32768)
	if err != nil {
		return Config{}, err
	}
	activateConfidence, err := decimal("CAUSAL_AUTO_ACTIVATE_CONFIDENCE", .90)
	if err != nil {
		return Config{}, err
	}
	rerankerTimeout, err := duration("RERANKER_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	rerankerRetries, err := integer("RERANKER_MAX_RETRIES", 3)
	if err != nil {
		return Config{}, err
	}
	rerankerDocumentBytes, err := integer("RERANKER_MAX_DOCUMENT_BYTES", 4096)
	if err != nil {
		return Config{}, err
	}
	rerankerPayloadBytes, err := integer("RERANKER_MAX_PAYLOAD_BYTES", 131072)
	if err != nil {
		return Config{}, err
	}
	budgets, err := loadAgentBudgets()
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTPAddr:      get("HTTP_ADDR", ":8080"),
		APIToken:      os.Getenv("API_TOKEN"),
		WebhookToken:  os.Getenv("ALERTMANAGER_WEBHOOK_TOKEN"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		RedisURL:      os.Getenv("REDIS_URL"),
		Chat:          ChatConfig{Protocol: get("CHAT_PROTOCOL", "openai-compatible"), BaseURL: os.Getenv("CHAT_BASE_URL"), APIPath: get("CHAT_API_PATH", "/chat/completions"), APIKey: os.Getenv("CHAT_API_KEY"), Model: os.Getenv("CHAT_MODEL"), Timeout: chatTimeout, MaxTokens: maxTokens, Temperature: temperature, ReasoningEffort: os.Getenv("CHAT_REASONING_EFFORT"), MaxRetries: maxRetries, Concurrency: chatConcurrency, InputPricePerMillion: inputPrice, OutputPricePerMillion: outputPrice, ReasoningPricePerMillion: reasoningPrice},
		Embedding:     EmbeddingConfig{BaseURL: os.Getenv("EMBEDDING_BASE_URL"), APIPath: get("EMBEDDING_API_PATH", "/embeddings"), APIKey: os.Getenv("EMBEDDING_API_KEY"), Model: os.Getenv("EMBEDDING_MODEL"), Dimensions: dimensions, BatchSize: embeddingBatchSize, Concurrency: embeddingConcurrency, Timeout: embedTimeout, RequestInterval: embedRequestInterval, MaxRetries: embeddingRetries},
		PrometheusURL: get("PROMETHEUS_URL", "http://localhost:9090"), LokiURL: get("LOKI_URL", "http://localhost:3100"), JaegerURL: get("JAEGER_URL", "http://localhost:16686"), MilvusAddress: get("MILVUS_ADDRESS", "localhost:19530"), HistoryCollection: get("HISTORY_COLLECTION", "kubepilot_history"), LogIndexCollection: get("LOG_INDEX_COLLECTION", "kubepilot_log_templates"), LogIndexerInterval: logIndexerInterval, BusinessProbeURL: os.Getenv("BUSINESS_PROBE_URL"),
		Drain3URL: get("DRAIN3_WS_URL", "ws://localhost:8081/ws/v1/parse"), Drain3Token: os.Getenv("DRAIN3_TOKEN"), Kubeconfig: os.Getenv("KUBECONFIG"), AllowedNamespaces: split(get("ALLOWED_NAMESPACES", "kubepilot-demo,kubepilot-benchmark")),
		ConfigEnvFile: os.Getenv("CONFIG_ENV_FILE"), ConfigReloadEvery: configReloadEvery, ConfigRetryEvery: configRetryEvery,
		Reasoning:    ReasoningConfig{SemanticTopK: semanticTopK, LexicalTopK: lexicalTopK, TopologyTopK: topologyTopK, RRFK: rrfK, RerankTopK: rerankTopK, ModelEvidenceMaxItems: maxEvidence, ModelContextMaxBytes: maxContextBytes, CausalAutoActivateConfidence: activateConfidence, CausalLearningNamespaces: split(get("CAUSAL_LEARNING_NAMESPACES", "kubepilot-demo")), CausalPatternFile: get("CAUSAL_PATTERN_FILE", "knowledge/causal_patterns.yaml"), CausalPatternDirectory: get("CAUSAL_PATTERN_DIR", "knowledge/patterns"), RankingPolicyFile: get("RANKING_POLICY_FILE", "knowledge/ranking_policy.yaml"), ToolCostFile: get("TOOL_COST_FILE", "internal/agent/skills/tool_costs.yaml")},
		Reranker:     RerankerConfig{Enabled: boolean("RERANKER_ENABLED", false), Protocol: get("RERANKER_PROTOCOL", "openai-compatible"), BaseURL: os.Getenv("RERANKER_BASE_URL"), APIPath: get("RERANKER_API_PATH", "/reranks"), APIKey: os.Getenv("RERANKER_API_KEY"), Model: os.Getenv("RERANKER_MODEL"), Timeout: rerankerTimeout, MaxRetries: rerankerRetries, MaxDocumentBytes: rerankerDocumentBytes, MaxPayloadBytes: rerankerPayloadBytes},
		AgentBudgets: budgets,
	}
	if err := cfg.ValidateBase(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) ValidateBase() error {
	if c.APIToken == "" {
		return errors.New("API_TOKEN is required")
	}
	if c.WebhookToken == "" {
		return errors.New("ALERTMANAGER_WEBHOOK_TOKEN is required")
	}
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	if c.RedisURL == "" {
		return errors.New("REDIS_URL is required")
	}
	if c.Chat.Protocol != "openai-compatible" && c.Chat.Protocol != "anthropic-compatible" {
		return fmt.Errorf("unsupported CHAT_PROTOCOL %q", c.Chat.Protocol)
	}
	if c.Reasoning.SemanticTopK <= 0 || c.Reasoning.LexicalTopK <= 0 || c.Reasoning.TopologyTopK <= 0 || c.Reasoning.RRFK <= 0 || c.Reasoning.RerankTopK <= 0 {
		return errors.New("retrieval top-k and RRF settings must be positive")
	}
	if c.Reasoning.ModelEvidenceMaxItems < 2 || c.Reasoning.ModelContextMaxBytes < 4096 {
		return errors.New("model evidence limits are too small")
	}
	if c.Reasoning.CausalAutoActivateConfidence < 0 || c.Reasoning.CausalAutoActivateConfidence > 1 {
		return errors.New("CAUSAL_AUTO_ACTIVATE_CONFIDENCE must be between 0 and 1")
	}
	if c.Reranker.Enabled {
		if err := ValidateReranker(c.Reranker); err != nil {
			return err
		}
	}
	return nil
}

func ValidateReranker(cfg RerankerConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.Protocol != "openai-compatible" {
		return fmt.Errorf("unsupported RERANKER_PROTOCOL %q", cfg.Protocol)
	}
	if cfg.BaseURL == "" || cfg.APIKey == "" || cfg.Model == "" {
		return errors.New("RERANKER_BASE_URL, RERANKER_API_KEY and RERANKER_MODEL are required when reranker is enabled")
	}
	if _, err := url.ParseRequestURI(cfg.BaseURL); err != nil {
		return fmt.Errorf("invalid RERANKER_BASE_URL: %w", err)
	}
	if cfg.MaxRetries < 0 || cfg.MaxRetries > 3 || cfg.MaxDocumentBytes < 256 || cfg.MaxPayloadBytes < 4096 {
		return errors.New("invalid reranker retry or payload limits")
	}
	return nil
}

func loadAgentBudgets() (AgentBudgetsConfig, error) {
	load := func(prefix string, defaults AgentBudgetConfig) (AgentBudgetConfig, error) {
		var err error
		if defaults.MaxIterations, err = integer(prefix+"_MAX_ITERATIONS", defaults.MaxIterations); err != nil {
			return AgentBudgetConfig{}, err
		}
		if defaults.MaxToolUses, err = integer(prefix+"_MAX_TOOL_USES", defaults.MaxToolUses); err != nil {
			return AgentBudgetConfig{}, err
		}
		if defaults.MaxTokens, err = integer(prefix+"_MAX_TOKENS", defaults.MaxTokens); err != nil {
			return AgentBudgetConfig{}, err
		}
		if defaults.MaxCorrections, err = integer(prefix+"_MAX_CORRECTIONS", defaults.MaxCorrections); err != nil {
			return AgentBudgetConfig{}, err
		}
		if defaults.MaxIterations <= 0 || defaults.MaxToolUses <= 0 || defaults.MaxTokens <= 0 || defaults.MaxCorrections < 0 {
			return AgentBudgetConfig{}, fmt.Errorf("%s budget values are invalid", prefix)
		}
		return defaults, nil
	}
	supervisor, err := load("SUPERVISOR", AgentBudgetConfig{MaxIterations: 10, MaxToolUses: 50, MaxTokens: 8192, MaxCorrections: 3})
	if err != nil {
		return AgentBudgetsConfig{}, err
	}
	diagnosis, err := load("DIAGNOSIS", AgentBudgetConfig{MaxIterations: 18, MaxToolUses: 50, MaxTokens: 8192, MaxCorrections: 3})
	if err != nil {
		return AgentBudgetsConfig{}, err
	}
	recovery, err := load("RECOVERY", AgentBudgetConfig{MaxIterations: 10, MaxToolUses: 50, MaxTokens: 8192, MaxCorrections: 2})
	if err != nil {
		return AgentBudgetsConfig{}, err
	}
	return AgentBudgetsConfig{Supervisor: supervisor, Diagnosis: diagnosis, Recovery: recovery}, nil
}

func (c Config) ValidateModel() error {
	return ValidateChat(c.Chat)
}

func ValidateChat(chat ChatConfig) error {
	if chat.BaseURL == "" || chat.APIKey == "" || chat.Model == "" {
		return errors.New("CHAT_BASE_URL, CHAT_API_KEY and CHAT_MODEL are required for model calls")
	}
	if chat.Protocol != "openai-compatible" && chat.Protocol != "anthropic-compatible" {
		return fmt.Errorf("unsupported CHAT_PROTOCOL %q", chat.Protocol)
	}
	if _, err := url.ParseRequestURI(chat.BaseURL); err != nil {
		return fmt.Errorf("invalid CHAT_BASE_URL: %w", err)
	}
	if chat.ReasoningEffort != "" && chat.ReasoningEffort != "low" && chat.ReasoningEffort != "medium" && chat.ReasoningEffort != "high" {
		return fmt.Errorf("CHAT_REASONING_EFFORT must be low, medium, or high")
	}
	if chat.MaxRetries < 0 || chat.MaxRetries > 3 {
		return fmt.Errorf("CHAT_MAX_RETRIES must be between 0 and 3")
	}
	if chat.Concurrency < 0 || chat.Concurrency > 64 {
		return fmt.Errorf("CHAT_CONCURRENCY must be between 1 and 64")
	}
	if chat.InputPricePerMillion < 0 || chat.OutputPricePerMillion < 0 || chat.ReasoningPricePerMillion < 0 {
		return errors.New("chat pricing values cannot be negative")
	}
	return nil
}

func (c Config) ValidateEmbedding() error {
	if c.Embedding.BaseURL == "" || c.Embedding.APIKey == "" || c.Embedding.Model == "" {
		return errors.New("EMBEDDING_BASE_URL, EMBEDDING_API_KEY and EMBEDDING_MODEL are required")
	}
	if c.Embedding.Dimensions <= 0 {
		return errors.New("EMBEDDING_DIMENSIONS must be positive")
	}
	if c.Embedding.BatchSize <= 0 || c.Embedding.BatchSize > 256 {
		return errors.New("EMBEDDING_BATCH_SIZE must be between 1 and 256")
	}
	if c.Embedding.Concurrency <= 0 || c.Embedding.Concurrency > 64 {
		return errors.New("EMBEDDING_CONCURRENCY must be between 1 and 64")
	}
	if c.Embedding.MaxRetries < 0 || c.Embedding.MaxRetries > 3 {
		return errors.New("EMBEDDING_MAX_RETRIES must be between 0 and 3")
	}
	return nil
}

func get(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func split(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}
func duration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}
func integer(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}
func decimal(key string, fallback float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func boolean(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

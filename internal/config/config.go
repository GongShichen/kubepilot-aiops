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
	HTTPAddr          string
	APIToken          string
	WebhookToken      string
	DatabaseURL       string
	RedisURL          string
	Chat              ChatConfig
	Embedding         EmbeddingConfig
	PrometheusURL     string
	LokiURL           string
	JaegerURL         string
	MilvusAddress     string
	HistoryCollection string
	Drain3URL         string
	Drain3Token       string
	Kubeconfig        string
	AllowedNamespaces []string
	ConfigEnvFile     string
	ConfigReloadEvery time.Duration
	ConfigRetryEvery  time.Duration
}

type ChatConfig struct {
	Protocol        string
	BaseURL         string
	APIPath         string
	APIKey          string
	Model           string
	Timeout         time.Duration
	MaxTokens       int
	Temperature     float64
	ReasoningEffort string
	MaxRetries      int
}

type EmbeddingConfig struct {
	BaseURL         string
	APIPath         string
	APIKey          string
	Model           string
	Dimensions      int
	Timeout         time.Duration
	RequestInterval time.Duration
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
	maxTokens, err := integer("CHAT_MAX_TOKENS", 4096)
	if err != nil {
		return Config{}, err
	}
	maxRetries, err := integer("CHAT_MAX_RETRIES", 1)
	if err != nil {
		return Config{}, err
	}
	dimensions, err := integer("EMBEDDING_DIMENSIONS", 1024)
	if err != nil {
		return Config{}, err
	}
	temperature, err := decimal("CHAT_TEMPERATURE", 0)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTPAddr:      get("HTTP_ADDR", ":8080"),
		APIToken:      os.Getenv("API_TOKEN"),
		WebhookToken:  os.Getenv("ALERTMANAGER_WEBHOOK_TOKEN"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		RedisURL:      os.Getenv("REDIS_URL"),
		Chat:          ChatConfig{Protocol: get("CHAT_PROTOCOL", "openai-compatible"), BaseURL: os.Getenv("CHAT_BASE_URL"), APIPath: get("CHAT_API_PATH", "/chat/completions"), APIKey: os.Getenv("CHAT_API_KEY"), Model: os.Getenv("CHAT_MODEL"), Timeout: chatTimeout, MaxTokens: maxTokens, Temperature: temperature, ReasoningEffort: os.Getenv("CHAT_REASONING_EFFORT"), MaxRetries: maxRetries},
		Embedding:     EmbeddingConfig{BaseURL: os.Getenv("EMBEDDING_BASE_URL"), APIPath: get("EMBEDDING_API_PATH", "/embeddings"), APIKey: os.Getenv("EMBEDDING_API_KEY"), Model: os.Getenv("EMBEDDING_MODEL"), Dimensions: dimensions, Timeout: embedTimeout, RequestInterval: embedRequestInterval},
		PrometheusURL: get("PROMETHEUS_URL", "http://localhost:9090"), LokiURL: get("LOKI_URL", "http://localhost:3100"), JaegerURL: get("JAEGER_URL", "http://localhost:16686"), MilvusAddress: get("MILVUS_ADDRESS", "localhost:19530"), HistoryCollection: get("HISTORY_COLLECTION", "kubepilot_history_v2"),
		Drain3URL: get("DRAIN3_WS_URL", "ws://localhost:8081/ws/v1/parse"), Drain3Token: os.Getenv("DRAIN3_TOKEN"), Kubeconfig: os.Getenv("KUBECONFIG"), AllowedNamespaces: split(get("ALLOWED_NAMESPACES", "kubepilot-demo,kubepilot-benchmark")),
		ConfigEnvFile: os.Getenv("CONFIG_ENV_FILE"), ConfigReloadEvery: configReloadEvery, ConfigRetryEvery: configRetryEvery,
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
	return nil
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
	return nil
}

func (c Config) ValidateEmbedding() error {
	if c.Embedding.BaseURL == "" || c.Embedding.APIKey == "" || c.Embedding.Model == "" {
		return errors.New("EMBEDDING_BASE_URL, EMBEDDING_API_KEY and EMBEDDING_MODEL are required")
	}
	if c.Embedding.Dimensions <= 0 {
		return errors.New("EMBEDDING_DIMENSIONS must be positive")
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

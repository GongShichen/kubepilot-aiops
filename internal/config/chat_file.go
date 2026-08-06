package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// LoadChatFile overlays CHAT_* values from a dotenv file onto fallback. It
// returns a value instead of mutating process environment, so a candidate
// configuration can be validated and probed before it becomes active.
func LoadChatFile(path string, fallback ChatConfig) (ChatConfig, error) {
	values, err := readEnvFile(path)
	if err != nil {
		return ChatConfig{}, err
	}
	chat := fallback
	assignString(values, "CHAT_PROTOCOL", &chat.Protocol)
	assignString(values, "CHAT_BASE_URL", &chat.BaseURL)
	assignString(values, "CHAT_API_PATH", &chat.APIPath)
	assignString(values, "CHAT_API_KEY", &chat.APIKey)
	assignString(values, "CHAT_MODEL", &chat.Model)
	assignString(values, "CHAT_REASONING_EFFORT", &chat.ReasoningEffort)
	if raw, ok := values["CHAT_TIMEOUT"]; ok {
		chat.Timeout, err = time.ParseDuration(raw)
		if err != nil {
			return ChatConfig{}, fmt.Errorf("CHAT_TIMEOUT: %w", err)
		}
	}
	if raw, ok := values["CHAT_MAX_TOKENS"]; ok {
		chat.MaxTokens, err = strconv.Atoi(raw)
		if err != nil {
			return ChatConfig{}, fmt.Errorf("CHAT_MAX_TOKENS: %w", err)
		}
	}
	if raw, ok := values["CHAT_TEMPERATURE"]; ok {
		chat.Temperature, err = strconv.ParseFloat(raw, 64)
		if err != nil {
			return ChatConfig{}, fmt.Errorf("CHAT_TEMPERATURE: %w", err)
		}
	}
	for key, target := range map[string]*float64{"CHAT_INPUT_PRICE_PER_MILLION": &chat.InputPricePerMillion, "CHAT_OUTPUT_PRICE_PER_MILLION": &chat.OutputPricePerMillion, "CHAT_REASONING_PRICE_PER_MILLION": &chat.ReasoningPricePerMillion} {
		if raw, ok := values[key]; ok {
			*target, err = strconv.ParseFloat(raw, 64)
			if err != nil {
				return ChatConfig{}, fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	if raw, ok := values["CHAT_MAX_RETRIES"]; ok {
		chat.MaxRetries, err = strconv.Atoi(raw)
		if err != nil {
			return ChatConfig{}, fmt.Errorf("CHAT_MAX_RETRIES: %w", err)
		}
	}
	if err = ValidateChat(chat); err != nil {
		return ChatConfig{}, err
	}
	return chat, nil
}

func LoadRerankerFile(path string, fallback RerankerConfig) (RerankerConfig, error) {
	values, err := readEnvFile(path)
	if err != nil {
		return RerankerConfig{}, err
	}
	cfg := fallback
	assignString(values, "RERANKER_PROTOCOL", &cfg.Protocol)
	assignString(values, "RERANKER_BASE_URL", &cfg.BaseURL)
	assignString(values, "RERANKER_API_PATH", &cfg.APIPath)
	assignString(values, "RERANKER_API_KEY", &cfg.APIKey)
	assignString(values, "RERANKER_MODEL", &cfg.Model)
	if raw, ok := values["RERANKER_ENABLED"]; ok {
		cfg.Enabled = raw == "1" || strings.EqualFold(raw, "true") || strings.EqualFold(raw, "yes") || strings.EqualFold(raw, "on")
	}
	if raw, ok := values["RERANKER_TIMEOUT"]; ok {
		cfg.Timeout, err = time.ParseDuration(raw)
		if err != nil {
			return RerankerConfig{}, fmt.Errorf("RERANKER_TIMEOUT: %w", err)
		}
	}
	for key, target := range map[string]*int{"RERANKER_MAX_RETRIES": &cfg.MaxRetries, "RERANKER_MAX_DOCUMENT_BYTES": &cfg.MaxDocumentBytes, "RERANKER_MAX_PAYLOAD_BYTES": &cfg.MaxPayloadBytes} {
		if raw, ok := values[key]; ok {
			*target, err = strconv.Atoi(raw)
			if err != nil {
				return RerankerConfig{}, fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	if err = ValidateReranker(cfg); err != nil {
		return RerankerConfig{}, err
	}
	return cfg, nil
}

func assignString(values map[string]string, key string, target *string) {
	if value, ok := values[key]; ok {
		*target = value
	}
}

func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNumber)
		}
		key = strings.TrimSpace(key)
		if !validEnvKey(key) {
			return nil, fmt.Errorf("%s:%d: invalid environment key", path, lineNumber)
		}
		value, parseErr := parseEnvValue(strings.TrimSpace(raw))
		if parseErr != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNumber, parseErr)
		}
		values[key] = value
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for index, char := range key {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || char == '_' || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}

func parseEnvValue(raw string) (string, error) {
	if len(raw) < 2 {
		return raw, nil
	}
	if raw[0] == '\'' {
		if raw[len(raw)-1] != '\'' {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		return raw[1 : len(raw)-1], nil
	}
	if raw[0] == '"' {
		if raw[len(raw)-1] != '"' {
			return "", fmt.Errorf("unterminated double-quoted value")
		}
		value, err := strconv.Unquote(raw)
		if err != nil {
			return "", err
		}
		return value, nil
	}
	return raw, nil
}

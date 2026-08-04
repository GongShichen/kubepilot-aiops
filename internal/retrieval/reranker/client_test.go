package reranker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/config"
)

func TestClientContractRetryAndRedaction(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/reranks" {
			t.Errorf("unexpected reranker API path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret-value" {
			t.Errorf("authorization header not configured")
		}
		var payload request
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload.ReturnDocuments || payload.Model != "bge-reranker-v2-m3" || payload.TopN != 2 {
			t.Errorf("unexpected request: %+v", payload)
		}
		if attempts.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"index": 1, "relevance_score": .9}, {"index": 0, "relevance_score": .1}}})
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	client := New(cfg)
	results, err := client.Rerank(context.Background(), "query", []string{"a", "b"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 || len(results) != 2 || results[0].Index != 1 {
		t.Fatalf("unexpected retry/result: attempts=%d results=%+v", attempts.Load(), results)
	}

	server.Close()
	_, err = client.Rerank(context.Background(), "query", []string{"a"}, 1)
	if err == nil || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("transport error leaked endpoint or secret: %v", err)
	}
}

func TestClientRejectsInvalidResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"index": 9, "relevance_score": .9}}})
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.MaxRetries = 0
	_, err := New(cfg).Rerank(context.Background(), "query", []string{"one"}, 1)
	if err == nil || !strings.Contains(err.Error(), "invalid index") {
		t.Fatalf("invalid response accepted: %v", err)
	}
}

func TestClientAcceptsAliyunOpenAICompatibleContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/compatible-api/v1/reranks" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "qwen3-rerank" || body["query"] == "" || body["top_n"] != float64(2) {
			t.Fatalf("unexpected compatible request: %+v", body)
		}
		if _, exists := body["return_documents"]; exists {
			t.Fatal("disabled return_documents must be omitted from the compatible request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "request-id",
			"model":   "qwen3-rerank",
			"object":  "list",
			"results": []map[string]any{{"index": 1, "relevance_score": .91}, {"index": 0, "relevance_score": .32}},
			"usage":   map[string]any{"total_tokens": 12},
		})
	}))
	defer server.Close()
	cfg := testConfig(server.URL + "/compatible-api/v1")
	cfg.Model = "qwen3-rerank"
	results, err := New(cfg).Rerank(context.Background(), "什么是重排序模型", []string{"候选一", "候选二"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Index != 1 || results[0].Score != .91 {
		t.Fatalf("unexpected compatible results: %+v", results)
	}
}

func TestHotClientReloadsChangedConfigurationImmediately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"index": 0, "relevance_score": .2}, {"index": 1, "relevance_score": .8}}})
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), ".env")
	writeRerankerEnv(t, path, server.URL, "model-a")
	hot := NewHotClient(config.RerankerConfig{Timeout: time.Second, MaxDocumentBytes: 1024, MaxPayloadBytes: 65536}, path, 5*time.Millisecond, time.Hour)
	if err := hot.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := hot.ConfigHash()
	writeRerankerEnv(t, path, server.URL, "model-b")
	if err := hot.refresh(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if hot.ConfigHash() == first || hot.Health()["desired_model"] != "model-b" {
		t.Fatalf("changed dotenv candidate was not activated: %+v", hot.Health())
	}
}

func testConfig(baseURL string) config.RerankerConfig {
	return config.RerankerConfig{Enabled: true, Protocol: "openai-compatible", BaseURL: baseURL, APIPath: "/reranks", APIKey: "secret-value", Model: "bge-reranker-v2-m3", Timeout: 300 * time.Millisecond, MaxRetries: 1, MaxDocumentBytes: 1024, MaxPayloadBytes: 65536}
}

func writeRerankerEnv(t *testing.T, path, baseURL, model string) {
	t.Helper()
	content := fmt.Sprintf("RERANKER_ENABLED=true\nRERANKER_PROTOCOL=openai-compatible\nRERANKER_BASE_URL=%s\nRERANKER_API_PATH=/reranks\nRERANKER_API_KEY=test-only\nRERANKER_MODEL=%s\nRERANKER_TIMEOUT=1s\nRERANKER_MAX_RETRIES=0\nRERANKER_MAX_DOCUMENT_BYTES=1024\nRERANKER_MAX_PAYLOAD_BYTES=65536\n", baseURL, model)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/config"
)

func TestEmbedderRetriesRateLimit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[{"index":0,"embedding":[0.1,0.2]}]}`)
	}))
	defer server.Close()

	embedder := NewEmbedder(config.EmbeddingConfig{
		BaseURL: server.URL, APIPath: "/embeddings", APIKey: "secret",
		Model: "test", Dimensions: 2, Timeout: 3 * time.Second,
	})
	vectors, err := embedder.Embed(context.Background(), []string{"one"})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(vectors) != 1 || len(vectors[0]) != 2 {
		t.Fatalf("calls=%d vectors=%v", calls.Load(), vectors)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestEmbedderRetriesTransientNetworkError(t *testing.T) {
	var calls atomic.Int32
	embedder := NewEmbedder(config.EmbeddingConfig{
		BaseURL: "https://embedding.invalid", APIPath: "/embeddings", APIKey: "secret",
		Model: "test", Dimensions: 2, Timeout: 3 * time.Second,
	})
	embedder.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"index":0,"embedding":[0.1,0.2]}]}`)),
		}, nil
	})
	if _, err := embedder.Embed(context.Background(), []string{"one"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d, want 2", calls.Load())
	}
}

func TestEmbedderDoesNotRetryNonRateLimitClientError(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid input"}}`))
	}))
	defer server.Close()

	embedder := NewEmbedder(config.EmbeddingConfig{
		BaseURL: server.URL, APIPath: "/embeddings", APIKey: "secret",
		Model: "test", Dimensions: 2, Timeout: time.Second,
	})
	if _, err := embedder.Embed(context.Background(), []string{"one"}); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1", calls.Load())
	}
}

func TestEmbedderSplitsConfiguredBatches(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Input) > 2 {
			t.Fatalf("oversized batch: %d", len(request.Input))
		}
		data := make([]map[string]any, len(request.Input))
		for index := range request.Input {
			data[index] = map[string]any{"index": index, "embedding": []float32{0.1, 0.2}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	embedder := NewEmbedder(config.EmbeddingConfig{BaseURL: server.URL, APIPath: "/embeddings", APIKey: "secret", Model: "test", Dimensions: 2, BatchSize: 2, Timeout: time.Second})
	vectors, err := embedder.Embed(context.Background(), []string{"1", "2", "3", "4", "5"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 5 || calls.Load() != 3 {
		t.Fatalf("vectors=%d calls=%d", len(vectors), calls.Load())
	}
}

func TestEmbedderRedactsProviderEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"https://embedding.example.test/v1 key=secret"}`))
	}))
	defer server.Close()
	embedder := NewEmbedder(config.EmbeddingConfig{BaseURL: server.URL, APIPath: "/embeddings", APIKey: "secret", Model: "test", Dimensions: 2, BatchSize: 1, Timeout: time.Second})
	_, err := embedder.Embed(context.Background(), []string{"one"})
	if err == nil {
		t.Fatal("expected provider error")
	}
	if strings.Contains(err.Error(), "embedding.example.test") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("provider error was not redacted: %s", err)
	}
}

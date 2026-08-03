package model

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/config"
)

func TestOpenAICompatible(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing auth")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer s.Close()
	c := New(config.ChatConfig{Protocol: "openai-compatible", BaseURL: s.URL, APIPath: "/chat/completions", APIKey: "secret", Model: "test", Timeout: time.Second})
	r, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || r.Content != "ok" {
		t.Fatalf("%v %#v", err, r)
	}
}

func TestModelRetriesTransientEndpointFailure(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer s.Close()
	c := New(config.ChatConfig{Protocol: "openai-compatible", BaseURL: s.URL, APIPath: "/chat/completions", Model: "test", Timeout: time.Second, MaxRetries: 1})
	result, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || result.Content != "ok" || requests.Load() != 2 {
		t.Fatalf("requests=%d result=%#v err=%v", requests.Load(), result, err)
	}
}

func TestModelDoesNotRetryPermanentEndpointFailure(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer s.Close()
	c := New(config.ChatConfig{Protocol: "openai-compatible", BaseURL: s.URL, APIPath: "/chat/completions", Model: "test", Timeout: time.Second, MaxRetries: 1})
	_, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || requests.Load() != 1 {
		t.Fatalf("requests=%d err=%v", requests.Load(), err)
	}
}

func TestUnexpectedEOFFromStreamIsRetryable(t *testing.T) {
	if !retryableModelError(io.ErrUnexpectedEOF) {
		t.Fatal("unexpected EOF should be retried")
	}
}

func TestOpenAICompatibleToolCallStringArguments(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"tools"`) {
			t.Error("missing tools request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"kubepilot_capability_probe","arguments":"{\"nonce\":\"kubepilot-probe\"}"}}]}}],"usage":{"prompt_tokens":8,"completion_tokens":3}}`))
	}))
	defer s.Close()
	c := New(config.ChatConfig{Protocol: "openai-compatible", BaseURL: s.URL, APIPath: "/chat/completions", APIKey: "secret", Model: "test", Timeout: time.Second})
	if err := c.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeOpenAIArgumentsObject(t *testing.T) {
	raw, err := normalizeOpenAIArguments([]byte(`{"nonce":"kubepilot-probe"}`))
	if err != nil || string(raw) != `{"nonce":"kubepilot-probe"}` {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
}

func TestOpenAICompatibleStreamingToolCall(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"stream":true`) {
			t.Error("streaming was not enabled")
		}
		if !strings.Contains(string(body), `"reasoning_effort":"low"`) {
			t.Error("reasoning effort was not forwarded")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"function\":{\"name\":\"kubepilot_capability_probe\",\"arguments\":\"{\\\"nonce\\\":\\\"kubepilot-\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"probe\\\"}\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4}}\n\ndata: [DONE]\n\n")
	}))
	defer s.Close()
	c := New(config.ChatConfig{Protocol: "openai-compatible", BaseURL: s.URL, APIPath: "/chat/completions", APIKey: "secret", Model: "test", Timeout: time.Second, ReasoningEffort: "low"})
	if err := c.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicCompatibleStreamingToolCall(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"stream":true`) {
			t.Error("streaming was not enabled")
		}
		if r.Header.Get("x-api-key") != "secret" {
			t.Error("missing Anthropic authentication")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool-1\",\"name\":\"kubepilot_capability_probe\",\"input\":{}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"nonce\\\":\\\"kubepilot-\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"probe\\\"}\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer s.Close()
	c := New(config.ChatConfig{Protocol: "anthropic-compatible", BaseURL: s.URL, APIPath: "/v1/messages", APIKey: "secret", Model: "test", Timeout: time.Second, MaxTokens: 128})
	if err := c.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
}

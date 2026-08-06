package model

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/config"
)

func TestEinoOpenAICompatibleStreamingToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/chat" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("path=%s authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"reasoning_effort":"low"`) {
			t.Errorf("reasoning effort was not sent: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"probe\",\"arguments\":\"{\\\"nonce\\\":\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"ok\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	chat, err := NewEinoChatModel(context.Background(), config.ChatConfig{Protocol: "openai-compatible", BaseURL: server.URL, APIPath: "/custom/chat", APIKey: "secret", Model: "test", Timeout: time.Second, MaxTokens: 64, ReasoningEffort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	assertEinoToolStream(t, chat)
}

func TestEinoCustomAPIPathPreservesBaseURLPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gateway/v1/chat/completions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"probe\",\"arguments\":\"{\\\"nonce\\\":\\\"ok\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	chat, err := NewEinoChatModel(context.Background(), config.ChatConfig{Protocol: "openai-compatible", BaseURL: server.URL + "/gateway/v1", APIPath: "/chat/completions", APIKey: "secret", Model: "test", Timeout: time.Second, MaxTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	assertEinoToolStream(t, chat)
}

func TestEinoAnthropicCompatibleStreamingToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/messages" || r.Header.Get("x-api-key") != "secret" {
			t.Errorf("path=%s api-key=%q", r.URL.Path, r.Header.Get("x-api-key"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"test\",\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call-1\",\"name\":\"probe\",\"input\":{}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"nonce\\\":\\\"ok\\\"}\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":4}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	chat, err := NewEinoChatModel(context.Background(), config.ChatConfig{Protocol: "anthropic-compatible", BaseURL: server.URL, APIPath: "/custom/messages", APIKey: "secret", Model: "test", Timeout: time.Second, MaxTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	assertEinoToolStream(t, chat)
}

func TestEinoStreamingRetriesBoundedTransientResponse(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"probe\",\"arguments\":\"{\\\"nonce\\\":\\\"ok\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	chat, err := NewEinoChatModel(context.Background(), config.ChatConfig{Protocol: "openai-compatible", BaseURL: server.URL, APIPath: "/chat", APIKey: "secret", Model: "test", Timeout: 2 * time.Second, MaxTokens: 64, MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertEinoToolStream(t, chat)
	if requests.Load() != 2 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestEinoStreamingBodyIsNotCutOffByHeaderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test server does not support flushing")
		}
		_, _ = io.WriteString(w, `data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"probe","arguments":"{\"ok\":\""}}]}}]}`+"\n\n")
		flusher.Flush()
		time.Sleep(60 * time.Millisecond)
		_, _ = io.WriteString(w, `data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ok\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()
	chat, err := NewEinoChatModel(context.Background(), config.ChatConfig{Protocol: "openai-compatible", BaseURL: server.URL, APIPath: "/chat", APIKey: "secret", Model: "test", Timeout: 20 * time.Millisecond, MaxTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	assertEinoToolStream(t, chat)
}

func TestEinoStreamingChunksResetIdleTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test server does not support flushing")
		}
		chunks := []string{
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"probe","arguments":"{\"ok\":\""}}]}}]}`,
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"pa"}}]}}]}`,
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ss"}}]}}]}`,
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]},"finish_reason":"tool_calls"}]}`,
		}
		for index, chunk := range chunks {
			_, _ = io.WriteString(w, chunk+"\n\n")
			flusher.Flush()
			if index < len(chunks)-1 {
				time.Sleep(12 * time.Millisecond)
			}
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()
	chat, err := NewEinoChatModel(context.Background(), config.ChatConfig{Protocol: "openai-compatible", BaseURL: server.URL, APIPath: "/chat", APIKey: "secret", Model: "test", Timeout: 20 * time.Millisecond, MaxRetries: 1, MaxTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	assertEinoToolStream(t, chat)
}

func TestEinoStreamingIdleTimeoutCancelsStalledResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test server does not support flushing")
		}
		_, _ = io.WriteString(w, `data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}\n\n`)
		flusher.Flush()
		time.Sleep(80 * time.Millisecond)
	}))
	defer server.Close()
	chat, err := NewEinoChatModel(context.Background(), config.ChatConfig{Protocol: "openai-compatible", BaseURL: server.URL, APIPath: "/chat", APIKey: "secret", Model: "test", Timeout: 20 * time.Millisecond, MaxRetries: 1, MaxTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := chat.Stream(context.Background(), []*schema.Message{schema.UserMessage("wait")})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for {
		_, recvErr := reader.Recv()
		if recvErr == nil {
			continue
		}
		if strings.Contains(strings.ToLower(recvErr.Error()), "context deadline exceeded") {
			return
		}
		if errors.Is(recvErr, io.EOF) {
			t.Fatal("stream ended before the idle timeout")
		}
		t.Fatalf("unexpected stream error: %v", recvErr)
	}
}

func assertEinoToolStream(t *testing.T, chat einomodel.BaseChatModel) {
	t.Helper()
	reader, err := chat.Stream(context.Background(), []*schema.Message{schema.UserMessage("call probe")}, einomodel.WithTools([]*schema.ToolInfo{{Name: "probe", Desc: "probe capability"}}))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var chunks []*schema.Message
	for {
		chunk, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		chunks = append(chunks, chunk)
	}
	message, err := schema.ConcatMessages(chunks)
	if err != nil {
		t.Fatal(err)
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name != "probe" || !strings.Contains(message.ToolCalls[0].Function.Arguments, "ok") {
		t.Fatalf("unexpected merged tool call: %#v", message.ToolCalls)
	}
}

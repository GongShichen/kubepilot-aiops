package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/config"
)

func TestHotClientEnforcesGlobalModelConcurrency(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "kubepilot_capability_probe") && strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"probe\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"probe\",\"type\":\"function\",\"function\":{\"name\":\"kubepilot_capability_probe\",\"arguments\":\"{\\\"nonce\\\":\\\"kubepilot-probe\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		active.Add(-1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), ".env")
	writeChatEnv(t, path, server.URL, "limited-model")
	hot := NewHotClient(config.ChatConfig{Concurrency: 2}, path, time.Millisecond, time.Millisecond)
	if err := hot.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := hot.Generate(context.Background(), []*schema.Message{schema.UserMessage("test")}); err != nil {
				t.Errorf("Generate: %v", err)
			}
		}()
	}
	wait.Wait()
	if maximum.Load() != 2 {
		t.Fatalf("maximum model concurrency=%d, want 2", maximum.Load())
	}
}

func TestHotClientSwitchesAtomicallyAndPinsWorkflowSnapshot(t *testing.T) {
	first := modelServer(t, "first")
	defer first.Close()
	second := modelServer(t, "second")
	defer second.Close()

	path := filepath.Join(t.TempDir(), ".env")
	writeChatEnv(t, path, first.URL, "model-one")
	hot := NewHotClient(config.ChatConfig{}, path, time.Millisecond, time.Millisecond)
	if err := hot.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstHash, _, _ := hot.SnapshotIdentity()
	pinned := hot.WithSnapshot(context.Background())

	writeChatEnv(t, path, second.URL, "model-two")
	if err := hot.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	secondHash, _, _ := hot.SnapshotIdentity()
	if firstHash == "" || firstHash == secondHash {
		t.Fatalf("model snapshot hashes did not change: %q %q", firstHash, secondHash)
	}
	hashContext, err := hot.WithSnapshotHash(context.Background(), firstHash)
	if err != nil {
		t.Fatal(err)
	}
	hashMessage, err := hot.Generate(hashContext, []*schema.Message{schema.UserMessage("test")})
	if err != nil || hashMessage.Content != "first" {
		t.Fatalf("checkpoint snapshot=%#v err=%v", hashMessage, err)
	}
	current, err := hot.Complete(context.Background(), []Message{{Role: "user", Content: "test"}}, nil)
	if err != nil || current.Content != "second" {
		t.Fatalf("current=%#v err=%v", current, err)
	}
	previous, err := hot.Complete(pinned, []Message{{Role: "user", Content: "test"}}, nil)
	if err != nil || previous.Content != "first" {
		t.Fatalf("snapshot=%#v err=%v", previous, err)
	}
	health := hot.Health()
	if health["configured"] != true || health["model"] != "model-two" || health["active_model"] != "model-two" {
		t.Fatalf("unexpected health: %#v", health)
	}
}

func TestHotClientKeepsActiveModelWhenCandidateFails(t *testing.T) {
	working := modelServer(t, "working")
	defer working.Close()
	path := filepath.Join(t.TempDir(), ".env")
	writeChatEnv(t, path, working.URL, "working-model")
	hot := NewHotClient(config.ChatConfig{}, path, time.Millisecond, time.Millisecond)
	if err := hot.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}

	writeChatEnv(t, path, "http://127.0.0.1:1", "broken-model")
	if err := hot.Probe(context.Background()); err == nil {
		t.Fatal("broken candidate should fail its probe")
	}
	response, err := hot.Complete(context.Background(), []Message{{Role: "user", Content: "test"}}, nil)
	if err != nil || response.Content != "working" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	health := hot.Health()
	if health["configured"] != true || health["model"] != "broken-model" || health["active_model"] != "working-model" {
		t.Fatalf("unexpected health: %#v", health)
	}
}

func TestHotClientRunDetectsFileChange(t *testing.T) {
	first := modelServer(t, "first")
	defer first.Close()
	second := modelServer(t, "second")
	defer second.Close()
	path := filepath.Join(t.TempDir(), ".env")
	writeChatEnv(t, path, first.URL, "model-one")
	hot := NewHotClient(config.ChatConfig{}, path, 10*time.Millisecond, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hot.Run(ctx)
	waitForActiveModel(t, hot, "model-one")

	writeChatEnv(t, path, second.URL, "model-two")
	waitForActiveModel(t, hot, "model-two")
}

func TestHotClientSnapshotPinsUnavailableState(t *testing.T) {
	server := modelServer(t, "ready")
	defer server.Close()
	path := filepath.Join(t.TempDir(), ".env")
	writeChatEnv(t, path, server.URL, "model-one")
	hot := NewHotClient(config.ChatConfig{}, path, time.Millisecond, time.Millisecond)
	pinned := hot.WithSnapshot(context.Background())
	if err := hot.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := hot.Complete(pinned, []Message{{Role: "user", Content: "test"}}, nil); err == nil || err.Error() != "model is not ready" {
		t.Fatalf("unavailable snapshot should remain unavailable, got %v", err)
	}
}

func TestSafeModelErrorRedactsKeyAndEndpoint(t *testing.T) {
	err := safeModelError(errors.New(`POST "https://models.example.test/v1/chat": key=secret-key`), "secret-key")
	if strings.Contains(err.Error(), "models.example.test") || strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("model error was not redacted: %s", err)
	}
}

func modelServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "kubepilot_capability_probe") && strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"probe\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"probe\",\"type\":\"function\",\"function\":{\"name\":\"kubepilot_capability_probe\",\"arguments\":\"{\\\"nonce\\\":\\\"kubepilot-probe\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, content)
	}))
}

func writeChatEnv(t *testing.T, path, baseURL, model string) {
	t.Helper()
	contents := fmt.Sprintf("CHAT_PROTOCOL=openai-compatible\nCHAT_BASE_URL=%s\nCHAT_API_PATH=/chat/completions\nCHAT_API_KEY=secret\nCHAT_MODEL=%s\nCHAT_TIMEOUT=100ms\nCHAT_MAX_RETRIES=0\nCHAT_MAX_TOKENS=128\nCHAT_TEMPERATURE=0\n", baseURL, model)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForActiveModel(t *testing.T, hot *HotClient, model string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hot.Health()["active_model"] == model {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("model %q was not activated; health=%#v", model, hot.Health())
}

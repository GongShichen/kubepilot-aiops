package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadChatFileOverlaysFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	contents := "# model\nexport CHAT_PROTOCOL=openai-compatible\nCHAT_BASE_URL='https://example.com/api'\nCHAT_API_PATH=/chat/completions\nCHAT_API_KEY=key=with=equals\nCHAT_MODEL=next-model\nCHAT_TIMEOUT=45s\nCHAT_MAX_RETRIES=3\nCHAT_MAX_TOKENS=2048\nCHAT_TEMPERATURE=0.2\nCHAT_REASONING_EFFORT=low\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	chat, err := LoadChatFile(path, ChatConfig{APIPath: "/fallback", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if chat.Model != "next-model" || chat.APIKey != "key=with=equals" || chat.Timeout != 45*time.Second || chat.MaxRetries != 3 || chat.MaxTokens != 2048 || chat.Temperature != 0.2 {
		t.Fatalf("unexpected chat config: %#v", chat)
	}
}

func TestLoadChatFileRejectsInvalidCandidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("CHAT_MODEL=broken\nCHAT_TIMEOUT=not-a-duration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadChatFile(path, ChatConfig{Protocol: "openai-compatible", BaseURL: "https://example.com", APIKey: "secret"})
	if err == nil {
		t.Fatal("invalid candidate should be rejected")
	}
}

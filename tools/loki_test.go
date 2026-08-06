package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLokiProbeIngestionRequiresAcceptedWrite(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/loki/api/v1/push" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := NewLoki(server.URL).ProbeIngestion(context.Background()); err != nil {
		t.Fatal(err)
	}
	streams, ok := payload["streams"].([]any)
	if !ok || len(streams) != 1 {
		t.Fatalf("unexpected probe payload: %#v", payload)
	}
}

func TestLokiProbeIngestionRejectsUnavailableIngester(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "ingester unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()
	if err := NewLoki(server.URL).ProbeIngestion(context.Background()); err == nil {
		t.Fatal("unavailable Loki write was accepted")
	}
}

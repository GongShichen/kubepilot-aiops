package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestCreateDoesNotLeakGroundTruth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		summary, _ := body["summary"].(string)
		if strings.Contains(summary, "busy loop") || strings.Contains(summary, "cpu-busy_loop") {
			t.Fatalf("ground truth leaked into summary: %q", summary)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"incident-1","status":"RECEIVED"}`))
	}))
	defer server.Close()
	client := NewHTTPClient(server.URL, "token")
	_, err := client.Create(context.Background(), scenarios.Scenario{ID: "cpu-busy_loop-01", Service: "gateway-service", Namespace: "kubepilot-benchmark", Target: "gateway-service", GroundTruth: scenarios.GroundTruth{RootCauseDetail: "uncontrolled CPU busy loop"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHTTPClientRetriesEveryRequestThreeTimes(t *testing.T) {
	var requests atomic.Int32
	var firstKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if firstKey == "" {
			firstKey = r.Header.Get("Idempotency-Key")
		} else if r.Header.Get("Idempotency-Key") != firstKey {
			t.Fatalf("retry changed idempotency key")
		}
		if requests.Add(1) <= 3 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"incident-1","status":"RECEIVED"}`))
	}))
	defer server.Close()
	client := NewHTTPClient(server.URL, "token")
	_, err := client.Create(context.Background(), scenarios.Scenario{Service: "gateway-service", Namespace: "kubepilot-benchmark", Target: "gateway-service"})
	if err != nil {
		t.Fatalf("request should succeed after retries: %v", err)
	}
	if got := requests.Load(); got != 4 {
		t.Fatalf("requests=%d, want initial request plus three retries", got)
	}
}

func TestHTTPClientReadinessGetAndApproval(t *testing.T) {
	var approved atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/runtime/readiness":
			_, _ = w.Write([]byte(`{"components":{"postgres":"ready","reranker":"disabled"}}`))
		case r.URL.Path == "/api/v1/incidents/incident-1" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"incident-1","status":"AWAITING_APPROVAL","recovery_proposal":{"id":"proposal-1"}}`))
		case r.URL.Path == "/api/v1/incidents/incident-1/approval" && r.Method == http.MethodPost:
			if r.Header.Get("Idempotency-Key") == "" {
				t.Fatal("approval request had no idempotency key")
			}
			approved.Store(true)
			_, _ = w.Write([]byte(`{"id":"incident-1","status":"RECOVERING"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewHTTPClient(server.URL, "token")
	if err := client.Preflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	incident, err := client.Get(context.Background(), "incident-1")
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Approve(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	if !approved.Load() {
		t.Fatal("approval endpoint was not called")
	}
	if err = client.Approve(context.Background(), &domain.Incident{ID: "missing-proposal"}); err == nil {
		t.Fatal("approval without a proposal was accepted")
	}
}

func TestHTTPClientPreflightRejectsUnreadyComponent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"components":{"postgres":"unavailable"}}`))
	}))
	defer server.Close()
	if err := NewHTTPClient(server.URL, "token").Preflight(context.Background()); err == nil || !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("unexpected readiness result: %v", err)
	}
}

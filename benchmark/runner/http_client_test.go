package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
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

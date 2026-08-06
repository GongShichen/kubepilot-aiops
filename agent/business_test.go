package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestBusinessProbeReportsApplicationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	evidence, err := (BusinessProbeCollector{URL: server.URL}).Collect(context.Background(), &domain.Incident{Service: "gateway"}, domain.EvidenceRequest{})
	if err != nil {
		t.Fatal(err)
	}
	probe := evaluateVerificationEvidence("business", evidence)
	if !probe.Applicable || probe.Success {
		t.Fatalf("unexpected probe result: %#v", probe)
	}
}

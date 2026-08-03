package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type HTTPClient struct {
	BaseURL, Token  string
	DiagnosisMethod string
	HTTP            *http.Client
}

func NewHTTPClient(base, token string) *HTTPClient {
	return &HTTPClient{BaseURL: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}
func (c *HTTPClient) do(ctx context.Context, method, path string, body any, out any) error {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, _ := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Idempotency-Key", fmt.Sprintf("bench-%d", time.Now().UnixNano()))
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("agent status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
func (c *HTTPClient) Create(ctx context.Context, s scenarios.Scenario) (*domain.Incident, error) {
	body := map[string]any{"severity": "critical", "service": s.Service, "namespace": s.Namespace, "resource": s.Target, "summary": "Service degradation detected during an isolated observation window; determine the root cause from collected evidence.", "diagnosis_method": c.DiagnosisMethod, "evidence_start_at": time.Now().UTC().Add(-40 * time.Second)}
	var out domain.Incident
	err := c.do(ctx, http.MethodPost, "/api/v1/incidents", body, &out)
	return &out, err
}
func (c *HTTPClient) Get(ctx context.Context, id string) (*domain.Incident, error) {
	var out domain.Incident
	err := c.do(ctx, http.MethodGet, "/api/v1/incidents/"+id, nil, &out)
	return &out, err
}
func (c *HTTPClient) Approve(ctx context.Context, in *domain.Incident) error {
	if in.Proposal == nil {
		return fmt.Errorf("incident has no proposal")
	}
	body := map[string]any{"proposal_id": in.Proposal.ID, "decision": "approve", "comment": "safe benchmark auto-approval"}
	var out domain.Incident
	return c.do(ctx, http.MethodPost, "/api/v1/incidents/"+in.ID+"/approval", body, &out)
}

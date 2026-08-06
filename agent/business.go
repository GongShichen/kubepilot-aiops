package agent

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// BusinessProbeCollector is invoked only through verify_business_probe in the
// Eino Verification ToolsNode. The endpoint is operator-configured and its
// response body is intentionally never persisted.
type BusinessProbeCollector struct {
	URL    string
	Client *http.Client
}

func (c BusinessProbeCollector) Collect(ctx context.Context, in *domain.Incident, _ domain.EvidenceRequest) ([]domain.Evidence, error) {
	if c.URL == "" {
		return nil, nil
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	observedAt := time.Now().UTC()
	success := response.StatusCode >= 200 && response.StatusCode < 300
	return []domain.Evidence{{
		Source: "business", Type: "business_probe", Timestamp: observedAt,
		Namespace: in.Namespace, Service: in.Service, Resource: in.Resource,
		Summary:    fmt.Sprintf("business probe returned HTTP %d", response.StatusCode),
		Confidence: 1, Content: map[string]any{"success": success, "status_code": response.StatusCode},
	}}, nil
}

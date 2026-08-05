package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/httpx"
)

type JaegerClient struct {
	base string
	http *http.Client
}

func NewJaeger(base string) *JaegerClient {
	return &JaegerClient{base: base, http: httpx.NewClient(20 * time.Second)}
}

type TraceSummary struct {
	TraceID         string `json:"trace_id"`
	DurationMicros  int64  `json:"duration_micros"`
	ErrorService    string `json:"error_service,omitempty"`
	SlowService     string `json:"slow_service,omitempty"`
	FailedOperation string `json:"failed_operation,omitempty"`
}

func (c *JaegerClient) Query(ctx context.Context, service string, start, end time.Time, limit int) ([]TraceSummary, error) {
	u, _ := url.Parse(c.base + "/api/traces")
	q := u.Query()
	q.Set("service", service)
	q.Set("start", strconv.FormatInt(start.UnixMicro(), 10))
	q.Set("end", strconv.FormatInt(end.UnixMicro(), 10))
	q.Set("limit", strconv.Itoa(limit))
	u.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("jaeger status %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			TraceID string `json:"traceID"`
			Spans   []struct {
				Operation string `json:"operationName"`
				ProcessID string `json:"processID"`
				Duration  int64  `json:"duration"`
				Tags      []struct {
					Key   string `json:"key"`
					Value any    `json:"value"`
				} `json:"tags"`
			} `json:"spans"`
			Processes map[string]struct {
				ServiceName string `json:"serviceName"`
			} `json:"processes"`
		} `json:"data"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]TraceSummary, 0, len(body.Data))
	for _, tr := range body.Data {
		sum := TraceSummary{TraceID: tr.TraceID}
		var max int64
		for _, sp := range tr.Spans {
			svc := tr.Processes[sp.ProcessID].ServiceName
			if sp.Duration > max {
				max = sp.Duration
				sum.SlowService = svc
				sum.DurationMicros = sp.Duration
			}
			for _, tag := range sp.Tags {
				if tag.Key == "error" && fmt.Sprint(tag.Value) == "true" {
					sum.ErrorService = svc
					sum.FailedOperation = sp.Operation
				}
			}
		}
		out = append(out, sum)
	}
	return out, nil
}

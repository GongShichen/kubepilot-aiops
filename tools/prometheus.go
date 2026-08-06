package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/httpx"
)

type PrometheusClient struct {
	base string
	http *http.Client
}

func NewPrometheus(base string) *PrometheusClient {
	return &PrometheusClient{base: base, http: httpx.NewClient(15 * time.Second)}
}

type PromResult struct {
	Query      string          `json:"query"`
	ResultType string          `json:"result_type"`
	Result     json.RawMessage `json:"result"`
}

func (c *PrometheusClient) Query(ctx context.Context, q string, at time.Time) (PromResult, error) {
	u, err := url.Parse(c.base + "/api/v1/query")
	if err != nil {
		return PromResult{}, err
	}
	v := u.Query()
	v.Set("query", q)
	if !at.IsZero() {
		v.Set("time", at.Format(time.RFC3339))
	}
	u.RawQuery = v.Encode()
	return c.execute(ctx, u, q)
}

func (c *PrometheusClient) QueryRange(ctx context.Context, q string, start, end time.Time, step time.Duration) (PromResult, error) {
	u, err := url.Parse(c.base + "/api/v1/query_range")
	if err != nil {
		return PromResult{}, err
	}
	v := u.Query()
	v.Set("query", q)
	v.Set("start", start.Format(time.RFC3339))
	v.Set("end", end.Format(time.RFC3339))
	v.Set("step", fmt.Sprintf("%.0f", step.Seconds()))
	u.RawQuery = v.Encode()
	return c.execute(ctx, u, q)
}

func (c *PrometheusClient) execute(ctx context.Context, u *url.URL, q string) (PromResult, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return PromResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return PromResult{}, fmt.Errorf("prometheus status %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string          `json:"resultType"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return PromResult{}, err
	}
	if body.Status != "success" {
		return PromResult{}, fmt.Errorf("prometheus: %s", body.Error)
	}
	return PromResult{Query: q, ResultType: body.Data.ResultType, Result: body.Data.Result}, nil
}

func MetricQueries(namespace, service string) map[string]string {
	return map[string]string{
		"cpu":                        fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{namespace=%q,pod=~%q}[5m]))`, namespace, service+".*"),
		"cpu_current":                fmt.Sprintf(`sum(irate(container_cpu_usage_seconds_total{namespace=%q,pod=~%q}[30s]))`, namespace, service+".*"),
		"cpu_throttling":             fmt.Sprintf(`sum(rate(container_cpu_cfs_throttled_periods_total{namespace=%q,pod=~%q}[5m])) / clamp_min(sum(rate(container_cpu_cfs_periods_total{namespace=%q,pod=~%q}[5m])), 1)`, namespace, service+".*", namespace, service+".*"),
		"cpu_throttling_current":     fmt.Sprintf(`sum(irate(container_cpu_cfs_throttled_periods_total{namespace=%q,pod=~%q}[30s])) / clamp_min(sum(irate(container_cpu_cfs_periods_total{namespace=%q,pod=~%q}[30s])), 1)`, namespace, service+".*", namespace, service+".*"),
		"runtime_goroutines":         fmt.Sprintf(`avg_over_time(go_goroutines{namespace=%q,service=%q}[5m])`, namespace, service),
		"runtime_goroutines_current": fmt.Sprintf(`go_goroutines{namespace=%q,service=%q}`, namespace, service),
		"memory":                     fmt.Sprintf(`sum(container_memory_working_set_bytes{namespace=%q,pod=~%q})`, namespace, service+".*"),
		"qps":                        fmt.Sprintf(`sum(rate(http_requests_total{namespace=%q,service=%q}[5m]))`, namespace, service),
		"qps_current":                fmt.Sprintf(`sum(irate(http_requests_total{namespace=%q,service=%q}[30s]))`, namespace, service),
		"error_rate":                 fmt.Sprintf(`sum(rate(http_requests_total{namespace=%q,service=%q,status=~"5.."}[5m])) / clamp_min(sum(rate(http_requests_total{namespace=%q,service=%q}[5m])), 1)`, namespace, service, namespace, service),
		"error_rate_current":         fmt.Sprintf(`sum(irate(http_requests_total{namespace=%q,service=%q,status=~"5.."}[30s])) / clamp_min(sum(irate(http_requests_total{namespace=%q,service=%q}[30s])), 1)`, namespace, service, namespace, service),
		"p95_latency":                fmt.Sprintf(`histogram_quantile(0.95,sum by(le)(rate(http_request_duration_seconds_bucket{namespace=%q,service=%q}[5m])))`, namespace, service),
		"p95_latency_current":        fmt.Sprintf(`histogram_quantile(0.95,sum by(le)(rate(http_request_duration_seconds_bucket{namespace=%q,service=%q}[30s])))`, namespace, service),
		"restarts":                   fmt.Sprintf(`sum(kube_pod_container_status_restarts_total{namespace=%q,pod=~%q} or kube_pod_container_status_restarts_total{exported_namespace=%q,pod=~%q})`, namespace, service+".*", namespace, service+".*"),
		"deployment_availability":    fmt.Sprintf(`kube_deployment_status_replicas_available{namespace=%q,deployment=%q} or kube_deployment_status_replicas_available{exported_namespace=%q,deployment=%q}`, namespace, service, namespace, service),
	}
}

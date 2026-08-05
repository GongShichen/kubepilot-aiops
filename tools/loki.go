package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/httpx"
)

type LokiClient struct {
	base string
	http *http.Client
}

func (c *LokiClient) Push(ctx context.Context, streams []map[string]any) error {
	b, err := json.Marshal(map[string]any{"streams": streams})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/loki/api/v1/push", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("loki push status %d", resp.StatusCode)
	}
	return nil
}

func NewLoki(base string) *LokiClient {
	return &LokiClient{base: base, http: httpx.NewClient(20 * time.Second)}
}

type LokiEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Line      string            `json:"line"`
	Labels    map[string]string `json:"labels"`
}

func (c *LokiClient) QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]LokiEntry, error) {
	u, _ := url.Parse(c.base + "/loki/api/v1/query_range")
	q := u.Query()
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	q.Set("limit", strconv.Itoa(limit))
	q.Set("direction", "backward")
	u.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("loki status %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	var out []LokiEntry
	for _, stream := range body.Data.Result {
		for _, v := range stream.Values {
			if len(v) != 2 {
				continue
			}
			ns, _ := strconv.ParseInt(v[0], 10, 64)
			out = append(out, LokiEntry{Timestamp: time.Unix(0, ns), Line: v[1], Labels: stream.Stream})
		}
	}
	return out, nil
}

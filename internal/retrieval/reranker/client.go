package reranker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kubepilot-aiops/kubepilot/internal/config"
)

type Result struct {
	Index int
	Score float64
}

type Client struct {
	config config.RerankerConfig
	http   *http.Client
}

type Service interface {
	Enabled() bool
	Rerank(context.Context, string, []string, int) ([]Result, error)
	Probe(context.Context) error
	ConfigHash() string
	Health() map[string]any
}

func New(cfg config.RerankerConfig) *Client {
	return &Client{config: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

func (c *Client) Enabled() bool { return c != nil && c.config.Enabled }

func (c *Client) ConfigHash() string {
	if c == nil || !c.config.Enabled {
		return ""
	}
	h := sha256.Sum256([]byte(c.config.Protocol + "\x00" + c.config.BaseURL + "\x00" + c.config.APIPath + "\x00" + c.config.Model))
	return hex.EncodeToString(h[:])
}
func (c *Client) Health() map[string]any {
	return map[string]any{"configured": c.Enabled(), "model": c.config.Model, "config_hash": c.ConfigHash()}
}

func (c *Client) Rerank(ctx context.Context, query string, documents []string, topN int) ([]Result, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("reranker is disabled")
	}
	if len(documents) == 0 {
		return []Result{}, nil
	}
	if topN <= 0 || topN > len(documents) {
		topN = len(documents)
	}
	query = truncateUTF8(query, c.config.MaxDocumentBytes)
	bounded := make([]string, len(documents))
	for index, document := range documents {
		bounded[index] = truncateUTF8(document, c.config.MaxDocumentBytes)
	}
	payload := request{Model: c.config.Model, Query: query, Documents: bounded, TopN: topN, ReturnDocuments: false}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(raw) > c.config.MaxPayloadBytes {
		return nil, fmt.Errorf("reranker payload exceeds %d bytes", c.config.MaxPayloadBytes)
	}
	var last error
	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		results, retry, callErr := c.call(ctx, raw, len(documents))
		if callErr == nil {
			return results, nil
		}
		last = callErr
		if !retry || attempt == c.config.MaxRetries {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
		}
	}
	return nil, last
}

func (c *Client) Probe(ctx context.Context) error {
	results, err := c.Rerank(ctx, "service request failed", []string{"unrelated healthy event", "service request returned an error"}, 2)
	if err != nil {
		return err
	}
	if len(results) != 2 {
		return fmt.Errorf("reranker probe returned %d results", len(results))
	}
	return nil
}

func (c *Client) call(ctx context.Context, payload []byte, documentCount int) ([]Result, bool, error) {
	endpoint, err := joinURL(c.config.BaseURL, c.config.APIPath)
	if err != nil {
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("reranker request failed")
	}
	defer resp.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(resp.Body, int64(c.config.MaxPayloadBytes)+1))
	if err != nil {
		return nil, true, fmt.Errorf("read reranker response: %w", err)
	}
	if len(limited) > c.config.MaxPayloadBytes {
		return nil, false, fmt.Errorf("reranker response exceeds %d bytes", c.config.MaxPayloadBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retry := resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, retry, fmt.Errorf("reranker returned HTTP %d", resp.StatusCode)
	}
	var decoded response
	if err = json.Unmarshal(limited, &decoded); err != nil {
		return nil, false, fmt.Errorf("decode reranker response: %w", err)
	}
	seen := map[int]bool{}
	results := make([]Result, 0, len(decoded.Results))
	for _, item := range decoded.Results {
		if item.Index < 0 || item.Index >= documentCount || seen[item.Index] || math.IsNaN(item.RelevanceScore) || math.IsInf(item.RelevanceScore, 0) || item.RelevanceScore < 0 || item.RelevanceScore > 1 {
			return nil, false, fmt.Errorf("reranker returned invalid index or relevance score")
		}
		seen[item.Index] = true
		results = append(results, Result{Index: item.Index, Score: item.RelevanceScore})
	}
	if len(results) != documentCount {
		return nil, false, fmt.Errorf("reranker returned partial results: got %d want %d", len(results), documentCount)
	}
	return results, false, nil
}

type request struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            int      `json:"top_n"`
	ReturnDocuments bool     `json:"return_documents,omitempty"`
}

type response struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

func joinURL(base, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid reranker base URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(path, "/")
	return parsed.String(), nil
}

func truncateUTF8(value string, maximum int) string {
	if maximum <= 0 || len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value
}

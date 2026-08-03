package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/config"
)

type Embedder struct {
	cfg         config.EmbeddingConfig
	http        *http.Client
	requestMu   sync.Mutex
	lastRequest time.Time
}

func NewEmbedder(cfg config.EmbeddingConfig) *Embedder {
	return &Embedder{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}
func (e *Embedder) Embed(ctx context.Context, input []string) ([][]float32, error) {
	body := struct {
		Model      string   `json:"model"`
		Input      []string `json:"input"`
		Dimensions int      `json:"dimensions,omitempty"`
	}{e.cfg.Model, input, e.cfg.Dimensions}
	b, _ := json.Marshal(body)
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if err := e.waitForRequestSlot(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(e.cfg.BaseURL, e.cfg.APIPath), bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := e.http.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt == 5 {
				return nil, lastErr
			}
			if err = waitEmbeddingRetry(ctx, min(500*time.Millisecond*(1<<attempt), 8*time.Second)); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode/100 == 2 {
			vectors, decodeErr := e.decode(resp, len(input))
			resp.Body.Close()
			return vectors, decodeErr
		}
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		retryAfter := retryAfterDuration(resp.Header.Get("Retry-After"))
		lastErr = decodeError(resp)
		resp.Body.Close()
		if !retryable || attempt == 5 {
			return nil, lastErr
		}
		backoff := min(500*time.Millisecond*(1<<attempt), 8*time.Second)
		if retryAfter > backoff {
			backoff = retryAfter
		}
		if err = waitEmbeddingRetry(ctx, backoff); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func waitEmbeddingRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (e *Embedder) waitForRequestSlot(ctx context.Context) error {
	e.requestMu.Lock()
	defer e.requestMu.Unlock()
	if wait := e.cfg.RequestInterval - time.Since(e.lastRequest); wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	e.lastRequest = time.Now()
	return nil
}

func (e *Embedder) decode(resp *http.Response, inputCount int) ([][]float32, error) {
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	vectors := make([][]float32, inputCount)
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vectors) {
			return nil, fmt.Errorf("embedding index %d out of range", d.Index)
		}
		if e.cfg.Dimensions > 0 && len(d.Embedding) != e.cfg.Dimensions {
			return nil, fmt.Errorf("embedding dimension %d, expected %d", len(d.Embedding), e.cfg.Dimensions)
		}
		vectors[d.Index] = d.Embedding
	}
	for i, vector := range vectors {
		if len(vector) == 0 {
			return nil, fmt.Errorf("embedding response missing index %d", i)
		}
	}
	return vectors, nil
}

func retryAfterDuration(value string) time.Duration {
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

package reranker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/config"
)

// HotClient validates a complete dotenv snapshot before atomically activating
// it. A failed reload never replaces the last healthy reranker.
type HotClient struct {
	path              string
	fallback          config.RerankerConfig
	pollEvery         time.Duration
	retryEvery        time.Duration
	mu                sync.RWMutex
	active            *Client
	desired           config.RerankerConfig
	lastError         string
	loadedAt          time.Time
	lastAttempt       time.Time
	lastCandidateHash string
}

func NewHotClient(fallback config.RerankerConfig, path string, pollEvery, retryEvery time.Duration) *HotClient {
	if pollEvery <= 0 {
		pollEvery = 2 * time.Second
	}
	if retryEvery <= 0 {
		retryEvery = 30 * time.Second
	}
	return &HotClient{path: path, fallback: fallback, desired: fallback, pollEvery: pollEvery, retryEvery: retryEvery}
}
func (h *HotClient) Run(ctx context.Context) {
	_ = h.refresh(ctx, false)
	ticker := time.NewTicker(h.pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = h.refresh(ctx, false)
		}
	}
}
func (h *HotClient) refresh(ctx context.Context, force bool) error {
	cfg := h.fallback
	var err error
	if h.path != "" {
		cfg, err = config.LoadRerankerFile(h.path, h.fallback)
		if err != nil {
			h.setError(err)
			return err
		}
	}
	h.mu.RLock()
	currentHash := ""
	if h.active != nil {
		currentHash = h.active.ConfigHash()
	}
	lastAttempt := h.lastAttempt
	lastCandidateHash := h.lastCandidateHash
	h.mu.RUnlock()
	candidate := New(cfg)
	candidateHash := candidate.ConfigHash()
	if !force && candidateHash == currentHash {
		return nil
	}
	if !force && candidateHash == lastCandidateHash && time.Since(lastAttempt) < h.retryEvery {
		return nil
	}
	h.mu.Lock()
	h.desired = cfg
	h.lastAttempt = time.Now().UTC()
	h.lastCandidateHash = candidateHash
	h.mu.Unlock()
	if cfg.Enabled {
		probeCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		err = candidate.Probe(probeCtx)
		cancel()
		if err != nil {
			h.setError(fmt.Errorf("reranker candidate probe failed"))
			return err
		}
	}
	h.mu.Lock()
	h.active = candidate
	h.lastError = ""
	h.loadedAt = time.Now().UTC()
	h.mu.Unlock()
	return nil
}
func (h *HotClient) setError(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastError = err.Error()
	h.lastAttempt = time.Now().UTC()
}
func (h *HotClient) Enabled() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.active != nil && h.active.Enabled()
}
func (h *HotClient) Rerank(ctx context.Context, query string, documents []string, topN int) ([]Result, error) {
	h.mu.RLock()
	active := h.active
	h.mu.RUnlock()
	if active == nil {
		return nil, fmt.Errorf("reranker is not ready")
	}
	return active.Rerank(ctx, query, documents, topN)
}
func (h *HotClient) Probe(ctx context.Context) error { return h.refresh(ctx, true) }
func (h *HotClient) ConfigHash() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.active == nil {
		return ""
	}
	return h.active.ConfigHash()
}
func (h *HotClient) Health() map[string]any {
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := map[string]any{"configured": h.active != nil && h.active.Enabled(), "desired_model": h.desired.Model, "config_hash": ""}
	if h.active != nil {
		result["config_hash"] = h.active.ConfigHash()
		result["loaded_at"] = h.loadedAt
	}
	if h.lastError != "" {
		result["last_error"] = h.lastError
	}
	return result
}

package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/config"
)

type clientSnapshotKey struct{}
type pinnedClient struct {
	client Client
	eino   einomodel.BaseChatModel
}

type HotClient struct {
	path       string
	fallback   config.ChatConfig
	pollEvery  time.Duration
	retryEvery time.Duration

	probeMu sync.Mutex
	mu      sync.RWMutex
	active  Client
	eino    einomodel.BaseChatModel
	desired config.ChatConfig
	current config.ChatConfig

	activeHash      [32]byte
	lastAttemptHash [32]byte
	hasLastAttempt  bool
	lastAttempt     time.Time
	loadedAt        time.Time
	lastError       string
	reloading       bool
	pinnedByHash    map[string]pinnedClient
}

func NewHotClient(fallback config.ChatConfig, path string, pollEvery, retryEvery time.Duration) *HotClient {
	if pollEvery <= 0 {
		pollEvery = 2 * time.Second
	}
	if retryEvery <= 0 {
		retryEvery = 30 * time.Second
	}
	return &HotClient{path: path, fallback: fallback, desired: fallback, pollEvery: pollEvery, retryEvery: retryEvery, pinnedByHash: map[string]pinnedClient{}}
}

func (c *HotClient) Run(ctx context.Context) {
	c.refreshAndLog(ctx, true)
	ticker := time.NewTicker(c.pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refreshAndLog(ctx, false)
		}
	}
}

func (c *HotClient) refreshAndLog(ctx context.Context, force bool) {
	changed, err := c.refresh(ctx, force)
	if err != nil {
		if changed || force {
			slog.Warn("model configuration probe failed; active model retained", "error", err)
		}
		return
	}
	if changed {
		health := c.Health()
		slog.Info("model configuration activated", "protocol", health["protocol"], "model", health["model"])
	}
}

func (c *HotClient) Complete(ctx context.Context, messages []Message, tools []Tool) (Response, error) {
	if snapshot, ok := ctx.Value(clientSnapshotKey{}).(pinnedClient); ok {
		if snapshot.client == nil {
			return Response{}, errors.New("model is not ready")
		}
		return snapshot.client.Complete(ctx, messages, tools)
	}
	c.mu.RLock()
	active := c.active
	c.mu.RUnlock()
	if active == nil {
		return Response{}, errors.New("model is not ready")
	}
	return active.Complete(ctx, messages, tools)
}

// Generate implements Eino's BaseChatModel and delegates to the immutable
// model snapshot pinned to the Incident workflow context.
func (c *HotClient) Generate(ctx context.Context, messages []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	m := c.einoModel(ctx)
	if m == nil {
		return nil, errors.New("model is not ready")
	}
	return m.Generate(ctx, messages, opts...)
}

// Stream implements Eino's streaming model interface.
func (c *HotClient) Stream(ctx context.Context, messages []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	m := c.einoModel(ctx)
	if m == nil {
		return nil, errors.New("model is not ready")
	}
	return m.Stream(ctx, messages, opts...)
}

func (c *HotClient) einoModel(ctx context.Context) einomodel.BaseChatModel {
	if snapshot, ok := ctx.Value(clientSnapshotKey{}).(pinnedClient); ok {
		return snapshot.eino
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eino
}

func (c *HotClient) Probe(ctx context.Context) error {
	_, err := c.refresh(ctx, true)
	return err
}

func (c *HotClient) Protocol() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.active != nil {
		return c.current.Protocol
	}
	return c.desired.Protocol
}

// WithSnapshot pins the currently active model client to a workflow context.
// A configuration reload can therefore never switch models midway through an
// Incident diagnosis.
func (c *HotClient) WithSnapshot(ctx context.Context) context.Context {
	c.mu.RLock()
	active := c.active
	eino := c.eino
	c.mu.RUnlock()
	return context.WithValue(ctx, clientSnapshotKey{}, pinnedClient{client: active, eino: eino})
}

func (c *HotClient) WithSnapshotHash(ctx context.Context, hash string) (context.Context, error) {
	c.mu.RLock()
	pinned, ok := c.pinnedByHash[hash]
	c.mu.RUnlock()
	if !ok || pinned.eino == nil {
		return ctx, errors.New("checkpoint model snapshot is unavailable; workflow requires operator retry")
	}
	return context.WithValue(ctx, clientSnapshotKey{}, pinned), nil
}

func (c *HotClient) SnapshotIdentity() (hash, protocol, modelName string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.eino == nil {
		return "", c.desired.Protocol, c.desired.Model
	}
	return hex.EncodeToString(c.activeHash[:]), c.current.Protocol, c.current.Model
}

func (c *HotClient) Health() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	health := map[string]any{
		"configured": c.active != nil,
		"protocol":   c.desired.Protocol,
		"model":      c.desired.Model,
		"reloading":  c.reloading,
	}
	if c.active != nil {
		health["active_model"] = c.current.Model
		health["active_protocol"] = c.current.Protocol
		health["loaded_at"] = c.loadedAt
	}
	if c.lastError != "" {
		health["last_error"] = c.lastError
	}
	if !c.lastAttempt.IsZero() {
		health["last_probe_at"] = c.lastAttempt
	}
	return health
}

func (c *HotClient) refresh(ctx context.Context, force bool) (bool, error) {
	c.probeMu.Lock()
	defer c.probeMu.Unlock()

	desired, err := c.loadCandidate()
	if err != nil {
		now := time.Now().UTC()
		c.mu.Lock()
		if !force && c.lastError == err.Error() && now.Sub(c.lastAttempt) < c.retryEvery {
			c.mu.Unlock()
			return false, nil
		}
		c.lastAttempt = now
		c.lastError = err.Error()
		c.reloading = c.active != nil
		c.mu.Unlock()
		return true, err
	}
	fingerprint := chatFingerprint(desired)
	now := time.Now().UTC()

	c.mu.Lock()
	previousDesired := chatFingerprint(c.desired)
	c.desired = desired
	if c.active != nil && c.activeHash == fingerprint && !force {
		c.reloading = false
		c.lastError = ""
		c.mu.Unlock()
		return previousDesired != fingerprint, nil
	}
	if !force && c.hasLastAttempt && c.lastAttemptHash == fingerprint && now.Sub(c.lastAttempt) < c.retryEvery {
		c.mu.Unlock()
		return false, nil
	}
	c.lastAttempt = now
	c.lastAttemptHash = fingerprint
	c.hasLastAttempt = true
	c.reloading = true
	c.mu.Unlock()

	candidate := New(desired)
	einoCandidate, einoErr := NewEinoChatModel(ctx, desired)
	if einoErr != nil {
		c.setFailure(desired, einoErr, true)
		return previousDesired != fingerprint, einoErr
	}
	probeTimeout := desired.Timeout
	if probeTimeout <= 0 {
		probeTimeout = 60 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	err = probeEinoToolCalling(probeCtx, einoCandidate)
	cancel()
	if err != nil {
		err = safeModelError(err, desired.APIKey)
		c.setFailure(desired, err, true)
		return previousDesired != fingerprint, err
	}

	c.mu.Lock()
	c.active = candidate
	c.eino = einoCandidate
	c.current = desired
	c.activeHash = fingerprint
	c.pinnedByHash[hex.EncodeToString(fingerprint[:])] = pinnedClient{client: candidate, eino: einoCandidate}
	c.loadedAt = time.Now().UTC()
	c.lastError = ""
	c.reloading = false
	c.mu.Unlock()
	return true, nil
}

func probeEinoToolCalling(ctx context.Context, chat einomodel.BaseChatModel) error {
	probe := &schema.ToolInfo{Name: "kubepilot_capability_probe", Desc: "Return the supplied capability nonce.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"nonce": {Type: schema.String, Required: true}})}
	reader, err := chat.Stream(ctx, []*schema.Message{schema.UserMessage("Call kubepilot_capability_probe exactly once with nonce kubepilot-probe. Do not answer in text.")}, einomodel.WithTools([]*schema.ToolInfo{probe}))
	if err != nil {
		return err
	}
	defer reader.Close()
	var chunks []*schema.Message
	for {
		chunk, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return recvErr
		}
		chunks = append(chunks, chunk)
	}
	message, err := schema.ConcatMessages(chunks)
	if err != nil {
		return err
	}
	for _, call := range message.ToolCalls {
		if call.Function.Name != probe.Name {
			continue
		}
		var arguments struct {
			Nonce string `json:"nonce"`
		}
		if json.Unmarshal([]byte(call.Function.Arguments), &arguments) == nil && arguments.Nonce == "kubepilot-probe" {
			return nil
		}
	}
	return errors.New("model did not return the Eino capability probe tool call")
}

func (c *HotClient) loadCandidate() (config.ChatConfig, error) {
	if c.path == "" {
		if err := config.ValidateChat(c.fallback); err != nil {
			return config.ChatConfig{}, err
		}
		return c.fallback, nil
	}
	return config.LoadChatFile(c.path, c.fallback)
}

func (c *HotClient) setFailure(desired config.ChatConfig, err error, attempted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if desired.Protocol != "" || desired.Model != "" {
		c.desired = desired
	}
	c.lastError = err.Error()
	c.reloading = attempted || c.active != nil
}

func chatFingerprint(chat config.ChatConfig) [32]byte {
	payload, _ := json.Marshal(chat)
	return sha256.Sum256(payload)
}

func safeModelError(err error, apiKey string) error {
	return errors.New(redactProviderError(err.Error(), apiKey))
}

var providerEndpointPattern = regexp.MustCompile(`https?://[^\s"']+`)

func redactProviderError(message, apiKey string) string {
	if apiKey != "" {
		message = strings.ReplaceAll(message, apiKey, "[REDACTED]")
	}
	return providerEndpointPattern.ReplaceAllString(message, "[REDACTED_ENDPOINT]")
}

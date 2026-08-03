package model

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	claudemodel "github.com/cloudwego/eino-ext/components/model/claude"
	openmodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/config"
)

// NewEinoChatModel is the only model factory used by the Agent runtime.
// Endpoint rewriting keeps CHAT_API_PATH configurable while model streaming,
// message merging, callbacks and tool binding remain owned by Eino.
func NewEinoChatModel(ctx context.Context, cfg config.ChatConfig) (model.BaseChatModel, error) {
	if err := config.ValidateChat(cfg); err != nil {
		return nil, err
	}
	transport := apiPathTransport{base: retryTransport{base: http.DefaultTransport, maxRetries: cfg.MaxRetries}, apiPath: joinedAPIPath(cfg.BaseURL, cfg.APIPath)}
	httpClient := &http.Client{Timeout: cfg.Timeout, Transport: transport}
	temperature := float32(cfg.Temperature)
	switch cfg.Protocol {
	case "openai-compatible":
		maxTokens := cfg.MaxTokens
		return openmodel.NewChatModel(ctx, &openmodel.ChatModelConfig{
			APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Model: cfg.Model, HTTPClient: httpClient,
			MaxTokens: &maxTokens, Temperature: &temperature,
		})
	case "anthropic-compatible":
		baseURL := cfg.BaseURL
		// Claude v0.1.25 starts its decoder goroutine before Stream returns and
		// writes a shared named return variable. Gate body reads until the
		// official Eino Stream method has returned, removing that startup race
		// without replacing or forking the pinned Eino Claude component.
		httpClient.Transport = gatedStreamTransport{base: transport}
		claude, err := claudemodel.NewChatModel(ctx, &claudemodel.Config{
			APIKey: cfg.APIKey, BaseURL: &baseURL, Model: cfg.Model, MaxTokens: cfg.MaxTokens,
			Temperature: &temperature, HTTPClient: httpClient, RequestTimeout: cfg.Timeout,
		})
		if err != nil {
			return nil, err
		}
		return gatedClaudeModel{inner: claude}, nil
	default:
		return nil, fmt.Errorf("unsupported chat protocol %q", cfg.Protocol)
	}
}

type streamGateKey struct{}

type gatedClaudeModel struct{ inner model.BaseChatModel }

func (m gatedClaudeModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.inner.Generate(ctx, input, opts...)
}

func (m gatedClaudeModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	gate := make(chan struct{})
	reader, err := m.inner.Stream(context.WithValue(ctx, streamGateKey{}, (<-chan struct{})(gate)), input, opts...)
	close(gate)
	return reader, err
}

type gatedStreamTransport struct{ base http.RoundTripper }

func (t gatedStreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	if gate, ok := req.Context().Value(streamGateKey{}).(<-chan struct{}); ok {
		response.Body = gatedReadCloser{ReadCloser: response.Body, gate: gate}
	}
	return response, nil
}

type gatedReadCloser struct {
	io.ReadCloser
	gate <-chan struct{}
}

func (r gatedReadCloser) Read(p []byte) (int, error) {
	select {
	case <-r.gate:
		return r.ReadCloser.Read(p)
	default:
		<-r.gate
		return r.ReadCloser.Read(p)
	}
}

type apiPathTransport struct {
	base    http.RoundTripper
	apiPath string
}

type retryTransport struct {
	base       http.RoundTripper
	maxRetries int
}

func (t retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		candidate := req
		if attempt > 0 {
			if req.GetBody == nil {
				return nil, fmt.Errorf("model request body cannot be replayed")
			}
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			candidate = req.Clone(req.Context())
			candidate.Body = body
		}
		response, err := t.base.RoundTrip(candidate)
		if attempt >= t.maxRetries || !retryableModelResponse(response, err) || req.Context().Err() != nil {
			return response, err
		}
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 250 * time.Millisecond)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}
}

func retryableModelResponse(response *http.Response, err error) bool {
	if err != nil {
		return true
	}
	return response != nil && (response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500)
}

func (t apiPathTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.TrimSpace(t.apiPath) == "" {
		return t.base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	clone.URL = cloneURL(req.URL)
	clone.URL.Path = path.Clean("/" + strings.TrimPrefix(t.apiPath, "/"))
	clone.URL.RawPath = ""
	return t.base.RoundTrip(clone)
}

func cloneURL(in *url.URL) *url.URL {
	out := *in
	return &out
}

func joinedAPIPath(baseURL, apiPath string) string {
	base, err := url.Parse(baseURL)
	if err != nil || strings.TrimSpace(base.Path) == "" || base.Path == "/" {
		return path.Clean("/" + strings.TrimPrefix(apiPath, "/"))
	}
	basePath := path.Clean("/" + strings.TrimPrefix(base.Path, "/"))
	candidate := path.Clean("/" + strings.TrimPrefix(apiPath, "/"))
	if candidate == basePath || strings.HasPrefix(candidate, basePath+"/") {
		return candidate
	}
	return path.Join(basePath, candidate)
}

package model

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
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
	// Chat responses are streamed. http.Client.Timeout covers body reads as
	// well as connection setup, so a healthy provider that is still emitting
	// chunks can be aborted midway and surfaced as a misleading request
	// timeout. Bound header establishment with the configured timeout and let
	// the caller's workflow context bound the full stream instead.
	transport := apiPathTransport{base: retryTransport{base: modelHTTPTransport(cfg.Timeout), maxRetries: cfg.MaxRetries}, apiPath: joinedAPIPath(cfg.BaseURL, cfg.APIPath)}
	httpClient := &http.Client{Transport: transport}
	streamTimeout := time.Duration(0)
	if cfg.MaxRetries > 0 {
		streamTimeout = cfg.Timeout
	}
	temperature := float32(cfg.Temperature)
	switch cfg.Protocol {
	case "openai-compatible":
		maxTokens := cfg.MaxTokens
		chat, err := openmodel.NewChatModel(ctx, &openmodel.ChatModelConfig{
			APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Model: cfg.Model, HTTPClient: httpClient,
			MaxTokens: &maxTokens, Temperature: &temperature,
		})
		if err != nil {
			return nil, err
		}
		return requestScopedChatModel{inner: chat, timeout: streamTimeout}, nil
	case "anthropic-compatible":
		baseURL := cfg.BaseURL
		// Claude's decoder starts its decoder goroutine before Stream returns and
		// writes a shared named return variable. Gate body reads until the
		// official Eino Stream method has returned, removing that startup race
		// without replacing or forking the pinned Eino Claude component.
		httpClient.Transport = gatedStreamTransport{base: transport}
		claude, err := claudemodel.NewChatModel(ctx, &claudemodel.Config{
			APIKey: cfg.APIKey, BaseURL: &baseURL, Model: cfg.Model, MaxTokens: cfg.MaxTokens,
			Temperature: &temperature, HTTPClient: httpClient,
			// The workflow context owns the full streamed-response deadline.
			// Anthropic's RequestTimeout wraps the response body too, which can
			// terminate a healthy slow stream after the header was received.
			RequestTimeout: 0,
		})
		if err != nil {
			return nil, err
		}
		return requestScopedChatModel{inner: gatedClaudeModel{inner: claude}, timeout: streamTimeout}, nil
	default:
		return nil, fmt.Errorf("unsupported chat protocol %q", cfg.Protocol)
	}
}

// requestScopedChatModel gives every model request a bounded request context.
// Generate remains bounded by the configured request timeout. Stream uses the
// same value as an idle timeout: every received chunk resets the timer, so a
// healthy provider may stream for longer than CHAT_TIMEOUT without being
// cancelled. This keeps retries available when a stream actually stalls while
// avoiding a wall-clock limit on a long, continuously-producing response.
type requestScopedChatModel struct {
	inner   model.BaseChatModel
	timeout time.Duration
}

func (m requestScopedChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if m.timeout <= 0 {
		return m.inner.Generate(ctx, input, opts...)
	}
	requestCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	return m.inner.Generate(requestCtx, input, opts...)
}

func (m requestScopedChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.timeout <= 0 {
		return m.inner.Stream(ctx, input, opts...)
	}
	requestCtx, finish, notify, idleExpired := newIdleStreamContext(ctx, m.timeout)
	reader, err := m.inner.Stream(requestCtx, input, opts...)
	if err != nil {
		finish()
		return nil, err
	}
	return schema.StreamReaderWithConvert(reader, func(chunk *schema.Message) (*schema.Message, error) {
		notify()
		return chunk, nil
	}, schema.WithErrWrapper(func(streamErr error) error {
		if idleExpired() {
			streamErr = fmt.Errorf("%w: model stream idle timeout: %v", context.DeadlineExceeded, streamErr)
		}
		finish()
		return streamErr
	}), schema.WithOnEOF(func() (any, error) {
		finish()
		return nil, io.EOF
	})), nil
}

// newIdleStreamContext cancels the request only after no stream chunk has
// arrived for timeout. The timer is deliberately reset from the stream
// conversion callback, rather than from a wall-clock deadline, so a provider
// that keeps producing chunks can run indefinitely. The response-header
// timeout in modelHTTPTransport still bounds a connection that never starts.
func newIdleStreamContext(parent context.Context, timeout time.Duration) (context.Context, func(), func(), func() bool) {
	ctx, cancel := context.WithCancel(parent)
	activity := make(chan struct{}, 1)
	done := make(chan struct{})
	var expired atomic.Bool
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-activity:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeout)
			case <-timer.C:
				expired.Store(true)
				cancel()
				return
			}
		}
	}()
	var once sync.Once
	finish := func() {
		once.Do(func() { cancel() })
		<-done
	}
	notify := func() {
		select {
		case activity <- struct{}{}:
		default:
		}
	}
	return ctx, finish, notify, expired.Load
}

func modelHTTPTransport(timeout time.Duration) http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	transport := base.Clone()
	transport.ResponseHeaderTimeout = timeout
	return transport
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

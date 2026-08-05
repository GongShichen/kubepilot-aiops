package httpx

import (
	"context"
	"net/http"
	"time"
)

// RetryTransport retries request-level failures for external HTTP capability
// clients. The request body must be replayable (the standard bytes.Reader and
// strings.Reader requests used by KubePilot provide GetBody). Response bodies
// are closed before replay so connections are not leaked.
type RetryTransport struct {
	Base       http.RoundTripper
	MaxRetries int
}

func (t RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	maxRetries := t.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	var lastResponse *http.Response
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		candidate := req
		if attempt > 0 {
			if req.GetBody == nil {
				return lastResponse, lastErr
			}
			body, err := req.GetBody()
			if err != nil {
				return lastResponse, err
			}
			candidate = req.Clone(req.Context())
			candidate.Body = body
		}
		response, err := base.RoundTrip(candidate)
		lastResponse, lastErr = response, err
		if !retryable(response, err) || req.Context().Err() != nil || attempt == maxRetries {
			return response, err
		}
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 250 * time.Millisecond)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}
	return lastResponse, lastErr
}

func retryable(response *http.Response, err error) bool {
	if err != nil {
		return true
	}
	return response != nil && (response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500)
}

// NewClient returns an HTTP client with a bounded request retry policy.
func NewClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	}
	return &http.Client{Timeout: timeout, Transport: RetryTransport{Base: transport, MaxRetries: 3}}
}

// ContextError reports whether an error came from the caller's context. It is
// useful to keep capability clients from retrying an already-cancelled Agent.
func ContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

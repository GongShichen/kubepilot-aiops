package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/oklog/ulid/v2"
)

const maxBatch = 500

type WSParser struct {
	url, token    string
	dialer        *websocket.Dialer
	mu            sync.Mutex
	conn          *websocket.Conn
	everConnected bool
	stats         ParserStats
}

func NewWSParser(url, token string) *WSParser {
	return &WSParser{url: url, token: token, dialer: &websocket.Dialer{HandshakeTimeout: 10 * time.Second}}
}

type parseRequest struct {
	Version   string      `json:"version"`
	Type      string      `json:"type"`
	RequestID string      `json:"request_id"`
	Records   []LogRecord `json:"records"`
}
type parseResponse struct {
	Version   string           `json:"version"`
	Type      string           `json:"type"`
	RequestID string           `json:"request_id"`
	Results   []TemplateResult `json:"results"`
	Code      string           `json:"code,omitempty"`
	Message   string           `json:"message,omitempty"`
}

func (p *WSParser) connect(ctx context.Context) error {
	if p.conn != nil {
		return nil
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+p.token)
	conn, _, err := p.dialer.DialContext(ctx, p.url, h)
	if err != nil {
		return err
	}
	if p.everConnected {
		p.stats.Reconnects++
	}
	p.everConnected = true
	conn.SetReadLimit(2 << 20)
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	p.conn = conn
	return nil
}
func (p *WSParser) ParseBatch(ctx context.Context, records []LogRecord) ([]TemplateResult, error) {
	if len(records) == 0 {
		return []TemplateResult{}, nil
	}
	if len(records) > maxBatch {
		return nil, fmt.Errorf("batch has %d records, maximum is %d", len(records), maxBatch)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	requestID := ulid.Make().String()
	req := parseRequest{Version: "1", Type: "parse_batch", RequestID: requestID, Records: records}
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		p.stats.Attempts++
		if attempt > 0 {
			p.stats.Retries++
			if err := waitReconnect(ctx, min(500*time.Millisecond*(1<<(attempt-1)), 10*time.Second)); err != nil {
				return nil, err
			}
		}
		if err := p.connect(ctx); err != nil {
			last = err
			continue
		}
		_ = p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := p.conn.WriteJSON(req); err != nil {
			last = err
			p.close()
			continue
		}
		_ = p.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var resp parseResponse
		if err := p.conn.ReadJSON(&resp); err != nil {
			last = err
			p.close()
			continue
		}
		if resp.RequestID != requestID {
			return nil, fmt.Errorf("unexpected response request_id %q", resp.RequestID)
		}
		if resp.Type == "error" {
			return nil, fmt.Errorf("drain3 %s: %s", resp.Code, resp.Message)
		}
		if resp.Type != "parse_result" {
			return nil, fmt.Errorf("unexpected response type %q", resp.Type)
		}
		p.stats.Batches++
		p.stats.Records += len(records)
		return resp.Results, nil
	}
	return nil, fmt.Errorf("drain3 websocket failed: %w", last)
}

func waitReconnect(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p *WSParser) Stats() ParserStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}
func (p *WSParser) close() {
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
}
func (p *WSParser) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return nil
	}
	err := p.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
	p.close()
	return err
}
func (p *WSParser) MarshalProtocolExample() ([]byte, error) {
	return json.Marshal(parseRequest{Version: "1", Type: "parse_batch", RequestID: "01EXAMPLE", Records: []LogRecord{}})
}

var _ Parser = (*WSParser)(nil)
var _ ParserStatsProvider = (*WSParser)(nil)
var _ = errors.Is

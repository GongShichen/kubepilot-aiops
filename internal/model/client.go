package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/config"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type Response struct {
	Content      string     `json:"content"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	InputTokens  int        `json:"input_tokens,omitempty"`
	OutputTokens int        `json:"output_tokens,omitempty"`
}
type Client interface {
	Complete(context.Context, []Message, []Tool) (Response, error)
	Probe(context.Context) error
	Protocol() string
}

func New(cfg config.ChatConfig) Client {
	base := baseClient{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
	var inner Client
	if cfg.Protocol == "anthropic-compatible" {
		inner = &anthropicClient{base}
	} else {
		inner = &openAIClient{base}
	}
	return &retryClient{inner: inner, maxRetries: cfg.MaxRetries}
}

type retryClient struct {
	inner      Client
	maxRetries int
}

func (c *retryClient) Protocol() string { return c.inner.Protocol() }
func (c *retryClient) Probe(ctx context.Context) error {
	_, err := c.run(ctx, func() (Response, error) {
		return Response{}, c.inner.Probe(ctx)
	})
	return err
}
func (c *retryClient) Complete(ctx context.Context, messages []Message, tools []Tool) (Response, error) {
	return c.run(ctx, func() (Response, error) { return c.inner.Complete(ctx, messages, tools) })
}
func (c *retryClient) run(ctx context.Context, operation func() (Response, error)) (Response, error) {
	var response Response
	var err error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		response, err = operation()
		if err == nil || attempt == c.maxRetries || ctx.Err() != nil || !retryableModelError(err) {
			return response, err
		}
		delay := 500 * time.Millisecond * time.Duration(1<<attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Response{}, ctx.Err()
		case <-timer.C:
		}
	}
	return response, err
}

type endpointError struct {
	Status int
	Body   string
}

func (e *endpointError) Error() string {
	return fmt.Sprintf("model endpoint status %d: %s", e.Status, e.Body)
}

func retryableModelError(err error) bool {
	var endpointErr *endpointError
	if errors.As(err, &endpointErr) {
		return endpointErr.Status == http.StatusTooManyRequests || endpointErr.Status >= 500
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unexpected eof") || strings.Contains(message, "unexpected end of json input") || strings.Contains(message, "connection reset") || strings.Contains(message, "broken pipe") {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) && (networkErr.Timeout() || networkErr.Temporary())
}

type baseClient struct {
	cfg  config.ChatConfig
	http *http.Client
}

func endpoint(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}
func decodeError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return &endpointError{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
}

type openAIClient struct{ baseClient }

func (c *openAIClient) Protocol() string { return "openai-compatible" }
func (c *openAIClient) Complete(ctx context.Context, messages []Message, tools []Tool) (Response, error) {
	type fn struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	}
	type ot struct {
		Type     string `json:"type"`
		Function fn     `json:"function"`
	}
	reqBody := struct {
		Model           string    `json:"model"`
		Messages        []Message `json:"messages"`
		Tools           []ot      `json:"tools,omitempty"`
		Temperature     float64   `json:"temperature"`
		MaxTokens       int       `json:"max_tokens,omitempty"`
		ReasoningEffort string    `json:"reasoning_effort,omitempty"`
		Stream          bool      `json:"stream"`
	}{Model: c.cfg.Model, Messages: messages, Temperature: c.cfg.Temperature, MaxTokens: c.cfg.MaxTokens, ReasoningEffort: c.cfg.ReasoningEffort, Stream: true}
	for _, t := range tools {
		reqBody.Tools = append(reqBody.Tools, ot{Type: "function", Function: fn{Name: t.Name, Description: t.Description, Parameters: t.InputSchema}})
	}
	b, _ := json.Marshal(reqBody)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(c.cfg.BaseURL, c.cfg.APIPath), bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Response{}, decodeError(resp)
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return decodeOpenAIStream(resp.Body)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   any `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Response{}, err
	}
	if len(out.Choices) == 0 {
		return Response{}, errors.New("model returned no choices")
	}
	content := ""
	if out.Choices[0].Message.Content != nil {
		content = fmt.Sprint(out.Choices[0].Message.Content)
	}
	r := Response{Content: content, InputTokens: out.Usage.Prompt, OutputTokens: out.Usage.Completion}
	for _, tc := range out.Choices[0].Message.ToolCalls {
		arguments, normalizeErr := normalizeOpenAIArguments(tc.Function.Arguments)
		if normalizeErr != nil {
			return Response{}, fmt.Errorf("decode tool %s arguments: %w", tc.Function.Name, normalizeErr)
		}
		r.ToolCalls = append(r.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: arguments})
	}
	return r, nil
}

// OpenAI-compatible providers normally encode function.arguments as a JSON
// string, while a few gateways return the object directly. Normalize both
// representations so downstream schema validation always receives JSON.
func normalizeOpenAIArguments(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`), nil
	}
	var encoded string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return nil, err
		}
		raw = json.RawMessage(encoded)
	}
	if !json.Valid(raw) {
		return nil, errors.New("arguments are not valid JSON")
	}
	return raw, nil
}

type openAIToolAccumulator struct {
	id, name, arguments string
}

func decodeOpenAIStream(body io.Reader) (Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	result := Response{}
	tools := map[int]*openAIToolAccumulator{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Choices []struct {
				Delta struct {
					Content   any `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string          `json:"name"`
							Arguments json.RawMessage `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Response{}, fmt.Errorf("decode OpenAI stream event: %w", err)
		}
		if chunk.Error != nil {
			return Response{}, fmt.Errorf("model stream error: %s", chunk.Error.Message)
		}
		result.InputTokens = max(result.InputTokens, chunk.Usage.Prompt)
		result.OutputTokens = max(result.OutputTokens, chunk.Usage.Completion)
		for _, choice := range chunk.Choices {
			if text, ok := choice.Delta.Content.(string); ok {
				result.Content += text
			}
			for _, call := range choice.Delta.ToolCalls {
				acc := tools[call.Index]
				if acc == nil {
					acc = &openAIToolAccumulator{}
					tools[call.Index] = acc
				}
				if call.ID != "" {
					acc.id += call.ID
				}
				if call.Function.Name != "" {
					acc.name += call.Function.Name
				}
				fragment, err := openAIArgumentFragment(call.Function.Arguments)
				if err != nil {
					return Response{}, fmt.Errorf("decode streamed tool arguments: %w", err)
				}
				acc.arguments += fragment
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Response{}, err
	}
	indices := make([]int, 0, len(tools))
	for index := range tools {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		acc := tools[index]
		arguments := json.RawMessage(acc.arguments)
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		arguments, err := normalizeOpenAIArguments(arguments)
		if err != nil {
			return Response{}, fmt.Errorf("decode tool %s arguments: %w", acc.name, err)
		}
		result.ToolCalls = append(result.ToolCalls, ToolCall{ID: acc.id, Name: acc.name, Arguments: arguments})
	}
	if result.Content == "" && len(result.ToolCalls) == 0 {
		return Response{}, errors.New("model stream returned no content or tool calls")
	}
	return result, nil
}

func openAIArgumentFragment(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		return value, nil
	}
	if !json.Valid(raw) {
		return "", errors.New("argument fragment is neither a string nor JSON")
	}
	return string(raw), nil
}
func (c *openAIClient) Probe(ctx context.Context) error {
	return probeTools(ctx, c)
}

type anthropicClient struct{ baseClient }

func (c *anthropicClient) Protocol() string { return "anthropic-compatible" }
func (c *anthropicClient) Complete(ctx context.Context, messages []Message, tools []Tool) (Response, error) {
	var system string
	var msgs []Message
	for _, m := range messages {
		if m.Role == "system" {
			system = m.Content
		} else {
			msgs = append(msgs, m)
		}
	}
	reqBody := struct {
		Model       string    `json:"model"`
		System      string    `json:"system,omitempty"`
		Messages    []Message `json:"messages"`
		Tools       []Tool    `json:"tools,omitempty"`
		Temperature float64   `json:"temperature"`
		MaxTokens   int       `json:"max_tokens"`
		Stream      bool      `json:"stream"`
	}{Model: c.cfg.Model, System: system, Messages: msgs, Tools: tools, Temperature: c.cfg.Temperature, MaxTokens: c.cfg.MaxTokens, Stream: true}
	b, _ := json.Marshal(reqBody)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(c.cfg.BaseURL, c.cfg.APIPath), bytes.NewReader(b))
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Response{}, decodeError(resp)
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return decodeAnthropicStream(resp.Body)
	}
	var out struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Response{}, err
	}
	r := Response{InputTokens: out.Usage.Input, OutputTokens: out.Usage.Output}
	for _, block := range out.Content {
		switch block.Type {
		case "text":
			r.Content += block.Text
		case "tool_use":
			r.ToolCalls = append(r.ToolCalls, ToolCall{ID: block.ID, Name: block.Name, Arguments: block.Input})
		}
	}
	if r.Content == "" && len(r.ToolCalls) == 0 {
		return Response{}, errors.New("model returned empty content")
	}
	return r, nil
}
func (c *anthropicClient) Probe(ctx context.Context) error {
	return probeTools(ctx, c)
}

type anthropicBlockAccumulator struct {
	kind, id, name, text, arguments string
}

func decodeAnthropicStream(body io.Reader) (Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	result := Response{}
	blocks := map[int]*anthropicBlockAccumulator{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Message struct {
				Usage struct {
					Input int `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage struct {
				Output int `json:"output_tokens"`
			} `json:"usage"`
			ContentBlock struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return Response{}, fmt.Errorf("decode Anthropic stream event: %w", err)
		}
		if event.Type == "error" || event.Error != nil {
			message := "unknown stream error"
			if event.Error != nil && event.Error.Message != "" {
				message = event.Error.Message
			}
			return Response{}, fmt.Errorf("model stream error: %s", message)
		}
		result.InputTokens = max(result.InputTokens, event.Message.Usage.Input)
		result.OutputTokens = max(result.OutputTokens, event.Usage.Output)
		switch event.Type {
		case "content_block_start":
			block := &anthropicBlockAccumulator{kind: event.ContentBlock.Type, id: event.ContentBlock.ID, name: event.ContentBlock.Name, text: event.ContentBlock.Text}
			if len(event.ContentBlock.Input) > 0 && string(event.ContentBlock.Input) != "{}" && string(event.ContentBlock.Input) != "null" {
				block.arguments = string(event.ContentBlock.Input)
			}
			blocks[event.Index] = block
		case "content_block_delta":
			block := blocks[event.Index]
			if block == nil {
				block = &anthropicBlockAccumulator{}
				blocks[event.Index] = block
			}
			block.text += event.Delta.Text
			block.arguments += event.Delta.PartialJSON
		}
	}
	if err := scanner.Err(); err != nil {
		return Response{}, err
	}
	indices := make([]int, 0, len(blocks))
	for index := range blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		block := blocks[index]
		switch block.kind {
		case "text":
			result.Content += block.text
		case "tool_use":
			arguments := json.RawMessage(block.arguments)
			if len(arguments) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			if !json.Valid(arguments) {
				return Response{}, fmt.Errorf("decode tool %s arguments: invalid JSON", block.name)
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{ID: block.id, Name: block.name, Arguments: arguments})
		}
	}
	if result.Content == "" && len(result.ToolCalls) == 0 {
		return Response{}, errors.New("model stream returned no content or tool calls")
	}
	return result, nil
}

func probeTools(ctx context.Context, client Client) error {
	tool := Tool{Name: "kubepilot_capability_probe", Description: "Return the supplied nonce without side effects.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"nonce": map[string]any{"type": "string"}}, "required": []string{"nonce"}, "additionalProperties": false}}
	r, err := client.Complete(ctx, []Message{{Role: "user", Content: "Call kubepilot_capability_probe exactly once with nonce kubepilot-probe. Do not answer in text."}}, []Tool{tool})
	if err != nil {
		return err
	}
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].Name != tool.Name {
		return errors.New("model endpoint did not produce the required tool call")
	}
	var input struct {
		Nonce string `json:"nonce"`
	}
	if err = json.Unmarshal(r.ToolCalls[0].Arguments, &input); err != nil || input.Nonce != "kubepilot-probe" {
		return errors.New("model endpoint produced an invalid tool call payload")
	}
	return nil
}

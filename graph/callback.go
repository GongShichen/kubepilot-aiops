package graph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/callbacks"
	componentmodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/oklog/ulid/v2"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type WorkflowEvent struct {
	IncidentID         string    `json:"incident_id"`
	RunID              string    `json:"run_id"`
	Type               string    `json:"type"`
	Name               string    `json:"name"`
	Component          string    `json:"component"`
	Error              string    `json:"error,omitempty"`
	InputTokens        int       `json:"input_tokens,omitempty"`
	OutputTokens       int       `json:"output_tokens,omitempty"`
	OccurredAt         time.Time `json:"occurred_at"`
	TimeToFirstChunkMS float64   `json:"time_to_first_chunk_ms,omitempty"`
	TimeToToolCallMS   float64   `json:"time_to_tool_call_ms,omitempty"`
	StreamDurationMS   float64   `json:"stream_duration_ms,omitempty"`
}

type EventSink func(context.Context, WorkflowEvent)

type spanContextKey struct{}
type callbackRun struct {
	span      trace.Span
	id        string
	startedAt time.Time
}

var (
	workflowEvents     = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kubepilot_eino_events_total", Help: "Eino runtime lifecycle events."}, []string{"type", "component"})
	componentLatency   = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "kubepilot_eino_component_duration_seconds", Help: "Eino component execution latency.", Buckets: prometheus.DefBuckets}, []string{"component", "name", "status"})
	modelTokens        = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kubepilot_eino_model_tokens_total", Help: "Model tokens reported through Eino callbacks."}, []string{"direction"})
	modelStreamLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "kubepilot_eino_model_stream_duration_seconds", Help: "Eino model streaming phase latency.", Buckets: prometheus.DefBuckets}, []string{"phase", "name"})
	endpointPattern    = regexp.MustCompile(`https?://[^\s"']+`)
	credentialPattern  = regexp.MustCompile(`(?i)(authorization|x-api-key|api[-_ ]?key)\s*[:=]\s*[^\s,;]+`)
)

func init() {
	prometheus.MustRegister(workflowEvents, componentLatency, modelTokens, modelStreamLatency)
}

type modelTimings struct{ firstChunk, timeToToolCall, full time.Duration }

func NewEinoCallback(incidentID string, sink EventSink) callbacks.Handler {
	emit := func(ctx context.Context, info *callbacks.RunInfo, suffix, errorMessage string, usage *componentmodel.TokenUsage, timings *modelTimings) context.Context {
		name, component := runInfo(info)
		run, _ := ctx.Value(spanContextKey{}).(callbackRun)
		event := WorkflowEvent{IncidentID: incidentID, RunID: run.id, Type: eventPrefix(name, component) + suffix, Name: name, Component: component, Error: errorMessage, OccurredAt: time.Now().UTC()}
		if usage != nil {
			event.InputTokens, event.OutputTokens = usage.PromptTokens, usage.CompletionTokens
			modelTokens.WithLabelValues("input").Add(float64(usage.PromptTokens))
			modelTokens.WithLabelValues("output").Add(float64(usage.CompletionTokens))
		}
		if timings != nil {
			event.TimeToFirstChunkMS = float64(timings.firstChunk.Microseconds()) / 1000
			event.TimeToToolCallMS = float64(timings.timeToToolCall.Microseconds()) / 1000
			event.StreamDurationMS = float64(timings.full.Microseconds()) / 1000
			isModel := strings.Contains(strings.ToLower(component), "chatmodel") || strings.Contains(strings.ToLower(name), "chatmodel")
			if isModel && timings.firstChunk > 0 {
				modelStreamLatency.WithLabelValues("first_chunk", name).Observe(timings.firstChunk.Seconds())
			}
			if isModel && timings.timeToToolCall > 0 {
				modelStreamLatency.WithLabelValues("tool_call", name).Observe(timings.timeToToolCall.Seconds())
			}
			if isModel && timings.full > 0 {
				modelStreamLatency.WithLabelValues("full", name).Observe(timings.full.Seconds())
			}
		}
		workflowEvents.WithLabelValues(event.Type, component).Inc()
		if sink != nil {
			sink(ctx, event)
		}
		return ctx
	}
	start := func(ctx context.Context, info *callbacks.RunInfo) context.Context {
		name, component := runInfo(info)
		ctx, span := otel.Tracer("kubepilot/eino").Start(ctx, "eino."+name, trace.WithAttributes(attribute.String("incident.id", incidentID), attribute.String("eino.component", component)))
		ctx = context.WithValue(ctx, spanContextKey{}, callbackRun{span: span, id: ulid.Make().String(), startedAt: time.Now().UTC()})
		return emit(ctx, info, "_started", "", nil, nil)
	}
	finish := func(ctx context.Context, info *callbacks.RunInfo, err error, usage *componentmodel.TokenUsage, timings *modelTimings) context.Context {
		name, component := runInfo(info)
		message := ""
		if err != nil {
			message = RedactError(err.Error())
		}
		ctx = emit(ctx, info, "_completed", message, usage, timings)
		if run, ok := ctx.Value(spanContextKey{}).(callbackRun); ok {
			status := "ok"
			if err != nil {
				status = "error"
				run.span.RecordError(err)
				run.span.SetStatus(codes.Error, "Eino component failed")
			}
			componentLatency.WithLabelValues(component, name, status).Observe(time.Since(run.startedAt).Seconds())
			run.span.End()
		}
		return ctx
	}
	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			return start(ctx, info)
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, outputValue callbacks.CallbackOutput) context.Context {
			var usage *componentmodel.TokenUsage
			if output := componentmodel.ConvCallbackOutput(outputValue); output != nil {
				usage = output.TokenUsage
			}
			return finish(ctx, info, nil, usage, nil)
		}).
		OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			timings := &modelTimings{}
			if run, ok := ctx.Value(spanContextKey{}).(callbackRun); ok {
				timings.full = time.Since(run.startedAt)
			}
			return finish(ctx, info, err, nil, timings)
		}).
		OnStartWithStreamInputFn(func(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
			input.Close()
			return start(ctx, info)
		}).
		OnEndWithStreamOutputFn(func(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
			defer output.Close()
			var usage *componentmodel.TokenUsage
			var timings modelTimings
			started := time.Now()
			if run, ok := ctx.Value(spanContextKey{}).(callbackRun); ok {
				started = run.startedAt
			}
			for {
				item, err := output.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return finish(ctx, info, err, usage, &timings)
				}
				if timings.firstChunk == 0 {
					timings.firstChunk = time.Since(started)
				}
				if converted := componentmodel.ConvCallbackOutput(item); converted != nil {
					if converted.TokenUsage != nil {
						usage = converted.TokenUsage
					}
					if timings.timeToToolCall == 0 && converted.Message != nil && len(converted.Message.ToolCalls) > 0 {
						timings.timeToToolCall = time.Since(started)
					}
				}
			}
			timings.full = time.Since(started)
			return finish(ctx, info, nil, usage, &timings)
		}).Build()
}

// RedactError removes endpoint and credential-shaped values before errors are
// persisted, exported as traces, or sent through Incident SSE.
func RedactError(message string) string {
	message = endpointPattern.ReplaceAllString(message, "[REDACTED_ENDPOINT]")
	return credentialPattern.ReplaceAllString(message, "$1=[REDACTED]")
}

func runInfo(info *callbacks.RunInfo) (string, string) {
	if info == nil {
		return "unknown", "unknown"
	}
	name := info.Name
	if name == "" {
		name = "unknown"
	}
	return name, fmt.Sprint(info.Component)
}

func eventPrefix(name, component string) string {
	lower := strings.ToLower(name + " " + component)
	switch {
	case strings.Contains(lower, "tool"):
		return "tool"
	case strings.HasSuffix(strings.ToLower(name), "_agent") || strings.Contains(lower, "chatmodel"):
		return "agent"
	default:
		return "node"
	}
}

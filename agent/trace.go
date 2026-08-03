package agent

import (
	"context"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/tools"
	"github.com/oklog/ulid/v2"
)

type TraceAgent struct{ Client *tools.JaegerClient }

func (a TraceAgent) Collect(ctx context.Context, in *domain.Incident) ([]domain.Evidence, error) {
	start := in.EvidenceStartAt
	if start.IsZero() {
		start = in.CreatedAt.Add(-5 * time.Minute)
	}
	traces, err := a.Client.Query(ctx, in.Service, start, time.Now().UTC(), 20)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Evidence, 0, len(traces))
	for _, t := range traces {
		out = append(out, domain.Evidence{ID: ulid.Make().String(), Source: "jaeger", Kind: "trace", Summary: "trace critical-path analysis", Data: map[string]any{"trace_id": t.TraceID, "duration_micros": t.DurationMicros, "slow_service": t.SlowService, "error_service": t.ErrorService, "failed_operation": t.FailedOperation}, ObservedAt: time.Now().UTC()})
	}
	return out, nil
}

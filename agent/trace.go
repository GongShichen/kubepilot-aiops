package agent

import (
	"context"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	evidencenorm "github.com/kubepilot-aiops/kubepilot/internal/evidence"
	"github.com/kubepilot-aiops/kubepilot/tools"
)

type TraceCollector struct{ Client *tools.JaegerClient }

func (a TraceCollector) Collect(ctx context.Context, in *domain.Incident, request domain.EvidenceRequest) ([]domain.Evidence, error) {
	request, err := validateEvidenceRequest(in, request, "trace", nil)
	if err != nil {
		return nil, err
	}
	in = requestTargetIncident(in, request)
	traces, err := a.Client.QueryNamespace(ctx, in.Service, in.Namespace, request.WindowStart, request.WindowEnd, 20)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Evidence, 0, len(traces))
	for _, t := range traces {
		out = append(out, domain.Evidence{Source: "jaeger", Kind: "trace", Namespace: in.Namespace, Service: in.Service, Resource: in.Resource, Summary: "trace critical-path analysis", Data: map[string]any{"trace_id": t.TraceID, "duration_micros": t.DurationMicros, "slow_service": t.SlowService, "error_service": t.ErrorService, "failed_operation": t.FailedOperation}, ObservedAt: time.Now().UTC()})
	}
	return evidencenorm.Normalize(in, request, out), nil
}

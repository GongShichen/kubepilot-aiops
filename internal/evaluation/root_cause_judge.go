// Package evaluation contains evaluator-only contracts shared by experiment
// runners. It is intentionally outside the Agent runtime: a reference answer
// must never be available while an incident is being diagnosed.
package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type RootCause struct {
	Category string `json:"category"`
	Variant  string `json:"variant"`
	Service  string `json:"service"`
	Resource string `json:"resource"`
}

type RootCauseVerdict struct {
	Equivalent bool    `json:"equivalent"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// RootCauseJudge evaluates semantic equivalence after a diagnosis completes.
// It must not be injected into the production Agent or its tools.
type RootCauseJudge interface {
	Judge(context.Context, RootCause, RootCause) (RootCauseVerdict, error)
}

type ChatRootCauseJudge struct {
	Chat model.BaseChatModel
}

func (j ChatRootCauseJudge) Judge(ctx context.Context, expected, actual RootCause) (RootCauseVerdict, error) {
	if j.Chat == nil {
		return RootCauseVerdict{}, fmt.Errorf("semantic root-cause judge model is required")
	}
	if !completeRootCause(actual) {
		return RootCauseVerdict{Equivalent: false, Confidence: 1, Reason: "diagnosis did not identify a complete root cause"}, nil
	}
	payload, err := json.Marshal(map[string]RootCause{"reference": expected, "diagnosis": actual})
	if err != nil {
		return RootCauseVerdict{}, fmt.Errorf("marshal root-cause judge input: %w", err)
	}
	message, err := j.Chat.Generate(ctx, []*schema.Message{
		schema.SystemMessage("You evaluate whether two Kubernetes root-cause labels name the same concrete mechanism. Return JSON only: {\"equivalent\":true|false,\"confidence\":0.0,\"reason\":\"short observable rationale\"}. Require the same service and resource and the same causal mechanism; a shared broad category alone is insufficient. Treat ordinary wording or identifier synonyms as equivalent only when they preserve that mechanism. Do not infer facts not present in the two labels."),
		schema.UserMessage(string(payload)),
	}, model.WithTemperature(0))
	if err != nil {
		return RootCauseVerdict{}, fmt.Errorf("semantic root-cause judge: %w", err)
	}
	var verdict RootCauseVerdict
	if err := json.Unmarshal([]byte(extractJSONObject(message.Content)), &verdict); err != nil {
		return RootCauseVerdict{}, fmt.Errorf("decode semantic root-cause verdict: %w", err)
	}
	return validateVerdict(verdict, expected, actual)
}

func validateVerdict(verdict RootCauseVerdict, expected, actual RootCause) (RootCauseVerdict, error) {
	if verdict.Confidence < 0 || verdict.Confidence > 1 || math.IsNaN(verdict.Confidence) {
		return RootCauseVerdict{}, fmt.Errorf("semantic root-cause judge returned invalid confidence")
	}
	if verdict.Equivalent && (!same(expected.Service, actual.Service) || !same(expected.Resource, actual.Resource)) {
		return RootCauseVerdict{}, fmt.Errorf("semantic root-cause judge accepted a different target")
	}
	verdict.Reason = strings.TrimSpace(verdict.Reason)
	if len(verdict.Reason) > 512 {
		verdict.Reason = verdict.Reason[:512]
	}
	return verdict, nil
}

func completeRootCause(root RootCause) bool {
	return strings.TrimSpace(root.Category) != "" && strings.TrimSpace(root.Variant) != "" && strings.TrimSpace(root.Service) != "" && strings.TrimSpace(root.Resource) != ""
}

func same(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func extractJSONObject(value string) string {
	start := strings.IndexByte(value, '{')
	end := strings.LastIndexByte(value, '}')
	if start < 0 || end < start {
		return value
	}
	return value[start : end+1]
}

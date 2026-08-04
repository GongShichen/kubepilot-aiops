package safety

import (
	"strings"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func Repairable(scope domain.SafetyScope, code, reason string, missing, capabilities []string, remaining int) domain.SafetyFeedback {
	return domain.SafetyFeedback{Scope: scope, Category: domain.SafetyRepairable, Code: clean(code), Reason: clean(reason), MissingRequirements: sanitize(missing), RequiredCapabilities: sanitize(capabilities), Retryable: true, RemainingCorrections: remaining}
}

func Fatal(scope domain.SafetyScope, code, reason string) domain.SafetyFeedback {
	return domain.SafetyFeedback{Scope: scope, Category: domain.SafetyFatal, Code: code, Reason: clean(reason), Retryable: false}
}

func HumanRequired(scope domain.SafetyScope, code, reason string) domain.SafetyFeedback {
	return domain.SafetyFeedback{Scope: scope, Category: domain.SafetyHumanRequired, Code: code, Reason: clean(reason), RequiresHuman: true}
}

func Allowed(scope domain.SafetyScope) domain.SafetyFeedback {
	return domain.SafetyFeedback{Allowed: true, Scope: scope, Code: "allowed", Reason: "all safety requirements satisfied"}
}

// ValidateFeedback prevents the controller itself from becoming a hidden
// workflow. Feedback may describe missing capabilities but must not prescribe
// concrete tools, raw query languages, or a recovery answer.
func ValidateFeedback(feedback domain.SafetyFeedback, knownTools []string) bool {
	text := strings.ToLower(strings.Join(append(append([]string{feedback.Reason}, feedback.MissingRequirements...), feedback.RequiredCapabilities...), " "))
	for _, name := range knownTools {
		if name != "" && strings.Contains(text, strings.ToLower(name)) {
			return false
		}
	}
	for _, forbidden := range []string{"kubectl", "promql", "logql", "select ", "delete deployment", "rollback_deployment", "restart_pod", "scale_deployment"} {
		if strings.Contains(text, forbidden) {
			return false
		}
	}
	return feedback.Scope != "" && feedback.Code != "" && feedback.Reason != ""
}

func sanitize(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if cleaned := clean(value); cleaned != "" {
			out = append(out, cleaned)
		}
	}
	return out
}

func clean(value string) string {
	value = strings.TrimSpace(value)
	characters := []rune(value)
	if len(characters) > 512 {
		value = string(characters[:512])
	}
	return value
}

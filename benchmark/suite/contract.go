package suite

import (
	"fmt"
	"strings"
)

// ValidateSectionBoundary prevents a dataset or observation from crossing
// benchmark responsibilities. It is intentionally conservative: the log
// suite accepts template-only observations, while all reasoning signals belong
// to incident/diagnosis/evolution suites.
func ValidateSectionBoundary(section string, fields map[string]any) error {
	if fields == nil {
		return fmt.Errorf("%s fields are required", section)
	}
	if section == "log_retrieval" {
		for key := range fields {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "topology") || strings.Contains(lower, "causal") || strings.Contains(lower, "root_cause") || strings.Contains(lower, "incident") {
				return fmt.Errorf("log_retrieval field %q crosses into incident reasoning", key)
			}
		}
	}
	if section == "incident_retrieval" && fields["template_id"] != nil {
		return fmt.Errorf("incident_retrieval cannot use log template ground truth")
	}
	return nil
}

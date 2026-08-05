package retrieval

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/tools"
)

func TestRankLogTemplatesUsesObservedContent(t *testing.T) {
	entries := []tools.LokiEntry{
		{Line: "database connection timeout", Labels: map[string]string{"template_id": "db-timeout"}},
		{Line: "pod restarted", Labels: map[string]string{"template_id": "restart"}},
	}
	ranked := RankLogTemplates("find database timeout", entries)
	if len(ranked) != 1 || ranked[0] != "db-timeout" {
		t.Fatalf("unexpected ranking: %v", ranked)
	}
}

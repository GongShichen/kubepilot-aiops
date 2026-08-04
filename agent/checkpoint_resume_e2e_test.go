package agent

import (
	"os"
	"testing"
)

// TestCheckpointResumeE2E is the named production-readiness test. The helper
// process is launched by runCheckpointResumeE2E with the same test binary.
func TestCheckpointResumeE2E(t *testing.T) {
	if os.Getenv("KUBEPILOT_AGENT_RESUME_HELPER") == "resume" {
		runResumeHelper()
		return
	}
	runCheckpointResumeE2E(t)
}

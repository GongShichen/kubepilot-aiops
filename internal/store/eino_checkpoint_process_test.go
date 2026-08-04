package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// TestEinoCheckpointPersistsAcrossProcesses exercises the same Redis-backed
// adapter from two OS processes. It is skipped unless TEST_REDIS_URL points at
// an isolated test Redis instance, so the default unit test suite never
// mutates a developer's cache.
func TestEinoCheckpointPersistsAcrossProcesses(t *testing.T) {
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("TEST_REDIS_URL is not configured")
	}
	checkpointID := "process-test-" + time.Now().UTC().Format("20060102150405.000000000")
	payload := map[string]any{
		"incident_id":  "incident-process-resume",
		"interrupt_id": "interrupt-1",
		"skill_hash":   "skill-hash-1",
		"budget": &domain.AgentBudgetState{
			Usage: map[string]domain.AgentBudgetUsage{"diagnosis_agent": {Iterations: 3, ToolUses: 5, ToolCost: 9, Tokens: 400, Corrections: 1}},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestEinoCheckpointPersistsAcrossProcesses")
	cmd.Env = append(os.Environ(),
		"TEST_REDIS_URL="+redisURL,
		"KUBEPILOT_CHECKPOINT_HELPER=save",
		"KUBEPILOT_CHECKPOINT_ID="+checkpointID,
		"KUBEPILOT_CHECKPOINT_PAYLOAD="+string(raw),
	)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("checkpoint helper failed: %v\n%s", runErr, output)
	}

	redis, err := NewRedis(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	defer redis.Close()
	checkpoint := EinoCheckpointStore{Redis: redis, TTL: time.Minute}
	loaded, exists, err := checkpoint.Get(context.Background(), checkpointID)
	if err != nil || !exists {
		t.Fatalf("checkpoint was not visible to the second process: exists=%v err=%v", exists, err)
	}
	if string(loaded) != string(raw) {
		t.Fatalf("checkpoint payload changed across processes: got=%s want=%s", loaded, raw)
	}
	var restored map[string]any
	if err := json.Unmarshal(loaded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored["interrupt_id"] != "interrupt-1" || restored["skill_hash"] != "skill-hash-1" {
		t.Fatalf("interrupt or skill snapshot did not restore: %+v", restored)
	}
	if err := checkpoint.Delete(context.Background(), checkpointID); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := checkpoint.Get(context.Background(), checkpointID); err != nil || exists {
		t.Fatalf("checkpoint delete did not persist: exists=%v err=%v", exists, err)
	}
}

func init() {
	if os.Getenv("KUBEPILOT_CHECKPOINT_HELPER") != "save" {
		return
	}
	redisURL := os.Getenv("TEST_REDIS_URL")
	checkpointID := os.Getenv("KUBEPILOT_CHECKPOINT_ID")
	payload := []byte(os.Getenv("KUBEPILOT_CHECKPOINT_PAYLOAD"))
	redis, err := NewRedis(redisURL)
	if err == nil {
		checkpoint := EinoCheckpointStore{Redis: redis, TTL: time.Minute}
		err = checkpoint.Set(context.Background(), checkpointID, payload)
		_ = redis.Close()
	}
	if err != nil {
		panic(errors.New("checkpoint helper failed"))
	}
	os.Exit(0)
}

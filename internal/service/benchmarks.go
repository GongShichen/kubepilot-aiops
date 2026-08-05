package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	artifactlayout "github.com/kubepilot-aiops/kubepilot/benchmark/artifactlayout"
	"github.com/oklog/ulid/v2"
)

type BenchmarkRun struct {
	ID           string    `json:"id"`
	Profile      string    `json:"profile"`
	Status       string    `json:"status"`
	Output       []string  `json:"output,omitempty"`
	Error        string    `json:"error,omitempty"`
	ArtifactRoot string    `json:"artifact_root"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type BenchmarkManager struct {
	Binary, AgentURL, Token, WebhookToken, Kubeconfig, ArtifactRoot string
	Hub                                                             *Hub
	mu                                                              sync.RWMutex
	runs                                                            map[string]*BenchmarkRun
	cancels                                                         map[string]context.CancelFunc
}

func NewBenchmarkManager(binary, agentURL, token, webhookToken, kubeconfig, artifactRoot string, hub *Hub) *BenchmarkManager {
	return &BenchmarkManager{Binary: binary, AgentURL: agentURL, Token: token, WebhookToken: webhookToken, Kubeconfig: kubeconfig, ArtifactRoot: artifactRoot, Hub: hub, runs: map[string]*BenchmarkRun{}, cancels: map[string]context.CancelFunc{}}
}
func (m *BenchmarkManager) Start(profile string, autoApprove bool) (*BenchmarkRun, error) {
	switch profile {
	case "smoke", "ci", "standard", "robustness", "correlation", "log-retrieval", "incident-retrieval", "full":
	default:
		return nil, fmt.Errorf("unsupported profile %q", profile)
	}
	id, now := ulid.Make().String(), time.Now().UTC()
	run := &BenchmarkRun{ID: id, Profile: profile, Status: "queued", ArtifactRoot: artifactlayout.RunDirectory(m.ArtifactRoot, artifactSuite(profile), artifactProfile(profile), now), CreatedAt: now, UpdatedAt: now}
	m.mu.Lock()
	m.runs[id] = run
	m.mu.Unlock()
	go m.execute(id, autoApprove)
	cp := *run
	return &cp, nil
}
func (m *BenchmarkManager) execute(id string, autoApprove bool) {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancels[id] = cancel
	run := m.runs[id]
	run.Status = "running"
	run.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()
	var commands [][]string
	switch run.Profile {
	case "smoke", "ci", "standard", "robustness":
		args := []string{"run", "--profile", run.Profile, "--run-id", run.ID, "--agent-url", m.AgentURL, "--token", m.Token, "--kubeconfig", m.Kubeconfig, "--artifacts", m.ArtifactRoot, "--artifact-dir", run.ArtifactRoot}
		if autoApprove {
			args = append(args, "--auto-approve")
		}
		commands = [][]string{args}
	case "correlation":
		commands = [][]string{{"correlation", "--agent-url", m.AgentURL, "--webhook-token", m.WebhookToken, "--output", filepath.Join(run.ArtifactRoot, "correlation-summary.json")}}
	case "log-retrieval":
		commands = [][]string{{"log-retrieval", "--corpus", filepath.Join(run.ArtifactRoot, "log-retrieval-500k.jsonl"), "--output", run.ArtifactRoot, "--count", "500000"}}
	case "incident-retrieval":
		commands = [][]string{{"incident-retrieval", "--output", run.ArtifactRoot}}
	case "full":
		diagnosisDir := filepath.Join(run.ArtifactRoot, "diagnosis")
		correlationDir := filepath.Join(run.ArtifactRoot, "correlation")
		logDir := filepath.Join(run.ArtifactRoot, "log-retrieval")
		incidentDir := filepath.Join(run.ArtifactRoot, "incident-retrieval")
		knowledgeDir := filepath.Join(run.ArtifactRoot, "knowledge-evolution")
		standard := []string{"run", "--profile", "standard", "--run-id", run.ID, "--agent-url", m.AgentURL, "--token", m.Token, "--kubeconfig", m.Kubeconfig, "--artifacts", m.ArtifactRoot, "--artifact-dir", diagnosisDir}
		if autoApprove {
			standard = append(standard, "--auto-approve")
		}
		commands = [][]string{standard, {"correlation", "--agent-url", m.AgentURL, "--webhook-token", m.WebhookToken, "--output", filepath.Join(correlationDir, "correlation-summary.json")}, {"log-retrieval", "--corpus", filepath.Join(logDir, "log-retrieval-500k.jsonl"), "--output", logDir, "--count", "500000"}, {"incident-retrieval", "--output", incidentDir}, {"intelligence", "--output", filepath.Join(knowledgeDir, "summary.json")}}
	}
	var err error
	for _, args := range commands {
		if err = m.runCommand(ctx, id, args); err != nil {
			break
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cancels, id)
	run = m.runs[id]
	if ctx.Err() != nil {
		run.Status = "cancelled"
	} else if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
	} else {
		run.Status = "completed"
	}
	run.UpdatedAt = time.Now().UTC()
}

func artifactSuite(profile string) string {
	switch profile {
	case "smoke", "ci", "standard", "robustness":
		return "diagnosis"
	case "correlation":
		return "correlation"
	case "log-retrieval":
		return "log-retrieval"
	case "incident-retrieval":
		return "incident-retrieval"
	case "full":
		return "autonomous"
	default:
		return profile
	}
}

func artifactProfile(profile string) string {
	switch profile {
	case "correlation", "log-retrieval", "incident-retrieval":
		return "full"
	default:
		return profile
	}
}
func (m *BenchmarkManager) runCommand(ctx context.Context, id string, args []string) error {
	cmd := exec.CommandContext(ctx, m.Binary, args...)
	cmd.Env = os.Environ()
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err = cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		m.append(id, scanner.Text())
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	return cmd.Wait()
}
func (m *BenchmarkManager) append(id, line string) {
	m.mu.Lock()
	run := m.runs[id]
	run.Output = append(run.Output, line)
	run.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()
	m.Hub.Publish("benchmark:"+id, []byte(line))
}
func (m *BenchmarkManager) Get(id string) (*BenchmarkRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	run := m.runs[id]
	if run == nil {
		return nil, fmt.Errorf("benchmark run not found")
	}
	cp := *run
	cp.Output = append([]string(nil), run.Output...)
	return &cp, nil
}
func (m *BenchmarkManager) List() []BenchmarkRun {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]BenchmarkRun, 0, len(m.runs))
	for _, run := range m.runs {
		out = append(out, *run)
	}
	return out
}
func (m *BenchmarkManager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cancel := m.cancels[id]
	if cancel == nil {
		return fmt.Errorf("benchmark run is not active")
	}
	cancel()
	return nil
}

func (m *BenchmarkManager) Results(id string) (any, error) {
	run, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	paths := []string{
		filepath.Join(run.ArtifactRoot, "summary.json"),
		filepath.Join(run.ArtifactRoot, "diagnosis", "summary.json"),
		filepath.Join(run.ArtifactRoot, "correlation", "correlation-summary.json"),
		filepath.Join(run.ArtifactRoot, "knowledge-evolution", "summary.json"),
		filepath.Join(run.ArtifactRoot, "correlation-summary.json"),
		filepath.Join(run.ArtifactRoot, "log_retrieval_report.json"),
		filepath.Join(run.ArtifactRoot, "incident_retrieval_report.json"),
		filepath.Join(run.ArtifactRoot, "benchmark_report.json"),
		filepath.Join(run.ArtifactRoot, "log-retrieval", "log_retrieval_report.json"),
		filepath.Join(run.ArtifactRoot, "incident-retrieval", "incident_retrieval_report.json"),
	}
	var path string
	for _, candidate := range paths {
		if _, statErr := os.Stat(candidate); statErr == nil {
			path = candidate
			break
		}
	}
	if path == "" {
		return nil, os.ErrNotExist
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value any
	if err = json.Unmarshal(b, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func (m *BenchmarkManager) Artifacts(id string) ([]map[string]any, error) {
	run, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0)
	err = filepath.WalkDir(run.ArtifactRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		rel, relErr := filepath.Rel(run.ArtifactRoot, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, map[string]any{"name": filepath.ToSlash(rel), "size": info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

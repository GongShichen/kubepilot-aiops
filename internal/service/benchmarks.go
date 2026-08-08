package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	artifactlayout "github.com/kubepilot-aiops/kubepilot/internal/artifacts"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/oklog/ulid/v2"
)

type BenchmarkRun = domain.BenchmarkRun

type BenchmarkRequest struct {
	Profile      string
	Strategies   []string
	DatasetSplit string
	Seeds        []int64
	Repetitions  int
	ModelProfile string
	AutoApprove  bool
}

type BenchmarkRunStore interface {
	SaveBenchmarkRun(context.Context, domain.BenchmarkRun) error
	SaveBenchmarkCaseResult(context.Context, domain.BenchmarkCaseResult) error
	ListBenchmarkRuns(context.Context) ([]domain.BenchmarkRun, error)
	InterruptActiveBenchmarkRuns(context.Context, time.Time) error
}

type BenchmarkManager struct {
	Binary, AgentURL, Token, WebhookToken, Kubeconfig, ArtifactRoot string
	Hub                                                             *Hub
	Store                                                           BenchmarkRunStore
	mu                                                              sync.RWMutex
	runs                                                            map[string]*BenchmarkRun
	cancels                                                         map[string]context.CancelFunc
	paused                                                          map[string]string
}

func NewBenchmarkManager(binary, agentURL, token, webhookToken, kubeconfig, artifactRoot string, hub *Hub, stores ...BenchmarkRunStore) *BenchmarkManager {
	manager := &BenchmarkManager{Binary: binary, AgentURL: agentURL, Token: token, WebhookToken: webhookToken, Kubeconfig: kubeconfig, ArtifactRoot: artifactRoot, Hub: hub, runs: map[string]*BenchmarkRun{}, cancels: map[string]context.CancelFunc{}, paused: map[string]string{}}
	if len(stores) > 0 {
		manager.Store = stores[0]
		manager.restorePersistedRuns()
	}
	return manager
}
func (m *BenchmarkManager) Start(profile string, autoApprove bool) (*BenchmarkRun, error) {
	return m.StartRequest(BenchmarkRequest{Profile: profile, Strategies: []string{domain.DiagnosisMethodKubePilot}, DatasetSplit: "test", Seeds: []int64{20260803, 20260804, 20260805}, Repetitions: 1, AutoApprove: autoApprove})
}

func (m *BenchmarkManager) StartRequest(request BenchmarkRequest) (*BenchmarkRun, error) {
	profile := request.Profile
	switch profile {
	case "smoke", "ci", "standard", "robustness", "correlation", "log-retrieval", "incident-retrieval", "full":
	default:
		return nil, fmt.Errorf("unsupported profile %q", profile)
	}
	if request.DatasetSplit == "" {
		request.DatasetSplit = "test"
	}
	if request.DatasetSplit != "dev" && request.DatasetSplit != "validation" && request.DatasetSplit != "test" && request.DatasetSplit != "all" {
		return nil, fmt.Errorf("unsupported dataset split %q", request.DatasetSplit)
	}
	if request.Repetitions <= 0 {
		request.Repetitions = 1
	}
	if len(request.Seeds) == 0 {
		request.Seeds = []int64{20260803, 20260804, 20260805}
	}
	if len(request.Strategies) == 0 {
		request.Strategies = []string{domain.DiagnosisMethodKubePilot, domain.DiagnosisMethodKubePilotNoReflection, domain.DiagnosisMethodKubePilotNoOptionalSkills}
	}
	seenStrategies := map[string]bool{}
	for index, strategy := range request.Strategies {
		canonical, ok := domain.NormalizeDiagnosisMethod(strategy)
		if !ok || !domain.IsKubePilotBrainMethod(canonical) || seenStrategies[canonical] {
			return nil, fmt.Errorf("invalid or duplicate strategy %q", strategy)
		}
		seenStrategies[canonical] = true
		request.Strategies[index] = canonical
	}
	id, now := ulid.Make().String(), time.Now().UTC()
	run := &BenchmarkRun{ID: id, Profile: profile, Status: "queued", Strategies: append([]string(nil), request.Strategies...), DatasetSplit: request.DatasetSplit, Seeds: append([]int64(nil), request.Seeds...), Repetitions: request.Repetitions, ModelProfile: request.ModelProfile, AutoApprove: request.AutoApprove, ArtifactRoot: artifactlayout.RunDirectory(m.ArtifactRoot, artifactSuite(profile), artifactProfile(profile), now), CreatedAt: now, UpdatedAt: now}
	m.mu.Lock()
	m.runs[id] = run
	snapshot := cloneBenchmarkRun(run)
	m.mu.Unlock()
	m.persist(snapshot)
	go m.execute(id, request.AutoApprove, false)
	return snapshot, nil
}
func (m *BenchmarkManager) execute(id string, autoApprove, resume bool) {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancels[id] = cancel
	run := m.runs[id]
	run.Status = "running"
	run.UpdatedAt = time.Now().UTC()
	m.persistLocked(run)
	m.mu.Unlock()
	var commands [][]string
	switch run.Profile {
	case "smoke", "ci", "standard", "robustness":
		args := []string{"run", "--profile", run.Profile, "--run-id", run.ID, "--agent-url", m.AgentURL, "--token", m.Token, "--kubeconfig", m.Kubeconfig, "--artifacts", m.ArtifactRoot, "--artifact-dir", run.ArtifactRoot}
		args = append(args, "--dataset-split", run.DatasetSplit, "--seeds", joinSeeds(run.Seeds), "--repetitions", fmt.Sprint(run.Repetitions), "--model-profile", run.ModelProfile)
		if len(run.Strategies) > 1 {
			args = append(args, "--compare-methods", "--strategies", strings.Join(run.Strategies, ","))
		} else {
			args = append(args, "--diagnosis-method", run.Strategies[0])
		}
		if autoApprove {
			args = append(args, "--auto-approve")
		}
		if resume {
			args = append(args, "--resume=true")
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
		standard := []string{"run", "--profile", "standard", "--run-id", run.ID, "--agent-url", m.AgentURL, "--token", m.Token, "--kubeconfig", m.Kubeconfig, "--artifacts", m.ArtifactRoot, "--artifact-dir", diagnosisDir, "--dataset-split", run.DatasetSplit, "--seeds", joinSeeds(run.Seeds), "--repetitions", fmt.Sprint(run.Repetitions), "--model-profile", run.ModelProfile}
		if len(run.Strategies) > 1 {
			standard = append(standard, "--compare-methods", "--strategies", strings.Join(run.Strategies, ","))
		} else {
			standard = append(standard, "--diagnosis-method", run.Strategies[0])
		}
		if autoApprove {
			standard = append(standard, "--auto-approve")
		}
		if resume {
			standard = append(standard, "--resume=true")
		}
		commands = [][]string{standard, {"correlation", "--agent-url", m.AgentURL, "--webhook-token", m.WebhookToken, "--output", filepath.Join(correlationDir, "correlation-summary.json")}, {"log-retrieval", "--corpus", filepath.Join(logDir, "log-retrieval-500k.jsonl"), "--output", logDir, "--count", "500000"}, {"incident-retrieval", "--output", incidentDir}, {"intelligence", "--output", filepath.Join(knowledgeDir, "summary.json")}}
	}
	var err error
	for _, args := range commands {
		if err = m.runCommand(ctx, id, args); err != nil {
			break
		}
	}
	if err == nil {
		err = m.persistCaseResults(run)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cancels, id)
	run = m.runs[id]
	if reason := m.paused[id]; reason != "" {
		run.Status = "paused"
		run.Error = reason
		delete(m.paused, id)
	} else if ctx.Err() != nil {
		run.Status = "cancelled"
	} else if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
	} else {
		run.Status = "completed"
	}
	run.UpdatedAt = time.Now().UTC()
	run.ResultArtifact = resultArtifact(run)
	m.persistLocked(run)
}

func (m *BenchmarkManager) Resume(id string) (*BenchmarkRun, error) {
	m.mu.Lock()
	run := m.runs[id]
	if run == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("benchmark run not found")
	}
	if run.Status != "interrupted" && run.Status != "failed" && run.Status != "cancelled" && run.Status != "paused" {
		m.mu.Unlock()
		return nil, fmt.Errorf("benchmark run is not resumable")
	}
	if run.Profile != "smoke" && run.Profile != "ci" && run.Profile != "standard" && run.Profile != "robustness" && run.Profile != "full" {
		m.mu.Unlock()
		return nil, fmt.Errorf("benchmark profile %s does not use case checkpoints", run.Profile)
	}
	run.Status = "queued"
	run.Error = ""
	run.ResultArtifact = ""
	run.UpdatedAt = time.Now().UTC()
	snapshot := cloneBenchmarkRun(run)
	autoApprove := run.AutoApprove
	m.mu.Unlock()
	m.persist(snapshot)
	go m.execute(id, autoApprove, true)
	return snapshot, nil
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
	m.persistLocked(run)
	m.mu.Unlock()
	if m.Hub != nil {
		m.Hub.Publish("benchmark:"+id, []byte(line))
	}
}
func (m *BenchmarkManager) Get(id string) (*BenchmarkRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	run := m.runs[id]
	if run == nil {
		return nil, fmt.Errorf("benchmark run not found")
	}
	return cloneBenchmarkRun(run), nil
}
func (m *BenchmarkManager) List() []BenchmarkRun {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]BenchmarkRun, 0, len(m.runs))
	for _, run := range m.runs {
		out = append(out, *cloneBenchmarkRun(run))
	}
	return out
}

func cloneBenchmarkRun(run *BenchmarkRun) *BenchmarkRun {
	if run == nil {
		return nil
	}
	copy := *run
	copy.Output = append([]string(nil), run.Output...)
	copy.Strategies = append([]string(nil), run.Strategies...)
	copy.Seeds = append([]int64(nil), run.Seeds...)
	return &copy
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
	if run.ResultArtifact == "" {
		return nil, os.ErrNotExist
	}
	b, err := os.ReadFile(run.ResultArtifact)
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

func (m *BenchmarkManager) restorePersistedRuns() {
	if m.Store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.Store.InterruptActiveBenchmarkRuns(ctx, time.Now().UTC())
	runs, err := m.Store.ListBenchmarkRuns(ctx)
	if err != nil {
		return
	}
	for index := range runs {
		run := runs[index]
		m.runs[run.ID] = cloneBenchmarkRun(&run)
	}
}

func (m *BenchmarkManager) persist(run *BenchmarkRun) {
	if m.Store == nil || run == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.Store.SaveBenchmarkRun(ctx, *cloneBenchmarkRun(run))
}

func (m *BenchmarkManager) persistLocked(run *BenchmarkRun) {
	m.persist(run)
}

func joinSeeds(seeds []int64) string {
	values := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		values = append(values, strconv.FormatInt(seed, 10))
	}
	return strings.Join(values, ",")
}

func resultArtifact(run *BenchmarkRun) string {
	if run == nil || run.Status != "completed" {
		return ""
	}
	switch run.Profile {
	case "smoke", "ci", "standard", "robustness":
		if len(run.Strategies) > 1 {
			return filepath.Join(run.ArtifactRoot, "diagnosis-comparison.json")
		}
		return filepath.Join(run.ArtifactRoot, "summary.json")
	case "correlation":
		return filepath.Join(run.ArtifactRoot, "correlation-summary.json")
	case "log-retrieval":
		return filepath.Join(run.ArtifactRoot, "log_retrieval_report.json")
	case "incident-retrieval":
		return filepath.Join(run.ArtifactRoot, "incident_retrieval_report.json")
	case "full":
		if len(run.Strategies) > 1 {
			return filepath.Join(run.ArtifactRoot, "diagnosis", "diagnosis-comparison.json")
		}
		return filepath.Join(run.ArtifactRoot, "diagnosis", "summary.json")
	default:
		return ""
	}
}

func (m *BenchmarkManager) persistCaseResults(run *BenchmarkRun) error {
	if m.Store == nil || run == nil || (run.Profile != "smoke" && run.Profile != "ci" && run.Profile != "standard" && run.Profile != "robustness" && run.Profile != "full") {
		return nil
	}
	caseRoot := run.ArtifactRoot
	if run.Profile == "full" {
		caseRoot = filepath.Join(caseRoot, "diagnosis")
	}
	for _, strategy := range run.Strategies {
		path := filepath.Join(caseRoot, "cases.jsonl")
		if len(run.Strategies) > 1 {
			path = filepath.Join(caseRoot, strategy, "cases.jsonl")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), 2<<20)
		for scanner.Scan() {
			raw := append([]byte(nil), scanner.Bytes()...)
			var item struct {
				CaseID     string `json:"case_id"`
				Seed       int64  `json:"seed"`
				Repetition int    `json:"repetition"`
				Status     string `json:"status"`
			}
			if err = json.Unmarshal(raw, &item); err != nil {
				file.Close()
				return err
			}
			if err = m.Store.SaveBenchmarkCaseResult(context.Background(), domain.BenchmarkCaseResult{RunID: run.ID, StrategyID: strategy, CaseID: item.CaseID, Seed: item.Seed, Repetition: item.Repetition, Status: item.Status, Result: raw}); err != nil {
				file.Close()
				return err
			}
		}
		err = scanner.Err()
		file.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func benchmarkCasePaths(run *BenchmarkRun) []string {
	if run == nil {
		return nil
	}
	roots := []string{run.ArtifactRoot, filepath.Join(run.ArtifactRoot, "diagnosis")}
	seen := map[string]bool{}
	var out []string
	for _, root := range roots {
		for _, path := range []string{filepath.Join(root, "cases.jsonl")} {
			if _, err := os.Stat(path); err == nil && !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
		}
		matches, _ := filepath.Glob(filepath.Join(root, "*", "cases.jsonl"))
		for _, path := range matches {
			if !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
		}
	}
	sort.Strings(out)
	return out
}

package runner

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"sync"

	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
)

const StableShardPolicy = "case-seed-repetition-hash"

// ConcurrencyGate is shared by all workers. A permit covers one complete case,
// including diagnosis and recovery, so a recovery model call cannot exceed the
// configured global model concurrency after diagnosis releases its slot.
type ConcurrencyGate struct {
	permits chan struct{}
}

func NewConcurrencyGate(limit int) (*ConcurrencyGate, error) {
	if limit < 1 {
		return nil, fmt.Errorf("concurrency limit must be positive")
	}
	return &ConcurrencyGate{permits: make(chan struct{}, limit)}, nil
}

func (g *ConcurrencyGate) Acquire(ctx context.Context) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	select {
	case g.permits <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-g.permits }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type ParallelWorker struct {
	ID        string
	Namespace string
	Runner    *Runner
}

type ParallelRunner struct {
	Workers []ParallelWorker
}

type indexedScenario struct {
	index    int
	scenario scenarios.Scenario
}

type indexedResult struct {
	index  int
	result reporter.CaseResult
}

// Run uses a stable hash instead of completion order to assign cases. Each
// worker executes its shard serially, while different worker namespaces run in
// parallel. Returned results preserve the caller's original case order.
func (p ParallelRunner) Run(ctx context.Context, items []scenarios.Scenario) ([]reporter.CaseResult, error) {
	if len(p.Workers) == 0 {
		return nil, fmt.Errorf("at least one parallel worker is required")
	}
	shards := make([][]indexedScenario, len(p.Workers))
	for i, item := range items {
		worker := StableWorkerIndex(item, len(p.Workers))
		copy := item
		copy.Namespace = p.Workers[worker].Namespace
		shards[worker] = append(shards[worker], indexedScenario{index: i, scenario: copy})
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan indexedResult, len(items))
	errors := make(chan error, len(p.Workers))
	var wg sync.WaitGroup
	for workerIndex := range p.Workers {
		worker := p.Workers[workerIndex]
		if worker.Runner == nil {
			return nil, fmt.Errorf("worker %s has no runner", worker.ID)
		}
		worker.Runner.WorkerID = worker.ID
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, item := range shards[workerIndex] {
				if runCtx.Err() != nil {
					return
				}
				caseResults := worker.Runner.Run(runCtx, []scenarios.Scenario{item.scenario})
				if len(caseResults) == 0 {
					return
				}
				result := caseResults[0]
				results <- indexedResult{index: item.index, result: result}
				if result.Status == "cleanup_failed" {
					select {
					case errors <- fmt.Errorf("worker %s cleanup failed for case %s", worker.ID, result.CaseID):
					default:
					}
					cancel()
					return
				}
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errors)

	ordered := make([]indexedResult, 0, len(results))
	for result := range results {
		ordered = append(ordered, result)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].index < ordered[j].index })
	out := make([]reporter.CaseResult, len(ordered))
	for i := range ordered {
		out[i] = ordered[i].result
	}
	for err := range errors {
		return out, err
	}
	return out, nil
}

func StableWorkerIndex(item scenarios.Scenario, workers int) int {
	if workers <= 1 {
		return 0
	}
	key := item.ID + "\x00" + strconv.FormatInt(item.Seed, 10) + "\x00" + strconv.Itoa(item.Repetition)
	digest := sha256.Sum256([]byte(key))
	return int(binary.BigEndian.Uint64(digest[:8]) % uint64(workers))
}

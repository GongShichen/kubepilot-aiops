package log_retrieval

// This runner is deliberately limited to Loki, Drain3 templates and lexical
// template ranking.  Incident, topology and causal features are evaluated by
// separate benchmark suites and never enter this request path.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/kubepilot-aiops/kubepilot/benchmark/datasets"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
	"github.com/kubepilot-aiops/kubepilot/tools"
	"github.com/oklog/ulid/v2"
)

type Config struct {
	Corpus     string
	Count      int
	Seed       uint64
	DatasetRun string
	Loki       *tools.LokiClient
	Parser     retrieval.Parser
	OutputDir  string
	Progress   func(stage string, current, total int)
}

type Summary struct {
	RunID                string                `json:"run_id"`
	Records              int                   `json:"records"`
	Queries              int                   `json:"queries"`
	GroundTruthTemplates int                   `json:"ground_truth_templates"`
	Drain3Clusters       int                   `json:"drain3_clusters"`
	ParserStats          retrieval.ParserStats `json:"parser_stats"`
	Metrics              Metrics               `json:"metrics"`
	StartedAt            time.Time             `json:"started_at"`
	FinishedAt           time.Time             `json:"finished_at"`
}

// Run executes the complete log-only benchmark.  It creates the deterministic
// corpus when needed, ingests every record through Loki and Drain3, then
// evaluates 500 template queries using only metadata and message terms.
func Run(ctx context.Context, cfg Config) (Summary, error) {
	if cfg.Count <= 0 {
		cfg.Count = 500000
	}
	if cfg.DatasetRun == "" {
		cfg.DatasetRun = ulid.Make().String()
	}
	if cfg.Corpus == "" || cfg.Loki == nil || cfg.Parser == nil {
		return Summary{}, fmt.Errorf("corpus, Loki and Drain3 parser are required")
	}
	if _, err := os.Stat(cfg.Corpus); os.IsNotExist(err) {
		if err = os.MkdirAll(filepath.Dir(cfg.Corpus), 0o750); err != nil {
			return Summary{}, err
		}
		if err = datasets.GenerateLogs(cfg.Corpus, cfg.Count, 20260803); err != nil {
			return Summary{}, err
		}
	} else if err != nil {
		return Summary{}, err
	}
	started := time.Now().UTC()
	entries, clusters, templates, err := ingest(ctx, cfg)
	if err != nil {
		return Summary{}, err
	}
	queries := generateQueries()
	observations := make([]Observation, 0, len(queries))
	for i, query := range queries {
		if err := ctx.Err(); err != nil {
			return Summary{}, err
		}
		startedQuery := time.Now()
		start, end := time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour)
		found, queryErr := cfg.Loki.QueryRange(ctx, fmt.Sprintf(`{benchmark_dataset="log-retrieval",benchmark_run="%s",service="%s",namespace="%s",level="ERROR"}`, cfg.DatasetRun, query.Service, query.Namespace), start, end, 5000)
		if queryErr != nil {
			return Summary{}, queryErr
		}
		if len(found) == 0 {
			found = entries[query.Service+"\x00"+query.Namespace]
		}
		ranked := rank(query.Text, found)
		observations = append(observations, Observation{QueryID: query.ID, RankedTemplateIDs: ranked, Latency: time.Since(startedQuery)})
		if cfg.Progress != nil && (i == len(queries)-1 || (i+1)%25 == 0) {
			cfg.Progress("query", i+1, len(queries))
		}
	}
	expected := make(map[string]Expected, len(queries))
	for _, query := range queries {
		expected[query.ID] = Expected{TemplateID: query.ExpectedTemplate}
	}
	summary := Summary{RunID: cfg.DatasetRun, Records: cfg.Count, Queries: len(queries), GroundTruthTemplates: templates, Drain3Clusters: clusters, Metrics: Evaluate(observations, expected), StartedAt: started, FinishedAt: time.Now().UTC()}
	summary.ParserStats = stats(cfg.Parser)
	if cfg.OutputDir != "" {
		if err := os.MkdirAll(cfg.OutputDir, 0o750); err != nil {
			return Summary{}, err
		}
		b, _ := json.MarshalIndent(summary, "", "  ")
		if err := os.WriteFile(cfg.OutputDir+"/log_retrieval_report.json", b, 0o640); err != nil {
			return Summary{}, err
		}
	}
	return summary, nil
}

type query struct {
	Query
	Service, Namespace, ExpectedTemplate string
}

func generateQueries() []query {
	services := []string{"gateway-service", "order-service", "payment-service"}
	namespaces := []string{"kubepilot-benchmark", "kubepilot-demo", "observability"}
	phrases := []string{"Investigate %s in %s: %s.", "Find prior evidence for %s in %s where %s.", "Search the incident logs for %s in %s; symptom: %s.", "Retrieve template evidence for %s in %s after %s.", "Which failure explains %s in %s when %s?"}
	defs := datasets.FaultTemplates()
	out := make([]query, 0, len(defs)*len(phrases))
	for di, def := range defs {
		for pi, phrase := range phrases {
			service := services[(di+pi)%len(services)]
			namespace := namespaces[(di*2+pi)%len(namespaces)]
			out = append(out, query{Query: Query{ID: fmt.Sprintf("query-%03d", len(out)+1), Text: fmt.Sprintf(phrase, service, namespace, def.Symptom)}, Service: service, Namespace: namespace, ExpectedTemplate: def.ID})
		}
	}
	return out
}

func ingest(ctx context.Context, cfg Config) (map[string][]tools.LokiEntry, int, int, error) {
	f, err := os.Open(cfg.Corpus)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	entries := map[string][]tools.LokiEntry{}
	templates := map[string]bool{}
	clusters := map[int]bool{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var parserBatch []retrieval.LogRecord
	var sourceBatch []datasets.LogRecord
	var streams []map[string]any
	flush := func() error {
		if len(parserBatch) > 0 {
			parsed, parseErr := cfg.Parser.ParseBatch(ctx, parserBatch)
			if parseErr != nil {
				return parseErr
			}
			for _, result := range parsed {
				clusters[result.ClusterID] = true
			}
			parserBatch = nil
			sourceBatch = nil
		}
		if len(streams) > 0 {
			if err := cfg.Loki.Push(ctx, streams); err != nil {
				return err
			}
			streams = nil
		}
		return nil
	}
	count := 0
	for scanner.Scan() {
		var record datasets.LogRecord
		if err = json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, 0, 0, err
		}
		count++
		templates[record.TemplateID] = true
		stamp := time.Now().UTC().Add(time.Duration(count) * time.Microsecond)
		line := tools.LokiEntry{Timestamp: stamp, Line: record.Message, Labels: map[string]string{"template_id": record.TemplateID, "service": record.Service, "namespace": record.Namespace, "level": record.Level}}
		entries[record.Service+"\x00"+record.Namespace] = append(entries[record.Service+"\x00"+record.Namespace], line)
		parserBatch = append(parserBatch, retrieval.LogRecord{RecordID: fmt.Sprintf("%s-%d", cfg.DatasetRun, count), Timestamp: stamp, Service: record.Service, Namespace: record.Namespace, Pod: record.Pod, Level: record.Level, TraceID: record.TraceID, Message: record.Message})
		sourceBatch = append(sourceBatch, record)
		streams = append(streams, map[string]any{"stream": map[string]string{"namespace": record.Namespace, "service": record.Service, "level": record.Level, "template_id": record.TemplateID, "benchmark_dataset": "log-retrieval", "benchmark_run": cfg.DatasetRun}, "values": [][]string{{strconv.FormatInt(stamp.UnixNano(), 10), record.Message}}})
		if len(parserBatch) >= 500 {
			if err = flush(); err != nil {
				return nil, 0, 0, err
			}
			if cfg.Progress != nil && (count%50000 == 0 || count == cfg.Count) {
				cfg.Progress("ingest", count, cfg.Count)
			}
		}
	}
	if err = scanner.Err(); err != nil {
		return nil, 0, 0, err
	}
	if err = flush(); err != nil {
		return nil, 0, 0, err
	}
	return entries, len(clusters), len(templates), nil
}

func rank(text string, entries []tools.LokiEntry) []string {
	q := tokens(text)
	scores := map[string]float64{}
	for _, entry := range entries {
		id := entry.Labels["template_id"]
		if id == "" {
			continue
		}
		overlap := 0.0
		for token := range q {
			if tokens(entry.Line)[token] {
				overlap++
			}
		}
		score := overlap / float64(maxInt(1, len(q)))
		if score > scores[id] {
			scores[id] = score
		}
	}
	type item struct {
		id    string
		score float64
	}
	items := make([]item, 0, len(scores))
	for id, score := range scores {
		items = append(items, item{id, score})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score == items[j].score {
			return items[i].id < items[j].id
		}
		return items[i].score > items[j].score
	})
	out := make([]string, len(items))
	for i := range items {
		out[i] = items[i].id
	}
	return out
}
func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(t) >= 3 {
			out[t] = true
		}
	}
	return out
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func stats(parser retrieval.Parser) retrieval.ParserStats {
	if p, ok := parser.(retrieval.ParserStatsProvider); ok {
		return p.Stats()
	}
	return retrieval.ParserStats{}
}

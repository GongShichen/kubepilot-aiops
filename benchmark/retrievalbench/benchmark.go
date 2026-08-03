package retrievalbench

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/kubepilot-aiops/kubepilot/benchmark/datasets"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
	"github.com/kubepilot-aiops/kubepilot/tools"
)

type Query struct {
	ID, Text, Category, Service, Namespace, ExpectedTemplate string
}

type Result struct {
	QueryID        string
	Strategy       string
	Rank           int
	Latency        time.Duration
	BackendLatency time.Duration
	CandidateCount int
	CorpusCount    int
}

type Metrics struct {
	Strategy           string  `json:"strategy"`
	Recall1            float64 `json:"recall_at_1"`
	Recall5            float64 `json:"recall_at_5"`
	Recall10           float64 `json:"recall_at_10"`
	MRR                float64 `json:"mrr"`
	P50MS              float64 `json:"p50_ms"`
	P95MS              float64 `json:"p95_ms"`
	P99MS              float64 `json:"p99_ms"`
	BackendP50MS       float64 `json:"backend_p50_ms"`
	BackendP95MS       float64 `json:"backend_p95_ms"`
	BackendP99MS       float64 `json:"backend_p99_ms"`
	AverageCandidates  float64 `json:"average_candidates"`
	CandidateReduction float64 `json:"candidate_reduction"`
}

type Summary struct {
	Records                     int            `json:"records"`
	GroundTruthTemplates        int            `json:"ground_truth_templates"`
	IndexedDocuments            int            `json:"indexed_documents"`
	Drain3Clusters              int            `json:"drain3_clusters"`
	Drain3CompressionRate       float64        `json:"drain3_compression_rate"`
	Drain3ClusterPurity         float64        `json:"drain3_cluster_purity"`
	Queries                     int            `json:"queries"`
	WSRecords                   int            `json:"websocket_records"`
	WSBatches                   int            `json:"websocket_batches"`
	WSAttempts                  int            `json:"websocket_attempts"`
	WSRetries                   int            `json:"websocket_retries"`
	WSReconnects                int            `json:"websocket_reconnects"`
	WSDurationMS                float64        `json:"websocket_duration_ms"`
	WSP50MS                     float64        `json:"websocket_batch_p50_ms"`
	WSP95MS                     float64        `json:"websocket_batch_p95_ms"`
	WSP99MS                     float64        `json:"websocket_batch_p99_ms"`
	WSThroughputRPS             float64        `json:"websocket_throughput_records_per_second"`
	LokiPushDurationMS          float64        `json:"loki_push_duration_ms"`
	EmbeddingCalls              int            `json:"embedding_calls"`
	DocumentEmbeddingDurationMS float64        `json:"document_embedding_duration_ms"`
	QueryEmbeddingDurationMS    float64        `json:"query_embedding_duration_ms"`
	MilvusUpsertDurationMS      float64        `json:"milvus_upsert_duration_ms"`
	RecordTypeDistribution      map[string]int `json:"record_type_distribution"`
	NamespaceDistribution       map[string]int `json:"namespace_distribution"`
	ServiceDistribution         map[string]int `json:"service_distribution"`
	Metrics                     []Metrics      `json:"metrics"`
}

type Config struct {
	Corpus, OutputDir  string
	DatasetRun         string
	Count              int
	Seed               uint64
	EmbeddingBatchSize int
	Loki               *tools.LokiClient
	Parser             retrieval.Parser
	Embedder           retrieval.EmbeddingClient
	Milvus             *retrieval.MilvusStore
	Progress           func(stage string, current, total int)
}

type loadResult struct {
	Documents        map[string]datasets.LogRecord
	Records          int
	WSDuration       time.Duration
	WSBatchLatencies []float64
	LokiDuration     time.Duration
	LokiStart        time.Time
	LokiEnd          time.Time
	ClusterTruth     map[int]map[string]int
	GroundTruth      map[string]bool
	RecordTypes      map[string]int
	Namespaces       map[string]int
	Services         map[string]int
}

func Run(ctx context.Context, cfg Config) (Summary, error) {
	if cfg.Count <= 0 {
		cfg.Count = 500000
	}
	if cfg.EmbeddingBatchSize <= 0 {
		cfg.EmbeddingBatchSize = 10
	}
	if cfg.DatasetRun == "" {
		return Summary{}, fmt.Errorf("retrieval dataset run ID is required")
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o750); err != nil {
		return Summary{}, err
	}
	if _, err := os.Stat(cfg.Corpus); os.IsNotExist(err) {
		if err = datasets.GenerateLogs(cfg.Corpus, cfg.Count, cfg.Seed); err != nil {
			return Summary{}, err
		}
	}
	if err := cfg.Milvus.Ensure(ctx); err != nil {
		return Summary{}, err
	}
	loaded, err := load(ctx, cfg)
	if err != nil {
		return Summary{}, err
	}

	docKeys := make([]string, 0, len(loaded.Documents))
	for key := range loaded.Documents {
		docKeys = append(docKeys, key)
	}
	sort.Strings(docKeys)
	docs := make([]retrieval.Document, 0, len(docKeys))
	texts := make([]string, 0, len(docKeys))
	for _, id := range docKeys {
		record := loaded.Documents[id]
		texts = append(texts, record.Namespace+" "+record.Service+" "+record.Message)
		docs = append(docs, retrieval.Document{
			ID: id, Service: record.Service, Namespace: record.Namespace,
			Category: record.Category, Template: record.TemplateID,
			RootCause: record.Category + " fault", Recovery: "restore_baseline",
		})
	}
	documentEmbeddingStarted := time.Now()
	vectors, calls, err := embedBatches(ctx, cfg.Embedder, texts, cfg.EmbeddingBatchSize)
	documentEmbeddingDuration := time.Since(documentEmbeddingStarted)
	if err != nil {
		return Summary{}, err
	}
	progress(cfg, "document_embeddings", len(texts), len(texts))
	for i := range docs {
		docs[i].Vector = vectors[i]
	}
	milvusUpsertStarted := time.Now()
	if err = upsertBatches(ctx, cfg.Milvus, docs, 100); err != nil {
		return Summary{}, err
	}
	milvusUpsertDuration := time.Since(milvusUpsertStarted)
	progress(cfg, "milvus_upsert", len(docs), len(docs))

	queries := generateQueries()
	queryTexts := make([]string, len(queries))
	for i := range queries {
		queryTexts[i] = queries[i].Text
	}
	queryEmbeddingStarted := time.Now()
	queryVectors, queryCalls, err := embedBatches(ctx, cfg.Embedder, queryTexts, cfg.EmbeddingBatchSize)
	queryEmbeddingDuration := time.Since(queryEmbeddingStarted)
	if err != nil {
		return Summary{}, err
	}
	progress(cfg, "query_embeddings", len(queries), len(queries))
	queryEmbeddingShare := queryEmbeddingDuration / time.Duration(len(queries))

	results := make([]Result, 0, len(queries)*3)
	for i, query := range queries {
		queryStart, queryEnd := lokiQueryWindow(loaded.LokiStart, loaded.LokiEnd)
		lokiStarted := time.Now()
		entries, queryErr := cfg.Loki.QueryRange(ctx,
			fmt.Sprintf(`{namespace=%q,service=%q,level="ERROR",benchmark_dataset="retrieval",benchmark_run=%q}`,
				query.Namespace, query.Service, cfg.DatasetRun),
			queryStart, queryEnd, 5000)
		if queryErr != nil {
			return Summary{}, queryErr
		}
		ranked, candidateCount := lexicalRank(query.Text, entries)
		lokiDuration := time.Since(lokiStarted)
		results = append(results, Result{
			QueryID: query.ID, Strategy: "loki", Rank: rank(query.ExpectedTemplate, ranked),
			Latency: lokiDuration, BackendLatency: lokiDuration,
			CandidateCount: candidateCount, CorpusCount: len(docs),
		})

		semanticStarted := time.Now()
		found, searchErr := cfg.Milvus.Search(ctx, queryVectors[i], nil, 10)
		if searchErr != nil {
			return Summary{}, searchErr
		}
		semanticSearchDuration := time.Since(semanticStarted)
		results = append(results, Result{
			QueryID: query.ID, Strategy: "semantic", Rank: rankDocs(query, found),
			Latency: semanticSearchDuration + queryEmbeddingShare, BackendLatency: semanticSearchDuration,
			CandidateCount: len(docs), CorpusCount: len(docs),
		})

		hybridCandidates := documentCount(docs, query.Namespace, query.Service)
		hybridStarted := time.Now()
		found, searchErr = cfg.Milvus.Search(ctx, queryVectors[i], map[string]string{
			"namespace": query.Namespace, "service": query.Service,
		}, 10)
		if searchErr != nil {
			return Summary{}, searchErr
		}
		hybridSearchDuration := time.Since(hybridStarted)
		results = append(results, Result{
			QueryID: query.ID, Strategy: "hybrid", Rank: rankDocs(query, found),
			Latency: hybridSearchDuration + queryEmbeddingShare, BackendLatency: hybridSearchDuration,
			CandidateCount: hybridCandidates, CorpusCount: len(docs),
		})
		if (i+1)%50 == 0 || i+1 == len(queries) {
			progress(cfg, "queries", i+1, len(queries))
		}
	}
	if err = writeResults(cfg.OutputDir, results); err != nil {
		return Summary{}, err
	}

	clusters, purity := clusterQuality(loaded.ClusterTruth)
	wsSeconds := loaded.WSDuration.Seconds()
	wsThroughput := 0.0
	if wsSeconds > 0 {
		wsThroughput = float64(loaded.Records) / wsSeconds
	}
	batchLatencies := append([]float64(nil), loaded.WSBatchLatencies...)
	sort.Float64s(batchLatencies)
	parserStats := retrieval.ParserStats{}
	if provider, ok := cfg.Parser.(retrieval.ParserStatsProvider); ok {
		parserStats = provider.Stats()
	}
	summary := Summary{
		Records: loaded.Records, GroundTruthTemplates: len(loaded.GroundTruth),
		IndexedDocuments: len(docs), Drain3Clusters: clusters,
		Drain3CompressionRate: 1 - float64(clusters)/float64(loaded.Records),
		Drain3ClusterPurity:   purity, Queries: len(queries),
		WSRecords: loaded.Records, WSBatches: parserStats.Batches, WSAttempts: parserStats.Attempts,
		WSRetries: parserStats.Retries, WSReconnects: parserStats.Reconnects,
		WSDurationMS: durationMS(loaded.WSDuration),
		WSP50MS:      pct(batchLatencies, .50), WSP95MS: pct(batchLatencies, .95),
		WSP99MS: pct(batchLatencies, .99), WSThroughputRPS: wsThroughput,
		LokiPushDurationMS:          durationMS(loaded.LokiDuration),
		EmbeddingCalls:              calls + queryCalls,
		DocumentEmbeddingDurationMS: durationMS(documentEmbeddingDuration),
		QueryEmbeddingDurationMS:    durationMS(queryEmbeddingDuration),
		MilvusUpsertDurationMS:      durationMS(milvusUpsertDuration),
		RecordTypeDistribution:      loaded.RecordTypes,
		NamespaceDistribution:       loaded.Namespaces,
		ServiceDistribution:         loaded.Services,
		Metrics:                     summarize(results),
	}
	b, _ := json.MarshalIndent(summary, "", "  ")
	err = os.WriteFile(cfg.OutputDir+"/summary.json", b, 0o640)
	return summary, err
}

func load(ctx context.Context, cfg Config) (loadResult, error) {
	f, err := os.Open(cfg.Corpus)
	if err != nil {
		return loadResult{}, err
	}
	defer f.Close()
	out := loadResult{
		Documents: map[string]datasets.LogRecord{}, ClusterTruth: map[int]map[string]int{},
		GroundTruth: map[string]bool{}, RecordTypes: map[string]int{},
		Namespaces: map[string]int{}, Services: map[string]int{},
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var parserBatch []retrieval.LogRecord
	var sourceBatch []datasets.LogRecord
	var streams []map[string]any
	flush := func() error {
		if len(parserBatch) > 0 {
			started := time.Now()
			parsed, parseErr := cfg.Parser.ParseBatch(ctx, parserBatch)
			elapsed := time.Since(started)
			if parseErr != nil {
				return parseErr
			}
			out.WSDuration += elapsed
			out.WSBatchLatencies = append(out.WSBatchLatencies, durationMS(elapsed))
			byRecord := make(map[string]retrieval.TemplateResult, len(parsed))
			for _, result := range parsed {
				byRecord[result.RecordID] = result
			}
			for i, source := range sourceBatch {
				result, ok := byRecord[parserBatch[i].RecordID]
				if !ok {
					return fmt.Errorf("drain3 response missing record %s", parserBatch[i].RecordID)
				}
				if out.ClusterTruth[result.ClusterID] == nil {
					out.ClusterTruth[result.ClusterID] = map[string]int{}
				}
				out.ClusterTruth[result.ClusterID][source.TemplateID]++
				key := source.TemplateID + "/" + source.Namespace + "/" + source.Service
				if _, exists := out.Documents[key]; !exists {
					source.Message = result.Template
					out.Documents[key] = source
				}
			}
			parserBatch = nil
			sourceBatch = nil
		}
		if len(streams) > 0 {
			started := time.Now()
			if pushErr := cfg.Loki.Push(ctx, streams); pushErr != nil {
				return pushErr
			}
			out.LokiDuration += time.Since(started)
			streams = nil
		}
		return nil
	}
	for scanner.Scan() {
		var record datasets.LogRecord
		if err = json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return out, err
		}
		out.Records++
		out.GroundTruth[record.TemplateID] = true
		out.RecordTypes[record.RecordType]++
		out.Namespaces[record.Namespace]++
		out.Services[record.Service]++
		recordID := fmt.Sprintf("%s-log-%d", cfg.DatasetRun, out.Records)
		parserBatch = append(parserBatch, retrieval.LogRecord{
			RecordID: recordID, Timestamp: record.Timestamp, Service: record.Service,
			Namespace: record.Namespace, Pod: record.Pod, Level: record.Level,
			TraceID: record.TraceID, Message: record.Message,
		})
		sourceBatch = append(sourceBatch, record)
		pushTime := time.Now().UTC().Add(-time.Duration(cfg.Count-out.Records) * time.Millisecond)
		if out.LokiStart.IsZero() || pushTime.Before(out.LokiStart) {
			out.LokiStart = pushTime
		}
		if out.LokiEnd.IsZero() || pushTime.After(out.LokiEnd) {
			out.LokiEnd = pushTime
		}
		streams = append(streams, map[string]any{
			"stream": map[string]string{
				"namespace": record.Namespace, "service": record.Service, "level": record.Level,
				"template_id": record.TemplateID, "record_type": record.RecordType,
				"benchmark_dataset": "retrieval", "benchmark_run": cfg.DatasetRun,
			},
			"values": [][]string{{strconv.FormatInt(pushTime.UnixNano(), 10), record.Message}},
		})
		if len(parserBatch) >= 500 {
			if err = flush(); err != nil {
				return out, err
			}
			if out.Records%50_000 == 0 || out.Records == cfg.Count {
				progress(cfg, "ingest", out.Records, cfg.Count)
			}
		}
	}
	if err = scanner.Err(); err != nil {
		return out, err
	}
	err = flush()
	return out, err
}

func progress(cfg Config, stage string, current, total int) {
	if cfg.Progress != nil {
		cfg.Progress(stage, current, total)
	}
}

func lokiQueryWindow(first, last time.Time) (time.Time, time.Time) {
	return first.Add(-time.Minute), last.Add(time.Minute)
}

func generateQueries() []Query {
	services := []string{"gateway-service", "order-service", "payment-service"}
	namespaces := []string{"kubepilot-benchmark", "kubepilot-demo", "observability"}
	phrases := []string{
		"Investigate %s in %s: %s.",
		"Find a similar incident for %s in %s where %s.",
		"Which historical failure explains why %s in %s had this symptom: %s?",
		"Retrieve prior evidence for %s in %s after operators observed that %s.",
		"Search incident history for %s in %s; the observable behavior was that %s.",
	}
	definitions := datasets.FaultTemplates()
	out := make([]Query, 0, len(definitions)*len(phrases))
	for definitionIndex, definition := range definitions {
		for variant, phrase := range phrases {
			service := services[(definitionIndex+variant)%len(services)]
			namespace := namespaces[(definitionIndex*2+variant)%len(namespaces)]
			out = append(out, Query{
				ID:       fmt.Sprintf("query-%03d", len(out)+1),
				Text:     fmt.Sprintf(phrase, service, namespace, definition.Symptom),
				Category: definition.Category, Service: service, Namespace: namespace,
				ExpectedTemplate: definition.ID,
			})
		}
	}
	return out
}

func embedBatches(ctx context.Context, embedder retrieval.EmbeddingClient, texts []string, size int) ([][]float32, int, error) {
	var out [][]float32
	calls := 0
	for i := 0; i < len(texts); i += size {
		end := min(i+size, len(texts))
		vectors, err := embedder.Embed(ctx, texts[i:end])
		if err != nil {
			return nil, calls, err
		}
		out = append(out, vectors...)
		calls++
	}
	return out, calls, nil
}

func upsertBatches(ctx context.Context, store *retrieval.MilvusStore, docs []retrieval.Document, size int) error {
	for start := 0; start < len(docs); start += size {
		end := min(start+size, len(docs))
		if err := store.Upsert(ctx, docs[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func lexicalRank(query string, entries []tools.LokiEntry) ([]string, int) {
	queryTokens := tokens(query)
	scores := map[string]float64{}
	for _, entry := range entries {
		templateID := entry.Labels["template_id"]
		if templateID == "" {
			continue
		}
		lineTokens := tokens(entry.Line)
		overlap := 0.0
		for token := range queryTokens {
			if lineTokens[token] {
				overlap++
			}
		}
		score := overlap / math.Sqrt(float64(max(1, len(queryTokens))*max(1, len(lineTokens))))
		if current, exists := scores[templateID]; !exists || score > current {
			scores[templateID] = score
		}
	}
	type scored struct {
		id    string
		score float64
	}
	items := make([]scored, 0, len(scores))
	for id, score := range scores {
		items = append(items, scored{id, score})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score == items[j].score {
			return items[i].id < items[j].id
		}
		return items[i].score > items[j].score
	})
	ranked := make([]string, len(items))
	for i := range items {
		ranked[i] = items[i].id
	}
	return ranked, len(items)
}

func tokens(value string) map[string]bool {
	stop := map[string]bool{"the": true, "a": true, "an": true, "in": true, "for": true, "to": true, "of": true, "and": true, "was": true, "that": true, "this": true, "where": true}
	out := map[string]bool{}
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(token) >= 3 && !stop[token] {
			out[token] = true
		}
	}
	return out
}

func rank(expected string, ids []string) int {
	for i, id := range ids {
		if id == expected {
			return i + 1
		}
	}
	return 0
}

func rankDocs(query Query, docs []retrieval.Document) int {
	for i, document := range docs {
		if document.Template == query.ExpectedTemplate && document.Service == query.Service && document.Namespace == query.Namespace {
			return i + 1
		}
	}
	return 0
}

func documentCount(docs []retrieval.Document, namespace, service string) int {
	count := 0
	for _, document := range docs {
		if document.Namespace == namespace && document.Service == service {
			count++
		}
	}
	return count
}

func clusterQuality(clusters map[int]map[string]int) (int, float64) {
	total, dominant := 0, 0
	for _, truthCounts := range clusters {
		maxCount := 0
		for _, count := range truthCounts {
			total += count
			if count > maxCount {
				maxCount = count
			}
		}
		dominant += maxCount
	}
	if total == 0 {
		return len(clusters), 0
	}
	return len(clusters), float64(dominant) / float64(total)
}

func summarize(results []Result) []Metrics {
	byStrategy := map[string][]Result{}
	for _, result := range results {
		byStrategy[result.Strategy] = append(byStrategy[result.Strategy], result)
	}
	var out []Metrics
	for name, items := range byStrategy {
		metric := Metrics{Strategy: name}
		var latencies, backendLatencies []float64
		var candidates, corpora float64
		for _, result := range items {
			if result.Rank == 1 {
				metric.Recall1++
			}
			if result.Rank > 0 && result.Rank <= 5 {
				metric.Recall5++
			}
			if result.Rank > 0 && result.Rank <= 10 {
				metric.Recall10++
				metric.MRR += 1 / float64(result.Rank)
			}
			latencies = append(latencies, durationMS(result.Latency))
			backendLatencies = append(backendLatencies, durationMS(result.BackendLatency))
			candidates += float64(result.CandidateCount)
			corpora += float64(result.CorpusCount)
		}
		n := float64(len(items))
		metric.Recall1 /= n
		metric.Recall5 /= n
		metric.Recall10 /= n
		metric.MRR /= n
		metric.AverageCandidates = candidates / n
		if corpora > 0 {
			metric.CandidateReduction = 1 - candidates/corpora
		}
		sort.Float64s(latencies)
		sort.Float64s(backendLatencies)
		metric.P50MS, metric.P95MS, metric.P99MS = pct(latencies, .5), pct(latencies, .95), pct(latencies, .99)
		metric.BackendP50MS, metric.BackendP95MS, metric.BackendP99MS = pct(backendLatencies, .5), pct(backendLatencies, .95), pct(backendLatencies, .99)
		out = append(out, metric)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Strategy < out[j].Strategy })
	return out
}

func pct(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	return values[min(int(float64(len(values))*percentile), len(values)-1)]
}

func durationMS(value time.Duration) float64 { return float64(value.Microseconds()) / 1000 }

func writeResults(dir string, results []Result) error {
	f, err := os.Create(dir + "/retrieval-results.csv")
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"query_id", "strategy", "rank", "relevant", "latency_ms", "backend_latency_ms", "candidate_count", "corpus_count", "candidate_reduction"})
	for _, result := range results {
		reduction := 0.0
		if result.CorpusCount > 0 {
			reduction = 1 - float64(result.CandidateCount)/float64(result.CorpusCount)
		}
		_ = w.Write([]string{
			result.QueryID, result.Strategy, strconv.Itoa(result.Rank), strconv.FormatBool(result.Rank > 0),
			fmt.Sprintf("%.3f", durationMS(result.Latency)), fmt.Sprintf("%.3f", durationMS(result.BackendLatency)),
			strconv.Itoa(result.CandidateCount), strconv.Itoa(result.CorpusCount), fmt.Sprintf("%.6f", reduction),
		})
	}
	return w.Error()
}

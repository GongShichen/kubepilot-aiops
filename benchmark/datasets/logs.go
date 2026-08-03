package datasets

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"time"
)

type LogRecord struct {
	Timestamp  time.Time `json:"timestamp"`
	Level      string    `json:"level"`
	Service    string    `json:"service"`
	Namespace  string    `json:"namespace"`
	Pod        string    `json:"pod"`
	TraceID    string    `json:"trace_id"`
	RequestID  string    `json:"request_id"`
	OrderID    string    `json:"order_id"`
	ClientIP   string    `json:"client_ip"`
	Message    string    `json:"message"`
	TemplateID string    `json:"template_id"`
	Category   string    `json:"category,omitempty"`
	RecordType string    `json:"record_type"`
}

type FaultTemplate struct {
	ID       string
	Category string
	LogText  string
	Symptom  string
}

type cause struct{ log, symptom string }
type situation struct{ log, symptom string }

// FaultTemplates produces exactly 20 semantically distinct templates for each
// benchmark category. LogText represents what the system emits; Symptom is a
// separately worded observation used to build retrieval queries, so the query
// never contains a ground-truth template ID or a copied log line.
func FaultTemplates() []FaultTemplate {
	situations5 := []situation{
		{"checkout requests", "while checkout traffic was steady"},
		{"order reconciliation", "during order reconciliation"},
		{"payment authorization", "when payments were authorized"},
		{"background workers", "inside background processing"},
		{"post rollout traffic", "immediately after a rollout"},
	}
	situations4 := situations5[:4]
	categories := []struct {
		name       string
		causes     []cause
		situations []situation
	}{
		{"cpu", []cause{
			{"worker goroutine entered an unbounded compute loop", "one pod stayed processor-saturated and latency climbed"},
			{"container quota repeatedly throttled request workers", "requests slowed while containers spent time CPU-throttled"},
			{"incoming traffic exceeded available processing capacity", "request volume overwhelmed the available compute capacity"},
			{"worker fanout created excessive concurrent tasks", "runaway concurrency consumed all available processor time"},
		}, situations5},
		{"memory", []cause{
			{"retained objects caused continuous heap growth", "working-set memory rose continuously without returning"},
			{"burst allocation exceeded the container memory ceiling", "a short allocation spike ended with an OOM restart"},
			{"application cache grew without a capacity bound", "cache growth steadily displaced available container memory"},
			{"memory limit was configured below steady-state demand", "healthy traffic exceeded an unusually small memory limit"},
		}, situations5},
		{"database", []cause{
			{"database connection pool had no free sessions", "requests waited because every database connection was occupied"},
			{"mysql endpoint became unavailable to application clients", "application calls could no longer reach the SQL backend"},
			{"database authentication rejected configured credentials", "new SQL sessions failed authentication after configuration changed"},
			{"transaction lock wait exceeded the query deadline", "blocked transactions caused database spans to time out"},
		}, situations5},
		{"network", []cause{
			{"egress policy denied the downstream connection", "a policy change blocked calls leaving the service pod"},
			{"service selector resolved to zero ready endpoints", "the service name had no backing endpoints"},
			{"service target port routed traffic to a closed socket", "connections reached the service but used the wrong destination port"},
			{"downstream response exceeded the client timeout budget", "upstream traces ended while waiting on a slow dependency"},
		}, situations5},
		{"deployment", []cause{
			{"container image could not be pulled from the registry", "new pods remained pending with an image pull failure"},
			{"new application revision returned errors after startup", "the latest revision became ready but served failing responses"},
			{"readiness probe failed for every replacement pod", "rollout replicas never entered the ready state"},
			{"configuration value prevented the process from starting", "pods crashed after receiving a bad environment configuration"},
			{"revision regression required rollback to the previous release", "service health degraded only on the newest deployment revision"},
		}, situations4},
	}
	var out []FaultTemplate
	for _, category := range categories {
		index := 0
		for _, item := range category.causes {
			for _, context := range category.situations {
				out = append(out, FaultTemplate{
					ID:       fmt.Sprintf("%s-%02d", category.name, index),
					Category: category.name,
					LogText:  item.log + " during " + context.log,
					Symptom:  item.symptom + " " + context.symptom,
				})
				index++
			}
		}
	}
	// Interleave categories so every prefix of the deterministic target stream
	// remains balanced; grouped construction above is kept for readability.
	grouped := out
	out = make([]FaultTemplate, 0, len(grouped))
	for templateIndex := 0; templateIndex < 20; templateIndex++ {
		for categoryIndex := 0; categoryIndex < 5; categoryIndex++ {
			out = append(out, grouped[categoryIndex*20+templateIndex])
		}
	}
	return out
}

var noiseMessages = []string{
	"request completed successfully", "health probe returned ready", "cache lookup completed", "trace export batch accepted", "connection returned to pool",
	"scheduled worker completed", "configuration refresh succeeded", "service endpoint remained healthy", "retry budget was not consumed", "response serialized successfully",
	"client disconnected after response", "background checkpoint persisted", "metrics scrape completed", "leadership lease renewed", "queue depth remained stable",
	"request validation succeeded", "authorization policy allowed request", "session state loaded", "order state transitioned", "payment status recorded",
	"temporary client cancellation did not affect service", "optional metadata field was absent", "stale cache entry refreshed", "duplicate request safely deduplicated", "rate limiter retained spare capacity",
	"graceful shutdown hook registered", "dependency health check passed", "connection handshake completed", "worker queue drained", "pod readiness gate satisfied",
	"informational timeout setting loaded", "error counter remained unchanged", "failed-attempt metric reset after success", "exception sampling rule loaded", "kill signal handler initialized",
	"request context deadline configured", "fallback route was not required", "circuit breaker remained closed", "replica observed current revision", "endpoint slice synchronized",
	// The final ten are hard negatives: they contain error-like language but do
	// not correspond to one of the 100 target incident templates.
	"client supplied malformed optional header", "expired user session was rejected", "unknown route returned not found", "duplicate webhook signature was ignored", "cancelled request released all resources",
	"synthetic probe intentionally returned an error", "debug timeout simulation completed without impact", "invalid test order was quarantined", "stale trace span was discarded", "noise generator emitted a controlled warning",
}

func GenerateLogs(path string, count int, seed uint64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	defer w.Flush()
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	services := []string{"gateway-service", "order-service", "payment-service"}
	namespaces := []string{"kubepilot-benchmark", "kubepilot-demo", "observability"}
	faults := FaultTemplates()
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	targetIndex := 0
	for i := 0; i < count; i++ {
		service := services[rng.IntN(len(services))]
		namespace := weightedNamespace(rng, namespaces)
		level, category, recordType := "INFO", "", "normal"
		noiseIndex := rng.IntN(40)
		template := fmt.Sprintf("noise-%02d", noiseIndex)
		message := noiseMessages[noiseIndex]
		roll := rng.Float64()
		if roll >= 0.80 && roll < 0.96 {
			// Cycle through all template/service/namespace combinations. This
			// preserves a balanced fixed-seed corpus and guarantees that every
			// one of the 500 queries has a concrete ground-truth document.
			combination := targetIndex % (len(services) * len(namespaces))
			definition := faults[(targetIndex/(len(services)*len(namespaces)))%len(faults)]
			service = services[combination%len(services)]
			namespace = namespaces[(combination/len(services))%len(namespaces)]
			targetIndex++
			level, category, recordType = "ERROR", definition.Category, "target_fault"
			template, message = definition.ID, definition.LogText
		} else if roll >= 0.96 {
			noiseIndex = 40 + rng.IntN(10)
			level, recordType = "ERROR", "interference"
			template, message = fmt.Sprintf("noise-%02d", noiseIndex), noiseMessages[noiseIndex]
		}
		requestID := fmt.Sprintf("req-%016x", rng.Uint64())
		orderID := fmt.Sprintf("ord-%010d", rng.IntN(1_000_000_0000))
		clientIP := fmt.Sprintf("10.%d.%d.%d", rng.IntN(256), rng.IntN(256), 1+rng.IntN(254))
		pod := fmt.Sprintf("%s-%02d", service, rng.IntN(20))
		message = fmt.Sprintf("%s request_id=%s order_id=%s client_ip=%s pod=%s", message, requestID, orderID, clientIP, pod)
		record := LogRecord{
			Timestamp: base.Add(time.Duration(i) * time.Millisecond), Level: level,
			Service: service, Namespace: namespace, Pod: pod,
			TraceID: fmt.Sprintf("%032x", rng.Uint64()), RequestID: requestID,
			OrderID: orderID, ClientIP: clientIP, Message: message,
			TemplateID: template, Category: category, RecordType: recordType,
		}
		b, _ := json.Marshal(record)
		if _, err = w.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func weightedNamespace(rng *rand.Rand, namespaces []string) string {
	roll := rng.Float64()
	if roll < 0.70 {
		return namespaces[0]
	}
	if roll < 0.90 {
		return namespaces[1]
	}
	return namespaces[2]
}

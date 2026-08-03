package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	requests  = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "http_requests_total", Help: "HTTP requests"}, []string{"service", "method", "path", "status"})
	durations = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "http_request_duration_seconds", Help: "HTTP request duration", Buckets: prometheus.DefBuckets}, []string{"service", "method", "path"})
	faults    = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kubepilot_faults_total", Help: "Injected faults"}, []string{"service", "mode"})
	leakMu    sync.Mutex
	leak      [][]byte
)

func init() { prometheus.MustRegister(requests, durations, faults) }

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	service := env("SERVICE_NAME", "gateway-service")
	controller := newFaultController(env("FAULT_MODE", ""), os.Getenv("BENCHMARK_CONTROL_TOKEN"))
	shutdownTrace := initTrace(ctx, service)
	defer shutdownTrace(context.Background())
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ready")) })
	mux.HandleFunc("/benchmark/v1/fault", controller.handle)
	mux.HandleFunc("/", handler(service, controller))
	server := &http.Server{Addr: ":8080", Handler: instrument(service, mux), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		slog.Info("demo service listening", "service", service)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server", "error", err)
			cancel()
		}
	}()
	<-ctx.Done()
	stop, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	_ = server.Shutdown(stop)
}
func handler(service string, controller *faultController) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, span := otel.Tracer("kubepilot-demo").Start(r.Context(), service+" "+r.URL.Path)
		defer span.End()
		mode := controller.modeValue()
		if mode != "" {
			faults.WithLabelValues(service, mode).Inc()
			if applyFault(mode) {
				symptom := faultSymptom(mode)
				logOperationError(service, r.URL.Path, symptom, spanID(ctx))
				write(w, 500, map[string]any{"service": service, "error": symptom})
				return
			}
		}
		switch service {
		case "gateway-service":
			status, body := call(ctx, env("ORDER_URL", "http://order-service:8080/orders"))
			if status >= 500 {
				logOperationError(service, r.URL.Path, "downstream order request failed: "+body, spanID(ctx))
			}
			write(w, status, map[string]any{"service": service, "downstream": body})
		case "order-service":
			if err := redisPing(ctx, env("REDIS_ADDR", "redis:6379")); err != nil {
				write(w, 500, map[string]any{"error": "redis: " + err.Error()})
				return
			}
			status, body := call(ctx, env("PAYMENT_URL", "http://payment-service:8080/pay"))
			if status >= 500 {
				logOperationError(service, r.URL.Path, "downstream payment request failed: "+body, spanID(ctx))
			}
			write(w, status, map[string]any{"service": service, "payment": body})
		case "payment-service":
			if err := mysqlPing(ctx); err != nil {
				logOperationError(service, r.URL.Path, "database connection failed: "+err.Error(), spanID(ctx))
				write(w, 500, map[string]any{"error": "database: " + err.Error()})
				return
			}
			write(w, 200, map[string]any{"service": service, "paid": true})
		default:
			write(w, 200, map[string]any{"service": service, "ok": true})
		}
	}
}

func faultSymptom(mode string) string {
	switch mode {
	case "traffic_overload":
		return "request rejected because execution capacity is unavailable"
	case "faulty_v2", "revision_regression":
		return "request handler returned an internal error"
	case "invalid_config":
		return "request handler is not initialized"
	case "pool_exhausted":
		return "dependency resource acquisition deadline exceeded"
	case "lock_wait":
		return "dependency transaction did not complete before deadline"
	case "downstream_timeout":
		return "remote call exceeded request deadline"
	default:
		return "application operation failed"
	}
}

func logOperationError(service, path, message, traceID string) {
	logJSON(map[string]any{"time": time.Now().UTC(), "level": "ERROR", "service": service, "path": path, "error": message, "trace_id": traceID})
}

type faultController struct {
	mu    sync.RWMutex
	mode  string
	token string
	stop  context.CancelFunc
}

func newFaultController(mode, token string) *faultController {
	c := &faultController{token: token}
	c.setMode(mode)
	return c
}

func (c *faultController) modeValue() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mode
}

func (c *faultController) handle(w http.ResponseWriter, r *http.Request) {
	provided := r.Header.Get("X-KubePilot-Benchmark-Token")
	if c.token == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(c.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodPost:
		var body struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || !validFaultMode(body.Mode) {
			http.Error(w, "invalid fault mode", http.StatusBadRequest)
			return
		}
		c.setMode(body.Mode)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		c.setMode("")
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (c *faultController) setMode(mode string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop != nil {
		c.stop()
		c.stop = nil
	}
	c.mode = mode
	if mode == "" {
		leakMu.Lock()
		leak = nil
		leakMu.Unlock()
		return
	}
	c.stop = startFaultWorkers(mode)
}

func startFaultWorkers(mode string) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	workers := 0
	switch mode {
	case "busy_loop":
		workers = 4
	case "worker_fanout":
		workers = 32
	}
	for range workers {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
					for i := 0; i < 100000; i++ {
					}
				}
			}
		}()
	}
	switch mode {
	case "memory_leak", "unbounded_cache":
		allocation := 2 << 20
		if mode == "unbounded_cache" {
			allocation = 4 << 20
		}
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					leakMu.Lock()
					leak = append(leak, make([]byte, allocation))
					leakMu.Unlock()
				}
			}
		}()
	case "memory_burst":
		leakMu.Lock()
		leak = append(leak, make([]byte, 160<<20))
		leakMu.Unlock()
	}
	return cancel
}

func validFaultMode(mode string) bool {
	switch mode {
	case "busy_loop", "cpu_limit_low", "traffic_overload", "worker_fanout",
		"memory_leak", "memory_burst", "unbounded_cache", "memory_limit_low",
		"pool_exhausted", "mysql_unavailable", "invalid_credentials", "lock_wait",
		"network_policy_deny", "selector_mismatch", "wrong_port", "downstream_timeout",
		"bad_image", "faulty_v2", "probe_failure", "invalid_config", "revision_regression":
		return true
	default:
		return false
	}
}
func applyFault(mode string) bool {
	switch mode {
	case "busy_loop", "worker_fanout", "memory_leak", "unbounded_cache", "memory_burst":
		return false
	case "traffic_overload", "faulty_v2", "invalid_config", "revision_regression", "pool_exhausted":
		return true
	case "lock_wait", "downstream_timeout":
		time.Sleep(4 * time.Second)
		return true
	}
	return false
}
func instrument(service string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/benchmark/") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		requests.WithLabelValues(service, r.Method, r.URL.Path, strconv.Itoa(rw.status)).Inc()
		durations.WithLabelValues(service, r.Method, r.URL.Path).Observe(time.Since(start).Seconds())
		logJSON(map[string]any{"time": time.Now().UTC(), "level": level(rw.status), "service": service, "path": r.URL.Path, "status": rw.status, "trace_id": spanID(r.Context())})
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(s int) { w.status = s; w.ResponseWriter.WriteHeader(s) }
func call(ctx context.Context, url string) (int, string) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 502, err.Error()
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Status
}
func mysqlPing(ctx context.Context) error {
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s", env("DB_USER", "kubepilot"), env("DB_PASSWORD", "kubepilot"), env("DB_ADDR", "mysql:3306"), env("DB_NAME", "kubepilot"))
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetConnMaxLifetime(time.Minute)
	ping, done := context.WithTimeout(ctx, 2*time.Second)
	defer done()
	return db.PingContext(ping)
}
func redisPing(ctx context.Context, addr string) error {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, err = conn.Write([]byte("*1\r\n$4\r\nPING\r\n"))
	if err != nil {
		return err
	}
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err != nil {
		return err
	}
	if !strings.Contains(string(buf[:n]), "PONG") {
		return fmt.Errorf("unexpected redis response")
	}
	return nil
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func level(status int) string {
	if status >= 500 {
		return "ERROR"
	}
	return "INFO"
}
func logJSON(v any)                     { b, _ := json.Marshal(v); fmt.Println(string(b)) }
func spanID(ctx context.Context) string { return trace.SpanContextFromContext(ctx).TraceID().String() }
func initTrace(ctx context.Context, service string) func(context.Context) error {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return func(context.Context) error { return nil }
	}
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		slog.Warn("trace exporter disabled", "error", err)
		return func(context.Context) error { return nil }
	}
	res, _ := resource.Merge(resource.Default(), resource.NewSchemaless(semconv.ServiceName(service)))
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp.Shutdown
}

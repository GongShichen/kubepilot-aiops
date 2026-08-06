package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

func TestMetricCollectorUsesInstantAndIncidentRangeQueries(t *testing.T) {
	var instant, ranges int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/query":
			instant++
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{}}})
		case "/api/v1/query_range":
			ranges++
			if r.URL.Query().Get("step") != "15" || r.URL.Query().Get("start") == "" || r.URL.Query().Get("end") == "" {
				t.Errorf("invalid range query: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	incident := &domain.Incident{Namespace: "kubepilot-demo", Service: "payment-service", EvidenceStartAt: time.Now().Add(-2 * time.Minute)}
	evidence, err := (MetricCollector{Client: captools.NewPrometheus(server.URL)}).Collect(context.Background(), incident)
	if err != nil {
		t.Fatal(err)
	}
	wantInstant := len(captools.MetricQueries(incident.Namespace, incident.Service))
	if instant != wantInstant || ranges != 1 || len(evidence) != wantInstant+1 || evidence[len(evidence)-1].Kind != "memory_trend" {
		t.Fatalf("unexpected metric collection: instant=%d ranges=%d evidence=%d", instant, ranges, len(evidence))
	}
}

type collectorIndex struct {
	documents []retrieval.Document
	freshness time.Duration
	err       error
}

func (i collectorIndex) Search(context.Context, string, string, string) ([]retrieval.Document, time.Duration, error) {
	return i.documents, i.freshness, i.err
}

func TestLogCollectorCombinesAuthoritativeLogsAndFilteredTemplates(t *testing.T) {
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" || !strings.Contains(r.URL.Query().Get("query"), "payment-service") {
			t.Errorf("unexpected Loki request: %s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{"result": []any{map[string]any{
				"stream": map[string]string{"pod": "payment-1", "level": "error", "trace_id": "trace-1"},
				"values": [][]string{{strconv.FormatInt(now.UnixNano(), 10), "mysql connection refused"}},
			}}},
		})
	}))
	defer server.Close()

	index := collectorIndex{freshness: 45 * time.Second, documents: []retrieval.Document{
		{ID: "failure-template", Template: "timeout while acquiring mysql connection", Level: "error", RootCause: "connection saturation", OccurrenceCount: 8, Score: .91},
		{ID: "normal-template", Template: `{"level":"info","message":"request complete"}`, Level: "info"},
	}}
	incident := &domain.Incident{Namespace: "kubepilot-demo", Service: "payment-service", Resource: "payment-service", Summary: "latency", CreatedAt: now.Add(-time.Minute)}
	evidence, err := (LogCollector{Loki: captools.NewLoki(server.URL), Indexed: index}).Collect(context.Background(), incident)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 2 || evidence[0].TraceID != "trace-1" || evidence[1].TemplateID != "failure-template" || evidence[1].Confidence != .5 {
		t.Fatalf("unexpected log evidence: %+v", evidence)
	}
}

func TestTraceCollectorMapsCriticalPathEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/traces" || r.URL.Query().Get("service") != "gateway-service" {
			t.Errorf("unexpected Jaeger request: %s", r.URL.String())
		}
		var tags map[string]string
		if err := json.Unmarshal([]byte(r.URL.Query().Get("tags")), &tags); err != nil || tags["k8s.namespace.name"] != "kubepilot-demo" {
			t.Errorf("trace query is not namespace scoped: tags=%v err=%v", tags, err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{
			"traceID":   "trace-slow",
			"processes": map[string]any{"p1": map[string]any{"serviceName": "gateway-service"}, "p2": map[string]any{"serviceName": "redis"}},
			"spans": []any{
				map[string]any{"operationName": "GET /orders", "processID": "p1", "duration": 1000, "tags": []any{}},
				map[string]any{"operationName": "GET redis", "processID": "p2", "duration": 8000, "tags": []any{map[string]any{"key": "error", "value": true}}},
			},
		}}})
	}))
	defer server.Close()

	evidence, err := (TraceCollector{Client: captools.NewJaeger(server.URL)}).Collect(context.Background(), &domain.Incident{Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service", CreatedAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].Data["trace_id"] != "trace-slow" || evidence[0].Data["slow_service"] != "redis" || evidence[0].Data["error_service"] != "redis" {
		t.Fatalf("unexpected trace evidence: %+v", evidence)
	}
}

func TestKubernetesCollectorBuildsSanitizedWorkloadAndDependencyEvidence(t *testing.T) {
	now := time.Now().UTC()
	replicas := int32(1)
	paymentDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "payment-service",
			Namespace:       "kubepilot-demo",
			UID:             "payment-uid",
			ResourceVersion: "12",
			Annotations:     map[string]string{"deployment.kubernetes.io/revision": "3"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "payment-service"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "payment-service"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  "payment",
					Image: "payment:test",
					Env: []corev1.EnvVar{
						{Name: "MYSQL_PASSWORD", Value: "must-not-leak"},
						{Name: "LOG_LEVEL", Value: "info"},
					},
				}}},
			},
		},
		Status: appsv1.DeploymentStatus{
			AvailableReplicas: 1,
			ReadyReplicas:     1,
			UpdatedReplicas:   1,
			Conditions: []appsv1.DeploymentCondition{{
				Type:           appsv1.DeploymentAvailable,
				LastUpdateTime: metav1.NewTime(now),
			}},
		},
	}
	mysqlDeployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "mysql", Namespace: "kubepilot-demo"}, Spec: appsv1.DeploymentSpec{Replicas: &replicas}, Status: appsv1.DeploymentStatus{AvailableReplicas: 1, ReadyReplicas: 1}}
	paymentPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "payment-1", Namespace: "kubepilot-demo", Labels: map[string]string{"app": "payment-service"}, UID: "pod-uid", ResourceVersion: "5"}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2", ContainerStatuses: []corev1.ContainerStatus{{Name: "payment", Ready: true, RestartCount: 1}}}}
	mysqlPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "mysql-1", Namespace: "kubepilot-demo", Labels: map[string]string{"app": "mysql"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	recentEvent := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "recent", Namespace: "kubepilot-demo", CreationTimestamp: metav1.NewTime(now)}, Type: "Warning", Reason: "BackOff", Message: "container restarting", InvolvedObject: corev1.ObjectReference{Name: "payment-1"}}
	paymentService := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "payment-service", Namespace: "kubepilot-demo"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "payment-service"}, Ports: []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080)}}}}
	paymentEndpoints := &corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Name: "payment-service", Namespace: "kubepilot-demo"}, Subsets: []corev1.EndpointSubset{{Addresses: []corev1.EndpointAddress{{IP: "10.0.0.2"}}}}}
	mysqlEndpoints := &corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Name: "mysql", Namespace: "kubepilot-demo"}, Subsets: []corev1.EndpointSubset{{Addresses: []corev1.EndpointAddress{{IP: "10.0.0.3"}}}}}
	paymentConfig := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "payment-config", Namespace: "kubepilot-demo"}}
	paymentPolicy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "allow-payment", Namespace: "kubepilot-demo"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "payment-service"}}}}
	clientset := fake.NewSimpleClientset(
		paymentDeployment, mysqlDeployment, paymentPod, mysqlPod, recentEvent, paymentService, paymentEndpoints, mysqlEndpoints, paymentConfig, paymentPolicy,
	)
	collector := KubernetesEvidenceCollector{Client: captools.NewKubernetesWithClient(clientset, []string{"kubepilot-demo"})}
	evidence, err := collector.Collect(context.Background(), &domain.Incident{Namespace: "kubepilot-demo", Service: "payment-service", Resource: "payment-service", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 {
		t.Fatalf("expected one workload evidence, got %d", len(evidence))
	}
	payload, _ := json.Marshal(evidence[0].Data)
	text := string(payload)
	for _, expected := range []string{"mysql_dependency", "allow-payment", "payment-config", "[REDACTED]"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in workload evidence: %s", expected, text)
		}
	}
	if strings.Contains(text, "must-not-leak") {
		t.Fatalf("sensitive environment value leaked: %s", text)
	}
}

func TestKubernetesCollectorRejectsDisallowedNamespace(t *testing.T) {
	collector := KubernetesEvidenceCollector{Client: captools.NewKubernetesWithClient(fake.NewSimpleClientset(), []string{"kubepilot-demo"})}
	if _, err := collector.Collect(context.Background(), &domain.Incident{Namespace: "production", Service: "payment-service"}); err == nil {
		t.Fatal("disallowed namespace was queried")
	}
}

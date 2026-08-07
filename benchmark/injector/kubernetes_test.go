package injector

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestKubernetesPreflightUsesExactNamespaceAllowlist(t *testing.T) {
	worker := "kubepilot-benchmark-worker-01"
	client := fake.NewSimpleClientset(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "payment-service", Namespace: worker}})
	injector := &Kubernetes{client: client, allowed: map[string]bool{worker: true}}
	allowed := scenarios.Scenario{Namespace: worker, Target: "payment-service"}
	if err := injector.Preflight(context.Background(), allowed); err != nil {
		t.Fatal(err)
	}
	for _, namespace := range []string{"kubepilot-benchmark", "kubepilot-benchmark-worker-02", "production"} {
		blocked := allowed
		blocked.Namespace = namespace
		if err := injector.Preflight(context.Background(), blocked); err == nil {
			t.Fatalf("namespace %q bypassed exact allowlist", namespace)
		}
	}
}

func TestLoadJobNameIsRFC1123(t *testing.T) {
	got := loadJobName(scenarios.Scenario{ID: "memory-memory_leak-01"})
	if got != "benchmark-load-memory-memory-leak-01" {
		t.Fatalf("unexpected name %q", got)
	}
	if len(got) > 63 {
		t.Fatalf("name exceeds 63 characters: %d", len(got))
	}
}

func TestRetryServiceProxyWaitsForTransientEndpoint(t *testing.T) {
	attempts := 0
	err := retryServiceProxy(context.Background(), func() error {
		attempts++
		if attempts < 3 {
			return fmt.Errorf("no endpoints available for service")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d", attempts)
	}
}

func TestRetryServiceProxyStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := retryServiceProxy(ctx, func() error { return fmt.Errorf("no endpoints available for service") })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
}

func TestHasReadyServiceEndpointRequiresNamedServicePort(t *testing.T) {
	service := &corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 8080}}}}
	withoutReady := &discoveryv1.EndpointSliceList{Items: []discoveryv1.EndpointSlice{{
		Ports:     []discoveryv1.EndpointPort{{Name: ptr("http"), Port: ptr(int32(8080))}},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr(false)}}},
	}}}
	if hasReadyServiceEndpoint(service, withoutReady) {
		t.Fatal("not-ready endpoint was accepted")
	}
	ready := withoutReady.DeepCopy()
	ready.Items[0].Endpoints[0].Conditions.Ready = ptr(true)
	if !hasReadyServiceEndpoint(service, ready) {
		t.Fatal("ready named endpoint was not accepted")
	}
}

func TestReplaceUnreadyPodsResetsCrashLoopBackoff(t *testing.T) {
	ready := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ready", Namespace: "kubepilot-benchmark", Labels: map[string]string{"app": "gateway-service"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	unready := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "crash-looping", Namespace: "kubepilot-benchmark", Labels: map[string]string{"app": "gateway-service"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}},
	}
	client := fake.NewSimpleClientset(ready, unready)
	injector := &Kubernetes{client: client}
	if err := injector.replaceUnreadyPods(context.Background(), "kubepilot-benchmark", "gateway-service"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("kubepilot-benchmark").Get(context.Background(), ready.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("ready pod was deleted: %v", err)
	}
	if _, err := client.CoreV1().Pods("kubepilot-benchmark").Get(context.Background(), unready.Name, metav1.GetOptions{}); err == nil {
		t.Fatal("unready pod was not replaced")
	}
}

func TestApplyLowResourceLimitKeepsRequestsValid(t *testing.T) {
	for _, tc := range []struct {
		name     string
		category string
		key      corev1.ResourceName
		want     string
	}{
		{name: "cpu", category: "cpu", key: corev1.ResourceCPU, want: "10m"},
		{name: "memory", category: "memory", key: corev1.ResourceMemory, want: "32Mi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			container := corev1.Container{Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{tc.key: resource.MustParse("1Gi")},
				Limits:   corev1.ResourceList{tc.key: resource.MustParse("2Gi")},
			}}
			applyLowResourceLimit(&container, tc.category)
			request := container.Resources.Requests[tc.key]
			limit := container.Resources.Limits[tc.key]
			if request.Cmp(limit) > 0 {
				t.Fatalf("request %s exceeds limit %s", request.String(), limit.String())
			}
			if limit.Cmp(resource.MustParse(tc.want)) != 0 {
				t.Fatalf("limit = %s, want %s", limit.String(), tc.want)
			}
		})
	}
}

func TestInjectRetriesDeploymentConflict(t *testing.T) {
	const namespace = "kubepilot-benchmark"
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway-service", Namespace: namespace},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "service", Image: "kubepilot/demo-service:0.1.0"}}}}},
	}
	client := fake.NewSimpleClientset(deployment)
	injector := &Kubernetes{
		client:      client,
		deployments: map[string]*appsv1.Deployment{},
		services:    map[string]*corev1.Service{},
		scales:      map[string]int32{},
		restarts:    map[string]int32{},
	}
	updates := 0
	client.PrependReactor("update", "deployments", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates == 1 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, deployment.Name, errors.New("concurrent update"))
		}
		return false, nil, nil
	})
	scenario := scenarios.Scenario{ID: "deployment-retry", Namespace: namespace, Target: deployment.Name, Injector: "deployment_patch", Variant: "revision_regression"}
	if err := injector.Inject(context.Background(), scenario); err != nil {
		t.Fatal(err)
	}
	if updates < 2 {
		t.Fatalf("deployment updates=%d, want conflict retry", updates)
	}
	current, err := client.AppsV1().Deployments(namespace).Get(context.Background(), deployment.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEnv(current.Spec.Template.Spec.Containers[0].Env, "FAULT_MODE", scenario.Variant) {
		t.Fatalf("fault mode was not applied after retry: %+v", current.Spec.Template.Spec.Containers[0].Env)
	}
}

func hasEnv(values []corev1.EnvVar, name, want string) bool {
	for _, value := range values {
		if value.Name == name && value.Value == want {
			return true
		}
	}
	return false
}

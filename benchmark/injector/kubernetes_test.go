package injector

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

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

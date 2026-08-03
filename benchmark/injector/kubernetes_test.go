package injector

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

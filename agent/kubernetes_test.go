package agent

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestSanitizeContainersRedactsSecretsWithoutEvaluationSpecialCases(t *testing.T) {
	containers := []corev1.Container{{
		Name: "payment",
		Env: []corev1.EnvVar{
			{Name: "FAULT_MODE", Value: "pool_exhausted"},
			{Name: "BENCHMARK_CONTROL_TOKEN", Value: "do-not-leak"},
			{Name: "DB_PASSWORD", Value: "do-not-leak"},
			{Name: "DB_ADDR", Value: "mysql:3306"},
		},
	}}
	got := sanitizeContainers(containers)
	environment := got[0]["env"].([]map[string]any)
	if len(environment) != 4 {
		t.Fatalf("environment=%#v", environment)
	}
	if environment[0]["name"] != "FAULT_MODE" || environment[0]["value"] != "pool_exhausted" {
		t.Fatalf("ordinary configuration was altered: %#v", environment[0])
	}
	for _, index := range []int{1, 2} {
		if environment[index]["value"] != "[REDACTED]" {
			t.Fatalf("secret was not redacted: %#v", environment[index])
		}
	}
	if environment[3]["name"] != "DB_ADDR" || environment[3]["value"] != "mysql:3306" {
		t.Fatalf("safe value missing: %#v", environment[3])
	}
}

func TestKubernetesStatusAndEndpointResolutionProjection(t *testing.T) {
	status := containerStatusFacts(corev1.ContainerStatus{
		Name: "checkout", Ready: true, RestartCount: 2,
		State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
	})
	last, ok := status["last_termination_state"].(map[string]any)
	if !ok || last["terminated"].(map[string]any)["reason"] != "OOMKilled" {
		t.Fatalf("diagnostic last termination state was dropped: %#v", status)
	}
	containers := []corev1.Container{{Name: "checkout", Env: []corev1.EnvVar{
		{Name: "CACHE_ADDR", Value: "missing-cache:6379"},
		{Name: "EXTERNAL_URL", Value: "https://example.com:443"},
		{Name: "DB_PASSWORD", Value: "cache:6379"},
	}}}
	resolved := unresolvedConfiguredEndpoints(containers, []corev1.Service{{}})
	if len(resolved) != 1 || resolved[0]["host"] != "missing-cache" || resolved[0]["status"] != "service_not_found" {
		t.Fatalf("cluster-local endpoint projection=%#v", resolved)
	}
}

package agent

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestSanitizeContainersRemovesBenchmarkControlsAndSecrets(t *testing.T) {
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
	if len(environment) != 2 {
		t.Fatalf("environment=%#v", environment)
	}
	if environment[0]["name"] != "DB_PASSWORD" || environment[0]["value"] != "[REDACTED]" {
		t.Fatalf("password was not redacted: %#v", environment[0])
	}
	if environment[1]["name"] != "DB_ADDR" || environment[1]["value"] != "mysql:3306" {
		t.Fatalf("safe value missing: %#v", environment[1])
	}
}

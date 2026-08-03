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

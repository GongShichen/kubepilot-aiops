package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/tools"
	"github.com/oklog/ulid/v2"
	corev1 "k8s.io/api/core/v1"
)

type KubernetesEvidenceAgent struct{ Client *tools.KubernetesClient }

func (a KubernetesEvidenceAgent) Collect(ctx context.Context, in *domain.Incident) ([]domain.Evidence, error) {
	pods, err := a.Client.Pods(ctx, in.Namespace, tools.SelectorForService(in.Service))
	if err != nil {
		return nil, err
	}
	deployment, depErr := a.Client.Deployment(ctx, in.Namespace, in.Resource)
	events, evErr := a.Client.Events(ctx, in.Namespace)
	service, serviceErr := a.Client.Service(ctx, in.Namespace, in.Service)
	endpoints, endpointsErr := a.Client.Endpoints(ctx, in.Namespace, in.Service)
	configMaps, configErr := a.Client.ConfigMaps(ctx, in.Namespace)
	networkPolicies, policiesErr := a.Client.NetworkPolicies(ctx, in.Namespace)
	data := map[string]any{}
	var podSummaries []map[string]any
	for _, pod := range pods.Items {
		containers := []map[string]any{}
		for _, status := range pod.Status.ContainerStatuses {
			containers = append(containers, map[string]any{"name": status.Name, "ready": status.Ready, "restart_count": status.RestartCount, "state": status.State})
		}
		podSummaries = append(podSummaries, map[string]any{"name": pod.Name, "uid": pod.UID, "resource_version": pod.ResourceVersion, "phase": pod.Status.Phase, "pod_ip": pod.Status.PodIP, "containers": containers})
	}
	data["pods"] = podSummaries
	if depErr == nil {
		conditions := make([]any, 0, len(deployment.Status.Conditions))
		cutoff := in.CreatedAt.Add(-45 * time.Second)
		for _, condition := range deployment.Status.Conditions {
			if condition.LastUpdateTime.Time.Before(cutoff) {
				continue
			}
			conditions = append(conditions, condition)
		}
		data["deployment"] = map[string]any{"name": deployment.Name, "uid": deployment.UID, "resource_version": deployment.ResourceVersion, "generation": deployment.Generation, "revision": deployment.Annotations["deployment.kubernetes.io/revision"], "replicas": deployment.Spec.Replicas, "available_replicas": deployment.Status.AvailableReplicas, "unavailable_replicas": deployment.Status.UnavailableReplicas, "recent_conditions": conditions, "containers": sanitizeContainers(deployment.Spec.Template.Spec.Containers)}
	}
	if evErr == nil {
		var summaries []map[string]any
		start := max(0, len(events.Items)-50)
		cutoff := in.CreatedAt.Add(-45 * time.Second)
		for _, event := range events.Items[start:] {
			observedAt := event.LastTimestamp.Time
			if observedAt.IsZero() {
				observedAt = event.CreationTimestamp.Time
			}
			if observedAt.Before(cutoff) {
				continue
			}
			summaries = append(summaries, map[string]any{"type": event.Type, "reason": event.Reason, "message": event.Message, "object": event.InvolvedObject.Name, "count": event.Count, "last_timestamp": event.LastTimestamp})
		}
		data["events"] = summaries
	}
	if serviceErr == nil {
		data["service"] = map[string]any{"selector": service.Spec.Selector, "ports": service.Spec.Ports, "cluster_ip": service.Spec.ClusterIP}
	}
	if endpointsErr == nil {
		data["endpoints"] = endpoints.Subsets
	}
	if configErr == nil {
		names := make([]string, 0, len(configMaps.Items))
		for _, cm := range configMaps.Items {
			names = append(names, cm.Name)
		}
		data["configmaps"] = names
	}
	if policiesErr == nil {
		policies := make([]map[string]any, 0, len(networkPolicies.Items))
		for _, policy := range networkPolicies.Items {
			policies = append(policies, map[string]any{"name": policy.Name, "pod_selector": policy.Spec.PodSelector, "policy_types": policy.Spec.PolicyTypes, "ingress": policy.Spec.Ingress, "egress": policy.Spec.Egress})
		}
		data["network_policies"] = policies
	}
	if in.Service == "payment-service" {
		data["mysql_dependency"] = dependencyState(ctx, a.Client, in.Namespace, "mysql")
	} else if in.Service == "order-service" {
		data["redis_dependency"] = dependencyState(ctx, a.Client, in.Namespace, "redis")
	}
	return []domain.Evidence{{ID: ulid.Make().String(), Source: "kubernetes", Kind: "workload_state", Summary: fmt.Sprintf("Kubernetes state for %s", in.Service), Data: data, ObservedAt: time.Now().UTC()}}, nil
}

func dependencyState(ctx context.Context, client *tools.KubernetesClient, namespace, name string) map[string]any {
	state := map[string]any{}
	if deployment, err := client.Deployment(ctx, namespace, name); err == nil {
		state["deployment"] = map[string]any{"replicas": deployment.Spec.Replicas, "ready_replicas": deployment.Status.ReadyReplicas, "available_replicas": deployment.Status.AvailableReplicas, "unavailable_replicas": deployment.Status.UnavailableReplicas}
	}
	if pods, err := client.Pods(ctx, namespace, tools.SelectorForService(name)); err == nil {
		items := make([]map[string]any, 0, len(pods.Items))
		for _, pod := range pods.Items {
			items = append(items, map[string]any{"name": pod.Name, "phase": pod.Status.Phase})
		}
		state["pods"] = items
	}
	if endpoints, err := client.Endpoints(ctx, namespace, name); err == nil {
		state["endpoints"] = endpoints.Subsets
	}
	return state
}

func sanitizeContainers(containers []corev1.Container) []map[string]any {
	out := make([]map[string]any, 0, len(containers))
	for _, container := range containers {
		environment := make([]map[string]any, 0, len(container.Env))
		for _, variable := range container.Env {
			upperName := strings.ToUpper(variable.Name)
			if upperName == "FAULT_MODE" || strings.HasPrefix(upperName, "BENCHMARK_") {
				continue
			}
			item := map[string]any{"name": variable.Name}
			if sensitiveEnvironmentName(upperName) {
				item["value"] = "[REDACTED]"
				if variable.ValueFrom != nil {
					item["source"] = "valueFrom"
				}
			} else if variable.ValueFrom != nil {
				item["value_from"] = variable.ValueFrom
			} else {
				item["value"] = variable.Value
			}
			environment = append(environment, item)
		}
		out = append(out, map[string]any{
			"name": container.Name, "image": container.Image,
			"image_pull_policy": container.ImagePullPolicy, "ports": container.Ports,
			"env": environment, "resources": container.Resources,
			"liveness_probe": container.LivenessProbe, "readiness_probe": container.ReadinessProbe,
		})
	}
	return out
}

func sensitiveEnvironmentName(upperName string) bool {
	for _, marker := range []string{"PASSWORD", "TOKEN", "API_KEY", "SECRET", "CREDENTIAL", "PRIVATE_KEY"} {
		if strings.Contains(upperName, marker) {
			return true
		}
	}
	return false
}

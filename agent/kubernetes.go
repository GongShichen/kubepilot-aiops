package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	evidencenorm "github.com/kubepilot-aiops/kubepilot/internal/evidence"
	"github.com/kubepilot-aiops/kubepilot/tools"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type KubernetesEvidenceCollector struct{ Client *tools.KubernetesClient }

func (a KubernetesEvidenceCollector) Collect(ctx context.Context, in *domain.Incident, request domain.EvidenceRequest) ([]domain.Evidence, error) {
	request, err := validateEvidenceRequest(in, request, "topology", nil)
	if err != nil {
		return nil, err
	}
	in = requestTargetIncident(in, request)
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
	services, servicesErr := a.Client.Services(ctx, in.Namespace)
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
		deploymentName := ""
		if depErr == nil {
			deploymentName = deployment.Name
		}
		targetNames := workloadEventTargets(in, pods.Items, deploymentName)
		for _, event := range events.Items[start:] {
			observedAt := event.LastTimestamp.Time
			if observedAt.IsZero() {
				observedAt = event.CreationTimestamp.Time
			}
			if observedAt.Before(cutoff) {
				continue
			}
			if !isWorkloadEvent(event.InvolvedObject.Name, targetNames) {
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
		policies, effects := relevantNetworkPolicyFacts(networkPolicies.Items, pods.Items)
		data["network_policies"] = policies
		data["network_policy_effects"] = effects
	}
	if depErr == nil && servicesErr == nil {
		data["discovered_dependencies"] = discoverDeploymentDependencies(deployment.Spec.Template.Spec.Containers, services.Items, in.Service)
	}
	summary := fmt.Sprintf("Kubernetes state for %s", in.Service)
	if effects, _ := data["network_policy_effects"].([]map[string]any); len(effects) > 0 {
		summary += "; active network policy isolation affects the selected workload"
	}
	items := []domain.Evidence{{Source: "kubernetes", Kind: "workload_state", Summary: summary, Data: data, ObservedAt: time.Now().UTC()}}
	return evidencenorm.Normalize(in, request, items), nil
}

// relevantNetworkPolicyFacts keeps only policies that select a pod in the
// requested workload, then projects Kubernetes's implicit isolation rules into
// explicit, model-readable effects. A nil ingress or egress list under the
// corresponding policy type means deny-all in Kubernetes, not "no change".
func relevantNetworkPolicyFacts(items []networkingv1.NetworkPolicy, pods []corev1.Pod) ([]map[string]any, []map[string]any) {
	policies := make([]map[string]any, 0, len(items))
	effects := make([]map[string]any, 0, len(items))
	for _, policy := range items {
		selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
		if err != nil {
			continue
		}
		selectedPods := make([]string, 0, len(pods))
		for _, pod := range pods {
			if selector.Matches(labels.Set(pod.Labels)) {
				selectedPods = append(selectedPods, pod.Name)
			}
		}
		if len(selectedPods) == 0 {
			continue
		}
		sort.Strings(selectedPods)
		policyTypes := effectiveNetworkPolicyTypes(policy.Spec)
		policies = append(policies, map[string]any{
			"name": policy.Name, "pod_selector": policy.Spec.PodSelector, "selected_pods": selectedPods,
			"policy_types": policyTypes, "ingress": policy.Spec.Ingress, "egress": policy.Spec.Egress,
		})
		for _, policyType := range policyTypes {
			direction := strings.ToLower(policyType)
			rules := any(policy.Spec.Ingress)
			if direction == "egress" {
				rules = policy.Spec.Egress
			}
			mode := "rules_configured"
			if lenRules(rules) == 0 {
				mode = "deny_all"
			}
			effects = append(effects, map[string]any{"policy": policy.Name, "direction": direction, "mode": mode, "selected_pods": selectedPods})
		}
	}
	return policies, effects
}

func effectiveNetworkPolicyTypes(spec networkingv1.NetworkPolicySpec) []string {
	if len(spec.PolicyTypes) > 0 {
		out := make([]string, 0, len(spec.PolicyTypes))
		for _, policyType := range spec.PolicyTypes {
			out = append(out, string(policyType))
		}
		sort.Strings(out)
		return out
	}
	out := []string{"Ingress"}
	if spec.Egress != nil {
		out = append(out, "Egress")
	}
	return out
}

func lenRules(value any) int {
	switch typed := value.(type) {
	case []networkingv1.NetworkPolicyIngressRule:
		return len(typed)
	case []networkingv1.NetworkPolicyEgressRule:
		return len(typed)
	default:
		return 0
	}
}

// workloadEventTargets scopes namespace-wide events to the workload that was
// actually requested. Kubernetes Events has no server-side selector for this,
// so we retain deployment/service/pod identities and their generated names.
// This avoids letting an unrelated Job or neighboring Deployment dictate the
// diagnosis of the incident service.
func workloadEventTargets(in *domain.Incident, pods []corev1.Pod, deploymentName string) map[string]struct{} {
	targets := map[string]struct{}{}
	for _, name := range []string{in.Service, in.Resource} {
		if name != "" {
			targets[name] = struct{}{}
		}
	}
	if deploymentName != "" {
		targets[deploymentName] = struct{}{}
	}
	for _, pod := range pods {
		if pod.Name != "" {
			targets[pod.Name] = struct{}{}
		}
	}
	return targets
}

func isWorkloadEvent(name string, targets map[string]struct{}) bool {
	if name == "" {
		return false
	}
	if _, ok := targets[name]; ok {
		return true
	}
	// ReplicaSets and Pods are generated from a Deployment name. The prefix is
	// an ownership-shaped identifier, not a fuzzy match against other services.
	for target := range targets {
		if target != "" && strings.HasPrefix(name, target+"-") {
			return true
		}
	}
	return false
}

func discoverDeploymentDependencies(containers []corev1.Container, services []corev1.Service, self string) []string {
	candidates := map[string]bool{}
	for _, service := range services {
		if service.Name != "" && service.Name != self {
			candidates[strings.ToLower(service.Name)] = true
		}
	}
	found := map[string]bool{}
	for _, container := range containers {
		for _, variable := range container.Env {
			value := strings.ToLower(variable.Value)
			for candidate := range candidates {
				if strings.Contains(strings.ToLower(variable.Name), candidate) || (!sensitiveEnvironmentName(strings.ToUpper(variable.Name)) && strings.Contains(value, candidate)) {
					found[candidate] = true
				}
			}
		}
	}
	out := make([]string, 0, len(found))
	for name := range found {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func sanitizeContainers(containers []corev1.Container) []map[string]any {
	out := make([]map[string]any, 0, len(containers))
	for _, container := range containers {
		environment := make([]map[string]any, 0, len(container.Env))
		for _, variable := range container.Env {
			upperName := strings.ToUpper(variable.Name)
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

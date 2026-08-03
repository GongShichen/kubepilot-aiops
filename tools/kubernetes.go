package tools

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type KubernetesClient struct {
	client  kubernetes.Interface
	allowed map[string]bool
}

func NewKubernetes(kubeconfig string, allowed []string) (*KubernetesClient, error) {
	var cfg *rest.Config
	var err error
	if kubeconfig != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		cfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, err
	}
	cli, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return NewKubernetesWithClient(cli, allowed), nil
}
func NewKubernetesWithClient(client kubernetes.Interface, allowed []string) *KubernetesClient {
	m := map[string]bool{}
	for _, ns := range allowed {
		m[ns] = true
	}
	return &KubernetesClient{client: client, allowed: m}
}
func (k *KubernetesClient) namespace(ns string) error {
	if !k.allowed[ns] {
		return fmt.Errorf("namespace %q is not allowed", ns)
	}
	return nil
}
func (k *KubernetesClient) Pods(ctx context.Context, ns, selector string) (*corev1.PodList, error) {
	if err := k.namespace(ns); err != nil {
		return nil, err
	}
	return k.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
}
func (k *KubernetesClient) Events(ctx context.Context, ns string) (*corev1.EventList, error) {
	if err := k.namespace(ns); err != nil {
		return nil, err
	}
	return k.client.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
}
func (k *KubernetesClient) Service(ctx context.Context, ns, name string) (*corev1.Service, error) {
	if err := k.namespace(ns); err != nil {
		return nil, err
	}
	return k.client.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
}
func (k *KubernetesClient) Endpoints(ctx context.Context, ns, name string) (*corev1.Endpoints, error) {
	if err := k.namespace(ns); err != nil {
		return nil, err
	}
	return k.client.CoreV1().Endpoints(ns).Get(ctx, name, metav1.GetOptions{})
}
func (k *KubernetesClient) ConfigMaps(ctx context.Context, ns string) (*corev1.ConfigMapList, error) {
	if err := k.namespace(ns); err != nil {
		return nil, err
	}
	return k.client.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{})
}
func (k *KubernetesClient) NetworkPolicies(ctx context.Context, ns string) (*networkingv1.NetworkPolicyList, error) {
	if err := k.namespace(ns); err != nil {
		return nil, err
	}
	return k.client.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
}
func (k *KubernetesClient) Deployment(ctx context.Context, ns, name string) (*appsv1.Deployment, error) {
	if err := k.namespace(ns); err != nil {
		return nil, err
	}
	return k.client.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
}
func (k *KubernetesClient) RestartDeployment(ctx context.Context, ns, name string) error {
	if err := k.namespace(ns); err != nil {
		return err
	}
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubepilot.io/restartedAt":%q}}}}}`, time.Now().UTC().Format(time.RFC3339))
	_, err := k.client.AppsV1().Deployments(ns).Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}
func (k *KubernetesClient) ScaleDeployment(ctx context.Context, ns, name string, replicas int32) error {
	if err := k.namespace(ns); err != nil {
		return err
	}
	if replicas < 1 || replicas > 10 {
		return fmt.Errorf("replicas must be between 1 and 10")
	}
	scale, err := k.client.AppsV1().Deployments(ns).GetScale(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	scale.Spec.Replicas = replicas
	_, err = k.client.AppsV1().Deployments(ns).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
	return err
}
func (k *KubernetesClient) RollbackDeployment(ctx context.Context, ns, name string) error {
	if err := k.namespace(ns); err != nil {
		return err
	}
	dep, err := k.client.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	selector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return err
	}
	sets, err := k.client.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return err
	}
	type revisionSet struct {
		revision int
		template appsv1.ReplicaSet
	}
	var candidates []revisionSet
	for _, rs := range sets.Items {
		rev, parseErr := strconv.Atoi(rs.Annotations["deployment.kubernetes.io/revision"])
		if parseErr == nil {
			candidates = append(candidates, revisionSet{rev, rs})
		}
	}
	if len(candidates) < 2 {
		return fmt.Errorf("no previous deployment revision available")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].revision > candidates[j].revision })
	dep.Spec.Template = candidates[1].template.Spec.Template
	dep.Spec.Paused = false
	_, err = k.client.AppsV1().Deployments(ns).Update(ctx, dep, metav1.UpdateOptions{})
	return err
}

func (k *KubernetesClient) TargetState(ctx context.Context, ns, name string) (uid, resourceVersion string, ready bool, restarts int32, err error) {
	dep, err := k.Deployment(ctx, ns, name)
	if err != nil {
		return "", "", false, 0, err
	}
	pods, err := k.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: SelectorForService(name)})
	if err != nil {
		return "", "", false, 0, err
	}
	ready = dep.Status.ReadyReplicas >= *dep.Spec.Replicas && dep.Status.UpdatedReplicas >= *dep.Spec.Replicas
	for _, pod := range pods.Items {
		podReady := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				podReady = true
			}
		}
		ready = ready && podReady
		for _, status := range pod.Status.ContainerStatuses {
			restarts += status.RestartCount
		}
	}
	return string(dep.UID), dep.ResourceVersion, ready, restarts, nil
}
func SelectorForService(service string) string { return "app=" + strings.TrimSpace(service) }

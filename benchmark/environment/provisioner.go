package environment

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	WorkerLabel   = "kubepilot.io/benchmark-worker"
	ScenarioLabel = "kubepilot.io/scenario"
)

var (
	deploymentNames = []string{"gateway-service", "order-service", "payment-service", "mysql", "redis"}
	serviceNames    = []string{"gateway-service", "order-service", "payment-service", "mysql", "redis"}
)

type Provisioner struct {
	Client             kubernetes.Interface
	TemplateNamespace  string
	ReadinessPollEvery time.Duration
}

func NewProvisioner(kubeconfig, templateNamespace string) (*Provisioner, error) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &Provisioner{Client: client, TemplateNamespace: templateNamespace}, nil
}

func WorkerNamespaces(base string, count int) ([]string, error) {
	if strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("base namespace is required")
	}
	if count < 1 || count > 32 {
		return nil, fmt.Errorf("worker count must be between 1 and 32")
	}
	out := make([]string, count)
	for index := range out {
		out[index] = fmt.Sprintf("%s-worker-%02d", base, index+1)
		if len(out[index]) > 63 {
			return nil, fmt.Errorf("worker namespace %q exceeds the Kubernetes name limit", out[index])
		}
	}
	return out, nil
}

// Prepare creates or reconciles an explicit pool of benchmark-only namespaces
// from the deployed template namespace. Existing namespaces are accepted only
// when they carry the worker label, so this operation cannot adopt user data.
func (p *Provisioner) Prepare(ctx context.Context, namespaces []string) error {
	if p == nil || p.Client == nil {
		return fmt.Errorf("Kubernetes client is required")
	}
	if p.TemplateNamespace == "" {
		return fmt.Errorf("template namespace is required")
	}
	if _, err := p.Client.CoreV1().Namespaces().Get(ctx, p.TemplateNamespace, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("read template namespace: %w", err)
	}
	for _, namespace := range namespaces {
		if err := p.prepareNamespace(ctx, namespace); err != nil {
			return fmt.Errorf("prepare worker namespace %s: %w", namespace, err)
		}
	}
	return nil
}

func (p *Provisioner) prepareNamespace(ctx context.Context, namespace string) error {
	if namespace == p.TemplateNamespace || !strings.HasPrefix(namespace, p.TemplateNamespace+"-worker-") {
		return fmt.Errorf("namespace is outside the explicit benchmark worker pool")
	}
	current, err := p.Client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = p.Client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{WorkerLabel: "true"}}}, metav1.CreateOptions{})
	} else if err == nil && current.Labels[WorkerLabel] != "true" {
		return fmt.Errorf("existing namespace is not labeled as a benchmark worker")
	}
	if err != nil {
		return err
	}
	if err = p.copySecret(ctx, namespace, "demo-secrets"); err != nil {
		return err
	}
	if err = p.copyServiceAccount(ctx, namespace, "kubepilot-agent"); err != nil {
		return err
	}
	if err = p.copyRole(ctx, namespace, "kubepilot-agent"); err != nil {
		return err
	}
	if err = p.copyRoleBinding(ctx, namespace, "kubepilot-agent"); err != nil {
		return err
	}
	for _, name := range serviceNames {
		if err = p.copyService(ctx, namespace, name); err != nil {
			return err
		}
	}
	for _, name := range deploymentNames {
		if err = p.copyDeployment(ctx, namespace, name); err != nil {
			return err
		}
	}
	deleteOptions := metav1.DeleteOptions{PropagationPolicy: pointer(metav1.DeletePropagationBackground)}
	if err = p.Client.BatchV1().Jobs(namespace).DeleteCollection(ctx, deleteOptions, metav1.ListOptions{LabelSelector: ScenarioLabel}); err != nil {
		return fmt.Errorf("delete stale load jobs: %w", err)
	}
	if err = p.Client.NetworkingV1().NetworkPolicies(namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: ScenarioLabel}); err != nil {
		return fmt.Errorf("delete stale network policies: %w", err)
	}
	return p.waitReady(ctx, namespace)
}

func (p *Provisioner) copySecret(ctx context.Context, namespace, name string) error {
	source, err := p.Client.CoreV1().Secrets(p.TemplateNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read template Secret %s: %w", name, err)
	}
	desired := &corev1.Secret{ObjectMeta: cloneMetadata(source.ObjectMeta, namespace), Data: cloneBytesMap(source.Data), Type: source.Type, Immutable: source.Immutable}
	current, err := p.Client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = p.Client.CoreV1().Secrets(namespace).Create(ctx, desired, metav1.CreateOptions{})
	} else if err == nil {
		current.Data, current.Type, current.Immutable = desired.Data, desired.Type, desired.Immutable
		_, err = p.Client.CoreV1().Secrets(namespace).Update(ctx, current, metav1.UpdateOptions{})
	}
	return err
}

func (p *Provisioner) copyServiceAccount(ctx context.Context, namespace, name string) error {
	source, err := p.Client.CoreV1().ServiceAccounts(p.TemplateNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read template ServiceAccount %s: %w", name, err)
	}
	desired := &corev1.ServiceAccount{ObjectMeta: cloneMetadata(source.ObjectMeta, namespace), AutomountServiceAccountToken: source.AutomountServiceAccountToken}
	_, err = p.Client.CoreV1().ServiceAccounts(namespace).Create(ctx, desired, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (p *Provisioner) copyRole(ctx context.Context, namespace, name string) error {
	source, err := p.Client.RbacV1().Roles(p.TemplateNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read template Role %s: %w", name, err)
	}
	desired := &rbacv1.Role{ObjectMeta: cloneMetadata(source.ObjectMeta, namespace), Rules: append([]rbacv1.PolicyRule(nil), source.Rules...)}
	current, err := p.Client.RbacV1().Roles(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = p.Client.RbacV1().Roles(namespace).Create(ctx, desired, metav1.CreateOptions{})
	} else if err == nil {
		current.Rules = desired.Rules
		_, err = p.Client.RbacV1().Roles(namespace).Update(ctx, current, metav1.UpdateOptions{})
	}
	return err
}

func (p *Provisioner) copyRoleBinding(ctx context.Context, namespace, name string) error {
	source, err := p.Client.RbacV1().RoleBindings(p.TemplateNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read template RoleBinding %s: %w", name, err)
	}
	desired := &rbacv1.RoleBinding{ObjectMeta: cloneMetadata(source.ObjectMeta, namespace), Subjects: append([]rbacv1.Subject(nil), source.Subjects...), RoleRef: source.RoleRef}
	for index := range desired.Subjects {
		if desired.Subjects[index].Kind == "ServiceAccount" {
			desired.Subjects[index].Namespace = namespace
		}
	}
	current, err := p.Client.RbacV1().RoleBindings(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = p.Client.RbacV1().RoleBindings(namespace).Create(ctx, desired, metav1.CreateOptions{})
	} else if err == nil {
		current.Subjects, current.RoleRef = desired.Subjects, desired.RoleRef
		_, err = p.Client.RbacV1().RoleBindings(namespace).Update(ctx, current, metav1.UpdateOptions{})
	}
	return err
}

func (p *Provisioner) copyService(ctx context.Context, namespace, name string) error {
	source, err := p.Client.CoreV1().Services(p.TemplateNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read template Service %s: %w", name, err)
	}
	desired := &corev1.Service{ObjectMeta: cloneMetadata(source.ObjectMeta, namespace), Spec: *source.Spec.DeepCopy()}
	desired.Spec.ClusterIP = ""
	desired.Spec.ClusterIPs = nil
	desired.Spec.IPFamilies = nil
	desired.Spec.IPFamilyPolicy = nil
	desired.Spec.HealthCheckNodePort = 0
	current, err := p.Client.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = p.Client.CoreV1().Services(namespace).Create(ctx, desired, metav1.CreateOptions{})
	} else if err == nil {
		clusterIP, clusterIPs := current.Spec.ClusterIP, append([]string(nil), current.Spec.ClusterIPs...)
		ipFamilies, ipPolicy := append([]corev1.IPFamily(nil), current.Spec.IPFamilies...), current.Spec.IPFamilyPolicy
		current.Spec = desired.Spec
		current.Spec.ClusterIP, current.Spec.ClusterIPs = clusterIP, clusterIPs
		current.Spec.IPFamilies, current.Spec.IPFamilyPolicy = ipFamilies, ipPolicy
		_, err = p.Client.CoreV1().Services(namespace).Update(ctx, current, metav1.UpdateOptions{})
	}
	return err
}

func (p *Provisioner) copyDeployment(ctx context.Context, namespace, name string) error {
	source, err := p.Client.AppsV1().Deployments(p.TemplateNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read template Deployment %s: %w", name, err)
	}
	desired := &appsv1.Deployment{ObjectMeta: cloneMetadata(source.ObjectMeta, namespace), Spec: *source.Spec.DeepCopy(), Status: source.Status}
	current, err := p.Client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = p.Client.AppsV1().Deployments(namespace).Create(ctx, desired, metav1.CreateOptions{})
	} else if err == nil {
		current.Spec = desired.Spec
		_, err = p.Client.AppsV1().Deployments(namespace).Update(ctx, current, metav1.UpdateOptions{})
	}
	return err
}

func (p *Provisioner) waitReady(ctx context.Context, namespace string) error {
	interval := p.ReadinessPollEvery
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		allReady := true
		for _, name := range deploymentNames {
			deployment, err := p.Client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			desired := int32(1)
			if deployment.Spec.Replicas != nil {
				desired = *deployment.Spec.Replicas
			}
			if deployment.Status.AvailableReplicas < desired || deployment.Status.ObservedGeneration < deployment.Generation {
				allReady = false
				break
			}
		}
		if allReady {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for worker workloads: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func cloneMetadata(source metav1.ObjectMeta, namespace string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: source.Name, Namespace: namespace, Labels: cloneStringMap(source.Labels), Annotations: cloneStringMap(source.Annotations)}
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func cloneBytesMap(source map[string][]byte) map[string][]byte {
	if source == nil {
		return nil
	}
	out := make(map[string][]byte, len(source))
	for key, value := range source {
		out[key] = append([]byte(nil), value...)
	}
	return out
}

func pointer[T any](value T) *T { return &value }

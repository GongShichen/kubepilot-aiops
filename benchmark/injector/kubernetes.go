package injector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

type Kubernetes struct {
	client      kubernetes.Interface
	mu          sync.Mutex
	deployments map[string]*appsv1.Deployment
	services    map[string]*corev1.Service
	scales      map[string]int32
	restarts    map[string]int32
}

func NewKubernetes(kubeconfig string) (*Kubernetes, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}
	cli, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Kubernetes{client: cli, deployments: map[string]*appsv1.Deployment{}, services: map[string]*corev1.Service{}, scales: map[string]int32{}, restarts: map[string]int32{}}, nil
}
func (k *Kubernetes) key(s scenarios.Scenario) string { return s.Namespace + "/" + s.ID }
func (k *Kubernetes) Preflight(ctx context.Context, s scenarios.Scenario) error {
	if s.Namespace != "kubepilot-benchmark" {
		return fmt.Errorf("unsafe namespace %q", s.Namespace)
	}
	_, err := k.client.AppsV1().Deployments(s.Namespace).Get(ctx, s.Target, metav1.GetOptions{})
	return err
}
func (k *Kubernetes) RestoreBaseline(ctx context.Context, s scenarios.Scenario) error {
	if err := k.Cleanup(ctx, s); err != nil {
		return err
	}
	return k.normalizeBaseline(ctx, s)
}

// normalizeBaseline restores the known demo contract without relying on an
// in-memory snapshot. This is required after runner crashes or machine restarts,
// where fields written by client-go are intentionally not removed by kubectl
// apply because they belong to a different field manager.
func (k *Kubernetes) normalizeBaseline(ctx context.Context, s scenarios.Scenario) error {
	for _, service := range []string{"gateway-service", "order-service", "payment-service"} {
		if err := k.controlFault(ctx, s.Namespace, service, ""); err != nil {
			return fmt.Errorf("clear stale in-process fault for %s: %w", service, err)
		}
	}
	selector := "kubepilot.io/scenario"
	deleteOptions := metav1.DeleteOptions{PropagationPolicy: ptr(metav1.DeletePropagationBackground)}
	if err := k.client.BatchV1().Jobs(s.Namespace).DeleteCollection(ctx, deleteOptions, metav1.ListOptions{LabelSelector: selector}); err != nil {
		return fmt.Errorf("delete stale benchmark jobs: %w", err)
	}
	if err := k.client.NetworkingV1().NetworkPolicies(s.Namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: selector}); err != nil {
		return fmt.Errorf("delete stale benchmark policies: %w", err)
	}
	for _, service := range []string{"gateway-service", "order-service", "payment-service"} {
		if err := k.normalizeDeployment(ctx, s.Namespace, service); err != nil {
			return err
		}
		if err := k.normalizeService(ctx, s.Namespace, service); err != nil {
			return err
		}
	}
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		scale, err := k.client.AppsV1().Deployments(s.Namespace).GetScale(ctx, "mysql", metav1.GetOptions{})
		if err != nil {
			return err
		}
		scale.Spec.Replicas = 1
		_, err = k.client.AppsV1().Deployments(s.Namespace).UpdateScale(ctx, "mysql", scale, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return fmt.Errorf("restore MySQL baseline: %w", err)
	}
	return nil
}

func (k *Kubernetes) normalizeDeployment(ctx context.Context, namespace, service string) error {
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		dep, err := k.client.AppsV1().Deployments(namespace).Get(ctx, service, metav1.GetOptions{})
		if err != nil {
			return err
		}
		one := int32(1)
		dep.Spec.Replicas = &one
		if len(dep.Spec.Template.Spec.Containers) == 0 {
			return fmt.Errorf("deployment %s has no containers", service)
		}
		container := &dep.Spec.Template.Spec.Containers[0]
		container.Image = "kubepilot/demo-service:0.1.0"
		container.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
		}
		cleanEnv := make([]corev1.EnvVar, 0, len(container.Env))
		for _, variable := range container.Env {
			if variable.Name == "FAULT_MODE" {
				continue
			}
			if variable.Name == "DB_PASSWORD" {
				variable.Value = ""
				variable.ValueFrom = &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "demo-secrets"}, Key: "mysql-password"}}
			}
			cleanEnv = append(cleanEnv, variable)
		}
		container.Env = cleanEnv
		_, err = k.client.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return fmt.Errorf("restore deployment %s baseline: %w", service, err)
	}
	return nil
}

func (k *Kubernetes) normalizeService(ctx context.Context, namespace, service string) error {
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		svc, err := k.client.CoreV1().Services(namespace).Get(ctx, service, metav1.GetOptions{})
		if err != nil {
			return err
		}
		svc.Spec.Selector = map[string]string{"app": service}
		if len(svc.Spec.Ports) == 0 {
			svc.Spec.Ports = []corev1.ServicePort{{Name: "http", Protocol: corev1.ProtocolTCP, Port: 8080, TargetPort: intstr.FromString("http")}}
		} else {
			svc.Spec.Ports[0].TargetPort = intstr.FromString("http")
		}
		_, err = k.client.CoreV1().Services(namespace).Update(ctx, svc, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return fmt.Errorf("restore service %s baseline: %w", service, err)
	}
	return nil
}

func ptr[T any](value T) *T { return &value }
func (k *Kubernetes) Inject(ctx context.Context, s scenarios.Scenario) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	key := k.key(s)
	if s.Injector == "service_fault" || s.Injector == "traffic" {
		k.restarts[key], _ = k.currentRestarts(ctx, s.Namespace, s.Service)
		if err := k.controlFault(ctx, s.Namespace, s.Service, s.Variant); err != nil {
			return err
		}
		return k.createLoad(ctx, s)
	}
	switch s.Injector {
	case "dependency_scale":
		scale, err := k.client.AppsV1().Deployments(s.Namespace).GetScale(ctx, "mysql", metav1.GetOptions{})
		if err != nil {
			return err
		}
		k.scales[key] = scale.Spec.Replicas
		scale.Spec.Replicas = 0
		_, err = k.client.AppsV1().Deployments(s.Namespace).UpdateScale(ctx, "mysql", scale, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		return k.createLoad(ctx, s)
	case "service_patch":
		svc, err := k.client.CoreV1().Services(s.Namespace).Get(ctx, s.Target, metav1.GetOptions{})
		if err != nil {
			return err
		}
		k.services[key] = svc.DeepCopy()
		if s.Variant == "selector_mismatch" {
			svc.Spec.Selector = map[string]string{"app": "no-such-workload"}
		} else if len(svc.Spec.Ports) > 0 {
			svc.Spec.Ports[0].TargetPort = intstr.FromInt(1)
		}
		_, err = k.client.CoreV1().Services(s.Namespace).Update(ctx, svc, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		return k.createLoad(ctx, s)
	case "network_policy":
		policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: networkPolicyName(s), Namespace: s.Namespace, Labels: map[string]string{"kubepilot.io/scenario": s.ID}}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": s.Service}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, Egress: []networkingv1.NetworkPolicyEgressRule{}}}
		_, err := k.client.NetworkingV1().NetworkPolicies(s.Namespace).Create(ctx, policy, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		return k.createLoad(ctx, s)
	default:
		dep, err := k.client.AppsV1().Deployments(s.Namespace).Get(ctx, s.Target, metav1.GetOptions{})
		if err != nil {
			return err
		}
		k.deployments[key] = dep.DeepCopy()
		if len(dep.Spec.Template.Spec.Containers) == 0 {
			return fmt.Errorf("deployment has no containers")
		}
		c := &dep.Spec.Template.Spec.Containers[0]
		if c.Resources.Limits == nil {
			c.Resources.Limits = corev1.ResourceList{}
		}
		switch s.Injector {
		case "resource_patch":
			applyLowResourceLimit(c, s.Category)
		case "deployment_patch":
			if s.Variant == "bad_image" {
				c.Image = "invalid.local/kubepilot/missing:" + s.ID
			} else {
				setEnv(c, "FAULT_MODE", s.Variant)
			}
		case "config_patch":
			if s.Variant == "invalid_credentials" {
				setEnv(c, "DB_PASSWORD", "invalid-benchmark-password")
			} else {
				setEnv(c, "FAULT_MODE", s.Variant)
			}
		default:
			return fmt.Errorf("unsupported injector %q", s.Injector)
		}
		_, err = k.client.AppsV1().Deployments(s.Namespace).Update(ctx, dep, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		return k.createLoad(ctx, s)
	}
}

// applyLowResourceLimit keeps requests valid while intentionally constraining
// the container below its normal steady-state requirement. Kubernetes rejects a
// Pod template when a request is greater than its corresponding limit.
func applyLowResourceLimit(container *corev1.Container, category string) {
	if container.Resources.Requests == nil {
		container.Resources.Requests = corev1.ResourceList{}
	}
	if container.Resources.Limits == nil {
		container.Resources.Limits = corev1.ResourceList{}
	}
	if category == "memory" {
		low := resource.MustParse("32Mi")
		container.Resources.Requests[corev1.ResourceMemory] = low
		container.Resources.Limits[corev1.ResourceMemory] = low
		return
	}
	low := resource.MustParse("10m")
	container.Resources.Requests[corev1.ResourceCPU] = low
	container.Resources.Limits[corev1.ResourceCPU] = low
}
func (k *Kubernetes) Cleanup(ctx context.Context, s scenarios.Scenario) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	key := k.key(s)
	if s.Injector == "service_fault" || s.Injector == "traffic" {
		if err := k.controlFault(ctx, s.Namespace, s.Service, ""); err != nil {
			return err
		}
		delete(k.restarts, key)
	}
	if original := k.deployments[key]; original != nil {
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			current, getErr := k.client.AppsV1().Deployments(s.Namespace).Get(ctx, s.Target, metav1.GetOptions{})
			if getErr != nil {
				return getErr
			}
			current.Spec = *original.Spec.DeepCopy()
			_, updateErr := k.client.AppsV1().Deployments(s.Namespace).Update(ctx, current, metav1.UpdateOptions{})
			return updateErr
		})
		if err != nil {
			return err
		}
		delete(k.deployments, key)
	}
	if original := k.services[key]; original != nil {
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			current, getErr := k.client.CoreV1().Services(s.Namespace).Get(ctx, s.Target, metav1.GetOptions{})
			if getErr != nil {
				return getErr
			}
			current.Spec.Selector = original.Spec.Selector
			current.Spec.Ports = original.Spec.Ports
			_, updateErr := k.client.CoreV1().Services(s.Namespace).Update(ctx, current, metav1.UpdateOptions{})
			return updateErr
		})
		if err != nil {
			return err
		}
		delete(k.services, key)
	}
	if replicas, ok := k.scales[key]; ok {
		scale, err := k.client.AppsV1().Deployments(s.Namespace).GetScale(ctx, "mysql", metav1.GetOptions{})
		if err != nil {
			return err
		}
		scale.Spec.Replicas = replicas
		if _, err = k.client.AppsV1().Deployments(s.Namespace).UpdateScale(ctx, "mysql", scale, metav1.UpdateOptions{}); err != nil {
			return err
		}
		delete(k.scales, key)
	}
	err := k.client.NetworkingV1().NetworkPolicies(s.Namespace).Delete(ctx, networkPolicyName(s), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	propagation := metav1.DeletePropagationBackground
	err = k.client.BatchV1().Jobs(s.Namespace).Delete(ctx, loadJobName(s), metav1.DeleteOptions{PropagationPolicy: &propagation})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func networkPolicyName(s scenarios.Scenario) string { return "service-egress-policy-" + s.Service }

func (k *Kubernetes) controlFault(ctx context.Context, namespace, service, mode string) error {
	secret, err := k.client.CoreV1().Secrets(namespace).Get(ctx, "demo-secrets", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read benchmark control token: %w", err)
	}
	token := string(secret.Data["benchmark-control-token"])
	if token == "" {
		return fmt.Errorf("benchmark control token is empty")
	}
	request := k.client.CoreV1().RESTClient().Delete()
	if mode != "" {
		body, marshalErr := json.Marshal(map[string]string{"mode": mode})
		if marshalErr != nil {
			return marshalErr
		}
		request = k.client.CoreV1().RESTClient().Post().Body(body).SetHeader("Content-Type", "application/json")
	}
	result := request.Namespace(namespace).
		Resource("services").
		SubResource("proxy").
		Name("http:"+service+":http").
		Suffix("benchmark", "v1", "fault").
		SetHeader("X-KubePilot-Benchmark-Token", token).
		Do(ctx)
	if err = result.Error(); err != nil {
		return fmt.Errorf("set in-process fault %q on %s: %w", mode, service, err)
	}
	return nil
}

type faultStatus struct {
	Mode          string `json:"mode"`
	RetainedBytes int    `json:"retained_bytes"`
}

func (k *Kubernetes) getFaultStatus(ctx context.Context, namespace, service string) (faultStatus, error) {
	secret, err := k.client.CoreV1().Secrets(namespace).Get(ctx, "demo-secrets", metav1.GetOptions{})
	if err != nil {
		return faultStatus{}, err
	}
	raw, err := k.client.CoreV1().RESTClient().Get().Namespace(namespace).
		Resource("services").SubResource("proxy").Name("http:"+service+":http").
		Suffix("benchmark", "v1", "fault").
		SetHeader("X-KubePilot-Benchmark-Token", string(secret.Data["benchmark-control-token"])).DoRaw(ctx)
	if err != nil {
		return faultStatus{}, err
	}
	var status faultStatus
	if err = json.Unmarshal(raw, &status); err != nil {
		return faultStatus{}, err
	}
	return status, nil
}

func (k *Kubernetes) currentRestarts(ctx context.Context, namespace, service string) (int32, error) {
	pods, err := k.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + service})
	if err != nil {
		return 0, err
	}
	var total int32
	for _, pod := range pods.Items {
		for _, container := range pod.Status.ContainerStatuses {
			total += container.RestartCount
		}
	}
	return total, nil
}

func (k *Kubernetes) createLoad(ctx context.Context, s scenarios.Scenario) error {
	backoff := int32(0)
	ttl := int32(120)
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: loadJobName(s), Namespace: s.Namespace, Labels: map[string]string{"kubepilot.io/scenario": s.ID}}, Spec: batchv1.JobSpec{BackoffLimit: &backoff, TTLSecondsAfterFinished: &ttl, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "load", Image: "busybox:1.37.0", Command: []string{"sh", "-c"}, Args: []string{`end=$(( $(date +%s) + 75 )); while [ "$(date +%s)" -lt "$end" ]; do wget -q -T 3 -O /dev/null http://gateway-service:8080/orders || true; sleep 0.2; done`}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("8Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m"), corev1.ResourceMemory: resource.MustParse("32Mi")}}}}}}}}
	_, err := k.client.BatchV1().Jobs(s.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}
func loadJobName(s scenarios.Scenario) string {
	name := strings.Map(func(r rune) rune {
		if unicode.IsLower(r) || unicode.IsDigit(r) || r == '-' {
			return r
		}
		if unicode.IsUpper(r) {
			return unicode.ToLower(r)
		}
		return '-'
	}, "benchmark-load-"+s.ID)
	name = strings.Trim(name, "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}
func (k *Kubernetes) Healthy(ctx context.Context, s scenarios.Scenario) error {
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		dep, err := k.client.AppsV1().Deployments(s.Namespace).Get(ctx, s.Target, metav1.GetOptions{})
		if err == nil && dep.Status.ReadyReplicas >= 1 && dep.Status.ObservedGeneration >= dep.Generation {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("deployment %s did not become healthy", s.Target)
		case <-ticker.C:
		}
	}
}
func (k *Kubernetes) WaitVisible(ctx context.Context, s scenarios.Scenario) error {
	started := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	observed := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if s.Injector == "service_fault" || s.Injector == "traffic" {
				status, statusErr := k.getFaultStatus(ctx, s.Namespace, s.Service)
				switch s.Variant {
				case "memory_leak", "unbounded_cache":
					restarts, restartErr := k.currentRestarts(ctx, s.Namespace, s.Service)
					observed = (statusErr == nil && status.Mode == s.Variant && status.RetainedBytes >= 64<<20) || (restartErr == nil && restarts > k.restarts[k.key(s)])
				case "memory_burst":
					restarts, restartErr := k.currentRestarts(ctx, s.Namespace, s.Service)
					observed = restartErr == nil && restarts > k.restarts[k.key(s)]
				default:
					observed = statusErr == nil && status.Mode == s.Variant
				}
			} else if s.Injector == "dependency_scale" {
				scale, err := k.client.AppsV1().Deployments(s.Namespace).GetScale(ctx, "mysql", metav1.GetOptions{})
				observed = err == nil && scale.Spec.Replicas == 0
			} else if s.Injector == "service_patch" || s.Injector == "network_policy" {
				observed = true
			} else {
				dep, err := k.client.AppsV1().Deployments(s.Namespace).Get(ctx, s.Target, metav1.GetOptions{})
				if err == nil {
					if s.Variant == "bad_image" {
						observed = dep.Status.UnavailableReplicas > 0
					} else {
						observed = dep.Status.ObservedGeneration >= dep.Generation
					}
				}
			}
			// Prometheus scrapes every 15 seconds locally. Keep the fault active
			// long enough for two complete samples so irate/current evidence cannot
			// reuse the final sample from the preceding serial benchmark case.
			if observed && time.Since(started) >= 35*time.Second {
				return nil
			}
		}
	}
}
func setEnv(c *corev1.Container, name, value string) {
	for i := range c.Env {
		if c.Env[i].Name == name {
			c.Env[i].Value = value
			c.Env[i].ValueFrom = nil
			return
		}
	}
	c.Env = append(c.Env, corev1.EnvVar{Name: name, Value: value})
}

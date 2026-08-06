package environment

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWorkerNamespacesAreDeterministicAndBounded(t *testing.T) {
	actual, err := WorkerNamespaces("kubepilot-benchmark", 4)
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"kubepilot-benchmark-worker-01", "kubepilot-benchmark-worker-02", "kubepilot-benchmark-worker-03", "kubepilot-benchmark-worker-04"}
	for index := range expected {
		if actual[index] != expected[index] {
			t.Fatalf("namespace %d=%q, want %q", index, actual[index], expected[index])
		}
	}
	if _, err = WorkerNamespaces("kubepilot-benchmark", 33); err == nil {
		t.Fatal("unbounded worker pool was accepted")
	}
}

func TestProvisionerReconcilesExplicitWorkerNamespace(t *testing.T) {
	template := "kubepilot-benchmark"
	objects := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: template}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "demo-secrets", Namespace: template}, Data: map[string][]byte{"benchmark-control-token": []byte("token")}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "kubepilot-agent", Namespace: template}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "kubepilot-agent", Namespace: template}, Rules: []rbacv1.PolicyRule{{Verbs: []string{"get"}, Resources: []string{"pods"}}}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "kubepilot-agent", Namespace: template}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "kubepilot-agent"}}, RoleRef: rbacv1.RoleRef{Kind: "Role", Name: "kubepilot-agent"}},
	}
	one := int32(1)
	for _, name := range serviceNames {
		objects = append(objects, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: template}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": name}, Ports: []corev1.ServicePort{{Name: "http", Port: 8080}}}})
	}
	for _, name := range deploymentNames {
		objects = append(objects, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: template, Generation: 1}, Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: name, Image: "image"}}}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, AvailableReplicas: 1}})
	}
	client := fake.NewSimpleClientset(objects...)
	provisioner := &Provisioner{Client: client, TemplateNamespace: template, ReadinessPollEvery: time.Millisecond}
	worker := "kubepilot-benchmark-worker-01"
	if err := provisioner.Prepare(context.Background(), []string{worker}); err != nil {
		t.Fatal(err)
	}
	namespace, err := client.CoreV1().Namespaces().Get(context.Background(), worker, metav1.GetOptions{})
	if err != nil || namespace.Labels[WorkerLabel] != "true" {
		t.Fatalf("worker namespace=%+v err=%v", namespace, err)
	}
	deployment, err := client.AppsV1().Deployments(worker).Get(context.Background(), "payment-service", metav1.GetOptions{})
	if err != nil || deployment.Spec.Template.Spec.Containers[0].Image != "image" {
		t.Fatalf("cloned deployment=%+v err=%v", deployment, err)
	}
	secret, err := client.CoreV1().Secrets(worker).Get(context.Background(), "demo-secrets", metav1.GetOptions{})
	if err != nil || string(secret.Data["benchmark-control-token"]) != "token" {
		t.Fatalf("cloned secret=%+v err=%v", secret, err)
	}
}

func TestProvisionerRefusesUnlabeledNamespace(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kubepilot-benchmark"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kubepilot-benchmark-worker-01"}},
	)
	provisioner := &Provisioner{Client: client, TemplateNamespace: "kubepilot-benchmark"}
	if err := provisioner.Prepare(context.Background(), []string{"kubepilot-benchmark-worker-01"}); err == nil {
		t.Fatal("unlabeled namespace was adopted")
	}
}

package tools

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestRestartDryRunUsesKubernetesDryRunAll(t *testing.T) {
	replicas := int32(1)
	clientset := fake.NewClientset(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "gateway-service", Namespace: "kubepilot-demo", UID: "uid-1", ResourceVersion: "rv-1"}, Spec: appsv1.DeploymentSpec{Replicas: &replicas}})
	observed := false
	clientset.PrependReactor("patch", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patchAction, ok := action.(k8stesting.PatchActionImpl)
		if !ok {
			t.Fatalf("action type %T", action)
		}
		observed = len(patchAction.GetPatchOptions().DryRun) == 1 && patchAction.GetPatchOptions().DryRun[0] == metav1.DryRunAll
		return false, nil, nil
	})
	client := NewKubernetesWithClient(clientset, []string{"kubepilot-demo"})
	if _, _, err := client.DryRunRestartDeployment(context.Background(), "kubepilot-demo", "gateway-service", "2026-08-03T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if !observed {
		t.Fatal("restart mutation did not use DryRunAll")
	}
}

package system

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestEnsureSystemOwnerReferenceUsesOperatorDeployment(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(apps) error = %v", err)
	}
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(edge) error = %v", err)
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chill-operator",
			Namespace: "chill-system",
			UID:       types.UID("operator-deployment-uid"),
		},
	}
	system := &edgev1alpha1.ChillSystem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chill",
			Namespace: "chill-system",
		},
	}
	reconciler := &ChillSystemReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment, system).Build(),
		Options: Options{
			SystemName:             "chill",
			Namespace:              "chill-system",
			OperatorDeploymentName: "chill-operator",
			RefreshInterval:        DefaultRefreshInterval,
		},
	}

	changed, err := reconciler.ensureSystemOwnerReference(context.Background(), system)
	if err != nil {
		t.Fatalf("ensureSystemOwnerReference() error = %v", err)
	}
	if !changed {
		t.Fatal("ensureSystemOwnerReference() changed = false, want true")
	}

	updated := &edgev1alpha1.ChillSystem{}
	if err := reconciler.Get(context.Background(), types.NamespacedName{Namespace: "chill-system", Name: "chill"}, updated); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(updated.OwnerReferences) != 1 {
		t.Fatalf("OwnerReferences len = %d, want 1", len(updated.OwnerReferences))
	}
	owner := updated.OwnerReferences[0]
	if owner.APIVersion != appsv1.SchemeGroupVersion.String() ||
		owner.Kind != "Deployment" ||
		owner.Name != "chill-operator" ||
		owner.UID != deployment.UID {
		t.Fatalf("OwnerReference = %#v, want operator Deployment owner", owner)
	}
}

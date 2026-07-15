package system

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
)

func TestObserveEvidenceStoreUsesOnlyResourcesControlledBySystem(t *testing.T) {
	scheme := runtime.NewScheme()
	for name, add := range map[string]func(*runtime.Scheme) error{
		"apps":  appsv1.AddToScheme,
		"batch": batchv1.AddToScheme,
		"core":  corev1.AddToScheme,
		"edge":  edgev1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("AddToScheme(%s) error = %v", name, err)
		}
	}

	system := &edgev1alpha1.ChillSystem{
		ObjectMeta: metav1.ObjectMeta{Name: "research", UID: types.UID("research-system-uid")},
		Spec: edgev1alpha1.ChillSystemSpec{
			ManagementNamespace: "chill-system",
			EvidenceStore:       edgev1alpha1.ChillEvidenceStoreSpec{Enabled: true},
		},
	}
	deployment := namedDeployment(component.EvidenceStoreDeploymentName(system.Name), 1)
	deployment.Namespace = system.Spec.ManagementNamespace
	job := bootstrapJob(batchv1.JobComplete, "")
	job.Name = component.EvidenceStoreBootstrapJobName(system.Name)
	job.Namespace = system.Spec.ManagementNamespace
	for _, object := range []metav1.Object{deployment, job} {
		if err := controllerutil.SetControllerReference(system, object, scheme); err != nil {
			t.Fatal(err)
		}
	}

	reconciler := &ChillSystemReconciler{
		Client:  fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, deployment, job).Build(),
		Scheme:  scheme,
		Options: Options{SystemName: system.Name, Namespace: system.Spec.ManagementNamespace},
	}
	observed := reconciler.observe(context.Background(), system)
	if observed.EvidenceStoreDeployment == nil || observed.EvidenceStoreBootstrapJob == nil || observed.EvidenceStoreError != nil {
		t.Fatalf("owned Evidence Store observation = %#v", observed)
	}

	deployment.OwnerReferences = nil
	job.OwnerReferences = nil
	reconciler.Client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, deployment, job).Build()
	observed = reconciler.observe(context.Background(), system)
	if observed.EvidenceStoreDeployment != nil || observed.EvidenceStoreBootstrapJob != nil {
		t.Fatalf("foreign Evidence Store resources were observed: %#v", observed)
	}
}

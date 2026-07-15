package evidencestore

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const retainedVolumeName = "retained-pv"

func TestEnsurePersistentVolumeClaimCreatesRetainedClaim(t *testing.T) {
	ctx := context.Background()
	instance := validInstance()
	config := validConfig()
	k8sClient := fake.NewClientBuilder().WithScheme(pvcTestScheme(t)).Build()

	claim, outcome, err := ensurePersistentVolumeClaim(ctx, k8sClient, instance, config)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != PersistentVolumeClaimOutcomeCreated {
		t.Fatalf("outcome = %q", outcome)
	}
	if len(claim.OwnerReferences) != 0 {
		t.Fatalf("retained claim ownerReferences = %#v", claim.OwnerReferences)
	}
	if claim.Spec.Resources.Requests.Storage().Cmp(config.StorageSize) != 0 {
		t.Fatalf("storage request = %s", claim.Spec.Resources.Requests.Storage().String())
	}
}

func TestEnsurePersistentVolumeClaimReusesAndExpands(t *testing.T) {
	ctx := context.Background()
	instance := validInstance()
	config := validConfig()
	retained := buildPersistentVolumeClaim(instance, config)
	retained.Spec.VolumeName = retainedVolumeName
	k8sClient := fake.NewClientBuilder().WithScheme(pvcTestScheme(t)).WithObjects(retained).Build()

	claim, outcome, err := ensurePersistentVolumeClaim(ctx, k8sClient, instance, config)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != PersistentVolumeClaimOutcomeExisting || claim.Spec.VolumeName != retainedVolumeName {
		t.Fatalf("existing claim = %#v, outcome = %q", claim.Spec, outcome)
	}

	config.StorageSize = resource.MustParse("2Gi")
	claim, outcome, err = ensurePersistentVolumeClaim(ctx, k8sClient, instance, config)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != PersistentVolumeClaimOutcomeExpanded ||
		claim.Spec.Resources.Requests.Storage().Cmp(config.StorageSize) != 0 ||
		claim.Spec.VolumeName != retainedVolumeName {
		t.Fatalf("expanded claim = %#v, outcome = %q", claim.Spec, outcome)
	}
}

func TestEnsurePersistentVolumeClaimRejectsDestructiveDrift(t *testing.T) {
	instance := validInstance()
	baseConfig := validConfig()
	tests := []struct {
		name       string
		mutatePVC  func(*corev1.PersistentVolumeClaim)
		mutateSpec func(*Config)
		want       string
	}{
		{
			name: "shrink",
			mutatePVC: func(claim *corev1.PersistentVolumeClaim) {
				claim.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("2Gi")
			},
			want: "cannot shrink",
		},
		{
			name: "storage class",
			mutateSpec: func(config *Config) {
				config.StorageClassName = "another-class"
			},
			want: "cannot change storageClassName",
		},
		{
			name: "foreign claim",
			mutatePVC: func(claim *corev1.PersistentVolumeClaim) {
				delete(claim.Labels, "app.kubernetes.io/managed-by")
			},
			want: "not managed by this ChillSystem",
		},
		{
			name: "owner reference",
			mutatePVC: func(claim *corev1.PersistentVolumeClaim) {
				claim.OwnerReferences = []metav1.OwnerReference{{Name: "unsafe-owner"}}
			},
			want: "must not have owner references",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := baseConfig
			if test.mutateSpec != nil {
				test.mutateSpec(&config)
			}
			claim := buildPersistentVolumeClaim(instance, baseConfig)
			if test.mutatePVC != nil {
				test.mutatePVC(claim)
			}
			k8sClient := fake.NewClientBuilder().WithScheme(pvcTestScheme(t)).WithObjects(claim).Build()

			_, _, err := ensurePersistentVolumeClaim(context.Background(), k8sClient, instance, config)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ensurePersistentVolumeClaim() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEnsurePersistentVolumeClaimAcceptsDefaultedStorageClass(t *testing.T) {
	instance := validInstance()
	config := validConfig()
	config.StorageClassName = ""
	claim := buildPersistentVolumeClaim(instance, config)
	defaultClass := "cluster-default"
	claim.Spec.StorageClassName = &defaultClass
	k8sClient := fake.NewClientBuilder().WithScheme(pvcTestScheme(t)).WithObjects(claim).Build()

	_, outcome, err := ensurePersistentVolumeClaim(context.Background(), k8sClient, instance, config)
	if err != nil || outcome != PersistentVolumeClaimOutcomeExisting {
		t.Fatalf("ensurePersistentVolumeClaim() outcome = %q, error = %v", outcome, err)
	}
}

func pvcTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

package evidencestore

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

const (
	testSystemName        = "research"
	testEvidenceStoreName = "research-evidence-store"
)

func TestOptionsDefaultAndValidate(t *testing.T) {
	options := Options{Namespace: " chill-system "}
	if err := options.DefaultAndValidate(); err != nil {
		t.Fatal(err)
	}

	if options.SystemName != "chill" || options.Namespace != "chill-system" {
		t.Fatalf("identity options = %#v", options)
	}
	if options.ConfigMapKey != DefaultConfigKey {
		t.Fatalf("config options = %#v", options)
	}
}

func TestOptionsRequireNamespace(t *testing.T) {
	options := Options{}
	if err := options.DefaultAndValidate(); err == nil {
		t.Fatal("DefaultAndValidate() error = nil, want namespace error")
	}
}

func TestResolveInstanceUsesDesiredState(t *testing.T) {
	system := &edgev1alpha1.ChillSystem{}
	system.Name = testSystemName
	system.Spec.ManagementNamespace = " evidence-system "
	system.Spec.EvidenceStore.ConfigRef = corev1.LocalObjectReference{Name: " custom-store-config "}

	instance := resolveInstance(system, Options{Namespace: "operator-system"})
	if instance.SystemName != testSystemName || instance.Namespace != "evidence-system" {
		t.Fatalf("instance identity = %#v", instance)
	}
	if instance.ConfigMapName != "custom-store-config" || instance.ConfigMapKey != DefaultConfigKey {
		t.Fatalf("config identity = %#v", instance)
	}
	if instance.DeploymentName != testEvidenceStoreName || instance.ServiceName != testEvidenceStoreName {
		t.Fatalf("workload identity = %#v", instance)
	}
	if instance.CredentialsSecretName != "research-evidence-store-credentials" ||
		instance.PersistentVolumeClaimName != testEvidenceStoreName ||
		instance.BootstrapJobName != "research-evidence-store-bootstrap" {
		t.Fatalf("operand identity = %#v", instance)
	}
}

func TestResolveInstanceUsesSystemScopedNames(t *testing.T) {
	system := &edgev1alpha1.ChillSystem{}
	system.Name = testSystemName

	instance := resolveInstance(system, Options{Namespace: "chill-system", ConfigMapKey: "store.yaml"})
	if instance.DeploymentName != testEvidenceStoreName || instance.ServiceName != testEvidenceStoreName ||
		instance.ConfigMapName != "research-evidence-store-config" ||
		instance.ConfigMapKey != "store.yaml" ||
		instance.CredentialsSecretName != "research-evidence-store-credentials" ||
		instance.PersistentVolumeClaimName != testEvidenceStoreName ||
		instance.BootstrapJobName != "research-evidence-store-bootstrap" {
		t.Fatalf("resolved instance = %#v", instance)
	}
}

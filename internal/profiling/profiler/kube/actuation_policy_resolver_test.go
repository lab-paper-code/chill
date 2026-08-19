package kube

import (
	"context"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	"github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

func TestKubernetesActuationPolicyResolverFreezesSelectedConfigMapPolicy(t *testing.T) {
	raw := controllerActuationPolicyCatalog(t)
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testSystemNamespace, Name: "chill-profiler-facts",
			UID: testFactsUID, ResourceVersion: "42",
		},
		Data: map[string]string{"actuation-policies.yaml": string(raw)},
	}
	resolver, err := NewKubernetesActuationPolicyResolver(
		fake.NewClientBuilder().WithObjects(configMap).Build(),
		ActuationPolicyResolverOptions{
			Namespace: testSystemNamespace, ConfigMapName: "chill-profiler-facts",
			ConfigMapKey: "actuation-policies.yaml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := resolver.ResolveActuationPolicy(context.Background(), controllerActuationSelection())
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Policy.Name != "xavier-nvpmodel-v1" || frozen.Source.UID != testFactsUID ||
		frozen.Source.ResourceVersion != "42" {
		t.Fatalf("resolved actuation policy = %#v", frozen)
	}
}

func TestKubernetesActuationPolicyResolverFailsClosed(t *testing.T) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testSystemNamespace, Name: "facts", UID: testFactsUID, ResourceVersion: "1",
		},
		BinaryData: map[string][]byte{"actuation-policies.yaml": controllerActuationPolicyCatalog(t)},
	}
	resolver, err := NewKubernetesActuationPolicyResolver(
		fake.NewClientBuilder().WithObjects(configMap).Build(),
		ActuationPolicyResolverOptions{
			Namespace: testSystemNamespace, ConfigMapName: "facts", ConfigMapKey: "actuation-policies.yaml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.ResolveActuationPolicy(context.Background(), controllerActuationSelection())
	if err == nil || !strings.Contains(err.Error(), "text data") {
		t.Fatalf("binary actuation policy error = %v", err)
	}
	if reason, terminal := profiler.TerminalFreezeReason(err); !terminal || reason != profiler.FreezeReasonActuationPolicyInvalid {
		t.Fatalf("binary actuation policy classification = %q, terminal=%t", reason, terminal)
	}

	malformed := configMap.DeepCopy()
	malformed.BinaryData = nil
	malformed.Data = map[string]string{"actuation-policies.yaml": "policies: ["}
	resolver, err = NewKubernetesActuationPolicyResolver(
		fake.NewClientBuilder().WithObjects(malformed).Build(),
		ActuationPolicyResolverOptions{
			Namespace: testSystemNamespace, ConfigMapName: "facts", ConfigMapKey: "actuation-policies.yaml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveActuationPolicy(context.Background(), controllerActuationSelection()); err == nil {
		t.Fatal("malformed actuation-policy catalog was accepted")
	} else if reason, terminal := profiler.TerminalFreezeReason(err); !terminal || reason != profiler.FreezeReasonActuationPolicyInvalid {
		t.Fatalf("malformed actuation-policy classification = %q, terminal=%t", reason, terminal)
	}
}

func controllerActuationSelection() profiler.ActuationSelection {
	return profiler.ActuationSelection{
		DeviceClass: "jetson-xavier-nx-8g", PowerMode: "MODE_15W_6CORE",
		Actuator: profilingrun.ActuatorTarget{
			Kind:           profilingrun.ActuatorKindJetsonNVPModel,
			JetsonNVPModel: &profilingrun.JetsonNVPModelTarget{ID: 2},
		},
	}
}

func controllerActuationPolicyCatalog(t *testing.T) []byte {
	t.Helper()
	raw, err := yaml.Marshal(profiler.ActuationPolicyCatalog{
		SchemaVersion: profiler.ActuationPolicyCatalogSchemaVersion,
		Policies: []profiler.ActuationPolicySpec{{
			Name: "xavier-nvpmodel-v1", DeviceClass: "jetson-xavier-nx-8g",
			PowerMode: "MODE_15W_6CORE", ActuatorKind: profilingrun.ActuatorKindJetsonNVPModel,
			SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

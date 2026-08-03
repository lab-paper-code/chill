package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/profiler"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

func TestKubernetesExecutionPolicyResolverFreezesSelectedConfigMapPolicy(t *testing.T) {
	intent := currentPreparedRun(t).Intent
	raw := controllerExecutionPolicyCatalog(t)
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       testSystemNamespace,
			Name:            "chill-profiler-facts",
			UID:             testFactsUID,
			ResourceVersion: "42",
		},
		Data: map[string]string{"execution-policies.yaml": string(raw)},
	}
	resolver, err := NewKubernetesExecutionPolicyResolver(
		fake.NewClientBuilder().WithObjects(configMap).Build(),
		ExecutionPolicyResolverOptions{
			Namespace:     testSystemNamespace,
			ConfigMapName: "chill-profiler-facts",
			ConfigMapKey:  "execution-policies.yaml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := resolver.Resolve(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Policy.Name != "configured-active" ||
		frozen.Source.UID != testFactsUID || frozen.Source.ResourceVersion != "42" {
		t.Fatalf("resolved execution policy = %#v", frozen)
	}
}

func TestKubernetesExecutionPolicyResolverFailsClosedForBinaryData(t *testing.T) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       testSystemNamespace,
			Name:            "facts",
			UID:             testFactsUID,
			ResourceVersion: "1",
		},
		BinaryData: map[string][]byte{"execution-policies.yaml": controllerExecutionPolicyCatalog(t)},
	}
	resolver, err := NewKubernetesExecutionPolicyResolver(
		fake.NewClientBuilder().WithObjects(configMap).Build(),
		ExecutionPolicyResolverOptions{
			Namespace:     testSystemNamespace,
			ConfigMapName: "facts",
			ConfigMapKey:  "execution-policies.yaml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), currentPreparedRun(t).Intent)
	if err == nil || !strings.Contains(err.Error(), "text data") {
		t.Fatalf("binary execution policy error = %v", err)
	}
	if reason, terminal := terminalFreezeReason(err); !terminal || reason != freezeReasonExecutionPolicyInvalid {
		t.Fatalf("binary execution policy classification = %q, terminal=%t", reason, terminal)
	}

	malformedConfigMap := configMap.DeepCopy()
	malformedConfigMap.BinaryData = nil
	malformedConfigMap.Data = map[string]string{"execution-policies.yaml": "policies: ["}
	malformed, err := NewKubernetesExecutionPolicyResolver(
		fake.NewClientBuilder().WithObjects(malformedConfigMap).Build(),
		ExecutionPolicyResolverOptions{
			Namespace: testSystemNamespace, ConfigMapName: "facts", ConfigMapKey: "execution-policies.yaml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := malformed.Resolve(context.Background(), currentPreparedRun(t).Intent); err == nil {
		t.Fatal("malformed execution-policy catalog was accepted")
	} else if reason, terminal := terminalFreezeReason(err); !terminal || reason != freezeReasonExecutionPolicyInvalid {
		t.Fatalf("malformed execution-policy classification = %q, terminal=%t", reason, terminal)
	}
}

func controllerExecutionPolicyCatalog(t *testing.T) []byte {
	t.Helper()
	intent := currentPreparedRun(t).Intent
	legacy, err := profiler.LegacyFrozenExecutionPolicy(intent)
	if err != nil {
		t.Fatal(err)
	}
	active := legacy.Policy.ExecutionPolicySpec
	active.Name = "configured-active"
	idle := active
	idle.Name = "configured-idle"
	idle.WorkloadMode = "idle-model-loaded"
	raw, err := yaml.Marshal(profiler.ExecutionPolicyCatalog{
		SchemaVersion: profiler.ExecutionPolicyCatalogSchemaVersion,
		Policies:      []profiler.ExecutionPolicySpec{active, idle},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/profiler"
	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

const (
	testConfiguredActiveExecutionPolicy = "configured-active"
	testConfiguredIdleExecutionPolicy   = "configured-idle"
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
	if frozen.Policy.Name != testConfiguredActiveExecutionPolicy ||
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
	active := controllerExecutionPolicySpec(profilingrun.SaturatedSerialFixedBatchWorkloadMode)
	active.Name = testConfiguredActiveExecutionPolicy
	idle := active
	idle.Name = testConfiguredIdleExecutionPolicy
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

func controllerExecutionPolicySpec(workloadMode string) profiler.ExecutionPolicySpec {
	const mebibyte = int64(1024 * 1024)
	return profiler.ExecutionPolicySpec{
		Name: "test-policy", DeviceClass: "*", ExecutionPath: "ort-cpu", WorkloadMode: workloadMode,
		ActiveDeadlineOverheadSeconds: 180, AttemptTimeoutOverheadSeconds: 150,
		ObserverReadyTimeoutSeconds: 30, ObserverStartOverheadSeconds: 120,
		MetricResultOverheadSeconds: 30, WorkloadEndFenceTimeoutSeconds: 15,
		WorkloadResultTimeoutSeconds: 30,
		MaxLatencySamples:            1_000_000, MaxInputBytes: 64 * mebibyte,
		MaxRuntimeResultBytes: 32 * mebibyte, MaxMetricResultBytes: 8 * mebibyte,
		MaxMetricEvents: 50_000, MaxWorkloadEvents: 10_000,
		MaxWorkloadResultBytes:    8 * mebibyte,
		RuntimeMemoryRequestBytes: 128 * mebibyte, RuntimeMemoryLimitBytes: 1024 * mebibyte,
		ObserverCPURequest: "5m", ObserverCPULimit: "100m",
		ObserverMemoryRequestBytes: 16 * mebibyte, ObserverMemoryLimitBytes: 64 * mebibyte,
		WorkloadObserverCPURequest: "5m", WorkloadObserverCPULimit: "100m",
		WorkloadObserverMemoryRequestBytes: 32 * mebibyte,
		WorkloadObserverMemoryLimitBytes:   128 * mebibyte,
		CoordinationReserveBytes:           1 * mebibyte, WorkspaceBytes: 128 * mebibyte,
	}
}

func configuredAdmissionPolicy(
	t *testing.T,
	name string,
	workloadMode string,
) profileradmission.FrozenPolicy {
	t.Helper()
	raw, err := yaml.Marshal(profileradmission.PolicyCatalog{
		SchemaVersion: profileradmission.PolicyCatalogSchemaVersion,
		Policies: []profileradmission.PolicySpec{{
			Name: name, WorkloadMode: workloadMode, MinimumSamples: 20,
			MaximumGapSeconds: 1.5, MaximumBoundaryDistanceSeconds: 1,
			MinimumCoverage: 0.95, AllowReceiptTimestamps: true,
			RejectConcurrentCHILLWorkloads: true, RejectExternalWorkloadChanges: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := profileradmission.FreezeConfiguredPolicy(raw, name, profileradmission.PolicySource{
		Kind: profileradmission.ConfigMapPolicySourceKind, Namespace: testSystemNamespace,
		Name: "chill-profiler-facts", UID: testFactsUID, ResourceVersion: "42",
		Key: "admission-policies.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}

package kube

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

const (
	testConfiguredActiveExecutionPolicy = "configured-active"
	testConfiguredIdleExecutionPolicy   = "configured-idle"
)

func TestKubernetesExecutionPolicyResolverFreezesSelectedConfigMapPolicy(t *testing.T) {
	config := currentRunConfig(t)
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
		fake.NewClientBuilder().WithObjects(configMap, executionPolicyNode(config, "4Gi")).Build(),
		ExecutionPolicyResolverOptions{
			Namespace:                 testSystemNamespace,
			ConfigMapName:             "chill-profiler-facts",
			ConfigMapKey:              "execution-policies.yaml",
			MeasurementMemoryFraction: 0.8,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := resolver.Resolve(context.Background(), config)
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
			Namespace:                 testSystemNamespace,
			ConfigMapName:             "facts",
			ConfigMapKey:              "execution-policies.yaml",
			MeasurementMemoryFraction: 0.8,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), currentRunConfig(t))
	if err == nil || !strings.Contains(err.Error(), "text data") {
		t.Fatalf("binary execution policy error = %v", err)
	}
	if reason, terminal := profiler.TerminalResolutionReason(err); !terminal || reason != profiler.ResolutionReasonExecutionPolicyInvalid {
		t.Fatalf("binary execution policy classification = %q, terminal=%t", reason, terminal)
	}

	malformedConfigMap := configMap.DeepCopy()
	malformedConfigMap.BinaryData = nil
	malformedConfigMap.Data = map[string]string{"execution-policies.yaml": "policies: ["}
	malformed, err := NewKubernetesExecutionPolicyResolver(
		fake.NewClientBuilder().WithObjects(malformedConfigMap).Build(),
		ExecutionPolicyResolverOptions{
			Namespace: testSystemNamespace, ConfigMapName: "facts", ConfigMapKey: "execution-policies.yaml",
			MeasurementMemoryFraction: 0.8,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := malformed.Resolve(context.Background(), currentRunConfig(t)); err == nil {
		t.Fatal("malformed execution-policy catalog was accepted")
	} else if reason, terminal := profiler.TerminalResolutionReason(err); !terminal || reason != profiler.ResolutionReasonExecutionPolicyInvalid {
		t.Fatalf("malformed execution-policy classification = %q, terminal=%t", reason, terminal)
	}
}

func TestExecutionPolicyResolverFreezesNodeCappedMemoryBeforePreparation(t *testing.T) {
	for _, fraction := range []float64{0.8, 0.5} {
		for _, policyLimit := range []int64{16 << 30, 1 << 30} {
			t.Run(fmt.Sprintf("%g/%d", fraction, policyLimit), func(t *testing.T) {
				config := currentRunConfig(t)
				policy := controllerExecutionPolicySpec(config.Measurement.WorkloadMode)
				policy.RuntimeMemoryLimitBytes = policyLimit
				raw, err := yaml.Marshal(profiler.ExecutionPolicyCatalog{
					SchemaVersion: profiler.ExecutionPolicyCatalogSchemaVersion, Policies: []profiler.ExecutionPolicySpec{policy},
				})
				if err != nil {
					t.Fatal(err)
				}
				configMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Namespace: testSystemNamespace, Name: "facts", UID: testFactsUID, ResourceVersion: "42"},
					Data:       map[string]string{"policy": string(raw)},
				}
				resolver, err := NewKubernetesExecutionPolicyResolver(
					fake.NewClientBuilder().WithObjects(configMap, executionPolicyNode(config, "4Gi")).Build(),
					ExecutionPolicyResolverOptions{Namespace: testSystemNamespace, ConfigMapName: "facts", ConfigMapKey: "policy", MeasurementMemoryFraction: fraction},
				)
				if err != nil {
					t.Fatal(err)
				}
				frozen, err := resolver.Resolve(context.Background(), config)
				if err != nil {
					t.Fatal(err)
				}
				want := min(policyLimit, int64(float64(int64(4<<30))*fraction))
				if frozen.Policy.RuntimeMemoryLimitBytes != want || frozen.Policy.RuntimeMemoryRequestBytes != policy.RuntimeMemoryRequestBytes {
					t.Fatalf("selected actor envelope = %d/%d, want %d/%d", frozen.Policy.RuntimeMemoryRequestBytes, frozen.Policy.RuntimeMemoryLimitBytes, policy.RuntimeMemoryRequestBytes, want)
				}
			})
		}
	}
}

func executionPolicyNode(config profilingrun.RunConfig, memory string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: config.TargetNode.Name, UID: types.UID(config.TargetNode.UID)},
		Status:     corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(memory)}},
	}
}

func controllerExecutionPolicyCatalog(t *testing.T) []byte {
	t.Helper()
	active := controllerExecutionPolicySpec(profilingrun.SaturatedSerialFixedBatchWorkloadMode)
	active.Name = testConfiguredActiveExecutionPolicy
	idle := active
	idle.Name = testConfiguredIdleExecutionPolicy
	idle.WorkloadMode = "idle-model-loaded"
	rknnActive := active
	rknnActive.Name = "test-rknn-active"
	rknnActive.ExecutionPath = profilingrun.RKNNLiteExecutionPath
	rknnIdle := idle
	rknnIdle.Name = "test-rknn-idle"
	rknnIdle.ExecutionPath = profilingrun.RKNNLiteExecutionPath
	raw, err := yaml.Marshal(profiler.ExecutionPolicyCatalog{
		SchemaVersion: profiler.ExecutionPolicyCatalogSchemaVersion,
		Policies:      []profiler.ExecutionPolicySpec{active, idle, rknnActive, rknnIdle},
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
		MaxWorkloadEvents:         10_000,
		MaxWorkloadResultBytes:    8 * mebibyte,
		RuntimeMemoryRequestBytes: 128 * mebibyte, RuntimeMemoryLimitBytes: 1024 * mebibyte,
		ObserverMemoryRequestBytes: 16 * mebibyte, ObserverMemoryLimitBytes: 64 * mebibyte,
		WorkloadObserverMemoryRequestBytes: 32 * mebibyte,
		WorkloadObserverMemoryLimitBytes:   128 * mebibyte,
		CoordinationReserveBytes:           1 * mebibyte, WorkspaceBytes: 128 * mebibyte,
	}
}

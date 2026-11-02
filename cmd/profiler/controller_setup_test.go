package main

import (
	"encoding/json"
	"flag"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	transitioncontroller "github.com/lab-paper-code/chill/internal/profiling/transitioncost/controller"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestControllerSetupTransitionJobUsesRuntimeMemoryWithoutNewSettings(t *testing.T) {
	// Exercise the same flag-to-options projection used by controller_setup, not
	// the controller unit fixture (which supplies an explicit memory limit).
	flags := flag.NewFlagSet("profiler", flag.ContinueOnError)
	opts := options{}
	opts.bindFlags(flags)
	if err := flags.Parse([]string{"--transition-observer-image=registry/observer:test",
		"--transition-observer-image-pull-policy=IfNotPresent", "--transition-observer-service-account=observer",
		"--transition-maximum-object-bytes=67108864",
		"--transition-observer-active-deadline-overhead-seconds=180",
	}); err != nil {
		t.Fatal(err)
	}
	delivery := transitionAttemptJobOptions(opts)
	if _, explicit := delivery.Resources.Limits[corev1.ResourceMemory]; explicit {
		t.Fatal("production wiring unexpectedly invents a memory limit")
	}
	measurement := &edge.PowerModeTransitionCost{
		ObjectMeta: metav1.ObjectMeta{Namespace: "measure", Name: "cost", UID: "cost-uid"},
		Spec:       edge.PowerModeTransitionCostSpec{TargetNode: "target"},
	}
	directions := make([]transitioncontroller.PreparedDirection, 2)
	for i := range directions {
		directions[i].FrozenContract.SourceState = transitioncost.StateIdentity{
			DeviceType: "device", Model: "model", PowerMode: "low",
		}
		directions[i].FrozenContract.TargetState = transitioncost.StateIdentity{
			DeviceType: "device", Model: "model", PowerMode: "high",
		}
		directions[i].Actuator = edge.PowerModeActuator{
			Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: int32(i)},
		}
	}
	prepared := transitioncontroller.PreparedMeasurement{
		Namespace: "measure", TargetNode: edge.ProfilingRunObjectReference{Name: "target", UID: "target-uid"},
		ObserverNodeName: "observer", EvidenceStore: "evidence",
		PowerSource: metricobserver.SourceIdentity{
			Kind: "edge-metrics", Endpoint: "http://meter/prometheus", Metric: "power_watts",
			MetricLabels: map[string]string{"hostname": "target", "device_type": "device"},
		},
		PowerRequest: metricobserver.Request{
			Interval: 500 * time.Millisecond, Duration: 960*time.Second + 500*time.Millisecond,
			RequestTimeout: 100 * time.Millisecond,
		},
		Directions: directions, FactPollInterval: 200 * time.Millisecond, FactObservationTimeout: 960 * time.Second,
	}
	scheme := runtime.NewScheme()
	if err := edge.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	job, err := transitioncontroller.BuildAttemptJob(transitioncontroller.AttemptJobRequest{
		Measurement: measurement, Prepared: prepared, DirectionIndex: 1, Name: "transition-1",
		RawLocation: evidencestore.ObjectLocation{Store: "evidence", Key: "transition/raw.json"},
		StoreBinding: storekube.Binding{
			Store: "evidence", Endpoint: "http://store", Bucket: "bucket", Region: "region",
			CredentialsSecretName: "credentials",
		},
	}, delivery, scheme)
	if err != nil {
		t.Fatalf("production schedule rejected before runtime memory is available: %v", err)
	}
	var resultLimit int64
	foundMemory := false
	for _, env := range job.Spec.Template.Spec.Containers[0].Env {
		switch env.Name {
		case "CHILL_OBSERVER_MEMORY_LIMIT_BYTES":
			if env.ValueFrom == nil || env.ValueFrom.ResourceFieldRef == nil {
				t.Fatal("runtime memory is not a downward API field")
			}
			ref := env.ValueFrom.ResourceFieldRef
			if ref.Resource != "limits.memory" ||
				ref.ContainerName != job.Spec.Template.Spec.Containers[0].Name || ref.Divisor.Value() != 1 {
				t.Fatalf("memory selector=%+v", ref)
			}
			foundMemory = true
		case "CHILL_TRANSITION_ATTEMPT_JSON":
			var wire struct {
				MaxResultBytes int64 `json:"maxResultBytes"`
			}
			if err := json.Unmarshal([]byte(env.Value), &wire); err != nil {
				t.Fatal(err)
			}
			resultLimit = wire.MaxResultBytes
		}
	}
	if !foundMemory || resultLimit != opts.transitionMaximumObjectBytes {
		t.Fatal("existing resource envelope was not delivered")
	}
	// Kubelet supplies node allocatable when no container memory limit is set.
	nodeAllocatableBytes := int64(1 << 30)
	capacity, err := metricobserver.RequiredCapacity(
		prepared.PowerRequest, prepared.PowerSource,
		metricobserver.Limits{MaxResultBytes: resultLimit, MemoryBudgetBytes: nodeAllocatableBytes}, false,
	)
	if err != nil {
		t.Fatalf("normal production schedule refused: %v", err)
	}
	if capacity.ResultBytes > resultLimit || capacity.MemoryBytes > nodeAllocatableBytes {
		t.Fatal("schedule exceeds the delivered byte envelope")
	}
}

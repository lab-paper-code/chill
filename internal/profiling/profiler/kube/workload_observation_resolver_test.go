package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMeasurementWorkloadFreezesActualSLOAndRejectsDifferentInputs(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	workload := &edgev1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "service", UID: "workload-uid", ResourceVersion: "7"},
		Spec: edgev1alpha1.WorkloadSpec{
			ModelRef:        edgev1alpha1.CatalogReference{Name: "model"},
			PreprocessorRef: edgev1alpha1.CatalogReference{Name: "preprocessor"},
			DatasetRef:      &edgev1alpha1.CatalogReference{Name: "data"},
			SLO:             edgev1alpha1.WorkloadSLO{Percentile: 99, TargetLatency: metav1.Duration{Duration: time.Second}},
		},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workload).Build()
	resolver := &KubernetesWorkloadObservationResolver{reader: reader}
	selection := profiler.CandidateSelection{ModelSpec: "model", Preprocessor: "preprocessor", Dataset: "data"}
	stop, err := resolver.ResolveMeasurementWorkload(context.Background(), "service", selection)
	// p99 needs ceil(ln(0.01)/ln(0.99)) = 459 known-gap records.
	if err != nil || stop.MinServiceSamples != 459 || stop.SLOPercentile != 99 ||
		stop.WorkloadUID != "workload-uid" || stop.WorkloadResourceVersion != "7" ||
		stop.SLOTargetLatencyNanos != int64(time.Second) {
		t.Fatalf("actual Workload stop policy=%+v err=%v", stop, err)
	}
	for _, mismatch := range []profiler.CandidateSelection{
		{ModelSpec: "other", Preprocessor: "preprocessor", Dataset: "data"},
		{ModelSpec: "model", Preprocessor: "other", Dataset: "data"},
		{ModelSpec: "model", Preprocessor: "preprocessor", Dataset: "other"},
	} {
		if _, err := resolver.ResolveMeasurementWorkload(context.Background(), "service", mismatch); err == nil {
			t.Fatalf("accepted mismatched Workload selection %+v", mismatch)
		}
	}
}

func TestKubernetesWorkloadObservationResolverReturnsValidatedInstallationFacts(t *testing.T) {
	config := workloadObservationConfigMap(validWorkloadObservationFacts())
	resolver := newTestWorkloadObservationResolver(t, config)

	resolved, err := resolver.ResolveWorkloadObservation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Image != testWorkloadObserverImage {
		t.Fatalf("resolved workload observation = %#v", resolved)
	}
}

func TestKubernetesWorkloadObservationResolverRejectsUntrustedFacts(t *testing.T) {
	tests := []struct {
		name  string
		facts string
		match string
	}{
		{name: "missing image", facts: strings.Replace(
			validWorkloadObservationFacts(), testWorkloadObserverImage, "", 1,
		), match: "image is required"},
		{name: "unknown field", facts: validWorkloadObservationFacts() + "unknown: true\n", match: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := newTestWorkloadObservationResolver(t, workloadObservationConfigMap(test.facts))
			_, err := resolver.ResolveWorkloadObservation(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("ResolveWorkloadObservation() error = %v", err)
			}
		})
	}
}

func newTestWorkloadObservationResolver(
	t *testing.T,
	config *corev1.ConfigMap,
) *KubernetesWorkloadObservationResolver {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config).Build()
	resolver, err := NewKubernetesWorkloadObservationResolver(reader, WorkloadObservationResolverOptions{
		Namespace: "chill-system", ConfigMapName: "chill-profiler-facts", ConfigMapKey: "workload-observation.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func workloadObservationConfigMap(facts string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: "chill-profiler-facts",
			UID: types.UID("facts-uid"), ResourceVersion: "10",
		},
		Data: map[string]string{"workload-observation.yaml": facts},
	}
}

const testWorkloadObserverImage = "registry.example/workload-observer@sha256:" +
	"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func validWorkloadObservationFacts() string {
	return `schemaVersion: chill.dacs.io/profiler-workload-observation.v1alpha2
image: ` + testWorkloadObserverImage + `
`
}

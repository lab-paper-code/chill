package main

import (
	"context"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilerapp "github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type prepareCandidates struct{}

func (prepareCandidates) ResolveCandidate(context.Context, profilerapp.CandidateSelection) (profilerapp.ResolvedCandidate, error) {
	return profilerapp.ResolvedCandidate{
		State:   profilingrun.ExecutionState{Firstfloor: &modelcompat.FirstfloorSettings{MaxBatchSize: 1}, Preprocessor: profilingrun.PreprocessorState{Architecture: "amd64"}},
		Dataset: &profilingrun.DatasetContract{Name: "data"},
	}, nil
}

type prepareNodes struct{}

func (prepareNodes) ResolveNode(context.Context, profilerapp.NodeSelection) (profilingrun.NodeSnapshot, error) {
	return profilingrun.NodeSnapshot{Name: "node", Architecture: "amd64"}, nil
}

type prepareExecution struct{}

func (prepareExecution) Resolve(context.Context, profilingrun.RunConfig) (profilerapp.FrozenExecutionPolicy, error) {
	return profilerapp.FrozenExecutionPolicy{}, nil
}

// The session sends the deadline serving gives the measured Workload's
// requests: its lifetime when set, otherwise the server's request timeout.
func TestSessionPrepareRequestLifetimeIsTheServingDeadline(t *testing.T) {
	for _, test := range []struct {
		lifetime *meta.Duration
		want     time.Duration
	}{
		{&meta.Duration{Duration: 7 * time.Second}, 7 * time.Second},
		{nil, 30 * time.Second},
	} {
		scheme := runtime.NewScheme()
		if err := edge.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		workload := &edge.Workload{ObjectMeta: meta.ObjectMeta{Name: "service", UID: "workload-uid"}, Spec: edge.WorkloadSpec{
			ModelRef: edge.CatalogReference{Name: "model"}, PreprocessorRef: edge.CatalogReference{Name: "pre"},
			DatasetRef: &edge.CatalogReference{Name: "data"}, RequestLifetime: test.lifetime,
			SLO: edge.WorkloadSLO{Percentile: 95, TargetLatency: meta.Duration{Duration: time.Second}},
		}}
		workloads, err := profilerkube.NewKubernetesSessionWorkloadResolver(fake.NewClientBuilder().WithScheme(scheme).WithObjects(workload).Build())
		if err != nil {
			t.Fatal(err)
		}
		preparer, err := profilerapp.NewSessionPreparer(prepareCandidates{}, prepareNodes{}, workloads, prepareExecution{})
		if err != nil {
			t.Fatal(err)
		}
		beo := &edge.BatchEnergyOptimization{Spec: edge.BatchEnergyOptimizationSpec{
			State:          edge.MeasurementStateSpec{Candidate: edge.ProfilingRunCandidateSpec{ModelSpec: "model", Preprocessor: "pre"}, TargetNode: "node"},
			ActiveProtocol: edge.ActiveProtocolSpec{Workload: "service", Dataset: "data"},
		}}
		preparation, err := sessionPrepare(preparer, 30*time.Second)(context.Background(), beo)
		if err != nil || preparation.RequestLifetime != test.want {
			t.Fatalf("Workload lifetime %v: request lifetime %s, want %s (%v)", test.lifetime, preparation.RequestLifetime, test.want, err)
		}
	}
}

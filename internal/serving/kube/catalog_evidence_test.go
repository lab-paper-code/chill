package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	campaigncontroller "github.com/lab-paper-code/chill/internal/profiling/campaign/controller"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilerprojection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	"github.com/lab-paper-code/chill/test/fixtures"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Reuse the real retained-evidence fixture: the first reference must finish
// verification successfully before the second reference's UID failure wins.
func TestCampaignReaderTrialIdentityPrecedesRepeatedRunName(t *testing.T) {
	catalog, source, _, _ := catalogEvidenceFixture(t, 4096)
	ctx := context.Background()
	cr := &edgev1alpha1.BatchEnergyOptimization{}
	if err := catalog.store.client.Get(ctx, source, cr); err != nil {
		t.Fatal(err)
	}
	retained, err := catalog.evidence.Read(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	spec := retained.Result.Spec()
	first := spec.CompletedTrials[0]
	spec.CompletedTrials = []batchenergyoptimization.CompletedTrialReference{first}
	retained.Result, err = batchenergyoptimization.NewResultRecord(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.evidence.ReadTrials(ctx, cr, retained); err != nil {
		t.Fatalf("first current Run reference did not verify: %v", err)
	}
	stale := first
	stale.Run.UID = "other-run-uid"
	spec.CompletedTrials = append(spec.CompletedTrials, stale)
	retained.Result, err = batchenergyoptimization.NewResultRecord(spec)
	if err != nil {
		t.Fatal(err)
	}
	if retained.Result.Spec().CompletedTrials[0].Run.UID != first.Run.UID {
		t.Fatal("test requires the current Run reference first")
	}
	trials, err := catalog.evidence.ReadTrials(ctx, cr, retained)
	want := fmt.Sprintf("completed Run %s/%s: Run identity or campaign ownership mismatch", first.Run.Namespace, first.Run.Name)
	if err == nil || err.Error() != want {
		t.Fatalf("repeated name masked the second reference's UID failure: %v", err)
	}
	if len(trials.Active) != 0 || len(trials.Idle) != 0 || len(trials.Evidence) != 0 {
		t.Fatalf("invalid second reference returned partial trials: %+v", trials)
	}
}

func TestCatalogFollowsFrozenPlanAndRawTrials(t *testing.T) {
	for _, memory := range []uint64{4096, 0} {
		t.Run(fmt.Sprint(memory), func(t *testing.T) {
			catalog, source, _, _ := catalogEvidenceFixture(t, memory)
			view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
			if err != nil {
				t.Fatal(err)
			}
			if memory == 0 {
				if len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 || view.Insufficiencies[0].Code != invalidEvidenceCode {
					t.Fatalf("absent required native memory became eligible: %+v", view)
				}
				return
			}
			if len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
				t.Fatalf("same-firstfloor measurements unavailable: %+v", view)
			}
			assertCatalogRawProfile(t, view.Profiles[0])
		})
	}
}

func TestCatalogIdleTrialsKeepPhysicalBindingWithoutHistoricalSourceRebinding(t *testing.T) {
	for _, foreignNode := range []bool{false, true} {
		t.Run(fmt.Sprintf("foreign-node-%t", foreignNode), func(t *testing.T) {
			catalog, source, objects, _ := catalogEvidenceFixture(t, 4096)
			ctx := context.Background()
			campaign := &edgev1alpha1.BatchEnergyOptimization{}
			if err := catalog.store.client.Get(ctx, source, campaign); err != nil {
				t.Fatal(err)
			}
			retained, err := catalog.evidence.Read(ctx, campaign)
			if err != nil {
				t.Fatal(err)
			}
			spec := retained.Result.Spec()
			reference := &spec.CompletedTrials[4]
			location := storedObjectIdentity(reference.Evidence).Location()
			var execution profilerevidence.ExecutionEvidence
			if err := json.Unmarshal(objects[location], &execution); err != nil {
				t.Fatal(err)
			}
			var raw profilerevidence.IdleRawMeasurement
			if err := json.Unmarshal(objects[execution.RawMeasurement.Location()], &raw); err != nil {
				t.Fatal(err)
			}
			raw.RunConfig.Metrics.Endpoint = "http://historical-meter/metrics"
			raw.Metrics.Source.Endpoint = raw.RunConfig.Metrics.Endpoint
			if foreignNode {
				const otherNodeUID = "other-physical-node"
				raw.RunConfig.TargetNode.UID = otherNodeUID
				raw.Runtime.DerivedContract.NodeSnapshot.UID = otherNodeUID
				raw.Workload.Target.NodeUID = otherNodeUID
				execution.Terminal.Node.UID = otherNodeUID
			}
			rawBytes, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			objects[execution.RawMeasurement.Location()] = rawBytes
			execution.RawMeasurement.Size = int64(len(rawBytes))
			execution.RunConfig = raw.RunConfig
			executionBytes, err := json.Marshal(execution)
			if err != nil {
				t.Fatal(err)
			}
			objects[location] = executionBytes
			reference.Evidence.Size = int64(len(executionBytes))
			run := &edgev1alpha1.ProfilingRun{}
			key := types.NamespacedName{Namespace: reference.Run.Namespace, Name: reference.Run.Name}
			if err := catalog.store.client.Get(ctx, key, run); err != nil {
				t.Fatal(err)
			}
			run.Status.Evidence.Size = reference.Evidence.Size
			run.Status.ObservedExecution.Node.UID = execution.Terminal.Node.UID
			if err := catalog.store.client.Update(ctx, run); err != nil {
				t.Fatal(err)
			}
			result, err := batchenergyoptimization.NewResultRecord(spec)
			if err != nil {
				t.Fatal(err)
			}
			resultBytes, err := result.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			objects[apiObjectIdentity(campaign.Status.Result).Location()] = resultBytes
			campaign.Status.Result.Size = int64(len(resultBytes))
			if err := catalog.store.client.Status().Update(ctx, campaign); err != nil {
				t.Fatal(err)
			}
			view, err := catalog.Refresh(ctx, []string{source.Namespace})
			if err != nil {
				t.Fatal(err)
			}
			if foreignNode {
				if len(view.Profiles) != 0 || len(view.Insufficiencies) == 0 {
					t.Fatalf("different physical idle execution entered baseline: %+v", view)
				}
			} else if len(view.Profiles) != 1 || view.Profiles[0].IdleBaselineWatts != 4 ||
				len(view.Profiles[0].Batches[0].ServiceSamples) == 0 {
				t.Fatalf("historical meter metadata hid usable idle baseline: %+v", view)
			}
		})
	}
}

func TestIdleTrialUsesVerifiedRuntimeAccessGPUWithoutActorRebinding(t *testing.T) {
	const (
		referenceGPU  = "GPU-reference"
		staleActorGPU = "GPU-stale-copy"
		otherGPU      = "GPU-other-physical"
		provider      = "CUDAExecutionProvider"
	)
	for _, differentGPU := range []bool{false, true} {
		t.Run(fmt.Sprintf("different-gpu-%t", differentGPU), func(t *testing.T) {
			configure := func(config *profilingrun.RunConfig) {
				config.State.Backend = profilingrun.FirstfloorORTCUDABackend
				config.Delivery.RuntimeAccess = &profilingrun.CandidateReportRuntimeAccess{
					GPUUUID: referenceGPU, Resources: map[string]string{"nvidia.com/gpu": "1"},
				}
				config.Actor.Backend, config.Actor.GPUUUID = config.State.Backend, staleActorGPU
				config.Actor.BuildRunnerCommand()
			}
			observe := func(_ int, active *profilingrun.RuntimeResult, idle *profilingrun.IdleRuntimeResult) {
				active.Provider, idle.Provider = provider, provider
				active.AvailableProviders, idle.AvailableProviders = []string{provider}, []string{provider}
				active.DerivedContract.Provider, idle.DerivedContract.Provider = provider, provider
				for _, native := range []*profilingrun.FirstfloorRuntimeEvidence{active.Firstfloor, idle.Firstfloor} {
					if native != nil {
						native.Runtime.Runtime = profilingrun.FirstfloorORTCUDABackend
						native.Runtime.Device, native.Runtime.GPUUUID = "cuda", referenceGPU
						native.Runtime.Providers, native.Runtime.AvailableProviders = []string{provider}, []string{provider}
					}
				}
			}
			catalog, source, objects, _ := catalogEvidenceFixtureConfigPolicy(
				t, 4096, [6]float64{6, 6, 6, 4, 4, 4}, 3, "", configure, observe,
			)
			ctx := context.Background()
			campaign := &edgev1alpha1.BatchEnergyOptimization{}
			if err := catalog.store.client.Get(ctx, source, campaign); err != nil {
				t.Fatal(err)
			}
			retained, err := catalog.evidence.Read(ctx, campaign)
			if err != nil {
				t.Fatal(err)
			}
			spec := retained.Result.Spec()
			reference := &spec.CompletedTrials[4]
			location := storedObjectIdentity(reference.Evidence).Location()
			var execution profilerevidence.ExecutionEvidence
			if err := json.Unmarshal(objects[location], &execution); err != nil {
				t.Fatal(err)
			}
			var raw profilerevidence.IdleRawMeasurement
			if err := json.Unmarshal(objects[execution.RawMeasurement.Location()], &raw); err != nil {
				t.Fatal(err)
			}
			actualGPU := referenceGPU
			if differentGPU {
				actualGPU = otherGPU
			}
			raw.RunConfig.Delivery.RuntimeAccess.GPUUUID = actualGPU
			raw.Runtime.Firstfloor.Runtime.GPUUUID = actualGPU
			raw.RunConfig.Metrics.Endpoint = "http://historical-gpu-meter/metrics"
			raw.Metrics.Source.Endpoint = raw.RunConfig.Metrics.Endpoint
			rawBytes, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			objects[execution.RawMeasurement.Location()] = rawBytes
			execution.RunConfig, execution.RawMeasurement.Size = raw.RunConfig, int64(len(rawBytes))
			executionBytes, err := json.Marshal(execution)
			if err != nil {
				t.Fatal(err)
			}
			objects[location] = executionBytes
			reference.Evidence.Size = int64(len(executionBytes))
			run := &edgev1alpha1.ProfilingRun{}
			key := types.NamespacedName{Namespace: reference.Run.Namespace, Name: reference.Run.Name}
			if err := catalog.store.client.Get(ctx, key, run); err != nil {
				t.Fatal(err)
			}
			run.Status.Evidence.Size = reference.Evidence.Size
			if err := catalog.store.client.Update(ctx, run); err != nil {
				t.Fatal(err)
			}
			retained.Result, err = batchenergyoptimization.NewResultRecord(spec)
			if err != nil {
				t.Fatal(err)
			}
			trials, err := catalog.evidence.ReadTrials(ctx, campaign, retained)
			if err != nil {
				t.Fatalf("each retained idle execution must independently pass its Loader: %v", err)
			}
			loaded, observed, err := trials.Idle[1].Records()
			if err != nil {
				t.Fatal(err)
			}
			if loaded.RunConfig.Actor.GPUUUID != staleActorGPU ||
				loaded.RunConfig.Delivery.RuntimeAccess.GPUUUID != actualGPU ||
				observed.Runtime.Firstfloor.Runtime.GPUUUID != actualGPU {
				t.Fatal("fixture did not retain stale Actor alongside Loader-bound actual GPU")
			}
			trial, err := profilerprojection.IdleTrial(trials.Idle[1], trials.Idle[0])
			if differentGPU {
				if err == nil {
					t.Fatalf("different validated physical GPU entered idle baseline: %+v", trial)
				}
			} else if err != nil || trial.ModelLoadedIdleMeanWallWatts != 4 {
				t.Fatalf("same physical GPU with historical source metadata was rejected: %+v %v", trial, err)
			}
		})
	}
}

func TestCatalogDiscoveryRetainsOnlyCompletedPrefix(t *testing.T) {
	for _, stop := range []string{batchenergyoptimization.DiscoveryStopOOM, batchenergyoptimization.DiscoveryStopMeasurementBudgetExceeded, firstfloor.FailureMemoryAllocation} {
		t.Run(stop, func(t *testing.T) {
			catalog, source, objects, _ := catalogEvidenceFixtureConfigPolicy(t, 4096, [6]float64{6, 6, 6, 4, 4, 4}, 3, "", func(config *profilingrun.RunConfig) {
				config.State.Firstfloor.MaxBatchSize = 2
				config.Actor.BatchSizes = []int{1, 2}
				config.Actor.MaxBatchSize = 2
				config.Measurement.WarmupIterations = 0
				config.Measurement.StartupWarmupIterations = 20
				config.Measurement.WarmupBatchSizes = []int{1}
				config.Actor.WarmupIterations = 20
				config.Actor.WarmupBatchSizes = []int{1}
				config.Actor.BuildRunnerCommand()
			}, nil, 1, 2)
			campaign := &edgev1alpha1.BatchEnergyOptimization{}
			ctx := context.Background()
			if err := catalog.store.client.Get(ctx, source, campaign); err != nil {
				t.Fatal(err)
			}
			retained, err := catalog.evidence.Read(ctx, campaign)
			if err != nil {
				t.Fatal(err)
			}
			campaign.Spec.ActiveProtocol.WarmupIterations = 20
			campaign.Spec.ActiveProtocol.Workload = retained.Plan.Shared().ActiveProtocol.WorkloadName
			if err := catalog.store.client.Update(ctx, campaign); err != nil {
				t.Fatal(err)
			}
			for index, reference := range retained.Result.Spec().CompletedTrials {
				built, err := campaigncontroller.BuildRun(campaign, batchenergyoptimization.Measurement(reference.Measurement), index%3+1, "ort-cpu")
				if err != nil {
					t.Fatal(err)
				}
				run := &edgev1alpha1.ProfilingRun{}
				if err := catalog.store.client.Get(ctx, types.NamespacedName{Namespace: reference.Run.Namespace, Name: reference.Run.Name}, run); err != nil {
					t.Fatal(err)
				}
				run.Spec = built.Spec
				if err := catalog.store.client.Update(ctx, run); err != nil {
					t.Fatal(err)
				}
			}
			spec := retained.Result.Spec()
			spec.Outcome = batchenergyoptimization.BatchDiscoveryComplete
			failed := batchenergyoptimization.RunReference{Namespace: source.Namespace, Name: "failed-b2", UID: "failed-b2-uid"}
			spec.ExcludedTrials = append(spec.ExcludedTrials, batchenergyoptimization.ExcludedTrialReference{
				Run: failed, Measurement: batchenergyoptimization.ResultMeasurement{WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 2}, Attempt: 1, Reasons: []string{stop},
			})
			detail := batchenergyoptimization.DiscoveryResult{
				MeasuredBatchSizes: []int{1},
				Stop: batchenergyoptimization.DiscoveryStop{
					Reason: stop, FailedMeasurement: &batchenergyoptimization.Measurement{WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 2}, Attempt: 1,
					Run: &failed,
				},
			}
			if stop == firstfloor.FailureMemoryAllocation {
				// Real error transport supplies only the failed trial. Successful
				// protocol evidence remains the existing three-trial raw fixture.
				detail = nativeAllocationDiscovery(t, campaign, retained.Result.Spec().CompletedTrials)
				excluded := &spec.ExcludedTrials[len(spec.ExcludedTrials)-1]
				excluded.Run = *detail.Stop.Run
				excluded.Reasons = []string{detail.Stop.Reason}
			}
			spec.Detail, err = json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			result, err := batchenergyoptimization.NewResultRecord(spec)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := result.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			objects[apiObjectIdentity(campaign.Status.Result).Location()] = payload
			campaign.Status.Result.Size = int64(len(payload))
			campaign.Status.Outcome = string(spec.Outcome)
			if err := catalog.store.client.Status().Update(ctx, campaign); err != nil {
				t.Fatal(err)
			}
			view, err := catalog.Refresh(ctx, []string{source.Namespace})
			if err != nil || len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
				t.Fatalf("stopped discovery lost completed prefix: %+v %v", view, err)
			}
			assertCatalogDiscoveryCompletedPrefix(t, view.Profiles[0])
			run := &edgev1alpha1.ProfilingRun{}
			if err := catalog.store.client.Get(ctx, types.NamespacedName{Namespace: source.Namespace, Name: "trial-0"}, run); err != nil {
				t.Fatal(err)
			}
			run.Spec.Measurement.WarmupIterations = 1
			if err := catalog.store.client.Update(ctx, run); err != nil {
				t.Fatal(err)
			}
			campaign.Annotations = map[string]string{"catalog-test-revision": "protocol-drift"}
			if err := catalog.store.client.Update(ctx, campaign); err != nil {
				t.Fatal(err)
			}
			view, err = catalog.Refresh(ctx, []string{source.Namespace})
			if err != nil || len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
				t.Fatalf("historical request warmup hid retained completed prefix: %+v %v", view, err)
			}
		})
	}
}

// nativeAllocationDiscovery exercises production IPC/HTTP, Python termination,
// native classification and campaign decision. Kubernetes objects are doubles;
// the allocator exception reproduces the retained ORT diagnostic without ORT.
func nativeAllocationDiscovery(t *testing.T, campaign *edgev1alpha1.BatchEnergyOptimization, completed []batchenergyoptimization.CompletedTrialReference) batchenergyoptimization.DiscoveryResult {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &firstfloor.Config{
		Model: "synthetic", Mode: "profiling", Inputs: []string{"input"}, Outputs: []string{"output"},
		RunnerCommand: []string{"python3", filepath.Join(root, "runtime/firstfloor/runner.py"), "--echo",
			"--preprocessor", filepath.Join(root, "runtime/firstfloor/testdata/echo"), "--warmup-iterations", "0"},
		BatchSizes: []int{2}, MaxBatchSize: 2, MaxQueueRequests: 8, MaxQueueBytes: 1 << 20,
		MaxRequestBytes: 1 << 20, RunnerTimeoutMS: 5000, Policy: firstfloor.Policy{DeadlinePolicy: "keep"},
	}
	if err := firstfloor.ValidateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	runner, err := firstfloor.StartRunner(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := firstfloor.NewScheduler(ctx, cfg, runner)
	t.Cleanup(func() {
		cancel()
		select {
		case <-scheduler.Done():
		case <-time.After(5 * time.Second):
			t.Error("native allocation smoke did not reap runner")
		}
	})
	server := httptest.NewServer(firstfloor.NewHandler(scheduler))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "termination.json")
	command := exec.CommandContext(ctx, "python3", "-c", `
import base64, json, os, sys
from urllib.parse import urlsplit
sys.path.insert(0, sys.argv[1] + "/runtime/firstfloor")
import measurement
os.environ["CHILL_TERMINATION_MESSAGE_PATH"] = sys.argv[3]
session = measurement.FirstfloorSession(urlsplit(sys.argv[2]).port, {"runConfig": {"state": {"model": "synthetic"}}})
measurement.record_stage("Execution")
try:
    value = base64.b64encode(b"allocation-failure").decode()
    session.json_call("POST", session.path + "/profile", json.dumps({
        "batchSize": 2, "trialID": "allocation-smoke",
        "inputs": [{"name": "input", "data": [value, value]}]}).encode())
    raise AssertionError("allocator failure was lost")
except measurement.ExecutionError as error:
    assert "1308622848" in str(error), str(error)
    measurement.record_failure(error)
finally:
    session.connection.close()
`, root, server.URL, path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native measurement error transport: %v\n%s", err, output)
	}
	message, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	record, ok := profilerevidence.DecodeTerminationMessage(string(message))
	if !ok || record.Stage != "Execution" || record.FailureReason != firstfloor.FailureMemoryAllocation {
		t.Fatalf("allocator termination lost: %s", message)
	}
	run, err := campaigncontroller.BuildRun(campaign, batchenergyoptimization.Measurement{WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 2}, 1, "ort-cpu")
	if err != nil {
		t.Fatal(err)
	}
	run.UID = "allocator-failed-k2"
	run.Status.Conditions = []metav1.Condition{{Type: "ExecutionComplete", Status: metav1.ConditionFalse, Reason: "JobFailed"}}
	controller := true
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: "allocation-job", UID: "allocation-job-uid"}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: "allocation-pod",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &controller}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "runtime",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", Message: string(message)}}}}},
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	finalizer, err := profilercontroller.NewKubernetesExecutionFinalizer(
		fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build(),
		func(storekube.Binding, string, string) (profilerevidence.ObjectStore, error) {
			t.Fatal("failure classification must not read evidence storage")
			return nil, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	run.Status.RuntimeTermination, err = finalizer.RuntimeTermination(ctx, run, job)
	if err != nil || run.Status.RuntimeTermination == nil || run.Status.RuntimeTermination.Class != "ExpectedStop" ||
		run.Status.RuntimeTermination.Reason == "OOMKilled" {
		t.Fatalf("native classifier lost allocation stop: %+v %v", run.Status.RuntimeTermination, err)
	}
	snapshot, err := campaigncontroller.ProjectSnapshot(campaign, []edgev1alpha1.ProfilingRun{*run}, nil, []int{1, 2}, false)
	if err != nil {
		t.Fatal(err)
	}
	attempts := make(map[batchenergyoptimization.Measurement]int)
	for _, trial := range completed {
		measurement := batchenergyoptimization.Measurement(trial.Measurement)
		attempts[measurement]++
		snapshot.Trials = append(snapshot.Trials, batchenergyoptimization.Trial{
			Measurement: measurement, Attempt: attempts[measurement],
			Verdict: batchenergyoptimization.VerdictCompleted})
	}
	method, err := batchenergyoptimization.NewQuickEnergyMinimization(batchenergyoptimization.QuickEnergyConfig{TrialsPerBatch: 3},
		func(prefix []int) (batchenergyoptimization.CurveResult, error) {
			if !slices.Equal(prefix, []int{1}) {
				t.Fatalf("failed K2 entered curve: %v", prefix)
			}
			return batchenergyoptimization.CurveResult{Continue: true}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := batchenergyoptimization.Decide(snapshot, method)
	if err != nil || decision.Action != batchenergyoptimization.ActionFinish || decision.Result == nil {
		t.Fatalf("allocation stop failed campaign: %+v %v", decision, err)
	}
	detail, ok := decision.Result.Detail.(batchenergyoptimization.DiscoveryResult)
	if !ok || !slices.Equal(detail.MeasuredBatchSizes, []int{1}) || detail.Stop.Reason != batchenergyoptimization.DiscoveryStopOOM {
		t.Fatalf("allocation prefix lost: %+v", decision)
	}
	t.Log("native allocation → IPC/HTTP → typed termination → ExpectedStop → campaign prefix K1; strict raw-evidence catalog follows")
	return detail
}

func assertCatalogDiscoveryCompletedPrefix(t *testing.T, profile serving.Profile) {
	t.Helper()
	if len(profile.Batches) != 1 || profile.Batches[0].Batch != 1 ||
		!slices.Equal(profile.Condition.PlannedBatchSizes, []int{1, 2}) ||
		!slices.Equal(profile.RuntimeMetadata.InitializedBatches, []int{1, 2}) ||
		profile.Batches[0].MemoryBytes != 4096 || profile.IdleBaselineWatts != 4 ||
		len(profile.Batches[0].ServiceSamples) != 180 || math.Abs(profile.Batches[0].EnergyPerInputJoules-1) > 1e-9 ||
		profile.Condition.ActiveProtocol.WarmupIterations != 0 || profile.Condition.Actor.WarmupIterations != 20 ||
		profile.RuntimeMetadata.WarmupIterations != 20 {
		t.Fatalf("candidate domain or measured service/J/idle/memory changed: %+v", profile)
	}
}

func TestCatalogDiscoveryRejectsUnfinishedClaimAndPreservesEmptyCompletion(t *testing.T) {
	for _, count := range []int{0, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			catalog, source, objects, _ := catalogEvidenceFixturePolicy(t, 4096, [6]float64{6, 6, 6, 4, 4, 4}, count, "", nil)
			ctx := context.Background()
			campaign := &edgev1alpha1.BatchEnergyOptimization{}
			if err := catalog.store.client.Get(ctx, source, campaign); err != nil {
				t.Fatal(err)
			}
			retained, err := catalog.evidence.Read(ctx, campaign)
			if err != nil {
				t.Fatal(err)
			}
			measured := []int{}
			if count != 0 {
				measured = []int{1}
			}
			spec := retained.Result.Spec()
			spec.Outcome = batchenergyoptimization.BatchDiscoveryComplete
			failed := batchenergyoptimization.RunReference{Namespace: source.Namespace, Name: "failed-idle-b1", UID: "failed-idle-b1-uid"}
			spec.ExcludedTrials = append(spec.ExcludedTrials, batchenergyoptimization.ExcludedTrialReference{
				Run: failed, Measurement: batchenergyoptimization.ResultMeasurement{WorkloadMode: batchenergyoptimization.WorkloadModeIdle, Batch: 1}, Attempt: 1, Reasons: []string{batchenergyoptimization.DiscoveryStopOOM},
			})
			spec.Detail, err = json.Marshal(batchenergyoptimization.DiscoveryResult{
				MeasuredBatchSizes: measured,
				Stop: batchenergyoptimization.DiscoveryStop{
					Reason:            batchenergyoptimization.DiscoveryStopOOM,
					FailedMeasurement: &batchenergyoptimization.Measurement{WorkloadMode: batchenergyoptimization.WorkloadModeIdle, Batch: 1},
					Attempt:           1,
					Run:               &failed,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := batchenergyoptimization.NewResultRecord(spec)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := result.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			objects[apiObjectIdentity(campaign.Status.Result).Location()] = payload
			campaign.Status.Result.Size = int64(len(payload))
			campaign.Status.Outcome = string(spec.Outcome)
			if err := catalog.store.client.Status().Update(ctx, campaign); err != nil {
				t.Fatal(err)
			}
			view, err := catalog.Refresh(ctx, []string{source.Namespace})
			if err != nil || len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 {
				t.Fatalf("unfinished discovery became executable: %+v %v", view, err)
			}
			want := "MeasurementEvidenceMissing"
			if count != 0 {
				want = invalidEvidenceCode
			}
			if view.Insufficiencies[0].Code != want {
				t.Fatalf("empty completion and invalid prefix were conflated: %+v", view.Insufficiencies)
			}
		})
	}
}

func TestCatalogTrialRuntimeFactsRespectSharedCPU(t *testing.T) {
	const changedObservationVersion = "new-observation"
	for _, phase := range []batchenergyoptimization.WorkloadMode{batchenergyoptimization.WorkloadModeActive, batchenergyoptimization.WorkloadModeIdle} {
		for _, tc := range []struct {
			name   string
			reason string
			mutate func(*profilingrun.RuntimeCPU, *firstfloor.RuntimeMetadata, *string)
		}{
			{name: "affinity", mutate: func(cpu *profilingrun.RuntimeCPU, native *firstfloor.RuntimeMetadata, _ *string) {
				cpu.AffinityCPUs = []int{2, 3}
				native.CPUAffinity = cpu.AffinityCPUs
			}},
			{name: "cpuset", mutate: func(cpu *profilingrun.RuntimeCPU, native *firstfloor.RuntimeMetadata, _ *string) {
				cpu.CPUSetEffective = "0-7"
				native.CPUSet = cpu.CPUSetEffective
			}},
			{name: "quota", mutate: func(cpu *profilingrun.RuntimeCPU, native *firstfloor.RuntimeMetadata, _ *string) {
				cpu.CgroupCPUMax = "-1 100000"
				native.CgroupCPUMax = cpu.CgroupCPUMax
			}},
			{name: "threads", mutate: func(cpu *profilingrun.RuntimeCPU, native *firstfloor.RuntimeMetadata, _ *string) {
				cpu.ORTIntraOpThreads, cpu.ORTInterOpThreads = 0, 0
				native.IntraOpThreads, native.InterOpThreads = 0, 0
			}},
			{name: "runtime version", mutate: func(_ *profilingrun.RuntimeCPU, native *firstfloor.RuntimeMetadata, _ *string) {
				native.RuntimeVersion = changedObservationVersion
			}},
			{name: "numpy version", mutate: func(_ *profilingrun.RuntimeCPU, native *firstfloor.RuntimeMetadata, _ *string) {
				native.NumpyVersion = changedObservationVersion
			}},
			{name: "available providers", mutate: func(_ *profilingrun.RuntimeCPU, native *firstfloor.RuntimeMetadata, _ *string) {
				native.AvailableProviders = []string{"CPUExecutionProvider", "unused"}
			}},
			{name: "physical-node", reason: "node", mutate: func(_ *profilingrun.RuntimeCPU, _ *firstfloor.RuntimeMetadata, node *string) {
				*node = "trial-node-changed"
			}},
			{name: "gpu", reason: "runner conditions", mutate: func(_ *profilingrun.RuntimeCPU, native *firstfloor.RuntimeMetadata, _ *string) {
				native.GPUUUID = "GPU-other"
			}},
		} {
			t.Run(string(phase)+"/"+tc.name, func(t *testing.T) {
				change := func(index int, active *profilingrun.RuntimeResult, idle *profilingrun.IdleRuntimeResult) {
					// Keep each raw record internally consistent while changing
					// only the second trial of the selected population.
					if phase == batchenergyoptimization.WorkloadModeActive && index == 1 {
						tc.mutate(&active.CPUContract, &active.Firstfloor.Runtime, &active.NodeName)
						active.RuntimeVersion = active.Firstfloor.Runtime.RuntimeVersion
						active.AvailableProviders = active.Firstfloor.Runtime.AvailableProviders
					} else if phase == batchenergyoptimization.WorkloadModeIdle && index == 4 {
						tc.mutate(&idle.CPUContract, &idle.Firstfloor.Runtime, &idle.NodeName)
						idle.RuntimeVersion = idle.Firstfloor.Runtime.RuntimeVersion
						idle.AvailableProviders = idle.Firstfloor.Runtime.AvailableProviders
					}
				}
				catalog, source, _, _ := catalogEvidenceFixturePolicy(t, 4096, [6]float64{6, 6, 6, 4, 4, 4}, 3, "", change)
				view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
				if err != nil {
					t.Fatal(err)
				}
				if tc.reason != "" {
					if len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 ||
						view.Insufficiencies[0].Code != invalidEvidenceCode ||
						!strings.Contains(strings.ToLower(view.Insufficiencies[0].Reason), tc.reason) {
						t.Fatalf("physical identity mismatch was not rejected: %+v", view)
					}
					return
				}
				if len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
					t.Fatalf("shared CPU observation difference rejected: %+v", view)
				}
				// Comparison must not erase the first trial's raw observations.
				assertCatalogRawProfile(t, view.Profiles[0])
			})
		}
	}
}

func assertCatalogRawProfile(t *testing.T, profile serving.Profile) {
	t.Helper()
	batch := profile.Batches[0]
	if profile.Condition.RuntimeFamily != "firstfloor" || profile.Condition.Artifact.ExecutionPath != "ort-cpu" ||
		profile.NodeName != "lattepanda" || profile.Condition.NodeUID != "uid" || len(profile.Evidence) != 13 ||
		profile.RuntimeMetadata == nil || profile.IdleBaselineWatts != 4 || profile.MeasuredAt.IsZero() ||
		profile.MetricSource.NodeName != "lattepanda" || len(profile.MetricSource.MetricLabels) == 0 ||
		profile.RuntimeMetadata.IntraOpThreads != 2 || len(profile.RuntimeMetadata.CPUAffinity) != 2 ||
		batch.MemoryBytes != 4096 || len(batch.ServiceSamples) != 180 || batch.ServiceSamples[0].ServiceDurationNanos != 500000 ||
		math.Abs(batch.EnergyPerInputJoules-1) > 1e-9 || batch.EnergyBelowResolution {
		t.Fatalf("frozen identity or measured raw distribution changed: %+v", profile)
	}
}

func TestRetainedCatalogUsesActualGapServicePairs(t *testing.T) {
	catalog, source, _, _ := catalogEvidenceFixture(t, 4096)
	view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 {
		t.Fatalf("retained admission failed: %+v %v", view, err)
	}
	distribution := view.Profiles[0].Batches[0].ServiceDistribution
	short, long := int64(1_001_000), int64(100_001_000)
	shortService, _, shortOK := distribution.QuantileNanos(&short, .95)
	longService, _, longOK := distribution.QuantileNanos(&long, .95)
	startupService, _, startupOK := distribution.QuantileNanos(nil, .95)
	if !shortOK || !longOK || !startupOK || shortService != 1500000 || longService != 500000 || startupService != 500000 {
		t.Fatalf("retained pairs lost their actual gap/startup conditions: short=%d long=%d startup=%d", shortService, longService, startupService)
	}
}

func TestCatalogNegativeIncrementalEnergyResolution(t *testing.T) {
	for _, tc := range []struct {
		name  string
		watts [6]float64
		below bool
	}{
		{name: "idle uncertainty includes zero", watts: [6]float64{3.9, 3.9, 3.9, 3, 4, 5}, below: true},
		{name: "active uncertainty includes zero", watts: [6]float64{2, 3.9, 5.8, 4, 4, 4}, below: true},
		{name: "joint uncertainty excludes zero", watts: [6]float64{1.99, 2, 2.01, 3.99, 4, 4.01}},
		{name: "exact negative excludes zero", watts: [6]float64{2, 2, 2, 4, 4, 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog, source, _, _ := catalogEvidenceFixtureWithPower(t, 4096, tc.watts)
			view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
			if err != nil {
				t.Fatal(err)
			}
			if !tc.below {
				if len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 ||
					view.Insufficiencies[0].Code != "NegativeIncrementalEnergy" ||
					!strings.Contains(view.Insufficiencies[0].Reason, "batch 1 incremental energy -1 J/item has 95% CI [") {
					t.Fatalf("resolved negative estimate was not diagnosed with value and CI: %+v", view)
				}
				return
			}
			if len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
				t.Fatalf("below-resolution energy was rejected: %+v", view)
			}
			profile := view.Profiles[0]
			batch := profile.Batches[0]
			if batch.EnergyPerInputJoules != 0 || !batch.EnergyBelowResolution ||
				len(batch.ServiceSamples) != 180 || batch.MemoryBytes != 4096 {
				t.Fatalf("below-resolution serving contract changed other measured facts: %+v", batch)
			}
			native := profile.NativeBundle().Batches[0]
			if native.EnergyPerItemJ != 0 {
				t.Fatalf("native bundle lost zero-energy representation: %+v", native)
			}
			bundleJSON, err := json.Marshal(profile.NativeBundle())
			if err != nil || bytes.Contains(bundleJSON, []byte("energyBelowResolution")) {
				t.Fatalf("native bundle changed the frozen runtime schema: %s, %v", bundleJSON, err)
			}
			payload, err := json.Marshal(view)
			if err != nil || !bytes.Contains(payload, []byte(`"energyBelowResolution":true`)) {
				t.Fatalf("catalog API lost below-resolution marker: %s, %v", payload, err)
			}
		})
	}
}

func TestCatalogTwoTrialsReloadAndSelectedConfidence(t *testing.T) {
	for _, confidence := range []string{"", "0.8"} {
		catalog, source, _, _ := catalogEvidenceFixturePolicy(t, 4096, [6]float64{6, 7, 8, 3, 5, 7}, 2, confidence, nil)
		view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
		if err != nil || len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
			t.Fatalf("two nonzero-variance populations failed retained reload: %+v, %v", view, err)
		}
		want := .95
		if confidence != "" {
			want = .8
		}
		profile := view.Profiles[0]
		if profile.ConfidenceLevel != want || profile.TrialsPerBatch != 2 || profile.IdleBaselineWatts != 4 ||
			len(profile.Batches[0].ServiceSamples) != 120 || profile.Batches[0].EnergyPerInputJoules != 1.25 {
			t.Fatalf("selected two-trial measurement lost serving values: %+v", profile)
		}
	}
}

func TestCatalogSelectedConfidenceChangesNegativeEnergyDecision(t *testing.T) {
	// At 95% the wide idle uncertainty includes zero; at 10% it excludes zero.
	for _, confidence := range []string{"", "0.1"} {
		catalog, source, _, _ := catalogEvidenceFixturePolicy(t, 4096, [6]float64{3.5, 3.5, 3.5, 3, 4, 5}, 3, confidence, nil)
		view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
		if err != nil {
			t.Fatal(err)
		}
		if confidence == "" {
			if len(view.Profiles) != 1 || !view.Profiles[0].Batches[0].EnergyBelowResolution {
				t.Fatalf("default CI changed: %+v", view)
			}
		} else if len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 ||
			!strings.Contains(view.Insufficiencies[0].Reason, "10% CI") {
			t.Fatalf("selected CI did not affect diagnosis: %+v", view)
		}
	}
}

func TestCatalogUsesRetainedExecutionPowerModeAfterRunSpecChange(t *testing.T) {
	catalog, source, _, _ := catalogEvidenceFixture(t, 4096)
	run := &edgev1alpha1.ProfilingRun{}
	ctx := context.Background()
	if err := catalog.store.client.Get(ctx, types.NamespacedName{Namespace: source.Namespace, Name: "trial-0"}, run); err != nil {
		t.Fatal(err)
	}
	measuredPowerMode := run.Spec.Candidate.PowerMode
	run.Spec.Candidate.PowerMode = "unmeasured-mode"
	if err := catalog.store.client.Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	view, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
		t.Fatalf("live Run power metadata hid retained execution: %+v %v", view, err)
	}
	if view.Profiles[0].Condition.PowerMode != measuredPowerMode {
		t.Fatalf("live Run power metadata replaced retained execution power: %+v", view.Profiles[0].Condition)
	}
	assertCatalogRawProfile(t, view.Profiles[0])
}

type catalogEvidenceObjects map[evidencestore.ObjectLocation][]byte

func (objects catalogEvidenceObjects) OpenLocation(_ context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
	payload, exists := objects[location]
	if !exists {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func catalogEvidenceFixture(t *testing.T, memory uint64) (*Catalog, types.NamespacedName, catalogEvidenceObjects, evidencestore.ObjectIdentity) {
	return catalogEvidenceFixtureWithPower(t, memory, [6]float64{6, 6, 6, 4, 4, 4})
}

func catalogEvidenceFixtureWithPower(t *testing.T, memory uint64, trialWatts [6]float64) (*Catalog, types.NamespacedName, catalogEvidenceObjects, evidencestore.ObjectIdentity) {
	return catalogEvidenceFixturePolicy(t, memory, trialWatts, 3, "", nil)
}

func catalogEvidenceFixturePolicy(t *testing.T, memory uint64, trialWatts [6]float64, count int, confidence string, change func(int, *profilingrun.RuntimeResult, *profilingrun.IdleRuntimeResult)) (*Catalog, types.NamespacedName, catalogEvidenceObjects, evidencestore.ObjectIdentity) {
	return catalogEvidenceFixtureConfigPolicy(t, memory, trialWatts, count, confidence, nil, change)
}

func catalogEvidenceFixtureConfigPolicy(t *testing.T, memory uint64, trialWatts [6]float64, count int, confidence string, configure func(*profilingrun.RunConfig), change func(int, *profilingrun.RuntimeResult, *profilingrun.IdleRuntimeResult), planned ...int) (*Catalog, types.NamespacedName, catalogEvidenceObjects, evidencestore.ObjectIdentity) {
	t.Helper()
	payload, err := os.ReadFile("../../profiling/profiler/evidence/testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var active profilingrun.RunConfig
	if err := json.Unmarshal(payload, &active); err != nil {
		t.Fatal(err)
	}
	active.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 1, MaxQueueSize: 32, DeadlinePolicy: "keep"}
	active.Measurement.DurationToleranceSeconds = .5
	if configure != nil {
		configure(&active)
	}
	idle := active
	idle.Measurement = profilingrun.MeasurementContract{Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30, DurationToleranceSeconds: .5,
		StartupWarmupIterations: active.Measurement.StartupWarmupIterations, WarmupBatchSizes: slices.Clone(active.Measurement.WarmupBatchSizes),
		WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode}
	if len(planned) == 0 {
		planned = []int{1}
	}
	spec, err := profilerprojection.ProjectCurvePlanSpec("catalog-plan", active, idle, profilederivation.CurveConversionContract{}, planned)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := profilederivation.NewCurvePlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	objects := catalogEvidenceObjects{}
	put := func(key, schema string, value any) batchenergyoptimization.StoredObjectReference {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		location := evidencestore.ObjectLocation{Store: "evidence", Key: key}
		objects[location] = data
		return batchenergyoptimization.StoredObjectReference{Store: location.Store, Key: key, SchemaVersion: schema, Size: int64(len(data))}
	}
	planBytes, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	planRef := batchenergyoptimization.StoredObjectReference{Store: "evidence", Key: "plan.json", SchemaVersion: plan.SchemaVersion(), Size: int64(len(planBytes))}
	objects[storedObjectIdentity(planRef).Location()] = planBytes
	campaign := &edgev1alpha1.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "catalog-campaign", UID: "campaign-uid", Generation: 1},
		Spec: edgev1alpha1.BatchEnergyOptimizationSpec{
			State: edgev1alpha1.MeasurementStateSpec{
				Candidate:  edgev1alpha1.ProfilingRunCandidateSpec{ModelSpec: active.Candidate.Inputs.ModelSpec.Name, Preprocessor: active.State.Preprocessor.Name, DeviceClass: active.State.DeviceClass, ExecutionPath: active.Candidate.ExecutionPath, PowerMode: active.State.PowerMode},
				TargetNode: active.TargetNode.Name,
			},
			ActiveProtocol: edgev1alpha1.ActiveProtocolSpec{WarmupIterations: int32(active.Measurement.WarmupIterations), DurationSeconds: 30, Dataset: active.Measurement.Dataset.Name,
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31}},
			IdleProtocol: edgev1alpha1.IdleProtocolSpec{SettlingDurationSeconds: 60, DurationSeconds: 30,
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31}},
			TrialsPerBatch: int32(count),
			PointPolicy:    edgev1alpha1.BatchEnergyOptimizationPointPolicySpec{ConfidenceLevel: confidence},
		},
		Status: edgev1alpha1.BatchEnergyOptimizationStatus{
			Phase: catalogCompletePhase, ObservedGeneration: 1, Outcome: "MinimumFound",
		},
	}
	apiObjects := []client.Object{campaign}
	completed := make([]batchenergyoptimization.CompletedTrialReference, 0, 6)
	for index := range 6 {
		if index%3 >= count {
			continue
		}
		config, watts, mode := active, trialWatts[index], batchenergyoptimization.WorkloadModeActive
		if index >= 3 {
			config, mode = idle, batchenergyoptimization.WorkloadModeIdle
		}
		name := fmt.Sprintf("trial-%d", index)
		raw, idleRaw, execution := catalogTrialRecords(config, name+"-pod", watts, memory)
		if change != nil {
			change(index, &raw.Runtime, &idleRaw.Runtime)
		}
		var rawRef batchenergyoptimization.StoredObjectReference
		if mode == batchenergyoptimization.WorkloadModeIdle {
			rawRef = put(name+"-raw", profilerevidence.IdleRawMeasurementSchemaVersion, idleRaw)
		} else {
			rawRef = put(name+"-raw", profilerevidence.RawMeasurementSchemaVersion, raw)
		}
		execution.RawMeasurement = storedObjectIdentity(rawRef)
		executionRef := put(name+"-execution", profilerevidence.ExecutionEvidenceSchemaVersion, execution)
		settling := int32(config.Measurement.SettlingDurationSeconds)
		measurement := edgev1alpha1.ProfilingRunMeasurementSpec{Batch: 1, WarmupIterations: int32(config.Measurement.WarmupIterations), DurationSeconds: 30,
			WorkloadMode: config.Measurement.WorkloadMode}
		measurement.StartupWarmupIterations = int32(config.Measurement.StartupWarmupIterations)
		measurement.Workload = config.Measurement.WorkloadName
		for _, batch := range config.Measurement.WarmupBatchSizes {
			measurement.WarmupBatchSizes = append(measurement.WarmupBatchSizes, int32(batch))
		}
		if config.Measurement.Dataset != nil {
			measurement.Dataset = config.Measurement.Dataset.Name
		}
		if mode == batchenergyoptimization.WorkloadModeIdle {
			measurement.SettlingDurationSeconds = &settling
		}
		run := &edgev1alpha1.ProfilingRun{
			ObjectMeta: metav1.ObjectMeta{Namespace: campaign.Namespace, Name: name, UID: types.UID(name + "-uid"), Generation: 1,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(campaign, edgev1alpha1.GroupVersion.WithKind("BatchEnergyOptimization"))}},
			Spec: edgev1alpha1.ProfilingRunSpec{MeasurementStateSpec: campaign.Spec.State, Measurement: measurement, MetricObservation: campaign.Spec.ActiveProtocol.MetricObservation},
			Status: edgev1alpha1.ProfilingRunStatus{ObservedGeneration: 1,
				Conditions: []metav1.Condition{{Type: edgev1alpha1.ProfilingRunConditionExecutionComplete, Status: metav1.ConditionTrue, ObservedGeneration: 1},
					{Type: edgev1alpha1.ProfilingRunConditionEvidenceVerified, Status: metav1.ConditionTrue, ObservedGeneration: 1}},
				Job: &edgev1alpha1.ProfilingRunObjectReference{Name: execution.Terminal.Job.Name, UID: execution.Terminal.Job.UID},
				ObservedExecution: &edgev1alpha1.ProfilingRunObservedExecutionStatus{
					Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: execution.Terminal.Pod.Name, UID: execution.Terminal.Pod.UID},
					Node: edgev1alpha1.ProfilingRunObjectReference{Name: config.TargetNode.Name, UID: config.TargetNode.UID}},
				Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{Store: executionRef.Store, Key: executionRef.Key, Size: executionRef.Size, SchemaVersion: executionRef.SchemaVersion}},
		}
		apiObjects = append(apiObjects, run)
		completed = append(completed, batchenergyoptimization.CompletedTrialReference{
			Run:         batchenergyoptimization.RunReference{Namespace: run.Namespace, Name: run.Name, UID: string(run.UID)},
			Measurement: batchenergyoptimization.ResultMeasurement{WorkloadMode: mode, Batch: 1}, Evidence: executionRef})
	}
	result, err := batchenergyoptimization.NewResultRecord(batchenergyoptimization.ResultRecordSpec{
		Outcome: "MinimumFound", MeasurementState: batchenergyoptimization.ResultMeasurementState{TargetNode: active.TargetNode.Name,
			ModelSpec: active.Candidate.Inputs.ModelSpec.Name, DeviceClass: active.State.DeviceClass, ExecutionPath: active.Candidate.ExecutionPath, PowerMode: active.State.PowerMode},
		CurvePlan: planRef, CompletedTrials: completed, Detail: []byte(`{"memoryBytes":999999,"latency":0,"energy":0}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resultBytes, err := result.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	campaign.Status.Result = &edgev1alpha1.ProfilingRunEvidenceReference{Store: "evidence", Key: "result.json", SchemaVersion: result.SchemaVersion(), Size: int64(len(resultBytes))}
	objects[apiObjectIdentity(campaign.Status.Result).Location()] = resultBytes
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store := &Store{client: fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.BatchEnergyOptimization{}).WithObjects(apiObjects...).Build()}
	catalog, err := NewCatalog(store, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return catalog, types.NamespacedName{Namespace: campaign.Namespace, Name: campaign.Name}, objects, storedObjectIdentity(planRef)
}

func catalogTrialRecords(config profilingrun.RunConfig, podUID string, watts float64, memory uint64) (profilerevidence.RawMeasurement, profilerevidence.IdleRawMeasurement, profilerevidence.ExecutionEvidence) {
	start := time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC)
	end := start.Add(30 * time.Second)
	completed := end.Add(30 * time.Second)
	targetDuration := float64(config.Measurement.DurationSeconds)
	cpu := profilingrun.RuntimeCPU{CgroupCPUMax: "400000 100000", ORTIntraOpThreads: 4, ORTInterOpThreads: 1, AffinityCPUCount: 4, AffinityCPUs: []int{0, 1, 2, 3}, CPUSetEffective: "0-3"}
	if config.State.RuntimeFamily == profilingrun.FirstfloorRuntimeFamily {
		cpu.CgroupCPUMax = measuredTwoCoreCPUMax
		cpu.ORTIntraOpThreads = 2
		cpu.AffinityCPUCount = 2
		cpu.AffinityCPUs = []int{0, 1}
	}
	provider := "CPUExecutionProvider"
	derived := profilingrun.DerivedContract{Backend: config.State.Backend, Provider: provider, NodeSnapshot: config.TargetNode}
	pod := workloadobserver.Pod{Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID, Phase: "Running", Images: []string{config.Images.Runtime}}
	thermal := config.Metrics.ThermalMetrics
	metrics := metricobserver.Result{
		Source: metricobserver.SourceIdentity{Kind: config.Metrics.SourceKind, NodeName: config.TargetNode.Name, Endpoint: config.Metrics.Endpoint,
			Metric: config.Metrics.PowerMetric, MetricLabels: config.Metrics.MetricLabels, ThermalEndpoint: config.Metrics.ThermalEndpoint,
			ThermalMetrics: &metricobserver.ThermalMetricIdentity{ObservationAvailable: thermal.ObservationAvailable, Active: thermal.Active,
				CPUCoreThrottleEvents: thermal.CPUCoreThrottleEvents, CPUPackageThrottleEvents: thermal.CPUPackageThrottleEvents, Latched: thermal.Latched,
				MaximumTemperatureCelsius: thermal.MaximumTemperatureCelsius}},
		StartedAt: start, EndedAt: end.Add(time.Second), Samples: []metricobserver.Sample{
			{ObservedAt: start, Watts: watts}, {ObservedAt: end, Watts: watts}},
		Failures: []metricobserver.Failure{}, Summary: metricobserver.Summary{Attempts: 2, SuccessfulSamples: 2, ObservationDurationSeconds: 31, MaximumSampleGapSeconds: 30, SourceTimestampAbsent: true},
	}
	workload := &workloadobserver.Result{SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target:            workloadobserver.TargetIdentity{NodeName: config.TargetNode.Name, NodeUID: config.TargetNode.UID},
		Run:               workloadobserver.RunIdentity{Namespace: pod.Namespace, JobUID: "job-" + podUID, PodName: pod.Name, PodUID: podUID},
		CoverageStartedAt: start.Add(-time.Second), CoverageEndedAt: end.Add(time.Second), InitialResourceVersion: "10", LastResourceVersion: "11", EventLimit: 10,
		InitialActivePods: []workloadobserver.Pod{pod}, FinalActivePods: []workloadobserver.Pod{pod}, Continuous: true}
	raw := profilerevidence.RawMeasurement{SchemaVersion: profilerevidence.RawMeasurementSchemaVersion, RunConfig: config, PodUID: podUID, Metrics: metrics, Workload: workload,
		Runtime: profilingrun.RuntimeResult{SchemaVersion: profilingrun.RuntimeResultSchemaVersion, Status: "Succeeded", ExperimentID: podUID,
			NodeName: config.TargetNode.Name, Model: config.State.Model, Architecture: "x86_64", Runtime: config.State.RuntimeFamily, RuntimeVersion: "1.16.0",
			Backend: config.State.Backend, Provider: provider, AvailableProviders: []string{provider}, Inputs: config.State.Preprocessor.Inputs,
			BatchSize: 1, WarmupIterations: config.Measurement.WarmupIterations, WarmupCompletedAt: start.Add(-time.Second).Format(time.RFC3339Nano), InferenceCount: 60,
			MeasurementMode: "duration", MeasurementStartedAt: start.Format(time.RFC3339Nano), MeasurementEndedAt: end.Format(time.RFC3339Nano), MeasurementElapsedSeconds: 30,
			TargetDurationSeconds: &targetDuration, MemoryBytes: memory,
			CPUContract: cpu, DerivedContract: derived, RepetitionResults: []profilingrun.Repetition{{Repetition: 1, MeasurementStartedAt: start.Format(time.RFC3339Nano), MeasurementEndedAt: end.Format(time.RFC3339Nano), LatencyMilliseconds: make([]float64, 60)}}}}
	modelFiles := make([]string, len(config.State.ArtifactFiles))
	for i, file := range config.State.ArtifactFiles {
		modelFiles[i] = "/model/" + filepath.Base(file)
	}
	slices.Sort(modelFiles)
	initialized := []int{1}
	if config.State.Firstfloor.MaxBatchSize > 1 {
		initialized = slices.Clone(config.Actor.BatchSizes)
	}
	native := profilingrun.FirstfloorRuntimeEvidence{Mode: "profiling", DeadlinePolicy: firstfloor.DeadlineKeep,
		ModelInstanceCount: 1, MaxBatchSize: int(config.State.Firstfloor.MaxBatchSize), MaxQueueSize: 32,
		Runtime: firstfloor.RuntimeMetadata{Runtime: "ort-cpu", RuntimeVersion: "1.16.0", ModelFiles: modelFiles, Inputs: config.State.Preprocessor.Inputs, Outputs: config.State.Preprocessor.Outputs,
			Providers: []string{provider}, AvailableProviders: []string{provider}, Device: "cpu", CPUAffinity: []int{0, 1}, IntraOpThreads: 2, InterOpThreads: 1, InitializedBatches: initialized,
			CgroupCPUMax: measuredTwoCoreCPUMax, CPUSet: "0-3"}}
	native.Runtime.WarmupIterations = config.Actor.WarmupIterations
	if config.Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		activeNative := native
		activeNative.ExecutionCount, activeNative.InferenceCount = 60, 60
		activeNative.BatchExecutionCounts = map[string]uint64{"1": 60}
		raw.Runtime.Firstfloor = &activeNative
		raw.Runtime.MemorySource = "cgroup-v2-memory.peak:runtime-container-lifetime"
		population := []profilingrun.NativeServiceSample{
			{BatchID: 1, BatchSize: 1, RecordIDs: []string{"r1"}, Units: []int{1}, RunnerServiceMilliseconds: 0.5, BackendCallMilliseconds: 0.25, NativeRequestMilliseconds: 0.75},
			{BatchID: 2, BatchSize: 1, RecordIDs: []string{"r2"}, Units: []int{1}, RunnerServiceMilliseconds: 1.5, BackendCallMilliseconds: 1, NativeRequestMilliseconds: 1.75},
		}
		repetition := &raw.Runtime.RepetitionResults[0]
		repetition.NativeSamples = make([]profilingrun.NativeServiceSample, 60)
		for index := range repetition.NativeSamples {
			repetition.LatencyMilliseconds[index] = float64(1 + index%2)
			repetition.NativeSamples[index] = population[index%len(population)]
			repetition.NativeSamples[index].BatchID = uint64(index + 1)
			repetition.NativeSamples[index].RecordIDs = []string{fmt.Sprintf("r%d", index+1)}
			service := int64(500000)
			if index%2 != 0 {
				service = 1500000
			}
			repetition.NativeSamples[index].ServiceSample = fixtures.ServiceSample(podUID, uint64(index), service)
		}
	}
	idle := profilerevidence.IdleRawMeasurement{SchemaVersion: profilerevidence.IdleRawMeasurementSchemaVersion, RunConfig: config, PodUID: podUID, Metrics: metrics, Workload: workload,
		Runtime: profilingrun.IdleRuntimeResult{SchemaVersion: profilingrun.IdleRuntimeResultSchemaVersion, Status: "Succeeded", PodUID: podUID, NodeName: config.TargetNode.Name,
			Model: config.State.Model, Architecture: "x86_64", Runtime: config.State.RuntimeFamily, RuntimeVersion: "1.16.0", Backend: config.State.Backend, Provider: provider,
			AvailableProviders: []string{provider}, Inputs: config.State.Preprocessor.Inputs, ModelLoadCompletedAt: start.Add(-61 * time.Second).Format(time.RFC3339Nano),
			SettlingStartedAt: start.Add(-60 * time.Second).Format(time.RFC3339Nano), SettlingCompletedAt: start.Format(time.RFC3339Nano), MeasurementStartedAt: start.Format(time.RFC3339Nano),
			MeasurementEndedAt: end.Format(time.RFC3339Nano), MeasurementElapsedSeconds: 30, ModelResidentAtEnd: true, CPUContract: cpu, DerivedContract: derived}}
	native.BatchExecutionCounts = map[string]uint64{}
	idle.Runtime.Firstfloor = &native
	idle.Runtime.MemoryBytes, idle.Runtime.MemorySource = memory, "cgroup-v2-memory.peak:runtime-container-lifetime"
	execution := profilerevidence.ExecutionEvidence{SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion, RunConfig: config, FinalizedAt: completed.Add(2 * time.Second),
		Terminal: profilerevidence.TerminalFacts{Job: profilerevidence.JobTerminalFacts{Namespace: pod.Namespace, Name: "job-" + podUID, UID: "job-" + podUID, Succeeded: 1, CompletionTime: completed},
			Pod:  profilerevidence.PodTerminalFacts{Namespace: pod.Namespace, Name: pod.Name, UID: podUID, ControllerOwnerUID: "job-" + podUID, NodeName: config.TargetNode.Name, Phase: "Succeeded"},
			Node: profilerevidence.NodeTerminalFacts{Name: config.TargetNode.Name, UID: config.TargetNode.UID}, NodeWorkload: profilerevidence.NodeWorkload{ObservedAt: completed.Add(time.Second),
				Pods: []profilerevidence.WorkloadPod{{Namespace: pod.Namespace, Name: pod.Name, UID: podUID, Phase: "Succeeded", Images: pod.Images}}}}}
	images := map[string]string{"runtime": config.Images.Runtime, "artifact": config.Images.Artifact, "preprocessor": config.State.Preprocessor.Image, "metric-observer": config.Images.MetricObserver, "workload-observer": config.Images.WorkloadObserver}
	if config.Measurement.Dataset != nil {
		images["dataset"] = config.Measurement.Dataset.Image
	}
	for name, image := range images {
		execution.Terminal.Containers = append(execution.Terminal.Containers, profilerevidence.ContainerTerminal{Name: name, RequestedImage: image, ImageID: "containerd://" + name,
			Reason: "Completed", StartedAt: start, FinishedAt: completed.Add(-time.Second)})
	}
	return raw, idle, execution
}

func TestCatalogRetainsAdmittedFactsWithCallerOwnedViews(t *testing.T) {
	const callerMutation = "caller-mutated"
	catalog, source, objects, plan := catalogEvidenceFixture(t, 4096)
	ctx := context.Background()
	view, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 {
		t.Fatalf("initial admission failed: %+v %v", view, err)
	}
	profile := &view.Profiles[0]
	inputs := slices.Clone(profile.RuntimeMetadata.Inputs)
	preprocessor := profile.Condition.Preprocessor.Inputs[0]
	profile.Evidence[0].Key = callerMutation
	profile.Batches[0].ServiceSamples[0].ServiceDurationNanos = 999
	profile.Condition.Firstfloor.MaxBatchSize = 999
	profile.Condition.PlannedBatchSizes[0] = 999
	profile.Condition.Preprocessor.Inputs[0] = callerMutation
	profile.RuntimeMetadata.CPUAffinity[0] = 999
	profile.RuntimeMetadata.Inputs[0] = callerMutation
	profile.MetricSource.MetricLabels[callerMutation] = "yes"
	// Previously admitted immutable facts remain usable during backing-object
	// unavailability; current native Campaign metadata must still be read.
	clear(objects)
	for range 2 {
		view, err = catalog.Refresh(ctx, []string{source.Namespace})
		if err != nil || len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
			t.Fatalf("unchanged terminal admission was lost: %+v %v", view, err)
		}
		profile = &view.Profiles[0]
		assertCatalogRawProfile(t, *profile)
		if profile.Evidence[0] != plan || profile.Condition.Firstfloor.MaxBatchSize != 1 ||
			profile.Condition.PlannedBatchSizes[0] != 1 ||
			profile.Condition.Preprocessor.Inputs[0] != preprocessor ||
			!slices.Equal(profile.RuntimeMetadata.Inputs, inputs) ||
			profile.MetricSource.MetricLabels[callerMutation] != "" {
			t.Fatalf("caller mutation reached retained admission: %+v", profile)
		}
		profile.Batches[0].ServiceSamples[0].ServiceDurationNanos = 999
		profile.Condition.Firstfloor.MaxBatchSize = 999
	}
}

func TestCatalogInvalidatesChangedPendingRemovedAndRecreatedSources(t *testing.T) {
	const recreatedSource = "recreated"
	const pendingSource = "pending"
	for _, change := range []string{"version", pendingSource, "unselected", "deleted", recreatedSource} {
		t.Run(change, func(t *testing.T) {
			catalog, source, objects, _ := catalogEvidenceFixture(t, 4096)
			ctx := context.Background()
			view, err := catalog.Refresh(ctx, []string{source.Namespace})
			if err != nil || len(view.Profiles) != 1 {
				t.Fatalf("initial admission failed: %+v %v", view, err)
			}
			retainedObjects := maps.Clone(objects)
			clear(objects)
			campaign := &edgev1alpha1.BatchEnergyOptimization{}
			if err := catalog.store.client.Get(ctx, source, campaign); err != nil {
				t.Fatal(err)
			}
			// Incomplete and deleted measurements are skipped; changed evidence is read again.
			issues := 1
			switch change {
			case "version":
				campaign.Annotations = map[string]string{"catalog-test-revision": "changed"}
				err = catalog.store.client.Update(ctx, campaign)
			case pendingSource:
				campaign.Status.Phase = "Measuring"
				err = catalog.store.client.Status().Update(ctx, campaign)
				issues = 0
			case "unselected":
				_, err = catalog.Refresh(ctx, nil)
			case "deleted", recreatedSource:
				err = catalog.store.client.Delete(ctx, campaign)
				if err == nil && change != recreatedSource {
					issues = 0
				}
				if err == nil && change == recreatedSource {
					campaign.UID, campaign.ResourceVersion = "replacement-uid", ""
					err = catalog.store.client.Create(ctx, campaign)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				view, err = catalog.Refresh(ctx, []string{source.Namespace})
				if err != nil || len(view.Profiles) != 0 || len(view.Insufficiencies) != issues {
					t.Fatalf("changed source reused an old success: %+v %v", view, err)
				}
			}
			if change == "deleted" || change == recreatedSource {
				return
			}
			maps.Copy(objects, retainedObjects)
			if change == pendingSource {
				if err := catalog.store.client.Get(ctx, source, campaign); err != nil {
					t.Fatal(err)
				}
				campaign.Status.Phase = catalogCompletePhase
				if err := catalog.store.client.Status().Update(ctx, campaign); err != nil {
					t.Fatal(err)
				}
			}
			view, err = catalog.Refresh(ctx, []string{source.Namespace})
			if err != nil || len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
				t.Fatalf("fresh evidence did not restore admission: %+v %v", view, err)
			}
			assertCatalogRawProfile(t, view.Profiles[0])
		})
	}
}

func apiObjectIdentity(reference *edgev1alpha1.ProfilingRunEvidenceReference) evidencestore.ObjectIdentity {
	return evidencestore.ObjectIdentity{Store: reference.Store, Key: reference.Key, Size: reference.Size}
}

func storedObjectIdentity(reference batchenergyoptimization.StoredObjectReference) evidencestore.ObjectIdentity {
	return evidencestore.ObjectIdentity{Store: reference.Store, Key: reference.Key, Size: reference.Size}
}

package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingworker "github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	"github.com/lab-paper-code/chill/test/fixtures"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	nativeIdentityModel   = "tiny-identity-fixture"
	nativeCatalogMaxBytes = 256 << 20
)

func TestRealFirstfloorWorkerHelper(t *testing.T) {
	fixtures.RunRealFirstfloorWorkerHelper(t)
}

// This is a local software-path integration test, not a hardware measurement.
// Python 3 with numpy, onnx and onnxruntime, and readable cgroup CPU/memory
// accounting are required. The shared helper generates a genuine tiny Identity
// model, not the MobileNet artifact whose basename the old fixture uses.
// Two native active trials retain sparse, saturation and utilization phases,
// each with its own 4 s AND 59-known-gap stop rule, followed by 18 s loaded
// post-load observation; idle retains 60 s settling + 30 s.
// Only meter readings, workload isolation and Kubernetes terminal/ownership facts
// are controlled fixtures. No cluster, measured hardware watts or deployment
// image verification is claimed. There are no donor, Eval or history objects.
func TestProfileIndependentNativeCommitCatalogCapacity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	command := fixtures.RealFirstfloorCommand(t)
	t.Setenv("POD_IP", "127.0.0.1")
	witnessDirectory := nativeCatalogWitnessDirectory(t)
	catalog, source, objects, planIdentity := catalogEvidenceFixtureConfigPolicy(t, 4096,
		[6]float64{6, 6, 6, 4, 4, 4}, 2, "", func(config *profilingrun.RunConfig) {
			// Explicitly label the generated model and its frozen recipe as test
			// inputs; retain the sampling and warmup contracts.
			config.State.Model = nativeIdentityModel
			config.Candidate.Inputs.ModelSpec.Name = config.State.Model
			config.State.Preprocessor.Name = nativeIdentityModel
			config.Measurement.Dataset.Name = nativeIdentityModel
			config.Measurement.WorkloadName = nativeIdentityModel
			config.Measurement.OperatingPoints.QueueBytesCap = 1 << 20
			config.Measurement.OperatingPoints.FramedDecodedRequestUpperBytes = (1 << 20) / 32
			// Keep multiple independent trials and every named phase, but bound
			// fast-model sample volume. Four seconds also accommodates the shared
			// fixture's alternating 100 ms gaps; the sample floor governs sparse.
			config.Measurement.OperatingPoints.PhaseDurationSeconds = 4
			config.Measurement.OperatingPoints.PostLoadDurationSeconds = 18
			config.Measurement.DurationSeconds = 4*(len(config.Measurement.OperatingPoints.Utilizations)+2) +
				config.Measurement.OperatingPoints.PostLoadDurationSeconds
			config.Metrics.DurationSeconds = config.Measurement.DurationSeconds + 1
			memoryRequest, memoryLimit := config.Actor.MemoryRequestBytes, config.Actor.MemoryLimitBytes
			config.Actor = config.ActorConfig()
			config.Actor.MemoryRequestBytes, config.Actor.MemoryLimitBytes = memoryRequest, memoryLimit
		}, nil)
	// The tiny model runs as fast as real IPC/ORT permit: retaining every actual
	// sample needs a test byte envelope, not pacing or truncating observations.
	var err error
	catalog, err = NewCatalog(catalog.store, objects, nativeCatalogMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	campaign := &edgev1alpha1.BatchEnergyOptimization{}
	if err := catalog.store.client.Get(ctx, source, campaign); err != nil {
		t.Fatal(err)
	}
	retained, err := catalog.evidence.Read(ctx, campaign)
	if err != nil {
		t.Fatal(err)
	}
	assertNativeCatalogRecipe(t, retained.Plan, campaign)
	resultSpec := retained.Result.Spec()
	writer := nativeCatalogImmutableWriter{directory: witnessDirectory, objects: objects}
	var samples []firstfloor.ServiceSample
	var firstMetadata *firstfloor.RuntimeMetadata
	var memory uint64
	var energy float64
	activeTrials, idleTrials := 0, 0
	for index := range resultSpec.CompletedTrials {
		trial := &resultSpec.CompletedTrials[index]
		run := &edgev1alpha1.ProfilingRun{}
		if err := catalog.store.client.Get(ctx, types.NamespacedName{Namespace: trial.Run.Namespace, Name: trial.Run.Name}, run); err != nil {
			t.Fatal(err)
		}
		oldExecution := storedObjectIdentity(trial.Evidence)
		var execution profilerevidence.ExecutionEvidence
		if err := json.Unmarshal(objects[oldExecution.Location()], &execution); err != nil {
			t.Fatal(err)
		}
		oldRaw := execution.RawMeasurement
		config, podUID := execution.RunConfig, execution.Terminal.Pod.UID
		verified, err := profilingrun.ValidateRunConfig(config)
		if err != nil {
			t.Fatal(err)
		}
		// Compare the serialized frozen recipe, not nil-versus-empty Go
		// allocations for fields omitted from the stored JSON contract.
		configJSON, err := config.CanonicalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var wireConfig profilingrun.RunConfig
		if err := json.Unmarshal(configJSON, &wireConfig); err != nil {
			t.Fatal(err)
		}
		rawIdentity, output, started := commitNativeCatalogTrial(t, ctx, command, writer, profilingworker.Request{
			RunConfig: verified, PodUID: podUID, ScheduledNodeName: config.TargetNode.Name,
			RawLocation: evidencestore.ObjectLocation{Store: "evidence", Key: "native/" + run.Name + "-raw.json"},
		})
		committed := objects[rawIdentity.Location()]
		var raw profilerevidence.RawMeasurement
		var idleRaw profilerevidence.IdleRawMeasurement
		if trial.Measurement.WorkloadMode == batchenergyoptimization.WorkloadModeIdle {
			idleRaw = assertNativeIdleTrial(t, wireConfig, committed, output)
			memory = max(memory, idleRaw.Runtime.MemoryBytes)
			idleTrials++
		} else {
			raw, samples = assertNativeActiveTrial(t, podUID, wireConfig, committed, output, samples)
			runtime := raw.Runtime
			if firstMetadata == nil {
				firstMetadata = &runtime.Firstfloor.Runtime
			}
			energy = accumulateNativeCatalogUtilizationEnergy(t, energy, runtime.Phases)
			memory = max(memory, runtime.MemoryBytes)
			activeTrials++
		}
		completed := time.Now().UTC()
		execution.RawMeasurement = rawIdentity
		execution.Terminal.Job.CompletionTime = completed
		execution.Terminal.NodeWorkload.ObservedAt = completed
		execution.FinalizedAt = completed
		for i := range execution.Terminal.Containers {
			execution.Terminal.Containers[i].StartedAt, execution.Terminal.Containers[i].FinishedAt = started, completed
		}
		var payload []byte
		if trial.Measurement.WorkloadMode == batchenergyoptimization.WorkloadModeIdle {
			payload, err = execution.CanonicalIdleJSON(idleRaw)
		} else {
			payload, err = execution.CanonicalJSON(raw)
		}
		if err != nil {
			t.Fatal(err)
		}
		executionRef := nativeCatalogCommit(t, ctx, writer, "native/"+run.Name+"-execution.json", profilerevidence.ExecutionEvidenceSchemaVersion, payload)
		trial.Evidence = executionRef
		run.Status.Evidence = &edgev1alpha1.ProfilingRunEvidenceReference{Store: executionRef.Store, Key: executionRef.Key, Size: executionRef.Size, SchemaVersion: executionRef.SchemaVersion}
		if err := catalog.store.client.Update(ctx, run); err != nil {
			t.Fatal(err)
		}
		delete(objects, oldExecution.Location())
		delete(objects, oldRaw.Location())
	}
	if activeTrials != 2 || idleTrials != 2 {
		t.Fatalf("physical trials were narrowed or duplicated: active=%d idle=%d", activeTrials, idleTrials)
	}
	result, err := batchenergyoptimization.NewResultRecord(resultSpec)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := result.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	resultRef := nativeCatalogCommit(t, ctx, writer, "native/result.json", result.SchemaVersion(), payload)
	delete(objects, apiObjectIdentity(campaign.Status.Result).Location())
	campaign.Status.Result = &edgev1alpha1.ProfilingRunEvidenceReference{Store: resultRef.Store, Key: resultRef.Key, Size: resultRef.Size, SchemaVersion: resultRef.SchemaVersion}
	if err := catalog.store.client.Status().Update(ctx, campaign); err != nil {
		t.Fatal(err)
	}
	view, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
		t.Fatalf("strict catalog did not admit real committed trials: %+v %v", view.Insufficiencies, err)
	}
	writeNativeCatalogWitness(t, ctx, catalog, campaign, objects, witnessDirectory)
	assertNativeCatalogConsumer(t, ctx, &view.Profiles[0], retained.Plan,
		planIdentity, samples, firstMetadata, memory, energy, 2)
}

func nativeCatalogWitnessDirectory(t *testing.T) string {
	t.Helper()
	directory := os.Getenv("CHILL_FIRST_PROFILE_FIXTURE_OUTPUT")
	if directory == "" {
		var err error
		directory, err = os.MkdirTemp("/tmp", "chill-native-catalog-")
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("native immutable witness directory: %s", directory)
	return directory
}

func assertNativeCatalogRecipe(t *testing.T, plan profilederivation.CurvePlan, campaign *edgev1alpha1.BatchEnergyOptimization) {
	t.Helper()
	frozen := plan.Shared()
	if frozen.ActiveProtocol.DurationSeconds != 30 || frozen.ActiveProtocol.MinServiceSamples != 59 ||
		frozen.ActiveProtocol.SLOPercentile != 95 || frozen.ActiveProtocol.OperatingPoints.PhaseDurationSeconds != 4 ||
		frozen.ActiveProtocol.OperatingPoints.PostLoadDurationSeconds != 18 || campaign.Spec.TrialsPerBatch != 2 {
		t.Fatalf("native acceptance recipe changed: %+v", frozen.ActiveProtocol)
	}
}

func accumulateNativeCatalogUtilizationEnergy(t *testing.T, energy float64, phases []profilingrun.Phase) float64 {
	t.Helper()
	for _, phase := range phases {
		if phase.Kind != "utilization" {
			continue
		}
		start, err := time.Parse(time.RFC3339Nano, phase.MeasurementStartedAt)
		if err != nil {
			t.Fatal(err)
		}
		end, err := time.Parse(time.RFC3339Nano, phase.MeasurementEndedAt)
		if err != nil {
			t.Fatal(err)
		}
		energy += 2 * end.Sub(start).Seconds() / float64(phase.CompletedInputs)
	}
	return energy
}

func TestNativeCatalogConsumerRetainsShortPhaseFixture(t *testing.T) {
	ctx := context.Background()
	catalog, source, objects, _ := catalogEvidenceFixtureConfigPolicy(t, 4096,
		[6]float64{6, 6, 6, 4, 4, 4}, 3, "", func(config *profilingrun.RunConfig) {
			config.Measurement.OperatingPoints.PhaseDurationSeconds = 4
			config.Measurement.OperatingPoints.PostLoadDurationSeconds = 18
			config.Measurement.DurationSeconds, config.Metrics.DurationSeconds = 30, 31
		}, nil)
	campaign := &edgev1alpha1.BatchEnergyOptimization{}
	if err := catalog.store.client.Get(ctx, source, campaign); err != nil {
		t.Fatal(err)
	}
	assertNativeCatalogRetainedObjects(t, ctx, catalog, campaign, objects)
}

func assertNativeCatalogRetainedObjects(t *testing.T, ctx context.Context, catalog *Catalog, campaign *edgev1alpha1.BatchEnergyOptimization, objects catalogEvidenceObjects) {
	t.Helper()
	retained, err := catalog.evidence.Read(ctx, campaign)
	if err != nil {
		t.Fatal(err)
	}
	planIdentity := storedObjectIdentity(retained.Result.Spec().CurvePlan)
	var samples []firstfloor.ServiceSample
	var metadata *firstfloor.RuntimeMetadata
	var memory uint64
	var energy float64
	for _, reference := range retained.Result.Spec().CompletedTrials {
		var execution profilerevidence.ExecutionEvidence
		if err := json.Unmarshal(objects[storedObjectIdentity(reference.Evidence).Location()], &execution); err != nil {
			t.Fatal(err)
		}
		if reference.Measurement.WorkloadMode == batchenergyoptimization.WorkloadModeIdle {
			var raw profilerevidence.IdleRawMeasurement
			if err := json.Unmarshal(objects[execution.RawMeasurement.Location()], &raw); err != nil {
				t.Fatal(err)
			}
			memory = max(memory, raw.Runtime.MemoryBytes)
			continue
		}
		var raw profilerevidence.RawMeasurement
		if err := json.Unmarshal(objects[execution.RawMeasurement.Location()], &raw); err != nil {
			t.Fatal(err)
		}
		if metadata == nil {
			metadata = &raw.Runtime.Firstfloor.Runtime
		}
		memory = max(memory, raw.Runtime.MemoryBytes)
		for _, phase := range raw.Runtime.Phases {
			for _, sample := range phase.NativeSamples {
				samples = append(samples, sample.ServiceSample.Clone())
			}
			if phase.Kind == "utilization" {
				start, _ := time.Parse(time.RFC3339Nano, phase.MeasurementStartedAt)
				end, _ := time.Parse(time.RFC3339Nano, phase.MeasurementEndedAt)
				energy += 2 * end.Sub(start).Seconds() / float64(phase.CompletedInputs)
			}
		}
	}
	view, err := catalog.Refresh(ctx, []string{campaign.Namespace})
	if err != nil || len(view.Profiles) != 1 {
		t.Fatalf("short fixture catalog: %+v %v", view.Insufficiencies, err)
	}
	assertNativeCatalogConsumer(t, ctx, &view.Profiles[0], retained.Plan, planIdentity, samples, metadata, memory, energy,
		int(campaign.Spec.TrialsPerBatch))
}

// Verification output always retains exact immutable bytes and controlled
// Kubernetes identities under /tmp (or the explicit output directory).
// A downstream fixture failure can replay these facts without remeasurement.
func writeNativeCatalogWitness(
	t *testing.T, ctx context.Context, catalog *Catalog,
	campaign *edgev1alpha1.BatchEnergyOptimization, objects catalogEvidenceObjects, directory string,
) {
	t.Helper()
	runs := &edgev1alpha1.ProfilingRunList{}
	if err := catalog.store.client.List(ctx, runs); err != nil {
		t.Fatal(err)
	}
	snapshot := nativeCatalogWitness{Campaign: campaign, Runs: runs.Items}
	for location, payload := range objects {
		snapshot.Objects = append(snapshot.Objects, nativeCatalogWitnessObject{Location: location, Payload: payload})
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "native-catalog-evidence.json")
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("retained exact native immutable evidence: %s (%d bytes)", path, len(payload))
}

type nativeCatalogWitnessObject struct {
	Location evidencestore.ObjectLocation `json:"location"`
	Payload  json.RawMessage              `json:"payload"`
}

type nativeCatalogWitness struct {
	Campaign *edgev1alpha1.BatchEnergyOptimization `json:"campaign"`
	Runs     []edgev1alpha1.ProfilingRun           `json:"runs"`
	Objects  []nativeCatalogWitnessObject          `json:"objects"`
}

func TestNativeCatalogReplayRetainedWitness(t *testing.T) {
	directory := os.Getenv("CHILL_FIRST_PROFILE_FIXTURE_OUTPUT")
	if directory == "" {
		t.Skip("set CHILL_FIRST_PROFILE_FIXTURE_OUTPUT to a retained native witness")
	}
	payload, err := os.ReadFile(filepath.Join(directory, "native-catalog-evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot nativeCatalogWitness
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Campaign == nil {
		t.Fatal("native witness has no campaign")
	}
	objects := make(catalogEvidenceObjects, len(snapshot.Objects))
	for _, object := range snapshot.Objects {
		objects[object.Location] = object.Payload
	}
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	apiObjects := []client.Object{snapshot.Campaign}
	for index := range snapshot.Runs {
		apiObjects = append(apiObjects, &snapshot.Runs[index])
	}
	store := &Store{client: fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.BatchEnergyOptimization{}).WithObjects(apiObjects...).Build()}
	catalog, err := NewCatalog(store, objects, nativeCatalogMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	assertNativeCatalogRetainedObjects(t, context.Background(), catalog, snapshot.Campaign, objects)
}

func commitNativeCatalogTrial(
	t *testing.T, ctx context.Context, command []string, writer nativeCatalogImmutableWriter,
	request profilingworker.Request,
) (evidencestore.ObjectIdentity, []byte, time.Time) {
	t.Helper()
	config, err := request.RunConfig.Record()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	path := func(name string) string { return filepath.Join(directory, name) }
	for _, name := range []string{"metrics-ready", "workload-ready", "window-ready"} {
		if err := profilingworker.PublishSignal(path(name)); err != nil {
			t.Fatal(err)
		}
	}
	var diagnostics bytes.Buffer
	native, err := profilingworker.NewFirstfloorRuntime(profilingworker.CPURuntimeConfig{
		Command: command, ObserverReadyPaths: []string{path("metrics-ready"), path("workload-ready")},
		WorkloadWindowStartPath: path("window-start"), MeasurementStartPath: path("measurement-start"),
		WorkloadWindowReadyPath: path("window-ready"), MeasurementEndPath: path("measurement-end"), ResultPath: path("runtime.json"),
		ObserverReadyTimeout: time.Minute, MaxLatencySamples: 1 << 20, MaxInputBytes: 1 << 20, MaxResultBytes: nativeCatalogMaxBytes,
		Stdout: &diagnostics, Stderr: &diagnostics,
	}, request.RunConfig)
	if err != nil {
		t.Fatal(err)
	}
	// Controlled observer fixtures are placed over the actual runtime window.
	// They never return runtime observations.
	observer, _, _ := catalogTrialRecords(config, request.PodUID, 6, 4096)
	if config.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		for i := range observer.Metrics.Samples {
			observer.Metrics.Samples[i].Watts = 4
		}
	}
	metricReader := nativeCatalogMetricReader(func(context.Context) (metricobserver.VerifiedResult, error) {
		payload, err := os.ReadFile(path("runtime.json"))
		if err != nil {
			return metricobserver.VerifiedResult{}, err
		}
		var window struct {
			Started string `json:"measurementStartedAt"`
			Ended   string `json:"measurementEndedAt"`
		}
		if err := json.Unmarshal(payload, &window); err != nil {
			return metricobserver.VerifiedResult{}, err
		}
		start, err := time.Parse(time.RFC3339Nano, window.Started)
		if err != nil {
			return metricobserver.VerifiedResult{}, err
		}
		end, err := time.Parse(time.RFC3339Nano, window.Ended)
		if err != nil {
			return metricobserver.VerifiedResult{}, err
		}
		observer.Metrics.StartedAt, observer.Metrics.EndedAt = start, end.Add(time.Second)
		watts := float64(6)
		if config.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
			watts = 4
		}
		count := int(math.Ceil(end.Sub(start).Seconds()))
		observer.Metrics.Samples = make([]metricobserver.Sample, count+1)
		for index := range observer.Metrics.Samples {
			observer.Metrics.Samples[index] = metricobserver.Sample{ObservedAt: start.Add(time.Duration(index) * end.Sub(start) / time.Duration(count)), Watts: watts}
		}
		observer.Metrics.Summary = metricobserver.Summarize(observer.Metrics)
		observer.Workload.CoverageStartedAt, observer.Workload.CoverageEndedAt = start.Add(-time.Second), end.Add(time.Second)
		return metricobserver.VerifyResult(observer.Metrics)
	})
	workloadReader := nativeCatalogWorkloadReader(func(context.Context) (workloadobserver.VerifiedResult, error) {
		return workloadobserver.VerifyResult(*observer.Workload)
	})
	coordinator, err := profilingworker.NewCoordinator(native, metricReader, workloadReader, writer)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	loadCtx, stopLoad := context.WithCancel(ctx)
	defer stopLoad()
	loadDone := make(chan error, 1)
	var loadDiagnostics bytes.Buffer
	active := config.Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode
	if active {
		go func() {
			for {
				connection, dialErr := net.DialTimeout("tcp", "127.0.0.1:8081", 100*time.Millisecond)
				if dialErr == nil {
					_ = connection.Close()
					break
				}
				select {
				case <-loadCtx.Done():
					loadDone <- loadCtx.Err()
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
			binary := os.Getenv("CHILL_TEST_CHILL_SERVER")
			if binary == "" {
				loadDone <- errors.New("CHILL_TEST_CHILL_SERVER must name the built chill-server binary; use make test-native")
				return
			}
			deadline, ok := loadCtx.Deadline()
			if !ok {
				loadDone <- errors.New("native load integration requires an absolute test deadline")
				return
			}
			producer := exec.CommandContext(loadCtx, binary, "profiling-load",
				"--control-endpoint=http://127.0.0.1:8081", "--dataset-dir="+filepath.Join(command[3], "dataset"),
				"--deadline="+deadline.UTC().Format(time.RFC3339Nano))
			producer.Stdout, producer.Stderr = &loadDiagnostics, &loadDiagnostics
			loadDone <- producer.Run()
		}()
	}
	receipt, err := coordinator.Execute(ctx, request)
	if err != nil {
		t.Fatalf("real native %s: %v\n%s", request.RawLocation.Key, err, &diagnostics)
	}
	if active {
		if loadErr := <-loadDone; loadErr != nil {
			t.Fatalf("off-node load process: %v\n%s", loadErr, &loadDiagnostics)
		}
	}
	committed := writer.objects[receipt.Object.Location()]
	if _, err := writer.PutImmutable(ctx, receipt.Object, bytes.NewReader(committed)); !errors.Is(err, evidencestore.ErrPrecondition) {
		t.Fatalf("raw commit was replaceable: %v", err)
	}
	output, err := os.ReadFile(path("runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	return receipt.Object, output, started
}

func assertNativeActiveTrial(
	t *testing.T, podUID string, wireConfig profilingrun.RunConfig,
	committed, output []byte, samples []firstfloor.ServiceSample,
) (profilerevidence.RawMeasurement, []firstfloor.ServiceSample) {
	t.Helper()
	var raw profilerevidence.RawMeasurement
	var runtime profilingrun.RuntimeResult
	if err := json.Unmarshal(committed, &raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output, &runtime); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw.Runtime, runtime) || !reflect.DeepEqual(raw.RunConfig, wireConfig) ||
		runtime.Firstfloor == nil || runtime.Firstfloor.Runtime.Synthetic ||
		runtime.MeasurementElapsedSeconds < float64(wireConfig.Measurement.DurationSeconds) {
		t.Fatalf("active raw/native runtime equal=%t serialized config equal=%t elapsed=%g metadata=%+v",
			reflect.DeepEqual(raw.Runtime, runtime), reflect.DeepEqual(raw.RunConfig, wireConfig),
			runtime.MeasurementElapsedSeconds, runtime.Firstfloor)
	}
	if len(runtime.Phases) != len(wireConfig.Measurement.OperatingPoints.Utilizations)+2 {
		t.Fatalf("native runtime lost operating-point phases: %+v", runtime.Phases)
	}
	post := runtime.PostLoad
	if post == nil || post.ActorPID <= 0 || !post.ModelResidentAtEnd ||
		post.CountersBefore != post.CountersAfter ||
		post.DurationSecondsMonotonic < float64(wireConfig.Measurement.OperatingPoints.PostLoadDurationSeconds) ||
		post.EndedAt != runtime.MeasurementEndedAt {
		t.Fatalf("native actor post-load continuity or zero-inference proof lost: %+v", post)
	}
	postStart, err := time.Parse(time.RFC3339Nano, post.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	postPower := 0
	for _, sample := range raw.Metrics.Samples {
		if !sample.ObservedAt.Before(postStart) {
			postPower++
		}
	}
	if postPower == 0 {
		t.Fatal("native raw lost the controlled post-load power series")
	}
	for phaseIndex, phase := range runtime.Phases {
		known := 0
		if phase.MeasurementElapsedSeconds < float64(wireConfig.Measurement.OperatingPoints.PhaseDurationSeconds) {
			t.Fatalf("phase %s stopped before its duration floor", phase.ID)
		}
		for index, sample := range phase.NativeSamples {
			if phaseIndex == 0 && index == 0 && sample.PrecedingIdleNanos != nil {
				t.Fatal("physical startup invented a preceding gap")
			}
			if sample.TrialID != podUID || sample.ServiceDurationNanos <= 0 {
				t.Fatal("native sample lost its physical trial or measured service")
			}
			if sample.PrecedingIdleNanos != nil {
				known++
			}
			samples = append(samples, sample.ServiceSample.Clone())
		}
		if known < wireConfig.Measurement.MinServiceSamples {
			t.Fatalf("phase %s supplied only %d known native gaps", phase.ID, known)
		}
	}
	return raw, samples
}

func assertNativeIdleTrial(
	t *testing.T, wireConfig profilingrun.RunConfig, committed, output []byte,
) profilerevidence.IdleRawMeasurement {
	t.Helper()
	var raw profilerevidence.IdleRawMeasurement
	var runtime profilingrun.IdleRuntimeResult
	if err := json.Unmarshal(committed, &raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output, &runtime); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw.Runtime, runtime) || !reflect.DeepEqual(raw.RunConfig, wireConfig) ||
		runtime.Firstfloor == nil || runtime.Firstfloor.Runtime.Synthetic || runtime.InferenceCountAfterModelLoad != 0 {
		t.Fatal("idle raw commit changed actual native observations or frozen config")
	}
	return raw
}

func assertNativeCatalogConsumer(
	t *testing.T, ctx context.Context, profile *serving.Profile, plan profilederivation.CurvePlan,
	planIdentity evidencestore.ObjectIdentity, samples []firstfloor.ServiceSample,
	firstMetadata *firstfloor.RuntimeMetadata, memory uint64, energy float64, trials int,
) {
	t.Helper()
	frozen := plan.Shared()
	if len(profile.Batches) != 1 {
		t.Fatalf("catalog returned %d batches, want the one measured batch", len(profile.Batches))
	}
	batch := profile.Batches[0]
	// The admitted raw facts are also the actual scheduler/conditional input.
	if batch.ServiceDistribution == nil {
		t.Fatal("catalog lost native service distribution")
	}
	if !reflect.DeepEqual(batch.ServiceSamples, samples) {
		for index := range min(len(batch.ServiceSamples), len(samples)) {
			if !reflect.DeepEqual(batch.ServiceSamples[index], samples[index]) {
				t.Fatalf("catalog service pair %d: got=%+v want=%+v", index, batch.ServiceSamples[index], samples[index])
			}
		}
		t.Fatalf("catalog service population: got=%d want=%d", len(batch.ServiceSamples), len(samples))
	}
	if !reflect.DeepEqual(profile.RuntimeMetadata, firstMetadata) {
		t.Fatalf("catalog runtime observations: got=%+v want=%+v", profile.RuntimeMetadata, firstMetadata)
	}
	if len(profile.Evidence) == 0 || profile.Evidence[0] != planIdentity {
		t.Fatalf("catalog plan identity: got=%+v want=%+v", profile.Evidence, planIdentity)
	}
	if !reflect.DeepEqual(profile.Condition.Actor, frozen.Actor) {
		t.Fatalf("catalog actor recipe: got=%+v want=%+v", profile.Condition.Actor, frozen.Actor)
	}
	if profile.TrialsPerBatch != trials {
		t.Fatalf("catalog trial count: got=%d want=%d", profile.TrialsPerBatch, trials)
	}
	if !nativeCatalogMeterEquivalent(profile.IdleBaselineWatts, 4) {
		t.Fatalf("catalog idle W: got=%g want=4", profile.IdleBaselineWatts)
	}
	if !nativeCatalogMeterEquivalent(batch.MeanWallWatts, 6) {
		t.Fatalf("catalog active W: got=%.17g want=6", batch.MeanWallWatts)
	}
	if !nativeCatalogMeterEquivalent(batch.EnergyPerInputJoules, energy/float64(trials)) {
		t.Fatalf("catalog incremental J/input: got=%.17g want=%.17g",
			batch.EnergyPerInputJoules, energy/float64(trials))
	}
	if batch.MemoryBytes != memory || memory == 0 {
		t.Fatalf("catalog memory bytes: got=%d want=%d (nonzero)", batch.MemoryBytes, memory)
	}
	assertNativeCatalogCapacity(t, ctx, profile, plan)
}

func nativeCatalogMeterEquivalent(got, want float64) bool {
	// Unequal wall-clock sample gaps accumulate float64 integration roundoff.
	return math.Abs(got-want) <= 1e-9
}

func TestNativeCatalogMeterEquivalenceRetainsIntegrationPrecision(t *testing.T) {
	if !nativeCatalogMeterEquivalent(3.9999999999999987, 4) {
		t.Fatal("rejected constant idle power after native timestamp integration")
	}
	for _, got := range []float64{4.000001, math.NaN(), math.Inf(1)} {
		if nativeCatalogMeterEquivalent(got, 4) {
			t.Fatalf("accepted different or non-finite idle power: %.17g", got)
		}
	}
}

func assertNativeCatalogCapacity(t *testing.T, ctx context.Context, profile *serving.Profile, plan profilederivation.CurvePlan) {
	t.Helper()
	frozen := plan.Shared()
	batch := profile.Batches[0]
	settings := serving.SchedulerSettings{CapacityWindow: time.Millisecond, CapacityWindows: 1, CapacityPassingWindows: 1,
		CapacityMinimumRequests: 100, CapacityGridSteps: 4, CapacityBisectionWidth: .1, CapacityTailSamples: 5}
	// Replay applies the same full model/input/dataset predicate as serving.
	// A UID-only Definition is not a compatible workload, regardless of route flags.
	c := profile.Condition
	workload := &serving.Definition{Name: frozen.ActiveProtocol.WorkloadName,
		UID: frozen.ActiveProtocol.WorkloadUID, Generation: 1,
		Admission: serving.Admission{Policy: serving.AdmissionPolicyReject, MaxQueuedRequests: 1024},
		Model: serving.Model{Name: c.LogicalModel, Paths: []serving.ModelPath{{
			Name: c.Artifact.ExecutionPath, Artifact: c.Artifact.Name, Files: c.Artifact.Files, Image: c.Artifact.Image,
			RuntimeFamily: c.RuntimeFamily, Backend: c.Backend, Firstfloor: c.Firstfloor,
			Preprocessor: serving.PreprocessorArtifact{Image: c.Preprocessor.Image,
				Architecture: c.Preprocessor.Architecture, Path: c.Preprocessor.Path},
		}}},
		Preprocessor: serving.Preprocessor{Name: c.Preprocessor.Name, MaxNewTokens: int32(c.Preprocessor.MaxNewTokens)},
		Dataset:      &serving.DatasetReference{Name: c.ActiveProtocol.Dataset.Name, UID: c.ActiveProtocol.Dataset.UID},
		SLO: serving.SLO{Percentile: frozen.ActiveProtocol.SLOPercentile,
			TargetLatency: time.Duration(frozen.ActiveProtocol.SLOTargetLatencyNanos)}}
	for _, name := range c.Preprocessor.Inputs {
		workload.Preprocessor.Input = append(workload.Preprocessor.Input, serving.RawField{Name: name})
	}
	for _, name := range c.Preprocessor.Outputs {
		workload.Preprocessor.Output = append(workload.Preprocessor.Output, serving.RawField{Name: name})
	}
	option := serving.SchedulerOption{Instance: serving.SchedulerInstance{ID: "native-catalog-option", Profile: profile,
		Node: serving.NodeReference{Name: profile.NodeName, UID: frozen.NodeUID}, MaxBatch: 1,
		Routable: map[string]bool{workload.UID: true}, Capacity: int(c.Firstfloor.MaxQueueSize),
		Allocation: serving.SchedulerAllocation{MemoryBytes: batch.MemoryBytes}},
		CompatibleUIDs: []string{workload.UID}}
	row, err := serving.BuildCapacityRow(ctx, settings, nil, option, workload, time.Now().UTC())
	if err != nil || row.Status != serving.CapacityBuilt || row.Kappa == nil || *row.Kappa < 0 ||
		row.Input.ProfileID != profile.ID || len(row.Grid) == 0 || row.ConditionalService == nil ||
		row.ConditionalService.Queries == 0 || row.ConditionalService.StartupQueries == 0 ||
		row.ConditionalService.MinimumTrialCount != profile.TrialsPerBatch {
		t.Fatalf("kappa did not consume the catalog's physical native populations: %+v %v", row, err)
	}
}

// This is a fast controlled-fixture qualification test, not native measurement.
// It catches downstream option/definition omissions without rerunning six trials.
func TestProfileIndependentCapacityFixtureQualification(t *testing.T) {
	ctx := context.Background()
	catalog, source, _, _ := catalogEvidenceFixture(t)
	campaign := &edgev1alpha1.BatchEnergyOptimization{}
	if err := catalog.store.client.Get(ctx, source, campaign); err != nil {
		t.Fatal(err)
	}
	retained, err := catalog.evidence.Read(ctx, campaign)
	if err != nil {
		t.Fatal(err)
	}
	view, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 {
		t.Fatalf("controlled qualification fixture: %v %+v", err, view.Insufficiencies)
	}
	assertNativeCatalogCapacity(t, ctx, &view.Profiles[0], retained.Plan)
}

type nativeCatalogMetricReader func(context.Context) (metricobserver.VerifiedResult, error)

func (read nativeCatalogMetricReader) Read(ctx context.Context) (metricobserver.VerifiedResult, error) {
	return read(ctx)
}

type nativeCatalogWorkloadReader func(context.Context) (workloadobserver.VerifiedResult, error)

func (read nativeCatalogWorkloadReader) Read(ctx context.Context) (workloadobserver.VerifiedResult, error) {
	return read(ctx)
}

// The local object adapter performs an exclusive-create disk commit before
// exposing bytes to the existing strict catalog reader; replacement is refused.
type nativeCatalogImmutableWriter struct {
	directory string
	objects   catalogEvidenceObjects
}

func (writer nativeCatalogImmutableWriter) PutImmutable(ctx context.Context, identity evidencestore.ObjectIdentity, input io.Reader) (evidencestore.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return evidencestore.Receipt{}, err
	}
	if err := identity.Validate(); err != nil {
		return evidencestore.Receipt{}, err
	}
	payload, err := io.ReadAll(io.LimitReader(input, identity.Size+1))
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	if int64(len(payload)) != identity.Size {
		return evidencestore.Receipt{}, evidencestore.ErrContentMismatch
	}
	file, err := os.OpenFile(filepath.Join(writer.directory, filepath.Base(identity.Key)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return evidencestore.Receipt{}, evidencestore.ErrPrecondition
	}
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return evidencestore.Receipt{}, err
	}
	if err := file.Close(); err != nil {
		return evidencestore.Receipt{}, err
	}
	writer.objects[identity.Location()] = payload
	return evidencestore.Receipt{SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: evidencestore.CreateOutcomeCreated}, nil
}

func nativeCatalogCommit(t *testing.T, ctx context.Context, writer nativeCatalogImmutableWriter, key, schema string, payload []byte) batchenergyoptimization.StoredObjectReference {
	t.Helper()
	receipt, err := writer.PutImmutable(ctx, evidencestore.ObjectIdentity{Store: "evidence", Key: key, Size: int64(len(payload))}, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("commit %s: %v", key, err)
	}
	return batchenergyoptimization.StoredObjectReference{Store: receipt.Object.Store, Key: receipt.Object.Key, Size: receipt.Object.Size, SchemaVersion: schema}
}

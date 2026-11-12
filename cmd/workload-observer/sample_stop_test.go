package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"
)

// The executable, source reads and same-watch handoff use real wall time.
// Inference below is explicitly simulated, not hardware evidence. There is no
// requested idle: each phase retains 60 physical calls and its natural-gap pairs.
func TestStandaloneObserversCoverSampleLimitedTrial(t *testing.T) {
	runStandaloneSampleStop(t, 2*time.Second)
}

func runStandaloneSampleStop(t *testing.T, service time.Duration) {
	t.Helper()
	binary := os.Getenv("CHILL_METRIC_OBSERVER_BIN")
	if binary == "" {
		t.Skip("set CHILL_METRIC_OBSERVER_BIN to the built standalone observer")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	opts, config := sampleStopConfiguration(t, directory)
	server, firstRead, delayFinalRead := sampleStopMetricSource(config)
	defer server.Close()
	config.Metrics.Endpoint, config.Metrics.ThermalEndpoint = server.URL, server.URL
	plan := sampleStopExecutionPlan(t, config)
	payload, err := config.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	opts.runConfig = string(payload)
	opts.endWaitTimeout = plan.AttemptTimeout
	opts.endFenceTimeout = plan.WorkloadEndFenceTimeout
	opts.maxEvents, opts.maxResultBytes = plan.MaxWorkloadEvents, plan.MaxWorkloadResultBytes
	ctx, cancel := context.WithTimeout(context.Background(), plan.AttemptTimeout)
	defer cancel()
	pod := workloadTestPod()
	client := fake.NewSimpleClientset(pod.DeepCopy())
	retained := watch.NewRaceFreeFake()
	preflightOnly := pod.DeepCopy()
	preflightOnly.Name, preflightOnly.UID = "preflight-only", "preflight-only-uid"
	preflightOnly.OwnerReferences = nil
	installPodObservationReactors(
		t, client, *pod, *preflightOnly, make(chan string, 2), watch.NewRaceFreeFake(), retained)
	workloadDone := make(chan error, 1)
	workloadExited := make(chan struct{})
	go func() {
		defer close(workloadExited)
		workloadDone <- execute(ctx, opts, client.CoreV1(), time.Millisecond)
	}()
	defer func() {
		cancel()
		<-workloadExited
	}()
	waitForFile(t, opts.readyPath)
	if err := worker.PublishSignal(opts.windowStartPath); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, opts.windowReadyPath)
	metricReady := filepath.Join(directory, "metric-ready")
	metricStart := filepath.Join(directory, "measurement-start")
	metricResult := filepath.Join(directory, "metric-result.json")
	command := exec.CommandContext(ctx, binary,
		"--run-config="+opts.runConfig, "--ready-file="+metricReady,
		"--start-signal-file="+metricStart, "--end-signal-file="+opts.endPath,
		"--runtime-failed-file="+opts.runtimeFailedPath, "--result-file="+metricResult,
		"--start-wait-timeout="+plan.ObserverStartTimeout.String(),
		"--end-wait-timeout="+plan.MetricObservationTimeout.String(),
		"--memory-budget-bytes="+strconv.FormatInt(plan.ObserverMemoryLimitBytes, 10),
		"--max-result-bytes="+strconv.FormatInt(plan.MaxMetricResultBytes, 10))
	metricDone := make(chan error, 1)
	metricExited := make(chan struct{})
	go func() {
		defer close(metricExited)
		output, err := command.CombinedOutput()
		if err != nil {
			err = fmt.Errorf("%w: %s", err, output)
		}
		metricDone <- err
	}()
	defer func() {
		cancel()
		<-metricExited
	}()
	if err := worker.WaitForSignal(ctx, metricReady, plan.ObserverReadyTimeout, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := worker.PublishSignal(metricStart); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstRead:
	case err := <-metricDone:
		t.Fatalf("metric observer ended before its first source read: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Give the initial real source response time to be retained before the
	// producer starts, without changing the frozen one-second polling interval.
	time.Sleep(time.Duration(config.Metrics.IntervalMilliseconds) * time.Millisecond)
	var started, ended time.Time
	var samples []profilingrun.NativeServiceSample
	producerEnded := make(chan struct{})
	runtime := sampleStopRuntimeFunc(func(
		runtimeCtx context.Context, request worker.RuntimeRequest,
	) (worker.RuntimeMeasurement, error) {
		started, ended, samples = runSampleStopProducer(t, runtimeCtx, config, opts.podUID, service, metricDone, workloadDone)
		delayFinalRead(ended)
		if err := worker.PublishSignal(opts.endPath); err != nil {
			return worker.RuntimeMeasurement{}, err
		}
		close(producerEnded)
		return sampleStopRuntime(t, directory, opts, config, plan, started, ended, samples).Measure(runtimeCtx, request)
	})
	fenceDone := make(chan error, 1)
	go func() {
		select {
		case <-producerEnded:
		case <-ctx.Done():
			fenceDone <- ctx.Err()
			return
		}
		select {
		case err := <-metricDone:
			if err != nil {
				fenceDone <- err
				return
			}
		case <-ctx.Done():
			fenceDone <- ctx.Err()
			return
		}
		// Only successful real metric-process exit supplies the same-watch fence.
		fence := pod.DeepCopy()
		fence.ResourceVersion = "21"
		fence.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  endFenceContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
		}}
		retained.Modify(fence)
		fenceDone <- <-workloadDone
	}()
	defer func() {
		wasLive := ctx.Err() == nil
		cancel()
		if err := <-fenceDone; wasLive && err != nil {
			t.Errorf("metric-exit workload fence: %v", err)
		}
	}()
	sampleStopCommit(t, ctx, runtime, config, opts, metricResult, directory, plan)
	assertSampleStopCoverage(t, metricResult, opts.resultPath, started, ended, len(samples), plan)
}

func sampleStopConfiguration(t *testing.T, directory string) (options, profilingrun.RunConfig) {
	t.Helper()
	writeWorkloadTestRunConfig(t, directory)
	opts := workloadTestOptions(directory)
	var config profilingrun.RunConfig
	if err := json.Unmarshal([]byte(opts.runConfig), &config); err != nil {
		t.Fatal(err)
	}
	sample, err := os.ReadFile("../../config/samples/profiling/edge_v1alpha1_batchenergyoptimization.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var campaign edgev1alpha1.BatchEnergyOptimization
	if err := yaml.Unmarshal(sample, &campaign); err != nil {
		t.Fatal(err)
	}
	protocol := campaign.Spec.ActiveProtocol
	config.Measurement.OperatingPoints.PhaseDurationSeconds = int(protocol.OperatingPoints.PhaseDurationSeconds)
	config.Measurement.OperatingPoints.PostLoadDurationSeconds = int(protocol.OperatingPoints.PostLoadDurationSeconds)
	config.Measurement.DurationSeconds = config.Measurement.OperatingPoints.PhaseDurationSeconds*
		(len(config.Measurement.OperatingPoints.Utilizations)+2) + config.Measurement.OperatingPoints.PostLoadDurationSeconds
	config.Metrics.DurationSeconds = config.Measurement.DurationSeconds + 1
	config.Metrics.IntervalMilliseconds = int(protocol.MetricObservation.IntervalMilliseconds)
	config.Metrics.TimeoutMilliseconds = int(protocol.MetricObservation.TimeoutMilliseconds)
	config.Measurement.OperatingPoints.QueueBytesCap = 64 << 20
	config.Measurement.OperatingPoints.FramedDecodedRequestUpperBytes = (64 << 20) / 32
	if config.Measurement.DurationSeconds != 120 || config.Metrics.DurationSeconds != 121 ||
		config.Measurement.MinServiceSamples != 59 {
		t.Fatal("regression requires three shipped 30-second phase floors, 30-second post-load and p95 n59 stop")
	}
	return opts, config
}

func assertSampleStopCoverage(
	t *testing.T, metricResult, workloadResult string, started, ended time.Time,
	calls int, plan profiler.ExecutionPlan,
) {
	t.Helper()
	metricPayload, err := os.ReadFile(metricResult)
	if err != nil {
		t.Fatal(err)
	}
	var metrics metricobserver.Result
	if err := json.Unmarshal(metricPayload, &metrics); err != nil {
		t.Fatal(err)
	}
	if err := metrics.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(metrics.Failures) < 2 || metrics.Summary.Failures != len(metrics.Failures) ||
		metrics.Summary.Attempts != len(metrics.Samples)+len(metrics.Failures) ||
		metrics.Summary.SuccessfulSamples != len(metrics.Samples) ||
		len(metrics.Samples) < int(ended.Sub(started)/time.Second) ||
		metrics.Samples[0].ObservedAt.After(started) ||
		metrics.Samples[len(metrics.Samples)-1].ObservedAt.Before(ended.Add(35*time.Second)) ||
		metrics.Summary.ObservationDurationSeconds < ended.Sub(started).Seconds()+35 {
		t.Fatalf("metric source does not cover the sample-limited producer plus 35s final-read retries: %+v", metrics)
	}
	for _, failure := range metrics.Failures {
		if failure.ObservedAt.Before(ended) || failure.Reason != metricobserver.FailureReasonSourceRead {
			t.Fatalf("unexpected final-read failure telemetry: %+v", failure)
		}
	}
	workloadPayload, err := os.ReadFile(workloadResult)
	if err != nil {
		t.Fatal(err)
	}
	var workloads workloadobserver.Result
	if err := json.Unmarshal(workloadPayload, &workloads); err != nil {
		t.Fatal(err)
	}
	if err := workloads.Validate(); err != nil {
		t.Fatal(err)
	}
	if !workloads.Continuous || workloads.CoverageStartedAt.After(started) ||
		workloads.CoverageEndedAt.Before(metrics.EndedAt) || workloads.LastResourceVersion != "21" {
		t.Fatalf("workload coverage lost the producer or metric-exit fence: %+v", workloads)
	}
	t.Logf("producer calls=%d known-gap pairs=%d elapsed=%s "+
		"final unavailable=35s failures=%d metric support=%gs workload support=%s failure bound=%s",
		calls, calls-1, ended.Sub(started), len(metrics.Failures), metrics.Summary.ObservationDurationSeconds,
		workloads.CoverageEndedAt.Sub(workloads.CoverageStartedAt), plan.AttemptTimeout)
}

func runSampleStopProducer(
	t *testing.T, ctx context.Context, config profilingrun.RunConfig, trialID string, service time.Duration,
	metricDone, workloadDone <-chan error,
) (time.Time, time.Time, []profilingrun.NativeServiceSample) {
	t.Helper()
	started := time.Now()
	var samples []profilingrun.NativeServiceSample
	wait := func(duration time.Duration) {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				return
			case err := <-metricDone:
				t.Fatalf("metric observer ended while producer active at call %d, elapsed %s: %v",
					len(samples), time.Since(started), err)
			case err := <-workloadDone:
				t.Fatalf("workload observer ended while producer active at call %d: %v", len(samples), err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	recordIDs, units := make([]string, config.Measurement.Batch), make([]int, config.Measurement.Batch)
	for index := range recordIDs {
		recordIDs[index], units[index] = "record-"+strconv.Itoa(index), 1
	}
	var previousEnd time.Time
	phaseCount := len(config.Measurement.OperatingPoints.Utilizations) + 2
	for range phaseCount {
		phaseStarted, offset := time.Now(), len(samples)
		for time.Since(phaseStarted) < time.Duration(config.Measurement.OperatingPoints.PhaseDurationSeconds)*time.Second ||
			len(samples)-offset-1 < config.Measurement.MinServiceSamples {
			pair := firstfloor.ServiceSample{TrialID: trialID}
			exchangeStart := time.Now()
			if !previousEnd.IsZero() {
				actualGap := exchangeStart.Sub(previousEnd).Nanoseconds()
				pair.PrecedingIdleNanos = &actualGap
			}
			wait(service)
			previousEnd = time.Now()
			pair.ServiceDurationNanos = previousEnd.Sub(exchangeStart).Nanoseconds()
			serviceMS := float64(pair.ServiceDurationNanos) / 1e6
			samples = append(samples, profilingrun.NativeServiceSample{
				BatchID: uint64(len(samples) + 1), BatchSize: config.Measurement.Batch,
				RecordIDs: recordIDs, Units: units, ServiceSample: pair,
				RunnerServiceMilliseconds: serviceMS, BackendCallMilliseconds: serviceMS, NativeRequestMilliseconds: serviceMS,
				StartedAt: exchangeStart, FinishedAt: previousEnd, LatestEnqueuedAt: exchangeStart, EarliestEnqueuedAt: exchangeStart,
			})
		}
	}
	wait(time.Duration(config.Measurement.OperatingPoints.PostLoadDurationSeconds) * time.Second)
	ended := time.Now()
	if len(samples) != 60*phaseCount {
		t.Fatalf("producer stop: calls=%d expected=%d elapsed=%s", len(samples), 60*phaseCount, ended.Sub(started))
	}
	return started, ended, samples
}

func sampleStopExecutionPlan(t *testing.T, config profilingrun.RunConfig) profiler.ExecutionPlan {
	t.Helper()
	data, err := os.ReadFile("../../charts/chill/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Profiling struct {
			Defaults profiler.ExecutionPolicySpec `json:"executionPolicyDefaults"`
		} `json:"profiling"`
	}
	if err := yaml.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	policy := values.Profiling.Defaults
	policy.Name, policy.DeviceClass = "sample-stop-regression", "*"
	policy.ExecutionPath, policy.WorkloadMode = config.Candidate.ExecutionPath, config.Measurement.WorkloadMode
	policy.AttemptTimeoutOverheadSeconds = max(policy.AttemptTimeoutOverheadSeconds, 420)
	policy.ActiveDeadlineOverheadSeconds = max(policy.ActiveDeadlineOverheadSeconds, policy.AttemptTimeoutOverheadSeconds+30)
	catalog, err := yaml.Marshal(profiler.ExecutionPolicyCatalog{
		SchemaVersion: profiler.ExecutionPolicyCatalogSchemaVersion,
		Policies:      []profiler.ExecutionPolicySpec{policy},
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := profiler.FreezeConfiguredExecutionPolicy(catalog, config, profiler.ExecutionPolicySource{
		Kind: profiler.ConfigMapExecutionPolicySourceKind, Namespace: "chill-system",
		Name: "sample-stop-regression", UID: "policy-uid", ResourceVersion: "1", Key: "execution-policies.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := profiler.DeriveExecutionPlan(config, frozen)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func sampleStopMetricSource(config profilingrun.RunConfig) (
	*httptest.Server, <-chan struct{}, func(time.Time),
) {
	firstRead := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var unavailableUntil time.Time
	delayFinalRead := func(ended time.Time) {
		mu.Lock()
		unavailableUntil = ended.Add(35 * time.Second)
		mu.Unlock()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		unavailable := time.Now().Before(unavailableUntil)
		mu.Unlock()
		if unavailable {
			// Fail promptly: every real HTTP request retains its frozen 500ms timeout.
			http.Error(w, "simulated final power source temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		// The real source requires an explicitly typed GAUGE power family;
		// untyped exposition would fail every read and exhaust final retries.
		_, _ = fmt.Fprintf(w, "# TYPE %s gauge\n%s{hostname=\"lattepanda\",device_type=\"test_device\"} 5.2\n"+
			"# TYPE %s gauge\n%s 1\n# TYPE %s gauge\n%s 0\n"+
			"# TYPE %s counter\n%s 0\n# TYPE %s counter\n%s 0\n"+
			"# TYPE %s gauge\n%s 0\n# TYPE %s gauge\n%s 40\n",
			config.Metrics.PowerMetric, config.Metrics.PowerMetric,
			config.Metrics.ThermalMetrics.ObservationAvailable, config.Metrics.ThermalMetrics.ObservationAvailable,
			config.Metrics.ThermalMetrics.Active, config.Metrics.ThermalMetrics.Active,
			config.Metrics.ThermalMetrics.CPUCoreThrottleEvents, config.Metrics.ThermalMetrics.CPUCoreThrottleEvents,
			config.Metrics.ThermalMetrics.CPUPackageThrottleEvents, config.Metrics.ThermalMetrics.CPUPackageThrottleEvents,
			config.Metrics.ThermalMetrics.Latched, config.Metrics.ThermalMetrics.Latched,
			config.Metrics.ThermalMetrics.MaximumTemperatureCelsius, config.Metrics.ThermalMetrics.MaximumTemperatureCelsius)
		once.Do(func() { close(firstRead) })
	}))
	return server, firstRead, delayFinalRead
}

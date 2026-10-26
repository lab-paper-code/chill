package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRetainedLoaderUsesRecordedJobBindingAndCredentials(t *testing.T) {
	t.Parallel()
	run, job, secret, reference := retainedLoaderFixture()
	want := errors.New("reader unavailable")
	var gotBinding storekube.Binding
	var gotCredentials storekube.Credentials
	loader, err := NewRetainedLoader(
		fake.NewClientBuilder().WithScheme(retainedLoaderScheme(t)).WithObjects(job, secret).Build(),
		func(binding storekube.Binding, credentials storekube.Credentials) (evidencestore.LocationReader, error) {
			gotBinding, gotCredentials = binding, credentials
			return nil, want
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = loader.Load(context.Background(), run, reference)
	if !errors.Is(err, want) {
		t.Fatalf("Load() error = %v", err)
	}
	if gotBinding.Store != reference.Store || gotBinding.Endpoint != "http://store:9000" ||
		gotCredentials.AccessKeyID != "access" || gotCredentials.SecretAccessKey != "secret" {
		t.Fatalf("binding/credentials = %#v, %#v", gotBinding, gotCredentials)
	}
}

func TestRetainedLoaderRejectsJobOrStoreIdentityMismatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*edgev1alpha1.ProfilingRun, *batchv1.Job, *evidencestore.ObjectIdentity)
		match  string
	}{
		{
			name: "Job UID drift",
			mutate: func(_ *edgev1alpha1.ProfilingRun, job *batchv1.Job, _ *evidencestore.ObjectIdentity) {
				job.UID = "replacement"
			},
			match: "does not match the recorded Run",
		},
		{
			name: "logical store drift",
			mutate: func(_ *edgev1alpha1.ProfilingRun, _ *batchv1.Job, reference *evidencestore.ObjectIdentity) {
				reference.Store = "other-store"
			},
			match: "does not match the retained Evidence Store binding",
		},
		{
			name: "retained status store drift",
			mutate: func(run *edgev1alpha1.ProfilingRun, _ *batchv1.Job, _ *evidencestore.ObjectIdentity) {
				run.Status.EvidenceStore = retainedStatusBinding()
				run.Status.EvidenceStore.Store = "mismatched-store"
			},
			match: "does not match the retained Evidence Store binding",
		},
		{
			name: "invalid retained status binding does not fall back",
			mutate: func(run *edgev1alpha1.ProfilingRun, _ *batchv1.Job, _ *evidencestore.ObjectIdentity) {
				run.Status.EvidenceStore = retainedStatusBinding()
				run.Status.EvidenceStore.Endpoint = ""
			},
			match: "binding endpoint is required",
		},
		{
			name: "missing retained credentials does not fall back",
			mutate: func(run *edgev1alpha1.ProfilingRun, _ *batchv1.Job, _ *evidencestore.ObjectIdentity) {
				run.Status.EvidenceStore = retainedStatusBinding()
				run.Status.EvidenceStore.CredentialsSecretName = "missing"
			},
			match: "not found",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			run, job, secret, reference := retainedLoaderFixture()
			test.mutate(run, job, &reference)
			loader, err := NewRetainedLoader(
				fake.NewClientBuilder().WithScheme(retainedLoaderScheme(t)).WithObjects(job, secret).Build(),
				func(storekube.Binding, storekube.Credentials) (evidencestore.LocationReader, error) {
					t.Fatal("invalid identity reached store construction")
					return nil, nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = loader.Load(context.Background(), run, reference)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Load() error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestNewRetainedLoaderRequiresDependencies(t *testing.T) {
	t.Parallel()
	reader := fake.NewClientBuilder().WithScheme(retainedLoaderScheme(t)).Build()
	if _, err := NewRetainedLoader(nil, func(storekube.Binding, storekube.Credentials) (evidencestore.LocationReader, error) {
		return nil, nil
	}); err == nil {
		t.Fatal("nil Kubernetes reader was accepted")
	}
	if _, err := NewRetainedLoader(reader, nil); err == nil {
		t.Fatal("nil store factory was accepted")
	}
}

func TestValidateRunEvidenceBindingBindsActuationRun(t *testing.T) {
	t.Parallel()
	run := &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "run", UID: "run-uid"},
		Status: edgev1alpha1.ProfilingRunStatus{
			Job: &edgev1alpha1.ProfilingRunObjectReference{Name: "job", UID: "job-uid"},
			ObservedExecution: &edgev1alpha1.ProfilingRunObservedExecutionStatus{
				Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "pod", UID: "pod-uid"},
				Node: edgev1alpha1.ProfilingRunObjectReference{Name: "node", UID: "node-uid"},
			},
			Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{
				SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
			},
		},
	}
	execution := profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		RunConfig: profilingrun.RunConfig{
			Actuation: &profilingrun.ActuationContract{},
		},
		Terminal: profilerevidence.TerminalFacts{
			Job:  profilerevidence.JobTerminalFacts{Namespace: run.Namespace, Name: "job", UID: "job-uid"},
			Pod:  profilerevidence.PodTerminalFacts{Namespace: run.Namespace, Name: "pod", UID: "pod-uid"},
			Node: profilerevidence.NodeTerminalFacts{Name: "node", UID: "node-uid"},
		},
		Actuation: &profilerevidence.ActuationFacts{
			Run: profilerevidence.NamespacedObjectFacts{
				Namespace: run.Namespace, Name: run.Name, UID: string(run.UID),
			},
			Handoff: &profilerevidence.NamespacedObjectFacts{
				Namespace: run.Namespace, Name: "handoff", UID: "handoff-uid",
			},
		},
	}

	if err := validateRunEvidenceBinding(run, execution); err != nil {
		t.Fatalf("validateRunEvidenceBinding() error = %v", err)
	}

	execution.Actuation.Run.UID = "replacement-uid"
	if err := validateRunEvidenceBinding(run, execution); err == nil ||
		!strings.Contains(err.Error(), "does not belong to the recorded Run") {
		t.Fatalf("validateRunEvidenceBinding() error = %v, want Run mismatch", err)
	}
}

func retainedLoaderFixture() (
	*edgev1alpha1.ProfilingRun,
	*batchv1.Job,
	*corev1.Secret,
	evidencestore.ObjectIdentity,
) {
	run := &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "run", UID: "run-uid"},
		Status: edgev1alpha1.ProfilingRunStatus{
			Job: &edgev1alpha1.ProfilingRunObjectReference{Name: "run-job", UID: "job-uid"},
		},
	}
	secretEnv := func(name, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "store-credentials"}, Key: key,
		}}}
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: run.Namespace, Name: run.Status.Job.Name, UID: "job-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "ProfilingRun",
				Name: run.Name, UID: run.UID, Controller: ptr.To(true),
			}},
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "runtime",
				Args: []string{
					"--store-name=chill-evidence", "--store-endpoint=http://store:9000",
					"--store-bucket=profiling", "--store-region=us-east-1",
				},
				Env: []corev1.EnvVar{
					secretEnv("AWS_ACCESS_KEY_ID", component.EvidenceStoreAccessKeyKey),
					secretEnv("AWS_SECRET_ACCESS_KEY", component.EvidenceStoreSecretKeyKey),
				},
			}},
		}}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: "store-credentials"},
		Data: map[string][]byte{
			component.EvidenceStoreAccessKeyKey: []byte("access"),
			component.EvidenceStoreSecretKeyKey: []byte("secret"),
		},
	}
	reference := evidencestore.ObjectIdentity{
		Store: "chill-evidence", Key: "execution.json",
		Size: 100,
	}
	return run, job, secret, reference
}

func retainedLoaderScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{batchv1.AddToScheme, corev1.AddToScheme, edgev1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func retainedStatusBinding() *edgev1alpha1.ProfilingRunEvidenceStoreBinding {
	return &edgev1alpha1.ProfilingRunEvidenceStoreBinding{
		Store: "chill-evidence", Endpoint: "http://store:9000", Bucket: "profiling",
		Region: "us-east-1", CredentialsSecretName: "store-credentials",
	}
}

func TestRetainedLoaderLoadsRetainedAndLegacyEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		idle   bool
		legacy bool
	}{
		{name: "active after Job deletion"},
		{name: "idle after Job deletion", idle: true},
		{name: "legacy active owned Job", legacy: true},
		{name: "legacy idle owned Job", idle: true, legacy: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			run, job, secret, _ := retainedLoaderFixture()
			if !test.legacy {
				run.Status.EvidenceStore = retainedStatusBinding()
			}
			objects, reference, execution := retainedEvidenceObjects(t, test.idle)
			run.Status.Job.Name = execution.Terminal.Job.Name
			run.Status.Evidence = &edgev1alpha1.ProfilingRunEvidenceReference{
				Store: reference.Store, Key: reference.Key, Size: reference.Size,
				SchemaVersion: execution.SchemaVersion,
			}
			run.Status.ObservedExecution = &edgev1alpha1.ProfilingRunObservedExecutionStatus{
				Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: execution.Terminal.Pod.Name, UID: execution.Terminal.Pod.UID},
				Node: edgev1alpha1.ProfilingRunObjectReference{Name: execution.Terminal.Node.Name, UID: execution.Terminal.Node.UID},
			}
			job.Name = run.Status.Job.Name
			reader := fake.NewClientBuilder().WithScheme(retainedLoaderScheme(t)).WithObjects(job, secret).Build()
			if !test.legacy {
				if err := reader.Delete(context.Background(), job); err != nil {
					t.Fatal(err)
				}
			}
			loader, err := NewRetainedLoader(reader, func(binding storekube.Binding, credentials storekube.Credentials) (evidencestore.LocationReader, error) {
				if binding != (storekube.Binding{Store: reference.Store, Endpoint: "http://store:9000", Bucket: "profiling", Region: "us-east-1", CredentialsSecretName: secret.Name}) ||
					credentials.AccessKeyID != "access" || credentials.SecretAccessKey != "secret" {
					t.Fatalf("retained binding/credentials = %#v, %#v", binding, credentials)
				}
				return objects, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.idle {
				_, err = loader.LoadIdle(context.Background(), run, reference)
			} else {
				_, err = loader.Load(context.Background(), run, reference)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type retainedObjects map[evidencestore.ObjectLocation][]byte

func (s retainedObjects) OpenLocation(_ context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
	content, ok := s[location]
	if !ok {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func retainedEvidenceObjects(t *testing.T, idle bool) (retainedObjects, evidencestore.ObjectIdentity, profilerevidence.ExecutionEvidence) {
	t.Helper()
	payload, err := os.ReadFile("../testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var config profilingrun.RunConfig
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatal(err)
	}
	config.SchemaVersion = profilingrun.RunConfigSchemaVersion
	config.State.ArtifactFiles = []string{"/artifact/mobilenet-v2-050.onnx"}
	config.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat("d", 64)
	config.Workload = profilingrun.CurrentWorkloadContract()
	if idle {
		config.Measurement = profilingrun.MeasurementContract{
			Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30, WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode,
		}
	}
	observation := validObservation()
	start := observation.Job.Status.CompletionTime.Time.Add(-30 * time.Second)
	podUID := string(observation.Pod.UID)
	cpu := profilingrun.RuntimeCPU{
		CgroupCPUMax: "400000 100000", ORTIntraOpThreads: config.CPU.ORTIntraOpThreads,
		ORTInterOpThreads: config.CPU.ORTInterOpThreads, AffinityCPUCount: config.CPU.ORTIntraOpThreads,
		AffinityCPUs: []int{0, 1, 2, 3}, CPUSetEffective: "0-3",
	}
	const (
		provider = "CPUExecutionProvider"
		version  = "1.16.0"
	)
	derived := profilingrun.DerivedContract{Backend: config.State.Backend, Provider: provider, NodeSnapshot: config.TargetNode}
	metrics := metricobserver.Result{
		Source: metricobserver.SourceIdentity{
			Kind: config.Metrics.SourceKind, NodeName: config.Metrics.NodeName, Endpoint: config.Metrics.Endpoint,
			Metric: config.Metrics.PowerMetric, MetricLabels: config.Metrics.MetricLabels, ThermalEndpoint: config.Metrics.ThermalEndpoint,
			ThermalMetrics: &metricobserver.ThermalMetricIdentity{
				ObservationAvailable: config.Metrics.ThermalMetrics.ObservationAvailable, Active: config.Metrics.ThermalMetrics.Active,
				CPUCoreThrottleEvents: config.Metrics.ThermalMetrics.CPUCoreThrottleEvents, CPUPackageThrottleEvents: config.Metrics.ThermalMetrics.CPUPackageThrottleEvents,
				Latched: config.Metrics.ThermalMetrics.Latched, MaximumTemperatureCelsius: config.Metrics.ThermalMetrics.MaximumTemperatureCelsius,
			},
		},
		StartedAt: start, EndedAt: start.Add(31 * time.Second),
		Samples: []metricobserver.Sample{{ObservedAt: start.Add(time.Second), Watts: 5}},
		Summary: metricobserver.Summary{Attempts: 1, SuccessfulSamples: 1, ObservationDurationSeconds: 31, SourceTimestampAbsent: true},
	}
	pod := workloadobserver.Pod{Namespace: observation.Pod.Namespace, Name: observation.Pod.Name, UID: podUID, Phase: "Running", Images: []string{config.Images.Runtime}}
	workload := &workloadobserver.Result{
		SchemaVersion:     workloadobserver.ResultSchemaVersion,
		Target:            workloadobserver.TargetIdentity{NodeName: config.TargetNode.Name, NodeUID: config.TargetNode.UID},
		Run:               workloadobserver.RunIdentity{Namespace: pod.Namespace, JobUID: string(observation.Job.UID), PodName: pod.Name, PodUID: podUID},
		CoverageStartedAt: start.Add(-time.Second), CoverageEndedAt: start.Add(31 * time.Second),
		InitialResourceVersion: "10", LastResourceVersion: "11", EventLimit: 10,
		InitialActivePods: []workloadobserver.Pod{pod}, FinalActivePods: []workloadobserver.Pod{pod}, Continuous: true,
	}
	var rawBytes []byte
	if idle {
		raw := profilerevidence.IdleRawMeasurement{
			SchemaVersion: profilerevidence.IdleRawMeasurementSchemaVersion, RunConfig: config, PodUID: podUID, Metrics: metrics, Workload: workload,
			Runtime: profilingrun.IdleRuntimeResult{
				SchemaVersion: profilingrun.IdleRuntimeResultSchemaVersion, Status: "Succeeded", PodUID: podUID,
				NodeName: config.TargetNode.Name, Model: config.State.Model, Architecture: "x86_64", Runtime: config.State.RuntimeFamily,
				RuntimeVersion: version, Backend: config.State.Backend, Provider: provider,
				AvailableProviders: []string{provider}, Inputs: config.State.Preprocessor.Inputs,
				ModelLoadCompletedAt: start.Add(-61 * time.Second).Format(time.RFC3339Nano), SettlingStartedAt: start.Add(-60 * time.Second).Format(time.RFC3339Nano),
				SettlingCompletedAt: start.Format(time.RFC3339Nano), MeasurementStartedAt: start.Format(time.RFC3339Nano),
				MeasurementEndedAt: start.Add(30 * time.Second).Format(time.RFC3339Nano), MeasurementElapsedSeconds: 30,
				ModelResidentAtEnd: true, CPUContract: cpu, DerivedContract: derived,
				MemoryBytes: 123456, MemorySource: "cgroup-v2-memory.peak:runtime-container-lifetime",
				Firstfloor: retainedFirstfloorEvidence(config, cpu, 0, provider, version),
			},
		}
		rawBytes, err = raw.CanonicalJSON()
	} else {
		duration := float64(config.Measurement.DurationSeconds)
		raw := profilerevidence.RawMeasurement{
			SchemaVersion: profilerevidence.RawMeasurementSchemaVersion, RunConfig: config, PodUID: podUID, Metrics: metrics, Workload: workload,
			Runtime: profilingrun.RuntimeResult{
				SchemaVersion: profilingrun.RuntimeResultSchemaVersion, Status: "Succeeded", ExperimentID: podUID,
				NodeName: config.TargetNode.Name, Model: config.State.Model, Architecture: "x86_64", Runtime: config.State.RuntimeFamily,
				RuntimeVersion: version, Backend: config.State.Backend, Provider: provider,
				AvailableProviders: []string{provider}, Inputs: config.State.Preprocessor.Inputs,
				BatchSize: config.Measurement.Batch, WarmupIterations: config.Measurement.WarmupIterations,
				WarmupCompletedAt: start.Add(-time.Second).Format(time.RFC3339Nano), InferenceCount: 2, MeasurementMode: "duration",
				MeasurementStartedAt: start.Format(time.RFC3339Nano), MeasurementEndedAt: start.Add(30 * time.Second).Format(time.RFC3339Nano),
				MeasurementElapsedSeconds: 30, TargetDurationSeconds: &duration, CPUContract: cpu, DerivedContract: derived,
				RepetitionResults: []profilingrun.Repetition{{Repetition: 1, MeasurementStartedAt: start.Format(time.RFC3339Nano),
					MeasurementEndedAt: start.Add(30 * time.Second).Format(time.RFC3339Nano), LatencyMilliseconds: []float64{1, 2}}},
				Latency:     map[string]float64{"p95": 2, "mean": 1.5},
				MemoryBytes: 123456, MemorySource: "cgroup-v2-memory.peak:runtime-container-lifetime",
				Firstfloor: retainedFirstfloorEvidence(config, cpu, 2, provider, version),
			},
		}
		recordIDs, units := make([]string, config.Measurement.Batch), make([]int, config.Measurement.Batch)
		for i := range recordIDs {
			recordIDs[i], units[i] = "record-"+strconv.Itoa(i), 1
		}
		for i := range raw.Runtime.RepetitionResults[0].LatencyMilliseconds {
			raw.Runtime.RepetitionResults[0].NativeSamples = append(raw.Runtime.RepetitionResults[0].NativeSamples, profilingrun.NativeServiceSample{
				BatchID: uint64(i + 1), BatchSize: config.Measurement.Batch, RecordIDs: recordIDs, Units: units,
				RunnerServiceMilliseconds: 0.7, PreprocessMilliseconds: 0.1, PostprocessMilliseconds: 0.1,
				BackendCallMilliseconds: 0.4, NativeRequestMilliseconds: 0.9,
			})
		}
		rawBytes, err = raw.CanonicalJSON()
	}
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := ProjectTerminalFacts(observation)
	if err != nil {
		t.Fatal(err)
	}
	terminal.Node.Name, terminal.Node.UID = config.TargetNode.Name, config.TargetNode.UID
	terminal.Pod.NodeName = config.TargetNode.Name
	images := map[string]string{
		"artifact": config.Images.Artifact, "preprocessor": config.State.Preprocessor.Image,
		"runtime": config.Images.Runtime, "metric-observer": config.Images.MetricObserver,
		"workload-observer": config.Images.WorkloadObserver,
	}
	if config.Measurement.Dataset != nil {
		images["dataset"] = config.Measurement.Dataset.Image
	}
	template := terminal.Containers[0]
	terminal.Containers = nil
	for name, image := range images {
		container := template
		container.Name, container.RequestedImage, container.ImageID = name, image, image
		terminal.Containers = append(terminal.Containers, container)
	}
	rawRef := evidencestore.ObjectIdentity{Store: "chill-evidence", Key: "raw.json", Size: int64(len(rawBytes))}
	execution := profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion, RunConfig: config,
		RawMeasurement: rawRef, Terminal: terminal, FinalizedAt: observation.ObservedAt.Add(time.Second),
	}
	finalBytes, err := json.Marshal(execution)
	if err != nil {
		t.Fatal(err)
	}
	reference := evidencestore.ObjectIdentity{Store: rawRef.Store, Key: "execution.json", Size: int64(len(finalBytes))}
	return retainedObjects{
		{Store: rawRef.Store, Key: rawRef.Key}:       rawBytes,
		{Store: reference.Store, Key: reference.Key}: finalBytes,
	}, reference, execution
}

func retainedFirstfloorEvidence(config profilingrun.RunConfig, cpu profilingrun.RuntimeCPU, executions uint64, provider, version string) *profilingrun.FirstfloorRuntimeEvidence {
	counts := map[string]uint64{}
	if executions > 0 {
		counts[strconv.Itoa(config.Measurement.Batch)] = executions
	}
	return &profilingrun.FirstfloorRuntimeEvidence{
		Mode: "profiling", ModelInstanceCount: 1,
		MaxBatchSize: int(config.State.Firstfloor.MaxBatchSize), MaxQueueSize: int(config.State.Firstfloor.MaxQueueSize),
		DeadlinePolicy: modelcompat.EffectiveDeadlinePolicy(config.State.Firstfloor.DeadlinePolicy),
		ExecutionCount: executions, InferenceCount: executions * uint64(config.Measurement.Batch), BatchExecutionCounts: counts,
		Runtime: firstfloor.RuntimeMetadata{
			Runtime: config.State.Backend, RuntimeVersion: version, ModelFiles: config.State.ModelFiles(),
			Inputs: config.State.Preprocessor.Inputs, Outputs: config.State.Preprocessor.Outputs,
			Providers: []string{provider}, AvailableProviders: []string{provider},
			Device: "cpu", CPUAffinity: cpu.AffinityCPUs, IntraOpThreads: cpu.ORTIntraOpThreads, InterOpThreads: cpu.ORTInterOpThreads,
			InitializedBatches: []int{config.Measurement.Batch}, CgroupCPUMax: cpu.CgroupCPUMax, CPUSet: cpu.CPUSetEffective,
		},
	}
}

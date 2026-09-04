package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testJobUID = "job-uid"

type staticEvidenceStoreResolver struct {
	binding EvidenceStoreBinding
	err     error
}

func (r staticEvidenceStoreResolver) Resolve(context.Context) (EvidenceStoreBinding, error) {
	return r.binding, r.err
}

type concurrentJobCreateClient struct {
	client.Client
	job         *batchv1.Job
	createCalls int
}

func (c *concurrentJobCreateClient) Create(
	ctx context.Context,
	object client.Object,
	_ ...client.CreateOption,
) error {
	c.createCalls++
	if err := c.Client.Create(ctx, c.job.DeepCopy()); err != nil {
		return err
	}
	return apierrors.NewAlreadyExists(schema.GroupResource{Group: "batch", Resource: "jobs"}, object.GetName())
}

func TestBuildExecutionJobRecordsRunConfigAndDeliveryBinding(t *testing.T) {
	run := testRun()
	prepared := currentPreparedRun(t)
	binding := testEvidenceStoreBinding()
	scheme := executionJobTestScheme(t)
	resolved := resolvedRunForPrepared(t, prepared)

	job, err := buildExecutionJob(run, resolved, binding, testExecutionJobOptions(), scheme)
	if err != nil {
		t.Fatal(err)
	}
	if job.Namespace != run.Namespace || job.Spec.Parallelism == nil || *job.Spec.Parallelism != 1 ||
		job.Spec.Completions == nil || *job.Spec.Completions != 1 ||
		job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 ||
		job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever ||
		job.Spec.Template.Spec.AutomountServiceAccountToken == nil || *job.Spec.Template.Spec.AutomountServiceAccountToken ||
		job.Spec.Template.Spec.ServiceAccountName != "chill-workload-observer" {
		t.Fatalf("Job retry or identity boundary drifted: %#v", job.Spec)
	}
	if !metav1.IsControlledBy(job, run) || job.Annotations[admissionPolicyAnnotation] == "" ||
		job.Annotations[executionPolicyAnnotation] == "" {
		t.Fatalf("Job ownership or execution identity missing: %#v", job.ObjectMeta)
	}
	pod := job.Spec.Template.Spec
	if pod.NodeSelector[corev1.LabelHostname] != prepared.RunConfig.TargetNode.Name ||
		len(pod.InitContainers) != 1 || len(pod.Containers) != 3 || len(pod.Volumes) != 4 {
		t.Fatalf("unexpected measurement topology: %#v", pod)
	}
	artifact := pod.InitContainers[0]
	if artifact.Image != prepared.RunConfig.Images.Artifact ||
		len(artifact.Args) != 4 || artifact.Args[2] != prepared.RunConfig.State.ArtifactPath ||
		artifact.Args[3] != cpuORTModelDestination {
		t.Fatalf("artifact supply was not resolved into the Job: %#v", artifact)
	}
	runtimeContainer := findContainer(t, pod.Containers, "runtime")
	if _, err := argumentValue(runtimeContainer.Args, runConfigArgumentPrefix); err != nil {
		t.Fatal(err)
	}
	assertArgument(t, runtimeContainer.Args, "--store-endpoint="+binding.Endpoint)
	assertArgument(t, runtimeContainer.Args, "--workload-result-timeout=30s")
	assertArgument(t, runtimeContainer.Args, "--max-workload-result-bytes=8388608")
	assertArgument(t, runtimeContainer.Args, "--raw-object-key=profiling-runs/"+string(run.UID)+"/raw.json")
	if runtimeContainer.Resources.Limits.Cpu().String() != prepared.RunConfig.CPU.Limit ||
		runtimeContainer.Resources.Requests.Cpu().String() != prepared.RunConfig.CPU.Request {
		t.Fatalf("runtime CPU contract drifted: %#v", runtimeContainer.Resources)
	}
	if !hasSecretEnv(runtimeContainer.Env, "AWS_ACCESS_KEY_ID", binding.CredentialsSecretName, component.EvidenceStoreAccessKeyKey) ||
		!hasSecretEnv(runtimeContainer.Env, "AWS_SECRET_ACCESS_KEY", binding.CredentialsSecretName, component.EvidenceStoreSecretKeyKey) {
		t.Fatalf("Evidence Store credentials were not projected by Secret reference: %#v", runtimeContainer.Env)
	}
	observer := findContainer(t, pod.Containers, "metric-observer")
	assertArgument(t, observer.Args, "--max-events=33")
	assertArgument(t, observer.Args, "--end-wait-timeout=3m1s")
	workloads := findContainer(t, pod.Containers, "workload-observer")
	assertArgument(t, workloads.Args, "--kube-api-server=https://kubernetes.example:6443")
	assertArgument(t, workloads.Args, "--max-events=10000")
	assertArgument(t, workloads.Args,
		"--selector-label-keys=app.kubernetes.io/component,app.kubernetes.io/name,edge.dacs.io/workload-role")
	assertWorkloadObserverCredentialIsolation(t, pod, runtimeContainer, observer, workloads)
}

func TestBuildExecutionJobProjectsExactRKNNLiteHostAccess(t *testing.T) {
	run := testRun()
	prepared := currentRKNNPreparedRun(t, "")
	run.Spec.Candidate.DeviceClass = prepared.RunConfig.State.DeviceClass
	run.Spec.Candidate.ExecutionPath = prepared.RunConfig.Candidate.ExecutionPath
	run.Spec.TargetNode = prepared.RunConfig.TargetNode.Name
	job, err := buildExecutionJob(
		run,
		resolvedRunForPrepared(t, prepared),
		testEvidenceStoreBinding(),
		testExecutionJobOptions(),
		executionJobTestScheme(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	pod := job.Spec.Template.Spec
	if !reflect.DeepEqual(pod.SecurityContext.SupplementalGroups, []int64{44, 110}) {
		t.Fatalf("RKNNLite supplemental groups drifted: %#v", pod.SecurityContext)
	}
	if len(pod.Volumes) != 11 {
		t.Fatalf("RKNNLite host volumes = %d, want 11: %#v", len(pod.Volumes), pod.Volumes)
	}
	artifact := pod.InitContainers[0]
	if len(artifact.Args) != 4 || artifact.Args[3] != rknnModelDestination {
		t.Fatalf("RKNNLite artifact destination drifted: %#v", artifact.Args)
	}
	runtimeContainer := findContainer(t, pod.Containers, "runtime")
	if runtimeContainer.SecurityContext == nil || runtimeContainer.SecurityContext.Privileged == nil ||
		!*runtimeContainer.SecurityContext.Privileged ||
		runtimeContainer.SecurityContext.AllowPrivilegeEscalation == nil ||
		!*runtimeContainer.SecurityContext.AllowPrivilegeEscalation ||
		runtimeContainer.SecurityContext.ReadOnlyRootFilesystem == nil ||
		!*runtimeContainer.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatalf("RKNNLite runtime access was not projected exactly: %#v", runtimeContainer.SecurityContext)
	}
	wantMounts := map[string]bool{
		"/usr/lib/librknnrt.so":        true,
		"/sys/module/rknpu/version":    true,
		"/proc/device-tree/compatible": true,
		"/dev/dri/card0":               false,
		"/dev/dri/card1":               false,
		"/dev/dri/renderD128":          false,
		"/dev/dri/renderD129":          false,
	}
	for path, readOnly := range wantMounts {
		mount := findMountPath(t, runtimeContainer.VolumeMounts, path)
		if mount.ReadOnly != readOnly {
			t.Fatalf("RKNNLite mount %q readOnly=%t, want %t", path, mount.ReadOnly, readOnly)
		}
	}
	for _, volume := range pod.Volumes {
		if volume.HostPath == nil {
			continue
		}
		wantType := corev1.HostPathFile
		if strings.HasPrefix(volume.HostPath.Path, "/dev/") {
			wantType = corev1.HostPathCharDev
		}
		if volume.HostPath.Type == nil || *volume.HostPath.Type != wantType {
			t.Fatalf("RKNNLite hostPath %q type=%v, want %s", volume.HostPath.Path, volume.HostPath.Type, wantType)
		}
	}
	for _, name := range []string{"metric-observer", "workload-observer"} {
		container := findContainer(t, pod.Containers, name)
		if container.SecurityContext == nil || container.SecurityContext.AllowPrivilegeEscalation == nil ||
			*container.SecurityContext.AllowPrivilegeEscalation || container.SecurityContext.Privileged != nil {
			t.Fatalf("%s inherited RKNNLite container privilege: %#v", name, container.SecurityContext)
		}
		for path := range wantMounts {
			if containsMountPath(container.VolumeMounts, path) {
				t.Fatalf("%s received runtime-only host mount %q", name, path)
			}
		}
	}
	if _, exists := runtimeContainer.Resources.Limits[corev1.ResourceName("example.com/rknpu")]; exists {
		t.Fatal("undeclared NPU extended resource was invented")
	}
}

func TestProjectRuntimeJobRejectsCrossCategoryHostPathCollision(t *testing.T) {
	prepared := currentRKNNPreparedRun(t, "")
	prepared.RunConfig.Delivery.HostContract.Driver.VersionPath =
		prepared.RunConfig.Delivery.HostContract.Libraries[0].HostPath
	prepared.RunConfig.State.HostContract = prepared.RunConfig.Delivery.HostContract
	if _, err := projectRuntimeJob(prepared.RunConfig); err == nil ||
		!strings.Contains(err.Error(), "projected more than once") {
		t.Fatalf("cross-category hostPath collision was accepted: %v", err)
	}
}

func TestBuildExecutionJobProjectsDeclaredRKNNLiteResource(t *testing.T) {
	run := testRun()
	prepared := currentRKNNPreparedRun(t, "example.com/rknpu")
	run.Spec.Candidate.DeviceClass = prepared.RunConfig.State.DeviceClass
	run.Spec.Candidate.ExecutionPath = prepared.RunConfig.Candidate.ExecutionPath
	run.Spec.TargetNode = prepared.RunConfig.TargetNode.Name
	job, err := buildExecutionJob(
		run,
		resolvedRunForPrepared(t, prepared),
		testEvidenceStoreBinding(),
		testExecutionJobOptions(),
		executionJobTestScheme(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	runtimeContainer := findContainer(t, job.Spec.Template.Spec.Containers, "runtime")
	resourceName := corev1.ResourceName("example.com/rknpu")
	request := runtimeContainer.Resources.Requests[resourceName]
	limit := runtimeContainer.Resources.Limits[resourceName]
	if request.String() != "1" || limit.String() != "1" {
		t.Fatalf("declared NPU allocation was not projected: %#v", runtimeContainer.Resources)
	}
}

func TestBuildExecutionJobExtendsOnlyIdleTimingEnvelope(t *testing.T) {
	run := testRun()
	settling := int32(60)
	run.Spec.Measurement.WarmupIterations = 0
	run.Spec.Measurement.SettlingDurationSeconds = &settling
	run.Spec.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
	run.Spec.Measurement.InputGenerator = profilingrun.NoInputGenerator
	run.Spec.Measurement.OutputValidation = profilingrun.NoOutputValidation
	run.Spec.AdmissionPolicy = testIdleAdmissionPolicy
	prepared := currentPreparedRun(t)
	prepared.RunConfig.Measurement = profilingrun.MeasurementContract{
		Batch: 1, WarmupIterations: 0, SettlingDurationSeconds: 60,
		DurationSeconds: 30,
		WorkloadMode:    profilingrun.IdleModelLoadedWorkloadMode,
		InputGenerator:  profilingrun.NoInputGenerator, OutputValidation: profilingrun.NoOutputValidation,
	}
	resolved := resolvedRunForPrepared(t, prepared)
	job, err := buildExecutionJob(
		run,
		resolved,
		testEvidenceStoreBinding(),
		testExecutionJobOptions(),
		executionJobTestScheme(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	recordedPolicy, err := profiler.DecodeFrozenExecutionPolicy([]byte(job.Annotations[executionPolicyAnnotation]))
	if err != nil {
		t.Fatal(err)
	}
	if recordedPolicy.Policy.Name != resolved.ExecutionPolicy.Policy.Name || job.Spec.ActiveDeadlineSeconds == nil ||
		*job.Spec.ActiveDeadlineSeconds != 271 {
		t.Fatalf("idle Job policy drifted: %#v", job)
	}
	runtimeContainer := findContainer(t, job.Spec.Template.Spec.Containers, "runtime")
	assertArgument(t, runtimeContainer.Args, "--attempt-timeout=4m1s")
	observer := findContainer(t, job.Spec.Template.Spec.Containers, "metric-observer")
	assertArgument(t, observer.Args, "--start-wait-timeout=3m0s")
}

func TestBuildExecutionJobUsesConfiguredExecutionPolicyIdentity(t *testing.T) {
	run := testRun()
	prepared := currentPreparedRun(t)
	resolved := resolvedRunForPrepared(t, prepared)
	policy, err := profiler.FreezeConfiguredExecutionPolicy(
		controllerExecutionPolicyCatalog(t),
		prepared.RunConfig,
		profiler.ExecutionPolicySource{
			Kind:            profiler.ConfigMapExecutionPolicySourceKind,
			Namespace:       run.Namespace,
			Name:            "chill-profiler-facts",
			UID:             testFactsUID,
			ResourceVersion: "42",
			Key:             "execution-policies.yaml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved.ExecutionPolicy = policy
	job, err := buildExecutionJob(
		run,
		resolved,
		testEvidenceStoreBinding(),
		testExecutionJobOptions(),
		executionJobTestScheme(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	recordedPolicy, err := profiler.DecodeFrozenExecutionPolicy([]byte(job.Annotations[executionPolicyAnnotation]))
	if err != nil {
		t.Fatal(err)
	}
	if recordedPolicy.Policy.Name != policy.Policy.Name {
		t.Fatalf(
			"configured Job policy identity = %q, want %q",
			recordedPolicy.Policy.Name, policy.Policy.Name,
		)
	}
}

func assertWorkloadObserverCredentialIsolation(
	t *testing.T,
	pod corev1.PodSpec,
	runtimeContainer corev1.Container,
	metricObserver corev1.Container,
	workloadObserver corev1.Container,
) {
	t.Helper()
	if !hasVolumeMount(workloadObserver.VolumeMounts, workloadObserverTokenVolumeName, workloadObserverTokenMountPath) ||
		hasVolumeMount(runtimeContainer.VolumeMounts, workloadObserverTokenVolumeName, workloadObserverTokenMountPath) ||
		hasVolumeMount(metricObserver.VolumeMounts, workloadObserverTokenVolumeName, workloadObserverTokenMountPath) {
		t.Fatalf("projected API credential isolation drifted: runtime=%#v metric=%#v workload=%#v",
			runtimeContainer.VolumeMounts, metricObserver.VolumeMounts, workloadObserver.VolumeMounts)
	}
	credentialVolume := findVolume(t, pod.Volumes, workloadObserverTokenVolumeName)
	if credentialVolume.Projected == nil || len(credentialVolume.Projected.Sources) != 2 ||
		credentialVolume.Projected.Sources[0].ServiceAccountToken == nil ||
		credentialVolume.Projected.Sources[0].ServiceAccountToken.Path != "token" ||
		credentialVolume.Projected.Sources[1].ConfigMap == nil ||
		credentialVolume.Projected.Sources[1].ConfigMap.Name != "kube-root-ca.crt" {
		t.Fatalf("unexpected WorkloadObserver projected credential: %#v", credentialVolume)
	}
}

func TestExecutionJobsRecoverExactOwnedJobAndRejectConflict(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	prepared := currentPreparedRun(t)
	binding := testEvidenceStoreBinding()
	scheme := executionJobTestScheme(t)
	resolved := resolvedRunForPrepared(t, prepared)
	desired, err := buildExecutionJob(run, resolved, binding, testExecutionJobOptions(), scheme)
	if err != nil {
		t.Fatal(err)
	}
	desired.UID = testJobUID
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(desired).Build()
	jobs, err := NewExecutionJobs(
		kubeClient,
		scheme,
		staticEvidenceStoreResolver{err: errors.New("current delivery binding must not be read during recovery")},
		testExecutionJobOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	recovered, created, err := jobs.Ensure(ctx, run, resolved)
	if err != nil || created || recovered.UID != testJobUID {
		t.Fatalf("expected exact Job recovery, got created=%v job=%#v err=%v", created, recovered, err)
	}

	retained := &batchv1.Job{}
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, retained); err != nil {
		t.Fatal(err)
	}
	retained.Annotations[admissionPolicyAnnotation] = "\n " + retained.Annotations[admissionPolicyAnnotation]
	retained.Annotations[executionPolicyAnnotation] = "\n " + retained.Annotations[executionPolicyAnnotation]
	for containerIndex := range retained.Spec.Template.Spec.Containers {
		container := &retained.Spec.Template.Spec.Containers[containerIndex]
		for argumentIndex := range container.Args {
			if strings.HasPrefix(container.Args[argumentIndex], runConfigArgumentPrefix) {
				container.Args[argumentIndex] = runConfigArgumentPrefix + "\n " +
					strings.TrimPrefix(container.Args[argumentIndex], runConfigArgumentPrefix)
			}
		}
	}
	if err := kubeClient.Update(ctx, retained); err != nil {
		t.Fatal(err)
	}
	if recovered, created, err = jobs.Ensure(ctx, run, resolved); err != nil || created || recovered.UID != testJobUID {
		t.Fatalf("semantically equal Job recovery failed: created=%v job=%#v err=%v", created, recovered, err)
	}
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, retained); err != nil {
		t.Fatal(err)
	}
	retained.Spec.Template.Spec.NodeSelector[corev1.LabelHostname] = "other-node"
	if err := kubeClient.Update(ctx, retained); err != nil {
		t.Fatal(err)
	}
	if _, _, err := jobs.Ensure(ctx, run, resolved); err == nil || !strings.Contains(err.Error(), "recorded execution contract") {
		t.Fatalf("conflicting retained Job accepted: %v", err)
	}
}

func TestExecutionJobsRecoverConcurrentlyCreatedJobAndRejectConflict(t *testing.T) {
	for _, test := range []struct {
		name      string
		mutate    func(*batchv1.Job)
		wantError string
	}{
		{name: "exact"},
		{
			name: "drift",
			mutate: func(job *batchv1.Job) {
				job.Spec.Template.Spec.NodeSelector[corev1.LabelHostname] = "other-node"
			},
			wantError: "recorded execution contract",
		},
		{
			name: "invalid retained binding",
			mutate: func(job *batchv1.Job) {
				for index := range job.Spec.Template.Spec.Containers {
					if job.Spec.Template.Spec.Containers[index].Name == runtimeContainerName {
						job.Spec.Template.Spec.Containers[index].Name = "other"
					}
				}
			},
			wantError: "existing measurement Job has no runtime container",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			run := testRun()
			prepared := currentPreparedRun(t)
			scheme := executionJobTestScheme(t)
			resolved := resolvedRunForPrepared(t, prepared)
			retainedBinding := testEvidenceStoreBinding()
			concurrent, err := buildExecutionJob(
				run, resolved, retainedBinding, testExecutionJobOptions(), scheme,
			)
			if err != nil {
				t.Fatal(err)
			}
			concurrent.UID = testJobUID
			if test.mutate != nil {
				test.mutate(concurrent)
			}
			baseClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			kubeClient := &concurrentJobCreateClient{Client: baseClient, job: concurrent}
			currentBinding := testEvidenceStoreBinding()
			currentBinding.Endpoint = "http://new-evidence-store.chill-system.svc.cluster.local:9000"
			jobs, err := NewExecutionJobs(
				kubeClient,
				scheme,
				staticEvidenceStoreResolver{binding: currentBinding},
				testExecutionJobOptions(),
			)
			if err != nil {
				t.Fatal(err)
			}

			recovered, created, ensureErr := jobs.Ensure(ctx, run, resolved)
			if test.wantError != "" {
				if ensureErr == nil || !strings.Contains(ensureErr.Error(), test.wantError) || recovered != nil || created {
					t.Fatalf("conflicting concurrent Job result: created=%v job=%#v err=%v", created, recovered, ensureErr)
				}
			} else if ensureErr != nil || created || recovered == nil || recovered.UID != testJobUID {
				t.Fatalf("exact concurrent Job recovery: created=%v job=%#v err=%v", created, recovered, ensureErr)
			}
			if kubeClient.createCalls != 1 {
				t.Fatalf("create calls = %d, want 1", kubeClient.createCalls)
			}
		})
	}
}

func testExecutionJobOptions() ExecutionJobOptions {
	return ExecutionJobOptions{
		WorkloadObserverServiceAccountName: "chill-workload-observer",
		WorkloadObserverKubeAPIServer:      "https://kubernetes.example:6443",
	}
}

func currentPreparedRun(t *testing.T) profiler.PreparedRun {
	t.Helper()
	prepared := testPreparedRun(t)
	prepared.RunConfig.SchemaVersion = profilingrun.RunConfigSchemaVersion
	prepared.RunConfig.State.ArtifactPath = "/artifact/mobilenet-v2-050.onnx"
	prepared.RunConfig.Images.WorkloadObserver = testWorkloadObserverImage
	prepared.RunConfig.Workload = profilingrun.CurrentWorkloadContract()
	return prepared
}

func currentRKNNPreparedRun(t *testing.T, resourceName string) profiler.PreparedRun {
	t.Helper()
	prepared := currentPreparedRun(t)
	config := &prepared.RunConfig
	config.Candidate.ExecutionPath = profilingrun.RKNNLiteExecutionPath
	config.State.DeviceClass = "orangepi-5-plus-16g"
	config.State.Artifact = "mobilenet-v2-050-rk3588-bs1"
	config.State.ArtifactPath = "/artifact/mobilenet-v2-050-rk3588-bs1.rknn"
	config.State.RuntimeFamily = profilingrun.RKNNLiteRuntimeFamily
	config.State.Backend = profilingrun.RKNNLiteBackend
	config.TargetNode.Name = "orangepi5plus"
	config.TargetNode.Architecture = testARM64Architecture
	config.TargetNode.AllocatableCPU = "8"
	config.CPU.Limit = "8"
	config.CPU.ORTIntraOpThreads = 0
	config.CPU.ORTInterOpThreads = 0
	config.Metrics.NodeName = config.TargetNode.Name

	host := profilingrun.CandidateReportHostContract{
		Libraries: []profilingrun.CandidateReportHostLibrary{{
			Name: "librknnrt", HostPath: "/usr/lib/librknnrt.so", ContainerPath: "/usr/lib/librknnrt.so",
		}},
		Driver: profilingrun.CandidateReportHostDriver{
			Name: "rknpu", VersionPath: "/sys/module/rknpu/version", Version: "0.9.6",
		},
		Devices: []profilingrun.CandidateReportHostDevice{
			{Path: "/dev/dri/card0"}, {Path: "/dev/dri/card1"},
			{Path: "/dev/dri/renderD128"}, {Path: "/dev/dri/renderD129"},
		},
		Platform: &profilingrun.CandidateReportHostPlatform{
			CompatiblePath: "/proc/device-tree/compatible", CompatibleToken: "rockchip,rk3588",
		},
		Allocation: profilingrun.CandidateReportNPUAllocation{
			ResourceName: resourceName, ExclusivityClaimed: resourceName != "",
		},
	}
	config.State.HostContract = &host
	config.Delivery = profilingrun.CandidateRuntimeDelivery{
		ArtifactCapability: &profilingrun.CandidateArtifactCapability{
			TargetPlatform: "rk3588", DeclaredBatchSizes: []int32{1}, InputSampleShape: []int32{224, 224, 3},
		},
		RuntimePackages: []profilingrun.CandidateReportRuntimePackage{{Name: "rknn-toolkit-lite2", Version: "2.3.2"}},
		HostContract:    &host,
		RuntimeAccess: &profilingrun.CandidateReportRuntimeAccess{
			Privileged: true, SupplementalGroups: []int64{44, 110},
		},
	}
	return prepared
}

func testEvidenceStoreBinding() EvidenceStoreBinding {
	return EvidenceStoreBinding{
		Store: "chill-evidence-store", Endpoint: "http://chill-evidence-store.chill-system.svc.cluster.local:9000",
		Bucket: "chill-profiling-evidence", Region: "us-east-1", CredentialsSecretName: "chill-evidence-store-credentials",
	}
}

func executionJobTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, batchv1.AddToScheme, edgev1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func findContainer(t *testing.T, containers []corev1.Container, name string) corev1.Container {
	t.Helper()
	for _, container := range containers {
		if container.Name == name {
			return container
		}
	}
	t.Fatalf("container %q not found", name)
	return corev1.Container{}
}

func findVolume(t *testing.T, volumes []corev1.Volume, name string) corev1.Volume {
	t.Helper()
	for _, volume := range volumes {
		if volume.Name == name {
			return volume
		}
	}
	t.Fatalf("volume %q not found", name)
	return corev1.Volume{}
}

func findMountPath(t *testing.T, mounts []corev1.VolumeMount, path string) corev1.VolumeMount {
	t.Helper()
	for _, mount := range mounts {
		if mount.MountPath == path {
			return mount
		}
	}
	t.Fatalf("mount path %q not found", path)
	return corev1.VolumeMount{}
}

func containsMountPath(mounts []corev1.VolumeMount, path string) bool {
	for _, mount := range mounts {
		if mount.MountPath == path {
			return true
		}
	}
	return false
}

func assertArgument(t *testing.T, arguments []string, want string) {
	t.Helper()
	for _, argument := range arguments {
		if argument == want {
			return
		}
	}
	t.Fatalf("argument %q not found in %#v", want, arguments)
}

func hasSecretEnv(env []corev1.EnvVar, name, secretName, key string) bool {
	for _, variable := range env {
		if variable.Name == name && variable.ValueFrom != nil && variable.ValueFrom.SecretKeyRef != nil &&
			variable.ValueFrom.SecretKeyRef.Name == secretName && variable.ValueFrom.SecretKeyRef.Key == key {
			return true
		}
	}
	return false
}

func hasVolumeMount(mounts []corev1.VolumeMount, name, path string) bool {
	for _, mount := range mounts {
		if mount.Name == name && mount.MountPath == path && mount.ReadOnly {
			return true
		}
	}
	return false
}

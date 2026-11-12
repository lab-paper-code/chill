package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testJobUID = "job-uid"

type staticEvidenceStoreResolver struct {
	binding storekube.Binding
	err     error
}

func (r staticEvidenceStoreResolver) Resolve(context.Context) (storekube.Binding, error) {
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
	if job.Spec.TTLSecondsAfterFinished != nil {
		t.Fatal("measurement Job must retain its Pods until evidence finalization")
	}
	if job.Namespace != run.Namespace || job.Spec.Parallelism == nil || *job.Spec.Parallelism != 1 ||
		job.Spec.Completions == nil || *job.Spec.Completions != 1 ||
		job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 ||
		job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever ||
		job.Spec.Template.Spec.AutomountServiceAccountToken == nil || *job.Spec.Template.Spec.AutomountServiceAccountToken ||
		job.Spec.Template.Spec.ServiceAccountName != "chill-workload-observer" {
		t.Fatalf("Job retry or identity boundary drifted: %#v", job.Spec)
	}
	if !metav1.IsControlledBy(job, run) ||
		job.Annotations[executionPolicyAnnotation] == "" {
		t.Fatalf("Job ownership or execution identity missing: %#v", job.ObjectMeta)
	}
	pod := job.Spec.Template.Spec
	assertDeliveryTopology(t, pod, prepared.RunConfig)
	if !slices.Contains(pod.Tolerations, corev1.Toleration{Key: "chill.dacs.io/measurement", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}) {
		t.Fatalf("pod tolerations = %#v, want measurement toleration", pod.Tolerations)
	}
	runtimeContainer := findContainer(t, pod.Containers, "runtime")
	if runtimeContainer.ReadinessProbe == nil || runtimeContainer.ReadinessProbe.TCPSocket == nil ||
		runtimeContainer.ReadinessProbe.TCPSocket.Port.IntVal != 8081 {
		t.Fatal("active worker readiness must wait for its off-node load control socket")
	}
	if !slices.ContainsFunc(runtimeContainer.Env, func(env corev1.EnvVar) bool {
		return env.Name == "POD_IP" && env.ValueFrom != nil && env.ValueFrom.FieldRef != nil && env.ValueFrom.FieldRef.FieldPath == "status.podIP"
	}) {
		t.Fatal("native URL must use the measurement Pod IP")
	}
	for _, path := range []string{"/model", "/preprocessor"} {
		if mount := findMountPath(t, runtimeContainer.VolumeMounts, path); !mount.ReadOnly {
			t.Fatalf("runtime %s mount is writable", path)
		}
	}
	if _, err := runConfigArgument(runtimeContainer.Args); err != nil {
		t.Fatal(err)
	}
	assertArgument(t, runtimeContainer.Args, "--store-endpoint="+binding.Endpoint)
	assertArgument(t, runtimeContainer.Args, "--max-workload-result-bytes=8388608")
	assertArgument(t, runtimeContainer.Args, "--raw-object-key=profiling-runs/"+string(run.UID)+"/raw.json")
	for _, container := range append(slices.Clone(pod.InitContainers), pod.Containers...) {
		for _, resources := range []corev1.ResourceList{container.Resources.Requests, container.Resources.Limits} {
			if _, present := resources[corev1.ResourceCPU]; present {
				t.Fatalf("container %s carries CPU resources: %#v", container.Name, container.Resources)
			}
		}
		if container.Resources.Requests.Memory().Sign() <= 0 || container.Resources.Limits.Memory().Sign() <= 0 {
			t.Fatalf("container %s lost its memory envelope", container.Name)
		}
	}
	if pod.Affinity != nil {
		t.Fatal("profiling Job carries affinity")
	}
	if !hasSecretEnv(runtimeContainer.Env, "AWS_ACCESS_KEY_ID", binding.CredentialsSecretName, component.EvidenceStoreAccessKeyKey) ||
		!hasSecretEnv(runtimeContainer.Env, "AWS_SECRET_ACCESS_KEY", binding.CredentialsSecretName, component.EvidenceStoreSecretKeyKey) {
		t.Fatalf("Evidence Store credentials were not projected by Secret reference: %#v", runtimeContainer.Env)
	}
	observer := findContainer(t, pod.Containers, "metric-observer")
	plan, err := profiler.DeriveExecutionPlan(prepared.RunConfig, resolved.ExecutionPolicy)
	if err != nil {
		t.Fatal(err)
	}
	assertArgument(t, observer.Args, "--memory-budget-bytes=67108864")
	assertArgument(t, observer.Args, "--end-wait-timeout="+plan.AttemptTimeout.String())
	assertArgument(t, observer.Args, "--max-result-bytes=8388608")
	workloads := findContainer(t, pod.Containers, "workload-observer")
	assertArgument(t, runtimeContainer.Args, "--attempt-timeout="+plan.AttemptTimeout.String())
	assertArgument(t, runtimeContainer.Args, "--metric-result-timeout="+plan.AttemptTimeout.String())
	assertArgument(t, runtimeContainer.Args, "--workload-result-timeout="+plan.WorkloadResultTimeout.String())
	assertArgument(t, workloads.Args, "--end-wait-timeout="+plan.AttemptTimeout.String())
	assertArgument(t, workloads.Args, "--end-fence-timeout="+plan.AttemptTimeout.String())
	assertArgument(t, workloads.Args, "--kube-api-server=https://kubernetes.example:6443")
	assertArgument(t, workloads.Args, "--max-events=10000")
	assertArgument(t, workloads.Args,
		"--selector-label-keys=app.kubernetes.io/component,app.kubernetes.io/name,edge.dacs.io/workload-role")
	assertWorkloadObserverCredentialIsolation(t, pod, runtimeContainer, observer, workloads)
}

func TestExecutionJobsKeepFrozenActorMemoryWithoutNodeLookup(t *testing.T) {
	run := testRun()
	resolved := resolvedRunForPrepared(t, currentPreparedRun(t))
	resolved.Prepared.RunConfig.Actor.MemoryRequestBytes = 256 << 20
	resolved.Prepared.RunConfig.Actor.MemoryLimitBytes = 2 << 30
	scheme := executionJobTestScheme(t)
	kube := fake.NewClientBuilder().WithScheme(scheme).Build()
	jobs, err := NewExecutionJobs(kube, scheme, staticEvidenceStoreResolver{binding: testEvidenceStoreBinding()}, testExecutionJobOptions())
	if err != nil {
		t.Fatal(err)
	}
	job, created, err := jobs.Ensure(context.Background(), run, resolved)
	if err != nil || !created {
		t.Fatalf("create Job without current Node facts: created=%v err=%v", created, err)
	}
	runtime := findContainer(t, job.Spec.Template.Spec.Containers, runtimeContainerName)
	for _, container := range append(slices.Clone(job.Spec.Template.Spec.InitContainers), runtime) {
		if container.Resources.Requests.Memory().Value() != 256<<20 || container.Resources.Limits.Memory().Value() != 2<<30 {
			t.Fatalf("%s did not retain frozen memory: %#v", container.Name, container.Resources)
		}
	}
	raw, err := runConfigArgument(runtime.Args)
	if err != nil {
		t.Fatal(err)
	}
	var recorded profilingrun.RunConfig
	if err := json.Unmarshal([]byte(raw), &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.Actor.MemoryRequestBytes != 256<<20 || recorded.Actor.MemoryLimitBytes != 2<<30 {
		t.Fatalf("serialized actor differs from actual Job memory: %#v", recorded.Actor)
	}
	if _, created, err := jobs.Ensure(context.Background(), run, resolved); err != nil || created {
		t.Fatalf("recover retained Job: created=%v err=%v", created, err)
	}
}

func TestServingPodAndMeasurementJobProjectTheSameFrozenConfig(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			prepared := currentPreparedRun(t)
			if changed {
				actor := &prepared.RunConfig.Actor
				actor.RuntimeImage = "registry.example/frozen-runtime:changed"
				actor.ArtifactImage, actor.ArtifactFiles = "registry.example/frozen-artifact:changed", []string{"/changed/model.onnx"}
				actor.PreprocessorImage, actor.PreprocessorPath = "registry.example/frozen-preprocessor:changed", "/changed/preprocessor"
				actor.MemoryRequestBytes, actor.MemoryLimitBytes = 256<<20, 2<<30
				actor.GPUUUID, actor.AcceleratorResource = "GPU-frozen", "nvidia.com/gpu"
			}
			actor := prepared.RunConfig.Actor
			job, err := buildExecutionJob(testRun(), resolvedRunForPrepared(t, prepared), testEvidenceStoreBinding(), testExecutionJobOptions(), executionJobTestScheme(t))
			if err != nil {
				t.Fatal(err)
			}
			profile := &serving.Profile{ID: "frozen-profile", RuntimeMetadata: &firstfloor.RuntimeMetadata{GPUUUID: actor.GPUUUID}}
			profile.Condition.Actor = actor
			profile.Condition.NodeUID = prepared.RunConfig.TargetNode.UID
			profile.Condition.Firstfloor = prepared.RunConfig.State.Firstfloor
			profile.Condition.Backend = prepared.RunConfig.State.Backend
			profile.Condition.Artifact.Image = prepared.RunConfig.Images.Artifact
			profile.Condition.Artifact.Files = prepared.RunConfig.State.ArtifactFiles
			profile.Condition.Preprocessor.Image = prepared.RunConfig.State.Preprocessor.Image
			profile.Condition.Preprocessor.Path = prepared.RunConfig.State.Preprocessor.Path
			servingConfig := actor
			servingConfig.Mode, servingConfig.ProfilePath = servingstate.NativeServingMode, "/profile/bundle.json"
			servingConfig.Policy.ProfileID = profile.ID
			spec := servingstate.PrepareSpec{
				Name: "frozen-instance", Config: servingConfig, Store: testEvidenceStoreBinding(),
				Bundle:      evidencestore.ObjectIdentity{Store: testEvidenceStoreBinding().Store, Key: "bundle", Size: 1},
				MemoryBytes: uint64(actor.MemoryLimitBytes),
				Instance: serving.SchedulerInstance{
					Profile: profile, Node: serving.NodeReference{Name: prepared.RunConfig.TargetNode.Name, UID: prepared.RunConfig.TargetNode.UID},
					Allocation: serving.SchedulerAllocation{MemoryBytes: uint64(actor.MemoryLimitBytes), GPUUUID: actor.GPUUUID},
				},
			}
			// No Observer/current-image resolver exists in this actual Pod builder.
			o := &operations.Operations{Config: operations.OperationConfig{Namespace: "measurements"}}
			pod, _, err := o.PreparationPod(spec)
			if err != nil {
				t.Fatal(err)
			}
			worker := pod.Spec.Containers[0]
			measurement := findContainer(t, job.Spec.Template.Spec.Containers, runtimeContainerName)
			if worker.Image != actor.RuntimeImage || measurement.Image != actor.RuntimeImage ||
				!reflect.DeepEqual(worker.Resources, measurement.Resources) {
				t.Fatalf("real Pod/Job performance projection differs: serving=%#v measurement=%#v", worker, measurement)
			}
			for _, container := range []corev1.Container{worker, measurement} {
				if container.Resources.Requests.Memory().Value() != actor.MemoryRequestBytes || container.Resources.Limits.Memory().Value() != actor.MemoryLimitBytes {
					t.Fatalf("%s ignored Config memory mutation", container.Name)
				}
				for _, path := range []string{"/model", "/preprocessor"} {
					if !reflect.DeepEqual(findMountPath(t, worker.VolumeMounts, path), findMountPath(t, measurement.VolumeMounts, path)) {
						t.Fatalf("shared artifact/preprocessor mounts differ at %s", path)
					}
				}
				if changed {
					if !slices.Contains(container.Env, corev1.EnvVar{Name: "CUDA_VISIBLE_DEVICES", Value: actor.GPUUUID}) {
						t.Fatalf("%s ignored Config GPU mutation: %#v", container.Name, container)
					}
					gpu := container.Resources.Limits[corev1.ResourceName(actor.AcceleratorResource)]
					if gpu.Value() != 1 {
						t.Fatalf("%s ignored Config accelerator resource", container.Name)
					}
				} else if _, hasCPU := container.Resources.Limits[corev1.ResourceCPU]; hasCPU || slices.ContainsFunc(container.Env, func(env corev1.EnvVar) bool { return env.Name == "CUDA_VISIBLE_DEVICES" }) {
					t.Fatalf("%s changed default CPU/GPU environment", container.Name)
				}
			}
			for _, containers := range [][]corev1.Container{pod.Spec.InitContainers, job.Spec.Template.Spec.InitContainers} {
				if containers[0].Image != actor.ArtifactImage || containers[1].Image != actor.PreprocessorImage ||
					!slices.Contains(containers[0].Args, actor.ArtifactFiles[0]) || !slices.Contains(containers[1].Args, actor.PreprocessorPath) {
					t.Fatalf("copy init ignored frozen Config recipe: %#v", containers)
				}
			}
			if pod.Spec.InitContainers[2].Image != actor.RuntimeImage {
				t.Fatal("profile materializer looked up another runtime image")
			}
		})
	}
}

func assertDeliveryTopology(t *testing.T, pod corev1.PodSpec, config profilingrun.RunConfig) {
	t.Helper()
	if pod.NodeSelector[corev1.LabelHostname] != config.TargetNode.Name ||
		len(pod.InitContainers) != 2 || len(pod.Containers) != 3 || len(pod.Volumes) != 5 {
		t.Fatalf("unexpected measurement topology: %#v", pod)
	}
	for index, want := range []struct {
		name, image, mount string
		args               []string
	}{
		{"artifact", config.Actor.ArtifactImage, "/chill-artifact",
			append([]string{`cp "$@" /chill-artifact/`, "artifact-copy"}, config.Actor.ArtifactFiles...)},
		{"preprocessor", config.Actor.PreprocessorImage, "/chill-preprocessor",
			[]string{`cp -R "$1"/. /chill-preprocessor/`, "--", config.Actor.PreprocessorPath}},
	} {
		got := pod.InitContainers[index]
		if got.Name != want.name || got.Image != want.image || !reflect.DeepEqual(got.Command, []string{"sh", "-ec"}) ||
			!reflect.DeepEqual(got.Args, want.args) || len(got.VolumeMounts) != 1 || got.VolumeMounts[0].MountPath != want.mount {
			t.Fatalf("%s delivery was not resolved into the Job: %#v", want.name, got)
		}
	}
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
	if len(pod.Volumes) != 12 || len(pod.InitContainers) != 2 {
		t.Fatalf("RKNNLite host volumes = %d, want 12: %#v", len(pod.Volumes), pod.Volumes)
	}
	artifact := pod.InitContainers[0]
	if len(artifact.Args) != 3 || artifact.Args[2] != prepared.RunConfig.State.ArtifactFiles[0] {
		t.Fatalf("RKNNLite artifact delivery drifted: %#v", artifact.Args)
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
	run.Spec.Measurement.StartupWarmupIterations = 0
	run.Spec.Measurement.SettlingDurationSeconds = &settling
	run.Spec.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
	run.Spec.Measurement.Dataset = ""
	run.Spec.Measurement.Workload = ""
	run.Spec.Measurement.OperatingPoints = nil
	prepared := currentPreparedRun(t)
	prepared.RunConfig.LoadOriginNode, prepared.RunConfig.LoadOriginGatewayPod = "", nil
	prepared.RunConfig.Metrics.DurationSeconds = int(run.Spec.MetricObservation.DurationSeconds)
	prepared.RunConfig.Measurement = profilingrun.MeasurementContract{
		Batch: 1, WarmupIterations: 0, SettlingDurationSeconds: 60, WarmupBatchSizes: []int{1},
		DurationSeconds:          30,
		DurationToleranceSeconds: .5,
		WorkloadMode:             profilingrun.IdleModelLoadedWorkloadMode,
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
	if pod := job.Spec.Template.Spec; len(pod.InitContainers) != 2 || pod.InitContainers[1].Name != "preprocessor" {
		t.Fatalf("idle Job must deliver the model and preprocessor only: %#v", pod.InitContainers)
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
	resolved.ExecutionPolicy.Source = profiler.ExecutionPolicySource{}
	resolved.Prepared.RunConfig.Candidate.Inputs.ModelSpec.ResourceVersion = "updated-report"
	if recovered, created, err = jobs.Ensure(ctx, run, resolved); err != nil || created || recovered.UID != testJobUID {
		t.Fatalf("metadata-only change stranded recorded Job: created=%v job=%#v err=%v", created, recovered, err)
	}

	retained := &batchv1.Job{}
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, retained); err != nil {
		t.Fatal(err)
	}
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
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: prepared.RunConfig.TargetNode.Name, UID: types.UID(prepared.RunConfig.TargetNode.UID)},
				Status:     corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4Gi")}},
			}
			baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()
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
	prepared.RunConfig.State.ArtifactFiles = []string{"/artifact/mobilenet-v2-050.onnx"}
	prepared.RunConfig.Images.WorkloadObserver = testWorkloadObserverImage
	prepared.RunConfig.Workload = profilingrun.CurrentWorkloadContract()
	prepared.RunConfig.Actor = prepared.RunConfig.ActorConfig()
	prepared.RunConfig.Actor.MemoryRequestBytes = 128 << 20
	prepared.RunConfig.Actor.MemoryLimitBytes = 1 << 30
	return prepared
}

func currentRKNNPreparedRun(t *testing.T, resourceName string) profiler.PreparedRun {
	t.Helper()
	prepared := currentPreparedRun(t)
	config := &prepared.RunConfig
	config.Candidate.ExecutionPath = profilingrun.RKNNLiteExecutionPath
	config.State.DeviceClass = "orangepi-5-plus-16g"
	config.State.Artifact = "mobilenet-v2-050-rk3588-bs1"
	config.State.ArtifactFiles = []string{"/artifact/mobilenet-v2-050-rk3588-bs1.rknn"}
	config.State.RuntimeFamily = profilingrun.FirstfloorRuntimeFamily
	config.State.Backend = profilingrun.FirstfloorRKNNLiteBackend
	config.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 32}
	config.TargetNode.Name = "orangepi5plus"
	config.TargetNode.Architecture = testARM64Architecture
	config.State.Preprocessor.Architecture = testARM64Architecture
	config.TargetNode.AllocatableCPU = "8"
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
			TargetPlatform: "rk3588", DeclaredBatchSizes: []int32{1},
			Tensors: []modelcompat.InputTensor{{Name: "input", Datatype: "FP32", SampleShape: []int32{224, 224, 3}}},
		},
		RuntimePackages: []profilingrun.CandidateReportRuntimePackage{{Name: "rknn-toolkit-lite2", Version: "2.3.2"}},
		HostContract:    &host,
		RuntimeAccess: &profilingrun.CandidateReportRuntimeAccess{
			Privileged: true, SupplementalGroups: []int64{44, 110},
		},
	}
	config.Actor = config.ActorConfig()
	config.Actor.MemoryRequestBytes = 128 << 20
	config.Actor.MemoryLimitBytes = 1 << 30
	return prepared
}

func testEvidenceStoreBinding() storekube.Binding {
	return storekube.Binding{
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

package controller

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	modelVolumeName                 = "model"
	preprocessorVolumeName          = "preprocessor"
	datasetVolumeName               = "dataset"
	coordinationVolumeName          = "coordination"
	workspaceVolumeName             = "workspace"
	workloadObserverTokenVolumeName = "workload-observer-kube-api"

	modelMountPath                 = "/model"
	preprocessorMountPath          = "/preprocessor"
	datasetMountPath               = "/dataset"
	coordinationMountPath          = "/coordination"
	workspaceMountPath             = "/workspace"
	workloadObserverTokenMountPath = "/var/run/secrets/chill-workload-observer"
	workloadObserverTokenPath      = workloadObserverTokenMountPath + "/token"
	workloadObserverCAPath         = workloadObserverTokenMountPath + "/ca.crt"

	artifactCopyMountPath     = "/chill-artifact"
	preprocessorCopyMountPath = "/chill-preprocessor"
	datasetCopyMountPath      = "/chill-dataset"
)

func buildExecutionJob(
	run *edgev1alpha1.ProfilingRun,
	resolved ResolvedRun,
	binding EvidenceStoreBinding,
	options ExecutionJobOptions,
	scheme *runtime.Scheme,
) (*batchv1.Job, error) {
	if run == nil || scheme == nil {
		return nil, errors.New("ProfilingRun and scheme are required")
	}
	if run.Namespace == "" || run.Name == "" || run.UID == "" {
		return nil, errors.New("ProfilingRun namespace, name, and UID are required")
	}
	if err := validateEvidenceStoreBinding(binding); err != nil {
		return nil, err
	}
	if strings.TrimSpace(options.WorkloadObserverServiceAccountName) == "" {
		return nil, errors.New("WorkloadObserver ServiceAccount name is required")
	}
	if err := validateResolvedRunForRun(run, resolved); err != nil {
		return nil, err
	}
	prepared := resolved.Prepared
	plan, err := profiler.DeriveExecutionPlan(prepared.RunConfig, resolved.ExecutionPolicy)
	if err != nil {
		return nil, fmt.Errorf("derive measurement Job policy: %w", err)
	}
	runtimeProjection, err := projectRuntimeJob(prepared.RunConfig)
	if err != nil {
		return nil, fmt.Errorf("project runtime Job contract: %w", err)
	}
	runConfigBytes, err := prepared.RunConfig.CanonicalJSON()
	if err != nil {
		return nil, fmt.Errorf("encode RunConfig for measurement Job: %w", err)
	}
	runConfig := string(runConfigBytes)
	executionPolicyBytes, err := resolved.ExecutionPolicy.CanonicalJSON()
	if err != nil {
		return nil, fmt.Errorf("encode selected execution policy: %w", err)
	}
	jobName, err := ExecutionJobName(run)
	if err != nil {
		return nil, err
	}
	rawKey := rawEvidenceObjectKey(run.UID)
	labels := map[string]string{
		"app.kubernetes.io/name":                 component.DefaultSystemName,
		"app.kubernetes.io/component":            component.Profiler,
		profilingrun.MeasurementWorkloadLabelKey: profilingrun.MeasurementWorkloadLabelValue,
	}
	annotations := map[string]string{
		executionPolicyAnnotation: string(executionPolicyBytes),
	}
	if prepared.ActuationPolicy != nil {
		actuationPolicyBytes, err := prepared.ActuationPolicy.CanonicalJSON()
		if err != nil {
			return nil, fmt.Errorf("encode selected actuation policy: %w", err)
		}
		annotations[actuationPolicyAnnotation] = string(actuationPolicyBytes)
	}
	deadlineSeconds := int64(plan.ActiveDeadline.Seconds())
	selectorLabelKeys, err := prepared.RunConfig.Workload.LabelKeys()
	if err != nil {
		return nil, fmt.Errorf("resolve workload observation label keys: %w", err)
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: run.Namespace, Name: jobName, Labels: labels, Annotations: annotations,
		},
		Spec: batchv1.JobSpec{
			Parallelism: ptr.To[int32](1), Completions: ptr.To[int32](1), BackoffLimit: ptr.To[int32](0),
			ActiveDeadlineSeconds: ptr.To(deadlineSeconds),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					ServiceAccountName:           options.WorkloadObserverServiceAccountName,
					RestartPolicy:                corev1.RestartPolicyNever,
					HostNetwork:                  true,
					DNSPolicy:                    corev1.DNSClusterFirstWithHostNet,
					NodeSelector: map[string]string{
						corev1.LabelHostname: prepared.RunConfig.TargetNode.Name,
					},
					Tolerations: []corev1.Toleration{{
						Key: "node-role.kubernetes.io/edge", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule,
					}},
					SecurityContext: runtimePodSecurityContext(runtimeProjection.SupplementalGroups),
					InitContainers:  deliveryContainers(prepared.RunConfig),
					Containers: []corev1.Container{
						runtimeContainer(prepared, runConfig, binding, rawKey, plan, runtimeProjection),
						metricObserverContainer(prepared, runConfig, plan),
						workloadObserverContainer(prepared, runConfig, selectorLabelKeys, options, plan),
					},
					Volumes: append(executionVolumes(prepared.RunConfig, plan), runtimeProjection.Volumes...),
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(run, job, scheme); err != nil {
		return nil, fmt.Errorf("set measurement Job owner: %w", err)
	}
	return job, nil
}

// deliveryContainers copies every model file to /model/<basename>, the
// preprocessor directory to /preprocessor, and, for active measurement only,
// the dataset profiling records to /dataset/<recordID>/<field>.
func deliveryContainers(config profilingrun.RunConfig) []corev1.Container {
	containers := []corev1.Container{
		copyContainer("artifact", config.Images.Artifact, modelVolumeName, artifactCopyMountPath,
			append([]string{`cp "$@" ` + artifactCopyMountPath + `/`, "artifact-copy"}, config.State.ArtifactFiles...)),
		copyContainer("preprocessor", config.State.Preprocessor.Image, preprocessorVolumeName, preprocessorCopyMountPath,
			[]string{`cp -R "$1"/. ` + preprocessorCopyMountPath + `/`, "--", config.State.Preprocessor.Path}),
	}
	if dataset := config.Measurement.Dataset; dataset != nil {
		containers = append(containers, copyContainer("dataset", dataset.Image, datasetVolumeName, datasetCopyMountPath,
			[]string{`cp -R "$1"/profiling/. ` + datasetCopyMountPath + `/`, "--", dataset.Path}))
	}
	return containers
}

func copyContainer(name, image, volume, mountPath string, args []string) corev1.Container {
	return corev1.Container{
		Name: name, Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sh", "-ec"},
		Args:            args,
		SecurityContext: restrictedContainerSecurityContext(),
		VolumeMounts:    []corev1.VolumeMount{{Name: volume, MountPath: mountPath}},
	}
}

func runtimeContainer(
	prepared profiler.PreparedRun,
	runConfig string,
	binding EvidenceStoreBinding,
	rawKey string,
	plan profiler.ExecutionPlan,
	projection runtimeJobProjection,
) corev1.Container {
	config := prepared.RunConfig
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(config.CPU.Request),
			corev1.ResourceMemory: *resource.NewQuantity(plan.RuntimeMemoryRequestBytes, resource.BinarySI),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(config.CPU.Limit),
			corev1.ResourceMemory: *resource.NewQuantity(plan.RuntimeMemoryLimitBytes, resource.BinarySI),
		},
	}
	if projection.AcceleratorResource != "" {
		resources.Requests[projection.AcceleratorResource] = resource.MustParse("1")
		resources.Limits[projection.AcceleratorResource] = resource.MustParse("1")
	}
	volumeMounts := []corev1.VolumeMount{
		{Name: modelVolumeName, MountPath: modelMountPath, ReadOnly: true},
		{Name: preprocessorVolumeName, MountPath: preprocessorMountPath, ReadOnly: true},
		{Name: coordinationVolumeName, MountPath: coordinationMountPath},
		{Name: workspaceVolumeName, MountPath: workspaceMountPath},
	}
	if config.Measurement.Dataset != nil {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{Name: datasetVolumeName, MountPath: datasetMountPath, ReadOnly: true})
	}
	volumeMounts = append(volumeMounts, projection.VolumeMounts...)
	return corev1.Container{
		Name: "runtime", Image: config.Images.Runtime, ImagePullPolicy: corev1.PullIfNotPresent,
		Command: []string{"/measurement-worker"},
		Args: []string{
			"--run-config=" + runConfig,
			"--attempt-timeout=" + plan.AttemptTimeout.String(),
			"--observer-ready-timeout=" + plan.ObserverReadyTimeout.String(),
			"--metric-result-timeout=" + plan.MetricResultTimeout.String(),
			"--max-latency-samples=" + strconv.Itoa(plan.MaxLatencySamples),
			"--max-input-bytes=" + strconv.FormatInt(plan.MaxInputBytes, 10),
			"--max-runtime-result-bytes=" + strconv.FormatInt(plan.MaxRuntimeResultBytes, 10),
			"--max-metric-result-bytes=" + strconv.FormatInt(plan.MaxMetricResultBytes, 10),
			"--workload-result-timeout=" + plan.WorkloadResultTimeout.String(),
			"--max-workload-result-bytes=" + strconv.FormatInt(plan.MaxWorkloadResultBytes, 10),
			"--store-name=" + binding.Store,
			"--store-endpoint=" + binding.Endpoint,
			"--store-bucket=" + binding.Bucket,
			"--store-region=" + binding.Region,
			"--raw-object-key=" + rawKey,
		},
		Env: []corev1.EnvVar{
			fieldEnv("POD_UID", "metadata.uid"), fieldEnv("NODE_NAME", "spec.nodeName"),
			secretEnv("AWS_ACCESS_KEY_ID", binding.CredentialsSecretName, component.EvidenceStoreAccessKeyKey),
			secretEnv("AWS_SECRET_ACCESS_KEY", binding.CredentialsSecretName, component.EvidenceStoreSecretKeyKey),
		},
		Resources:                resources,
		SecurityContext:          projection.SecurityContext,
		VolumeMounts:             volumeMounts,
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}
}

func metricObserverContainer(
	prepared profiler.PreparedRun,
	runConfig string,
	plan profiler.ExecutionPlan,
) corev1.Container {
	return corev1.Container{
		Name: "metric-observer", Image: prepared.RunConfig.Images.MetricObserver, ImagePullPolicy: corev1.PullIfNotPresent,
		Args: []string{
			"--run-config=" + runConfig,
			"--start-wait-timeout=" + plan.ObserverStartTimeout.String(),
			"--end-wait-timeout=" + plan.AttemptTimeout.String(),
			"--max-events=" + strconv.Itoa(plan.MaxMetricEvents),
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(plan.ObserverCPURequest),
				corev1.ResourceMemory: *resource.NewQuantity(plan.ObserverMemoryRequestBytes, resource.BinarySI),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(plan.ObserverCPULimit),
				corev1.ResourceMemory: *resource.NewQuantity(plan.ObserverMemoryLimitBytes, resource.BinarySI),
			},
		},
		SecurityContext: restrictedContainerSecurityContext(),
		VolumeMounts: []corev1.VolumeMount{
			{Name: coordinationVolumeName, MountPath: coordinationMountPath},
		},
	}
}

func workloadObserverContainer(
	prepared profiler.PreparedRun,
	runConfig string,
	selectorLabelKeys []string,
	options ExecutionJobOptions,
	plan profiler.ExecutionPlan,
) corev1.Container {
	arguments := []string{
		"--run-config=" + runConfig,
		"--selector-label-keys=" + strings.Join(selectorLabelKeys, ","),
		"--end-wait-timeout=" + plan.AttemptTimeout.String(),
		"--end-fence-timeout=" + plan.WorkloadEndFenceTimeout.String(),
		"--max-events=" + strconv.Itoa(plan.MaxWorkloadEvents),
		"--max-result-bytes=" + strconv.FormatInt(plan.MaxWorkloadResultBytes, 10),
		"--kube-api-token-file=" + workloadObserverTokenPath,
		"--kube-api-ca-file=" + workloadObserverCAPath,
	}
	if options.WorkloadObserverKubeAPIServer != "" {
		arguments = append(arguments, "--kube-api-server="+options.WorkloadObserverKubeAPIServer)
	}
	return corev1.Container{
		Name: "workload-observer", Image: prepared.RunConfig.Images.WorkloadObserver, ImagePullPolicy: corev1.PullIfNotPresent,
		Args: arguments,
		Env: []corev1.EnvVar{
			fieldEnv("POD_NAMESPACE", "metadata.namespace"),
			fieldEnv("POD_NAME", "metadata.name"),
			fieldEnv("POD_UID", "metadata.uid"),
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(plan.WorkloadObserverCPURequest),
				corev1.ResourceMemory: *resource.NewQuantity(plan.WorkloadObserverMemoryRequestBytes, resource.BinarySI),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(plan.WorkloadObserverCPULimit),
				corev1.ResourceMemory: *resource.NewQuantity(plan.WorkloadObserverMemoryLimitBytes, resource.BinarySI),
			},
		},
		SecurityContext: restrictedContainerSecurityContext(),
		VolumeMounts: []corev1.VolumeMount{
			{Name: coordinationVolumeName, MountPath: coordinationMountPath},
			{Name: workloadObserverTokenVolumeName, MountPath: workloadObserverTokenMountPath, ReadOnly: true},
		},
	}
}

func executionVolumes(config profilingrun.RunConfig, plan profiler.ExecutionPlan) []corev1.Volume {
	emptyDir := corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
	volumes := []corev1.Volume{
		{Name: modelVolumeName, VolumeSource: emptyDir},
		{Name: preprocessorVolumeName, VolumeSource: emptyDir},
		{Name: coordinationVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
			SizeLimit: resource.NewQuantity(plan.CoordinationBytes, resource.BinarySI),
		}}},
		{Name: workspaceVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
			SizeLimit: resource.NewQuantity(plan.WorkspaceBytes, resource.BinarySI),
		}}},
		{Name: workloadObserverTokenVolumeName, VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
			Sources: []corev1.VolumeProjection{
				{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
					Path: "token", ExpirationSeconds: ptr.To[int64](3600),
				}},
				{ConfigMap: &corev1.ConfigMapProjection{
					LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"},
					Items:                []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}},
				}},
			},
		}}},
	}
	if config.Measurement.Dataset != nil {
		volumes = append(volumes, corev1.Volume{Name: datasetVolumeName, VolumeSource: emptyDir})
	}
	return volumes
}

func restrictedPodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To[int64](65532), RunAsGroup: ptr.To[int64](65532),
		FSGroup: ptr.To[int64](65532), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func runtimePodSecurityContext(supplementalGroups []int64) *corev1.PodSecurityContext {
	security := restrictedPodSecurityContext()
	security.SupplementalGroups = append([]int64(nil), supplementalGroups...)
	return security
}

func restrictedContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
		Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

func fieldEnv(name, field string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: field}}}
}

func secretEnv(name, secretName, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: key,
	}}}
}

func validateEvidenceStoreBinding(binding EvidenceStoreBinding) error {
	return binding.Validate()
}

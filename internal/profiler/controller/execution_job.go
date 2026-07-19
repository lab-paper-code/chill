package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/profiler"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	executionPolicyAnnotation  = "edge.dacs.io/profiling-worker-policy"
	executionJobHashCharacters = 32

	intentVolumeName       = "intent"
	modelVolumeName        = "model"
	coordinationVolumeName = "coordination"
	workspaceVolumeName    = "workspace"

	intentMountPath       = "/etc/chill/run"
	modelMountPath        = "/model"
	coordinationMountPath = "/coordination"
	workspaceMountPath    = "/workspace"
)

// EvidenceStoreBindingResolver resolves the current CHILL-owned delivery
// binding. It returns no credential bytes.
type EvidenceStoreBindingResolver interface {
	Resolve(context.Context) (EvidenceStoreBinding, error)
}

// ExecutionJobs owns only deterministic Job materialization and recovery.
// Status publication remains the reconciler's responsibility.
type ExecutionJobs struct {
	client client.Client
	scheme *runtime.Scheme
	stores EvidenceStoreBindingResolver
}

func NewExecutionJobs(
	kubeClient client.Client,
	scheme *runtime.Scheme,
	stores EvidenceStoreBindingResolver,
) (*ExecutionJobs, error) {
	if kubeClient == nil || scheme == nil || stores == nil {
		return nil, errors.New("Kubernetes client, scheme, and Evidence Store resolver are required")
	}
	return &ExecutionJobs{client: kubeClient, scheme: scheme, stores: stores}, nil
}

func (j *ExecutionJobs) Ensure(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	prepared profiler.PreparedRun,
) (*batchv1.Job, bool, error) {
	jobName, err := executionJobName(run)
	if err != nil {
		return nil, false, err
	}
	key := types.NamespacedName{Namespace: run.Namespace, Name: jobName}
	existing := &batchv1.Job{}
	if err := j.client.Get(ctx, key, existing); err == nil {
		binding, err := executionJobEvidenceStoreBinding(existing)
		if err != nil {
			return nil, false, err
		}
		desired, err := buildExecutionJob(run, prepared, binding, j.scheme)
		if err != nil {
			return nil, false, err
		}
		if err := verifyExecutionJob(run, existing, desired); err != nil {
			return nil, false, err
		}
		return existing, false, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, false, fmt.Errorf("get measurement Job %s: %w", key, err)
	}

	binding, err := j.stores.Resolve(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("resolve Evidence Store delivery binding: %w", err)
	}
	desired, err := buildExecutionJob(run, prepared, binding, j.scheme)
	if err != nil {
		return nil, false, err
	}
	if err := j.client.Create(ctx, desired); err == nil {
		return desired, true, nil
	} else if !apierrors.IsAlreadyExists(err) {
		return nil, false, fmt.Errorf("create measurement Job %s: %w", key, err)
	}
	if err := j.client.Get(ctx, key, existing); err != nil {
		return nil, false, fmt.Errorf("get concurrently created measurement Job %s: %w", key, err)
	}
	retainedBinding, err := executionJobEvidenceStoreBinding(existing)
	if err != nil {
		return nil, false, err
	}
	desired, err = buildExecutionJob(run, prepared, retainedBinding, j.scheme)
	if err != nil {
		return nil, false, err
	}
	if err := verifyExecutionJob(run, existing, desired); err != nil {
		return nil, false, err
	}
	return existing, false, nil
}

func executionJobEvidenceStoreBinding(job *batchv1.Job) (EvidenceStoreBinding, error) {
	if job == nil {
		return EvidenceStoreBinding{}, errors.New("measurement Job is required")
	}
	runtimeContainer, ok := containerNamed(job.Spec.Template.Spec.Containers, "runtime")
	if !ok {
		return EvidenceStoreBinding{}, errors.New("existing measurement Job has no runtime container")
	}
	binding := EvidenceStoreBinding{
		Store:    argumentValue(runtimeContainer.Args, "--store-name="),
		Endpoint: argumentValue(runtimeContainer.Args, "--store-endpoint="),
		Bucket:   argumentValue(runtimeContainer.Args, "--store-bucket="),
		Region:   argumentValue(runtimeContainer.Args, "--store-region="),
	}
	accessSecret, accessKey := secretReference(runtimeContainer.Env, "AWS_ACCESS_KEY_ID")
	secretSecret, secretKey := secretReference(runtimeContainer.Env, "AWS_SECRET_ACCESS_KEY")
	if accessSecret == "" || accessSecret != secretSecret ||
		accessKey != component.EvidenceStoreAccessKeyKey || secretKey != component.EvidenceStoreSecretKeyKey {
		return EvidenceStoreBinding{}, errors.New("existing measurement Job has an invalid Evidence Store Secret binding")
	}
	binding.CredentialsSecretName = accessSecret
	if err := validateEvidenceStoreBinding(binding); err != nil {
		return EvidenceStoreBinding{}, fmt.Errorf("validate existing measurement Job delivery binding: %w", err)
	}
	return binding, nil
}

func containerNamed(containers []corev1.Container, name string) (corev1.Container, bool) {
	for _, container := range containers {
		if container.Name == name {
			return container, true
		}
	}
	return corev1.Container{}, false
}

func argumentValue(arguments []string, prefix string) string {
	value := ""
	for _, argument := range arguments {
		if strings.HasPrefix(argument, prefix) {
			if value != "" {
				return ""
			}
			value = strings.TrimPrefix(argument, prefix)
		}
	}
	return value
}

func secretReference(env []corev1.EnvVar, name string) (string, string) {
	for _, variable := range env {
		if variable.Name == name && variable.ValueFrom != nil && variable.ValueFrom.SecretKeyRef != nil {
			return variable.ValueFrom.SecretKeyRef.Name, variable.ValueFrom.SecretKeyRef.Key
		}
	}
	return "", ""
}

func buildExecutionJob(
	run *edgev1alpha1.ProfilingRun,
	prepared profiler.PreparedRun,
	binding EvidenceStoreBinding,
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
	if _, err := validatePreparedForRun(run, prepared); err != nil {
		return nil, err
	}
	plan, err := profiler.DeriveExecutionPlan(prepared.Intent)
	if err != nil {
		return nil, fmt.Errorf("derive measurement Job policy: %w", err)
	}
	intentName, err := intentRecordName(run)
	if err != nil {
		return nil, err
	}
	jobName, err := executionJobName(run)
	if err != nil {
		return nil, err
	}
	rawKey := rawEvidenceObjectKey(run.UID, prepared.IntentDigest)
	policyIdentity := plan.Policy + "/" + plan.PolicyVersion
	labels := map[string]string{
		"app.kubernetes.io/name":      component.DefaultSystemName,
		"app.kubernetes.io/component": component.Profiler,
	}
	annotations := map[string]string{
		intentDigestAnnotation:    prepared.IntentDigest,
		executionPolicyAnnotation: policyIdentity,
	}
	deadlineSeconds := int64(plan.ActiveDeadline.Seconds())

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: run.Namespace, Name: jobName, Labels: labels, Annotations: annotations,
		},
		Spec: batchv1.JobSpec{
			Parallelism: ptr.To[int32](1), Completions: ptr.To[int32](1), BackoffLimit: ptr.To[int32](0),
			ActiveDeadlineSeconds: ptr.To(deadlineSeconds),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					RestartPolicy:                corev1.RestartPolicyNever,
					NodeSelector: map[string]string{
						corev1.LabelHostname: prepared.Intent.TargetNode.Name,
					},
					Tolerations: []corev1.Toleration{{
						Key: "node-role.kubernetes.io/edge", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule,
					}},
					SecurityContext: restrictedPodSecurityContext(),
					InitContainers:  []corev1.Container{artifactContainer(prepared.Intent)},
					Containers: []corev1.Container{
						runtimeContainer(prepared, binding, rawKey, plan),
						powerObserverContainer(prepared, plan),
					},
					Volumes: executionVolumes(intentName, plan),
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(run, job, scheme); err != nil {
		return nil, fmt.Errorf("set measurement Job owner: %w", err)
	}
	return job, nil
}

func artifactContainer(intent profilingrun.Intent) corev1.Container {
	digest := strings.TrimPrefix(intent.State.ArtifactDigest, "sha256:")
	return corev1.Container{
		Name: "artifact", Image: intent.Images.Artifact, ImagePullPolicy: corev1.PullIfNotPresent,
		Command: []string{"sh", "-ec"},
		Args: []string{
			`cp "$1" /model/model.onnx && echo "$2  /model/model.onnx" | sha256sum -c -`,
			"artifact-copy", intent.State.ArtifactPath, digest,
		},
		SecurityContext: restrictedContainerSecurityContext(),
		VolumeMounts:    []corev1.VolumeMount{{Name: modelVolumeName, MountPath: modelMountPath}},
	}
}

func runtimeContainer(
	prepared profiler.PreparedRun,
	binding EvidenceStoreBinding,
	rawKey string,
	plan profiler.ExecutionPlan,
) corev1.Container {
	intent := prepared.Intent
	return corev1.Container{
		Name: "runtime", Image: intent.Images.Runtime, ImagePullPolicy: corev1.PullIfNotPresent,
		Args: []string{
			"--intent-digest=" + prepared.IntentDigest,
			"--attempt-timeout=" + plan.AttemptTimeout.String(),
			"--observer-ready-timeout=" + plan.ObserverReadyTimeout.String(),
			"--power-result-timeout=" + plan.PowerResultTimeout.String(),
			"--max-latency-samples=" + strconv.Itoa(plan.MaxLatencySamples),
			"--max-input-bytes=" + strconv.FormatInt(plan.MaxInputBytes, 10),
			"--max-runtime-result-bytes=" + strconv.FormatInt(plan.MaxRuntimeResultBytes, 10),
			"--max-power-result-bytes=" + strconv.FormatInt(plan.MaxPowerResultBytes, 10),
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
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(intent.CPU.Request),
				corev1.ResourceMemory: *resource.NewQuantity(plan.RuntimeMemoryRequestBytes, resource.BinarySI),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(intent.CPU.Limit),
				corev1.ResourceMemory: *resource.NewQuantity(plan.RuntimeMemoryLimitBytes, resource.BinarySI),
			},
		},
		SecurityContext: restrictedContainerSecurityContext(),
		VolumeMounts: []corev1.VolumeMount{
			{Name: intentVolumeName, MountPath: intentMountPath, ReadOnly: true},
			{Name: modelVolumeName, MountPath: modelMountPath, ReadOnly: true},
			{Name: coordinationVolumeName, MountPath: coordinationMountPath},
			{Name: workspaceVolumeName, MountPath: workspaceMountPath},
		},
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}
}

func powerObserverContainer(prepared profiler.PreparedRun, plan profiler.ExecutionPlan) corev1.Container {
	return corev1.Container{
		Name: "power-observer", Image: prepared.Intent.Images.PowerObserver, ImagePullPolicy: corev1.PullIfNotPresent,
		Args: []string{
			"--intent-digest=" + prepared.IntentDigest,
			"--start-wait-timeout=" + plan.ObserverStartTimeout.String(),
			"--max-events=" + strconv.Itoa(plan.MaxPowerEvents),
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
			{Name: intentVolumeName, MountPath: intentMountPath, ReadOnly: true},
			{Name: coordinationVolumeName, MountPath: coordinationMountPath},
		},
	}
}

func executionVolumes(intentName string, plan profiler.ExecutionPlan) []corev1.Volume {
	return []corev1.Volume{
		{
			Name: intentVolumeName,
			VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: intentName},
				Items:                []corev1.KeyToPath{{Key: intentRecordIntentKey, Path: "intent.json"}},
			}},
		},
		{Name: modelVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: coordinationVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
			SizeLimit: resource.NewQuantity(plan.CoordinationBytes, resource.BinarySI),
		}}},
		{Name: workspaceVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
			SizeLimit: resource.NewQuantity(plan.WorkspaceBytes, resource.BinarySI),
		}}},
	}
}

func restrictedPodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To[int64](65532), RunAsGroup: ptr.To[int64](65532),
		FSGroup: ptr.To[int64](65532), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
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
	for name, value := range map[string]string{
		"store": binding.Store, "endpoint": binding.Endpoint, "bucket": binding.Bucket,
		"region": binding.Region, "credentials Secret": binding.CredentialsSecretName,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("Evidence Store binding %s is required without surrounding whitespace", name)
		}
	}
	return nil
}

func executionJobName(run *edgev1alpha1.ProfilingRun) (string, error) {
	if run == nil || run.UID == "" {
		return "", errors.New("ProfilingRun UID is required")
	}
	sum := sha256.Sum256([]byte(run.UID))
	return "profilingrun-" + hex.EncodeToString(sum[:])[:executionJobHashCharacters], nil
}

func rawEvidenceObjectKey(uid types.UID, digest string) string {
	return "profiling-runs/" + string(uid) + "/raw/" + strings.TrimPrefix(digest, "sha256:") + ".json"
}

func verifyExecutionJob(run *edgev1alpha1.ProfilingRun, existing, desired *batchv1.Job) error {
	if existing.Namespace != desired.Namespace || existing.Name != desired.Name || !metav1.IsControlledBy(existing, run) {
		return errors.New("existing measurement Job identity or ownership mismatch")
	}
	if existing.Annotations[intentDigestAnnotation] != desired.Annotations[intentDigestAnnotation] ||
		existing.Annotations[executionPolicyAnnotation] != desired.Annotations[executionPolicyAnnotation] {
		return errors.New("existing measurement Job execution identity mismatch")
	}
	// Job specs are immutable. Fake clients do not apply API defaults, while a
	// real API server does, so compare the fields CHILL owns before creation.
	if !apiequality.Semantic.DeepDerivative(desired.Spec.Template.Spec, existing.Spec.Template.Spec) ||
		existing.Spec.Parallelism == nil || *existing.Spec.Parallelism != 1 ||
		existing.Spec.Completions == nil || *existing.Spec.Completions != 1 ||
		existing.Spec.BackoffLimit == nil || *existing.Spec.BackoffLimit != 0 ||
		existing.Spec.ActiveDeadlineSeconds == nil ||
		*existing.Spec.ActiveDeadlineSeconds != *desired.Spec.ActiveDeadlineSeconds {
		return errors.New("existing measurement Job does not match the frozen execution contract")
	}
	return nil
}

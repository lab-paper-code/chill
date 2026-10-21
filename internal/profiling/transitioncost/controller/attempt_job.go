package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	transitionAttemptSchemaVersion     = "chill.dacs.io/transition-observer-attempt.v1alpha5"
	transitionAttemptJSONEnvName       = "CHILL_TRANSITION_ATTEMPT_JSON"
	transitionObserverBinaryPath       = "/transition-observer"
	transitionContractMaximumBytesFlag = "--max-contract-bytes="
	transitionDirectionIndexAnnotation = "edge.dacs.io/transition-direction-index"
	transitionCoordinationVolumeName   = "coordination"
	transitionWorkspaceVolumeName      = "workspace"
	transitionCoordinationMountPath    = "/coordination"
	transitionWorkspaceMountPath       = "/workspace"
	transitionObserverContainerName    = "transition-observer"
)

// AttemptJobRequest selects one direction and the name of the one
// attempt-scoped observer Job that watches its Node power-mode transition.
type AttemptJobRequest struct {
	Measurement    *edgev1alpha1.PowerModeTransitionCost
	Prepared       PreparedMeasurement
	DirectionIndex int
	Name           string
	RawLocation    evidencestore.ObjectLocation
	StoreBinding   storekube.Binding
}

// AttemptJobOptions contains operational Job material supplied by static
// Profiler configuration. None of these values changes the measured window.
type AttemptJobOptions struct {
	ObserverImage          string
	ImagePullPolicy        corev1.PullPolicy
	ServiceAccountName     string
	ActiveDeadlineOverhead time.Duration
	CoordinationBytes      int64
	WorkspaceBytes         int64
	Resources              corev1.ResourceRequirements
	Tolerations            []corev1.Toleration
}

type attemptPayload struct {
	SchemaVersion    string                       `json:"schemaVersion"`
	PowerSource      attemptPowerSource           `json:"powerSource"`
	PowerObservation attemptPowerObservation      `json:"powerObservation"`
	TransitionFacts  attemptTransitionFacts       `json:"transitionFacts"`
	RawLocation      evidencestore.ObjectLocation `json:"rawLocation"`
}

type attemptPowerSource struct {
	Kind         string            `json:"kind"`
	Endpoint     string            `json:"endpoint"`
	Metric       string            `json:"metric"`
	MetricLabels map[string]string `json:"metricLabels"`
}

type attemptPowerObservation struct {
	IntervalMilliseconds       int64 `json:"intervalMilliseconds"`
	DurationMilliseconds       int64 `json:"durationMilliseconds"`
	RequestTimeoutMilliseconds int64 `json:"requestTimeoutMilliseconds"`
	MaxEvents                  int   `json:"maxEvents"`
}

type attemptTransitionFacts struct {
	Namespace                      string                         `json:"namespace"`
	SourcePowerMode                string                         `json:"sourcePowerMode"`
	SourceActuator                 edgev1alpha1.PowerModeActuator `json:"sourceActuator"`
	TargetPowerMode                string                         `json:"targetPowerMode"`
	TargetActuator                 edgev1alpha1.PowerModeActuator `json:"targetActuator"`
	TargetNode                     string                         `json:"targetNode"`
	TargetNodeUID                  string                         `json:"targetNodeUID"`
	DeviceType                     string                         `json:"deviceType"`
	Model                          string                         `json:"model"`
	ServingService                 string                         `json:"servingService"`
	ServingContainer               string                         `json:"servingContainer"`
	PollIntervalMilliseconds       int64                          `json:"pollIntervalMilliseconds"`
	ObservationTimeoutMilliseconds int64                          `json:"observationTimeoutMilliseconds"`
}

// BuildAttemptJob projects one prepared direction into one immutable native
// Job. It creates no object and does not request the power mode.
func BuildAttemptJob(
	request AttemptJobRequest,
	options AttemptJobOptions,
	scheme *runtime.Scheme,
) (*batchv1.Job, error) {
	direction, err := validateAttemptJobRequest(request, options, scheme)
	if err != nil {
		return nil, err
	}
	payload, err := encodeAttemptPayload(request, direction)
	if err != nil {
		return nil, err
	}
	activeDeadline := request.Prepared.PowerRequest.Duration + options.ActiveDeadlineOverhead
	deadlineSeconds := durationSecondsCeiling(activeDeadline)
	labels := map[string]string{
		"app.kubernetes.io/name":                 component.DefaultSystemName,
		"app.kubernetes.io/component":            transitionObserverContainerName,
		profilingrun.MeasurementWorkloadLabelKey: profilingrun.TransitionObserverRole,
	}
	annotations := map[string]string{
		transitionDirectionIndexAnnotation: strconv.Itoa(request.DirectionIndex),
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: request.Measurement.Namespace, Name: request.Name,
			Labels: labels, Annotations: annotations,
		},
		Spec: batchv1.JobSpec{
			Parallelism: ptr.To[int32](1), Completions: ptr.To[int32](1), BackoffLimit: ptr.To[int32](0),
			ActiveDeadlineSeconds: ptr.To(deadlineSeconds),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: maps.Clone(labels), Annotations: maps.Clone(annotations)},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(true),
					ServiceAccountName:           options.ServiceAccountName,
					RestartPolicy:                corev1.RestartPolicyNever,
					NodeSelector: map[string]string{
						corev1.LabelHostname: request.Prepared.ObserverNodeName,
					},
					Tolerations:     append(cloneTolerations(options.Tolerations), profilingrun.MeasurementToleration()),
					SecurityContext: restrictedAttemptPodSecurityContext(),
					Containers: []corev1.Container{
						attemptContainer(request.StoreBinding, options, string(payload)),
					},
					Volumes: attemptVolumes(options),
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(request.Measurement, job, scheme); err != nil {
		return nil, fmt.Errorf("set transition observer Job owner: %w", err)
	}
	return job, nil
}

func validateAttemptJobRequest(
	request AttemptJobRequest,
	options AttemptJobOptions,
	scheme *runtime.Scheme,
) (PreparedDirection, error) {
	if request.Measurement == nil || scheme == nil {
		return PreparedDirection{}, errors.New("PowerModeTransitionCost and scheme are required")
	}
	measurement := request.Measurement
	if !clean(measurement.Namespace) || !clean(measurement.Name) || measurement.UID == "" {
		return PreparedDirection{}, errors.New("PowerModeTransitionCost namespace, name, and UID are required")
	}
	if request.Prepared.Namespace != measurement.Namespace ||
		request.Prepared.TargetNode.Name != measurement.Spec.TargetNode {
		return PreparedDirection{}, errors.New("prepared measurement does not match the PowerModeTransitionCost")
	}
	if request.DirectionIndex < 0 || request.DirectionIndex >= len(request.Prepared.Directions) {
		return PreparedDirection{}, errors.New("transition direction index must be zero or one")
	}
	if errs := validation.IsDNS1123Subdomain(request.Name); len(errs) > 0 {
		return PreparedDirection{}, fmt.Errorf("transition attempt Job name is invalid: %s", strings.Join(errs, "; "))
	}
	if err := request.RawLocation.Validate(); err != nil {
		return PreparedDirection{}, fmt.Errorf("validate raw evidence location: %w", err)
	}
	if err := request.StoreBinding.Validate(); err != nil {
		return PreparedDirection{}, err
	}
	if request.RawLocation.Store != request.Prepared.EvidenceStore ||
		request.RawLocation.Store != request.StoreBinding.Store {
		return PreparedDirection{}, errors.New("raw evidence store differs from the preparation or delivery binding")
	}
	for name, duration := range map[string]time.Duration{
		"power interval":           request.Prepared.PowerRequest.Interval,
		"power duration":           request.Prepared.PowerRequest.Duration,
		"power request timeout":    request.Prepared.PowerRequest.RequestTimeout,
		"fact poll interval":       request.Prepared.FactPollInterval,
		"fact observation timeout": request.Prepared.FactObservationTimeout,
	} {
		if duration%time.Millisecond != 0 {
			return PreparedDirection{}, fmt.Errorf("%s must be an exact millisecond duration", name)
		}
	}
	if !clean(options.ObserverImage) || !clean(options.ServiceAccountName) {
		return PreparedDirection{}, errors.New("observer image and ServiceAccount are required")
	}
	switch options.ImagePullPolicy {
	case corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever:
	default:
		return PreparedDirection{}, errors.New("observer image pull policy is invalid")
	}
	if options.ActiveDeadlineOverhead <= 0 {
		return PreparedDirection{}, errors.New("Job active deadline overhead must be positive")
	}
	if request.Prepared.PowerRequest.Duration > time.Duration(math.MaxInt64)-options.ActiveDeadlineOverhead {
		return PreparedDirection{}, errors.New("Job active deadline overflows time.Duration")
	}
	if options.CoordinationBytes < 1 || options.WorkspaceBytes < 1 {
		return PreparedDirection{}, errors.New("coordination and workspace byte limits must be positive")
	}
	return request.Prepared.Directions[request.DirectionIndex], nil
}

func encodeAttemptPayload(request AttemptJobRequest, direction PreparedDirection) ([]byte, error) {
	power := request.Prepared.PowerRequest
	payload, err := json.Marshal(attemptPayload{
		SchemaVersion: transitionAttemptSchemaVersion,
		PowerSource: attemptPowerSource{
			Kind: request.Prepared.PowerSource.Kind, Endpoint: request.Prepared.PowerSource.Endpoint,
			Metric:       request.Prepared.PowerSource.Metric,
			MetricLabels: maps.Clone(request.Prepared.PowerSource.MetricLabels),
		},
		PowerObservation: attemptPowerObservation{
			IntervalMilliseconds: power.Interval.Milliseconds(), DurationMilliseconds: power.Duration.Milliseconds(),
			RequestTimeoutMilliseconds: power.RequestTimeout.Milliseconds(), MaxEvents: power.MaxEvents,
		},
		TransitionFacts: attemptTransitionFacts{
			Namespace:       request.Prepared.Namespace,
			SourcePowerMode: direction.FrozenContract.SourceState.PowerMode,
			SourceActuator:  request.Prepared.Directions[1-request.DirectionIndex].Actuator,
			TargetPowerMode: direction.FrozenContract.TargetState.PowerMode,
			TargetActuator:  direction.Actuator,
			TargetNode:      request.Prepared.TargetNode.Name,
			TargetNodeUID:   request.Prepared.TargetNode.UID,
			DeviceType:      direction.FrozenContract.TargetState.DeviceType,
			Model:           direction.FrozenContract.TargetState.Model,
			ServingService:  request.Prepared.ServingService, ServingContainer: request.Prepared.ServingContainer,
			PollIntervalMilliseconds:       request.Prepared.FactPollInterval.Milliseconds(),
			ObservationTimeoutMilliseconds: request.Prepared.FactObservationTimeout.Milliseconds(),
		},
		RawLocation: request.RawLocation,
	})
	if err != nil {
		return nil, fmt.Errorf("encode transition observer attempt: %w", err)
	}
	return payload, nil
}

func attemptContainer(
	binding storekube.Binding,
	options AttemptJobOptions,
	payload string,
) corev1.Container {
	return corev1.Container{
		Name: transitionObserverContainerName, Image: options.ObserverImage,
		ImagePullPolicy: options.ImagePullPolicy, Command: []string{transitionObserverBinaryPath},
		Args: []string{
			transitionContractMaximumBytesFlag + strconv.Itoa(len(payload)),
			"--store-endpoint=" + binding.Endpoint,
			"--store-bucket=" + binding.Bucket,
			"--store-region=" + binding.Region,
		},
		Env: []corev1.EnvVar{
			{Name: transitionAttemptJSONEnvName, Value: payload},
			secretAttemptEnv("AWS_ACCESS_KEY_ID", binding.CredentialsSecretName, component.EvidenceStoreAccessKeyKey),
			secretAttemptEnv("AWS_SECRET_ACCESS_KEY", binding.CredentialsSecretName, component.EvidenceStoreSecretKeyKey),
		},
		Resources:       *options.Resources.DeepCopy(),
		SecurityContext: restrictedAttemptContainerSecurityContext(),
		VolumeMounts: []corev1.VolumeMount{
			{Name: transitionCoordinationVolumeName, MountPath: transitionCoordinationMountPath},
			{Name: transitionWorkspaceVolumeName, MountPath: transitionWorkspaceMountPath},
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
				Command: []string{transitionObserverBinaryPath, "--probe-ready-file"},
			}},
			PeriodSeconds: 1, TimeoutSeconds: 1,
		},
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}
}

func attemptVolumes(options AttemptJobOptions) []corev1.Volume {
	return []corev1.Volume{
		{
			Name: transitionCoordinationVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
				SizeLimit: resource.NewQuantity(options.CoordinationBytes, resource.BinarySI),
			}},
		},
		{
			Name: transitionWorkspaceVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
				SizeLimit: resource.NewQuantity(options.WorkspaceBytes, resource.BinarySI),
			}},
		},
	}
}

func secretAttemptEnv(name, secretName, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: key,
	}}}
}

func restrictedAttemptPodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To[int64](65532), RunAsGroup: ptr.To[int64](65532),
		FSGroup: ptr.To[int64](65532), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func restrictedAttemptContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
		Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

func cloneTolerations(source []corev1.Toleration) []corev1.Toleration {
	cloned := make([]corev1.Toleration, len(source))
	for index := range source {
		source[index].DeepCopyInto(&cloned[index])
	}
	return cloned
}

func durationSecondsCeiling(duration time.Duration) int64 {
	seconds := int64(duration / time.Second)
	if duration%time.Second != 0 {
		seconds++
	}
	return seconds
}

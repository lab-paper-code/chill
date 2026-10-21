package controller

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestBuildAttemptJobProjectsOneOffTargetAttempt(t *testing.T) {
	request, options, scheme := validAttemptJobInputs(t)
	tolerationSeconds := *options.Tolerations[0].TolerationSeconds

	job, err := BuildAttemptJob(request, options, scheme)
	if err != nil {
		t.Fatal(err)
	}
	assertAttemptJobIdentityAndBounds(t, job, request)
	if job.Spec.Template.Labels[profilingrun.MeasurementWorkloadLabelKey] != profilingrun.TransitionObserverRole {
		t.Fatalf("Pod labels = %#v", job.Spec.Template.Labels)
	}
	pod := job.Spec.Template.Spec
	if !slices.Contains(pod.Tolerations, corev1.Toleration{Key: "chill.dacs.io/measurement", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}) {
		t.Fatalf("pod tolerations = %#v, want measurement toleration", pod.Tolerations)
	}
	assertAttemptPod(t, pod, request, options)
	container := pod.Containers[0]
	assertAttemptContainer(t, container, options)
	payload := envValue(container.Env, transitionAttemptJSONEnvName)
	if payload == "" || argumentValueForTest(container.Args, transitionContractMaximumBytesFlag) != len(payload) {
		t.Fatalf("inline attempt bytes = %d, args = %v", len(payload), container.Args)
	}
	var wire attemptPayload
	if err := json.Unmarshal([]byte(payload), &wire); err != nil {
		t.Fatal(err)
	}
	assertAttemptPayload(t, wire, payload, container.Env, request)

	request.Prepared.PowerSource.MetricLabels["device_type"] = "caller-mutation"
	options.Tolerations[0].TolerationSeconds = new(int64)
	options.Resources.Requests[corev1.ResourceCPU] = resource.MustParse("9")
	if pod.Tolerations[0].TolerationSeconds == nil || *pod.Tolerations[0].TolerationSeconds != tolerationSeconds ||
		container.Resources.Requests.Cpu().String() != "10m" ||
		wire.PowerSource.MetricLabels["device_type"] != "device-b" {
		t.Fatal("Job aliases caller-owned attempt material")
	}
}

func assertAttemptJobIdentityAndBounds(
	t *testing.T,
	job *batchv1.Job,
	request AttemptJobRequest,
) {
	t.Helper()
	if job.Namespace != request.Measurement.Namespace || job.Name != request.Name ||
		len(job.OwnerReferences) != 1 || job.OwnerReferences[0].UID != request.Measurement.UID ||
		job.Annotations[transitionDirectionIndexAnnotation] != "1" {
		t.Fatalf("Job identity = %#v", job.ObjectMeta)
	}
	if job.Spec.Parallelism == nil || *job.Spec.Parallelism != 1 ||
		job.Spec.Completions == nil || *job.Spec.Completions != 1 ||
		job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 ||
		job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 7 {
		t.Fatalf("Job bounds = %#v", job.Spec)
	}
}

func assertAttemptPod(t *testing.T, pod corev1.PodSpec, request AttemptJobRequest, options AttemptJobOptions) {
	t.Helper()
	if pod.RestartPolicy != corev1.RestartPolicyNever || pod.ServiceAccountName != options.ServiceAccountName ||
		pod.AutomountServiceAccountToken == nil || !*pod.AutomountServiceAccountToken ||
		pod.NodeSelector[corev1.LabelHostname] != request.Prepared.ObserverNodeName ||
		len(pod.Containers) != 1 || len(pod.InitContainers) != 0 || len(pod.Volumes) != 2 {
		t.Fatalf("attempt Pod = %#v", pod)
	}
	for _, volume := range pod.Volumes {
		if volume.EmptyDir == nil || volume.ConfigMap != nil || volume.EmptyDir.SizeLimit == nil {
			t.Fatalf("attempt volume = %#v", volume)
		}
	}
}

func assertAttemptContainer(t *testing.T, container corev1.Container, options AttemptJobOptions) {
	t.Helper()
	if container.Name != transitionObserverContainerName || container.Image != options.ObserverImage ||
		strings.Join(container.Command, " ") != "/transition-observer" ||
		container.ReadinessProbe == nil || container.ReadinessProbe.Exec == nil ||
		strings.Join(container.ReadinessProbe.Exec.Command, " ") != "/transition-observer --probe-ready-file" ||
		container.TerminationMessagePolicy != corev1.TerminationMessageReadFile ||
		len(container.VolumeMounts) != 2 {
		t.Fatalf("attempt container = %#v", container)
	}
}

func assertAttemptPayload(
	t *testing.T,
	wire attemptPayload,
	payload string,
	environment []corev1.EnvVar,
	request AttemptJobRequest,
) {
	t.Helper()
	if wire.SchemaVersion != transitionAttemptSchemaVersion ||
		wire.TransitionFacts.SourcePowerMode != request.Prepared.Directions[0].FrozenContract.TargetState.PowerMode ||
		wire.TransitionFacts.TargetPowerMode != request.Prepared.Directions[1].FrozenContract.TargetState.PowerMode ||
		wire.TransitionFacts.SourceActuator.JetsonNVPModel.ID != 0 ||
		wire.TransitionFacts.TargetActuator.JetsonNVPModel.ID != 1 ||
		wire.TransitionFacts.DeviceType != "device-b" || wire.TransitionFacts.Model != "model" ||
		wire.RawLocation != request.RawLocation || wire.PowerObservation.DurationMilliseconds != 5000 {
		t.Fatalf("attempt payload = %#v", wire)
	}
	if strings.Contains(payload, "frozenContract") || strings.Contains(payload, "policy") ||
		envValue(environment, "AWS_ACCESS_KEY_ID") != "" ||
		secretName(environment, "AWS_ACCESS_KEY_ID") != request.StoreBinding.CredentialsSecretName {
		t.Fatalf("attempt evidence or Secret projection = %s / %#v", payload, environment)
	}
}

func TestBuildAttemptJobRejectsInvalidOperationalSelection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AttemptJobRequest, *AttemptJobOptions)
	}{
		{name: "direction index", mutate: func(request *AttemptJobRequest, _ *AttemptJobOptions) {
			request.DirectionIndex = 2
		}},
		{name: "Job name", mutate: func(request *AttemptJobRequest, _ *AttemptJobOptions) {
			request.Name = "NOT_A_NAME"
		}},
		{name: "store mismatch", mutate: func(request *AttemptJobRequest, _ *AttemptJobOptions) {
			request.RawLocation.Store = "other"
		}},
		{name: "missing active deadline overhead", mutate: func(_ *AttemptJobRequest, options *AttemptJobOptions) {
			options.ActiveDeadlineOverhead = 0
		}},
		{name: "active deadline overflow", mutate: func(request *AttemptJobRequest, options *AttemptJobOptions) {
			request.Prepared.PowerRequest.Duration = time.Duration(math.MaxInt64)
			options.ActiveDeadlineOverhead = time.Nanosecond
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, options, scheme := validAttemptJobInputs(t)
			test.mutate(&request, &options)
			if _, err := BuildAttemptJob(request, options, scheme); err == nil {
				t.Fatal("BuildAttemptJob() unexpectedly succeeded")
			}
		})
	}
}

func validAttemptJobInputs(t *testing.T) (AttemptJobRequest, AttemptJobOptions, *runtime.Scheme) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	measurement := &edgev1alpha1.PowerModeTransitionCost{
		ObjectMeta: metav1.ObjectMeta{Namespace: "measure", Name: "cost", UID: types.UID("cost-uid")},
		Spec:       edgev1alpha1.PowerModeTransitionCostSpec{TargetNode: "target"},
	}
	directions := make([]PreparedDirection, 2)
	directions[0].FrozenContract.TargetState = transitioncost.StateIdentity{DeviceType: "device-a", Model: "model", PowerMode: "low"}
	directions[1].FrozenContract.SourceState = directions[0].FrozenContract.TargetState
	directions[1].FrozenContract.TargetState = transitioncost.StateIdentity{DeviceType: "device-b", Model: "model", PowerMode: "high"}
	for index := range directions {
		directions[index].Actuator = edgev1alpha1.PowerModeActuator{
			Kind: edgev1alpha1.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: int32(index)},
		}
	}
	prepared := PreparedMeasurement{
		Namespace: "measure", TargetNode: edgev1alpha1.ProfilingRunObjectReference{Name: "target", UID: "target-uid"},
		ObserverNodeName: "observer", EvidenceStore: "evidence",
		ServingService: "serving", ServingContainer: "runtime",
		PowerSource: metricobserver.SourceIdentity{
			Kind: "edge-metrics", Endpoint: "http://meter/prometheus", Metric: "power_watts",
			MetricLabels: map[string]string{"hostname": "target", "device_type": "device-b"},
		},
		PowerRequest: metricobserver.Request{
			Interval: 100 * time.Millisecond, Duration: 5 * time.Second,
			RequestTimeout: 50 * time.Millisecond, MaxEvents: 52,
		},
		FactPollInterval: 50 * time.Millisecond, FactObservationTimeout: 4 * time.Second,
		Directions: directions,
	}
	tolerationSeconds := int64(30)
	return AttemptJobRequest{
			Measurement: measurement, Prepared: prepared, DirectionIndex: 1,
			Name:        "transition-1",
			RawLocation: evidencestore.ObjectLocation{Store: "evidence", Key: "transition/raw.json"},
			StoreBinding: storekube.Binding{
				Store: "evidence", Endpoint: "http://store", Bucket: "bucket", Region: "region",
				CredentialsSecretName: "credentials",
			},
		}, AttemptJobOptions{
			ObserverImage: "registry/transition-observer@sha256:fixture", ImagePullPolicy: corev1.PullIfNotPresent,
			ServiceAccountName:     "transition-observer",
			ActiveDeadlineOverhead: 1100 * time.Millisecond,
			CoordinationBytes:      1024, WorkspaceBytes: 4096,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			},
			Tolerations: []corev1.Toleration{{Key: "dedicated", TolerationSeconds: &tolerationSeconds}},
		}, scheme
}

func envValue(environment []corev1.EnvVar, name string) string {
	for _, variable := range environment {
		if variable.Name == name {
			return variable.Value
		}
	}
	return ""
}

func secretName(environment []corev1.EnvVar, name string) string {
	for _, variable := range environment {
		if variable.Name == name && variable.ValueFrom != nil && variable.ValueFrom.SecretKeyRef != nil {
			return variable.ValueFrom.SecretKeyRef.Name
		}
	}
	return ""
}

func argumentValueForTest(arguments []string, prefix string) int {
	for _, argument := range arguments {
		if strings.HasPrefix(argument, prefix) {
			value, _ := strconv.Atoi(strings.TrimPrefix(argument, prefix))
			return value
		}
	}
	return 0
}

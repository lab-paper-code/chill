package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/lab-paper-code/chill/internal/profilingrun"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func main() {
	input := flag.String("intent", "", "profiling Run intent JSON")
	candidate := flag.String("candidate-report", "", "exact Plan 1 candidate report JSON")
	namespace := flag.String("namespace", "chill-profiling-run", "target namespace")
	flag.Parse()
	if *input == "" || *candidate == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "-intent and -candidate-report are required")
		os.Exit(2)
	}
	intent, err := readIntent(*input)
	if err != nil {
		fatal(err)
	}
	candidatePayload, err := os.ReadFile(*candidate)
	if err != nil {
		fatal(err)
	}
	if err := profilingrun.ValidateCandidateReport(candidatePayload, intent); err != nil {
		fatal(err)
	}
	objects, err := materialize(intent, *namespace)
	if err != nil {
		fatal(err)
	}
	for index, object := range objects {
		if index > 0 {
			fmt.Println("---")
		}
		payload, err := yaml.Marshal(object)
		if err != nil {
			fatal(err)
		}
		_, _ = os.Stdout.Write(payload)
	}
}

func readIntent(path string) (profilingrun.Intent, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return profilingrun.Intent{}, fmt.Errorf("read intent: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var intent profilingrun.Intent
	if err := decoder.Decode(&intent); err != nil {
		return intent, fmt.Errorf("decode intent: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return intent, fmt.Errorf("decode intent: trailing JSON value")
	}
	if err := intent.Validate(); err != nil {
		return intent, fmt.Errorf("validate intent: %w", err)
	}
	return intent, nil
}

func materialize(intent profilingrun.Intent, namespace string) ([]any, error) {
	digest, err := intent.Digest()
	if err != nil {
		return nil, err
	}
	short := strings.TrimPrefix(digest, "sha256:")[:12]
	name := "cpu-ort-" + short
	contract, err := executionContract(intent, digest)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{"app.kubernetes.io/name": "chill-profiling-run", "chill.dacs.io/run-intent": short}
	annotations := map[string]string{"chill.dacs.io/run-intent-digest": digest}
	zero := int32(0)
	one := int32(1)
	deadline := int64(intent.Measurement.DurationSeconds + 180)
	job := &batchv1.Job{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels, Annotations: annotations},
		Spec: batchv1.JobSpec{
			Parallelism:           &one,
			Completions:           &one,
			BackoffLimit:          &zero,
			ActiveDeadlineSeconds: &deadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations},
				Spec:       podSpec(intent, name, digest),
			},
		},
	}
	ns := &corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{"app.kubernetes.io/part-of": "chill"}},
	}
	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name + "-contract",
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Data: map[string]string{"execution-contract.json": string(contract)},
	}
	return []any{ns, cm, job}, nil
}

func executionContract(intent profilingrun.Intent, digest string) ([]byte, error) {
	value := map[string]any{
		"schemaVersion": "chill.dacs.io/cpu-execution-contract.v1alpha1", "runIntentDigest": digest,
		"provider":     intent.State.Backend,
		"nodeSnapshot": intent.TargetNode,
		"cpu": map[string]any{
			"policy":              intent.CPU.Policy,
			"policyVersion":       intent.CPU.PolicyVersion,
			"budgetSource":        "KubernetesNodeStatusAllocatable",
			"observedAllocatable": intent.TargetNode.AllocatableCPU,
			"limit":               intent.CPU.Limit,
			"exclusivityClaimed":  false,
		},
		"runtimeOptions": map[string]any{
			"intraOpThreads": intent.CPU.ORTIntraOpThreads,
			"interOpThreads": intent.CPU.ORTInterOpThreads,
		},
		"scheduling": map[string]any{"cpuRequest": intent.CPU.Request, "status": "RequestedNotExclusive"},
	}
	return json.Marshal(value)
}

func podSpec(i profilingrun.Intent, contractName, digest string) corev1.PodSpec {
	falseValue := false
	qRequest := resource.MustParse(i.CPU.Request)
	qLimit := resource.MustParse(i.CPU.Limit)
	modelVolume := corev1.Volume{
		Name:         "model",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}
	coordVolume := corev1.Volume{
		Name:         "coordination",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}
	contractVolume := corev1.Volume{
		Name: "execution-contract",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: contractName + "-contract"},
			},
		},
	}
	secure := &corev1.SecurityContext{
		AllowPrivilegeEscalation: &falseValue,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
	return corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &falseValue,
		NodeSelector: map[string]string{"kubernetes.io/hostname": i.TargetNode.Name},
		Tolerations: []corev1.Toleration{
			{
				Key:      "node-role.kubernetes.io/edge",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			},
		},
		InitContainers: []corev1.Container{
			{
				Name:            "artifact",
				Image:           i.Images.Artifact,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"sh", "-ec"},
				Args: []string{
					"cp /artifact/mobilenet-v2-050.onnx /model/model.onnx && echo '" + strings.TrimPrefix(
						i.State.ArtifactDigest,
						"sha256:",
					) + "  /model/model.onnx' | sha256sum -c -",
				},
				SecurityContext: secure,
				VolumeMounts:    []corev1.VolumeMount{{Name: "model", MountPath: "/model"}},
			},
		},
		Containers: []corev1.Container{
			{
				Name:            "runtime",
				Image:           i.Images.Runtime,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Env:             runtimeEnv(i, digest),
				SecurityContext: secure,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    qRequest,
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    qLimit,
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "model", MountPath: "/model", ReadOnly: true},
					{Name: "coordination", MountPath: "/coordination"},
					{Name: "execution-contract", MountPath: "/evidence", ReadOnly: true},
				},
			},
			{
				Name:            "power-observer",
				Image:           i.Images.PowerObserver,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Args: []string{
					"-node-name=" + i.Power.NodeName,
					"-resolved-endpoint=" + i.Power.Endpoint,
					"-interval=" + strconv.Itoa(i.Power.IntervalMilliseconds) + "ms",
					"-duration=" + strconv.Itoa(i.Power.DurationSeconds) + "s",
					"-request-timeout=" + strconv.Itoa(i.Power.TimeoutMilliseconds) + "ms",
					"-ready-file=/coordination/observer-ready",
					"-start-signal-file=/coordination/measurement-start",
				},
				SecurityContext: secure,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("5m"),
						corev1.ResourceMemory: resource.MustParse("16Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "coordination", MountPath: "/coordination"}},
			},
		}, Volumes: []corev1.Volume{modelVolume, coordVolume, contractVolume},
	}
}

func runtimeEnv(i profilingrun.Intent, digest string) []corev1.EnvVar {
	values := map[string]string{
		"MODEL_NAME":                   i.State.Model,
		"MODEL_PATH":                   "/model/model.onnx",
		"ARTIFACT_DIGEST":              i.State.ArtifactDigest,
		"RUNTIME_PROVIDER":             i.State.Backend,
		"BATCH_SIZES":                  strconv.Itoa(i.Measurement.Batch),
		"WARMUP_ITERATIONS":            strconv.Itoa(i.Measurement.WarmupIterations),
		"MEASUREMENT_ITERATIONS":       "0",
		"REPETITIONS":                  strconv.Itoa(i.Measurement.Repetitions),
		"MEASUREMENT_DURATION_SECONDS": strconv.Itoa(i.Measurement.DurationSeconds),
		"ORT_INTRA_OP":                 strconv.Itoa(i.CPU.ORTIntraOpThreads),
		"ORT_INTER_OP":                 strconv.Itoa(i.CPU.ORTInterOpThreads),
		"OBSERVER_READY_FILE":          "/coordination/observer-ready",
		"MEASUREMENT_SIGNAL_FILE":      "/coordination/measurement-start",
		"EXECUTION_CONTRACT_FILE":      "/evidence/execution-contract.json",
		"JOB_COMPLETION_INDEX":         "0",
		"SWEEP_ID":                     digest,
	}
	names := []string{
		"MODEL_NAME",
		"MODEL_PATH",
		"ARTIFACT_DIGEST",
		"RUNTIME_PROVIDER",
		"BATCH_SIZES",
		"WARMUP_ITERATIONS",
		"MEASUREMENT_ITERATIONS",
		"REPETITIONS",
		"MEASUREMENT_DURATION_SECONDS",
		"ORT_INTRA_OP",
		"ORT_INTER_OP",
		"OBSERVER_READY_FILE",
		"MEASUREMENT_SIGNAL_FILE",
		"EXECUTION_CONTRACT_FILE",
		"JOB_COMPLETION_INDEX",
		"SWEEP_ID",
	}
	env := make([]corev1.EnvVar, 0, len(names)+2)
	for _, name := range names {
		env = append(env, corev1.EnvVar{Name: name, Value: values[name]})
	}
	env = append(
		env,
		corev1.EnvVar{
			Name:      "NODE_NAME",
			ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}},
		},
		corev1.EnvVar{
			Name:      "EXPERIMENT_ID",
			ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}},
		},
	)
	return env
}

func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }

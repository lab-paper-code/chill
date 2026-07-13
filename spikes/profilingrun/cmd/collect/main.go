package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/powerobserver"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type bundle struct {
	SchemaVersion   string                     `json:"schemaVersion"`
	CollectedAt     time.Time                  `json:"collectedAt"`
	IntentDigest    string                     `json:"intentDigest"`
	CandidateReport json.RawMessage            `json:"candidateReport"`
	Intent          profilingrun.Intent        `json:"intent"`
	Job             json.RawMessage            `json:"job"`
	Pod             json.RawMessage            `json:"pod"`
	ActualNode      json.RawMessage            `json:"actualNode"`
	CoResidentPods  json.RawMessage            `json:"coResidentPods"`
	Runtime         profilingrun.RuntimeResult `json:"runtime"`
	Power           powerobserver.Result       `json:"power"`
}

//nolint:gocyclo // One-shot collection keeps I/O failure gates linear and local; it has no reusable workflow engine.
func main() {
	intentPath := flag.String("intent", "", "Run intent JSON")
	candidatePath := flag.String("candidate-report", "", "exact Plan 1 candidate report JSON")
	namespace := flag.String("namespace", "chill-profiling-run", "Job namespace")
	outputDir := flag.String("output-dir", "spikes/profilingrun/results", "evidence output directory")
	kubeconfig := flag.String("kubeconfig", "", "optional kubeconfig")
	flag.Parse()
	if *intentPath == "" || *candidatePath == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "-intent and -candidate-report are required")
		os.Exit(2)
	}
	intent, err := readIntent(*intentPath)
	if err != nil {
		fatal(err)
	}
	digest, err := intent.Digest()
	if err != nil {
		fatal(err)
	}
	candidatePayload, err := os.ReadFile(*candidatePath)
	if err != nil {
		fatal(err)
	}
	if err := profilingrun.ValidateCandidateReport(candidatePayload, intent); err != nil {
		fatal(err)
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if *kubeconfig != "" {
		loadingRules.ExplicitPath = *kubeconfig
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).
		ClientConfig()
	if err != nil {
		fatal(fmt.Errorf("load Kubernetes config: %w", err))
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		fatal(err)
	}
	ctx := context.Background()
	short := strings.TrimPrefix(digest, "sha256:")[:12]
	name := "cpu-ort-" + short
	job, err := client.BatchV1().Jobs(*namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		fatal(err)
	}
	pods, err := client.CoreV1().
		Pods(*namespace).
		List(ctx, metav1.ListOptions{LabelSelector: "chill.dacs.io/run-intent=" + short})
	if err != nil {
		fatal(err)
	}
	if len(pods.Items) != 1 {
		fatal(fmt.Errorf("expected exactly one Run Pod, found %d", len(pods.Items)))
	}
	pod := pods.Items[0]
	if err := validateKubernetes(job, &pod, intent, digest); err != nil {
		fatal(err)
	}
	if string(pod.UID) == "" || pod.Spec.NodeName == "" {
		fatal(errors.New("Pod identity or binding is missing"))
	}
	node, err := client.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		fatal(err)
	}
	if string(node.UID) != intent.TargetNode.UID {
		fatal(fmt.Errorf("actual Node UID mismatch: %s", node.UID))
	}
	if err := validateStatuses(intent, pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses); err != nil {
		fatal(err)
	}
	runtimeOptions := corev1.PodLogOptions{Container: "runtime"}
	powerOptions := corev1.PodLogOptions{Container: "power-observer"}
	runtimeLog, err := client.CoreV1().Pods(*namespace).GetLogs(pod.Name, &runtimeOptions).DoRaw(ctx)
	if err != nil {
		fatal(err)
	}
	powerLog, err := client.CoreV1().Pods(*namespace).GetLogs(pod.Name, &powerOptions).DoRaw(ctx)
	if err != nil {
		fatal(err)
	}
	var runtimeResult profilingrun.RuntimeResult
	if err := decodePrefixed(runtimeLog, "EXPERIMENT_RESULT_JSON ", &runtimeResult); err != nil {
		fatal(err)
	}
	if err := profilingrun.ValidateRuntime(intent, string(pod.UID), runtimeResult); err != nil {
		fatal(fmt.Errorf("validate runtime evidence: %w", err))
	}
	var powerResult powerobserver.Result
	if err := decodePrefixed(powerLog, "POWER_OBSERVATION_JSON ", &powerResult); err != nil {
		fatal(err)
	}
	if powerResult.Source.NodeName != intent.Power.NodeName || powerResult.Source.Endpoint != intent.Power.Endpoint ||
		powerResult.Source.Metric != intent.Power.Metric {
		fatal(errors.New("power source identity mismatch"))
	}
	coResidents, err := client.CoreV1().
		Pods("").
		List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + pod.Spec.NodeName})
	if err != nil {
		fatal(err)
	}
	b := bundle{
		SchemaVersion:   "chill.dacs.io/profiling-run-evidence.v1alpha1",
		CollectedAt:     time.Now().UTC(),
		IntentDigest:    digest,
		CandidateReport: json.RawMessage(candidatePayload),
		Intent:          intent,
		Job:             mustJSON(job),
		Pod:             mustJSON(&pod),
		ActualNode:      mustJSON(node),
		CoResidentPods:  mustJSON(coResidents),
		Runtime:         runtimeResult,
		Power:           powerResult,
	}
	if err := os.MkdirAll(*outputDir, 0o755); err != nil {
		fatal(err)
	}
	pretty, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		fatal(err)
	}
	pretty = append(pretty, '\n')
	sum := sha256.Sum256(pretty)
	id := hex.EncodeToString(sum[:])
	final := filepath.Join(*outputDir, "sha256-"+id+".json")
	temp := final + ".tmp"
	if err := os.WriteFile(temp, pretty, 0o644); err != nil {
		fatal(err)
	}
	if err := os.Rename(temp, final); err != nil {
		fatal(err)
	}
	fmt.Printf("%s\n", final)
}

func readIntent(path string) (profilingrun.Intent, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return profilingrun.Intent{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var value profilingrun.Intent
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return value, errors.New("trailing JSON value")
	}
	return value, value.Validate()
}

func decodePrefixed(payload []byte, prefix string, target any) error {
	var found []byte
	for _, line := range strings.Split(string(payload), "\n") {
		if strings.HasPrefix(line, prefix) {
			if found != nil {
				return fmt.Errorf("multiple %s records", strings.TrimSpace(prefix))
			}
			found = []byte(strings.TrimPrefix(line, prefix))
		}
	}
	if found == nil {
		return fmt.Errorf("missing %s record", strings.TrimSpace(prefix))
	}
	decoder := json.NewDecoder(strings.NewReader(string(found)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", strings.TrimSpace(prefix), err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing evidence JSON")
	}
	return nil
}

func validateKubernetes(job *batchv1.Job, pod *corev1.Pod, intent profilingrun.Intent, digest string) error {
	if job.Annotations["chill.dacs.io/run-intent-digest"] != digest ||
		pod.Annotations["chill.dacs.io/run-intent-digest"] != digest {
		return errors.New("full Run intent annotation mismatch")
	}
	complete := false
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			complete = true
		}
	}
	if !complete || job.Status.Succeeded != 1 || job.Status.Failed != 0 {
		return errors.New("Job is not an unambiguous terminal success")
	}
	owned := false
	for _, owner := range pod.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.Kind == "Job" && owner.UID == job.UID {
			owned = true
		}
	}
	if !owned {
		return errors.New("Pod is not controller-owned by the selected Job")
	}
	images := map[string]string{}
	for _, container := range pod.Spec.InitContainers {
		images[container.Name] = container.Image
	}
	for _, container := range pod.Spec.Containers {
		images[container.Name] = container.Image
	}
	expected := map[string]string{
		"artifact":       intent.Images.Artifact,
		"runtime":        intent.Images.Runtime,
		"power-observer": intent.Images.PowerObserver,
	}
	for name, want := range expected {
		if images[name] != want {
			return fmt.Errorf("Pod spec image mismatch for %s", name)
		}
	}
	return nil
}

func validateStatuses(intent profilingrun.Intent, initStatuses, containerStatuses []corev1.ContainerStatus) error {
	expected := map[string]string{
		"artifact":       intent.Images.Artifact,
		"runtime":        intent.Images.Runtime,
		"power-observer": intent.Images.PowerObserver,
	}
	all := append(append([]corev1.ContainerStatus{}, initStatuses...), containerStatuses...)
	if len(all) != 3 {
		return fmt.Errorf("expected three container statuses, found %d", len(all))
	}
	for _, status := range all {
		want, ok := expected[status.Name]
		if !ok {
			return fmt.Errorf("unexpected container %q", status.Name)
		}
		digest := want[strings.LastIndex(want, "@sha256:")+1:]
		// Some KubeEdge/containerd versions report a cached tag identity for a
		// completed init container. The artifact bytes are independently checked
		// by the runtime result; preserve, but do not over-interpret, that imageID.
		if status.Name != "artifact" && !strings.Contains(status.ImageID, digest) {
			return fmt.Errorf("container %s imageID mismatch: %s", status.Name, status.ImageID)
		}
		if status.RestartCount != 0 || status.State.Terminated == nil || status.State.Terminated.ExitCode != 0 {
			return fmt.Errorf("container %s did not complete exactly once", status.Name)
		}
	}
	return nil
}

func mustJSON(value any) json.RawMessage {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return payload
}
func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }

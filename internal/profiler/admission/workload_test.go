package admission

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/profilingrun"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

func TestProjectWorkloadWindowClassifiesExactControllerOwnedBackground(t *testing.T) {
	intent := admissionIntentWithWorkloadPolicy()
	result := admissionWorkloadResult(t, intent)

	known, unexpected, err := projectWorkloadWindow(intent, &result)
	if err != nil {
		t.Fatal(err)
	}
	if !known || len(unexpected) != 0 {
		t.Fatalf("allowed workload projection = known:%t unexpected:%v", known, unexpected)
	}

	transient := workloadobserver.Pod{
		Namespace: "default", Name: "foreign", UID: "foreign-uid", Phase: "Running",
		Owner:  &workloadobserver.OwnerIdentity{Kind: "ReplicaSet", Name: "foreign-abc", UID: "owner-uid"},
		Images: []string{"registry.example/foreign@sha256:" + strings.Repeat("c", 64)},
	}
	result.Events = []workloadobserver.Event{
		{Kind: workloadobserver.EventEntered, ObservedAt: result.CoverageStartedAt.Add(time.Second), ResourceVersion: "11", Pod: transient},
		{Kind: workloadobserver.EventExited, ObservedAt: result.CoverageStartedAt.Add(2 * time.Second), ResourceVersion: "12", Pod: transient},
	}
	result.LastResourceVersion = "12"
	known, unexpected, err = projectWorkloadWindow(intent, &result)
	if err != nil {
		t.Fatal(err)
	}
	if !known || len(unexpected) != 1 || unexpected[0] != "default/foreign@foreign-uid" {
		t.Fatalf("transient workload projection = known:%t unexpected:%v", known, unexpected)
	}
}

func TestProjectWorkloadWindowRejectsPartialSelectorMatches(t *testing.T) {
	intent := admissionIntentWithWorkloadPolicy()
	base := admissionWorkloadResult(t, intent).InitialActivePods[1]
	tests := []struct {
		name string
		edit func(*workloadobserver.Pod)
	}{
		{name: "namespace", edit: func(pod *workloadobserver.Pod) { pod.Namespace = "default" }},
		{name: "owner", edit: func(pod *workloadobserver.Pod) { pod.Owner.Name = "copied-labels" }},
		{name: "labels", edit: func(pod *workloadobserver.Pod) {
			pod.Labels["app.kubernetes.io/component"] = "other"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := admissionWorkloadResult(t, intent)
			spoof := base
			spoof.UID = "spoof-" + test.name
			spoof.Name = "spoof-" + test.name
			spoof.Labels = map[string]string{"app.kubernetes.io/component": "exporter"}
			owner := *spoof.Owner
			spoof.Owner = &owner
			test.edit(&spoof)
			result.InitialActivePods = append(result.InitialActivePods, spoof)
			result.FinalActivePods = append(result.FinalActivePods, spoof)
			for _, pods := range [][]workloadobserver.Pod{result.InitialActivePods, result.FinalActivePods} {
				sort.Slice(pods, func(i, j int) bool {
					left := pods[i].Namespace + "\x00" + pods[i].Name + "\x00" + pods[i].UID
					right := pods[j].Namespace + "\x00" + pods[j].Name + "\x00" + pods[j].UID
					return left < right
				})
			}

			known, unexpected, err := projectWorkloadWindow(intent, &result)
			if err != nil {
				t.Fatal(err)
			}
			if !known || len(unexpected) != 1 || !strings.Contains(unexpected[0], spoof.UID) {
				t.Fatalf("partial selector match = known:%t unexpected:%v", known, unexpected)
			}
		})
	}
}

func TestProjectWorkloadWindowPreservesIncompleteCoverage(t *testing.T) {
	intent := admissionIntentWithWorkloadPolicy()
	result := admissionWorkloadResult(t, intent)
	result.Continuous = false
	result.Failure = &workloadobserver.Failure{
		ObservedAt: result.CoverageEndedAt,
		Reason:     workloadobserver.FailureReasonEndFenceFailed,
		Message:    "closing bookmark was not observed",
	}
	known, unexpected, err := projectWorkloadWindow(intent, &result)
	if err != nil {
		t.Fatal(err)
	}
	if known || len(unexpected) != 0 {
		t.Fatalf("incomplete workload projection = known:%t unexpected:%v", known, unexpected)
	}
}

func admissionIntentWithWorkloadPolicy() profilingrun.Intent {
	intent := admissionIntent()
	intent.Workload = profilingrun.WorkloadContract{AllowedBackground: profilingrun.AllowedBackgroundPolicy{
		SchemaVersion: profilingrun.AllowedBackgroundPolicySchemaVersion,
		Selectors: []profilingrun.BackgroundPodSelector{{
			Namespace: "monitoring",
			ControllerOwner: profilingrun.ControllerOwnerSelector{
				Kind: "DaemonSet", Name: "edge-metrics-exporter",
			},
			MatchLabels: map[string]string{"app.kubernetes.io/component": "exporter"},
		}},
	}}
	return intent
}

func admissionWorkloadResult(t *testing.T, intent profilingrun.Intent) workloadobserver.Result {
	t.Helper()
	digest, err := intent.Workload.AllowedBackground.Digest()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 7, 20, 1, 2, 2, 0, time.UTC)
	run := workloadobserver.Pod{
		Namespace: "chill-system", Name: "run", UID: "run-pod", Phase: "Running",
		Images: []string{"registry.example/runtime@sha256:" + strings.Repeat("a", 64)},
	}
	allowed := workloadobserver.Pod{
		Namespace: "monitoring", Name: "edge-metrics-exporter-abc", UID: "metrics-uid", Phase: "Running",
		Owner: &workloadobserver.OwnerIdentity{
			Kind: "DaemonSet", Name: "edge-metrics-exporter", UID: "metrics-owner-uid",
		},
		Images: []string{"registry.example/metrics@sha256:" + strings.Repeat("b", 64)},
		Labels: map[string]string{"app.kubernetes.io/component": "exporter"},
	}
	pods := []workloadobserver.Pod{run, allowed}
	return workloadobserver.Result{
		SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target:        workloadobserver.TargetIdentity{NodeName: "lattepanda", NodeUID: "node-uid"},
		Run: workloadobserver.RunIdentity{
			Namespace: "chill-system", JobUID: "job-uid", PodName: run.Name, PodUID: run.UID,
		},
		PolicyDigest:      digest,
		CoverageStartedAt: started, CoverageEndedAt: started.Add(32 * time.Second),
		InitialResourceVersion: "10", LastResourceVersion: "10", EventLimit: 10,
		InitialActivePods: pods, FinalActivePods: append([]workloadobserver.Pod(nil), pods...),
		Continuous: true,
	}
}

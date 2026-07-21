package admission

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/profilingrun"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

func TestProjectWorkloadWindowRecordsStableExternalWorkloadsWithoutRejectingThem(t *testing.T) {
	intent := admissionIntentWithWorkloadContract()
	result := admissionWorkloadResult(t, intent)

	window, err := projectWorkloadWindow(intent, &result)
	if err != nil {
		t.Fatal(err)
	}
	if !window.Known || len(window.ConcurrentCHILLWorkloads) != 0 ||
		len(window.ExternalWorkloadChanges) != 0 || !strings.HasPrefix(window.ExternalEnvironmentIdentity, "sha256:") {
		t.Fatalf("stable external workload projection = %#v", window)
	}
}

func TestProjectWorkloadWindowBindsDifferentStableExternalSets(t *testing.T) {
	intent := admissionIntentWithWorkloadContract()
	first := admissionWorkloadResult(t, intent)
	second := admissionWorkloadResult(t, intent)
	extra := workloadobserver.Pod{
		Namespace: "system", Name: "stable-agent", UID: "stable-agent-uid", Phase: "Running",
		Owner:  &workloadobserver.OwnerIdentity{Kind: "DaemonSet", Name: "stable-agent", UID: "stable-owner-uid"},
		Images: []string{"registry.example/agent@sha256:" + strings.Repeat("e", 64)},
	}
	second.InitialActivePods = append(second.InitialActivePods, extra)
	second.FinalActivePods = append(second.FinalActivePods, extra)
	for _, pods := range [][]workloadobserver.Pod{second.InitialActivePods, second.FinalActivePods} {
		sort.Slice(pods, func(i, j int) bool { return workloadIdentity(pods[i]) < workloadIdentity(pods[j]) })
	}

	firstWindow, err := projectWorkloadWindow(intent, &first)
	if err != nil {
		t.Fatal(err)
	}
	secondWindow, err := projectWorkloadWindow(intent, &second)
	if err != nil {
		t.Fatal(err)
	}
	if firstWindow.ExternalEnvironmentIdentity == secondWindow.ExternalEnvironmentIdentity ||
		len(secondWindow.ExternalWorkloadChanges) != 0 {
		t.Fatalf("environment identities = %q, %q", firstWindow.ExternalEnvironmentIdentity, secondWindow.ExternalEnvironmentIdentity)
	}
}

func TestProjectWorkloadWindowReportsExternalMembershipChange(t *testing.T) {
	intent := admissionIntentWithWorkloadContract()
	result := admissionWorkloadResult(t, intent)
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

	window, err := projectWorkloadWindow(intent, &result)
	if err != nil {
		t.Fatal(err)
	}
	if !window.Known || len(window.ExternalWorkloadChanges) != 1 ||
		window.ExternalWorkloadChanges[0] != "Exited:default/foreign@foreign-uid" ||
		len(window.ConcurrentCHILLWorkloads) != 0 {
		t.Fatalf("external workload change projection = %#v", window)
	}
}

func TestProjectWorkloadWindowReportsConcurrentCHILLMeasurement(t *testing.T) {
	intent := admissionIntentWithWorkloadContract()
	result := admissionWorkloadResult(t, intent)
	other := workloadobserver.Pod{
		Namespace: "chill-system", Name: "other-run", UID: "other-run-uid", Phase: "Running",
		Owner: &workloadobserver.OwnerIdentity{Kind: "Job", Name: "profilingrun-other", UID: "other-job-uid"},
		Labels: map[string]string{
			profilingrun.MeasurementWorkloadLabelKey: profilingrun.MeasurementWorkloadLabelValue,
		},
		Images: []string{"registry.example/runtime@sha256:" + strings.Repeat("d", 64)},
	}
	result.InitialActivePods = append(result.InitialActivePods, other)
	result.FinalActivePods = append(result.FinalActivePods, other)
	for _, pods := range [][]workloadobserver.Pod{result.InitialActivePods, result.FinalActivePods} {
		sort.Slice(pods, func(i, j int) bool {
			return workloadIdentity(pods[i]) < workloadIdentity(pods[j])
		})
	}

	window, err := projectWorkloadWindow(intent, &result)
	if err != nil {
		t.Fatal(err)
	}
	if !window.Known || len(window.ConcurrentCHILLWorkloads) != 1 ||
		window.ConcurrentCHILLWorkloads[0] != "chill-system/other-run@other-run-uid" ||
		len(window.ExternalWorkloadChanges) != 0 {
		t.Fatalf("concurrent CHILL projection = %#v", window)
	}
}

func TestProjectWorkloadWindowPreservesIncompleteCoverage(t *testing.T) {
	intent := admissionIntentWithWorkloadContract()
	result := admissionWorkloadResult(t, intent)
	result.Continuous = false
	result.Failure = &workloadobserver.Failure{
		ObservedAt: result.CoverageEndedAt,
		Reason:     workloadobserver.FailureReasonEndFenceFailed,
		Message:    "closing bookmark was not observed",
	}
	window, err := projectWorkloadWindow(intent, &result)
	if err != nil {
		t.Fatal(err)
	}
	if window.Known || len(window.ConcurrentCHILLWorkloads) != 0 || len(window.ExternalWorkloadChanges) != 0 {
		t.Fatalf("incomplete workload projection = %#v", window)
	}
}

func admissionIntentWithWorkloadContract() profilingrun.Intent {
	intent := admissionIntent()
	intent.Workload = profilingrun.CurrentWorkloadContract()
	return intent
}

func admissionWorkloadResult(t *testing.T, intent profilingrun.Intent) workloadobserver.Result {
	t.Helper()
	digest, err := intent.Workload.Digest()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 7, 20, 1, 2, 2, 0, time.UTC)
	run := workloadobserver.Pod{
		Namespace: "chill-system", Name: "run", UID: "run-pod", Phase: "Running",
		Images: []string{"registry.example/runtime@sha256:" + strings.Repeat("a", 64)},
	}
	external := workloadobserver.Pod{
		Namespace: "monitoring", Name: "edge-metrics-exporter-abc", UID: "metrics-uid", Phase: "Running",
		Owner: &workloadobserver.OwnerIdentity{
			Kind: "DaemonSet", Name: "edge-metrics-exporter", UID: "metrics-owner-uid",
		},
		Images: []string{"registry.example/metrics@sha256:" + strings.Repeat("b", 64)},
	}
	pods := []workloadobserver.Pod{run, external}
	return workloadobserver.Result{
		SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target:        workloadobserver.TargetIdentity{NodeName: "lattepanda", NodeUID: "node-uid"},
		Run: workloadobserver.RunIdentity{
			Namespace: "chill-system", JobUID: "job-uid", PodName: run.Name, PodUID: run.UID,
		},
		PolicyDigest: digest, CoverageStartedAt: started, CoverageEndedAt: started.Add(32 * time.Second),
		InitialResourceVersion: "10", LastResourceVersion: "10", EventLimit: 10,
		InitialActivePods: pods, FinalActivePods: append([]workloadobserver.Pod(nil), pods...), Continuous: true,
	}
}

package admission

import (
	"sort"
	"strings"
	"testing"
	"time"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

func TestProjectWorkloadWindowRecordsStableExternalWorkloadsWithoutRejectingThem(t *testing.T) {
	result := admissionWorkloadResult(t)

	window, err := projectDefaultWorkloadWindow(&result)
	if err != nil {
		t.Fatal(err)
	}
	if !window.Known || len(window.ConcurrentCHILLWorkloads) != 0 ||
		len(window.ExternalWorkloadChanges) != 0 ||
		!strings.HasPrefix(window.EnvironmentIdentity, environmentIdentitySchemaVersion+"@sha256:") {
		t.Fatalf("stable external workload projection = %#v", window)
	}
}

func TestProjectWorkloadWindowBindsDifferentStableExternalSets(t *testing.T) {
	first := admissionWorkloadResult(t)
	second := admissionWorkloadResult(t)
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

	firstWindow, err := projectDefaultWorkloadWindow(&first)
	if err != nil {
		t.Fatal(err)
	}
	secondWindow, err := projectDefaultWorkloadWindow(&second)
	if err != nil {
		t.Fatal(err)
	}
	if firstWindow.EnvironmentIdentity == secondWindow.EnvironmentIdentity ||
		len(secondWindow.ExternalWorkloadChanges) != 0 {
		t.Fatalf("environment identities = %q, %q", firstWindow.EnvironmentIdentity, secondWindow.EnvironmentIdentity)
	}
}

func TestWorkloadEnvironmentIdentityUsesStableControllerOwner(t *testing.T) {
	t.Parallel()
	first := workloadobserver.Pod{
		Namespace: "monitoring", Name: "agent-a", UID: "pod-a", Phase: "Running",
		Owner:  &workloadobserver.OwnerIdentity{Kind: "DaemonSet", Name: "agent", UID: "owner-uid"},
		Images: []string{"registry.example/agent@sha256:" + strings.Repeat("a", 64)},
	}
	replacement := first
	replacement.Name = "agent-b"
	replacement.UID = "pod-b"

	base, err := workloadEnvironmentIdentity([]workloadobserver.Pod{first})
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := workloadEnvironmentIdentity([]workloadobserver.Pod{replacement})
	if err != nil {
		t.Fatal(err)
	}
	if replaced != base {
		t.Fatalf("same owner replacement drifted: %q != %q", replaced, base)
	}

	newOwner := replacement
	newOwner.Owner = &workloadobserver.OwnerIdentity{Kind: "DaemonSet", Name: "agent", UID: "new-owner-uid"}
	changed, err := workloadEnvironmentIdentity([]workloadobserver.Pod{newOwner})
	if err != nil {
		t.Fatal(err)
	}
	if changed == base {
		t.Fatal("owner UID change must change environment identity")
	}

	newImage := replacement
	newImage.Images = []string{"registry.example/agent@sha256:" + strings.Repeat("c", 64)}
	changed, err = workloadEnvironmentIdentity([]workloadobserver.Pod{newImage})
	if err != nil {
		t.Fatal(err)
	}
	if changed == base {
		t.Fatal("owner-controlled image change must change environment identity")
	}

	twoReplicas, err := workloadEnvironmentIdentity([]workloadobserver.Pod{first, replacement})
	if err != nil {
		t.Fatal(err)
	}
	if twoReplicas == base {
		t.Fatal("owner replica count must change environment identity")
	}
}

func TestWorkloadEnvironmentIdentityKeepsUnownedPodsUIDBound(t *testing.T) {
	t.Parallel()
	first := workloadobserver.Pod{
		Namespace: "default", Name: "standalone", UID: "pod-a", Phase: "Running",
		Images: []string{"registry.example/standalone@sha256:" + strings.Repeat("b", 64)},
	}
	replacement := first
	replacement.UID = "pod-b"

	base, err := workloadEnvironmentIdentity([]workloadobserver.Pod{first})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := workloadEnvironmentIdentity([]workloadobserver.Pod{replacement})
	if err != nil {
		t.Fatal(err)
	}
	if changed == base {
		t.Fatal("unowned Pod replacement must remain fail-safe and UID-bound")
	}
}

func TestProjectWorkloadWindowReportsExternalMembershipChange(t *testing.T) {
	result := admissionWorkloadResult(t)
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

	window, err := projectDefaultWorkloadWindow(&result)
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
	result := admissionWorkloadResult(t)
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

	window, err := projectDefaultWorkloadWindow(&result)
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
	result := admissionWorkloadResult(t)
	result.Continuous = false
	result.Failure = &workloadobserver.Failure{
		ObservedAt: result.CoverageEndedAt,
		Reason:     workloadobserver.FailureReasonEndFenceFailed,
		Message:    "causal Pod-status end fence failed",
	}
	window, err := projectDefaultWorkloadWindow(&result)
	if err != nil {
		t.Fatal(err)
	}
	if window.Known || len(window.ConcurrentCHILLWorkloads) != 0 || len(window.ExternalWorkloadChanges) != 0 {
		t.Fatalf("incomplete workload projection = %#v", window)
	}
}

func TestProjectWorkloadWindowMarksEndDrainChangesUnknown(t *testing.T) {
	result := admissionWorkloadResult(t)
	runtimeStarted := result.CoverageStartedAt.Add(10 * time.Second)
	runtimeEnded := result.CoverageStartedAt.Add(20 * time.Second)
	before := workloadobserver.Pod{
		Namespace: "default", Name: "before", UID: "before-uid", Phase: "Running",
		Images: []string{"registry.example/before@sha256:" + strings.Repeat("c", 64)},
	}
	after := workloadobserver.Pod{
		Namespace: "default", Name: "after", UID: "after-uid", Phase: "Running",
		Images: []string{"registry.example/after@sha256:" + strings.Repeat("d", 64)},
	}
	result.Events = []workloadobserver.Event{
		{
			Kind: workloadobserver.EventEntered, ObservedAt: runtimeStarted.Add(-2 * time.Second),
			ResourceVersion: "11", Pod: before,
		},
		{
			Kind: workloadobserver.EventExited, ObservedAt: runtimeStarted.Add(-time.Second),
			ResourceVersion: "12", Pod: before,
		},
		{
			Kind: workloadobserver.EventEntered, ObservedAt: runtimeEnded.Add(time.Second),
			ResourceVersion: "13", Pod: after,
		},
		{
			Kind: workloadobserver.EventExited, ObservedAt: runtimeEnded.Add(2 * time.Second),
			ResourceVersion: "14", Pod: after,
		},
	}
	result.LastResourceVersion = "14"

	window, err := projectWorkloadWindow(&result, runtimeStarted, runtimeEnded)
	if err != nil {
		t.Fatal(err)
	}
	if window.Known || len(window.ExternalWorkloadChanges) != 0 ||
		len(window.ConcurrentCHILLWorkloads) != 0 {
		t.Fatalf("ambiguous end-drain workload projection = %#v", window)
	}
}

func TestProjectWorkloadWindowIgnoresCHILLWorkloadThatExitedBeforeRuntime(t *testing.T) {
	result := admissionWorkloadResult(t)
	other := workloadobserver.Pod{
		Namespace: "chill-system", Name: "earlier-run", UID: "earlier-run-uid", Phase: "Running",
		Owner: &workloadobserver.OwnerIdentity{Kind: "Job", Name: "profilingrun-earlier", UID: "earlier-job-uid"},
		Labels: map[string]string{
			profilingrun.MeasurementWorkloadLabelKey: profilingrun.MeasurementWorkloadLabelValue,
		},
		Images: []string{"registry.example/runtime@sha256:" + strings.Repeat("d", 64)},
	}
	result.InitialActivePods = append(result.InitialActivePods, other)
	sort.Slice(result.InitialActivePods, func(i, j int) bool {
		return workloadIdentity(result.InitialActivePods[i]) < workloadIdentity(result.InitialActivePods[j])
	})
	runtimeStarted := result.CoverageStartedAt.Add(10 * time.Second)
	runtimeEnded := result.CoverageStartedAt.Add(20 * time.Second)
	result.Events = []workloadobserver.Event{{
		Kind: workloadobserver.EventExited, ObservedAt: runtimeStarted.Add(-time.Second),
		ResourceVersion: "11", Pod: other,
	}}
	result.LastResourceVersion = "11"

	window, err := projectWorkloadWindow(&result, runtimeStarted, runtimeEnded)
	if err != nil {
		t.Fatal(err)
	}
	if !window.Known || len(window.ConcurrentCHILLWorkloads) != 0 || len(window.ExternalWorkloadChanges) != 0 {
		t.Fatalf("pre-runtime CHILL workload projection = %#v", window)
	}
}

func admissionRunConfigWithWorkloadContract() profilingrun.RunConfig {
	config := admissionRunConfig()
	config.Workload = profilingrun.CurrentWorkloadContract()
	return config
}

func projectDefaultWorkloadWindow(
	result *workloadobserver.Result,
) (workloadWindow, error) {
	return projectWorkloadWindow(
		result,
		result.CoverageStartedAt.Add(time.Second),
		result.CoverageEndedAt.Add(-time.Second),
	)
}

func admissionWorkloadResult(t *testing.T) workloadobserver.Result {
	t.Helper()
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
		CoverageStartedAt: started, CoverageEndedAt: started.Add(32 * time.Second),
		InitialResourceVersion: "10", LastResourceVersion: "10", EventLimit: 10,
		InitialActivePods: pods, FinalActivePods: append([]workloadobserver.Pod(nil), pods...), Continuous: true,
	}
}

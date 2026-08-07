package workloadobserver

import (
	"strings"
	"testing"
	"time"
)

func TestResultValidateAcceptsContinuousWindowAndTransientPod(t *testing.T) {
	t.Parallel()
	result := validResult()
	transient := testPod("default", "transient", "transient-uid", "Running")
	result.Events = []Event{
		{
			Kind: EventEntered, ObservedAt: result.CoverageStartedAt.Add(time.Second),
			ResourceVersion: "101", Pod: transient,
		},
		{
			Kind: EventExited, ObservedAt: result.CoverageStartedAt.Add(2 * time.Second),
			ResourceVersion: "102", Pod: transient,
		},
	}
	result.LastResourceVersion = "102"
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestResultValidateAcceptsTypedIncompleteObservation(t *testing.T) {
	t.Parallel()
	result := validResult()
	result.Continuous = false
	result.Failure = &Failure{
		ObservedAt: result.CoverageStartedAt.Add(time.Second),
		Reason:     FailureReasonHistoryExpired,
		Message:    "Pod watch history expired",
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestResultCanonicalNormalizesTimeAndOwnsMutableFacts(t *testing.T) {
	const changed = "changed"
	result := validResult()
	location := time.FixedZone("KST", 9*60*60)
	result.CoverageStartedAt = result.CoverageStartedAt.In(location)
	result.CoverageEndedAt = result.CoverageEndedAt.In(location)
	canonical, err := result.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if canonical.CoverageStartedAt.Location() != time.UTC || canonical.CoverageEndedAt.Location() != time.UTC {
		t.Fatalf("canonical coverage is not UTC: %#v", canonical)
	}
	result.InitialActivePods[0].Labels["edge.dacs.io/profiling-background"] = changed
	result.InitialActivePods[0].Owner.Name = changed
	if canonical.InitialActivePods[0].Labels["edge.dacs.io/profiling-background"] != "allowed" ||
		canonical.InitialActivePods[0].Owner.Name == changed {
		t.Fatalf("canonical result aliases source facts: %#v", canonical.InitialActivePods[0])
	}
}

func TestResultValidateRejectsContradictoryEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		alter func(*Result)
		match string
	}{
		{name: "schema", alter: func(result *Result) { result.SchemaVersion = "old" }, match: "schemaVersion"},
		{
			name: "coverage", alter: func(result *Result) { result.CoverageEndedAt = result.CoverageStartedAt },
			match: "coverage window",
		},
		{
			name: "resource version", alter: func(result *Result) { result.InitialResourceVersion = "" },
			match: "resourceVersion",
		},
		{
			name: "event limit", alter: func(result *Result) { result.EventLimit = 0 },
			match: "event limit",
		},
		{
			name: "continuous with failure", alter: func(result *Result) {
				result.Failure = &Failure{
					ObservedAt: result.CoverageStartedAt, Reason: FailureReasonWatchFailed, Message: "closed",
				}
			},
			match: "continuity",
		},
		{
			name: "incomplete without failure", alter: func(result *Result) { result.Continuous = false },
			match: "continuity",
		},
		{
			name: "unknown failure", alter: func(result *Result) {
				result.Continuous = false
				result.Failure = &Failure{
					ObservedAt: result.CoverageStartedAt, Reason: "Unknown", Message: "closed",
				}
			},
			match: "failure reason",
		},
		{
			name: "missing Run Pod", alter: func(result *Result) {
				result.InitialActivePods = result.InitialActivePods[1:]
			},
			match: "Run Pod",
		},
		{
			name: "unsorted initial", alter: func(result *Result) {
				result.InitialActivePods[0], result.InitialActivePods[1] =
					result.InitialActivePods[1], result.InitialActivePods[0]
			},
			match: "canonically ordered",
		},
		{
			name: "invalid phase", alter: func(result *Result) {
				result.InitialActivePods[0].Phase = "Succeeded"
			},
			match: "not active",
		},
		{
			name: "entered existing Pod", alter: func(result *Result) {
				result.Events = []Event{{
					Kind: EventEntered, ObservedAt: result.CoverageStartedAt,
					ResourceVersion: "101", Pod: result.InitialActivePods[0],
				}}
			},
			match: "already active",
		},
		{
			name: "event outside coverage", alter: func(result *Result) {
				result.Events = []Event{{
					Kind: EventUpdated, ObservedAt: result.CoverageEndedAt.Add(time.Second),
					ResourceVersion: "101", Pod: result.InitialActivePods[0],
				}}
			},
			match: "outside coverage",
		},
		{
			name: "final mismatch", alter: func(result *Result) {
				result.FinalActivePods = result.FinalActivePods[:1]
			},
			match: "final active set",
		},
		{
			name: "invalid labels", alter: func(result *Result) {
				result.InitialActivePods[0].Labels = map[string]string{" bad": "value"}
			},
			match: "selector labels",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validResult()
			test.alter(&result)
			if err := result.Validate(); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.match)
			}
		})
	}
}

func validResult() Result {
	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	run := testPod("chill-system", "run-pod", "run-pod-uid", "Running")
	background := testPod("monitoring", "edge-metrics", "edge-metrics-uid", "Running")
	pods := []Pod{run, background}
	return Result{
		SchemaVersion:          ResultSchemaVersion,
		Target:                 TargetIdentity{NodeName: "lattepanda", NodeUID: "node-uid"},
		Run:                    RunIdentity{Namespace: "chill-system", JobUID: "job-uid", PodName: run.Name, PodUID: run.UID},
		CoverageStartedAt:      started,
		CoverageEndedAt:        started.Add(30 * time.Second),
		InitialResourceVersion: "100",
		LastResourceVersion:    "100",
		EventLimit:             64,
		InitialActivePods:      append([]Pod(nil), pods...),
		FinalActivePods:        append([]Pod(nil), pods...),
		Continuous:             true,
	}
}

func testPod(namespace, name, uid, phase string) Pod {
	return Pod{
		Namespace: namespace, Name: name, UID: uid, Phase: phase,
		Owner:  &OwnerIdentity{Kind: "DaemonSet", Name: name, UID: uid + "-owner"},
		Images: []string{"registry.example/" + name + "@sha256:" + strings.Repeat("b", 64)},
		Labels: map[string]string{"edge.dacs.io/profiling-background": "allowed"},
	}
}

package serving

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
)

func (a *observationArchive) Open(_ context.Context, ref evidencestore.ObjectIdentity) (io.ReadCloser, error) {
	return os.Open(filepath.Join(a.directory, ref.Key))
}

const (
	calibrationPriorDefinition = "prior definition window"
	calibrationWrongProfile    = "wrong profile"
)

func TestCompletionCalibrationOutlivesArrivalsWithoutChangingTheirWindow(t *testing.T) {
	for _, condition := range []string{
		"complete", calibrationPriorDefinition, "incomplete", calibrationWrongProfile,
		"native incomplete", "native span invalid", "response failed",
	} {
		t.Run(condition, func(t *testing.T) {
			j, archive, selection, snapshot := prepareCalibrationTrial(t, condition)
			instance := snapshot.Instances[0]
			if condition == calibrationWrongProfile {
				profile := *instance.Profile
				profile.ID = "unmeasured-calibration-profile"
				instance.Profile = &profile
			}
			facts, err := LoadCompletionCalibration(context.Background(), archive, selection, 64<<20)
			if condition == calibrationWrongProfile {
				if err != nil {
					t.Fatal(err)
				}
				for _, fact := range facts {
					if fact.Matches(instance) {
						t.Fatalf("wrong preparation profile accepted raw calibration: %+v", fact)
					}
				}
				return
			}
			if condition != "complete" && condition != calibrationPriorDefinition {
				if err == nil || len(facts) != 0 {
					t.Fatalf("invalid calibration became usable: %+v %v", facts, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			requireRetainedCalibration(t, j, selection, snapshot, facts["removed-executor"])
		})
	}
}

func prepareCalibrationTrial(t *testing.T, condition string) (
	*ObservationLog, *observationArchive, CompletionCalibration, SchedulerSnapshot,
) {
	t.Helper()
	ctx := context.Background()
	archive := &observationArchive{directory: t.TempDir()}
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: archive})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observationLog.Close(ctx) })
	_, snapshot := schedulerFixture()
	instance, d := snapshot.Instances[0], snapshot.Workloads[0]
	if err = observationLog.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	if condition == calibrationPriorDefinition {
		if err = observationLog.finish(); err != nil {
			t.Fatal(err)
		}
		if err = observationLog.start(time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().UTC()
	snapshot.At = at
	snapshot.ObservationEnd = at
	p := &BackendProgress{RequestID: "trial", WorkloadUID: d.UID, Definition: d.Identity(),
		Phase: phaseFinished, ProfileID: instance.Profile.ID, ExecutionComplete: true, NativeElapsedMS: 6}
	outcome := outcomeCompleted
	switch condition {
	case "native incomplete":
		p.ExecutionComplete = false
	case "native span invalid":
		p.NativeElapsedMS = 11
	case "response failed":
		outcome = "response_failed"
	}
	for _, event := range []RequestEvent{
		{RequestID: "trial", Kind: phaseReceived, WorkloadUID: d.UID, Definition: d.Identity(), At: at, ReceivedAt: at},
		{RequestID: "trial", Kind: eventBackendExchangeFinished, WorkloadUID: d.UID, Definition: d.Identity(),
			Outcome: outcomeCompleted, At: at, ReceivedAt: at,
			Assignment: &Assignment{Instance: "removed-executor", Node: instance.Node, Profile: instance.Profile.ID,
				Artifact: instance.Profile.Condition.Artifact.Name, ExecutionPath: instance.Profile.Condition.Artifact.ExecutionPath},
			NativeProgress: p, ClientSpanNanos: int64(10 * time.Millisecond), ElapsedNanos: int64(20 * time.Millisecond)},
		{RequestID: "trial", Kind: eventResponseFinished, WorkloadUID: d.UID, Definition: d.Identity(),
			Outcome: outcome, At: at, ReceivedAt: at, ElapsedNanos: int64(23 * time.Millisecond)},
	} {
		if err = observationLog.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if condition == "incomplete" {
		observationLog.windows[observationLog.active].Incomplete = true
	}
	if err = observationLog.Close(ctx); err != nil {
		t.Fatal(err)
	}
	windows := observationLog.Health().Windows
	selection := CompletionCalibration{
		From: windows[len(windows)-1].StartedAt, To: at.Add(time.Nanosecond),
		Windows: make([]CompletionCalibrationWindow, 0, len(windows)),
	}
	for _, window := range windows {
		refs := CompletionCalibrationWindow{}
		for _, ref := range window.Objects {
			switch filepath.Base(ref.Key) {
			case "metadata.json":
				refs.Metadata = ref
			case "definitions.jsonl":
				refs.Definitions = ref
			case "requests.jsonl":
				refs.Requests = ref
			}
		}
		selection.Windows = append(selection.Windows, refs)
	}
	return observationLog, archive, selection, snapshot
}

func requireRetainedCalibration(
	t *testing.T, observationLog *ObservationLog, selection CompletionCalibration, snapshot SchedulerSnapshot, fact ObservedOverhead,
) {
	t.Helper()
	instance := snapshot.Instances[0]
	other := *snapshot.Workloads[0]
	other.UID, other.Name, other.SLO = "new-uid", "new-workload", SLO{Percentile: 90, TargetLatency: 1500 * time.Millisecond}
	instance.WorkloadUIDs = []string{other.UID}
	if !MatchesProfile(&other, instance.Profile) || !fact.Matches(instance) ||
		fact.Completion != 17*time.Millisecond || fact.Requests != 1 ||
		len(fact.TrialIDs) != 1 || fact.TrialIDs[0] != "trial" ||
		len(fact.References) != 3*len(selection.Windows) ||
		!fact.From.Equal(selection.From) || !fact.To.Equal(selection.To) {
		t.Fatalf("old UID calibration did not retain compatible raw quantity: %+v", fact)
	}
	requireBoundedCompletionPolicy(t, instance, fact)
	later := snapshot.At.Add(61 * time.Second)
	arrivals, err := observationLog.SchedulerArrivals(later.Add(-60*time.Second), later)
	if err != nil || len(arrivals) != 0 {
		t.Fatalf("fixed 60s arrivals window changed: %+v %v", arrivals, err)
	}
	live, err := observationLog.ObservedOverhead(later.Add(-60*time.Second), later)
	if err != nil || len(live) != 0 || !fact.Matches(instance) {
		t.Fatalf("calibration incorrectly expired with arrivals: %+v %v", live, err)
	}
}

func requireBoundedCompletionPolicy(t *testing.T, instance SchedulerInstance, fact ObservedOverhead) {
	t.Helper()
	fullSource := fact.Source
	references := slices.Clone(fact.References)
	if len(fullSource) <= 256 {
		t.Fatal("raw observation log regression requires genuinely long completion provenance")
	}
	instance.CompletionMeasurement = &fact
	instance.CompletionOverhead, instance.EnvelopeSource = fact.Completion, fullSource
	policy := schedulerPolicy(instance)
	policy.DecisionID = "completion-policy-regression"
	if len(policy.EnvelopeSource) > 256 || policy.CompletionOverheadMS != float64(fact.Completion)/float64(time.Millisecond) ||
		!strings.Contains(policy.EnvelopeSource, fact.From.UTC().Format(time.RFC3339Nano)) ||
		!strings.Contains(policy.EnvelopeSource, fact.To.UTC().Format(time.RFC3339Nano)) {
		t.Fatalf("native policy lost bounded actual quantity/provenance link: %+v", policy)
	}
	config := native.Config{
		Model: "model", Mode: "serving", ProfilePath: "/profile/bundle.json",
		Inputs:        []native.InputSpec{{Name: "x", Datatype: "FP32", Shape: []int{1}}},
		RunnerCommand: []string{"runner"}, RunnerTimeoutMS: 1000,
		MaxQueueRequests: 4, MaxQueueBytes: 1024, MaxRequestBytes: 1024,
		MaxBatchSize: 1, BatchSizes: []int{1}, Policy: policy,
	}
	if err := native.ValidateConfig(&config); err != nil {
		t.Fatalf("real completion quantity generated invalid native policy: %v", err)
	}
	if fact.Source != fullSource || instance.EnvelopeSource != fullSource || !slices.Equal(fact.References, references) {
		t.Fatal("compact native witness rewrote full Serving source or immutable raw references")
	}
}

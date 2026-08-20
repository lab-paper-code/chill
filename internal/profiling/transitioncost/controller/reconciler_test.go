package controller

import (
	"context"
	"errors"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPowerModeTransitionCostReconcilerProjectsEveryMeasurementStage(t *testing.T) {
	tests := []struct {
		name        string
		stage       measurementStage
		phase       edgev1alpha1.PowerModeTransitionCostPhase
		requeue     bool
		terminal    bool
		writerCalls int
	}{
		{name: "active", stage: measurementStageAttemptActive,
			phase: edgev1alpha1.PowerModeTransitionCostPhaseMeasuring},
		{name: "attempt finalized", stage: measurementStageAttemptFinalized,
			phase: edgev1alpha1.PowerModeTransitionCostPhaseMeasuring, requeue: true},
		{name: "complete", stage: measurementStageComplete,
			phase: edgev1alpha1.PowerModeTransitionCostPhaseComplete, terminal: true, writerCalls: 1},
		{name: "insufficient", stage: measurementStageInsufficient,
			phase: edgev1alpha1.PowerModeTransitionCostPhaseFailed, terminal: true, writerCalls: 1},
		{name: "failed", stage: measurementStageFailed,
			phase: edgev1alpha1.PowerModeTransitionCostPhaseFailed, terminal: true, writerCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReconcilerFixture(t, test.stage)
			result, err := fixture.reconciler.Reconcile(context.Background(), fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			if result.Requeue != test.requeue || fixture.writer.calls != test.writerCalls ||
				fixture.kube.patches != 1 {
				t.Fatalf("result = %+v, writer calls = %d, status patches = %d",
					result, fixture.writer.calls, fixture.kube.patches)
			}
			current := fixture.current(t)
			if current.Status.ObservedGeneration != current.Generation || current.Status.Phase != test.phase ||
				len(current.Status.Progress) != 2 {
				t.Fatalf("status = %#v", current.Status)
			}
			assertReconcilerProgress(t, current, test.terminal)
			if test.terminal {
				want := fixture.writer.result.reference
				got := current.Status.Result
				if got == nil || got.Store != want.Store || got.Key != want.Key ||
					got.SchemaVersion != want.SchemaVersion || got.Size != want.Size ||
					fixture.writer.statusAtCall != "" {
					t.Fatalf("terminal status/result order = %#v, writer saw phase %q",
						got, fixture.writer.statusAtCall)
				}
			} else if current.Status.Result != nil {
				t.Fatalf("nonterminal result = %#v", current.Status.Result)
			}

			if _, err := fixture.reconciler.Reconcile(context.Background(), fixture.request); err != nil {
				t.Fatal(err)
			}
			if fixture.kube.patches != 1 || fixture.writer.calls != test.writerCalls {
				t.Fatalf("stable status patches = %d, writer calls = %d",
					fixture.kube.patches, fixture.writer.calls)
			}
		})
	}
}

func TestPowerModeTransitionCostReconcilerSkipsCompleteTerminalStatus(t *testing.T) {
	fixture := newReconcilerFixture(t, measurementStageComplete)
	stored := fixture.writer.result
	measurement := fixture.measurement.DeepCopy()
	measurement.Status = projectMeasurementStatus(
		measurement,
		edgev1alpha1.PowerModeTransitionCostPhaseComplete,
		stored.record.Directions,
		&stored.reference,
	)
	fixture = newReconcilerFixtureWithMeasurement(t, measurement, measurementStageComplete)
	if _, err := fixture.reconciler.Reconcile(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	if fixture.sequence.calls != 0 || fixture.writer.calls != 0 || fixture.kube.patches != 0 {
		t.Fatalf("terminal no-op crossed ports: sequence=%d writer=%d patches=%d",
			fixture.sequence.calls, fixture.writer.calls, fixture.kube.patches)
	}

	measurement.Status.Progress = measurement.Status.Progress[:1]
	fixture = newReconcilerFixtureWithMeasurement(t, measurement, measurementStageAttemptActive)
	if _, err := fixture.reconciler.Reconcile(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	if fixture.sequence.calls != 1 || fixture.kube.patches != 1 ||
		fixture.current(t).Status.Phase != edgev1alpha1.PowerModeTransitionCostPhaseMeasuring {
		t.Fatal("incomplete terminal status was trusted")
	}
}

func TestPowerModeTransitionCostReconcilerReturnsTransientErrorsWithoutStatus(t *testing.T) {
	tests := []struct {
		name        string
		stage       measurementStage
		sequenceErr error
		writerErr   error
		statusErr   error
	}{
		{name: "sequence", stage: measurementStageAttemptActive, sequenceErr: errors.New("sequence unavailable")},
		{name: "writer", stage: measurementStageFailed, writerErr: errors.New("store unavailable")},
		{name: "status", stage: measurementStageAttemptActive, statusErr: errors.New("status conflict")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReconcilerFixture(t, test.stage)
			fixture.sequence.err = test.sequenceErr
			fixture.writer.err = test.writerErr
			fixture.kube.patchErr = test.statusErr
			if _, err := fixture.reconciler.Reconcile(context.Background(), fixture.request); err == nil {
				t.Fatal("transient error was swallowed")
			}
			current := fixture.current(t)
			if current.Status.ObservedGeneration != 0 || current.Status.Phase != "" || current.Status.Result != nil {
				t.Fatalf("transient error published status %#v", current.Status)
			}
		})
	}
}

func TestNewPowerModeTransitionCostReconcilerRequiresExactPorts(t *testing.T) {
	measurement, _, scheme := preparationRecordFixture(t)
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(measurement).Build()
	sequence := &reconcileSequence{}
	writer := &reconcileResultWriter{}
	for name, build := range map[string]func() error{
		"client": func() error {
			_, err := newPowerModeTransitionCostReconciler(nil, sequence, writer)
			return err
		},
		"sequence": func() error {
			_, err := newPowerModeTransitionCostReconciler(kube, nil, writer)
			return err
		},
		"writer": func() error {
			_, err := newPowerModeTransitionCostReconciler(kube, sequence, nil)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := build(); err == nil {
				t.Fatal("missing reconciler port was accepted")
			}
		})
	}
}

type reconcilerFixture struct {
	measurement *edgev1alpha1.PowerModeTransitionCost
	request     ctrl.Request
	kube        *trackingStatusClient
	sequence    *reconcileSequence
	writer      *reconcileResultWriter
	reconciler  *powerModeTransitionCostReconciler
}

func newReconcilerFixture(t *testing.T, stage measurementStage) reconcilerFixture {
	t.Helper()
	measurement, _, _ := preparationRecordFixture(t)
	return newReconcilerFixtureWithMeasurement(t, measurement, stage)
}

func newReconcilerFixtureWithMeasurement(
	t *testing.T,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	stage measurementStage,
) reconcilerFixture {
	t.Helper()
	_, prepared, scheme := preparationRecordFixture(t)
	measurement.Generation = 3
	attempts := []FinalizedAttempt{
		sequenceFinalizedAttempt(t, prepared, 0, transitioncost.VerdictAccepted, "d0-a1"),
		sequenceFinalizedAttempt(t, prepared, 1, transitioncost.VerdictInvalid, "d1-a1"),
		sequenceFinalizedAttempt(t, prepared, 0, transitioncost.VerdictAccepted, "d0-a2"),
	}
	base := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.PowerModeTransitionCost{}).
		WithObjects(measurement).
		Build()
	kube := &trackingStatusClient{Client: base}
	sequence := &reconcileSequence{progress: measurementProgress{stage: stage, attempts: attempts}}
	writer := &reconcileResultWriter{result: reconcilerStoredResult(stage)}
	reconciler, err := newPowerModeTransitionCostReconciler(kube, sequence, writer)
	if err != nil {
		t.Fatal(err)
	}
	return reconcilerFixture{
		measurement: measurement,
		request:     ctrl.Request{NamespacedName: client.ObjectKeyFromObject(measurement)},
		kube:        kube,
		sequence:    sequence,
		writer:      writer,
		reconciler:  reconciler,
	}
}

func reconcilerStoredResult(stage measurementStage) storedMeasurementResult {
	return storedMeasurementResult{
		reference: edgev1alpha1.ProfilingRunEvidenceReference{
			Store: attemptEvidenceStore, Key: "measurement/result.json",
			SchemaVersion: measurementResultSchemaVersion, Size: 10,
		},
		record: measurementResultRecord{
			Outcome: stage,
			Directions: []measurementResultDirection{
				{AcceptedCount: 2, FinalEvidence: reconcilerEvidence(3, "d0")},
				{AcceptedCount: 1, FinalEvidence: reconcilerEvidence(2, "d1")},
			},
		},
	}
}

func reconcilerEvidence(count int, prefix string) []evidencestore.ObjectIdentity {
	references := make([]evidencestore.ObjectIdentity, count)
	for index := range references {
		references[index] = evidencestore.ObjectIdentity{
			Store: attemptEvidenceStore, Key: prefix + "/final.json", Size: int64(index + 1),
		}
	}
	return references
}

func assertReconcilerProgress(
	t *testing.T,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	terminal bool,
) {
	t.Helper()
	progress := measurement.Status.Progress
	wantAttempts, wantAccepted := [2]int32{2, 1}, [2]int32{2, 0}
	if terminal {
		wantAttempts, wantAccepted = [2]int32{3, 2}, [2]int32{2, 1}
	}
	for index := 0; index < 2; index++ {
		if progress[index].SourceMode != measurement.Spec.Modes[index].PowerMode ||
			progress[index].TargetMode != measurement.Spec.Modes[1-index].PowerMode ||
			progress[index].AttemptCount != wantAttempts[index] ||
			progress[index].AcceptedCount != wantAccepted[index] {
			t.Fatalf("progress %d = %#v", index, progress[index])
		}
	}
}

func (f reconcilerFixture) current(t *testing.T) *edgev1alpha1.PowerModeTransitionCost {
	t.Helper()
	current := &edgev1alpha1.PowerModeTransitionCost{}
	if err := f.kube.Get(context.Background(), f.request.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	return current
}

type reconcileSequence struct {
	progress measurementProgress
	err      error
	calls    int
}

func (s *reconcileSequence) advance(
	context.Context,
	*edgev1alpha1.PowerModeTransitionCost,
) (measurementProgress, error) {
	s.calls++
	return s.progress, s.err
}

type reconcileResultWriter struct {
	result       storedMeasurementResult
	err          error
	calls        int
	statusAtCall edgev1alpha1.PowerModeTransitionCostPhase
}

func (w *reconcileResultWriter) write(
	_ context.Context,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	_ measurementProgress,
) (storedMeasurementResult, error) {
	w.calls++
	w.statusAtCall = measurement.Status.Phase
	return w.result, w.err
}

type trackingStatusClient struct {
	client.Client
	patches  int
	patchErr error
}

func (c *trackingStatusClient) Status() client.SubResourceWriter {
	return &trackingStatusWriter{SubResourceWriter: c.Client.Status(), client: c}
}

type trackingStatusWriter struct {
	client.SubResourceWriter
	client *trackingStatusClient
}

// Composition remains intentionally outside this work unit.
var _ = (*powerModeTransitionCostReconciler).setupWithManager

func (w *trackingStatusWriter) Patch(
	ctx context.Context,
	object client.Object,
	patch client.Patch,
	options ...client.SubResourcePatchOption,
) error {
	w.client.patches++
	if w.client.patchErr != nil {
		return w.client.patchErr
	}
	return w.SubResourceWriter.Patch(ctx, object, patch, options...)
}

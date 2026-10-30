package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
			if current.Status.CleanupPending != test.terminal {
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

func TestPowerModeTransitionCostReconcilerSkipsTerminalSequenceButRetriesCleanup(t *testing.T) {
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

func TestPowerModeTransitionCostReconcilerDoesNotPromoteLegacyResultCleanup(t *testing.T) {
	fixture := newReconcilerFixture(t, measurementStageFailed)
	fixture.writer.result.record.CleanupPending = false
	if _, err := fixture.reconciler.Reconcile(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	current := fixture.current(t)
	if current.Status.Result == nil || current.Status.CleanupPending {
		t.Fatalf("legacy result cleanup eligibility changed: %+v", current.Status)
	}
	eligible := current.Status
	eligible.CleanupPending = true
	if sameMeasurementStatus(current.Status, eligible) {
		t.Fatal("status comparison ignored cleanup eligibility")
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

func TestPowerModeTransitionCostReconcilerWaitsForAdmission(t *testing.T) {
	for _, test := range []struct {
		name, admission string
		requeue         time.Duration
		phase           edgev1alpha1.PowerModeTransitionCostPhase
	}{
		{"not admitted", "", 5 * time.Second, ""},
		{"refused", profilingrun.RefusedAdmission("RunnerProfileMissing", ""), 0, edgev1alpha1.PowerModeTransitionCostPhaseFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			measurement, _, _ := preparationRecordFixture(t)
			metav1.SetMetaDataAnnotation(&measurement.ObjectMeta, profilingrun.AdmissionAnnotation, test.admission)
			fixture := newReconcilerFixtureWithMeasurement(t, measurement, measurementStageAttemptActive)
			result, err := fixture.reconciler.Reconcile(context.Background(), fixture.request)
			if err != nil || result.RequeueAfter != test.requeue || fixture.sequence.calls != 0 || fixture.writer.calls != 0 {
				t.Fatalf("result=%+v err=%v sequence=%d writer=%d", result, err, fixture.sequence.calls, fixture.writer.calls)
			}
			current := fixture.current(t)
			if current.Status.Phase != test.phase || current.Status.Result != nil {
				t.Fatalf("status = %#v", current.Status)
			}
			if _, err := fixture.reconciler.Reconcile(context.Background(), fixture.request); err != nil || fixture.sequence.calls != 0 {
				t.Fatalf("second reconcile err=%v sequence=%d", err, fixture.sequence.calls)
			}
			if test.phase != "" && fixture.kube.patches != 1 {
				t.Fatalf("refusal status patches = %d, want 1", fixture.kube.patches)
			}
		})
	}
}

func TestNewPowerModeTransitionCostReconcilerRequiresExactPorts(t *testing.T) {
	measurement, _, scheme := preparationRecordFixture(t)
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(measurement).Build()
	sequence := &reconcileSequence{}
	writer := &reconcileResultWriter{}
	cleanup := &attemptJobCleanup{}
	for name, build := range map[string]func() error{
		"client": func() error {
			_, err := newPowerModeTransitionCostReconciler(nil, sequence.advance, writer.write, cleanup)
			return err
		},
		"sequence": func() error {
			_, err := newPowerModeTransitionCostReconciler(kube, nil, writer.write, cleanup)
			return err
		},
		"writer": func() error {
			_, err := newPowerModeTransitionCostReconciler(kube, sequence.advance, nil, cleanup)
			return err
		},
		"cleanup": func() error {
			_, err := newPowerModeTransitionCostReconciler(kube, sequence.advance, writer.write, nil)
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
	measurement.Spec.AttemptLimit = 5
	return newReconcilerFixtureWithMeasurement(t, measurement, stage)
}

func newReconcilerFixtureWithMeasurement(
	t *testing.T,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	stage measurementStage,
) reconcilerFixture {
	t.Helper()
	_, prepared, scheme := preparationRecordFixture(t)
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	measurement.Generation = 3
	if _, set := measurement.Annotations[profilingrun.AdmissionAnnotation]; !set {
		metav1.SetMetaDataAnnotation(&measurement.ObjectMeta, profilingrun.AdmissionAnnotation, profilingrun.GrantedAdmission(time.Unix(0, 0)))
	}
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
	finalizer, err := NewAttemptFinalizer(kube, newFinalizationStore(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := &attemptJobCleanup{client: kube, finalizer: finalizer, failedJobRetentionSeconds: 86400}
	reconciler, err := newPowerModeTransitionCostReconciler(kube, sequence.advance, writer.write, cleanup)
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
			SchemaVersion: transitioncost.MeasurementResultSchemaVersion, Size: 10,
		},
		record: measurementResultRecord{
			Outcome:        stage,
			CleanupPending: true,
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

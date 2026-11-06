package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type projectionObjects struct {
	payload   []byte
	locations []evidencestore.ObjectLocation
	err       error
	closeErr  error
	closed    bool
	onOpen    func()
}

func (o *projectionObjects) OpenLocation(ctx context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
	o.locations = append(o.locations, location)
	if o.onOpen != nil {
		o.onOpen()
	}
	if o.err != nil {
		return nil, o.err
	}
	return &projectionBody{Reader: bytes.NewReader(o.payload), owner: o}, nil
}

type projectionBody struct {
	*bytes.Reader
	owner *projectionObjects
}

func (b *projectionBody) Close() error {
	b.owner.closed = true
	return b.owner.closeErr
}

func projectionRecord(t *testing.T) (edge.OperationCostMeasurement, servingops.OperationMeasurement) {
	t.Helper()
	key, raw := costTestRecord(t, "cr-uid.0.prepare", "fixed", time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))
	var record servingops.OperationMeasurement
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	cr := edge.OperationCostMeasurement{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "cost", UID: "cr-uid"},
		Spec: edge.OperationCostMeasurementSpec{Node: record.Request.Operation.Node.Name, Steps: []edge.OperationCostStep{
			{Name: "prepare", Kind: edge.OperationCostStepPrepare},
			{Name: "not-executed", Kind: edge.OperationCostStepRemove},
		}},
		Status: edge.OperationCostMeasurementStatus{Phase: edge.MeasurementPhaseFailed, Reason: "sequence stopped", Steps: []edge.OperationCostStepStatus{{Name: "prepare", Key: key, Verdict: servingstate.MeasurementAccepted}}},
	}
	return cr, record
}

func projectionPayload(t *testing.T, record servingops.OperationMeasurement) []byte {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLoadOperationCostStepsActualHistoryAndIdle(t *testing.T) {
	for _, name := range []string{"accepted", "zero", servingstate.NativeOperationFailed, "insufficient", "isolation", "not-invoked", "idle"} {
		t.Run(name, func(t *testing.T) {
			cr, record := projectionRecord(t)
			wantJ := true
			switch name {
			case "zero":
				for i := range record.Power.Samples {
					record.Power.Samples[i].Watts = 0
				}
				zero := 0.0
				record.WallJoules, record.EnergyUncertaintyJoules = &zero, &zero
				record.PhaseSensitivityJoules, record.MissingGapJoules = &zero, &zero
			case servingstate.NativeOperationFailed:
				record.Verdict, record.Reason, record.Result.Status = string(edge.MeasurementPhaseFailed), "physical action failed", servingstate.NativeOperationFailed
				// An unfinished/failed power window must not be given Accepted's
				// completeness checks, nor expose a leftover energy value.
				record.Power.Samples = nil
				wantJ = false
			case "insufficient":
				record.Verdict, record.Reason = servingops.MeasurementInsufficient, "power window insufficient"
				record.Power.Samples = nil
				wantJ = false
			case "isolation":
				_ = record.ApplyIsolation(servingops.MeasurementIsolation{Node: record.Request.Operation.Node}, errors.New("isolation unconfirmed"))
				wantJ = false
			case "not-invoked":
				record.Verdict, record.Reason, record.Result = servingops.MeasurementInsufficient, "no first sample", servingstate.OperationResult{}
				wantJ = false
			case "idle":
				cr.Spec.Steps[0].Kind = edge.OperationCostStepIdle
				planned := int32(99)
				cr.Spec.Steps[0].IdleDurationSeconds = &planned
				record.Request.Operation.Kind, record.Result.Operation.Kind = servingstate.OperationIdle, servingstate.OperationIdle
				record.Result.Before = record.Result.Actual
				record.StateObservations = []servingstate.OperationState{record.Result.Before, record.Result.Actual}
				record.MeanWatts = record.WallJoules
			}
			cr.Status.Steps[0].Verdict = record.Verdict
			objects := &projectionObjects{payload: projectionPayload(t, record)}
			steps, err := LoadOperationCostSteps(context.Background(), cr, objects, "store", 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if len(steps) != 1 || steps[0].OperationID != "cr-uid.0.prepare" || steps[0].Reason != record.Reason {
				t.Fatalf("invented successor or lost identity/reason: %+v", steps)
			}
			if name == "not-invoked" {
				if steps[0].DurationNanos != nil {
					t.Fatal("invented actual duration")
				}
			} else if steps[0].DurationNanos == nil || *steps[0].DurationNanos != int64(time.Second) {
				t.Fatalf("not actual end-start: %+v", steps[0])
			}
			if (steps[0].WallJoules != nil) != wantJ || (wantJ && *steps[0].WallJoules != *record.WallJoules) {
				t.Fatalf("incorrect accepted energy: %+v", steps[0])
			}
			if !objects.closed || len(objects.locations) != 1 || objects.locations[0].Key != cr.Status.Steps[0].Key {
				t.Fatalf("not an exact closed read: %+v", objects)
			}
		})
	}
}

func TestLoadOperationCostStepsUsesStoredHistoryWithoutReadmission(t *testing.T) {
	cr, record := projectionRecord(t)
	cr.UID = "new-request"
	cr.Spec.Steps = nil
	cr.Status.Steps[0].Name = "renamed"
	cr.Status.Steps[0].Key = "archive/step.json"
	cr.Status.Steps[0].Verdict = string(edge.MeasurementPhaseFailed)
	record.Request.Store = "historical-store"
	record.Power.Samples = nil
	record.EnergyUncertaintyJoules = nil
	record.Result.Operation.ID = "old-result-metadata"
	steps, err := LoadOperationCostSteps(context.Background(), cr,
		&projectionObjects{payload: projectionPayload(t, record)}, "store", 1<<20)
	if err != nil || len(steps) != 1 || steps[0].Verdict != servingstate.MeasurementAccepted ||
		steps[0].DurationNanos == nil || *steps[0].DurationNanos != int64(time.Second) ||
		steps[0].WallJoules == nil || *steps[0].WallJoules != 20 {
		t.Fatalf("stored accepted history was re-admitted or hidden: %+v %v", steps, err)
	}
}

func TestLoadOperationCostStepsRejectsUnreadableEvidence(t *testing.T) {
	for _, name := range []string{"schema", "corrupt", "bound"} {
		t.Run(name, func(t *testing.T) {
			cr, record := projectionRecord(t)
			if name == "schema" {
				record.SchemaVersion = "unsupported-projection-schema"
			}
			objects := &projectionObjects{payload: projectionPayload(t, record)}
			bound := int64(1 << 20)
			if name == "corrupt" {
				objects.payload = []byte("{")
			}
			if name == "bound" {
				bound = int64(len(objects.payload) - 1)
			}
			if _, err := LoadOperationCostSteps(context.Background(), cr, objects, "store", bound); err == nil {
				t.Fatal("accepted unreadable evidence")
			}
		})
	}
}

func TestLoadOperationCostStepsPropagatesIOAndCancellation(t *testing.T) {
	cr, record := projectionRecord(t)
	for _, failure := range []error{evidencestore.ErrNotFound, errors.New("permission denied"), context.Canceled} {
		objects := &projectionObjects{err: failure}
		if _, err := LoadOperationCostSteps(context.Background(), cr, objects, "store", 1<<20); !errors.Is(err, failure) {
			t.Fatalf("lost store error: %v", err)
		}
	}
	failure := errors.New("close failed")
	objects := &projectionObjects{payload: projectionPayload(t, record), closeErr: failure}
	if _, err := LoadOperationCostSteps(context.Background(), cr, objects, "store", 1<<20); !errors.Is(err, failure) || !objects.closed {
		t.Fatalf("lost close error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	objects = &projectionObjects{}
	if _, err := LoadOperationCostSteps(ctx, cr, objects, "store", 1<<20); !errors.Is(err, context.Canceled) || len(objects.locations) != 0 {
		t.Fatalf("cancellation performed evidence I/O: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	objects = &projectionObjects{payload: projectionPayload(t, record), onOpen: cancel}
	if _, err := LoadOperationCostSteps(ctx, cr, objects, "store", 1<<20); !errors.Is(err, context.Canceled) || !objects.closed {
		t.Fatalf("cancellation while reading lost or body not closed: %v", err)
	}
	cr.Status.Steps = nil
	steps, err := LoadOperationCostSteps(context.Background(), cr, nil, "", 0)
	if err != nil || len(steps) != 0 {
		t.Fatalf("unexecuted sequence: %v, %v", steps, err)
	}
	cr.Status.Steps = []edge.OperationCostStepStatus{{Name: "prepare"}}
	steps, err = LoadOperationCostSteps(context.Background(), cr, nil, "", 0)
	if err != nil || len(steps) != 1 || steps[0].DurationNanos != nil || steps[0].WallJoules != nil {
		t.Fatalf("absent reference invented evidence: %+v, %v", steps, err)
	}
}

func TestOperationProjectionRetainsOnlyRecordedDirectionAndModel(t *testing.T) {
	cr, record := projectionRecord(t)
	record.Verdict, cr.Status.Steps[0].Verdict = servingops.MeasurementInsufficient, servingops.MeasurementInsufficient
	record.Result.Before.PowerMode, record.Result.Actual.PowerMode = "MAXN", "MODE_15W"
	record.Request.Operation.Prepare = &servingstate.PrepareSpec{Instance: serving.SchedulerInstance{Profile: &serving.Profile{}}}
	record.Request.Operation.Prepare.Instance.Profile.Condition.LogicalModel = "frozen-model"
	got, err := projectOperationStep(record)
	if err != nil {
		t.Fatal(err)
	}
	if got.FromPowerMode != "MAXN" || got.ToPowerMode != "MODE_15W" || got.Model != "frozen-model" || got.WallJoules != nil {
		t.Fatalf("lost recorded metadata or invented accepted energy: %+v", got)
	}
	record.Request.Operation.Prepare = nil
	record.Result = servingstate.OperationResult{}
	got, err = projectOperationStep(record)
	if err != nil || got.FromPowerMode != "" || got.ToPowerMode != "" || got.Model != "" {
		t.Fatalf("missing metadata inferred from plan or name: %+v, %v", got, err)
	}
}

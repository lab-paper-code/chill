package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const projectionForeignIdentity = "foreign-operation-identity"

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

func projectionRecord(t *testing.T) (edge.OperationCostMeasurement, OperationMeasurement) {
	t.Helper()
	key, raw := costTestRecord(t, "cr-uid.0.prepare", "fixed", time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))
	var record OperationMeasurement
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	cr := edge.OperationCostMeasurement{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "cost", UID: "cr-uid"},
		Spec: edge.OperationCostMeasurementSpec{Node: record.Request.Operation.Node.Name, Steps: []edge.OperationCostStep{
			{Name: "prepare", Kind: edge.OperationCostStepPrepare},
			{Name: "not-executed", Kind: edge.OperationCostStepRemove},
		}},
		Status: edge.OperationCostMeasurementStatus{Phase: edge.MeasurementPhaseFailed, Reason: "sequence stopped", Steps: []edge.OperationCostStepStatus{{Name: "prepare", Key: key, Verdict: measurementAccepted}}},
	}
	return cr, record
}

func projectionPayload(t *testing.T, record OperationMeasurement) []byte {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLoadOperationCostStepsActualHistoryAndIdle(t *testing.T) {
	for _, name := range []string{"accepted", "zero", nativeOperationFailed, "insufficient", "isolation", "not-invoked", "idle"} {
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
			case nativeOperationFailed:
				record.Verdict, record.Reason, record.Result.Status = operationFailed, "physical action failed", nativeOperationFailed
				// An unfinished/failed power window must not be given Accepted's
				// completeness checks, nor expose a leftover energy value.
				record.Power.Samples = nil
				wantJ = false
			case "insufficient":
				record.Verdict, record.Reason = measurementInsufficient, "power window insufficient"
				record.Power.Samples = nil
				wantJ = false
			case "isolation":
				_ = record.applyIsolation(MeasurementIsolation{Node: record.Request.Operation.Node}, errors.New("isolation unconfirmed"))
				wantJ = false
			case "not-invoked":
				record.Verdict, record.Reason, record.Result = measurementInsufficient, "no first sample", OperationResult{}
				wantJ = false
			case "idle":
				cr.Spec.Steps[0].Kind = edge.OperationCostStepIdle
				planned := int32(99)
				cr.Spec.Steps[0].IdleDurationSeconds = &planned
				record.Request.Operation.Kind, record.Result.Operation.Kind = operationIdle, operationIdle
				record.Result.Before = record.Result.Actual
				record.StateObservations = []OperationState{record.Result.Before, record.Result.Actual}
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

func TestLoadOperationCostStepsRejectsForeignAndCorruptEvidence(t *testing.T) {
	for _, name := range []string{"foreign-uid", "index", "name", "kind", "foreign-node", "node-uid", "key", "store", "result-id", "result-kind", "result-node", "schema", "verdict", "cost", "idle", "corrupt", "bound"} {
		t.Run(name, func(t *testing.T) {
			cr, record := projectionRecord(t)
			switch name {
			case "foreign-uid":
				cr.UID = projectionForeignIdentity
			case "index":
				record.Request.Operation.ID = "cr-uid.1.prepare"
			case "name":
				cr.Status.Steps[0].Name = projectionForeignIdentity
			case "kind":
				record.Request.Operation.Kind = operationRemove
			case "foreign-node":
				record.Request.Operation.Node.Name = projectionForeignIdentity
			case "node-uid":
				record.Request.Operation.Node.UID = projectionForeignIdentity
			case "key":
				cr.Status.Steps[0].Key += ".other"
			case "store":
				record.Request.Store = projectionForeignIdentity
			case "result-id":
				record.Result.Operation.ID = projectionForeignIdentity
			case "result-kind":
				record.Result.Operation.Kind = operationIdle
			case "result-node":
				record.Result.Operation.Node.UID = projectionForeignIdentity
			case "schema":
				record.SchemaVersion = "unsupported-projection-schema"
			case "verdict":
				cr.Status.Steps[0].Verdict = operationFailed
			case "cost":
				record.WallJoules = nil
			case "idle":
				cr.Spec.Steps[0].Kind = edge.OperationCostStepIdle
				record.Request.Operation.Kind, record.Result.Operation.Kind = operationIdle, operationIdle
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
				t.Fatal("accepted foreign/corrupt evidence")
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

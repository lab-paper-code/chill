package main

import (
	"context"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func coverageOccupancyFixture(t *testing.T) (
	*measurementCoverageBase, queryapi.MeasurementCoverageCell, api.DispatchOccupancyMeasurement,
) {
	t.Helper()
	base := coverageSizeBase()
	initial := buildMeasurementCoverage(base, coverageSizeNamespace, httpapi.MeasurementCoverageQuery{})
	var selected queryapi.MeasurementCoverageCell
	for _, cell := range initial.Cells {
		if cell.Occupancy == nil || len(cell.Occupancy) != 0 {
			t.Fatal("request-free coverage invented node/batch occupancy tuples")
		}
		if selected.ID == "" && len(cell.Variants) > 0 && len(cell.Variants[0].ProfileIDs) > 0 {
			selected = cell
		}
	}
	if selected.ID == "" {
		t.Fatal("current profile cell missing")
	}
	task := api.DispatchOccupancyMeasurement{
		ObjectMeta: meta.ObjectMeta{
			Name: "occupancy", UID: "occupancy-uid", CreationTimestamp: meta.NewTime(time.Unix(1, 0)),
		},
		Spec: api.DispatchOccupancyMeasurementSpec{
			Node: coverageSizeNode, BatchEnergyOptimization: strings.Split(selected.CurrentMeasurement.ID, "/")[1],
			Batch: int32(selected.Variants[0].BatchProgress.Target[0]),
		},
	}
	task.Spec.Conditions.ProfileID = selected.Variants[0].ProfileIDs[0]
	base.occupancyResults = map[string]dispatchOccupancyProjection{}
	return base, selected, task
}

func findCoverageOccupancy(
	t *testing.T, base *measurementCoverageBase, selected queryapi.MeasurementCoverageCell,
	task api.DispatchOccupancyMeasurement,
) queryapi.MeasurementOccupancy {
	t.Helper()
	got := buildMeasurementCoverage(base, coverageSizeNamespace, httpapi.MeasurementCoverageQuery{})
	for _, cell := range got.Cells {
		if cell.ID != selected.ID {
			continue
		}
		for _, entry := range cell.Occupancy {
			if entry.ProfileID == task.Spec.Conditions.ProfileID && entry.Batch == int(task.Spec.Batch) {
				return entry
			}
		}
	}
	t.Fatal("occupancy tuple missing")
	return queryapi.MeasurementOccupancy{}
}

func acceptCoverageOccupancy(base *measurementCoverageBase, task api.DispatchOccupancyMeasurement) int64 {
	progress := occupancyProgress(task)
	d := int64(-9223372036854775807)
	progress.D = &queryapi.OccupancyValue{PerRequestNanos: &d, Verdict: measurementVerdictAccepted}
	progress.Evidence = &queryapi.OccupancyEvidence{Store: "evidence", Key: "accepted.json", Size: 123}
	record := &servingkube.DispatchOccupancyRecord{
		Node:    serving.NodeReference{Name: coverageSizeNode, UID: coverageSizeNodeUID},
		Verdict: measurementVerdictAccepted, ProfileID: task.Spec.Conditions.ProfileID,
	}
	base.occupancyResults[string(task.UID)] = dispatchOccupancyProjection{
		DispatchOccupancyProgress: progress, record: record,
	}
	return d
}

func TestCoverageOccupancyExecutionStates(t *testing.T) {
	base, selected, task := coverageOccupancyFixture(t)
	for _, tc := range []struct {
		phase api.MeasurementPhase
		state string
	}{
		{"", occupancyStateMeasuring}, {api.MeasurementPhaseMeasuring, occupancyStateMeasuring},
		{api.MeasurementPhaseFailed, occupancyStateFailed}, {api.MeasurementPhaseInsufficient, occupancyStateFailed},
		{api.MeasurementPhaseComplete, occupancyStateFailed},
	} {
		task.Status.Phase = tc.phase
		base.occupancies.Items = []api.DispatchOccupancyMeasurement{task}
		entry := findCoverageOccupancy(t, base, selected, task)
		if entry.State != tc.state || entry.Request == nil || entry.Request.UID != string(task.UID) {
			t.Fatalf("phase %s became %+v", tc.phase, entry)
		}
		if tc.phase == api.MeasurementPhaseComplete && entry.Reason != occupancyEvidenceInvalid {
			t.Fatalf("unverified Complete lacked reason: %+v", entry)
		}
	}
}

func TestCoverageOccupancyAcceptedPrecedence(t *testing.T) {
	base, selected, task := coverageOccupancyFixture(t)
	task.Status.Phase = api.MeasurementPhaseMeasuring
	failed := task.DeepCopy()
	failed.Name, failed.UID = "failed-latest", "failed-latest-uid"
	failed.CreationTimestamp = meta.NewTime(time.Unix(3, 0))
	failed.Status.Phase = api.MeasurementPhaseFailed
	base.occupancies.Items = []api.DispatchOccupancyMeasurement{*failed, task}
	if entry := findCoverageOccupancy(t, base, selected, task); entry.State != occupancyStateMeasuring {
		t.Fatalf("newer failure hid measuring: %+v", entry)
	}
	task.Status.Phase = api.MeasurementPhaseComplete
	d := acceptCoverageOccupancy(base, task)
	base.occupancies.Items = []api.DispatchOccupancyMeasurement{*failed, task}
	entry := findCoverageOccupancy(t, base, selected, task)
	if entry.State != occupancyStatePresent || *entry.D.PerRequestNanos != d ||
		entry.Request.Name != task.Name || entry.Adopted != nil || entry.Node.UID != coverageSizeNodeUID {
		t.Fatalf("newer failure hid current accepted evidence: %+v", entry)
	}
	// A retained accepted record for a previous physical node is not current.
	base.occupancyResults[string(task.UID)].record.Node.UID = "replaced-node-uid"
	if entry := findCoverageOccupancy(t, base, selected, task); entry.State == occupancyStatePresent {
		t.Fatal("replaced physical node became current")
	}
}

func TestCoverageOccupancyHistoricalTupleRetainsEvidence(t *testing.T) {
	base, selected, task := coverageOccupancyFixture(t)
	task.Spec.Conditions.ProfileID = "previous-profile"
	task.Status.Phase = api.MeasurementPhaseComplete
	acceptCoverageOccupancy(base, task)
	base.occupancies.Items = []api.DispatchOccupancyMeasurement{task}
	entry := findCoverageOccupancy(t, base, selected, task)
	if entry.State != "none" || entry.Reason != "ProfileSuperseded" ||
		entry.Request == nil || entry.D == nil || entry.Evidence == nil {
		t.Fatalf("historical tuple lost raw evidence or became present: %+v", entry)
	}
}

func TestCoverageOccupancyEqualPriorityUsesNewestCreatedAt(t *testing.T) {
	base, selected, task := coverageOccupancyFixture(t)
	for _, phase := range []api.MeasurementPhase{
		api.MeasurementPhaseMeasuring, api.MeasurementPhaseFailed, api.MeasurementPhaseComplete,
	} {
		task.Status.Phase = phase
		newer := task.DeepCopy()
		newer.Name, newer.UID = "newer", "newer-uid"
		newer.CreationTimestamp = meta.NewTime(time.Unix(2, 0))
		if phase == api.MeasurementPhaseComplete {
			acceptCoverageOccupancy(base, task)
			acceptCoverageOccupancy(base, *newer)
		}
		for _, tasks := range [][]api.DispatchOccupancyMeasurement{{task, *newer}, {*newer, task}} {
			base.occupancies.Items = tasks
			entry := findCoverageOccupancy(t, base, selected, task)
			if entry.Request == nil || entry.Request.UID != string(newer.UID) {
				t.Fatalf("phase %s selected older equal-priority request: %+v", phase, entry)
			}
		}
	}
}

func TestCoverageOccupancyInvalidEvidenceReason(t *testing.T) {
	for _, payload := range []string{"not-json", "{}"} {
		t.Run(payload, func(t *testing.T) {
			base, selected, task := coverageOccupancyFixture(t)
			task.Status.Phase = api.MeasurementPhaseComplete
			task.Status.Result = &api.ProfilingRunEvidenceReference{Store: "evidence", Key: "invalid.json"}
			service := &measurementService{
				objects: &operationProjectionStore{payload: []byte(payload)},
				opts:    options{maxEvidenceBytes: 1 << 20},
			}
			projection, err := service.projectDispatchOccupancy(context.Background(), task)
			if err != nil {
				t.Fatal(err)
			}
			base.occupancyResults[string(task.UID)] = projection
			base.occupancies.Items = []api.DispatchOccupancyMeasurement{task}
			entry := findCoverageOccupancy(t, base, selected, task)
			if entry.State != occupancyStateFailed || entry.Reason != occupancyEvidenceInvalid {
				t.Fatalf("invalid evidence became %+v", entry)
			}
		})
	}
}

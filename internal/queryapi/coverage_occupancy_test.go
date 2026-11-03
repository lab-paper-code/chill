package queryapi

import (
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func coverageOccupancyFixture(t *testing.T) (
	*CoverageBase, MeasurementCoverageCell, api.DispatchOccupancyMeasurement,
) {
	t.Helper()
	base := coverageSizeBase()
	initial := BuildMeasurementCoverage(base, coverageSizeNamespace, CoverageQuery{})
	var selected MeasurementCoverageCell
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
	base.OccupancyResults = map[string]OccupancyEvidenceProjection{}
	return base, selected, task
}

func findCoverageOccupancy(
	t *testing.T, base *CoverageBase, selected MeasurementCoverageCell,
	task api.DispatchOccupancyMeasurement,
) MeasurementOccupancy {
	t.Helper()
	got := BuildMeasurementCoverage(base, coverageSizeNamespace, CoverageQuery{})
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
	return MeasurementOccupancy{}
}

func acceptCoverageOccupancy(base *CoverageBase, task api.DispatchOccupancyMeasurement) int64 {
	progress := OccupancyProgress(task)
	d := int64(-9223372036854775807)
	progress.D = &OccupancyValue{PerRequestNanos: &d, Verdict: MeasurementVerdictAccepted}
	progress.Evidence = &OccupancyEvidence{Store: "evidence", Key: "accepted.json", Size: 123}
	record := &OccupancyRecordFacts{
		Node:    MeasurementIdentity{Name: coverageSizeNode, UID: coverageSizeNodeUID},
		Verdict: MeasurementVerdictAccepted, ProfileID: task.Spec.Conditions.ProfileID,
	}
	base.OccupancyResults[string(task.UID)] = OccupancyEvidenceProjection{
		DispatchOccupancyProgress: progress, Record: record,
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
		base.Occupancies.Items = []api.DispatchOccupancyMeasurement{task}
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
	base.Occupancies.Items = []api.DispatchOccupancyMeasurement{*failed, task}
	if entry := findCoverageOccupancy(t, base, selected, task); entry.State != occupancyStateMeasuring {
		t.Fatalf("newer failure hid measuring: %+v", entry)
	}
	task.Status.Phase = api.MeasurementPhaseComplete
	d := acceptCoverageOccupancy(base, task)
	base.Occupancies.Items = []api.DispatchOccupancyMeasurement{*failed, task}
	entry := findCoverageOccupancy(t, base, selected, task)
	if entry.State != occupancyStatePresent || *entry.D.PerRequestNanos != d ||
		entry.Request.Name != task.Name || entry.Adopted != nil || entry.Node.UID != coverageSizeNodeUID {
		t.Fatalf("newer failure hid current accepted evidence: %+v", entry)
	}
	// A retained accepted record for a previous physical node is not current.
	base.OccupancyResults[string(task.UID)].Record.Node.UID = "replaced-node-uid"
	if entry := findCoverageOccupancy(t, base, selected, task); entry.State == occupancyStatePresent {
		t.Fatal("replaced physical node became current")
	}
}

func TestCoverageOccupancyHistoricalTupleRetainsEvidence(t *testing.T) {
	base, selected, task := coverageOccupancyFixture(t)
	task.Spec.Conditions.ProfileID = "previous-profile"
	task.Status.Phase = api.MeasurementPhaseComplete
	acceptCoverageOccupancy(base, task)
	base.Occupancies.Items = []api.DispatchOccupancyMeasurement{task}
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
			base.Occupancies.Items = tasks
			entry := findCoverageOccupancy(t, base, selected, task)
			if entry.Request == nil || entry.Request.UID != string(newer.UID) {
				t.Fatalf("phase %s selected older equal-priority request: %+v", phase, entry)
			}
		}
	}
}

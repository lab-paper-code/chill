package queryapi

import (
	"testing"
	"time"
)

const occupancyOtherIdentity = "other"

func TestW8CoverageWithoutDOM(t *testing.T) {
	base, cell, task := coverageOccupancyFixture(t)
	at := time.Now().UTC()
	estimate := OccupancyEstimate{
		Node:  MeasurementIdentity{Name: coverageSizeNode, UID: coverageSizeNodeUID},
		Model: cell.Model.Name, PowerMode: cell.PowerMode, ExecutionPath: cell.ExecutionPath,
		Batch: int(task.Spec.Batch), Profile: OccupancyProfile{ProfileID: task.Spec.Conditions.ProfileID},
		Pairs: 9, Statistics: &OccupancyStatistics{Median: -42, Mean: -20},
		Window: OccupancyMeasurementWindow{Start: at.Add(-time.Minute), End: at},
	}
	base.AcceptedOccupancy = []CoverageSelectedOccupancy{{
		Node: estimate.Node, Model: estimate.Model, PowerMode: estimate.PowerMode,
		ExecutionPath: estimate.ExecutionPath, ProfileID: estimate.Profile.ProfileID, Batch: estimate.Batch,
		Provenance: &SchedulerOccupancy{Source: "default", Value: 2000},
	}}
	for _, tc := range []struct {
		name    string
		change  func(*OccupancyEstimate)
		present bool
	}{
		{name: "signed observation and default coexist", present: true},
		{name: "runtime image display only", present: true, change: func(e *OccupancyEstimate) {
			e.RuntimeImage = "different"
		}},
		{name: "wrong physical node", change: func(e *OccupancyEstimate) { e.Node.UID = "replaced" }},
		{name: "wrong model", change: func(e *OccupancyEstimate) { e.Model = occupancyOtherIdentity }},
		{name: "wrong mode", change: func(e *OccupancyEstimate) { e.PowerMode = occupancyOtherIdentity }},
		{name: "wrong execution path", change: func(e *OccupancyEstimate) {
			e.ExecutionPath = occupancyOtherIdentity
		}},
		{name: "wrong profile", change: func(e *OccupancyEstimate) { e.Profile.ProfileID = "old" }},
		{name: "wrong batch", change: func(e *OccupancyEstimate) { e.Batch++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := estimate
			if tc.change != nil {
				tc.change(&observed)
			}
			base.ObservedOccupancy = []OccupancyEstimate{observed}
			entry := findCoverageOccupancy(t, base, cell, task)
			if entry.Request != nil || entry.D != nil || entry.SchedulerD == nil ||
				entry.SchedulerD.Value != 2000 || entry.Adopted == nil || *entry.Adopted {
				t.Fatalf("no-DOM tuple invented a request, scalar or adoption: %+v", entry)
			}
			if (entry.ObservedD != nil) != tc.present {
				t.Fatalf("observed identity mismatch: %+v", entry)
			}
			if entry.ObservedD != nil && (entry.ObservedD.Statistics.Median != -42 || entry.ObservedD.Pairs != 9) {
				t.Fatalf("signed observation changed: %+v", entry.ObservedD)
			}
		})
	}
}

package kube

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/queryapi"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const occupancyOtherIdentity = "other"

// Exercise the actual private catalog decoder and refresh while retaining the DOM fixture.
func readCoverageAdoptionFixture(
	t *testing.T, previous *queryapi.CoverageBase,
	read func(context.Context) (queryapi.CoverageCatalogRead, error),
) (*queryapi.CoverageBase, error) {
	t.Helper()
	service := &CoverageService{
		Reader: fake.NewClientBuilder().WithScheme(inventoryScheme(t)).Build(), Catalog: coverageCatalogFunc(read),
		Inventory: func(context.Context) (queryapi.InventoryView, error) { return previous.Inventory, nil },
		Compatibility: func(context.Context) (queryapi.ModelCompatibilityMatrixView, error) {
			return previous.Compatibility, nil
		},
	}
	next, err := service.readCoverage(context.Background(), coverageSizeNamespace, previous)
	if next != nil {
		next.Requests, next.Runs, next.Catalog = previous.Requests, previous.Runs, previous.Catalog
		next.Occupancies, next.OccupancyResults = previous.Occupancies, previous.OccupancyResults
	}
	return next, err
}

func TestCoverageOccupancyAdoption(t *testing.T) {
	fixture, cell, task := coverageOccupancyFixture(t)
	acceptCoverageOccupancy(fixture, task)
	fixture.Occupancies.Items = append(fixture.Occupancies.Items, task)
	at := time.Now().UTC()
	window := queryapi.OccupancyMeasurementWindow{Start: at.Add(-time.Minute), End: at}
	selection := queryapi.CoverageSelectedOccupancy{
		Node:  fixture.OccupancyResults[string(task.UID)].Record.Node,
		Model: cell.Model.Name, PowerMode: cell.PowerMode, ExecutionPath: cell.ExecutionPath,
		ProfileID: task.Spec.Conditions.ProfileID, Batch: int(task.Spec.Batch),
		Provenance: &queryapi.SchedulerOccupancy{
			Source: "measured", Value: 0, Pairs: 12, Window: &window, UpdatedAt: at,
		},
	}
	observed := queryapi.OccupancyEstimate{Node: selection.Node, Model: selection.Model, PowerMode: selection.PowerMode,
		ExecutionPath: selection.ExecutionPath, RuntimeImage: "display-only", Batch: selection.Batch,
		Profile: queryapi.OccupancyProfile{ProfileID: selection.ProfileID}, Pairs: 12, Window: window,
		Statistics: &queryapi.OccupancyStatistics{Median: -20, Mean: -10, NegativeFraction: .75}}
	for _, tc := range []struct {
		name     string
		change   func(*queryapi.CoverageCatalogRead)
		want     string
		selected bool
	}{
		{name: "measured", want: "true", selected: true},
		{name: "dom provenance", want: "true", selected: true, change: func(r *queryapi.CoverageCatalogRead) {
			r.AcceptedOccupancy[0].Provenance.Source = "dom"
		}},
		{name: "default is not adoption", want: "false", selected: true, change: func(r *queryapi.CoverageCatalogRead) {
			r.AcceptedOccupancy[0].Provenance.Source = "default"
		}},
		{name: "runtime image display only", want: "true", selected: true, change: func(r *queryapi.CoverageCatalogRead) {
			r.ObservedOccupancy[0].RuntimeImage = occupancyOtherIdentity
		}},
		{name: "missing provenance", want: "null", change: func(r *queryapi.CoverageCatalogRead) {
			r.AcceptedOccupancy[0].Provenance = nil
		}},
		{name: "empty selections", want: "false", change: func(r *queryapi.CoverageCatalogRead) {
			r.AcceptedOccupancy = []queryapi.CoverageSelectedOccupancy{}
		}},
		{name: "missing selections", want: "null", change: func(r *queryapi.CoverageCatalogRead) {
			r.AcceptedOccupancy = nil
		}},
		{name: "accepted absent", want: "null", change: func(r *queryapi.CoverageCatalogRead) { r.Accepted = nil }},
		{name: "different node name", want: "false", change: func(r *queryapi.CoverageCatalogRead) {
			r.AcceptedOccupancy[0].Node.Name = occupancyOtherIdentity
		}},
		{name: "different node UID", want: "false", change: func(r *queryapi.CoverageCatalogRead) {
			r.AcceptedOccupancy[0].Node.UID = "replaced"
		}},
		{name: "different model", want: "false", change: func(r *queryapi.CoverageCatalogRead) {
			r.AcceptedOccupancy[0].Model = occupancyOtherIdentity
		}},
		{name: "different mode", want: "false", change: func(r *queryapi.CoverageCatalogRead) {
			r.AcceptedOccupancy[0].PowerMode = occupancyOtherIdentity
		}},
		{name: "different path", want: "false", change: func(r *queryapi.CoverageCatalogRead) {
			r.AcceptedOccupancy[0].ExecutionPath = occupancyOtherIdentity
		}},
		{name: "different profile", want: "false", change: func(r *queryapi.CoverageCatalogRead) {
			r.AcceptedOccupancy[0].ProfileID = occupancyOtherIdentity
		}},
		{name: "different batch", want: "false", change: func(r *queryapi.CoverageCatalogRead) {
			r.AcceptedOccupancy[0].Batch++
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copySelection := selection
			copyProvenance := *selection.Provenance
			copySelection.Provenance = &copyProvenance
			read := queryapi.CoverageCatalogRead{
				SourceCheckedAt: at, Accepted: &queryapi.CoverageCatalog{},
				AcceptedOccupancy: []queryapi.CoverageSelectedOccupancy{copySelection},
				ObservedOccupancy: []queryapi.OccupancyEstimate{observed},
			}
			if tc.change != nil {
				tc.change(&read)
			}
			wire, err := json.Marshal(read)
			if err != nil {
				t.Fatal(err)
			}
			next, err := readCoverageAdoptionFixture(
				t,
				fixture,
				func(context.Context) (queryapi.CoverageCatalogRead, error) {
					var decoded queryapi.CoverageCatalogRead
					err := json.Unmarshal(wire, &decoded)
					return decoded, err
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			entry := findCoverageOccupancy(t, next, cell, task)
			adopted, err := json.Marshal(entry.Adopted)
			if err != nil || string(adopted) != tc.want {
				t.Fatalf("adopted=%s want=%s error=%v", adopted, tc.want, err)
			}
			if (entry.SchedulerD != nil) != tc.selected {
				t.Fatalf("wrong selected provenance: %+v", entry)
			}
			if entry.SchedulerD != nil && (entry.SchedulerD.Value != 0 || entry.SchedulerD.Pairs != 12 ||
				entry.SchedulerD.Window == nil || !entry.SchedulerD.UpdatedAt.Equal(at)) {
				t.Fatalf("provenance lost: %+v", entry.SchedulerD)
			}
			if read.Accepted != nil && (entry.ObservedD == nil ||
				entry.ObservedD.Statistics.Median != -20 || entry.ObservedD.Pairs != 12) {
				t.Fatalf("observed signed facts conflated with scheduler value: %+v", entry)
			}
		})
	}
}

func TestCoverageOccupancyAdoptionRefresh(t *testing.T) {
	fixture, cell, task := coverageOccupancyFixture(t)
	acceptCoverageOccupancy(fixture, task)
	fixture.Occupancies.Items = append(fixture.Occupancies.Items, task)
	checked := time.Now().UTC().Add(-time.Second)
	accepted := []queryapi.CoverageSelectedOccupancy{{
		Node: fixture.OccupancyResults[string(task.UID)].Record.Node, Model: cell.Model.Name,
		PowerMode: cell.PowerMode, ExecutionPath: cell.ExecutionPath, ProfileID: task.Spec.Conditions.ProfileID,
		Batch: int(task.Spec.Batch), Provenance: &queryapi.SchedulerOccupancy{Source: "dom", Value: 10},
	}}
	read := queryapi.CoverageCatalogRead{
		SourceCheckedAt: checked, Accepted: &queryapi.CoverageCatalog{}, AcceptedOccupancy: accepted,
	}
	previous, err := readCoverageAdoptionFixture(
		t,
		fixture,
		func(context.Context) (queryapi.CoverageCatalogRead, error) {
			return read, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if entry := findCoverageOccupancy(t, previous, cell, task); entry.Adopted == nil || !*entry.Adopted {
		t.Fatalf("initial selection missing: %+v", entry)
	}
	for _, tc := range []struct {
		name string
		read queryapi.CoverageCatalogRead
		err  error
		want string
	}{
		{name: "source failure", read: queryapi.CoverageCatalogRead{
			SourceCheckedAt: checked, Reporting: queryapi.CoverageCatalog{SourceError: "unavailable"},
			Accepted: &queryapi.CoverageCatalog{}, AcceptedOccupancy: accepted,
		}, want: "null"},
		{name: "transport failure", err: errors.New("transport unavailable"), want: "null"},
		{name: "unconfirmed", read: queryapi.CoverageCatalogRead{Accepted: &queryapi.CoverageCatalog{}, AcceptedOccupancy: accepted}, want: "null"},
		{name: "accepted missing", read: queryapi.CoverageCatalogRead{
			SourceCheckedAt: checked,
		}, want: "null"},
		{name: "selection missing", read: queryapi.CoverageCatalogRead{
			SourceCheckedAt: checked,
			Accepted:        &queryapi.CoverageCatalog{},
		}, want: "null"},
		{name: "selection removed", read: queryapi.CoverageCatalogRead{
			SourceCheckedAt: checked,
			Accepted:        &queryapi.CoverageCatalog{}, AcceptedOccupancy: []queryapi.CoverageSelectedOccupancy{},
		}, want: "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, err := readCoverageAdoptionFixture(
				t,
				previous,
				func(context.Context) (queryapi.CoverageCatalogRead, error) {
					return tc.read, tc.err
				},
			)
			failed := tc.err != nil || tc.read.Reporting.SourceError != "" || tc.read.SourceCheckedAt.IsZero()
			if next == nil || (err != nil) != failed {
				t.Fatalf("snapshot=%v error=%v", next, err)
			}
			entry := findCoverageOccupancy(t, next, cell, task)
			adopted, err := json.Marshal(entry.Adopted)
			if err != nil || string(adopted) != tc.want || entry.SchedulerD != nil {
				t.Fatalf("stale selection reused: %+v (%v)", entry, err)
			}
			view := queryapi.BuildMeasurementCoverage(next, coverageSizeNamespace, queryapi.CoverageQuery{})
			if (view.Freshness == queryapi.CoverageFreshnessStale) != failed ||
				!next.Checked[queryapi.CoverageCatalogSource].Equal(checked) {
				t.Fatalf("source freshness changed: %+v", view)
			}
		})
	}
}

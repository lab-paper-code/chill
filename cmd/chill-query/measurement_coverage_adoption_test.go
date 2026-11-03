package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const occupancyOtherIdentity = "other"

// Exercise the actual private catalog decoder and refresh while retaining the DOM fixture.
func readCoverageAdoptionFixture(
	t *testing.T, previous *measurementCoverageBase,
	read func(context.Context) (servingkube.CatalogRead, error),
) (*measurementCoverageBase, error) {
	t.Helper()
	service := &measurementService{
		client: fake.NewClientBuilder().WithScheme(measurementScheme(t)).Build(), catalog: read,
		inventory: func(context.Context) (queryapi.InventoryView, error) { return previous.inventory, nil },
		compatibility: func(context.Context) (queryapi.ModelCompatibilityMatrixView, error) {
			return previous.compatibility, nil
		},
	}
	next, err := service.readCoverage(context.Background(), coverageSizeNamespace, previous)
	if next != nil {
		next.requests, next.runs, next.catalog = previous.requests, previous.runs, previous.catalog
		next.occupancies, next.occupancyResults = previous.occupancies, previous.occupancyResults
	}
	return next, err
}

func TestCoverageOccupancyAdoption(t *testing.T) {
	fixture, cell, task := coverageOccupancyFixture(t)
	acceptCoverageOccupancy(fixture, task)
	fixture.occupancies.Items = append(fixture.occupancies.Items, task)
	at := time.Now().UTC()
	window := serving.OccupancyWindow{Start: at.Add(-time.Minute), End: at}
	selection := serving.DispatchOccupancy{
		Node:  fixture.occupancyResults[string(task.UID)].record.Node,
		Model: cell.Model.Name, PowerMode: cell.PowerMode, ExecutionPath: cell.ExecutionPath,
		ProfileID: task.Spec.Conditions.ProfileID, Batch: int(task.Spec.Batch), RuntimeImage: "selected-image",
		Provenance: &serving.DispatchOccupancyProvenance{
			Source: "measured", Value: 0, Pairs: 12, Window: &window, UpdatedAt: at,
		},
	}
	observed := serving.OccupancyEstimate{Node: selection.Node, Model: selection.Model, PowerMode: selection.PowerMode,
		ExecutionPath: selection.ExecutionPath, RuntimeImage: "display-only", Batch: selection.Batch,
		Profile: serving.OccupancyProfile{ProfileID: selection.ProfileID}, Pairs: 12, Window: window,
		Statistics: &serving.OccupancyStatistics{Median: -20, Mean: -10, NegativeFraction: .75}}
	for _, tc := range []struct {
		name     string
		change   func(*servingkube.CatalogRead)
		want     string
		selected bool
	}{
		{name: "measured", want: "true", selected: true},
		{name: "dom provenance", want: "true", selected: true, change: func(r *servingkube.CatalogRead) {
			r.Accepted.DispatchOccupancy[0].Provenance.Source = "dom"
		}},
		{name: "default is not adoption", want: "false", selected: true, change: func(r *servingkube.CatalogRead) {
			r.Accepted.DispatchOccupancy[0].Provenance.Source = "default"
		}},
		{name: "runtime image display only", want: "true", selected: true, change: func(r *servingkube.CatalogRead) {
			r.Accepted.DispatchOccupancy[0].RuntimeImage = occupancyOtherIdentity
		}},
		{name: "missing provenance", want: "null", change: func(r *servingkube.CatalogRead) {
			r.Accepted.DispatchOccupancy[0].Provenance = nil
		}},
		{name: "empty selections", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.DispatchOccupancy = []serving.DispatchOccupancy{}
		}},
		{name: "missing selections", want: "null", change: func(r *servingkube.CatalogRead) {
			r.Accepted.DispatchOccupancy = nil
		}},
		{name: "accepted absent", want: "null", change: func(r *servingkube.CatalogRead) { r.Accepted = nil }},
		{name: "different node name", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.DispatchOccupancy[0].Node.Name = occupancyOtherIdentity
		}},
		{name: "different node UID", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.DispatchOccupancy[0].Node.UID = "replaced"
		}},
		{name: "different model", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.DispatchOccupancy[0].Model = occupancyOtherIdentity
		}},
		{name: "different mode", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.DispatchOccupancy[0].PowerMode = occupancyOtherIdentity
		}},
		{name: "different path", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.DispatchOccupancy[0].ExecutionPath = occupancyOtherIdentity
		}},
		{name: "different profile", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.DispatchOccupancy[0].ProfileID = occupancyOtherIdentity
		}},
		{name: "different batch", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.DispatchOccupancy[0].Batch++
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copySelection := selection
			copyProvenance := *selection.Provenance
			copySelection.Provenance = &copyProvenance
			read := servingkube.CatalogRead{
				CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: at},
				Accepted: &servingkube.CatalogSnapshot{
					DispatchOccupancy:  []serving.DispatchOccupancy{copySelection},
					OccupancyEstimates: []serving.OccupancyEstimate{observed},
				},
			}
			if tc.change != nil {
				tc.change(&read)
			}
			wire, err := json.Marshal(read)
			if err != nil {
				t.Fatal(err)
			}
			next, err := readCoverageAdoptionFixture(t, fixture, func(context.Context) (servingkube.CatalogRead, error) {
				var decoded servingkube.CatalogRead
				err := json.Unmarshal(wire, &decoded)
				return decoded, err
			})
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
	fixture.occupancies.Items = append(fixture.occupancies.Items, task)
	checked := time.Now().UTC().Add(-time.Second)
	accepted := &servingkube.CatalogSnapshot{DispatchOccupancy: []serving.DispatchOccupancy{{
		Node: fixture.occupancyResults[string(task.UID)].record.Node, Model: cell.Model.Name,
		PowerMode: cell.PowerMode, ExecutionPath: cell.ExecutionPath, ProfileID: task.Spec.Conditions.ProfileID,
		Batch: int(task.Spec.Batch), Provenance: &serving.DispatchOccupancyProvenance{Source: "dom", Value: 10},
	}}}
	read := servingkube.CatalogRead{
		CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: checked}, Accepted: accepted,
	}
	previous, err := readCoverageAdoptionFixture(t, fixture, func(context.Context) (servingkube.CatalogRead, error) {
		return read, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry := findCoverageOccupancy(t, previous, cell, task); entry.Adopted == nil || !*entry.Adopted {
		t.Fatalf("initial selection missing: %+v", entry)
	}
	for _, tc := range []struct {
		name string
		read servingkube.CatalogRead
		err  error
		want string
	}{
		{name: "source failure", read: servingkube.CatalogRead{
			CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: checked, SourceError: "unavailable"},
			Accepted:        accepted,
		}, want: "null"},
		{name: "transport failure", err: errors.New("transport unavailable"), want: "null"},
		{name: "unconfirmed", read: servingkube.CatalogRead{Accepted: accepted}, want: "null"},
		{name: "accepted missing", read: servingkube.CatalogRead{
			CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: checked},
		}, want: "null"},
		{name: "selection missing", read: servingkube.CatalogRead{
			CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: checked},
			Accepted:        &servingkube.CatalogSnapshot{},
		}, want: "null"},
		{name: "selection removed", read: servingkube.CatalogRead{
			CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: checked},
			Accepted:        &servingkube.CatalogSnapshot{DispatchOccupancy: []serving.DispatchOccupancy{}},
		}, want: "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, err := readCoverageAdoptionFixture(t, previous, func(context.Context) (servingkube.CatalogRead, error) {
				return tc.read, tc.err
			})
			failed := tc.err != nil || tc.read.SourceError != "" || tc.read.SourceCheckedAt.IsZero()
			if next == nil || (err != nil) != failed {
				t.Fatalf("snapshot=%v error=%v", next, err)
			}
			entry := findCoverageOccupancy(t, next, cell, task)
			adopted, err := json.Marshal(entry.Adopted)
			if err != nil || string(adopted) != tc.want || entry.SchedulerD != nil {
				t.Fatalf("stale selection reused: %+v (%v)", entry, err)
			}
			view := buildMeasurementCoverage(next, coverageSizeNamespace, httpapi.MeasurementCoverageQuery{})
			if (view.Freshness == coverageFreshnessStale) != failed || !next.checked[coverageCatalogSource].Equal(checked) {
				t.Fatalf("source freshness changed: %+v", view)
			}
		})
	}
}

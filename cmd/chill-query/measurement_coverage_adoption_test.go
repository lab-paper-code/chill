package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const occupancyAdoptionStore = "evidence"

// Keep the verified DOM fixture while exercising the real catalog refresh and
// private JSON decoder; adoption must not be inferred from the profile map.
func readCoverageAdoptionFixture(
	t *testing.T, previous *measurementCoverageBase,
	read func(context.Context) (servingkube.CatalogRead, error),
) (*measurementCoverageBase, error) {
	t.Helper()
	service := &measurementService{
		client:  fake.NewClientBuilder().WithScheme(measurementScheme(t)).Build(),
		catalog: read,
		inventory: func(context.Context) (queryapi.InventoryView, error) {
			return previous.inventory, nil
		},
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
	selection := servingkube.OccupancySelection{
		Node:  fixture.occupancyResults[string(task.UID)].record.Node,
		Model: cell.Model.Name, PowerMode: cell.PowerMode, ExecutionPath: cell.ExecutionPath,
		ProfileID: task.Spec.Conditions.ProfileID, Batch: int(task.Spec.Batch), RuntimeImage: "selected-image",
	}
	selection.Evidence.Store, selection.Evidence.Key = occupancyAdoptionStore, "accepted.json"
	for _, tc := range []struct {
		name   string
		change func(*servingkube.CatalogRead)
		wire   string
		fail   bool
		want   string
	}{
		{name: "exact selected tuple", want: "true"},
		{name: "runtime image is not a validity key", want: "true", change: func(r *servingkube.CatalogRead) {
			r.Accepted.Occupancy[0].RuntimeImage = "different-image"
		}},
		{name: "available empty selection", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.Occupancy = []servingkube.OccupancySelection{}
		}},
		{name: "different node name", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.Occupancy[0].Node.Name = "other-node"
		}},
		{name: "different node UID", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.Occupancy[0].Node.UID = "replaced-node"
		}},
		{name: "different profile", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.Occupancy[0].ProfileID = "other-profile"
		}},
		{name: "different batch", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.Occupancy[0].Batch++
		}},
		{name: "different evidence store", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.Occupancy[0].Evidence.Store = "other-store"
		}},
		{name: "different evidence key", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.Occupancy[0].Evidence.Key = "other.json"
		}},
		{name: "different model", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.Occupancy[0].Model = "other-model"
		}},
		{name: "different power mode", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.Occupancy[0].PowerMode = "other-mode"
		}},
		{name: "different execution path", want: "false", change: func(r *servingkube.CatalogRead) {
			r.Accepted.Occupancy[0].ExecutionPath = "other-path"
		}},
		{name: "accepted absent", wire: `{}`, want: "null"},
		{name: "accepted null", wire: `{"accepted":null}`, want: "null"},
		{name: "occupancy absent", wire: `{"accepted":{}}`, want: "null"},
		{name: "occupancy null", want: "null", change: func(r *servingkube.CatalogRead) {
			r.Accepted.Occupancy = nil
		}},
		{name: "catalog failure", fail: true, want: "null"},
		{name: "catalog source failure", want: "null", change: func(r *servingkube.CatalogRead) {
			r.SourceError = "catalog source unavailable"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := servingkube.CatalogRead{
				CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: time.Now().UTC()},
				Accepted:        &servingkube.CatalogSnapshot{Occupancy: []servingkube.OccupancySelection{selection}},
			}
			if tc.change != nil {
				tc.change(&read)
			}
			wire, err := json.Marshal(read)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wire != "" {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal([]byte(tc.wire), &fields); err != nil {
					t.Fatal(err)
				}
				fields["sourceCheckedAt"], err = json.Marshal(read.SourceCheckedAt)
				if err != nil {
					t.Fatal(err)
				}
				wire, err = json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
			}
			next, err := readCoverageAdoptionFixture(t, fixture, func(context.Context) (servingkube.CatalogRead, error) {
				if tc.fail {
					return servingkube.CatalogRead{}, errors.New("catalog transport unavailable")
				}
				var decoded servingkube.CatalogRead
				err := json.Unmarshal(wire, &decoded)
				if tc.name == "available empty selection" && (decoded.Accepted == nil || decoded.Accepted.Occupancy == nil) {
					t.Error("private catalog decoding lost present empty occupancy")
				}
				return decoded, err
			})
			if next == nil || (err != nil) != (tc.fail || read.SourceError != "") {
				t.Fatalf("catalog refresh returned snapshot=%v error=%v", next, err)
			}
			entry := findCoverageOccupancy(t, next, cell, task)
			got, err := json.Marshal(entry.Adopted)
			if err != nil || string(got) != tc.want {
				t.Errorf("adopted=%s, want %s (error=%v)", got, tc.want, err)
			}
		})
	}
}

func TestCoverageOccupancyAdoptionRefresh(t *testing.T) {
	fixture, cell, task := coverageOccupancyFixture(t)
	acceptCoverageOccupancy(fixture, task)
	fixture.occupancies.Items = append(fixture.occupancies.Items, task)
	selection := servingkube.OccupancySelection{
		Node:  fixture.occupancyResults[string(task.UID)].record.Node,
		Model: cell.Model.Name, PowerMode: cell.PowerMode, ExecutionPath: cell.ExecutionPath,
		ProfileID: task.Spec.Conditions.ProfileID, Batch: int(task.Spec.Batch),
	}
	selection.Evidence.Store, selection.Evidence.Key = occupancyAdoptionStore, "accepted.json"
	checked := time.Now().UTC().Add(-10 * time.Second)
	read := servingkube.CatalogRead{
		CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: checked, Revision: "unchanged"},
		Accepted:        &servingkube.CatalogSnapshot{Occupancy: []servingkube.OccupancySelection{selection}},
	}
	reader := func(context.Context) (servingkube.CatalogRead, error) { return read, nil }
	previous, err := readCoverageAdoptionFixture(t, fixture, reader)
	if err != nil || previous == nil {
		t.Fatalf("initial refresh: snapshot=%v error=%v", previous, err)
	}
	checked = time.Now().UTC().Add(-time.Second)
	read.SourceCheckedAt = checked
	refreshed, err := readCoverageAdoptionFixture(t, previous, reader)
	if err != nil || refreshed == nil || !refreshed.checked[coverageCatalogSource].Equal(checked) {
		t.Fatalf("unchanged successful catalog did not advance source check: snapshot=%v error=%v", refreshed, err)
	}
	if entry := findCoverageOccupancy(t, refreshed, cell, task); entry.Adopted == nil || !*entry.Adopted {
		t.Errorf("successful selected occupancy was not adopted: %+v", entry)
	}
	for _, tc := range []struct {
		name string
		read servingkube.CatalogRead
		err  error
		want string
	}{
		{name: "source failure", read: servingkube.CatalogRead{
			CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: checked, SourceError: "source unavailable"},
			Accepted:        read.Accepted,
		}, want: "null"},
		{name: "transport failure", err: errors.New("transport unavailable"), want: "null"},
		{name: "unconfirmed source", read: servingkube.CatalogRead{Accepted: read.Accepted}, want: "null"},
		{name: "accepted missing", read: servingkube.CatalogRead{
			CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: checked},
		}, want: "null"},
		{name: "occupancy missing", read: servingkube.CatalogRead{
			CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: checked}, Accepted: &servingkube.CatalogSnapshot{},
		}, want: "null"},
		{name: "selection removed", read: servingkube.CatalogRead{
			CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: checked},
			Accepted:        &servingkube.CatalogSnapshot{Occupancy: []servingkube.OccupancySelection{}},
		}, want: "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, err := readCoverageAdoptionFixture(t, refreshed, func(context.Context) (servingkube.CatalogRead, error) {
				return tc.read, tc.err
			})
			failed := tc.err != nil || tc.read.SourceError != "" || tc.read.SourceCheckedAt.IsZero()
			if next == nil || (err != nil) != failed {
				t.Fatalf("refresh returned snapshot=%v error=%v", next, err)
			}
			entry := findCoverageOccupancy(t, next, cell, task)
			adopted, err := json.Marshal(entry.Adopted)
			if err != nil || string(adopted) != tc.want {
				t.Errorf("refresh reused last selection: adopted=%s, want %s (error=%v)", adopted, tc.want, err)
			}
			view := buildMeasurementCoverage(next, coverageSizeNamespace, httpapi.MeasurementCoverageQuery{})
			wantFreshness := "fresh"
			if failed {
				wantFreshness = coverageFreshnessStale
			}
			if !next.checked[coverageCatalogSource].Equal(checked) || view.DataAsOf != checked.Format(time.RFC3339Nano) ||
				view.Freshness != wantFreshness || (next.failures[coverageCatalogSource] != nil) != failed {
				t.Errorf("catalog confirmation/failure flow changed: checked=%v dataAsOf=%s freshness=%s failures=%v",
					next.checked[coverageCatalogSource], view.DataAsOf, view.Freshness, next.failures)
			}
			for _, source := range view.Sources {
				if source.Kind == coverageCatalogSource && (source.Complete == failed || (source.Error != nil) != failed) {
					t.Errorf("catalog source completeness hid refresh failure: %+v", source)
				}
			}
		})
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

func TestCoverageCatalogAdapterPreservesValuesAndRowJSON(t *testing.T) {
	zero := 0.0
	at := time.Unix(100, 123).UTC()
	facts := profilederivation.MeasurementFacts{
		Request: profilederivation.MeasurementRequestReference{Namespace: "ns", Name: "request", UID: "request-uid"},
		Condition: profilederivation.CurvePlanSpec{
			LogicalModel:      "model",
			NodeUID:           coverageFixturePhysicalUID,
			PlannedBatchSizes: []int{1},
		},
		Batches: []profilederivation.MeasurementBatchFacts{
			{Batch: 1, P95Milliseconds: &zero, IncrementalJoulesPerItem: &zero},
		},
	}
	for _, reused := range []bool{false, true} {
		profile := serving.Profile{
			ID:         "ns/request/request-uid",
			Reference:  evidencestore.ObjectIdentity{Store: "store", Key: "result"},
			Condition:  facts.Condition,
			Batches:    []serving.BatchProfile{{Batch: 1}},
			MeasuredAt: at,
			NodeName:   "node",
		}
		if reused {
			profile.Reuse = &serving.ProfileReuseCheck{
				SourceProfileID:       "source",
				ConfirmationProfileID: profile.ID,
				Outcome:               "Accepted",
				SourceNode: serving.NodeReference{
					Name: "node",
					UID:  "previous-physical-uid",
				},
				TargetNode:                  serving.NodeReference{Name: "node", UID: coverageFixturePhysicalUID},
				IdlePowerRelativeDifference: &zero,
				Batches:                     []serving.ProfileReuseBatchCheck{{Batch: 1, SourceMeanServiceNanos: -20}},
				Record:                      &evidencestore.ObjectIdentity{Store: "store", Key: "reuse"},
			}
		}
		snapshot := servingkube.ProjectCatalogSnapshot(servingkube.CatalogView{
			Profiles: []serving.Profile{
				profile,
			}, CurrentMeasurements: []profilederivation.MeasurementSelection{{ID: profile.ID}},
			MeasurementFacts: map[string]serving.MeasurementFacts{
				profile.ID: facts,
			}, SourceCheckedAt: at, Revision: "revision",
		}, "config", nil)
		source := coverageCatalogSourceAdapter{read: func(context.Context) (servingkube.CatalogRead, error) {
			return servingkube.CatalogRead{CatalogSnapshot: snapshot}, nil
		}}
		read, err := source.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(read.Reporting, queryCoverageCatalog(catalogView(snapshot))) ||
			!read.SourceCheckedAt.Equal(at) {
			t.Fatal("snapshot conversion changed queried catalog values")
		}
		converted := queryCoverageProfile(profile)
		if !reflect.DeepEqual(converted, read.Reporting.Profiles[0]) ||
			converted.Condition.NodeUID != coverageFixturePhysicalUID ||
			converted.MeasuredAt != at {
			t.Fatal("profile conversion changed identity or measured time")
		}
		_, rows := queryapi.AssessCatalogProfile(read.Reporting, converted)
		if len(rows) != 1 {
			t.Fatalf("rows=%d", len(rows))
		}
		want := queryapi.MeasurementResultRow(facts, facts.Batches[0], profile.ID, reused, true)
		want.RowKind, want.ID = "direct", profile.ID+"/direct/1"
		if reused {
			want.RowKind, want.ID = queryapi.ResultRowConfirmation, profile.ID+"/confirmation/1"
			want.SourceProfileID, want.Reuse = &profile.Reuse.SourceProfileID, profile.Reuse
		}
		actualJSON, err := json.Marshal(rows[0])
		if err != nil {
			t.Fatal(err)
		}
		wantJSON, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actualJSON, wantJSON) {
			t.Fatalf("row JSON changed: got %s want %s", actualJSON, wantJSON)
		}
	}
}

func TestCoverageCatalogSourceRejectsMissingReader(t *testing.T) {
	if queryCatalogSource(nil) != nil {
		t.Fatal("missing catalog source was wrapped")
	}
}

const coverageFixturePhysicalUID = "physical-uid"

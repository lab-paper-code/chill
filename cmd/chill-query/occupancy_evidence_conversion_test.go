package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	querykube "github.com/lab-paper-code/chill/internal/queryapi/kube"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestOccupancyServingEvidenceBoundaryPreservesValues(t *testing.T) {
	for _, d := range []int64{0, -9223372036854775807, 9223372036854775807} {
		cr := api.DispatchOccupancyMeasurement{
			ObjectMeta: meta.ObjectMeta{
				Name:       "request",
				Namespace:  "allowed",
				UID:        "request-uid",
				Generation: 9223372036854775807,
			},
			Spec: api.DispatchOccupancyMeasurementSpec{Node: "node", Batch: 2, Seed: ^uint64(0)},
		}
		cr.Spec.Conditions.ProfileID = "profile"
		cr.Status.PerRequestNanos = &d
		cr.Status.Result = &api.ProfilingRunEvidenceReference{
			Store: "evidence",
			Key:   servingkube.DispatchOccupancyPrefix + "node-uid/request-uid.json",
			Size:  9223372036854775807,
		}
		record := servingkube.DispatchOccupancyRecord{SchemaVersion: servingkube.DispatchOccupancySchema,
			Node: serving.NodeReference{
				Name: "node",
				UID:  "node-uid",
			}, Model: "model", PowerMode: "mode", ProfileID: "profile", Batch: 2,
			Conditions: cr.Spec, PerRequestNanos: d, Throughput: 1.25, IdleFraction: 0,
			Verdict: queryapi.MeasurementVerdictAccepted,
		}
		record.Window.Start, record.Window.End = time.Unix(100, 1).UTC(), time.Unix(200, 2).UTC()
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		projection, err := querykube.ProjectDispatchOccupancy(
			context.Background(),
			&operationProjectionStore{payload: raw},
			1<<20,
			cr,
			decodeOccupancyEvidence,
		)
		if err != nil {
			t.Fatal(err)
		}
		got := dispatchOccupancyHTTPProjection(cr, projection)
		assertLegacyOccupancyFacts(t, projection, record, d)
		wire, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Conditions api.DispatchOccupancyMeasurementSpec `json:"conditions"`
			D          *queryapi.OccupancyValue             `json:"d"`
			Evidence   *queryapi.OccupancyEvidence          `json:"evidence"`
			UID        string                               `json:"uid"`
		}
		if err := json.Unmarshal(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		// Legacy conditions are the safe HTTP envelope; the read verifier still
		// compares the complete uint64-bearing immutable spec before accepting.
		if decoded.D == nil || *decoded.D.PerRequestNanos != d || decoded.Evidence.Size != cr.Status.Result.Size ||
			decoded.UID != string(cr.UID) {
			t.Fatalf("wire precision/identity changed: %s", wire)
		}
		cr.Spec.MinimumPairs = 4
		cr.Status.PerRequestNanos = nil
		cr.Status.Result.Key = servingkube.BurstOccupancyPrefix + "node-uid/request-uid.json"
		for _, estimate := range []*serving.OccupancyEstimate{nil, {
			Node: record.Node, Model: record.Model, PowerMode: record.PowerMode, Batch: record.Batch,
			Profile: serving.OccupancyProfile{ProfileID: record.ProfileID},
			Window:  serving.OccupancyWindow{Start: record.Window.Start, End: record.Window.End},
		}} {
			burst := servingkube.BurstOccupancyRecord{
				SchemaVersion: servingkube.BurstOccupancySchema,
				Node:          record.Node,
				Model:         record.Model,
				PowerMode:     record.PowerMode,
				ProfileID:     record.ProfileID,
				Batch:         record.Batch,
				Conditions:    cr.Spec,
				Verdict:       queryapi.MeasurementVerdictObserved,
				Estimate:      estimate,
			}
			raw, err := json.Marshal(burst)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := querykube.ProjectDispatchOccupancy(
				context.Background(),
				&operationProjectionStore{payload: raw},
				1<<20,
				cr,
				decodeOccupancyEvidence,
			)
			if err != nil {
				t.Fatal(err)
			}
			got := dispatchOccupancyHTTPProjection(cr, projection)
			if projection.Burst == nil || got.D != nil ||
				!reflect.DeepEqual(got.Estimate, queryOccupancyEstimate(estimate)) {
				t.Fatalf("nil/zero burst evidence changed: %+v", got)
			}
			wantJSON, err := json.Marshal(estimate)
			if err != nil {
				t.Fatal(err)
			}
			gotJSON, err := json.Marshal(got.Estimate)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("estimate wire changed: %s != %s", gotJSON, wantJSON)
			}
			wire, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(wire, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded.Conditions, cr.Spec) || decoded.Conditions.Seed != ^uint64(0) ||
				decoded.D != nil {
				t.Fatalf("uint64 spec or nil scalar changed: %s", wire)
			}
		}
	}
}

func TestOccupancySourceDecoderPreservesFullServingWireValidation(t *testing.T) {
	for _, burst := range []bool{false, true} {
		cr := api.DispatchOccupancyMeasurement{ObjectMeta: meta.ObjectMeta{Name: "request", UID: "request-uid"},
			Spec: api.DispatchOccupancyMeasurementSpec{Node: "node", Batch: 2}}
		d := int64(0)
		cr.Status.PerRequestNanos = &d
		cr.Status.Result = &api.ProfilingRunEvidenceReference{
			Store: "evidence",
			Key:   servingkube.DispatchOccupancyPrefix + "node-uid/request-uid.json",
		}
		var source any = servingkube.DispatchOccupancyRecord{
			SchemaVersion: servingkube.DispatchOccupancySchema, Node: serving.NodeReference{Name: "node", UID: "node-uid"},
			Batch: 2, Conditions: cr.Spec, Verdict: queryapi.MeasurementVerdictAccepted,
		}
		if burst {
			cr.Status.Result.Key = servingkube.BurstOccupancyPrefix + "node-uid/request-uid.json"
			source = servingkube.BurstOccupancyRecord{
				SchemaVersion: servingkube.BurstOccupancySchema, Node: serving.NodeReference{Name: "node", UID: "node-uid"},
				Batch: 2, Conditions: cr.Spec, Verdict: queryapi.MeasurementVerdictObserved,
			}
		}
		baseline, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct{ name, field, value string }{
			{name: "valid"},
			{name: "unused runtime image", field: "runtimeImage", value: `123`},
			{name: "unused timestamp", field: "measuredAt", value: `"not-a-time"`},
			{name: "unused drain state", field: "drainState", value: `[]`},
			{name: "unused isolation", field: "isolation", value: `[]`},
			{name: "unused load job", field: "loadJob", value: `{"image":123}`},
			{name: "valid unknown field", field: "futureField", value: `{"anything":[1,null]}`},
		} {
			t.Run(tc.name+map[bool]string{false: "/legacy", true: "/burst"}[burst], func(t *testing.T) {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(baseline, &fields); err != nil {
					t.Fatal(err)
				}
				if tc.field != "" {
					fields[tc.field] = json.RawMessage(tc.value)
				}
				raw, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				var originalErr error
				if burst {
					var original servingkube.BurstOccupancyRecord
					originalErr = json.Unmarshal(raw, &original)
				} else {
					var original servingkube.DispatchOccupancyRecord
					originalErr = json.Unmarshal(raw, &original)
				}
				decoded, decodeErr := decodeOccupancyEvidence(cr, raw)
				if decoded.Burst != burst || (decodeErr == nil) != (originalErr == nil) ||
					(decodeErr != nil && decodeErr.Error() != originalErr.Error()) {
					t.Fatalf(
						"source decode changed: branch=%t err=%v original=%v",
						decoded.Burst,
						decodeErr,
						originalErr,
					)
				}
				got, err := querykube.ProjectDispatchOccupancy(
					context.Background(),
					&operationProjectionStore{payload: raw},
					1<<20,
					cr,
					decodeOccupancyEvidence,
				)
				if err != nil {
					t.Fatal(err)
				}
				var assessment queryapi.MeasurementAssessment
				if err := json.Unmarshal(got.Assessment, &assessment); err != nil {
					t.Fatal(err)
				}
				invalid := false
				for _, reason := range assessment.Reasons {
					invalid = invalid || reason.Code == "OccupancyEvidenceInvalid"
				}
				if invalid != (originalErr != nil) || (originalErr == nil && got.Record == nil && got.Burst == nil) {
					t.Fatalf("full-wire decode outcome changed: %s (original %v)", got.Assessment, originalErr)
				}
			})
		}
	}
}

func assertLegacyOccupancyFacts(
	t *testing.T, got queryapi.OccupancyEvidenceProjection, record servingkube.DispatchOccupancyRecord, d int64,
) {
	t.Helper()
	if got.Record == nil ||
		got.Record.Node != (queryapi.MeasurementIdentity{Name: record.Node.Name, UID: record.Node.UID}) ||
		got.Record.Model != record.Model ||
		got.Record.PowerMode != record.PowerMode ||
		got.Record.ProfileID != record.ProfileID ||
		got.Record.Verdict != record.Verdict {
		t.Fatalf("serving identity/facts changed: %+v", got.Record)
	}
	if got.D == nil || got.D.PerRequestNanos == nil || *got.D.PerRequestNanos != d || got.D.Throughput == nil ||
		*got.D.Throughput != 1.25 ||
		got.D.IdleFraction == nil ||
		*got.D.IdleFraction != 0 ||
		got.MeasurementWindow == nil ||
		got.MeasurementWindow.Start != record.Window.Start ||
		got.MeasurementWindow.End != record.Window.End {
		t.Fatalf("signed scalar, zero, precision or nanoseconds changed: %+v", got.DispatchOccupancyProgress)
	}
}

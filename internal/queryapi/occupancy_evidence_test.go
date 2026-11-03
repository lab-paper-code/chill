package queryapi

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func occupancyEvidenceFixture() (api.DispatchOccupancyMeasurement, OccupancyEvidenceRecord) {
	d := int64(-9223372036854775807)
	cr := api.DispatchOccupancyMeasurement{
		ObjectMeta: meta.ObjectMeta{Name: "request", UID: "request-uid", Generation: 1},
		Spec:       api.DispatchOccupancyMeasurementSpec{Node: "node", Batch: 2, Seed: ^uint64(0)},
	}
	cr.Spec.Conditions.ProfileID = "profile"
	cr.Status.Phase, cr.Status.PerRequestNanos, cr.Status.Throughput = api.MeasurementPhaseComplete, &d, "10"
	cr.Status.Result = &api.ProfilingRunEvidenceReference{
		Store: "evidence",
		Key:   DispatchOccupancyPrefix + "node-uid/request-uid.json",
	}
	return cr, OccupancyEvidenceRecord{OccupancyRecordFacts: OccupancyRecordFacts{
		Node: MeasurementIdentity{
			Name: "node",
			UID:  "node-uid",
		}, Model: "model", PowerMode: "mode", ProfileID: "profile", Verdict: MeasurementVerdictAccepted},
		SchemaVersion: DispatchOccupancySchema, Batch: 2, Conditions: cr.Spec, PerRequestNanos: d, Throughput: 10,
		Window: OccupancyMeasurementWindow{Start: time.Unix(100, 1).UTC(), End: time.Unix(200, 2).UTC()}}
}

func TestOccupancyEvidenceStrictIdentityAndSignedValues(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*api.DispatchOccupancyMeasurement, *OccupancyEvidenceRecord)
		accepted bool
	}{
		{name: "exact", accepted: true},
		{name: "zero", accepted: true, change: func(cr *api.DispatchOccupancyMeasurement, r *OccupancyEvidenceRecord) {
			r.PerRequestNanos = 0
			zero := int64(0)
			cr.Status.PerRequestNanos = &zero
		}},
		{name: "schema", change: func(_ *api.DispatchOccupancyMeasurement, r *OccupancyEvidenceRecord) {
			r.SchemaVersion = occupancyForeignValue
		}},
		{name: "node name", change: func(_ *api.DispatchOccupancyMeasurement, r *OccupancyEvidenceRecord) {
			r.Node.Name = occupancyForeignValue
		}},
		{name: "node UID", change: func(_ *api.DispatchOccupancyMeasurement, r *OccupancyEvidenceRecord) {
			r.Node.UID = occupancyForeignValue
		}},
		{name: "empty UID", change: func(_ *api.DispatchOccupancyMeasurement, r *OccupancyEvidenceRecord) { r.Node.UID = "" }},
		{name: "request UID", change: func(cr *api.DispatchOccupancyMeasurement, _ *OccupancyEvidenceRecord) { cr.UID = occupancyForeignValue }},
		{name: "conditions uint64", change: func(_ *api.DispatchOccupancyMeasurement, r *OccupancyEvidenceRecord) { r.Conditions.Seed-- }},
		{name: "batch", change: func(_ *api.DispatchOccupancyMeasurement, r *OccupancyEvidenceRecord) { r.Batch++ }},
		{name: "profile", change: func(_ *api.DispatchOccupancyMeasurement, r *OccupancyEvidenceRecord) {
			r.ProfileID = occupancyForeignValue
		}},
		{name: "scalar", change: func(_ *api.DispatchOccupancyMeasurement, r *OccupancyEvidenceRecord) { r.PerRequestNanos++ }},
		{name: "status scalar nil", change: func(cr *api.DispatchOccupancyMeasurement, _ *OccupancyEvidenceRecord) {
			cr.Status.PerRequestNanos = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr, record := occupancyEvidenceFixture()
			if tc.change != nil {
				tc.change(&cr, &record)
			}
			got, err := InterpretDispatchOccupancy(
				cr,
				DecodedOccupancyEvidence{Record: record, HasPerRequestNanos: true},
				nil,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			var assessment MeasurementAssessment
			if err := json.Unmarshal(got.Assessment, &assessment); err != nil {
				t.Fatal(err)
			}
			if assessment.Evidence.Accepted != tc.accepted || (got.Record != nil) != tc.accepted {
				t.Fatalf("identity acceptance changed: %+v %s", got, got.Assessment)
			}
			if tc.accepted &&
				(got.D == nil || *got.D.PerRequestNanos != record.PerRequestNanos || got.MeasurementWindow == nil || *got.MeasurementWindow != record.Window || got.Record.Node != record.Node) {
				t.Fatalf("signed value, nanoseconds or node identity changed: %+v", got)
			}
			if !tc.accepted && !strings.Contains(string(got.Assessment), "OccupancyEvidenceMismatch") {
				t.Fatalf("missing mismatch: %s", got.Assessment)
			}
		})
	}
}

func TestW8OccupancyEvidenceLegacyMissingScalarAndReadFailures(t *testing.T) {
	cr, record := occupancyEvidenceFixture()
	decoded := DecodedOccupancyEvidence{Record: record, HasPerRequestNanos: false}
	for _, tc := range []struct {
		name      string
		decodeErr error
		readErr   error
		code      string
	}{
		{"missing scalar", nil, nil, "OccupancyEvidenceMismatch"},
		{"corrupt", errors.New("invalid JSON"), nil, "OccupancyEvidenceInvalid"},
		{"missing", nil, evidencestore.ErrNotFound, "OccupancyEvidenceMissing"},
		{"unavailable", nil, errors.New("denied"), "OccupancyEvidenceUnavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := InterpretDispatchOccupancy(cr, decoded, tc.decodeErr, tc.readErr)
			if err != nil || got.Record != nil || !strings.Contains(string(got.Assessment), tc.code) {
				t.Fatalf("failure distinction changed: %+v %v", got, err)
			}
			if tc.name == "missing scalar" && got.D != nil {
				t.Fatalf("missing scalar invented zero: %+v", got.D)
			}
		})
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if finiteOccupancyValue(value) != nil {
			t.Fatal("nonfinite value exposed")
		}
	}
	for _, value := range []string{"NaN", "+Inf", "-Inf", "not-number"} {
		if !invalidOccupancyThroughput(value) {
			t.Fatalf("nonfinite status accepted: %s", value)
		}
	}
}

func TestW8BurstEvidenceInterpretation(t *testing.T) {
	for _, verdict := range []string{MeasurementVerdictObserved, "Insufficient"} {
		for _, statistics := range []*OccupancyStatistics{nil, {Median: -20, Mean: -10}} {
			cr, legacy := occupancyEvidenceFixture()
			cr.Status.PerRequestNanos = nil
			cr.Spec.MinimumPairs = 4
			cr.Status.Result.Key = BurstOccupancyPrefix + "node-uid/request-uid.json"
			record := legacy
			record.SchemaVersion, record.Conditions, record.Verdict = BurstOccupancySchema, cr.Spec, verdict
			record.Estimate = &OccupancyEstimate{
				Node:      legacy.Node,
				Model:     legacy.Model,
				PowerMode: legacy.PowerMode,
				Batch:     2,
				Profile: OccupancyProfile{
					ProfileID: legacy.ProfileID,
				},
				Pairs:      3,
				Statistics: statistics,
				Window:     legacy.Window,
			}
			decoded := DecodedOccupancyEvidence{Burst: true, Record: record}
			got, err := InterpretDispatchOccupancy(cr, decoded, nil, nil)
			if err != nil || got.Burst == nil || got.Record != nil || got.D != nil || got.Estimate == nil ||
				got.Estimate.Pairs != 3 ||
				got.Verdict != verdict ||
				got.MinimumPairs != 4 {
				t.Fatalf("burst facts changed: %+v %v", got, err)
			}
			if (got.Estimate.Statistics == nil) != (statistics == nil) ||
				(statistics != nil && got.Estimate.Statistics.Median != -20) {
				t.Fatal("null or signed statistics changed")
			}
			var assessment MeasurementAssessment
			if err := json.Unmarshal(got.Assessment, &assessment); err != nil {
				t.Fatal(err)
			}
			if !assessment.Evidence.Available || assessment.Evidence.Accepted ||
				(verdict == MeasurementVerdictObserved && assessment.Status != MeasurementComplete) {
				t.Fatalf("burst admission or signed-status semantics changed: %s", got.Assessment)
			}
			cr.Status.Result.Key = DispatchOccupancyPrefix + "node-uid/request-uid.json"
			wrong, err := InterpretDispatchOccupancy(cr, decoded, nil, nil)
			if err != nil || wrong.Burst != nil ||
				!strings.Contains(string(wrong.Assessment), "OccupancyEvidenceMismatch") {
				t.Fatalf("wrong path accepted: %+v %v", wrong, err)
			}
		}
	}
}

const occupancyForeignValue = "foreign"

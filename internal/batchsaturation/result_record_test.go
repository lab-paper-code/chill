package batchsaturation

import (
	"bytes"
	"reflect"
	"testing"
)

func TestResultRecordRoundTripsWithoutInterpretingDetail(t *testing.T) {
	detail := []byte(`{"selected":2,"method":{"z":1,"a":[3,2,1]}}`)
	record, err := NewResultRecord(ResultRecordSpec{
		Outcome: "Stopped/NoFurtherImprovement",
		CurvePlan: StoredObjectReference{
			Store: "evidence", Key: "batch-saturations/parent-uid/curve-plan.json",
			SchemaVersion: "chill.dacs.io/curve-plan.v1alpha4", Size: 1024,
		},
		AdmittedTrials: []AdmittedTrialReference{{
			Run:         RunReference{Name: "active-b2-a2", UID: "active-uid"},
			Measurement: ResultMeasurement{WorkloadMode: WorkloadModeActive, Batch: 2},
			Evidence: StoredObjectReference{
				Store: "evidence", Key: "profiling-runs/active-uid/execution.json",
				SchemaVersion: "chill.dacs.io/profiling-execution-evidence.v1alpha2", Size: 2048,
			},
		}},
		ExcludedTrials: []ExcludedTrialReference{{
			Run:         RunReference{Name: "active-b2-a1", UID: "failed-uid"},
			Measurement: ResultMeasurement{WorkloadMode: WorkloadModeActive, Batch: 2},
			Attempt:     1, Reasons: []string{"JobFailed"},
		}},
		Detail: detail,
	})
	if err != nil {
		t.Fatalf("construct result record: %v", err)
	}
	payload, err := record.CanonicalJSON()
	if err != nil {
		t.Fatalf("encode result record: %v", err)
	}
	decoded, err := DecodeResultRecord(payload)
	if err != nil {
		t.Fatalf("decode result record: %v", err)
	}
	if !reflect.DeepEqual(decoded.Spec(), record.Spec()) {
		t.Fatalf("round trip changed record:\n got: %+v\nwant: %+v", decoded.Spec(), record.Spec())
	}
	if got := decoded.Spec().Detail; !bytes.Equal(got, detail) {
		t.Fatalf("detail = %s, want byte-identical %s", got, detail)
	}
	if decoded.SchemaVersion() != "chill.dacs.io/batch-saturation-result.v1alpha1" {
		t.Fatalf("schema version = %q", decoded.SchemaVersion())
	}
}

func TestDecodeResultRecordRejectsAnyOtherSchemaVersion(t *testing.T) {
	record, err := NewResultRecord(ResultRecordSpec{
		Outcome: "Stopped/CapabilityBoundary",
		CurvePlan: StoredObjectReference{
			Store: "evidence", Key: "batch-saturations/parent-uid/curve-plan.json",
			SchemaVersion: "chill.dacs.io/curve-plan.v1alpha4", Size: 1024,
		},
		Detail: []byte(`null`),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := record.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{
		"chill.dacs.io/batch-saturation-result.v1alpha0",
		"chill.example.io/batch-saturation-result.v1alpha1",
	} {
		mutated := bytes.Replace(
			payload,
			[]byte("chill.dacs.io/batch-saturation-result.v1alpha1"),
			[]byte(schema),
			1,
		)
		if _, err := DecodeResultRecord(mutated); err == nil {
			t.Fatalf("schema version %q was accepted", schema)
		}
	}
}

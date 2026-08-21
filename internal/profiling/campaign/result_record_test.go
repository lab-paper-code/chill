package batchsaturation

import (
	"bytes"
	"testing"
)

func TestResultRecordCanonicalEncodingIsStableAndPreservesDetailValue(t *testing.T) {
	spec := validResultRecordSpec()
	spec.Detail = []byte(` { "selected": 2, "note": "a<b && c>d" } `)
	record, err := NewResultRecord(spec)
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
	if !record.SemanticallyEqual(decoded) {
		t.Fatalf("round trip changed decoded record value:\n got: %+v\nwant: %+v", decoded.Spec(), record.Spec())
	}
	if got := decoded.Spec().Detail; bytes.Equal(got, spec.Detail) || !equalJSONValues(got, spec.Detail) {
		t.Fatalf("detail encoding = %s, original = %s", got, spec.Detail)
	}
	reencoded, err := decoded.CanonicalJSON()
	if err != nil {
		t.Fatalf("re-encode result record: %v", err)
	}
	if !bytes.Equal(reencoded, payload) {
		t.Fatalf("canonical encoding is not idempotent:\n got: %s\nwant: %s", reencoded, payload)
	}
	decodedSpec := decoded.Spec()
	if decodedSpec.MeasurementState != spec.MeasurementState {
		t.Fatalf("measurement state = %+v, want %+v", decodedSpec.MeasurementState, spec.MeasurementState)
	}
	if decodedSpec.AdmittedTrials[0].Run.Namespace != "chill-system" ||
		decodedSpec.ExcludedTrials[0].Run.Namespace != "chill-system" {
		t.Fatalf("Run namespaces were not retained: %+v", decodedSpec)
	}
	if decoded.SchemaVersion() != "chill.dacs.io/batch-saturation-result.v1alpha3" {
		t.Fatalf("schema version = %q", decoded.SchemaVersion())
	}
}

func TestDecodeResultRecordRejectsAnyOtherSchemaVersion(t *testing.T) {
	record, err := NewResultRecord(validResultRecordSpec())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := record.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{
		"chill.dacs.io/batch-saturation-result.v1alpha2",
		"chill.dacs.io/batch-saturation-result.v1alpha1",
		"chill.dacs.io/batch-saturation-result.v1alpha0",
		"chill.example.io/batch-saturation-result.v1alpha3",
	} {
		mutated := bytes.Replace(
			payload,
			[]byte("chill.dacs.io/batch-saturation-result.v1alpha3"),
			[]byte(schema),
			1,
		)
		if _, err := DecodeResultRecord(mutated); err == nil {
			t.Fatalf("schema version %q was accepted", schema)
		}
	}
}

func TestResultRecordRequiresCleanMeasurementState(t *testing.T) {
	fields := map[string]func(*ResultMeasurementState, string){
		"target Node":    func(state *ResultMeasurementState, value string) { state.TargetNode = value },
		"ModelSpec":      func(state *ResultMeasurementState, value string) { state.ModelSpec = value },
		"DeviceClass":    func(state *ResultMeasurementState, value string) { state.DeviceClass = value },
		"execution path": func(state *ResultMeasurementState, value string) { state.ExecutionPath = value },
		"power mode":     func(state *ResultMeasurementState, value string) { state.PowerMode = value },
	}
	for name, set := range fields {
		for _, value := range []string{"", " padded "} {
			spec := validResultRecordSpec()
			set(&spec.MeasurementState, value)
			if _, err := NewResultRecord(spec); err == nil {
				t.Fatalf("%s value %q was accepted", name, value)
			}
		}
	}
}

func TestResultRecordRejectsDuplicateRunUID(t *testing.T) {
	spec := validResultRecordSpec()
	spec.ExcludedTrials[0].Run.UID = spec.AdmittedTrials[0].Run.UID
	if _, err := NewResultRecord(spec); err == nil {
		t.Fatal("duplicate ProfilingRun UID was accepted")
	}
}

func TestResultRecordRejectsInvalidDetail(t *testing.T) {
	spec := validResultRecordSpec()
	spec.Detail = []byte(`{"unfinished":`)
	if _, err := NewResultRecord(spec); err == nil {
		t.Fatal("invalid result detail was accepted")
	}
}

func TestResultRecordRejectsNonPositiveExcludedAttempt(t *testing.T) {
	spec := validResultRecordSpec()
	spec.ExcludedTrials[0].Attempt = 0
	if _, err := NewResultRecord(spec); err == nil {
		t.Fatal("non-positive excluded attempt was accepted")
	}
}

func TestResultRecordRequiresRunNamespace(t *testing.T) {
	spec := validResultRecordSpec()
	spec.AdmittedTrials[0].Run.Namespace = ""
	if _, err := NewResultRecord(spec); err == nil {
		t.Fatal("Run reference without a namespace was accepted")
	}
}

func validResultRecordSpec() ResultRecordSpec {
	return ResultRecordSpec{
		Outcome: "Stopped/NoFurtherImprovement",
		MeasurementState: ResultMeasurementState{
			TargetNode: "jetsono", ModelSpec: "mobilenet-v2-050",
			DeviceClass: "jetson-agx-orin-64g", ExecutionPath: "ort-cpu", PowerMode: "MODE_50W",
		},
		CurvePlan: StoredObjectReference{
			Store: "evidence", Key: "batch-saturations/parent-uid/curve-plan.json",
			SchemaVersion: "chill.dacs.io/curve-plan.v1alpha6", Size: 1024,
		},
		AdmittedTrials: []AdmittedTrialReference{{
			Run: RunReference{
				Namespace: "chill-system", Name: "active-b2-a2", UID: "active-uid",
			},
			Measurement: ResultMeasurement{WorkloadMode: WorkloadModeActive, Batch: 2},
			Evidence: StoredObjectReference{
				Store: "evidence", Key: "profiling-runs/active-uid/execution.json",
				SchemaVersion: "chill.dacs.io/profiling-execution-evidence.v1alpha2", Size: 2048,
			},
		}},
		ExcludedTrials: []ExcludedTrialReference{{
			Run: RunReference{
				Namespace: "chill-system", Name: "active-b2-a1", UID: "failed-uid",
			},
			Measurement: ResultMeasurement{WorkloadMode: WorkloadModeActive, Batch: 2},
			Attempt:     1, Reasons: []string{"JobFailed"},
		}},
		Detail: []byte(`null`),
	}
}

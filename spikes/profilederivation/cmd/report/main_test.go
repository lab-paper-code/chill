package main

import (
	"testing"

	"github.com/lab-paper-code/chill/internal/profilederivation"
	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

func TestRunIdentitiesSeparateStateAndProtocolButNotBatch(t *testing.T) {
	intent := profilingrun.Intent{
		Measurement: profilingrun.MeasurementContract{Batch: 1, InputGenerator: "a"},
		Metrics:     profilingrun.MetricContract{PowerMetric: "watts"},
	}
	base, err := profileradmission.ProjectRunIdentities(intent, "node")
	if err != nil {
		t.Fatal(err)
	}
	intent.Measurement.Batch = 2
	got, err := profileradmission.ProjectRunIdentities(intent, "node")
	if err != nil {
		t.Fatal(err)
	}
	if got != base {
		t.Fatal("batch must remain the curve dimension")
	}
	intent.Measurement.InputGenerator = "b"
	got, err = profileradmission.ProjectRunIdentities(intent, "node")
	if err != nil {
		t.Fatal(err)
	}
	if got.StateIdentity != base.StateIdentity || got.MeasurementProtocolIdentity == base.MeasurementProtocolIdentity {
		t.Fatal("input generator must change only measurement-protocol identity")
	}
	intent.Measurement.InputGenerator = "a"
	intent.Metrics.PowerMetric = "other"
	got, err = profileradmission.ProjectRunIdentities(intent, "node")
	if err != nil {
		t.Fatal(err)
	}
	if got.StateIdentity == base.StateIdentity {
		t.Fatal("power metric must change execution-state compatibility identity")
	}
}

func TestAdapterRejectsUnknownBundleField(t *testing.T) {
	payload := []byte(`{"schemaVersion":"chill.dacs.io/profiling-run-evidence.v1alpha1","unknown":true}`)
	_, err := deriveReport(payload, "sha256:test", profilederivation.AdmissionPolicy{})
	if err == nil {
		t.Fatal("expected strict schema rejection")
	}
}

func TestStrictRejectsDuplicateKeys(t *testing.T) {
	var value map[string]any
	if err := strict([]byte(`{"a":1,"a":2}`), &value); err == nil {
		t.Fatal("expected duplicate key rejection")
	}
}

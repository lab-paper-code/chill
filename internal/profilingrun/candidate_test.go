package profilingrun

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

func TestCandidateReportBindsEveryMaterialSelection(t *testing.T) {
	payload, err := os.ReadFile("../../spikes/profilingrun/fixtures/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	intentPayload, err := os.ReadFile("../../spikes/profilingrun/fixtures/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := decodeTestIntent(intentPayload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	intent.Candidate.ReportDigest = "sha256:" + hex.EncodeToString(sum[:])
	if err := ValidateCandidateReport(payload, intent); err != nil {
		t.Fatal(err)
	}
	intent.Candidate.ExecutionPath = "other"
	if err := ValidateCandidateReport(payload, intent); err == nil {
		t.Fatal("expected pre-run selection mismatch rejection")
	}
}

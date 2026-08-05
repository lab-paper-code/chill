package controller

import (
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/profiler"
	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
)

func TestFrozenRunRejectsMixedConfiguredPolicySnapshots(t *testing.T) {
	frozen := testFrozenRun(t)
	frozen.AdmissionPolicy.Source.Kind = profileradmission.ConfigMapPolicySourceKind
	frozen.AdmissionPolicy.Source.Namespace = testSystemNamespace
	frozen.AdmissionPolicy.Source.Name = "facts"
	frozen.AdmissionPolicy.Source.UID = testFactsUID
	frozen.AdmissionPolicy.Source.ResourceVersion = "1"
	frozen.AdmissionPolicy.Source.Key = "admission-policies.yaml"
	frozen.ExecutionPolicy.Source.Kind = profiler.ConfigMapExecutionPolicySourceKind
	frozen.ExecutionPolicy.Source.Namespace = testSystemNamespace
	frozen.ExecutionPolicy.Source.Name = "facts"
	frozen.ExecutionPolicy.Source.UID = testFactsUID
	frozen.ExecutionPolicy.Source.ResourceVersion = "2"
	frozen.ExecutionPolicy.Source.Key = "execution-policies.yaml"

	err := frozen.Validate()
	if err == nil || !strings.Contains(err.Error(), "different ConfigMap snapshots") {
		t.Fatalf("mixed policy snapshot error = %v", err)
	}
}

func TestFrozenRunRejectsActuationPolicyFromAnotherSnapshot(t *testing.T) {
	_, frozen := testActuatedRunAndFrozen(t)
	frozen.Prepared.ActuationPolicy.Source.ResourceVersion = "43"
	err := frozen.Validate()
	if err == nil || !strings.Contains(err.Error(), "actuation and scientific policies") {
		t.Fatalf("mixed actuation policy snapshot error = %v", err)
	}
}

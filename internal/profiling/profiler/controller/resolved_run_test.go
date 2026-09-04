package controller

import (
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profileradmission "github.com/lab-paper-code/chill/internal/profiling/profiler/admission"
)

func TestResolvedRunRejectsMixedConfiguredPolicySnapshots(t *testing.T) {
	resolved := testResolvedRun(t)
	resolved.AdmissionPolicy = configuredAdmissionPolicyWithSource(
		t,
		profileradmission.WallPowerPolicyV2,
		resolved.Prepared.RunConfig.Measurement.WorkloadMode,
		profileradmission.PolicySource{
			Kind: profileradmission.ConfigMapPolicySourceKind, Namespace: testSystemNamespace,
			Name: "facts", UID: testFactsUID, ResourceVersion: "1", Key: "admission-policies.yaml",
		},
	)
	resolved.ExecutionPolicy.Source.Kind = profiler.ConfigMapExecutionPolicySourceKind
	resolved.ExecutionPolicy.Source.Namespace = testSystemNamespace
	resolved.ExecutionPolicy.Source.Name = "facts"
	resolved.ExecutionPolicy.Source.UID = testFactsUID
	resolved.ExecutionPolicy.Source.ResourceVersion = "2"
	resolved.ExecutionPolicy.Source.Key = "execution-policies.yaml"

	err := resolved.Validate()
	if err == nil || !strings.Contains(err.Error(), "different ConfigMap snapshots") {
		t.Fatalf("mixed policy snapshot error = %v", err)
	}
}

func TestResolvedRunRejectsActuationPolicyFromAnotherSnapshot(t *testing.T) {
	_, frozen := testActuatedRunAndFrozen(t)
	frozen.Prepared.ActuationPolicy.Source.ResourceVersion = "43"
	err := frozen.Validate()
	if err == nil || !strings.Contains(err.Error(), "actuation and scientific policies") {
		t.Fatalf("mixed actuation policy snapshot error = %v", err)
	}
}

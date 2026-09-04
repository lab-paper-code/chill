package controller

import (
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"sigs.k8s.io/yaml"
)

const (
	testSystemNamespace = "chill-system"
	testFactsUID        = "facts-uid"
)

func testPreparedRun(t *testing.T) profiler.PreparedRun {
	t.Helper()
	return profiler.PreparedRun{RunConfig: testFrozenIntent(t)}
}

func testActuatedRunAndFrozen(t *testing.T) (*edgev1alpha1.ProfilingRun, ResolvedRun) {
	t.Helper()
	run := testRun()
	run.Spec.Candidate.PowerMode = testSelectablePowerMode
	prepared := testPreparedRun(t)
	prepared.RunConfig.SchemaVersion = profilingrun.RunConfigSchemaVersion
	prepared.RunConfig.State.PowerMode = run.Spec.Candidate.PowerMode
	target := profilingrun.ActuatorTarget{
		Kind:           profilingrun.ActuatorKindJetsonNVPModel,
		JetsonNVPModel: &profilingrun.JetsonNVPModelTarget{ID: 1},
	}
	selection := profiler.ActuationSelection{
		DeviceClass: prepared.RunConfig.State.DeviceClass,
		PowerMode:   prepared.RunConfig.State.PowerMode,
		Actuator:    target,
	}
	policySpec := profiler.ActuationPolicySpec{
		Name: "lattepanda-15w-v1", DeviceClass: selection.DeviceClass,
		PowerMode: selection.PowerMode, ActuatorKind: selection.Actuator.Kind,
		SettlingDurationSeconds: 30, MaxActuationDurationSeconds: 300,
	}
	policy, err := profiler.FreezeConfiguredActuationPolicy(
		actuationPolicyCatalogForSpecs(t, policySpec), selection,
		profiler.ActuationPolicySource{
			Kind: profiler.ConfigMapActuationPolicySourceKind, Namespace: testSystemNamespace,
			Name: "chill-profiler-facts", UID: testFactsUID, ResourceVersion: "42",
			Key: "actuation-policies.yaml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared.RunConfig.Actuation = &profilingrun.ActuationContract{
		PowerMode: selection.PowerMode, Actuator: target, PolicyName: policy.Policy.Name,
	}
	prepared.ActuationPolicy = &policy
	return run, resolvedRunForPrepared(t, prepared)
}

func actuationPolicyCatalogForSpecs(t *testing.T, specs ...profiler.ActuationPolicySpec) []byte {
	t.Helper()
	raw, err := yaml.Marshal(profiler.ActuationPolicyCatalog{
		SchemaVersion: profiler.ActuationPolicyCatalogSchemaVersion,
		Policies:      specs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testResolvedRun(t *testing.T) ResolvedRun {
	t.Helper()
	return resolvedRunForPrepared(t, testPreparedRun(t))
}

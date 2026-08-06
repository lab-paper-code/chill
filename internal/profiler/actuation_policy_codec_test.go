package profiler

import (
	"testing"

	"github.com/lab-paper-code/chill/internal/profilingrun"
	"sigs.k8s.io/yaml"
)

func TestFreezeConfiguredActuationPolicySelectsAndBindsCatalogSnapshot(t *testing.T) {
	selection := testActuationSelection()
	raw := testActuationPolicyCatalog(t, testActuationPolicySpec())
	frozen, err := FreezeConfiguredActuationPolicy(raw, selection, testActuationPolicySource("7"))
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Policy.Name != "xavier-nvpmodel-v1" ||
		frozen.Policy.PowerMode != selection.PowerMode {
		t.Fatalf("selected actuation policy = %#v", frozen)
	}
	canonical, err := frozen.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeFrozenActuationPolicy(append([]byte(" \n"), canonical...))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Policy.Name != frozen.Policy.Name || decoded.Source != frozen.Source {
		t.Fatalf("decoded actuation policy = %#v", decoded)
	}
}

func TestActuationPolicyRetainsSelectionAndConfigMapRevision(t *testing.T) {
	raw := testActuationPolicyCatalog(t, testActuationPolicySpec())
	first, err := FreezeConfiguredActuationPolicy(raw, testActuationSelection(), testActuationPolicySource("1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := FreezeConfiguredActuationPolicy(raw, testActuationSelection(), testActuationPolicySource("2"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Policy != second.Policy {
		t.Fatal("source revision changed the selected policy semantics")
	}
	if first.Source.ResourceVersion == second.Source.ResourceVersion {
		t.Fatal("source revision was not retained in the frozen policy")
	}
}

func TestFreezeConfiguredActuationPolicyRejectsAmbiguousMissingAndInvalidCatalogs(t *testing.T) {
	selection := testActuationSelection()
	first := testActuationPolicySpec()
	duplicate := first
	duplicate.Name = "duplicate"
	for name, raw := range map[string][]byte{
		"duplicate selector": testActuationPolicyCatalog(t, first, duplicate),
		"missing selector": testActuationPolicyCatalog(t, ActuationPolicySpec{
			Name: "other", DeviceClass: "other-class", PowerMode: "15W",
			ActuatorKind:            profilingrun.ActuatorKindJetsonNVPModel,
			SettlingDurationSeconds: 30, MaxActuationDurationSeconds: 300,
		}),
		"invalid duration": testActuationPolicyCatalog(t, ActuationPolicySpec{
			Name: "invalid", DeviceClass: selection.DeviceClass, PowerMode: selection.PowerMode,
			ActuatorKind: selection.Actuator.Kind, SettlingDurationSeconds: 60,
			MaxActuationDurationSeconds: 60,
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := FreezeConfiguredActuationPolicy(raw, selection, testActuationPolicySource("1")); err == nil {
				t.Fatal("invalid actuation-policy catalog was accepted")
			}
		})
	}
}

func testActuationSelection() ActuationSelection {
	return ActuationSelection{
		DeviceClass: "jetson-xavier-nx-8g",
		PowerMode:   "MODE_15W_6CORE",
		Actuator: profilingrun.ActuatorTarget{
			Kind:           profilingrun.ActuatorKindJetsonNVPModel,
			JetsonNVPModel: &profilingrun.JetsonNVPModelTarget{ID: 2},
		},
	}
}

func testActuationPolicySpec() ActuationPolicySpec {
	return ActuationPolicySpec{
		Name: "xavier-nvpmodel-v1", DeviceClass: "jetson-xavier-nx-8g",
		PowerMode: "MODE_15W_6CORE", ActuatorKind: profilingrun.ActuatorKindJetsonNVPModel,
		SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
	}
}

func testActuationPolicyCatalog(t *testing.T, policies ...ActuationPolicySpec) []byte {
	t.Helper()
	raw, err := yaml.Marshal(ActuationPolicyCatalog{
		SchemaVersion: ActuationPolicyCatalogSchemaVersion,
		Policies:      policies,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testActuationPolicySource(resourceVersion string) ActuationPolicySource {
	return ActuationPolicySource{
		Kind: ConfigMapActuationPolicySourceKind, Namespace: "chill-system", Name: "facts",
		UID: "facts-uid", ResourceVersion: resourceVersion, Key: "actuation-policies.yaml",
	}
}

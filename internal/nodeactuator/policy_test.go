package nodeactuator

import "testing"

func TestActuationPolicyDigestPreservesProfilerCanonicalIdentity(t *testing.T) {
	t.Parallel()
	policy := ActuationPolicy{
		SchemaVersion: ActuationPolicySchemaVersion,
		ActuationPolicySpec: ActuationPolicySpec{
			Name: "xavier-nvpmodel-v1", DeviceClass: "jetson-xavier-nx-8g",
			PowerMode: "MODE_15W_6CORE", ActuatorKind: ActuatorKindJetsonNVPModel,
			SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
		},
	}
	digest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	const want = "sha256:2745246d67dee7023f268d38aa828a9599a5730633e90bb63dcbcb617ad73533"
	if digest != want {
		t.Fatalf("policy digest = %q, want %q", digest, want)
	}
}

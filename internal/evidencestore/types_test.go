package evidencestore

import (
	"errors"
	"testing"
)

func TestObjectIdentityValidate(t *testing.T) {
	valid := ObjectIdentity{
		Store:  "profiling-evidence",
		Key:    "runs/run-1.json",
		Digest: "sha256:165d50e316de2b3d7da0728df1245aaf56f8f5a407668db1c24a756b6ba3fe42",
		Size:   389364,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}

	tests := map[string]ObjectIdentity{
		"missing store":       {Key: valid.Key, Digest: valid.Digest, Size: valid.Size},
		"missing key":         {Store: valid.Store, Digest: valid.Digest, Size: valid.Size},
		"key whitespace":      {Store: valid.Store, Key: " " + valid.Key, Digest: valid.Digest, Size: valid.Size},
		"noncanonical digest": {Store: valid.Store, Key: valid.Key, Digest: "165d50e3", Size: valid.Size},
		"empty object":        {Store: valid.Store, Key: valid.Key, Digest: valid.Digest, Size: 0},
	}
	for name, identity := range tests {
		t.Run(name, func(t *testing.T) {
			if err := identity.Validate(); !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("Validate() error = %v, want ErrInvalidIdentity", err)
			}
		})
	}
}

func TestReceiptValidate(t *testing.T) {
	identity := ObjectIdentity{
		Store:  "profiling-evidence",
		Key:    "runs/run-1.json",
		Digest: "sha256:165d50e316de2b3d7da0728df1245aaf56f8f5a407668db1c24a756b6ba3fe42",
		Size:   389364,
	}
	for _, outcome := range []CreateOutcome{CreateOutcomeCreated, CreateOutcomeExistingExactMatch} {
		receipt := Receipt{SchemaVersion: ReceiptSchemaVersion, Object: identity, Outcome: outcome}
		if err := receipt.Validate(); err != nil {
			t.Fatalf("valid receipt with outcome %q rejected: %v", outcome, err)
		}
	}

	invalid := Receipt{SchemaVersion: ReceiptSchemaVersion, Object: identity, Outcome: "Conflict"}
	if err := invalid.Validate(); err == nil {
		t.Fatal("unsupported receipt outcome was accepted")
	}
}

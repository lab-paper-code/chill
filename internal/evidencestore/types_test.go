package evidencestore

import (
	"errors"
	"testing"
)

func TestObjectIdentityValidate(t *testing.T) {
	valid := ObjectIdentity{
		Store: "profiling-evidence",
		Key:   "runs/run-1.json",
		Size:  389364,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}

	tests := map[string]ObjectIdentity{
		"missing store":  {Key: valid.Key, Size: valid.Size},
		"missing key":    {Store: valid.Store, Size: valid.Size},
		"key whitespace": {Store: valid.Store, Key: " " + valid.Key, Size: valid.Size},
		"empty object":   {Store: valid.Store, Key: valid.Key, Size: 0},
	}
	for name, identity := range tests {
		t.Run(name, func(t *testing.T) {
			if err := identity.Validate(); !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("Validate() error = %v, want ErrInvalidIdentity", err)
			}
		})
	}
}

func TestObjectLocationValidate(t *testing.T) {
	valid := ObjectLocation{Store: "profiling-evidence", Key: "runs/run-1/final.json"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid location rejected: %v", err)
	}
	for name, location := range map[string]ObjectLocation{
		"missing store": {Key: valid.Key},
		"missing key":   {Store: valid.Store},
		"whitespace":    {Store: valid.Store, Key: valid.Key + " "},
	} {
		t.Run(name, func(t *testing.T) {
			if err := location.Validate(); !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("Validate() error = %v, want ErrInvalidIdentity", err)
			}
		})
	}
}

func TestReceiptValidate(t *testing.T) {
	identity := ObjectIdentity{
		Store: "profiling-evidence",
		Key:   "runs/run-1.json",
		Size:  389364,
	}
	for _, outcome := range []CreateOutcome{CreateOutcomeCreated, CreateOutcomeExisting} {
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

package evidencestore

import (
	"fmt"
	"regexp"
	"strings"
)

const ReceiptSchemaVersion = "chill.dacs.io/evidence-store-receipt.v1alpha1"

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ObjectIdentity is the expected identity of one immutable evidence object.
// Key is opaque to this package; path conventions belong to its caller.
type ObjectIdentity struct {
	Store  string `json:"store"`
	Key    string `json:"key"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

func (i ObjectIdentity) Validate() error {
	for name, value := range map[string]string{
		"store": i.Store,
		"key":   i.Key,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidIdentity, name)
		}
		if strings.TrimSpace(value) != value {
			return fmt.Errorf("%w: %s must not have surrounding whitespace", ErrInvalidIdentity, name)
		}
	}
	if !digestPattern.MatchString(i.Digest) {
		return fmt.Errorf("%w: digest must be canonical sha256", ErrInvalidIdentity)
	}
	if i.Size < 1 {
		return fmt.Errorf("%w: size must be positive", ErrInvalidIdentity)
	}
	return nil
}

type CreateOutcome string

const (
	CreateOutcomeCreated            CreateOutcome = "Created"
	CreateOutcomeExistingExactMatch CreateOutcome = "ExistingExactMatch"
)

// Receipt is a bounded result of an immutable-create attempt. It deliberately
// contains no endpoint, credentials, backend response, or evidence bytes.
type Receipt struct {
	SchemaVersion string         `json:"schemaVersion"`
	Object        ObjectIdentity `json:"object"`
	Outcome       CreateOutcome  `json:"outcome"`
}

func (r Receipt) Validate() error {
	if r.SchemaVersion != ReceiptSchemaVersion {
		return fmt.Errorf("unsupported receipt schemaVersion %q", r.SchemaVersion)
	}
	if err := r.Object.Validate(); err != nil {
		return err
	}
	switch r.Outcome {
	case CreateOutcomeCreated, CreateOutcomeExistingExactMatch:
		return nil
	default:
		return fmt.Errorf("unsupported create outcome %q", r.Outcome)
	}
}

package evidencestore

import (
	"fmt"
	"strings"
)

const ReceiptSchemaVersion = "chill.dacs.io/evidence-store-receipt.v1alpha1"

// ObjectLocation identifies where an object is stored without claiming an
// expected content identity. Reading by location must be followed by content
// validation before the bytes are trusted.
type ObjectLocation struct {
	Store string `json:"store"`
	Key   string `json:"key"`
}

func (l ObjectLocation) Validate() error {
	for name, value := range map[string]string{
		"store": l.Store,
		"key":   l.Key,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidIdentity, name)
		}
		if strings.TrimSpace(value) != value {
			return fmt.Errorf("%w: %s must not have surrounding whitespace", ErrInvalidIdentity, name)
		}
	}
	return nil
}

// ObjectIdentity describes one immutable evidence object.
// Key is opaque to this package; path conventions belong to its caller.
type ObjectIdentity struct {
	Store string `json:"store"`
	Key   string `json:"key"`
	Size  int64  `json:"size"`
}

func (i ObjectIdentity) Validate() error {
	if err := i.Location().Validate(); err != nil {
		return err
	}
	if i.Size < 1 {
		return fmt.Errorf("%w: size must be positive", ErrInvalidIdentity)
	}
	return nil
}

func (i ObjectIdentity) Location() ObjectLocation {
	return ObjectLocation{Store: i.Store, Key: i.Key}
}

type CreateOutcome string

const (
	CreateOutcomeCreated  CreateOutcome = "Created"
	CreateOutcomeExisting CreateOutcome = "Existing"
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
	case CreateOutcomeCreated, CreateOutcomeExisting:
		return nil
	default:
		return fmt.Errorf("unsupported create outcome %q", r.Outcome)
	}
}

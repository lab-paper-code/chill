package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

// WriteRaw encodes and immutably writes one already-observed transition. It
// does not classify, derive, retry, or recover the observation.
func WriteRaw(
	ctx context.Context,
	store evidencestore.ImmutableWriter,
	location evidencestore.ObjectLocation,
	raw transitioncost.RawObservation,
) (evidencestore.Receipt, error) {
	if store == nil {
		return evidencestore.Receipt{}, errors.New("immutable evidence writer is required")
	}
	if err := location.Validate(); err != nil {
		return evidencestore.Receipt{}, fmt.Errorf(
			"validate transition raw evidence location: %w", err,
		)
	}
	payload, err := transitioncost.EncodeRawRecord(raw)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	identity := evidencestore.ObjectIdentity{
		Store: location.Store,
		Key:   location.Key,
		Size:  int64(len(payload)),
	}
	receipt, err := store.PutImmutable(ctx, identity, bytes.NewReader(payload))
	if err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("write transition raw evidence: %w", err)
	}
	if err := receipt.Validate(); err != nil || receipt.Object != identity {
		return evidencestore.Receipt{}, errors.New(
			"transition raw evidence writer returned an invalid receipt",
		)
	}
	return receipt, nil
}

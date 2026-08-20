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
// does not perform observation, retry, recovery, or downstream revalidation.
func WriteRaw(
	ctx context.Context,
	store evidencestore.ImmutableWriter,
	location evidencestore.ObjectLocation,
	frozen transitioncost.FrozenContract,
	raw transitioncost.RawObservation,
	policy transitioncost.Policy,
) (evidencestore.Receipt, transitioncost.Result, error) {
	if store == nil {
		return evidencestore.Receipt{}, transitioncost.Result{}, errors.New("immutable evidence writer is required")
	}
	if err := location.Validate(); err != nil {
		return evidencestore.Receipt{}, transitioncost.Result{}, fmt.Errorf(
			"validate transition raw evidence location: %w", err,
		)
	}
	payload, result, err := transitioncost.EncodeRawRecord(frozen, raw, policy)
	if err != nil {
		return evidencestore.Receipt{}, result, err
	}
	identity := evidencestore.ObjectIdentity{
		Store: location.Store,
		Key:   location.Key,
		Size:  int64(len(payload)),
	}
	receipt, err := store.PutImmutable(ctx, identity, bytes.NewReader(payload))
	if err != nil {
		return evidencestore.Receipt{}, result, fmt.Errorf("write transition raw evidence: %w", err)
	}
	if err := receipt.Validate(); err != nil || receipt.Object != identity {
		return evidencestore.Receipt{}, result, errors.New(
			"transition raw evidence writer returned an invalid receipt",
		)
	}
	return receipt, result, nil
}

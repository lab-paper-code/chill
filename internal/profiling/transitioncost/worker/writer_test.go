package worker_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	transitionworker "github.com/lab-paper-code/chill/internal/profiling/transitioncost/worker"
)

func TestWriteRawStoresOneDecodableObservation(t *testing.T) {
	raw := writeObservation()
	location := evidencestore.ObjectLocation{Store: "evidence", Key: "transition/raw.json"}
	var storedIdentity evidencestore.ObjectIdentity
	var storedPayload []byte
	store := immutableWriterFunc(func(
		_ context.Context,
		identity evidencestore.ObjectIdentity,
		content io.Reader,
	) (evidencestore.Receipt, error) {
		storedIdentity = identity
		var readErr error
		storedPayload, readErr = io.ReadAll(content)
		if readErr != nil {
			return evidencestore.Receipt{}, readErr
		}
		return validReceipt(identity), nil
	})

	receipt, err := transitionworker.WriteRaw(context.Background(), store, location, raw)
	if err != nil {
		t.Fatal(err)
	}
	if storedIdentity.Location() != location || storedIdentity.Size != int64(len(storedPayload)) {
		t.Fatalf("stored identity = %#v, want location %#v and size %d",
			storedIdentity, location, len(storedPayload))
	}
	if receipt.Object != storedIdentity {
		t.Fatalf("receipt object = %#v, want %#v", receipt.Object, storedIdentity)
	}
	decoded, err := transitioncost.DecodeRawRecord(storedPayload)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.ObservationStartedAt.Equal(raw.ObservationStartedAt) ||
		len(decoded.PowerSamples) != 1 || decoded.PowerSamples[0].Watts != raw.PowerSamples[0].Watts {
		t.Fatalf("decoded raw observation changed: %#v", decoded)
	}
}

func TestWriteRawRejectsInvalidLocationBeforeWriting(t *testing.T) {
	calls := 0
	store := immutableWriterFunc(func(
		context.Context,
		evidencestore.ObjectIdentity,
		io.Reader,
	) (evidencestore.Receipt, error) {
		calls++
		return evidencestore.Receipt{}, nil
	})

	_, err := transitionworker.WriteRaw(
		context.Background(), store, evidencestore.ObjectLocation{}, writeObservation(),
	)
	if err == nil || calls != 0 {
		t.Fatalf("WriteRaw() error = %v, writer calls = %d; want validation error before write", err, calls)
	}
}

func TestWriteRawReportsWriterAndReceiptFailures(t *testing.T) {
	location := evidencestore.ObjectLocation{Store: "evidence", Key: "transition/raw.json"}
	writeErr := errors.New("store unavailable")
	tests := map[string]immutableWriterFunc{
		"writer": func(
			context.Context,
			evidencestore.ObjectIdentity,
			io.Reader,
		) (evidencestore.Receipt, error) {
			return evidencestore.Receipt{}, writeErr
		},
		"receipt": func(
			_ context.Context,
			identity evidencestore.ObjectIdentity,
			_ io.Reader,
		) (evidencestore.Receipt, error) {
			receipt := validReceipt(identity)
			receipt.Object.Size++
			return receipt, nil
		},
	}
	for name, store := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := transitionworker.WriteRaw(
				context.Background(), store, location, writeObservation(),
			); err == nil {
				t.Fatal("WriteRaw() unexpectedly succeeded")
			}
		})
	}
}

type immutableWriterFunc func(
	context.Context,
	evidencestore.ObjectIdentity,
	io.Reader,
) (evidencestore.Receipt, error)

func (f immutableWriterFunc) PutImmutable(
	ctx context.Context,
	identity evidencestore.ObjectIdentity,
	content io.Reader,
) (evidencestore.Receipt, error) {
	return f(ctx, identity, content)
}

func validReceipt(identity evidencestore.ObjectIdentity) evidencestore.Receipt {
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion,
		Object:        identity,
		Outcome:       evidencestore.CreateOutcomeCreated,
	}
}

func writeObservation() transitioncost.RawObservation {
	base := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	return transitioncost.RawObservation{
		ObservationStartedAt: base,
		PowerSamples: []transitioncost.PowerSample{{
			ObservedAt: base.Add(time.Second), RequestLatencySeconds: 0.01, Watts: 20,
		}},
	}
}

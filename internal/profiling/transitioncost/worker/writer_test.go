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

func TestWriteRawStoresOneDecodableRecord(t *testing.T) {
	frozen, raw, policy := writeInputs()
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

	receipt, result, err := transitionworker.WriteRaw(
		context.Background(), store, location, frozen, raw, policy,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict() != transitioncost.VerdictAccepted {
		t.Fatalf("result = %s/%s", result.Verdict(), result.Reason())
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
	if decoded.Verdict() != transitioncost.VerdictAccepted {
		t.Fatalf("stored result = %s/%s", decoded.Verdict(), decoded.Reason())
	}
	writtenAccepted, _ := result.AcceptedObservation()
	decodedAccepted, _ := decoded.AcceptedObservation()
	if decodedAccepted.TauMilliseconds() != writtenAccepted.TauMilliseconds() ||
		decodedAccepted.TransitionWallJoules() != writtenAccepted.TransitionWallJoules() ||
		decodedAccepted.EtaJoules() != writtenAccepted.EtaJoules() {
		t.Fatalf("decoded stored values = tau %v, wall %v, eta %v; want %v, %v, %v",
			decodedAccepted.TauMilliseconds(), decodedAccepted.TransitionWallJoules(), decodedAccepted.EtaJoules(),
			writtenAccepted.TauMilliseconds(), writtenAccepted.TransitionWallJoules(), writtenAccepted.EtaJoules())
	}
}

func TestWriteRawRejectsInvalidLocationBeforeWriting(t *testing.T) {
	frozen, raw, policy := writeInputs()
	calls := 0
	store := immutableWriterFunc(func(
		context.Context,
		evidencestore.ObjectIdentity,
		io.Reader,
	) (evidencestore.Receipt, error) {
		calls++
		return evidencestore.Receipt{}, nil
	})

	_, _, err := transitionworker.WriteRaw(
		context.Background(), store, evidencestore.ObjectLocation{}, frozen, raw, policy,
	)
	if err == nil || calls != 0 {
		t.Fatalf("WriteRaw() error = %v, writer calls = %d; want validation error before write", err, calls)
	}
}

func TestWriteRawReportsWriterAndReceiptFailures(t *testing.T) {
	frozen, raw, policy := writeInputs()
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
			_, result, err := transitionworker.WriteRaw(
				context.Background(), store, location, frozen, raw, policy,
			)
			if err == nil {
				t.Fatal("WriteRaw() unexpectedly succeeded")
			}
			if result.Verdict() != transitioncost.VerdictAccepted {
				t.Fatalf("result = %s/%s, want retained Accepted classification", result.Verdict(), result.Reason())
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

func writeInputs() (
	transitioncost.FrozenContract,
	transitioncost.RawObservation,
	transitioncost.Policy,
) {
	base := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	source := transitioncost.StateIdentity{
		DeviceType: "jetson-orin", Model: "mobilenet-v2", PowerMode: "MODE_15W",
	}
	target := transitioncost.StateIdentity{
		DeviceType: "jetson-orin", Model: "mobilenet-v2", PowerMode: "MAXN",
	}
	raw := completedRestartFacts(base, source, target)
	for _, offset := range []time.Duration{
		2100 * time.Millisecond, 3 * time.Second, 4 * time.Second,
		4900 * time.Millisecond, 5900 * time.Millisecond,
	} {
		raw.PowerSamples = append(raw.PowerSamples, transitioncost.PowerSample{
			ObservedAt: base.Add(offset), RequestLatencySeconds: 0.01, Watts: 20,
		})
	}
	return transitioncost.FrozenContract{
			SourceState: source, TargetState: target,
			TargetIdleBaseline: transitioncost.IdleBaseline{
				EvidenceReference: "evidence://idle-maxn", State: target, MeanWallWatts: 12,
			},
		}, raw, transitioncost.Policy{
			MaximumBoundaryDistance:      200 * time.Millisecond,
			MinimumCoverage:              0.9,
			MaximumSampleGap:             1100 * time.Millisecond,
			MaximumBoundaryUncertainty:   50 * time.Millisecond,
			MaximumReadyObservationDelay: 3 * time.Second,
		}
}

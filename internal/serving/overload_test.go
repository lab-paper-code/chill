package serving

import (
	"context"
	"errors"
	"math"
	goruntime "runtime"
	"runtime/debug"
	"testing"
	"time"
)

// The guard rejects new requests while the live heap exceeds the configured
// fraction of the Go memory limit and admits them again below it; without a
// memory limit it stays off.
func TestHeapOverloadRejectsNewRequests(t *testing.T) {
	_, snapshot := schedulerFixture()
	d, p := snapshot.Workloads[0], snapshot.Instances[0].Profile
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observationLog.Close(context.Background()) }()
	r, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 4, ObservationLog: observationLog, OverloadHeapFraction: 1e-9})
	if err != nil {
		t.Fatal(err)
	}
	defer r.cancel()
	r.SetProfiles([]Profile{*p})
	if err = r.Publish(d); err != nil {
		t.Fatal(err)
	}
	if err = r.InstallInstances([]ReadyInstance{{State: InstanceState{ID: "i", Node: snapshot.Nodes[0].Node, Profile: p.ID, Ready: true}, Profile: p, Weight: 1, Capacity: 4,
		Execute: func(context.Context, Dispatch) (BackendResult, error) { return BackendResult{Finished: true}, nil }}}); err != nil {
		t.Fatal(err)
	}
	code := func() string {
		receipt, err := r.Receive(d.Name, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.Accept(receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
		var domain *Error
		if errors.As(err, &domain) {
			return domain.Code
		}
		if err != nil {
			t.Fatal(err)
		}
		return ""
	}
	defer debug.SetMemoryLimit(debug.SetMemoryLimit(4 << 30))
	goruntime.GC()
	r.sampleHeap()
	if got := code(); got != "GatewayOverloaded" {
		t.Fatalf("live heap above the fraction admitted a request: %q", got)
	}
	debug.SetMemoryLimit(math.MaxInt64)
	r.sampleHeap()
	if got := code(); got != "" {
		t.Fatalf("guard without a memory limit rejected a request: %q", got)
	}
}

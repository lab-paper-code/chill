package serving

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestAssignmentAppendFailureRevertsAssignment(t *testing.T) {
	r, i, _ := routingRuntime(t, map[string]int32{"w1": 1})
	observationLog := r.config.ObservationLog.(*runtimeTestLog)
	blockRuntimeObservationWrites(t, observationLog)
	_, snapshot := schedulerFixture()
	d := snapshot.Workloads[0]
	receipt, err := r.Receive(d.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := r.Accept(receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = call.Wait(context.Background())
	var domain *Error
	if !errors.As(err, &domain) || domain.Code != ObservationUnavailableCode || call.assignment != nil ||
		r.instances[i.ID].outstanding != 0 || r.observationError == nil {
		t.Fatalf("failed assigned row kept the assignment: %v %+v", err, call.assignment)
	}
}

func TestSynchronousTransitionsReturnFinalWriteFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		run     func(*Runtime) error
		primary string
	}{
		{"observe", func(r *Runtime) error {
			r.calls["progress"] = &Call{}
			return r.ObserveBackend("missing", BackendProgress{RequestID: "progress"})
		}, "BackendContextMismatch"},
		{"stopped", func(r *Runtime) error {
			return r.ConfirmInstanceStopped("missing")
		}, ""},
		{"publish", func(r *Runtime) error {
			_, snapshot := schedulerFixture()
			return r.Publish(snapshot.Workloads[0])
		}, ""},
		{"install", func(r *Runtime) error {
			return r.InstallInstances([]ReadyInstance{{}})
		}, "InvalidDistribution"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _ := routingRuntime(t, map[string]int32{"w1": 1})
			log := r.config.ObservationLog.(*runtimeTestLog)
			blockRuntimeObservationWrites(t, log)
			if err := log.RequestEvent(RequestEvent{
				RequestID: "closed", WorkloadUID: "w1", Kind: phaseReceived, At: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
			r.mu.Lock()
			r.closeLocked(&Call{receipt: Receipt{ID: "closed"}})
			r.mu.Unlock()
			err := tc.run(r)
			var pathError *os.PathError
			if !errors.As(err, &pathError) || r.observationError == nil ||
				!errors.Is(err, r.observationError) || r.finalizing != 0 {
				t.Fatalf("final-write failure lost or not retained for retry: %v, sticky=%v", err, r.observationError)
			}
			retained, queryErr := log.HasRequest("w1", "closed")
			if queryErr != nil || !retained {
				t.Fatalf("failed final row was not retained: present=%v, error=%v", retained, queryErr)
			}
			if tc.primary != "" {
				var domain *Error
				if !errors.As(err, &domain) || domain.Code != tc.primary {
					t.Fatalf("primary failure lost while joining final-write error: %v", err)
				}
			}
		})
	}
}

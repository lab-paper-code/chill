package serving

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
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

func TestAssignmentENOSPCNeverExecutes(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	log := r.config.ObservationLog.(*runtimeTestLog)
	executed := make(chan struct{}, 1)
	r.mu.Lock()
	r.instances[instance.ID].Execute = func(context.Context, Dispatch) (BackendResult, error) {
		executed <- struct{}{}
		return BackendResult{Finished: true}, nil
	}
	r.mu.Unlock()
	// /dev/full returns real ENOSPC from Write, without allocating a disk-filling fixture.
	hour := time.Now().UTC().Truncate(time.Hour)
	for h := range 2 {
		directory := filepath.Join(log.config.Directory, "v1alpha5", hour.Add(time.Duration(h)*time.Hour).Format("20060102T15"))
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		for minute := range 60 {
			path := filepath.Join(directory, "requests."+time.Date(2000, 1, 1, 0, minute, 0, 0, time.UTC).Format("04")+".jsonl")
			if err := os.Symlink("/dev/full", path); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(path) })
		}
	}
	_, snapshot := schedulerFixture()
	receipt, err := r.Receive(snapshot.Workloads[0].Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := r.Accept(receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = call.Wait(context.Background())
	if !errors.Is(r.observationError, syscall.ENOSPC) || call.assignment != nil {
		t.Fatalf("failed assignment Write did not retain ENOSPC: %v (storage: %v)", err, r.observationError)
	}
	select {
	case <-executed:
		t.Fatal("native Execute followed failed assignment Write")
	default:
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

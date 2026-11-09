package observation

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func interferenceRuntime(t testing.TB, log *ObservationLog) (*serving.Runtime, []*serving.Definition, []*serving.Profile) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var admitted sync.Map
	log.config.OnRequestEvent = func(event serving.RequestEvent) {
		if event.Kind == serving.RequestRowAssigned {
			admitted.Store(event.RequestID, true)
		}
	}
	runtime, err := serving.NewRuntime(ctx, serving.RuntimeConfig{QueueLimit: 2, ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot := schedulerFixture()
	slow, fast := *snapshot.Workloads[0], *snapshot.Workloads[0]
	slow.Name, fast.Name, fast.UID = "slow", "fast", "fast-uid"
	profiles := []serving.Profile{*snapshot.Instances[0].Profile, *snapshot.Instances[0].Profile}
	profiles[0].ID, profiles[1].ID = "slow-profile", "fast-profile"
	profiles[0].RuntimeDelivery = json.RawMessage(`{"payload":"` + strings.Repeat("x", 1<<20) + `"}`)
	runtime.SetProfiles(profiles)
	definitions := []*serving.Definition{&slow, &fast}
	var instances []serving.ReadyInstance
	for index, definition := range definitions {
		if err := runtime.Publish(definition); err != nil {
			t.Fatal(err)
		}
		profile := &profiles[index]
		instances = append(instances, serving.ReadyInstance{
			State:   serving.InstanceState{ID: definition.Name, Node: snapshot.Nodes[0].Node, Profile: profile.ID, Ready: true},
			Profile: profile, Routing: map[string]int32{definition.UID: 1}, Capacity: 1,
			Execute: func(_ context.Context, dispatch serving.Dispatch) (serving.BackendResult, error) {
				if _, ok := admitted.Load(dispatch.RequestID); !ok {
					return serving.BackendResult{}, errors.New("native started before assignment admission")
				}
				return serving.BackendResult{Finished: true}, nil
			},
		})
	}
	if err := runtime.InstallInstances(instances); err != nil {
		t.Fatal(err)
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	return runtime, definitions, []*serving.Profile{&profiles[0], &profiles[1]}
}

func acceptObservedRequest(runtime *serving.Runtime, definition *serving.Definition) (serving.Receipt, *serving.Call, error) {
	receipt, err := runtime.Receive(definition.Name, time.Now())
	if err != nil {
		return receipt, nil, err
	}
	call, err := runtime.Accept(&receipt, &serving.InferenceInput{Inputs: []serving.RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	return receipt, call, err
}

func finishObservedRequest(runtime *serving.Runtime, receipt serving.Receipt, call *serving.Call) error {
	if _, err := call.Wait(context.Background()); err != nil {
		return err
	}
	return runtime.Response(receipt, "completed", "")
}

// These gates pause actual synchronous JSON preparation, never a simulated log
// or a manually held mutex. Watchdogs detect deadlocks, not a latency budget.
func TestRuntimeRequestsProgressDuringObservationPreparation(t *testing.T) {
	for _, operation := range []string{"assignment", "final", "scheduler", "scheduler-error"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			log, _ := clockedObservationLog(t, ObservationLogConfig{}, time.Now().UTC())
			runtime, definitions, profiles := interferenceRuntime(t, log)
			var slowReceipt serving.Receipt
			var slowCall *serving.Call
			if operation == "final" {
				var err error
				slowReceipt, slowCall, err = acceptObservedRequest(runtime, definitions[0])
				if err != nil {
					t.Fatal(err)
				}
				if _, err := slowCall.Wait(ctx); err != nil {
					t.Fatal(err)
				}
				// Scheduler's canonical immutable clone displaces Runtime's source.
				canonical := *profiles[0]
				if err := log.SchedulerRecord(SchedulerObservation{At: time.Now(), Snapshot: &serving.SchedulerSnapshot{Instances: []serving.SchedulerInstance{{ID: "slow", Profile: &canonical}}}}); err != nil {
					t.Fatal(err)
				}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			resume := sync.OnceFunc(func() { close(release) })
			t.Cleanup(resume)
			var once sync.Once
			log.marshal = func(value any) ([]byte, error) {
				blocked := false
				switch value := value.(type) {
				case *serving.Profile:
					blocked = (operation == "assignment" || operation == "final") && value == profiles[0]
				case schedulerLine:
					blocked = strings.HasPrefix(operation, "scheduler")
				}
				if blocked {
					once.Do(func() { close(entered); <-release })
				}
				return json.Marshal(value)
			}
			slowDone := make(chan error, 1)
			go func() {
				switch operation {
				case "assignment":
					var err error
					slowReceipt, slowCall, err = acceptObservedRequest(runtime, definitions[0])
					if err == nil {
						err = finishObservedRequest(runtime, slowReceipt, slowCall)
					}
					slowDone <- err
				case "final":
					slowDone <- runtime.Response(slowReceipt, "completed", "")
				default:
					payload := json.RawMessage(`{"payload":"` + strings.Repeat("s", 1<<20) + `"}`)
					if operation == "scheduler-error" {
						payload = json.RawMessage(`{"invalid":`)
					}
					slowDone <- log.SchedulerRecord(SchedulerObservation{At: time.Now(), Action: &SchedulerActionObservation{ID: "slow-record", Expected: payload}})
				}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("consumer did not enter JSON preparation")
			}
			if operation == "assignment" || operation == "final" {
				if done, err := runtime.Drain(ctx, definitions[0].UID); err != nil || done {
					t.Fatalf("Drain passed pending assignment/final admission: %v %v", done, err)
				}
			}
			fastDone := make(chan error, 1)
			go func() {
				receipt, call, err := acceptObservedRequest(runtime, definitions[1])
				if err == nil {
					err = finishObservedRequest(runtime, receipt, call)
				}
				if err == nil {
					var done bool
					done, err = runtime.Drain(ctx, definitions[1].UID)
					if err == nil && !done {
						err = errors.New("unrelated request did not drain")
					}
				}
				fastDone <- err
			}()
			select {
			case err := <-fastDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("unrelated Runtime request waited for observation preparation")
			}
			resume()
			select {
			case err := <-slowDone:
				if operation == "scheduler-error" {
					var marshalError *json.MarshalerError
					if !errors.As(err, &marshalError) || log.Err() != nil {
						t.Fatalf("serializer error was hidden or became sticky disk failure: %v %v", err, log.Err())
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("prepared operation did not finish")
			}
			if done, err := runtime.Drain(ctx, definitions[0].UID); err != nil || !done {
				t.Fatalf("completed request did not drain: %v %v", done, err)
			}
			if err := log.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(operation, "scheduler") {
				page, _, _, _, err := log.SchedulerObservationPage(ctx, log.started.Add(-time.Second), time.Now().Add(time.Second), 10, 0, SchedulerObservationSelection{})
				want := 1
				if operation == "scheduler-error" {
					want = 0
				}
				if err != nil || len(page) != want {
					t.Fatalf("scheduler admission/error order changed: %d %v", len(page), err)
				}
				if want != 0 {
					var body struct {
						Payload string `json:"payload"`
					}
					if err := json.Unmarshal(page[0].Action.Expected, &body); err != nil || body.Payload != strings.Repeat("s", 1<<20) {
						t.Fatalf("scheduler consumer lost prepared payload: bytes=%d %v", len(body.Payload), err)
					}
				}
			}
			byRequest := map[string][]serving.RequestRow{}
			for _, row := range readRequestRows(t, log.config.Directory) {
				byRequest[row.RequestID] = append(byRequest[row.RequestID], row)
			}
			want := 1
			if operation == "assignment" || operation == "final" {
				want++
			}
			if len(byRequest) != want {
				t.Fatalf("retained %d requests, want %d", len(byRequest), want)
			}
			for id, rows := range byRequest {
				if len(rows) != 2 || rows[0].Kind != serving.RequestRowAssigned || rows[1].Kind != serving.RequestRowFinal || rows[0].ProfileID != rows[1].ProfileID || rows[1].Outcome != "completed" || !rows[1].ExecutionComplete {
					t.Fatalf("request %s lost ordered physical/final facts: %+v", id, rows)
				}
			}
		})
	}
}

func TestSchedulerCatalogSerializerErrorPrecedesRecordError(t *testing.T) {
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	_, snapshot := schedulerFixture()
	profile := *snapshot.Instances[0].Profile
	profile.Batches = append([]serving.BatchProfile(nil), profile.Batches...)
	profile.Batches[0].EnergyPerInputJoules = math.NaN()
	snapshot.Instances[0].Profile = &profile
	err := log.SchedulerRecord(SchedulerObservation{At: observationTestHour, Snapshot: &snapshot,
		Action: &SchedulerActionObservation{ID: "invalid-record", Expected: json.RawMessage(`{"invalid":`)}})
	var unsupported *json.UnsupportedValueError
	if !errors.As(err, &unsupported) || log.Err() != nil {
		t.Fatalf("catalog serializer error lost precedence: %v %v", err, log.Err())
	}
	if err := log.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, _, _, _, err := log.SchedulerObservationPage(context.Background(), observationTestHour, observationTestHour.Add(time.Second), 10, 0, SchedulerObservationSelection{})
	if err != nil || len(page) != 0 {
		t.Fatalf("failed preparation admitted a partial record: %d %v", len(page), err)
	}
}

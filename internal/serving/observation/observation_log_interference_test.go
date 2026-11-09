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
	profiles[0] = *repeatedPairedObservationProfile(t, &profiles[0], 8192)
	runtime.SetProfiles(profiles)
	definitions := []*serving.Definition{&slow, &fast}
	instances := make([]serving.ReadyInstance, 0, len(definitions))
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
	return runtime.Response(receipt, outcomeCompleted, "")
}

const (
	preparationAssignment     = "assignment"
	preparationFinal          = "final"
	preparationScheduler      = "scheduler"
	preparationSchedulerError = "scheduler-error"
)

func prepareObservedFinal(t *testing.T, log *ObservationLog, runtime *serving.Runtime, definition *serving.Definition, profile *serving.Profile) serving.Receipt {
	t.Helper()
	receipt, call, err := acceptObservedRequest(runtime, definition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Scheduler's canonical immutable clone displaces Runtime's source.
	canonical := *profile
	if err := log.SchedulerRecord(SchedulerObservation{At: time.Now(), Snapshot: &serving.SchedulerSnapshot{Instances: []serving.SchedulerInstance{{ID: "slow", Profile: &canonical}}}}); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func gateObservationPreparation(t *testing.T, log *ObservationLog, operation string, profile *serving.Profile) (<-chan struct{}, func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	var once sync.Once
	log.marshal = func(value any) ([]byte, error) {
		blocked := false
		switch value := value.(type) {
		case *serving.Profile:
			blocked = (operation == preparationAssignment || operation == preparationFinal) && value == profile
		case schedulerLine:
			blocked = strings.HasPrefix(operation, preparationScheduler)
		}
		if blocked {
			once.Do(func() { close(entered); <-release })
		}
		return json.Marshal(value)
	}
	return entered, resume
}

func runObservedPreparation(log *ObservationLog, runtime *serving.Runtime, definition *serving.Definition, receipt serving.Receipt, operation string) error {
	switch operation {
	case preparationAssignment:
		receipt, call, err := acceptObservedRequest(runtime, definition)
		if err == nil {
			err = finishObservedRequest(runtime, receipt, call)
		}
		return err
	case preparationFinal:
		return runtime.Response(receipt, outcomeCompleted, "")
	default:
		payload := json.RawMessage(`{"payload":"` + strings.Repeat("s", 1<<20) + `"}`)
		if operation == preparationSchedulerError {
			payload = json.RawMessage(`{"invalid":`)
		}
		return log.SchedulerRecord(SchedulerObservation{At: time.Now(), Action: &SchedulerActionObservation{ID: "slow-record", Expected: payload}})
	}
}

func assertUnrelatedObservedRequestProgress(t *testing.T, runtime *serving.Runtime, definition *serving.Definition) {
	t.Helper()
	fastDone := make(chan error, 1)
	go func() {
		receipt, call, err := acceptObservedRequest(runtime, definition)
		if err == nil {
			err = finishObservedRequest(runtime, receipt, call)
		}
		if err == nil {
			var done bool
			done, err = runtime.Drain(context.Background(), definition.UID)
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
}

func assertObservedPreparationResult(t *testing.T, log *ObservationLog, operation string, slowDone <-chan error) {
	t.Helper()
	select {
	case err := <-slowDone:
		if operation == preparationSchedulerError {
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
}

func assertPreparedSchedulerObservation(t *testing.T, log *ObservationLog, operation string) {
	t.Helper()
	page, _, _, _, err := log.SchedulerObservationPage(context.Background(), log.started.Add(-time.Second), time.Now().Add(time.Second), 10, 0, SchedulerObservationSelection{})
	want := 1
	if operation == preparationSchedulerError {
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

func assertPreparedRequestRows(t *testing.T, log *ObservationLog, operation string) {
	t.Helper()
	byRequest := map[string][]serving.RequestRow{}
	for _, row := range readRequestRows(t, log.config.Directory) {
		byRequest[row.RequestID] = append(byRequest[row.RequestID], row)
	}
	want := 1
	if operation == preparationAssignment || operation == preparationFinal {
		want++
	}
	if len(byRequest) != want {
		t.Fatalf("retained %d requests, want %d", len(byRequest), want)
	}
	for id, rows := range byRequest {
		if len(rows) != 2 || rows[0].Kind != serving.RequestRowAssigned || rows[1].Kind != serving.RequestRowFinal || rows[0].ProfileID != rows[1].ProfileID || rows[1].Outcome != outcomeCompleted || !rows[1].ExecutionComplete {
			t.Fatalf("request %s lost ordered physical/final facts: %+v", id, rows)
		}
	}
}

// These gates pause actual synchronous JSON preparation, never a simulated log
// or a manually held mutex. Watchdogs detect deadlocks, not a latency budget.
func TestRuntimeRequestsProgressDuringObservationPreparation(t *testing.T) {
	for _, operation := range []string{preparationAssignment, preparationFinal, preparationScheduler, preparationSchedulerError} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			log, _ := clockedObservationLog(t, ObservationLogConfig{}, time.Now().UTC())
			runtime, definitions, profiles := interferenceRuntime(t, log)
			var slowReceipt serving.Receipt
			if operation == preparationFinal {
				slowReceipt = prepareObservedFinal(t, log, runtime, definitions[0], profiles[0])
			}
			entered, resume := gateObservationPreparation(t, log, operation, profiles[0])
			slowDone := make(chan error, 1)
			go func() {
				slowDone <- runObservedPreparation(log, runtime, definitions[0], slowReceipt, operation)
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("consumer did not enter JSON preparation")
			}
			if operation == preparationAssignment || operation == preparationFinal {
				if done, err := runtime.Drain(ctx, definitions[0].UID); err != nil || done {
					t.Fatalf("Drain passed pending assignment/final admission: %v %v", done, err)
				}
			}
			assertUnrelatedObservedRequestProgress(t, runtime, definitions[1])
			resume()
			assertObservedPreparationResult(t, log, operation, slowDone)
			if done, err := runtime.Drain(ctx, definitions[0].UID); err != nil || !done {
				t.Fatalf("completed request did not drain: %v %v", done, err)
			}
			if err := log.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(operation, preparationScheduler) {
				assertPreparedSchedulerObservation(t, log, operation)
			}
			assertPreparedRequestRows(t, log, operation)
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

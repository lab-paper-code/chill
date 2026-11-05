package serving_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	servingnative "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

func TestCapacityBridgeCatalogProjectionDoesNotMutateLiveProfiles(t *testing.T) {
	before := time.Now()
	decision, config := serving.CapacityBridgeObservationFixture(t)
	after := time.Now()
	candidate := *decision.Target
	stage := slices.IndexFunc(candidate.Actions, func(action serving.Action) bool { return action.RouteTarget != nil })
	if stage < 0 {
		t.Fatal("bridge fixture lost its staged handoff")
	}
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: config.Directory, Store: config.Store, Writer: config.Writer, Retention: config.Retention,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	at := time.Now().UTC()
	if err = log.SchedulerRecord(observation.SchedulerObservation{At: at, Decision: &decision}); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(config.Directory, "v1alpha5", "*", "scheduler.*.jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("scheduler evidence paths: %v, %v", paths, err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Decision *serving.SchedulerDecision `json:"decision"`
	}
	if err = json.Unmarshal(bytes.TrimSpace(raw), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Decision == nil || stored.Decision.Target == nil {
		t.Fatal("recorded bridge lost its target")
	}
	if stored.Decision.Target.Actions[stage].RouteTarget.Instances[0].Profile != nil || candidate.Actions[stage].RouteTarget.Instances[0].Profile == nil {
		t.Fatal("nested bridge catalog projection leaked or mutated live profiles")
	}
	if stored.Decision.Capacity == nil || stored.Decision.Capacity.Search == nil {
		t.Fatal("recorded decision lost its actual sweep")
	}
	search := stored.Decision.Capacity.Search
	if search.StartedAt.Before(before) || search.FinishedAt.After(after) || search.FinishedAt.Before(search.StartedAt) {
		t.Fatalf("stored search does not describe the actual sweep: before=%s search=[%s,%s] after=%s", before, search.StartedAt, search.FinishedAt, after)
	}
	var swept time.Duration
	for _, step := range search.Steps {
		swept += step.Duration
	}
	if swept <= 0 || search.FinishedAt.Sub(search.StartedAt) < swept {
		t.Fatalf("stored interval does not enclose sweep work: search=%+v work=%s", search, swept)
	}
}

func TestNativeHTTPServicePairsReachPersistedRequestConsumers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	from := time.Now().UTC()
	zero, short, long := int64(0), int64(time.Millisecond), int64(time.Second)
	samples := []*native.ServiceSample{
		{ServiceDurationNanos: 2_000_001, TrialID: "physical-runner"},
		{ServiceDurationNanos: 3_000_001, PrecedingIdleNanos: &zero, TrialID: "physical-runner"},
		{ServiceDurationNanos: 5_000_001, PrecedingIdleNanos: &short, TrialID: "physical-runner"},
		{ServiceDurationNanos: 80_000_001, PrecedingIdleNanos: &long, TrialID: "physical-runner"},
		nil, // Old progress has no canonical pair; do not manufacture G=0.
	}
	next := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request native.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		sample := samples[next]
		next++
		serviceMS := 1.0
		if sample != nil {
			serviceMS = float64(sample.ServiceDurationNanos) / float64(time.Millisecond)
		}
		progress := native.Progress{
			RequestID: request.ID, WorkloadUID: request.Context.WorkloadUID, Definition: request.Context.Definition,
			Phase: "finished", At: time.Now().UTC(), BatchID: uint64(next), BatchSize: 1, ProfileID: "profile",
			ServiceMS: serviceMS, NativeElapsedMS: serviceMS, ExecutionComplete: true, Outcome: "completed",
			ServiceSample: sample,
		}
		if err := json.NewEncoder(w).Encode(native.Response{
			ModelName: "model", ID: request.ID, ExecutionComplete: true, Progress: progress,
			Outputs: []native.Field{{Name: "y", Data: [][]byte{[]byte("answer")}}},
		}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client := &servingnative.Client{HTTPClient: server.Client()}
	runtime, definition, sink := serving.NativeHTTPObservationFixture(t, func(ctx context.Context, dispatch serving.Dispatch) (serving.BackendResult, error) {
		return client.Execute(ctx, servingnative.Spec{Endpoint: server.URL, ModelName: "model", Profile: &serving.Profile{ID: "profile"}}, dispatch)
	})
	log := sink.(*runtimeObservation)
	for range samples {
		receipt, err := runtime.Receive(definition.Name, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		call, err := runtime.Accept(receipt, &serving.InferenceInput{Inputs: []serving.RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
		if err != nil {
			t.Fatal(err)
		}
		result, err := call.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = runtime.Response(receipt, "completed", ""); err != nil {
			t.Fatal(err)
		}
		// The same actual completion arriving after HTTP settlement is retained
		// as native evidence, not used to rewrite the final HTTP row.
		if result.Progress == nil {
			t.Fatal("native HTTP completion lost progress")
		}
		if err = runtime.ObserveBackend("i1", *result.Progress); err != nil {
			t.Fatal(err)
		}
	}
	to := time.Now().UTC().Add(time.Second)
	read, err := log.Query(ctx, definition.UID, from, to, nil, nil, 0, "")
	if err != nil || len(read.Requests) != len(samples) {
		t.Fatalf("persisted final request read: %d rows, %v", len(read.Requests), err)
	}
	populations := map[string][]native.ServiceSample{}
	unknown := map[string]int{}
	_, err = log.ScanOccupancyRows(ctx, definition.UID, "", from, to, func(row serving.RequestRow) {
		if row.Kind != serving.RequestRowFinal && row.Kind != serving.RequestRowNative {
			return
		}
		if row.ServiceDurationNanos == 0 {
			if row.PrecedingIdleNanos != nil {
				t.Error("legacy progress became an observed zero gap")
			}
			unknown[row.Kind]++
			return
		}
		populations[row.Kind] = append(populations[row.Kind], native.ServiceSample{
			ServiceDurationNanos: row.ServiceDurationNanos, PrecedingIdleNanos: row.PrecedingIdleNanos, TrialID: row.NativeTrialID,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{serving.RequestRowFinal, serving.RequestRowNative} {
		if len(populations[kind]) != 4 || unknown[kind] != 1 {
			t.Fatalf("%s consumer lost canonical/legacy population: %+v, unknown=%d", kind, populations[kind], unknown[kind])
		}
		distribution, err := native.NewServiceDistribution(populations[kind])
		if err != nil {
			t.Fatal(err)
		}
		for _, pair := range []struct {
			gap     *int64
			service int64
		}{{nil, 2_000_001}, {&zero, 3_000_001}, {&short, 5_000_001}, {&long, 80_000_001}} {
			service, _, ok := distribution.QuantileNanos(pair.gap, 95)
			if !ok || service != pair.service {
				t.Fatalf("%s persisted G/S conditional support: gap=%v service=%d, supported=%v", kind, pair.gap, service, ok)
			}
		}
	}
}

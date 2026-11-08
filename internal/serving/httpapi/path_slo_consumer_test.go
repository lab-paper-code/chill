package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	servingnative "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

// This smoke uses real ingress, runtime, native HTTP client, observation storage,
// capacity replay and both scheduler engines. Only native execution and Kubernetes
// authorization are in-process doubles; this is not hardware evidence.
func newPathSLOHTTPFixture(t *testing.T, ctx context.Context, count int) *pathSLOHTTPFixture {
	t.Helper()
	entered := make(chan native.Request, count)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var runtime *serving.Runtime
	var first sync.Once
	// Named native HTTP double preserves the production raw-field protocol and
	// emits direct queued/running progress, rather than reconstructing timestamps.
	nativeHTTPDouble := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request native.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(request.Inputs) != 1 || request.Inputs[0].Name != "x" || len(request.Inputs[0].Data) != 1 || string(request.Inputs[0].Data[0]) != "sample" {
			t.Error("native transport changed the raw sample")
		}
		entered <- request
		first.Do(func() {
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		at := time.Now().UTC()
		progress := serving.BackendProgress{RequestID: request.ID, WorkloadUID: request.Context.WorkloadUID, Definition: request.Context.Definition, At: at, ProfileID: "profile"}
		for _, phase := range []string{"queued", "running"} {
			progress.Phase = phase
			if err := runtime.ObserveBackend("instance", progress); err != nil {
				t.Error(err)
			}
		}
		finished := time.Now().UTC()
		elapsed := finished.Sub(at)
		p := native.Progress{RequestID: request.ID, WorkloadUID: request.Context.WorkloadUID, Definition: request.Context.Definition,
			Phase: "finished", At: finished, BatchID: uint64(finished.UnixNano()), BatchSize: 1, ProfileID: "profile",
			ServiceMS: float64(elapsed) / float64(time.Millisecond), NativeElapsedMS: float64(elapsed) / float64(time.Millisecond),
			ExecutionComplete: true, Outcome: "completed", ServiceSample: &native.ServiceSample{ServiceDurationNanos: max(1, elapsed.Nanoseconds()), TrialID: "in-process-native-double"}}
		if err := json.NewEncoder(w).Encode(native.Response{ModelName: "model", ID: request.ID, ExecutionComplete: true, Progress: p,
			Outputs: []native.Field{{Name: "y", Data: [][]byte{[]byte("answer")}}}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(nativeHTTPDouble.Close)
	client := &servingnative.Client{HTTPClient: nativeHTTPDouble.Client()}
	execute := func(ctx context.Context, dispatch serving.Dispatch) (serving.BackendResult, error) {
		return client.Execute(ctx, servingnative.Spec{Endpoint: nativeHTTPDouble.URL, ModelName: "model", Profile: &serving.Profile{ID: "profile"}}, dispatch)
	}
	h, log := timingHandler(t, execute)
	runtime = h.runtime
	h.config.RequestTimeout = 10 * time.Second
	settings, snapshot := pathSLOConsumerModel(t)
	definition := snapshot.Workloads[0]
	runtime.SetProfiles([]serving.Profile{*snapshot.Instances[0].Profile})
	if err := runtime.Publish(definition); err != nil {
		t.Fatal(err)
	}
	if err := runtime.InstallInstances([]serving.ReadyInstance{{State: serving.InstanceState{ID: "instance", Node: snapshot.Instances[0].Node, Ready: true},
		Profile: snapshot.Instances[0].Profile, Capacity: 1, Routing: map[string]int32{definition.UID: 1}, Execute: execute}}); err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(h)
	t.Cleanup(front.Close)
	t.Cleanup(unblock)
	return &pathSLOHTTPFixture{runtime: runtime, log: log, front: front, entered: entered, unblock: unblock, settings: settings, snapshot: snapshot}
}

type pathSLOHTTPFixture struct {
	runtime  *serving.Runtime
	log      *observation.ObservationLog
	front    *httptest.Server
	entered  chan native.Request
	unblock  func()
	settings serving.SchedulerSettings
	snapshot serving.SchedulerSnapshot
}

func TestPathSLOHTTPObservationCapacityConsumers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const count = 24 // p50 splits to p75; 24 completed samples support the upper rank.
	fixture := newPathSLOHTTPFixture(t, ctx, count)
	runtime, log := fixture.runtime, fixture.log
	settings, snapshot := fixture.settings, fixture.snapshot
	definition := snapshot.Workloads[0]
	entered, unblock := fixture.entered, fixture.unblock
	old := serving.PathBudget{WorkloadUID: definition.UID, DefinitionID: definition.Identity(), Source: "observed", TotalMS: 19, PostMS: 7, JudgmentPercentile: 75}
	next := old
	next.TotalMS, next.PostMS = 31, 13
	runtime.SetCompletionMargin(settings.CompletionMarginMS)
	runtime.SetPathBudgets([]serving.PathBudget{old})
	invoke := func() pathSLOHTTPReply { return fixture.invoke(ctx) }
	from := time.Now().UTC().Add(-time.Minute)
	results := make(chan pathSLOHTTPReply, 2)
	go func() { results <- invoke() }()
	var requests []native.Request
	select {
	case request := <-entered:
		requests = append(requests, request)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { results <- invoke() }()
	// The occupied instance forces the second accepted request to remain queued.
	// Publish only after acceptance, before its eventual sender freeze/dispatch.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for len(runtime.SchedulerInputs(time.Now).Outstanding) != 2 {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	runtime.SetPathBudgets([]serving.PathBudget{next})
	unblock()
	ids := make(map[string]bool, count)
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			ids[result.id] = true
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	requests = append(requests, <-entered)
	for len(requests) < count {
		result := invoke()
		if result.err != nil {
			t.Fatal(result.err)
		}
		ids[result.id] = true
		requests = append(requests, <-entered)
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	to := time.Now().UTC().Add(time.Millisecond)
	read, err := log.Query(ctx, definition.UID, from, to, nil, nil, 0, "")
	if err != nil || len(read.Requests) != count || len(ids) != count || ids[""] {
		t.Fatalf("persisted HTTP final population: rows=%d ids=%d err=%v", len(read.Requests), len(ids), err)
	}
	requirePathSLOPersistedPaths(t, read.Requests, requests, old, next)
	window, err := log.LoadWindow(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	requirePathSLOCapacityConsumers(t, ctx, settings, snapshot, window, count)
}

type pathSLOHTTPReply struct {
	id  string
	err error
}

func (f *pathSLOHTTPFixture) invoke(ctx context.Context) pathSLOHTTPReply {
	body, err := json.Marshal(serving.InferenceInput{Inputs: []serving.RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("sample")}}})
	if err != nil {
		return pathSLOHTTPReply{err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.front.URL+"/api/workloads/workload/infer", bytes.NewReader(body))
	if err != nil {
		return pathSLOHTTPReply{err: err}
	}
	req.Header.Set("Authorization", "Bearer client")
	response, err := f.front.Client().Do(req)
	if err != nil {
		return pathSLOHTTPReply{err: err}
	}
	raw, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
		return pathSLOHTTPReply{err: fmt.Errorf("ingress HTTP %d: %s; read=%v close=%v", response.StatusCode, raw, readErr, closeErr)}
	}
	return pathSLOHTTPReply{id: response.Header.Get("X-Chill-Request-ID")}
}

func requirePathSLOPersistedPaths(t *testing.T, persisted []serving.RequestRow, requests []native.Request, old, next serving.PathBudget) {
	t.Helper()
	rows := make(map[string]serving.RequestRow, len(persisted))
	for _, row := range persisted {
		rows[row.RequestID] = row
		if row.Kind != serving.RequestRowFinal || row.Outcome != "completed" {
			t.Fatalf("HTTP request did not finalize: %+v", row)
		}
		if _, reason := serving.RequestPathSample(&row); reason != "" {
			t.Fatalf("real transport/readback lost usable path evidence: %s %+v", reason, row)
		}
	}
	nativePolicy := pathSLONativePolicy(t)
	for i, request := range requests {
		row := rows[request.ID]
		freeze, present := row.ServerTiming.Elapsed(serving.ServerSenderFreeze)
		if !present || math.Abs(float64(freeze)/float64(time.Millisecond)-request.Context.SenderElapsedMS) > 1e-9 {
			t.Fatalf("sender F diverged from persisted observation: F=%s context=%+v", freeze, request.Context)
		}
		want := next.PostMS
		if i < 2 {
			want = old.PostMS
		}
		if request.Context.CompletionMarginMS != want || row.CompletionMarginMS == nil || *row.CompletionMarginMS != want {
			t.Fatalf("acceptance-frozen POST changed or TOTAL leaked into native charge: native=%+v row=%+v want=%g", request.Context, row.CompletionMarginMS, want)
		}
		// A single POST charge fits, two POST charges do not. Exercise the same
		// native selector used in production with the transported frozen context.
		state := native.RequestState{Context: request.Context, Phase: "queued"}
		state.Context.SLO.TargetMS = state.Context.SenderElapsedMS + want + 10 + want/2
		choice := native.ChooseBatch(nativePolicy, []native.RequestState{state}, nil)
		if choice.Size != 1 {
			t.Fatalf("native selector double charged sender/POST: %+v", choice)
		}
	}
}

func requirePathSLOCapacityConsumers(t *testing.T, ctx context.Context, settings serving.SchedulerSettings, snapshot serving.SchedulerSnapshot, window observation.LoadWindow, count int) {
	t.Helper()
	if len(window.PathBudgets) != 1 {
		t.Fatalf("LoadWindow path summary: %+v", window.PathBudgets)
	}
	budget := window.PathBudgets[0]
	if budget.Source != "observed" || budget.Samples != count || budget.Completed != count || budget.Missing != 0 || budget.JudgmentPercentile != 75 || budget.PostMS < 0 || budget.TotalMS < budget.PostMS {
		t.Fatalf("final rows did not reach the observed bound consumer: %+v", budget)
	}
	snapshot.At, snapshot.ObservationEnd, snapshot.ObservedSince = window.To, window.To, window.From.Add(-time.Hour)
	snapshot.Arrivals, snapshot.PathBudgets = window.Arrivals, window.PathBudgets
	settings.ObservationWindow = window.To.Sub(window.From)
	table := serving.NewCapacityTable(1, nil, nil)
	table.Ensure(ctx, settings, snapshot)
	table.Wait()
	capacityRows, active := table.Inputs()
	if len(capacityRows) != 1 || len(active) != 1 || capacityRows[0].Status != serving.CapacityBuilt || capacityRows[0].Kappa == nil || *capacityRows[0].Kappa <= 0 {
		t.Fatalf("observed path budget did not build usable capacity: rows=%+v budgets=%+v", capacityRows, active)
	}
	if capacityRows[0].Input.CompletionMarginMS != budget.TotalMS || capacityRows[0].Input.PostMarginMS != budget.PostMS {
		t.Fatalf("table numerical inputs lost selected TOTAL/POST: %+v", capacityRows[0].Input)
	}
	snapshot.PathBudgets = active
	for _, strategy := range []string{"v1", "v2"} {
		settings.PlanSearchStrategy = strategy
		decision, err := serving.EvaluateScheduler(ctx, settings, snapshot, capacityRows)
		if err != nil {
			t.Fatal(err)
		}
		if len(decision.Workloads) != 1 || decision.Workloads[0].State != serving.WorkloadControlled || decision.Capacity == nil || len(decision.Capacity.Workloads) != 1 || decision.Capacity.Workloads[0].KappaNew+decision.Capacity.Workloads[0].KappaRetained <= 0 {
			t.Fatalf("%s did not consume observed capacity over infeasible configured fallback: %+v", strategy, decision)
		}
	}
	t.Logf("native HTTP double; finals=%d observed=%d TOTAL=%.6fms POST=%.6fms kappa=%.6f; engines=v1,v2", count, budget.Samples, budget.TotalMS, budget.PostMS, *capacityRows[0].Kappa)
}

func pathSLONativePolicy(t *testing.T) native.Policy {
	t.Helper()
	samples := []native.ServiceSample{{ServiceDurationNanos: int64(10 * time.Millisecond), TrialID: "in-process-native-double"}}
	distribution, err := native.NewServiceDistribution(samples)
	if err != nil {
		t.Fatal(err)
	}
	return native.Policy{DeadlinePolicy: native.DeadlineDrop, Batches: []native.BatchProfile{{Size: 1, EnergyPerItemJ: 1, ServiceSamples: samples, Distribution: distribution}}}
}

// The model follows serving.schedulerFixture's measured-profile contract. All
// evidence identities are synthetic fixture identities, never hardware claims.
func pathSLOConsumerModel(t *testing.T) (serving.SchedulerSettings, serving.SchedulerSnapshot) {
	t.Helper()
	at := time.Now().UTC()
	node := serving.NodeReference{Name: "node", UID: "node"}
	image := "registry/model@sha256:" + strings.Repeat("a", 64)
	nativeSettings := &modelcompat.FirstfloorSettings{MaxBatchSize: 1, MaxQueueSize: 100}
	path := serving.ModelPath{Name: "cpu", Artifact: "model", Files: []string{"/model"}, Format: "onnx", Image: image, RuntimeFamily: "firstfloor", Backend: "ort-cpu", Firstfloor: nativeSettings}
	definition := &serving.Definition{Name: "workload", UID: "uid", Generation: 2,
		Model:        serving.Model{Name: "model", UID: "model", Generation: 1, Paths: []serving.ModelPath{path}},
		Preprocessor: serving.Preprocessor{Name: "pre", Input: []serving.RawField{{Name: "x", Format: "UTF8", MaxBytes: 16}}, Output: []serving.RawField{{Name: "y", Format: "UTF8"}}},
		Dataset:      &serving.DatasetReference{Name: "data", UID: "data"}, SLO: serving.SLO{Percentile: 50, TargetLatency: time.Second}}
	policy := pathSLONativePolicy(t)
	gap := int64(0)
	batch := policy.Batches[0]
	batch.ServiceSamples = append(batch.ServiceSamples, native.ServiceSample{ServiceDurationNanos: int64(10 * time.Millisecond), PrecedingIdleNanos: &gap, TrialID: "in-process-native-double"})
	distribution, err := native.NewServiceDistribution(batch.ServiceSamples)
	if err != nil {
		t.Fatal(err)
	}
	profile := &serving.Profile{ID: "profile", NodeName: node.Name, MeasuredAt: at, IdleBaselineWatts: 12,
		Reference: evidencestore.ObjectIdentity{Store: "fixture", Key: "curve", Size: 1}, Evidence: []evidencestore.ObjectIdentity{{Store: "fixture", Key: "profile", Size: 1}},
		Condition: derivation.CurvePlanSpec{LogicalModel: "model", NodeUID: node.UID, DeviceClass: "cpu", RuntimeFamily: "firstfloor", Backend: "ort-cpu", RuntimeImage: image, Firstfloor: nativeSettings, PowerMode: "fixed", PlannedBatchSizes: []int{1},
			Preprocessor:   derivation.CurvePreprocessorContract{Name: "pre", Inputs: []string{"x"}, Outputs: []string{"y"}},
			ActiveProtocol: derivation.CurveProtocolContract{Dataset: &derivation.CurveDatasetContract{Name: "data", UID: "data"}},
			Artifact:       derivation.CurveArtifactContract{Name: "model", ExecutionPath: "cpu", Files: []string{"/model"}, Image: image}},
		RuntimeMetadata: &native.RuntimeMetadata{Runtime: "onnxruntime", RuntimeVersion: "1.19.0", ModelFiles: []string{"/model/model"}, Inputs: []string{"x"}, Outputs: []string{"y"}, Providers: []string{"CPUExecutionProvider"}, Device: "cpu", CPUAffinity: []int{0}, IntraOpThreads: 1, InterOpThreads: 1, InitializedBatches: []int{1}, WarmupIterations: 1},
		Batches:         []serving.BatchProfile{{Batch: 1, EnergyPerInputJoules: 1, MemoryBytes: 100, ServiceSamples: batch.ServiceSamples, ServiceDistribution: distribution}}}
	instance := serving.SchedulerInstance{ID: "instance", Node: node, Profile: profile, WorkloadUID: definition.UID, Allocation: serving.SchedulerAllocation{MemoryBytes: 1000}, Ready: true, Routable: map[string]bool{definition.UID: true}, Capacity: 100, Weight: 1, MaxBatch: 1, ObservedAt: at, IdleMeter: "wall", IdleScope: "node-exclusive-wall"}
	settings := serving.SchedulerSettings{ObservationWindow: time.Minute, ControlInterval: time.Second, ComparisonPeriod: time.Minute, MaxReplicas: 1, MaxInstances: 1, AutonomousWorkloads: []string{definition.UID}, CompletionMarginMS: 2000,
		CapacityWindow: time.Second, CapacityWindows: 2, CapacityPassingWindows: 1, CapacityMinimumRequests: 32, CapacityGridSteps: 4, CapacityBisectionWidth: .1, CapacityTailSamples: 1,
		PlanSearchStrategy: "v1", PlanSearchWorkers: 1, PlanSearchEvaluations: 100, PlanExactFinalists: 1, PlanPolishActive: 1,
		Controls:             serving.SchedulerControls{Placement: true, Resources: true, Replicas: true, Routing: true, Batching: true, Membership: true},
		DefaultOperationCost: map[string]serving.OperationCostValue{"prepare": {Seconds: 1, Joules: 1}, "remove": {Seconds: 1, Joules: 1}, "power": {Seconds: 1, Joules: 1}}, DefaultIdlePower: &serving.IdlePowerDefault{NodeWatts: 10, PerInstanceWatts: 2}}
	return settings, serving.SchedulerSnapshot{Workloads: []*serving.Definition{definition},
		Nodes: []serving.SchedulerNode{{Node: node, PowerMode: "fixed", Ready: true, MemoryBytes: 10000, ObservedAt: at}}, Instances: []serving.SchedulerInstance{instance},
		Options: []serving.SchedulerOption{{Instance: instance, CompatibleUIDs: []string{definition.UID}}}, Routing: map[string]map[string]int32{definition.UID: {instance.ID: 1}},
		Baselines: []serving.NodeBaseline{{ID: "unloaded", Node: node, PowerMode: "fixed", Watts: 10, Meter: "wall", Scope: "node-exclusive-wall", MeasuredAt: at, Reference: evidencestore.ObjectIdentity{Store: "fixture", Key: "idle", Size: 1}}}}
}

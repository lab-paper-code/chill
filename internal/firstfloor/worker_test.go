package firstfloor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func testPolicy() Policy {
	return Policy{DecisionID: "decision-1", ProfileID: "synthetic-profile", MaxBatchWaitMS: 100, Batches: []BatchProfile{testBatch(1, 2, startupSample("trial-a", 10), pairedSample("trial-a", 1, 10)), testBatch(2, 1, startupSample("trial-a", 12), pairedSample("trial-a", 1, 12))}}
}

// Each fixture row is an actual startup or known-gap exchange in one physical trial.
func startupSample(trial string, durationMS float64) ServiceSample {
	return ServiceSample{TrialID: trial, ServiceDurationNanos: int64(durationMS * float64(time.Millisecond))}
}
func testBatch(size int, energy float64, samples ...ServiceSample) BatchProfile {
	distribution, err := NewServiceDistribution(samples)
	if err != nil {
		panic(err)
	}
	return BatchProfile{Size: size, EnergyPerItemJ: energy, ServiceSamples: samples, Distribution: distribution}
}
func testState(uid string, target float64, rate float64) RequestState {
	return RequestState{Context: RequestContext{WorkloadUID: uid, Definition: uid + "/1", AdmissionPolicy: AdmissionReject, SLO: SLO{Percentile: 95, TargetMS: target}}, ArrivalRate: rate, Phase: "queued"}
}

func TestMeasuredEnergyRemainsNonnegative(t *testing.T) {
	for _, energy := range []float64{-1, 0, 1} {
		policy := testPolicy()
		policy.Batches[0].EnergyPerItemJ = energy
		err := validatePolicy(policy, []int{1, 2})
		if (err != nil) != (energy < 0) {
			t.Fatalf("energy %g: %v", energy, err)
		}
	}
}
func TestMeasuredSelector(t *testing.T) {
	p := testPolicy()
	q := []RequestState{testState("a", 100, 100)}
	choice := ChooseBatch(p, q, nil)
	if choice.Size != 0 || choice.WaitMS != 10 {
		t.Fatalf("strict measured gain with observed fill should wait: %+v", choice)
	}
	capped := p
	capped.MaxBatchSize = 1
	if c := ChooseBatch(capped, q, nil); c.Size != 1 || c.WaitMS != 0 {
		t.Fatalf("mutable cap must forbid energy-gain waits for a larger initialized batch: %+v", c)
	}
	pair := []RequestState{testState("a", 100, 0), testState("a", 100, 0)}
	if c := ChooseBatch(capped, pair, nil); c.Size != 1 {
		t.Fatalf("available energy choice must honor cap: %+v", c)
	}
	pair[0].Context.SLO.TargetMS = 1
	pair[1].Context.SLO.TargetMS = 1
	if c := ChooseBatch(capped, pair, nil); c.Reason != OutcomeDeadlineDrop || c.WaitMS != 0 {
		t.Fatalf("infeasible requests must reject without exceeding the batch cap: %+v", c)
	}
	if e := validatePolicy(capped, []int{1, 2}); e != nil {
		t.Fatal(e)
	}
	invalid := p
	invalid.MaxBatchSize = 3
	if e := validatePolicy(invalid, []int{1, 2}); e == nil {
		t.Fatal("uninitialized/unmeasured cap accepted")
	}
	invalid.MaxBatchSize = -1
	if e := validatePolicy(invalid, []int{1, 2}); e == nil {
		t.Fatal("negative cap accepted")
	}
	equal := p
	equal.Batches = []BatchProfile{testBatch(1, 1, startupSample("trial-a", 10)), testBatch(2, 1, startupSample("trial-a", 12))}
	if c := ChooseBatch(equal, q, nil); c.Size != 1 || c.WaitMS != 0 {
		t.Fatalf("equal energy must not wait: %+v", c)
	}
	q[0].ArrivalRate = 0
	if c := ChooseBatch(p, q, nil); c.Size != 1 {
		t.Fatalf("no actual arrival rate must dispatch: %+v", c)
	}
	q[0].ArrivalRate = 100
	q = append(q, testState("b", 15, 0))
	if c := ChooseBatch(p, q, nil); c.WorkloadUID != "b" || c.Size != 1 || c.WaitMS != 0 {
		t.Fatalf("urgent other UID must prevent wait: %+v", c)
	}
	q = []RequestState{testState("a", 100, 0), testState("b", 100, 0)}
	if c := ChooseBatch(p, q, nil); c.Size != 1 {
		t.Fatalf("UIDs must never form combined batch: %+v", c)
	}
	q = []RequestState{testState("a", 1, 100)}
	if c := ChooseBatch(p, q, nil); c.Reason != OutcomeDeadlineDrop || c.WaitMS != 0 {
		t.Fatalf("late Reject request must not execute: %+v", c)
	}
	samples := p
	samples.Batches = []BatchProfile{testBatch(1, 2, startupSample("trial-a", 1), startupSample("trial-b", 100)), testBatch(2, 1, startupSample("trial-a", 2), startupSample("trial-b", 101))}
	q = []RequestState{testState("a", 50, 100)}
	q[0].Context.SLO.Percentile = 50
	if c := ChooseBatch(samples, q, nil); c.WaitMS == 0 {
		t.Fatalf("empirical p50 should leave slack: %+v", c)
	}
	q[0].Context.SLO.Percentile = 100
	if c := ChooseBatch(samples, q, nil); c.Reason != OutcomeDeadlineDrop || c.WaitMS != 0 {
		t.Fatalf("empirical p100 must not borrow p50 slack: %+v", c)
	}
	q = []RequestState{testState("a", 50, 100)}
	q[0].Context.SenderElapsedMS = 25
	q[0].ElapsedMS = 10
	q[0].Context.CompletionMarginMS = 6
	if c := ChooseBatch(p, q, nil); c.WaitMS != 0 {
		t.Fatalf("sender, local and completion margin all consume original slack: %+v", c)
	}
}
func TestCompletionMarginIsReservedOnceRegardlessOfSenderElapsed(t *testing.T) {
	p := testPolicy()
	p.DeadlinePolicy = DeadlineDrop
	p.Batches = []BatchProfile{testBatch(1, 1, startupSample("trial-a", 100))}
	q := testState("uid", 507, 0)
	q.Context.SenderElapsedMS, q.Context.CompletionMarginMS = 400, 7
	var costs serviceCosts
	if slack := requestSlack(q, p.Batches[0], nil, &costs); slack != 0 {
		t.Fatalf("400ms sender + 100ms service + 7ms margin must fit 507ms exactly: %v", slack)
	}
	if c := ChooseBatch(p, []RequestState{q}, nil); c.Size != 1 || c.Reason == OutcomeDeadlineDrop {
		t.Fatalf("feasible deadline must execute: %+v", c)
	}
	for _, sender := range []float64{0, 400, 500} {
		wire := Request{ID: "wire", Context: RequestContext{RequestID: "wire", WorkloadUID: "uid", Definition: "uid/1", AdmissionPolicy: AdmissionReject, ReceivedAt: time.Now().Add(time.Hour).UTC(), SenderElapsedMS: sender, CompletionMarginMS: 7, LifetimeMS: 507, SLO: SLO{Percentile: 95, TargetMS: 507}}, Inputs: rawInputs("sample")}
		body, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		cfg := &Config{Mode: modeServing, Inputs: []string{"input"}, Outputs: []string{"output"}, Policy: p}
		r := &request{received: time.Now()}
		w := httptest.NewRecorder()
		accepted := prepareInference(w, context.Background(), json.NewDecoder(bytes.NewReader(body)), cfg, r)
		if sender == 500 {
			if accepted || w.Code != http.StatusRequestTimeout {
				t.Fatalf("lifetime consumed by sender and margin was accepted: %d", w.Code)
			}
			continue
		}
		if !accepted {
			t.Fatalf("feasible lifetime rejected: %d %s", w.Code, w.Body.String())
		}
		want := time.Duration(507-sender-7) * time.Millisecond
		if got := r.lifetime.Sub(r.received); got != want {
			t.Fatalf("lifetime must reserve the full margin once: got %s want %s", got, want)
		}
	}
}
func withDeadline(policy string) func(*Config) {
	return func(c *Config) { c.Policy.DeadlinePolicy = policy }
}
func startTestActor(t *testing.T, mode string, delay string, adjust func(*Config)) (*Scheduler, *Config) {
	t.Helper()
	cfg := &Config{Model: "synthetic", Mode: mode, Inputs: []string{"input"}, Outputs: []string{"output"}, RunnerCommand: []string{"python3", "../../runtime/firstfloor/runner.py", "--echo", "--preprocessor", "../../runtime/firstfloor/testdata/echo", "--delay-ms", delay, "--warmup-iterations", "1"}, RunnerTimeoutMS: 3000, MaxQueueRequests: 8, MaxQueueBytes: 1 << 20, MaxRequestBytes: 1 << 20, MaxBatchSize: 2, BatchSizes: []int{1, 2}, Policy: testPolicy()}
	if mode == "profiling" {
		cfg.BatchSizes = []int{2}
		cfg.Policy = Policy{FixedBatchSize: 2}
	}
	if adjust != nil {
		adjust(cfg)
	}
	if e := ValidateConfig(cfg); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner, e := StartRunner(ctx, cfg)
	if e != nil {
		cancel()
		t.Fatal(e)
	}
	s := NewScheduler(ctx, cfg, runner)
	t.Cleanup(func() {
		cancel()
		select {
		case <-s.Done():
		case <-time.After(5 * time.Second):
			t.Error("actor did not terminate/reap runner")
		}
	})
	return s, cfg
}

// The echo fixture copies field "input" to field "output"; its units are one plus the count of '+'.
func rawInputs(values ...string) []Field {
	return []Field{{Name: "input", Data: elements(values)}}
}
func echoOutputs(values ...string) []Field {
	return []Field{{Name: "output", Data: elements(values)}}
}
func elements(values []string) [][]byte {
	data := make([][]byte, len(values))
	for i, v := range values {
		data[i] = []byte(v)
	}
	return data
}
func actorRequest(t *testing.T, cfg *Config, id, uid string, target float64) *request {
	t.Helper()
	d, n, e := prepareInputs(cfg.Inputs, rawInputs("sample"), 1)
	if e != nil {
		t.Fatal(e)
	}
	return &request{received: time.Now(), context: RequestContext{RequestID: id, WorkloadUID: uid, Definition: uid + "/1", AdmissionPolicy: AdmissionDelay, ReceivedAt: time.Now().UTC(), SLO: SLO{Percentile: 95, TargetMS: target}}, data: d, bytes: n}
}
func postJSON(h http.Handler, path string, v any) *httptest.ResponseRecorder {
	body, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
	return recorder
}
func awaitActorState(t *testing.T, s *Scheduler, want func(State) bool) State {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		state, e := s.State(ctx)
		cancel()
		if e != nil {
			t.Fatal(e)
		}
		if want(state) {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("actor state did not reach expected physical phase")
	return State{}
}
func TestLocalActorCancellationSnapshotsAndTypedOutputs(t *testing.T) {
	s, cfg := startTestActor(t, "serving", "250", nil)
	activeCtx, cancelActive := context.WithCancel(context.Background())
	activeReply := make(chan result, 1)
	r := actorRequest(t, cfg, "active", "a", 1)
	go func() { activeReply <- s.submit(activeCtx, r) }()
	awaitActorState(t, s, func(state State) bool { return len(state.Requests) == 1 && state.Requests[0].Phase == phaseRunning })
	next := *cfg
	next.Policy.DecisionID = "decision-2"
	next.Policy.ProfileID = "relabelled-profile"
	next.Policy.MaxBatchSize = 1
	if e := s.Update(context.Background(), &next); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.ProfilePath = "/different/profile.json" },
		func(c *Config) { c.Model = "different-model" },
		func(c *Config) { c.MaxQueueRequests++ },
	} {
		invalid := next
		change(&invalid)
		if err := s.Update(context.Background(), &invalid); err == nil {
			t.Fatal("immutable startup execution change was accepted")
		}
	}
	cancelActive()
	reply := <-activeReply
	if !errors.Is(reply.err, context.Canceled) || reply.complete {
		t.Fatalf("running cancellation is not physical completion: %+v", reply)
	}
	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	queuedReply := make(chan result, 1)
	q := actorRequest(t, cfg, "queued", "b", 100)
	go func() { queuedReply <- s.submit(queuedCtx, q) }()
	state := awaitActorState(t, s, func(state State) bool { return len(state.Requests) == 2 })
	if state.Metadata.DecisionID != "decision-2" || state.Metadata.Policy.MaxBatchSize != 1 || state.Requests[0].Phase != "queued" || state.Requests[1].DecisionID != "decision-1" || state.Requests[1].Context.SLO.TargetMS != 1 {
		t.Fatalf("active frozen policy/SLO and applied metadata must stay independent: %+v", state)
	}
	cancelQueued()
	<-queuedReply
	state = awaitActorState(t, s, func(state State) bool { return len(state.Requests) == 1 && state.CompletedRequests == 1 })
	if state.Requests[0].Context.RequestID != "active" || state.Requests[0].Phase != phaseRunning {
		t.Fatalf("queued cancellation must not release active occupancy: %+v", state)
	}
	awaitActorState(t, s, func(state State) bool { return len(state.Requests) == 0 && state.CompletedRequests == 2 })
	fresh := actorRequest(t, cfg, "late", "a", 1)
	reply = s.submit(context.Background(), fresh)
	if p := reply.response.Progress; reply.err != nil || !reply.response.ExecutionComplete || p.BatchSize != 1 || p.ServiceMS < 100 || p.BackendMS < 100 || p.Units != 1 || p.PreprocessMS+p.BackendMS+p.PostprocessMS > p.ServiceMS || p.FirstTokenMS != 0 || !reflect.DeepEqual(reply.response.Outputs, echoOutputs("sample")) {
		t.Fatalf("late SLO still returns every exact raw result: %+v", reply)
	}
	state = awaitActorState(t, s, func(state State) bool { return state.CompletedRequests == 3 })
	if state.Executions != 2 || state.AcceptedRequests != 3 {
		t.Fatalf("cancelled queued request cannot count as physical execution: %+v", state)
	}
}
func TestGenerativeProfilingInferReportsFirstToken(t *testing.T) {
	s, _ := startTestActor(t, modeProfiling, "0", func(c *Config) { c.MaxNewTokens = 4 })
	h := NewHandler(s)
	for _, reply := range postFixedInference(h, "a", "b") {
		var response Response
		if reply.Code != http.StatusOK || json.Unmarshal(reply.Body.Bytes(), &response) != nil || response.Progress.FirstTokenMS > response.Progress.BackendMS {
			t.Fatalf("generative inference must report its first-token span: %d %s", reply.Code, reply.Body.String())
		}
	}
	for _, reply := range postFixedInference(h, "a", "two-first-tokens") {
		if reply.Code != http.StatusInternalServerError {
			t.Fatalf("second first-token callback must end the batch: %d %s", reply.Code, reply.Body.String())
		}
	}
}
func TestItemFailureAndBatchErrorHTTP(t *testing.T) {
	s, _ := startTestActor(t, "serving", "0", nil)
	h := NewHandler(s)
	send := func(id, value string) *httptest.ResponseRecorder {
		return postJSON(h, "/v2/models/synthetic/infer", Request{ID: id, Context: RequestContext{RequestID: id, WorkloadUID: "uid", Definition: "uid/1", AdmissionPolicy: AdmissionDelay, ReceivedAt: time.Now().UTC(), SLO: SLO{Percentile: 95, TargetMS: 1000}}, Inputs: rawInputs(value)})
	}
	for _, test := range []struct {
		value, outcome, reason string
		status                 int
	}{{"fail:bad jpeg", OutcomePreprocessFailed, "bad jpeg", http.StatusUnprocessableEntity}, {"raise", OutcomeBatchError, "", http.StatusInternalServerError}} {
		reply := send(test.outcome, test.value)
		var failure ErrorResponse
		if reply.Code != test.status || json.Unmarshal(reply.Body.Bytes(), &failure) != nil || !failure.ExecutionComplete || failure.Progress == nil || failure.Progress.Outcome != test.outcome || failure.Progress.Phase != phaseFinished || failure.Progress.BatchID == 0 || test.reason != "" && failure.Error != test.reason {
			t.Fatalf("%s: %d %s", test.value, reply.Code, reply.Body.String())
		}
	}
	reply := send("ok", "tile+tile")
	var response Response
	if reply.Code != http.StatusOK || json.Unmarshal(reply.Body.Bytes(), &response) != nil || response.Progress.Units != 2 || response.Progress.Outcome != outcomeCompleted || !reflect.DeepEqual(response.Outputs, echoOutputs("tile+tile")) {
		t.Fatalf("runner must keep serving after a batch error: %d %s", reply.Code, reply.Body.String())
	}
	state := awaitActorState(t, s, func(state State) bool { return state.CompletedRequests == 3 })
	if state.Executions != 3 || len(state.Requests) != 0 {
		t.Fatalf("item and batch failures are physical executions: %+v", state)
	}
}
func TestNativeAllocationFailureHTTPPreservesCategoryAndServing(t *testing.T) {
	for _, mode := range []string{modeServing, modeProfiling} {
		t.Run(mode, func(t *testing.T) {
			s, _ := startTestActor(t, mode, "0", func(c *Config) {
				if mode == modeProfiling {
					c.BatchSizes = []int{1}
					c.Policy.FixedBatchSize = 1
				}
			})
			h := NewHandler(s)
			send := func(value string) *httptest.ResponseRecorder {
				return postJSON(h, "/v2/models/synthetic/infer", Request{
					ID: value, Context: RequestContext{RequestID: value, WorkloadUID: "uid", Definition: "uid/1", AdmissionPolicy: AdmissionDelay,
						ReceivedAt: time.Now().UTC(), SLO: SLO{Percentile: 95, TargetMS: 1000}}, Inputs: rawInputs(value)})
			}
			for _, tc := range []struct{ value, reason string }{
				{"allocation-failure", FailureMemoryAllocation}, {"ort-shape-failure", ""}, {"fake-allocation-text", ""},
			} {
				reply := send(tc.value)
				var failure ErrorResponse
				if reply.Code != http.StatusInternalServerError || json.Unmarshal(reply.Body.Bytes(), &failure) != nil ||
					failure.FailureReason != tc.reason || !failure.ExecutionComplete || failure.Error == "" {
					t.Fatalf("native category lost: %d %s", reply.Code, reply.Body.String())
				}
				if failure.Progress == nil || failure.Progress.Outcome != OutcomeBatchError {
					t.Fatalf("inference completion lost: %+v", failure)
				}
			}
			if reply := send("ok"); reply.Code != http.StatusOK {
				t.Fatalf("batch failure poisoned runner: %d %s", reply.Code, reply.Body.String())
			}
		})
	}
}
func TestWorkerHTTPBoundaries(t *testing.T) {
	s, _ := startTestActor(t, "serving", "4", nil)
	h := NewHandler(s)
	invoke := func(path string, body []byte) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
		return recorder
	}
	fieldless := Request{ID: "fields", Context: RequestContext{RequestID: "fields", WorkloadUID: "uid", Definition: "uid/1", AdmissionPolicy: AdmissionReject, ReceivedAt: time.Now().UTC(), SLO: SLO{Percentile: 95, TargetMS: 100}}, Inputs: []Field{{Name: "other", Data: [][]byte{[]byte("x")}}}}
	wrongField, e := json.Marshal(fieldless)
	if e != nil {
		t.Fatal(e)
	}
	for _, body := range [][]byte{[]byte(`{"id":"bad","inputs":[]}`), []byte(`{"unexpected":true}`), []byte(`null`), wrongField} {
		reply := invoke("/v2/models/synthetic/infer", body)
		var failure ErrorResponse
		if reply.Code != http.StatusBadRequest || json.Unmarshal(reply.Body.Bytes(), &failure) != nil || !failure.ExecutionComplete {
			t.Fatalf("pre-submission rejection must prove no execution: %d %s", reply.Code, reply.Body.String())
		}
	}
	request := Request{ID: "wire", Context: RequestContext{RequestID: "wire", WorkloadUID: "wire-uid", Definition: "wire-uid/1", AdmissionPolicy: AdmissionDelay, ReceivedAt: time.Now().Add(time.Hour).UTC(), SenderElapsedMS: 1000, SLO: SLO{Percentile: 99, TargetMS: 1}}, Inputs: rawInputs("sample")}
	body, e := json.Marshal(request)
	if e != nil {
		t.Fatal(e)
	}
	reply := invoke("/v2/models/synthetic/infer", body)
	var response Response
	if reply.Code != http.StatusOK || json.Unmarshal(reply.Body.Bytes(), &response) != nil || !response.ExecutionComplete || response.ID != "wire" || !reflect.DeepEqual(response.Outputs, echoOutputs("sample")) {
		t.Fatalf("remote UTC must not replace elapsed or turn SLO into lifetime; exact raw wire response required: %d %s", reply.Code, reply.Body.String())
	}
	request.ID = "expired"
	request.Context.RequestID = request.ID
	request.Context.LifetimeMS = 10
	body, e = json.Marshal(request)
	if e != nil {
		t.Fatal(e)
	}
	reply = invoke("/v2/models/synthetic/infer", body)
	var failure ErrorResponse
	if reply.Code != http.StatusRequestTimeout || json.Unmarshal(reply.Body.Bytes(), &failure) != nil || !failure.ExecutionComplete {
		t.Fatalf("explicit expired lifetime must reject before submission: %d %s", reply.Code, reply.Body.String())
	}
	state := awaitActorState(t, s, func(state State) bool { return state.CompletedRequests == 1 })
	if state.AcceptedRequests != 1 || state.Executions != 1 {
		t.Fatalf("rejections cannot create native execution: %+v", state)
	}
}

func TestWorkerBodyFailureStatus(t *testing.T) {
	for _, mode := range []string{modeServing, modeProfiling} {
		t.Run(mode, func(t *testing.T) {
			s, cfg := startTestActor(t, mode, "0", nil)
			h := NewHandler(s)
			path := "/v2/models/synthetic/infer"
			oversized := bytes.Repeat([]byte{' '}, cfg.MaxRequestBytes+1)
			oversized[0] = '{'
			trailingOversized := bytes.Repeat([]byte{' '}, cfg.MaxRequestBytes+1)
			copy(trailingOversized, "{}")
			for _, test := range []struct {
				name     string
				body     []byte
				canceled bool
				status   int
			}{
				{"malformed", []byte("{"), false, http.StatusBadRequest},
				{"canceled", []byte("{"), true, http.StatusRequestTimeout},
				{"oversized-canceled", oversized, true, http.StatusRequestEntityTooLarge},
				{"oversized-trailing", trailingOversized, false, http.StatusRequestEntityTooLarge},
			} {
				t.Run(test.name, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(test.body))
					if test.canceled {
						ctx, cancel := context.WithCancel(req.Context())
						cancel()
						req = req.WithContext(ctx)
					}
					reply := httptest.NewRecorder()
					h.ServeHTTP(reply, req)
					var failure ErrorResponse
					if reply.Code != test.status || json.Unmarshal(reply.Body.Bytes(), &failure) != nil || !failure.ExecutionComplete {
						t.Fatalf("body rejection must preserve status and prove no execution: %d %s", reply.Code, reply.Body.String())
					}
				})
			}
			state := awaitActorState(t, s, func(state State) bool { return state.AcceptedRequests == 0 })
			if state.Executions != 0 {
				t.Fatalf("body failures must not execute: %+v", state)
			}
		})
	}
}

func TestNativeLifetimeWitnessKeepsAcceptedDeadlineAcrossPolicyChange(t *testing.T) {
	s, cfg := startTestActor(t, "serving", "250", nil)
	r := actorRequest(t, cfg, "lifetime", "uid", 100)
	deadline := time.Now().Add(2 * time.Second)
	r.lifetime = deadline
	result := make(chan result, 1)
	go func() { result <- s.submit(context.Background(), r) }()
	awaitActorState(t, s, func(state State) bool { return len(state.Requests) == 1 && state.Requests[0].Phase == phaseRunning })
	next := *cfg
	next.Policy.DecisionID = "changed-policy"
	next.Policy.MaxBatchWaitMS = 1
	if err := s.Update(context.Background(), &next); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	state, err := s.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	remaining := state.Requests[0].RemainingLifetimeMS
	lower, upper := milliseconds(deadline.Sub(after)), milliseconds(deadline.Sub(before))
	if remaining == nil || *remaining < lower || *remaining > upper {
		t.Fatalf("accepted deadline changed with mutable policy: remaining=%v bounds=[%g,%g]", remaining, lower, upper)
	}
	if completed := <-result; completed.err != nil || !completed.complete {
		t.Fatalf("physical execution: %+v", completed)
	}
}

func TestLateQueueMaximumThroughput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		costs []float64
		cap   int
		want  int
	}{
		// Rounded jetsonx-012-mnv2-cuda-q1-15w6c profile durations (ms).
		{"late_head_eight", 8, []float64{31, 50, 92, 172}, 0, 8},
		{"late_head_four", 4, []float64{31, 50, 92, 172}, 0, 4},
		{"partial_queue", 3, []float64{31, 50, 92, 172}, 0, 2},
		{"single_item_cheaper", 8, []float64{31, 80, 160, 320}, 0, 1},
		{"equal_cost_larger_batch", 8, []float64{31, 62, 124, 248}, 0, 8},
		{"maximum_batch_cap", 8, []float64{31, 50, 92, 172}, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testPolicy()
			p.MaxBatchSize = tc.cap
			p.Batches = nil
			for i, size := range []int{1, 2, 4, 8} {
				p.Batches = append(p.Batches, testBatch(size, 1, startupSample("trial-a", tc.costs[i])))
			}
			queue := make([]RequestState, tc.count)
			for i := range queue {
				queue[i] = testState("a", 1000, 100)
				queue[i].ElapsedMS = 1100
				queue[i].Phase = phaseDeferred
			}
			if c := ChooseBatch(p, queue, nil); c.Size != tc.want || c.WaitMS != 0 || c.Reason != ReasonMaximumThroughput {
				t.Fatalf("late queue must minimize service cost per item: want size %d, got %+v", tc.want, c)
			}
		})
	}
}

func TestDeadlineSelectorModesAndOrdering(t *testing.T) {
	p := testPolicy()
	p.Batches = []BatchProfile{
		testBatch(1, 3, startupSample("trial-a", 10)),
		testBatch(2, 2, startupSample("trial-a", 10)),
		testBatch(3, 1, startupSample("trial-a", 30)),
	}
	late := []RequestState{testState("a", 1, 100), testState("a", 1, 100), testState("a", 1, 100)}
	for i := range late {
		late[i].Context.RequestID = string(rune('a' + i))
	}
	for i := range late {
		late[i].Phase = phaseDeferred
	}
	if c := ChooseBatch(p, late, nil); c.Size != 2 || c.WaitMS != 0 {
		t.Fatalf("deferred work must run maximum throughput among fillable batches: %+v", c)
	}
	for _, policy := range []string{AdmissionReject, AdmissionDelay} {
		for i := range late {
			late[i].Phase = phaseQueued
			late[i].Context.AdmissionPolicy = policy
		}
		c := ChooseBatch(p, late, nil)
		reason := OutcomeDeadlineDrop
		if policy == AdmissionDelay {
			reason = ReasonDeadlineDefer
		}
		if c.RequestID != "a" || c.Size != 0 || c.WaitMS != 0 || c.Reason != reason {
			t.Fatalf("infeasible head must receive its request policy transition (%s): %+v", policy, c)
		}
	}
	for i := range late {
		late[i].Context.AdmissionPolicy = AdmissionReject
	}
	running := testState("running", 1, 0)
	running.Phase = phaseRunning
	if c := ChooseBatch(p, append(late, running), nil); c.Size != 0 || c.RequestID != "" {
		t.Fatalf("infeasible prediction is idle-only: %+v", c)
	}
	late[1].ElapsedMS = 1
	if c := ChooseBatch(p, append(late, running), nil); c.RequestID != "b" || c.Reason != "deadline_drop" {
		t.Fatalf("expired queued work drops even during an active batch: %+v", c)
	}
	for i := range late {
		late[i].Context.AdmissionPolicy = AdmissionDelay
	}
	if c := ChooseBatch(p, append(late, running), nil); c.RequestID != "" || c.Size != 0 {
		t.Fatalf("defer does not demote while another batch is active: %+v", c)
	}
	for i := range late {
		late[i].Phase = phaseDeferred
	}
	late[1].Context.WorkloadUID = "other"
	if c := ChooseBatch(p, late, nil); c.Size != 1 || c.RequestID != "a" {
		t.Fatalf("deferred FIFO cannot skip an incompatible entry: %+v", c)
	}
	normal := testState("normal", 100, 0)
	if c := ChooseBatch(p, append(late, normal), nil); c.WorkloadUID != "normal" {
		t.Fatalf("normal work precedes every deferred request: %+v", c)
	}
	normal.Context.RequestID = "first"
	equal := normal
	equal.Context.RequestID = "second"
	equal.Context.Definition = "another-definition"
	if c := ChooseBatch(p, []RequestState{normal, equal}, nil); c.RequestID != "first" || c.Size != 1 {
		t.Fatalf("stable EDF keeps arrival ties and definitions separate: %+v", c)
	}
	p.DeadlinePolicy = DeadlineDrop
	p.Batches = []BatchProfile{testBatch(1, 1, startupSample("trial-a", 1), startupSample("trial-b", 100))}
	a, b := testState("a", 50, 0), testState("b", 60, 0)
	a.Context.SLO.Percentile = 50
	b.Context.SLO.Percentile = 100
	if c := ChooseBatch(p, []RequestState{b, a}, nil); c.WorkloadUID != "a" || c.Size != 1 {
		t.Fatalf("unrelated infeasible workload cannot reject a feasible EDF head: %+v", c)
	}
	a.Context.SenderElapsedMS, a.ElapsedMS = 20, 30
	a.Context.ReceivedAt = time.Now().Add(time.Hour)
	if RemainingSLOMS(a) != 0 || !SLOExpired(a) {
		t.Fatal("frozen sender/local spans alone must determine SLO expiration")
	}
}

func TestDeadlineActorModesAndActiveNonpreemption(t *testing.T) {
	for _, mode := range []string{AdmissionReject, AdmissionDelay} {
		t.Run(mode, func(t *testing.T) {
			s, cfg := startTestActor(t, modeServing, "250", withDeadline(DeadlineKeep))
			active := actorRequest(t, cfg, "active", "a", 1000)
			activeReply := make(chan result, 1)
			go func() { activeReply <- s.submit(context.Background(), active) }()
			awaitActorState(t, s, func(state State) bool {
				return len(state.Requests) == 1 && state.Requests[0].Phase == phaseRunning
			})
			queued := actorRequest(t, cfg, "queued", "b", 30)
			queued.context.AdmissionPolicy = mode
			queuedReply := make(chan result, 1)
			go func() { queuedReply <- s.submit(context.Background(), queued) }()
			if mode == AdmissionReject {
				reply := <-queuedReply
				p := reply.response.Progress
				if !errors.Is(reply.err, ErrDeadlineInfeasible) || !reply.complete || p.Outcome != "deadline_drop" || p.Phase != phaseFinished || !p.ExecutionComplete || p.BatchID != 0 || p.BatchSize != 0 || p.ServiceMS != 0 {
					t.Fatalf("queued expiry must prove a finished non-execution: %+v", reply)
				}
				state := awaitActorState(t, s, func(state State) bool { return state.CompletedRequests == 1 })
				if len(state.Requests) != 1 || state.Requests[0].Phase != phaseRunning {
					t.Fatalf("expired queued drop must not preempt active work: %+v", state)
				}
			}
			next := *cfg
			next.Policy.DeadlinePolicy = DeadlineDrop
			if err := s.Update(context.Background(), &next); err != nil {
				t.Fatalf("serving provenance policy switch must not require restart: %v", err)
			}
			if reply := <-activeReply; reply.err != nil || !reply.complete {
				t.Fatalf("active batch must finish normally: %+v", reply)
			}
			if mode != AdmissionReject {
				reply := <-queuedReply
				if reply.err != nil || !reply.complete || reply.response.Progress.Outcome != outcomeCompleted || !reflect.DeepEqual(reply.response.Outputs, echoOutputs("sample")) {
					t.Fatalf("keep/defer overdue work must eventually return exact success: %+v", reply)
				}
			}
			state := awaitActorState(t, s, func(state State) bool { return state.CompletedRequests == 2 && len(state.Requests) == 0 })
			wantExecutions := uint64(2)
			if mode == AdmissionReject {
				wantExecutions = 1
			}
			if state.AcceptedRequests != 2 || state.Executions != wantExecutions {
				t.Fatalf("drop/defer lifecycle counters must match physical work: %+v", state)
			}
			if mode == AdmissionReject {
				active = actorRequest(t, cfg, "late-active", "a", 30)
				reply := s.submit(context.Background(), active)
				if reply.err != nil || reply.response.Progress.Outcome != outcomeCompleted {
					t.Fatalf("drop mode must return late success once the batch is active: %+v", reply)
				}
			}
		})
	}
}

func TestDeferredQueueSharesBoundsCancellationAndState(t *testing.T) {
	now := time.Now()
	cfg := &Config{Model: "model", MaxQueueRequests: 1, MaxQueueBytes: 8, Policy: testPolicy()}
	cfg.Policy.DeadlinePolicy = DeadlineDefer
	ctx, cancel := context.WithCancel(context.Background())
	r := &request{ctx: ctx, received: now, context: RequestContext{RequestID: "deferred", SLO: SLO{TargetMS: 1}}, bytes: 8, result: make(chan result, 1), lifetime: now.Add(time.Second)}
	a := schedulerActor{cfg: cfg, deferred: []*request{r}, queuedBytes: 8, accepted: 1}
	states := a.requestStates(now)
	if len(states) != 1 || states[0].Phase != phaseDeferred || states[0].RemainingLifetimeMS == nil {
		t.Fatalf("deferred snapshot retains frozen context and lifetime: %+v", states)
	}
	rejected := &request{ctx: context.Background(), bytes: 1, result: make(chan result, 1)}
	a.admit(rejected)
	if reply := <-rejected.result; !errors.Is(reply.err, ErrQueueFull) {
		t.Fatalf("deferred work must share normal count and byte limits: %+v", reply)
	}
	cancel()
	a.expireQueued(now)
	if reply := <-r.result; !errors.Is(reply.err, context.Canceled) || !reply.complete {
		t.Fatalf("deferred cancellation must physically release the request: %+v", reply)
	}
	if len(a.deferred) != 0 || a.queuedBytes != 0 || a.completed != 1 {
		t.Fatalf("deferred cancellation leaked capacity or counters: %+v", a)
	}
}

func TestDeadlinePolicyValidationIncludesProfiling(t *testing.T) {
	cfg := &Config{Model: "profile", Mode: modeProfiling, Inputs: []string{"x"}, Outputs: []string{"y"}, RunnerCommand: []string{"runner"}, RunnerTimeoutMS: 1000, MaxQueueRequests: 2, MaxQueueBytes: 32, MaxRequestBytes: 32, MaxBatchSize: 2, BatchSizes: []int{2}, Policy: Policy{FixedBatchSize: 2}}
	if err := ValidateConfig(cfg); err != nil || cfg.Policy.DeadlinePolicy != DeadlineKeep {
		t.Fatalf("omitted mode must normalize to keep even in profiling: %v %+v", err, cfg)
	}
	cfg.Policy.DeadlinePolicy = "invalid"
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("profiling accepted an invalid deadline policy")
	}
	cfg.Policy.DeadlinePolicy = DeadlineKeep
	cfg.Inputs = []string{"x", "x"}
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("duplicate raw input field accepted")
	}
}

func TestWorkerDeadlineDropHTTP(t *testing.T) {
	s, _ := startTestActor(t, modeServing, "0", withDeadline(DeadlineDrop))
	request := Request{ID: "expired-slo", Context: RequestContext{RequestID: "expired-slo", WorkloadUID: "uid", Definition: "uid/1", AdmissionPolicy: AdmissionReject, ReceivedAt: time.Now().Add(time.Hour).UTC(), SenderElapsedMS: 100, SLO: SLO{Percentile: 95, TargetMS: 1}}, Inputs: rawInputs("sample")}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	NewHandler(s).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v2/models/synthetic/infer", bytes.NewReader(body)))
	var failure ErrorResponse
	if recorder.Code != http.StatusServiceUnavailable || json.Unmarshal(recorder.Body.Bytes(), &failure) != nil || !failure.ExecutionComplete ||
		failure.Code != CodeDeadlineInfeasible || failure.Stage != AdmissionStageExecutor || failure.Policy != AdmissionReject ||
		failure.RemainingSLONanos == nil || *failure.RemainingSLONanos >= 0 || failure.ExecutorWaitNanos == nil ||
		failure.Progress == nil || failure.Progress.BatchID != 0 {
		t.Fatalf("SLO rejection must return structured non-execution HTTP 503: %d %s", recorder.Code, recorder.Body.String())
	}
	state := awaitActorState(t, s, func(state State) bool { return state.CompletedRequests == 1 })
	if state.AcceptedRequests != 1 || state.Executions != 0 || len(state.Requests) != 0 {
		t.Fatalf("HTTP SLO drop must release accepted work without execution: %+v", state)
	}
}

func TestFixedBatchActorServesExactlyB(t *testing.T) {
	cfg := &Config{Model: "synthetic", Mode: modeServing, Inputs: []string{"input"}, Outputs: []string{"output"}, RunnerCommand: []string{"python3", "../../runtime/firstfloor/runner.py", "--echo", "--preprocessor", "../../runtime/firstfloor/testdata/echo", "--delay-ms", "4", "--warmup-iterations", "1"}, RunnerTimeoutMS: 3000, MaxQueueRequests: 3, MaxQueueBytes: 1 << 20, MaxRequestBytes: 1 << 20, MaxBatchSize: 2, BatchSizes: []int{1, 2}, Policy: testPolicy()}
	cfg.Policy.FixedBatchSize, cfg.Policy.DeadlinePolicy = 2, DeadlineDrop
	if e := ValidateConfig(cfg); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner, e := StartRunner(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	s := NewScheduler(ctx, cfg, runner)
	submit := func(ctx context.Context, r *request) chan result {
		reply := make(chan result, 1)
		go func() { reply <- s.submit(ctx, r) }()
		return reply
	}
	late := submit(context.Background(), actorRequest(t, cfg, "late", "a", 1))
	cancelled, cancelQueued := context.WithCancel(context.Background())
	cancelledReply := submit(cancelled, actorRequest(t, cfg, "cancelled", "b", 100))
	expiring := actorRequest(t, cfg, "expiring", "c", 100)
	expiring.lifetime = time.Now().Add(50 * time.Millisecond)
	expiringReply := submit(context.Background(), expiring)
	state := awaitActorState(t, s, func(state State) bool { return len(state.Requests) == 3 })
	if state.Metadata.Policy.FixedBatchSize != 2 || state.Executions != 0 {
		t.Fatalf("B-1 compatible requests must wait without SLO drop: %+v", state)
	}
	if reply := s.submit(context.Background(), actorRequest(t, cfg, "full", "a", 100)); !errors.Is(reply.err, ErrQueueFull) {
		t.Fatalf("queue count cap must reject: %+v", reply)
	}
	cancelQueued()
	if reply := <-cancelledReply; !errors.Is(reply.err, context.Canceled) {
		t.Fatalf("cancelled queued request must be removed: %+v", reply)
	}
	if reply := <-expiringReply; !errors.Is(reply.err, context.DeadlineExceeded) || !reply.complete {
		t.Fatalf("explicit lifetime must remove queued request: %+v", reply)
	}
	second := submit(context.Background(), actorRequest(t, cfg, "second", "a", 100))
	for _, reply := range []chan result{late, second} {
		if r := <-reply; r.err != nil || r.response.Progress.BatchSize != 2 || r.response.Progress.Outcome != outcomeCompleted {
			t.Fatalf("B compatible requests must run as exactly B, SLO-expired included: %+v", r)
		}
	}
	tail := submit(context.Background(), actorRequest(t, cfg, "tail", "a", 1))
	awaitActorState(t, s, func(state State) bool { return len(state.Requests) == 1 })
	time.Sleep(20 * time.Millisecond)
	state = awaitActorState(t, s, func(state State) bool { return len(state.Requests) == 1 })
	if state.Executions != 1 || state.Requests[0].Phase != phaseQueued {
		t.Fatalf("tail below B must wait: %+v", state)
	}
	cancel()
	if r := <-tail; !errors.Is(r.err, ErrUnavailable) || r.response.Progress.BatchSize != 0 {
		t.Fatalf("shutdown must end the tail unexecuted: %+v", r)
	}
	<-s.Done()
}

func TestFixedBatchValidationAndReload(t *testing.T) {
	base := func() *Config {
		return &Config{Model: "synthetic", Mode: modeServing, Inputs: []string{"input"}, Outputs: []string{"output"}, RunnerCommand: []string{"runner"}, RunnerTimeoutMS: 1000, MaxQueueRequests: 4, MaxQueueBytes: 32, MaxRequestBytes: 32, MaxBatchSize: 3, BatchSizes: []int{1, 2, 3}, Policy: testPolicy()}
	}
	for name, adjust := range map[string]func(*Config){
		"negative":                func(c *Config) { c.Policy.FixedBatchSize = -1 },
		"unmeasured":              func(c *Config) { c.Policy.FixedBatchSize = 3 },
		"capped":                  func(c *Config) { c.Policy.FixedBatchSize, c.Policy.MaxBatchSize = 2, 1 },
		"profiling-uninitialized": func(c *Config) { c.Mode, c.Policy.FixedBatchSize = modeProfiling, 4 },
		"uninitialized":           func(c *Config) { c.BatchSizes, c.MaxBatchSize, c.Policy.FixedBatchSize = []int{1, 3}, 3, 2 },
	} {
		cfg := base()
		adjust(cfg)
		if err := ValidateConfig(cfg); err == nil {
			t.Fatalf("%s fixed batch size accepted", name)
		}
	}
	cfg := base()
	cfg.Policy.FixedBatchSize = 2
	if err := ValidateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{}
	s.config.Store(cfg)
	for _, change := range []func(*Config){
		func(c *Config) { c.Policy.MaxBatchSize = 1 },
		func(c *Config) { c.Policy.FixedBatchSize = 1 },
	} {
		next := *cfg
		change(&next)
		if err := s.Update(context.Background(), &next); err == nil {
			t.Fatalf("reload removing B was accepted: %+v", next.Policy)
		}
	}
}

func TestProfilingInferPreservesExchangePairsAndNaturalIdle(t *testing.T) {
	s, _ := startTestActor(t, modeProfiling, "100", nil)
	h := NewHandler(s)
	infer := func() ServiceSample {
		t.Helper()
		replies := postFixedInference(h, "a", "b+c")
		var response Response
		if reply := replies[0]; reply.Code != http.StatusOK || json.Unmarshal(reply.Body.Bytes(), &response) != nil || !response.ExecutionComplete || response.Progress.ServiceSample == nil {
			t.Fatalf("profiling inference must return actual exchange: %d %s", reply.Code, reply.Body.String())
		}
		return *response.Progress.ServiceSample
	}
	first := infer()
	if first.PrecedingIdleNanos != nil {
		t.Fatalf("Python startup warmup cannot invent a Go exchange anchor: %+v", first)
	}
	start := time.Now()
	second := infer()
	if second.PrecedingIdleNanos == nil || second.ServiceDurationNanos <= 0 || time.Since(start) >= 400*time.Millisecond {
		t.Fatalf("profiling must execute without adding idle: %+v", second)
	}
	time.Sleep(600 * time.Millisecond)
	start = time.Now()
	third := infer()
	if third.PrecedingIdleNanos == nil || *third.PrecedingIdleNanos < int64(600*time.Millisecond) || time.Since(start) >= 400*time.Millisecond || third.ServiceDurationNanos >= int64(400*time.Millisecond) {
		t.Fatalf("natural idle must remain in G, not S or an added wait: %+v", third)
	}
	for _, sample := range []ServiceSample{first, second, third} {
		if sample.TrialID != "physical-trial" {
			t.Fatalf("physical trial identity lost: %+v", sample)
		}
	}
}

func TestNativeBundleRejectsDurationOnlyProfiles(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.json")
	configPath := filepath.Join(dir, "config.json")
	cfg := Config{Model: "synthetic", Mode: modeServing, Inputs: []string{"input"}, Outputs: []string{"output"}, RunnerCommand: []string{"runner"}, RunnerTimeoutMS: 3000, MaxQueueRequests: 8, MaxQueueBytes: 1 << 20, MaxRequestBytes: 1 << 20, MaxBatchSize: 2, BatchSizes: []int{1, 2}, ProfilePath: profilePath, Policy: testPolicy()}
	configJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configJSON, 0600); err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"profileID":"synthetic-profile","batches":[{"size":1,"serviceSamplesMS":[10],"energyPerItemJ":1}]}`)
	if err := os.WriteFile(profilePath, old, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(configPath); err == nil {
		t.Fatal("duration-only bundle silently synthesized missing idle history")
	}
	paired, err := json.Marshal(ProfileBundle{ProfileID: "relabelled-profile", Batches: testPolicy().Batches})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, paired, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	queue := []RequestState{testState("w", 100, 0), testState("w", 100, 0)}
	if choice := ChooseBatch(loaded.Policy, queue, nil); choice.Size != 2 || choice.Reason != ReasonAvailableEnergy {
		t.Fatalf("paired bundle must hydrate a usable conditional native policy: %+v", choice)
	}
}

func TestSharedActorUsesFrozenRequestAdmission(t *testing.T) {
	s, cfg := startTestActor(t, modeServing, "150", withDeadline(DeadlineDrop))
	active := actorRequest(t, cfg, "blocker", "blocker", 1000)
	activeReply := make(chan result, 1)
	go func() { activeReply <- s.submit(context.Background(), active) }()
	awaitActorState(t, s, func(state State) bool { return len(state.Requests) == 1 && state.Requests[0].Phase == phaseRunning })
	reject := actorRequest(t, cfg, "reject", "reject", 20)
	reject.context.AdmissionPolicy = AdmissionReject
	delayed := actorRequest(t, cfg, "delay", "delay", 20)
	rejectReply, delayReply := make(chan result, 1), make(chan result, 1)
	go func() { rejectReply <- s.submit(context.Background(), reject) }()
	go func() { delayReply <- s.submit(context.Background(), delayed) }()
	if reply := <-rejectReply; !errors.Is(reply.err, ErrDeadlineInfeasible) || reply.response.Progress.BatchID != 0 {
		t.Fatalf("Reject must not execute on the shared actor: %+v", reply)
	}
	if reply := <-activeReply; reply.err != nil {
		t.Fatal(reply.err)
	}
	if reply := <-delayReply; reply.err != nil || reply.response.Progress.BatchSize != 1 || reply.response.Progress.ExecutorWaitNanos == nil || *reply.response.Progress.ExecutorWaitNanos < int64(100*time.Millisecond) {
		t.Fatalf("Delay must execute late with its actual queue wait: %+v", reply)
	}
	state := awaitActorState(t, s, func(state State) bool { return state.CompletedRequests == 3 })
	if state.Executions != 2 {
		t.Fatalf("only blocker and Delay may execute: %+v", state)
	}
}

func TestBatchConfirmationRefreshesMonotonicExpiry(t *testing.T) {
	stale := time.Now().Add(-time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	makeRequest := func(id, policy string) *request {
		return &request{ctx: context.Background(), context: RequestContext{RequestID: id, AdmissionPolicy: policy, SLO: SLO{TargetMS: 100}}, received: stale, enqueued: stale, bytes: 1, result: make(chan result, 1)}
	}
	canceled := makeRequest("cancel", AdmissionDelay)
	canceled.ctx = ctx
	timedOut := makeRequest("lifetime", AdmissionDelay)
	timedOut.lifetime = stale.Add(500 * time.Millisecond)
	rejected := makeRequest("slo", AdmissionReject)
	a := schedulerActor{cfg: &Config{Mode: modeServing, Policy: testPolicy()}, queue: []*request{canceled, timedOut, rejected}, queuedBytes: 3}
	a.startBatch(context.Background(), time.Time{})
	for _, tc := range []struct {
		request *request
		err     error
	}{{canceled, context.Canceled}, {timedOut, context.DeadlineExceeded}, {rejected, ErrDeadlineInfeasible}} {
		if reply := <-tc.request.result; !errors.Is(reply.err, tc.err) || !reply.complete || reply.response.Progress.BatchID != 0 {
			t.Fatalf("confirmation retained expired work: %+v", reply)
		}
	}
	if len(a.queue) != 0 || len(a.active) != 0 || a.queuedBytes != 0 || a.completed != 3 {
		t.Fatalf("confirmation must reselect after removing expired requests: %+v", a)
	}
}

func TestNativeQueueFullAdmissionProofs(t *testing.T) {
	s, cfg := startTestActor(t, modeServing, "0", func(c *Config) {
		c.Policy.FixedBatchSize = 2
		c.MaxQueueBytes = 12
	})
	h := NewHandler(s).(*handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queued := actorRequest(t, cfg, "queued", "queued", 1000)
	reply := make(chan result, 1)
	go func() { reply <- s.submit(ctx, queued) }()
	awaitActorState(t, s, func(state State) bool { return len(state.Requests) == 1 })
	actor := postJSON(h, "/v2/models/synthetic/infer", nativeIngressRequest("actor-full"))
	var failure ErrorResponse
	if actor.Code != http.StatusTooManyRequests || json.Unmarshal(actor.Body.Bytes(), &failure) != nil ||
		failure.Code != CodeQueueFull || failure.Stage != AdmissionStageExecutor || failure.Policy != AdmissionDelay ||
		failure.NotAdmitted || !failure.ExecutionComplete || failure.Progress == nil ||
		failure.Progress.RequestID != "actor-full" || failure.Progress.Outcome != "queue_full" ||
		failure.Progress.BatchID != 0 || failure.Progress.BatchSize != 0 || failure.RemainingSLONanos == nil || failure.ExecutorWaitNanos != nil {
		t.Fatalf("actor queue full must prove correlated non-execution without queue wait: %d %s", actor.Code, actor.Body.String())
	}
	for range cap(h.admission) {
		h.admission <- struct{}{}
	}
	semaphore := postJSON(h, "/v2/models/synthetic/infer", map[string]any{"invalid": true})
	failure = ErrorResponse{}
	if semaphore.Code != http.StatusTooManyRequests || json.Unmarshal(semaphore.Body.Bytes(), &failure) != nil ||
		failure.Code != CodeQueueFull || failure.Stage != AdmissionStageExecutor || !failure.NotAdmitted ||
		!failure.ExecutionComplete || failure.Progress != nil || failure.Policy != "" ||
		failure.RemainingSLONanos != nil || failure.ExecutorWaitNanos != nil {
		t.Fatalf("pre-decode rejection must not fabricate request evidence: %d %s", semaphore.Code, semaphore.Body.String())
	}
	for range cap(h.admission) {
		<-h.admission
	}
	cancel()
	<-reply
}

func TestNativeLifetimeTimeoutIsExplicitAndNonpreemptive(t *testing.T) {
	s, _ := startTestActor(t, modeServing, "200", nil)
	request := nativeIngressRequest("lifetime")
	request.Context.LifetimeMS = 30
	response := postJSON(NewHandler(s), "/v2/models/synthetic/infer", request)
	var failure ErrorResponse
	if response.Code != http.StatusRequestTimeout || json.Unmarshal(response.Body.Bytes(), &failure) != nil || failure.Code != CodeRequestTimeout || failure.ExecutionComplete || failure.Progress != nil {
		t.Fatalf("active lifetime timeout must remain physically incomplete: %d %s", response.Code, response.Body.String())
	}
	state := awaitActorState(t, s, func(state State) bool { return len(state.Requests) == 1 })
	if state.Requests[0].Phase != phaseRunning || state.CompletedRequests != 0 {
		t.Fatalf("timeout must retain physical occupancy: %+v", state)
	}
	awaitActorState(t, s, func(state State) bool { return state.CompletedRequests == 1 && len(state.Requests) == 0 })
}

func TestRequestAdmissionValidationAndMissingPolicyFailSafe(t *testing.T) {
	r := nativeIngressRequest("policy")
	for _, invalid := range []string{"", DeadlineKeep, "invalid"} {
		r.Context.AdmissionPolicy = invalid
		if err := validateContext(r.Context, r.ID); err == nil {
			t.Fatalf("invalid frozen policy accepted: %q", invalid)
		}
		state := testState("uid", 1000, 0)
		state.Context.AdmissionPolicy = invalid
		if choice := ChooseBatch(testPolicy(), []RequestState{state}, nil); choice.Size != 0 || choice.Reason != "admission_unknown" {
			t.Fatalf("missing replay policy must fail closed: %+v", choice)
		}
	}
}

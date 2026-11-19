package serving

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func admissionRuntimeCall(t *testing.T, r *Runtime, uid string, at, deadline time.Time) (*Call, Receipt) {
	t.Helper()
	receipt, err := r.Receive(r.workloads[uid].definition.Name, at)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Deadline = deadline
	c, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	return c, receipt
}

// publishDelay republishes Workloads uids with the Delay policy: their
// requests wait in the gateway queue while every executor holds its limit.
func publishDelay(t *testing.T, r *Runtime, uids ...string) {
	t.Helper()
	for _, uid := range uids {
		d := *r.workloads[uid].definition
		d.Generation++
		d.Admission.Policy = AdmissionPolicyDelay
		if err := r.Publish(&d); err != nil {
			t.Fatal(err)
		}
	}
}

// depthTestProfile has one batch per mean service time, of sizes 1, 2, 4, ...
func depthTestProfile(id string, means ...float64) *Profile {
	p := &Profile{ID: id}
	for j, mean := range means {
		p.Batches = append(p.Batches, schedulerTestBatchProfile(BatchProfile{Batch: 1 << j}, []float64{mean}))
	}
	return p
}

// The control410 capture's image-classification profiles (mean service time
// of batches 1 to 16) at its 200 ms target.
func TestAdmissionLimitOfControl410Profiles(t *testing.T) {
	orangepi := depthTestProfile("orangepi", 17672911, 30520545, 58671629, 116672712, 235869031)
	nano := depthTestProfile("jetson-nano", 56001757, 84054761, 167772853, 337812846, 637774039)
	for _, tc := range []struct {
		name                        string
		profile                     *Profile
		maxBatch, fixedBatch, queue int
		want                        int
	}{
		{"orangepi max batch 2", orangepi, 2, 0, 256, 13},
		{"orangepi max batch 16", orangepi, 16, 0, 256, 13},
		{"orangepi max batch 1", orangepi, 1, 0, 256, 11},
		{"jetson-nano max batch 1", nano, 1, 0, 256, 3},
		{"orangepi queue 8", orangepi, 16, 0, 8, 8},
		// Only batch 2 runs: 2 per 30.5 ms clears 13 in 200 ms.
		{"orangepi fixed batch 2", orangepi, 16, 2, 256, 13},
		// 16 per 636 ms clears 5 in 200 ms, but one batch needs 16.
		{"jetson-nano fixed batch 16", nano, 16, 16, 256, 16},
	} {
		if got := admissionLimit(tc.profile, tc.maxBatch, tc.fixedBatch, 200*time.Millisecond, tc.queue); got != tc.want {
			t.Errorf("%s: limit %d, want %d", tc.name, got, tc.want)
		}
	}
}

// A fixed-batch executor runs only full batches, so the gateway must let it
// hold one batch even where its throughput at the target is smaller: the
// executor alone judges the deadline. Here 4 per 100 ms clears 2 in 50 ms.
func TestReplayFixedBatchExecutorFillsItsBatch(t *testing.T) {
	s := replayAdmissionState(t)
	i := s.instances[0]
	profile := *i.config.Profile
	profile.Batches = []BatchProfile{
		schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{40e6}),
		schedulerTestBatchProfile(BatchProfile{Batch: 4}, []float64{100e6}),
	}
	i.config.Profile, i.config.MaxBatch, i.config.FixedBatchSize = &profile, 4, 4
	i.policy = schedulerPolicy(i.config)
	d := *s.snap.Workloads[0]
	d.SLO.TargetLatency = 50 * time.Millisecond
	var requests []*schedulerReplayRequest
	for j := range 4 {
		requests = append(requests, replayAdmissionPending(s, fmt.Sprint("fixed-", j), &d))
	}
	s.arrived = len(s.requests)
	for step := 0; ; step++ {
		if step > 100 {
			t.Fatal("replay did not settle")
		}
		s.expireRequests()
		s.completeExecution()
		s.routeRequests(context.Background())
		s.receiveNative()
		if !s.chooseBatches(context.Background()) {
			t.Fatalf("batch choice failed: %v", s.out.reasons)
		}
		next, remaining := s.nextEvent()
		if !remaining {
			break
		}
		s.now = next
	}
	for _, r := range requests {
		if !r.dispatched || r.dropped || r.expired || !r.completed {
			t.Fatalf("a fixed-batch executor limited below its batch never ran: %+v", r)
		}
	}
}

// depthGates holds each dispatched request in its executor until released,
// then replies with success or, for a request marked rejected, the executor's
// deadline rejection.
type depthGates struct {
	mu       sync.Mutex
	gates    map[string]chan struct{}
	rejected map[string]bool
}

func (g *depthGates) gate(id string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gates[id] == nil {
		g.gates[id] = make(chan struct{})
	}
	return g.gates[id]
}

func (g *depthGates) release(id string, reject bool) {
	g.mu.Lock()
	g.rejected[id] = reject
	g.mu.Unlock()
	close(g.gate(id))
}

// depthRuntime serves Workload w1 with policy and target through one
// executor per batch-1 mean service time, routed by weights.
func depthRuntime(t *testing.T, policy AdmissionPolicy, target time.Duration, services []float64, weights []int32) (*Runtime, *depthGates, []string) {
	t.Helper()
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	d := *r.workloads["w1"].definition
	d.Generation++
	d.SLO.TargetLatency, d.Admission.Policy = target, policy
	if err := r.Publish(&d); err != nil {
		t.Fatal(err)
	}
	gates := &depthGates{gates: map[string]chan struct{}{}, rejected: map[string]bool{}}
	base := r.instances[wanted.ID].ReadyInstance
	instances := make([]ReadyInstance, 0, len(services))
	ids := make([]string, 0, len(services))
	for j, service := range services {
		ready := base
		p := *base.Profile
		p.ID = fmt.Sprintf("profile-%d", j)
		p.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{service})}
		ready.Profile, ready.State.Native = &p, runtimeTestNative(&p)
		ready.State.ID, ready.State.Profile = fmt.Sprintf("executor-%d", j), p.ID
		ready.Capacity, ready.Routing = 100, map[string]int32{"w1": weights[j]}
		ready.Execute = func(ctx context.Context, dispatch Dispatch) (BackendResult, error) {
			select {
			case <-gates.gate(dispatch.RequestID):
			case <-ctx.Done():
			}
			gates.mu.Lock()
			defer gates.mu.Unlock()
			if gates.rejected[dispatch.RequestID] {
				return BackendResult{Finished: true}, &Error{Code: CodeDeadlineInfeasible, Stage: AdmissionStageExecutor, Policy: policy}
			}
			return BackendResult{Finished: true}, nil
		}
		instances = append(instances, ready)
		ids = append(ids, ready.State.ID)
	}
	if err := r.InstallInstances(instances); err != nil {
		t.Fatal(err)
	}
	return r, gates, ids
}

func TestRuntimeAdmissionAtLimitRejectsOrWaitsUntilReply(t *testing.T) {
	for _, policy := range []AdmissionPolicy{AdmissionPolicyReject, AdmissionPolicyDelay} {
		t.Run(string(policy), func(t *testing.T) {
			// 35 ms at one request per 10 ms: a limit of 3.
			r, gates, ids := depthRuntime(t, policy, 35*time.Millisecond, []float64{10e6}, []int32{1})
			var held []*Call
			for range 3 {
				c, _ := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
				held = append(held, c)
			}
			extra, receipt := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
			r.mu.Lock()
			assigned, rejection, queued := extra.assignment != nil, extra.receipt.Rejection, len(r.workloads["w1"].queue)
			r.mu.Unlock()
			if policy == AdmissionPolicyDelay && (assigned || rejection != nil || queued != 1) {
				t.Fatalf("delay request at the limit did not wait: assigned=%v rejection=%v queued=%d", assigned, rejection, queued)
			}
			if policy == AdmissionPolicyReject {
				if assigned || rejection == nil || rejection.Code != CodeQueueFull || rejection.Stage != AdmissionStageGateway || queued != 0 {
					t.Fatalf("reject request at the limit was not rejected at once: assigned=%v rejection=%+v queued=%d", assigned, rejection, queued)
				}
				if _, err := extra.Wait(context.Background()); !errors.Is(err, error(rejection)) {
					t.Fatalf("rejection reply %v", err)
				}
				if err := r.Response(receipt, schedulerOutcomeRejected, rejection.Code); err != nil {
					t.Fatal(err)
				}
				row := admissionFinalJSON(t, r, receipt.ID)
				depths, _ := row["admissionDepths"].(map[string]any)
				depth, _ := depths[ids[0]].(map[string]any)
				if depth["inFlight"] != float64(3) || depth["limit"] != float64(3) || row["rejection"] == nil || row["gatewayWaitNanos"] == nil || row["executorWaitNanos"] != nil {
					t.Fatalf("gateway rejection row lost its depth or queue provenance: %+v", row)
				}
			}
			close(gates.gate(held[0].receipt.ID))
			if _, err := held[0].Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			next := extra
			if policy == AdmissionPolicyReject {
				next, _ = admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if next.assignment == nil || next.assignment.Instance != ids[0] || next.receipt.Rejection != nil {
				t.Fatalf("a reply did not free one place under the limit: %+v", next.assignment)
			}
		})
	}
}

// The runtime gateway and the replay see the same events and must leave every
// request in the same state after each. "+" is an arrival, "-n" the executor's
// reply to the n-th arrival, "!n" its deadline rejection of it, "xn" the end
// of its lifetime while it waits in the gateway queue. Limits are 3 (10 ms)
// and 2 (12 ms) at 35 ms, shares 1:2. The replay's releases go through its
// executor paths: service end (completeExecution) and drop
// (transitionQueuedChoice).
func TestAdmissionRuntimeAndReplayDecideAlike(t *testing.T) {
	const target = 35 * time.Millisecond
	services, weights := []float64{10e6, 12e6}, []int32{1, 2}
	script := []string{"+", "+", "+", "+", "+", "+", "!1", "+", "+", "x8", "-2", "-3", "+", "-5", "+", "+", "+"}
	for _, policy := range []AdmissionPolicy{AdmissionPolicyReject, AdmissionPolicyDelay} {
		t.Run(string(policy), func(t *testing.T) {
			runtimeStates := runtimeDepthScript(t, policy, target, services, weights, script)
			replayStates := replayDepthScript(t, policy, target, services, weights, script)
			for j := range script {
				if !slices.Equal(runtimeStates[j], replayStates[j]) {
					t.Fatalf("after %q runtime and replay differ:\nruntime %v\nreplay  %v", script[j], runtimeStates[j], replayStates[j])
				}
			}
			final := runtimeStates[len(script)-1]
			want := map[AdmissionPolicy]string{AdmissionPolicyReject: CodeQueueFull, AdmissionPolicyDelay: "waiting"}[policy]
			if !slices.Contains(final, want) || slices.Contains(final, "timeout") != (policy == AdmissionPolicyDelay) {
				t.Fatalf("script did not reach a %s request and the lifetime path: %v", want, final)
			}
		})
	}
}

func runtimeDepthScript(t *testing.T, policy AdmissionPolicy, target time.Duration, services []float64, weights []int32, script []string) [][]string {
	r, gates, _ := depthRuntime(t, policy, target, services, weights)
	t.Cleanup(func() {
		gates.mu.Lock()
		defer gates.mu.Unlock()
		for id, gate := range gates.gates {
			if _, released := gates.rejected[id]; !released {
				close(gate)
			}
		}
	})
	var calls []*Call
	var states [][]string
	for _, event := range script {
		if event == "+" {
			c, _ := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Hour))
			calls = append(calls, c)
		} else {
			c := calls[mustAtoi(t, event[1:])-1]
			switch event[0] {
			case 'x':
				r.mu.Lock()
				c.receipt.Deadline = time.Now()
				r.dispatchLocked()
				r.mu.Unlock()
			default:
				gates.release(c.receipt.ID, event[0] == '!')
			}
			_, _ = c.Wait(context.Background())
		}
		r.mu.Lock() // the reply's own dispatch has finished
		state := make([]string, len(calls))
		for j, c := range calls {
			switch {
			case c.assignment != nil:
				state[j] = c.assignment.Instance
			case c.receipt.Rejection != nil:
				state[j] = c.receipt.Rejection.Code
			case c.backendDone:
				state[j] = "timeout"
			default:
				state[j] = "waiting"
			}
		}
		r.mu.Unlock()
		states = append(states, state)
	}
	return states
}

func replayDepthScript(t *testing.T, policy AdmissionPolicy, target time.Duration, services []float64, weights []int32, script []string) [][]string {
	s := replayAdmissionState(t)
	d := *s.snap.Workloads[0]
	d.SLO.TargetLatency, d.Admission.Policy = target, policy
	prototype := s.instances[0]
	s.instances, s.byID = nil, map[string]*schedulerReplayInstance{}
	routes := map[string]int32{}
	for j, service := range services {
		i := *prototype
		p := *prototype.config.Profile
		p.ID, p.Batches = fmt.Sprintf("profile-%d", j), []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{service})}
		i.config.ID, i.config.Profile, i.config.Capacity, i.config.MaxBatch = fmt.Sprintf("executor-%d", j), &p, 100, 1
		s.instances, s.byID[i.config.ID] = append(s.instances, &i), &i
		routes[i.config.ID] = weights[j]
	}
	s.snap.Routing = map[string]map[string]int32{d.UID: routes}
	s.weights, s.available = make([]int32, len(s.instances)), make([]bool, len(s.instances))
	var requests []*schedulerReplayRequest
	var states [][]string
	for _, event := range script {
		if event == "+" {
			r := replayAdmissionPending(s, fmt.Sprint("arrival-", len(requests)), &d)
			r.expires = math.Inf(1)
			requests = append(requests, r)
		} else {
			r := requests[mustAtoi(t, event[1:])-1]
			i := s.byID[r.source.Instance]
			switch event[0] {
			case 'x':
				r.expires = s.now
				s.expireRequests()
			case '!':
				i.queue = append(i.queue, r)
				s.transitionQueuedChoice(i, firstfloor.BatchChoice{RequestID: r.source.ID, Reason: firstfloor.OutcomeDeadlineDrop})
			default:
				r.native, r.running, i.batch, i.finish = math.Inf(1), true, []*schedulerReplayRequest{r}, s.now
				s.completeExecution()
			}
		}
		s.routeRequests(context.Background())
		state := make([]string, len(requests))
		for j, r := range requests {
			switch {
			case r.dispatched:
				state[j] = r.source.Instance
			case r.dropped:
				state[j] = CodeQueueFull
			case r.expired:
				state[j] = "timeout"
			default:
				state[j] = "waiting"
			}
		}
		states = append(states, state)
	}
	return states
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRuntimeAdmissionQueueLimitIsPerWorkload(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1, "w2": 1})
	d := *r.workloads["w1"].definition
	d.Generation++
	d.Admission.MaxQueuedRequests = 1
	if err := r.Publish(&d); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.instances[wanted.ID].State.Ready = false
	r.mu.Unlock()
	admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	receipt, err := r.Receive(d.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	var domain *Error
	if !errors.As(err, &domain) || domain.Code != CodeQueueFull || domain.Stage != AdmissionStageGateway || domain.Policy != AdmissionPolicyReject {
		t.Fatalf("queue boundary lost per-definition rejection: %v", err)
	}
	other, _ := admissionRuntimeCall(t, r, "w2", time.Now(), time.Now().Add(time.Second))
	if other == nil || len(r.workloads["w1"].queue) != 1 || len(r.workloads["w2"].queue) != 1 {
		t.Fatal("one workload consumed another workload's queue budget")
	}
}

func TestRuntimeAdmissionRetirementPreservesQueuedRoute(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	publishDelay(t, r, "w1")
	r.mu.Lock()
	r.instances[wanted.ID].Capacity = 0 // the ready executor holds its limit
	r.mu.Unlock()
	admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	r.mu.Lock()
	targets := map[string]map[string]int32{"w1": {}}
	r.retainAcceptedRoutesLocked(targets, []string{"w1"})
	r.mu.Unlock()
	if targets["w1"][wanted.ID] <= 0 {
		t.Fatal("route retirement discarded accepted queued work")
	}
	if stopped, err := r.StopSchedulerInstance(wanted.ID); err != nil || stopped {
		t.Fatalf("only queued route stopped before drain: %v %v", stopped, err)
	}
}

// A request that waits holds back only later requests of its own Definition:
// one of a newer Definition with a target an executor still has room for is
// admitted past it, in the runtime and in the replay alike.
func TestAdmissionWaitingDefinitionDoesNotHoldAnother(t *testing.T) {
	// One request per 10 ms: 15 ms allows one in flight, 35 ms three.
	r, gates, ids := depthRuntime(t, AdmissionPolicyDelay, 15*time.Millisecond, []float64{10e6}, []int32{1})
	t.Cleanup(func() {
		gates.mu.Lock()
		defer gates.mu.Unlock()
		for _, gate := range gates.gates {
			close(gate)
		}
	})
	admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	held, _ := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	d := *r.workloads["w1"].definition
	d.Generation++
	d.SLO.TargetLatency = 35 * time.Millisecond
	if err := r.Publish(&d); err != nil {
		t.Fatal(err)
	}
	later, _ := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	r.mu.Lock()
	if held.assignment != nil || later.assignment == nil || later.assignment.Instance != ids[0] {
		r.mu.Unlock()
		t.Fatalf("runtime: waiting request held back another Definition: held=%+v later=%+v", held.assignment, later.assignment)
	}
	// The waiting request's record keeps its own limit, not the later one's.
	waits := held.admissionWaits
	r.mu.Unlock()
	if last := waits[len(waits)-1]; last.Depth == nil || last.Depth.Limit != 1 || last.Reason != CodeQueueFull {
		t.Fatalf("waiting request recorded another Definition's limit: %+v", last)
	}

	s := replayAdmissionState(t)
	s.instances[0].outstanding = 1
	tight, loose := *s.snap.Workloads[0], *s.snap.Workloads[0]
	tight.Admission.Policy, tight.SLO.TargetLatency = AdmissionPolicyDelay, 15*time.Millisecond
	loose.Generation++
	loose.Admission.Policy, loose.SLO.TargetLatency = AdmissionPolicyDelay, 35*time.Millisecond
	waiting, admitted := replayAdmissionPending(s, "held", &tight), replayAdmissionPending(s, "later", &loose)
	s.routeRequests(context.Background())
	if waiting.dispatched || waiting.dropped || !admitted.dispatched {
		t.Fatalf("replay: waiting request held back another Definition: held=%+v later=%+v", waiting, admitted)
	}
}

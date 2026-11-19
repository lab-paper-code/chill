package serving

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
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
		name            string
		profile         *Profile
		maxBatch, queue int
		want            int
	}{
		{"orangepi max batch 2", orangepi, 2, 256, 13},
		{"orangepi max batch 16", orangepi, 16, 256, 13},
		{"orangepi max batch 1", orangepi, 1, 256, 11},
		{"jetson-nano max batch 1", nano, 1, 256, 3},
		{"orangepi queue 8", orangepi, 16, 8, 8},
	} {
		if got := admissionLimit(tc.profile, tc.maxBatch, 200*time.Millisecond, tc.queue); got != tc.want {
			t.Errorf("%s: limit %d, want %d", tc.name, got, tc.want)
		}
	}
}

// depthGates holds each dispatched request in its executor until released.
type depthGates struct {
	mu    sync.Mutex
	gates map[string]chan struct{}
}

func (g *depthGates) gate(id string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gates[id] == nil {
		g.gates[id] = make(chan struct{})
	}
	return g.gates[id]
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
	gates := &depthGates{gates: map[string]chan struct{}{}}
	base := r.instances[wanted.ID].ReadyInstance
	var instances []ReadyInstance
	var ids []string
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
				if assigned || rejection == nil || rejection.Code != CodeDeadlineInfeasible || rejection.Stage != AdmissionStageGateway || queued != 0 {
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

// The runtime gateway and the replay see the same arrivals and replies and
// must decide every request alike. "+" is an arrival, "-n" the reply of the
// n-th arrival. Limits are 3 (10 ms) and 2 (12 ms) at 35 ms, shares 1:2.
func TestAdmissionRuntimeAndReplayDecideAlike(t *testing.T) {
	const target = 35 * time.Millisecond
	services, weights := []float64{10e6, 12e6}, []int32{1, 2}
	script := []string{"+", "+", "+", "+", "+", "+", "-1", "+", "+", "-2", "-3", "-5", "+", "+", "+", "+"}

	r, gates, ids := depthRuntime(t, AdmissionPolicyReject, target, services, weights)
	var runtimeDecisions []string
	var calls []*Call
	for _, event := range script {
		if event == "+" {
			c, _ := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
			r.mu.Lock()
			decision := "waiting"
			if c.assignment != nil {
				decision = c.assignment.Instance
			} else if c.receipt.Rejection != nil {
				decision = c.receipt.Rejection.Code
			}
			r.mu.Unlock()
			calls, runtimeDecisions = append(calls, c), append(runtimeDecisions, decision)
			continue
		}
		n, _ := strconv.Atoi(event[1:])
		close(gates.gate(calls[n-1].receipt.ID))
		if _, err := calls[n-1].Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	s := replayAdmissionState(t)
	d := *s.snap.Workloads[0]
	d.SLO.TargetLatency = target
	prototype := s.instances[0]
	s.instances, s.byID = nil, map[string]*schedulerReplayInstance{}
	routes := map[string]int32{}
	for j, service := range services {
		i := *prototype
		p := *prototype.config.Profile
		p.ID, p.Batches = fmt.Sprintf("profile-%d", j), []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{service})}
		i.config.ID, i.config.Profile, i.config.Capacity, i.config.MaxBatch = ids[j], &p, 100, 1
		s.instances, s.byID[ids[j]] = append(s.instances, &i), &i
		routes[ids[j]] = weights[j]
	}
	s.snap.Routing = map[string]map[string]int32{d.UID: routes}
	s.weights, s.available = make([]int32, len(s.instances)), make([]bool, len(s.instances))
	var replayDecisions []string
	var requests []*schedulerReplayRequest
	for _, event := range script {
		if event == "+" {
			request := replayAdmissionPending(s, fmt.Sprint("arrival-", len(requests)), &d)
			s.routeRequests(context.Background())
			decision := "waiting"
			if request.dispatched {
				decision = request.source.Instance
			} else if request.dropped {
				decision = CodeDeadlineInfeasible
			}
			requests, replayDecisions = append(requests, request), append(replayDecisions, decision)
			continue
		}
		n, _ := strconv.Atoi(event[1:])
		requests[n-1].returning, requests[n-1].completion = true, s.now
		s.completeExecution()
	}

	if !slices.Equal(runtimeDecisions, replayDecisions) {
		t.Fatalf("runtime and replay decided differently:\nruntime %v\nreplay  %v", runtimeDecisions, replayDecisions)
	}
	var rejected []int
	for j, decision := range runtimeDecisions {
		if decision == CodeDeadlineInfeasible {
			rejected = append(rejected, j+1)
		}
	}
	if !slices.Equal(rejected, []int{6, 8, 12}) || !slices.Contains(runtimeDecisions, ids[0]) || !slices.Contains(runtimeDecisions, ids[1]) {
		t.Fatalf("decisions %v do not exercise both limits", runtimeDecisions)
	}
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
	// 5 ms at 10 ms per request allows none; 35 ms allows three.
	r, gates, ids := depthRuntime(t, AdmissionPolicyDelay, 5*time.Millisecond, []float64{10e6}, []int32{1})
	t.Cleanup(func() {
		gates.mu.Lock()
		defer gates.mu.Unlock()
		for _, gate := range gates.gates {
			close(gate)
		}
	})
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
	r.mu.Unlock()

	s := replayAdmissionState(t)
	tight, loose := *s.snap.Workloads[0], *s.snap.Workloads[0]
	tight.Admission.Policy, tight.SLO.TargetLatency = AdmissionPolicyDelay, 5*time.Millisecond
	loose.Generation++
	loose.Admission.Policy, loose.SLO.TargetLatency = AdmissionPolicyDelay, 35*time.Millisecond
	waiting, admitted := replayAdmissionPending(s, "held", &tight), replayAdmissionPending(s, "later", &loose)
	s.routeRequests(context.Background())
	if waiting.dispatched || waiting.dropped || !admitted.dispatched {
		t.Fatalf("replay: waiting request held back another Definition: held=%+v later=%+v", waiting, admitted)
	}
}

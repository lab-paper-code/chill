package serving

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
)

const nextGatewayPlanVersion = "plan-2"

func gatewayRuntime(t *testing.T) (*Runtime, GatewayPlan, []ReadyInstance) {
	t.Helper()
	_, fixture := schedulerFixture()
	d, p := fixture.Workloads[0], fixture.Instances[0].Profile
	d.Preprocessor.UID, d.Preprocessor.Generation = "pre-uid", 1
	log, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), RouterID: "router-1", Store: "evidence", Retention: 2 * time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close(context.Background()) })
	r, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 8, RouterID: "router-1", ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.cancel)
	target := GatewayTarget{InstanceID: "pod-1/containerd://one", PodUID: "pod-1", ContainerID: "containerd://one", Node: fixture.Instances[0].Node, Endpoint: "http://native:8080", ModelName: "chill_model", Profile: p, ExpectedRuntime: p.RuntimeMetadata, DecisionID: "native-policy", OwnerWorkloadUID: d.UID, Capacity: 100, RouterLimit: 1}
	plan := GatewayPlan{PlanVersion: "plan-1", CreatedAt: time.Now(), Participants: 100, Definitions: []*Definition{d}, ObservationWindow: time.Second, Admission: map[string]bool{d.UID: true}, Targets: []GatewayTarget{target}, Routing: map[string]map[string]int32{d.UID: {target.InstanceID: 1}}}
	ready := []ReadyInstance{{State: InstanceState{ID: target.InstanceID, PodUID: target.PodUID, ContainerID: target.ContainerID, Node: target.Node, Profile: p.ID, Ready: true}, Profile: p, Capacity: target.RouterLimit, OwnerWorkloadUID: d.UID, Execute: func(context.Context, Dispatch) (BackendResult, error) { return BackendResult{Finished: true}, nil }}}
	r.SetGatewayConnected(false)
	return r, plan, ready
}

func installGateway(t *testing.T, r *Runtime, plan GatewayPlan, ready []ReadyInstance) {
	t.Helper()
	if err := r.ApplyGatewayPlan(plan, ready); err != nil {
		t.Fatal(err)
	}
	r.SetGatewayConnected(true)
}

func gatewayCall(t *testing.T, r *Runtime, name string) (Receipt, *Call) {
	t.Helper()
	receipt, err := r.Receive(name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	receipt.Deadline = receipt.ReceivedAt.Add(time.Second)
	call, err := r.Accept(receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	return receipt, call
}

func finishGatewayCall(t *testing.T, r *Runtime, receipt Receipt, call *Call) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := call.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Response(receipt, outcomeCompleted, ""); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayApplyRejectsPartialSnapshotAtomically(t *testing.T) {
	cases := map[string]func(*GatewayPlan, *[]ReadyInstance){
		"partial routing":   func(p *GatewayPlan, _ *[]ReadyInstance) { p.Routing = nil },
		"partial executors": func(_ *GatewayPlan, r *[]ReadyInstance) { *r = nil },
		"partial admission": func(p *GatewayPlan, _ *[]ReadyInstance) { p.Admission = nil },
		"unknown admission": func(p *GatewayPlan, _ *[]ReadyInstance) { p.Admission = map[string]bool{"unknown": true} },
		"missing horizon":   func(p *GatewayPlan, _ *[]ReadyInstance) { p.ObservationWindow = 0 },
		"duplicate target": func(p *GatewayPlan, r *[]ReadyInstance) {
			p.Targets = append(p.Targets, p.Targets[0])
			*r = append(*r, (*r)[0])
		},
		"duplicate definition": func(p *GatewayPlan, _ *[]ReadyInstance) { p.Definitions = append(p.Definitions, p.Definitions[0]) },
		"source identity":      func(p *GatewayPlan, _ *[]ReadyInstance) { p.Targets[0].ContainerID = "containerd://other" },
		"executor identity":    func(_ *GatewayPlan, r *[]ReadyInstance) { (*r)[0].State.PodUID = "other-pod" },
		"wrong profile":        func(_ *GatewayPlan, r *[]ReadyInstance) { (*r)[0].State.Profile = "other-profile" },
		"negative weight": func(p *GatewayPlan, _ *[]ReadyInstance) {
			p.Routing[p.Definitions[0].UID][p.Targets[0].InstanceID] = -1
		},
		"wrong quota":    func(p *GatewayPlan, _ *[]ReadyInstance) { p.Targets[0].RouterLimit++ },
		"wrong capacity": func(p *GatewayPlan, _ *[]ReadyInstance) { p.Targets[0].Capacity++ },
		"unknown definition": func(p *GatewayPlan, _ *[]ReadyInstance) {
			p.Routing = map[string]map[string]int32{"unknown": {p.Targets[0].InstanceID: 1}}
		},
		"missing policy": func(p *GatewayPlan, _ *[]ReadyInstance) { p.Targets[0].DecisionID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r, plan, ready := gatewayRuntime(t)
			installGateway(t, r, plan, ready)
			before := r.gateway
			next := plan
			next.PlanVersion = nextGatewayPlanVersion
			next.Targets = append([]GatewayTarget(nil), plan.Targets...)
			next.Routing = map[string]map[string]int32{plan.Definitions[0].UID: maps.Clone(plan.Routing[plan.Definitions[0].UID])}
			mutate(&next, &ready)
			if err := r.ApplyGatewayPlan(next, ready); err == nil {
				t.Fatal("invalid complete snapshot was accepted")
			}
			if r.gateway != before || !r.GatewayReady() || r.workloads[plan.Definitions[0].UID].definition != plan.Definitions[0] || r.instances[plan.Targets[0].InstanceID].Capacity != 1 {
				t.Fatal("failed apply partially changed serving state")
			}
		})
	}
}

func TestGatewaySameVersionKeepsOriginalBodyAndAppliedTime(t *testing.T) {
	r, plan, ready := gatewayRuntime(t)
	installGateway(t, r, plan, ready)
	before := r.gateway
	report := r.GatewayReport("router-1", time.Time{})
	// An already installed identity is an idempotent confirmation, not a body
	// equality/digest gate and not a second application.
	if err := r.ApplyGatewayPlan(GatewayPlan{PlanVersion: plan.PlanVersion}, nil); err != nil {
		t.Fatal(err)
	}
	if after := r.GatewayReport("router-1", time.Now()); r.gateway != before || !after.AppliedAt.Equal(report.AppliedAt) {
		t.Fatal("same version replaced its body or invented a new applied event")
	}
}

func TestGatewayInflightVersionAndRetiredReservationGate(t *testing.T) {
	r, plan, ready := gatewayRuntime(t)
	started, release := make(chan Dispatch, 2), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	ready[0].Execute = func(ctx context.Context, d Dispatch) (BackendResult, error) {
		started <- d
		select {
		case <-release:
		case <-ctx.Done():
		}
		return BackendResult{Finished: true}, nil
	}
	installGateway(t, r, plan, ready)
	receipt, call := gatewayCall(t, r, plan.Definitions[0].Name)
	old := <-started
	updated := *plan.Definitions[0]
	updated.Generation++
	lifetime := 2 * time.Second
	updated.RequestLifetime = &lifetime
	next := plan
	next.PlanVersion, next.Definitions = nextGatewayPlanVersion, []*Definition{&updated}
	next.Targets = append([]GatewayTarget(nil), plan.Targets...)
	next.Targets[0].InstanceID, next.Targets[0].PodUID, next.Targets[0].ContainerID = "pod-2/containerd://two", "pod-2", "containerd://two"
	next.Routing = map[string]map[string]int32{updated.UID: {next.Targets[0].InstanceID: 1}}
	newReady := ready[0]
	newReady.State.ID, newReady.State.PodUID, newReady.State.ContainerID = next.Targets[0].InstanceID, next.Targets[0].PodUID, next.Targets[0].ContainerID
	newReady.Execute = func(_ context.Context, d Dispatch) (BackendResult, error) {
		started <- d
		return BackendResult{Finished: true}, nil
	}
	if err := r.ApplyGatewayPlan(next, []ReadyInstance{newReady}); err != nil {
		t.Fatal(err)
	}
	newReceipt, newCall := gatewayCall(t, r, updated.Name)
	fresh := <-started
	if old.Definition != plan.Definitions[0] || call.definition != old.Definition || call.receipt.PlanVersion != plan.PlanVersion || old.Assignment.PlanVersion != plan.PlanVersion || old.Deadline != receipt.Deadline || fresh.Definition != &updated || fresh.Assignment.PlanVersion != next.PlanVersion || fresh.Assignment.Instance != next.Targets[0].InstanceID || newReceipt.RequestLifetime != lifetime {
		t.Fatal("plan swap migrated old request context or reserved a retired source")
	}
	report := r.GatewayReport("router-1", time.Time{})
	found := false
	for _, source := range report.Outstanding {
		if source.InstanceID == old.Assignment.Instance {
			found = source.Count == 1 && len(source.RequestIDs) == 1 && source.RequestIDs[0] == receipt.ID
		}
	}
	if !found || old.Assignment.PodUID != plan.Targets[0].PodUID || old.Assignment.ContainerID != plan.Targets[0].ContainerID || old.Assignment.RouterID != "router-1" {
		t.Fatal("retired source lost its original physical reservation or metadata")
	}
	finishGatewayCall(t, r, newReceipt, newCall)
	close(release)
	released = true
	finishGatewayCall(t, r, receipt, call)
}

func TestGatewayQueuedRequestPinsDefinitionAndRejectsUnsafeCutover(t *testing.T) {
	r, plan, ready := gatewayRuntime(t)
	started, release := make(chan Dispatch, 2), make(chan struct{})
	ready[0].Execute = func(ctx context.Context, d Dispatch) (BackendResult, error) {
		started <- d
		select {
		case <-release:
		case <-ctx.Done():
		}
		return BackendResult{Finished: true}, nil
	}
	installGateway(t, r, plan, ready)
	firstReceipt, first := gatewayCall(t, r, plan.Definitions[0].Name)
	<-started
	queuedReceipt, queued := gatewayCall(t, r, plan.Definitions[0].Name)
	withdrawn := plan
	withdrawn.PlanVersion, withdrawn.Targets = "withdrawn", nil
	withdrawn.Routing = map[string]map[string]int32{plan.Definitions[0].UID: {}}
	if err := r.ApplyGatewayPlan(withdrawn, nil); err == nil {
		t.Fatal("cutover stranded accepted pinned ingress")
	}
	updated := *plan.Definitions[0]
	updated.Generation++
	lifetime := 3 * time.Second
	updated.RequestLifetime = &lifetime
	next := plan
	next.PlanVersion, next.Definitions = nextGatewayPlanVersion, []*Definition{&updated}
	if err := r.ApplyGatewayPlan(next, ready); err != nil {
		t.Fatal(err)
	}
	close(release)
	finishGatewayCall(t, r, firstReceipt, first)
	dispatched := <-started
	if dispatched.Definition != plan.Definitions[0] || dispatched.Assignment.PlanVersion != plan.PlanVersion || dispatched.Deadline != queuedReceipt.Deadline || queued.definition != plan.Definitions[0] {
		t.Fatal("accepted queue migrated its definition, lifetime or routing version")
	}
	finishGatewayCall(t, r, queuedReceipt, queued)
}

func TestGatewayZeroQuotaAndStaleGates(t *testing.T) {
	r, plan, ready := gatewayRuntime(t)
	plan.Participants, plan.Targets[0].RouterLimit, ready[0].Capacity = 101, 0, 0
	executed := make(chan struct{}, 1)
	ready[0].Execute = func(context.Context, Dispatch) (BackendResult, error) {
		executed <- struct{}{}
		return BackendResult{Finished: true}, nil
	}
	if r.GatewayReady() {
		t.Fatal("router became ready before synchronization")
	}
	installGateway(t, r, plan, ready)
	receipt, call := gatewayCall(t, r, plan.Definitions[0].Name)
	if call.assignment != nil || r.instances[plan.Targets[0].InstanceID].Capacity != 0 {
		t.Fatal("zero quota was promoted to dispatch capacity")
	}
	r.SetGatewayConnected(false)
	if r.GatewayReady() {
		t.Fatal("disconnected channel left readiness open")
	}
	if state, err := r.State(receipt.Name); err != nil || state.Admission || state.Reason != "GatewayNotReady" {
		t.Fatalf("stale admission remained open: %+v %v", state, err)
	}
	if _, err := r.Receive(receipt.Name, time.Now()); err == nil {
		t.Fatal("stale router accepted a new receipt")
	}
	if _, err := r.Accept(receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}}); err == nil {
		t.Fatal("stale router accepted a request on an existing connection")
	}
	if report := r.GatewayReport("router-1", time.Time{}); len(report.Requests) != 1 || report.Requests[0].ID != receipt.ID || call.assignment != nil {
		t.Fatal("disconnect discarded accepted unassigned work")
	}
	select {
	case <-executed:
		t.Fatal("zero quota or disconnected router dispatched native work")
	default:
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = call.Wait(ctx)
	if err := r.Response(receipt, "canceled", "RequestCanceled"); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayCancellationRetainsOldPhysicalOwnership(t *testing.T) {
	r, plan, ready := gatewayRuntime(t)
	started := make(chan struct{})
	ready[0].Execute = func(ctx context.Context, _ Dispatch) (BackendResult, error) {
		close(started)
		<-ctx.Done()
		return BackendResult{}, failure("RequestCanceled", "transport canceled without native completion")
	}
	installGateway(t, r, plan, ready)
	receipt, call := gatewayCall(t, r, plan.Definitions[0].Name)
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = call.Wait(ctx)
	wait, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	_, _ = call.Wait(wait)
	if err := r.Response(receipt, "canceled", "RequestCanceled"); err != nil {
		t.Fatal(err)
	}
	r.SetGatewayConnected(false)
	next := plan
	next.PlanVersion, next.Targets = "withdrawn", nil
	next.Routing = map[string]map[string]int32{plan.Definitions[0].UID: {}}
	if err := r.ApplyGatewayPlan(next, nil); err != nil {
		t.Fatal(err)
	}
	report := r.GatewayReport("router-1", time.Time{})
	if len(report.Outstanding) != 1 || report.Outstanding[0].Count != 1 || report.Outstanding[0].InstanceID != plan.Targets[0].InstanceID || report.Outstanding[0].PodUID != plan.Targets[0].PodUID || report.Outstanding[0].ContainerID != plan.Targets[0].ContainerID {
		t.Fatalf("cancel/disconnect/cutover released physical ownership: %+v", report.Outstanding)
	}
	progress := BackendProgress{RequestID: receipt.ID, WorkloadUID: receipt.UID, Definition: call.identity, Phase: phaseFinished, At: time.Now(), ExecutionComplete: true}
	if err := r.ObserveBackend("other/container", progress); err == nil {
		t.Fatal("foreign source completion released physical ownership")
	}
	if err := r.ObserveBackend(plan.Targets[0].InstanceID, progress); err != nil {
		t.Fatal(err)
	}
	if report = r.GatewayReport("router-1", time.Time{}); report.Outstanding[0].Count != 0 {
		t.Fatal("exact native completion did not release its owner")
	}
}

func TestGatewayReportIncludesAssignedBeforeSend(t *testing.T) {
	r, plan, ready := gatewayRuntime(t)
	installGateway(t, r, plan, ready)
	receipt, err := r.Receive(plan.Definitions[0].Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	log := r.config.ObservationLog
	log.mu.Lock()
	locked := true
	defer func() {
		if locked {
			log.mu.Unlock()
		}
	}()
	call, err := r.Accept(receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	report := r.GatewayReport("router-1", time.Time{})
	if len(report.Outstanding) != 1 || report.Outstanding[0].Count != 1 || report.Outstanding[0].RequestIDs[0] != receipt.ID {
		t.Fatal("assignment awaiting persistence disappeared from physical report")
	}
	log.mu.Unlock()
	locked = false
	finishGatewayCall(t, r, receipt, call)
}

func TestGatewayControlCallbacksBlockAllRetirementButNotWithdrawal(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	_, fixture := schedulerFixture()
	r.config.GatewayDrained = func(string, string, string) bool { return false }
	r.config.GatewayRequests = func(time.Time) []SchedulerRequest {
		return []SchedulerRequest{{ID: "remote", Definition: fixture.Workloads[0], Instance: instance.ID, Phase: phaseRunning}}
	}
	if r.Drained("w1") || r.Drained("unknown") {
		t.Fatal("empty local state was mistaken for all-router zero")
	}
	if done, err := r.DetachInstance(instance.ID); err != nil || done {
		t.Fatalf("remote physical ownership did not block instance detach: %v %v", done, err)
	}
	if done, err := r.DetachNode(instance.Node.UID); err != nil || done {
		t.Fatalf("remote physical ownership did not block node detach: %v %v", done, err)
	}
	if err := r.StopAdmission("w1", "test"); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{r.Detach("w1"), r.Forget("w1"), r.Detach("unknown"), r.Forget("unknown")} {
		var domain *Error
		if !errors.As(err, &domain) || domain.Code != "DrainIncomplete" {
			t.Fatalf("remote workload ownership was not retained: %v", err)
		}
	}
	if requests := r.SchedulerInputs(time.Now).Outstanding; len(requests) != 1 || requests[0].ID != "remote" {
		t.Fatal("control scheduler ignored remote requests")
	}
	physicalRequests := r.config.GatewayRequests
	r.config.GatewayRequests = func(time.Time) []SchedulerRequest {
		return []SchedulerRequest{{ID: "remote-pending", Definition: fixture.Workloads[0], Phase: phasePending}}
	}
	if done, err := r.StopSchedulerInstance(instance.ID); err != nil || done {
		t.Fatalf("withdrawal stranded remote accepted unassigned ingress: %v %v", done, err)
	}
	r.config.GatewayRequests = physicalRequests
	if done, err := r.StopSchedulerInstance(instance.ID); err != nil || !done {
		t.Fatalf("remote physical occupancy deadlocked source withdrawal: %v %v", done, err)
	}
	r.instances[instance.ID].State.PodUID, r.instances[instance.ID].State.Stopped = "pod", true
	if err := r.RetirePod("pod"); err == nil {
		t.Fatal("local empty state allowed remote-owned Pod retirement")
	}
}

func TestGatewayStaleDispatchRetainsQueueUntilResynchronized(t *testing.T) {
	r, plan, ready := gatewayRuntime(t)
	started, release := make(chan Dispatch, 2), make(chan struct{})
	ready[0].Execute = func(ctx context.Context, d Dispatch) (BackendResult, error) {
		started <- d
		select {
		case <-release:
		case <-ctx.Done():
		}
		return BackendResult{Finished: true}, nil
	}
	installGateway(t, r, plan, ready)
	firstReceipt, first := gatewayCall(t, r, plan.Definitions[0].Name)
	<-started
	queuedReceipt, queued := gatewayCall(t, r, plan.Definitions[0].Name)
	r.SetGatewayConnected(false)
	close(release)
	finishGatewayCall(t, r, firstReceipt, first)
	report := r.GatewayReport("router-1", time.Time{})
	if len(report.Requests) != 1 || report.Requests[0].ID != queuedReceipt.ID || report.Requests[0].Instance != "" || report.Outstanding[0].Count != 0 {
		t.Fatal("disconnected router dispatched or discarded retained accepted work")
	}
	select {
	case <-started:
		t.Fatal("stale channel dispatched new native work")
	default:
	}
	next := plan
	next.PlanVersion = nextGatewayPlanVersion
	if err := r.ApplyGatewayPlan(next, ready); err != nil {
		t.Fatal(err)
	}
	if r.GatewayReady() {
		t.Fatal("plan application reopened a disconnected router")
	}
	r.SetGatewayConnected(true)
	dispatch := <-started
	if dispatch.Assignment.PlanVersion != plan.PlanVersion {
		t.Fatal("resynchronization migrated accepted queue to a newer snapshot")
	}
	finishGatewayCall(t, r, queuedReceipt, queued)
}

func TestGatewayOldReceiptCannotReserveAfterApply(t *testing.T) {
	r, plan, ready := gatewayRuntime(t)
	installGateway(t, r, plan, ready)
	receipt, err := r.Receive(plan.Definitions[0].Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	next := plan
	next.PlanVersion = nextGatewayPlanVersion
	if err := r.ApplyGatewayPlan(next, ready); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Accept(receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}}); err == nil {
		t.Fatal("an old unaccepted receipt increased reservations after applied")
	}
	if r.GatewayReport("router-1", time.Time{}).Outstanding[0].Count != 0 {
		t.Fatal("rejected old receipt leaked a physical reservation")
	}
	if err := r.ApplyGatewayPlan(plan, ready); err == nil || r.gateway.version != next.PlanVersion {
		t.Fatal("replayed obsolete plan replaced the installed version")
	}
	if err := r.Response(receipt, "error", "GatewayPlanChanged"); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayNativeAbsenceRequiresExactSourceEvidence(t *testing.T) {
	r, plan, ready := gatewayRuntime(t)
	ready[0].Execute = func(context.Context, Dispatch) (BackendResult, error) {
		return BackendResult{}, failure("BackendUnavailable", "submission result unknown")
	}
	installGateway(t, r, plan, ready)
	receipt, call := gatewayCall(t, r, plan.Definitions[0].Name)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := call.Wait(ctx); err == nil {
		t.Fatal("unknown native exchange reported success")
	}
	if err := r.Response(receipt, "error", "BackendUnavailable"); err != nil {
		t.Fatal(err)
	}
	observed := ready[0].State
	observed.ObservedAt = time.Now()
	observed.Native = &native.State{At: observed.ObservedAt, Ready: true, Metadata: native.Metadata{ProfileID: observed.Profile}}
	mismatch := observed
	mismatch.ContainerID = "containerd://replacement"
	if err := r.ObserveGatewayInstance(mismatch); err == nil {
		t.Fatal("different source discharged an uncertain physical owner")
	}
	if r.GatewayReport("router-1", time.Time{}).Outstanding[0].Count != 1 {
		t.Fatal("mismatched source changed physical ownership")
	}
	if err := r.ObserveGatewayInstance(observed); err != nil {
		t.Fatal(err)
	}
	if r.GatewayReport("router-1", time.Time{}).Outstanding[0].Count != 0 {
		t.Fatal("fresh exact-source native absence did not reconcile ownership")
	}
}

func TestGatewayClosedAdmissionStillDrainsAcceptedQueue(t *testing.T) {
	r, plan, ready := gatewayRuntime(t)
	started, release := make(chan Dispatch, 2), make(chan struct{})
	ready[0].Execute = func(ctx context.Context, d Dispatch) (BackendResult, error) {
		started <- d
		select {
		case <-release:
		case <-ctx.Done():
		}
		return BackendResult{Finished: true}, nil
	}
	installGateway(t, r, plan, ready)
	firstReceipt, first := gatewayCall(t, r, plan.Definitions[0].Name)
	<-started
	queuedReceipt, queued := gatewayCall(t, r, plan.Definitions[0].Name)
	oldReceipt, err := r.Receive(plan.Definitions[0].Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	next := plan
	next.PlanVersion = "closed"
	next.Admission = map[string]bool{plan.Definitions[0].UID: false}
	if err := r.ApplyGatewayPlan(next, ready); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Receive(plan.Definitions[0].Name, time.Now()); err == nil {
		t.Fatal("closed split admission accepted a new receipt")
	}
	if _, err := r.Accept(oldReceipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}}); err == nil {
		t.Fatal("closed split admission accepted an old connection's request")
	}
	close(release)
	finishGatewayCall(t, r, firstReceipt, first)
	dispatch := <-started
	if dispatch.Assignment.PlanVersion != plan.PlanVersion {
		t.Fatal("closing admission migrated queued accepted work")
	}
	finishGatewayCall(t, r, queuedReceipt, queued)
	if err := r.Response(oldReceipt, "error", "GatewayAdmissionStopped"); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayArrivalsReportOnlyCompleteObservedSeconds(t *testing.T) {
	r, plan, ready := gatewayRuntime(t)
	installGateway(t, r, plan, ready)
	at := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Second)
	r.mu.Lock()
	r.gatewayArrivalCursor = at
	receipt := Receipt{UID: plan.Definitions[0].UID, ReceivedAt: at.Add(time.Millisecond)}
	r.recordGatewayArrivalLocked(receipt, false)
	r.recordGatewayArrivalLocked(receipt, true)
	r.recordGatewayArrivalLocked(Receipt{ReceivedAt: at}, false)
	r.advanceGatewayArrivalsLocked(at.Add(2 * time.Second))
	r.gatewayArrivalCursor = time.Now().UTC().Truncate(time.Second).Add(time.Second)
	r.mu.Unlock()
	report := r.GatewayReport("router-1", time.Time{})
	if len(report.Arrivals) != 2 || report.Arrivals[0].From != at || report.Arrivals[0].Received != 1 || report.Arrivals[0].Validated != 1 || report.Arrivals[1].Received != 0 || report.Arrivals[1].Validated != 0 {
		t.Fatalf("known windows were duplicated or zero coverage was fabricated: %+v", report.Arrivals)
	}
	r.SetGatewayConnected(false)
	r.mu.Lock()
	r.gatewayArrivalCursor = at.Add(-time.Hour)
	r.advanceGatewayArrivalsLocked(time.Now())
	r.mu.Unlock()
	if after := r.GatewayReport("router-1", time.Time{}); len(after.Arrivals) != len(report.Arrivals) {
		t.Fatal("disconnected observation gap became synthetic zero windows")
	}
	r.SetGatewayConnected(true)
	r.mu.Lock()
	r.gatewayArrivalCursor = at.Add(-time.Hour)
	r.mu.Unlock()
	bounded := r.GatewayReport("router-1", time.Time{})
	if len(bounded.Arrivals) > 12 {
		t.Fatalf("arrival report exceeded its configured horizon: %d", len(bounded.Arrivals))
	}
}

func TestGatewayExactTerminationEvidenceNeedsNoNativeState(t *testing.T) {
	r, plan, ready := gatewayRuntime(t)
	ready[0].Execute = func(context.Context, Dispatch) (BackendResult, error) {
		return BackendResult{}, failure("BackendUnavailable", "submission result unknown")
	}
	installGateway(t, r, plan, ready)
	receipt, call := gatewayCall(t, r, plan.Definitions[0].Name)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = call.Wait(ctx)
	if err := r.Response(receipt, "error", "BackendUnavailable"); err != nil {
		t.Fatal(err)
	}
	stopped := ready[0].State
	stopped.Ready, stopped.Stopped = false, true
	if err := r.ObserveGatewayInstance(stopped); err != nil {
		t.Fatal(err)
	}
	if r.GatewayReport("router-1", time.Time{}).Outstanding[0].Count != 0 {
		t.Fatal("exact container termination retained impossible execution")
	}
}

func TestGatewayPodRetirementChecksRemoteEvenWithoutLocalInventory(t *testing.T) {
	r, _, _ := gatewayRuntime(t)
	r.config.GatewayPodDrained = func(uid string) bool { return uid == "confirmed-zero" }
	if err := r.RetirePod("missing-local-pod"); err == nil {
		t.Fatal("absent local inventory was mistaken for all-router Pod zero")
	}
	if err := r.RetirePod("confirmed-zero"); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayObservationCoverageFailureClosesGate(t *testing.T) {
	r, plan, ready := gatewayRuntime(t)
	installGateway(t, r, plan, ready)
	if err := r.AdvanceGatewayObservation(); err != nil {
		t.Fatal(err)
	}
	if err := r.config.ObservationLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.AdvanceGatewayObservation(); err == nil || r.GatewayReady() {
		t.Fatal("failed observation coverage left the synchronized gate open")
	}
}

func TestGatewayExpiredFreshnessDoesNotWaitForRuntimeLock(t *testing.T) {
	r, plan, ready := gatewayRuntime(t)
	installGateway(t, r, plan, ready)
	r.mu.Lock()
	locked := true
	defer func() {
		if locked {
			r.mu.Unlock()
		}
	}()
	r.SetGatewayFreshUntil(time.Now().Add(-time.Second))
	readyResult := make(chan bool, 1)
	go func() { readyResult <- r.GatewayReady() }()
	select {
	case ready := <-readyResult:
		if ready {
			t.Fatal("expired channel remained ready behind the runtime lock")
		}
	case <-time.After(time.Second):
		t.Fatal("expired readiness waited for stalled runtime processing")
	}
	r.mu.Unlock()
	locked = false
	r.SetGatewayConnected(true)
	if r.GatewayReady() {
		t.Fatal("late connected confirmation extended expired freshness")
	}
	if _, err := r.Receive(plan.Definitions[0].Name, time.Now()); err == nil {
		t.Fatal("expired channel accepted a new receipt")
	}
	r.SetGatewayFreshUntil(time.Now().Add(time.Minute))
	if !r.GatewayReady() {
		t.Fatal("fresh synchronized channel did not restore readiness")
	}
}

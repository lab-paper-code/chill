package kube

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
)

const controllerWorkTestOverload = "Overload"

func controllerWorkFixture(t *testing.T) (*SchedulerController, serving.SchedulerSnapshot, serving.SchedulerDecision) {
	t.Helper()
	controller, target, _ := acknowledgedHandoffFixture(t)
	instance := target.Instances[0]
	profile := *instance.Profile
	profile.Condition.PowerMode = "policy-mode"
	profile.Condition.CPU.AffinityCPUs = []int{0}
	profile.IdleBaselineWatts = 12
	profile.Evidence = []evidencestore.ObjectIdentity{{Store: "raw", Key: "profile", Size: 1}}
	profile.Batches = []serving.BatchProfile{{Batch: 1, DurationNanos: []float64{float64(time.Millisecond)}, EnergyPerInputJoules: 0.1, MemoryBytes: 100}}
	instance.Profile = &profile
	instance.IdleMeter, instance.IdleScope = "policy-wall", "node-exclusive-wall"
	at := time.Now().UTC()
	snapshot := serving.SchedulerSnapshot{At: at.Add(time.Millisecond), ObservationEnd: at,
		Workloads: controller.runtime.SchedulerInputs(time.Now).Workloads,
		Nodes:     []serving.SchedulerNode{{Node: instance.Node, Pool: instance.Pool, PowerMode: profile.Condition.PowerMode, Ready: true, CPUs: []int{0, 1}, MemoryBytes: 10000, ObservedAt: at}},
		Instances: []serving.SchedulerInstance{instance},
		Routing:   map[string]map[string]int32{instance.WorkloadUIDs[0]: {instance.ID: 1}},
		Baselines: []serving.NodeBaseline{{ID: "policy-idle", Node: instance.Node, PowerMode: profile.Condition.PowerMode, Watts: 10, Meter: instance.IdleMeter, Scope: instance.IdleScope, MeasuredAt: at, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "idle", Size: 1}}},
	}
	settings := serving.SchedulerSettings{Algorithm: serving.SchedulerAlgorithmProjectedWorkHistory, ObservationWindow: time.Second, ControlInterval: time.Second, Horizon: time.Second, MaxObservationAge: time.Hour, Replays: 1, MaxCandidates: 4, MaxReplicas: 2, MaxInstances: 4, MaxReplayRequests: 10, MaxCPUs: 2, Controls: serving.SchedulerControls{Enabled: true}}
	energy := 1.0
	keep := serving.SchedulerCandidate{ID: "keep", Keep: true, Nodes: snapshot.Nodes, Instances: snapshot.Instances, Routing: snapshot.Routing, Status: schedulerPolicyFeasible, ExpectedJoules: &energy}
	decision := serving.SchedulerDecision{Settings: settings, Keep: keep, Candidates: []serving.SchedulerCandidate{keep}, Target: &keep, Status: "Keep"}
	return controller, snapshot, decision
}

func TestControllerWorkPolicyConsumesDisjointIntervalsAndPreservesProgress(t *testing.T) {
	controller, snapshot, decision := controllerWorkFixture(t)
	work, first, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Policy.Selection != schedulerPolicyProjected || len(work.history.Values) != 1 {
		t.Fatalf("initial actual state missing: %+v", first.Policy)
	}
	controller.work = work
	from := work.history.Through
	snapshot.ObservationEnd, snapshot.At = from.Add(time.Second), from.Add(time.Second+time.Millisecond)
	snapshot.Actions = []serving.Action{{ID: "actual-route", Kind: operationRoute, Status: nativeOperationRunning}}
	snapshot.Outstanding = []serving.SchedulerRequest{{ID: "actual-running", Phase: "running"}}
	goal := decision.Keep
	snapshot.InProgressTarget = &goal
	interval := &serving.LoadWindow{From: from, To: snapshot.ObservationEnd, Duration: time.Second}
	advanced, result, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, interval, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Policy.PendingReason != "" || !advanced.history.Through.Equal(interval.To) || len(result.Policy.Kernels) != 1 {
		t.Fatalf("interval did not advance: %+v", result.Policy)
	}
	if snapshot.Actions[0].Status != nativeOperationRunning || snapshot.Outstanding[0].ID != "actual-running" || snapshot.InProgressTarget != &goal {
		t.Fatal("projection changed actual progress")
	}
	controller.work = advanced
	// Ordinary ticks and physical executor replacements do not consume the same interval again.
	snapshot.Instances = slices.Clone(snapshot.Instances)
	oldID := snapshot.Instances[0].ID
	snapshot.Instances[0].ID = "replacement-pod/container"
	snapshot.Routing = map[string]map[string]int32{snapshot.Workloads[0].UID: {snapshot.Instances[0].ID: 1}}
	decision.Keep.Instances, decision.Keep.Routing = snapshot.Instances, snapshot.Routing
	decision.Candidates = []serving.SchedulerCandidate{decision.Keep}
	unchanged, after, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if oldID == snapshot.Instances[0].ID || !unchanged.history.Through.Equal(advanced.history.Through) || len(unchanged.history.Values) != len(advanced.history.Values) || after.Policy.Kernels != nil {
		t.Fatal("replacement reset or duplicated history")
	}
	blocked, pending, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, nil, errors.New("missing interval evidence"))
	if err != nil {
		t.Fatal(err)
	}
	if !blocked.history.Through.Equal(advanced.history.Through) || pending.Policy.Selection != "KeepWhileHistoryPending" || pending.Policy.PendingReason == "" {
		t.Fatal("missing interval advanced cursor or became a projected choice")
	}
}

func TestControllerWorkPolicyRespectsActualSLOAndBaseline(t *testing.T) {
	controller, snapshot, decision := controllerWorkFixture(t)
	work, _, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	controller.work = work
	alternative := decision.Keep
	alternative.Keep, alternative.ID = false, "different-batching"
	alternative.Instances = slices.Clone(alternative.Instances)
	alternative.Instances[0].MaxWait = time.Millisecond
	energy := 10.0
	alternative.ExpectedJoules = &energy
	decision.Candidates = append(decision.Candidates, alternative)
	key, err := serving.SchedulerLogicalConfigKey(snapshot, alternative)
	if err != nil {
		t.Fatal(err)
	}
	for current := range work.history.Values {
		work.history.Values[current] = 100
	}
	work.history.Values[key] = 0
	_, selected, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Target == nil || selected.Target.Keep || selected.Policy.Selection != schedulerPolicyProjected {
		t.Fatal("history energy choice never reached target finalization")
	}
	decision.Candidates[1].Status = controllerWorkTestOverload
	decision.Candidates[1].WorstExceedance = 1
	_, safe, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if safe.Target == nil || !safe.Target.Keep {
		t.Fatal("cheap infeasible history displaced actual feasible Keep")
	}
	decision.Settings.Algorithm = serving.SchedulerAlgorithmFixedHorizon
	_, baseline, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Target != decision.Target || baseline.Policy != nil {
		t.Fatal("baseline mode changed selection or allocated projected diagnostics")
	}
}

func TestControllerWorkPolicyPendingKeepsBoundAndIdentifiesSLOResponse(t *testing.T) {
	controller, snapshot, decision := controllerWorkFixture(t)
	work, _, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	controller.work = work
	alternative := decision.Keep
	alternative.ID, alternative.Keep = "new-feasible-policy", false
	alternative.Instances = slices.Clone(alternative.Instances)
	alternative.Instances[0].MaxWait = time.Millisecond
	decision.Candidates = append(decision.Candidates, alternative)
	decision.Settings.MaxCandidates = 1
	bounded, pending, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounded.configurations) != 1 || pending.Policy.PendingReason != "RetainedLogicalConfigurationsExceedMaxCandidates" || !pending.Target.Keep {
		t.Fatal("configured universe bound discarded retained history or silently grew")
	}
	decision.Settings.MaxCandidates = 4
	decision.Keep.Status, decision.Keep.WorstExceedance = controllerWorkTestOverload, 2
	decision.Candidates[0] = decision.Keep
	decision.Target = &alternative
	_, safety, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if safety.Target == nil || safety.Target.Keep || safety.Policy.Selection != schedulerPolicySLOResponse || safety.Policy.PendingReason == "" {
		t.Fatal("missing projected history hid the actual feasible SLO response")
	}
	decision.Candidates[1].Status, decision.Candidates[1].WorstExceedance = controllerWorkTestOverload, 1
	_, overload, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if overload.Target == nil || overload.Target.Keep || overload.Target.WorstExceedance != 1 || overload.Policy.Selection != schedulerPolicySLOResponse {
		t.Fatal("history replaced the existing all-infeasible overload ordering")
	}
}

func TestControllerWorkIntervalReadsDelayedSpanOnce(t *testing.T) {
	controller, snapshot, decision := controllerWorkFixture(t)
	work, _, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	controller.work = work
	from := work.history.Through
	definition := snapshot.Workloads[0]
	for index, offset := range []time.Duration{10 * time.Millisecond, 2 * time.Second} {
		id := []string{"early", "late"}[index]
		for _, kind := range []string{"received", "validated"} {
			if err := controller.journal.RequestEvent(serving.RequestEvent{RequestID: id, WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: kind, At: from.Add(offset), ReceivedAt: from.Add(offset)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	end := from.Add(2500 * time.Millisecond)
	interval, err := controller.workInterval(end, decision.Settings)
	if err != nil || interval == nil || len(interval.Arrivals) != 2 || !interval.From.Equal(from) || interval.Duration != 2500*time.Millisecond {
		t.Fatalf("delayed tick lost the earlier non-overlapping receipts: %+v %v", interval, err)
	}
	snapshot.ObservationEnd, snapshot.At = end, end.Add(time.Millisecond)
	advanced, result, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, interval, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Policy.PendingReason != "" || !advanced.history.Through.Equal(end) {
		t.Fatalf("complete delayed interval not consumed: %+v", result.Policy)
	}
	controller.work = advanced
	if repeat, err := controller.workInterval(end, decision.Settings); repeat != nil || err != nil {
		t.Fatal("same receipt interval was consumed twice")
	}
}

func TestControllerWorkIntervalWaitsForDurableReceiptClassification(t *testing.T) {
	controller, snapshot, decision := controllerWorkFixture(t)
	work, _, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	controller.work = work
	from := work.history.Through
	receivedAt := from.Add(10 * time.Millisecond)
	end := from.Add(time.Second)
	definition := snapshot.Workloads[0]
	if err := controller.journal.RequestEvent(serving.RequestEvent{RequestID: "classification-pending", WorkloadUID: definition.UID, Kind: "received", At: receivedAt, ReceivedAt: receivedAt}); err != nil {
		t.Fatal(err)
	}
	// Ordinary rolling observations retain the receipt without classifying it.
	rolling, err := controller.journal.LoadWindow(from, end)
	if err != nil || len(rolling.Arrivals) != 1 || rolling.Arrivals[0].Compute || rolling.Arrivals[0].Accepted {
		t.Fatalf("rolling observation contract changed: %+v %v", rolling, err)
	}
	interval, evidenceErr := controller.workInterval(end, decision.Settings)
	if evidenceErr == nil || interval != nil {
		t.Fatal("unclassified receipt became an empty successful history interval")
	}
	snapshot.ObservationEnd, snapshot.At = end, end.Add(time.Millisecond)
	blocked, pending, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, interval, evidenceErr)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked.history.Through.Equal(from) || pending.Policy.PendingReason == "" {
		t.Fatal("history cursor skipped a receipt before classification was durable")
	}
	controller.work = blocked
	// Classification can be appended after the cutoff while referring to the
	// original historical receipt; it must remain in the still-unconsumed span.
	if err := controller.journal.RequestEvent(serving.RequestEvent{RequestID: "classification-pending", WorkloadUID: definition.UID, Definition: definition.Identity(), Kind: "validated", At: end.Add(time.Millisecond), ReceivedAt: receivedAt}); err != nil {
		t.Fatal(err)
	}
	interval, err = controller.workInterval(end, decision.Settings)
	if err != nil || interval == nil || len(interval.Arrivals) != 1 || !interval.Arrivals[0].Compute || interval.Arrivals[0].Accepted || !interval.Arrivals[0].At.Equal(receivedAt) {
		t.Fatalf("late classification lost the historical compute demand: %+v %v", interval, err)
	}
	advanced, result, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, interval, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Policy.PendingReason != "" || !advanced.history.Through.Equal(end) {
		t.Fatalf("durably classified interval did not advance: %+v", result.Policy)
	}
	controller.work = advanced
	if repeat, err := controller.workInterval(end, decision.Settings); repeat != nil || err != nil {
		t.Fatal("classified receipt interval was consumed twice")
	}
}

func TestControllerCanceledProjectionPreservesCommittedHistory(t *testing.T) {
	controller, snapshot, decision := controllerWorkFixture(t)
	work, _, err := controller.applyWorkPolicy(context.Background(), snapshot, decision, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	controller.work = work
	through := work.history.Through
	interval := &serving.LoadWindow{From: through, To: through.Add(time.Second), Duration: time.Second}
	snapshot.ObservationEnd, snapshot.At = interval.To, interval.To.Add(time.Millisecond)
	decision.Settings.Replays = 1_000_000_000
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	next, result, err := controller.applyWorkPolicy(ctx, snapshot, decision, interval, nil)
	if !errors.Is(err, context.DeadlineExceeded) || next != nil || result.Status != "" {
		t.Fatalf("canceled kernel became a partial history decision: %+v %+v %v", next, result, err)
	}
	if controller.work != work || !work.history.Through.Equal(through) || len(work.history.Values) != 1 || len(work.configurations) != 1 {
		t.Fatal("canceled projection changed the previously committed history")
	}
}

package serving

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

func TestCapacityTableAppliedInputsKeepMissingUIDLocalAcrossEngines(t *testing.T) {
	for _, strategy := range []string{protocol.V1, protocol.V2} {
		t.Run(strategy, func(t *testing.T) {
			settings, snapshot := capacityFixture()
			settings.PlanSearchStrategy, settings.CompletionMarginMS = strategy, 3
			// Exercise lifecycle and both engines without the dense replay benchmark.
			settings.CapacityGridSteps, settings.CapacityBisectionWidth, settings.CapacityTailSamples = 4, .1, 1
			other := *snapshot.Workloads[0]
			other.UID, other.Name = "w2", schedulerTestOther
			snapshot.Workloads = append(snapshot.Workloads, &other)
			settings.AutonomousWorkloads = append(settings.AutonomousWorkloads, other.UID)
			snapshot.Options[0].CompatibleUIDs = slices.Clone(settings.AutonomousWorkloads)
			snapshot = capacityLifecycleBudgets(snapshot, 5, 900)
			table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, nil))
			table.Ensure(context.Background(), settings, snapshot)
			first := nextCapacityBuild(t, started)
			if first.uid != snapshot.Workloads[0].UID {
				t.Fatalf("unexpected initial workload: %+v", first)
			}
			close(first.release)
			missing := nextCapacityBuild(t, started)
			if missing.uid != other.UID {
				t.Fatalf("missing UID did not remain queued: %+v", missing)
			}
			requireActiveCapacityUID(t, table, settings, snapshot)
			if rows := table.Rows(); len(rows) != 2 || rows[1].Status != CapacityPending {
				t.Fatalf("management lost pending UID: %+v", rows)
			}
			close(missing.release)
			table.Wait()
			rows, budgets := table.Inputs()
			applied := snapshot
			applied.PathBudgetObservations, applied.PathBudgets = snapshot.PathBudgets, budgets
			decision := capacityDecideForTest(t, settings, applied, rows)
			if len(rows) != 2 || len(budgets) != 2 || !slices.Contains(decision.Reasons, schedulerPathBudgetExceedsSLO+":"+other.UID) {
				t.Fatalf("completed UID did not consume its own observed pair: %+v", decision)
			}
			changed := other
			changed.Generation++
			snapshot.Workloads[1] = &changed
			snapshot = capacityLifecycleBudgets(snapshot, 5, 900)
			table.Ensure(context.Background(), settings, snapshot)
			table.Wait()
			rows, budgets = table.Inputs()
			if len(rows) != 2 || len(budgets) != 2 || budgets[1].DefinitionID != changed.Identity() {
				t.Fatalf("definition provenance refresh lost shared numerical rows: rows=%+v budgets=%+v", rows, budgets)
			}
			select {
			case running := <-started:
				t.Fatalf("definition provenance refresh repeated numerical work: %+v", running)
			default:
			}
		})
	}
}

func requireActiveCapacityUID(t *testing.T, table *CapacityTable, settings SchedulerSettings, snapshot SchedulerSnapshot) {
	t.Helper()
	readyUID, missingUID := snapshot.Workloads[0].UID, snapshot.Workloads[1].UID
	rows, budgets := table.Inputs()
	if len(rows) != 1 || len(budgets) != 1 || budgets[0].WorkloadUID != readyUID || rows[0].Input.WorkloadUID != readyUID {
		t.Fatalf("missing UID leaked rows or budget: rows=%+v budgets=%+v", rows, budgets)
	}
	applied := snapshot
	applied.PathBudgetObservations, applied.PathBudgets = snapshot.PathBudgets, budgets
	fallback := ResolvePathBudget(settings, applied.PathBudgets, applied.Workloads[1])
	if fallback.Source != PathBudgetConfigured || fallback.TotalMS != 3 || fallback.PostMS != 3 {
		t.Fatalf("missing UID lost configured fallback: %+v", fallback)
	}
	decision := capacityDecideForTest(t, settings, applied, rows)
	for _, workload := range decision.Workloads {
		if workload.UID == readyUID && workload.State != WorkloadControlled ||
			workload.UID == missingUID && (workload.State != schedulerWorkloadFixed || workload.Reason != CapacityPending+":"+missingUID) {
			t.Fatalf("missing UID blocked independent active capacity: %+v", decision)
		}
	}
	if !reflect.DeepEqual(decision.PathBudgets, budgets) || !reflect.DeepEqual(decision.PathBudgetObservations, snapshot.PathBudgets) ||
		decision.Capacity == nil || len(decision.Capacity.Workloads) != 1 || decision.Capacity.Workloads[0].UID != readyUID {
		t.Fatalf("engine consumed desired rather than active evidence: %+v", decision)
	}
	planner := newCapacityPlanner(context.Background(), settings, applied, decision.Keep, rows, decision)
	if len(planner.ws) != 1 || planner.ws[0].slack != applied.Workloads[0].SLO.TargetLatency.Seconds()-.005 || rows[0].Key != rows[0].Input.Key() {
		t.Fatalf("slack or row key lost the active numerical pair: %+v", planner.ws)
	}
}

func TestCapacityTableActivationAndWithdrawalFreezeRuntimeRequestBudgets(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	d := r.workloads["w1"].definition
	r.mu.Lock()
	r.instances[instance.ID].outstanding = 1 // Keep acceptances queued across table transitions.
	r.mu.Unlock()
	settings, snapshot := capacityLifecycleFixture(2)
	settings.CompletionMarginMS = 13
	snapshot.Workloads = []*Definition{d}
	profile := *snapshot.Options[0].Instance.Profile
	for index, batch := range profile.Batches {
		profile.Batches[index] = schedulerTestBatchProfile(BatchProfile{Batch: batch.Batch}, []float64{float64(d.SLO.TargetLatency) + 1})
	}
	snapshot.Options[0].Instance.Profile = &profile
	desired := capacityHysteresisBudget(snapshot, 10, 6, capacityLower(8), capacityLower(4))
	desired.PathBudgets[0].JudgmentPercentile = 100
	table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, nil))
	r.SetPathBudgetPolicy(settings)
	apply := func() {
		_, budgets := table.Inputs()
		r.SetPathBudgets(budgets)
	}
	assertDefault := func(total, post float64) {
		t.Helper()
		r.mu.Lock()
		margins := r.pathMarginsLocked(d.Identity())
		r.mu.Unlock()
		if margins.totalMS != total || margins.postMS != post {
			t.Fatalf("runtime default does not match applied table: %+v", margins)
		}
	}
	calls := []*Call{}
	frozen := map[string]runtimePathMargins{}
	accept := func(total, post float64) {
		t.Helper()
		receipt, err := r.Receive(d.Name, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
		frozen[call.receipt.ID] = runtimePathMargins{totalMS: total, postMS: post}
	}
	table.Ensure(context.Background(), settings, desired)
	first := nextCapacityBuild(t, started)
	apply()
	assertDefault(13, 13)
	accept(13, 13)
	close(first.release)
	second := nextCapacityBuild(t, started)
	apply()
	assertDefault(13, 13)
	close(second.release)
	table.Wait()
	apply()
	assertDefault(10, 6)
	accept(10, 6)
	desired.PathBudgets[0].TotalMS, desired.PathBudgets[0].PostMS = 20, 12
	table.Ensure(context.Background(), settings, desired)
	obsolete := nextCapacityBuild(t, started)
	apply()
	assertDefault(10, 6)
	profile.ID += "-replacement"
	table.Ensure(context.Background(), settings, desired)
	if obsolete.ctx.Err() != context.Canceled {
		t.Fatal("structural invalidation kept old observed work")
	}
	close(obsolete.release)
	replacement := nextCapacityBuild(t, started)
	apply()
	assertDefault(13, 13)
	accept(13, 13)
	enabled := false
	settings.ObservedPathBudgets, settings.CompletionMarginMS = &enabled, 25
	r.SetPathBudgetPolicy(settings)
	table.Configure(settings)
	apply()
	assertDefault(25, 25)
	accept(25, 25)
	if replacement.ctx.Err() != context.Canceled {
		t.Fatal("OFF kept incompatible observed work")
	}
	close(replacement.release)
	table.Wait()
	r.mu.Lock()
	outstanding := r.schedulerOutstandingLocked(time.Now())
	r.instances[instance.ID].outstanding = 0
	r.dispatchLocked()
	r.mu.Unlock()
	if len(outstanding) != len(calls) {
		t.Fatalf("accepted requests disappeared across activation: %+v", outstanding)
	}
	for _, request := range outstanding {
		want := frozen[request.ID]
		if request.TotalMarginMS == nil || request.CompletionMarginMS == nil || *request.TotalMarginMS != want.totalMS || *request.CompletionMarginMS != want.postMS {
			t.Fatalf("active/default transition changed an accepted budget: %+v", request)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, call := range calls {
		if _, err := call.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

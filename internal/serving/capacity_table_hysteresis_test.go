package serving

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func capacityLower(ms float64) *float64 { return &ms }

func capacityHysteresisBudget(snapshot SchedulerSnapshot, total, post float64, totalLower, postLower *float64) SchedulerSnapshot {
	w := snapshot.Workloads[0]
	snapshot.PathBudgets = []PathBudget{{WorkloadUID: w.UID, DefinitionID: w.Identity(), Source: PathBudgetObserved,
		From: snapshot.At.Add(-time.Minute), To: snapshot.At, TotalMS: total, PostMS: post,
		TotalLowerMS: totalLower, PostLowerMS: postLower, JudgmentPercentile: 97.5, Samples: 200}}
	return snapshot
}

func TestCapacityTableObservedPairHysteresis(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		total, post           float64
		totalLower, postLower *float64
		build                 bool
	}{
		{name: "same numbers refresh bounds", total: 10, post: 6, totalLower: capacityLower(8), postLower: capacityLower(4)},
		{name: "total overlap", total: 9, post: 6, totalLower: capacityLower(8), postLower: capacityLower(4)},
		{name: "total equality", total: 8, post: 6, totalLower: capacityLower(8), postLower: capacityLower(4)},
		{name: "missing total lower", total: 1, post: 6, postLower: capacityLower(4)},
		{name: "post overlap", total: 10, post: 5, totalLower: capacityLower(8), postLower: capacityLower(4)},
		{name: "post equality", total: 10, post: 4, totalLower: capacityLower(8), postLower: capacityLower(4)},
		{name: "missing post lower", total: 10, post: 1, totalLower: capacityLower(8)},
		{name: "one decrease overlaps", total: 7, post: 5, totalLower: capacityLower(8), postLower: capacityLower(4)},
		{name: "both separated", total: 7, post: 3, totalLower: capacityLower(8), postLower: capacityLower(4), build: true},
		{name: "total separated unchanged post needs no lower", total: 7, post: 6, totalLower: capacityLower(8), build: true},
		{name: "post separated unchanged total needs no lower", total: 10, post: 3, postLower: capacityLower(4), build: true},
		{name: "total increase", total: 11, post: 6, build: true},
		{name: "post increase", total: 10, post: 7, build: true},
		{name: "total increase takes lower post", total: 11, post: 1, build: true},
		{name: "post increase takes lower total", total: 1, post: 7, build: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, snapshot := capacityLifecycleFixture(2)
			active := capacityHysteresisBudget(snapshot, 10, 6, tc.totalLower, tc.postLower)
			table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, nil))
			table.Ensure(context.Background(), settings, active)
			for range 2 {
				close(nextCapacityBuild(t, started).release)
			}
			table.Wait()
			before, original := table.Inputs()
			candidate := capacityHysteresisBudget(snapshot, tc.total, tc.post, capacityLower(0), capacityLower(0))
			candidate.PathBudgets[0].From = candidate.PathBudgets[0].From.Add(time.Second)
			candidate.PathBudgets[0].To = candidate.PathBudgets[0].To.Add(time.Second)
			candidate.PathBudgets[0].Samples++
			table.Ensure(context.Background(), settings, candidate)
			if tc.build {
				for range 2 {
					boundary := nextCapacityBuild(t, started)
					if !reflect.DeepEqual(boundary.budget, candidate.PathBudgets[0]) {
						t.Fatalf("builder mixed windows or coordinates: %+v", boundary.budget)
					}
					rows, budgets := table.Inputs()
					if !reflect.DeepEqual(before, rows) || !reflect.DeepEqual(original, budgets) {
						t.Fatalf("partial replacement changed active group: rows=%+v budgets=%+v", rows, budgets)
					}
					close(boundary.release)
				}
				table.Wait()
			} else {
				if table.WorkerStatus().Active != 0 {
					t.Fatal("held or equivalent pair started numerical work")
				}
				select {
				case boundary := <-started:
					t.Fatalf("held pair admitted: %+v", boundary.budget)
				default:
				}
			}
			rows, budgets := table.Inputs()
			want := original[0]
			if tc.build || tc.total == 10 && tc.post == 6 {
				want = candidate.PathBudgets[0]
			}
			if len(rows) != 2 || len(budgets) != 1 || !reflect.DeepEqual(budgets[0], want) {
				t.Fatalf("wrong active pair or reference metadata: rows=%+v budgets=%+v want=%+v", rows, budgets, want)
			}
			for _, row := range rows {
				if row.Key != row.Input.Key() || row.Input.CompletionMarginMS != want.TotalMS || row.Input.PostMarginMS != want.PostMS ||
					row.Input.JudgmentPercentile != want.JudgmentPercentile || row.Input.SLO.Percentile != 95 {
					t.Fatalf("active row key or native percentile mismatches budget: %+v", row)
				}
			}
			if !tc.build && !reflect.DeepEqual(before, rows) {
				t.Fatal("metadata-only refresh changed numerical rows")
			}
		})
	}
}

func TestCapacityTableHeldCandidateKeepsActiveReference(t *testing.T) {
	settings, snapshot := capacityLifecycleFixture(1)
	table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, nil))
	table.Ensure(context.Background(), settings, capacityHysteresisBudget(snapshot, 10, 6, capacityLower(8), capacityLower(4)))
	close(nextCapacityBuild(t, started).release)
	table.Wait()
	// Each candidate has a low lower bound. Neither may replace the active
	// reference and thereby prevent the later genuinely separated decrease.
	for _, total := range []float64{9, 8} {
		table.Ensure(context.Background(), settings, capacityHysteresisBudget(snapshot, total, 6, capacityLower(0), capacityLower(0)))
	}
	table.Ensure(context.Background(), settings, capacityHysteresisBudget(snapshot, 7, 3, capacityLower(1), capacityLower(1)))
	boundary := nextCapacityBuild(t, started)
	if boundary.budget.TotalMS != 7 || boundary.budget.PostMS != 3 {
		t.Fatalf("held window replaced the active confidence reference: %+v", boundary.budget)
	}
	close(boundary.release)
	table.Wait()
	assertCapacityGroup(t, table, 7, 1, CapacityBuilt)
}

func TestCapacityTableQueuedPairRecheckedAfterRunningCompletion(t *testing.T) {
	for _, next := range []struct {
		name       string
		total      float64
		activeLow  float64
		wantBuild  bool
	}{
		{name: "overlap against completed upward pair holds", total: 8, activeLow: 7},
		{name: "separation against completed upward pair builds", total: 8, activeLow: 9, wantBuild: true},
		{name: "queued decrease becomes upward", total: 9, activeLow: 1, wantBuild: true},
	} {
		t.Run(next.name, func(t *testing.T) {
			settings, snapshot := capacityLifecycleFixture(1)
			table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, nil))
			table.Ensure(context.Background(), settings, capacityHysteresisBudget(snapshot, 10, 6, capacityLower(9.5), capacityLower(5.5)))
			close(nextCapacityBuild(t, started).release)
			table.Wait()
			runningTotal := 12.0
			if next.name == "queued decrease becomes upward" {
				runningTotal = 7
			}
			table.Ensure(context.Background(), settings, capacityHysteresisBudget(snapshot, runningTotal, 6, capacityLower(next.activeLow), capacityLower(5)))
			running := nextCapacityBuild(t, started)
			table.Ensure(context.Background(), settings, capacityHysteresisBudget(snapshot, next.total, 6, capacityLower(0), capacityLower(0)))
			if running.ctx.Err() != nil {
				t.Fatal("queued observation canceled admitted work")
			}
			close(running.release)
			want := runningTotal
			if next.wantBuild {
				successor := nextCapacityBuild(t, started)
				if successor.budget.TotalMS != next.total {
					t.Fatalf("wrong rechecked successor: %+v", successor.budget)
				}
				close(successor.release)
				want = next.total
			}
			table.Wait()
			assertCapacityGroup(t, table, want, 1, CapacityBuilt)
			select {
			case boundary := <-started:
				t.Fatalf("overlapping queued pair was admitted: %+v", boundary.budget)
			default:
			}
		})
	}
}

func TestCapacityTableBudgetPolicyTransitionsBypassObservedHysteresis(t *testing.T) {
	for _, transition := range []string{"configured to observed", "observed to configured", "configured margin", "judgment percentile", "definition"} {
		t.Run(transition, func(t *testing.T) {
			settings, snapshot := capacityLifecycleFixture(1)
			settings.CompletionMarginMS = 10
			active := capacityHysteresisBudget(snapshot, 10, 6, nil, nil)
			if transition == "configured to observed" || transition == "configured margin" {
				active.PathBudgets = nil
			}
			table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, nil))
			table.Ensure(context.Background(), settings, active)
			close(nextCapacityBuild(t, started).release)
			table.Wait()
			candidate := capacityHysteresisBudget(snapshot, 1, 1, nil, nil)
			switch transition {
			case "observed to configured", "configured margin":
				settings.CompletionMarginMS = 1
				candidate.PathBudgets = nil
			case "judgment percentile":
				candidate.PathBudgets[0].JudgmentPercentile = 99
			case "definition":
				w := *snapshot.Workloads[0]
				w.Generation++
				candidate.Workloads = []*Definition{&w}
				candidate.PathBudgets[0].DefinitionID = w.Identity()
			}
			table.Ensure(context.Background(), settings, candidate)
			boundary := nextCapacityBuild(t, started)
			if boundary.budget.TotalMS != 1 || boundary.budget.PostMS != 1 {
				t.Fatalf("policy transition was compared across distributions: %+v", boundary.budget)
			}
			close(boundary.release)
			table.Wait()
			assertCapacityGroup(t, table, 1, 1, CapacityBuilt)
		})
	}
}

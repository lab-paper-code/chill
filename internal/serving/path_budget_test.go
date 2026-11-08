package serving

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func TestPathUpperRankBoundaries(t *testing.T) {
	for _, tc := range []struct {
		n    int
		q    float64
		want int
	}{
		{0, .975, 0}, {118, .975, 0}, {119, .975, 119}, {120, .975, 120},
		{200, .975, 199}, {597, .995, 0}, {598, .995, 598},
		{4, .5, 0}, {5, .5, 5}, {10, .5, 9},
		{1000, 1, 0}, {10, 0, 0}, {10, math.NaN(), 0},
	} {
		t.Run(fmt.Sprintf("n=%d/q=%g", tc.n, tc.q), func(t *testing.T) {
			if got := pathUpperRank(tc.n, tc.q); got != tc.want {
				t.Fatalf("rank = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPathLowerRankBoundaries(t *testing.T) {
	for _, tc := range []struct {
		n    int
		q    float64
		want int
	}{
		{0, .5, 0}, {4, .5, 0}, {5, .5, 1}, {10, .5, 2},
		{1, .94, 0}, {1, .96, 1}, {11, .75, 7},
		{10, 0, 0}, {10, 1, 0}, {10, math.NaN(), 0},
	} {
		t.Run(fmt.Sprintf("n=%d/q=%g", tc.n, tc.q), func(t *testing.T) {
			if got := pathLowerRank(tc.n, tc.q); got != tc.want {
				t.Fatalf("lower rank = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPathBudgetsSeparateLowerAndUpperOrderStatistics(t *testing.T) {
	from := time.Unix(1000, 0)
	d := &Definition{UID: "workload", SLO: SLO{Percentile: 50}}
	rows := pathBudgetTestRows(d, from, 11)
	for i := range rows {
		rows[i] = pathBudgetTestRow(d, from.Add(time.Second), int64(10-i))
	}
	got := ObservePathBudgets(rows, map[string]*Definition{d.Identity(): d}, from, from.Add(time.Minute))[0]
	if got.Source != PathBudgetObserved || got.TotalMS != 42 || got.PostMS != 25 ||
		got.TotalLowerMS == nil || *got.TotalLowerMS != 38 || got.PostLowerMS == nil || *got.PostLowerMS != 21 {
		t.Fatalf("separate marginal ranks: %+v", got)
	}
	if got.Samples != 11 || got.Completed != 11 || got.Batches != 1 || got.MaxBatchSamples != 11 || got.Missing != 0 {
		t.Fatalf("bounds changed request sample population: %+v", got)
	}
}

func pathBudgetTestRow(d *Definition, received time.Time, extraMS int64) *RequestRow {
	elapsed, accepted, dispatched := (100+extraMS)*int64(time.Millisecond), int64(10*time.Millisecond), int64(30*time.Millisecond)
	native := 50.0
	queued := time.Unix(123, 0)
	running, finished := queued.Add(8*time.Millisecond), queued.Add(40*time.Millisecond)
	row := &RequestRow{
		Kind: RequestRowFinal, RequestID: "request", WorkloadUID: d.UID, DefinitionID: d.Identity(), ReceivedAt: received,
		ElapsedNanos: &elapsed, AcceptedNanos: &accepted, DispatchedNanos: &dispatched,
		Outcome: outcomeCompleted, ExecutionComplete: true, Instance: "instance", BatchID: 1, BatchSize: 4,
		NativeQueuedAt: &queued, NativeRunningAt: &running, NativeFinishedAt: &finished,
		NativeElapsedMS: &native, NativeExecutionComplete: true, NativeOutcome: outcomeCompleted, QueueMS: 10,
	}
	row.ServerTiming.Record(ServerSenderFreeze, 35*time.Millisecond)
	return row
}

func pathBudgetTestRows(d *Definition, from time.Time, n int) []*RequestRow {
	rows := make([]*RequestRow, n)
	for i := range rows {
		rows[i] = pathBudgetTestRow(d, from.Add(time.Second), 0)
		rows[i].RequestID = fmt.Sprint(i)
	}
	return rows
}

func TestPathBudgetsSupportAndDefinitionSegregation(t *testing.T) {
	from := time.Unix(1000, 0)
	to := from.Add(time.Minute)
	for _, tc := range []struct {
		p        float64
		n        int
		observed bool
	}{
		{95, 118, false}, {95, 119, true}, {99, 597, false}, {99, 598, true}, {100, 1000, false},
	} {
		t.Run(fmt.Sprintf("p=%g/n=%d", tc.p, tc.n), func(t *testing.T) {
			d := &Definition{UID: "workload", Generation: 1, SLO: SLO{Percentile: tc.p}}
			rows := pathBudgetTestRows(d, from, tc.n)
			got := ObservePathBudgets(rows, map[string]*Definition{d.Identity(): d}, from, to)[0]
			if (got.Source == PathBudgetObserved) != tc.observed || got.Samples != tc.n {
				t.Fatalf("sample support: %+v", got)
			}
			if tc.observed && (got.TotalMS != 32 || got.PostMS != 15 || math.Abs(got.JudgmentPercentile-(100-(100-tc.p)/2)) > 1e-12) {
				t.Fatalf("path bound or judgment percentile: %+v", got)
			}
			if tc.observed && (got.TotalLowerMS == nil || *got.TotalLowerMS != 32 || got.PostLowerMS == nil || *got.PostLowerMS != 15) {
				t.Fatalf("observed lower limits missing: %+v", got)
			}
			if !tc.observed && (got.TotalLowerMS != nil || got.PostLowerMS != nil) {
				t.Fatalf("configured source manufactured lower limits: %+v", got)
			}
			if !tc.observed && (got.TotalMS != 0 || got.PostMS != 0) {
				t.Fatalf("configured source manufactured measured bounds: %+v", got)
			}
		})
	}
	first := &Definition{UID: "workload", Generation: 1, SLO: SLO{Percentile: 95}}
	second := &Definition{UID: "workload", Generation: 2, SLO: SLO{Percentile: 95}}
	other := &Definition{UID: schedulerTestOther, Generation: 1, SLO: SLO{Percentile: 95}}
	rows := append(pathBudgetTestRows(first, from, 119), pathBudgetTestRows(second, from, 118)...)
	rows = append(rows, pathBudgetTestRows(other, from, 118)...)
	definitions := map[string]*Definition{first.Identity(): first, second.Identity(): second, other.Identity(): other}
	got := ObservePathBudgets(rows, definitions, from, to)
	if len(got) != 3 {
		t.Fatalf("definitions merged: %+v", got)
	}
	for _, budget := range got {
		if (budget.Source == PathBudgetObserved) != (budget.DefinitionID == first.Identity()) {
			t.Fatalf("support borrowed from another definition or UID: %+v", got)
		}
	}
	delete(definitions, first.Identity())
	if budget := ObservePathBudgets(rows[:119], definitions, from, to)[0]; budget.Source != PathBudgetConfigured || budget.Reason != "definition_missing" {
		t.Fatalf("missing authoritative definition accepted: %+v", budget)
	}
}

func TestPathBudgetsOrderStatisticOutliersAndBatchDependence(t *testing.T) {
	from := time.Unix(1000, 0)
	to := from.Add(time.Minute)
	d := &Definition{UID: "workload", SLO: SLO{Percentile: 95}}
	definitions := map[string]*Definition{d.Identity(): d}
	rows := pathBudgetTestRows(d, from, 200)
	rows[0] = pathBudgetTestRow(d, from.Add(time.Second), 1000)
	got := ObservePathBudgets(rows, definitions, from, to)[0]
	if got.Source != PathBudgetObserved || got.TotalMS != 32 || got.PostMS != 15 || got.Batches != 1 || got.MaxBatchSamples != 200 || got.Samples != 200 {
		t.Fatalf("one outlier or correlated batch changed request-weighted rank: %+v", got)
	}
	rows[1] = pathBudgetTestRow(d, from.Add(time.Second), 1000)
	got = ObservePathBudgets(rows, definitions, from, to)[0]
	if got.TotalMS != 1032 || got.PostMS != 1015 {
		t.Fatalf("second tail outlier ignored: %+v", got)
	}
	// Select the two marginal bounds separately; request pairing must not turn
	// an extreme total bound into an unrelated extreme post bound.
	rows[0].ServerTiming = RequestTimings{}
	rows[0].ServerTiming.Record(ServerSenderFreeze, 1035*time.Millisecond)
	got = ObservePathBudgets(rows, definitions, from, to)[0]
	if got.TotalMS != 1032 || got.PostMS != 15 {
		t.Fatalf("total and post order statistics coupled: %+v", got)
	}
}

func TestPathBudgetsCompletionWindowAndReceiptCohort(t *testing.T) {
	from := time.Unix(1000, 0)
	to := from.Add(time.Second)
	d := &Definition{UID: "workload", SLO: SLO{Percentile: 95}}
	old := pathBudgetTestRow(d, from.Add(-100*time.Millisecond), 0)
	normal := pathBudgetTestRow(d, from.Add(100*time.Millisecond), 0)
	atEnd := pathBudgetTestRow(d, to.Add(-100*time.Millisecond), 0)
	failed := pathBudgetTestRow(d, from.Add(200*time.Millisecond), 0)
	failed.Outcome = runtimeTimingCanceled
	unknown := pathBudgetTestRow(d, from.Add(300*time.Millisecond), 0)
	unknown.Outcome = ""
	open := pathBudgetTestRow(d, from.Add(400*time.Millisecond), 0)
	open.ElapsedNanos, open.Outcome = nil, ""
	outside := pathBudgetTestRow(d, from.Add(-time.Second), 0)
	newReceipt := pathBudgetTestRow(d, to, 0)
	physical := pathBudgetTestRow(d, from, 0)
	physical.Kind = RequestRowNative
	got := ObservePathBudgets([]*RequestRow{old, normal, atEnd, failed, unknown, open, outside, newReceipt, physical}, map[string]*Definition{d.Identity(): d}, from, to)[0]
	if got.Received != 5 || got.Completed != 4 || got.Samples != 2 || got.Missing != 2 || got.Incomplete != 3 || got.Failed != 1 || len(got.MissingReasons) == 0 {
		t.Fatalf("completion population mixed with receipt cohort: %+v", got)
	}
}

func TestPathBudgetsMissingSamplesCannotSupplySupportOrZero(t *testing.T) {
	from := time.Unix(1000, 0)
	to := from.Add(time.Minute)
	d := &Definition{UID: "workload", SLO: SLO{Percentile: 95}}
	definitions := map[string]*Definition{d.Identity(): d}
	for _, mutate := range []func(*RequestRow){
		func(row *RequestRow) { row.NativeRunningAt = nil },
		func(row *RequestRow) { row.NativeExecutionComplete = false },
		func(row *RequestRow) { row.NativeOutcome = "error" },
		func(row *RequestRow) { row.Outcome = "" },
		func(row *RequestRow) { row.Error = "failed response" },
		func(row *RequestRow) { *row.NativeElapsedMS = math.NaN() },
	} {
		rows := pathBudgetTestRows(d, from, 119)
		mutate(rows[0])
		got := ObservePathBudgets(rows, definitions, from, to)[0]
		if got.Source != PathBudgetConfigured || got.Samples != 118 || got.Missing != 1 || got.Completed != 119 || len(got.MissingReasons) == 0 {
			t.Fatalf("unusable request became successful zero: %+v", got)
		}
	}
	rows := pathBudgetTestRows(d, from, 119)
	for _, row := range rows {
		*row.ElapsedNanos, *row.AcceptedNanos, *row.DispatchedNanos = int64(50*time.Millisecond), 0, 0
		row.QueueMS = 8
		row.ServerTiming = RequestTimings{}
		row.ServerTiming.Record(ServerSenderFreeze, 0)
	}
	if got := ObservePathBudgets(rows, definitions, from, to)[0]; got.Source != PathBudgetObserved || got.TotalMS != 0 || got.PostMS != 0 {
		t.Fatalf("measured zero treated as missing: %+v", got)
	} else if got.TotalLowerMS == nil || *got.TotalLowerMS != 0 || got.PostLowerMS == nil || *got.PostLowerMS != 0 {
		t.Fatalf("measured lower zero treated as missing: %+v", got)
	}
}

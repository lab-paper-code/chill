package serving

import (
	"math"
	"sort"
	"time"

	"gonum.org/v1/gonum/stat/distuv"
)

// PathBudget summarizes request-weighted path observations for one immutable
// Workload definition. Only Source == "observed" supplies measured bounds;
// configured budgets leave TotalMS/PostMS unset for the settings adapter.
// The two bounds each use one-sided 95% confidence under an IID assumption,
// not joint 95% confidence or independent verification of correlated batches.
// No request samples survive this reduction.
type PathBudget struct {
	WorkloadUID        string    `json:"workloadUID"`
	DefinitionID       string    `json:"definitionID"`
	Source             string    `json:"source"`
	Reason             string    `json:"reason,omitempty"`
	From               time.Time `json:"from"`
	To                 time.Time `json:"to"`
	TotalMS            float64   `json:"totalMS"`
	PostMS             float64   `json:"postMS"`
	JudgmentPercentile float64   `json:"judgmentPercentile"`
	Samples            int       `json:"samples"`
	Received           int       `json:"received"`
	// Completed counts HTTP response events in [From,To), including unusable
	// outcomes. Missing = Completed - Samples; it never creates a zero sample.
	Completed int `json:"completed"`
	Missing   int `json:"missing"`
	// Incomplete and Failed concern only the received cohort, as of To.
	// Unknown outcomes and responses at/after To remain incomplete.
	// MissingReasons reports native validity separately from the HTTP outcome.
	Incomplete     int            `json:"incomplete"`
	Failed         int            `json:"failed"`
	MissingReasons map[string]int `json:"missingReasons,omitempty"`
	// Batch counts diagnose dependence without replacing the request population
	// by equally weighted batch maxima or claiming IID support was verified.
	Batches          int `json:"batches"`
	MaxBatchSamples  int `json:"maxBatchSamples"`
	UnbatchedSamples int `json:"unbatchedSamples"`
}

type pathBudgetKey struct{ uid, definition string }
type pathBatchKey struct {
	instance string
	id       uint64
}
type pathBudgetSamples struct {
	budget PathBudget
	total  []int64
	post   []int64
	batches map[pathBatchKey]int
}

// ObservePathBudgets reuses the caller's merged HTTP request rows and authoritative
// definition catalog. Completion timing selects samples; receipt timing selects
// the separate cohort diagnostics. Late physical-only rows are never HTTP samples.
func ObservePathBudgets(rows []*RequestRow, definitions map[string]*Definition, from, to time.Time) []PathBudget {
	groups := make(map[pathBudgetKey]*pathBudgetSamples)
	for _, row := range rows {
		if row == nil || row.Kind == RequestRowNative || row.ReceivedAt.IsZero() {
			continue
		}
		received := ObservationInInterval(row.ReceivedAt, from, to)
		var ended time.Time
		if row.ElapsedNanos != nil && *row.ElapsedNanos >= 0 {
			ended = row.ReceivedAt.Add(time.Duration(*row.ElapsedNanos))
		}
		completed := ObservationInInterval(ended, from, to)
		if !received && !completed {
			continue
		}
		key := pathBudgetKey{row.WorkloadUID, row.DefinitionID}
		group := groups[key]
		if group == nil {
			group = &pathBudgetSamples{budget: PathBudget{
				WorkloadUID: key.uid, DefinitionID: key.definition, From: from, To: to, Source: "configured",
			}}
			groups[key] = group
		}
		budget := &group.budget
		if received {
			budget.Received++
			switch {
			case ended.IsZero() || !ended.Before(to) || row.Outcome == "" || row.Outcome == outcomeIncomplete:
				budget.Incomplete++
			case row.Outcome != outcomeCompleted || row.Error != "":
				budget.Failed++
			}
		}
		if !completed {
			continue
		}
		budget.Completed++
		sample, reason := RequestPathSample(row)
		if reason != "" {
			budget.Missing++
			if budget.MissingReasons == nil {
				budget.MissingReasons = make(map[string]int)
			}
			budget.MissingReasons[reason]++
			continue
		}
		group.total = append(group.total, sample.TotalNanos)
		group.post = append(group.post, sample.PostNanos)
		budget.Samples++
		if row.Instance == "" || row.BatchID == 0 {
			budget.UnbatchedSamples++
		} else {
			if group.batches == nil {
				group.batches = make(map[pathBatchKey]int)
			}
			batch := pathBatchKey{row.Instance, row.BatchID}
			group.batches[batch]++
			budget.MaxBatchSamples = max(budget.MaxBatchSamples, group.batches[batch])
		}
	}
	result := make([]PathBudget, 0, len(groups))
	for _, group := range groups {
		budget := &group.budget
		budget.Batches = len(group.batches)
		definition := definitions[budget.DefinitionID]
		switch {
		case definition == nil || definition.UID != budget.WorkloadUID || definition.Identity() != budget.DefinitionID:
			budget.Reason = "definition_missing"
		case math.IsNaN(definition.SLO.Percentile) || math.IsInf(definition.SLO.Percentile, 0) || definition.SLO.Percentile <= 0 || definition.SLO.Percentile > 100:
			budget.Reason = "percentile_invalid"
		case definition.SLO.Percentile == 100:
			budget.JudgmentPercentile = 100
			budget.Reason = "absolute_percentile"
		default:
			p := definition.SLO.Percentile
			budget.JudgmentPercentile = p
			q := 1 - (1-p/100)/2
			rank := pathUpperRank(budget.Samples, q)
			if rank == 0 {
				budget.Reason = "insufficient_samples"
				break
			}
			sort.Slice(group.total, func(i, j int) bool { return group.total[i] < group.total[j] })
			sort.Slice(group.post, func(i, j int) bool { return group.post[i] < group.post[j] })
			budget.TotalMS = float64(group.total[rank-1]) / float64(time.Millisecond)
			budget.PostMS = float64(group.post[rank-1]) / float64(time.Millisecond)
			budget.JudgmentPercentile = 100 * q
			budget.Source = "observed"
		}
		result = append(result, *budget)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].WorkloadUID != result[j].WorkloadUID {
			return result[i].WorkloadUID < result[j].WorkloadUID
		}
		return result[i].DefinitionID < result[j].DefinitionID
	})
	return result
}

// pathUpperRank returns the smallest one-based order statistic k satisfying
// P(Binomial(n,q) <= k-1) >= .95. Zero means no finite observed bound has support.
func pathUpperRank(n int, q float64) int {
	if n <= 0 || !(q > 0 && q < 1) {
		return 0
	}
	binomial := distuv.Binomial{N: float64(n), P: q}
	if binomial.CDF(float64(n-1)) < .95 {
		return 0
	}
	return 1 + sort.Search(n, func(i int) bool { return binomial.CDF(float64(i)) >= .95 })
}

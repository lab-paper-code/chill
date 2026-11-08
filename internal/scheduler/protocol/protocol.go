// Package protocol defines the scalar scheduler input and CHILL-owned evaluation ports.
package protocol

import (
 "context"
 "time"
)

const (
 V1 = "v1"
 V2 = "v2"
 Cover = "cover"
 Polish = "polish"
 Combine = "combine"
 NoImprovement = "noImprovement"
 BudgetCut = "budgetCut"
 Deadline = "deadline"
 EvaluationBudget = "evaluationBudget"
)

type Plan struct { Eligible, Empty bool }
type Workload struct { Rate float64; Queued int; Slack float64 }
type Entry struct { Node int; Option, Mode string; Workload int; Variants []int }

// Problem borrows immutable tick-local scalar buffers from the adapter.
type Problem struct {
 Context context.Context
 Plans [][]Plan
 Workloads []Workload
 Workers, Evaluations, Finalists, Active int
 Priced, PostFilter bool
 Deadline time.Time
 Table *Table
 NewState func([]int) State
}

type Table struct {
 ByPlan [][]int
 Entries []Entry
 NodeOrder []int
 EntryOrder [][]int
 EntryOf, Index []int
 NodeUID, KeyParts []string
 Kappa, KappaE, KappaRet [][]float64
 Count [][]int
 Keep, Searchable, Compared, Nonnegative []bool
 Mid, After, TPre []float64
 Period float64
 Energy func(int, float64, float64) float64
 EnergyLowerBound func(int, float64, float64, float64) float64
 Pruned map[string]int
}

// State owns reusable numerical scratch. Returned ranks borrow it until the next call.
type State interface {
 Choice() []int
 Reset([]int)
 Apply(int, int)
 Delta(int, int) Rank
 MoveTerms(int, int, int) (Workload, float64, float64, float64)
 RouteTimes() []float64
 Overloaded() bool
}

type Point struct {
 Key string
 Joules, KeepJoules, Period float64
 Feasible, Final bool
}

type Step struct {
 Start, Phase string
 K int
 ChangedNodes []int
 Point
 Evaluated int
 Duration time.Duration
 BudgetCut bool
}

type Diagnostics struct {
 Strategy string
 Neighborhood, Evaluated, TrailingEvaluated int
 StartedAt, FinishedAt time.Time
 Duration time.Duration
 BudgetCut bool
 TerminationReason string
 Key string
 Joules float64
 Steps []Step
 Pruned map[string]int
 SurrogateEvaluated, Finalists, ExactEvaluated int
 SurrogateGapJoules float64
 PostFilterReverts int
 Fallback, Start string
 DualBound float64
}

// Exact keeps native result storage in the CHILL adapter. Engines retain R directly.
// Observe uses a stable worker slot; origins and postfilter probes are not observed.
type Exact[R any] interface {
 Evaluate([]int) R
 Bounded([]int) bool
 Admits(R) bool
 Better(R, *R) bool
 Point(R) Point
 Choice(R) []int
 SeedKeep(R)
 Observe(R, int)
 PostFilter(R) R
 FilterKept(R) bool
}

type Engine[R any] interface { Search(Problem, Exact[R]) (R, Diagnostics, error) }

type Candidate struct { Choice []int; Joules float64; Feasible bool; Start string }

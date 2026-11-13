# Candidate Comparison and Bootstrap Capacity Waiting

## Status

Accepted

## Decision

Rank applicable candidates ahead of excluded candidates, then prefer SLO-feasible plans. For infeasible candidates, compare the ordered feedback tuples lexicographically. Each tuple retains failure fraction, success excess and potential excess. Configured Workload priority precedes the within-priority worst-first ordering.

After a feedback tie, compare energy relative to each candidate's keep reference, then prefer keep and finally the logical key. The exact feedback evaluator owns this order; feasibility and final feasibility are the same current rank tier.

At empty start, missing/pending selectable capacity remains Pending with its actual reason. Evaluate terminal built/rejected evidence. Elapsed time cannot authorize a fallback target, reservation or saving action.

Use only the configured ControlInterval, ObservationWindow and ComparisonPeriod policy times. Bootstrap has no separate deadline or timeout state. Preserve scoped operation and frozen owner continuation under [Resource Ledger and Scoped Reservations](0023-resource-ledger.md).

## Consequences

One unserved Workload does not reduce every candidate to a single worst scalar. Evaluation identifies the controller and frozen settings used for its ordering.

Comparison does not preempt another owner's Node or rewrite its frozen execution graph. A pending Workload's only usable Node can remain reserved elsewhere.

See [SLO 미충족 후보 비교](../spec/scheduler/SRS.md#slo-미충족-후보-비교), [빈 출발의 직접 후보 평가](../spec/scheduler/SRS.md#빈-출발의-직접-후보-평가), [SLO 미충족 독립 개선 검증](../spec/scheduler/SRS.md#slo-미충족-독립-개선-검증), and [빈 출발의 직접 평가 검증](../spec/scheduler/SRS.md#빈-출발의-직접-평가-검증).


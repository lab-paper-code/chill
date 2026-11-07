# 0024 Candidate Comparison and Bootstrap Capacity Waiting

## Status

Accepted by the user on 2026-10-26 after the revision-76 startup failure. Option A is implemented; option B remains an explicitly known limitation.

## Context

Removing the global transition barrier in [0023](0023-resource-ledger.md) revealed two independent problems. During the revision-76 expansion test, all three Workloads still lacked an owned Ready instance with a positive route after 282 seconds. See [the incident](../postmortems/0003-rev76-bootstrap-starvation.md).

First, candidate comparison considered only the single worst demand exceedance. If one Workload had no usable capacity, its infinite exceedance tied every plan, even a plan that could serve the others. The energy tie-break then preferred keep: doing nothing cost less. This defect predated the ledger cutover.

Second, the removed barrier had accidentally also delayed decisions until more capacity information arrived. Capacity (κ, measured sustainable serving rate) rows are calculated asynchronously. The first SmolVLM2 FP32 row on genesis2 could reserve the only Node available to the programmable logic controller (PLC) Workload before the PLC alternatives were comparable. That preparation took about 268 seconds; the first Smol row and first MobileNet row completed over two minutes apart (20:07:12 versus 20:09:24 in the source capture).

Waiting globally during normal operation would undo scoped planning. Ignoring one impossible Workload or special-casing infinite values would hide the shared comparison defect rather than define a consistent order for every candidate.

## Decision

Preserve the existing preference for fully feasible plans, then final-feasible plans. For remaining overloaded plans, form each Workload's larger final/transition exceedance, sort those values worst first, and minimize that vector lexicographically (the adopted leximin comparison). Compare the next value when the worst values tie, including infinity. Only a complete vector tie reaches energy, then keep, then the logical key. There is no Workload-specific exception.

At empty start, missing or pending selectable capacity rows remain Pending with their actual reason. Evaluate terminal built/rejected evidence; elapsed time never enables a fallback target, reservation or saving action.

The scheduler policy times are ControlInterval, ObservationWindow and ComparisonPeriod. No bootstrap deadline or timeout state is retained. Ordinary scoped operation and owner/frozen-DAG continuation remain unchanged.

Current code anchors:

- `internal/serving/scheduler_capacity.go:capacityResult.better` — vector ordering.
- `internal/serving/testdata/rev76-bootstrap.json` — retained incident fixture.

Full rules and acceptance criteria are in [scheduler SRS](../spec/scheduler/SRS.md), SCH-53–54 / AC-40–41, and [implementation §5.4](../design/serving/scheduler-implementation.md#54-자원-장부--sch-5052-ac-3739).

## Consequences

Historical revision-77 evidence: all three Workloads became Ready/routed in 166.85 seconds. This predates removal of bounded bootstrap waiting and is not validation of the current policy.

Lexicographic comparison can also change rankings among finite exceedances. Evaluation must therefore identify the controller/settings used; this is not merely an infinity-handling patch.

**Known limit:** during normal scoped operation, a pending Workload’s only usable Node may still be reserved by another Workload. No general Node-preemption prohibition is introduced. Candidate comparison neither reclaims a Node nor rewrites an owner’s frozen execution graph.

Raw evidence: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/checkpoints.md` (revision-76 diagnosis and accepted A/B decision) and `campaign/v2/exp-20261026-ga/` under the same operations root.

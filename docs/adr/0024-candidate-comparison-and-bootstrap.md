# 0024 Candidate Comparison and Bootstrap Capacity Waiting

## Status

Accepted by the user on 2026-10-26 after the revision-76 startup failure.
Option A is implemented; option B remains an explicitly known limitation.

## Context

Removing the global transition barrier in [0023](0023-resource-ledger.md)
revealed two independent problems. During the revision-76 expansion test, all
three Workloads still lacked an owned Ready instance with a positive route after
282 seconds. See [the incident](../postmortems/0003-rev76-bootstrap-starvation.md).

First, candidate comparison considered only the single worst demand exceedance.
If one Workload had no usable capacity, its infinite exceedance tied every plan,
even a plan that could serve the others. The energy tie-break then preferred
keep: doing nothing cost less. This defect predated the ledger cutover.

Second, the removed barrier had accidentally also delayed decisions until more
capacity information arrived. Capacity (κ, measured sustainable serving rate)
rows are calculated asynchronously. The first SmolVLM2 FP32 row on genesis2 could
reserve the only Node available to the programmable logic controller (PLC)
Workload before the PLC alternatives were comparable. That preparation took
about 268 seconds; the first Smol row and first MobileNet row completed over two
minutes apart (20:07:12 versus 20:09:24 in the source capture).

Waiting globally during normal operation would undo scoped planning. Ignoring
one impossible Workload or special-casing infinite values would hide the shared
comparison defect rather than define a consistent order for every candidate.

## Decision

Preserve the existing preference for fully feasible plans, then final-feasible
plans. For remaining overloaded plans, form each Workload's larger final/transition
exceedance, sort those values worst first, and minimize that vector
lexicographically (the adopted leximin comparison). Compare the next value when
the worst values tie, including infinity. Only a complete vector tie reaches
energy, then keep, then the logical key. There is no Workload-specific exception.

At an empty start **only when both physical instances and reservations are
absent**, wait for every actually selectable compatible option/batch capacity row
to become terminal: built or explicitly rejected. Rejected rows need no κ.
Missing and capacity-pending rows continue to wait, up to a configured bound.
Before that bound, issue no target, reservation or new power-saving action.
After it, evaluate current evidence and record pending rows and timeout status.

`bootstrapCapacityWaitNanos` defaults to **10 minutes**, accepts **0..1 hour**,
and uses 0 to disable waiting. This is an operational bound, not a guarantee
that every calculation finishes. Record start, deadline, pending keys and timeout
in the decision, and freeze the setting with the other scheduler settings.
A restart or configuration update starts a new empty-start interval. Once a
physical instance or reservation exists, ordinary scoped operation applies.

Current code anchors:

- `internal/serving/scheduler_capacity.go:capacityResult.better` — vector ordering.
- `internal/serving/scheduler_capacity.go:schedulerBootstrap` — eligibility/wait.
- `internal/serving/scheduler.go:SchedulerSettings.Validate` — setting bounds.
- `internal/serving/testdata/rev76-bootstrap.json` — retained incident fixture.

Full rules and acceptance criteria are in [scheduler SRS](../spec/scheduler/SRS.md),
SCH-53–54 / AC-40–41, and [implementation §5.4](../design/serving/scheduler-implementation.md#54-자원-장부--sch-5052-ac-3739).

## Consequences

An impossible Workload no longer masks useful improvements for the others.
The raw-order regression retains the three-Workload joint placement with five
prepare actions; disabling the wait reproduces the early reservation problem.
Revision 77 subsequently reached all three Ready/routed Workloads in 166.85 seconds;
a separate observer readiness defect, not bootstrap, then failed the next gate.

Lexicographic comparison can also change rankings among finite exceedances.
Evaluation scenario E5 must therefore identify the controller/settings used;
this is not merely an infinity-handling patch.

**Known limit / unimplemented option B:** after the deadline, or during normal
operation, a pending Workload's only usable Node may still be reserved by another
Workload. We did not adopt a general prohibition on preempting such Nodes.
The comparison neither reclaims a Node nor rewrites an existing owner's frozen
execution graph. A longer bootstrap wait is not a proof that this cannot occur.

Raw evidence: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/checkpoints.md`
(revision-76 diagnosis and accepted A/B decision) and
`campaign/v2/exp-20261026-ga/` under the same operations root.

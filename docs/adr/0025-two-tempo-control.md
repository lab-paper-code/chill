# 0025 Two-Tempo Control and a Fixed Energy Comparison Period

## Status

Accepted by the user on 2026-10-26: question choices **1-A / 2-A / 3-A / 4-B**.
Implemented after demonstration three and before evaluation scenario E5.
This changes the policy measured by E5, not just implementation scheduling.

## Context

The [resource ledger decision](0023-resource-ledger.md) separated independent
execution scopes, but did not decide how quickly to expand, shrink or replace
capacity. One decision tempo coupled safety response to energy optimization:
transient demand could trigger reduction, while a long unselected transition
could lengthen the energy comparison period for every candidate.

The question sheet deliberately separated user policy choices from research.
Research supports distinguishing ready supply from supply being prepared and
isolating scopes; it does not establish the correct stabilization seconds or
reserve ratio. Those values require explicit operating choices.

The user was offered these alternatives:

| Question | Alternatives | Accepted choice |
|---|---|---|
| 1. Fast/slow boundary | A: allocation and expansion fast; reduction and energy replacement slow. B: allocation alone fast, every physical change slow. C: retain one tempo. | **A**, to preserve expansion responsiveness. |
| 2. Stabilization | A: one shared period for reduction/replacement. B: separate periods. C: no duration condition. | **A**, default 300 s; demonstration 120 s. |
| 3. Transition reserve | A: no extra reserve. B: fixed inputs/s. C: fraction of Workload demand. | **A** by default; one configurable ratio, 0 disables it, positive values use demand fraction. |
| 4. Energy period H | A: approve a fixed value immediately. B: approve fixed H but first justify its value with observations. C: retain candidate-dependent H. | **B**, followed by a recorded 600 s value. |

Observed actions support 600 seconds: Jetson MAXN transition about **149–151 s**,
SmolVLM2 preparation about **269–292 s**, MobileNet preparation **10–27 s**, and
a downward power transition **196.180 s**. A conservative serial combination of
the longest preparation and downward power transition is about **488 s**.
600 s covers that observed combination; it is not a future transition upper bound.

## Decision

Keep one planner and existing admission, resource, service-level objective (SLO),
readiness and policy-acknowledgment (ACK) checks. Evaluate safe allocation and
expansion every control cycle. Gate physical reduction/stopping, mixed add/remove
plans and energy-only replacement behind the same sustained-demand-drop period.
A policy-only capacity recovery without physical removal may remain fast;
when classifications overlap, slow wins. Ready-subset routing need not wait for
all preparation or the slow gate, and independent reserved scopes remain isolated.

Use the following scheduler settings, validated at the existing input boundary:

- `scaleDownStabilizationNanos`: integer nanoseconds ≥0; default **300 s**,
  demonstration setting **120 s**. Zero removes the additional duration gate.
- `transitionReserveFraction`: finite **[0,1]**, default **0**. Positive f requires
  actual Ready/ACK supply for demand × (1+f) throughout affected transition
  intervals. Unready targets and supply pledged elsewhere do not count twice.
  Zero disables only this extra reserve condition, never existing SLO checks.
- `comparisonPeriodNanos`: positive integer nanoseconds, operational value
  **600 s**, shared by keep and every candidate. Do not add an alias or a
  single-value-only configuration field.

For each Workload UID, time a drop below its pre-drop baseline, including minimum
demand. Small increases still below that baseline do not restart the timer.
Return to baseline cancels it; observation gaps, restart and settings changes
require a new valid stabilization interval. Empty-Node power saving also uses
the common demand gate rather than bypassing it through postprocessing.

Do not stretch H to accommodate a candidate. Exclude a transition whose common
route plus longest post-route chain exceeds H; exactly H remains eligible.
H is neither an action timeout nor evidence that the physical action completed.
Include reserve preparation, serving and removal energy in whole-POOL E5 energy.
Freeze the values in `decision.settings`, search bounds and Run
`binding.schedulerSettings`, along with drop/gate and reserve decisions.

Authoritative detail: [scheduler SRS](../spec/scheduler/SRS.md), SCH-55–58 /
AC-42–44, and [planner §15](../design/serving/analytic-planner.md#15-두-박자-후보-허가와-고정-h--sch-5558).
Current code: `internal/serving/scheduler_tempo.go` (`SchedulerTempo.Observe`,
`Selected`), `scheduler_capacity.go` (candidate comparison/period),
`scheduler_reserve.go` (transition supply), `scheduler_idle_gate.go` (idle paths),
and `scheduler.go:SchedulerSettings.Validate` (settings).

## Consequences

E5 comparisons must use the final controller and identical frozen H/settings on
both sides. Older measurements cannot be merged as though policy were unchanged.
Energy savings may arrive later, and a positive reserve consumes real energy.
Neither a configured reserve nor successful analytical checks alone prove SLO.

The corrected demonstration-setting test admitted reduction **127.96 s** after
the load drop (configured 120 s), with high-load MobileNet success **96.52%** and
low-load **99.98%**; PLC and SmolVLM2 were 100%. This was not an E5 energy result:
two power archives were missing, and diagnostic archive tolerance is forbidden
for E5. The earlier 21.97% high-load run also exposed frozen-target and subset
routing delays; two-tempo policy alone does not solve every transition loss.

Raw evidence: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/two-tempo-questions-20261026.md`,
`intent/checkpoints.md`, `campaign/v2/demo-*/summary.json` and
`campaign/v2/exp-20261027-tempo-rev82/` under that operations root.

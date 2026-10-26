# 0025 Two-Tempo Control and a Fixed Energy Comparison Period

## Status

Accepted by the user on 2026-10-26: question choices **1-A / 2-A / 3-A / 4-B**.
The original policy was implemented after demonstration three. On 2026-10-26
the user corrected stabilization to a rolling maximum-demand window and removed
the empty-Node Fixed energy exception. These corrections are accepted design,
not an implementation or regression-pass claim. They change the policy measured
by E5, not just implementation scheduling.

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
  demonstration setting **120 s**. Zero removes only the history-duration wait,
  not the current-demand-versus-supply or existing safety checks.
- `transitionReserveFraction`: finite **[0,1]**, default **0**. Positive f requires
  actual Ready/ACK supply for demand × (1+f) throughout affected transition
  intervals. Unready targets and supply pledged elsewhere do not count twice.
  Zero disables only this extra reserve condition, never existing SLO checks.
- `comparisonPeriodNanos`: positive integer nanoseconds, operational value
  **600 s**, shared by keep and every candidate. Do not add an alias or a
  single-value-only configuration field.

Use the Kubernetes HPA scale-down stabilization-window principle: for each
Workload UID, use the **maximum effective demand** (including queued/minimum
demand) observed in the last `scaleDownStabilizationNanos` as the demand for
reduction admission. Expire samples at the left boundary. Require a complete
valid window and maximum demand strictly below current actual Ready/ACK supply;
the candidate must also serve that maximum. Selection does not rebase history.
Gaps, clock reversal, restart, settings or UID changes require a fresh valid
window. Zero uses the current valid observation only.

Empty-Node new Fixed saving uses the same per-UID maxima, not a separate timer:
all controlled UIDs must have complete windows and maximum demand ≤ supply,
with at least one strict inequality. Do not offset one UID's rise with another's
fall; without controlled UIDs there is no drop evidence. Preserve already
authorized running actions and fast expansion/ready-subset allocation.

Remove the Fixed exception, rather than introduce another energy calculator.
Compare new empty-Node saving against the unchanged keep using the existing
common-H payback/minimumGain path. Nominal gain within H must be positive and
pass both existing gates. Do not preapply saving to all candidates and keep,
cancel its costs as common, or dispatch new saving from targetless Keep.Actions
without admission. With H=600 s, jetsono MAXN→15W has **−699 J** gain and an
approximately **52-minute** break-even: do not execute or extend H to justify it.

Replace `SchedulerDownState` baseline/previous/downSince/elapsed and
`SchedulerTempo.globalSince` with timestamped per-UID demand history.
`decision.capacity.workloads[].scaleDown` records rate, windowStart/windowEnd,
observedSince, windowComplete, maxRate, currentCapacity, allowed, reason and
optional resetReason. Remove old fields/readers rather than dual-write aliases;
old stored records remain historical evidence, not proof under the new rule.

Do not stretch H to accommodate a candidate. Exclude a transition whose common
route plus longest post-route chain exceeds H; exactly H remains eligible.
H is neither an action timeout nor evidence that the physical action completed.
Include reserve preparation, serving and removal energy in whole-POOL E5 energy.
Freeze the values in `decision.settings`, search bounds and Run
`binding.schedulerSettings`, along with drop/gate and reserve decisions.

Authoritative detail: [scheduler SRS](../spec/scheduler/SRS.md), SCH-55–59 /
AC-42–44, and [planner §15](../design/serving/analytic-planner.md#15-두-박자-후보-허가와-고정-h--sch-5558).
Current code: `internal/serving/scheduler_tempo.go` (`SchedulerTempo.Observe`,
`Selected`), `scheduler_capacity.go` (candidate comparison/period),
`scheduler_reserve.go` (transition supply), `scheduler_idle_gate.go` (idle paths),
and `scheduler.go:SchedulerSettings.Validate` (settings).

## Rationale and rejected alternatives

Demonstration four's B stage held **100 req/s**, yet the baseline timer started
at **09:00:49** and admitted reduction only **39 s** after the actual drop.
Staying below a noisy local high is not a complete stabilization window.
The maximum-demand rule protects the recent high demand directly and is shared
by workload reduction and empty-Node saving. Fixed saving is not free: its
transition energy can outweigh the lower steady-state baseline within H.

Rejected: retaining/restarting a pre-drop baseline timer, a separate global
empty-Node timer, independent stabilization settings, summing UID demand to hide
an increase, an unconditional Fixed exception, a separate saving calculator,
and extending H to the 52-minute break-even. Reuse the existing setting and
energy path instead.

Required regressions: (1) constant 100 req/s plus observation noise produces
no reduction/new saving when current supply is at or below the window maximum;
(2) after a real drop, expiry of the old high samples at W permits otherwise
eligible reduction/saving; (3) the −699 J Fixed saving is not executed, nor is
positive gain below minimumGain or failing payback; (4) empty start, expansion,
ready-subset allocation and independent-scope progress remain unchanged.
Include gaps/restart/settings/UID changes, zero W, exact expiry and new decision
record shape. These are acceptance criteria, not reported test results.

Evidence: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/demo-20261027-4/report.md`
§C and the `DemoPowerDown27` / `TempoTestCause27` investigations. Historical
baseline-timer results below do not validate the corrected policy.

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

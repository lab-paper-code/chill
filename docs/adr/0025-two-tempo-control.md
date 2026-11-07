# 0025 Two-Tempo Control and a Fixed Energy Comparison Period

## Status

Accepted by the user on 2026-10-26: question choices **1-A / 2-A / 3-A / 4-B**. The original policy was implemented after demonstration three. On 2026-10-26 the user corrected stabilization to a rolling maximum-demand window and removed the empty-Node Fixed energy exception. These corrections are accepted design, not an implementation or regression-pass claim. They change the policy measured by E5, not just implementation scheduling.

## Context

The [resource ledger decision](0023-resource-ledger.md) separated independent execution scopes, but did not decide how quickly to expand, shrink or replace capacity. One decision tempo coupled safety response to energy optimization: transient demand could trigger reduction, while a long unselected transition could lengthen the energy comparison period for every candidate.

The question sheet deliberately separated user policy choices from research. Research supports distinguishing ready supply from supply being prepared and isolating scopes; it does not establish the correct stabilization seconds or reserve ratio. Those values require explicit operating choices.

The user was offered these alternatives:

| Question | Alternatives | Accepted choice |
|---|---|---|
| 1. Fast/slow boundary | A: allocation and expansion fast; reduction and energy replacement slow. B: allocation alone fast, every physical change slow. C: retain one tempo. | **A**, to preserve expansion responsiveness. |
| 2. Stabilization | A: one shared period for reduction/replacement. B: separate periods. C: no duration condition. | **A**, default 300 s; demonstration 120 s. |
| 3. Transition reserve | A: no extra reserve. B: fixed inputs/s. C: fraction of Workload demand. | **A** by default; one configurable ratio, 0 disables it, positive values use demand fraction. |
| 4. Energy period H | A: approve a fixed value immediately. B: approve fixed H but first justify its value with observations. C: retain candidate-dependent H. | **B**, followed by a recorded 600 s value. |

Observed actions support 600 seconds: Jetson MAXN transition about **149–151 s**, SmolVLM2 preparation about **269–292 s**, MobileNet preparation **10–27 s**, and a downward power transition **196.180 s**. A conservative serial combination of the longest preparation and downward power transition is about **488 s**. 600 s covers that observed combination; it is not a future transition upper bound.

## Decision

Keep one planner and existing admission, resource, SLO, readiness and ACK checks. Compare allocation, expansion, reduction, replacement and empty-Node saving each control cycle using current effective demand. v1/v2 use the same candidates and physical rules.

Use the following scheduler settings, validated at the existing input boundary:

- Policy times: ControlInterval, ObservationWindow and ComparisonPeriod only. No stabilization, replacement dwell, observation-age TTL or bounded bootstrap waiting.
- `transitionReserveFraction`: finite **[0,1]**, default **0**. Positive f requires actual Ready/ACK supply for demand × (1+f) throughout affected transition intervals. Unready targets and supply pledged elsewhere do not count twice. Zero disables only this extra reserve condition, never existing SLO checks.
- `scaleDownHeadroomFraction`: finite **[0,1]**, default **0**. Final energy-downscale routes and idle cleanup must carry `current effective demand × (1+h)` on positive integer weights, each within executor κ. Zero-weight executors are not headroom. Never inflate bridge/reserve demand or actual request joules. Offline `.25` is not a deployment safety recommendation.
- `comparisonPeriodNanos`: positive integer nanoseconds, operational value **600 s**, shared by keep and every candidate. Do not add an alias or a single-value-only configuration field.

Effective demand is max(long-window rate, recent rate) + outstanding / actual observation length, with the minimumDemand floor. reductionDemand is this same current value; no rolling maximum, trend gate or elapsed-time qualification applies.

Empty-Node saving uses the same current-demand, capacity/SLO and energy admission rules. Preserve authorized running actions, owner reservations, frozen targets and Ready-subset allocation.

Preserve actual shortage recovery, backlog, partial observation based on available demand, unknown/future timestamp checks, live-capture failure handling, ownership, exact Ready/native ACK, old+new peak, bridge, reserve, H, leximin, payback and minimumGain.


Remove the Fixed exception, rather than introduce another energy calculator. Compare new empty-Node saving against the unchanged keep using the existing common-H payback/minimumGain path. Nominal gain within H must be positive and pass both existing gates. Do not preapply saving to all candidates and keep, cancel its costs as common, or dispatch new saving from targetless Keep.Actions without admission. With H=600 s, jetsono MAXN→15W has **−699 J** gain and an approximately **52-minute** break-even: do not execute or extend H to justify it.

Delete timing-history state and obsolete decision projections without aliases or compatibility readers. Existing stored raw records remain untouched historical evidence.

Do not stretch H to accommodate a candidate. Account for complete operation joules and nonnegative waiting/serving time within H. Long transitions retain diagnostics without excluding SLO expansion. H is neither an action timeout nor completion evidence. Freeze settings in decisions and Run bindings.

Authoritative detail: [scheduler SRS](../spec/scheduler/SRS.md), SCH-55–59 / AC-42–44, and [planner §15](../design/serving/analytic-planner.md).

## Rationale and rejected alternatives

Historical rationale: demonstration four admitted reduction 39 seconds after an actual drop despite a configured window. The current user-approved policy removes timing admission and compares current demand directly. Fixed saving still requires positive common-H gain, minimumGain and payback.

Retain one energy path and fixed H. Do not add substitute timers, history state, TTLs or compatibility knobs.

Required regressions: low current demand permits otherwise feasible reduction; high current demand protects capacity/SLO in v1/v2; negative gain or failing minimumGain/payback rejects saving; owner cleanup, Ready-subset allocation and actual missing-evidence handling survive. These are criteria, not reported test results.

Evidence: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/demo-20261027-4/report.md` §C and the `DemoPowerDown27` / `TempoTestCause27` investigations. Historical baseline-timer results below do not validate the corrected policy.

## Consequences

Evaluation comparisons must identify controller and frozen H/settings. Older measurements reflect older policies. Analytical feasibility and configured reserve alone do not prove SLO.

The corrected demonstration-setting test admitted reduction **127.96 s** after the load drop (configured 120 s), with high-load MobileNet success **96.52%** and low-load **99.98%**; PLC and SmolVLM2 were 100%. This was not an E5 energy result: two power archives were missing, and diagnostic archive tolerance is forbidden for E5. The earlier 21.97% high-load run also exposed frozen-target and subset routing delays; two-tempo policy alone does not solve every transition loss.

Raw evidence: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/two-tempo-questions-20261026.md`, `intent/checkpoints.md`, `campaign/v2/demo-*/summary.json` and `campaign/v2/exp-20261027-tempo-rev82/` under that operations root.

## 알려진 한계

정지형 전환은 실제 backlog·bridge·reserve·Ready/ACK·SLO를 충족해야 한다. 이력 최대 수요나 경과 시간으로 후보를 금지하지 않는다.

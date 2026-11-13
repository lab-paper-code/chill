# Current-Demand Control and a Fixed Energy Comparison Period

## Status

Accepted

## Decision

Use one planner to compare allocation, expansion, reduction, replacement and empty-Node saving each control cycle. Preserve admission, resource bounds, SLO, exact readiness/native acknowledgment, reservations, frozen targets and Ready-subset allocation.

The policy times are `ControlInterval`, `ObservationWindow` and `ComparisonPeriod`. There is no stabilization history, replacement dwell, observation-age TTL or bounded bootstrap timer.

Effective demand is the larger long-window/recent rate plus unexpired pending, assigned, queued and deferred requests divided by actual observed window length, with the configured minimum-demand floor. Observed running and returning work is excluded from this backlog term.

After restart, require one control interval of observation, bounded by the configured observation window. Until the complete observation window is available, plans only expand and preserve existing routes.

Validate the existing scheduler settings:

- `transitionReserveFraction`: finite in `[0,1]`, default 0. Positive values require actual Ready/ACK supply for demand multiplied by one plus this fraction through affected transition intervals. Never double-count unready or pledged supply.
- `scaleDownHeadroomFraction`: finite in `[0,1]`, default 0. Final energy-downscale routes and idle cleanup require effective demand multiplied by one plus this fraction, carried on positive integer weights within executor capacity. Do not inflate bridge/reserve demand or actual request energy.
- `comparisonPeriodNanos`: positive integer nanoseconds shared by keep and every candidate. Freeze the selected value in decisions and Run bindings.

Use the same current-demand and SLO rules for reduction and empty-Node saving. No rolling maximum, trend gate or elapsed stabilization period excludes an otherwise eligible candidate.

Compare new empty-Node saving with unchanged keep using common-period payback and minimum gain. Gain must be positive and pass both gates. Do not preapply saving to every candidate, cancel its cost as a common term or dispatch targetless saving without admission.

The common comparison period does not grow to accommodate a candidate. Include complete operation energy and nonnegative waiting/serving time. Keep long-transition diagnostics; the energy period is separate from action deadlines and completion evidence.

## Consequences

Current demand, complete physical observations and common-period energy determine selection. Configured reserve and analytical feasibility still require measured SLO validation. Existing scientific raw records remain immutable.

See [현재 수요의 공통 후보 비교](../spec/scheduler/SRS.md#현재-수요의-공통-후보-비교), [현재 유효 수요와 정책 시간](../spec/scheduler/SRS.md#현재-유효-수요와-정책-시간), [전환 예비 용량 비율](../spec/scheduler/SRS.md#전환-예비-용량-비율), [후보와 독립적인 에너지 비교 기간](../spec/scheduler/SRS.md#후보와-독립적인-에너지-비교-기간), and [빈 노드 절전의 에너지 허가](../spec/scheduler/SRS.md#빈-노드-절전의-에너지-허가).

Acceptance covers [현재 수요의 공통 비교 검증](../spec/scheduler/SRS.md#현재-수요의-공통-비교-검증), [전환 예비와 에너지 검증](../spec/scheduler/SRS.md#전환-예비와-에너지-검증), and [고정 비교 기간과 절전 허가 검증](../spec/scheduler/SRS.md#고정-비교-기간과-절전-허가-검증).


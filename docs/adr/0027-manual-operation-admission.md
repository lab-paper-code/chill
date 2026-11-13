# Live Manual Admission and Confirmed Reservation Release

## Status

Accepted

## Decision

Admit manual changes from current live observer, runtime and Node inputs under Tick serialization. Capture the full affected scope, reserve it atomically and recheck safety at physical execution boundaries. Do not publish a synthetic scheduler decision.

Reset may reject invalid structure or scope before discovery. Its actual cleanup plan and Node admission use fresh state.

Execution termination and reservation release are separate. Releasing a failed/partial routing owner's scope while Pods remain requires a fresh observation proving:

- Exact reserved Pod UID, container/physical identity and Node UID.
- Pod/native readiness and successful observation after operation end.
- Acknowledged requested policy or an unchanged recorded pre-operation native policy.

An unchanged previous policy proves actual state without inventing an acknowledgment of the failed request. Mismatch, unready state, stale observation and executor replacement retain reservations. Confirmed Pod absence with no pending power transition remains another release path.

Reset may atomically inherit a terminated owner's reservation only when the owner's entire responsibility fits the admitted Workloads and exact `operation.Nodes`. Reject active/measurement owners and out-of-scope responsibility. Expanded historical conflict state grants no extra cleanup permission.

## Consequences

Each completed manual effect is visible at the next admission. Uncertainty blocks its affected scope while independent planning can continue under [Resource Ledger and Scoped Reservations](0023-resource-ledger.md).

Route/unroute captures `Before.Native`. Release records routing and instances without that native baseline, so unchanged-policy fallback cannot prove its return while the Pod remains. Exact requested-policy proof and explicit reset/physical cleanup remain separate paths.

See [작업 범위 예약과 효과 반환](../spec/scheduler/SRS.md#작업-범위-예약과-효과-반환), [예약 충돌과 효과 반환 검증](../spec/scheduler/SRS.md#예약-충돌과-효과-반환-검증), and the [scheduler operations design](../design/serving/scheduler-operations.md).


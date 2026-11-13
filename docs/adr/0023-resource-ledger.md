# Resource Ledger and Scoped Reservations

## Status

Accepted

## Context

Observation quality and physical existence have separate meanings. A pending policy acknowledgment or failed probe cannot make an occupied executor available for a duplicate preparation.

## Decision

Use the observer's Pod UID-keyed source map as the physical ledger. Runtime and scheduler snapshots project that inventory. Observation failure preserves identity, allocation, ownership and last confirmed state; freshness and identity separately govern dispatch eligibility.

Create sources from Pod lifecycle observations. Late probe/log publication rechecks exact physical identity and cannot resurrect a retired source. Retire entries only after confirmed executor termination and required Pod cleanup.

Reserve affected Nodes, Workload UIDs, Pod UIDs and physical executors through existing action runs. Admission, conflict detection and release use the same complete scope. Uncertain remote effects retain ownership beyond goroutine termination.

Keep one global planner. Freeze reserved scopes and evaluate independent ones. An owner's Ready-subset routing remains within its frozen responsibility. Exact observation proofs or bounded cleanup release ownership under [Live Manual Admission and Confirmed Reservation Release](0027-manual-operation-admission.md).

## Consequences

Unknown physical state remains occupied. Independent expansion can proceed outside reservations, while shared physical safety failures such as exclusive CPU overlap still block affected actions. Reservations conservatively include whole Nodes.

The scheduler specifies [Pod별 실물 자원 장부](../spec/scheduler/SRS.md#pod별-실물-자원-장부), [작업 범위 예약과 효과 반환](../spec/scheduler/SRS.md#작업-범위-예약과-효과-반환), and [예약 범위 밖 판단 지속](../spec/scheduler/SRS.md#예약-범위-밖-판단-지속). Acceptance covers [관측 실패 중 실물 장부 검증](../spec/scheduler/SRS.md#관측-실패-중-실물-장부-검증), [예약 충돌과 효과 반환 검증](../spec/scheduler/SRS.md#예약-충돌과-효과-반환-검증), and [예약 밖 확장과 배분 검증](../spec/scheduler/SRS.md#예약-밖-확장과-배분-검증).


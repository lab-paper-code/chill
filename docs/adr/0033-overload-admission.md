# 0033 Workload-Owned Overload Admission

## Status

Accepted — 2026-11-11. 구현과 측정의 완료 여부는 별도 검증한다.

## Decision

서비스의 과부하 정책과 gateway 큐 상한은 `Workload.spec.admission`이 소유한다. `policy`는 `Reject`(기본) 또는 `Delay`, `maxQueuedRequests`는 양의 정수(기본 1024)다. 두 값은 applied Definition과 평가 Run 입력에 동결한다. 변경은 Workload generation을 전진시키며 이미 수락한 요청의 Definition을 바꾸거나 executor 재시작을 요구하지 않는다. ModelSpec firstfloor `deadlinePolicy`와 frozen profiling RunConfig는 측정 provenance로 유지하고 serving 정책 선택에는 쓰지 않는다.

## Contracts

- SLO 기한은 `receivedAt + slo.targetLatency`, 최종 기한은 수신 때 확정한 `Receipt.Deadline`이다. 적용 Workload lifetime, header override, server fallback과 HTTP 상한을 반영한 유효 수명을 평가 입력에 보존한다.
- gateway는 공유 완료 가능성 예측으로 SLO 안에 끝낼 후보를 고른다. executor는 batch 실행 직전에 최신 monotonic 시각으로 재확인한다. 예측은 판단 규칙이며 E2E 분위수 보장이 아니다. 판단 입력이 없으면 `AdmissionUnknown`으로 최종 기한까지 기다리고 무검사 배정하지 않는다. fixed-batch는 기존 SLO 판단 예외를 유지한다.
- `Reject`는 완료 불가능한 요청을 실행 없이 거절한다. `Delay`는 정상 요청 뒤로 보내 늦은 완료를 허용하며, 정상 유입으로 굶을 수 있다. aging·quota는 추가하지 않는다. 최종 기한과 취소는 양쪽 큐에 적용하고 실행 중 batch는 선점하지 않는다. 물리 완료까지 점유를 추적한다.
- `maxQueuedRequests`는 gateway 정상·지연 큐 합의 상한이다. native instance `maxQueueSize`와 HTTP semaphore는 별도 물리 상한이다. 전역 `--queue-limit`은 Workload 상한으로 대체한다.
- 거절은 `QueueFull`(HTTP 429), `DeadlineInfeasible`(HTTP 503), `stage=gateway|executor`, 적용 policy와 판단 metadata를 기록한다. executor의 미실행 증명을 유지하고 자동 재시도는 추가하지 않는다.
- runtime과 replay는 같은 예측·요청 정책을 쓴다. 평가는 offered 전체를 분모로 `onTime`, `late`, `rejected`, `timeout`, `failed`, `incomplete`를 분리한다. 기한 내 성공률과 goodput으로 개선을 판단하며 거절을 성공이나 완료 latency 표본에 넣지 않는다.

## Consequences

프로파일의 service 분포는 정책과 독립적으로 재사용한다. Definition·요청·native protocol·scheduler snapshot/capacity·관측·client·평가 입력과 결과는 [구현 계획 §3](../design/serving/overload-admission-plan.md#3-저장-계약-adr-0019)에 따라 한 번에 cutover한다. [ADR 0019](0019-durable-stored-contract-compatibility.md)와 [ADR 0031](0031-configuration-ownership-and-freezing.md)을 따르며 과거 필드 부재를 `Reject`·1024로 소급 해석하지 않는다. 이전 reader·alias·shim은 두지 않는다. 캠페인 사이에 배포하고 영향받는 작업용 객체를 정리한다. 게시된 과학 원문은 변경하지 않는다.

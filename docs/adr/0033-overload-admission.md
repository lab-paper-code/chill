# Workload-Owned Overload Admission

## Status

Accepted

## Decision

서비스의 과부하 정책과 gateway 큐 상한은 `Workload.spec.admission`이 소유한다. `policy`는 기본 `Reject` 또는 `Delay`, `maxQueuedRequests`는 기본 1024인 양의 정수다.

두 값은 applied Definition과 평가 Run 입력에 동결한다. 변경은 Workload generation을 전진시키며 이미 수락한 요청의 Definition을 바꾸거나 executor를 재시작하지 않는다. ModelSpec firstfloor `deadlinePolicy`와 frozen profiling RunConfig는 측정 provenance로 유지한다.

## Contracts

- SLO 기한은 `receivedAt + slo.targetLatency`, 최종 기한은 수신 때 정한 `Receipt.Deadline`이다. Workload lifetime, header override, server fallback과 HTTP 상한을 반영한 유효 수명을 평가 입력에 보존한다.
- Gateway는 route의 ready executor 가운데 응답을 받지 못한 dispatch 요청 수가 한도 미만인 것만 남기고 plan의 share로 고른다. 한도는 `min(max(floor(R x T), 최소 실행 가능 batch), executor 큐 크기)`다. T는 Workload의 latency target, R은 실행 가능한 batch들의 batch size / 평균 service 시간 최댓값이다. batch는 허용 최대 batch 이하이고 고정 batch 정책이면 그 고정 batch와 같을 때 실행 가능하다. 고정 batch 인스턴스의 한도는 그 batch만으로 계산하며 어떤 인스턴스도 가장 작은 실행 가능 batch보다 낮게 제한되지 않아 batch 하나는 항상 담는다. 한도는 target 시간 동안 처리하는 요청 수인 운영점이며 초과 요청이 target을 놓친다는 증명이 아니다. 한도 함수는 runtime과 replay가 공유한다. 요청이 기한을 지킬 수 있는지는 executor만 배치 실행 직전 monotonic 시각으로 판단한다. 취소한 요청은 executor 상태가 떠났음을 확인할 때까지 처리 중으로 세고, replay는 executor가 요청을 놓는 지점(service 종료, executor 거절, 큐 안 만료)에서 반환 시간 0으로 푼다.
- 한도에 닿은 executor만 남으면 `Reject`는 요청을 `QueueFull`(HTTP 429, stage=gateway)로 즉시 거절하고 `Delay`는 gateway 큐에서 기다린다. 큐는 Workload별 도착순이다. 기다리는 요청은 같은 Definition의 뒤 요청만 붙잡고 다른 Definition의 뒤 요청은 따로 판단하며, aging이나 quota를 두지 않는다.
- 최종 기한과 취소는 gateway 큐와 executor 큐에 적용한다. 실행 중 배치는 선점하지 않고 물리 완료까지 점유를 추적한다.
- `maxQueuedRequests`는 gateway 큐의 상한이다. Native instance `maxQueueSize`와 HTTP semaphore는 별도 물리 상한이다.
- gateway 거절은 `QueueFull`(HTTP 429, stage=gateway)이며 Workload ingress 큐가 상한이거나 ready한 모든 route executor가 한도에 있을 때다. `DeadlineInfeasible`(HTTP 503, stage=executor)은 executor만 낸다. executor 큐 포화는 `QueueFull`(stage=executor)이다. 기록은 적용 policy와 판단 metadata를 가진다. 결정 시점의 executor별 `inFlight`와 한도는 `admissionDepths`에 기록한다. gateway 큐 대기는 `admissionWaits`에 남기며 `state`는 `QueueFull`(ready executor가 한도) 또는 `NoCapacity`(ready한 route executor 없음), 각 항목의 `reason`은 그 executor 자신의 `QueueFull`(+`depth`), `NoCapacity`, `AdmissionStopped`다. Executor의 미실행 증명을 보존하고 자동 재시도하지 않는다.
- Runtime과 replay는 같은 한도 함수와 요청 정책을 쓴다. 평가는 offered 전체를 분모로 `onTime`, `late`, `rejected`, `timeout`, `failed`, `incomplete`를 구분한다. 거절은 성공이나 완료 latency 표본에 포함하지 않으며 기한 내 성공률과 goodput으로 비교한다.

## Consequences

프로파일의 service 분포는 정책과 독립적으로 재사용한다. Definition, 요청, native protocol, scheduler 입력, 관측과 평가 결과에 같은 적용 정책을 전달한다.

[Durable Stored-Contract Compatibility](0019-durable-stored-contract-compatibility.md)와 [Configuration Ownership and Freezing Paths](0031-configuration-ownership-and-freezing.md)를 따른다. 과거 필드 부재를 현재 `Reject`나 큐 상한 1024로 소급 해석하지 않는다. 의존 캠페인 사이에 작업용 객체를 정리하며 게시된 과학 원문은 변경하지 않는다.


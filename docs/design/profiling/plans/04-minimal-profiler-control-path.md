# Plan 4: Minimal Profiler Control Path

## 결과 요약 — completed
- 결정: Operator는 `ChillSystem`의 Profiler Deployment/availability만, Helm은 ServiceAccount/RBAC/base configuration을, standalone Profiler는 immutable namespaced `ProfilingRun` lifecycle을 소유한다. 두 controller identity/권한과 root finalizer 책임을 섞지 않는다.
- Profiler는 CHILL release namespace(기록된 배포 `chill-system`)의 Run/Job을 관리한다. raw polling/inference는 one-shot Job에서만 수행하고 reconcile 메모리/로그를 authoritative state나 handoff로 쓰지 않는다.
- 한 Run/Job/Pod attempt와 exact image/catalog/Node/source/policy snapshot을 유지한다. recorded Job loss는 `ExecutionLost`; replacement UID/owner drift는 거부하며 같은 Run에서 재실행하지 않는다. 재시도는 새 Run이다.
- worker raw commit → controller terminal-fact finalization → 독립 retrieve/verify → compact evidence reference 순서다. deterministic immutable key에 이미 유효한 record가 있으면 재사용한다. raw samples/credentials/endpoint는 status에 복제하지 않는다.
- API I/O와 fact-source revision 경합은 retryable; 읽힌 catalog의 malformed/absent entry 또는 execution-policy 불일치는 terminal pre-execution error다. `observedGeneration`이 처리한 immutable generation을 식별한다.
- Run 삭제는 native owner GC로 Job을 취소하지만 committed evidence는 지우지 않는다. root finalizer는 Run/Job/evidence cleanup을 맡지 않고 evidence 자동 GC/expiry는 없다.
- 완료는 verified execution evidence 저장이며 scientific admission은 removed @`788bcf93`; 과거 `Accepted/Rejected/Insufficient`, `wall-power-v2`와 admission snapshots는 현행 계약이 아니다. 계산은 [Plan 3](03-profile-admission-derivation.md).
- 결과: CPU ORT active/loaded-idle live path, duplicate reconcile/restart/recorded-Job loss/identity drift, Operator retained-Deployment restart, deletion fence가 검증 기록에 포함된다. 실제 owner GC 기록은 Kubernetes 1.31 kind e2e이며 envtest GC 증거로 대신하지 않는다.
- 근거 커밋: `9edde945` verified Job cleanup/failed diagnostics, `788bcf93` scientific admission 제거. 현행 [worker](../components/measurement-worker.md)·[store](../components/evidence-store.md) 계약이 serialization/retention의 권위다.
- 내부 조립: Run·campaign·transition-cost reconciler와 Console preflight는 단일 구현 포트 대신 함수 필드에 메서드/closure를 주입한다. 기존 테스트 대역도 같은 함수 경계로 연결하며 Evidence Store binding과 actuation policy는 `evidencestore/kube.Binding`·`nodeactuator` 원본 타입을 사용한다. 저장 형식·증거 검증·읽기 전용 경계는 바꾸지 않는다.
- 남은 것: 새 profile/SLO/scheduler 권한은 이 계획이 부여하지 않는다. [Plan 7](07-batch-energy-optimization-control-path.md)이 반복 Run 조합을 소유한다.
- [Profiling index](../README.md)

# 하드코딩 정리 — 결과와 열린 항목

## 결과 요약

- 상태: completed(슬라이스2~5 코드). A(측정·판정)/B(환경)만 기존 설정 소유자로 옮기고 C(프로토콜·안전·방법론)·D(이미 설정)는 유지했다.
- 근거 `58348057`…`30e3a6d6`; 결정 [ADR0031](../adr/0031-configuration-ownership-and-freezing.md), 현행 소비자·동결 계약 [설계](../design/serving/configuration-hardcoding.md). 표 집계는 [인벤토리 결과](2026-10-30-configuration-hardcoding-inventory.md).
- serving capacity settings3개·BEO 단일 confidenceLevel·reuse annotation·기존 cluster/server-start3필드·하네스 binding으로 이전했다. profileConfidenceLevel CLI/values/생성자 경로는 제거했다.
- serving 변경 기록·불변 Run·완결 binding은 각 소유 경로에서 동결한다. Decision.Settings가 실제 사용값의 권위이며 PUT 오류만으로 미적용이라고 단정하지 않는다. 초기/외부 설정은 실제 Decision으로 해석한다.
- 과거 새 필드 부재는 미기록 사실이다. archive/publication #213~#225·ADR0030 v1alpha5와 과학 원시는 보존하며 소급 기본값·dual reader·migration·alias·shim을 만들지 않는다. 새 CapacityInput은 기본값도 새 κ 키로 계산한다.
- rev112의 stored duration tolerance0 vs CR기본 .5 불일치로 profiles0이 된 문제는 `75127da9`에서 spec nil인 옛0을 해석하도록 수정했다. 명시 spec 불일치는 거부한다([ADR0019](../adr/0019-durable-stored-contract-compatibility.md)).
- 기본값은 기존 계산/fixture와 비교하며 생략=명시만으로 입증하지 않는다. 비영분산 active2/idle2의 유도→저장→재로딩→serving과 typed round-trip은 기존 구현 수용 범위다. live 동일성은 별도다.

## 열린 점

원분류 ?10행은 동작 유지다. 근거 없이 A/B/C로 바꾸지 않고 이동·기록48행과 구분한다. 위치는 조사 당시 줄이며 심볼로 찾는다.

| 원본 | 남은 결정 |
|---|---|
| internal/serving/overload.go:12-13 | heap .9/1s가 OOM 안전 경계인지 조정할 admission 정책인지 |
| api/v1alpha1/dispatchoccupancymeasurement_types.go:161-162 | cap64/64MiB 단일값 mirror를 native 계약 상수로 둘지 범위 입력으로 바꿀지 |
| internal/serving/kube/observer.go:466,1183; cmd/node-agent/controller_setup.go:37; transitioncost/controller/reconciler.go:16 | ready/drain/actuation poll이 실제 측정 구간 안에 포함되는지 |
| campaign/controller/reconciler.go:25,33; idlebaseline/controller/reconciler.go:27; profiler/controller/run_reconciler.go:27 | admission/backstop이 측정 전 운영 envelope인지 |
| internal/deviceclass/inference.go:17,75-76 | 미확인 장치1Gi fallback의 사실 근거·환경 소유자 |
| hack/e5/e5.py:1073,1103,1325-1327 | disk310GB gate를 실측 저장 예산에서 유도할지 환경 예산으로 받을지 |
| load-generator/pyproject.toml:2,7,10; __init__.py | package/Python 지원 pin은 측정 knob가 아님; 일반 구성관리 유지 |
| charts/chill/templates/operator.yaml:63-74 | probe timing/port와 기동 flag 일치·운영 envelope 소유권 |
| charts/chill/templates/console.yaml:198-205; dashboard.yaml:66-74 | UI readiness/liveness 운영 값의 소유권 |
| charts/chill/templates/serving_entrypoint.yaml:30,47-49,228; console.yaml:216; dashboard.yaml:92,95 | single-writer 배치와 임시 volume 크기의 안전/환경 경계 |

## 삭제 후보: 승인 아님

현재 E5 실행 경로 밖이지만 외부 runbook·독립 호출·보존 필요를 확인하기 전 삭제하지 않는다(18파일). 비활성 숫자 탐색 CLI 한 파일은 배치3 승인으로 삭제했다.

- `hack/measure/`: gen.py, capture-progress.py, feeder.py, plan-e5a.py, soak_ledger.py, xcheck.py.
- `hack/measure/phase-a/measurement-generalization-20261024/`: meter-cadence/capture.py, meter-cadence/analyze.py, cost-shift-approved/recalculate.py, post-d2-25-20261025/prepare.py, cost-budget/arithmetic_check.py.
- `hack/measure/`: d9c/split.py, d9c/timeline.py, smolcheck/b1stats.py, smolcheck/collect.py, smoke/smoke.py, smoke/send.py. `hack/e5/`: timeline.py.
- D2 bind_before·conduct_before·analyze_before·sample_cgroups의 독립 main은 E5 helper와 별도 검토한다(18개에 미포함). 파일 전체 삭제 금지, demo.py offline import 회귀·재사용 가능한 example config는 유지한다.

source/profile interval은 사용자 결정대로 기동 설정+실제값 기록으로 유지한다. binding의 물리 불변성 저장 장치는 추가하지 않고 완결 후 수정 금지·새 실험 새 binding을 적용한다. 운영 values는 rev74 캡처이며 현 배포값이 아니다.

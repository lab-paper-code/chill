# 하드코딩 정리 계획

2026-10-30 · 최초 문서 단계 이후 구현 상태를 아래에 추적한다.
상태: **2차 검토(fusion: 패널 3모델+판정) 반영**, 슬라이스 2~5 구현 완료. 배포·live 측정 수용 완료를 뜻하지 않는다.

## 목표와 결정

코드에 박힌 값을 분류해 **A(측정·판정), B(환경)만 기존 설정 소유자에게 옮긴다**.
C(프로토콜·안전·고정 방법론)는 상수로, 이름 없는 C는 의미 있는 이름으로, D(이미 설정)는 그대로 둔다.
모든 숫자를 API로 만들지 않는다. 기본값은 현재값이고 유효한 다른 값도 받을 수 있어야 한다.

| 값의 집 | 결정과 이유 |
|---|---|
| 운영 중 조정할 serving A | 기존 SchedulerSettings에 capacityGridSteps·capacityBisectionWidth·capacityTailSamples **3개 추가**. 기존 필드 유지 |
| 측정 신뢰수준 | BEO 측정별 단일 confidenceLevel 정책. 기존 독립 idle CR 소유권 유지. profileConfidenceLevel CLI/values/생성자 경로 삭제 |
| 재사용 확인 | 기존 profile-reuse annotation에 minimumMeasurementSeconds·minimumTrials; 확인 결과와 정책 identity에 동결 |
| 기동 기록 | 기존 cluster/server-start에 queueLimit·sourceIntervalNanos·profileIntervalNanos만 보강. 새 scheduler startup/초기 가짜 변경 행 없음 |
| profiler/측정 A | 기존 CR 입력 재사용, 하한2 종단 지원. 기본 trial3/전이target5는 유지 |
| 하네스·환경 | cutover 후 잔존 실험 정책만 binding에 동결. 실제 환경 결합만 B; 이미 있는 values·패키징·예제 계약 유지 |

근거: AGENTS.md Configured behavior/Verification boundaries, ADR0019, 기존
`internal/serving/httpapi/scheduler.go:140-170`의 settings 사전/적용 행,
`observation_log_scheduler.go`의 scheduler stream 저장,
`hack/e5/e5.py:1294`의 config/schedulerSettings/sources/schedule binding이다.
**serving 변경 기록**, **불변 측정 Run**, **완결 후 변경하지 않는 실험 binding**은 서로 다른 동결 경로다.
PUT 기록만으로 시작값이나 외부 ConfigMap 변경까지 완전하다고 주장하지 않는다.

## 범위와 조사 집계

현재 실행 경로의 internal/cmd/api/charts/load-generator, E5와 실제 호출 helper만 바꾼다.
config/samples는 실제 B 제거만 포함한다. 일회성·역사 script·개발 도구 일반화는 제외한다.
Dashboard UI 재설계는 제외하지만 API typed round-trip은 포함한다. E5가 import하는 D2 파일은 호출 함수만 대상이다.
E5→helper 호출 근거와 제외되는 D2 main은 [설계 §6](../design/serving/configuration-hardcoding.md#6-하네스-bind에서-한-번-선택)에 있다.

입력은 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/hardcoding-20261030/`의
`go.md`82행, `harness.md`175행, `chart.md`133행, 합계390행이다(숫자 토큰 수 아님).
[인벤토리](2026-10-30-configuration-hardcoding-inventory.md)는 기존140행을 재분류하고 누락3행을 더한143행이다.
최초 표 집계 A26/A-미동결17/B5/C51/D32/제외12는 cutover 재산정 후 **A26/A-미동결17/B5/C53/D32/제외10**이다. 이동·기록 대상은 **48행** 그대로다.
원 유지·재분류 포함 현재 범위는 A26/A-미동결17/B5/C89/D114/전환제외10, 기존 제외122·열린?10은 별도다. 원390행 대응과 슬라이스별 집계는 인벤토리에 보존한다.
같은 값의 Go/Chart 표현은 별도 조사 행이므로 정책 필드 개수와 같지 않다.

## 구현 순서와 완료 기준

**시작 조건:** 증거 API 승인 배포·1회 전환 리허설·CHILL 우회 수집 제거를 포함한 하네스 cutover·대시보드 브라우저 수용이 기록으로 완료되고 의존 측정 캠페인이 없어야 한다. rev106 배포 시도는 완료가 아니다. rev110/결손 운영 기록도 전체 수용 PASS나 이번 live 확인으로 읽지 않는다.

1. **전환 후 재산정·인터페이스 확정**: 삭제된 host/SSH/crictl 경로 제외, 잔존 행 확정.
2. **측정 정책**: 단일 CI·CR 입력·하한2·결과 저장/재로딩/serving 소비 완결.
3. **serving 용량 settings·요청 reuse**: 모든 typed caller·CapacityInput·κ 캐시·정책 identity 연결.
4. **기존 기록 최소 보강**: cluster/server-start와 settings 실패 의미 정리.
5. **잔존 하네스 A/B — 완료**: cutover 재산정29행 유지/새 제외0행, 선행 우회 수집12개 후보는 제외10/C유지2로 확정. binding에서 실행·분석까지 연결하고 SETTINGS/CapacityInput 후속3필드도 완료. C 정리는 관련 변경에 묶었다.
6. **통합 소유자 단일 검증·인계**: 모두 완료한 뒤 한 번 검사.

파일 영역·병렬 가능 경계·인터페이스·각 slice의 회귀는
[설계 §8](../design/serving/configuration-hardcoding.md#8-구현-슬라이스와-시작-조건)에 있다.
독립 slice는 병렬 가능하지만 같은 파일은 단일 소유자가 맡는다.
구현 완료는 인벤토리의 모든 대상 전달/소비/기록과 SRS 수용 조건을 만족해야 한다.
기본값 회귀는 구조·계산·수용 결과 비교이며 바이트/hash 동등성 관문은 금지한다.

저장 계약은 캠페인 사이 한 번 모아 바꾼다. ADR0030의 v1alpha5와 archive/publication #213~#225는 보존한다. 과거 새 필드 부재는 미기록 사실이며 현재 기본값으로 소급 채우지 않는다. ADR0019 정리는 읽을 수 없어진 영향 작업용 CR/파생 객체만 열거해 운영자가 수행한다. 과학 원시를 삭제하지 않고 dual reader·migration·alias·shim은 없다. 새 CapacityInput은 기본값이어도 새 κ 키로 재계산한다.
검증은 경계에서 한 번, 새 의존성·전역 설정 계층은 없다.
다음 구현 소유자가 마지막에 AGENTS.md의 해당 Go/API/Helm 검사 및 helper의 기존 offline 회귀를 한 번 실행한다.
최초 문서 단계에서는 build/test/make/docker/kubectl/SSH를 실행하지 않았다. 슬라이스5 구현의 offline 검사·기본값 필드 대조는 설계 §6에 기록했으며 e5 실 실행·클러스터·SSH·배포는 하지 않았다.

## 삭제 후보 (19개 파일; 삭제 승인 아님)

다음은 현재 E5 실행 경로 밖의 역사/독립 진입점이다. 다른 운영자가 독립 호출할 가능성은
이 문서만으로 배제하지 않는다. **이번 리팩토링에서 고치거나 삭제하지 않는다**.
사용자 삭제 결정 전에 각 파일의 외부 runbook·독립 호출·보존 필요를 확인해야 한다.

| 번호 | 파일 |
|---|---|
| 1 | hack/measure/gen.py |
| 2 | hack/measure/capture-progress.py |
| 3 | hack/measure/feeder.py |
| 4 | hack/measure/plan-e5a.py |
| 5 | hack/measure/soak_ledger.py |
| 6 | hack/measure/xcheck.py |
| 7 | hack/measure/phase-a/measurement-generalization-20261024/meter-cadence/capture.py |
| 8 | hack/measure/phase-a/measurement-generalization-20261024/meter-cadence/analyze.py |
| 9 | hack/measure/phase-a/measurement-generalization-20261024/cost-shift-approved/recalculate.py |
| 10 | hack/measure/phase-a/measurement-generalization-20261024/post-d2-25-20261025/prepare.py |
| 11 | hack/measure/phase-a/measurement-generalization-20261024/cost-budget/arithmetic_check.py |
| 12 | hack/measure/d9c/split.py |
| 13 | hack/measure/d9c/timeline.py |
| 14 | hack/measure/smolcheck/b1stats.py |
| 15 | hack/measure/smolcheck/collect.py |
| 16 | hack/measure/smoke/smoke.py |
| 17 | hack/measure/smoke/send.py |
| 18 | hack/e5/numbers.py |
| 19 | hack/e5/timeline.py |

D2 `bind_before`의 독립 main·SLO/계단 계획, `conduct_before`의 독립 load/main,
`analyze_before`의 독립 p95/throttle verdict, `sample_cgroups`의 독립 main은
같은 파일의 실제 E5 helper와 분리해서 삭제 가능성을 검토한다(위19개 파일 수에는 미포함).
helper 파일 전체 삭제는 금지한다. example config는 실험 입력으로 재사용 가능한지 확인 전 삭제하지 않는다.

## 열린 점

아래 원분류 ?10행은 현재 동작을 유지한다. 근거 없이 A/B/C로 바꾸어 구현하지 않는다.
이동·기록48행과는 분리한다. cutover 후 슬라이스1에서 실제 잔존을 다시 확정한다.

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

반론: source/profile interval을 API로 바꿀 수 있다는 조사 의견은 있다. 하지만 사용자 결정대로
기동 설정+실제값 기록으로 둔다. binding은 일반 JSON 파일이라 물리적 불변성을 보장하지 않는다.
별도 새 저장 장치를 만들지 않고 완결 binding 수정 금지·새 실험 새 binding 계약을 적용한다.

## 기본값 증명과 저장 증거

기본값 생략과 명시값끼리만 비교하지 않는다. 변경 전 리터럴·fixture·실행 함수를 기준으로 실제 계산을 대조한다. κ는 μ1000/κ959.375와 Grid/Bisection/판정, CI는 평균·분산·자유도·음수 energy 처리, reuse는30초/3회 기본 수락·거절과 비기본 identity, 하한2는 비영분산 active2/idle2 종단을 확인한다. 하네스는 stage/part 시간·rate·offset·offered·full/static/factor·판정을 비교한다. 경로별30/129초·1/2/5초를 합치지 않는다.
배포 비교의 승인 overlay 경로·revision·release·namespace는 구현 착수 기록에 지정한다. values-operating.yaml은 rev74 캡처이지 현재 배포값이 아니다. default/profiler 별도 render의 의미 있는 delta를 비교하며 live 동일성은 별도 증거가 필요하다. 세부 표와 전체 소비자는 설계 §3·§8을 따른다.
settings의 권위는 Decision.Settings다. PUT 사전 append 실패는 변경 전 중단, 저장 후 refresh 실패는 요청/적용 불일치 가능, refresh 후 후속 append 실패의 HTTP 오류는 미적용을 뜻하지 않는다. 증거가 부족하면 미확인이다. 초기·외부 설정은 실제 사용 Decision으로 해석하며 초기/refresh 합성 행·별도 provenance 저장소는 없다.
`demo.py`는 offline 회귀 import 대상이라 수정 금지 목록에서 제외하고 회귀를 유지한다.

## 문서 추적

- spec: [시스템 원칙](../spec/README.md)
- SRS: SCH-71~74 / SCH-AC-56~59, PC-04~06 / PR-05, PA-NV-06~07
- 설계: [소유권·동결·기동 행·슬라이스](../design/serving/configuration-hardcoding.md)
- ADR: [0031](../adr/0031-configuration-ownership-and-freezing.md)

관측 로그·binding·CR의 소스 근거를 읽었으며 live 상태·실제 새 계약 산출물은 검증하지 않았다.
슬라이스2~5 구현은 각 구현 커밋과 설계의 완료 기록을 따른다. 이번 슬라이스5는 코드·환경 sample 선택 절차·인벤토리/설계를 함께 갱신했다.

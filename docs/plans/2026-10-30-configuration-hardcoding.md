# 하드코딩 정리 계획

2026-10-30 · **문서 단계 완료 기준**: 이 계획 → 인벤토리 → spec → SRS → 설계/ADR를 연결하고 문서만 커밋한다. 구현·배포·측정은 하지 않는다.

## 목표와 결정

코드에 박힌 값을 분류해 **A(측정·판정), B(환경)만 기존 설정 소유자에게 옮긴다**.
C(프로토콜·안전·고정 방법론)는 상수로, 이름 없는 C는 의미 있는 이름으로, D(이미 설정)는 그대로 둔다.
모든 숫자를 API로 만들지 않는다. 기본값은 현재값이고 유효한 다른 값도 받을 수 있어야 한다.

| 값의 집 | 결정과 이유 |
|---|---|
| 운영 중 조정할 serving A | 기존 SchedulerSettings GET/PUT. κ 탐색·신뢰수준·재사용·전력 경계의 8필드. 현재 settings 변경 행과 판단 snapshot을 재사용 |
| 배포 시 serving A-미동결 | request/drain timeout, source/profile/directory interval 등은 flags/values 유지. 기존 scheduler stream에 실제 기동값1행을 기록해 숨은 선택을 없앰 |
| profiler/측정 A | 기존 측정 CR spec과 frozen Run/attempt. 이미 있는 minimumRuns·target 등은 재사용하며 숨은 floor 제거 |
| 하네스 A | 실험 config의 effective default를 bind에서 확정해 binding.config에 보존. 서버 settings와 섞지 않음 |
| B | values-operating.yaml/환경 JSON. 기본 values·샘플·스크립트에서 특정 registry/IP/Node/path를 제거하고 실제 생성자까지 연결 |
| 표시용 분위수·chart bin·live window | C/D로 재분류. 저장 원시·수용 판정을 바꾸지 않으므로 이동하지 않음 |

근거: AGENTS.md Configured behavior/Verification boundaries, ADR0019, 기존
`internal/serving/httpapi/scheduler.go:140-170`의 settings 사전/적용 행,
`observation_log_scheduler.go`의 scheduler stream 저장,
`hack/e5/e5.py:1294`의 config/schedulerSettings/sources/schedule binding이다.
**serving 변경 기록**, **불변 측정 Run**, **완결 후 변경하지 않는 실험 binding**은 서로 다른 동결 경로다.
PUT 기록만으로 시작값이나 외부 ConfigMap 변경까지 완전하다고 주장하지 않는다.

## 범위와 조사 집계

현재 실행 경로의 internal/cmd/api/charts/load-generator, E5와 실제 호출 helper만 바꾼다.
config/samples는 B 제거에 필요한 만큼 포함한다. 일회성·역사 측정 script와 별도 Dashboard 구현,
개발 도구·Makefile 일반화는 제외한다. E5가 import하는 D2 파일도 **호출된 함수만** 대상이다.
E5→helper 호출 근거와 제외되는 D2 main은 [설계 §6](../design/serving/configuration-hardcoding.md#6-하네스-bind에서-한-번-선택)에 있다.

입력은 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/hardcoding-20261030/`의
`go.md`82행, `harness.md`175행, `chart.md`133행, 합계390행이다(숫자 토큰 수 아님).
[인벤토리](2026-10-30-configuration-hardcoding-inventory.md)는 이동/기록140행만 나열한다:
A41, A-미동결29, B35, C-이름없음35. D79/C23은 원분류 개수만 유지한다.
재분류16행(C13, D3)을 합치면 최종 C36/D82다. 범위 제외122행, 열린 ?10행이다.
같은 값의 Go/Chart 표현은 별도 조사 행이므로 정책 필드 개수와 같지 않다.

## 구현 순서와 완료 기준

1. **C-이름없음 상수화**(35행): 동작·입출력·오류 불변. 의미가 같은 계약만 공유한다.
2. **B 환경 결합 제거**(35행): 운영 overlay를 주면 기존 port/path/user/catalog로 연결되고 기본·샘플에는 실클러스터 binding이 없다.
3. **serving settings 확장**(9행/8필드): GET/PUT·typed 소비자·ConfigMap·판단·catalog/κ 캐시까지 연결. 옛 confidence CLI/values 경로는 삭제한다.
4. **기동·기존 설정 기록**(12행): 실제 기동값 scheduler1행, 초기/외부 settings 적용 기록, Node cadence는 기존 resources 행에 기록. 새 stream 없음.
5. **측정 CR/frozen 입력**(9행): 기본0.5s/.95/3/5/60s/1.2/seed0의 수용 결과 유지, 비기본값도 끝까지 전달·동결한다.
6. **하네스 config**(40행): 모든 실제 fallback을 bind에서 채우고 실행/분석은 binding만 읽는다. 기본 schedule·offered·판정 의미 유지.

파일 영역·병렬 가능 경계·인터페이스·각 slice의 회귀는
[설계 §8](../design/serving/configuration-hardcoding.md#8-구현-슬라이스와-병렬-경계)에 있다.
독립 slice는 병렬 가능하지만 같은 파일은 단일 소유자가 맡는다.
구현 완료는 인벤토리의 모든 대상 전달/소비/기록과 SRS 수용 조건을 만족해야 한다.
기본값 회귀는 구조·계산·수용 결과 비교이며 바이트/hash 동등성 관문은 금지한다.

저장 계약은 캠페인 사이 한 번 모아 바꾼다. 구형 객체는 운영자가 정리하고 다중 버전 reader·alias·shim은 만들지 않는다.
검증은 경계에서 한 번, 새 의존성·전역 설정 계층은 없다.
다음 구현 소유자가 마지막에 AGENTS.md의 해당 Go/API/Helm 검사 및 helper의 기존 offline 회귀를 한 번 실행한다.
이번 문서 단계에서는 build/test/make/docker/kubectl/SSH를 실행하지 않는다.

## 삭제 후보 (20개 파일; 삭제 승인 아님)

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
| 18 | hack/e5/demo.py |
| 19 | hack/e5/numbers.py |
| 20 | hack/e5/timeline.py |

D2 `bind_before`의 독립 main·SLO/계단 계획, `conduct_before`의 독립 load/main,
`analyze_before`의 독립 p95/throttle verdict, `sample_cgroups`의 독립 main은
같은 파일의 실제 E5 helper와 분리해서 삭제 가능성을 검토한다(위20개 파일 수에는 미포함).
helper 파일 전체 삭제는 금지한다. example config는 실험 입력으로 재사용 가능한지 확인 전 삭제하지 않는다.

## 열린 점

아래 원분류 ?10행은 현재 동작을 유지한다. 근거 없이 A/B/C로 바꾸어 구현하지 않는다.
명시된 A/B/C-이름없음140행은 이것들과 독립적으로 완성할 수 있다.

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

## 문서 추적

- spec: [시스템 원칙](../spec/README.md)
- SRS: SCH-71~74 / SCH-AC-56~59, PC-04~06 / PR-05, PA-NV-06~07
- 설계: [소유권·동결·기동 행·슬라이스](../design/serving/configuration-hardcoding.md)
- ADR: [0031](../adr/0031-configuration-ownership-and-freezing.md)

관측 로그·binding·CR의 소스 근거를 읽었으며 live 상태·실제 새 계약 산출물은 검증하지 않았다.
구현은 후속 세션의 일이며 이 커밋에는 문서만 포함한다.

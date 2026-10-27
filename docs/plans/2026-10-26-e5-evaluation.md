# E5 연구 평가: 남은 입력과 실행 계획

2026-10-26 07:34Z 기준 **평가 시나리오 0/7**. 운영 시연과 30분 불변식 시험은 E5 성적이 아니다. 정본은 [워크로드 풀 설계 §9](../design/serving/workload-pools.md#9-검증-계획), [평가 명세](../spec/README.md#실클러스터-검증과-평가), [용량 계획기](../design/serving/analytic-planner.md)다. 아래는 남은 일과 결정 경계이며 과거 초안의 이미 끝난 배포·비용 보수는 제외했다.

## 입구와 숫자 확정 순서

1. [전체 남은 길](2026-10-26-remaining-roadmap.md)의 rev85·BEO 종결·PLC 후보·이중 컨트롤러 확인과 최종 시연을 닫는다.
2. 새 규칙 dispatch 점유 d **34/34 완료**(control-189/194/195 Accepted·planner 소비 근거). 정확 prepare/remove 비용 **69/69**도 완료이며 두 수를 합산하지 않는다. U2 추가 모드는 기본 숫자 승인과 별도로 확장한다.
3. Workload별 completion residual 원시를 모아 선택 분위수의 값을 계산하고 공통 완료 여유 `m=max(각 추정값)`를 정한다. 현재29ms는 기존 MNV p95 근거이지 세 Workload 공통 새 측정값이 아니다.
4. 최종 d/m과 SLO로 실제 κ(포아송 재생 용량)를 산출한다. μ(프로파일 평균 서비스율)를 κ로 대체하지 않는다. Pending/Rejected/미측정을 0으로 채우지 않는다.
5. Node별 적격 κ와 고립 기준 `R_w=Σ_Node max eligible κ_w`를 확정한다. **단계률은 `rate_w=α_w×s_w×R_w`**, 기본 `poolShares={MNV:0.45,PLC:0.1,Smol:0.45}`, 각 s는 [0,1], 합 1이다. α∈{0.3,0.5,0.7,0.9,1.2}는 POOL 사용률 단계다. Node당 인스턴스 하나이므로 세 고립 R은 동시 사용 불가다. 포화 G를 측정하지 않았으므로 `G` 숫자·추정 headroom 부등식은 입력하지 않는다. 대신 최종 R·몫·α=1.2 총률과 일치하는 **G0 단일 실측 증거**(runID, planned=issued 전체 공급, generator VALID, cs CPU/limit 분율, 승인 PLC 수신 원점 p95)를 binding에 동결하고 검증한다. α는 실제 배치 효율/SLO 보장이 아니라 offered 부하 정규화다.
6. 초기/확장/축소/교체의 실제 후보 witness와 제어요소 비교 집합을 확정한 뒤 숫자·개시 승인을 받는다. 방향 승인을 매 단계 재요청하지 않되 새 조건·범위 확대는 별도 결정이다.

**철회된 안:** 10CPU/8Gi ChillServer 증설을 먼저 하고 G/m을 재측정하자는 10월25일 초안은 현재 계획이 아니다. [게이트웨이 프로파일](../reports/gateway-performance-20261026.md)의 selector 수정으로 기존6CPU/4Gi에서 회복했다. 극단 처리 한계 G 탐색은 시연 선행조건이 아니지만, E5 실제 혼합 부하가 게이트웨이 병목이 아닌지는 확인해야 한다.

## 평가 시나리오 7+1

| ID | 시간 | 부하 | 확인할 결과 |
|---|---:|---|---|
| E5-0 시작 | 10분 | 모두0 | 측정 근거 기반 첫 인스턴스, 빈 Node 최저 모드, 무부하 전체 풀 전력 |
| E5-1 일정 | 30분 | 모두 α=0.3 | 혼합 Workload 지연 분포·성공/실패·전체/활성 풀 에너지 |
| E5-2 버스트 | 15분 | Smol α=0.3→0.9, 나머지 α=0.3 | 대응 시간, 측정 가능한 GPU 장착 Node의 Workload 소유권 재배치, 전환 중 모든 SLO |
| E5-3 증가 | 30분 | 모두 α=0.5→0.7→0.9 각10분 | 확장 순서, κ_new/B/W, 관측 큐와 전환 실패 |
| E5-4 한계 | 15분 | 모두 α=1.2 | Overload·사전식 최소최대 비교와 실제 처리 결과; 게이트웨이 병목과 구별 |
| E5-5 급감/재급증 | 20분 | 모두 α=0.3 10분→0.9 10분 | 느린 축소·재확장, 반복 전환 시간과 에너지 |
| E5-6 장시간 | 60분 | 모두 α=0.3 | 예측/실측 에너지, residual 분포, 설정 여유와 시스템 안정성 |
| E5-7 실패 | 15분 | 모두 α=0.3 | 인스턴스 강제 종료와 Node 하나 배치 차단·서빙 소실의 격리·대체·복구(`SCH-AC-45`) |

현재 알려진 K 대입 예시(승인 R·공식 실행률 아님), MNV/PLC/Smol req/s:

| α | MNV (s=.45) | PLC (s=.1) | Smol (s=.45) | 합계 |
|---|---:|---:|---:|---:|
| .3 | 44.771023 | 49.723307 | .436939 | 94.931269 |
| .5 | 74.618372 | 82.872178 | .728231 | 158.218781 |
| .7 | 104.465720 | 116.021049 | 1.019524 | 221.506294 |
| .9 | 134.313069 | 149.169921 | 1.310817 | 284.793806 |
| 1.2 | 179.084092 | 198.893228 | 1.747755 | 379.725075 |

E5-2의 Smol만 .9인 버스트 합계는95.805146req/s다. α=.3은 대략45/50/.43, G 확인1회는 대략179/199/1.7(합계380)이다.

E5-2 A가 승인안이다. genesis2/genesis3 Smol CUDA 메모리 부족 제외를 유지하고 측정된 CPU 또는 다른 Workload CUDA를 비교한다. GPU 채택 자체를 성공 조건으로 하지 않는다. Jetsono의 Smol CPU INT8 b1 mean1.592s/13.95J 대 CUDA1.651s/48.30J라는 프로파일 근거가 있어 CPU를 제외해 GPU 승자를 만들지 않는다. 별도 CUDA 경쟁 실험으로 자동 전환하지 않는다.

## 비교 기준과 재현 입력

- E5-S 정적 기준선170분은 full-control E5-3 끝 α=0.9에서 실제 선택된 인스턴스·모드·route를 고정한다. 같은 arrival trace/payload/초기 block state를 쓰며 약한 정적 구성을 고르지 않는다.
- 제어요소 비교는 **N=2(membership-off, replicas-off)**를 권고값으로 고정한다. E5-2/E5-5의 같은 arrival trace에서 각각 하나만 끈다. membership은 가용 노드 편입 범위, replicas는 인스턴스 수 변경을 제한하며 실행 전 소스 설정과 실제 구성 차이 witness를 보존한다. 차이가 없으면 no-effect로 보고하며 다른 요소로 몰래 대체하지 않는다.
- placement는 membership/replicas와 겹치는 배치 제약으로 독립 기여를 분리하기 어렵고, routing은 대체 인스턴스로 전달하는 안전 경로이므로 off가 no-op 또는 서비스 중단이 된다. batching은 고정 B 기준선과 deadline 의미가 달라 별도 연구 질문이며, power는 최소안 모드 장면을 full-control에서 관찰하되 별도 off 반복은 하지 않는다. 자원 할당도 측정된 profile과 결합되어 있다. 따라서 명세의 제어요소를 하나씩 비교하는 목표를 모두 충족했다고 주장하지 않는다.
- seed101/202/303, 도착시각, 평가 payload IDs, profiling/evaluation 부분집합을 실행 전 동결한다. 하나의 paired trace set은 case study이며 독립 seed 반복과 다르다.
- 소스 승인은 단일 `approval.sourceSHA` 대신 `approval.sources={"server": "<full Git SHA 또는 pinned binary 경로>", "loadgen": "...", "power": "..."}` 세 식별자를 명시한다. 각 gateway capture·producer publication receipt·power executable receipt를 해당 구성요소와만 대조하고, 서로 다른 실제 버전을 같은 SHA로 바꾸지 않는다. pinned binary 식별자는 repository-relative 경로(예 `bin/observation-power-source-f8c3d2b4`)이며 receipt executable 경로·실제 파일 hash·성공 build receipt를 함께 보존한다. 기존 receipt의 원본 `sourceSHA`는 사실로 유지한다. binding의 `config.approval.sources`와 CPO release의 `sources` map을 동일하게 동결하며 구형 단일-source 승인/release는 거부한다.
- 생성기 설정은 E5 bind와 demo prepare에서 top-level `producerResources: {"cpu":"2","memory":"1Gi"}`, `producerShards: {"mobilenet":3,"plc":1,"smolvlm2":1}`를 기본값까지 binding의 `config`에 동결한다. 명시할 때 resources는 두 키의 양수 Kubernetes quantity 문자열(CPU는 core 또는 `m`, 최소1m 정밀도), shards는 기존 rate alias 세 키의 양의 정수 전체 map이며 bool은 허용하지 않는다. demo 설정의 명시값은 참조 `e5Config`보다 우선하고 미지정값은 참조 설정/기본값을 쓴다. 입력 설정·producer 소스·Job template hash도 기존 sources에 보존한다. 각 producer Pod의 load 및 기존 다른/init container 모두 CPU/메모리 requests=limits를 동결값으로 덮어 Guaranteed QoS를 유지한다. 자원은 shard당이 아닌 Pod/container당 총량이다.
- Workload당 기존 Job/Pod 하나와 기존 CPO setup/GO/drain/cleanup을 유지하며, producer 부모가 `shards`개의 독립 process를 공통 시작 원점으로 실행한다. 동결된 **전역** trace의 `requestIndex % shards`로 분할하고 전역 index·payload 선택·예정시각을 유지하며 stdout을 하나로 합친다. 전체 rate를 shard마다 재생하지 않으며 shard 합집합은 원래 trace와 정확히 같아야 한다. offered/성공률·generator 품질의 분모는 shard별 재정의 없이 기존 전역 planned trace 그대로다. 오프라인 자체 검사: `python3 hack/e5/e5.py self-check`, `python3 hack/e5/demo.py --self-check`(live cluster 호출 없음).
- full195분+static170분=`365분`; 제어요소 비교 `35×2=70분`을 더해 **435분**이다. E5-S에는 실패 주입을 재생하지 않으므로 E5-7의 인과적 정적 비교는 없다. reset/settling/warmup/drain/flush 및 추가 seed는 별도다.
- Tick p50/p99, ChillServer CPU/RSS/GC, 관측 저장량, EdgeMesh throttle/restart, 노드별 전력과 요청 결과를 같은 시간축에 보존한다.

## 집계·제외 규칙

로컬 표 행 생성은 `nice -n 19 python3 hack/e5/e5.py tables <결과 디렉터리>`를 사용한다. 이미 저장된 `e5-summary.json`(기존 `summarize` 출력)과 단계별 `judgment.json`, 또는 G0/시연의 단계별 `summary.json`만 읽는다. `run/` 하위 결과도 인식한다. 출력은 명세 평가 표와 workload-pools 설계 §9 E5 표의 열·설명을 그대로 보존하고 근거 칸만 채우며 정본 파일은 수정하지 않는다. 기존 측정 연결 상태는 PASS가 아니며 G0/시연은 E5 미측정으로 유지한다. 부록에는 생성기 지각(ns), 입구 지연(ns, 음수·시계 불확실성 유지), 수신 기준 SLO 지연(ns 또는 시연 ms)과 기존 판정을 분리한다. 누락값은 `null`이며 재판정하지 않는다. 최소 자체 검사는 `nice -n 19 python3 hack/e5/e5.py tables --self-check`다.

- **offered는 실행 전에 동결한 전체 planned trace의 요청 수**이며 실제 발행 수나 완료 JSONL 행 수로 다시 정의하지 않는다. 미발행·유실 행도 deadline 내 성공의 분모에 남긴다. 실패·거절·취소·만료·drain·미완료·censoring과 표본수를 남긴다. 완료 응답 분위수만으로 전체 성적을 대체하지 않는다.
- 시간은 세 구간을 분리한다. **① scheduled arrival→actual send의 lateness는 generator 품질**, **② actual send→gateway receivedAt은 entrance delay**, **③ CHILL 수신→응답은 SLO**다. [03_routing 결과 반환과 SLO](../spec/03_routing.md#결과-반환과-slo)의 수신 원점을 유지하며 D1/deadline/완료 분위수는 성공·executionComplete HTTP final의 동일 게이트웨이 단조시계 `elapsedNanos`를 사용한다. 현 관측 경계는 gateway Response 진입이며 전체 socket 전달 완료와 같다고 확장하지 않는다. `receivedAt`은 requestID로 raw final/assigned에 결합하고, entrance delay=`receivedAtNS-sentAtUnixNS`는 호스트 간 시계 오차·불확실성을 그대로 표시하며 음수를 0으로 자르지 않는다. native supplement는 HTTP final을 대체하지 않는다.
- `sentMonotonicNS`/`finishedMonotonicNS`와 `sentAtUnixNS`/`finishedAtUnixNS`를 보존한다. `latencyMs`는 실제 send→finish HTTP 왕복이고 `scheduledLatencyMs` 및 `finishedMonotonicNS-scheduledMonotonicNS`(보존 원시 fallback=`finishedMonotonicNS-(startMonotonicNS+scheduledOffsetNs)`)는 **reference only**다. producer lateness를 SLO에 더하거나 요청 lifetime **L에서 차감하지 않는다**. L과 gateway/native의 실제 수신 이후 경과 시간 예산은 원래 계약 그대로이며 residual/tail·occupancy d 정의도 바꾸지 않는다. 결측은 unknown이다.
- 전환 중 수신/전환 밖 수신/분류 불명 세 집단의 합이 전체 분모다. 겹친 전환은 Workload별 합집합으로 한 번만 센다. 경계는 사전에 고정한 반열린 수신시각 구간이다.
- 전환 시작→route/Ready 완료의 실패도 전체에 남기고 전환 전·중·후, stage/substage, 1분 창을 보고한다. 전환 에너지도 전체 풀에 포함한다.
- **Node 끊김 권고안 적용:** 전체 offered 결과를 주 결과로 유지하고, E5-7 Node 차단 명령 시작부터 uncordon 뒤 실제 Ready·관측·route 회복 확인까지 반열린 구간을 표시한다. 전체와 그 구간 제외 집계를 각각 분모·실패 수와 함께 병기한다. 회복이 없으면 단계 끝까지이며, 유리한 요청만 사후 제거하지 않는다.
- **첫 준비 권고안 적용:** 부하 시작부터 첫 실제 분배 ACK까지 요청을 전체 분모에 유지하고 `initialPreparation` 집단으로 별도 표시한다(미반영이면 단계 끝까지). 전환 집단과 중복 가능하므로 합산하지 않는다. 관측된 20~55초를 사후 제외 창으로 쓰지 않는다.
- 전력 결측 허용 `tolerateArchive`는 진단 전용이며 E5 설정에서 거부된다. 단발 후속 HTTP200은 누락된 전력 구간을 복구하지 않는다. 불완전한 에너지 값을 0으로 채우거나 연구 PASS로 출판하지 않는다.
- 알려진 profile-versus-serving 편향은 해석 한계로 공개한다. signed raw·Accepted·planner `max(d,0)`를 보존하고 원인별 인과 기여·전역95% 보장을 주장하지 않는다.
- thermal은5초 주기, active/latched/unknown과 사전 온도 bin(<60/60–70/70–80/≥80°C)을 쓰며 미관측 bin을 일반화하지 않는다.
- 보낸 부하가 계획 trace와 다른 구간과 50ms 초과 발행 공백의 빈도·원인을 [U5/U6 관측 설계](../design/serving/observability-split.md)에 따라 표시한다. `issuanceFidelity`는 planned/issued/미발행 수, 실제/계획 gap, `sendLatenessNS`의 p50/p95/p99/max, 50ms 초과 lateness, `sendsAfterWindow`를 보고한다. `latenessNs = sentMonotonicNS - scheduledMonotonicNS`이며 nominal 창 이후 발행도 버리지 않는다. gap 관측은 nominal 창 끝과 마지막 send 중 늦은 시각까지다. 계획/발행/수신 모집단을 혼동하지 않는다.
- 실행 전 `generatorQuality: {"latenessToleranceNS": 50000000, "maxExceedingFraction": 0.01}`를 권고 기본값(50ms 초과 발행 지각 비율 ≤1%; U6 발행 공백 분석 임계와 동일)으로 예시·G-check·E5 7+1·preflight 설정에 명시하고 binding에 동결한다. 실제 발행 요청 중 `lateness > latenessToleranceNS` 비율이 상한을 넘거나 frozen trace가 전부 공급되지 않으면 `INVALID_GENERATOR`이며 SLO 실패로 귀속하지 않는다. 정책 미설정·필수 timestamp/모집단 결측은 `UNKNOWN`이지 임의의 통과가 아니다. full planned offered 분모는 그대로 두고 `issued=false`인 thread-start 실패도 미발행으로 센다.

## 명세 7행 커버리지와 한계

| 명세 평가 행 | E5 연결 | 남는 한계 |
|---|---|---|
| 일정 부하 | E5-1, E5-6 | 한 paired trace, 독립 반복 없음 |
| 점진적 증가·감소 | E5-3 증가, E5-5 급감 | 점진 감소는 미평가이며 급감으로 대체했다고 주장하지 않음 |
| 갑작스러운 버스트 | E5-2 | Smol만; 다른 모델 버스트 미평가 |
| 전환 직후 급감·재급증 | E5-5 | 실제 직전 전환 완료/진행 시각을 연결; 전환 witness 없으면 미충족 |
| 이기종 동시 혼합 부하 | E5-1~7 | 승인된 세 모델·장비·모드 범위에 한정 |
| 전체 POOL 한계 초과 | E5-4 | gateway 병목과 구분; α=1.2가 실제 overload인지 확인 |
| 노드·서빙·제어 동작 실패 | E5-7 | Pod 소실/배치 차단과 그로 인한 동작 실패 관찰; 실제 전원·네트워크 단절 및 모든 제어 실패 유형 미평가 |

**전력 모드 장면:** [U2 최소안](2026-10-27-power-mode-coverage.md)의 Jetson 각각 두 모드 × 세 모델을 전제로 E5-5 하강/재상승에서 모드 선택·실제 read-back·전환 시간/J를 연결한다. 실행 프로파일, d, prepare/remove, 유휴 baseline, 양방향 모드 전환 비용이 모두 Accepted인 조합만 후보에 둔다. 비용상 KEEP이면 이유와 차단 부등식을 보고하며 강제 변경으로 성공 장면을 만들지 않는다. 선언만 있고 근거가 없는 모드는 후보에 넣지 않는다.

**E5-7 실행 계약:** 시작 뒤 180초 서빙 Pod 하나 강제 삭제 → 360초 Node 하나 cordon 후 그 Node의 사전 기록한 서빙 Pod 삭제 → 600초 uncordon → 900초까지 복구 관찰. cordon은 기존 Pod를 끄거나 통신을 끊지 않으므로 Pod 삭제를 함께 사용한 **배치 차단·서빙 소실 대리 실험**이며 실제 네트워크 단절이라고 보고하지 않는다. 대상은 제어평면·gateway·측정 Node가 아닌 서빙 Node 하나다. 운영 담당만 기존 kubectl을 실행하고 Pod/Node UID, 명령·응답 UTC, scheduler 격리/대체 결정, Ready/ACK·route 회복과 요청/전력을 보존한다. 단계 종료/실패에도 uncordon 복원을 시도하고 read-back을 기록한다. 새 장애 API·컨트롤러는 만들지 않는다.

full binding은 E5-0~7 총195분(11700초), `rowOrder`는 E5-S까지9행이다. E5-S는 계속 E5-1~6만170분(10200초)이며 E5-3 freeze→S→E5-4 release 순서를 바꾸지 않는다. `full-E5-7/failure-events.json`에는 `stage:"E5-7"`, `events` 세 개를 순서대로 남긴다: `offsetSeconds` 180/360/600, `kind` `delete-serving-pod`/`cordon-delete`/`uncordon`, 실제 `atUTC`, `exitCode:0`, `nodeUID`, 삭제 사건의 `podUID`, 실제 명령·응답 파일 경로 `commandRef`/`responseRef`. `recovery`는 같은 `nodeUID`, 실제 `atUTC`, `schedulableRef`/`readyRef`/`observationRef`/`routeRef` 파일 경로다. judge는 시각·동일 Node·파일 hash를 보존하며 누락/실패는 NOT_READY로 남긴다. 이 실제 증거가 있는 E5-7만 summarize/tables의 실패 시나리오를 MEASURED로 연결하고, 선언 runbook만으로 완료 처리하지 않는다.

실행 설정은 `hack/e5/demo-config.e5.example.json`이다. 최종 승인 R을 `capacityR`에 넣어 prepare하면 요청률을 계산한다. 실패 주입은 설정의 운영자 runbook이며 도구가 자동 실행하지 않는다. preflight는 짧은 E5-0+혼합 부하로 토큰·관측·Ready·정리를 확인하고, 실제 실패 주입은 운영자 직렬 실행으로 남긴다. E5 숫자 1회 승인 전 연구 실행을 시작하지 않는다.

## 아직 결정·측정이 필요한 숫자

| 항목 | 현재 경계 |
|---|---|
| MNV SLO | [최종 R 재승인 요청(G0 결과 대기)](file:///home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/e5-numbers-20261027.md): **genesis2 하드웨어결함으로제외한7Node**, PLC36ms·Smol7.034s·MNV1s·m29ms·몫.45/.1/.45 유지확정 |
| PLC/Smol percentile·SLO | 7Node PLC36/40/50ms재생완료·36ms유지확정. R_P387.365408/752.333535/1211.605198, α1.2몫.1 rate46.483849/90.280024/145.392624. 과거genesis2 cache및8Node R합산금지 |
| 적격 Node 선별 | PLC36positive4/7·orangepiBuilt0,jetsonx6resourceRejected≠κ0한계유지. 40/50orange양수로느린2조건FAIL. Smolpositive4/7·느린lattepanda/jetsonx Built0. MNV/PLC관측적격6Node,Smol7Node합 |
| 공통m·L·cap·delivery/socket | residual p95 후보16ms/p99 후보195ms와 구별해 m29ms 유지 승인 요청. L MNV2/PLC1/Smol75초·cap129초·delivery10ms/socket L+20ms, native remaining=L−senderElapsed−m |
| G0·혼합 단계 | 7Node α.3/.5/.7/.9/1.2 총43.639668/72.732780/101.825892/130.919004/174.558672req/s,Smolburst43.986671. 최대 G는 미측정이며 추정값을 입력하지 않는다. G0는 전체 trace 공급·generator VALID·CPU 분율·PLC 수신 p95로 게이트웨이 비병목만 판정한다. control-203의 역사적 전체 FAIL은 보존하되 고정 배치 Smol SLO 미달과 분리하여 gateway gate를 자체 판정한다. |
| κ/R·계산 근거 | `control-201-plc-36/{scheduler,capacity-final,kappa-seven-nodes}.json`,excludedNodes=g1/orin/g2; R MNV235.890404/PLC387.365408/Smol1.285195req/s. 후속40/50비교도동일7Node필터,기본모드/U2분리. 8Node총263.182222입력은현실행에사용금지 |
| 전환 witness | 과거preflight-control-195-u2x 초기 MNV B519.700616/W14.099864초·PLC B1039.401232/W0.802653초는측정SLO에서도초과. capacity재생은rev98에서완료; 일부U2후보/과거UID와최종E5율의초기·확장·축소·소유권swap witness는계속구별 |
| 에너지 선택 | minimumGain 고정%없음; 후보별signed상쇄B_J·gain>B_J/equality KEEP, H600초/reserve0. 최신변경후보gain5005.869972J/B463.558920J는 관측 예이며 기본안 최종통과 아님 |
| 정적·N·추가 모드 | 정적full E5-3끝 α=0.9 실제구성·N=2(membership-off/replicas-off), 없으면no-effect. 기본 fixed/MAXN/jetsonx15W6만 묶고 live jetsono15W 및 U2추가모드는 별도 승인. 전체435분(reset등별도), stabilization300초 유지 |

leximin 변경은 유한 초과량의 선택도 바꿀 수 있으므로 과거 최악값 하나 비교 결과와 섞지 않는다. 최종 실행 설정(두 박자 기본300초/시연120초, reserve비율0, 비교기간600초 포함)은 binding과 decision.settings에 고정한다.
설정 예시의 source SHA·`measurementSLO`·request timing은 기존 도구 입력 형태를 보여주는 값이며 최종 E5 승인값이 아니다. 운영 담당은 승인 숫자 묶음으로 이 필드들과 `capacityR`, `poolShares`, 단계 `alpha`, `g0Evidence`, 실제 digest/근거 binding을 함께 고정한다. 미확정 R은 예시에서 null로 두어 prepare가 거절하며 부분합이나 가상 R로 실행하지 않는다. capacity 재생과 G0 확인 준비는 별도 트랙이며 클러스터 실행은 CampaignOps 직렬 큐만 사용한다.

## G0 확인 1회 운영 절차 (CampaignOps 전용)

`hack/e5/g-check-config.example.json`은 `demo.py`의 기존 staticPlacement 경로를 사용한다. 이 작업의 클러스터 실행은0회다. 계단·최대 G 탐색·자동 재시도는 하지 않는다.

1. 후보 SLO·jetsonx capacity 재생 뒤 최종 승인 R을 설정 복사본 `capacityR`에 넣고 source SHA·digest·SLO·m·request timing·s를 실제 release와 맞춘다. 현재 K 부분합은 설명용이며 R로 대입하지 않는다. 기본 몫은 MNV .45/PLC .1/Smol .45다.
2. 자율제어를 끄고 기존 수동6개(Workload당2개) 또는 현재8 Node의 최대 적격 배치 중 하나를 **고정**한다. Node당 인스턴스 하나, 실제 자원·측정 적격성과 세 route를 확인하고 Node/Pod UID·profile·mode·allocation·batch·route·cs CPU/메모리 limit·예약/전환0을 동결한다. staticPlacement는 배치를 만들거나 자율제어를 끄지 않는다.
3. 기존 `demo.py prepare --config <복사본> --out <새 외부 경로>`, 동결 binding/release의 `start`, `run --stage G-check`, `finish`를 사용한다. **α=1.2의 총 혼합 부하를120s 단1회** 실행하며 현재K 예시는179+199+1.7≈380req/s다. 부하 중 배치 변경·rate clip·단계 추가 없이 finish는 소유 producer만 정리하고 수동 배치·route를 보존한다. 실패/timeout은 증거를 남기며 반복하지 않는다.
4. 기존1s cgroup 원시에서 cs CPU=`ΔcpuUsageNanos/(ΔUTC초×1e9)` core와 경계 `memoryBytes`의10s 비중첩 표본을 만든다. Pod UID/containerID·간격·누락·throttle/restart·OOM·동결 limit와 최대치를 보고하고 재시작 경계를 차분으로 잇지 않는다. planned offered rate, 실제 issued/completed/success rate, **gateway 수신 원점 `elapsedNanos` 기준 PLC p95**와 전체 planned offered 대비 승인 PLC SLO deadline 성공률을 보존한다. generator lateness 품질, send→gateway entrance delay(호스트 시계 불확실성 포함), HTTP 왕복과 scheduled reference는 별도로 병기한다.
5. **게이트웨이 한정 판정:** 동결 planned=offered=issued 전체 공급, generator VALID, 최종 α=1.2 혼합률 일치, gateway 수신→응답 기준 PLC p95≤승인 SLO, 기록 cs CPU peak/동결 limit<1을 원시 지표로 직접 확인한다. 역사적 `g0-verdict.json.status`의 전체 PASS/FAIL 및 Smol 성공률/SLO는 gate 입력이 아니다. 고정 배치 Smol 미달은 별도 참고값으로 남기고 원본 전체 판정을 덮어쓰지 않는다. Smol 외 성공률도 기록만 하며 이 G0 gate의 추가 합격 조건으로 삼지 않는다. 공급·생성기 품질·PLC·CPU 결측/미달은 계속 거부한다.
6. full 입력은 `g0Evidence={"runID":"실제 run 디렉터리명","verdict":"절대 g0-verdict.json 경로","binding":"해당 실제 G0 binding 절대 경로","csCPULimitCores":실제 승인 limit}`를 요구한다. 120초 α=1.2, 최종 R·몫의 Workload별/합계율, binding planned=issued, generator VALID, PLC 승인 수신 p95, cs CPU peak/limit<1을 교차 검증하고 원시 refs·계산 분율·`gatewayVerdict:"PASS"` 및 역사적 전체/Smol 참고값을 별도로 동결한다. G-check 자체는 선행 G0를 요구하지 않지만 연구 full bind는 증거 없이 통과하지 않는다. `G`·`GSource`와 최대 G/80% headroom 숫자는 없고 `GCertified:false`는 미측정 최대G를 뜻한다.

로컬 회귀 점검(클러스터 접근 없음): `python3 hack/e5/e5.py self-check`, `python3 hack/e5/demo.py --self-check`, `python3 hack/e5/smoke_preflight.py`.


원시 근거: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/`의 `deployment2-e5-proposal-20261024.md` §9–10, `e5-numbers-proposal-20261025.md`, `north-star.md`, `d-measurement-decision.md`, `checkpoints.md` 1953–2001행. 원시 capacity·CSV·JSON·측정 캠페인은 옮기지 않았다.

# E5 연구 평가: 남은 입력과 실행 계획

2026-10-26 07:34Z 기준 **평가 시나리오 0/7**. 운영 시연과 30분 불변식 시험은 E5 성적이 아니다. 정본은 [워크로드 풀 설계 §9](../design/serving/workload-pools.md#9-검증-계획), [평가 명세](../spec/README.md#실클러스터-검증과-평가), [용량 계획기](../design/serving/analytic-planner.md)다. 아래는 남은 일과 결정 경계이며 과거 초안의 이미 끝난 배포·비용 보수는 제외했다.

## 입구와 숫자 확정 순서

1. [전체 남은 길](2026-10-26-remaining-roadmap.md)의 rev85·BEO 종결·PLC 후보·이중 컨트롤러 확인과 최종 시연을 닫는다.
2. 새 규칙 dispatch 점유 d **10/34**, 남은 **24/34**를 실제 Accepted 원시와 planner 소비 증거로 채운다. 정확 prepare/remove 비용 **69/69**는 완료이며 d 인증과 합산하지 않는다.
3. Workload별 completion residual 원시를 모아 선택 분위수의 값을 계산하고 공통 완료 여유 `m=max(각 추정값)`를 정한다. 현재29ms는 기존 MNV p95 근거이지 세 Workload 공통 새 측정값이 아니다.
4. 최종 d/m과 SLO로 실제 κ(포아송 재생 용량)를 산출한다. μ(프로파일 평균 서비스율)를 κ로 대체하지 않는다. Pending/Rejected/미측정을 0으로 채우지 않는다.
5. Node별 적격 κ, `R_w=Σ_Node max eligible κ_w`, 0.3R/0.9R, 혼합 단계별 총률과 실제 게이트웨이 통과 범위를 함께 고정한다. 고립 R 셋을 동시 보장 용량으로 더하지 않는다.
6. 초기/확장/축소/교체의 실제 후보 witness와 제어요소 비교 집합을 확정한 뒤 숫자·개시 승인을 받는다. 방향 승인을 매 단계 재요청하지 않되 새 조건·범위 확대는 별도 결정이다.

**철회된 안:** 10CPU/8Gi ChillServer 증설을 먼저 하고 G/m을 재측정하자는 10월25일 초안은 현재 계획이 아니다. [게이트웨이 프로파일](../reports/gateway-performance-20261026.md)의 selector 수정으로 기존6CPU/4Gi에서 회복했다. 극단 처리 한계 G 탐색은 시연 선행조건이 아니지만, E5 실제 혼합 부하가 게이트웨이 병목이 아닌지는 확인해야 한다.

## 평가 시나리오 7+1

| ID | 시간 | 부하 | 확인할 결과 |
|---|---:|---|---|
| E5-0 시작 | 10분 | 모두0 | 측정 근거 기반 첫 인스턴스, 빈 Node 최저 모드, 무부하 전체 풀 전력 |
| E5-1 일정 | 30분 | 모두0.3R | 혼합 Workload 지연 분포·성공/실패·전체/활성 풀 에너지 |
| E5-2 버스트 | 15분 | SmolVLM2 0.3→0.9R, 나머지0.3R | 대응 시간, 측정 가능한 GPU 장착 Node의 Workload 소유권 재배치, 전환 중 모든 SLO |
| E5-3 증가 | 30분 | 모두0.5→0.7→0.9R 각10분 | 확장 순서, κ_new/B/W, 관측 큐와 전환 실패 |
| E5-4 한계 | 15분 | 모두1.2R | Overload·사전식 최소최대 비교와 실제 처리 결과; 게이트웨이 병목과 구별 |
| E5-5 급감/재급증 | 20분 | 모두0.3R 10분→0.9R 10분 | 느린 축소·재확장, 반복 전환 시간과 에너지 |
| E5-6 장시간 | 60분 | 모두0.3R | 예측/실측 에너지, residual 분포, 설정 여유와 시스템 안정성 |
| E5-7 실패 | 15분 | 모두0.3R | 인스턴스 강제 종료와 Node 하나 배치 차단·서빙 소실의 격리·대체·복구(`SCH-AC-45`) |

E5-2 A가 승인안이다. genesis2/genesis3 Smol CUDA 메모리 부족 제외를 유지하고 측정된 CPU 또는 다른 Workload CUDA를 비교한다. GPU 채택 자체를 성공 조건으로 하지 않는다. Jetsono의 Smol CPU INT8 b1 mean1.592s/13.95J 대 CUDA1.651s/48.30J라는 프로파일 근거가 있어 CPU를 제외해 GPU 승자를 만들지 않는다. 별도 CUDA 경쟁 실험으로 자동 전환하지 않는다.

## 비교 기준과 재현 입력

- E5-S 정적 기준선170분은 full-control E5-3 끝0.9R에서 실제 선택된 인스턴스·모드·route를 고정한다. 같은 arrival trace/payload/초기 block state를 쓰며 약한 정적 구성을 고르지 않는다.
- 제어요소 비교는 **N=2(membership-off, replicas-off)**를 권고값으로 고정한다. E5-2/E5-5의 같은 arrival trace에서 각각 하나만 끈다. membership은 가용 노드 편입 범위, replicas는 인스턴스 수 변경을 제한하며 실행 전 소스 설정과 실제 구성 차이 witness를 보존한다. 차이가 없으면 no-effect로 보고하며 다른 요소로 몰래 대체하지 않는다.
- placement는 membership/replicas와 겹치는 배치 제약으로 독립 기여를 분리하기 어렵고, routing은 대체 인스턴스로 전달하는 안전 경로이므로 off가 no-op 또는 서비스 중단이 된다. batching은 고정 B 기준선과 deadline 의미가 달라 별도 연구 질문이며, power는 최소안 모드 장면을 full-control에서 관찰하되 별도 off 반복은 하지 않는다. 자원 할당도 측정된 profile과 결합되어 있다. 따라서 명세의 제어요소를 하나씩 비교하는 목표를 모두 충족했다고 주장하지 않는다.
- seed101/202/303, 도착시각, 평가 payload IDs, profiling/evaluation 부분집합을 실행 전 동결한다. 하나의 paired trace set은 case study이며 독립 seed 반복과 다르다.
- full195분+static170분=`365분`; 제어요소 비교 `35×2=70분`을 더해 **435분**이다. E5-S에는 실패 주입을 재생하지 않으므로 E5-7의 인과적 정적 비교는 없다. reset/settling/warmup/drain/flush 및 추가 seed는 별도다.
- Tick p50/p99, ChillServer CPU/RSS/GC, 관측 저장량, EdgeMesh throttle/restart, 노드별 전력과 요청 결과를 같은 시간축에 보존한다.

## 집계·제외 규칙

- 모든 **유효 offered 요청**을 deadline 내 성공의 분모에 둔다. 실패·거절·취소·만료·drain·미완료·censoring과 표본수를 남긴다. 완료 응답 분위수만으로 전체 성적을 대체하지 않는다.
- 전환 중 수신/전환 밖 수신/분류 불명 세 집단의 합이 전체 분모다. 겹친 전환은 Workload별 합집합으로 한 번만 센다. 경계는 사전에 고정한 반열린 수신시각 구간이다.
- 전환 시작→route/Ready 완료의 실패도 전체에 남기고 전환 전·중·후, stage/substage, 1분 창을 보고한다. 전환 에너지도 전체 풀에 포함한다.
- **Node 끊김 권고안 적용:** 전체 offered 결과를 주 결과로 유지하고, E5-7 Node 차단 명령 시작부터 uncordon 뒤 실제 Ready·관측·route 회복 확인까지 반열린 구간을 표시한다. 전체와 그 구간 제외 집계를 각각 분모·실패 수와 함께 병기한다. 회복이 없으면 단계 끝까지이며, 유리한 요청만 사후 제거하지 않는다.
- **첫 준비 권고안 적용:** 부하 시작부터 첫 실제 분배 ACK까지 요청을 전체 분모에 유지하고 `initialPreparation` 집단으로 별도 표시한다(미반영이면 단계 끝까지). 전환 집단과 중복 가능하므로 합산하지 않는다. 관측된 20~55초를 사후 제외 창으로 쓰지 않는다.
- 전력 결측 허용 `tolerateArchive`는 진단 전용이며 E5 설정에서 거부된다. 단발 후속 HTTP200은 누락된 전력 구간을 복구하지 않는다. 불완전한 에너지 값을 0으로 채우거나 연구 PASS로 출판하지 않는다.
- 알려진 profile-versus-serving 편향은 해석 한계로 공개한다. signed raw·Accepted·planner `max(d,0)`를 보존하고 원인별 인과 기여·전역95% 보장을 주장하지 않는다.
- thermal은5초 주기, active/latched/unknown과 사전 온도 bin(<60/60–70/70–80/≥80°C)을 쓰며 미관측 bin을 일반화하지 않는다.
- 보낸 부하가 계획 trace와 다른 구간과 50ms 초과 발행 공백의 빈도·원인을 [U5/U6 관측 설계](../design/serving/observability-split.md)에 따라 표시한다. 계획/발행/수신 모집단을 혼동하지 않는다.

## 명세 7행 커버리지와 한계

| 명세 평가 행 | E5 연결 | 남는 한계 |
|---|---|---|
| 일정 부하 | E5-1, E5-6 | 한 paired trace, 독립 반복 없음 |
| 점진적 증가·감소 | E5-3 증가, E5-5 급감 | 점진 감소는 미평가이며 급감으로 대체했다고 주장하지 않음 |
| 갑작스러운 버스트 | E5-2 | Smol만; 다른 모델 버스트 미평가 |
| 전환 직후 급감·재급증 | E5-5 | 실제 직전 전환 완료/진행 시각을 연결; 전환 witness 없으면 미충족 |
| 이기종 동시 혼합 부하 | E5-1~7 | 승인된 세 모델·장비·모드 범위에 한정 |
| 전체 POOL 한계 초과 | E5-4 | gateway 병목과 구분; 1.2R이 실제 overload인지 확인 |
| 노드·서빙·제어 동작 실패 | E5-7 | Pod 소실/배치 차단과 그로 인한 동작 실패 관찰; 실제 전원·네트워크 단절 및 모든 제어 실패 유형 미평가 |

**전력 모드 장면:** [U2 최소안](2026-10-27-power-mode-coverage.md)의 Jetson 각각 두 모드 × 세 모델을 전제로 E5-5 하강/재상승에서 모드 선택·실제 read-back·전환 시간/J를 연결한다. 실행 프로파일, d, prepare/remove, 유휴 baseline, 양방향 모드 전환 비용이 모두 Accepted인 조합만 후보에 둔다. 비용상 KEEP이면 이유와 차단 부등식을 보고하며 강제 변경으로 성공 장면을 만들지 않는다. 선언만 있고 근거가 없는 모드는 후보에 넣지 않는다.

**E5-7 실행 계약:** 시작 뒤 180초 서빙 Pod 하나 강제 삭제 → 360초 Node 하나 cordon 후 그 Node의 사전 기록한 서빙 Pod 삭제 → 600초 uncordon → 900초까지 복구 관찰. cordon은 기존 Pod를 끄거나 통신을 끊지 않으므로 Pod 삭제를 함께 사용한 **배치 차단·서빙 소실 대리 실험**이며 실제 네트워크 단절이라고 보고하지 않는다. 대상은 제어평면·gateway·측정 Node가 아닌 서빙 Node 하나다. 운영 담당만 기존 kubectl을 실행하고 Pod/Node UID, 명령·응답 UTC, scheduler 격리/대체 결정, Ready/ACK·route 회복과 요청/전력을 보존한다. 단계 종료/실패에도 uncordon 복원을 시도하고 read-back을 기록한다. 새 장애 API·컨트롤러는 만들지 않는다.

실행 설정은 `hack/e5/demo-config.e5.example.json`이다. 최종 승인 R을 `capacityR`에 넣어 prepare하면 요청률을 계산한다. 실패 주입은 설정의 운영자 runbook이며 도구가 자동 실행하지 않는다. preflight는 짧은 E5-0+혼합 부하로 토큰·관측·Ready·정리를 확인하고, 실제 실패 주입은 운영자 직렬 실행으로 남긴다. E5 숫자 1회 승인 전 연구 실행을 시작하지 않는다.

## 아직 결정·측정이 필요한 숫자

| 항목 | 현재 경계 |
|---|---|
| MNV SLO | canonical p95 1초 유지. [숫자 결정 요청서](file:///home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/e5-numbers-20261027.md) §1–8에 현재값·원시 근거·선택지·권고 |
| PLC/Smol percentile·SLO | 요청서 §5: p95 약32ms/7.034초(m29ms) 비교 후보; 관측 HTTP p95 최대20.417ms/4.437초, p99 최대65.088ms/5.997초. 새 κ 미취득이며 측정용50ms/60초 자동 채택 금지 |
| 적격 Node 선별 | 요청서 §3·5: 현 측정설정 PLC1/8·Smol6/8 κ>0; 누락/자원Rejected는κ0 아님. Smol lattepanda κ0.061346>0이므로 느린2개κ0 조건 불충족; 최종 선별 미성립 |
| 공통m·L·cap·delivery/socket | 요청서 §4·6: residual p95 공통후보16ms/p99후보195ms, 현재29ms 임시유지 권고. 현L MNV2/PLC1/Smol75초, cap129초, delivery10ms/socket L+20ms; native remaining=L−senderElapsed−m |
| G·PLC remainder | 요청서 §6: rev83 고정창140.3req/s·cs CPU최대0.63core(6core 상한)로 여유; 정확G와E5 혼합 통과는 미확인. 알려진κ 부분합의 최소혼합157.420394req/s도 관측범위 초과; rate 자동축소 금지 |
| κ/R·계산시간 | 요청서 §2·3: 새규칙d10/34·미인증24(현재소비legacy MNV CPU8+CUDA2 명시). 실제Built107행, 기록계산시간합1478.446초(병렬wall 아님). 알려진Node 부분합K MNV334.605496/PLC187.046050/Smol3.083102req/s; 완전R·최종SLO/m κ는미확정 |
| 전환 witness | 요청서 §7: 실제 초기/확장/축소/물리교체의λ/q/κ_ret/κ_new/r/B/W 보존. 초기MNV W4.353초·확장5.490초로현SLO초과; 최종E5 혼합률·Workload간GPU소유권swap witness와구별 |
| 에너지 선택 | 요청서 §7: minimumGain 고정%설정없음; 후보별signed상쇄B, gain>B/equality KEEP. 실제교체gain6398.966J/B441.823J, 축소857.547J/B18.151J; H600초/reserve0 유지 |
| 교체·N·기타 threshold | 요청서 §8: 정적기준선은full E5-3끝0.9R 실제구성; N후보2(membership-off/replicas-off), 해당률효과확인전미확정. 기본stabilization300초(최근시연120초); 예시5%/2%/100ms 승인값아님 |

leximin 변경은 유한 초과량의 선택도 바꿀 수 있으므로 과거 최악값 하나 비교 결과와 섞지 않는다. 최종 실행 설정(두 박자 기본300초/시연120초, reserve비율0, 비교기간600초 포함)은 binding과 decision.settings에 고정한다.

원시 근거: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/`의 `deployment2-e5-proposal-20261024.md` §9–10, `e5-numbers-proposal-20261025.md`, `north-star.md`, `d-measurement-decision.md`, `checkpoints.md` 1953–2001행. 원시 capacity·CSV·JSON·측정 캠페인은 옮기지 않았다.

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

## 평가 일곱 시나리오

| ID | 시간 | 부하 | 확인할 결과 |
|---|---:|---|---|
| E5-0 시작 | 10분 | 모두0 | 측정 근거 기반 첫 인스턴스, 빈 Node 최저 모드, 무부하 전체 풀 전력 |
| E5-1 일정 | 30분 | 모두0.3R | 혼합 Workload 지연 분포·성공/실패·전체/활성 풀 에너지 |
| E5-2 버스트 | 15분 | SmolVLM2 0.3→0.9R, 나머지0.3R | 대응 시간, 측정 가능한 GPU 장착 Node의 Workload 소유권 재배치, 전환 중 모든 SLO |
| E5-3 증가 | 30분 | 모두0.5→0.7→0.9R 각10분 | 확장 순서, κ_new/B/W, 관측 큐와 전환 실패 |
| E5-4 한계 | 15분 | 모두1.2R | Overload·사전식 최소최대 비교와 실제 처리 결과; 게이트웨이 병목과 구별 |
| E5-5 급감/재급증 | 20분 | 모두0.3R 10분→0.9R 10분 | 느린 축소·재확장, 반복 전환 시간과 에너지 |
| E5-6 장시간 | 60분 | 모두0.3R | 예측/실측 에너지, residual 분포, 설정 여유와 시스템 안정성 |

E5-2 A가 승인안이다. genesis2/genesis3 Smol CUDA 메모리 부족 제외를 유지하고 측정된 CPU 또는 다른 Workload CUDA를 비교한다. GPU 채택 자체를 성공 조건으로 하지 않는다. Jetsono의 Smol CPU INT8 b1 mean1.592s/13.95J 대 CUDA1.651s/48.30J라는 프로파일 근거가 있어 CPU를 제외해 GPU 승자를 만들지 않는다. 별도 CUDA 경쟁 실험으로 자동 전환하지 않는다.

## 비교 기준과 재현 입력

- E5-S 정적 기준선170분은 full-control E5-3 끝0.9R에서 실제 선택된 인스턴스·모드·route를 고정한다. 같은 arrival trace/payload/초기 block state를 쓰며 약한 정적 구성을 고르지 않는다.
- 제어요소 끄기 비교는 소스에서 구별되고 E5-2/E5-5 해당 rate에서 실제 효과 witness가 있는 N개만 사전 선택한다. membership-off와 replicas-off 우선; placement 중복·routing no-op·batching/power 의미 차이를 확인한다. 7 flag를7회 자동 실행하지 않는다.
- seed101/202/303, 도착시각, 평가 payload IDs, profiling/evaluation 부분집합을 실행 전 동결한다. 하나의 paired trace set은 case study이며 독립 seed 반복과 다르다.
- full180분+static170분=`350분`; 제어요소 비교는 `35N분` 추가. reset/settling/warmup/drain/flush 및 추가 seed는 별도다. 옛595분은7개 자동 실행 산술이며 확정 일정이 아니다.
- Tick p50/p99, ChillServer CPU/RSS/GC, 관측 저장량, EdgeMesh throttle/restart, 노드별 전력과 요청 결과를 같은 시간축에 보존한다.

## 집계·제외 규칙

- 모든 **유효 offered 요청**을 deadline 내 성공의 분모에 둔다. 실패·거절·취소·만료·drain·미완료·censoring과 표본수를 남긴다. 완료 응답 분위수만으로 전체 성적을 대체하지 않는다.
- 전환 중 수신/전환 밖 수신/분류 불명 세 집단의 합이 전체 분모다. 겹친 전환은 Workload별 합집합으로 한 번만 센다. 경계는 사전에 고정한 반열린 수신시각 구간이다.
- 전환 시작→route/Ready 완료의 실패도 전체에 남기고 전환 전·중·후, stage/substage, 1분 창을 보고한다. 전환 에너지도 전체 풀에 포함한다.
- **노드 끊김 구간을 표시하고 별도 제외 집계를 병기하자는 안은 결정 대기**다. 기준·시간 경계·적용 대상 요청을 실행 전에 승인하지 않은 채 사후 좋은 구간만 선택하지 않는다. 전체 offered 결과를 함께 보존한다.
- 전력 결측 허용 `tolerateArchive`는 진단 전용이며 E5 설정에서 거부된다. 단발 후속 HTTP200은 누락된 전력 구간을 복구하지 않는다. 불완전한 에너지 값을 0으로 채우거나 연구 PASS로 출판하지 않는다.
- 알려진 profile-versus-serving 편향은 해석 한계로 공개한다. signed raw·Accepted·planner `max(d,0)`를 보존하고 원인별 인과 기여·전역95% 보장을 주장하지 않는다.
- thermal은5초 주기, active/latched/unknown과 사전 온도 bin(<60/60–70/70–80/≥80°C)을 쓰며 미관측 bin을 일반화하지 않는다.

## 아직 결정·측정이 필요한 숫자

| 항목 | 현재 경계 |
|---|---|
| MNV SLO | canonical p95 1초 유지 |
| PLC/Smol percentile·SLO | p95/p99와 실제 d/m/κ로 선택; 측정용PLC50ms/Smol60초 자동 채택 금지 |
| 적격 Node 선별 | PLC/Smol 최소5/8 κ>0 및 가장 느린2개 κ=0 확인; profile 간격 중점은 후보일 뿐 |
| 공통m·L·cap·delivery/socket | residual 실측 후 고정; L≥SLO, cap≥L, socket>L+delivery, native remaining=L−senderElapsed−m |
| G·PLC remainder | 단계 합계 실제 통과 범위 확인; remainder 음수 또는 overload 의미 소실이면 자동 rate 축소 금지 |
| κ/R·계산시간 | 최종 UID/definition/SLO/m/d/settings/Node 모드가 일치한 실제 행 필요 |
| 전환 witness | `B=q+max(0,λ−κ_ret)r`, `W=B/κ_new≤SLO−m`, 한 Node 한 실물 및 모든 Workload 조건 |
| 에너지 선택 | 실제 keepJ/bestJ와 signed 상쇄 문턱B; 둘 다 feasible이면 gain>B, equality KEEP |
| 교체·N·기타 threshold | 자연 swap 없으면 차단 부등식 공개; 예시5%/2%/100ms를 승인값으로 쓰지 않음 |

leximin 변경은 유한 초과량의 선택도 바꿀 수 있으므로 과거 최악값 하나 비교 결과와 섞지 않는다. 최종 실행 설정(두 박자 기본300초/시연120초, reserve비율0, 비교기간600초 포함)은 binding과 decision.settings에 고정한다.

원시 근거: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/`의 `deployment2-e5-proposal-20261024.md` §9–10, `e5-numbers-proposal-20261025.md`, `north-star.md`, `d-measurement-decision.md`, `checkpoints.md` 1953–2001행. 원시 capacity·CSV·JSON·측정 캠페인은 옮기지 않았다.

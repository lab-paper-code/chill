# SLO 개선을 위한 배치 제어 복구 계획

기준 소스: `4f2d97ca6a9ae09539aa205934d7784c4b2365aa`(Runtime `4fac6584`).
근거 루트: `Q=ops/pools-deploy/deployB/control-374-control-loop-20261110`, `I=ops/pools-deploy/intent/control-decision-20261110/placement`.

## 1. 문제 정의

CHILL은 허용된 자원·물리 제약 안에서 서비스별 E2E SLO(설정 분위수)를 충족하는 배치를 선택하고, 충족 후보끼리는 에너지를 줄인다. 충족이 어려우면 실패·지연 비교 순서로 개선 배치를 선택한다. 이 판단은 제어 주기 안에 나와야 한다.

control374(두 정책 E5, 각 900초)에서 IC는 offered 28,793건 중 15건(excess)과 4,384건(workload)만 기한 내 완료했다. PLC·Smol은 전부 기한 내 완료했다. 부하창 17/17 Tick이 `Overload/NoPlanMeetsSLO`였고, 선택은 IC를 JetsonX 한 대에 둔 채 배치 상한만 바꿨다. 전체 Tick 중앙값은 46.9/44.9초다.

**문제:** 과부하를 관측하면서도, CHILL 자신의 정밀 평가가 모든 SLO 충족으로 판정하는 배치를 선택하지 못했다. 판단도 제어 주기 10초의 4~5배 늦다.

## 2. 근원 원인 — 동일 입력으로 재현

### 2.1 재현 방법

`Q/exact-065051-decision-full.json`의 06:50:51 decision을 복원했다. request stream의 arrival 2,712건과 raw profile 112개를 결합했다. 기록된 keep·selected의 정밀 평가가 원 decision과 일치했다. 수정 전 production v2 Search도 기록과 같은 배치를 다시 선택했다(`I/production-search-before-k1.jsonl`).

### 2.2 원인: 탐색 근사의 처리율이 정밀 평가의 처리율과 다르다

v2는 저비용 탐색 근사로 후보를 정렬하고 상위 16개만 정밀 replay한다. 탐색 근사의 처리율 μ는 profile의 **가장 빠른 service 표본**으로 만든 routing 가중치다(`capacityThroughput`). 정밀 replay는 service 분포 전체와 dispatch occupancy d를 사용한다. 설계 문서도 처리율 `b/(profileMean(b)+b×d)`와 RoutingWeight를 구분하는데([analytic-planner](analytic-planner.md)), M0 §3.3 탐색 식은 RoutingWeight를 μ로 쓴다.

| IC profile (maxBatch 4) | 탐색 μ (최소 표본) | `b/(mean+b·d)` |
|---|---:|---:|
| JetsonX 15W-4core (현재 배치) | 43.9 | 24.5 |
| Rasp5 CPU | 67.0 | 33.2 |
| genesis2 CUDA 1500 | 285.0 | 115.9 |
| JetsonO CUDA MAXN | 124.5 | 50.1 |

IC 수요는 34.3 req/s였다. 탐색 근사는 현재 JetsonX가 수요를 감당한다고 보고 대기열 70건만 초과로 계산했다(excess 0.74). 정밀 평가는 같은 배치에서 IC 실패율 0.936을 냈다. 그 결과 준비 시간이 없는 같은 Node 변형이 상위 16개를 채웠고, 처리 능력을 늘리는 후보는 정밀 평가에 도달하지 못했다. 근사 순위만 문제였다. μ를 바꾼 뒤에도 finalist는 근사상 미충족이었고, 순위가 회복돼 정밀 평가에 도달했다.

**반사실 확인(같은 입력, production v2 Search, 다른 코드·설정 동일):**

| 변형 | 선택 | IC 실패율 | 전체 SLO |
|---|---|---:|---|
| 수정 전 | JetsonX batch 4→1 | 0.936 | 미충족 |
| 탐색 μ와 routing 가중치를 `b/(mean+b·d)`로 | genesis2 GPU IC 추가 | 0.019 | **충족** |
| 위 변형 + 준비시간 항 수정 | 위와 동일 | 0.019 | 충족 |
| 준비시간 항만 수정 | JetsonO CPU 15W IC로 이동 | 0.929 | 미충족 |

이 반사실은 routing 가중치까지 바꿨다. 같은 recorded 선택의 정밀 결과도 0.936→0.937, 84,986→85,070J로 달라졌다. 따라서 원인 메커니즘은 확인됐지만, 해결 여부는 routing·정밀 평가를 보존한 table-only 구현을 같은 입력으로 다시 실행해 판정한다. 증거: `I/production-search-{before-k1,v2,v3-mu-only,v1}.jsonl`.

같은 입력에서 keep 기준 적격 단일 Node 변경 383개를 모두 정밀 평가했다(`I/production-search-before-k1{,-rest}.jsonl`). 물리 admission을 통과하고 SLO를 충족하는 후보는 20개(genesis2 GPU 10, Rasp5 5, JetsonO CPU 5)였다. 이 중 JetsonO CPU IC 추가는 keep보다 에너지가 낮았다(84,828J). 수정 전 탐색 근사는 20개 모두를 미충족으로 보고 finalist에서 뺐다.

### 2.3 기각한 가설

- **준비시간 `+r`을 retained 공급 Workload에 부과:** 계산상 PLC에 가짜 excess를 만들지만, 이번 decision의 선택 실패 원인은 아니다. 단독 수정은 IC 공급을 늘리지 않는 긴 준비 후보를 충족으로 오판해 finalist를 채웠다(위 표 마지막 행). 이번 범위에서 바꾸지 않는다.
- **탐색 예산·마감, finalist 수:** μ 수정 후 같은 예산(500,000)·K=16에서 충족 배치를 선택했다.
- **요청 잠금·자동 제어 비활성·적용 실패:** 잠금합 p50 0.001ms, 모든 controls true, 선택된 배치는 적용됐다.

## 3. 수정 계획

| 순위 | 단위 | 변경 | 완료 기준 |
|---|---|---|---|
| 1 | P 탐색 처리율 | 탐색 table의 profile 항 μ_i를 `max_{b≤maxBatch} b/(MeanNanos(b)+b·d)`로 정의한다. d는 해당 instance에 이미 resolve한 occupancy다. table의 공급·retained 공급·service 가중합·에너지 가중합을 모두 같은 μ_i로 만든다. M0 §3.3 식의 형태와 priced start의 선형화는 그대로이며 μ_i 정의만 바뀐다. 탐색 energy proxy의 평균도 μ_i 가중이 된다. routing 가중치·정수 routing·정밀 replay·전환 예비·bridge·κ 행은 바꾸지 않는다. M0 §3.3·analytic-planner를 같이 고친다 | table 열·Delta·priced 회귀, 같은 입력에서 recorded 선택의 정밀 결과 불변 + production v2 Search가 정밀 평가상 SLO 충족 배치 선택 |
| 2 | R1·R2 정밀 평가 반복 제거 | 구현 완료(`3594b4a5`, `a4288523`). 의미 출력·v1/v2 선택 동일 | main 실측의 전체 Tick·exact 시간 |
| 3 | V1 측정 유효성 전달 | 구현 완료(`aff954ab`). `INVALID > NOT_EVALUATED > FAIL > PASS` 전달 | 보존 두 stage 재처리 |

P는 배치 판단의 해결 경로다. R·V는 독립 작업이며 P의 효과를 대신하지 않는다. P만으로는 시스템 복구가 끝나지 않는다. 제어 주기 10초 충족이 함께 필요하다.

하지 않는 것: 준비시간 항 변경, finalist 수·예산·ranking·seed 순서 변경, 모든 과부하에 ExactK1 추가, routing 가중치 정의 변경, SLO·분위수·minimumDemand·비교기간 변경, 새 cache·설정.

**제어 시간(복구 완료 조건):** 전체 Tick 중앙값 45~47초 중 exact 평가가 32~37초다. 같은 입력의 memo 없는 cold production Search로 비교했다(`I/cold-search-{base,r12,fixed}.jsonl`).

| 소스 | Search | exact | 선택 |
|---|---:|---:|---|
| 기준 `4f2d97ca` | 38.7초 | 37.3초 | JetsonX 4→1, 미충족 |
| + R1·R2 | 38.3초 | 37.2초 | 동일 |
| + R1·R2 + P | 9.95초 | 8.6초 | genesis2 GPU 추가, 충족 |

R1·R2는 이 입력에서 시간을 줄이지 못했다. CPU profile에서 exact 시간의 약 60%는 과부하 replay의 `chooseBatches`가 매 event마다 대기열 전체의 요청 상태를 다시 만드는 비용이었다(`I/cold-search-r12.cpu.pb.gz`). P 뒤 finalist가 과부하 배치가 아니게 되면서 exact가 줄었다. Search만 10초 근처이므로 capture(실측 평균 6.1~8.5초)를 더한 전체 Tick은 10초를 넘을 가능성이 높다. 실측 §5-2로 판정하고, 넘으면 capture와 과부하 replay 비용을 다음 작업 단위로 다룬다.

## 4. 로컬 검증 결과

- 같은 입력, routing·정밀 replay 보존: recorded 선택의 정밀 결과가 기준과 동일했다(84,985.531J, IC 0.935753). finalist 16개가 모두 정밀 평가 충족이었고 production v2 Search는 genesis2 CUDA IC 추가 + JetsonX batch 16을 선택했다. IC 실패율 0.019, PLC·Smol 0, 87,801J(`I/production-search-fixed.jsonl`).
- 한계: 같은 입력의 단일 Node 정밀 평가에서 JetsonO CPU IC 추가가 SLO를 충족하면서 3.4% 더 적은 에너지(84,828J)를 썼다. 탐색 근사는 과도 구간의 최악 대기를 분위수 SLO 대신 쓰고 준비 시간을 모든 Workload에 더하므로 이 후보를 finalist에 넣지 못했다. SLO 충족이 우선이므로 이번 범위에서 바꾸지 않고 실측 후 판단한다.
- `go test ./internal/serving ./internal/scheduler/...` PASS. lint·`make test`·architecture·gopls 결과는 handoff에 기록한다.

## 5. main 실수용

정확 source를 공식 fast-deploy로 배포하고 control374와 같은 두 정책 E5를 실행한다.

1. **배치(주 판정):** Overload decision에서 IC 증설·이동이 선택·적용되는지. 선택 후보의 정밀 평가와 offered 전체 on-time/late/error/timeout을 설정 분위수 SLO로 판정한다. 충족 후보끼리는 에너지, 미충족이면 실패·지연 순서를 확인한다.
2. **제어 시간:** 전체 round/Tick, capture/exact/surrogate 시간, 10초 초과 횟수·최대값.
3. **보존·자원:** frozen POST, 정상 drain/return, heap·GC·working set·재시작.
4. **측정 유효성:** stage·전체 measurement 판정과 서술적 SLO 결과를 분리한다.

배치 효과, 제어 시간, 측정 유효성을 각각 판정한다.

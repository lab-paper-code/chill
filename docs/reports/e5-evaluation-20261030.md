# E5 결과 확정 보고서 — 2026-10-30

## 1. 확정 범위와 결론

정본은 [명세 평가 표](../spec/README.md#실클러스터-검증과-평가)와 [설계 E5 표](../design/serving/workload-pools.md#92-실클러스터-e2e)다. 이 보고서는 두 표의 1·2·3회차 결과를 확정 기록하며, 새 수치 계산·재측정·배포·클러스터 변경을 하지 않는다. 아래 결과 표는 정본 셀을 그대로 전재하고 읽기 편하게 회차를 행으로 바꿨다. 정본의 판정과 UNKNOWN을 유지한다.

수정 후 일정·버스트에서 MNV 실패가 감소했고, C1의 예약 반환은 E5-5에서 관측됐다. 반면 증가 끝 공급 부족, E5-5 Smol 지연, 유효 E5-7의 Smol D1 실패는 남았다. 따라서 전 조건 SLO 보장이나 일관된 에너지 절감으로 결론 내리지 않는다. 1·2회차의 명세 시나리오 결과 연결 7/7, 설계 계획 행 연결 9/9는 PASS 수나 완전 커버리지가 아니다. 3회차는 표적 재실행이며 전체 회차·독립 반복이 아니다.

## 2. 실험 설정

- 실제 서빙 Node 7개: genesis3, lattepanda, rasp5, orangepi, jetsono, jetsonx, jetson-nano. genesis2는 Xid 79 하드웨어 결함으로 제외했다. genesis1은 서버·게이트웨이 Node이며 서빙 후보에서 제외, orin-nano도 제외다. Node당 서빙 인스턴스 하나다.
- 워크로드: MNV=`image-classification-tight-slo`, PLC=`plc-anomaly`(plc-extruder-01-windows), Smol=`smolvlm2-vqa`(VQAv2 vqav2-val-subset).
- 최종 승인 R은 MNV 235.89 / PLC 387.37 / Smol 1.285 req/s, SLO는 각각 수신 기준 p95≤1,000/36/7,034 ms다. R은 전체 Node를 해당 워크로드에 고립 사용했을 때의 적격 κ 합으로, 세 R을 동시에 달성할 수 있다는 뜻이 아니다. 근거: [결정 로그의 7 Node 최종 R·최종 숫자 승인](../intent/decision-log.md).
- 몫(poolShares)은 MNV .45 / PLC .1 / Smol .45. 단계 요청률은 `rate_w=α_w×s_w×R_w`, α는 .3/.5/.7/.9/1.2다. 혼합 feasible을 보장하지 않는다. 단계별 시간·α는 아래 정본 표에 보존했다.
- 공통 완료 여유 m=29ms, 수명 L=MNV 2s / PLC 1s / Smol 75s, gateway cap 129s, delivery 10ms, socket L+20ms다. 생성기 지각 때문에 L을 차감하지 않는다.
- 열린 고리 `chill-loadgen replay --arrivals poisson`: 응답을 기다리지 않고 고정 trace의 예정 시각에 비동기 송신한다. seeds101/202/303과 payload ID를 비교 쌍에서 재사용했다. 예정→송신은 생성기 지각, 송신→수신은 입구 지연, 수신→응답은 SLO로 분리한다. 예정 기준 종단 지연은 참고이며 SLO 원점이 아니다.
- 생성기 품질 조건은 지각 50ms 초과 비율 1% 이하와 frozen trace 전체 공급이다. 위반은 INVALID_GENERATOR이며 서버 SLO 실패로 바꾸지 않는다. 요청마다 새 TLS 연결(keep-alive 없음)을 사용해 입구 지연·게이트웨이 CPU에 핸드셰이크 비용이 포함된다.
- 완료-only p95는 성공·executionComplete HTTP final 분포다. D1은 전체 planned 분모에 오류·거절·취소·무발행·미완료를 유지한 deadline 성공/필요 rank다. 낮은 완료-only p95로 실패를 감추지 않는다.
- 에너지는 전환 포함 7개 관측 Node 합(전체 POOL)과 활성 POOL을 병기한다. 전체 구성 meter 합과 다르며 워크로드별 귀속값이 아니다. ±는 공백 보간 오차 상한이지 통계적 신뢰구간이 아니다. coverage는 gapΣNode/max/maxNode다. 반복 셀·시나리오를 합산하지 않는다. 정적 비교 Δ는 고정−CHILL(양수=CHILL 절감)이다.

## 3. 회차별 소스와 채택 조건

| 회차 | 소스·실행 | 채택 근거와 조건 |
|---|---|---|
| 1 | 수정 전 `control-213-full` | `tables.txt`, `e5-summary.json`; R1~R6 수정 전 결과를 보존한다. |
| 2 | R1~R6+R4b, rev104 `e3f28f23`; `control-222-full` | #222 full E5-0~3/static E5-1~2와 #223 static E5-3~6/full E5-4~7의 합성. `tables-223.txt`, `comparison-control-213-223.txt/.json`, `e5-summary.json`. 복원 accepted=true·fingerprint 일치, prepare ACK 물리 ID만 재결합. 중단 없는 단일 실행이 아니다. |
| 3 | R1~R6+R4b+C1+C2, rev105 `5f1dc4f1`; 같은 Run 아래 표적 재실행 | E5-1/7=#225 `tables-3b.txt`, `comparison-225.txt/.json`; E5-5/6=#224 `tables-3.txt`, `comparison-224.txt/.json`. 시작 상태 복원 fingerprint 일치. 나머지 행 미실행. 고정 E5-S는 2회차 재사용이며 정적 재측정이 아니다. |

원시 루트는 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/`이며 행별 절대 경로는 아래 보존했다. #224 E5-1은 release routing 훼손, E5-7은 selector TTL로 stale snapshot 거절·mutation 없음이므로 무효이며 #225만 채택했다. E5-5는 C1 예약을 포함한 복원 조건을 사용하되 C1 효과는 행 내 신규 예약 반환으로 판단한다. E5-6/7은 2회차의 시작 예약 1개+owner-only 3개를 복원에서 제외했으므로 개선을 C1 효과로 귀속하지 않는다.

## 4. 명세 시나리오와 행별 확정 결과

| 명세 평가 행 | 대응 E5 | 커버리지 경계 |
|---|---|---|
| 일정 부하 | E5-1/6 | 혼합 부하와 공유 결과, 중복 합산 금지 |
| 점진 증가·감소 | E5-3 | 증가만, 점진 감소 미평가 |
| 갑작스러운 버스트 | E5-2 | Smol만 |
| 전환 직후 급감·재급증 | E5-5 | 점진 감소가 아님 |
| 이기종 혼합 | E5-1~7 | 동일 단계 결과를 재사용 |
| 전체 POOL 한계 초과 | E5-4 | OVERLOAD_DESCRIPTIVE |
| 노드·서빙·제어 동작 실패 | E5-7 | 서빙 손실/cordon 대리 실험 |
| 정적 배포 비교(별도) | E5-S | 각 회차 E5-3 끝 α=.9 구성·모드·route 고정 |

이하 MNV/PLC/Smol 순서이며 계획·완료·실패·거절·취소·미완료, 상위 오류 코드와 D1을 생략하지 않았다. 표의 수치는 정본의 소수 셋째 자리 표시 그대로다. E5-S의 묶음 셀은 각 대응 단계의 재기록이지 추가 측정이 아니다.

### E5-0 시작

시간: 10분; 부하: 모두 0. 관찰: Workload마다 측정 근거가 있는 노드의 첫 인스턴스, 나머지 가용 노드의 가장 낮은 모드, 무부하 전체 POOL 전력.

| 회차 | 수신 p95·결과 수·D1 | CHILL 전체/활성 POOL·오차·coverage | 고정 E5-S 전체/활성 POOL·오차·coverage | Δ 고정−CHILL 전체 POOL | 근거 |
|---|---|---|---|---|---|
| 1회차 | E5-0: MNV: p95 UNKNOWN ms; 계획 0; 완료 0 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 UNMEASURED (0/0)<br>PLC: p95 UNKNOWN ms; 계획 0; 완료 0 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 UNMEASURED (0/0)<br>Smol: p95 UNKNOWN ms; 계획 0; 완료 0 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 UNMEASURED (0/0) | E5-0: 53,044.429 / 39,267.728 J; ±0.000 J; gapΣNode=0.000s, max=0.000s, maxNode=0.000% | E5-0: N/A (E5-S 대응 단계 없음) | E5-0: N/A | MEASURED_DESCRIPTIVE; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` |
| 2회차 | E5-0: MNV: p95 UNKNOWN ms; 계획 0; 완료 0 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 UNMEASURED (0/0)<br>PLC: p95 UNKNOWN ms; 계획 0; 완료 0 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 UNMEASURED (0/0)<br>Smol: p95 UNKNOWN ms; 계획 0; 완료 0 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 UNMEASURED (0/0) | E5-0: 53,230.508 / 38,535.972 J; ±154.155 J; gapΣNode=2.012s, max=2.012s, maxNode=0.335% | E5-0: N/A (E5-S 대응 단계 없음) | E5-0: N/A | MEASURED_DESCRIPTIVE; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` |
| 3회차 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 |

### E5-1 일정

시간: 30분; 부하: 모두 α=0.3. 관찰: Workload별 지연 분포와 처리 결과 수(완료, 실패, 거절, 취소, 미완료), 전체 POOL과 활성 POOL 에너지, 창별 예측 백분위와 관측 백분위.

| 회차 | 수신 p95·결과 수·D1 | CHILL 전체/활성 POOL·오차·coverage | 고정 E5-S 전체/활성 POOL·오차·coverage | Δ 고정−CHILL 전체 POOL | 근거 |
|---|---|---|---|---|---|
| 1회차 | E5-1: MNV: p95 395.003 ms; 계획 57,495; 완료 47,268 / 실패 10,225 (BackendUnavailable=9,092; RequestTimeout=1,133) / 거절 0 / 취소 0 / 미완료·불명 2; D1 FAIL (45754/54621)<br>PLC: p95 17.519 ms; 계획 20,605; 완료 20,605 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (20320/19575)<br>Smol: p95 3,280.336 ms; 계획 301; 완료 301 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (301/286) | E5-1: 173,062.740 / 148,610.058 J; ±516.514 J; gapΣNode=37.043s, max=2.999s, maxNode=0.645% | E5-1: 202,214.814 / 184,924.811 J; ±531.703 J; gapΣNode=46.537s, max=6.537s, maxNode=0.657% | E5-1: 29,152.074 ±1,048.217 J | FAIL; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` |
| 2회차 | E5-1: MNV: p95 299.334 ms; 계획 57,495; 완료 57,199 / 실패 296 (BackendUnavailable=272; RequestTimeout=24) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (55983/54621)<br>PLC: p95 16.278 ms; 계획 20,605; 완료 20,605 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (20381/19575)<br>Smol: p95 2,557.590 ms; 계획 301; 완료 301 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (301/286) | E5-1: 179,783.315 / 160,425.068 J; ±6.261 J; gapΣNode=1.841s, max=1.841s, maxNode=0.102% | E5-1: 184,964.392 / 168,219.474 J; ±159.922 J; gapΣNode=9.445s, max=2.262s, maxNode=0.126% | E5-1: 5,181.077 ±166.182 J | MEASURED_DESCRIPTIVE; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` |
| 3회차 | E5-1: MNV: p95 274.882 ms; 계획 57,495; 완료 57,428 / 실패 67 (BackendUnavailable=67) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (56650/54621)<br>PLC: p95 17.094 ms; 계획 20,605; 완료 20,605 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (20377/19575)<br>Smol: p95 3,276.135 ms; 계획 301; 완료 301 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (301/286) | E5-1: 176,890.024 / 153,851.839 J; ±13.133 J; gapΣNode=5.263s, max=1.872s, maxNode=0.104% | E5-1: 184,964.392 / 168,219.474 J; ±159.922 J; gapΣNode=9.445s, max=2.262s, maxNode=0.126% | E5-1: 8,074.368 ±173.054 J | #225; Run=`/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-3b.txt`, `comparison-225.txt/.json`; `full-E5-1-3b/` |

### E5-2 버스트

시간: 15분; 부하: Smol α=0.3→0.9, 나머지 α=0.3. 관찰: 대응 시작 시간, 측정 가능한 GPU 장착 Node의 Workload 소유권 재배치, 전환 중 모든 Workload SLO와 Jetson 모드 변경 지연.

| 회차 | 수신 p95·결과 수·D1 | CHILL 전체/활성 POOL·오차·coverage | 고정 E5-S 전체/활성 POOL·오차·coverage | Δ 고정−CHILL 전체 POOL | 근거 |
|---|---|---|---|---|---|
| 1회차 | E5-2: MNV: p95 1,721.845 ms; 계획 28,643; 완료 26,689 / 실패 1,954 (BackendUnavailable=1,816; RequestTimeout=138) / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (23818/27211)<br>PLC: p95 16.195 ms; 계획 10,374; 완료 10,374 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (10252/9856)<br>Smol: p95 3,963.959 ms; 계획 337; 완료 337 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (337/321) | E5-2: 98,316.669 / 87,536.871 J; ±35.737 J; gapΣNode=9.731s, max=2.146s, maxNode=0.408% | E5-2: 104,571.577 / 96,191.559 J; ±0.000 J; gapΣNode=0.000s, max=0.000s, maxNode=0.000% | E5-2: 6,254.908 ±35.737 J | FAIL; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` |
| 2회차 | E5-2: MNV: p95 236.091 ms; 계획 28,643; 완료 28,643 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (28643/27211)<br>PLC: p95 16.073 ms; 계획 10,374; 완료 10,374 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (10265/9856)<br>Smol: p95 3,416.503 ms; 계획 337; 완료 337 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (337/321) | E5-2: 100,255.448 / 91,732.549 J; ±3.525 J; gapΣNode=1.958s, max=1.958s, maxNode=0.217% | E5-2: 102,597.859 / 94,212.984 J; ±122.848 J; gapΣNode=3.507s, max=1.770s, maxNode=0.197% | E5-2: 2,342.410 ±126.373 J | MEASURED_DESCRIPTIVE; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` |
| 3회차 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 |

### E5-3 증가

시간: 30분; 부하: 모두 α=0.5,0.7,0.9 각10분. 관찰: 확장 순서, 전환 구간 SLO, 판단의 `κ_new`, `B`, `W`와 관측 큐.

| 회차 | 수신 p95·결과 수·D1 | CHILL 전체/활성 POOL·오차·coverage | 고정 E5-S 전체/활성 POOL·오차·coverage | Δ 고정−CHILL 전체 POOL | 근거 |
|---|---|---|---|---|---|
| 1회차 | E5-3: MNV: p95 1,917.629 ms; 계획 134,156; 완료 115,060 / 실패 19,095 (BackendUnavailable=14,084; RequestTimeout=5,011) / 거절 0 / 취소 0 / 미완료·불명 1; D1 FAIL (102509/127449)<br>PLC: p95 16.128 ms; 계획 48,053; 완료 48,053 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (47202/45651)<br>Smol: p95 12,008.491 ms; 계획 682; 완료 682 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (561/648) | E5-3: 211,449.733 / 193,442.322 J; ±516.091 J; gapΣNode=53.626s, max=3.183s, maxNode=0.742% | E5-3: 239,190.398 / 222,443.845 J; ±23.823 J; gapΣNode=2.002s, max=2.002s, maxNode=0.111% | E5-3: 27,740.665 ±539.914 J | FAIL; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` |
| 2회차 | E5-3: MNV: p95 1,654.981 ms; 계획 134,156; 완료 120,593 / 실패 13,562 (BackendUnavailable=13,368; RequestTimeout=194) / 거절 0 / 취소 0 / 미완료·불명 1; D1 FAIL (113590/127449)<br>PLC: p95 16.031 ms; 계획 48,053; 완료 48,053 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (47349/45651)<br>Smol: p95 3,257.114 ms; 계획 682; 완료 682 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (682/648) | E5-3: 220,081.631 / 203,078.172 J; ±86.364 J; gapΣNode=10.886s, max=3.006s, maxNode=0.264% | E5-3: 218,442.310 / 201,706.164 J; ±268.924 J; gapΣNode=10.676s, max=2.018s, maxNode=0.206% | E5-3: -1,639.320 ±355.288 J | FAIL; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` |
| 3회차 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 |

### E5-S 정적 기준선

시간: 170분; 부하: E5-1부터 E5-6까지의 요청률과 seed. 관찰: E5-1부터 E5-6까지와 같은 항목, 고정 구성의 인스턴스와 전력모드.

| 회차 | 수신 p95·결과 수·D1 | CHILL 전체/활성 POOL·오차·coverage | 고정 E5-S 전체/활성 POOL·오차·coverage | Δ 고정−CHILL 전체 POOL | 근거 |
|---|---|---|---|---|---|
| 1회차 | E5-1 고정: MNV: p95 138.124 ms; 계획 57,495; 완료 57,495 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (57495/54621)<br>PLC: p95 17.245 ms; 계획 20,605; 완료 20,605 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (20346/19575)<br>Smol: p95 3,310.022 ms; 계획 301; 완료 301 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (301/286)<br>E5-2 고정: MNV: p95 137.532 ms; 계획 28,643; 완료 28,643 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (28643/27211)<br>PLC: p95 16.336 ms; 계획 10,374; 완료 10,374 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (10268/9856)<br>Smol: p95 16,208.841 ms; 계획 337; 완료 337 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (254/321)<br>E5-3 고정: MNV: p95 132.675 ms; 계획 134,156; 완료 134,156 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (134156/127449)<br>PLC: p95 15.873 ms; 계획 48,053; 완료 48,053 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (47342/45651)<br>Smol: p95 13,027.826 ms; 계획 682; 완료 682 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (549/648)<br>E5-4 고정: MNV: p95 1,964.475 ms; 계획 114,558; 완료 95,171 / 실패 19,386 (BackendUnavailable=19,250; RequestTimeout=136) / 거절 0 / 취소 0 / 미완료·불명 1; D1 OVERLOAD_DESCRIPTIVE (75448/108831)<br>PLC: p95 17.663 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (40705/39542)<br>Smol: p95 74,407.736 ms; 계획 617; 완료 497 / 실패 120 (BackendUnavailable=120) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (58/587)<br>E5-5 고정: MNV: p95 147.741 ms; 계획 76,632; 완료 76,632 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (76632/72801)<br>PLC: p95 16.296 ms; 계획 27,523; 완료 27,523 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (27049/26147)<br>Smol: p95 14,213.074 ms; 계획 382; 완료 382 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (300/363)<br>E5-6 고정: MNV: p95 136.058 ms; 계획 114,558; 완료 114,558 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (114558/108831)<br>PLC: p95 16.425 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (41203/39542)<br>Smol: p95 3,317.283 ms; 계획 617; 완료 617 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (617/587) | E5-1: 173,062.740 / 148,610.058 J; ±516.514 J; gapΣNode=37.043s, max=2.999s, maxNode=0.645%<br>E5-2: 98,316.669 / 87,536.871 J; ±35.737 J; gapΣNode=9.731s, max=2.146s, maxNode=0.408%<br>E5-3: 211,449.733 / 193,442.322 J; ±516.091 J; gapΣNode=53.626s, max=3.183s, maxNode=0.742%<br>E5-4: 133,012.645 / 124,613.068 J; ±206.141 J; gapΣNode=12.187s, max=2.251s, maxNode=0.673%<br>E5-5: 142,065.495 / 125,443.003 J; ±0.000 J; gapΣNode=0.000s, max=0.000s, maxNode=0.000%<br>E5-6: 342,989.174 / 307,660.371 J; ±328.993 J; gapΣNode=20.693s, max=4.811s, maxNode=0.238% | E5-1: 202,214.814 / 184,924.811 J; ±531.703 J; gapΣNode=46.537s, max=6.537s, maxNode=0.657%<br>E5-2: 104,571.577 / 96,191.559 J; ±0.000 J; gapΣNode=0.000s, max=0.000s, maxNode=0.000%<br>E5-3: 239,190.398 / 222,443.845 J; ±23.823 J; gapΣNode=2.002s, max=2.002s, maxNode=0.111%<br>E5-4: 133,396.452 / 125,018.421 J; ±48.755 J; gapΣNode=13.593s, max=2.227s, maxNode=0.704%<br>E5-5: UNKNOWN / UNKNOWN J; ±UNKNOWN J; gapΣNode=77.900s, max=3.511s, maxNode=1.637%<br>E5-6: 405,232.339 / 371,636.855 J; ±67.083 J; gapΣNode=14.787s, max=2.010s, maxNode=0.105% | E5-1: 29,152.074 ±1,048.217 J<br>E5-2: 6,254.908 ±35.737 J<br>E5-3: 27,740.665 ±539.914 J<br>E5-4: 383.807 ±254.896 J<br>E5-5: UNKNOWN ±UNKNOWN J<br>E5-6: 62,243.166 ±396.077 J | INCOMPLETE; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` |
| 2회차 | E5-1 고정: MNV: p95 142.116 ms; 계획 57,495; 완료 57,495 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (57495/54621)<br>PLC: p95 16.268 ms; 계획 20,605; 완료 20,605 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (20397/19575)<br>Smol: p95 2,108.634 ms; 계획 301; 완료 301 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (301/286)<br>E5-2 고정: MNV: p95 141.463 ms; 계획 28,643; 완료 28,643 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (28643/27211)<br>PLC: p95 16.879 ms; 계획 10,374; 완료 10,374 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (10242/9856)<br>Smol: p95 3,654.120 ms; 계획 337; 완료 337 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (337/321)<br>E5-3 고정: MNV: p95 281.794 ms; 계획 134,156; 완료 123,365 / 실패 10,787 (BackendUnavailable=10,643; RequestTimeout=144) / 거절 0 / 취소 0 / 미완료·불명 4; D1 FAIL (119667/127449)<br>PLC: p95 16.417 ms; 계획 48,053; 완료 48,053 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (47395/45651)<br>Smol: p95 3,292.177 ms; 계획 682; 완료 682 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (682/648)<br>E5-4 고정: MNV: p95 1,970.866 ms; 계획 114,558; 완료 61,326 / 실패 53,218 (BackendUnavailable=52,079; RequestTimeout=1,139) / 거절 0 / 취소 0 / 미완료·불명 14; D1 OVERLOAD_DESCRIPTIVE (46146/108831)<br>PLC: p95 16.138 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (40922/39542)<br>Smol: p95 6,337.105 ms; 계획 617; 완료 617 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (607/587)<br>E5-5 고정: MNV: p95 1,890.595 ms; 계획 76,632; 완료 65,747 / 실패 10,883 (BackendUnavailable=10,684; RequestTimeout=199) / 거절 0 / 취소 0 / 미완료·불명 2; D1 FAIL (62133/72801)<br>PLC: p95 15.894 ms; 계획 27,523; 완료 27,523 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (27197/26147)<br>Smol: p95 3,620.171 ms; 계획 382; 완료 382 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (382/363)<br>E5-6 고정: MNV: p95 170.689 ms; 계획 114,558; 완료 114,519 / 실패 39 (RequestTimeout=38; BackendUnavailable=1) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (114519/108831)<br>PLC: p95 16.262 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (41210/39542)<br>Smol: p95 1,976.317 ms; 계획 617; 완료 617 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (617/587) | E5-1: 179,783.315 / 160,425.068 J; ±6.261 J; gapΣNode=1.841s, max=1.841s, maxNode=0.102%<br>E5-2: 100,255.448 / 91,732.549 J; ±3.525 J; gapΣNode=1.958s, max=1.958s, maxNode=0.217%<br>E5-3: 220,081.631 / 203,078.172 J; ±86.364 J; gapΣNode=10.886s, max=3.006s, maxNode=0.264%<br>E5-4: 128,282.988 / 119,920.115 J; ±0.886 J; gapΣNode=1.771s, max=1.771s, maxNode=0.197%<br>E5-5: 139,317.646 / 128,142.974 J; ±755.285 J; gapΣNode=34.949s, max=2.757s, maxNode=0.772%<br>E5-6: 369,924.973 / 336,437.807 J; ±302.348 J; gapΣNode=14.153s, max=2.156s, maxNode=0.111% | E5-1: 184,964.392 / 168,219.474 J; ±159.922 J; gapΣNode=9.445s, max=2.262s, maxNode=0.126%<br>E5-2: 102,597.859 / 94,212.984 J; ±122.848 J; gapΣNode=3.507s, max=1.770s, maxNode=0.197%<br>E5-3: 218,442.310 / 201,706.164 J; ±268.924 J; gapΣNode=10.676s, max=2.018s, maxNode=0.206%<br>E5-4: 128,565.990 / 120,191.187 J; ±32.030 J; gapΣNode=7.023s, max=3.008s, maxNode=0.334%<br>E5-5: 139,031.237 / 127,874.805 J; ±15.373 J; gapΣNode=1.767s, max=1.767s, maxNode=0.147%<br>E5-6: 367,778.969 / 334,305.722 J; ±146.063 J; gapΣNode=12.874s, max=3.024s, maxNode=0.133% | E5-1: 5,181.077 ±166.182 J<br>E5-2: 2,342.410 ±126.373 J<br>E5-3: -1,639.320 ±355.288 J<br>E5-4: 283.002 ±32.915 J<br>E5-5: -286.409 ±770.658 J<br>E5-6: -2,146.003 ±448.411 J | MEASURED_DESCRIPTIVE; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json`; `static-selection.json`, `static-replay-restore.json` |
| 3회차 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 |

### E5-4 한계

시간: 15분; 부하: 모두 α=1.2. 관찰: `Overload` 판단과 최대 초과량 순서, Workload별 실제 지연과 처리 결과, 노드별 인스턴스와 전력모드.

| 회차 | 수신 p95·결과 수·D1 | CHILL 전체/활성 POOL·오차·coverage | 고정 E5-S 전체/활성 POOL·오차·coverage | Δ 고정−CHILL 전체 POOL | 근거 |
|---|---|---|---|---|---|
| 1회차 | E5-4: MNV: p95 1,961.366 ms; 계획 114,558; 완료 101,318 / 실패 13,239 (BackendUnavailable=13,046; RequestTimeout=193) / 거절 0 / 취소 0 / 미완료·불명 1; D1 OVERLOAD_DESCRIPTIVE (77524/108831)<br>PLC: p95 16.410 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (40728/39542)<br>Smol: p95 74,482.013 ms; 계획 617; 완료 499 / 실패 118 (BackendUnavailable=118) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (55/587) | E5-4: 133,012.645 / 124,613.068 J; ±206.141 J; gapΣNode=12.187s, max=2.251s, maxNode=0.673% | E5-4: 133,396.452 / 125,018.421 J; ±48.755 J; gapΣNode=13.593s, max=2.227s, maxNode=0.704% | E5-4: 383.807 ±254.896 J | MEASURED_DESCRIPTIVE; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` |
| 2회차 | E5-4: MNV: p95 1,970.691 ms; 계획 114,558; 완료 59,501 / 실패 55,040 (BackendUnavailable=53,719; RequestTimeout=1,320; ResponseDeliveryFailed=1) / 거절 0 / 취소 0 / 미완료·불명 17; D1 OVERLOAD_DESCRIPTIVE (45077/108831)<br>PLC: p95 15.897 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (40901/39542)<br>Smol: p95 6,308.208 ms; 계획 617; 완료 617 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (609/587) | E5-4: 128,282.988 / 119,920.115 J; ±0.886 J; gapΣNode=1.771s, max=1.771s, maxNode=0.197% | E5-4: 128,565.990 / 120,191.187 J; ±32.030 J; gapΣNode=7.023s, max=3.008s, maxNode=0.334% | E5-4: 283.002 ±32.915 J | MEASURED_DESCRIPTIVE; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` |
| 3회차 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 | 3회차 미실행 |

### E5-5 급감과 재급증

시간: 20분; 부하: 모두 α=0.3 10분, α=0.9 10분. 관찰: 축소와 유휴 제거, 반복 전환의 J와 시간, U2 최소안 Jetson 모드 변경/KEEP 근거와 read-back.

| 회차 | 수신 p95·결과 수·D1 | CHILL 전체/활성 POOL·오차·coverage | 고정 E5-S 전체/활성 POOL·오차·coverage | Δ 고정−CHILL 전체 POOL | 근거 |
|---|---|---|---|---|---|
| 1회차 | E5-5: MNV: p95 286.946 ms; 계획 76,632; 완료 71,169 / 실패 5,463 (BackendUnavailable=4,507; RequestTimeout=956) / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (70317/72801)<br>PLC: p95 16.646 ms; 계획 27,523; 완료 27,523 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (27075/26147)<br>Smol: p95 14,318.976 ms; 계획 382; 완료 382 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (300/363) | E5-5: 142,065.495 / 125,443.003 J; ±0.000 J; gapΣNode=0.000s, max=0.000s, maxNode=0.000% | E5-5: UNKNOWN / UNKNOWN J; ±UNKNOWN J; gapΣNode=77.900s, max=3.511s, maxNode=1.637% | E5-5: UNKNOWN ±UNKNOWN J | FAIL; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` |
| 2회차 | E5-5: MNV: p95 1,895.488 ms; 계획 76,632; 완료 65,848 / 실패 10,780 (BackendUnavailable=10,636; RequestTimeout=144) / 거절 0 / 취소 0 / 미완료·불명 4; D1 FAIL (62176/72801)<br>PLC: p95 17.674 ms; 계획 27,523; 완료 27,523 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (27078/26147)<br>Smol: p95 3,730.820 ms; 계획 382; 완료 382 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (382/363) | E5-5: 139,317.646 / 128,142.974 J; ±755.285 J; gapΣNode=34.949s, max=2.757s, maxNode=0.772% | E5-5: 139,031.237 / 127,874.805 J; ±15.373 J; gapΣNode=1.767s, max=1.767s, maxNode=0.147% | E5-5: -286.409 ±770.658 J | FAIL; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` |
| 3회차 | E5-5: MNV: p95 226.188 ms; 계획 76,632; 완료 75,573 / 실패 1,059 (BackendUnavailable=1,026; RequestTimeout=33) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (75059/72801)<br>PLC: p95 16.189 ms; 계획 27,523; 완료 27,523 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (27142/26147)<br>Smol: p95 14,092.610 ms; 계획 382; 완료 382 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (300/363) | E5-5: 141,731.555 / 130,480.129 J; ±0.000 J; gapΣNode=0.000s, max=0.000s, maxNode=0.000% | E5-5: 139,031.237 / 127,874.805 J; ±15.373 J; gapΣNode=1.767s, max=1.767s, maxNode=0.147% | E5-5: -2,700.318 ±15.373 J | #224; Run=`/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-3.txt`, `comparison-224.txt/.json`; `full-E5-5-3/` |

### E5-6 soak

시간: 60분; 부하: 모두 α=0.3. 관찰: 1시간 예측 에너지 합과 비교 집합 실측, 전체 POOL 실측, `completionResidualNanos` 분포와 설정 여유.

| 회차 | 수신 p95·결과 수·D1 | CHILL 전체/활성 POOL·오차·coverage | 고정 E5-S 전체/활성 POOL·오차·coverage | Δ 고정−CHILL 전체 POOL | 근거 |
|---|---|---|---|---|---|
| 1회차 | E5-6: MNV: p95 394.530 ms; 계획 114,558; 완료 105,294 / 실패 9,264 (BackendUnavailable=8,417; RequestTimeout=847) / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (103903/108831)<br>PLC: p95 15.905 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (41150/39542)<br>Smol: p95 3,290.431 ms; 계획 617; 완료 617 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (617/587) | E5-6: 342,989.174 / 307,660.371 J; ±328.993 J; gapΣNode=20.693s, max=4.811s, maxNode=0.238% | E5-6: 405,232.339 / 371,636.855 J; ±67.083 J; gapΣNode=14.787s, max=2.010s, maxNode=0.105% | E5-6: 62,243.166 ±396.077 J | FAIL; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` |
| 2회차 | E5-6: MNV: p95 147.477 ms; 계획 114,558; 완료 114,558 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (114558/108831)<br>PLC: p95 16.079 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (41257/39542)<br>Smol: p95 1,916.040 ms; 계획 617; 완료 617 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (617/587) | E5-6: 369,924.973 / 336,437.807 J; ±302.348 J; gapΣNode=14.153s, max=2.156s, maxNode=0.111% | E5-6: 367,778.969 / 334,305.722 J; ±146.063 J; gapΣNode=12.874s, max=3.024s, maxNode=0.133% | E5-6: -2,146.003 ±448.411 J | MEASURED_DESCRIPTIVE; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` |
| 3회차 | E5-6: MNV: p95 305.084 ms; 계획 114,558; 완료 114,458 / 실패 100 (BackendUnavailable=93; RequestTimeout=7) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (114099/108831)<br>PLC: p95 16.952 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (41226/39542)<br>Smol: p95 1,962.304 ms; 계획 617; 완료 617 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (617/587) | E5-6: 351,301.405 / 317,219.260 J; ±395.014 J; gapΣNode=39.022s, max=3.994s, maxNode=0.282% | E5-6: 367,778.969 / 334,305.722 J; ±146.063 J; gapΣNode=12.874s, max=3.024s, maxNode=0.133% | E5-6: 16,477.565 ±541.077 J | #224; Run=`/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-3.txt`, `comparison-224.txt/.json`; `full-E5-6-3/` |

### E5-7 실패

시간: 15분; 부하: 모두 α=0.3. 관찰: 180초 인스턴스 강제 종료, 360초 Node 하나 cordon+서빙 Pod 삭제, 600초 uncordon; 격리·대체·실제 복구와 실패 동작 원인.

| 회차 | 수신 p95·결과 수·D1 | CHILL 전체/활성 POOL·오차·coverage | 고정 E5-S 전체/활성 POOL·오차·coverage | Δ 고정−CHILL 전체 POOL | 근거 |
|---|---|---|---|---|---|
| 1회차 | E5-7: MNV: p95 401.963 ms; 계획 28,793; 완료 28,793 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (28789/27354)<br>PLC: p95 17.780 ms; 계획 10,310; 완료 10,310 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (10177/9795)<br>Smol: p95 2,971.640 ms; 계획 140; 완료 140 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (140/133) | E5-7: 83,877.847 / 75,449.598 J; ±1.948 J; gapΣNode=1.498s, max=1.498s, maxNode=0.166% | E5-7: N/A (E5-S 대응 단계 없음) | E5-7: N/A | MEASURED_DESCRIPTIVE; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` |
| 2회차 | E5-7: MNV: p95 145.595 ms; 계획 28,793; 완료 28,793 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (28793/27354)<br>PLC: p95 15.668 ms; 계획 10,310; 완료 10,310 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (10219/9795)<br>Smol: p95 1,775.231 ms; 계획 140; 완료 140 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (140/133) | E5-7: 91,617.432 / 83,235.477 J; ±132.328 J; gapΣNode=3.695s, max=1.929s, maxNode=0.214% | E5-7: N/A (E5-S 대응 단계 없음) | E5-7: N/A | MEASURED_DESCRIPTIVE; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` |
| 3회차 | E5-7: MNV: p95 93.983 ms; 계획 28,793; 완료 28,793 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (28793/27354)<br>PLC: p95 16.053 ms; 계획 10,310; 완료 10,310 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (10226/9795)<br>Smol: p95 1,255.905 ms; 계획 140; 완료 26 / 실패 114 (RequestTimeout=114) / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (26/133) | E5-7: 85,096.635 / 76,740.712 J; ±3.414 J; gapΣNode=2.008s, max=2.008s, maxNode=0.223% | E5-7: N/A (E5-S 대응 단계 없음) | E5-7: N/A | #225; Run=`/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-3b.txt`, `comparison-225.txt/.json`; `full-E5-7-3b/` |

## 5. 제어요소(factor) 비교

다음은 [명세 제어요소 비교](../spec/README.md#제어요소-비교)의 전재다. Δ는 이 절에서만 **factor−full**이다.

| factor | stage | 판정 | M/P/S 완료 p95 ms | 완료/실패/미완료 (M;P;S) | whole POOL J ± gap J | full J ± gap J | Δ J ± 합산 gap J | Δ p95 ms (M/P/S) |
|---|---|---|---|---|---|---|---|---|
| membership-off | E5-2 | FAIL | 1873.910/16.237/16442.460 | 26266/2377/0; 10374/0/0; 337/0/0 | 87482.049 ±6.537 | 98316.669 ±35.737 | -10834.621 ±42.274 | 152.065/0.042/12478.501 |
| membership-off | E5-5 | FAIL | 1952.014/16.636/14549.599 | 20380/56252/12; 27523/0/0; 382/0/0 | 117935.117 ±95.157 | 142065.495 ±0.000 | -24130.378 ±95.157 | 1665.069/-0.010/230.622 |
| replicas-off | E5-2 | FAIL | 660.805/16.652/16343.464 | 28548/95/0; 10374/0/0; 337/0/0 | 94693.407 ±125.736 | 98316.669 ±35.737 | -3623.262 ±161.473 | -1061.040/0.457/12379.505 |
| replicas-off | E5-5 | FAIL | 164.425/17.044/14475.377 | 75458/1174/0; 27523/0/0; 382/0/0 | 135117.952 ±60.594 | 142065.495 ±0.000 | -6947.543 ±60.594 | -122.521/0.398/156.401 |
| power-off | E5-2 | FAIL | 332.523/15.896/2897.453 | 26733/1910/0; 10374/0/0; 337/0/0 | 97588.787 ±0.000 | 98316.669 ±35.737 | -727.883 ±35.737 | -1389.322/-0.299/-1066.506 |
| power-off | E5-5 | FAIL | 1970.804/14.910/2788.570 | 23999/52633/17; 27523/0/0; 382/0/0 | 129255.999 ±0.000 | 142065.495 ±0.000 | -12809.495 ±0.000 | 1683.858/-1.736/-11530.406 |
| routing-off | E5-2 | FAIL | 1688.082/15.526/2979.623 | 24642/4001/0; 10374/0/0; 337/0/0 | 95237.598 ±146.614 | 98316.669 ±35.737 | -3079.072 ±182.352 | -33.763/-0.669/-984.336 |
| routing-off | E5-5 | FAIL | 301.424/15.064/14411.334 | 49106/27526/9; 27523/0/0; 382/0/0 | 124579.162 ±170.154 | 142065.495 ±0.000 | -17486.333 ±170.154 | 14.478/-1.583/92.358 |
| resources-off | E5-2 | FAIL | 355.956/15.890/2777.320 | 26753/1890/0; 10374/0/0; 337/0/0 | 95481.953 ±0.000 | 98316.669 ±35.737 | -2834.716 ±35.737 | -1365.889/-0.306/-1186.639 |
| resources-off | E5-5 | FAIL | 1949.342/24.142/2394.628 | 20306/56326/8; 27467/56/0; 382/0/0 | 126604.774 ±330.508 | 142065.495 ±0.000 | -15460.720 ±330.508 | 1662.396/7.495/-11924.348 |
| batching-off | E5-2 | NOT_READY | 1970.030/219.843/17286.651 | 9662/18981/3; 10361/13/0; 337/0/0 | UNKNOWN ±UNKNOWN | 98316.669 ±35.737 | UNKNOWN ±UNKNOWN | 248.185/203.647/13322.692 |
| batching-off | E5-5 | NOT_READY | 1972.644/158.506/14147.736 | 16959/59673/20; 27286/237/0; 382/0/0 | UNKNOWN ±UNKNOWN | 142065.495 ±0.000 | UNKNOWN ±UNKNOWN | 1685.698/141.860/-171.240 |

### 비교 조건

- **factor 행 = rev102(48c1a44f)**; source SHA는 `48c1a44f5ef5a92aa9645e06e0dc82c9f37acf68`이다. 대응 full은 원본 `control-213-full` 행이며 원본 binding을 보존했다.
- 제어 이미지는 `155.230.35.213:5000/chill/chill-serving-entrypoint@sha256:433265c86aba897fd6f122cc4461a18adb729ef713c24a63e17c90c31bcc3d23`이다. published manifest digest는 같은 `sha256:433265c86aba897fd6f122cc4461a18adb729ef713c24a63e17c90c31bcc3d23`, platform digest는 `sha256:39d2b6afdfea222734048728aa7c8a5b730ec15b7190a02e7d91526ef807f98f`다.
- M/P/S는 MNV/PLC/Smol이며 p95는 완료-only다. 결과 수와 기존 판정은 정본 그대로이며, 완료 수 감소·실패·미완료를 함께 읽는다. 에너지 감소만으로 개선을 주장하지 않는다.
- 단일 paired seed다. Δ는 위 정적 비교와 달리 **factor − full**이며, 에너지 ±는 기록된 gap bound, Δ의 ±는 두 gap bound의 합이다. 통계적 신뢰구간이나 독립 반복 오차가 아니다. 전체 POOL 에너지는 워크로드별 귀속값이 아니며 행끼리 합산하지 않는다.
- placement-off는 offline replay에서 membership-off와 동일 목표이므로 제외했다.
- batching-off는 **고정-cap 대조**다. 전력 gap으로 에너지와 에너지 Δ는 **UNKNOWN**이며 0이나 추정 절감으로 대체하지 않는다.
- E5-5 factor 행은 원본 예약으로 시작 구성을 동결했다. 따라서 원본 예약·초기 구성의 영향과 해당 요소의 독립 기여를 분리하는 식별력에 한계가 있다.
- power-off는 1-step replay에서 차이가 없으며 시간축 효과만 관찰한다. 표의 차이를 단일 판단의 효과로 해석하지 않는다.
- `Main #216 revised order`에 따라 membership-off(#215 보존 결과) → replicas-off → power-off → routing-off → resources-off → batching-off 순서다.
- 정본 수치는 [factor-tables.txt](/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-216-rev102-factors/factor-tables.txt) 그대로다. 상세는 같은 디렉터리 `tables.txt`, 행별 판정·원시는 `<factor>-<stage>/judgment.json`, 대응 full은 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`에 있다. 이 문서 갱신은 클러스터 조작 0, 코드 변경 0이다.


해석: membership/replicas/power/routing/resources-off는 표의 기존 FAIL을 유지하고, batching-off는 NOT_READY·에너지 UNKNOWN을 유지한다. 실패·미완료와 처리 작업량이 다르므로 에너지 감소만으로 개선을 주장하지 않는다. rev102 factor와 수정 전 full의 제한된 단일 paired case study이며, R6 수정 뒤 재실험이 아니므로 수정된 제품에서 요소별 독립 기여나 batching 효과량을 확정하지 않는다.

## 6. 결함 → 수정 → 관측 추적

[결정 로그](../intent/decision-log.md)의 사이클 1·2를 기준으로 한다. 카드 정본은 원시 루트의 `analysis/P2-synthesis.md`(사이클 1), `analysis/P2-synthesis-c2.md`와 `analysis/cards-r2/`(사이클 2)다. R 번호는 군집이며 카드 ID와 구별한다. 여러 수정이 함께 적용됐으므로 아래 전후 관측을 각 커밋의 독립 인과 효과량으로 해석하지 않는다.

| 군집 | 카드 ID | 결함과 수정 커밋 | 관측 효과·미검증 경계 |
|---|---|---|---|
| R1 | `full-E5-1-01`, `full-E5-2-01`, `full-E5-3-01`, `full-E5-5-01`, `full-E5-6-01`; 각 단계 `-90` | ready-subset이 retained 공급을 철회 → old-serving 보존 `75bf0a4f` | 사이클 2에서 old-serving 보존 확인. 정본 MNV 실패 E5-1 10,225→296, E5-2 1,954→0. 카드의 E5-1 10,227은 원시 오류 집계로 표의 실패·미완료 분류와 분모가 다르다. 창 오류 37,187은 시간 겹침이지 전량 인과 귀속이 아니다. |
| R2 | `full-E5-1-02`, `replacement-01` | 역방향 교체 최소 체류 부재 → optional 교체 dwell `22296404` | 사이클 2 직접 ABA(E5-1/2) 14→3. 용량 회복은 예외이며 실제 에너지 인과는 미확정. E5-3 부족을 dwell 차단으로 해석하는 가설은 반증됐다. |
| R3 | `full-E5-5-02`, `reservation-lifecycle-05` | 성공 cleanup이 route까지 동결 → handoff 후 route-only 권한 분리 `9b237c1c`, `fdb7cb53` | 사이클 2 routeOnly 19 owner-stage/50 owner-tick(14단계) 관측. 모든 tick에서 실제 갱신했다는 뜻은 아니다. |
| R4 | `full-E5-6-02`, `full-E5-7-02`, `reservation-lifecycle-01`~`04` | partial 후 예약 고착 → autonomous UID·late prepare known-effect 수렴 `d0cea2eb` | 사이클 2 실패 복구 성공 관측 0, 실패 owner 잔류. 정상 owner 반환을 실패 복구 효과로 세지 않는다. |
| R4b | #218 반쯤 된 prepare 사건(결정 로그; 별도 신규 카드 ID 없음), 후속 `reservation-01` | Pod Ready/native policy 미ACK의 미청구 Pod → 보상 퇴역 `f2d26f8e`, 보상 검사 분리 `e3f28f23` | #218 잔류 경로 보완. 그러나 사이클 2 `/compensate/` action 0, 실패 owner `plan-ca1ac7eb0b69` 잔류로 C1 확인. R4b 단독 성공을 주장하지 않는다. |
| R5 | `full-E5-7-01` | 제거할 dead/zero-weight executor ACK 의존 → positive-route ACK만 `78cf4699`, `0f206bf6` | 2회차 E5-7은 비활성 Pod·assigned 0 주입이라 R5 검증 무효. 3회차 #225 활성 경로 주입·격리 CAPTURED는 유효하나 Smol D1 FAIL은 남는다. 완전 복구 보장이 아니다. |
| R6 | `factors-01` | batching OFF가 다른 축 후보까지 소거 → 현재 정책 후보 투영 `3433204f` | replay E5-2 허가 8→78. 수정 후 factor 종단 재실험은 보류돼 지연·에너지 개선량 미검증. |
| C1 | 사이클 2 `reservation-01` | R4b child scope는 workload 전체인데 proof는 자기 receipt만 → frozen parent sibling exact-proof 공유 `651cd021`, `0b67b252` | E5-5 신규 예약 6개 전부 행 내 반환, Fixed tick 0(2회차 560). 근거 `full-E5-5-3/c1-reservation-return.json`; 최초 부재 archived tick 기준이다. BU 10,636→1,026, MNV p95 1,895.488→226.188ms. E5-6/7은 시작 조건 달라 C1 귀속 금지. |
| C2 | 사이클 2 `full-E5-1-01` | all-positive-ready 장벽이 독립 Node 새 Ready를 못 씀 → old 보존+검증된 순증 `048624d7`, `8639a569` | 원 카드 178 실패 노출은 회복량이 아니다. 유효 #225 E5-1 MNV p95 299.334→274.882ms, 실패 296→67. #224 release 훼손 실행의 악화는 제품 회귀 판정에서 제외. |
| C3(비결함) | 사이클 2 `full-E5-3.json=[]`; `full-E5-3-notes.md` | 빈 Node add-only 413후보 전부 CapacityRejected(측정 row 없음); 제품 diff 0 | 구조적 용량/탐색 공간 한계. 새 Node MNV 실측·더 큰 profile 등록이 다음 일이며 dwell·물리 fence 해제로 해결하지 않는다. |

## 7. 조건·한계와 중단 이력

1. **기준 배치 차이:** 각 회차 E5-3 종료에서 정적 배치를 독립 선택해 MNV 공급 κ가 119.601→91.907 req/s로 달랐다. E5-4/5의 실패·에너지 차이는 이 차이와 함께 읽어야 하며 동일 배치 A/B가 아니다. 명세 조건의 “원인 조사 중”은 기록 그대로 남아 있으나, 이후 사이클 2는 빈 Node add-only 측정 row 부재(C3)를 확인했고 dwell 차단 가설을 반증했다. 같은 profile κ 산식 변경으로 해석하지 않는다.
2. **에너지 미확정:** 1회차 E5-S S5 maxNode 공백 1.637%>1%로 S5·정적 합·총 차이는 UNKNOWN이다. 대응 CHILL 170분 전체 POOL 1,100,896.456 J는 정본 인용이며 UNKNOWN을 0으로 채우지 않는다. 2회차와 3회차도 행별 음수 Δ·오차·실패를 유지하며 총 절감을 새로 계산하지 않는다.
3. **생성기·환경 중단:** #217 collector 교체 경합(`d45fe66e`), #218 rate-limiter prepare 실패 뒤 예약 잔류→R4b, #221 genesis1 디스크 압박·eviction, #222 stdout/stderr 로그 혼입(`41f352e6`)을 분리 기록한다. 중단 단계는 채택 표에 합산하지 않는다. 하네스/환경 중단을 제품 성능 판정으로 바꾸지 않되 #218이 드러낸 예약 수렴 제품 결함은 별도 추적한다.
4. **3회차 무효 실행:** #224 release routing 훼손은 `7d4e812c`, selector TTL은 `46ec78be`, 태그 일반화는 `22fa6439`로 하네스를 수정했다. E5-1/7은 #225만 유효하다. 표적 복원 하네스 `c6c4a80c`와 제품 rev105를 구별한다.
5. **장애 범위:** E5-7은 UID 사전조건부 Pod 삭제(강제 삭제·gracePeriodSeconds=0 아님), cordon+삭제, uncordon이다. 1회차 genesis3/lattepanda, 2회차 jetson-nano/orangepi로 대상도 달랐다. 2회차 결과 수 자체는 보존하지만 비활성 경로 주입이므로 R5 복구 검증에 쓰지 않는다. #225는 routing weight>0·직전 assigned>0인 실제 serving Pod 두 Node에 180.7/360.7/600.1s 주입 exit0, 격리 CAPTURED다. Smol 실패 114(RequestTimeout), D1 FAIL(26/133)은 낮은 완료-only p95와 무관하게 보존한다. 물리 전원·네트워크 손실이나 모든 제어 실패·완전 복구는 미측정이다.
6. **일반화 범위:** 점진 감소·다른 모델 버스트·독립 반복·모든 요소의 독립 기여는 미평가다. 단일 paired seed 집합이고 3회차는 표적 재실행이다. BackendUnavailable은 포화 뒤 native deadline도 포함하므로 모두 라우팅 공백으로 일반화하지 않는다. genesis2 제외·TLS per-request 조건 밖으로 결과를 일반화하지 않는다.
7. **측정 모델:** Smol D04/D09/D14/D23/D33의 알려진 profile-versus-serving 체계적 차이와 원인별 기여 미확정은 설계 조건대로 유지한다. 미측정/부적격 자원을 κ0의 측정 근거로 바꾸지 않는다. 특히 PLC jetsonx resourceRejected와 κ0을 구분한다.

## 8. 다음 과제

[최종 사용자 결정](../intent/decision-log.md)의 순서를 따른다.

1. kubeedge CloudCore nodeName→Helm nodeSelector 및 genesis1 디스크 점유·보존 규칙 정리.
2. 게이트웨이 분리 미사용 코드 정리. ADR 0029 Deferred를 유지하고 U11 G2 배선은 하지 않는다.
3. [E5 evidence API 계획](../plans/2026-10-28-e5-evidence-api.md)의 0장 publication 계약부터 구현.
4. [Dashboard live-stage 계획](../plans/2026-10-26-dashboard-live-stage.md) S1→S2.

별도 누적 과제는 C3 빈 Node MNV/더 큰 profile 실측, rate-limiter 원인, summarize 메모리, E5-7 활성 경로 자동 주입, TLS keep-alive, R6 factor 재실험이다. 이 보고서는 이들을 완료하거나 배포했다고 주장하지 않는다. 새 수치·새 판정·독립 효과량을 만들지 않고 현재 증거의 결과 확정으로 종료한다.

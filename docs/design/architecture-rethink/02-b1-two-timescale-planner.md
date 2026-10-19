# 02. B1 — 두 시간척도 분리와 분석형 용량 계획기

상태: **권고(B2와 함께)**. 명세 변경 ①, ①', ②, ③, ④가 필요하다([06](06-spec-change-proposals.md)).
채택 전 M4 shadow 검증을 통과해야 한다([05](05-recommendation-migration.md), [07](07-validation.md)).

## 핵심 아이디어

- **빠른 경로(ms~s):** 요청 처리는 지금의 router 가중치와 firstfloor EDF batcher가 계속 담당한다.
  버스트는 1차로 여기서 흡수한다.
- **느린 경로(30–60초, 또는 SLO 위반 신호가 오면 즉시):** 계획기가 요청 재생 대신 프로파일에서 미리 계산한
  **조건별 용량표**로 후보를 평가한다. SLO 위반 신호(짧은 창의 관측 분위수 > 목표, 또는 큐 길이 임계)로
  즉시 재계획하는 것은 Knative panic mode와 같은 *같은 계획 로직의 조기 호출*이므로 04 명세의
  "버스트와 점진적 변화에 서로 분리된 임시 대응을 붙이는 대신 같은 제어 로직" 조건과 맞는다.
- **용량표 항목:** 측정 조건 *i*(노드·모드·할당)와 Workload *w*마다:
  - SLO를 만족하는 최대 배치 b\*. Nexus식 보수 상한: 직전 배치를 기다리고 자기 배치를 실행하므로
    `l_q(b) + l_q(b) + 완료여유 ≤ SLO_w`. 공유 인스턴스면 다른 Workload 배치의 `l_q`를 더한다
    (워크로드별 배치 + EDF에서의 최악 대기: `Σ_{w'} l_q(b_{w'}) + l_q(b_w) + 완료여유 ≤ SLO_w`).
    `l_q`는 SLO 백분위(또는 더 엄격한 분위수)의 배치 실행시간.
  - 지속 처리율 `μ = ρ·b*/l̄(b*)` (`l̄`은 평균 배치 시간)
  - 요청당 증분 에너지 `e_inc(b*)`
  - 여유율 ρ(예: 0.8)는 Serving 관측으로 보정한다.
  - 더 촘촘한 상한이 필요하면 batch-size 의존 서비스시간 큐의 closed form이나 edge 가속기 공유 queueing
    모델을 쓸 수 있다([references](references.md)).
- **계획 문제:** "λ_w를 인스턴스에 나눠 맡기고 `Σ노드전력(모드) + Σλ·e_inc`를 최소화한다"는 작은
  bin-packing이다. 탐색 범위는 현재 구성의 한 동작 이웃(인스턴스 추가·제거, 모드 변경, 할당 변경, 노드 이동)으로
  한정한다(유지 후보 포함 → `SCH-02`의 유지·변경 비교 유지).
- **재생의 역할 축소:** 재생은 상위 K개(예: 3개 × 20회)의 **검증기**와 오프라인 실험 simulator로만 쓴다
  (AlpaServe식). 재생 코드는 지우지 않는다.

## 현재 설계에서 지우는 것

- 128후보 × 100회 재생 루프. 작업량 ~12800 → ~60 재생, 약 200배 감소 `[추론]`.
- snapshot의 arrival 전체 보존, 후보마다 Profile 본문 보존.
- 전체 POOL 절대 에너지 비교에 딸린 장치들(명세 변경 ②가 전제):
  - 측정 POOL 노드 공통 항(workload-pools §3.4)
  - 가용 노드 내림의 `Fixed`와 `AvailableNodeLoweringExcludedFromComparison` assumption
  - "baseline 없는 노드 하나 → 전체 Pending"

## Workload별 자동제어

- 계획기는 "Workload–공유 노드" 그래프의 **연결 성분** 단위로 푼다.
- 꺼진 Workload의 인스턴스는 고정 제약이다.
- 꺼진 Workload의 SLO 평가는 그 노드의 동작이 바뀔 때만 한다(workload-pools §4.4와 같음).
- 다른 성분은 서로 영향이 없으므로 따로 풀어도 같은 답이다. `SCH-15`의 "켜진 모든 Workload의 SLO를 함께
  만족"은 성분별로 성립하면 전체로 성립하므로 문장 변경이 필요 없다.

## 활성 POOL과 측정 POOL

- 활성 POOL은 workload-pools §3.2처럼 인스턴스에서 도출한다(유지).
- 측정 POOL 노드는 계획 대상에서 빠지기만 하면 된다. 에너지 항을 더할 필요가 없다(차이 비교).

## 전환 비용

- 비교식: `E_c(H) = E_trans(c) + P_old·d_c + P_c·(H − d_c)` (H: 비교 기간, d_c: 측정한 전환 시간).
  지금 정의와 같고 계산만 분석형이다. 시간과 에너지 단위는 섞지 않는다(`SCH-18`).
- 전환 중 용량: make-before-break(새 용량 Ready → route → drain → remove)를 고정하면 "전환 중 용량 ≥
  min(이전, 이후) − 동시에 빠지는 용량"이라는 상수 시간 검사로 끝난다(`SCH-25`).
- 반복 전환 억제는 전환 비용 자체가 히스테리시스 역할을 한다. 고정 대기시간 규칙은 명세가 금지하지 않지만
  (05 "명세의 고정 규칙으로 두지는 않는다") 필요하지 않다.

## 에너지 계산

- 예측: `P_node = P_base(모드, 적재 상태) + Σ λ·e_inc`. 가용 노드는 `P_unloaded(최저 모드)`.
  유휴분 중복 계산 금지, 노드당 한 번(`SCH-17`)은 이 식에서 자연히 성립한다.
- 보고: 실측 노드 벽전력을 TSDB에서 적분한다. 전체 POOL과 활성 POOL 소비는 `chill_node_pool{node,pool}`
  소속 metric과 join하는 recording rule로 실시간 산출한다.

## 관측과 실시간 조회

- metric: ChillServer와 firstfloor의 `/metrics`로 Workload별 요청 counter, 지연 native histogram,
  노드 전력, 판단의 예측 백분위와 예측 J를 내보내 Prometheus 또는 VictoriaMetrics single-node에 저장한다.
  - Prometheus는 sample당 평균 1–2 byte(원문 확인). 2k series × 5초 간격 ≈ 하루 ~70MB `[추론]`.
- 원시 요청 기록: 사건 줄 여러 개(현재 `RequestEvent`, `types.go:236-254`, 요청당 ~8줄) 대신 완료 시
  **요청당 wide row 1개**(모든 phase 시각 포함)를 쓰고, 시간 단위 zstd 세그먼트로 S3에 보낸다.
  재집계(`SCH-31`)는 DuckDB 등으로 한다.
  - 현재 요청당 ~8줄 × ~400B ≈ 3KB, wide row 압축 시 ≲100B → 약 30배 감소 `[추론]`.
- 예측과 실측을 같은 시간축에서 비교하는 것(`SCH-30`)은 PromQL로 바로 된다.
- 이력 조회: TSDB range query로 일 단위도 1초 미만 `[추론, 일반적 TSDB 특성]`.

## 성능 `[추론]`

N≈10 노드, M≈2–4 모드, A≈3 할당, W≈3–5 Workload 기준.

| 항목 | 현재 | B1 |
|---|---|---|
| 판단 1회 계산 | ≥3.8×10⁷ 요청 재생, 초 단위 | 이웃 ~N·(M+A+W) ≈ 100–200개 × O(인스턴스) 분석 평가 ≲10ms + 상위 3개 검증 |
| 판단 기록 | ~10MB × 6/분 ≈ 3.6GB/h | 계획 diff(≤20 동작 × 200B) + 상위 10개 요약(×300B) + 입력 digest(λ, 관측 분위수, profile ID ~2KB) ≲10KB. 변경 시 + 분당 heartbeat → 0.6–3.6MB/h (~1000배 감소) |

## 실패 처리

- 계획기는 상태 없이 관측 상태에서 다시 계산한다(level-based reconciliation).
- 실행기는 (plan generation, action ID)로 멱등하다.
- 새 계획이 이전 계획을 대체하면 실행기가 diff만 실행한다. 진행 중 동작은 기존 진행으로 추적한다(`SCH-27`).
- 노드 소실은 kubelet node Lease 만료로 감지해 제외하고, 실제 복구 확인 후 재참여한다(`SCH-26`).
- 실패한 변경이 원복됐다고 가정하지 않는다(05 명세 유지).

## 유지·변경하는 요구사항

- 유지: SLO 우선, 전환+Serving 총에너지, 측정 근거로만 선택, Workload별 자동제어, 측정 POOL 의미,
  완료 여유(workload-pools §3.6), 가용 노드 최저 모드 고정 규칙.
- 변경: `SCH-13/14`의 "요청별 재생으로 분포 계산"과 `SCH-16`의 "절대 전체 POOL 비교"(06 문서 ①, ①', ②, ③, ④).

## 위험

- edge CPU(RPi5, LattePanda, RK3588)의 꼬리 지연과 열 throttling은 Clockwork가 GPU에서 보인 예측 가능성만큼
  좋지 않을 수 있다.
- SmolVLM2처럼 입력 단위 수(타일)가 변하면 분포가 혼합 분포가 된다. 프로파일이 데이터셋 표본으로 측정되므로
  혼합 분위수를 쓰면 된다. 재생은 이를 자연히 다루므로 검증기로 남기는 이유이기도 하다.
- 보수 상한이 과대 프로비저닝(에너지 손해)으로 이어질 수 있다.
- 공존 간섭은 지금처럼 관측으로 확인한다(명세가 사전 측정을 요구하지 않음).

## 검증

1. 기존 ~10MB 기록 자체가 입력 전체를 담고 있으므로 그대로 오프라인 A/B 데이터셋으로 쓴다.
   분석형 평가기와 재생이 고른 target의 일치율, 에너지 차이를 본다.
2. E1/E5 부하 단계별로 창마다 예측 p와 관측 p를 비교한다. 기준 예: "≥95% 창에서 예측 ≥ 실측(보수적)이고,
   재생 선택 대비 에너지 손해 ≤5%".
3. 판단 지연 p99와 ChillServer RSS.

## 외부 선례 (링크는 [references](references.md))

- Nexus SOSP'19: `2ℓ(b) ≤ L`로 최대 배치를 정하고 squishy bin-packing(원문 확인).
- Shepherd NSDI'23: 주기적 계획(Herd, ILP)과 온라인 serving(Flex)의 2단 설계(원문 확인).
- Clockwork OSDI'20: 예측 가능한 실행시간, 동작마다 earliest/latest 창(원문 확인).
- Kairos HPDC'23: 온라인 탐색 없이 상한 근사로 이기종 구성 선택(원문 확인).
- INFaaS ATC'21: 오프라인 프로파일 + 온라인 휴리스틱으로 variant와 autoscaling.
- MArk ATC'19: SLO-aware 비용 최소화.
- DynamoLLM HPCA'25: request-type별 pool과 분(노드)~초(주파수) 단위 계층 controller(원문 확인).
- 분석 모델: batch-size 의존 서비스시간 큐 closed form(arXiv 1912.06322), edge 가속기 공유 queueing 모델(UMass TAAS'23).
- AlpaServe OSDI'23: 재생 simulator를 배치 계획에 사용(재생을 오프라인 simulator로 남기는 근거).

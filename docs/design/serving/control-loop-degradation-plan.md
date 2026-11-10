# 국소 사실 하나가 제어 전체를 멈추는 문제 해결 계획

기준 소스: `4b2a25f1`. 근거 루트: `Q=ops/pools-deploy/deployB/control-375-decision-recovery-20261110`, `I=ops/pools-deploy/intent/control-recovery-20261111`.

## 1. 문제 정의

CHILL은 과부하를 관측하면 허용 자원 안에서 서비스별 설정 분위수 SLO를 충족하는 배치를 고르고(충족 후보끼리는 에너지 최소), 그 배치를 제어 주기 10초 안에 실제로 적용해야 한다.

control375에서 탐색 처리율 수정은 동작했다. excess는 genesis2 IC 증설을 선택·적용했고 이후 14분간 IC 실패가 0이었다. 그런데도 두 stage 모두 IC SLO를 충족하지 못했다.

| stage | IC 기한 내 | 실패 시점 | 직접 원인 |
|---|---:|---|---|
| excess | 27,211/28,793 | 1,582건 전부 부하 시작 후 첫 1분 | 증설까지 51초. 허용 tail 1,440건 초과 |
| workload | 43/28,793 | 15분 전체 | 준비된 genesis2 IC가 15분간 route되지 않음 |

**문제:** 제어 루프의 한 국소 사실이 전체 판단이나 적용을 멈춘다. 그리고 반응이 느려 정상 판단도 SLO를 지키지 못한다.

## 2. 근원 원인

### 2.1 적용: Pod-only remove의 오검증이 증설 owner 전체를 실패시킨다 — 확정

1. workload 증설 plan `plan-d97f10757600`은 genesis2 IC prepare와 lattepanda의 종료 Pod-only remove를 함께 실행했다. prepare는 14:40:37에 완료됐고 executor는 Ready였다.
2. remove는 `operation physical instance Node observation changed`로 매번 `partial`이 됐다(실행 전체에서 약 80초마다 반복).
3. `operations.go:331-334`는 Pod-only operation(`Instance=""`)의 빈 ID를 다른 Node의 다른 Pod-only 재고의 빈 ID와 비교한다. Node가 실제로 바뀐 것이 아니라 Pod identity와 executor identity를 혼동한 검증 결함이다.
4. `failTargetLocked`(`scheduler_state_reconcile.go:170-191`)는 필수 action 하나가 complete가 아니면 owner 전체를 실패시킨다. 실패 owner의 continuation은 완료된 stop의 정리만 남겨 route를 버린다(`scheduler_continuation.go:65-75`).
5. 실패 owner는 예약을 유지한다(`:144-149`). 준비된 genesis2 executor도 예약에 묶여, 이후 Tick이 같은 executor를 고른 plan(`plan-681b833ea7ee`, 14:41~)도 실행되지 않았다.

증거: `Q/overload-decision-full.json`의 reservations·actions, `Q/native-control-pages/014.json.gz` offset 3696–3700, action 시계열.

### 2.2 판단: 공유 진행 상태 하나가 모든 후보의 정밀 평가를 무효로 만든다 — 확정

14:49:21 decision에서 정밀 평가 수는 0이었다. keep과 모든 후보가 `RunningElapsedOutsideProfileRange`로 Pending이었다.

- JetsonX의 실행 중 batch2는 195.9ms가 지났다. 잔여 시간은 1-nearest gap 조건부 분포(`service_distribution.go:170-208`)에서 표본을 고른다.
- 이 profile은 gap이 연속값이라 trial마다 nearest 그룹이 표본 1개다. 세 trial 합쳐 3개 표본이 모두 195.9ms보다 짧아 생존 표본이 0이 됐다. 전체 844개 중에는 더 긴 표본이 있다.
- `initializeRunning`(`scheduler_replay.go:571-618`)은 이 경우 replay 전체를 무효로 반환한다. 모든 후보가 같은 실행 중 batch를 공유하므로 판단 전체가 멈춘다.
- 같은 snapshot의 실패한 genesis3 prepare(14:19 실패, 30분 잔존)도 keep replay를 `ActionFailed`로 무효로 만든다(`scheduler_replay_actions.go:142-145`). 실패한 prepare는 Ready 공급을 만들지 않는데 비교 전체를 막는다.

같은 입력 재생으로 재현했다(`I/replay-1449-cold.jsonl`): 정밀 평가 0, v1 fallback, 무변경. B1·B2 방향을 실험으로 적용하면 같은 입력에서 keep이 평가되고(IC 실패율 0.961) production v2 Search가 준비된 genesis2 IC executor를 route하는 배치를 선택했다. IC 0.004, PLC·Smol 0, 전체 SLO 충족(`I/replay-1449-b1b2.jsonl`, 실험 diff `I/experiment-b1b2.diff`).

현재 [runtime measurement](scheduler-runtime-measurement.md) D40 계약에는 선택된 분포에서 `P(S > elapsed) = 0`일 때의 재선택 규칙이 없어 판단이 정지한다. B1에서 이 residual 계약을 변경한다. 위 실험 수치는 다른 알고리즘(전체 기록 marginal) 결과이므로 B1 검증으로 쓰지 않는다.

### 2.3 반응 시간: Tick이 제어 주기의 2배 — 확정(분해는 부분)

excess의 실패는 전부 증설 전 51초에 났다. 부하 시작 14:18:21, 첫 증설 판단 14:18:39, prepare 시작 14:18:51, route 완료 14:19:12다. 판단 하나에 16~18초가 걸려 이 구간에 Tick이 2번만 돌았다.

| 단계(평균 초) | excess | workload |
|---|---:|---:|
| capture | 8.1 | 8.2 |
| 정밀 평가(exact) | 7.1 | 8.0 |
| surrogate | 1.1 | 1.1 |
| payback | 0.7 | 2.6 |
| rates | 1.1 | 1.1 |

- 정밀 평가: v2 `Funnel`은 finalist 16개를 순차 replay한다(`v2/funnel.go:38`). 설정 `planSearchWorkers=3`은 v1만 쓴다.
- capture: 매 Tick 동기 observer refresh가 cluster 전체 Pod List와 Deployment/RS List를 수행하고, serving source Pod에 native probe와 UID 재조회를 수행한다(`observer.go:236-354`). 이어 capture가 Node와 DeviceClass를 다시 List한다(`scheduler_application_capture.go:43-63`). CPU 표본에서 capture CPU의 대부분이 refresh였다. 8초 wall time의 항목별 분해는 계측이 없어 확정하지 못했다.

## 3. 수정 계획

원칙: 국소 사실의 실패는 그 범위에서 처리하고 제어 전체를 멈추지 않는다. 판단 규칙·SLO·예산·설정은 바꾸지 않는다.

| 순위 | 단위 | 변경 | 완료 기준 |
|---|---|---|---|
| 1 | A1 Pod-only 검증 | Pod-only operation은 같은 `PodUID`의 runtime 관측만 Node와 비교한다. executor operation은 기존처럼 exact ID로 비교한다. Pod UID·Node·종료 검증은 유지 | 다른 Node에 Pod-only 재고가 있어도 종료 Pod remove가 complete. 다른 Node로 실제 바뀐 같은 Pod는 계속 거부 |
| 2 | A2 실패 owner의 준비된 공급 | 14:49 snapshot은 Node·Workload 범위가 빈 채 genesis2 Pod/instance 예약만 남았다. 기존 반환 경계(`reservation.go` fresh exact known-effect 반환)가 왜 이 executor를 반환하지 않았는지 먼저 확정한다(`freezePreparingExecutorsLocked`가 반환 뒤 identity를 다시 넣는 순서가 후보). 확정된 경계만 고치며 fresh ACK·terminal/future writer·settled power 조건은 유지 | 형제 prepare가 실패해도 complete된 prepare의 Ready executor가 다음 Tick의 Ready 재고로 반환·route |
| 3 | B1 실행 중 batch 잔여 시간 | D40 계약을 명시적으로 바꾼다. 어떤 trial이든 기존 nearest 그룹에 `S > elapsed` 기록이 있으면 기존 규칙(생존 비율 posterior trial 가중)을 그대로 쓴다. 모든 trial의 nearest 그룹에 생존이 없을 때만, 생존 기록 중 gap이 가장 가까운 기록으로 1-nearest mixture를 만든다(생존 있는 trial 균등). 그래도 생존이 없으면 기존처럼 그 replay만 무효다. 즉시 완료 같은 임의 값은 쓰지 않는다. 선택 기록의 거리·trial 수는 기존 conditional evidence로 남긴다 | 14:49 입력에서 정밀 평가가 진행되고 production Search가 IC 증설 배치를 선택. 새 batch의 service 표본 선택(`Sample`)은 불변 |
| 4 | B2 실패한 진행 action | 실패한 action의 미래 실행 효과를 replay에 추가하지 않는다. 현재 snapshot의 실제 공급·배분·점유는 그대로다. 실패 action은 성공 의존성으로 소비되지 않아 그 action에 의존한 후속 action도 시작하지 않는다 | 같은 입력의 keep 평가가 Pending이 아님. 실패 action에 의존한 action이 replay에서 실행되지 않음 |
| 5 | C1 정밀 평가 병렬화 | v2 `Funnel`과 payback 재평가의 candidate replay를 기존 `planSearchWorkers`로 병렬 실행한다. `Observe`·PostFilter·`Better`는 원래 candidate 순서로 순차 집계하고 worker별 exact slot을 쓴다 | 같은 입력·cold planner에서 선택·diagnostics 동일(postfilter ON/OFF, 동률, 취소). exact 시간 감소 |
| 6 | C2 capture 중복 조회 제거와 분해 | refresh가 이미 읽은 Node·DeviceClass를 capture가 재사용한다. capture 하위 구간(window 읽기, refresh List, source probe, 나머지)을 기존 control 기록에 추가 | 다음 실측에서 capture 지배 항목 확정 |
| 7 | D Completed Pod 정리 | 사용자 요청. 아래 §4 | Succeeded Pod가 증거 소비 뒤 남지 않음 |

A1이 이번 workload 붕괴의 직접 원인이다. B1은 같은 입력에서 판단 정지의 직접 원인이다. A2·B2는 같은 원칙의 형제 경로이며 control375에서 실제로 발생한 사실이다(genesis3 prepare 실패, keep Pending).

**10초 제어 주기는 시스템 복구의 완료 조건이다.** 기존 평균에서 exact를 모두 없애도 capture+surrogate+payback+rates가 11~13초다. C1·C2만으로는 충족을 기대하지 않는다. 실측에서 전체 round와 판단→적용 지연이 10초를 넘으면 C2 분해로 지배 항목을 확정하고 다음 작업 단위에서 그 항목을 고친다. capture의 구조 변경을 분해 근거 없이 하지 않는다.

하지 않는 것: 새 batch의 1-nearest 조건부 모델 변경, 탐색 예산·finalist 수 변경, 측정 유효성 gap·대시보드 계약 오류(이번 SLO 실패와 독립, 다음 단위).

## 4. Completed Pod 정리 (사용자 요청)

최종 snapshot의 Succeeded Pod 29개:

| 집합 | 수 | 원인 | 정리 경계 |
|---|---:|---|---|
| Deployment/ReplicaSet 소유 | 21 | kubelet 종료(디스크 부족 메시지·`TerminationByKubelet`). ReplicaSet은 대체 replica를 만듦 | operator가 CHILL release/component 식별과 실제 controller UID 관계(Deployment→ReplicaSet→Pod)를 확인한 Succeeded Pod만 삭제 |
| evaluation harness Job 소유 | 8 | 과거 Run의 procedure Job. Run 단위 정리가 실행되지 않음 | procedure 결과가 Run 또는 EvaluationStage status에 영속된 뒤 해당 Job의 Succeeded Pod 삭제. Job 기록 유지. 이미 완료된 과거 procedure도 재조정 |
| evidence-store bootstrap Job | (해당 시 생성) | 성공 Job이 bootstrap 완료 기록이라 Job을 유지 | `JobComplete` 소비 뒤 Succeeded Pod만 삭제. Job 기록 유지 |

유지: Running Pod, Failed Pod(로그 보존), 정상 Pod의 Completed init container(Pod phase 기준으로 판정), profiling·transition-cost Job(기존 owner가 receipt 확인 후 정리), serving executor(종료 증명 경로가 직접 삭제), 증거 미소비 Job Pod.

## 5. 검증

- 로컬: A1 다른 Node Pod-only 재고 회귀, A2 형제 실패 회귀, B1·B2 replay 회귀, C1 순차·병렬 결과 동일, D 소유자별 회귀.
- 같은 입력: 14:49:21(`Q/overload-decision-full.json`)과 06:50:51(control374) production Search 전후.
- 독립 리뷰: 시스템 목적·책임 경계·과구현, 메모리·경쟁·연산 비용.
- gate: `make lint`, `make test`, `make arch-check`, `make gopls-check`.

### 로컬 결과

같은 입력의 memo 없는 production Search(`I/fixed-*.jsonl`):

| 입력 | 소스 | 정밀 평가 수 | 선택 | IC 실패율 | Search(exact) 초 |
|---|---|---:|---|---:|---:|
| 14:49:21 workload | `4b2a25f1` | 0 | 무변경(v1 fallback) | — | 16.0 |
| 14:49:21 workload | 통합본 | 16 | 준비된 genesis2 IC executor route, 전체 SLO 충족 | 0.004 | 5.9 (5.0) |
| 06:50:51 control374 | `4b2a25f1` | 16 | genesis2 IC 추가, 전체 SLO 충족 | 0.019 | 9.95 (8.6) |
| 06:50:51 control374 | 통합본 | 16 | 동일 | 0.019 | 6.5 (5.1) |

14:49 keep 평가는 B1·B2 뒤 Pending이 아니다(IC 0.961). 독립 리뷰 2건의 지적(실패 action의 종료 판정 제외, Pod GC 오류의 진행 차단, operator Pod 캐시 범위, 병렬 slot 테스트 결합)을 반영했다. capture 8초는 로컬에서 재현할 수 없어 실측 하위 구간으로 판정한다.

## 6. main 실수용

control375와 같은 E5를 실행하고 각각 판정한다.

1. **SLO:** 두 정책 IC의 offered 전체 결과를 설정 분위수로 판정. excess 첫 1분 실패 수와 증설 반응 시간.
2. **적용:** 증설 owner의 prepare→route 완료, Pod-only remove complete, 형제 실패 시 준비된 executor 재사용.
3. **판단:** Overload decision의 정밀 평가 수(0이 아님), out-of-support 기록.
4. **Tick:** 전체 round·Tick·판단→적용 지연을 10초 기준으로 판정. exact·capture 하위 구간.
5. **정리:** 실행 후 CHILL namespace의 Succeeded Pod 수.

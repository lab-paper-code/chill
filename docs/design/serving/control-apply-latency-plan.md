# 검증한 배치를 그대로·빨리 적용하는 문제 해결 계획

기준 소스: `d1c6de54`. 근거 루트: `Q=ops/pools-deploy/deployB/control-376-control-recovery-20261111`, `I=ops/pools-deploy/intent/control-latency-20261111`.

## 1. 문제 정의

CHILL은 허용 자원 안에서 서비스별 설정 분위수 SLO를 충족하는 배치를 고르고(충족 후보끼리는 에너지 최소), 그 배치를 제어 주기 10초 안에 실제로 적용해야 한다.

control376에서 두 정책 모두 genesis2 IC 증설을 정밀 평가로 고르고 적용했다. 그런데도 IC SLO를 놓쳤다(필요 성공 27,354건 대비 25,747/26,631건).

| 실패 구간 | excess | workload | 직접 원인 |
|---|---:|---:|---|
| 첫 1분 | 1,843 | 1,853 | 부하 시작→증설 route 완료 63/69초 |
| 60–75초 | 110 | 309 | 같은 증설 지연의 꼬리 |
| 180–221초 | 1,093 | 0 | 적용된 route가 검증한 route와 다름 |
| 판단 정지 | 18:39 1회 | 0 | 정밀 평가 0, v1 fallback |

**문제:** 정밀 평가가 SLO를 확인한 배치가 다른 형태로 적용되고, 적용까지 수십 초가 걸린다.

## 2. 근원 원인

### 2.1 적용 routing이 검증 routing과 다르다 — 확정

18:31:36 판단의 target은 rasp5를 빼고 IC를 JetsonX 537 / JetsonO 463으로 나눈다(정밀 평가 실패율 0.004). 실제 적용은 JetsonX 131 / JetsonO 869였다. 처리율 약 22 req/s인 JetsonO에 IC의 87%가 몰려 40초 동안 1,093건이 실패했다. 다음 route-only 판단이 537/463을 그대로 적용하자 실패가 사라졌다.

- `kube/target_scope.go:159-216`: route rebase는 transition이 물리적으로 건드린 Node의 instance만 owner 범위로 본다. 제거되는 rasp5만 delta에 들어가고, 살아남는 JetsonX/O의 목표 weight는 버려진다.
- `scheduler_routing.go:15-57`: 범위 밖 destination은 기존 적용 비율 52:343을 유지한 채 재정규화되어 131:869가 된다. 직전 판단(18:31:02, genesis2 제거)에서도 같은 방식으로 39:450:255 → 52:605:343이 됐다.
- 물리 Node 소유권을 routing weight 변경 권한으로 쓰는 것이 원인이다. dispatcher, credit, stop, ApplyTarget은 받은 weight를 그대로 적용했다.

증거: `Q/route-decision-inputs/{1794335462964034776,1794335496260586862,1794335528202756953}.json`의 `snapshot.routing`·`target.routing`, server final의 10초 단위 Node 분포.

### 2.2 판단 정지: 실행 중 batch가 profile 범위를 넘음 — 확정

18:39:22 JetsonO IC CPU의 batch1이 100.8ms 경과했다. profile batch1 표본 1,426개의 최댓값은 99.97ms라 생존 표본이 하나도 없다. B1의 재선택 규칙으로도 표본이 없어 replay 전체가 무효가 되고, 모든 후보가 같은 실행 중 batch를 공유하므로 판단이 멈췄다. 같은 입력 재생으로 재현했다(`I/replay-1839-cold.jsonl`).

과부하 상태의 실제 service는 profile 측정 조건보다 길어질 수 있다. 관측이 profile 범위를 넘은 것은 그 instance의 남은 시간을 profile로 예측할 수 없다는 사실이다. 이 사실이 다른 모든 후보의 비교를 막을 이유는 없다.

### 2.3 반응 시간 — 확정(분해 완료)

| 구간(초) | excess 첫 증설 | workload 첫 증설 |
|---|---:|---:|
| capture(판단 시각 이전) | 6.9 | 6.5 |
| 판단 시각→prepare 시작(evaluate 포함) | 7.8 | 4.0 |
| prepare | 14.4 | 20.6 |
| prepare 완료→route 시작 | 7.0 | 6.4 |
| route | 13.4 | 7.2 |
| 판단 시각→route 완료 | 42.7 | 38.2 |

- **capture `refreshList` 6.2초:** 매 Tick 6개 List를 직렬로 수행한다. 그중 Pod List는 모든 namespace를 필터 없이 읽는다. CPU의 32~38%가 Kubernetes List 응답 decode였다. `podWatch`가 이미 모든 Pod 변화를 받고 있지만 List를 대체하지 않는다.
- **같은 refresh를 여러 경로가 반복한다:** Tick, 2초 source loop, Continue(전체 capture), route 적용 직후(`operations_routing.go:62-87`)가 각각 전체 refresh를 수행한다. route 13.4초에는 이 refresh가 포함된다.
- **prepare 완료→route 7초:** excess는 진행 중인 Tick의 평가가 끝날 때까지 기다렸다. workload는 Continue가 전체 capture를 다시 한 뒤 출발했다.
- prepare 14~21초는 실제 executor 준비 시간이다. 이번 범위에서 줄이지 않는다.

## 3. 수정 계획

원칙: 정밀 평가가 검증한 결과를 실제로 적용하고, 국소 사실 하나가 판단 전체를 멈추지 않게 한다. SLO·예산·설정·판단 규칙은 바꾸지 않는다.

| 순위 | 단위 | 변경 | 완료 기준 |
|---|---|---|---|
| 1 | R1 route 의도 보존 | rebase에서 routing weight 변경 집합과 writer·정책 소유 집합을 분리한다. 변경 destination은 owner의 frozen `Before.Routing`과 frozen 최종 target routing을 비교해 식별하고, delta 값은 실행 중인 stage의 `selected.Routing`(Ready-subset·bridge 포함)에서 읽는다. 기존 물리·정책 변경 집합과 writer watermark·정책 적용 대상(`owned`)은 그대로다. 다른 owner의 `newer/held/withdrawn` fence와 기존 동시 owner 합성(`reservation_partial_test.go`)을 유지 | 18:31:36 입력에서 rebased route가 537/463. 기존 동시 owner·Ready-subset·bridge 회귀 통과 |
| 2 | S1 범위 밖 실행 중 batch | 생존 표본이 전혀 없으면 남은 시간을 같은 gap 조건 분포에서 새로 뽑은 service 시간으로 둔다(명시적 재시작 대체모델). 실제 남은 시간은 이 표본보다 길거나 짧을 수 있어 SLO·에너지 순위에 양방향 편향이 생기며 보수성을 보장하지 않는다. assumptions와 conditional evidence에 `RunningResidualBeyondSupport`로 instance·batch·elapsed·profile 최대·적용 모델을 남긴다. 해당 batch 하나의 시간에만 영향을 주고 replay 전체를 무효로 하지 않는다. D40 계약을 바꾼다 | 18:39 입력에서 정밀 평가가 진행되고 keep·후보 결과에 가정이 기록됨 |
| 3 | T1 refresh 분해 | `refreshListNanos`를 List 종류별(Nodes, Pods, 자원, DeviceClasses, Services) 시간으로 나눠 기록한다. Continue의 capture 시간도 같은 구간으로 기록한다. route 적용 내부 refresh 시간은 Operations 인터페이스 확장이 필요해 다음 단위로 둔다 | 다음 실측에서 6.2초의 지배 List와 Continue의 refresh 비중 확정 |

R1이 첫 1분 이후 실패의 직접 원인이다. S1은 판단 정지의 직접 원인이다.

**반응 시간과 10초 제어 주기는 시스템 복구 완료 조건이며 이번 단위로 충족하지 않는다.** 독립 검토에서 다음 두 단축안이 안전 계약을 깬다는 것이 확인됐다.
- cluster Pod List를 nonterminal로 줄이면 최초·watch 복구의 terminal executor 발견과 OnCluster terminal 관측이 사라진다.
- route 적용 직전 refresh를 재사용하면 정책 ACK 이후 관측이 아니어서 handoff·effect 판정이 틀어진다.

T1로 List별 시간을 확정한 뒤, 복구 baseline과 steady 관측을 분리하는 구조(예: watch 기반 cache)와 ACK 게시 경로를 다음 단위에서 설계한다. prepare 14~21초도 별도 항목이다.

하지 않는 것: prepare 시간 단축, 탐색 예산·finalist 수 변경, 측정 유효성 gap·대시보드 계약 오류.

## 4. 검증

- 로컬: R1 회귀(물리 변경 transition에서 생존 destination의 목표 weight 적용, 기존 동시 owner 합성 유지), S1 replay 회귀, T1 기록 회귀.
- 같은 입력: 18:31:36 route rebase, 18:39:22 production Search 전후.
- 독립 리뷰: 시스템 목적·책임 경계·과구현, 메모리·경쟁·연산 비용.
- gate: `make lint`, `make test`, `make arch-check`, `make gopls-check`.

## 5. main 실수용

control376과 같은 E5를 실행하고 각각 판정한다.

1. **SLO:** 두 정책 IC offered 전체를 설정 p95로 판정. 구간별 실패(첫 1분, 이후).
2. **적용 일치:** 각 route의 admission된 frozen `Operation.Target`(rebase 결과)과 적용 직후 실제 routing을 비교하고, 동시 owner가 없을 때 decision target과 같은지 확인.
3. **판단:** Overload decision의 `search.evaluated`, `RunningResidualBeyondSupport` 기록.
4. **반응 시간:** 부하 시작→첫 증설 route 완료, prepare 완료→route 시작, route 시간, capture·refresh List별 시간. 전체 round·Tick 10초 판정.

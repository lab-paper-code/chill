# 게이트웨이 성능 퇴행: 원인과 수정 근거

2026-10-26 07:34Z 기준. 사용자 결정은 증설로 가리지 않고 Go pprof의 CPU·할당·mutex·block 자료로 원인을 확정한 뒤 같은 고정 배치로 재측정하는 것이었다. 이 문서는 보존 자료의 증류이며 새 측정을 수행하지 않았다.

## 현상과 비교 조건

시연 3회차 B에서 공정 이상 탐지(PLC)는 전 요청에 성공했지만 클라이언트 왕복 p95 67.09ms로 50ms 목표를 넘었다. MobileNetV2 부하와 함께 ChillServer가 5.8–6코어에 도달했다. 1회차 동일 offered 규모에서는 CPU 약0.6–1.6코어였다. 소스·초기 상태가 달라 시연끼리 인과 효과를 계산하지 않았다.

고정 배치에서는 MobileNetV2 20→100, PLC 40, SmolVLM2 0.3 req/s를 각각 180초씩 보냈다(합계 60.3/140.3). 이전 프로파일의 소스는 rev82, 이후는 rev83이며 자원 상한 6CPU/4Gi를 유지했다. producer별 실제 시작 시계는 각 `context.json`에 고정돼 있다. 진단 실행은 연구 결과에서 제외한다.

| 지표 | 수정 전 낮은 부하 / 높은 부하 | 수정 후 낮은 부하 / 높은 부하 |
|---|---:|---:|
| 10초 표본 CPU 최대(코어, 체크포인트 요약) | 4.71 / 4.72 | 0.47 / 0.63 |
| PLC 성공률 | 100% / 100% | 100% / 100% |
| PLC 왕복 p95 ms | 56.3 / 58.3 | 15.6 / 10.7 |
| charged memory 대표 최대 MiB | 약3,965 | 약2,006 |

CPU는 요청당 CPU가 아니라 cgroup 사용시간 차이를 실제 표본 간격으로 나눈 코어 값이다. `cs-10s.csv`의 `cpuCoresIntervalMean`은 각 10초 구간 평균이므로 위 최대치와 전체 180초 평균을 혼동하지 않는다. 초기 분석의 약4.3/4.5코어는 창 요약으로, 위 4.71/4.72 최대 표본과 다른 집계다. charged memory는 Go live heap 또는 RSS와 같지 않다.

## 확정 원인

장부 cutover에서 observer가 native 인스턴스를 확인할 때마다 namespace의 Pod 전체를 label selector 없이 List했다. 당시 1,053개 중 Succeeded 981개였고 응답이 약24MiB였다. 한 인스턴스 확인에 관계없는 종료 측정 Pod까지 반복 역직렬화했다.

- 프로파일에서 이 경로가 CPU 82.9%/76.1%(약3.5코어), 할당 약95%(약735MiB/s), live heap 약68%를 차지했다.
- 수정 전 위치는 `internal/serving/kube/observer.go:271`이었다. 현재 코드는 대상 Workload UID label selector로 Pod 목록을 제한한다.
- 고정 부하의 작은 차이에도 높은 고정 부담이 유지됐고, selector 수정만 배포한 뒤 같은 창에서 CPU와 PLC 지연이 함께 내려갔다. 이것이 namespace 전체 List를 퇴행 원인으로 확정한 근거다.
- 끝난 측정 Pod 정리는 별도 수명주기 수정이다. selector 수정 효과를 Pod 청소 효과와 혼합하지 않는다.

## 기각하거나 보류한 가설

| 가설 | 관측 / 결정 |
|---|---|
| 자원 장부 `InstallInstances` 자체가 비싸다 | CPU 약0.02%, mutex 약68ms. 주원인 가설 기각 |
| PLC native 모델 실행이 느려졌다 | native elapsed p95 약8ms였으며 client 왕복은 50ms 이상. 모델 실행만으로 설명 불가 |
| accept→dispatch 지연이 지배한다 | 해당 프로파일에서 약7µs. 주된 병목으로 보지 않음 |
| ObservationLog writer 잠금이 원인이다 | assignment 기록 전 writer 대기(high block 누적63초)는 관측됨. 그러나 selector 수정 후 회복했으므로 별도 수정 불필요로 보류 |
| JSON marshal을 writer mutex 밖으로 옮긴다 | Flush/RequestClosed 공유 순서 계약 영향. 근거 없는 동시성 변경을 추가하지 않음 |
| ChillServer를 10CPU/8Gi로 증설한다 | 이전 E5 숫자 초안의 제안은 철회. 현재 자원에서 원인 수정과 회복을 먼저 입증 |

block·mutex 누적값은 프로파일 기간 전체 goroutine 대기 합계이며 단일 요청 지연으로 읽지 않는다. STW 16.8/23ms와 전송 경로 지연은 시연 당시 후보였지만 이번 확정 원인을 대체하지 않는다.

## 남은 후보와 경계

- 정책 ACK 직렬화와 Tick 재진입 대기(3.4–7.3초)는 증설 반응시간 후보이지 PLC 고정 배치 퇴행의 미해결 원인이 아니다. 승인 전 병렬화를 추가하지 않는다.
- ObservationLog 잠금은 동일 조건에서 다시 지배적이라는 프로파일이 있을 때만 재검토한다.
- 정확한 게이트웨이 처리 한계 G는 이 60.3/140.3 진단으로 측정되지 않았다. E5 실제 혼합 부하가 통과하는 범위를 확인하되 극단 한계 탐색을 시연 선행조건으로 만들지 않는다.
- [시연 4회차와 E5 계획](../plans/2026-10-26-remaining-roadmap.md)은 수정된 최종 제어부로 별도 수행한다.

원시 근거: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/perf/20261026T040607Z/` 및 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/perf/20261026T052424Z/`의 `context.json`, `cs-10s.csv`, pprof 자료; 같은 운영 루트 `intent/checkpoints.md` 1967–1995행. 대용량 프로파일·요청 원시·CSV는 옮기지 않았다.

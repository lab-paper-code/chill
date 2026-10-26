# CHILL 인수인계

## 목표

- mobilenet, SmolVLM2 시각 질의응답, PLC 이상 탐지를 같은 서빙 경로로 함께 운영한다.
- 실측 근거로 첫 구성을 만들고 부하에 따라 확장과 축소를 수행하며 워크로드별 서비스 수준 목표(Service Level Objective, SLO)를 지킨다.
- 전환을 포함한 전체 자원 풀의 에너지를 정적 구성과 비교하고 [평가 시나리오 7개](spec/README.md)를 채운다.

## 현재 상태

**기록 기준: 2026-10-26 07:34Z.** 아래는 보존된 관측이며 접속 시점의 실시간 상태를 뜻하지 않는다. 원문에 사용자 시각 표기와 실행 시각이 혼재하므로 세부 이력의 시각을 새 현재 시각으로 옮겨 쓰지 않는다.

| 구분 | 마지막 확인 |
|---|---|
| 저장소 | `/home/genesis1/ETRI/chill`, `main`; workload-pools 통합과 spec 전체 추적 완료, 기준 HEAD `00590c5a`; 원격 push 미실행 |
| 배포 | Helm `chill` rev84, 소스 `07721178`; ChillServer와 측정 컨트롤러 포함 |
| 미배포 | 에너지 목적 배치 탐색(Batch Energy Objective, BEO) 정책 불일치 종결 수정 `1f4a60fb`, `30bafe78`, 대시보드 표시 `26890be2` |
| 제어 자원 | ChillServer 6 CPU / 4 GiB, `GOMEMLIMIT=3072MiB`, `planSearchWorkers=3`; runtime 이미지 7종 고정 |
| 측정 진도 | dispatch 점유 보정값 d 새 규칙 **10/34**, 정확 전환 비용 **69/69**, 평가 **0/7** |
| 시연 | B 고부하 mobilenet 성공률 1회차 63.8%, 2회차 0.67%(중복 준비 사고), 3회차 85.4%; 3회차 A/C 전 워크로드 100% |
| 제어 확인 | 독립 확장 시험 (가)(나) 통과; 두 박자 제어 시험 고부하 mobilenet 96.5%, 축소 허용 약 128초(설정 120초); 30분 장시간 시험 위반 0 |
| 성능 수정 | Pod 조회 범위 수정 후 고정 60/140 req/s에서 서버 CPU 4.7→0.47/0.63코어, PLC p95 56/58→15.6/10.7ms |
| 완료 Pod 정리 | 끝난 Pod만 삭제해 1,049→73; 옛 Job은 증거 저장소 연결 정보 의존 때문에 보존; 표본 5개 증거 읽기 성공 |

시연, 진단, 장시간 시험은 연구 평가 7개의 완료 수에 포함하지 않는다. 정확 비용 69/69는 2026-10-24 소비자 snapshot의 참조 연결 수이며 모든 후보의 통계적 적격성 보장을 뜻하지 않는다. d 10/34에는 SmolVLM2 9개와 PLC 대표 1개가 포함된다. 남은 24개와 극단 부하 한계 검증은 시연 우선 결정으로 보류됐다.

## 운영 규칙

- 클러스터 context는 `kubernetes-admin@kubernetes`, namespace는 `chill-system`, Helm release는 `chill`이다. [hack 운영 안내](../hack/README.md)에 따라 testbed kubeconfig를 명시하고 기본 shell context에 의존하지 않는다. 자격 증명은 승인된 운영 환경에서 인계받는다.
- runtime 7종 digest는 고정한다. 선언과 이미지 참조는 [운영 values](../charts/chill/values-operating.yaml)를 참조한다. 이 파일의 제어 이미지 기준은 rev74이므로 최신 rev84 배포 입력으로 그대로 사용하지 않는다. runtime 변경 시 영향받는 프로파일, d, 전환 비용과 비교 기준을 먼저 제시하고 사용자 승인을 받는다.
- 측정 조건, 계수, 판정, 제어 동작과 승인 범위 밖 배포 변경은 사용자 승인 대상이다. 이미 승인된 동일 조건 재측정과 실패 재시도 최대 1회는 승인 범위 안에서 진행 후 보고한다.
- 실험 조종 코드는 `hack/e5/`, `hack/measure/`에 둔다. 환경값은 설정 또는 인자로 분리한다. 원시 결과, 백업과 민감값은 저장소 밖에 둔다. `CHILL_EXPERIMENT_CONFIG`로 승인된 환경 설정을 지정한다.
- 라이브 변경은 한 담당이 직렬 수행하고 한 파일은 한 담당이 소유한다. 빌드와 검증은 측정 및 부하 창과 겹치지 않게 조율한다. 이 문서 자체는 배포나 측정 개시 승인을 부여하지 않는다.
- d는 정밀도 1%, 양측 95% 계획 근사, 여유 1.25, 유휴 오차 1ms/요청, 본창 360초와 준비 60초 하한, 전체 3600초 상한을 유지한다. signed 원시 값, Accepted 판정과 계획기의 `max(d,0)` 투영을 구분한다. 상세는 [측정 설계](design/serving/measurement-api.md)를 따른다.
- 전력 결측 허용 `tolerateArchive`는 진단 전용이며 E5 설정에서는 거부한다. 원시 결측을 0으로 채우거나 표본 확인을 전체 검증으로 확대하지 않는다.
- 기존 완료 Job은 임의 삭제하지 않는다. 새 측정의 증거 확정 후 삭제 계약과 기존 기록의 보존 경계는 [ADR 0026](adr/0026-finished-measurement-pods.md)을 따른다.

## 다음 할 일

1. rev85에 미배포 BEO 수정과 대시보드 표시를 포함해 배포한다. `orangepi-017-plc-cpu-q4`가 `Failed/EnergyObjectivePolicyMismatch`로 종결되고 원시 근거가 유지되는지 확인한다.
2. PLC orangepi 후보가 사용하는 근거를 확인하고 재측정 필요성을 판단한다. 성공 trial을 임의 제외하거나 상태를 강제 Complete로 바꾸지 않는다.
3. 측정 컨트롤러 이중 실행 여부를 확인한다.
4. 최종 제어부로 첫 구성→확장→축소 시연 4회차를 수행한다. 준비 시간과 전환 중 실패를 숨김 없이 기록한다.
5. E5와 대시보드 라이브 무대의 실행 순서를 사용자와 결정한다. [남은 로드맵](plans/2026-10-26-remaining-roadmap.md)의 승인 경계와 의존성을 따른다.
6. E5 정량 입력 및 개시 승인, 저장소 스크립트 축소 시운전과 정상 정리 뒤 평가 7개를 진행한다. 알려진 편향과 결측을 결과에 공개한다.
7. 통합 이력과 최종 검증 상태를 확인한 뒤 원격 push한다.

## 읽기 순서와 문서 목차

### 목표와 현재 계약

- [의도 문서 안내](intent/README.md), [연구 목표](intent/north-star.md), [사용자 의도와 작업 방식](intent/user-intent.md), [핵심 결정 일지](intent/decision-log.md)
- [근원 명세와 평가](spec/README.md), [스케줄러 SRS](spec/scheduler/SRS.md), [워크로드 SRS](spec/workloads/SRS.md), [대시보드 SRS](spec/dashboard/SRS.md)
- [자원 장부와 실행 설계(§5.4)](design/serving/scheduler-implementation.md), [측정](design/serving/measurement-api.md), [구성 선택](design/serving/analytic-planner.md), [자원 풀과 평가 실행](design/serving/workload-pools.md)

### 결정과 사고 근거

- [ADR 0023: 자원 장부](adr/0023-resource-ledger.md)
- [ADR 0024: 후보 비교와 빈 출발 대기](adr/0024-candidate-comparison-and-bootstrap.md)
- [ADR 0025: 두 박자 제어](adr/0025-two-tempo-control.md)
- [ADR 0026: 끝난 측정 Pod](adr/0026-finished-measurement-pods.md)
- [ADR 0027: 수동 작업 허가](adr/0027-manual-operation-admission.md)
- [사고 0002: 시연 2회차 중복 준비](postmortems/0002-demo2-duplicate-preparation.md)
- [사고 0003: rev76 빈 출발 정체](postmortems/0003-rev76-bootstrap-starvation.md)
- [사고 0004: 전체 Pod 조회 성능 퇴행](postmortems/0004-gateway-pod-list-regression.md)
- [사고 0005: 측정 캠페인 정체](postmortems/0005-measurement-campaign-stuck.md)

### 결과와 계획

- [시연 결과](reports/demos/README.md), [구조 검토](reports/structure-review-20261026.md), [게이트웨이 성능](reports/gateway-performance-20261026.md)
- [남은 로드맵](plans/2026-10-26-remaining-roadmap.md), [대시보드 라이브 무대](plans/2026-10-26-dashboard-live-stage.md), [E5 평가](plans/2026-10-26-e5-evaluation.md)

원시 근거 위치: 운영 호스트 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/`의 `intent/`, `audit-20261024/`, `campaign/v2/`, `perf/`, `deployB/`. 대용량 원문과 실행 로그는 이 위치에 보존하며, 프로젝트 재개의 필수 읽기 경로는 위 저장소 문서다.

# 핵심 결정 일지

결정의 결론과 근거만 시간순으로 보존한다. 현재 계약의 정본은 spec, SRS와 설계다. 과거 승인 범위는 이후 사용자 결정으로 대체될 수 있다.
원문은 사용자 시각 표기와 실제 실행 시각이 혼재한다. 날짜가 같은 항목은 원문의 사건 순서를 따르며 실행 시각을 재구성하지 않는다.
2026-10-23보다 앞선 결정의 정확한 날짜는 확인되지 않아 별도 표시했다. 날짜를 추정해 붙이지 않는다.

## 2026-10-27: PMTC 에너지 비용 창에 선언 settling 포함

- 결정: `PowerModeTransitionCost.spec.settlingSeconds`는 기본 60초, 범위 0..3600초다. 에너지 비용 창은 apply-start → fresh Ready + 선언 settling이며 적분 J와 목표 baseline W × 창 길이를 같은 시간 범위로 계산한다. 실제 전환 `tau`는 apply-start → fresh Ready로 유지하고 선언 settling을 frozen contract와 방향별 결과에 별도 기록한다.
- 근거: NVIDIA 전환 측정 15/15가 `PowerWindowInsufficient`(coverage 13회, 표본 부족 2회)다. 1초 cadence·0.95 coverage의 짧은 창을 완화하지 않고, [빈 Node 60초 settling 선례](../design/serving/scheduler-operations.md)를 재사용해 안정화 관측까지의 비용으로 계약을 명시한다. 60초는 물리 안정화 시간의 실측값이 아니다.
- 유지: 기존 raw/finalized와 판정은 불변이다. 새 시도는 settling 동안 power/native/generation을 계속 관측한다. 기본 coverage·boundary·gap 정책과 runtime-7 serving 이미지는 바꾸지 않는다. 새 CR과 일치하는 Profiler/transition-observer control 이미지가 필요하며 이 구현 자체는 새 클러스터 실행 승인이 아니다.
- 연결: [Plan 10](../design/profiling/plans/10-transition-cost-measurement.md), [CampaignOps NVIDIA CR](../../config/samples/profiling/edge_v1alpha1_powermodetransitioncost_nvidia.yaml).

## 2026-10-27: d 공급 깊이의 관측 최대 기반 권고값

- 결정: 새 closed-loop 공급의 수명 상한 분모를 평균에서 같은 batch의 frozen profile 관측 최대 `maxObservedSeconds`로 바꾼다. `K=min(64, bytes cap, floor((L−delivery)/(B×mu_tail)))`이며 출처 `profile.maxObservedSeconds`와 값을 동결한다. 최대값이 없으면 거절하고 평균 배수로 대체하지 않는다.
- 근거: D34의 평균 기반 K52는 L1초에서 서비스 합974ms로 여유26ms뿐이다. 관측 최대37.825158ms를 쓰면 권고 K26이다. 이 값은 관측 기반 계획이지 미래 무실패 보증이 아니다.
- 유지: 기존 L·delivery·3600초 예산·native count/bytes·수용 기준·Accepted 원시는 바꾸지 않는다. 새 측정/클러스터 실행 승인과 별개다.
- 연결: [측정 명세](../spec/00_measurement.md#디스패치-점유-d의-공급-조건), [측정 설계](../design/serving/measurement-api.md).

## 2026-10-26: 운영 전력 gap 정책 재평가

- 결정: 운영 E5 에너지 집계의 MaximumGap을 2.0 s에서 3.0 s로 바꾼다. 읽기 실패 한 건 자체는 불완전 사유로 두지 않고 기록된 coverage 정책으로 판정한다. MinimumCoverage 0.75와 경계 거리 1.0 s 및 정확 비용 등 다른 측정 정책은 유지한다.
- 근거: 단발 실패의 실측 공백은 ≤2.52 s이며 연속 두 번 실패 공백은 ≥3.2 s다. 2.0 s는 일부 단발 실패까지 제외한다.
- 재현: 원본 raw와 기록 정책은 보존하며 오프라인 `--maximum-gap 3s` 재평가는 `policyOverride`를 표시한다. 새 E5 binding은 운영 powerObservation을 동결하고 실행 전 실제 ConfigMap과 대조한다.
- 연결: [측정 명세](../spec/00_measurement.md), [측정 설계 §2.1](../design/serving/measurement-api.md#21-전력-곡선과-민감도).

## 2026-10-23 이전: 이어받은 핵심 방향

### 공통 서빙과 근거 기반 자동제어

- 결정: 이질 워크로드는 원시 요청부터 응답까지 같은 경로를 사용한다. 측정 근거가 있는 후보에서 첫 구성을 자동 생성한다.
- 근거: 특정 모델 전용 경로와 수동 첫 구성은 일반적인 자동제어라는 연구 주장에 맞지 않는다.
- 연결: [연구 목표](north-star.md), [근원 명세](../spec/README.md).

### 워크로드별 자원 풀과 전환 회계

- 결정: 워크로드별 인스턴스와 자원 풀을 사용하고 전환 비용을 전체 풀 에너지에 포함한다. 인스턴스 공유는 연구 범위 밖에 둔다.
- 근거: 구성 선택과 실제 실행의 자원 및 에너지 경계를 일치시켜야 비교할 수 있다.
- 연결: [자원 풀 설계](../design/serving/workload-pools.md), [구성 선택 명세](../spec/04_configuration_selection.md).

## 2026-10-23: 측정 일반화와 실제 서빙 우선

### d를 실제 물리 배치로 판정

- 결정: dispatch 점유 보정값 d는 `1/X − 프로파일 평균 실행시간/B`로 유지한다. X와 포화 여부는 실제 실행된 물리 batch로 계산한다.
- 근거: HTTP 성공 행만 세면 만료된 요청의 실제 실행을 누락한다. 느린 모델에서는 잘못된 처리율과 유휴 판정으로 이어진다.
- 연결: [측정 계약](../design/serving/measurement-api.md), [측정 명세](../spec/00_measurement.md).

### 관측 불충분은 불충분으로 유지

- 결정: BatchID와 실행 시각의 일치를 확인하고 창 경계와 오류 주변 유휴까지 포함한다. 관측 없음이 판정에 영향을 주면 Insufficient로 둔다.
- 근거: ingress를 포함한 queueMS는 물리 큐 포화의 직접 근거가 아니다. 실제 actor 대기와 배치 연속성이 필요하다.
- 연결: [측정 계약](../design/serving/measurement-api.md).

### 대상 실측에서 측정 조건 계산

- 결정: 표본 수, 창, 요청 수명, 공급과 유휴 허용치를 프로파일 분포와 서비스 수준 목표(Service Level Objective, SLO)에서 계산한다. 계수와 계산 결과 및 입력 ID를 기록한다.
- 근거: 수작업 SmolVLM2 실행시간 10초 추정은 실제 약 15.9초/batch와 달랐다. 같은 고정값 문제가 준비 기한과 비용 관측에도 반복됐다.
- 연결: [측정 계약](../design/serving/measurement-api.md), [사용자 의도](user-intent.md).

### 폐회로 공급과 요청 수명

- 결정: d는 고정 동시성 공급으로 실행과 대기 batch를 유지한다. 요청 수명은 워크로드 SLO 이상으로 맞추고 공급 가능성을 확인한다.
- 근거: 느린 모델에 30초 수명을 강제하면 측정과 서빙 모두 실제 목표 안의 요청을 버릴 수 있다. 공급 부족을 모델 처리 한계로 오인하면 안 된다.
- 연결: [측정 계약](../design/serving/measurement-api.md), [워크로드 SRS](../spec/workloads/SRS.md).

### 측정 일반화는 하나의 승인 묶음

- 결정: d, 비용, 전력계 갱신, 수명과 기한의 변경을 함께 조사하고 runtime digest 영향 및 재측정 범위를 제시한다.
- 근거: 개별 고정값만 수리하면 다음 모델에서 같은 원인이 반복되고 배포 공백과 재측정이 늘어난다.
- 연결: [측정 설계](../design/serving/measurement-api.md).

### 정적 서빙 관통 시험 우선

- 결정: 세 워크로드의 정적 배포 서빙을 먼저 확인하고 E5 필수 측정부터 수집한다. 전력 모드 확장과 새 최적화는 뒤로 둔다.
- 근거: 에너지 목적 배치 탐색(Batch Energy Objective, BEO) 통과는 실제 서빙 증명이 아니다. 느린 모델 준비 제한, PLC 진행 기록 유실과 약한 testbed가 드러났다.
- 연결: [평가 계획](../plans/2026-10-26-e5-evaluation.md), [연구 목표](north-star.md).

## 2026-10-23~24: 계수 확정과 근거 해석

### d 승인값과 초기 제안의 대체

- 결정: 처리율 정밀도 1%, 양측 95% 계획 근사, 공통 여유 1.25, 유휴 오차 1ms/요청, 본창 360초와 준비 60초 하한, 상한 3600초를 유지한다.
- 근거: 초기 제안의 1800초 예산과 60초 본창은 최종 승인값으로 대체됐다. 95%는 표본 계획 근사이며 전체 d 오차의 보장이 아니다.
- 연결: [측정 계약](../design/serving/measurement-api.md), [인수인계 운영 규칙](../handoff.md).

### 원시 d와 정책 투영 분리

- 결정: signed 원시 d와 기존 Accepted 판정을 유지하고 계획기에는 `max(d,0)`을 투영한다. 새 신뢰구간으로 과거 상태를 다시 쓰지 않는다.
- 근거: SmolVLM2의 D04/D09/D14/D23/D33은 프로파일 포함 95% 근사구간이 0 아래였다. 이는 알려진 프로파일-서빙 체계적 차이로 공개한다.
- 연결: [E5 계획](../plans/2026-10-26-e5-evaluation.md), [측정 계약](../design/serving/measurement-api.md).

### 최소 에너지 이득은 B-only

- 결정: 측정 위상과 결측 민감도 경계 B를 사용하고 추가 profile 신뢰구간 장벽은 제거한다. nominal 값, 같은 근거의 상쇄, 전환 chain과 불가능한 구성에서의 회복을 유지한다.
- 근거: 추가 profile 장벽은 공유 추정원과 공분산 및 실제 전환 노출량에 정합되지 않았다. B-only도 모든 통계오차와 전역 95%를 보장하지 않는다.
- 연결: [구성 선택](../design/serving/analytic-planner.md), [E5 계획](../plans/2026-10-26-e5-evaluation.md).

## 2026-10-24~25: 배포2와 평가 승인 범위

### 배포2 제안의 방향 승인과 정량 승인 분리

- 결정: 여섯 실행 권고와 E5의 워크로드 재배치, 의미 있는 제어요소 비교, seed 101/202/303 방향을 승인한다. 정량 부하, SLO, 용량과 개시 승인은 실측 후 별도로 결정한다.
- 근거: 측정 가능한 GPU 노드도 CPU 후보를 유지한다. genesis의 SmolVLM2 CUDA 메모리 부족은 제외 사유이며 GPU 채택을 강제하지 않는다.
- 연결: [평가 계획](../plans/2026-10-26-e5-evaluation.md), [자원 풀 설계](../design/serving/workload-pools.md).

### 측정 진도와 소비자 연결을 구분

- 결정: 새 규칙 d, 정확 비용 연결, 평가 7개를 서로 다른 분모로 보고한다. legacy 소비를 새 규칙 인증으로 세지 않는다.
- 근거: 비용 69/69는 정확한 prepare/remove 참조 연결이다. d 적격성, 자원 가능성과 평가 완료를 함께 증명하지 않는다.
- 연결: [인수인계](../handoff.md), [E5 계획](../plans/2026-10-26-e5-evaluation.md).

### 최소 범위 재측정과 상시 승인

- 결정: 승인된 동일 조건의 legacy 및 결측 대체, 실패 재시도 최대 1회는 재승인 없이 진행한다. 대체 소비 확인과 검증된 백업 뒤 승인 범위의 옛 기록만 정리한다.
- 근거: 매 단계 재승인 대기가 평가 경로를 막았다. 새 조건, 판정과 범위 확대의 사용자 승인 경계는 유지한다.
- 연결: [사용자 작업 방식](user-intent.md).

### 실험 조종 코드의 저장소 관리

- 결정: 실험 Python은 `hack/e5/`와 `hack/measure/`로 옮기고 환경값을 설정이나 인자로 분리한다. 원시 결과와 백업 및 민감값은 외부에 둔다.
- 근거: 연구 결과를 만든 실행 코드가 main과 재현 경로에 포함돼야 한다. 코드 이관과 실제 축소 시운전 완료는 구분한다.
- 연결: [인수인계](../handoff.md), [E5 계획](../plans/2026-10-26-e5-evaluation.md).

## 2026-10-25: 자율 출발과 시연 우선 전환

### 실제 실패 분석과 전송 계층 정리

- 결정: 전송 계층 edgemesh는 고정 이미지와 승인 자원으로 한 대씩 교체한다. 실제 자율제어 실패는 기존 원시의 시간, 전환 구간과 오류 발생 구성요소로 분석한다.
- 근거: 이전 자율 진단의 실패가 높았고 임시 이미지와 초기화 전 확인 문제가 있었다. 표본 통신 성공을 10대 전체 성공으로 확대했던 보고는 정정됐다.
- 연결: [사용자 의도](user-intent.md), [시연 보고](../reports/demos/README.md).

### 최근 수요와 작업별 축소 보호

- 결정: 최근 유입을 반영하고 증가 중 작업별 축소를 보호하며 사용하지 않는 빈 노드의 절전 대기와 요청 배분을 분리한다.
- 근거: 초기값과 오래된 관측창 및 무관한 느린 작업이 출발과 회복을 지연시켰다. 진단은 연구 결과에서 제외한다.
- 연결: [스케줄러 SRS](../spec/scheduler/SRS.md), [실행 설계](../design/serving/scheduler-implementation.md).

### 잔존 빈 분배 선언 정리

- 결정: 마지막 소유 워크로드 키를 제거하고 선언을 명시적으로 비운다. 설정 요청의 필드 생략은 보존으로 취급한다.
- 근거: 남은 `staticRouting: {UID: {}}`가 전역 배분 제어를 유지해 PLC D06 측정의 Pod seed가 무시됐다. 동일 조건 재시도는 Accepted와 실제 소비 및 반환까지 확인됐다.
- 연결: [실행 설계](../design/serving/scheduler-implementation.md), [측정 계약](../design/serving/measurement-api.md).

### 극단 검증 중단과 시연 우선

- 결정: 첫 구성→확장→축소의 종단 간 시연을 최우선으로 한다. 남은 d 24개, 게이트웨이 한계 G 계단과 통계적 완결성 추구는 보류한다.
- 근거: PLC 병렬 측정 합계 약 2,055 req/s에서 서버 CPU와 메모리가 포화됐다. 모든 극단 조건을 채우는 일보다 연구 동작의 실증이 우선이다.
- 연결: [연구 목표](north-star.md), [남은 로드맵](../plans/2026-10-26-remaining-roadmap.md).

### 최소 수요와 준비된 부분집합 배분

- 결정: 운영자가 워크로드별 최소 수요를 명시하고 준비된 인스턴스부터 배분한다. 모든 느린 작업에 일반적으로 적용한다.
- 근거: 첫 시연의 B mobilenet 성공률은 63.8%였고 먼저 준비된 용량이 느린 전력 전환을 기다렸다. 무부하의 첫 구성도 보완할 필요가 있었다.
- 연결: [시연 보고](../reports/demos/README.md), [스케줄러 SRS](../spec/scheduler/SRS.md).

### 대시보드와 문서 선행 순서

- 결정: 최소 수요는 scheduler 설정 맵, 기록 재생은 근거 저장소 조회, 라이브는 WebSocket 첫 메시지 인증으로 한다. spec→SRS→설계→구현 순서를 지킨다.
- 근거: 로컬 보존 24시간 연장은 시연 2회차의 임시 조치다. 장기 재생과 실시간 전달 계약을 분리해야 한다.
- 연결: [대시보드 계획](../plans/2026-10-26-dashboard-live-stage.md), [사용자 작업 방식](user-intent.md).

## 2026-10-26: 사고에서 구조 수정으로

### 시연 2회차 사고의 원인 확정

- 결정: 정책 응답 대기를 오류와 구분하고 마지막 확인 상태를 유지한다. 관측 실패를 실물 부재로 해석하지 않는다.
- 근거: 배분 변경의 정책 응답 대기→snapshot 소실→같은 자리와 CPU 중복 준비→CPU 독점 전역 검증으로 모든 판단 정지, B mobilenet 0.67%였다.
- 연결: [사고 0002](../postmortems/0002-demo2-duplicate-preparation.md), [구조 검토](../reports/structure-review-20261026.md).

### 시연 3회차 전 자원 장부 채택

- 결정: 개별 땜질에 머물지 않고 Pod 기준 실물 장부, 작업별 영향 범위 예약, 예약 범위만 고정하는 구조를 채택한다. 전역 완료 장벽을 제거한다.
- 근거: 약 25건을 앎의 상태, 쓰기와 권위, 실행 소유와 승인, 장애 격리, 제어 동역학의 다섯 묶음으로 검토했다. 실물과 관측 및 실행 권한의 경계를 바로잡아야 했다.
- 연결: [ADR 0023](../adr/0023-resource-ledger.md), [구조 검토](../reports/structure-review-20261026.md).

### 구조 수정의 범위와 수용 시험

- 결정: 기존 서비스와 저장소 및 Kubernetes 리소스 경계를 유지한다. 정식 리뷰와 ponytail 리뷰 뒤 단독 확장과 타 워크로드 전환 중 확장의 두 시험을 수행한다.
- 근거: 둘 다 실제 배치가 증가해야 통과다. 새 서비스, 저장소나 사용자 정의 리소스 추가는 승인 범위가 아니다.
- 연결: [ADR 0023](../adr/0023-resource-ledger.md), [시연 보고](../reports/demos/README.md).

### rev76 후보 비교와 빈 출발 정보 대기

- 결정: 워크로드별 초과량을 나쁜 순서로 비교하는 사전식 최소최대(leximin)와 빈 출발의 전체 후보 용량 대기를 채택한다. `bootstrapCapacityWaitNanos` 기본 10분, 범위 0~1시간이다.
- 근거: 최악 초과량 하나만 보면 한 워크로드가 불가능한 순간 전 후보가 ∞ 동점이 되고 에너지로 무동작을 골랐다. 장벽 제거는 정보 수집 전 미확정 역할도 없앴다.
- 연결: [ADR 0024](../adr/0024-candidate-comparison-and-bootstrap.md), [사고 0003](../postmortems/0003-rev76-bootstrap-starvation.md).

### 대기 워크로드의 노드 선점은 알려진 한계

- 결정: 용량 대기 워크로드가 쓸 노드의 선점을 금지하는 별도 안은 구현하지 않고 한계로 기록한다.
- 근거: 전체 후보 공동 배치와 설정된 대기 상한이 채택 범위다. 무한 대기나 모델별 특수 처리를 추가하지 않는다.
- 연결: [ADR 0024](../adr/0024-candidate-comparison-and-bootstrap.md).

### 장부 관측과 퇴역 실행기 수명 정리

- 결정: refresh 시작의 전역 Ready 초기화를 제거하고 probe 결과에 따라 게시한다. 늦은 probe는 퇴역 실행기를 다시 생성하거나 게시하지 않는다.
- 근거: 중간 false 상태가 정상 인스턴스를 고정했고 제거된 Pod source의 재생성은 정리를 막았다. 독립 확장 시험 두 종류는 수정 뒤 통과했다.
- 연결: [ADR 0023](../adr/0023-resource-ledger.md), [시연 보고](../reports/demos/README.md).

### 두 박자 제어 채택

- 결정: 증가 배분과 확장은 빠르게, 축소와 에너지 교체는 같은 안정화 기간 뒤 수행한다. 기본 300초, 시연 120초이며 전환 예비 용량 비율 기본은 0이다.
- 근거: 수요 회복과 절전을 같은 속도로 제어하면 느린 전환 중 왕복과 용량 손실이 생긴다. 모든 수치는 설정, 범위 검사와 실행 기록으로 고정한다.
- 연결: [ADR 0025](../adr/0025-two-tempo-control.md).

### 하강 최대 수요 창과 빈 Node Fixed 본전 통일

- 결정(2026-10-26): SCH-56은 Kubernetes HPA scale-down stabilization window 방식으로 재정의한다. 기존 `scaleDownStabilizationNanos` 동안 관측한 UID별 유효 수요의 최대값을 하강 판정 수요로 사용하고, 완전한 관측 창 및 현재 실제 가용 공급보다 낮은 수요를 요구한다. 후보도 그 수요를 감당한다. 기준률·`downSince`·`globalSince`를 폐기하며 Workload 축소와 빈 Node 새 절전은 같은 창을 사용한다. 빈 Node는 모든 UID의 최대 수요≤공급 및 하나 이상의 엄격한 여유를 요구하고 UID 간 합계 상쇄는 금지한다. 새 단일값·별도 절전 설정은 없다.
- 본전(SCH-59): 빈 Node Fixed 예외를 삭제하여 기존 일반 교체의 동일 H 안 양수 이득·minimumGain·payback을 그대로 적용한다. 별도 계산기를 추가하거나 keep에도 절전을 선반영하여 비용을 소거하지 않는다. H=600초의 jetsono MAXN→15W 이득 −699J·손익분기 약 52분은 미실행이다.
- 근거: 4회차 B 일정 100 req/s에서 09:00:49에 시작된 기준률 timer가 실제 하강 39초 만에 축소를 허용했다. 국소 고점 미만 유지는 지속 하강의 증거가 아니다. `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/demo-20261027-4/report.md` §C, `DemoPowerDown27`·`TempoTestCause27` 조사에 따른 사용자 결정이다.
- 기록/수용: `decision.capacity.workloads[].scaleDown`을 timer 필드에서 창·최대 수요·현재 공급·완전성·허가/사유로 교체한다. 일정 부하+잡음에서 축소·절전 없음, 실제 하강 뒤 설정 기간 경과 시 적격 후보 허용, 손해 절전 미실행, 빈 출발/증설 불변의 4건을 회귀 수용 조건으로 둔다. 이 항목은 문서 결정이며 구현·검증 완료가 아니다.
- 연결: [SRS SCH-56·59 / AC-42·44](../spec/scheduler/SRS.md), [계획기 §15](../design/serving/analytic-planner.md#15-두-박자-후보-허가와-고정-h--sch-5558), [구현 설계](../design/serving/scheduler-implementation.md), [ADR 0025](../adr/0025-two-tempo-control.md).

### 에너지 비교 기간 600초 고정

- 결정: `comparisonPeriodNanos`는 후보와 무관한 600초로 둔다. 후보별 전환 길이에 따라 비교 기간을 늘리던 방식은 대체됐다.
- 근거: Jetson 전력 전환 약 150초, SmolVLM2 준비 270~290초, 보수적 직렬 조합 약 490초다. 같은 기간에서 후보 에너지를 비교해야 한다.
- 연결: [ADR 0025](../adr/0025-two-tempo-control.md).

### 게이트웨이 성능은 증설 전에 원인 확정

- 결정: pprof CPU/할당/잠금/대기 자료와 고정 배치 창으로 퇴행 원인을 확정하고 수정한다. 서버 증설은 해법으로 채택하지 않는다.
- 근거: 장부 도입 뒤 CPU가 급증했고 PLC 지연이 목표를 넘었다. probe마다 namespace 전체 1,053 Pod(완료 981)를 조회한 것이 주원인이었다.
- 연결: [사고 0004](../postmortems/0004-gateway-pod-list-regression.md), [성능 보고](../reports/gateway-performance-20261026.md).

### UID selector로 조회 범위 제한

- 결정: 인스턴스 UID label selector로 조회한다. ObservationLog writer 잠금 수정은 필요성이 사라져 보류한다.
- 근거: 고정 60/140 req/s에서 서버 CPU 4.7→0.47/0.63코어, PLC p95 56/58→15.6/10.7ms로 회복됐다. 장부 설치 잠금 주원인 가설은 기각됐다.
- 연결: [성능 보고](../reports/gateway-performance-20261026.md).

### 끝난 측정 Job과 Pod의 수명 분리

- 결정: 증거 검증 완료 Run은 Job 없어도 완료로 유지한다. 새 측정은 증거 확정 직후 Job을 Background 삭제하고 실패는 기본 86400초 보존한다. 생성 시 TTL은 두지 않는다.
- 근거: 증거 확정 전 Job 삭제는 근거 유실 위험이 있다. 새 기록에는 증거 저장소 binding과 정리 상태를 남겨 Job 의존을 제거한다.
- 연결: [ADR 0026](../adr/0026-finished-measurement-pods.md).

### 기존 Job은 보존하고 완료 Pod만 삭제

- 결정: 기존 성공 Job 1,012개 삭제를 보류하고 완료 Pod만 삭제한다.
- 근거: 옛 retained loader가 Job에서 증거 저장소 연결 정보를 읽는다. Pod는 1,049→73으로 줄었고 표본 5개의 증거 읽기는 성공했다. 이 표본 결과를 전수 검증으로 확대하지 않는다.
- 연결: [ADR 0026](../adr/0026-finished-measurement-pods.md).

### 수동 작업은 허가 직전 현재 상태 기준

- 결정: 마지막 Tick snapshot 대신 허가 직전 신선한 상태로 수동 작업을 판정한다.
- 근거: 직전 작업이 바꾼 분배가 snapshot에 반영되지 않아 실제 두 건의 잘못된 거절이 있었다. 실패한 수동 작업의 예약도 현재 효과 확인과 소유 범위에 맞춰 처리한다.
- 연결: [ADR 0027](../adr/0027-manual-operation-admission.md).

### 측정 정책 불일치는 명시적 실패

- 결정: orangepi-017의 BEO 정책 불일치는 `Failed/EnergyObjectivePolicyMismatch`로 종결한다. 임의 trial 제외와 강제 Complete는 금지한다.
- 근거: 정책은 배치당 trial 3개인데 성공 4개가 있어 집계 오류가 반복됐다. 사후 선택은 선택 편향을 만든다. 네 번째 trial 생성 경위는 로그 부재로 미확정이다.
- 연결: [사고 0005](../postmortems/0005-measurement-campaign-stuck.md), [인수인계](../handoff.md).

### 결측 허용은 진단 전용

- 결정: `tolerateArchive`는 진단에 한정하고 E5 설정에서 거부한다.
- 근거: 두 박자 검증의 전력 결측을 진단 기록으로 보존할 수는 있지만 완전한 전체 풀 에너지 평가로 해석할 수 없다.
- 연결: [E5 계획](../plans/2026-10-26-e5-evaluation.md).

### main 통합과 다음 실행 순서

- 결정: workload-pools를 main으로 fast-forward하고 spec 전체를 추적한다. 이후 작업은 main 저장소에서 진행한다. 원격 push는 아직 수행하지 않았다.
- 근거: 저장소만으로 프로젝트를 이어갈 수 있어야 하며 spec 공개는 사용자 승인 사항이다. 옛 main 보존 및 로컬 비공개 spec 지시는 대체됐다.
- 연결: [인수인계](../handoff.md), [남은 로드맵](../plans/2026-10-26-remaining-roadmap.md).

- 다음: rev85 배포→orangepi-017 Failed 확인→PLC orangepi 근거와 재측정 판단→컨트롤러 이중 실행 확인→시연 4회차→E5/대시보드 순서 결정→평가 7개→원격 push.
- 현재 기준: d 새 규칙 10/34, 정확 비용 69/69, 평가 0/7. 30분 장시간 시험 위반 0과 두 박자 검증 통과는 평가 완료 수와 분리한다.

## 원시 근거 위치

`/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/`의 `checkpoints.md`(주로 1581행 이후), `d-measurement-decision.md`, `d-condition-proposal.md`, `measurement-generalization-brief.md`, `deployment2-e5-proposal-20261024.md`, `structure-redesign-20261026.md`, `two-tempo-questions-20261026.md`, `workflow-plan-20261025.md`. 상세 수치의 원시는 같은 상위 디렉터리의 `audit-20261024/`, `campaign/v2/`, `perf/`, `deployB/`에 보존한다.

## 2026-10-26 15:3xZ — E5 숫자 1~5 승인, d 24 측정 선행

- MNV p95 1s 유지; PLC/Smol은 p95안 비교(후보 PLC≈32ms, Smol≈7.03s; κ 재취득 후 확정). 공통 m=29ms 임시 유지(실측 residual p95 최대 15.46ms, p99 195ms 후보는 채택 안 함). L=MNV2s/PLC1s/Smol75s, delivery 10ms, socket L+20ms, cap 129s 유지. 정적 기준선=E5-3 끝 0.9R 실제 구성. 제어요소 N=2 후보(membership-off, replicas-off).
- 완전 R 미확정(PLC κ>0 1/8, Smol 6/8, MNV 7/8 legacy)이므로 E5 전에 d 24 측정: PLC 11 → MNV GPU 5 → MNV CPU 8(옛 규칙 값 사용 중; 시간 부족 시 결과에 '옛 규칙' 명시). 시작 전 조건표·예상 시간·창 경계 수정(a74a595) 범위 보고, 묶음마다 preflight.
- 근거: `O/intent/e5-numbers-20261027.md`, docs/plans/2026-10-26-e5-evaluation.md.

## 2026-10-26 15:4xZ — d 24 측정 A안 승인

- 기존 승인 조건, 전역 직렬 1개, 새 규칙. B(workers 변경) 하지 않음. 순서: 예산 수정(a74a595 후속) → 리뷰·검사 → ChillServer 배포 → 묶음① PLC 11 → 보고 → 묶음② MNV GPU 5 → 보고하고 멈춤; 묶음③ MNV CPU 8은 ②까지의 실제 소요·실패율 보고 후 결정(C안 유지). 묶음마다 preflight; 불충분이면 조건 확대 없이 중단·보고. 근거 `O/intent/d-campaign-proposal-20261027.md`.

## 2026-10-26 16:5xZ — d 닫힌 고리 공급 깊이 공식

- D05(genesis2 PLC CUDA) Insufficient(유휴 5.54%, 실측 460/s vs profile 668/s, K=22): 닫힌 고리 포화 조건(리틀의 법칙) 미충족 — 한 바퀴 ≈ K/X = 48ms → 668/s 포화에 평균 32개 필요.
- 결정: `K = max(기존 K, ceil(profile 처리율 × 한 바퀴 시간 p99 × 1.25))`. 한 바퀴 시간은 기존 측정 원시(생산자 왕복)에서 대상별 산출, 없으면 0.25s. 조건 `K×B×서비스시간 + 한 바퀴 ≤ L`. 유휴 1%·d 정의 불변. 실행 전 34개 대입표(Accepted 10개 K 불변 확인)·묶음① 새 K/L·시간 보고 후 ① 재개.

## 2026-10-26 17:3xZ — 공급 깊이 규칙 단순화(이전 공식 폐기)

- 목적은 '측정 중 서버가 쉬지 않음'. 줄이 길면 대기만 늘고 d(서버 쪽 시간)는 불변. **K = min(큐 상한 64, floor((L − 여유)/(B × profile 서비스 시간)))** — 최대로 세움, 측정 불필요. Z/p99/한 바퀴 산출 코드 삭제.
- 포화 판정(유휴 1%)이 측정 조건, K는 수단 → Accepted 10과 K가 달라도 무관. 유휴 >1%면 생산자/경로 한계로 보고(추가 조정 없음). PLC 11(D05·D10 포함) 재산출 후 바로 진행.

## 2026-10-26 20:0xZ — d 판정 기준 재설정(유휴 비율 1% → 식별된 유휴 ≤ 5% 설정값)

- 근거: d = 1/처리량 − 계산시간/B → 측정 중 식별된 유휴는 d를 키우는 쪽(κ 작게, SLO 안전 쪽)으로만 오염하며 κ 상대 오차 ≤ 유휴 비율이다. 승인 0.05는 계획기 여유 25%(1.25−1)의 1/5다.
- 새 기준: (1) 유휴 식별됨(연속성·실행 증거 완전; unknown, hole, 누락 native와 불완전 실행은 기존 탈락), (2) 유휴 비율 ≤ 동결된 `maximumIdleFraction`(승인 0.05; [0,1] 범위 검사하는 설정값으로 고정값 검증 아님), (3) 요청당 유휴 1ms 유지. 동결 `maximumIdleBaseFraction` 승인값은 0.05이며 profile에서 더 엄격한 비율을 역산하지 않는다.
- 보존: d는 유휴를 빼지 않은 보수적 원 측정값 그대로다. 식별된 경우만 나노초 단위 `dRange=[d−idleNanosPerRequest,d]`, `kappaErrorBound=idleFraction`을 기록하고 미식별이면 생략한다. 유휴 차감 보정 금지; 범위/상한은 95% CI나 전체 모델 오차 보장이 아니다.
- 범위: Insufficient D05/D11/D15의 원시 재판정은 보고만 하며 기존 immutable record나 Accepted를 바꾸거나 새 Accepted 근거를 만들지 않는다. live 변경·배포·측정·다른 진행 중 캠페인은 승인 범위 밖이다.
- 연결: [ADR-0028](../adr/0028-identified-idle-acceptance.md), [측정 계약](../design/serving/measurement-api.md).

## 2026-10-26 20:3xZ — E5 전 보완 워크플로우(U1~U10) 연속 진행

- `docs/plans/2026-10-27-pre-e5-workflow.md` §5. 사용자 승인은 E5 숫자 1회만; U2 최소안, U3 클럭 단계·U4 허용 차이·U7 결정 2개는 권고값으로 진행하고 여기에 기록.

### U4 — 같은 기종 프로파일 동등성 권고값(2026-10-27)

- 결정: PR-01..04. 같은 DeviceClass·동일 실행 조건의 원본을 다른 Node에 공유하되, 대상의 활성·유휴 확인 측정을 필수로 한다. 배치별 평균 서비스 시간·평균 활성 벽전력과 유휴 벽전력의 원본 대비 상대 차이를 각각 설정값 `maximumRelativeDifference=0.05` 이하로 확인한다. 5%는 계획 여유 25%의 1/5인 초기 screening 권고값이지 통계적 오차 보장이 아니다.
- 확인 창 권고: 활성·유휴 각각 30초 이상, trial 3개 이상; 원본의 warmup·settling과 원시/벽전력 완전성 규칙 유지. 모든 재사용 배치를 확인한다. 거절되면 대상의 독립 측정으로 전환하며 기준을 넓혀 통과시키지 않는다.
- 표현: 새 CRD 없이 대상 BatchEnergyOptimization의 `chill.dacs.io/profile-reuse` annotation에 원본 profile ID·허용 차이를 선언하고 기존 불변 결과와 원시 측정을 확인 기록으로 보존한다. Catalog가 대상 UID·GPU UUID·meter·메모리의 재사용 행과 판정 결과를 제공한다. d·operation cost·mode transition은 Node별로 유지한다.
- 주의: U3의 읽기 전용 호스트 조사에서 genesis2/3의 RTX 2080 Ti 이름·driver는 같지만 기본 power limit(250/300W), 최대 graphics clock(2100/2175MHz)과 VBIOS가 다르다. 기종 이름만으로 통과시키지 않고 실제 확인 차이로 판정한다.
- 연결: [catalog SRS §7](../spec/catalog/SRS.md#7-u4-같은-기종-프로파일-재사용), [측정 API §11](../design/serving/measurement-api.md#11-같은-기종-node-프로파일-재사용--pr-0104). 실클러스터 변경·실측은 CampaignOps 소유이며 이 결정 자체는 실증 완료 기록이 아니다.

## 2026-10-27 — U7 E5 실패 장면·집계 권고안 적용

- 첫 준비 구간은 부하 시작→첫 실제 분배 ACK의 요청을 전체 offered 분모에 유지하고 별도 표시한다. Node 차단은 명령 시작→uncordon 뒤 실제 Ready·관측·route 회복 구간을 표시하고 전체 결과와 해당 구간 제외 집계를 분모·실패 수와 함께 병기한다. 미복구는 단계 끝까지다. 사후 유리한 요청 제거는 금지한다.
- E5-7은 15분 0.3R, 180초 인스턴스 강제 종료·360초 Node 하나 cordon+서빙 Pod 삭제·600초 uncordon 권고값이다. 운영 담당이 기존 kubectl만 사용하고 실패 시 복원한다. cordon은 실제 네트워크 단절이 아니며 배치 차단·서빙 소실 대리 실험으로 한정한다.
- 제어요소 비교는 N=2(membership-off, replicas-off), full195+static170+비교70=435분(부대 시간 별도). U2 최소 Jetson 두 모드의 Accepted 근거 5종을 E5-5에 연결한다. 점진 감소·다른 모델 버스트·독립 반복·모든 제어요소 독립 기여는 미평가임을 공개한다.
- 근거: [연속 진행 §5](../plans/2026-10-27-pre-e5-workflow.md#5-사용자-수정2026-10-26-203xz-대기-없이-연속-진행), [E5 계획](../plans/2026-10-26-e5-evaluation.md), [설계 §9](../design/serving/workload-pools.md#9-검증-계획), scheduler `SCH-AC-45`. spec 평가 7행은 이미 실패 격리·대체·복구를 요구하므로 변경하지 않는다. 최종 E5 숫자 승인은 별도 1회이며 실행 완료나 장애 실증을 뜻하지 않는다.

## 2026-10-27 — U3 NVIDIA 클럭 단계·관측·권한 권고안

- 결정: genesis 공통 지원 목록에 있는 GPU_1500(기준), GPU_1200(중간), GPU_900(낮음), min=max·mode 0만 구현한다. 기존 fixed는 드라이버 기본 정책 그대로 별도 유지한다. 코어 동적 전력 감소는 기대일 뿐 실측 절감량이 아니며 memory clock/power limit/runtime 7 digest는 변경하지 않는다.
- 근거: SSH read-only에서 양쪽 driver 555.58.02, 지원 MHz·-lgc/-rgc 도움말·sudo 권한 확인. genesis2는 max2100MHz/default250W, genesis3는 max2175MHz/default300W라 기본 상태의 동등성은 가정하지 않는다(U4 통보).
- 관측 한계: application clocks=N/A, locked-range getter 없음. graphics와 SM 현재 MHz가 모두 정확한 요청값일 때만 기존 settling 후 성공으로 본다. 이는 잠금 레지스터 증명이 아니다. 실패하면 이전 선언 모드 복원·재조회 또는 -rgc 기본 정책 cleanup; reset 종료만으로 unlocked 검증 성공을 주장하지 않는다. 재시작은 조회만, 재부팅 뒤 기본 정책/새 bootID는 새 요청 전까지 재적용하지 않는다.
- 배포: Jetson과 분리한 NVIDIA operand, root+SYS_ADMIN·RuntimeClass nvidia/utility·read-only 호스트 도구/라이브러리, hostPID/privileged/SYS_BOOT 없음. 초기 genesis2 한 대. node-agent와 operator 이미지만 새로 빌드; runtime 7 아님.
- 연결: [PA-NV-01–05](../spec/platform/SRS.md), [Plan 6 GPU clock](../design/profiling/plans/06-execution-support-expansion.md#gpu-clock). 실제 변경·배포·GPU_1500↔GPU_900 비용 CR(방향당 Accepted 5회)은 CampaignOps 직렬 소유. 이 항목은 클럭 변경이나 실증 완료를 뜻하지 않는다.

## 2026-10-27 — U2 전력모드 최소 커버리지 권고값

- 결정: 별도 승인 대기 없이 jetsono `MAXN↔MODE_15W`, jetsonx `MODE_15W_6CORE↔MODE_15W_4CORE`, jetson-nano `MAXN↔5W`의 세 모델 CPU q1/B1, 총18조합을 최소 측정 범위로 한다. S는 INT8이며 o/x의 새 q1을 독립 측정한다. 2CORE는 예약 후 자원 부족 위험 때문에 x 최소 후보로 택하지 않았다. nano5W의 q1 실현 가능성은 착수 전 확인하고 부적격이면 미달 blocker로 보고하며 모드 제외·예약 임의 변경·κ0 대체를 하지 않는다.
- 근거: 읽기 전용 live snapshot `2026-10-26T20:33:57.347740004Z`에서 실행 options69, costs153, baselines75를 확인했다. 실행 프로파일은 각 Node 한 모드뿐이며 선언 모드와 현재 물리 모드는 다르다. 정확 준비·제거 비용69/69는 다른 모드의 모델 근거가 아니다. d source 보유와 새 규칙 Accepted를 구분한다. `O/intent/u2-power-coverage-{live,rows}-20261027.json`에 원시 참조와 전 조합을 보존했다.
- 실행 권고: 신규 profile11조합→d 최대17조합→부족 loaded 비용14조합·빈 idle1모드·전환2쌍4방향 순으로 기존 BEO/DispatchOccupancyMeasurement/OperationCostMeasurement를 사용한다. 모델 제거 뒤 빈 Node 모드 전환 범위이며 loaded 직접 전환은 별도 근거 전까지 제외한다. 전역1건, CampaignOps만 변경한다.
- 시간: [INFERENCE] 최소 잔여 약13.2–34.9시간(793–2091분), U1이 완료한 exact d는 차감하며 재시도·U3/U4 대기는 별도다. 중간/전체는 계획만 공개하고 E5를 전체안 완료까지 기다리게 하지 않는다. E5 숫자·κ/R는 최소 범위 근거 수용과 최종 SLO/m 재계산 뒤 사용자1회 승인 대상이다.
- 연결: catalog `PC-01..03`, [커버리지 계획 §§2–5](../plans/2026-10-27-power-mode-coverage.md). 이 기록은 실측 완료·클러스터 변경·E5 시작 승인이 아니다.

## 2026-10-27 — U5 native 결손 진단과 사전 원본 보존

- 결정: 기존 `SCH-31·42`/측정의 native 누락=Insufficient를 유지하며 emitter marshal/4096-byte/stdout 실패에 requestID·phase를, observer parse/runtime/supplement 거절에 requestID·phase·정확한 physicalID를 기록한다. decode가 안 된 식별자는 빈 값이며 hostname 등으로 꾸미지 않는다. native 자체의 physicalID는 Pod/container 보존 manifest로 연결한다.
- 보존: `hack/measure/capture-progress.py`를 다음 CR 생성 전에 read-only로 시작한다. load Job terminal 직후 자동 삭제되므로 그때 처음 logs를 열지 않고 미리 firstfloor follow를 저장하며, terminal/disappearance에서 timestamp 포함 snapshot을 시도한다. 삭제·회전·identity 확인 실패는 manifest에 남기고 완전 보존으로 표시하지 않는다. 원문 미회수 시 queued/running 추정이나 합격 완화 없이 Insufficient·재측정이다.
- 배포 경계: observer는 서버 재빌드, emitter는 native 실행 파일 재빌드 대상이다. runtime 7 digest 동결을 깨지 않으며 이번 소스 수정이 현재 실행기에 배포됐다고 주장하지 않는다. 다음 측정의 원인 확정·3회 무재발도 아직 운영 근거가 아니다.
- 연결: [관측 설계 §15](../design/serving/observability-split.md#15-u5-직접-native-진행-기록-결손-진단-sch-31-sch-42). `ProgressLoss27`·`ContinuityReplay27`의 D05/D10은 requests archive의 직접 phase 결손이며 정확한 소실 단계는 미확정이다.

## 2026-10-27 — U6 발행 충실도·최소 lock 축소와 원인 한계

- 관측: batch1-plc-3 완료8개(전체 생성기 창)의 공통 `O/campaign/v2/loadpods` 원시는 961,435 gap 중 >50ms 3,541개, 최대313–959ms다. 최대 D11/D26 공백의 미완료≈64·추정 완료1은 응답이 부족한 closed-loop 공급 경계이지 생산자 lock 원인 확정이 아니다. 인근 GC/CPU는 같은 길이의 STW/throttle을 보여주지 않으며 시각 정렬도 근사다.
- 같은 구간 검증: read-only S3 raw-19를 추가 보존하고 D11 final99,368개를 requestID로 전부 연결했다. assignment/pre-native·decode 잔여 `exchange−dispatched−clientSpan`은 전체 p95=.633ms·>50ms164개이나 최대gap 당시64개는 max=.588ms·>50ms0개다. 이 공백을 ObservationLog writer 체류 증가로 귀속하지 않는다. 정확한 지연 단계는 미확정([INFERENCE]); loader CPU/스케줄·출력 timing 결손을 0으로 채우지 않는다.
- 결정: CLI·E5 주입 producer의 JSON 직렬화만 공유 stdout lock 밖으로 옮기고 완전한 한 줄 write+flush는 보호한다. 별도 writer thread/queue를 추가하거나 이 작은 변경이 공백을 해결했다고 주장하지 않는다. CLI send/finish 단조 시각과 E5 `issuanceFidelity`에 실제/계획 gap, >50ms 빈도·구간, lateness를 남기며 예정된 Poisson/0부하 공백과 결손을 구분한다. 기존 SLO offered 분모는 유지한다.
- 배포: CLI 변경은 **load-generator 이미지** 재빌드·digest/receipt 갱신이 필요하다. U6는 runtime 7 재빌드 대상이 아니다. ObservationLog writer 개선은 Main의 별도 담당 영역이며 이번에는 immutable snapshot marshal→writer lock 아래 reference/append·hour·실패 재시도/최신 row 보존 경계만 설계하고 구현하지 않는다.
- 근거/연결: [관측 설계 §16](../design/serving/observability-split.md#16-u6-부하-발행-공백과-trace-충실도), 기존 `SCH-31·45`, 외부 `O/campaign/v2/d-20261027/u6-issuance-gaps.json`, `raw-19/{receipt.json,u6-gateway-residual.json,analyze-u6.py}`. 원격쓰기·클러스터 변경0이며 새 이미지 배포·재측정 성능 검증은 수행하지 않았다.

### U5/U6 리뷰 보완 — 실제 E5 결과 경로와 READY 실패 차단

- `demo.py run`의 summarize/publish에도 단일 발행 충실도 계산을 연결한다. 기존 함수를 환경 비의존 `d2/analyze_before.py`로 이동해 e5/demo가 재사용하며, 단계·전체 JSON/Markdown·출판 KPI에 실측/계획 gap과 >50ms 빈도·lateness를 보존한다. 누락은 UNKNOWN과 사유로 남긴다.
- 초기 follow는 process 객체 생성만으로 준비됐다고 보지 않는다. READY 직전 `poll()`로 이미 종료된 follow도 거절하며, 실패 증거는 manifest/원본 stderr로 유지한다. 이 확인이 READY 이후 스트림 생존이나 원문 완전성을 보장하지는 않는다.

## 2026-10-26 23:5xZ — U3 idle baseline 안정화 창 권고값(15s+30s)

- GPU_1500 ModelLoadedIdleBaseline(settling 60s+idle 30s)이 3/3 실패: firstfloor HTTP keep-alive 유휴 60s 초과로 측정기 연결 끊김(`Remote end closed`, `history://U3IdleFail27`). runtime 7 재빌드 금지 → 기존 Complete CR 7개와 같은 **settling 15s + idle 30s**로 진행(권고값). 근본 수정(측정기 연결 재수립)은 runtime 재빌드 시점에.

## 2026-10-27 00:0xZ — U4 실증 결과: genesis2↔genesis3는 동등 장치 아님

- 확인 측정(30s×3)은 Complete이나 재사용 Rejected(frozen condition 불일치 — observer digest 차이 유력[추정]; 확정엔 curve-plan 원본 필요). 그와 별개로 모델 로드 idle 벽전력 108.3W vs 71.9W(33.65%), GPU 기본 전력 한도 250 vs 300W, 최대 클럭 2100 vs 2175MHz → 현행 5% 동등성 기준에서 통과 불가(`history://U4RejectCause27`).
- 결정(권고 기본값): U4 재사용 메커니즘은 유지(거절이 올바른 동작), genesis2→3 공유는 적용하지 않고 두 Node를 독립 측정. U2 커버리지 표의 genesis 측정량 절반 감축 가정 철회. CPU 집합 완화·digest 조건 완화는 하지 않음.

## 2026-10-27 00:3xZ — U5 Pod 로그 동시 캡처 비활성(측정 중)

- D18 격리: capture-progress(kubectl logs --follow) 동시 실행 시 unknownRows 114, 없을 때 0(Accepted 1.15%). D15/D26의 대량 손실(21,797/5,100)도 같은 조건. 인과 미확정[추정: 약한 Node의 로그 스트림 경합]이지만 측정 중 캡처는 손실을 유발하므로 **측정 중 동시 캡처 금지**. 진행 기록 손실 원인 확정(U5)은 서버 측 reject 로그(cf92a845)만으로 계속.

## 2026-10-27 02:0xZ — PMTC 비용 창에 settling 포함(권고 기본값 60s)

- NVIDIA 클럭 전환(apply→Ready 2~12s)은 1s 표본·coverage 0.95에서 구조적으로 PowerWindowInsufficient(15/15; `history://PmtcPowerWindow27`). 비용 창 = apply→fresh Ready + settlingSeconds(설정, 기본 60, [0,3600]); η 적분·baseline 차감 범위 일치; τ와 settling 분리 기록(`52da1d38`). 기존 raw/finalized 불변. profiler/transition-observer 이미지 재빌드(runtime 7 아님).

## 2026-10-27 06:3xZ — U2 저전력 Smol BEO 관측 창 권고값

- jetsono 15W Smol q1 BEO: 마지막 추론(~12s/건)이 metric observer durationSeconds 299s를 초과해 결과 미발행(`history://SmolBeoTimeout27`). 권고: 느린 조합의 `activeProtocol.metricObservation.durationSeconds = 297 + 최대 단일 추론 + 여유`(이번 330s), 측정 목표 297s 유지. 코드 변경 없음.

## 2026-10-27 — E5 전 로컬 관측 보존 2h 복원

- 결정: `values-operating.yaml`의 시연 임시24h(`e659ccb1`, SCH-42 대기)를 기본2h로 복원한다. 공통 reader의 archive 투명 조회·UTC 정규화와 `e5-archive.py`의 없는 과거 시간 S3 fallback이 있어, 정상 archive/접근·유한 수집기한 안에서는 E5 180분+static170분 집계 원자료를 잃지 않는다. 6h/24h는 정확성 요구가 아니며 보존 실패·미완료 pin 보호는 유지한다.
- 근거: 읽기 전용 stat(10:03:32Z) 실제24h=14.251GB 원문/requests11.123GB/2,022파일; 가상2h=0.152GB/requests9.37MB/74파일. `34ec34f7`은 완료 payload를 버리지만 ID tombstone과 전체 requests 스캔은 남긴다. 첫 축소 rollout도 기존24h를 먼저 읽고, 첫 archive pass의 재압축 읽기도 남으므로 파일 수동삭제/빈 경로 전환은 하지 않는다.
- 예산: 150MB gz/h 가정의350분=875MB gz, 최근 혼합 압축비4.813배 조건부 원문4.212GB[INFERENCE]. rev93 기동 heap 관측880MiB와 GOMEMLIMIT3072MiB/limit4Gi 사이 산술 여유는 있지만 RSS/전체 E5 메모리 상한 인증은 아니다. 메모리 증설 없이2h 채택; 라이브 적용·cgroup/GC·archive 출처 확인은 CampaignOps 직렬 큐. 이번에는 stat/코드/기존 증거만 읽었고 빌드·테스트·라이브 변경0.
- 상세: [관측 설계 §5.1](../design/serving/observability-split.md#51-e5-전-운영-보존기동-예산-결정-2026-10-27), `O/intent/observation-retention-20261027.json`. 긴 창 S3 다운로드 시간은 E5-0만으로 검증됐다고 하지 않는다.

## 2026-10-27 — U2 추가 모드와 분리한 E5 숫자 요청 경계

- E5 기본 승인 요청은 U2 추가 모드를 기다리지 않고 진행한다. m29ms/MNV p95 1s·PLC p95 32ms·Smol p95 7.034s 비교후보, L2/1/75s·cap129s·delivery10ms/socketL+20ms·N2·정적E5-3끝 유지. 숫자 승인은 Main이 요청하며 이 기록은 실행 승인이 아니다.
- d 새 규칙34/34 완료와 후보SLO capacity 부재/jetsonx resourceRejected는 구분한다. 측정50ms/p99 60s와 부분합K를 승인R 또는 메모리 상한으로 쓰지 않는다. 상세 `O/intent/e5-numbers-20261027.md` 최신 결론(KappaR27 산출).

## 2026-10-27 10:2xZ — E5 숫자 승인(1차) + 부하 단계 정의 수정

- 승인: 기본 범위 8 Node 현재 모드; m=29ms; MNV p95 1s; PLC/Smol p95 32ms/7.034s 비교안(측정용 50ms/60s 대조); L=MNV2s/PLC1s/Smol75s, cap 129s, delivery 10ms, socket L+20ms; 전환/에너지 식 유지; 정적 기준선 E5-3 끝 0.9 구성; N=2.
- 부하 단계 정의 수정: R_w는 'Node 전체를 그 Workload에만 쓸 때' 용량이고 Node당 인스턴스 1개라 세 R 동시 사용 불가(현 정의면 0.3R 혼합 = Node 0.9개분, 0.9R = 2.7배). **rate_w = α × s_w × R_w**, 몫 s_w 설정값(기본 1/3씩, 합 1), α ∈ {0.3,0.5,0.7,0.9,1.2} = 전체 POOL 사용률.
- 병행: 빈 출발 capacity 재생(jetsonx·후보 SLO 포함) + 게이트웨이 G 계단 측정(새 정의 α 0.3~1.2 혼합 합계 구간). α=0.9 합계 > G면 원인(게이트웨이) 명시·보고. 최종 R·G 반영 숫자는 1회 추가 승인.

## 2026-10-27 10:4xZ — 부하 몫 s_w 보완: 게이트웨이 비병목 조건

- E5는 CHILL의 Node 배분을 시험하므로 실험 대상 밖(게이트웨이)이 먼저 막히면 과부하 시나리오가 입구 정체 실험이 됨. 몫 s_w는 균등 1/3 대신 **1.2 × Σ(s_w × R_w) ≤ 0.8 × G** 조건으로 정함. PLC는 요청 多·Node 少 → 몫 작게(권고 0.1), MNV/Smol 0.45/0.45. 설정값·근거 기록.
- G는 탐색 대신 확인 1회: 정한 몫으로 α=1.2 총 요청 수에서 2분 고정 배치 부하, 게이트웨이 CPU·PLC p95 확인(140 req/s에서 cs CPU 0.63/6 → 선형 외삽 ≈1,300 req/s [추정]). 통과하면 E5, 아니면 PLC 몫 축소 후 재확인.

## 2026-10-27 11:1xZ — E5 canonical 정의 SLO cutover(권고 기본값)

- E5용 canonical 3 정의의 SLO를 연구 비교안(PLC p95 32ms, Smol p95 7.034s, MNV p95 1s)으로 교체(새 UID; 측정용 50ms/p99 60s는 d 측정 CR에만 사용). capacity 재생·G 확인·E5는 이 정의로. 측정용 SLO κ는 대조 수치로만 보고.

## 2026-10-27 11:2xZ — U11 게이트웨이 구조 개편 등록(제어/데이터 평면 분리)

- 제안서 `O/intent/gateway-20261027/proposal.md`. G0(현 구조 G 확인 1회, E5 준비와 동일) 통과 시 E5 먼저, 직후 G1~G3; 실패 시 E5 앞. 워크플로우 U11.

## 2026-10-27 11:4xZ — U5 종결(원인 미확정, 재발 없음)

- 단발 progress stamp 손실: 새 규칙 24대상 중 D15 1건(unknown 0, 연속성 위반 1 pair); 해당 창·실물에 서버 reject/source issue/stream ended 0건(`history://U5RejectScan27`) → 손실은 observer 이전 단계(native stdout→CRI/follow 경계)[추정]. 이후 15회 연속 시도 재발 0 → 완료 기준('3회 연속 재발 없음') 충족. 동시 Pod 로그 캡처 금지 유지. runtime 재빌드 시 emitter 진단 로그 반영.

## 2026-10-27 14:2xZ — PLC 연구 SLO 확정(권고 기본값): p95 36ms

- sweep(재생 ~1~3분/회, 7af95dd4 이후): 32ms positive 1/8(FAIL), 36ms positive 5/8·orangepi κ=0·jetsonx 자원 Rejected(비양수), 40/50ms positive 7/8이나 느린 2 Node 양수(FAIL). 계획 규칙(조건 통과 중 가장 엄격)대로 **PLC p95 36ms** 채택(느린 2 중 jetsonx는 κ=0 증거가 아니라 자원 부적격 — 비양수로 해석; 기록). R_P 691.15 req/s(7 Node), α=1.2 몫 .1 → 82.94 req/s. Smol p95 7.034s(positive 5/8 PASS), MNV p95 1s 유지.
- G0 입력(α=1.2, .45/.1/.45): MNV 179.08 / PLC 82.94 / Smol 1.16 ≈ 263 req/s.

## 2026-10-27 15:0xZ — E5 부하 생성기를 열린 고리로(coordinated omission 제거)

- G0 Smol offered 120/139·U6 발행 공백은 같은 뿌리: E5 부하가 닫힌 고리(작업자가 응답 대기)라 서버가 느려지면 덜 보내고 지연을 낙관 측정. E5는 열린 고리: 고정 trace 예정 시각에 비동기 송신(동시 상한 없음), 지연은 예정 시각부터, 예정−실제 송신 차 기록. d 측정은 닫힌 고리 유지. 먼저 `feat/loadgen-async` 확인·재사용. 그 뒤 G0 1회(offered=계획) → E5-0 리허설 → E5.

## 2026-10-27 15:4xZ — 정정: 열린 고리에서 수명 차감 금지, 세 구간 분리

- efd5c177의 잔여 수명 차감(L − (송신−예정)) 되돌림: 생성기 지각은 장비 책임이지 서버 책임이 아님 — 깎은 수명을 주면 drop/우선순위 행동이 바뀌어 측정 오염. 명세(03_routing)대로 수명·SLO는 게이트웨이 수신 시각부터, X-Chill-Request-Timeout은 L 그대로.
- 세 구간 분리 기록·보고: (1) 예정→송신 시작 = 생성기 지각(장비 품질; 허용치 초과 비율이 기준을 넘으면 실행 무효, SLO 실패로 세지 않음), (2) 송신 시작→게이트웨이 수신 = CHILL 입구 지연(서버 책임, 입구 병목 판단), (3) 수신→응답 = SLO 판정. '예정 시각 기준 지연' 판정(7135c435)은 (3) 기준으로 되돌리고 예정 기준 종단 지연은 참고 지표. coordinated omission은 열린 고리(응답 대기 없는 송신)로 막음.

## 2026-10-28 — E5 생성기 품질 권고 기본값: 50ms·1%

- 결정: `generatorQuality = {"latenessToleranceNS": 50000000, "maxExceedingFraction": 0.01}`를 예시 실행·G-check·E5 7+1·preflight 설정에 명시한다. 50ms는 U6 발행 공백 분석 임계와 맞춘 권고값이며, gap 자체가 아니라 예정→실제 송신의 lateness를 판정한다. 실제 발행 요청 중 50ms **초과** 비율이 1% 이하면 품질 허용 범위다.
- frozen trace 전부 공급도 필수다. 초과 비율 >1% 또는 공급 불완전은 `INVALID_GENERATOR`로 실행 무효이며 서버 SLO 실패로 돌리지 않는다. 정책 미설정·필수 timestamp/모집단 결측은 계속 `UNKNOWN`; 런타임에 숨은 기본값을 넣거나 기존 binding·원시·판정을 소급 변경하지 않는다. 다음 prepare에서 실행 설정과 binding에 동결하고 full planned offered 분모·수신 원점 SLO·수명 L은 유지한다.

## 2026-10-27 16:5xZ — 증분 에너지가 분해능 이하(음수 점추정)일 때의 처리

- 관측: jetsonx 15W6CORE/15W4CORE Smol(q1) batch-1 증분 에너지 −8.96/−4.90 J/item — active 평균 전력(10.25/10.09W)이 idle 기준(10.55/10.25W, 95% 반폭 ±1.76/±1.93W)보다 낮음. derivation은 부호를 보존하고 serving admission(`firstfloor/config.go:140`)·replay(`scheduler_replay.go:375`)는 비음수만 허용 → 거부(`history://SmolInvalid27`).
- 결정(권고 기본값): 본질은 측정 불확실성 — 증분 에너지가 idle 기준 불확실성 이하라 0과 구별 불가. 자르기(clamp)는 묵시적 근사라 금지하되, **신뢰구간이 0을 포함하면 0 J/item + `energyBelowResolution` 표시로 명시 보고**, 상한도 음수면 측정 오류로 거부(재측정 요구). 재측정으로 양수 나올 때까지 반복 금지. 프로파일 계약 필드 추가는 E5 전 1회(ADR 0019).

## 2026-10-27 17:5xZ — U2 jetsonx 15W4CORE 잔여 항목 종결

- Smol x6C/x4C(q1) d: ConditionsInfeasible(유도 L이 게이트웨이 상한 초과; 서비스 ~30s/item, 연구 SLO 7.034s 불가 → κ=0 확정) → **Infeasible**로 기록(nano 5W와 동일 취급), 입력 L 축소 없음.
- PLC x4C d: 1회 RequestTimeout 3,375(L=1s, 64 workers, CPU 1), 분리 재측 1회 UnknownPhysicalContinuity(U5 양상) → 2회 실패. jetsonx PLC는 용량 재생에서 자원 Rejected라 E5 입력이 아님 → 추가 재측 없이 **미완(2회 실패)** 기록, E5 후 U5와 함께 재조사.
- 증분 에너지 분해능 이하 처리 배포(rev99, 331624f0): 두 Smol x preparation `energyBelowResolution=true` 확인.

## 2026-10-27 18:3xZ — genesis2 제외(7 Node)로 E5 (사용자 결정)

- genesis2 kern.log: Xid 79 총 5회(10/10 ×3 — 재부팅 6분 뒤 재발 포함, 10/14, 10/28 00:31 KST) → 만성 하드웨어(PCIe/전원/카드) 불안정 [추정 원인]. device plugin 10/14 재시작 12회도 같은 고장의 재부팅 흔적. 재부팅은 E5 수 시간 생존 보장 못 함.
- 진행: genesis2 제외로 capacity 재생 → R·PLC SLO 후보 같은 규칙으로 재선정 → 열린 고리 G0 1회 → 최종 숫자 재승인 요청. genesis2는 하드웨어 점검 항목(카드·전원 케이블 재장착, PCIe 절전 비활성, 재부팅 후 24h 무재발)으로 별도 기록; 복구되면 E5 뒤 추가 실행 비교. 결과 보고서에 "genesis2 하드웨어 결함으로 제외" 조건 명시.

## 2026-10-27 19:1xZ — 7 Node(genesis2 제외) 최종 R: PLC p95 36ms 유지

- 7 Node sweep(control-201-plc-{36,40,50}): PLC positive 36=4/7(≥4), orangepi κ0, jetsonx 자원 Rejected(≠κ0 — 한계 공개); 40/50은 orangepi 양수(FAIL). 같은 규칙 → **36ms 유지**.
- R: MNV 235.89 / PLC 387.37 / Smol 1.285 req/s. α=1.2(.45/.1/.45): MNV 127.38 / PLC 46.48 / Smol 0.694, 총 174.56 req/s. 열린 고리 G0 1회 후 재승인 요청.
- 병행(클러스터 무접촉): ① E5Tables(hack/e5 → spec 표 행), ② T5 WebSocket → ③ G2 게이트웨이 분리(배포 E5 뒤). 원격 push는 사용자에게 1회 질문.

## 2026-10-27 20:3xZ — 열린 고리 G0 종결(게이트웨이 비병목), 생성기 품질 조치

- 생성기 지각 1.33%→1.13%(SSLContext 1회 `7e324981`)→**0건**(Guaranteed 2CPU/1Gi + MNV 3 shard `e8ed3cff`, config 값). planned=issued 전부 일치.
- #203 G0(174.56 req/s, 120s, 고정 6 instance): PLC 수신 기준 p95 14.2ms≤36, 입구 지연 p95 ~23ms, cs CPU peak 1.60/6, 메모리 623MB, throttle 0 → **게이트웨이 비병목 확인(G0 목적 달성)**. Smol p95 12.47s>7.034s·성공 94.5%는 고정 6 instance 배치(Smol 용량 미달)의 산물로 분리 기록 — E5는 자율 배분이므로 G0 판정 대상 아님. 도구 판정 FAIL은 보존.

## 2026-10-27 20:5xZ — 최종 숫자 승인(사용자): 7 Node, PLC 36ms, R M235.89/P387.37/S1.285

- 토큰 갱신 후 E5-0 → E5. G0 Smol 미달은 고정 6 배치+α1.2 과부하 산물로 G0 판정과 분리.
- 보고서 조건: 생성기는 요청마다 새 TLS 연결(keep-alive 없음) → 입구 지연(p95 23ms)·게이트웨이 CPU에 핸드셰이크 비용 포함. 연결 재사용은 E5 뒤 과제(지금 변경 금지).
- E5 실행 창 중 genesis1 build/test 금지(부하 창 방송 규칙). 원격 push 승인(자격 증명·대용량 원시 제외).

## 2026-10-27 22:2xZ — 오케스트레이터 자세(사용자) 및 bind 전수 조사

- bind 관문에서 낡은 계약 4번째(sourceSHA, E5-7, G 숫자, precisionTargetMet) → 라이브 재시도 중단. 실제 입력으로 bind/gate 전체 오프라인 dry-run → 실패 항목 전수 목록 → 오늘 결정과 대조 → 한 커밋 교체 → 그 뒤에만 라이브.
- 시험 대상(CHILL) 실행 중 불변 / 측정 장비(생성기·하네스) 실행 시작 후 동결 / 판정 기준은 사용자 결정만. 한계는 보고서 조건으로 공개. 같은 문제 1시간·2회 실패면 정지 보고. 보고 3줄(진도 n/8 · 막힌 것과 본질 · 다음 한 가지).

## 2026-10-27 22:1xZ — E5 judge: 같은 Node 중첩 허용 조건(사용자 결정 A)

- 같은 Node 인스턴스 중첩은 교체 전환 구간(새 prepare 시작 ~ 옛 remove 완료, 다른 workload)이면서 장부상 자원이 겹치지 않을 때(CPU 집합 분리, 메모리 합 ≤ Node 할당량)만 허용. 중첩 건수·시간·Node 보고 지표. 정상 상태 >1·자원 겹침은 FAIL. 근거: make-before-break 표준; spec은 "인스턴스 하나=워크로드 하나", Node당 1은 R 계산 정상 상태 전제.
- 재시작 전 필수: E5-0 원시(control-210-full)·시연·preflight 원시로 E5-0~7 judge/summarize 규칙 전체 오프라인 dry-run → 결정과 어긋나는 규칙 전부 목록 → 한 커밋 교체(판정 기준 변경 필요 항목은 사용자에게 한 번에 묶어 질문) → archive → preflight → E5 full.

## 2026-10-27 22:3xZ — 정정: Node 중첩 판정 규칙 삭제(결정 A 대체)

- 과밀은 CHILL 자원 장부(ADR 0023)가 입장 시 예약으로, 실행 중 CPU 겹침은 CHILL 공용 안전 검사가 막는다. 하네스의 사후 재검사는 AGENTS.md "경계 1회 검증, 하류 재검증 금지" 위반 → 중첩 판정 삭제(기록도 없음). 진짜 과밀은 E5가 재는 SLO·에너지에 드러남.
- judge 전체를 같은 눈으로 E5-0 원시 오프라인 dry-run: CHILL이 이미 보장하는 것을 재검사하는 규칙은 모두 삭제, 판정에는 SLO·에너지·생성기 품질·전력 완전성만 남김 → 한 커밋 → archive → preflight → E5 full.

## 2026-10-27 23:5xZ — E5-1 정지 원인과 조치

- E5-1 부하 완료(planned=issued M57495/P20605/S301, 지각 0.009%) 후 하네스 단계 경계 quiet 대기(예약 0) 194s 초과 → 정지. quiet 요구는 CHILL 재검사 → 삭제(`6f6b28c3`, 기록 전용; freeze는 자율 off·route ACK 유지).
- 예약 잔류 본질: jetson-nano MNV prepare가 `client rate limiter Wait … context canceled`로 partial 실패 후 예약 미해제 — **CHILL 결함(E5 보고서 조건, E5 뒤 수정)**. 복구는 기존 reset takeover 1회.
- kube client QPS 50/Burst 100(Helm `servingEntrypoint.kubeAPI`)에서 rate limiter 오류 2회 관측(PLC x4C drain, 이 prepare) → 재시작 전 **200/400**으로 상향(값만, 이미지 불변; 설정값 변경을 binding에 기록).

## 2026-10-28 01:0xZ — 단계 judgment 비치명(기록만)

- #212 E5-1: 부하·지각·SLO 정상(M p95 746ms/P 16ms/S 2.9s), 전력 gap 3.5~4.8s(>3s) 3 Node → judgment가 run 정지. 결정(권고 기본값): 단계 judgment(FAIL/NOT_READY/INCOMPLETE)는 기록만 하고 계속; 전력 gap은 coverage·해당 창 에너지 UNKNOWN; run 정지는 운영 실패(producer/bind/route ACK, 생성기 INVALID)만(`158e106c`).

## 2026-10-28 01:2xZ — E5 행 에너지 판정 기준(사용자 결정)

- "최대 공백 3초"는 짧은 측정 창(d·비용)용. E5 행(30분)에서는 공백 구간을 앞뒤 표본 선형 보간으로 적분, 공백 합 ≤ 창 1% 그리고 단일 공백 ≤ 30초면 에너지 값 산출 + coverage(공백 합·최대 공백)와 오차 상한(공백초 × 해당 Node 창 내 최대−최소 W) 기록. 초과 시 UNKNOWN. config(기본 0.01, 30s); d·비용 3초 기준 유지.
- #213 진행 중이면 중단 금지: 원시 보존되므로 E5 종료 후 judge/summarize 재실행으로 전 행 적용(재측정 없음). 하네스 수정은 E5 창 밖, 한 커밋. 읽기 실패(30분 60회) 원인은 E5 뒤 edge-metrics 과제로만 기록.

## 2026-10-28 06:5xZ — 게이트웨이 분리(ADR 0029) 보류(사용자 요청 2차 검토)

- fusion 2차 검토(패널 opus-5-5 / gpt-6-astra / gemini-4-argon, 판정 gpt-6.1-sol)에서 3패널 모두 보류. G0 gateway PASS(CPU 1.60/6, throttle 0), spec 측정 표가 router N개를 요구하지 않음. #213 탐색 중 `gatewayTailNS` 꼬리(PLC p95 37~46ms 대 7~8ms)는 상관 관찰로만 기록.
- ADR 0029·gateway-split·SRS §8·U11을 Deferred로 전환. 재개 조건: gateway 귀속 G 확인 실패 또는 제어 간섭이 측정 표를 훼손하고 직접 수정으로 해결되지 않을 때. 재개 시 control 1 + router 1부터.
- `e947cd5e` 미사용 분리 코드는 E5 종료·표 기입 뒤, 다음 서버 재빌드 전에 사용처 단위로 제거(공유 Runtime 변경 포함). T5·`internal/serving/live` 유지. evidence API는 단일 source로 진행.

## 2026-10-28 — 대시보드 라이브 무대 계층화(사용자 결정)

- 사용자 리뷰: 한 장에 정보 과밀 → 고수준 축약만 보이고 누르면 하위 페이지·펼침 카드로 내려가는 트리(개요 → POOL → 인스턴스·Node·예약 → 근거). 실시간 배치는 POOL(워크로드 UID) 단위 묶음. 요청·데이터 경로와 제어 경로를 CHILL 구조 그림(클라이언트 → ChillServer 접수 → 워크로드별 큐·분배 → Node 인스턴스 → 응답; 관측 → 판단 → 작업 → 예약 장부) 위에 실시간으로 표시.
- 반영: SRS DB-FR-50~52 추가, DB-FR-36~39 계층 재배치(카드 3개 고정·Node×워크로드 격자 삭제). 설계 §6.1 재작성(정보 트리, 엣지별 DTO 출처 표, POOL 레인, URL `/pools/[uid]`·`?open=`·`?at=`). 계획 4장 화면 범위·7장 S2 기준 갱신. 지표 의미(N=completed+failed, 비율 없음)·예약·decision.at 근거·폴링 금지·연기 항목·서버 선행 수정은 유지. 새 DTO 필드 없음(대기 수·예약·Node 사유는 기존 선행 수정 항목). 그리기는 기존 스택(CSS grid + inline SVG, Recharts)만, 그래프 라이브러리 추가 없음. 아키텍처 그림은 단일 ChillServer(ADR 0029 Deferred) 기준.
- 2차 검토(코드 대조) 수정: 대기 수는 워크로드 유입 큐(`WorkloadState.Pending`, 미투영 → 비움)가 아니라 분배 뒤 인스턴스 native 대기(`instances[].waiting`)이므로 E2→E3로 이동. `actions[].kind`에 측정 run 3종(`operation-cost-measurement`·`dispatch-occupancy-measurement`·`measurement-handback`)도 오므로 kind 그대로 표시. 인스턴스 기호는 순서 있는 판정(× → ◌ → ● → ◐ → ○ → ◇ 준비 중)으로 고정. 레벨 0 레인에 Node별 인스턴스 기호 포함(SRS DB-FR-36·설계 표 일치). 레벨 1→0 focus 복원은 `sessionStorage` 대신 `/#pool-<uid>` hash, 뒤로가기 focus 복원은 같은 페이지 계층(2·3)만. `/pools/[uid]`는 기존 `features/resources.tsx:PoolPage`(REST 폴링 옵션) route를 live 페이지로 대체. 설계 §4.3을 계획 4장과 일치(`recordRef`·`workloadUID`·`targetPowerMode`·`watts` 삭제, `waiting`·`reservations`·Node `ready/measurement/reasons` 추가, status `canceled` 없음).

## 2026-10-28 — 라이브 `workloads[].pending` 추가 (사용자 결정)

- 흐름 보드 E2(접수→POOL 큐)에 워크로드 유입 큐 길이(배정 전, runtime `WorkloadState.Pending`)를 표시한다. 라이브 DTO에 `workloads[].pending` 1필드를 대시보드 계획 4장 선행 수정으로 추가한다. 인스턴스 `waiting`(배정 뒤 대기)과 더하지 않고, 수신−종결 차로 대신하지 않는다.

## 2026-10-28 09:0xZ — E5 1회차 8/8 완료 후 재판정·표 작성 지시(사용자)

- (1) spec 표 전 필수: MNV D1=FAIL(E5-1/5/6, 수신 p95 0.29~0.40s<1s) 원인 1줄 — 오류·미완료가 rank에 섞였는지, 판정 계산(분모/분위수) 오류인지. 계산 오류면 같은 재판정 커밋에서 고치고 FAIL을 그대로 옮기지 않음.
- (2) 표: 행마다 지연 p95·결과 수(완료/실패/거절/취소/미완료)·풀 에너지(오차 상한·coverage)·CHILL vs 고정 배치 에너지 차이 나란히(spec README 220-223).
- (3) 정정: 제어요소 비교는 spec 요구(README 225) — 7행 표 먼저, 이어서 factor 준비 거부(manual cap/wait/resource override 미지원) 최소 수정 후 실행; 보고서 "다음 작업".

## 2026-10-28 09:4xZ — CHILL 결함 후보(E5 1회차, 수정은 결과 확정 뒤)

- **전환 중 MNV 라우팅 공백**: BackendUnavailable E5-1 9,092 / E5-5 4,507 / E5-6 8,417건, 초 단위 군집 43/43이 MNV prepare/route 실행 구간과 겹침(오류 초 기준 85.6/53.5/84.8%); 그러나 scheduler snapshot의 MNV routable 수는 항상 1~3(0 아님) → 전체 공백이 아니라 특정 대상(신규/route 전환 인스턴스)으로의 전달 실패 [추정]. 고정 배치(E5-S)에는 없음. 원시 `control-213-full/routing-gap-analysis.md`, `history://RoutingGap27`.
- **rasp5 교체 진동**: `006-mnv2-cpu-q1 ↔ 008-mnv2-cpu-q1-lut` 반복 교체(E5-1 약 1~2분 간격 14회) — 계획기 진동 후보.
- **예약 잔류**(prepare rate-limiter 실패 후) — 23:5xZ 항목.

## 2026-10-28 11:0xZ — 제어요소 비교(factor) 실행 준비

- 거부 원인: factor 초기 상태 복원에 planner가 정한 인스턴스별 maxBatch cap(2→1, 32→1)이 필요한데 public prepare가 받지 못함(`history://FactorPrep27`). 최소 수정: prepare 선택 필드 `maxBatch`(측정된 batch 집합 내, 경계 1회 검증) `c13f13f1`; 하네스 cap 복원·instances[] ACK `d6ebf33c`. options 템플릿 의미 불변.
- 시험 대상 변경 공개: factor 실행은 rev102(48c1a44f: prepare 필드 + T5/G2 코드 포함, G2 미배선) — full 행(rev101)과 제어 로직 동일, 보고서 조건으로 명시. lint/test/arch clean PASS(48c1a44f).

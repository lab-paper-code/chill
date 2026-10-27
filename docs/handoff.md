# CHILL 인수인계

## 목표

- mobilenet, SmolVLM2 시각 질의응답, PLC 이상 탐지를 같은 서빙 경로로 함께 운영한다.
- 실측 근거로 첫 구성을 만들고 부하에 따라 확장과 축소를 수행하며 워크로드별 서비스 수준 목표(Service Level Objective, SLO)를 지킨다.
- 전환을 포함한 전체 자원 풀의 에너지를 정적 구성과 비교하고 [평가 시나리오 7개](spec/README.md)를 채운다.

## 현재 상태

**기록 기준: 2026-10-27, decision-log 11:2xZ 항목 및 운영 checkpoint 마지막 40행.** 아래는 보존된 관측이며 접속 시점의 실시간 상태를 뜻하지 않는다. 원문에 사용자 시각 표기와 실행 시각이 혼재하므로 세부 이력의 시각을 새 현재 시각으로 옮겨 쓰지 않는다. `O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`다.

| 구분 | 마지막 확인 |
|---|---|
| 저장소 | `/home/genesis1/ETRI/chill`, `main`; workload-pools 통합과 spec 전체 추적 완료; 원격 push 미실행(U10 사용자 결정) |
| 배포 | 라이브 Helm `chill` **rev96**(node-agent `29bff51e` 포함); **rev97 예정**: 로컬 관측 보존 24h→2h(`b7ccd4a8`, 수동 overlay), 아직 적용 완료로 세지 않음 |
| 제어 자원 | ChillServer 6 CPU / 4 GiB, `GOMEMLIMIT=3072MiB`, `planSearchWorkers=3`; runtime 이미지 7종 고정 |
| 측정 진도 | dispatch 점유 보정값 d 새 규칙 **34/34 Accepted**, 새 raw key의 planner 소비 확인(`O/campaign/v2/d-20261027/control-195/report.json`); 정확 전환 비용 참조 **69/69**, 평가 **0/7** |
| E5 숫자 | 10:2xZ 1차 승인: 기본 8 Node 현재 모드, m=29ms, MNV/PLC/Smol p95=1s/32ms/7.034s 비교안; canonical SLO cutover와 capacity 재생 전이므로 최종 R 미확정 |
| 부하 정의 | `rate_w=α×s_w×R_w`; 몫 MNV/PLC/Smol=0.45/0.1/0.45, `1.2×Σ(s_w×R_w)≤0.8×G`; G0는 α=1.2 혼합 부하 2분 확인 1회(약380 req/s는 잠정값) |
| 운영 큐 | **CampaignOps38 직렬: U2 일시중지 → rev97 retention → canonical SLO cutover·capacity 재생 → G0 확인 → U2 재개** |
| 사고 복구 | 게이트웨이 CPU 퇴행은 UID selector로 회복; rev92 기동 OOM은 `34ec34f7`→rev93으로 복구. 상세 근거는 아래 사고 2건 참조 |

시연·진단·장시간 시험과 G0는 연구 평가 7개의 완료 수에 포함하지 않는다. 정확 비용 69/69는 기존 소비자 snapshot의 참조 연결 수이며 모든 후보의 통계적 적격성이나 U2 추가 모드의 근거 완비를 뜻하지 않는다. d 34/34 완료와 최종 SLO의 capacity·R 확정도 구분한다.

### U1~U11 상태

정의와 의존성은 [E5 전 보완 워크플로우](plans/2026-10-27-pre-e5-workflow.md)를 따른다.

| 항목 | 상태 | 완료 근거 / 남은 일 |
|---|---|---|
| U1 d 마무리 | **완료** | 06:0xZ 새 규칙34/34; PLC11·MNV GPU5·MNV CPU8 완료, 기존10 포함; planner 소비 확인 |
| U2 전력 모드 커버리지 | **진행(일시중지)** | 최소안18조합. jetsono 15W/MAXN 전 항목 완료, jetsonx 6CORE Smol BEO Complete(4048s); 4CORE·nano MAXN 보완 남음. nano5W 3조합은 서빙 CPU0으로 Infeasible; 임의 제외·κ0 대체 금지 |
| U3 NVIDIA 클럭 | **완료** | genesis에서1500↔900 적용·read-back·`-rgc` 복원, idle baseline 및 양방향 전환 비용5/5. τ≈2.00/2.33s, η≈+76.2/−123.7J |
| U4 같은 기종 공유 | **완료(공유 거절)** | 메커니즘·확인 측정 실증; genesis2→3는 비동등으로 Rejected(idle108.3/71.9W 등). 공유하지 않고 MNV CPU8 독립 측정 완료; 측정량 절반 감축 가정 철회 |
| U5 진행 기록 유실 | **진행** | 서버 수집 손실 관측·reject 진단 보강; 정확한 단발 소실 단계는 미확정. 측정 중 Pod 로그 동시 캡처 금지; 원인 확정 또는3회 연속 무재발 기준의 종결은 아직 기록 없음 |
| U6 생성기 멈춤 | **진행** | lock 밖 JSON 직렬화·발행 충실도 기록 반영, rev91 loadgen 배포. >50ms gap3,541/961,435 관측; 최대 공백 원인은 미확정이며 수정으로 해결됐다고 주장하지 않음 |
| U7 E5 계획 보강 | **진행** | 실패 장면·7행 커버리지·집계 권고안 반영, 숫자1차 승인. canonical SLO capacity 재생·G0 후 최종 R·G 승인과 짧은 E5-0 리허설 남음 |
| U8 E5 실행 | **예정** | 선행 근거·최종 숫자 승인·preflight 뒤 실행; 평가0/7 |
| U9 대시보드 T5/T6 | **예정** | E5 뒤, 예약·전력 모드 표시 포함 |
| U10 원격 push | **예정** | 사용자 시점 결정, 아직 미실행 |
| U11 게이트웨이 구조 개편 | **예정(G0 대기)** | 현 구조 G0 통과 시 E5 직후 G1(spec/SRS/설계/ADR0029)→G2 구현→G3 실증; 실패 시 G1~G3를 E5 앞으로 |

## 운영 규칙

- 클러스터 context는 `kubernetes-admin@kubernetes`, namespace는 `chill-system`, Helm release는 `chill`이다. [hack 운영 안내](../hack/README.md)에 따라 testbed kubeconfig를 명시하고 기본 shell context에 의존하지 않는다. 자격 증명은 승인된 운영 환경에서 인계받는다.
- runtime 7종 digest는 고정한다. 선언과 이미지 참조는 [운영 values](../charts/chill/values-operating.yaml)를 참조하되, 이 파일만으로 라이브 rev96 입력을 재구성하지 않는다. rev97 retention은 운영 담당의 수동 overlay 큐다. runtime 변경 시 영향받는 프로파일, d, 전환 비용과 비교 기준을 먼저 제시하고 사용자 승인을 받는다.
- 현재 연속 진행 범위는 [워크플로우 §5](plans/2026-10-27-pre-e5-workflow.md#5-사용자-수정2026-10-26-203xz-대기-없이-연속-진행)와 이후 decision-log를 따른다. U2 최소안·U3/U4 권고값·U5/U6 최소 수정은 대기 없이 진행·기록하되, 최종 R·G를 반영한 E5 숫자는 추가 승인 대상이다. 승인 밖 조건·판정·runtime 변경을 이 문서로 허가하지 않는다.
- 실험 조종 코드는 `hack/e5/`, `hack/measure/`에 둔다. 환경값은 설정 또는 인자로 분리한다. 원시 결과, 백업과 민감값은 저장소 밖에 둔다. `CHILL_EXPERIMENT_CONFIG`로 승인된 환경 설정을 지정한다.
- 라이브 변경은 한 담당이 직렬 수행하고 한 파일은 한 담당이 소유한다. 빌드와 검증은 측정 및 부하 창과 겹치지 않게 조율한다. 이 문서 자체는 배포나 측정 개시 승인을 부여하지 않는다.
- d는 정밀도1%, 양측95% 계획 근사, 여유1.25, 요청당 유휴1ms, 본창360초·준비60초 하한, 전체3600초 상한을 유지한다. 현재 판정은 **유휴 식별 필수 + 동결된 유휴 비율 상한5%**이며, d에서 유휴를 빼지 않고 `dRange=[d−idle/request,d]`·κ 오차 상한을 기록한다. signed 원시 값·Accepted·계획기의 `max(d,0)` 투영을 구분한다. 상세는 [측정 설계](design/serving/measurement-api.md)와 [ADR0028](adr/0028-identified-idle-acceptance.md)을 따른다.
- 전력 결측 허용 `tolerateArchive`는 진단 전용이며 E5 설정에서는 거부한다. 원시 결측을 0으로 채우거나 표본 확인을 전체 검증으로 확대하지 않는다.
- 기존 완료 Job은 임의 삭제하지 않는다. 새 측정의 증거 확정 후 삭제 계약과 기존 기록의 보존 경계는 [ADR 0026](adr/0026-finished-measurement-pods.md)을 따른다.

## 다음 할 일

1. 운영 담당 CampaignOps38만 직렬 큐를 수행한다: rev97 retention2h → canonical 3정의 SLO cutover(MNV1s·PLC32ms·Smol7.034s p95, 새 UID) → 빈 출발 capacity 재생(jetsonx 포함) → G0 확인 → U2 재개. 수동 파일 삭제나 메모리 증설로 대체하지 않는다.
2. **다음 승인 지점은 최종 R·G를 반영한 E5 숫자1회 추가 승인**이다. 현재 부분합 K(MNV331.6/PLC1657.4/Smol3.24)와 측정용 SLO의 κ를 승인 R로 쓰지 않는다. 확정 R, 몫0.45/0.1/0.45, `1.2Σ(s·R)≤0.8G` 만족 여부, CPU·PLC p95 및 실제 부하를 함께 보고한다.
3. G0 통과 시 E5 준비를 계속한다. 실패 시 원인·PLC 몫 축소 후 재확인과 U11 선행 경로를 워크플로우/결정에 따라 처리한다. 외삽 G≈1300 req/s는 [추정]일 뿐 측정된 한계값이 아니다.
4. U5/U6 잔여 원인·재발 근거와 U2 최소안 Accepted 연결/미달 blocker를 기록한다. U2 추가 모드 완료 대기로 기본8 Node 현재 모드의 숫자 승인 요청을 막지 않으며, 추가 모드 근거가 없는 장면을 완료로 세지 않는다.
5. 최종 승인·토큰 만료 검사·전력 결측 불허 preflight와 짧은 E5-0 리허설/정상 정리 뒤 U8 평가를 실행한다. 첫 준비 손실은 전체 offered 분모 유지+별도 표시, Node 차단은 전체와 해당 구간 제외 집계를 병기한다.
6. E5 뒤 U9 T5/T6와 U11의 해당 경로를 진행하고, U10 push는 사용자 결정에 따른다. 이 문서 갱신으로 라이브 변경·측정·빌드·테스트를 개시하지 않는다.

### 사고 2건과 결정 일지 날짜

- **게이트웨이 CPU 사고(2026-10-26):** [사고0004](postmortems/0004-gateway-pod-list-regression.md), [성능 보고](reports/gateway-performance-20261026.md). probe마다 전체1053 Pod 조회가 주원인; UID selector 후60/140 req/s에서 CPU4.7→0.47/0.63core, PLC p95 56/58→15.6/10.7ms. 증설·writer 잠금 가설을 해결책으로 채택하지 않았다.
- **기동 OOM 사고(2026-10-27 01:0xZ):** rev92에서24h request 복원 map의 live heap3.7GiB로 OOMKilled 반복·제어부 약30분 중단. `34ec34f7`의 완료 payload 제거·tail 읽기를 rev93에 적용해 Ready294s·postGC 최대456MB로 복구. [복원/보존 설계와 운영 근거](design/serving/observability-split.md#51-e5-전-운영-보존기동-예산-결정-2026-10-27), [원시 checkpoint](file:///home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/checkpoints.md)(2054–2055행), `O/campaign/v2/d-20261027/control-192/` 참조. 완료 ID tombstone·전체 requests 스캔은 남으므로 전체 E5 메모리 상한 인증은 아니다.

[decision-log](intent/decision-log.md)의 관련 날짜 목록(같은 날짜의 사건 순서는 원문 기준):

- **2026-10-26:** 「게이트웨이 성능은 증설 전에 원인 확정」, 「UID selector로 조회 범위 제한」; **20:0xZ** d 유휴 판정 재설정; **20:3xZ** U1~U10 연속 진행; **23:5xZ** U3 idle15s+30s.
- **2026-10-27(시각 미기재):** d 관측 최대 기반 공급 깊이, U2 최소 커버리지, U3 클럭·관측·권한, U4 동등성5%/30s×3, U5 native 진단, U6 발행 충실도 및 리뷰 보완, U7 실패 장면·집계, PMTC settling, 로컬 보존2h 복원, U2 추가 모드와 E5 숫자 요청 분리.
- **2026-10-27 00:0xZ / 00:3xZ / 02:0xZ / 06:3xZ:** U4 비동등 거절 / U5 동시 캡처 금지 / PMTC 비용 창 settling / U2 저전력 Smol BEO 관측 창.
- **2026-10-27 10:2xZ / 10:4xZ / 11:1xZ / 11:2xZ:** E5 숫자1차·부하 정의 / 몫·G 비병목 조건 / canonical SLO cutover / U11 등록. OOM 발생·복구의 원시 일지는 checkpoint이며, 후속 보존 결정은 위2h 복원 항목이다.

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
- [ADR 0028: 식별된 유휴의 d 판정](adr/0028-identified-idle-acceptance.md)
- [사고 0002: 시연 2회차 중복 준비](postmortems/0002-demo2-duplicate-preparation.md)
- [사고 0003: rev76 빈 출발 정체](postmortems/0003-rev76-bootstrap-starvation.md)
- [사고 0004: 전체 Pod 조회 성능 퇴행](postmortems/0004-gateway-pod-list-regression.md)
- [사고 0005: 측정 캠페인 정체](postmortems/0005-measurement-campaign-stuck.md)

### 결과와 계획

- [시연 결과](reports/demos/README.md), [구조 검토](reports/structure-review-20261026.md), [게이트웨이 성능](reports/gateway-performance-20261026.md)
- [E5 전 보완 워크플로우(U1~U11)](plans/2026-10-27-pre-e5-workflow.md), [전력 모드 커버리지](plans/2026-10-27-power-mode-coverage.md)
- [남은 로드맵(이전 단계)](plans/2026-10-26-remaining-roadmap.md), [대시보드 라이브 무대](plans/2026-10-26-dashboard-live-stage.md), [E5 평가](plans/2026-10-26-e5-evaluation.md)

원시 근거 위치: 운영 호스트 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/`의 `intent/`, `audit-20261024/`, `campaign/v2/`, `perf/`, `deployB/`. 대용량 원문과 실행 로그는 이 위치에 보존하며, 프로젝트 재개의 필수 읽기 경로는 위 저장소 문서다.

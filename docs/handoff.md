# CHILL workload-pools 인수인계

다음 세션은 아래 외부 원문을 먼저 읽는다. 이 경로들은 운영 호스트의 로컬 자료이며 Git clone에 포함되지 않는다. 경로 접근 권한과 승인된 환경 설정은 운영 담당에게 인계받는다.
- `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/north-star.md`: 연구 목표와 기본 원칙
- `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/user-intent.md`: 사용자 결정과 승인 경계
- `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/checkpoints.md`: 최신 시간순 결정. 2026-10-25 11:3xZ 시연 우선 지시까지 읽는다.
- `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/merge-gap-and-remaining-20261025.md`: 병합 차이와 보완 범위. 시연 정리 완료는 아래 최신 근거를 적용한다.
- `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/dashboard-redesign-20261025.md`: 대시보드 보존과 라이브 무대 요구

## 현재 상태

목표는 mobilenet, SmolVLM2 VQAv2, PLC anomaly의 혼합 부하에서 워크로드별 SLO와 처리 결과를 확인하고, 전환을 포함한 전체 POOL 에너지를 정적 구성 및 제어요소별 결과와 비교하는 것이다. 현재 우선순위는 E2E 시연 보고서, 전환 손실 수정 선택, 대시보드 준비와 병합 준비다.

**보고 기준: d 새 규칙 10/34, 정확 비용 69/69, 평가 시나리오 0/7.**
PLC genesis2 D06 retry1은 2026-10-25T07:39:10.403115Z까지 새 규칙 Accepted, 계획기 소비와 물리 반환이 확인됐다. 비용 69/69는 2026-10-24T12:49:27.350175648Z 소비자 snapshot 기준이다. 시연은 연구 결과에서 제외하며 평가 7개의 완료 수를 유지한다.

- 이 문서의 소스 기준점은 `feat/workload-pools`의 `6b868b739cf7c41daa26383447b48fcde684b5b9`다. 이 커밋에는 시연 정리 재개 도구 수정이 포함된다. 문서 및 설정 준비 커밋과 실제 배포 소스를 구분한다.
- 마지막 배포 기록은 Helm `chill`, namespace `chill-system`의 **rev74**다. 2026-10-25T08:17:03.013339Z 적용 뒤 08:19:39.730152Z에 서버 시작과 설정을 확인했다. ChillServer 소스는 `3dff4e789c3e44c8dc1d8dd1f2addbec2ed0568c`, 나머지 제어 이미지는 기존 rev73 소스 `4fb9bf8b0f8112549cf8d939b911c19ddfc9c54e`를 유지한다. runtime 7종 digest, 서버 6 CPU / 4 GiB, `GOMEMLIMIT=3072MiB`, `planSearchWorkers=3`도 유지한다.
- 첫 시연은 E0 → A → B → C를 수집했다. A의 Smol 성공률 30%, B의 mobilenet 성공률 약 63.8%와 도구 수리 공백을 원시 결과와 함께 보존한다. B에서는 먼저 준비된 용량이 느린 전력 모드 전환을 기다리는 문제가 관측됐다.
- 시연 정리는 2026-10-25T11:13:28.033945Z에 끝났고 소유한 canonical 정의 3개의 삭제는 11:13:37.332504Z에 끝났다. 11:13:25.345688425Z snapshot은 실물 0, 진행 작업 0, 대상 8대 Ready와 원모드를 기록한다. 자율 제어는 꺼졌고 분배 선언은 제거됐다. rev74와 runtime 7종은 유지됐다. 이는 보존된 관측 시점의 상태다.
- 남은 d 24개, G 계단, 진단 2회, E5 숫자 승인, 창 경계 수정 `a74a595`와 workers 재산정은 보류 상태다. 현재 문서 작업에서 측정, 이미지 발행과 배포를 수행하지 않는다.

## 승인과 소유권

현재 계약은 저장소의 `measurement-api.md`, `analytic-planner.md`, `workload-pools.md` 본문이 정의하며 정확한 경로는 아래 정본 진입점에 연결한다. `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/deployment2-e5-proposal-20261024.md`는 승인 원문, `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/state-audit-execution-contract-20261024.txt`는 실행 근거로 보존한다. 사용자 12:20Z 결정으로 여섯 권고와 §10 방향이 승인됐다. #055의 실제 시각 12:27:17.194Z는 정확한 8 roots, 38 steps 범위에서 #054를 대체하고 나머지 hold를 보존한다.

| 담당 | 현재 소유권 |
|---|---|
| Main | 계획과 승인 경계를 감독하고 근거 확인, 단계 전환 및 최종 검증을 조율한다. |
| 구현 및 문서 담당 | 합의한 파일만 수정하고 최소 단위로 commit한다. |
| CampaignOps | 번호와 UTC를 기록한 승인 범위의 live 변경을 전담한다. |
| 운영 기록 담당 | 외부 intent, checkpoints, 환경 설정과 실행 증거를 관리한다. |

작업과 도구는 `/home/genesis1/ETRI/chill-pools` 및 `/home/genesis1/ETRI/chill-pools/bin`을 사용한다. `/home/genesis1/ETRI/chill`은 bin까지 보존하며 main 이관은 P11에서 수행한다.

- B-only는 nominal 값, 동일 근거 상쇄, 전환 chain과 keep-infeasible을 보존한다. 13개 BEO idle fallback의 통계 CI 미포괄과 전역 95% 미보장 한계를 명시한다.
- d는 승인된 1%, 양측 95%, 여유 1.25, 요청당 idle 1ms, 본창 360초와 준비 60초 하한, 3600초 상한을 유지한다. signed 원시 값과 Accepted 판정, `max(d,0)` 투영을 보존한다. D04/D09/D14/D23/D33의 systematic profile-serving bias는 E5에 기록하며 새 정책은 M5 후보로 관리한다.
- 원본012의 Complete, raw와 LocalMinimum, 원본017의 16개 raw와 Measuring 상태를 보존한다. coherent 대체 2개의 실제 소비자 참조와 recordedJob 예방 규칙을 유지한다.
- 원본012/017, Smol d, 프로파일과 측정 raw, 외부 백업 및 runtime 7종을 보존한다. 과거 G2 대체와 정리의 세부 실행 근거는 checkpoints에서 읽는다.
- 현재 보류 범위는 위의 최신 사용자 결정에 따른다. 새 조건, 판정, 제어 동작, 이미지 배포와 범위 변경은 사용자 결정으로 다룬다.

## 다음 실행

1. 첫 시연의 시간축과 쉬운 설명을 작성한다. 원시 창, 수리 공백, 성공 응답 지연과 전체 요청 실패를 구분한다.
2. 준비된 용량이 느린 전력 전환을 기다리는 문제와 무부하 첫 구성 문제의 수정 선택지 2~3개를 제시한다. 사용자 결정 뒤 깨끗한 두 번째 시연을 진행한다.
3. 대시보드 관측과 삭제한 워크로드 이름 보존을 먼저 정리하고 WebSocket 라이브 무대 계획을 준비한다.
4. 문서와 배포 설정의 병합 준비를 병렬 수행한다. 전체 운영 배포 입력은 `charts/chill/values-operating.yaml` 하나를 `-f`로 사용한다. 이 파일은 rev74와 기본값의 차이 및 고정 runtime 정보를 담는다. `values-profiler.yaml`은 Profiler 전용 입력이며 두 profile은 별도로 다룬다. 운영 profile은 필요한 Secret 참조, 기존 PVC와 외부 환경 설정의 준비가 전제다. 이 문서는 배포 실행 승인을 부여하지 않는다.
5. 연구 단계는 E5 → M5 잔여 → P11 main 병합과 로컬 명세 이관 → 병합 main 제어 이미지 빌드 → runtime digest를 유지한 최종 배포 → P10 순서다. 보류 항목의 재개는 최신 사용자 결정으로 조율한다.

프로파일, runtime 또는 측정 계약의 변경은 같은 변경에서 영향받는 d, 비용, baseline 목록을 제공한다. 대표 1건의 측정부터 실제 계획기 소비까지 연결한 뒤 묶음 수집을 진행한다. 빌드, 테스트와 formatter는 측정 및 부하와 겹치지 않게 Main이 조율한다. 승인된 단계는 현재 계약으로 연속 실행하며 실제 새 조건, 판정 또는 범위는 사용자 결정으로 다룬다.

## 로컬 명세 보존과 P11

세 파일은 의도된 로컬 비공개 명세이며 Git 병합의 자동 전달 대상에서 제외된다. 이관의 source 디렉터리는 `/home/genesis1/ETRI/chill-pools/docs/spec/`, destination은 `/home/genesis1/ETRI/chill/docs/spec/`다. 보존본은 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/merge-preflight/spec-preserved/`의 같은 파일명이며, 정확한 경로와 절차의 정본은 같은 상위 디렉터리의 `spec-migration-manifest.json`, 설명은 `spec-transfer-report.txt`다.

| 파일 | manifest의 보존 내용 | P11 처리 |
|---|---|---|
| `00_measurement.md` | 11,569 bytes, SHA-256 `9e85ddd78affb07f2bacbddb13ede7911a5ec36550d21cdc8ea23e369f5e4e8c` | 측정 POOL, 점유 측정과 Evidence 계약을 검토해 source 내용을 이관한다. |
| `04_configuration_selection.md` | 7,620 bytes, SHA-256 `c63537d3b12661af63c6f05326d640fb9207fba3f131b930ca3897b31f332cd1` | 워크로드별 POOL, 용량 재생과 전체 POOL 비용 계약을 검토해 source 내용을 이관한다. |
| `05_transition_control.md` | 5,583 bytes, SHA-256 `8113e19fd14374ef5bd19e86e4bf546e3b2a1c0c44c2fa1130f075adf5bc334f` | 당시 양쪽 내용이 같으면 destination을 유지하고, 차이가 있으면 원문 검토 후 이관한다. |

위 보존 정보는 manifest의 **2026-10-24T12:58:59Z 기록**이다. destination 해시와 00/04 상이, 05 동일 판정은 **2026-10-23 사전 확인**이다. 실제 이관 완료 여부와 당시 해시는 P11 수행 결과로 기록한다.

P11에서는 다음 순서로 세 파일을 모두 확인한다.
1. 당시 source, 보존본과 destination을 읽고 변경분 및 destination 사용자 편집을 보존한다. source가 보존 시점 이후 바뀌었다면 검토 후 보존 정보도 갱신한다.
2. 00/04의 옛 공유 인스턴스 및 요청별 온라인 예측 문구를 워크로드별 인스턴스와 용량 재생 계약에 맞춰 해소한다. 검토한 source 내용을 정확한 destination 경로로 이관한다. 05는 위의 동일 여부별 절차를 따른다.
3. 최종 세 파일의 내용, 경로와 SHA-256을 대조하고 manifest에 수행 시각과 유지 또는 이관 결과를 기록한다. 세 파일은 로컬 비공개 정책을 유지한다.
4. 추적 파일의 Git 통합과 세 로컬 파일의 이관 완료를 각각 기록한다. 추적되는 `03_routing.md`는 Git 통합으로 전달하며 워크로드당 인스턴스 계약과의 일관성을 확인한다.

## 정본 진입점과 선별 근거

현재 계약은 저장소 설계 본문에서 읽고 외부 승인 및 원시 자료는 근거로 연결한다.

| 목적 | 정본 또는 선별 근거 경로 |
|---|---|
| 측정과 비용 계약 | `/home/genesis1/ETRI/chill-pools/docs/design/serving/measurement-api.md` |
| 구성 선택과 minimumGain | `/home/genesis1/ETRI/chill-pools/docs/design/serving/analytic-planner.md` |
| 실행 순서와 E5 계획 | `/home/genesis1/ETRI/chill-pools/docs/design/serving/workload-pools.md` |
| 평가 시나리오 7개 | `/home/genesis1/ETRI/chill-pools/docs/spec/README.md` |
| 원계획과 최종 수용 | `/home/genesis1/ETRI/chill-pools/docs/design/architecture-rethink/README.md`, 같은 디렉터리의 `05-recommendation-migration.md`, `07-validation.md`, `08-final-verification.md` |
| 조건 생성기와 승인 manifest | `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/gen.py`, `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/phase-a/measurement-generalization-20261024/approved/` |
| d의 record, identity와 원시 위치 | `/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-d-followthrough.json`, 같은 디렉터리의 `smol-systematic-bias-20261024.json` |
| 38단계 소비자 snapshot | `/home/genesis1/.cache/chill-assets/ops/pools-deploy/deployB/generalization-20261024/input-repair-055-20261024T122904Z/completion-20261024T124935Z/state.json` |
| 정확 비용 69개 연결 | `/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/cost-coverage-after-055.json` |
| 기존 소스 검증의 실제 결과 | `/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/source-verification-20261024/final-report.json`, 같은 디렉터리의 `commands.json` |
| rev74 배포 원형 | `/home/genesis1/.cache/chill-assets/ops/pools-deploy/deployB/control-b5711f1-141-20261025/helm-upgrade.stdout`, 같은 디렉터리의 `buildx-metadata-3dff4e789c3e.json`, `serving-startup.log` |
| 현재 배포와 정리 경과 | `/home/genesis1/.cache/chill-assets/ops/pools-deploy/deploy-log.md`의 rev74 및 E2E 시연 정리 완료 절 |
| 첫 시연과 정리 완료 | `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/demo-20261025-2/summary.md`, 같은 디렉터리의 `finish-complete.json`, `finish-definitions-complete.json` |
| PLC D06 새 규칙 소비와 반환 | `/home/genesis1/.cache/chill-assets/ops/pools-deploy/deployB/d25-121-20261025/genesis2-975-d06-occ-plc-cpu/`의 retry1 기록 |
| 로컬 명세 이관 정본 | `/home/genesis1/.cache/chill-assets/ops/pools-deploy/merge-preflight/spec-migration-manifest.json`, 같은 디렉터리의 `spec-transfer-report.txt`, `spec-preserved/` |

이 선별 목록은 조건 생성기, 승인 원문, manifest, profile/runtime/NodeUID/SHA와 원시 위치를 연결한다. 저장소와 증거 인덱스는 공개 가능한 선별 자료만 보존한다. `.token`, `before/chill-system-cm-secret.yaml`, 자격 증명을 담은 snapshot과 캐시 전체 묶음은 수록 대상에서 제외한다.
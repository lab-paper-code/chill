# CHILL workload-pools 인수인계

다음 세션은 세 원문을 먼저 읽는다.
- `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/north-star.md`
- `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/user-intent.md`
- `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/checkpoints.md`

## 현재 상태

목표는 mobilenet, SmolVLM2 VQAv2, PLC anomaly의 혼합 부하에서 워크로드별 SLO와 처리 결과를 확인하고, 전환을 포함한 전체 POOL 에너지를 정적 구성 및 제어요소별 결과와 비교하는 것이다. 현재 위치는 P7b(a)의 정확 비용 입력 보수와 rev72 BEFORE 수집이다.

**보고 기준: d 새 규칙 9/34, 정확 비용 69/69, 평가 시나리오 0/7.**
Smol d는 2026-10-24T11:42:19.448243Z 반환 확인을 포함한 `smol-d-followthrough.json` 기준이다. 비용은 CostCoverage24가 12:49:27.350175648Z 소비자 snapshot에서 69개 후보의 Node, 동작, 출발 및 목표 모드 참조를 연결한 결과다. 평가 7개는 실행 ID와 원시 결과가 아직 연결되지 않은 상태다. 새 보고는 같은 세 분모와 관측 시각, 근거를 함께 적는다.

- 운영 묶음의 비용 14개 요청과 172개 Accepted 기록, 새 프로파일 7개와 96개 trial은 확보됐다. 후보별 정확 비용 커버리지는 위의 69개 분모로 관리한다.
- Smol9는 모두 새 규칙 Accepted이며, 승인된 추가 8개는 과학 판정, 실제 소비자 선택, 실물 반환까지 완료됐다. 전체 d34는 새 규칙 9개, legacy 10개, 결측 15개다. D2 뒤 재측정 25개는 CPU8, GPU5, PLC12이며 legacy와 결측에 중첩된다.
- 배포는 rev72, 소스 `e18ac391d4353e5abba03558b163312f2a7b629b`다. 기존 검증 소스는 `a33e4fad057241ac7e341c952b6bdf4e05001ac6`, 승인 시점 문서 HEAD는 `ef2a45c73bec8e6f1f4c0b718ce7aa543e3d1385`다. 새 소스 편집은 검증 전이며 기존 검증의 실패, skip, 경고는 원 보고서에 보존한다.
- G2 실제 소비자 분류 뒤 ranking9 재측정 2 roots, 18 steps와 실제 최저선정의 조건부 직접 전환을 포함한 상한 4 roots, 22 steps가 승인됐다. 각 root의 raw, 계획기 소비와 정상 반환을 확인한다. 9개 대체 확인과 검증된 외부 백업의 실제 절대경로, 개수 및 목록 대조 보고 뒤 raw 80개 키와 terminal OCM 6개를 정리한다. old ranking9는 80개에 포함된다.

## 승인과 소유권

현재 계약은 저장소의 `measurement-api.md`, `analytic-planner.md`, `workload-pools.md` 본문이 정의하며 정확한 경로는 아래 정본 진입점에 연결한다. `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/deployment2-e5-proposal-20261024.md`는 승인 원문, `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/state-audit-execution-contract-20261024.txt`는 실행 근거로 보존한다. 사용자 12:20Z 결정으로 여섯 권고와 §10 방향이 승인됐다. #055의 실제 시각 12:27:17.194Z는 정확한 8 roots, 38 steps 범위에서 #054를 대체하고 나머지 hold를 보존한다.

| 담당 | 현재 소유권 |
|---|---|
| Main | 계획, 승인 경계와 증거를 감독하고 단계 전환 및 검증 실행을 조율한다. |
| SourceAlign24 | `/home/genesis1/ETRI/chill-pools`의 소스 통합과 git commit을 전담한다. 문서 담당은 합의한 파일만 편집한다. |
| CampaignOps | 번호와 UTC를 기록한 승인 범위의 live 변경을 전담한다. |
| D2Before24 | 외부 실행 스크립트와 binding을 관리한다. |
| ApprovalLedger24 | 외부 intent와 checkpoints의 승인 및 시간순 기록을 관리한다. |

작업과 도구는 `/home/genesis1/ETRI/chill-pools` 및 `/home/genesis1/ETRI/chill-pools/bin`을 사용한다. `/home/genesis1/ETRI/chill`은 bin까지 보존하며 main 이관은 P11에서 수행한다.

- B-only는 nominal 값, 동일 근거 상쇄, 전환 chain과 keep-infeasible을 보존한다. 13개 BEO idle fallback의 통계 CI 미포괄과 전역 95% 미보장 한계를 명시한다.
- d는 승인된 1%, 양측 95%, 여유 1.25, 요청당 idle 1ms, 본창 360초와 준비 60초 하한, 3600초 상한을 유지한다. signed 원시 값과 Accepted 판정, `max(d,0)` 투영을 보존한다. D04/D09/D14/D23/D33의 systematic profile-serving bias는 E5에 기록하며 새 정책은 M5 후보로 관리한다.
- 원본012의 Complete, raw와 LocalMinimum, 원본017의 16개 raw와 Measuring 상태를 보존한다. coherent 대체 2개의 실제 소비자 참조와 recordedJob 예방 규칙을 유지한다.
- fresh empty 10개와 승인된 ranking9 대체를 연결한다. 동일한 승인 조건의 legacy 또는 결측 대체, 대체 확인, 검증 백업과 옛 기록 삭제는 연속 실행한다. Timing 없는 소비 기록 0건 뒤 reader를 완결하고 첫 BEFORE 전에 공통 입력을 동결한다. fresh10 밖 baseline의 실제 소비는 보고 대상이며 native8 보존 실패 시 paired 비교를 중단한다.
- runtime7, 기존 자원 한도, evidence store와 나머지 hold를 유지한다. 진단 제어 rollout은 최대 1회, 최종 D2 rollout은 최대 1회다. 정량 SLO, m, G, κ, R, N은 D2 뒤 실측 제안으로 정한다.

## 다음 실행

1. rev72의 8 roots, 38 steps 원시 기록과 실제 소비자 연결을 보존하며 승인된 G2 대체, 검증 백업과 정확한 참조 정리를 완료한다. 원본012/017, Smol d와 runtime7을 보존한다.
2. 자율 BEFORE와 고정 native8 BEFORE를 수집한다. 배포 전 관측과 실제 Pod 및 container 보존 근거를 확보한다.
3. 유한 진단, D2, paired 및 자율 AFTER를 수행한다. 관측한 원인과 수정 효과, 미관측 한계를 구분한다.
4. d25와 G, m을 측정하고 정량 조건을 제안한 뒤 E5, M5를 수행한다.
5. P11에서 main과 로컬 명세를 이관하고 main 제어 이미지를 빌드한다. runtime을 유지한 최종 배포 뒤 P10을 수행한다.

프로파일, runtime 또는 측정 계약의 변경은 같은 변경에서 영향받는 d, 비용, baseline 목록을 제공한다. 대표 1건의 측정부터 실제 계획기 소비까지 연결한 뒤 묶음 수집을 진행한다. 빌드, 테스트와 formatter는 측정 및 부하와 겹치지 않게 Main이 조율한다. 승인된 단계는 현재 계약으로 연속 실행하며 실제 새 조건, 판정 또는 범위는 사용자 결정으로 다룬다.

## 로컬 명세 보존과 P11

`/home/genesis1/ETRI/chill-pools/docs/spec/00_measurement.md`, `/home/genesis1/ETRI/chill-pools/docs/spec/04_configuration_selection.md`, `/home/genesis1/ETRI/chill-pools/docs/spec/05_transition_control.md`는 내부 연구 명세의 의도된 로컬 보존 정책을 따른다. 보존 사본은 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/merge-preflight/spec-preserved/`에 두며, 기존 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/merge-preflight/spec-migration-manifest.json`이 경로, SHA256과 P11 절차를 관리한다.

P11은 세 파일의 보존본과 현재 원본 해시를 확인하고, 대상 파일의 변경 여부를 점검한 뒤 00과 04의 내용을 검토하여 이관한다. 05도 이관 확인 대상이며, 내용이 같으면 대상 파일을 보존하고 차이가 있으면 원문 검토 뒤 이관한다. 이관 뒤 세 파일의 내용과 해시를 대조하고 manifest에 실제 결과를 기록한다. 추적되는 `03_routing.md`는 git 통합 대상이다. 로컬 세 파일의 전달은 이 체크리스트로 명시적으로 확인한다.

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

이 선별 목록은 조건 생성기, 승인 원문, manifest, profile/runtime/NodeUID/SHA와 원시 위치를 연결한다. 저장소와 증거 인덱스는 공개 가능한 선별 자료만 보존한다. `.token`, `before/chill-system-cm-secret.yaml`, 자격 증명을 담은 snapshot과 캐시 전체 묶음은 수록 대상에서 제외한다.
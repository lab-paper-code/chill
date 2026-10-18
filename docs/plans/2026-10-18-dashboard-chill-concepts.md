# Dashboard에 CHILL 확장 개념 반영 계획 — 2026-10-18

## 목적과 판정 기준

`1132c50`(heterogeneous workloads 병합)과 그 뒤의 scheduler·runtime·device 변경으로 CHILL에
새 개념이 생겼다. 이 계획은 그 개념을 Dashboard에서 운영자가 "실험을 돌리고 이해하는 데"
필요한 만큼 반영한다. 기준은 다음과 같다.

- ChillServer API가 source of truth다. read API가 있는 개념은 Dashboard 작업으로 끝낸다.
  config·Helm 값만 있는 개념은 ChillServer read가 먼저 필요하므로 Phase 2의 선행 조건으로 분리한다.
- 화면 규칙은 기존과 같다: 한 화면 = 한 기능, 결론 제목, `›` 행, 하단 주 동작 하나,
  쉬운 한국어. 코드·UID·나노초는 원본 창에서만 보여준다.
- Dashboard는 특정 워크로드·모델을 알지 못한다. 입력·결과 화면은 전처리기 계약(필드 이름·형식·크기)만으로
  그리며, 샘플 이름이나 필드 의미에 따른 분기·문구를 두지 않는다.
- 자동 제어는 OFF를 유지한다. `loaded`를 실행기 준비·실제 서빙으로 표시하지 않는다.

근거 조사: SpecGap(DB/DP/WM/SCH 추적), ApiGap(ChillServer endpoint·필드·enum 대조),
ConceptMap(개념 사전). 아래 표의 `file:line`은 HEAD `3846183` 기준이다.

## 현재 상태

- 실제 클러스터는 ChillServer·Dashboard 모두 `merge-1132c50`이다. Preprocessor·Dataset·
  PowerModeTransitionCost CRD가 설치되어 있어 catalog 화면도 실제 검증이 가능하다.
- 이미 반영됨: workload·model·preprocessor·dataset CRUD와 호환 선택, JPEG/UTF8 단건 추론과
  단계 시간, 판단 알고리즘 선택·표시, compact scheduler 조회, `manual-static-routing` 이름,
  측정 요청·채택, 전체/활성 POOL 전력.

## Phase 1 — ChillServer API가 이미 있는 것 (Dashboard 작업만)

| # | 개념 | 지금 Dashboard | 반영할 것 | ChillServer 근거 |
|---|---|---|---|---|
| 1 | 실행 방식(runtime family/backend) | 실행 방식 편집이 family를 자유 입력으로 받고, backend는 원본 코드로 보임 (`features/management.tsx:170-173`) | family는 `firstfloor` 고정, backend는 파일 형식으로 고르게 한다(ONNX→`ort-cpu`/`ort-cuda`, RKNN→`rknnlite`). RKNN은 target platform `rk3588`, 선언 batch 크기, FP32 rank-3 입력 하나를 쉬운 말로 미리 안내한다. 상세는 "CPU에서 ONNX 실행"처럼 쓴다 | `internal/modelcatalog/catalog.go:107-123` |
| 2 | 측정된 실행 정보(runtimeMetadata: providers, device, gpuUUID, rknn) | `unknown` 타입, 원본 JSON만 있음 (`lib/read-types.ts:39`, `features/read-pages.tsx:178-193`) | 측정 결과 상세에 "실제로 쓴 실행 장치" 행(CPU/CUDA GPU/NPU). 원본은 창으로 둔다 | `internal/firstfloor/types.go:136-155` |
| 3 | 전력모드 actuator(`Fixed`, `JetsonNVPModel`, `NvidiaLockedGPUClock`) | `actuator: unknown`, 인벤토리 원본 JSON만 있음 (`lib/read-types.ts:6`, `features/resources.tsx:24-39`) | 장치 상세에 모드마다 "Jetson 전력 모드 N", "GPU 클럭 고정 a–b MHz", "고정"으로 표시. 측정 요청의 모드 선택에도 같은 설명을 붙인다 | `api/v1alpha1/deviceclass_types.go:34-88`, `internal/operatorconsole/inventory_view.go:26-52` |
| 4 | 인벤토리 모델 목록 | `models`·`modelPage`가 `spec: unknown`이고 렌더하지 않음 (`lib/read-types.ts:7`) | 하지 않는다. 모델은 serving `/models`가 이미 보여준다. 중복 화면은 만들지 않는다 | — |
| 5 | 프로파일 key(모델 산출물·runtime·전처리기·데이터셋 조건) | profile condition이 `unknown`이라 원본 JSON으로만 보임 (`features/scheduler.tsx:50-51`) | 채택 프로파일·후보 상세에 "어떤 조건으로 잰 결과인지" 행 4개(모델 파일, 실행 방식, 전처리기, 데이터셋). 같은 서비스인데 조건이 다른 프로파일을 구분해 보여준다 | `internal/serving/types.go:137-150`, `internal/profiling/derivation/curve_plan.go:74-126` |
| 6 | 판단 정책(`decision.policy`: 작업 이력, 후보 key, interval, 전환 kernel/projection) | 타입·화면 없음 (`lib/read-types.ts:49`) | "작업 이력 예측"으로 판단했을 때만 판단 상세에 "예측에 쓴 근거" 행을 보인다: 이력 구간, 비교한 구성 수, 전환이 판단 주기 안에 끝나는지. 계산 원본은 창으로 둔다 | `internal/serving/scheduler.go:234-249`, `internal/serving/scheduler_policy.go:17-28`, `internal/serving/scheduler_projection.go` |
| 7 | 작업 이력 예측 사유 코드 | 라벨이 없어 원본 코드로 보임 | `MissingHistoryConfiguration`, `ProjectionTransitionExceedsInterval`, `ExpectedEnergyOutsidePhysicalNumericalRange`, `ProjectionPoolInventoryMismatch`, `MissingOperationCost`, `RetainedLogicalConfigurationsExceedMaxCandidates`, `ProjectedWorkHistoryPending:<reason>`에 한국어 사유를 붙인다 | `internal/serving/scheduler_policy.go:53-55`, `scheduler_projection.go:31-33,92-100,191-210`, `kube/scheduler.go:354-356,400-404` |
| 8 | 설정 기반 요청 분배(`staticRouting`, `sourceConfigRV`) | 동작 이름만 있고 분배 내용은 원본에만 있음 (`features/scheduler-transitions.tsx:45-62`) | 변경 기록 상세에 "설정 revision N의 분배: 서비스 → 인스턴스 비율" 표와 실제 반영 결과를 나란히 보인다 | `internal/serving/kube/operations.go:66-76`, `kube/static_routing.go:13-42` |
| 9 | 완료 오버헤드 측정(`completionMeasurement`) | compact 복원은 되지만 타입·화면이 없음 | 인스턴스 상세에 "완료 처리 시간 측정: 구간·표본 수·값" 행 | `internal/serving/scheduler.go:62-88`, `observation_log_scheduler.go:290-303` |
| 10 | 서비스 준비 상태·사유 | `Preparing`, `Changing`, `AppliedDefinitionUnavailable`, `RestartDrain`, `RestartUnconfirmed`, `InstanceStopped`, `NodeDrained`, `NoCurrentReadiness`, `ConditionChangedBeforeDrain`가 "확인이 필요해요"로 뭉개짐 (`components/read-region.tsx:33-58`) | 사유마다 결론 문장과 다음 행동 링크를 붙인다 | `internal/serving/kube/workload_view.go:45-77`, `internal/serving/runtime.go:117-128,488-530,701-715` |
| 11 | 측정 결과 코드 | `MinimumFound/LocalMinimum`처럼 접두사가 붙은 결과와 `CurvePlanResolutionFailed`에 라벨이 없음 (`features/measurement-plans.tsx:27-36`) | 접두사가 붙은 원본 값과 실패 사유에 라벨을 붙인다 | `internal/profiling/campaign/controller/curve_deriver.go:22-24,258-268`, `internal/profiling/controller/reconciler.go:108-113,325-336` |
| 12 | ChillServer 오류 코드 | 일부만 처리하고 나머지는 일반 안내와 원본으로 보임 | serving 코드(`IncompatibleDataset`, `PreprocessFailed`, `BackendError`, `SLODeadlineDrop`, `RequestCanceled`, `AppliedStateUnconfirmed`, `EntrypointRestarted` 등)와 console `*Unavailable`/`*ProjectionFailed`를 표 하나로 매핑하고, 화면 공통 오류 영역에서 쓴다 | `internal/serving/kube/manager.go:340-341,418-426`, `internal/firstfloor/client.go:206-212`, `internal/operatorconsole/readapi/*.go` |
| 13 | 측정 사전 확인(preflight) 결과 | 응답 타입이 없어 원본 JSON으로만 보임 (`features/read-pages.tsx:137-169`) | `resolves`·`reason`을 결론 문장으로, `plan`은 "이렇게 측정해요" 요약으로 보인다 | `internal/operatorconsole/batch_energy_optimization_preflight_view.go:26-48` |
| 14 | 워크로드·모델에 특화된 문구 제거(일반성) | 샘플 필드 이름을 한국어로 바꾸는 표(`image`→"이미지", `question`→"질문", `answer`→"답", `label`→"분류 결과")와 VQA용 placeholder·"질문" 제목이 있음 (`features/management.tsx:50-52`, `features/inference.tsx:20-21,127,191`) | 필드 이름은 전처리기 계약의 이름을 그대로 쓰고, 한국어는 형식(JPEG→사진, UTF8→글)과 크기에만 붙인다. 출력이 여러 개면 계약 순서대로 이름·값 행으로 보이고, 글 출력이 하나일 때만 그 값을 결과 제목으로 쓴다(기존 일반 규칙). PLC·VQA·분류 등 특정 워크로드를 위한 표시 분기는 두지 않는다 | `internal/serving` 전처리기 계약(`input.fields`, `output.fields`) |
| 15 | 홈 시스템 상태 | 홈은 workload·scheduler·측정만 조회함 (`features/overview.tsx:48-65`) | `/systems`의 component 상태를 홈 첫 행 "시스템 상태"로 연결한다. `/readyz`는 relay에 열지 않는다(`/systems`로 충분) | `internal/operatorconsole/readapi/queries.go:87-117` |
| 16 | 설계 문서 불일치 | `docs/design/dashboard/README.md:261`이 "운영 전력 집계 API 미제공"이라고 되어 있음 | `GET /observations/power`의 실제 구현으로 고친다 | `internal/serving/httpapi/power.go:10-32` |
| 17 | 용어 | Dashboard 주석·문서·설정 필드가 백엔드를 `owner`로 부름(`lib/server-config.ts`의 `config.owner` 등 10개 파일) | `dad2aec`의 이름대로 ChillServer로 바꾼다. 환경 변수 `CHILL_BASE_URL`은 그대로 둔다. 동작 변경 없음 | `dad2aec refactor(serving): clarify ChillServer roles` |

### 작업 단위와 파일 소유

같은 파일을 두 단위가 고치지 않도록 나눈다. `lib/read-types.ts`는 통합 담당(Main 세션)이
먼저 타입을 한 번에 추가하고, 이를 계약 문서로 고정한 뒤 단위를 병렬로 시작한다.

| 단위 | 항목 | 소유 파일 |
|---|---|---|
| A. 카탈로그·실행 방식·일반화 | 1, 14 | `features/management.tsx`, `features/inference.tsx`, `lib/domain.ts` |
| B. 장치·측정 | 2, 3, 11, 13 | `features/resources.tsx`, `features/measurement-request.tsx`, `features/measurement-plans.tsx`, `features/read-pages.tsx` |
| C. 운영 판단 | 5, 6, 7, 8, 9 | `features/scheduler.tsx`, `features/scheduler-transitions.tsx`, `features/scheduler-profiles.tsx`, `features/scheduler-history.tsx` |
| D. 공통 상태·오류·홈 | 10, 12, 15 | `components/read-region.tsx`, `lib/labels.ts`(신규, 코드→문장 표 하나), `features/overview.tsx`, `lib/relay.ts` |
| 통합(Main) | 타입, 16, 17, 문서 | `lib/read-types.ts`, `lib/server-config.ts`, `docs/design/dashboard/README.md`, `chill-dashboard/README.md` |

## Phase 2 — ChillServer read가 먼저 필요한 것

Dashboard만으로는 할 수 없다. 측정 표를 채우는 데 필요한 것부터 순서를 매겼다. 각 항목은
ChillServer(백엔드 세션)가 최소 read를 낸 뒤 Phase 1과 같은 방식으로 화면을 붙인다.

| 순위 | 개념 | 지금 있는 곳 | 필요한 ChillServer read | 화면 |
|---|---|---|---|---|
| 1 | 장치별 후보 커버리지(어떤 서비스·실행 방식을 어떤 장치 종류에서 잴 수 있는지와 그 이유) | `internal/modelcompat`이 판정만 함. 인벤토리는 계산하지 않는다고 명시 (`internal/operatorconsole/inventory_view.go:70-72`) | 서비스·실행 방식 × DeviceClass 판정과 사유 코드(`internal/modelcompat/types.go:10-37`) | 측정 요청에서 고를 수 없는 장치에 이유를 보인다. "측정 가능 범위" 표 |
| 2 | 노드 runtime 선언·측정 정책 | Helm `profiling.executionDeclarations`·metric source·policy만 있음 (`charts/chill/values.yaml:110-160,284-288`) | 선언과 정책을 읽는 projection | 장치 상세의 "설치된 실행 방식", 측정 조건 설명 |
| 3 | 전환 비용 원본(PowerModeTransitionCost) | CRD는 있음. scheduler는 계산된 비용만 노출 | 원본 read(trial, J, 방향), 가능하면 측정 요청 | 전환 비용 상세(DB-FR-12/13 남은 부분) |
| 4 | 설정한 요청 분배 선언 | scheduler ConfigMap에만 있음. 실제 적용은 action으로 읽힘 | 선언 map과 revision read | 운영 설정에 "설정한 분배" 화면(읽기 먼저). 편집은 실험에서 필요해질 때 |
| 5 | 프로파일 catalog·원본 evidence | 채택된 것만 `/scheduler/profiles`에 있음 | 조건별 profile 목록과 원본 참조 | 모델 상세의 "조건별 측정 근거" |
| 6 | 그 밖의 DB SRS 항목 | 허용 자원·maintenance, `/experiments`, `/exports`, 통합 변경 feed, 요청 ID 단건 조회, loadgen 재생 실행 기록 | 각 ChillServer API | 측정 표가 요구할 때만 시작한다(AGENTS.md: 표나 실험에 쓰이지 않으면 미룬다) |

## 진행 방식 (이전과 같음)

1. `git status --short`로 다른 작업을 확인하고 보존한다. main 작업 트리에서 진행한다.
2. 통합 담당이 `lib/read-types.ts` 타입과 `local://dashboard-concepts-contract.md`(화면 문구
   원칙, 단위 경계, 라벨 표 위치)를 먼저 고정한다.
3. 단위 A–D를 subagent로 병렬 실행한다. 단위 중간에는 build·lint·test를 돌리지 않는다.
4. fixture 검증: port-forward로 ChillServer 실제 응답(scheduler compact, inventory, models,
   preprocessors, datasets, passport, preflight)을 `/tmp`에 받아 dev server fetch override로
   화면을 확인한다. 데스크톱과 390px, 다크 모드를 본다.
5. `npm exec --yes --package=node@24.21.0 -- make dashboard-check`를 한 번 돌린다.
6. 문서(16번, 설계 §6.1 표, `chill-dashboard/README.md`)를 같은 변경에서 고친다.
7. Dashboard 파일만 커밋한다. 배포는 `kubectl set image deploy/chill-dashboard ...`로만 한다.
   ChillServer 이미지·자원·evidence PVC는 배포 전후에 확인하고 건드리지 않는다. Helm upgrade는 쓰지 않는다.
8. 실제 브라우저에서 읽기만 검증한다. 설정 변경이나 제어 요청은 보내지 않는다.
   PLC 추론처럼 쓰기에 해당하는 검증은 사용자 승인 뒤에만 한다.
9. 결과를 보고한다: 화면별 변경, 근거, 실행한 명령, 실행하지 않은 검사, 작업 트리 상태,
   실제 클러스터 확인 여부.

## 하지 않는 것

- ChillServer 판정을 Dashboard가 다시 계산하지 않는다. 호환성 미리보기는 ChillServer 규칙을 그대로 옮긴
  것만 둔다(기존 원칙).
- 인벤토리 모델 목록 화면은 만들지 않는다(4번).
- 자동 제어 켜기, 정적 분배 편집, maintenance 요청 같은 변경 기능은 Phase 2 ChillServer API와
  측정 필요가 확인되기 전에는 만들지 않는다.

# CHILL 캠페인 진행 기록 — 실행 선언·상시 모델 등록과 설정 지속성

[현재 목표·규칙·상태와 다음 의존성](2026-10-13-scheduler-live-evidence-campaign.md)

아래는 기록 당시의 진행 상태다. 현재 상태는 캠페인 진입 문서를 따른다.

### 2026-10-13 — rasp5용 firstfloor Profiler 실행 선언 연결

**결과: 선언 연결 및 실제 production resolver 해석 검증 완료. 측정 캠페인은 미생성.**

- 이미지의 실제 ARM64 child manifest를 `rasp5` inspection Pod에서 실행하여 runtime
  producer JSON을 확보했다. ORT `1.27.0`, NumPy `2.2.6`, ARM64와 CPU provider,
  firstfloor module 및 worker executable을 확인했다.
- 선언의 image는 index가 아니라 runnable child:
  `155.230.35.213:5000/chill/chill-firstfloor@sha256:7b919a87721d2d0ff30da488a8b9ee215252c63b49facda1faaae32784b6da9a`.
- producer 출력을 `config/samples/firstfloor/ortcpu-arm64-runtime-declaration.json`에
  보존했다. 이 파일은 runtime 공급 사실이며 qualifying measurement evidence가 아니다.
- live `chill-system/chill-profiler-facts`의 `execution-facts.yaml`에
  `raspberry-pi-5-8g` / `firstfloor` / `ort-cpu` 항목을 추가했다.
  ConfigMap UID는 `3459e08f-8381-4ed6-917c-92606ee3cddc`,
  resourceVersion은 `344344987`에서 `350938567`로 변경됐다.
- 기존 선언 4개는 유지하여 전체 5개가 되었고, 다른 ConfigMap data 키도 유지했다.
  resourceVersion 전제조건이 있는 JSON Patch로 해당 data 키만 변경했다.

#### 발견한 schema 불일치와 수정

- 변경 전 실제 Console preflight는 HTTP 200이지만 `resolves=false`였으며,
  이유는 `unsupported Profiler execution facts schema "chill.dacs.io/profiler-execution-facts.v1alpha6"`였다.
- 현재 Go resolver는 v1alpha7을 요구하지만 live ConfigMap과 Helm renderer는
  v1alpha6을 사용하고 있었다. 진행 중인 측정 객체가 없음을 확인한 뒤 live envelope을
  v1alpha7로 변경하고, `charts/chill/templates/profiler_facts.yaml`도 v1alpha7로 맞췄다.
  이전 schema reader나 compatibility shim은 추가하지 않았다.
- pinned gopls `v0.23.0`의 references/definition과 반환된 source로
  execution declaration → candidate resolver → production curve-plan projector의
  소비 경로를 확인했다. Console preflight는 같은 profiling resolver/projector를 호출한다.

#### 실제 읽기·선택 검증

- 기존 manager 신원의 짧은 토큰과 검증된 CA HTTPS owner를 사용하여
  `firstfloor-declaration-rasp5-unit2`라는 임시 ModelSpec을 등록했다(HTTP 201).
  기존 immutable ONNX artifact를 참조하고 firstfloor CPU 경로만 선언했다.
  사용자 ModelSpec·Workload는 변경하지 않았다.
- 배포된 Console의 읽기 전용
  `POST /api/v1/namespaces/chill-system/batch-energy-optimizations/preflight`에서
  실제 candidate를 해석했다. HTTP 200, `resolves=true`, curve-plan v1alpha8의
  `runtimeFamily=firstfloor`, `backend=ort-cpu`, 정확한 위 runtime image를 확인했다.
- plan에는 실제 rasp5 Node UID, 기존 power metric source, 입력 tensor와 K=1,
  firstfloor maxBatchSize 2 / maxQueueSize 8이 연결됐다.
  선택만 검증했으며 이 계획을 측정 요청으로 제출하지 않았다.
- 기존 `mobilenet-v2-050` / standalone `ort-cpu` 경로도 같은 live preflight에서
  `resolves=true`를 확인했다.
- 임시 ModelSpec은 owner의 `If-Match` DELETE로 삭제했다(HTTP 204).
  후속 GET은 404였다. inspection Pod도 삭제했고, 측정 객체 수는 0이었다.

#### Helm gate와 desired manifest

| 입력 profile | 명령 | 결과 |
|---|---|---|
| default; release `chill`, namespace `chill-system`, values override 없음 | `make helm-lint helm-template HELM_RELEASE=chill HELM_NAMESPACE=chill-system HELM_VALUES=` | lint 통과; resources 29, valid 17, invalid/errors 0, skipped 12 |
| 별도 Profiler enabled; 기존 선언과 새 선언을 포함한 values, observer Node `genesis1`, API endpoint 명시 | `make helm-lint helm-template HELM_RELEASE=chill HELM_NAMESPACE=chill-system HELM_VALUES=/tmp/chill-firstfloor-declaration-unit2-profiler-render-values.yaml` | lint 통과; resources 42, valid 30, invalid/errors 0, skipped 12 |

- 같은 명시 입력의 `helm template` manifest를 보존했고, fact source가 v1alpha7 및
  선언 5개와 정확한 native image를 출력하는 것을 확인했다.
- Helm upgrade나 Deployment rollout은 수행하지 않았다. live 선언 추가는 ConfigMap
  patch이며 Helm release values에 자동 저장되지 않는다. 후속 upgrade에는 기존 항목을
  유지한 merged `profiling.executionDeclarations`를 명시적으로 포함해야 한다.
  현재 전체 5개 항목의 override snapshot은
  `/tmp/chill-firstfloor-declaration-unit2-preserved-values.yaml`에 보존했다.

#### 근거 위치와 남은 범위

- 로컬 보존 파일: `/tmp/chill-firstfloor-declaration-unit2-config-before.json`,
  `...-config-after.json`, `...-runtime.json`, `...-runtime.log`,
  `...-native-preflight.json`, `...-existing-preflight.json`,
  `...-model-response.json`, `...-model-cleanup.json`, `...-profiler-render.yaml`.
  각 `...`는 `/tmp/chill-firstfloor-declaration-unit2` prefix다.
  토큰·TLS private key는 출력하거나 파일로 저장하지 않았다.
- throwaway inspection Pod manifest와 preflight 입력 파일은 정리했다.
- 이번 저장소 변경은 Helm schema 한 줄, inspected declaration JSON,
  sample guide 및 이 세션 기록이다. 다른 세션의 Dashboard 작업은 보존했다.
- Go 동작·API·resource topology는 변경하지 않았으므로 Go lint/test·architecture·
  golden-query·deployment graph·e2e gate는 실행하지 않았다.
- Profiler controller의 실제 Run 실행·observer·원시 evidence 보존은 검증하지 않았다.
  검증한 것은 배포된 read-only preflight에서의 production profiling resolver 선택이다.
- 남은 선행조건은 상시 모델의 firstfloor 경로 등록과 정식 frozen 측정 요청·observer
  실행 연결이다. 기존 상시 ModelSpec과 Workload는 아직 이전 정의를 유지한다.

### 연속 실행 — 상시 native 모델 및 설정 지속성

- 사용자가 추가 승인 없이 의존성 순서대로 끝까지 실행하도록 승인했다.
  실제 Dashboard FULL 제어 표시·브라우저 e2e, 기존 API 우선, 필요한 코드의 작은
  커밋·ponytail 리뷰·리팩토링, 매 중간 검증의 `docs/spec/` 대조를 완료 조건에 반영했다.
- 상시 `mobilenet-v2-050-firstfloor`를 owner HTTPS로 등록했다(HTTP 201,
  UID `545ef2c0-b931-4c96-b978-b4cef1195210`). 기존 사용자 모델·Workload는 유지했다.
- 이후 다른 Helm 배포에서 live-only 선언이 v1alpha6으로 되돌아간 사실을 실제 preflight
  오류로 확인했다. 기존 release values를 보존하면서 native 선언을 Helm values에
  저장하고 schema v1alpha7을 적용했다. release revision 13의 upgrade가 통과했다.
- desired manifest에서 추가·삭제 리소스는 없었다. facts ConfigMap 및 이미 소스에 있는
  ModelSpec deadlinePolicy schema만 달라졌으며, node API endpoint·이미지는 유지했다.
  kubeconform: resources 72, valid 60, invalid/errors 0, skipped 12.
- production preflight가 다시 `resolves=true`와 정확한 native child image를 반환했다.
  선언의 Helm values 보존도 확인했다.
- 기존 active/idle 실행 policy 형식을 재사용하여 `firstfloor-ort-cpu` policy 두 개를
  추가했다. release revision 14 upgrade 통과. 현재 정식 Run을 실행하여 검증 중이다.
- schema/producer sample 변경의 ponytail 리뷰: `Lean already. Ship.` 리팩토링 추가 없음.
  default/Profiler Helm gate와 live resolver smoke 후 commit `873b6a0`
  (`fix(helm): align profiler facts with native execution declarations`)에 해당 3개 파일만 포함했다.
- 모델·선언 단계의 명세 근거: `docs/spec/00_measurement.md` 측정 단위·실패·Evidence
  역할과 `docs/spec/scheduler/SRS.md` SCH-08~12. 선언/preflight는 실행 준비의 근거이며,
  원시 측정·스케줄러 제어 통과로 확대하지 않는다.
- 근거: `/tmp/chill-native-campaign-model-registration.json`,
  `...-model-preflight.json`, `...-helm-checked-render.yaml`,
  `...-helm-checked-values.yaml`, `...-firstfloor-policy-values.yaml`.
  prefix는 `/tmp/chill-native-campaign`이다.


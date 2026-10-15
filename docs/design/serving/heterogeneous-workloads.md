# 이기종 워크로드 구현 설계

## 1. 목적과 설계 범위

[데이터셋과 전처리 관리 SRS](../../spec/catalog/SRS.md)의 `DP-01`부터 `DP-12`까지,
[워크로드 관리 SRS](../../spec/workloads/SRS.md)의 원시 표본과 전처리 참조 요구사항,
[스케줄러 SRS](../../spec/scheduler/SRS.md)의 측정 조건 요구사항과
[Dashboard SRS](../../spec/dashboard/SRS.md)의 전처리기와 데이터셋 흐름에 대응하는 구현 계약이다.
기준 명세는 [시스템 명세](../../spec/README.md), [측정의 단위](../../spec/00_measurement.md#측정의-단위),
[라우팅과 배칭](../../spec/03_routing.md)이다. 경계 결정은
[ADR 0021](../../adr/0021-raw-sample-preprocessing-boundary.md)에 기록한다.

목표 상태는 mobilenet-v2(이미지 분류)와 SmolVLM2-500M-Video-Instruct(VQAv2 이미지와 질문에 대한
짧은 답 생성)를 같은 POOL에서 하나의 서빙 경로로 동시에 제공하는 것이다. mobilenet은 같은 경로의
단순한 경우다. 기존 Workload 관리 계약은 [워크로드 관리 구현 설계](workload-management.md)를 따르며,
이 문서는 그 위에 더하는 부분만 정한다.

### 핵심 결정

| 결정 | 이유 |
|---|---|
| Preprocessor와 Dataset은 ModelSpec과 같은 cluster-scoped 정적 CRD | 기존 catalog 저장, 관리 API, 권한, 참조 보호 경로를 그대로 사용 |
| 원시 필드의 자료형은 BYTES 하나로 고정하고 선언에는 내용 형식과 크기 상한만 둔다 | 한 값만 허용하는 항목은 상수로 둔다. 요청 본문과 조회 응답은 `BYTES`를 표기 |
| 모델 산출물 하나가 여러 모델 파일을 가진다(`source.files`) | 실행 경로, 전처리 산출물의 대상, frozen 조건이 산출물 하나를 계속 식별 |
| 전처리 코드는 산출물 디렉터리의 Python module 하나 | 배치 입력 구성, 이미지 특징 결합, decode loop 같은 모델별 결합 코드를 산출물과 함께 고정. runner는 session 생성, 시간 측정, 결과 계약 검사, IPC를 소유 |
| 원시 입력은 gateway에서 한 번 검증하고 이후 경로는 구조만 확인 | 검증 경계를 하나로 유지 |
| 표본 하나의 전처리 실패는 runner가 보고하고 runner는 계속 실행 | `DP-05`, `WM-29` |
| 프로파일 조건은 모델 산출물, runtime, 전처리 산출물, 데이터셋 | 측정의 단위와 인스턴스 공유 조건 |
| 데이터셋 일치는 이름과 UID로 판단하고 내용 식별은 RunConfig에 고정 | 데이터셋 수정은 이후 측정부터 적용되고 기존 프로파일은 그대로 사용(`DP-10`) |
| `maxNewTokens`는 Preprocessor의 설정값이고 전처리 블록과 함께 Run에 고정 | serving과 profiling이 같은 frozen 조건에서 같은 값을 읽음 |
| 데이터셋 부분집합은 고정 디렉터리 `profiling`, `evaluation`이고 표본 추출 seed는 runtime image의 상수 | runtime image와 데이터셋 image가 frozen 조건이므로 두 값도 Run에 고정 |
| 측정은 firstfloor 경로만 사용 | 직접 ORT, RKNNLite runner 실행 경로는 삭제(5.6절) |
| 저장 계약은 이 branch에서 한 번씩 바꾸고 이전 객체는 삭제 | [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md) |

## 2. 현재 코드와 변경 경계

| 현재 근거 | 변경 |
|---|---|
| `ModelArtifactSource`는 파일 경로 하나(`api/v1alpha1/modelspec_types.go:45-58`), 실행 경로는 산출물 하나를 참조(`:117-130`) | `source.path`를 `source.files`로 바꾸고 실행 경로의 산출물 참조는 유지 |
| 모든 산출물이 `input.tensors`를 가진다(`modelspec_types.go:36-37`, `internal/modelcatalog/catalog.go:67-76`) | `input`은 RKNN 산출물에만 필요한 선언으로 한정(`catalog.go:110-113`의 규칙 유지) |
| firstfloor 경로는 BYTES를 거부(`catalog.go:122-126`, `internal/firstfloor/tensor.go:22-34`, `runtime/firstfloor/runner.py:16`) | 원시 필드 전달은 BYTES framing 하나로 수행 |
| `WorkloadSpec`은 모델 참조와 SLO만 보유(`api/v1alpha1/workload_types.go:12-16`) | `preprocessorRef`, `datasetRef` 추가 |
| 공유 변경 barrier는 모델 전용(`workload_types.go:54-64`, `internal/serving/kube/manager.go:23`, `:686-688`, `:690-766`) | 대상 kind를 `ModelUpdate`와 `PreprocessorUpdate`로 일반화 |
| gateway는 요청 tensor를 모델 경로 계약과 비교해 경로 index를 고른다(`internal/serving/input.go:33-62`, `internal/serving/runtime.go:217-246`) | 원시 입력 계약으로 검증하고 경로 index를 삭제 |
| 경로별 route와 `InputPath` 기록(`runtime.go:816-860`, `internal/serving/types.go:204`, `internal/serving/scheduler.go:99`, `:118`) | Workload별 route 하나 |
| runner는 `session.run` 한 번 실행, 오류는 batch 전체와 runner 종료(`runner.py:293-345`, `internal/firstfloor/scheduler.go:273-285`) | 전처리, 추론, 후처리를 한 실행 단위로 수행하고 항목 실패와 배치 오류를 결과 frame으로 반환 |
| IPC 완료 frame은 `service_ms`, `backend_ms`만 전달(`internal/firstfloor/ipc.go:22-33`) | 단계별 경과 시간, 첫 토큰 시간, 요청별 단위 수 추가 |
| 활성 측정은 seed 0 무작위 tensor를 한 번 만들고 반복 사용(`runtime/firstfloor/measurement.py:377-385`, `runtime/ortcpu/runner.py:794-828`) | 실행마다 데이터셋 프로파일링 부분집합에서 새 표본 추출 |
| artifact-copy는 파일 하나 복사(`internal/profiling/profiler/controller/execution_job_spec.go:143-153`, `internal/serving/kube/operations.go:360`) | 모델 파일 여러 개, 전처리 디렉터리, 데이터셋 부분집합을 같은 방식으로 복사 |
| 프로파일 조건은 모델 산출물, runtime, tensor, 자원과 프로토콜로 구성(`internal/profiling/derivation/curve_plan.go:94-113`) | 조건에 전처리 산출물과 데이터셋 추가 |
| 측정 worker는 직접 ORT, firstfloor, 직접 RKNNLite를 선택(`cmd/measurement-worker/run.go:122-170`) | firstfloor만 유지 |

기존 package만 사용한다. Preprocessor와 Dataset 선언은 `api/v1alpha1`,
순수 검증은 `internal/modelcatalog`, 저장과 참조 판정은 `internal/serving/kube`가 소유하며
이 방향은 이미 `.go-arch-lint.yml:185-188`, `:300-314`에서 허용한다.
`internal/serving`은 `serving.Definition`의 자체 타입으로 원시 입력 계약을 받는다.
`.go-arch-lint.yml`은 그대로 유지한다.

## 3. 카탈로그

### 3.1 Preprocessor

`edge.dacs.io/v1alpha1`의 cluster-scoped `Preprocessor`다. 정적 선언이므로 spec만 가진다.
다음 YAML은 저장 형태이며, 관리자는 3.6절의 HTTPS API로 등록한다.

```yaml
apiVersion: edge.dacs.io/v1alpha1
kind: Preprocessor
metadata:
  name: smolvlm2-vqa
spec:
  input:                       # 원시 입력 계약, 표본 1개의 모든 필드
    fields:
    - {name: image, format: JPEG, maxBytes: 4194304}
    - {name: question, format: UTF8, maxBytes: 1024}
  output:                      # 원시 출력 계약
    fields:
    - {name: answer, format: UTF8}
  maxNewTokens: 16             # 생성형 전처리만 선언
  artifacts:                   # MinItems=1
  - target: {model: smolvlm2-500m-video-instruct, artifact: onnx-fp32}
    source:
      image: registry/chill/smolvlm2-vqa-preprocessor@sha256:...
      architecture: arm64
      path: /artifact/smolvlm2-vqa   # preprocessor.py를 담은 디렉터리
```

- `format`은 `JPEG` 또는 `UTF8`이다. `maxBytes`는 입력 필드에 필수이고 1 이상이다.
  출력 필드는 이름과 형식만 가진다. 필드 이름은 계약 안에서 유일하다.
- 입력, 출력, 레코드 필드 이름은 runtime이 파일 이름과 key로 쓰므로 경로 요소 하나이며(`/`, `\`를 담지
  않고 `.`, `..`가 아님) 256 bytes 이하다. CRD pattern과 `MaxLength`가 이를 확인한다.
- `artifacts`는 CRD marker `MinItems=1`로 하나 이상이다. `artifacts[].target`은 `DP-02`의 대상 모델과
  모델 산출물이며 같은 대상을 가진 항목은 하나다.
- `source`는 `ModelArtifactSource`와 같은 의미의 carrier image, Kubernetes 아키텍처, clean absolute
  디렉터리 경로다(`DP-03`).
- `maxNewTokens`는 1 이상이며 생성형 전처리 코드가 사용하는 최대 생성 토큰 수다.

mobilenet은 Preprocessor 하나에 산출물 둘을 둔다. NCHW 디렉터리는 `canonical-onnx`, NHWC 디렉터리는
`mobilenet-v2-050-rk3588`을 대상으로 한다.

### 3.2 Dataset

```yaml
apiVersion: edge.dacs.io/v1alpha1
kind: Dataset
metadata:
  name: vqav2-val-subset
spec:
  source:
    image: registry/chill/vqav2-val-subset@sha256:...
    path: /artifact/vqav2-val-subset
  fields:                      # 레코드 필드 선언
  - {name: image, format: JPEG, maxBytes: 1048576}
  - {name: question, format: UTF8, maxBytes: 512}
```

- carrier 안의 배치는 고정 디렉터리 `<path>/profiling/<recordID>/<field>`와
  `<path>/evaluation/<recordID>/<field>`이다. 레코드 식별자는 디렉터리 이름, 필드 값은 파일 내용 그대로다.
- 두 부분집합의 분리는 자산 빌드 스크립트가 `question_id`로 레코드를 나누어 만들고 그 단위 테스트가
  확인한다(7장, `DP-06`). CHILL은 선언과 고정 디렉터리 이름만 사용한다.
- 데이터셋 carrier는 amd64와 arm64를 모두 담은 multi-arch image index digest를 사용한다. 복사
  init container는 이 image로 모든 노드에서 `sh`와 `cp`를 실행한다.

### 3.3 ModelSpec의 여러 모델 파일

```yaml
  artifacts:
  - name: onnx-fp32
    format: onnx
    source:
      image: registry/chill/smolvlm2-500m-onnx@sha256:...
      architecture: arm64
      files:
      - /artifact/vision_encoder.onnx
      - /artifact/embed_tokens.onnx
      - /artifact/decoder_model_merged.onnx
```

- `files`는 1개 이상의 clean absolute 경로이고 basename이 서로 다르다. 실행 위치에는
  `/model/<basename>`으로 전달한다. session key인 확장자를 뺀 이름(stem)도 서로 달라야 하고,
  RKNN 산출물은 `.rknn` 파일 하나다(`modelcompat.ValidateModelFileNames`).
- runtime은 모든 파일을 함께 적재한다. ORT는 파일마다 session 하나, RKNNLite는 파일 하나를 사용한다.
- `input`은 RKNN 산출물에만 있고 다른 형식의 산출물에서는 거부한다. RKNN delivery capability와 runner
  검사가 이 선언을 사용하고(`catalog.go:110-113`, `runner.py:248-256`), ONNX 산출물의 모델 입력은 전처리
  산출물이 정한다.
- 실행 경로의 runtime family는 `firstfloor`다(5.6절). `validateExecutionRuntime`의 직접
  `rknnlite`, `onnxruntime` family 분기를 삭제한다(`catalog.go:103-131`).

### 3.4 검증 위치

`internal/modelcatalog`에 순수 함수를 둔다.

| 함수 | 내용 | 요구사항 |
|---|---|---|
| `ValidatePreprocessor(p)` | 필드 이름 유일성, 형식과 상한, 대상 중복, source 형식 | `DP-01`, `DP-02`, `DP-03` |
| `ValidateDataset(d)` | source와 레코드 필드 | `DP-06` |
| `Compatible(d, p)` | 입력 계약의 모든 필드가 같은 이름과 형식으로 레코드 필드에 있고 레코드 상한이 계약 상한 이하. 불일치 필드 목록을 반환 | `DP-07` |
| `PreprocessorArtifact(p, model, artifact)` | 대상 모델 산출물의 전처리 산출물 선택 | `WM-08` |

Kubernetes 조회, 존재와 삭제 진행 여부는 관리자 경계(`internal/serving/kube`)가 확인한다.

### 3.5 Workload 참조와 적용 정의

```yaml
spec:
  modelRef: {name: smolvlm2-500m-video-instruct}
  preprocessorRef: {name: smolvlm2-vqa}
  datasetRef: {name: vqav2-val-subset}      # 선택
  slo: {percentile: 99, targetLatency: 3s}
status:
  appliedDefinition:
    workloadGeneration: 3
    modelRef: {name: ..., uid: ..., generation: 2}
    preprocessorRef: {name: ..., uid: ..., generation: 1}
    datasetRef: {name: ..., uid: ...}
    slo: {...}
  operation:
    kind: PreprocessorUpdate         # Update, Delete, ModelUpdate, PreprocessorUpdate
    targetCatalogUID: ...            # ModelUpdate 또는 PreprocessorUpdate의 대상
    targetCatalogGeneration: 4
```

- 참조 타입 하나(`CatalogReference{name}`)를 세 참조가 공유한다. `ValidateWorkload`는 세 이름의
  DNS 형식과 `preprocessorRef` 필수 여부를 확인한다(`WM-02`).
- `targetModelUID`, `targetModelGeneration`은 `targetCatalogUID`, `targetCatalogGeneration`으로 바꾼다.
  대상 kind는 `operation.kind`가 정한다.
- `serving.Definition`에 `Preprocessor{Name, UID, Generation, Input, Output, MaxNewTokens}`와
  `Dataset *{Name, UID}`를 더한다. 각 `ModelPath`는 해당 산출물의 전처리 산출물
  `Preprocessor{Image, Architecture, Path}`를 가진다. 참조 전처리에 전처리 산출물이 있는 실행 경로만
  snapshot에 넣는다(`WM-08`). `Identity()`에 전처리 UID와 generation을 더해 요청 기록이 수락 당시
  모델과 전처리 정의를 가리킨다(`WM-28`).
- 검증은 `validatedModel`(`manager.go:267-303`)을 `validatedReferences`로 넓혀 수행한다. 세 참조의
  존재와 삭제 진행 여부, 모델 검증, 진행 중 공유 변경과의 충돌, 데이터셋 참조 시 `Compatible`을
  확인하고 오류에는 해당 필드 경로를 담는다(`WM-06`).
- `UpdateWorkload`는 모델과 전처리의 UID와 generation이 같으면 SLO 변경과 같은 즉시 적용 경로를
  사용한다(`manager.go:490-496`). 데이터셋 참조 변경도 이 경로로 적용한다(`WM-16`).
- 조회 응답의 지원 입력은 적용 정의의 `preprocessor.input`과 `preprocessor.output`이다.
  `SupportedInput`(`internal/serving/kube/workload_view.go:17-22`)에서 `Tensors`를 삭제하고 경로별
  측정 근거 목록만 유지한다(`WM-04`).
- 데이터셋 참조가 없는 적용 정의는 runtime 상태 사유 `MissingDataset`을 가진다. 조회 응답은
  준비 대기와 데이터셋 없음 사유를 제공하고 신규 요청은 같은 사유로 제한한다(`WM-07`, `WM-09`).

### 3.6 관리 API와 권한

`internal/serving/entrypoint/handler.go:74-84`의 models 경로와 같은 형태로 다음 경로를 추가한다.

| 경로 | 동작 |
|---|---|
| `GET/POST /api/v1/preprocessors`, `GET/PUT/DELETE /api/v1/preprocessors/{id}` | 목록, 등록, 상세, 수정(`If-Match`), 삭제(`If-Match`) |
| `GET/POST /api/v1/datasets`, `GET/PUT/DELETE /api/v1/datasets/{id}` | 같은 형태 |

- 본문은 `{name, spec}`과 `{spec}`이다. 참조 Workload가 있는 Preprocessor 수정은 공유 변경을
  시작하고 `202`를 반환한다. 그 외 수정은 `200`이다.
- `kube.Store`에 kind별 Get, List, Create, Update, Delete를 `store.go:136-178`과 같은 direct client로
  추가하고, `kube.Manager`에 같은 잠금 안에서 동작하는 관리 메서드를 추가한다(`manager.go:826-1005` 형태).
- 미등록 식별자는 `404`, 검증 오류는 기존 `managementError`의 코드, 메시지, 필드로 반환한다(`DP-08`, `DP-09`).
- 권한: `charts/chill/templates/serving_entrypoint_rbac.yaml:19-21`의 owner resource 규칙에
  `preprocessors`, `datasets`의 `get, list, create, update, delete`를 더하고, manager
  `nonResourceURLs`(`:197-198`)에 `/chill/api/v1/preprocessors`, `/chill/api/v1/datasets`와 하위 경로를
  더한다. Profiler의 catalog 읽기 규칙(`charts/chill/templates/profiler_rbac.yaml:158-166`)에 두 resource의
  `get`을 더한다. 관리 변경 권한은 `WM-01`의 기존 SAR 경로를 그대로 사용한다.

### 3.7 참조 보호와 변경 절차

참조 판정은 `references`(`manager.go:854-859`)와 같은 세 근거를 사용한다. 원하는 spec의 참조 이름,
적용 정의의 UID, 진행 중 작업의 대상 UID다. 판정과 저장은 같은 관리 잠금 안에서 수행한다.

| 동작 | 처리 | 요구사항 |
|---|---|---|
| Preprocessor 등록과 수정 | `ValidatePreprocessor`, 대상 ModelSpec과 산출물의 존재, 대상 ModelSpec의 삭제 진행 여부 확인 | `DP-09` |
| 참조 중인 Preprocessor 수정 | 참조 Workload 전체의 데이터셋과 새 선언의 `Compatible` 확인 후 `PreprocessorUpdate` 공유 변경 시작 | `DP-10`, `WM-16` |
| Dataset 수정 | 참조 Workload 전체의 전처리와 `Compatible` 확인 후 저장. Serving 적용 정의는 그대로 유지하고 이후 측정이 새 선언을 고정 | `DP-10` |
| Preprocessor, Dataset 삭제 | 참조 Workload가 있으면 `PreprocessorReferenced`, `DatasetReferenced`와 Workload 목록으로 거부 | `DP-11` |
| ModelSpec 수정 | 새 선언에서 사라지는 산출물을 대상으로 하는 Preprocessor가 있으면 `ModelArtifactReferenced`와 Preprocessor 목록으로 거부 | `DP-12` |
| ModelSpec 삭제 | 해당 모델을 대상으로 하는 Preprocessor가 있으면 Workload 참조와 함께 Preprocessor 목록으로 거부 | `DP-12` |

`PreprocessorUpdate`는 `UpdateModel`(`manager.go:878-974`)과 같은 barrier를 사용한다. 관련 gate 닫기,
전체 드레인, 연결 해제, 닫힌 snapshot 게시, 전체 gate 열기 순서다. `sameGroup`은 kind와
`targetCatalogUID`, `targetCatalogGeneration`을 비교한다. `advanceGroup`은 적용 직전 Workload의
ModelSpec과 Preprocessor를 다시 읽고 작업 kind의 대상 identity를 확인한 뒤
`snapshot(w, model, preprocessor, dataset)`을 설치한다. 진행 중 공유 변경의 대상 Preprocessor를
새로 참조하는 등록과 수정은 `modelBusy`와 같은 규칙으로 충돌을 반환한다(`WM-18`).

## 4. 요청 경로

```text
gateway 수신 → 원시 입력 계약 검증(한 번) → Workload별 유입 큐 → 인스턴스 분배
  → firstfloor Workload UID별 큐 → 요청 수 n의 배치 선택
  → runner [전처리 → 추론 → 후처리] → 요청별 결과 또는 항목 실패 → 응답
```

### 4.1 원시 표본 요청

```json
{"inputs": [
  {"name": "image", "datatype": "BYTES", "data": "<base64>"},
  {"name": "question", "datatype": "BYTES", "data": "<base64>"}
]}
```

`InferenceInput.Inputs`는 `RawInput{Name, Datatype string; Data []byte}`이고 `encoding/json`이
base64를 직접 해석한다. `validateInput`(`input.go:33-62`)은 적용 정의의 원시 입력 계약으로
다음을 한 번 확인한다(`WM-26`).

1. 필드 이름 집합이 계약과 같고 각 이름이 한 번만 나타난다.
2. `datatype`이 `BYTES`다.
3. 길이가 1 이상이고 `maxBytes` 이하다.
4. `JPEG`는 시작 바이트 `FF D8 FF`를 확인하고 `image.DecodeConfig`로 헤더를 해석한다. 화소 복호는
   runner의 전처리가 수행한다.
5. `UTF8`은 `utf8.Valid`로 확인한다.

오류는 `InvalidInput`과 `inputs[i].data` 필드로 반환한다. gateway의 `MaxRequestBytes`는 계약 상한의
base64 크기를 담을 수 있게 운영 설정으로 정한다(`handler.go:138`).
`validated` 사건에 `ElapsedNanos`를 채워 수신부터 입력 검사 완료까지의 경과 시간을 기록한다
(`runtime.go:225`). 원시 계약은 Workload의 모든 실행 경로에 공통이므로 경로 index,
`RequestEvent.InputPath`, `SchedulerRequest.InputPath`, `SchedulerArrival.InputPath`, 경로별 route를
삭제하고 Workload별 route 하나로 분배한다. `ModelPath.Tensors`와 `sameInputs`도 삭제한다.
scheduler snapshot의 `routingCredits`는 Workload UID와 인스턴스 ID를 key로 가진다.

### 4.2 firstfloor 전달

native 요청과 profile 요청의 입력은 `Field{Name string; Data [][]byte}`다. 추론 요청은 원소 1개,
profile 요청은 원소 n개를 보낸다. native 설정 `Config`는 `Inputs []string`(원시 입력 필드 이름),
`Outputs []string`, `MaxNewTokens int`를 가진다. `prepareInputs`(`internal/firstfloor/server.go:41-68`)는
이름 집합과 원소 수를 확인하고 원소마다 `uint32 little-endian 길이 + 바이트`로 framing해 보관한다.
내용 검사는 gateway가 소유한다. `packBatch`(`scheduler.go:584-606`)는 framing된
원소를 그대로 이어 붙이고 tensor descriptor를 `{name, datatype: BYTES, shape: [n], bytes}`로 만든다.
`ProfileRequest.Executions`는 삭제하고 profile 호출 하나가 실행 하나다(`server.go:169`).

IPC frame은 다음과 같다. 길이 prefix와 JSON header 형식은 그대로다(`ipc.go:197-`).

| frame | header | payload |
|---|---|---|
| `init` | `inputs`(필드 이름), `outputs`(필드 이름), `batch_sizes`, `max_new_tokens` | 빈 payload |
| `infer` | `batch_id`, `tensors`(필드별 BYTES descriptor) | 필드 순서대로 framing된 n개 원소 |
| `output` | `batch_id`, `items`(요청 순서의 `{"units": k}` 또는 `{"error": "..."}`), `tensors`(출력 필드별 BYTES, `shape: [성공 수]`), `service_ms`, `preprocess_ms`, `backend_ms`, `postprocess_ms`, 생성형의 `first_token_ms` | 성공 요청의 출력 원소 |
| `batch_error` | `batch_id`, `error` | 빈 payload |
| `error` | 기존 runner 종료 오류 | 빈 payload |

Go `exchange`(`ipc.go:168-196`)는 framing, `items` 수가 batch 크기와 같은지, 결과를 나누는 데 필요한
출력 shape가 성공 수와 같은지만 확인한다. 나머지 결과 계약은 runner가 확인한다(4.4절).

### 4.3 실행 단위와 전처리 코드 계약

runner(`runtime/firstfloor/runner.py`)는 `--model-dir /model`의 모든 모델 파일로 session을 만들고
`--preprocessor <dir>`의 `preprocessor.py`를 import한다. 전처리 module은 다음을 제공한다.

```python
WARMUP_SAMPLE: dict[str, bytes]   # 원시 입력 계약의 모든 필드

def preprocess(samples: list[dict[str, bytes]]) -> tuple[object, list[int | str]]: ...
def infer(sessions: dict[str, object], inputs: object, max_new_tokens: int,
          first_token: Callable[[], None]) -> object: ...
def postprocess(outputs: object) -> list[dict[str, bytes]]: ...
```

- `preprocess`의 보고 목록은 요청 순서이며 성공은 단위 수(정수), 실패는 사유(문자열)다.
  모델 입력은 성공 요청만 순서대로 담는다(`DP-04`, `DP-05`).
- `sessions`의 key는 모델 파일 basename에서 확장자를 뺀 이름이다. ORT는 `InferenceSession`,
  RKNNLite는 초기화된 `RKNNLite`다. provider 고정, CUDA UUID 확인, thread와 affinity 검사는
  현재 runner 코드(`runner.py:139-265`)를 파일별 session 생성에 그대로 적용한다.
- 성공 요청이 있으면 runner는 `infer`와 `postprocess`를 순서대로 호출하고 각 단계를
  `perf_counter_ns`로 잰다. `infer` 구간이 `backend_ms`다.
- runner는 `first_token`이 호출된 시각으로 `first_token_ms`를 기록한다. 생성형 경로(`max_new_tokens > 0`)에
  성공 요청이 있으면 `infer` 동안 정확히 한 번의 호출이 필요하다.
- `postprocess` 결과 수는 성공 수와 같고 각 dict의 key는 원시 출력 계약의 필드 이름, 값은 bytes다.
- 초기화 시 runner는 initialized batch마다 `WARMUP_SAMPLE`을 batch 크기만큼 복제해 설정된 횟수만큼
  전체 실행 단위를 수행한다(`runner.py:285-290`의 위치). serving과 profiling은 같은 warmup을 수행하며
  횟수는 지금처럼 프로파일의 활성 프로토콜과 비교한다(`internal/serving/firstfloor/client.go:144`).
- 코드가 사용하는 라이브러리는 runtime image가 제공한다. import 실패나 필수 이름 누락은 ready 전의
  runner 시작 오류다. serving `firstfloor` container에 `terminationMessagePolicy: FallbackToLogsOnError`를
  두고, `PrepareInstance`의 Pod 실패 오류(`operations.go:394`)에 종료 메시지를 담아 실행 준비 오류와
  원인을 제공한다(`DP-03`).
- ready metadata의 `modelPath`는 `modelFiles`(실제 적재 파일 목록), `inputs`와 `outputs`는 원시 필드
  이름이 된다. 관측 비교(`internal/serving/kube/observer.go:371`, `internal/serving/kube/cost_power.go:253-255`)도
  이 값을 사용한다.

### 4.4 항목 실패와 배치 오류

runner는 module 호출 결과를 검사하는 유일한 경계다.

| 상황 | runner | actor와 응답 |
|---|---|---|
| 일부 요청의 전처리 실패 | `items`에 사유를 담고 나머지 요청을 실행 | 실패 요청은 HTTP `422`와 `progress.outcome = "preprocess_failed"`, 나머지는 결과와 단위 수 |
| 보고 수가 n과 다름, 성공 단위 수가 1 미만, 출력 수나 필드 불일치, 생성형 경로에서 `first_token` 호출이 없거나 두 번 이상, module 호출 중 예외 | `batch_error` frame을 보내고 다음 frame을 기다림 | 해당 배치의 모든 요청을 `outcome = "batch_error"`로 종료. 대기 요청과 runner는 유지 |
| IPC framing 위반, runner 종료, timeout | 기존 `error` frame 또는 종료 | 기존 `fail` 경로(`scheduler.go:273-285`) |

`finishBatch`(`scheduler.go:544-583`)는 `items`로 성공 요청과 실패 요청을 나누고 `splitOutputs`는 성공
요청에만 출력 원소를 나눈다. serving adapter는 `422`를 gateway 오류 코드 `PreprocessFailed`로,
`batch_error`를 `BackendError`(HTTP `502`)로 기록하고 요청 응답에 사유를 담는다(`WM-29`).

### 4.5 생성형 실행

생성은 greedy, static batching이다. SmolVLM 전처리 module의 `infer`는 `vision_encoder`, `embed_tokens`,
KV cache를 쓰는 `decoder_model_merged`를 greedy로 실행하고, 첫 step 뒤 `first_token()`을 한 번
호출하며, 모든 요청이 EOS를 내거나 `max_new_tokens`에 도달하면 끝낸다. 세 파일의 입출력 이름은
자산 commit에서 ONNX graph로 고정한다. `max_new_tokens`는 Preprocessor의 `maxNewTokens`이며 frozen
조건에서 native 설정으로 전달한다. 비생성형은 0이고 `first_token_ms`를 생략한다.

단위 수는 mobilenet 1, SmolVLM은 요청의 실제 이미지 타일 수다. actor의 batch 선택(`ChooseBatch`,
`serviceCost`, `internal/firstfloor/policy.go`)은 요청 수 n을 그대로 사용하고, 단위 수는 분석용
관측값으로만 기록한다(`SCH-06`).

### 4.6 관측 항목

| 위치 | 추가 필드 | 의미 |
|---|---|---|
| native `Progress`(`internal/firstfloor/types.go:117-133`), `BackendProgress`(`internal/serving/types.go:258-277`) | `preprocessMS`, `postprocessMS`, `firstTokenMS`(생성형), `units`(성공 요청) | 배치 단계 시간은 같은 `batchID`의 요청이 공유하고 단위 수는 요청별 |
| 기존 필드 | `backendMS`는 추론 단계, `serviceMS`는 runner 교환 전체 | 의미 유지 |
| `RequestEvent` `validated` | `elapsedNanos` | gateway 입력 검사 경과 시간 |
| native `ProfileResponse`(`types.go:211-222`) | `units`, `preprocessSamplesMS`, `postprocessSamplesMS`, `firstTokenSamplesMS` | profile 실행의 같은 값. `outputs`는 삭제 |

serving adapter의 진행 복사(`client.go:176-178`)와 observer journal이 새 필드를 그대로 보존한다(`WM-28`).
요청 조회 API는 journal 사건을 그대로 반환하므로 Dashboard가 이 필드를 표시한다.

### 4.7 Serving 인스턴스 준비

`PrepareInstance`(`operations.go:360-362`)의 init container는 셋이다.

1. `artifact`: 모델 carrier에서 `cp "$@" /chill-artifact/`로 모든 모델 파일 복사, `/model`에 읽기 전용 mount.
2. `preprocessor`: 전처리 carrier에서 `cp -R <path> /chill-preprocessor/`로 디렉터리 복사, `/preprocessor`에
   읽기 전용 mount.
3. `profile`: 기존 그대로.

복사 원본은 모두 프로파일의 frozen 조건이다. `samePreparedPod`(`operations.go:312-322`)와 observer의
Pod 형태 검사(`observer.go:412-432`, `:477`)를 세 init container와 두 artifact volume에 맞춘다.
native 설정의 `Inputs`, `Outputs`, `MaxNewTokens`는 조건의 전처리 블록에서 만든다
(`cmd/serving-entrypoint/profiles.go:551`).

### 4.8 측정 조건 일치와 공유

`profilePath`(`runtime.go:768-790`)는 기존 조건에 다음을 더해 비교한다.

- 경로의 전처리 산출물 `{image, architecture, path}`와 적용 정의의 전처리 이름, 입력과 출력 필드 이름,
  `maxNewTokens`가 조건의 전처리 블록과 같다.
- 적용 정의의 데이터셋이 있고 그 UID가 조건 `activeProtocol.dataset.uid`와 같다.

`MatchesProfile`을 쓰는 scheduler의 `CompatibleUIDs`(`internal/serving/kube/scheduler.go:389-408`)와
catalog의 조건별 최신 선택(`internal/serving/kube/catalog.go:154-180`)은 조건 직렬화로 새 항목을 자동
포함한다. 인스턴스 묶음 key `schedulerOptionCondition`(`internal/serving/scheduler_candidates.go:494-500`)에
`Condition.Preprocessor`와 `Condition.ActiveProtocol.Dataset`을 더한다. 네 조건이 같은 Workload만
인스턴스와 프로파일을 공유한다(`SCH-09`, `SCH-AC-23`).

## 5. 프로파일링

### 5.1 측정 요청

`MeasurementStateSpec.candidate`(`api/v1alpha1/profilingrun_types.go:47-73`)에 `preprocessor`를 더한다.
활성 측정의 `ProfilingRunMeasurementSpec`과 campaign `ActiveProtocolSpec`
(`api/v1alpha1/batchenergyoptimization_types.go:51-68`)은 `inputGenerator`, `outputValidation`을 삭제하고
`dataset`(이름)을 가진다. 이 필드는 활성 측정에만 있으며 CEL 규칙(`profilingrun_types.go:74`)이 모드별
존재를 확인한다. campaign의 child Run 생성(`internal/profiling/campaign/controller/run_builder.go:19`,
`:70`, `:83`)은 활성 Run에 이 값을 복사한다.

Workload를 선택한 측정 요청은 Dashboard가 Workload의 모델, 전처리, 데이터셋 참조로 위 필드를 채운다
(`DB-FR-12`, `WM-07`). API는 catalog 이름을 직접 받는다.

### 5.2 고정 조건

| 기록 | 변경 |
|---|---|
| `RunConfig.State`(`internal/profiling/run/types.go:96-106`) | `ArtifactPath`를 `ArtifactFiles []string`으로 바꾸고 `Preprocessor{Name, UID, Image, Architecture, Path, MaxNewTokens, Inputs, Outputs}` 추가 |
| `RunConfig.Measurement`(`types.go:161-170`) | `InputGenerator`, `OutputValidation` 삭제. 활성 측정에 `Dataset{Name, UID, Image, Path}` 추가 |
| `CurvePlanSpec`(`internal/profiling/derivation/curve_plan.go:94-113`) | `Artifact.Path`를 `Files`로 바꾸고 `Tensor`를 삭제하며 `Preprocessor{Name, Image, Architecture, Path, MaxNewTokens, Inputs, Outputs}` 추가 |
| `CurveProtocolContract`(`curve_plan.go:58-71`) | `InputGenerator`, `OutputValidation` 삭제. 활성 프로토콜에 `Dataset{Name, UID}` 추가 |

`KubernetesCandidateResolver`(`internal/profiling/profiler/kube/candidate_resolver.go:16-21`)는 Preprocessor와
Dataset을 같은 cluster-scoped 읽기로 가져오고, 선택한 모델 산출물을 대상으로 하는 전처리 산출물을
고른다. Run 고정(`internal/profiling/profiler/preparation.go`)은 그 산출물의 아키텍처가 대상 Node와 같고
데이터셋과 전처리가 `Compatible`인지 확인한다. 데이터셋 수정은 이후 고정하는 Run부터 반영된다(`DP-10`).

### 5.3 Job 전달

측정 Job(`execution_job_spec.go:124-126`)의 init container는 모델 파일 복사, 전처리 디렉터리 복사,
활성 측정의 데이터셋 `<path>/profiling` 복사(`/dataset`으로)다. 모두 `artifactContainer`(`:143-153`)와
같은 `sh -ec cp` 형태다. measurement worker가 Python에 넘기는 요청(`firstfloor-runtime-request`)은
`modelDirectory`, `preprocessorPath`, `datasetPath`를 가진다.

### 5.4 실행마다 표본 추출

`runtime/firstfloor/measurement.py`는 `/dataset`의 레코드 디렉터리를 이름순으로 정렬하고 상수
`SAMPLE_SEED = 0`의 `random.Random`으로 측정 실행마다 `rng.sample(records, n)`을 뽑는다. runtime image가
frozen 조건이므로 seed도 Run에 고정된다. 상수 옆에 `# ponytail: seed는 상수다. seed를 바꾸는 실험이
생기면 활성 프로토콜 설정으로 옮긴다.`를 둔다. 요청 본문은 전처리 입력 필드 파일만 읽어 만든다.
다음 실행의 본문은 시간 측정 밖에서 준비한다. 공통 측정 loop(`runtime/ortcpu/runner.py:1108-1111`)는
타이머 시작 전에 `session.prepare_next()`를 호출하고 `FirstfloorSession`이 이를 구현한다.
`generate_inputs` 호출(`measurement.py:377-380`)과 `prepare_inputs`(`:254-261`)의 고정 본문은 삭제한다.
warmup은 runner의 `WARMUP_SAMPLE` 실행이고 측정 표본은 데이터셋에서만 뽑는다. 측정 표본의 전처리
실패는 해당 trial의 오류다.

### 5.5 원시 trial

`NativeServiceSample`(`internal/profiling/run/evidence.go:162-168`)과 derivation 사본
(`internal/profiling/derivation/types.go:31-37`)에 `recordIDs []string`, `units []int`,
`preprocessMs`, `postprocessMs`, 생성형의 `firstTokenMs`를 더한다. `native_sample`
(`measurement.py:107-124`)이 이 값을 채우고, 런타임 결과 검증은 두 배열 길이가 batch 크기와 같은지
확인한다. 출력 계약 검사는 runner가 소유한다(4.4절).
projection과 derivation은 값을 복사해 보존한다. 측정량은 기존 n별 배치 실행시간 분포이며
단위 수는 분석용 관측값이다.

### 5.6 직접 runner 실행 경로 삭제

[측정의 단위](../../spec/00_measurement.md#측정의-단위)는 프로파일링과 Serving이 같은 firstfloor/backend
경로를 사용하고 모든 활성 측정이 데이터셋 표본으로 전처리부터 후처리까지 실행하도록 정한다.
Serving Pod는 `serving-worker`만 실행하고(`operations.go:362`) 직접 runner 프로파일은 serving 경로와 다른
독립 identity를 가진다(`run_config.go:294-296`). 직접 runner에 표본 추출과 전처리를 다시 구현하면 같은 측정
경로가 세 개가 된다. 따라서 다음 실행 경로를 삭제한다.

- Go: `IsSupportedCPUORTRunConfig`, `IsSupportedActiveRunConfig`, `IsSupportedRKNNLiteRunConfig`와 evidence의 해당
  분기(`run_config.go:265-292`, `evidence.go:314-330`, `idle_evidence.go:158-175`), `worker.NewORTCPURuntime`,
  `worker.NewRKNNLiteRuntime`(`internal/profiling/profiler/worker/ort_cpu.go`, `rknn_lite.go`), measurement worker의
  해당 선택과 옵션(`cmd/measurement-worker/run.go:122-170`, `options.go:15-16`, `:77`).
- Python: 직접 실행의 요청 처리, `main`, `generate_input(s)`(`runtime/ortcpu/runner.py:794-828`,
  `runtime/rknnlite/runner.py:848-875`)와 해당 테스트. firstfloor가 가져다 쓰는 helper
  (`measurement.py`의 `common.*`, `runner.py`의 `read_cpu_environment`와 RKNN delivery helper)는 library로 유지한다.
- image: `build/docker/measurement-worker.Dockerfile`, `build/docker/rknnlite-measurement-worker.Dockerfile`.
- Makefile: `MEASUREMENT_WORKER_IMG`(`Makefile:14`), `RKNNLITE_MEASUREMENT_WORKER_IMG`(`:18`),
  `docker-build-measurement-worker`(`:359-361`)와 `docker-build-all`의 항목(`:386`),
  `docker-push-measurement-worker`(`:419-421`)와 `docker-push-all`의 항목(`:424`), 두 platform 변수(`:433-434`),
  `docker-buildx-measurement-worker`(`:474-478`), `docker-buildx-rknnlite-measurement-worker`(`:480-484`),
  `docker-buildx-all`의 항목(`:505`). `cmd/measurement-worker` binary 빌드(`:320`)는 firstfloor image가
  사용하므로 유지한다.
- `hack/fast-deploy.sh`의 measurement-worker component: image와 platform 변수(`:18`, `:22`), 사용법(`:62`, `:87`),
  component 목록(`:237`), image 선택(`:361-362`, `:389-390`), 출력(`:442`, `:446`, `:837`), 빌드(`:614-618`, `:687-692`).
- Profiler 값: `charts/chill/values-profiler-arm64.yaml:80`, `:83`, `:86`의 `onnxruntime` runtime 선언과
  `:89`의 `rknnlite` runtime 선언.
- 실행 경로 sample: `config/samples/catalog/edge_v1alpha1_modelspec.yaml:58-66`의 `ort-cpu`, `rknnlite` 경로와
  ModelSpec 실행 경로의 직접 family 검증(3.3절).

## 6. Dashboard

| 화면과 모듈 | 변경 | 요구사항 |
|---|---|---|
| relay 허용 목록(`chill-dashboard/lib/relay.ts:47-48`) | `(models|workloads|preprocessors|datasets)` 목록과 항목 경로 | `DB-FR-28` |
| 관리 화면(`chill-dashboard/features/management.tsx:16`) | `Kind`에 두 kind 추가. Preprocessor 편집은 입력과 출력 필드 표, 대상 모델과 산출물 선택이 있는 산출물 목록, `maxNewTokens`. Dataset 편집은 source와 레코드 필드. 상세는 참조 Workload, 변경 영향, 삭제 거부 사유를 기존 `Impact`, `Problem`으로 표시 | `DB-FR-28`, `DB-FR-07` |
| 모델 화면 | `source.files` 목록, RKNN 산출물의 `input`, 산출물을 대상으로 하는 Preprocessor | `DB-FR-04` |
| Workload 편집 | 전처리기 선택(필수), 데이터셋 선택(선택), 데이터셋 없음 준비 대기 안내, 전처리 변경의 드레인 안내 | `DB-FR-05`, `DB-FR-06`, `DB-FR-07` |
| 도메인 타입(`chill-dashboard/lib/domain.ts:6-17`) | ModelSpec files, Preprocessor, Dataset, Workload 참조와 `targetCatalog*` | 위 전체 |
| 측정 요청(`chill-dashboard/features/measurement-request.tsx:22-25`, `:145`) | Workload 선택으로 모델, 전처리기, 데이터셋 채움. `inputGenerator`, `outputValidation` 입력 삭제 | `DB-FR-12` |
| 단일 추론(`chill-dashboard/features/inference.tsx`) | 원시 입력 계약에서 JPEG 필드는 파일 선택, UTF8 필드는 텍스트 입력. base64 본문 생성, 출력 필드를 형식대로 표시 | `DB-FR-08`, `DB-FR-09` |
| 요청 기록과 측정 근거 화면 | 요청별 `preprocessMS`, `backendMS`, `postprocessMS`, `firstTokenMS`, `units`, 입력 검사 경과 시간. 원시 trial의 `recordIDs`, `units`, 단계 시간 | `DB-FR-12`, `DB-FR-22` |

## 7. 자산과 평가 부하

| 자산 | 위치와 내용 |
|---|---|
| carrier Dockerfile | `build/docker/derived-model-artifact.Dockerfile`을 build context의 `artifact/` 디렉터리 전체를 `/artifact/`로 복사하는 형태로 바꾸고 모델, 전처리, 데이터셋 carrier에 공통 사용. 데이터셋 carrier는 multi-arch로 빌드 |
| SmolVLM ONNX 산출물 | HF `HuggingFaceTB/SmolVLM2-500M-Video-Instruct`의 고정 revision에서 `onnx/vision_encoder*.onnx`, `onnx/embed_tokens*.onnx`, `onnx/decoder_model_merged*.onnx` 중 한 변형 세 파일. 산출물 하나, 실행 경로 `firstfloor-ort-cpu` |
| mobilenet 전처리 | Preprocessor `mobilenet-imagenet` 하나의 두 산출물 디렉터리 `build/model-artifacts/preprocessors/mobilenet-nchw/`(`canonical-onnx` 대상), `mobilenet-nhwc/`(`mobilenet-v2-050-rk3588` 대상). JPEG 복호, 짧은 변 256 resize, 224 center crop, ImageNet 정규화. 단위 수 1, 출력 `label`(UTF8 class index), `WARMUP_SAMPLE`은 module이 import 시 메모리에서 만드는 작은 JPEG |
| SmolVLM 전처리 | Preprocessor `smolvlm2-vqa`의 산출물 디렉터리 `build/model-artifacts/preprocessors/smolvlm2-vqa/`. `SmolVLMProcessor`(이미지 분할, longest edge 2048, 타일 512, 타일당 64 토큰)와 chat template, 4.5절의 decode loop, tokenizer와 processor 설정 파일. 단위 수는 타일 수, 출력 `answer` |
| VQAv2 부분집합 | `build/model-artifacts/vqav2/build_subset.py`가 `lmms-lab-encoder/VQAv2` validation split의 고정 revision에서 고정 seed로 레코드를 골라 `question_id`로 나누어 `profiling/<question_id>/{image,question}`, `evaluation/<question_id>/{image,question}`을 만든다. 부분집합은 고정한 shard 하나(`validation-00000-of-00068`)에서만 뽑아 전체 split을 내려받지 않는다. 단위 테스트가 두 디렉터리의 `question_id` 분리를 확인한다. 같은 Dataset이 두 Workload에 쓰이며 mobilenet 전처리는 `image` 필드만 사용 |
| runtime image | `build/docker/firstfloor.Dockerfile:18-31`에 `pillow`와 SmolVLM 지원 transformers 고정 버전을 더한다. 로컬 환경의 transformers는 4.46.3이므로 SmolVLM processor를 처음 포함한 release 이상으로 고정한다 |
| 등록 sample | `config/samples/catalog/`에 두 ModelSpec, 두 Preprocessor(`mobilenet-imagenet`, `smolvlm2-vqa`), Dataset, `config/samples/manual/`에 두 Workload. 등록 안내는 `config/samples/catalog/README.md` |
| 평가 부하 | `load-generator`의 `WorkloadHTTPClient.infer`(`load-generator/src/chill_loadgen/http_client.py:96-152`)를 원시 필드 본문으로 바꾸고, CLI(`cli.py:12-25`)에 `replay --workload ID --dataset-dir DIR --fields image,question --rate R --duration S`를 추가한다. `DIR`는 데이터셋의 `evaluation` 디렉터리다. 레코드를 이름순으로 순환하며 `run_schedule`(`schedule.py:41-67`)의 고정 간격 open-loop로 보내고 요청마다 record ID, 상태, `X-Chill-Request-ID`를 JSONL로 출력한다. 이기종 혼합 부하는 Workload별 replay 프로세스를 동시에 실행해 만든다 |

## 8. 저장 계약

한 branch가 하나의 작업 단위다. 아래 version은 이 branch에서 처음 바꾸는 commit에서 한 번 올리고,
이후 commit은 같은 미배포 version의 형태를 이어서 바꾼다. 진행 중 campaign이 끝난 뒤 merge하고, merge 시점에
정리 대상을 삭제한 뒤 다시 등록하고 측정한다.

| 계약 | 현재 | 이 branch의 값 | 정리 대상 |
|---|---|---|---|
| ModelSpec CRD | `source.path`, 모든 산출물의 `input` | `source.files`, RKNN 산출물의 `input` | 모든 ModelSpec 삭제 후 재등록 |
| Workload CRD | `modelRef`, `slo`, `targetModel*` | `preprocessorRef`, `datasetRef`, 적용 참조, `PreprocessorUpdate`, `targetCatalog*` | 모든 Workload 삭제 |
| Preprocessor, Dataset CRD | - | `v1alpha1` 신규 | - |
| ProfilingRun, BatchEnergyOptimization, ModelLoadedIdleBaseline CRD | `inputGenerator`, `outputValidation` | `candidate.preprocessor`, 활성 측정의 `dataset` | 모든 객체 삭제 |
| `RunConfigSchemaVersion`(`internal/profiling/run/types.go:14`, Python `RUN_CONFIG_SCHEMA`) | `profiling-run-config.v1alpha16` | `v1alpha17` | RunConfig를 담은 모든 Run과 evidence |
| `CandidateReportSchemaVersion`(`internal/profiling/run/candidate.go:19`) | `modelcompat-report.v1alpha12` | `v1alpha13`(산출물 files) | 저장된 candidate report |
| `curvePlanSchemaVersion`(`internal/profiling/derivation/curve_plan.go:20`) | `curve-plan.v1alpha8` | `v1alpha9` | Evidence Store의 `curve-plan.json` |
| `RuntimeResultSchemaVersion`(`internal/profiling/run/evidence.go:19`, Python `RESULT_SCHEMA`) | `batch-characterization.v1alpha7` | `v1alpha8`(native sample 필드, 원시 필드 이름 `inputs`, firstfloor `modelFiles`) | 활성 raw evidence |
| `IdleRuntimeResultSchemaVersion`(`internal/profiling/run/idle_evidence.go:14`, Python `IDLE_RESULT_SCHEMA`) | `model-loaded-idle-runtime.v1alpha5` | `v1alpha6`(원시 필드 이름 `inputs`, firstfloor `modelFiles`) | 유휴 raw evidence |
| `stateIdentitySchemaVersion`(`internal/profiling/profiler/projection/identity.go:13`) | `execution-state-identity.v1alpha5` | `v1alpha6`(`ExecutionState`의 files와 전처리) | identity를 담은 Run과 evidence |
| `measurementProtocolIdentitySchemaVersion`(`identity.go:15`) | `measurement-protocol-identity.v1alpha1` | `v1alpha2`(`MeasurementContract`의 데이터셋) | identity를 담은 Run과 evidence |
| `observationSchema`(`internal/serving/observations.go:20`) | `serving-observations.v1alpha2` | `v1alpha3`(진행 필드, `InputPath` 삭제, Definition 형태) | serving journal window 전체 |
| `firstfloorRequestSchemaVersion`(`internal/profiling/profiler/worker/firstfloor.go:9`, `measurement.py:22`) | `firstfloor-runtime-request.v1alpha1` | `v1alpha2` | - (전달용 계약) |
| 직접 runner 요청(`ort-cpu-runtime-request.v1alpha15`, `rknnlite-runtime-request.v1alpha11`) | 사용 중 | 실행 경로와 함께 삭제 | - |

외곽 형태가 그대로인 다음 version은 유지하고, 안에 담은 계약 때문에 읽을 수 없는 객체는 위 정리 대상과
함께 삭제한다: `RawMeasurementSchemaVersion` `v1alpha8`, `ExecutionEvidenceSchemaVersion` `v1alpha15`,
`IdleRawMeasurementSchemaVersion` `v1alpha6`, idle baseline result `v1alpha2`, campaign result record `v1alpha4`,
`WorkloadContractSchemaVersion` `v1alpha1`, `cpu-execution-contract` `v1alpha2`.

scheduler 설정의 campaign과 profile 선택은 삭제된 campaign을 가리키므로 새 측정 뒤 다시 선택한다.

## 9. 커밋 단위

제목은 `git log`의 `type(scope): summary` 형식이다. 모든 commit은 단독으로 build되고 `make lint`와
`make test`를 통과하며, 아래 검사는 [AGENTS.md](../../../AGENTS.md)의 표면별 기준으로 그에 더한다.
Go 타입이나 wire 계약을 바꾸는 commit은 그 producer와 모든 consumer(Go, Python, Dashboard,
load generator, sample)를 함께 바꾼다.

| # | 제목 | 파일과 package | 선행 | 요구사항 | 추가 검사 |
|---|---|---|---|---|---|
| 1 | `refactor(profiling): measure only through firstfloor` | 5.6절 목록 전체, `internal/modelcatalog`의 직접 family 분기, `docs/design/profiling/README.md`의 직접 runner 설명 | 없음 | 측정의 단위 | focused `go test ./internal/profiling/... ./internal/modelcatalog/... ./cmd/measurement-worker/...`, `make helm-lint helm-template`(Profiler render) |
| 2 | `feat(catalog): add preprocessor and dataset kinds` | `api/v1alpha1/{preprocessor,dataset}_types.go`(`artifacts` `MinItems=1`), 생성 deepcopy와 CRD, `config/crd/kustomization.yaml`, chart CRD, `internal/modelcatalog/preprocessor.go`(`ValidatePreprocessor`, `ValidateDataset`, `Compatible`, `PreprocessorArtifact`) | 없음 | `DP-01`, `DP-02`, `DP-03`, `DP-06`, `DP-07` | `make manifests generate`와 생성 drift 확인, `go test ./api/... ./internal/modelcatalog/...`, `make helm-lint helm-template` |
| 3 | `feat(serving): manage preprocessors and datasets` | `internal/serving/kube/{store,manager}.go`의 두 kind CRUD와 `DP-09`, ModelSpec 수정과 삭제의 `DP-12` 거부, `internal/serving/entrypoint/handler.go`, owner와 manager RBAC, Dashboard relay와 두 kind 관리 화면, `config/samples/catalog/README.md`의 등록 안내 | 2 | `DP-08`, `DP-09`, `DP-12`, `DB-FR-28` | `go test ./internal/serving/...`, `make helm-lint helm-template`(default와 Profiler), `make deployment-graph-check`, `make dashboard-check` |
| 4 | `feat(serving): bind workloads to preprocessors and datasets` | Workload API와 CRD(`preprocessorRef`, `datasetRef`, 적용 참조, `targetCatalog*`, `PreprocessorUpdate`), `validatedReferences`, `serving.Definition`의 전처리와 데이터셋 identity, 경로별 전처리 산출물 선택, 즉시 적용 조건, `PreprocessorUpdate` barrier, Dataset 수정 호환성, `DP-11` 거부, `MissingDataset`, observation schema `v1alpha3`, Dashboard Workload 편집과 도메인 타입 | 3 | `WM-02`, `WM-06`, `WM-07`, `WM-09`, `WM-16`, `WM-18`, `DP-10`, `DP-11`, `DB-FR-05`, `DB-FR-06`, `DB-FR-07` | `make manifests generate`, `go test ./api/... ./internal/serving/...`, `make dashboard-check` |
| 5 | `feat(serving): serve and profile raw samples through preprocessor modules` | ModelSpec `source.files`와 RKNN 전용 `input`. `internal/firstfloor`의 BYTES framing, IPC, 항목 결과, 배치 오류, 진행과 profile 응답 필드, `Executions` 삭제와 `runtime/firstfloor/runner.py`의 module 실행, 파일별 session, 첫 토큰 검사(synthetic module fixture 테스트). gateway 원시 입력 검증, route 단일화와 `InputPath` 삭제(scheduler replay와 관측 포함), `validated` 경과 시간, serving adapter 원시 필드와 `422`, Pod init container와 observer, `profilePath`, `schedulerOptionCondition`, `SupportedInput`, `cmd/serving-entrypoint/profiles.go`. 측정 API와 CRD, campaign child Run, resolver와 preparation, Profiler RBAC 읽기, RunConfig, candidate report, curve plan, 두 identity, Job init container, measurement worker 요청, `measurement.py` 표본 추출, RuntimeResult와 idle 결과, projection과 derivation. Dashboard 모델 files, 측정 요청, 단일 추론, 단계 시간 표시. load generator `infer` 본문. mobilenet ModelSpec sample의 `files` | 1, 4 | `DP-03`, `DP-04`, `DP-05`, `WM-04`, `WM-08`, `WM-26`, `WM-27`, `WM-28`, `WM-29`, `SCH-03`, `SCH-06`, `SCH-09`, `SCH-AC-23`, `DB-FR-04`, `DB-FR-08`, `DB-FR-09`, `DB-FR-12`, `DB-FR-22`, 측정의 단위 | `make manifests generate`, focused `go test ./internal/firstfloor/... ./internal/serving/... ./internal/profiling/... ./cmd/...`, `make gopls-check`, `make helm-lint helm-template`(default와 Profiler), `make deployment-graph-check`, `make dashboard-check`, `make test-loadgen` |
| 5a | `fix(serving): check applied preprocessors on dataset updates` | Dataset 수정 시 적용 정의가 참조하는 Preprocessor와의 `Compatible` 검사 | 5 | `DP-10` | `go test ./internal/serving/...` |
| 6 | `feat(loadgen): replay dataset evaluation subsets` | `load-generator/src/chill_loadgen/cli.py`의 `replay`, 테스트, README, `Makefile`의 `test-loadgen` | 5 | 이기종 혼합 부하, `SCH-AC-23` | `make test-loadgen` |
| 7 | `feat(assets): build mobilenet and SmolVLM carriers with a VQAv2 subset` | `build/model-artifacts/preprocessors/{mobilenet-nchw,mobilenet-nhwc,smolvlm2-vqa}`(ONNX graph에서 고정한 입출력 이름 포함), `build/model-artifacts/vqav2/`와 분리 단위 테스트, `test-artifact-tools` 대상 확장, carrier Dockerfile, runtime image 의존성, 두 Preprocessor, Dataset, SmolVLM ModelSpec, 두 Workload sample | 5 | `DP-03`, `DP-04`, `DP-06`, `WM-26`, Mobilenet 원시 JPEG 전환 | `make test-artifact-tools`, 로컬 `docker buildx build`(push 없이)로 carrier와 runtime image 확인 |
| 7a | `fix(catalog): reject file and field names the runtime cannot load` | 3.1절과 3.3절의 필드 이름과 모델 파일 이름 규칙(`internal/modelcompat/files.go`, CRD pattern), RunConfig와 Dashboard 관측의 같은 규칙 | 7 | `DP-01`, `DP-03`, `DP-06` | `make manifests generate`, `go test ./internal/modelcatalog/... ./internal/modelcompat/... ./internal/profiling/run/...` |
| 8 | `docs(design): record heterogeneous workload verification` | 9.1절, `docs/design/serving/workload-management.md`의 입력 계약과 검증 경계, `docs/design/serving/README.md`, `docs/design/profiling/README.md`의 입력 계약 절, 구현 계획의 현재 상태 | 6, 7a | 전체 | 문서 링크 확인 |

5는 firstfloor native 요청과 IPC, 프로파일 frozen 조건을 serving과 profiling이 함께 사용하므로 하나의
commit이다. [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)와 이 설계의 단일 경로 결정에
따라 각 commit은 한 계약만 지원한다. 작성은 catalog, firstfloor와 runner, serving, profiling,
Dashboard와 load generator 영역으로 나누어 동시에 진행하고 한 integration owner가 하나의 commit으로 합친다.

병렬 진행: 1은 2, 3, 4와 동시에 진행한다(1은 `internal/modelcatalog/catalog.go`, 2는 새 파일
`preprocessor.go`를 바꾼다). 2, 3, 4는 같은 manager와 생성 파일을 바꾸므로 순서대로 진행한다. 5는 1과 4 뒤에
진행한다. 6과 7은 5 뒤에 동시에 진행한다. 생성 파일(`zz_generated.deepcopy.go`, CRD)을 바꾸는 commit은 각 merge
뒤 `make manifests generate`를 다시 실행한다.

### 9.1 검증 기록

| 실행 경계 | 결과 |
|---|---|
| 로컬 unit과 contract test | 각 단위의 추가 검사 통과. Go package test, runner synthetic module fixture, `make test-loadgen`, `make test-artifact-tools`(전처리 module과 VQAv2 분리) |
| runtime image의 통합 runner smoke(CPU), mobilenet | 4개 batch 중 잘린 JPEG 1개가 항목 오류가 되고 나머지 3개가 `label`을 반환, runner 유지, `service_ms` 72 |
| 같은 smoke의 SmolVLM | 3개 batch의 단위 수 17, 17, 13, 답 `Up.`, `Watching.`, `Http://foodiebaker.com.`, `first_token_ms` 50404, `backend_ms` 53974(8 CPU) |

다음은 검증하지 않았다: live cluster의 등록, serving과 관측, Jetson과 RKNN 장비의 실행, 실제 Node의
profiling Job, 이기종 혼합 부하 평가. 이 검증은 merge 시점 절차(8장)의 재등록과 재측정 뒤에 수행한다.

## 10. 범위 밖

- 승인 단계에서 기각한 설계: 이미지 타일을 배치 축 Σk로 쓰는 batching, 선언형 기여 규칙, tensor별 단위나
  요청 표시, a×k+b 제어 모델, continuous batching, 비디오 입력, gateway의 전체 화소 복호, 범용 pipeline 또는 DAG engine.
- 단위 수를 batch 선택이나 비용 계산의 입력으로 쓰는 것. 단위 비용은 오프라인 파생 표로만 만든다.
- 정답 비교와 정확도 평가, VQAv2 answers 보관.
- JPEG와 UTF8 이외의 내용 형식, 여러 이미지를 담는 표본.
- CHILL이 데이터셋이나 산출물을 업로드, 변환, 검사하는 기능. carrier는 운영자가 빌드한다.
- 등록 시 자동 측정 시작, 전처리 코드 격리 실행 환경.
- CLI inventory의 새 kind 표시.
- 직접 ORT와 RKNNLite runner의 독립 기준 측정.

## 11. 요구사항 대응

| 요구사항 | 설계 |
|---|---|
| `DP-01`, `DP-02`, `DP-03` | 3.1, 3.4, 4.3, 4.7 |
| `DP-04`, `DP-05` | 4.3, 4.4 |
| `DP-06`, `DP-07` | 3.2, 3.4, 7 |
| `DP-08`, `DP-09` | 3.6, 3.7 |
| `DP-10` | 3.7, 5.2 |
| `DP-11`, `DP-12` | 3.7 |
| `WM-02`, `WM-04`, `WM-06`, `WM-07`, `WM-09` | 3.5 |
| `WM-08` | 3.3, 3.5, 4.8 |
| `WM-16`, `WM-18` | 3.5, 3.7 |
| `WM-26` | 4.1 |
| `WM-27` | 4.8 |
| `WM-28` | 3.5, 4.6 |
| `WM-29`와 전처리 실패 오류 | 4.4 |
| `SCH-03`, `SCH-09`, `SCH-AC-23` | 3.5, 4.8 |
| `SCH-06` | 4.5 |
| firstfloor 단계별 실행 결과(스케줄러 SRS 책임 표) | 4.3, 4.6 |
| `DB-FR-04`, `DB-FR-05`, `DB-FR-07`, `DB-FR-12`, `DB-FR-28`과 관리 시나리오 | 6 |
| 시스템 명세의 원시 표본 요청, 전처리 참조, 데이터셋 참조, 공유 조건 | 3.5, 4.1, 4.8 |
| 라우팅과 배칭의 실행 단위, 입력 검사 시각, 단계 시간, 단위 수 | 4 |
| 측정의 단위와 원시 trial | 5 |
| 이기종 혼합 부하 평가와 Mobilenet 원시 JPEG 전환 | 7 |

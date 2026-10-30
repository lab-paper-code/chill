# 이기종 워크로드 구현 설계

## 1. 목적과 결정

요구사항 원본은 [catalog SRS](../../spec/catalog/SRS.md) `DP-01`~`DP-12`, [Workload SRS](../../spec/workloads/SRS.md), [scheduler SRS](../../spec/scheduler/SRS.md), [Dashboard SRS](../../spec/dashboard/SRS.md)다. [시스템 명세](../../spec/README.md)·[측정 단위](../../spec/00_measurement.md)·[라우팅/배칭](../../spec/03_routing.md), [ADR 0021](../../adr/0021-raw-sample-preprocessing-boundary.md)이 기준이며 공통 CRUD/drain은 [워크로드 관리](workload-management.md)를 따른다.
목표는 mobilenet-v2 이미지 분류와 SmolVLM2-500M-Video-Instruct의 VQAv2 이미지/질문 짧은 답 생성을 같은 POOL·서빙 경로로 제공하는 것이다.

### 핵심 결정

| 결정 | 이유 |
|---|---|
| cluster-scoped spec-only Preprocessor/Dataset CRD | ModelSpec의 catalog 저장·관리 API·권한·참조 보호 재사용 |
| 원시 datatype은 BYTES 상수, 선언은 format/maxBytes | 한 값의 선택지를 만들지 않되 wire/조회에는 BYTES 표시 |
| 모델 산출물은 `source.files`, 전처리는 디렉터리의 Python module 하나 | 다중 모델 파일도 단일 산출물/실행 identity. 모델 결합은 module, session/측정/결과 검사/IPC는 runner 소유 |
| gateway 내용 검증 한 번, downstream은 구조만 | 중복 경계 방지. 표본 전처리 실패 후 runner 유지(`DP-05`,`WM-29`) |
| profile 조건은 모델 산출물/runtime/전처리 산출물/Dataset | 인스턴스·실측 공유 조건 일치 |
| Dataset 일치는 이름/UID, 내용 identity는 RunConfig frozen | 수정은 이후 Run부터 반영, 기존 profile 유지(`DP-10`) |
| `maxNewTokens`는 Preprocessor 설정, subset/seed는 frozen image의 상수 | Serving/Profiling이 동일 조건 소비. subset은 `profiling`,`evaluation` |
| 측정은 firstfloor만, 저장은 단일 계약 | 직접 ORT/RKNNLite 실행 중복 제거, [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)에 따라 구객체 정리 |

기존 package만 사용한다: API 선언 `api/v1alpha1`, 순수 검증 `internal/modelcatalog`, 존재/삭제/참조·저장 `internal/serving/kube`, core는 자체 `serving.Definition`. `.go-arch-lint.yml`의 기존 경계를 유지한다.

## 3. 카탈로그

### Preprocessor (`DP-01`~`DP-05`)

| spec 필드 | 계약 |
|---|---|
| `input.fields[]{name,format,maxBytes}` | 표본 하나의 전체 원시 입력. format `JPEG`/`UTF8`, 필수 maxBytes≥1 |
| `output.fields[]{name,format}` | 원시 출력; maxBytes 없음 |
| `maxNewTokens` | 생성형만 선언, 정수≥1 |
| `artifacts[]` | CRD `MinItems=1`; `target:{model,artifact}`마다 항목 하나 |
| `artifacts[].source:{image,architecture,path}` | carrier image·Kubernetes 아키텍처·clean absolute 디렉터리(`preprocessor.py` 포함), ModelArtifactSource와 같은 의미 |

입력/출력/레코드 필드 이름은 계약 안에서 유일하고 파일/key로 쓸 수 있는 경로 요소 하나다: `/`,`\` 금지, `.`/`..` 금지, ≤256bytes(CRD pattern/MaxLength).
예시 상한은 image JPEG4194304bytes·question UTF81024bytes, answer UTF8, maxNewTokens16이다. mobilenet은 동일 Preprocessor의 NCHW 디렉터리가 canonical ONNX arm64/amd64 각각, NHWC가 `mobilenet-v2-050-rk3588` 대상이며 multi-arch carrier를 재사용한다.

### Dataset (`DP-06`,`DP-07`)

`spec.source:{image,path}`, `spec.fields[]{name,format,maxBytes}`를 가진다(예: image JPEG1048576bytes, question UTF8512bytes).
carrier 배치는 `<path>/profiling/<recordID>/<field>`와 `<path>/evaluation/<recordID>/<field>` 고정, recordID는 디렉터리명·값은 파일 원문이다. 빌드 script/unit test가 question_id 기준 두 subset의 분리를 보장하며 CHILL은 선언/고정 디렉터리만 사용한다.
image는 amd64/arm64 multi-arch index digest이며 init container가 모든 노드에서 `sh`,`cp`를 실행한다.

### ModelSpec·순수 검증

`artifacts[].source:{image,architecture,files}`의 files는 clean absolute 경로≥1, basename과 확장자 제거 stem이 각각 유일해야 한다. 실행 mount는 `/model/<basename>`; ORT는 파일마다 session 하나, RKNNLite는 `.rknn` 파일 하나다(`modelcompat.ValidateModelFileNames`).
`input` tensor 선언은 RKNN만 필수/허용이며 delivery capability/runner 검사가 소비한다. ONNX 입력은 전처리 산출물 소유다. 실행 family는 `firstfloor`만이며 직접 `onnxruntime`,`rknnlite` 분기는 없다.

| modelcatalog 함수 | 검증 |
|---|---|
| `ValidatePreprocessor(p)` | 필드 유일성·형식/상한·중복 target·source |
| `ValidateDataset(d)` | source·레코드 필드 |
| `Compatible(d,p)` | 모든 전처리 입력이 같은 이름/형식으로 Dataset에 존재, Dataset maxBytes≤입력 maxBytes; 불일치 필드 목록 |
| `PreprocessorArtifact(p,model,artifact)` | 해당 모델 산출물 대상 전처리 선택(`WM-08`) |

### Workload 참조·조회 (`WM-02`,`WM-04`,`WM-06`~`WM-09`,`WM-16`,`WM-18`,`WM-28`)

`modelRef`, 필수 `preprocessorRef`, 선택 `datasetRef`는 `CatalogReference{name}`를 공유하고 DNS 이름을 검증한다. applied는 Workload generation·model/preprocessor 이름/UID/generation·Dataset 이름/UID·SLO를 보존한다.
`operation.kind`는 `Update,Delete,ModelUpdate,PreprocessorUpdate`; 공유 대상은 `targetCatalogUID,targetCatalogGeneration`이며 kind가 대상 종류를 정한다(`targetModel*` 대체).
`Definition.Preprocessor{Name,UID,Generation,Input,Output,MaxNewTokens}`, `Dataset *{Name,UID}`, `ModelPath.Preprocessor{Image,Architecture,Path}`를 사용한다. 해당 전처리 산출물이 있는 실행 경로만 snapshot에 넣고 Identity에 전처리 UID/generation을 포함한다.
`validatedReferences`가 세 참조 존재/삭제 상태·모델 검증·공유변경 충돌·Dataset Compatible을 확인하며 필드 경로를 오류에 담는다. 모델/전처리 UID/generation 동일 시 SLO·Dataset 참조 변경은 즉시 `200`, 실행 변경은 drain이다.
지원 입력/출력은 applied `preprocessor.input/output`; 과거 `SupportedInput.Tensors` 없이 경로별 evidence 목록만 제공한다. Dataset 없는 적용은 준비 대기·`MissingDataset` admission 제한(`WM-07`,`WM-09`)이며 등록은 가능하다.

### 관리 API·권한·참조 보호 (`DP-08`~`DP-12`)

`GET/POST /api/v1/preprocessors`, `GET/PUT/DELETE /api/v1/preprocessors/{id}`와 datasets의 같은 경로를 사용한다. POST `{name,spec}`, PUT `{spec}`, PUT/DELETE `If-Match`. 미등록404, validation은 기존 managementError code/message/field다.
참조 Preprocessor 수정은 공유 변경202, 나머지 수정200. Store kind별 direct Get/List/Create/Update/Delete와 Manager의 공통 관리 잠금을 재사용한다.
ChillServer SA는 preprocessors/datasets get/list/create/update/delete, manager nonresourceURLs는 `/chill/api/v1/preprocessors`,`/chill/api/v1/datasets` 및 하위 경로, Profiler는 두 resource get이다. caller 변경 권한은 기존 SAR(`WM-01`), 직접 쓰기 우회는 없다.
참조 판정은 desired 이름·applied UID·진행 작업 target UID를 같은 관리 잠금에서 확인한다.

| 동작 | 계약 |
|---|---|
| Preprocessor 등록/수정 | 순수 검증+target ModelSpec/산출물 존재·삭제 상태 |
| 참조 Preprocessor 수정 | 관련 Workload Dataset 전체와 새 선언 Compatible 후 PreprocessorUpdate |
| Dataset 수정 | 참조 Workload 전체(적용 정의의 전처리 포함) Compatible 확인 후 저장; Serving snapshot은 유지하고 이후 Run만 새 내용 동결 |
| Preprocessor/Dataset 삭제 | 참조 Workload 목록과 `PreprocessorReferenced`/`DatasetReferenced`로 거부 |
| 모델 산출물 제거/모델 삭제 | 대상 Preprocessor 목록과 `ModelArtifactReferenced`/참조 오류로 거부; Workload 참조도 보호 |

공유 barrier는 전체 gate 닫기→drain→연결 해제→닫힌 snapshot 게시→전체 gate 열기다. group identity는 kind+targetCatalogUID/generation. 적용 직전 ModelSpec/Preprocessor를 재조회하여 대상 identity 확인 후 `snapshot(w,model,preprocessor,dataset)`을 설치한다. 진행 target의 새 참조 등록/수정도 충돌이다.

## 4. 요청 경로

gateway→Workload 유입 큐→인스턴스→firstfloor UID별 큐→요청 수 n 배치→전처리/추론/후처리→개별 결과 순서다.

### 4.1 원시 표본 요청
`InferenceInput.Inputs`는 `RawInput{Name,Datatype string;Data []byte}`이며 JSON base64를 직접 decode한다. gateway는 ①계약과 같은 필드 집합/중복 없음 ②BYTES ③1≤길이≤maxBytes ④JPEG `FF D8 FF`+`image.DecodeConfig` header ⑤`utf8.Valid`를 한 번 검사한다. 화소 복호는 runner다.
오류는 `InvalidInput`, `inputs[i].data`. MaxRequestBytes는 base64 상한을 담도록 운영 설정한다. `validated.ElapsedNanos`는 수신~검사 완료다. 모든 경로의 raw 계약이 같으므로 InputPath/경로 index/ModelPath.Tensors/sameInputs/경로별 route 없이 Workload route 하나, routingCredits key는 UID+인스턴스다.

### Native·IPC

입력 `Field{Name string;Data [][]byte}`는 추론 원소1/profile 원소n. Config는 `Inputs []string,Outputs []string,MaxNewTokens int`다. prepareInputs는 이름/원소 수만 확인하여 각 원소를 uint32 little-endian 길이+bytes framing; packBatch는 그대로 이어 `{name,datatype:BYTES,shape:[n],bytes}`를 만든다. profile 호출 하나=실행 하나(`ProfileRequest.Executions` 없음).
길이 prefix/JSON header는 기존 IPC 형식을 유지한다.

| frame | header / payload |
|---|---|
| init | inputs,outputs,batch_sizes,max_new_tokens / empty |
| infer | batch_id,tensors / 필드순 framing n개 |
| output | batch_id,items(요청순 `{units:k}` 또는 `{error:reason}`),tensors(BYTES shape:[성공수]),service_ms,preprocess_ms,backend_ms,postprocess_ms,생성형 first_token_ms / 성공 원소 |
| batch_error | batch_id,error / empty |
| error | runner 종료 오류 / empty |

Go exchange는 framing/items=batch 크기/output shape=성공 수만 확인하고 module 결과 계약은 runner가 검사한다.
runner는 `--model-dir /model` 파일별 session, `--preprocessor <dir>/preprocessor.py`를 적재하며 module 계약은 다음이다.

```python
WARMUP_SAMPLE: dict[str, bytes]
def preprocess(samples: list[dict[str, bytes]]) -> tuple[object, list[int | str]]: ...
def infer(sessions: dict[str, object], inputs: object, max_new_tokens: int,
          first_token: Callable[[], None]) -> object: ...
def postprocess(outputs: object) -> list[dict[str, bytes]]: ...
```

preprocess 보고는 요청순 성공 단위 정수≥1/실패 사유 문자열, 모델 입력은 성공만 순서대로다. sessions key는 파일 stem, 값은 ORT InferenceSession/RKNNLite. provider·CUDA UUID·thread/affinity 검사를 파일마다 유지한다. ort-cuda provider는 정확히 `[CUDAExecutionProvider,CPUExecutionProvider]`(ORT shape subgraph는 CPU), profile에 실제 목록을 남긴다.
성공이 있으면 infer→postprocess를 호출하고 perf_counter_ns로 각 단계 측정, backend_ms=infer다. 생성형(max_new_tokens>0)의 infer는 first_token을 정확히1회 호출하며 해당 시각을 기록한다. postprocess는 성공 수만큼의 dict, key=출력 필드, 값=bytes다.
warmup은 initialized batch마다 WARMUP_SAMPLE을 batch만큼 복제해 설정 횟수 전체 경로 실행; Serving/Profiling이 동일 warmup과 profile 활성 protocol 횟수를 사용한다.
ChillServer 준비 runnerTimeoutMS는 `--operation-timeout` 기본30분이며 serving-worker가 startup(load/warmup)과 batch 하나에 같은 값을 쓴다. profiling은 observerReadyTimeoutMilliseconds다.
라이브러리는 runtime image 소유. import/필수 이름 실패는 ready 전 시작 오류다. serving container `terminationMessagePolicy:FallbackToLogsOnError`, PrepareInstance 오류에 종료 메시지를 포함한다. ready metadata는 `modelFiles` 실제 목록·inputs/outputs 원시 이름이며 observer/power-cost도 이를 비교한다.

| 상황 | 결과 |
|---|---|
| 일부 전처리 실패 | 실패만422 PreprocessFailed, `progress.outcome=preprocess_failed`; 나머지 실행, runner 유지 |
| 보고 수≠n·units<1·출력 수/필드 오류·first_token0/2회 이상·module 예외 | batch_error, 배치 전체502 BackendError/outcome=batch_error; runner/대기 요청 유지 |
| IPC framing 위반·runner 종료·timeout | 기존 error/종료 fail 경로 |

finishBatch는 items로 성공/실패 분리, splitOutputs는 성공에만 나눈다. 응답에 사유를 제공한다.
Smol 생성은 greedy static batch: vision_encoder→embed_tokens→KV cache decoder_model_merged, 첫 step 후 first_token1회, 전 요청 EOS 또는 max_new_tokens 도달까지. graph 입출력 이름은 자산 commit에서 고정한다. 비생성형 MaxNewTokens0/first_token_ms생략.
units는 MNV1/Smol 실제 타일 수이며 batch 선택·serviceCost는 요청 수 n만 사용(`SCH-06`). units는 분석용 관측이며 제어/비용 입력이 아니다.

### 관측·prepare·profile 일치

Progress/BackendProgress는 preprocessMS,postprocessMS,생성형 firstTokenMS,성공 요청 units를 보존한다. 같은 batchID 요청은 단계 시간을 공유, units는 요청별이다. backendMS=infer, serviceMS=runner 교환 전체라는 기존 의미를 유지한다.
ProfileResponse는 units,preprocessSamplesMS,postprocessSamplesMS,firstTokenSamplesMS이며 outputs는 없다. adapter/ObservationLog/query/Dashboard는 이를 그대로 보존한다(`WM-28`).
PrepareInstance init은 artifact(모델 파일 전체 `cp "$@"`, `/model` read-only), preprocessor(`cp -R`, `/preprocessor` read-only), profile의 셋이다. 원본은 frozen profile; samePreparedPod/observer는3 init·2 artifact volumes를 확인한다. Config Inputs/Outputs/MaxNewTokens는 frozen 전처리 블록에서 만든다.
profilePath는 전처리 산출물 image/architecture/path, 전처리 이름·입출력 이름·maxNewTokens와 Dataset 이름/UID(activeProtocol.dataset.uid)를 맞춘다. 조건 직렬화 최신선택과 schedulerOptionCondition은 Preprocessor/ActiveProtocol.Dataset을 포함한다. 네 조건(모델 산출물/runtime/전처리 산출물/Dataset)이 같을 때만 공유 가능(`SCH-03`,`SCH-09`,`SCH-AC-23`,`WM-27`); 현재 노드당 인스턴스 운영 규칙은 [POOL 설계](workload-pools.md)를 따른다.

## 5. 프로파일링

측정 candidate에 preprocessor, 활성 Run/campaign ActiveProtocol에 dataset 이름을 둔다. inputGenerator/outputValidation은 없고 CEL이 모드별 존재를 확인한다. campaign child 활성 Run에 복사한다. Dashboard Workload 선택은 모델/전처리/Dataset을 채우며 API는 catalog 이름을 직접 받는다(`DB-FR-12`,`WM-07`).

| frozen 기록 | 필드 |
|---|---|
| RunConfig.State | ArtifactFiles[]; Preprocessor{Name,UID,Image,Architecture,Path,MaxNewTokens,Inputs,Outputs} |
| RunConfig.Measurement | 활성 Dataset{Name,UID,Image,Path} |
| CurvePlanSpec | Artifact.Files, Preprocessor{Name,Image,Architecture,Path,MaxNewTokens,Inputs,Outputs}; Tensor 없음 |
| CurveProtocolContract | 활성 Dataset{Name,UID}; InputGenerator/OutputValidation 없음 |

resolver는 cluster-scoped 두 catalog를 읽고 모델 산출물 대상 전처리를 고른다. preparation은 전처리 architecture=Node와 Dataset Compatible을 확인한다. Dataset 수정은 이후 frozen Run에만 적용한다(`DP-10`).
Job init은 모델 파일·전처리 디렉터리·활성 Dataset `<path>/profiling`을 `/dataset`으로 복사한다. worker Python 요청은 modelDirectory,preprocessorPath,datasetPath다.
measurement.py는 record 디렉터리 이름순+`SAMPLE_SEED=0`의 random.Random, 실행마다 `rng.sample(records,n)`을 뽑는다. seed는 runtime image와 frozen이며 seed 실험이 필요할 때만 protocol 설정으로 옮긴다. 필요한 입력 필드 파일만 읽고 다음 본문은 timer 밖 `session.prepare_next()`에서 준비한다. warmup은 WARMUP_SAMPLE, 측정은 Dataset만; 표본 전처리 실패는 trial 오류다.
NativeServiceSample/derivation은 `recordIDs []string,units []int,preprocessMs,postprocessMs,생성형 firstTokenMs`를 native_sample에서 받아 그대로 보존한다. 두 배열 길이는 batch 크기, output 검사는 runner 소유. 측정량은 기존 n별 batch 시간 분포다.
직접 ORT/RKNNLite의 독립 Go runtime 선택·검증/evidence 분기·Python main/input 생성·전용 image/Makefile/fast-deploy component/직접 runtime 선언은 제거한다. firstfloor가 쓰는 CPU 환경/RKNN delivery/common helper는 library로, measurement-worker binary는 firstfloor image용으로 유지한다. runtime 선언은 `hack/runtime-declarations.py`, sample은 firstfloor만이다. 세 경로에 같은 전처리/표본 추출을 중복 구현하지 않는다.

## 7. 자산과 평가 부하

Dashboard relay는 `(models|workloads|preprocessors|datasets)` 목록/항목을 허용한다. 관리 화면은 Preprocessor 입력/출력·대상 모델/산출물·maxNewTokens, Dataset source/레코드 필드, 참조/영향/거부 사유를 기존 Impact/Problem으로 제공한다(`DB-FR-28`,`DB-FR-07`).
모델은 files/RKNN input/대상 Preprocessor(`DB-FR-04`), Workload는 필수 전처리/선택 Dataset·MissingDataset/변경 drain 안내(`DB-FR-05`,`06`,`07`), inference는 JPEG 파일/UTF8 텍스트→base64·format 출력(`DB-FR-08`,`09`), 측정 요청은 Workload 참조 채움, 기록은 단계시간/units/validated/recordIDs를 표시한다(`DB-FR-12`,`22`). domain은 같은 fields/targetCatalog*를 사용한다.
carrier 공통 Dockerfile `build/docker/derived-model-artifact.Dockerfile`은 context artifact/ 전체를 /artifact/로 복사하고 Dataset은 multi-arch로 빌드한다. 등록은 `config/samples/catalog/`의 ModelSpec3/Preprocessor3/Dataset2와 `config/samples/manual/`의 Workload3, [sample 안내](../../../config/samples/catalog/README.md)를 따른다.

| 자산 | 고정 계약·근거 |
|---|---|
| Smol ONNX | HF HuggingFaceTB/SmolVLM2-500M-Video-Instruct 고정 revision; stage.py가 vision_encoder.onnx/embed_tokens.onnx/decoder_model_merged.onnx로 배치. fp32 arm64/amd64 CPU·CUDA, int8 arm64/amd64 CPU. ORT GPU1.23.2는 int8 vision_encoder 초기화 실패하므로 int8 CUDA 없음 |
| MNV 전처리 | mobilenet-imagenet; mobilenet-nchw(ONNX canonical arm64/amd64), mobilenet-nhwc(RK3588). JPEG→짧은변256 resize→224 center crop→ImageNet 정규화, units1, label UTF8 class index. WARMUP_SAMPLE은 import 때 만든 작은 JPEG |
| Smol 전처리 | smolvlm2-vqa: SmolVLMProcessor longest edge2048/타일512/타일당64tokens·chat template·tokenizer/config·decode loop, output answer, fp32 대상. 같은 carrier smolvlm2-vqa-1tile은 do_image_splitting:false로512타일1개/64tokens/units1, int8 대상 |
| VQAv2 | build_subset.py가 lmms-lab-encoder/VQAv2 validation 고정 revision/seed, shard validation-00000-of-00068 하나에서만 선택. question_id로 profiling/evaluation 분리 test. 같은 Dataset에서 MNV는 image만 사용 |
| PLC | edge_poc2.zip PLC_EXTRUDER_01(port_type9006)을 measurement_time순, 간격≤10s의60개 비중첩 window. train07-28..08-13, threshold08-14..16, profiling08-17..18의256개, evaluation08-19..20의1024개. records UTF8=원본60개 JSON. CPU 학습10특징 LSTM AutoEncoder, ONNX opset16(JetPack4 ORT1.11 최신지원), MSE score·stats.json(train평균/표준편차/검증p99 threshold). plc-extruder-anomaly는 z-score→[b,60,10], units1, score/anomaly; ModelSpec plc-lstm-ae, Workload plc-anomaly |
| 평가 부하 | replay --workload ID --dataset-dir DIR --fields image,question --rate R --duration S; DIR=evaluation, 이름순 순환·고정간격 open-loop run_schedule, 요청별 recordID/status/X-Chill-Request-ID JSONL. Workload별 동시 replay로 혼합 |

### 7.1 DeviceClass별 후보

CPU 경로는 모든 DeviceClass 후보이며 아키텍처별 산출물에 묶인다. CUDA/RKNN은 아래 실제 선언만 후보, `runtime-declarations.py`가 해당 image를 Node에서 검사한다. profiling memory16GiB는 Smol fp32 b4 최대12.7GB를 담는다. int8 one-tile ORT1.27 CPU4threads는 b1 1.0GiB/b4 1.8GiB로4/8GB에도 b4가 가능하다.

| DeviceClass | arch | CUDA 후보 | RKNN | 제외 |
|---|---|---|---|---|
| raspberry-pi-5-8g | arm64 | - | - | - |
| orangepi-5-plus-16g | arm64 | - | MNV | - |
| jetson-nano-4g | arm64 | MNV,PLC(TAG-l4t-r32) | - | Smol CUDA(Python≥3.9 필요), Smol fp32(4GB<최대5.2GB); CPU int8 사용 |
| jetson-agx-orin-64g | arm64 | 세 Workload(TAG-l4t-r35) | - | - |
| jetson-xavier-nx-8g | arm64 | MNV(TAG-l4t-r35) | - | PLC CUDA 수치오류, Smol CUDA 메모리 부족 |
| jetson-orin-nano-8g | arm64 | MNV,PLC(TAG-l4t-r36) | - | Smol CUDA 메모리 부족 |
| lattepanda-3-delta-8g | amd64 | - | - | - |
| edge-server-rtx-2080-ti-64g | amd64 | 세 Workload(TAG-cuda) | - | - |

MNV ModelSpec mobilenet-v2-050 경로는 firstfloor-ort-{cpu,cuda}-{arm64,amd64}/firstfloor-rknnlite. Smol/PLC CPU는 firstfloor-ort-cpu-{arm64,amd64}, Smol int8는 firstfloor-ort-cpu-int8-{arm64,amd64}; arm64 CUDA PLC는 firstfloor-ort-cuda-rnn-arm64(Nano/AGX/Orin Nano 정책만), Smol은 firstfloor-ort-cuda-vlm-arm64(AGX만). 정책 없는 DeviceClass는 해당 경로 측정/evidence 없이 CPU 후보를 유지한다.
firstfloor.Dockerfile은 Pillow와 Smol 지원 transformers를 고정한다(로컬4.46.3보다 Smol processor 포함 release 이상). CPU python3.11-slim multi-arch; amd64 CUDA12.5.1-cudnn/ORT GPU1.23.2; JP6 l4t-jetpack:r36.4.0/jetson-ai-lab ORT1.24.0; JP5 r35.4.1/standalone CPython3.11/NVIDIA ORT1.18.0/GLIBCXX_3.4.29의 focal libstdc++6 13.1; Smol RotaryEmbedding은 ORT≥1.17이다.
JP4.6은 l4t-tensorrt:r8.2.1-runtime/CUDA10.2/cuDNN8.2/Ubuntu18.04 Python3.8/NVIDIA ORT1.11.0/Pillow10.4.0. RKNN toolkit-lite2 2.3.2+host librknnrt.so. PyPI ORT GPU1.27은 CUDA13/R580≥를 요구하나 RTX driver570/555이므로 CUDA12 wheel을 쓴다. runner/helper는 Python≥3.8, GPU UUID는 libcuda.so.1 driver API다. [native runtime 근거](../code-intelligence/README.md)를 따른다.
CPU 선언은 DeviceClass별, CUDA는 UUID를 읽은 node(jetson-nano/jetsono/jetsonx/orin-nano/genesis2/genesis3)별이며 Run target과 같아야 한다. genesis1은 전환 관측 Node/metric source 없음으로 측정 후보 아님. RKNN은 orangepi driver/host librknnrt.so에 묶인다.

### 장비 제외 근거 (보편적 PASS 아님)

Xavier NX PLC CUDA evaluation record0은 CPU0.04130 대비 ORT1.16 b4 0.6133/b32 0.4613, ORT1.18 b4 0.6002/b32 0.1550이다. 같은 image AGX sm_87은0.041298/0.041291, Orin Nano1.24/Nano1.11/RTX1.23.2도 CPU와 같다. MNV label은 NX도 CPU와 같고 두 wheel에 sm_72 native code가 있으며 Volta TF32는 없다. JetPack5.1.2 CUDA11.4/cuDNN8.6의 sm_72 LSTM 오류에 지원 해결 설정이 없어 CPU로 측정한다.
Smol fp32 CUDA는 container limit 밖 통합 board 메모리를 쓴다. NX ORT1.18 적재 중 Node 무응답(limit6GiB), Orin Nano는 limit7GiB에서도654MB arena 실패; AGX b1은 Gray./VmHWM3.9GB였다. 두8GB board는 int8 one-tile CPU를 사용한다.
Orin Nano 전력모드는 boot0과 다른 tpc_pg_mask/fbp_pg_mask가 필요하나 golden context 뒤 변경 불가다. JP6 nvidia-cdi-refresh.service의 boot `nvidia-smi -L`이 nvpmodel보다 빠르면 Reboot required/설정 미적용인데 `nvpmodel -q`는 저장된 이름만 보고한다. read-back은 verbose mask0 잔류를 실패 처리한다. node-agent init은 `/etc/systemd/system/nvidia-cdi-refresh.service.d/10-after-nvpmodel.conf`에 After=nvpmodel.service를 고정(`nodeAgent.actuation.jetsonNVPModel.ensureBootOrdering`, ADR0015). testbed orin-nano 네 모드는 CPU online/max CPU·GPU clock/mask가 conf와 일치했다. JP4/5는 /usr/bin/nvidia-smi가 없어 이 unit이 실행되지 않는다.

## 8. 저장 계약

[ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)의 단일 reader다. 다음은 이기종 cutover의 세대/정리 근거이며 후속 현재 관측 v1alpha5는 [워크로드 관리](workload-management.md)를 따른다. 진행 campaign 종료 뒤 merge·구객체 삭제/재등록/재측정하며 미배포 version을 commit마다 반복 올리지 않는다.

| 계약 | cutover 값 | 정리 대상 |
|---|---|---|
| ModelSpec/Workload | files·RKNN전용 input / preprocessorRef,datasetRef,applied참조,targetCatalog*,PreprocessorUpdate | 모든 기존 모델/Workload 재등록 |
| Preprocessor/Dataset | v1alpha1 신규 | - |
| Run/BEO/MLIB | candidate.preprocessor, 활성 dataset | 구객체 전체 |
| RunConfig | profiling-run-config.v1alpha16→17 | RunConfig 포함 Run/evidence |
| CandidateReport | modelcompat-report.v1alpha12→13 | 저장 report |
| CurvePlan | curve-plan.v1alpha8→9 | curve-plan.json |
| RuntimeResult | batch-characterization.v1alpha7→8 | active raw, native sample/inputs/modelFiles |
| IdleRuntimeResult | model-loaded-idle-runtime.v1alpha5→6 | idle raw, inputs/modelFiles |
| stateIdentity | execution-state-identity.v1alpha5→6 | files/전처리 포함 Run/evidence |
| protocolIdentity | measurement-protocol-identity.v1alpha1→2 | Dataset 포함 Run/evidence |
| observation | serving-observations.v1alpha2→3 | 당시 window 전체, 진행필드/InputPath삭제/Definition |
| firstfloor request | firstfloor-runtime-request.v1alpha1→2 | 전달 계약 |
| executionFacts | profiler-execution-facts.v1alpha7→8 | node/GPU UUID 필수 선언 ConfigMap 재render |
| 직접 runner 요청 | ort-cpu-runtime-request.v1alpha15/rknnlite-runtime-request.v1alpha11 삭제 | 실행 경로와 함께 제거 |

외곽 version 유지: RawMeasurement v1alpha8, ExecutionEvidence v1alpha15, IdleRawMeasurement v1alpha6, idle baseline result v1alpha2, campaign result record v1alpha4, WorkloadContract v1alpha1, cpu-execution-contract v1alpha2. 내부 구계약을 읽을 수 없는 객체도 위 정리 대상이다. 삭제된 campaign을 가리킨 구 scheduler 선택은 재측정 뒤 교체하며 현재 자동 도출은 [POOL 설계](workload-pools.md)를 따른다.
producer/consumer(Go/Python/Dashboard/loadgen/sample)는 같은 작업 단위로 맞춘다. 직접 실행 제거→catalog→관리/참조→raw/IPC/측정 통합→loadgen/assets 순서의 구현 체크리스트는 반복하지 않는다.

## 9. 검증 기록과 범위 밖

### 9.1 검증 기록

| 실행 경계 | 관측 결과 |
|---|---|
| local unit/contract | 당시 Go package·synthetic module fixture·test-loadgen·test-artifact-tools(전처리/VQAv2 분리) 통과 |
| CPU image MNV smoke | b4 중 잘린JPEG1 실패/정상3 label, runner 유지, service72ms |
| CPU Smol smoke(8CPU) | b3 units17/17/13, Up./Watching./Http://foodiebaker.com., first_token50404ms/backend53974ms |
| RTX2080Ti/driver570/ORT1.23.2 CUDA smoke | 세 Workload ready·정확 provider/UUID; MNV b2 label; PLC b4 잘린1 실패/정상2 false/주입1 true, b32 전체 반환·runner 유지; Smol b2 units17/17, Up./Watching., first_token1444ms |
| CPU int8 one-tile(3584m,4CPU/threads4,evaluation4개) | b1/b4 units1, VmHWM1.0/1.8GiB; b1 Gray./yes./brick./black shirt and tie., service1268–1465ms; b4 마지막 답 마침표 없음, service4941ms. 같은 fp32(8threads,b1,units13)는 Gray./No./Wood./Black shirt and tie.,5.6GiB,19226–32494ms |
| CUDA int8 | vision_encoder 초기화 실패(transformer_memcpy.cc provider 없는 node), 나머지 두 session 초기화 |

이 표의 검증은 live cluster 등록/Serving 관측·실제 Node profiling Job·Jetson/RKNN end-to-end·혼합 부하 평가를 증명하지 않는다. 위 장비별 관측과 구분하며 해당 검증은 재등록/재측정 후 별도 근거가 필요하다.
범위 밖: Σk 이미지 타일 batch축·선언형 기여·tensor별 단위/요청 표시·a×k+b 제어·continuous batching·비디오/다중이미지·gateway 전체화소복호·범용 pipeline/DAG, units 제어/비용 입력(오프라인 파생만), 정답/정확도/VQAv2 answers, JPEG/UTF8 외 형식, CHILL 업로드/변환/검사, 자동 측정·전처리 격리환경·CLI inventory 신규kind·직접 runner 독립 기준 측정.

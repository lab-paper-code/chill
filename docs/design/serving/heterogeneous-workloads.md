# 이기종 워크로드 구현 설계

이미지 분류, 이미지와 질문의 짧은 답 생성, 시계열 이상 탐지를 같은 접수와 firstfloor 경로로 제공한다. 입력은 [Raw-Sample Preprocessing Boundary](../../adr/0021-raw-sample-preprocessing-boundary.md), 관리 수명은 [워크로드 관리](workload-management.md)를 따른다.

## 카탈로그와 실행 조건

Preprocessor와 Dataset은 cluster-scoped spec-only CRD다. 기존 관리 인증, 참조 보호와 저장 경계를 사용한다.

| 객체 | 선언 |
|---|---|
| Preprocessor | `input.fields[]{name,format,maxBytes}`, `output.fields[]{name,format}`, 선택 `maxNewTokens`, `artifacts[]` |
| 전처리 artifact | `target:{model,artifact}`, `source:{image,architecture,path}`. path는 preprocessor.py가 있는 clean absolute 디렉터리 |
| Dataset | `source:{image,path}`, `fields[]{name,format,maxBytes}` |
| 모델 artifact | `source:{image,architecture,files}`. files는 하나 이상의 clean absolute 파일 |
| Workload | 필수 modelRef와 preprocessorRef, 선택 datasetRef |

원시 datatype은 BYTES다. format은 JPEG 또는 UTF8이며 입력 maxBytes는 양수다. 출력에는 maxBytes가 없다. 생성형 maxNewTokens는 양수다. 필드명은 유일한 경로 요소이며 `/`, `\`, `.`, `..`를 허용하지 않는다.

모델 파일 basename과 확장자를 뺀 stem은 각각 유일하다. `/model/<basename>`으로 mount하고 ORT는 파일별 session을 만든다. RKNNLite는 rknn 파일 하나와 모델 input tensor 선언을 사용한다. ONNX tensor 구성은 전처리 module이 소유한다.

Dataset은 모든 전처리 필드를 같은 이름과 형식으로 제공하고 크기 상한을 넘지 않아야 한다. carrier는 `<path>/profiling/<recordID>/<field>`와 `<path>/evaluation/<recordID>/<field>`를 제공한다. 필드 내용은 파일 bytes이며 원시 recordID와 부분집합을 유지한다.

실행 조건은 모델 산출물, runtime image와 관측 version, 전처리, Dataset과 actor를 포함한다. Dataset 수정은 이후 Run에 동결하며 기존 evidence를 다시 쓰지 않는다.

## 관리와 참조 보호

`GET/POST /api/v1/preprocessors`, `GET/PUT/DELETE /api/v1/preprocessors/{id}`와 datasets의 같은 형태를 사용한다. POST는 `{name,spec}`, PUT은 `{spec}`이며 PUT/DELETE는 If-Match가 필요하다. 순수 검증 뒤 대상 모델과 artifact, 삭제 상태와 Dataset 호환성을 확인한다.

참조 Preprocessor 실행 변경은 관련 Workload 전체를 drain하는 공유 변경이다. Dataset 변경은 모든 참조의 Compatible을 확인하고 저장하되 현재 Serving snapshot은 유지하고 이후 Run만 새 내용을 동결한다.

desired 이름, applied UID와 진행 target UID를 같은 관리 잠금에서 검사한다. 참조 중 삭제와 전처리가 참조하는 모델 artifact 제거를 보호한다. 공유 변경은 전체 gate 닫기, drain과 연결 해제, 새 snapshot 전체 게시, gate 열기의 barrier를 쓴다.

## 원시 요청과 IPC

추론 본문은 `inputs:[{name,datatype:"BYTES",data:<base64>}]`다. 적용 전처리의 전체 필드가 표본 한 개다. gateway에서 필드 집합과 중복, BYTES, 길이1..maxBytes, JPEG 시작 bytes와 DecodeConfig header, UTF8을 한 번 확인한다.

오류는 InvalidInput과 입력 field를 반환한다. 화소 복호와 tensor 변환은 runner 책임이다. body 한도는 base64 팽창을 포함한다. 이후 Workload별 큐와 동일 UID/정의의 native 배칭을 거친다.

IPC는 기존 길이 prefix와 JSON header, binary payload를 쓴다. 원시 필드는 원소별 little-endian uint32 길이와 bytes를 이어 보낸다. batch는 요청 수이며 units는 관측용 기여량이다.

| frame | 계약 |
|---|---|
| init | inputs, outputs, batch_sizes, max_new_tokens |
| infer | batch_id와 BYTES shape `[n]` descriptors |
| output | 요청별 units 또는 error, 성공 원소 출력과 단계 시간 |
| batch_error | 배치 오류. runner와 대기 요청 유지 |
| error | runner 종료나 framing 실패 |

module 계약은 다음과 같다.

```python
WARMUP_SAMPLE: dict[str, bytes]
def preprocess(samples: list[dict[str, bytes]]) -> tuple[object, list[int | str]]: ...
def infer(sessions: dict[str, object], inputs: object, max_new_tokens: int,
          first_token: Callable[[], None]) -> object: ...
def postprocess(outputs: object) -> list[dict[str, bytes]]: ...
```

preprocess 보고는 요청순 양수 units 또는 실패 사유다. 성공만 모델에 넣고 postprocess는 성공 수와 같은 dict를 반환한다. key는 출력 필드, 값은 bytes다. session key는 파일 stem이다. 생성형 infer는 first_token을 정확히 한 번 호출한다.

| 실패 | 결과 |
|---|---|
| 일부 전처리 실패 | 해당 요청422 PreprocessFailed, 나머지 실행, runner 유지 |
| module 예외나 보고 수, units, 출력, first_token 계약 위반 | 배치502 BackendError, runner와 대기 요청 유지 |
| framing, 종료, timeout | 기존 물리 실패 경로 |

native는 preprocess, backend와 postprocess 경과, 생성형 firstTokenMS와 개별 units를 보존한다. batch 요청은 단계 시간을 공유한다. Go serviceMS와 canonical S는 runner IPC 교환 전체이며 native backendMS는 infer다.

ORT는 default SessionOptions를 쓰며 실제 threads는 관측한다. CUDA는 정확한 GPU UUID와 CUDAExecutionProvider, CPUExecutionProvider 순서를 검증한다. shape subgraph의 CPU EP는 허용한다. library와 provider는 runtime image 소유다.

## Serving과 Profiling

준비는 frozen 모델 files, 전처리 디렉터리와 profile bundle을 materialize한다. startup Config와 native metadata의 모델, 정확한 Node/Pod/container identity를 맞춘다. 측정 image reference를 그대로 사용하며 현재 declaration으로 대입하지 않는다.

측정 batch 집합 전체를 동일 startup 순서로 warmup한다. WARMUP_SAMPLE은 startup에만 쓰고 측정은 frozen Dataset을 쓴다. initialized/measured 범위 안 cap 증가는 재시작과 재warmup 없이 정책 ACK로 적용한다.

active profiling은 frozen operatingPoints를 사용한다. 다른 노드 producer가 native `/infer`로 공급하며 sparse, saturation과 utilization phase를 구분한다. 실제 S와 G, TrialID, recordIDs와 units를 보존하고 목표 gap으로 대체하지 않는다.

utilization 에너지와 독립 idle로 증분 에너지를 도출한다. saturation은 backlogged 인접 batch의 dispatch 표본을 제공한다. phase 사이 공백과 sparse/saturation population은 증분 에너지에 섞지 않는다. 같은 actor post-load idle과 독립 idle도 구분한다.

공유 CPU는 request/limit/quota, 명시 affinity와 threads 선택을 두지 않는다. 실제 CPU, cgroup과 thread는 원시 evidence에 남긴다. memory envelope와 physical GPU, runtime delivery 검증은 유지한다.

## 저장과 지원 범위

RunConfig, CurvePlan과 raw는 모델 files, 전처리, Dataset과 Actor, 실제 runtime metadata와 trial을 동결한다. 현행 reader는 owner source의 단일 schema를 사용한다. [Durable Stored-Contract Compatibility](../../adr/0019-durable-stored-contract-compatibility.md)를 따른다.

모델과 전처리, Dataset 예제는 [catalog sample](../../../config/samples/catalog/README.md)에 있다. 실제 image와 architecture, CUDA와 RKNN 지원은 runtime declaration과 [image recipe](../../../build/docker/firstfloor.Dockerfile)로 확인한다. 과거 장비 smoke를 현재 전체 지원이나 SLO 보증으로 사용하지 않는다.

continuous batching, 타일을 별도 배치 축으로 삼는 제어, 범용 pipeline DAG, gateway 전체 화소 복호, 정답 정확도와 자동 artifact 변환은 이 계약에 포함하지 않는다.

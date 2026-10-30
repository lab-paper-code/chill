# 워크로드 관리 구현 설계

## 1. 목적과 결정

요구사항 원본은 [워크로드 SRS](../../spec/workloads/SRS.md)의 `WM-01`~`WM-34`다. 이 문서는 리소스 표현·책임·적용 순서와 검증 경계를 정한다.
구성 선택과 adaptive SLO 배칭은 [스케줄러 SRS](../../spec/scheduler/SRS.md), 실제 전환과 원시 관측은 [스케줄러 구현](scheduler-implementation.md), 원시 입력은 [이기종 워크로드](heterogeneous-workloads.md)를 따른다.
Serving과 Profiling은 동일 firstfloor native actor·queue·IPC persistent runner와 실측 SLO·에너지 selector를 사용한다.

| 결정 | 이유 |
|---|---|
| cluster-scoped Workload CRD | ModelSpec과 같은 연구 클러스터 범위, 등록 건은 Kubernetes UID로 구분([ADR 0005](../../adr/0005-crd-scope.md)) |
| 모델·전처리·선택 Dataset·SLO·선택 요청 수명만 선언 | 서비스 식별자는 객체 이름. 정적 POOL/profile 참조나 산출물 복사본을 넣지 않음 |
| POOL은 runtime 상태, 별도 Pool CRD 없음 | 활성 소속은 인스턴스 owner/binding, 측정 소속은 Node taint로 산출 |
| 기존 ChillServer가 관리 API·접수 제어·단일 writer 소유 | 모델 삭제/참조 생성과 정의 변경/신규 접수 경쟁을 같은 경계에서 해결. 별도 서버·WorkloadController 없음 |
| 수락 당시 불변 정의, bounded in-memory 큐 | 기존 요청의 의미 유지. 영속 broker·요청별 Kubernetes 객체 불필요 |

CHILL 서비스마다 활성 ChillServer 하나: `replicas:1`, `strategy:Recreate`. RollingUpdate의 프로세스 중첩, 다중 진입점 큐 동기화·분산 잠금·HA 복구는 전제가 아니다. 강제 Pod 삭제나 RWO 강제 해제로 drain을 우회하지 않는다.

## 2. 소유권과 재사용

| 경계 | 책임 |
|---|---|
| [modelcatalog](../../../internal/modelcatalog/), [modelcompat](../../../internal/modelcompat/) | ModelSpec 공통 projection·등록 검증, Kubernetes 독립 입력·실행 경로·native 정적 호환성 |
| [serving core](../../../internal/serving/) | 불변 적용 정의, gate, Workload별 미배정 큐, 요청 문맥·관리 동작; HTTP/Kubernetes/Profiler application import 금지 |
| [httpapi](../../../internal/serving/httpapi/) | bearer 조회·CRUD·단일 표본 추론, 크기 제한·디코딩·오류·응답 전달 관측; 투명 모델 proxy 없음 |
| [serving kube](../../../internal/serving/kube/) | private uncached writer, TokenReview/SAR, 상태 기록·POOL/native 관측, 검증된 profile catalog |
| [firstfloor adapter](../../../internal/serving/firstfloor/), [firstfloor](../../../internal/firstfloor/) | HTTP V2 native 상태·추론, monotonic progress, frozen 입력·UID별 배칭·순차 runner |
| [ChillServer](../../../cmd/chill-server/) | concrete 객체·공유 transport 조립, 설정·종료 |
| 구성·전환 경계 | Node/physical allocation/ready/stop, 용량표 κ 합과 whole-pool 비용, 원자 동작 완료 |
| [Evidence Loader](../../../internal/profiling/profiler/evidence/loader.go) | frozen 조건·raw evidence typed 검증; 이것만으로 SLO/scientific admission을 증명하지 않음 |

[ADR 0012](../../adr/0012-model-artifact-compatibility-boundary.md)는 정적 호환성·실측 근거·현재 배치 가능성·SLO 적합성을 분리한다. [ADR 0011](../../adr/0011-deviceclass-status-boundary.md)의 DeviceClass는 POOL/Serving 구성 저장소가 아니며, [ADR 0008](../../adr/0008-chillsystem-status-surface.md)의 설치 상태에 큐·노드 구성·관리 작업을 추가하지 않는다.
전력 변경은 [ADR 0018](../../adr/0018-transition-ownership-boundary.md)의 NodeActuator 단일 writer 소유다. Workload 등록은 측정/전환을 소유하지 않는다.
기존 concrete 타입과 typed evidence를 재사용하며 Profiler controller·전용 후보 resolver를 handler에서 호출하거나 범용 repository/workflow engine을 추가하지 않는다. import 경계는 [.go-arch-lint.yml](../../../.go-arch-lint.yml)과 [패키지 아키텍처](../code-intelligence/README.md)를 따른다.

## 3. 리소스와 요청 문맥

`edge.dacs.io/v1alpha1 Workload`는 다음 계약을 가진다. YAML 직접 쓰기가 아니라 HTTPS 관리 API로 등록한다.

| 필드 | 계약 |
|---|---|
| `metadata.name`, `metadata.uid` | 외부 서비스 식별자(Kubernetes 이름 형식·길이 제한), 등록 identity. 이름 변경 불가; 삭제 후 동명 생성은 새 UID |
| `spec.modelRef.name`, `spec.preprocessorRef.name` | 기존 ModelSpec·Preprocessor 필수 참조 |
| `spec.datasetRef.name` | 활성 측정 표본 Dataset 선택 참조. POOL·Node·인스턴스·profile 역참조 없음 |
| `spec.slo.percentile` | 유한한 `0 < p ≤ 100` |
| `spec.slo.targetLatency` | 명시적 단위의 양수 duration. Go `time.Duration` 표현 범위 밖은 오류, clipping/근사 없음 |
| `spec.requestLifetime` | 선택 `*metav1.Duration`, JSON `requestLifetime,omitempty`. 생략은 fallback이며 0과 다름. 지정 시 `targetLatency ≤ requestLifetime ≤ 129s`(`WM-33`, `WM-34`) |
| `status.appliedDefinition` | 실제 적용 모델 이름·UID·generation, SLO·선택 수명·Workload generation 및 전처리/데이터셋 identity |
| `status.admission` | 실제 신규 접수 가능 여부·제한 사유·확인 시각 |
| `status.operation` | 변경/삭제 종류·대상 generation·단계·수락 시각·실패 원인 |

수명은 SLO 변경 때도 검증한다. SLO>129s이면 수명 지정 불가. malformed duration, 0·음수·SLO 미만·상한 초과는 필드별 사유로 저장 전에 거절한다. 영구 CRD default를 채우지 않는다.
`spec` 저장/`observedGeneration`만으로 적용을 증명하지 않는다. status writer는 Serving 어댑터 하나이며 큐 길이·요청별 측정값·Node/Pod 상태를 복제하지 않는다.
operation은 gate 중지→drain→연결 해제→새 정의 적용/전용 자원 정리→완료/실패만 표현한다. 범용 단계 그래프·다중 버전 조회기 없이 현재 적용 정의와 변경 대상 한 건을 둔다. Ready/준비 중/준비 대기는 근거와 실제 인스턴스로 산출하고 admission·관리 진행과 구분한다.

### POOL과 재시작

`PoolState`는 POOL ID, `currentNodes`(Node 이름·UID), `instances`(Pod UID·Node UID·실행 조건·실제 준비/연결), `servingWorkloads`(확인된 Workload 이름·UID)를 담는다. 상세 소속·자동제어는 [워크로드 POOL](workload-pools.md)이 원본이다.
측정은 `chill.dacs.io/measurement:NoSchedule` taint로 표시하며 [측정 admission](measurement-admission.md)에 따라 ChillServer만 변경한다. 해당 노드는 scheduler 후보에서 제외하고 prepare/ChangePower는 `NodeInMeasurement`다.
서빙 인스턴스도 taint도 없는 노드는 가용 노드이며 최저 전력모드로 유지한다. DeviceClass label은 장비 분류일 뿐 별도 POOL label은 없다.
Pod Running·목표 배정은 준비/연결 증거가 아니다. 실제 연결만 목록에 넣고 drain 연결은 실제 해제까지 유지한다. snapshot에는 관측 시각·source identity를 담되 여러 Kubernetes/backend 관측을 원자적 API transaction으로 표현하지 않는다.
재시작은 gate부터 닫고 taint·managed Pod·실행기, 삭제 의도·진행 작업·desired/last-applied 차이를 재관측한다. 불명 관계는 사유를 표시하고 분배 제외; 오래된 목표를 실제 연결로 복원하거나 status 저장 실패를 적용 성공으로 덮지 않는다.

### 불변 요청 기록

| 확정 시점 | 기록 |
|---|---|
| 최초 수신 | request ID, 서비스 식별자, 수신 시각·종단 경과 기준 |
| 수락 | Workload UID, 적용 Workload/ModelSpec identity, 모델·입력 계약·SLO·수명 |
| 분배 | POOL ID, Node/Pod UID·container physical identity, 산출물·실행 조건, profile/confidence, 분배 시각 |
| 실행/반환 | native queue/service/backend/native spans, 실제 batch/policy, backend 완료·응답 전달 결과 |

큰 정의/payload를 단계마다 복사하지 않는다. 미배정 요청에 POOL을 미리 쓰거나 재구성 후 실제 배정 기록을 덮지 않는다. payload 전체는 관측에 남기지 않는다.

## 5. API와 권한

| HTTPS 경로 | 결과 |
|---|---|
| `GET /api/v1/workloads[/{id}]` | 변경·삭제 중을 포함한 생존 등록 목록/상세; desired와 applied 분리 |
| `POST /api/v1/workloads` | `{name,spec}` 검증 후 `201`; 근거 없어도 등록 가능 |
| `PUT /api/v1/workloads/{id}` | `{spec}` 전체 목표 교체; 같은 실행 경로 `200`, drain 변경 `202` |
| `DELETE /api/v1/workloads/{id}` | 실제 gate 중지·삭제 의도 저장 후 `202`; 실제 완료는 조회 `404` |
| `POST /api/v1/workloads/{id}/infer` | 독립 원시 표본 한 건, backend 해당 결과 |
| `GET /api/v1/pools[/{id}]` | 현재 Node·Workload 참조·인스턴스·전환, 운영자 조회 |
| `/api/v1/models[/{id}]` | ModelSpec 목록/상세/CRUD, 참조 보호·공유 변경. 무참조 삭제 `204` |
| `GET /api/v1/observations/workloads/{uid}` | 삭제 UID도 구간 요청·관리 raw/집계 조회 |

PUT/DELETE는 조회한 `resourceVersion`을 `If-Match`로 제출한다(Workload `desired.metadata.resourceVersion`, Model `metadata.resourceVersion`). stale은 `409`. 알 수 없는 JSON 필드와 클라이언트의 모델/SLO 덮어쓰기는 거부한다.
`202`는 작업 수락일 뿐 준비/자원 해제 완료가 아니다. HTTP 연결 취소와 별개로 저장된 작업은 유지된다.
조회 필드는 `desired`, `appliedDefinition`, `supportedInputs`, `admission`, `readiness`, `reason`, `pending`, `assigned`, `operation`, `observedAt`을 구분한다. 유효한 무근거 등록은 `201`, `admission:false`, `MissingEvidence`다.
추론은 `inputs:[{name,datatype:"BYTES",data:<base64>}]`이며 적용 Preprocessor의 전체 필드가 표본 하나다. 응답은 `X-Chill-Request-ID`, 확인된 `X-Chill-Workload-UID`를 제공한다.

### 요청 수명 (`WM-33`, `WM-34`: 구현할 계약, 필드 존재/검증 완료 주장이 아님)

`H=X-Chill-Request-Timeout`, `W=적용 requestLifetime`, `D=HandlerConfig.RequestTimeout`, `C=129s`다.

| 지정 | 실제 수명 L |
|---|---|
| H와 W | `min(H,W,C)` |
| H만 / W만 / 둘 다 없음 | 각각 `min(H,C)` / `min(W,C)` / `min(D,C)` |

H는 명시 단위의 양수·129s 이하이며 먼저 검증한다. 잘못된 H는 fallback 없이 `400 InvalidRequestTimeout`. SLO 이상 제약은 W에만 적용하여 더 짧은 H는 허용한다.
시작 옵션은 `0 < --request-timeout ≤129s`, 기본 `30s`; 범위 밖은 시작 거부한다. `X-Chill-Request-Lifetime`은 Go duration 문자열 L, 관측 `lifetimeNanos`는 같은 L의 ns 정수다.
`Runtime.Receive`가 적용 identity·W를 Receipt에 고정하고 `Handler.infer`가 `receivedAt+L`을 한 번 계산한다. `Runtime.Accept`는 입력 검증 전후 identity가 같음을 확인하며 수락 전 교체는 `DefinitionChanged`, 수락 후 deadline은 유지한다. 큐·분배·native에서 시간을 재시작하지 않는다.
`Handler.ServeHTTP` 추론 외곽 상한은 129s로 두고 infer가 실제 L로 context/read deadline을 조인다. W=120s를 D=30s가 자르지 않는다. 관리 operation timeout은 유지한다.
만료 후 오류 응답에 기존 D 길이의 유한 delivery 구간을 예약한다. body drain/flush 전 connection deadline을 지우지 않고 전달 구간도 완료 지연에 포함한다.
API 타입·CEL·`ValidateWorkload`, manager `validatedReferences/CreateWorkload/UpdateWorkload`, `snapshot/appliedStatus/restoreDefinition`, core `Definition`의 선택 수명·retained snapshot을 함께 맞춘다. failed PUT은 desired/applied/resourceVersion/generation/operation을 유지한다. CRD/deepcopy는 `make manifests generate`로 생성하며 수작업 schema·별도 검증 경로는 없다.
loadgen의 `request_lifetime_seconds=None`과 선택 `--request-lifetime`을 재사용한다. W=120s 헤더 생략은 socket `--timeout`을 120s+delivery보다 크게 지정한다. socket 기본 30s를 gateway 수명으로 오인하거나 자동 조회를 추가하지 않는다. 고정 측정의 명시 H 경로는 유지한다.

### 오류·인증·단일 writer

오류는 `code,message[,field]`: 없음 `404`, 등록/입력 `400`, 인증/인가 `401/403`, 진행 충돌 `409`, `QueueFull` `429`, 근거/접수 제한 `503`, adapter transport/backend `502`, 명시 종단 timeout `504`, 연결 생존 취소 `408`이다. native HTTP status(실제 queue timeout `503` 포함)는 유지한다. 닫힌 연결에 전달 성공을 기록하지 않는다.
Kubernetes bearer TokenReview와 동작/경로별 SAR를 사용하며 조회·추론·정의 변경·POOL 운영 권한을 분리한다. 실패는 fail closed, 외부는 TLS, 외부 신원 header는 불신한다. backend에는 사용자 Authorization/cookie/신원 header를 넘기지 않고 CHILL이 확인한 모델/SLO/시간만 전달한다.
전용 SA만 managed spec/status를 쓴다. 사용자 직접 kubectl apply/PATCH 호환 경로·별도 계정 저장소는 없다(관리자 강제 변경은 전제 밖). status는 resourceVersion precondition의 full Update로 `allowed:false`도 보존한다.
짧은 관리 잠금이 참조 생성/삭제·충돌/목표 확정·저장을 직렬화한다. uncached 또는 반영된 현재 등록 상태를 사용하고 informer만으로 승인하지 않는다. drain/준비 중 잠금은 풀고 Workload당 작업 하나, 공유 변경은 관련 전체 작업이다.
허용 TokenReview/SAR는 token hash·HTTP 동작·경로별 10s cache(회수 반영≤10s), 만료 동시 요청은 review 하나(API 2회)를 자기 deadline까지 기다린다. 거절/실패는 cache하지 않는다.

## 6. 등록과 입력 검증

등록은 이름·필수 필드·SLO·참조 존재/삭제 상태를 공통 modelcatalog/modelcompat로 저장 전에 검증한다. 유효한 무근거/무인스턴스 등록은 허용하며 자동 Run/campaign 생성은 없다.
후보는 ①정적 호환성(Unknown 제외) ②입력·runtime·장비·전력·자원별 유효 실측 ③현재 준비 가능성 ④실제 준비/연결 확인으로 구분한다. 무근거 준비 대기와 근거는 있으나 일시적 무인스턴스 준비 중을 구분하며 후자는 큐/timeout 범위에서 접수 가능하다. admission과 SLO 적합성은 같은 값이 아니다.
Catalog는 완료 campaign frozen 조건·결과·검증 raw 참조의 메모리 view다. 현재 후보/비용의 조건별 최신 성공 도출과 namespace 범위는 [워크로드 POOL](workload-pools.md)이 원본이며, 과거 운영자 campaign 선택 선언을 새로 추가하지 않는다.
key는 산출물/입력, runtime/backend 버전·설정, 측정 Node/장비, 전력모드·할당·threads·batch를 포함한다. 같은 DeviceClass라는 이유로 다른 Node를 대체하지 않는다. 필수 메모리/실행 분포 부족은 후보 제외·항목 사유로 제공한다. profile key·적용 범위와 frozen/raw 출처를 보존한다.
요청마다 evidence를 다운로드하지 않는다. batch1 포함 measured/initialized domain, sparse capability/warmup/affinity/threads/GPU UUID/메모리/service 분포를 원 raw에 연결한다. idle baseline·incremental-energy confidence는 BEO `pointPolicy.confidenceLevel`(생략 `"0.95"`)을 쓰고 profile·배정에 보존한다.
입력 필드 `name,format,maxBytes`(`JPEG`,`UTF8`)의 집합/상한/JPEG header/UTF-8은 gateway에서 한 번 검사하고 화소 복호/모델 tensor는 runner module 소유다. RKNN만 모델 `input` tensor 선언을 갖는다. 항목 전처리 실패는 `422 PreprocessFailed`, batch 오류는 `502 BackendError`. 상세 wire/전처리/측정 공유는 [이기종 계약](heterogeneous-workloads.md)이 원본이다.
API·frozen campaign·worker·evidence는 [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)의 단일 저장 계약을 사용한다. 구계약을 새 의미로 읽거나 검증된 입력/profile을 downstream에서 반복 검증하지 않는다.

## 7. 요청 경로와 큐

수신 시각→인증/본문 제한·디코딩→적용 입력·근거 검증→gate/수락 snapshot→ready 인스턴스 또는 Workload FIFO→UID/정의별 firstfloor 배칭→실제 결과/전달 기록 순서다.
의미 검증·gate·수락 카운터·큐 삽입은 같은 Workload 동기화 경계이며 네트워크/준비를 기다리지 않는다. gate 닫기/빈 큐 판정 뒤 새 접수 경쟁을 막는다.
미배정 bounded FIFO는 실행 batch 대기 목적이 아니다. 취소/timeout은 실제 결과로 종료한다. 공유 인스턴스의 Workload 큐를 섞인 FIFO로 바꾸지 않는다.
가중치 target은 Node가 아니라 physical 인스턴스이며 Workload UID/입력 조건별 ready view를 쓴다. 중복·범위·합을 view 설치 때 한 번 검증하고 route/누적 가중치를 cache한다. 요청마다 map/vector/transport/전달 객체를 생성하거나 새 계획 계산을 기다리지 않는다. 실제 준비·capacity로 선택한다.
backend 전달 후 연결 장애로 자동 재실행하지 않는다. backend 완료와 응답 전달은 별도 결과다. 종료는 모든 gate부터 닫고 유한 종료 한도에서 처리하며 확인되지 않은 backend 해제를 주장하지 않는다.
Drain은 gate를 닫아도 기존 미배정 요청을 이전 정의로 분배하여 ①미배정 0 ②해당 UID의 native waiting/running 0 ③실제 결과·전달 기록 완료를 모두 만족해야 끝난다. SLO 초과는 timeout이 아니므로 일괄 폐기하지 않는다.
HTTP timeout/연결 종료/Python `execute` 반환은 physical release 증거가 아니다. native `REQUEST_END`(실행 미진입 queue timeout/cancel 포함), 실제 backend 완료 또는 해당 container 종료로만 해제한다. `assignedUnknown`을 waiting/running으로 바꾸지 않고 점유를 유지한다. queued cancel은 제거, running cancel은 실제 완료/종료까지 소유권 유지; persisted `executionComplete`는 restart drain proof다. 낙관 완료·자동 재실행·별도 recovery engine은 없다.
worker duration은 monotonic, SDK `ObservedAt`은 local receipt, remote UTC는 별도 보존한다. host 시각 차를 duration으로 쓰지 않고 local age/frozen elapsed/measured transport envelope를 쓴다. 개별 SLO·원래 elapsed·UID·request ID와 immutable measured bundle을 전달하고 mutable cap/wait/envelope는 분리한다. actual state에는 profile/decision/cap/적용 시각을 둔다.

### E5의 ChillServer 메모리와 CPU 상한

완료까지 원본, 배정 후 JSON base64 본문(원본의 4/3)을 보유한다. 상한은 인스턴스 `maxQueueSize`의 합+Workload별 `--queue-limit:1024`; `maxQueueBytes:64MiB`는 아래 설정에서 제한을 더 줄이지 않는다.

| Workload | 원본 평균/최대 | 원본+본문 | queue | 최악 인스턴스/몫 | 유입 큐 몫 |
|---|---|---|---|---|---|
| MNV(VQAv2 image) | 147.3/435.8 KB | 343.6 KB | 256 | 8 / 703.8 MB | 150.8 MB |
| Smol(image+question) | 147.3/435.9 KB | 343.7 KB(최대1,017 KB) | 64 | 8 / 176.0 MB(최대520.7 MB) | 150.8 MB(최대446.3 MB) |
| PLC(records) | 21.1/21.2 KB | 49.3 KB | 64 | 0 / 0 | 21.7 MB |

8 Node·`maxReplicas:8`·`maxInstances:16`에서 큰 순(MNV88/Smol22/PLC3.2 MB per instance)으로 채우면 5,632요청, 평균1.20 GB(1.12GiB), 최대 Smol image1.84 GB(1.72GiB)다.
d9 MNV1,600 보유·working set873MiB·기본약100MiB에서 요청당507KB=보유바이트1.47배(GOGC100 여유/임시 decode). r8 P40 최대488MiB, P2 136MiB. 예상 최악 working set `1.12×1.47+0.48=2.13GiB`, 최대 image3.0GiB; live heap1.60/2.19GiB로 GOMEMLIMIT3GiB 아래, limit4GiB에≥1GiB 여유다. 순환 레코드는 평균 쪽이며 E5 단계마다 working set/GC/gateway CPU를 기록한다.
CPU는 r8 P40-P2 차4.7mcore/(req/s), d9 최대7.6mcore/(req/s). 과거 G≈220req/s에서 gateway≤1.7core+search workers3+Tick약0.5=5.2core, limit6에0.8 여유. 같은 크기 Smol 입력의 gateway 비용은 MNV와 같고 PLC는 작다. 이는 sample queue/replica/instance 조건의 선행 계산이지 최종 E5 G 보장이 아니다. d는 이 prefetch 깊이로 측정한다([측정 API](measurement-api.md)).
1s마다 `/gc/heap/live:bytes`를 현재 `debug.SetMemoryLimit`의0.9배와 비교해 atomic 상태를 갱신한다. Accept의 QueueFull 뒤 과부하면 `503 GatewayOverloaded`, `Retry-After:1`; validated 수요와 response_finished(rejected,error)를 남긴다. Workload 큐429와 프로세스 과부하503은 다르다. GOMEMLIMIT 미설정(`MaxInt64`)이면 보호가 걸리지 않는다. 전체 heap 대신 회수 불가 live heap으로 GC 폭주를 피한다. 재측정 G에서 Tick/관측 지연이 남으면 추론 동시 처리 상한을 추가하는 조건부 과제가 남는다.

## 8. 수정과 삭제 적용

| 변경 | 적용 순서와 실패 의미 |
|---|---|
| SLO/수명만, 같은 모델·전처리 identity의 Dataset 참조 변경 | precondition/충돌 검증→desired 저장→수락 경계 snapshot 교체→status/관측→`200`. 수명 생략은 fallback, 기존 요청은 이전 snapshot. SLO 적용과 POOL 완료는 별개 |
| 모델/전처리 실행 변경 | 목표 검증/저장→gate 닫기/진행 기록→`202`→기존 정의 drain→해당 연결 해제→새 snapshot/status→근거 있으면 한도 내 gate 열기, 없으면 적용 완료+준비 대기 |
| Workload 삭제 | precondition→gate 중지+finalizer 삭제 의도→`202`→drain→route/binding 해제→전용 prepare/Serving 자원 실제 종료→finalizer 제거/실제 삭제→`404` |
| 참조 모델 변경 | 관련 충돌 하나라도 있으면 시작 거부. 전체 gate 닫기→전체 drain→전체 연결 해제→닫힌 새 snapshot 전체 게시→전체 gate 열기 barrier. 중간 새 참조 등록/수정도 충돌 |
| ModelSpec 삭제 | desired 참조·applied UID·진행 변경 참조를 같은 관리 잠금에서 확인하고 관련 Workload를 알려 거부. Preprocessor 보호는 이기종 계약 |

삭제 cleanup은 blocking Pod→ReplicaSet→Deployment→Workload owner chain만 지원한다. 미지원 chain/nonblocking link는 cleanup 전 fail closed, 임의 child 정리 없음. 공유 인스턴스의 다른 연결/자원·보존 기록은 삭제하지 않는다.
실패는 실제 단계·적용 정의·종료 결과로 남고 자동 rollback/resume/retry는 없다. 진행 동작이 남으면 충돌, 다음 명시 PUT/DELETE는 확인된 상태에서 시작하여 완료 동작을 중복하거나 실패 후속을 성공으로 실행하지 않는다. 다중 status 쓰기는 Kubernetes transaction이 아니며 barrier는 단일 Runtime 소유다.
POOL 전환은 신규 인스턴스 실제 준비/연결 후 route 편입, 제외 인스턴스 신규 배정 차단→기배정 완료→해제다. Node 이동은 remove+prepare이며 동일 Node를 두 POOL 현재 용량으로 중복 계산하지 않는다. Workload spec/서비스 ID는 바꾸지 않고 snapshot과 조회/분배를 함께 갱신한다. 계획 용량·실행 중 요청 이동을 실제로 기록하지 않는다.

## 11. 관측과 보존

| stream | 보존 내용 |
|---|---|
| `definitions.jsonl` | 지우지 않는 루트; UID/적용 identity/서비스 이름/모델·입력/SLO |
| `requests` | 요청당 배정/최종 행, 수신·검증·접수·분배·큐·실행·교환·응답의 수신 기준 offset, 마지막 batch, 결과/오류 |
| `management` | UID·종류·전후 정의·저장/수락/실제 적용 시각·단계/결과 |
| `scheduler`, `catalog` | 설정/snapshot/decision/action/power raw; 불변 정의/profile/비용/baseline/meter는 catalog key 참조 |

[관측 분리](observability-split.md): private PVC `v1alpha5/<시>/<stream>.<분>.jsonl` append, 지난 시간은 stream별 gzip 하나를 immutable S3 `serving/observations.v1alpha5/<시>/`에 보존한다. retention은 시간 디렉터리 시작 기준 양수 duration 기본2h(현재/직전 시간), 업로드 성공·미완료 보호 조건을 유지한다. 업로드는 writer 잠금 밖이며 append/조회·Flush/Close가 같은 경계를 사용한다.
조회 `from,to`는 RFC3339Nano `[from,to)` 접수 모집단, 반복 `definition,outcome`, `cursor=nextCursor`; 구간과 겹치는 창의 해당 사실만 읽는다. UID/정의/outcome/표본 모집단을 반환하며 전체 필터 집계를 먼저 하고 raw page를 투영한다. page1000 등의 크기로 백분위를 계산하지 않는다.
최종 행의 양수 monotonic elapsed만 지연에 사용하고 `delivery_failed,canceled,incomplete`는 제외한다. error/rejected/timeout은 별도 집계; 무표본은 `sampleCount:0,quantileNanos:null`, quantile은 `gonum/stat.Empirical`. 완료 시각/지연 합성, SLO 변경/동명 재등록의 묵시 합산, 완료-only 유입 계산은 금지한다.
무영속 큐 재시작은 요청을 재실행하지 않는다. 최종 행 없는 배정은 RestartDrain이 실제 종료를 관측하여 incomplete 최종 행을 쓴다. 미확인 실행은 drain 성공이 아니다. append/local sync/read 오류는 무결성을 닫고 window incomplete·기록 공백을 남긴다. local 온전한 archive-only 오류는 반환/로그하고 기존 Flush가 미게시 창을 재전송한다(영구 local 손실/새 recovery 상태 아님).

## 12. 배포와 구현 검증 경계

설정 원본은 [options.go](../../../cmd/chill-server/options.go), [Helm servingEntrypoint](../../../charts/chill/values.yaml)다. TLS Secret과 serving namespace scheduler ConfigMap을 먼저 만들고 SAN 일치 HTTPS/CA 검증을 사용한다. chart는 측정/capacity를 자동 생성하지 않는다.

| 값 | flag/배포 계약 |
|---|---|
| `enabled,tls.secretName,port:8080` | HTTPS, `/etc/chill/tls/tls.crt`, `tls.key`, `--bind-address` |
| `namespace,actuationNamespace` | `--serving-namespace,--actuation-namespace`; 빈 값 release namespace |
| `profileNamespaces:[]` | 반복 `--profile-namespace`; 빈 값 serving namespace. 측정 조회/제출/삭제/profile 범위 |
| limits CPU6/memory4Gi, `goMemLimit:3GiB` | `GOMAXPROCS=limits.cpu`, 필수 Go 형식 `GOMEMLIMIT=goMemLimit`(memory75%, limit과 함께 변경) |
| `goDebug:""` | 비면 미설정, E5 `GODEBUG=gctrace=1`(GC마다 stderr1행) |
| queue1024/request30s/drain2m | `--queue-limit,--request-timeout,--drain-timeout`; SLO timeout 아님 |
| source1s/profile1m | source/profile refresh, 관측 upload1분 |
| maxRequest8388608/maxEvidence67108864/maxQueryEvents1000 | body/evidence/raw page 상한 |
| observationDefinitions/Outcomes `[]` | 반복 `--observation-definition,--observation-outcome` 기본 필터 |
| storage size1Gi/class null/directory `/data`/retention2h | 유지 private RWO PVC; null default class, 빈 문자열 no class; `--observation-directory,--observation-retention`. cutover는 새 경로, 이전 raw 보존 |
| kubeAPI apiServer/tokenFile/caFile | 명시 `--kube-api-server,--kube-token-file,--kube-ca-file`; 기본 in-cluster |
| kubeAPI qps200/burst400 | 항상 `--kube-api-qps,--kube-api-burst`, 시작 때 양수/유한 float32 검증 |
| grace150s | drainTimeout+HTTP 종료 여유보다 커야 함 |

chart가 Go env를 소유한다. 기존 수동 `GOMEMLIMIT=3072MiB,GODEBUG=gctrace=1`은 upgrade 전에 제거하고 overlay `goDebug:gctrace=1`로 이관하여 Pod에 `3GiB/gctrace=1`이 각각 한 번만 남게 한다.
client 생성 전 rest.Config budget 적용. 제어 typed clientset(observer/scheduler/측정), HTTP typed clientset(TokenReview/SAR/설정 ConfigMap), uncached Store는 별도 limiter이므로200QPS는 프로세스 총합 상한이 아니다. 기본200/400은 #232~234 배포에 기록된 설정과 일치시키며 최적값/APF 보장이나 취소 해결책으로 해석하지 않는다. 실제 `context canceled`는 autonomy-off에서도 발생하며 [G1b 진단](scheduler-operations.md#g1b-취소와-client-limiter-진단--sch-51-sch-ac-38)을 따른다.
M4 24.7req/s에서 요청당2 review가50QPS를 채워 접수≈25req/s, validated p95 2,591ms, scheduler API읽기≈1.3s/Tick15–19s였다. d9≈220req/s·대기1,600에서 만료마다 중복 review가 수십 초 제어 호출을 막아30s기한 초과, 전 Node batch≈140s정지. 실제8req/s의5/10 limiter에서도 TokenReview≈5s 대기했다. 위 cache/분리와 실제 두 budget flag를 실험 manifest에 고정하며 [실클러스터 기록](../../plans/2026-10-13-scheduler-live-evidence-campaign-05-closed-loop.md)에 실패·미해명 공백을 구분한다.
caller 권한은 SA와 별개: unbound `FULLNAME-serving-manager` ClusterRole은 허용 namespace의 정확 `/chill/api/v1/namespaces/NS/batch-energy-optimizations[/*]` get/post/delete, namespace Role은 BEO get/list/create/delete다. 선택 caller에 각각 binding한다. scheduler 편집은 unbound `FULLNAME-scheduler-editor`의 `/chill/api/v1/scheduler/settings` PUT+serving namespace 지정 ConfigMap update 모두 필요하다. profiles GET은 read-only. TokenReview 후 `/chill` nonresource SAR+native resource SAR, Dashboard SA/전체 사용자 자동 binding 없음.
측정 삭제는 UID+If-Match RV, 귀속 S3 원본/결과 영구 삭제이며 공유/현재 참조 보호·S3/native 부분 실패를 드러낸다. ChillServer만 전체 namespace BEO/Run/MLIB/PMTC list로 참조 검사, 허용 profiling namespace Job get/list·Pod list로 owned Run Job UID/owner/terminal/실제 Pod 및 Job 생성 전 실패의 부재를 확인한다. caller Job/Pod·범위 밖 child 변경/삭제 권한은 추가하지 않는다.
S3 credential은 귀속 bucket key DeleteObject가 필요하나 chart가 외부 IAM을 확대하거나 caller에 전달하지 않는다. 삭제 사용 전 profiler/controller도 갱신하여 terminal BEO 재실행 금지·BEO/Run uncached typed 조회로 stale reconcile 재발행 창을 닫는다.
ClusterIP HTTPS·전용 SA·기존 evidenceStore.connection/credentials를 사용한다. binary는 `--store-name/endpoint/bucket/region/credentials-secret`, `--observation-directory`, `--tls-cert/key`를 받는다. read-only rootfs에 `/tmp` bounded512Mi emptyDir(immutable candidate·gzip staging), raw는 retained PVC다.

### 남은 수명 검증과 기존 증거

WM-33/34는 구현 후 생략/경계(SLO와 같음,129s)/malformed·0·음수·SLO미만·130s, POST/PUT실패 불변성, H/W 조합·헤더/관측 일치, D30s/SLO60s/W120s의 무조기종료, D0/음수/130s 거부·30/129s 수락, 무드레인200·제거 fallback·수락 경쟁·restart/native 수명·헤더생략 loadgen을 검사해야 한다. 이는 문서 변경의 실행 결과가 아니다.
기존 cutover의 local kind verified HTTPS는 CRUD201/200/202→404, 무근거 admission, fractional percentile, stale/참조409, 새UID, shared barrier, 직접 PATCH403을 확인했다. 실제 native smoke는 named numeric/bool multi-input, UID batch guard·batch 크기·실행 없는 REQUEST_END, unrelated ID 격리; profiling smoke는 fixed batch ACTIVE/warmup제외·IDLE실행0/resident·cgroup peak·Docker root RAPL을 확인했다.
JSONL/MinIO immutable windows·TLS/bearer loadgen503 MissingEvidence, HTTP timeout 전달/archive-only health/append incomplete 회귀를 확인했고 당시 `make lint test`, `make arch-check gopls-check`, default/owner/Profiler helm lint/template·deployment graph가 통과했다. RWO와 replica/Recreate/status/HTTP-only manager는 manifest 증거다.
이 과거 gate는 full qualifying campaign·모든 SRS/hardware PASS가 아니다. 완전 native campaign→Ready gateway, ARM/RKNN 실물·live testbed는 해당 cutover에서 미검증이었다. 원시 표본 이후 증거는 [이기종 검증 기록](heterogeneous-workloads.md), 현재 firstfloor 증거는 [scheduler 구현](scheduler-implementation.md)에서 별도로 읽는다. 코드·생성물·이미지 publish·live 변경을 이 문서 갱신으로 주장하지 않는다.

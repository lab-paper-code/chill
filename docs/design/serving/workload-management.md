# 워크로드 관리 구현 설계

Workload는 모델, 전처리, 선택 Dataset, SLO, 과부하 정책과 요청 수명을 선언한다. 서비스 이름은 객체 이름, 등록 identity는 Kubernetes UID다. 활성 풀은 실제 인스턴스 owner와 binding에서 도출하며 별도 Pool CRD를 두지 않는다.

단일 ChillServer가 관리 API와 접수 gate, 불변 정의, Workload별 ingress 큐와 drain을 소유한다. 배포는 `replicas:1`, `strategy:Recreate`다. 다중 진입점의 큐 동기화나 HA 복구를 가정하지 않는다. 강제 Pod 삭제나 RWO 강제 해제로 drain을 우회하지 않는다.

## 책임 경계

| 소유자 | 책임 |
|---|---|
| `internal/modelcatalog`, `internal/modelcompat` | 선언 검증과 정적 실행 호환성 |
| `internal/serving` | 적용 정의, 요청 문맥, 접수와 분배, physical drain |
| `internal/serving/management` | 참조 보호, 변경과 삭제, status와 관리 관측 |
| `internal/serving/kube` | 현재 자원 조회, private writer, 실물 관측과 예약 |
| `internal/serving/httpapi` | 인증, strict decode, precondition과 HTTP |
| `internal/firstfloor` | 동일 UID와 정의의 배칭, runner와 progress |
| `internal/serving/observation` | FIFO 기록, 보존, 조회와 복구 근거 |
| `cmd/chill-server` | 공유 객체와 transport 조립, 초기화와 종료 |

정적 호환성, 측정 근거, 현재 배치 가능성과 SLO 적합성은 각각 확인한다. DeviceClass와 ChillSystem status에 풀 구성이나 요청별 상태를 복제하지 않는다. 노드 전력 변경은 NodeActuator의 단일 host writer를 사용한다.

## 선언과 적용 정의

`edge.dacs.io/v1alpha1 Workload`는 cluster-scoped다. 등록과 변경은 HTTPS API를 통한다.

| 필드 | 계약 |
|---|---|
| `metadata.name`, `metadata.uid` | 이름 변경 불가. 동명 재등록은 새 UID |
| `spec.modelRef.name`, `spec.preprocessorRef.name` | 필수 catalog 참조 |
| `spec.datasetRef.name` | 선택 측정 Dataset. 없으면 등록 가능, MissingDataset 대기 |
| `spec.slo.percentile` | 유한한 `(0,100]` |
| `spec.slo.targetLatency` | 명시 단위의 양수 Go duration |
| `spec.admission.policy` | `Reject` 또는 `Delay`, 기본 Reject |
| `spec.admission.maxQueuedRequests` | 양수, 기본1024 |
| `spec.requestLifetime` | 선택 duration. 지정 시 SLO 이상, 양수, 최대129초 |
| `status.appliedDefinition` | 실제 적용 catalog identity, SLO, admission과 수명 |
| `status.admission` | 신규 접수 가능 여부, 제한 사유와 확인 시각 |
| `status.operation` | 수락 변경, 대상 generation, 단계와 실패 |

desired spec이나 observedGeneration만으로 적용 완료를 판단하지 않는다. 큰 payload와 정의를 요청 단계마다 복사하지 않으며 관측에는 payload를 저장하지 않는다. 수락 요청은 당시 UID와 정의를 유지한다.

## API와 권한

| 경로 | 동작 |
|---|---|
| `GET /api/v1/workloads[/{id}]` | 생존 등록 목록과 상세, desired/applied/readiness 분리 |
| `POST /api/v1/workloads` | `{name,spec}`, 유효하면201. 측정 없이도 등록 가능 |
| `PUT /api/v1/workloads/{id}` | `{spec}` 교체. 같은 실행 조건200, drain 변경202 |
| `DELETE /api/v1/workloads/{id}` | gate 중지와 삭제 의도202. 실제 삭제 뒤404 |
| `POST /api/v1/workloads/{id}/infer` | 원시 표본 한 건 |
| `POST /api/v1/workloads/{id}/feedback` | client 원본 보고 FIFO 수락204 |
| `/api/v1/models`, `/api/v1/preprocessors`, `/api/v1/datasets`와 항목 경로 | catalog CRUD와 참조 보호 |
| `GET /api/v1/pools[/{id}]` | 실제 풀 소속과 실물 |
| `GET /api/v1/observations/workloads/{uid}` | 삭제 UID도 당시 정의로 관측 조회 |

PUT과 DELETE는 조회한 resourceVersion의 `If-Match`가 필요하다. stale은409이며 미선언 JSON 필드는 거절한다. 202는 수락이며 준비와 자원 반환은 별도다. 연결이 끊겨도 저장된 관리 작업은 유지한다.

Bearer TokenReview와 동작별 `/chill` nonresource SAR를 적용한다. 설정과 측정에는 필요한 resource SAR도 확인한다. 실패는 fail closed다. 외부 TLS를 사용하고 사용자 신원 header, cookie와 Authorization을 backend에 전달하지 않는다. 성공 인증과 인가는 token hash, 동작과 경로별10초 재사용한다. 동시 miss는 review를 공유하며 거절과 실패는 cache하지 않는다.

전용 SA가 managed spec과 status를 쓴다. 짧은 공통 관리 잠금이 참조 생성과 삭제, 충돌 확인, 목표 확정과 저장을 직렬화한다. drain과 원격 준비 중 관리 잠금을 유지하지 않는다.

## 요청 수명

H는 `X-Chill-Request-Timeout`, W는 적용 `requestLifetime`, D는 handler 기본값이다. H는 양수이고129초 이하여야 하며 잘못된 값은 `400 InvalidRequestTimeout`이다.

| 지정 | 실제 수명 |
|---|---|
| H와 W | `min(H,W,129s)` |
| H만 | `min(H,129s)` |
| W만 | `min(W,129s)` |
| 둘 다 없음 | `min(D,129s)` |

W는 SLO 이상이어야 한다. 더 짧은 H는 허용한다. Receipt에 W를 고정하고 server 수신시각에 실제 수명을 더해 deadline을 한 번 정한다. 입력 검증 전후 정의가 바뀌면 `DefinitionChanged`다. accept 이후 변경은 기존 deadline을 바꾸지 않는다.

`X-Chill-Request-Lifetime`과 `lifetimeNanos`는 같은 실제 수명이다. 큐와 native에서 시간을 재시작하지 않는다. 만료 뒤 유한 delivery 구간도 완료 경과에 포함하며 connection deadline을 무기한 해제하지 않는다. SLO 초과와 timeout은 구분한다.

## 접수와 물리 반환

gateway는 원시 필드 집합, BYTES, 길이, JPEG header와 UTF8을 한 번 검사한다. tensor 변환은 runner 전처리 책임이다. [이기종 입력 계약](heterogeneous-workloads.md)을 따른다.

gate 확인과 정의 수락, 카운터와 큐 삽입은 같은 Workload 동기화 경계다. Reject는 SLO 안에 완료 가능한 executor를 고르고 Delay는 대기를 허용한다. 미지 입력은 AdmissionUnknown이다. Workload 큐와 native count/byte cap, HTTP 동시 처리 상한은 별도 물리 한도다.

분배는 정확한 physical instance와 유효 weight를 사용한다. 실제 Ready, owner, 입력과 가용 capacity를 확인한다. backend 전달 뒤 연결 오류로 자동 재실행하지 않는다.

drain은 미배정 요청0, 해당 UID의 native waiting/running0, 실제 결과와 전달 기록 완료를 모두 요구한다. gate가 닫혀도 기존 미배정 요청은 이전 정의로 분배한다. HTTP timeout이나 runner 호출 반환만으로 physical slot을 풀지 않는다. 실제 native 종료, backend 완료 또는 정확한 container 종료가 반환 근거다.

queued 취소는 큐에서 제거하고 running 취소는 실제 완료나 종료까지 소유한다. backendDone과 transportDone을 구분한다. 미확인 assignment는 점유를 유지한다.

## 변경과 삭제

SLO, admission, 수명과 같은 실행 identity의 Dataset 참조 변경은 desired 저장 뒤 수락 경계에서 새 snapshot을 적용한다. 기존 요청은 이전 정의다. 모델과 전처리 실행 변경은 gate를 닫고 기존 정의를 drain한 뒤 연결을 해제하고 새 정의를 게시한다.

공유 catalog 변경은 관련 Workload 전체의 gate 닫기, drain, 연결 해제, 닫힌 snapshot 전체 게시, gate 열기의 barrier를 쓴다. 진행 target의 새 참조는 충돌이다. 삭제는 desired 이름과 applied UID, 진행 target UID를 같은 관리 잠금에서 확인해 보호한다.

Workload 삭제는 drain과 분배 해제, 소유 실물 종료와 정리 뒤 finalizer를 제거한다. 지원 cleanup owner chain은 Pod, ReplicaSet, Deployment, Workload다. 미지원 chain은 fail closed다. 실패는 실제 단계와 partial이며 자동 rollback이나 실패 후속 실행을 하지 않는다.

## 관측과 운영 설정

요청은 수신, 적용 UID와 정의, 배정 실물, native progress, HTTP 교환과 전달 결과를 연결한다. client feedback은 별도 원본이며 요청 final을 덮지 않는다. 중복 원본도 남기고 첫 identity 보고만 집계한다. cohort와 보존은 [관측 설계](observability-split.md)를 따른다.

기록 호출은 불변 FIFO admission이다. disk failure는 sticky ObservationUnavailable로 신규 접수를 닫고 기존 물리 소유권은 유지한다. lifecycle 성공은 유한 Sync prefix와 실제 drain을 확인한다. crash로 유실된 미배정 요청을 재실행하지 않는다.

| 설정 | 의미 |
|---|---|
| binary `--request-timeout` | 기본30초, 양수, 최대129초 |
| Helm `servingEntrypoint.requestTimeout` | 기본129초. binary 기본과 구분 |
| `--operation-timeout` | 기본30분, 원자 작업과 긴 원시 조회 |
| `--drain-timeout` | 기본2분. SLO와 별개 |
| serving memory | frozen Actor request/limit 유지. queue는 그 envelope 안에서 계산 |
| ChillServer memory | chart limit4Gi, GOMEMLIMIT3GiB. CPU request/limit 없음 |
| observation retention | 기본 2시간 private PVC와 immutable archive |
| profile namespaces | 측정 조회와 제출 범위. 생략 시 serving namespace |
| kube API budget | QPS200/burst400. 별도 client limiter의 프로세스 총합 상한이 아님 |

GatewayOverloaded는 회수 불가 live heap 기준503이며 QueueFull429와 구분한다. FIFO는 고정 메모리 상한이 없으므로 요청 큐만으로 RSS 상한을 주장하지 않는다.

정적 조건과 저장 원칙은 [Model Artifact Compatibility Boundary](../../adr/0012-model-artifact-compatibility-boundary.md), [Durable Stored-Contract Compatibility](../../adr/0019-durable-stored-contract-compatibility.md)를 따른다.

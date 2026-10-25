# 워크로드 관리 구현 설계

## 1. 목적과 설계 범위

[워크로드 관리 SRS](../../spec/workloads/SRS.md)의 `WM-01`부터 `WM-34`까지에 대응하는 구현 계약이다.
요구사항의 원본은 SRS이며, 아래에는 현재 리소스 표현, 책임과 적용 순서를 기록한다.
실제 검증 범위와 남은 실행 증거는 12장에서 구분한다.

구성 선택과 adaptive SLO 배칭의 요구사항은 [스케줄러 SRS](../../spec/scheduler/SRS.md)를 따른다.
firstfloor actor와 용량표 replay는 같은 실측 SLO·에너지 selector를 사용한다.
실행기 분담과 실제 전환·원시 관측은 [스케줄러 구현 설계](scheduler-implementation.md)에 기록한다.
native 원시 입력·전처리 module·persistent runner의 동일 실행 경로로 Serving과 Profiling을 연결한다.

### 핵심 결정

| 결정 | 이유 |
|---|---|
| Workload는 cluster-scoped CRD로 저장 | 기존 cluster-scoped ModelSpec과 동일한 연구 클러스터 관리 범위를 사용하고, 등록 건은 Kubernetes UID로 구분 |
| Workload 정의에는 모델·전처리·선택 Dataset 참조, SLO와 선택 요청 수명을 저장 | 서비스 식별자는 객체 이름으로 표현. 정적 POOL 참조나 산출물 복사본을 선언에 추가하지 않음 |
| POOL은 현재 구성의 운영 상태로 제공 | 활성 POOL은 CHILL 서빙 인스턴스에서 산출하고, 측정 POOL은 Node taint로 표시. Workload 정의와 분리하며 별도 Pool CRD는 추가하지 않음 |
| 관리 API와 요청 접수 제어는 기존 ChillServer에 배치 | 신규 접수 중지와 큐 상태를 실제로 소유하는 경로에서 변경을 적용. 별도 관리 서버와 WorkloadController를 만들지 않음 |
| 정의 변경 writer는 하나로 제한 | 워크로드 생성과 모델 삭제의 경쟁, 모델 변경과 신규 접수 사이의 경쟁을 같은 관리 경계에서 처리 |
| 요청은 수락 당시 적용 정의를 유지 | SLO 변경과 POOL 재구성 때문에 기존 요청의 의미가 바뀌지 않게 함 |
| 큐는 bounded in-memory 구조 | SRS는 영속 요청 큐를 요구하지 않음. broker와 요청별 Kubernetes 객체를 추가하지 않음 |
| 실행과 배칭은 firstfloor 재사용 | CHILL은 evidence·워크로드 문맥·정책을 연결하고 existing actor·queue·IPC runner가 실제 실행을 소유 |

하나의 CHILL 서비스에 활성 ChillServer 하나를 둔다. 다중 활성 진입점의 큐 동기화,
분산 관리 잠금이나 고가용성 복구는 이 연구 클러스터의 구현 전제가 아니다.
배포는 `replicas: 1`과 `strategy: Recreate`로 고정한다. replica 수만 1이어도 기본 RollingUpdate는
두 프로세스를 겹쳐 실행할 수 있으므로 허용하지 않는다. 강제 Pod 삭제로 단일 ChillServer를 우회하지 않는다.
이 전제는 chart 검증과 실제 배포 검증에서 확인한다.

## 2. 현재 코드와 재사용 경계

| 현재 근거 | 구현 책임 |
|---|---|
| [ModelSpec API](../../../api/v1alpha1/modelspec_types.go)와 [modelcatalog](../../../internal/modelcatalog/) | 논리 모델, 산출물과 named tensor 계약의 공통 등록 검증 및 projection |
| [HTTP API](../../../internal/serving/httpapi/handler.go) | bearer 인증된 Workload 조회, 관리와 단일 샘플 추론. 기존 투명 모델 proxy 계약은 제거 |
| [Serving core](../../../internal/serving/)와 [ChillServer](../../../cmd/chill-server/) | 불변 적용 정의, 접수 gate, bounded 큐, 인스턴스 route와 실제 physical drain 및 조립 |
| [modelcompat](../../../internal/modelcompat/) | Kubernetes와 독립적인 입력, 실행 경로와 native 설정 호환성. Profiler 후보 resolver를 Serving에 연결하지 않음 |
| [Serving kube](../../../internal/serving/kube/) | private uncached writer, TokenReview/SAR, 현재 POOL과 native 관측, 완료된 측정에서 산출한 검증된 profile catalog |
| [firstfloor adapter](../../../internal/serving/firstfloor/) | 실제 HTTP V2 native 상태·추론과 요청별 monotonic progress. 사용자 인증 header는 ChillServer 경계에 유지 |
| [Evidence Loader](../../../internal/profiling/profiler/evidence/loader.go) | frozen 실행 조건과 원시 evidence의 typed 검증 경계. 이것만으로 SLO나 scientific admission을 증명하지 않음 |
| [모델 호환성 ADR](../../adr/0012-model-artifact-compatibility-boundary.md) | 정적 호환성, 측정 근거, 현재 배치 가능성과 SLO 적합성을 서로 다른 판단으로 유지 |
| [DeviceClass 상태 ADR](../../adr/0011-deviceclass-status-boundary.md) | DeviceClass를 POOL이나 실제 Serving 구성 저장소로 사용하지 않음 |
| [ChillSystem 상태 ADR](../../adr/0008-chillsystem-status-surface.md) | 설치 상태에 워크로드 큐, 노드 구성이나 관리 작업 상태를 덧붙이지 않음 |
| [전환 소유권 ADR](../../adr/0018-transition-ownership-boundary.md) | 전력 변경은 NodeActuator의 단일 writer를 유지. 워크로드 등록이 측정이나 전력 전환을 소유하지 않음 |

현재 입력 계약은 Workload가 참조하는 Preprocessor의 원시 입력 필드(`name`, `format`, `maxBytes`)이며
필드 전체를 하나의 독립 표본으로 검증한다. 모델별 고정 테이블과 shape-only reader는 남기지 않는다.
profile은 선택된 완료 campaign의 메모리 view를 사용한다.
프로파일링은 firstfloor native 경로로만 측정하고 각 측정은 frozen 실행 조건으로 관리한다.

## 3. 데이터 모델과 원본

### 3.1 Workload

`edge.dacs.io/v1alpha1`의 `Workload`는 cluster-scoped CRD다.
cluster scope는 기존 [CRD 범위 ADR](../../adr/0005-crd-scope.md)의 단일 연구 클러스터 방향을 따른다.
다음 YAML은 저장 형태 설명이며, 관리자는 5장의 HTTPS API로 등록한다.

```yaml
apiVersion: edge.dacs.io/v1alpha1
kind: Workload
metadata:
  name: detector-fast
spec:
  modelRef:
    name: detector
  preprocessorRef:
    name: detector-preprocessor
  datasetRef:
    name: detector-records
  slo:
    percentile: 99.5
    targetLatency: 100ms
  requestLifetime: 120s # 선택; targetLatency 이상, 129초 이하
```

- `metadata.name`은 외부 서비스 식별자다. Kubernetes 객체 이름의 형식과 길이 제한을 API에 명시한다.
- `metadata.uid`는 등록 건 식별자다. 삭제 후 같은 이름으로 생성한 객체는 별도 등록 건이다.
- `spec.modelRef`는 기존 ModelSpec을, `spec.preprocessorRef`는 기존 Preprocessor를 참조한다. 선택 항목인
  `spec.datasetRef`는 활성 측정이 표본을 뽑는 Dataset을 참조한다. POOL, 노드, 인스턴스와 profile 참조를
  Workload spec에 넣지 않는다.
- `percentile`은 `0 < p ≤ 100`이며 유한해야 한다. `targetLatency`는 명시적 단위를 가진 양수 duration이다.
  Go `time.Duration`이 표현하지 못하는 값은 오류로 반환하고, 잘라내거나 근사하지 않는다.
- `spec.requestLifetime`은 선택 `metav1.Duration`(`*metav1.Duration`, JSON `requestLifetime,omitempty`)이다.
  생략은 기본값 사용이며 0과 구분한다. 지정하면 `slo.targetLatency ≤ requestLifetime ≤ 129s`여야 한다(`WM-33`, `WM-34`).
  CRD에는 영구 기본값을 채우지 않는다. SLO 변경 시에도 함께 검증하며, SLO가 129초보다 크면
  수명을 지정할 수 없다. duration 파싱 오류, 0·음수, SLO 미만과 상한 초과는 항목별 사유로 거부한다.
- 이름 변경은 지원하지 않는다. 다른 식별자가 필요하면 별도 등록 건으로 생성한다.

상태는 다음 정보만 보유한다. 큐 길이나 요청마다 변하는 측정값은 status에 쓰지 않는다.

| 상태 정보 | 의미 |
|---|---|
| `appliedDefinition` | 진입 경로가 실제 적용한 모델 참조와 모델 UID 및 generation, SLO, 선택 요청 수명, Workload generation |
| `admission` | 실제 신규 접수 가능 여부, 제한 이유와 확인 시각 |
| `operation` | 현재 변경 또는 삭제의 종류, 대상 generation, 진행 단계, 수락 시각과 실패 원인 |

`spec`은 원하는 정의이고 `appliedDefinition`은 요청 경로가 확인한 정의다.
`observedGeneration`이나 CRD 저장 성공만으로 실제 적용 완료를 판단하지 않는다.
상태 writer는 Serving 제어 어댑터 하나이며, 노드나 Pod의 Kubernetes 상태를 복제하지 않는다.
`operation`의 단계는 접수 중지, 드레인, 기존 연결 해제, 새 정의 적용 또는 전용 자원 정리,
완료와 실패다. 8장의 해당 절차만 표현하며 범용 단계 그래프를 저장하지 않는다.
사용자에게 보이는 준비 대기와 준비 중 및 Ready는 실행 근거와 현재 인스턴스로 산출하고,
변경 및 삭제의 진행과 실제 접수 가능 여부는 별도 정보로 함께 제공한다.

현재 적용 정의와 변경 대상 한 건만 운영 상태에 둔다. 이미 수락한 요청이 보유한 불변 문맥과
보존용 정의 기록은 별도이며, 다중 버전 워크로드 조회기나 호환 reader를 만들지 않는다.

`WM-33`/`WM-34` 구현 변경 위치는 다음과 같다. 아래는 구현할 계약이며 현재 필드가 존재한다는 뜻이 아니다.

| 위치(파일:타입·함수) | 변경 계약 |
|---|---|
| `api/v1alpha1/workload_types.go:WorkloadSpec`, `WorkloadAppliedDefinition` | 선택 `RequestLifetime` 필드를 선언하고 status에도 실제 적용 값을 제공. WorkloadSpec CEL은 필드 존재 시 SLO 이상·129초 이하를 검증 |
| `api/v1alpha1/workload_types.go:ValidateWorkload` | 기존 `field.ErrorList`에 `spec.requestLifetime` 범위 오류를 추가. malformed duration은 기존 JSON 디코딩에서 거부 |
| `internal/serving/kube/manager.go:validatedReferences`, `CreateWorkload`, `UpdateWorkload` | 공통 `ValidateWorkload` 경계에서 저장 전에 검증. 기존 `InvalidWorkload`/HTTP 400과 `field`·사유를 반환하고 실패한 PUT은 desired/applied·generation을 바꾸지 않음 |
| `internal/serving/kube/manager.go:snapshot`, `appliedStatus`, `restoreDefinition` 및 `internal/serving/types.go:Definition` | API duration을 core의 선택 duration으로 옮겨 불변 적용·보존 snapshot과 status에 유지. 재시작도 retained definition을 사용하며 새 desired 값으로 기존 요청을 바꾸지 않음 |

API 타입과 CEL marker 변경 후 `make manifests generate`로 CRD schema와 deepcopy를 함께 재생성한다.
생성 파일을 수작업으로 고치거나 별도 검증 경로를 추가하지 않는다.

### 3.2 POOL의 운영 상태

POOL은 Serving runtime의 현재 구성에서 제공하는 `PoolState`다.
각 상태에는 POOL 식별자, 현재 Node 참조와 현재 서빙 Workload 참조를 포함한다.
Workload 참조는 이름과 UID를 함께 사용해 같은 이름의 재등록을 혼동하지 않는다.

```text
PoolState
  currentNodes: Node 이름과 UID
  instances: Pod UID, Node UID, 실행 조건, 실제 준비와 연결 상태
  servingWorkloads: 현재 연결된 Workload 이름과 UID
```

- 활성 POOL 소속은 CHILL 서빙 인스턴스의 owner와 Workload binding에서 산출한다.
  별도 소속 Node label은 두지 않으며, DeviceClass label은 장비 분류로만 사용한다.
- 측정 POOL은 `chill.dacs.io/measurement:NoSchedule` Node taint로 표시한다. ChillServer가 측정 객체에 따라
  붙이고 뗀다([측정 admission](measurement-admission.md)). 스케줄러는 이 노드를 후보에서 제외하고,
  해당 노드의 prepare와 ChangePower는 `NodeInMeasurement`로 실패한다.
- 서빙 인스턴스가 없고 측정 taint도 없는 노드는 가용 노드이며, 가장 낮은 전력모드로 유지한다.
- 실제 인스턴스의 원본은 Kubernetes Pod와 실행기 관측이다. Pod 식별 metadata는 관측 대상을 찾는 데 사용하며,
  Running이나 원하는 설정만으로 모델 준비와 워크로드 연결 완료를 증명하지 않는다.
- `servingWorkloads`는 실제 인스턴스의 확인된 워크로드 연결에서 산출한다.
  등록이나 목표 배정만 있는 워크로드는 포함하지 않는다. 드레인 중인 기존 연결은 실제 해제될 때까지 유지한다.
- 조회하는 `PoolState`는 현재 구성 snapshot에 노드와 참조 목록을 함께 보유한다.
  독립적으로 수정할 서빙 워크로드 목록이나 Workload 쪽의 역방향 POOL 필드는 만들지 않는다.
- 한 노드는 서빙 인스턴스의 binding으로 하나의 활성 POOL에 속한다. 여러 POOL 상태를 산출할 때 같은 Node UID를 중복 배정하지 않는다.
  노드의 Serving 참여 여부와 POOL 소속은 구분하며, 비활성 노드도 전체 POOL 비용 관측에서 제외하지 않는다.

현재 구성은 스케줄러가 선택한 목표와 분리한다. 전환 실행이 실제 준비, 연결, 드레인과 자원 해제를
확인한 결과로 현재 구성을 갱신하고, 같은 snapshot으로 POOL 조회와 분배 대상을 제공한다.
조회에는 각 관측의 시각과 source identity를 포함한다. runtime 내부 snapshot의 일관성과
여러 Kubernetes 리소스 및 backend 관측의 원자성은 다르므로, 이들을 한 API transaction처럼 설명하지 않는다.
재시작 시 Node taint, 관리 대상 Pod와 실행기 상태를 다시 관측한다. 확인할 수 없는 관계는
불명 사유를 표시하고 신규 분배에서 제외한다. 오래된 목표를 실제 서빙 관계로 복구하지 않는다.
재시작 시 접수 gate는 먼저 닫는다. 삭제 의도와 진행 중 작업, 원하는 정의와 마지막 적용 identity의
차이를 실제 실행 상태와 대조한 뒤에만 접수를 연다. status 저장 실패를 적용 성공으로 덮어쓰지 않는다.

### 3.3 요청 문맥

수락한 요청은 적용 정의의 불변 snapshot을 참조한다. 큰 모델 정의나 입력 payload를 매 단계 복사하지 않는다.

| 확정 시점 | 기록 |
|---|---|
| 최초 CHILL 수신 | request ID, 요청한 서비스 식별자, 수신 시각과 종단 경과 시간 기준 |
| 요청 수락 | Workload UID, 적용 Workload 및 ModelSpec identity, 모델과 입력 계약, SLO |
| 실제 분배 | POOL ID, Node UID, Pod UID와 container identity로 식별한 physical 인스턴스, 산출물과 실행 조건, profile 및 confidence, 분배 시각 |
| 실행 및 반환 | native queue/service/backend/native spans, 실제 batch·policy, backend 완료와 응답 전달 결과 |

미배정 요청에 담당 POOL을 미리 기록하지 않는다. 배정 후 현재 POOL 구성이 바뀌어도 해당 요청의
실제 배정 기록을 덮어쓰지 않는다. 입력 payload 전체를 관측 기록에 남기지는 않는다.

## 4. 책임과 패키지

| 소유자 | 책임 |
|---|---|
| `internal/serving` | 적용 정의, 접수 gate, 워크로드별 미배정 큐, 요청 문맥과 관리 동작. Kubernetes와 HTTP에 의존하지 않는 core |
| `internal/serving/httpapi` | 조회와 관리 및 추론 HTTP 경로, 본문 제한과 디코딩, 오류 응답, backend 전달과 응답 전달 관측 |
| `internal/serving/kube` | Workload와 ModelSpec의 private uncached 저장 및 조회, Kubernetes 권한 검증, 모델 및 profile boundary 연결, 상태 기록과 실제 구성 관측 |
| `cmd/chill-server` | concrete 객체와 공유 transport의 조립, 설정 로딩과 종료 처리 |
| 구성 및 전환 경계 | 현재 Node·physical allocation·ready/stop 상태, 용량표 κ 합과 노드 계획의 whole-pool 비용, 원자 동작 완료 |
| `internal/serving/firstfloor` 및 `internal/firstfloor` | frozen context·typed 입력 전달, actual state/progress, UID별 실측 SLO·에너지 배칭·순차 runner |

어댑터는 API와 core를 연결하지만 core가 API, Kubernetes client나 Profiler application을 import하지 않는다.
profile은 6장에서 정한 catalog로 조회하고 evidence는 기존 typed 검증 경계로 소비한다.
Profiler controller나 Profiler 전용 후보 resolver를 요청 handler에서 호출하지 않는다.
구성은 기존 concrete 타입을 사용하며 범용 repository나 workflow engine을 추가하지 않는다.

현재 [.go-arch-lint.yml](../../../.go-arch-lint.yml)은 `model-catalog`, `serving-core`,
`serving-httpapi`, `serving-kube`, `serving-firstfloor`, `firstfloor`를 구분한다.
허용 경계와 `go list`로 캡처한 실제 import는 [패키지 아키텍처](../code-intelligence/README.md#reviewed-model-and-serving-boundaries)에 기록한다.

## 5. API와 권한

### 5.1 HTTP 계약

ChillServer의 HTTPS 경로는 다음과 같다. Workload 조회는 선언과 실제 적용 정의를 구분한다.

| 경로 | 의미와 결과 |
|---|---|
| `GET /api/v1/workloads` | 모든 생존 등록 건의 목록과 접수 상태. 변경 중과 삭제 중인 객체 포함 |
| `GET /api/v1/workloads/{id}` | 원하는 정의, 현재 적용 정의, 지원 입력, 접수 상태와 관리 진행 |
| `POST /api/v1/workloads` | 등록 검증 후 생성. `201`, 근거 부족이어도 등록 가능 |
| `PUT /api/v1/workloads/{id}` | 모델·전처리·Dataset·SLO·선택 요청 수명의 전체 목표 정의 수정. 실행 경로가 같으면 `200`, 드레인이 필요한 변경은 `202` |
| `DELETE /api/v1/workloads/{id}` | 신규 접수 중지와 삭제 의도를 저장한 뒤 `202`. 실제 삭제 완료는 조회에서 확인 |
| `POST /api/v1/workloads/{id}/infer` | 독립 입력 한 건의 추론. backend의 해당 요청 결과 반환 |
| `GET /api/v1/pools` 및 `GET /api/v1/pools/{id}` | 현재 노드와 서빙 워크로드 참조, 인스턴스와 전환 관측. 운영자 조회 |
| `/api/v1/models` 및 `/api/v1/models/{id}` | ModelSpec 목록과 상세 및 생성, 수정, 삭제. 참조 보호와 연관 워크로드 변경 절차 적용 |
| `GET /api/v1/observations/workloads/{uid}` | 지정 구간의 요청 및 관리 기록과 집계. 적용 정의와 결과 종류별로 구분하며, 삭제된 등록 건도 UID로 조회 |

수정과 삭제는 조회한 `resourceVersion`을 `If-Match`로 제출한다. stale 변경은 `409`로 거부한다.
`202`는 관리 요청을 맡았다는 뜻이지 실행 준비나 자원 해제가 끝났다는 뜻이 아니다.
수락 후 HTTP 연결이 끊겨도 저장된 관리 작업은 요청 취소와 별개이며, 객체 조회로 실제 결과를 확인한다.
관측 조회의 `from`, `to`와 적용 정의 및 결과 종류 필터를 집계 조건으로 반환한다.
원시 기록 pagination과 집계 대상 구간을 구분해, 한 페이지의 표본만으로 전체 구간 백분위를 계산하지 않는다.

POST 본문은 `{"name":"...","spec":{...}}`, PUT은 `{"spec":{...}}`만 받는다.
Workload GET의 `desired.metadata.resourceVersion`과 Model GET의 `metadata.resourceVersion`을
사용한다. 알 수 없는 JSON 필드와 클라이언트 모델/SLO 덮어쓰기는 허용하지 않는다.
다음은 이미 등록된 `detector`를 참조하는 예다:

```sh
curl --cacert "$CHILL_SERVER_CA" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' "$CHILL_SERVER/api/v1/workloads" \
  --data '{"name":"detector-fast","spec":{"modelRef":{"name":"detector"},"preprocessorRef":{"name":"detector-preprocessor"},"datasetRef":{"name":"detector-records"},"slo":{"percentile":99.5,"targetLatency":"100ms"}}}'
curl --cacert "$CHILL_SERVER_CA" -H "Authorization: Bearer $TOKEN" \
  "$CHILL_SERVER/api/v1/workloads/detector-fast"
# RV는 방금 조회한 desired.metadata.resourceVersion이다.
curl --cacert "$CHILL_SERVER_CA" -H "Authorization: Bearer $TOKEN" \
  -H "If-Match: $RV" -H 'Content-Type: application/json' -X PUT \
  "$CHILL_SERVER/api/v1/workloads/detector-fast" \
  --data '{"spec":{"modelRef":{"name":"detector"},"preprocessorRef":{"name":"detector-preprocessor"},"datasetRef":{"name":"detector-records"},"slo":{"percentile":99.5,"targetLatency":"200ms"}}}'
```

조회는 `desired`, `appliedDefinition`, `supportedInputs`, `admission`, `readiness`,
`reason`, `pending`, `assigned`, `operation`, `observedAt`을 구분한다.
근거 없는 유효 등록도 `201`이며 `admission:false`와 `MissingEvidence`로 닫힌다.
SLO-only 적용은 `200`, 드레인 변경과 Workload 삭제는 `202`다.
삭제 완료 뒤 상세는 실제 `404`, 같은 이름 재등록은 새 UID다. 참조 없는 Model 삭제는 `204`다.

입력은 적용 정의의 Preprocessor 원시 입력 필드를 사용하며 각 필드는 `BYTES`와 base64 `data`다.
예를 들어 `mobilenet-imagenet` 전처리기의 입력 필드가 `image / JPEG`이면 한 요청은 다음과 같다:

```sh
curl --cacert "$CHILL_SERVER_CA" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -H 'X-Chill-Request-Timeout: 5s' \
  "$CHILL_SERVER/api/v1/workloads/example/infer" \
  --data '{"inputs":[{"name":"image","datatype":"BYTES","data":"'"$(base64 -w0 cat.jpg)"'"}]}'
```

이는 `example`이 이 전처리기를 참조할 때의 예이며 detector의 입력을 대신하지 않는다.
응답은 해당 backend 결과이고 `X-Chill-Request-ID`, 확인된 경우 `X-Chill-Workload-UID`를 제공한다.
`WM-33`의 수명 결정은 아래와 같다. `H`는 선택 `X-Chill-Request-Timeout`, `W`는 현재 적용
정의의 선택 `requestLifetime`, `D`는 기존 `HandlerConfig.RequestTimeout` 기본값이다.

| 지정 값 | 실제 적용 수명 L |
|---|---|
| H와 W | `min(H, W)` — 헤더가 Workload 상한을 늘리지 못함 |
| H만 | H |
| W만 | W |
| 둘 다 없음 | D |

헤더는 명시적 단위의 양수 duration이며 게이트웨이 상한 129초 이하여야 한다.
헤더가 있으면 먼저 검증하고, 잘못된 헤더를 W나 D로 대체하지 않는다(`InvalidRequestTimeout`, HTTP 400).
SLO 이상 제약은 Workload 필드 검증에만 적용하며, 요청자가 더 짧은 H를 지정하는 것은 허용한다.
응답 헤더 `X-Chill-Request-Lifetime`에 실제 L을 Go duration 문자열로 제공하고,
동일한 값을 기존 요청 관측의 `lifetimeNanos`에 나노초 정수로 남긴다. 응답과 관측에서
요청 헤더 값이나 선언값을 실제 적용값으로 대신 표시하지 않는다.

부하 생성기는 헤더를 생략할 수 있다. 기존
`load-generator/src/chill_loadgen/http_client.py:WorkloadHTTPClient.__init__`, `infer`의
`request_lifetime_seconds=None` 경로와 CLI의 선택 `--request-lifetime`을 그대로 재사용한다.
W=120s인 헤더 생략 실행은 `--timeout`을 120초와 응답 전달 여유보다 크게 지정한다.
기존 헤더 생략 시 socket 기본 30초를 게이트웨이 수명으로 오인하거나 새 자동 조회로 바꾸지 않는다.
조건이 고정된 측정의 명시적 lifetime 헤더 경로는 그대로 둔다.

```sh
curl --cacert "$CHILL_SERVER_CA" -H "Authorization: Bearer $TOKEN" \
  "$CHILL_SERVER/api/v1/observations/workloads/$UID?from=$FROM&to=$TO&limit=100"
```

`from`/`to`는 RFC3339Nano, `definition`/`outcome`은 반복 가능한 필터,
`cursor`는 반환된 `nextCursor`다. 집계는 raw page 크기와 무관한 전체 선택 모집단을 사용한다.
모집단은 `[from,to)`에 접수된 요청이다. 조회는 이 접수 시각과 겹치는 관측 창의 줄만 해독하고, 그 요청의 사실만 메모리에 둔다.

오류 응답은 `code`, `message`, 해당할 때 `field`다. 관리 단계는 Workload 조회의 `operation`으로 확인한다.
존재하지 않는 객체는 `404`, 등록 및 입력 검증 오류는 `400`, 권한 오류는 `401` 또는 `403`,
진행 중 작업과 충돌은 `409`, `QueueFull`은 `429`, 근거 부족이나 접수 제한은 `503`으로 반환한다.
transport/backend 어댑터 오류는 `502`, 명시적인 종단 timeout은 `504`다.
native backend의 HTTP 결과는 해당 status를 유지하며 queue timeout의 실제 `503`과 구분한다.
연결이 살아 있는 요청 취소는 `408`, 이미 닫힌 경우 응답을 전달했다고 기록하지 않는다.

### 5.2 변경 권한과 단일 writer

사용자의 신원과 권한은 기존 Kubernetes 인증 및 RBAC를 사용한다.
HTTP 경로에서는 bearer 신원의 `TokenReview`와 경로 및 동작의 `SubjectAccessReview`를 사용한다.
별도 계정 저장소를 만들거나, 외부 호출자가 보내는 신원 header를 권한 근거로 신뢰하지 않는다.
조회와 추론 권한, 모델 및 워크로드 변경 권한, POOL 운영 조회 권한을 분리한다.
권한 확인 실패는 접수를 허용하지 않는다. bearer를 받는 외부 연결은 TLS를 사용한다.
backend로 보낼 때 사용자 `Authorization`, cookie와 신원 header를 전달하지 않고,
모델 및 SLO와 내부 시간 정보는 CHILL이 확인한 값만 연결한다.

사용자에게 ModelSpec과 Workload spec의 직접 쓰기 권한을 주지 않는다.
관리 어댑터의 service account만 정의를 쓰며, 모델 선언 변경을 이 관리 경로로 옮긴다.
운영자의 기존 manifest 입력도 관리 API에 제출하는 방식으로 전환한다.
직접 `kubectl apply`가 gate와 참조 보호를 우회하는 호환 경로를 남기지 않는다.
클러스터 관리자가 강제로 상태를 변경하는 행위는 이 단일 writer 전제 밖의 운영 변경이다.

관리 검증과 선언 저장은 짧은 단일 관리 잠금으로 직렬화한다.
이 잠금 안에서 모델 참조 확인과 생성, 모델 삭제 판정, 충돌 판정 및 변경 대상 확정을 수행한다.
backend 드레인과 준비를 기다리는 동안에는 잠금을 보유하지 않는다.
작업 진행은 워크로드마다 하나이며, 공유 모델 변경은 관련 워크로드들을 함께 잠근 작업으로 취급한다.
이 방식은 낮은 빈도의 연구 클러스터 관리 변경을 전제로 한다.
참조와 충돌 판정은 ChillServer가 반영한 현재 등록 상태 또는 uncached API 조회를 사용한다.
오래된 informer 결과만으로 모델 삭제나 참조 생성의 승인 여부를 정하지 않는다.
전용 ChillServer ServiceAccount만 managed 정의와 status를 쓴다. 상태는 resourceVersion precondition을
가진 full status Update로 기록하여 필수 `allowed:false`도 빠뜨리지 않는다.
TLS Secret, TokenReview/SAR 권한, 유지되는 RWO PVC와 S3 연결은 chart의 ChillServer 배포에 포함된다.

## 6. 등록과 입력 검증

등록에서는 식별자, 필수 필드, SLO와 ModelSpec의 존재 및 삭제 상태를 검증한다.
모델 입력과 산출물 및 실행 경로는 공통 `internal/modelcatalog` projection과
`internal/modelcompat` 순수 계약으로 검증한다. Kubernetes 변환은 어댑터가 맡으며
Serving만의 두 번째 모델 검증 규칙을 만들지 않는다.
검증 실패는 저장 전에 반환하므로 현재 정의를 바꾸지 않는다.
profile이나 준비 인스턴스가 없다는 이유만으로 유효한 Workload 등록을 거부하지 않는다.
근거 부족 상태를 제공하며, ProfilingRun이나 campaign을 자동으로 생성하지 않는다.

실행 후보는 다음 조건을 구분해 얻는다.

1. 모델 산출물과 runtime/backend의 정적 호환성을 확인한다. Unknown도 후보에서 제외한다.
2. 입력과 실행 조건, 장비, 전력모드와 자원 할당에 대응하는 유효한 측정 근거를 조회한다.
3. 현재 제공된 노드와 runtime 및 자원으로 준비 가능한지 확인한다.
4. 준비 인스턴스는 실제 모델 준비와 현재 연결 상태를 확인한 뒤 분배 대상으로 사용한다.

근거가 전혀 없는 준비 대기와, 근거가 있고 인스턴스만 일시적으로 없는 Serving 준비 중을 구분한다.
후자는 설정된 유입 큐와 timeout 안에서 수락할 수 있다. SLO 만족 가능성과 요청 수락 가능성을 동일한 값으로 만들지 않는다.

### 실행 근거 조회 경계

새 catalog는 운영자가 선택한 완료 campaign의 frozen 실행 조건과 결과 및 원시 evidence 참조를 읽어
현재 사용 가능한 profile을 제공하는 메모리 read model이다.
기존 campaign 결과 조회와 `profilerevidence.Loader`의 검증된 결과를 재사용하되,
`VerifiedEvidence` 자체를 SLO 적합 판정이나 scientific admission 결과로 해석하지 않는다.

조회 key는 모델 산출물과 입력 계약, runtime/backend 및 버전과 설정, 측정 대상 Node와 장비,
전력모드, 자원 할당과 스레드, 실행 배치 조건을 포함한다.
측정 출처를 유지하고, 같은 DeviceClass라는 이유만으로 다른 Node의 결과를 자동 대체하지 않는다.
현재 경로에 필요한 메모리나 실행 분포 등 필수 근거가 없으면 후보를 제외하고 부족한 항목을 제공한다.
profile key, 선택할 campaign과 적용 범위는 설정으로 기록한다.

요청 경로는 이 catalog의 검증된 현재 view를 사용하며, 매 요청마다 evidence를 내려받지 않는다.
선택 결과에는 frozen 조건과 원시 근거 참조를 보존한다.
firstfloor/backend 조건에 대응하는 native 근거와 batch 1을 포함한 실제 measured·
initialized batch domain을 사용한다. sparse capability·warmup·affinity·threads·
GPU UUID·메모리와 service 분포를 원래 raw 조건에 연결한다.
idle baseline confidence는 `profileConfidenceLevel`로 설정하며 선택 profile과 실제 배정 기록에 보존한다.

### 구현된 입력 계약

Workload는 Preprocessor와 Dataset을 참조하고, 외부 추론 본문은 Preprocessor 원시 입력 계약의 필드
(`JPEG`, `UTF8`)를 base64로 담은 표본 하나다. 실행 배치는 CHILL 내부에서 요청들로 구성한다.
gateway는 필드 이름 집합, 크기 상한, JPEG 헤더와 UTF-8을 경계에서 확인하고, 화소 복호와 모델 입력 생성은
경로별 전처리 산출물의 module이 runner 안에서 수행한다. 모델 입력 tensor 계약은 RKNN 산출물의 `input`에만
있다. 전처리 실패 항목은 `422 PreprocessFailed`, 배치 실행 오류는 `502 BackendError`다.
backend 전달 시 native 실행 계약의 batch 축과 모델 파일, frozen UID와 정의, 개별 SLO와 elapsed를 연결한다.
계약 전체는 [이기종 워크로드 구현 설계](heterogeneous-workloads.md) 3장과 4장이 원본이다.

API, campaign frozen 조건, worker와 evidence는 같은 계약을 사용한다.
[저장 계약 ADR](../../adr/0019-durable-stored-contract-compatibility.md)에 따라 읽을 수 없는
이전 저장 계약을 새 계약처럼 해석하지 않는다. 검증된 입력 및 profile을 downstream에서 반복 검증하지 않는다.

## 7. 요청 경로와 큐

```text
최초 수신 시각 기록
  -> 권한과 워크로드 확인, 본문 디코딩
  -> 현재 적용 정의로 입력과 실행 근거 검증
  -> 접수 gate 확인과 수락, 적용 정의 고정
  -> 준비된 인스턴스에 분배 또는 워크로드별 유입 큐
  -> firstfloor의 UID·정의별 큐와 실측 SLO·에너지 배칭 및 순차 실행
  -> 해당 요청에 결과 반환, 실제 결과와 시간 기록
```

본문 디코딩은 configured 크기 한도를 적용한다.
현재 정의에 대한 의미 검증, gate 확인, 수락 카운터 증가와 큐 삽입은 워크로드별 같은 동기화 경계에서 처리한다.
모델 변경이 gate를 닫고 큐가 비었다고 판단한 직후 새 요청이 들어오는 경쟁을 허용하지 않는다.
이 경계 안에서는 네트워크나 인스턴스 준비를 기다리지 않는다.

`WM-33`의 실제 수명 결정부는 `internal/serving/httpapi/handler.go:Handler.infer`다.
현재는 D 또는 H만으로 `Receipt.Deadline`과 context를 만든다. 여기에 W를 연결하되 별도 Kubernetes
조회를 하지 않는다. `internal/serving/runtime.go:Runtime.Receive`에서 현재 적용 정의의 수명과
identity를 Receipt에 담고, `infer`가 위 표로 L 및 `receivedAt + L`을 한 번 계산한다.
`Runtime.Accept`에서 정의가 바뀌었다면 수명 판단과 수락 정의가 섞이지 않도록 확인하고,
수명 변경을 가로지른 미수락 요청은 `DefinitionChanged`로 거부한다. 이미 수락된 요청은
기존 deadline을 유지하며 큐·분배·native 전달에서 시간을 다시 시작하지 않는다.
기존 `Runtime.Accept`/`requestEvent`의 `LifetimeNanos`와
`internal/serving/observation_log.go:RequestRow.LifetimeNanos`를 재사용한다.

`handler.go:Handler.ServeHTTP`는 현재 infer 이전에 D로 parent context와 read/write deadline을
설정한다. W=120s가 D=30s에 잘리지 않도록 추론의 외곽 처리 상한은 129초로 두고,
`infer`가 실제 L로 context·read deadline 및 유한 delivery deadline을 조인다.
관리 API와 장시간 operation의 기존 timeout 계약은 바꾸지 않는다.

수용 가능한 준비 인스턴스가 있으면 즉시 분배한다.
미배정 큐는 워크로드별 bounded FIFO로 두며 실행 배치를 모으기 위한 대기를 추가하지 않는다.
취소와 명시적 timeout은 큐에서 요청을 종료하고 실제 결과를 남긴다.
공유 인스턴스를 향한 여러 워크로드 큐의 선택은 배칭 및 구성 정책과 연결하고, 하나의 섞인 FIFO로 대체하지 않는다.

기존 가중치 선택은 유지하되 대상 identity를 노드가 아닌 인스턴스까지 구분한다.
같은 Node의 여러 Pod가 하나의 backend 항목으로 합쳐지지 않게 한다.
분배 기준은 Workload UID와 입력 조건별로 제공하고, 준비가 확인된 인스턴스에 대해서만 갱신한다.
현재 경로는 새로운 구성 계산을 요청마다 기다리지 않는다.
가중치의 중복 대상, 범위와 합은 분배 view를 설치하는 입력 경계에서 한 번 검증한다.
입력별 route와 가중치 합을 캐시하며 요청마다 map이나 누적 가중치 vector를 만들지 않는다.
현재 준비와 physical capacity를 반영하여 가용 대상에서 선택한다.

HTTP transport는 공유하고, 인스턴스 목적지와 전달 객체는 준비 시 구성해 요청마다 만들지 않는다.
backend에 전달한 뒤에는 연결 장애를 이유로 같은 요청을 다른 인스턴스에 자동 재실행하지 않는다.
실제 backend 결과와 응답 전달 결과를 분리해서 기록한다.
정상 종료도 모든 접수 gate를 먼저 닫고 설정된 종료 한도 안에서 요청을 마무리한다.
종료 한도에서 취소하거나 끝내지 못한 요청은 그 결과로 기록하며, backend 종료를 확인하지 못하면
해당 연결이 해제되었다고 기록하지 않는다.

### 드레인 완료 기준

신규 수락을 막는 gate와 기존 수락 요청을 처리하는 경로는 다르다.
gate를 닫아도 기존 유입 큐의 요청은 이전 적용 정의로 배정하고, 인스턴스 큐와 실행을 마무리한다.

드레인 완료는 다음 조건을 모두 만족해야 한다.

- 해당 등록 건의 미배정 요청이 남아 있지 않다.
- 인스턴스에서 해당 워크로드의 대기와 진행 중 실행이 남아 있지 않다.
- 각 요청의 실제 결과와 응답 전달 상태가 기록되었다.

HTTP timeout이나 연결 종료만으로 backend 실행이 끝났다고 간주하지 않는다.
실행기는 완료 또는 실제 취소 결과를 제공해야 하며, 확인할 수 없으면 드레인 미완료와 원인을 표시한다.
SLO 목표 시간 초과는 timeout이 아니므로 이를 이유로 기존 요청을 일괄 폐기하지 않는다.

native trace의 `REQUEST_END`는 Python 실행에 진입하지 않은 큐 timeout/cancel도
실제 release로 관측하는 근거다. transport 종료만 관측하고 native 종료를 모르면
physical occupancy를 유지한다. backend 완료나 해당 physical container 종료 증거가 있어야 해제한다.
Python 모델의 `execute` 반환은 native request release보다 이르므로 종료 근거로 사용하지 않는다.
`assignedUnknown`은 배정은 되었지만 native waiting/running phase 증거가 없는 상태다.
이 값을 실제 큐 대기나 실행 수로 바꾸지 않으며 미확인 physical 점유는 계속 유지한다.
별도 recovery engine, 자동 inference 재실행이나 낙관적인 드레인 완료는 없다.

### E5의 ChillServer 메모리와 CPU 상한

게이트웨이는 요청의 원본 입력을 완료까지 쥐고, 배정된 요청은 firstfloor로 보낼 JSON 본문(base64, 원본의 4/3)도
완료까지 쥔다. 유입 큐의 요청은 원본만 쥔다. 그래서 보유 요청 수의 상한은 인스턴스마다 `maxQueueSize`(게이트웨이는
인스턴스의 미완료 요청을 이 값 아래로 둔다)와 Workload마다 `--queue-limit` 1024의 합이다. firstfloor
`maxQueueBytes`(64 MiB)는 세 Workload 모두 `maxQueueSize` × 원본보다 커서 상한을 바꾸지 않는다.

E5의 계획 공간은 노드 8개, Workload마다 노드당 인스턴스 1개(`maxReplicas` 8), 전체 `maxInstances` 16이다.
요청 크기는 각 Dataset의 evaluation 레코드 평균이다.

| Workload | 원본 (평균, 최대) | 보유 바이트 (원본 + 본문) | `maxQueueSize` | 최악 인스턴스 수 | 인스턴스 몫 | 유입 큐 몫 |
|---|---|---|---|---|---|---|
| mobilenet (`vqav2-val-subset` image) | 147.3 KB, 435.8 KB | 343.6 KB | 256 | 8 | 703.8 MB | 150.8 MB |
| SmolVLM2 (image와 question) | 147.3 KB, 435.9 KB | 343.7 KB (최대 1,017 KB) | 64 | 8 | 176.0 MB (최대 520.7 MB) | 150.8 MB (최대 446.3 MB) |
| PLC (`records`) | 21.1 KB, 21.2 KB | 49.3 KB | 64 | 0 | 0 | 21.7 MB |

- 인스턴스 몫이 큰 순서(mobilenet 88 MB, SmolVLM2 22 MB, PLC 3.2 MB)로 16개를 채운 구성이 최악이다. 보유 요청은
  5,632개이고 보유 바이트는 평균 크기에서 1.20 GB(1.12 GiB), 모든 SmolVLM2 요청이 가장 큰 image일 때 1.84 GB(1.72 GiB)다.
- 보유 요청당 실측 메모리는 d9 병렬 단계(mobilenet 보유 1,600개, working set 최대 873 MiB, 기본 약 100 MiB)에서
  507 KB이고 보유 바이트의 1.47배다. GOGC 100의 heap 여유와 디코딩 임시 버퍼가 차이를 만든다.
- 기본값은 r8 P40(40 req/s)의 working set 최대 488 MiB다. 저부하 P2의 최대는 136 MiB다.
- 최악 working set은 평균 크기에서 1.12 GiB × 1.47 + 0.48 GiB = 2.13 GiB, 최대 image에서 3.0 GiB다. live heap은
  각각 1.60 GiB와 2.19 GiB다. live heap이 `GOMEMLIMIT` 3 GiB 아래이므로 GC는 heap을 3 GiB 안에 두고, memory limit
  4 GiB에는 1 GiB 이상이 남는다. 부하 생성기는 레코드를 순환하므로 E5의 보유 바이트는 평균 크기 쪽이다.

CPU는 게이트웨이 몫과 계획 탐색 몫의 합이다. 게이트웨이는 1 req/s마다 r8 P40과 P2의 차이에서 4.7 millicore,
d9 병렬 단계의 최대 표본에서 7.6 millicore를 쓴다. E5의 총 요청률은 G 이하이고(9.2절), 지금 기록의 G는 d9의 약 220 req/s이므로
게이트웨이는 최대 1.7 core다. 계획 탐색 `planSearchWorkers` 3과 Tick의 나머지 작업 약 0.5 core를 더하면 약 5.2 core이고
CPU limit 6에서 0.8 core가 남는다. SmolVLM2 요청의 게이트웨이 처리(JPEG 머리 검사, base64 해독과 재인코딩)는 같은
크기의 mobilenet 요청과 같고, PLC 요청은 그보다 작다.

이 계산은 E5 설정이 sample 값(`maxQueueSize` 256, 64, 64, `--queue-limit` 1024, `maxReplicas` 8, `maxInstances` 16)과
같을 때 성립한다. E5는 단계마다 ChillServer working set, GC CPU와 게이트웨이 CPU를 기록한다.

이번 캠페인과 E5는 firstfloor `maxQueueSize`(인스턴스의 prefetch 깊이, mobilenet 256, 나머지는 ModelSpec 실행 경로의 값)를
그대로 쓴다. 계획기의 d는 이 큐 깊이에서 잰 값이다([측정 API 3장](measurement-api.md#3-dispatchoccupancymeasurement-occupancy)).

### 과부하 보호

ChillServer는 1초마다 `runtime/metrics`의 `/gc/heap/live:bytes`(마지막 GC가 live로 표시한 heap)를 읽어
`GOMEMLIMIT`(`debug.SetMemoryLimit`의 현재 값)의 0.9배(`overload.go`의 상수)와 비교하고,
결과를 atomic 값 하나에 둔다. 요청 경로는 그 값만 읽는다. 값이 넘은 동안 새 추론 요청은 `Accept`의 `QueueFull` 검사 바로 뒤에서
`GatewayOverloaded`로 거절되고 응답은 503과 `Retry-After: 1`이다. 기록은 `QueueFull`과 같은 경로다. `validated` 사건이
계산 수요로 남고, 응답은 `response_finished`(`rejected`, `GatewayOverloaded`)로 남는다. `QueueFull`은 Workload 하나의
유입 큐 한도를 알리는 429이고 과부하는 프로세스 전체의 상태이므로 이유 코드를 따로 둔다. `GOMEMLIMIT`이 없으면 한도가
`math.MaxInt64`라 보호가 걸리지 않는다.
Envoy Overload Manager의 heap 기준 동작과 같은 모양이다. 비교 대상은 GC가 회수하지 못하는 live heap이다. 전체 사용량은
GOGC 100의 여유로 정상 부하에서도 한도까지 자라고, GC 폭주는 live heap이 한도에 다가갈 때 시작된다.

다음 단계(조건부): 배포 뒤 G를 다시 잴 때 게이트웨이 부하가 Tick이나 관측 기록을 늦추면 추론 동시 처리 수 상한을 더한다.

## 8. 수정과 삭제 적용

### 8.1 SLO 또는 요청 수명만 변경

1. 새 SLO·선택 요청 수명 조합과 수정 precondition을 검증한다. 진행 중 관리 작업이 있으면 거부한다.
2. 원하는 정의를 저장한다.
3. 워크로드별 수락 경계에서 새 적용 snapshot으로 교체한다.
4. 적용과 관측 정의를 상태 및 관측 기록에 남긴 뒤 완료 응답을 반환한다.

기존 요청은 이전 snapshot을 유지한다. 새로 수락하는 요청은 새 SLO를 사용한다.
진입 경로의 SLO 적용 완료와 POOL 재구성 완료를 동일한 사건으로 보고하지 않는다.
저장이나 실제 적용이 실패하면 확인한 적용 정의와 실패 원인을 반환한다.
요청 수명만 바뀌어도 `manager.go:UpdateWorkload`의 `Definition.SameExecution` 경로로
드레인 없이 `200`을 반환한다. 수명을 PUT에서 생략하면 새 요청에는 기본 fallback을 적용한다(`WM-33`, `WM-34`).

### 8.2 모델 또는 모델과 SLO 변경

1. 새 정의를 검증하고 목표를 저장한다.
2. 실제 접수 gate를 닫고 관리 진행을 기록한 뒤 `202`로 수락을 알린다.
3. 기존 유입 큐, 인스턴스 큐와 실행을 수락 당시 정의로 마무리한다.
4. 완료가 확인되면 기존 워크로드 연결을 해제한다. 공유 인스턴스의 다른 워크로드 연결은 유지한다.
5. 새 모델 및 SLO의 적용 snapshot을 설치하고 상태에 기록한다.
6. 새 정의의 근거가 있으면 설정 한도에서 접수를 열고 준비 또는 Ready 상태로 전환한다.
   근거가 없으면 적용은 완료하되 준비 대기로 두고 접수 제한 사유를 제공한다.

저장된 목표와 실제 접수 중지, 적용 완료의 시각을 구분한다.
연결 해제나 준비 실패는 실패한 단계와 실제 적용 정의로 표시한다.
실패했다고 기존 모델로 모두 원복되었다고 가정하지 않는다.
실패한 작업은 종료 결과와 실제 상태로 조회에 남긴다. 진행 중 동작이 남아 있으면 충돌을 거부한다.
다음 명시적 수정이나 삭제는 확인된 현재 상태에서 시작하며, 완료된 동작을 중복 실행하거나
실패한 단계의 후속 동작을 성공한 것처럼 실행하지 않는다. 자동 재시도나 별도 resume API는 추가하지 않는다.

### 8.3 워크로드 삭제

1. 충돌과 삭제 precondition을 확인한다.
2. 실제 접수 gate를 닫고 finalizer가 있는 Workload에 삭제 의도를 저장한다.
3. gate 중지 확인과 삭제 진행 저장 후 `202`를 반환한다.
4. 기존 요청을 같은 드레인 기준으로 마무리한다.
5. 라우팅과 실제 인스턴스의 워크로드 연결을 해제한다.
6. 해당 워크로드만 사용하는 준비 작업과 Serving 자원의 실제 종료를 확인한다.
7. finalizer를 제거한 뒤 실제 객체 삭제를 확인한다. 이후 서비스 목록에서 사라지고 상세는 `404`다.
   전용 자원은 blocking owner link의 Pod → ReplicaSet → Deployment → Workload chain을 확인한다.
   지원하지 않는 chain이나 nonblocking link는 cleanup 전에 fail closed하며 임의 child 정리를 추가하지 않는다.

공유 자원 자체를 해당 워크로드의 소유 자원으로 삭제하지 않는다.
남은 워크로드를 위한 인스턴스 축소나 재배치는 구성 선택에서 판단한다.
삭제 실패도 조회 가능한 단계와 원인으로 남긴다. 실패 종료 후의 명시적 DELETE는 현재 상태에서
같은 삭제 계약을 적용하며, 미완료를 실제 삭제 완료로 보고하지 않는다.
보존된 요청과 정의 기록은 Workload 삭제의 garbage collection 대상으로 두지 않는다.

### 8.4 ModelSpec 수정과 삭제

모델 삭제 판정에는 Workload의 원하는 참조와 실제 적용 참조를 모두 확인한다.
진행 중 모델 교체가 사용하는 참조도 포함하며, 관련 등록 건을 알려주고 삭제를 거부한다.
워크로드 참조 확인과 ModelSpec 삭제는 같은 관리 잠금 안에서 수행해 신규 등록과의 경쟁을 막는다.

참조 중인 ModelSpec의 산출물 또는 입력 정의 수정은 관련 워크로드 전체에 모델 변경 절차를 적용한다.
모델의 새 선언을 저장하더라도 Serving이 사용하는 기존 불변 실행 정의를 즉시 덮어쓰지 않는다.
관련 gate를 모두 닫고 수락 상태를 기록하며, 각 기존 요청이 끝나기 전 새 실행 정의를 적용하지 않는다.
다른 작업과 충돌하는 워크로드가 하나라도 있으면 시작 전에 변경을 거부한다.
모델 변경이 진행 중이면 그 모델을 새로 참조하는 등록과 수정도 충돌로 거부한다.

모든 관련 gate 닫기 → 전체 드레인 → 전체 연결 해제 → 모든 새 snapshot을 닫힌 상태로
게시 → 전체 gate 열기의 barrier를 적용한다. 공유 인스턴스도 포함하며 일부 Workload만 먼저 열지 않는다.
진행과 실패는 관련 워크로드의 관리 상태에서 조회하고, 관리 기록은 같은 모델 변경 대상과 연결한다.
다중 객체 status 업데이트를 원자적 Kubernetes transaction이라고 설명하지 않으며,
요청 경로의 적용 barrier는 단일 Runtime이 소유한다.

## 9. POOL 재구성과 요청 의미

구성 선택 알고리즘은 스케줄러 SRS의 구현 과제다. 현재 POOL은 실제 Node의 측정 taint와 자원,
준비된 native 인스턴스 및 확인된 Workload 연결로 산출한다. 등록만으로 노드를 합성하지 않는다.
Workload spec이나 서비스 식별자를 POOL 이동을 위해 수정하지 않는다.

- 새 인스턴스는 실제 모델 준비와 워크로드 연결이 확인된 후 분배에 편입한다.
- 제외할 인스턴스는 신규 배정을 막고 이미 배정된 요청을 마무리한 뒤 연결 및 자원을 해제한다.
- 노드를 다른 POOL로 옮기는 것은 기존 Workload 인스턴스의 remove와 새 Workload 인스턴스의 prepare로 수행한다.
  기존 연결과 실행을 먼저 마무리하고 새 POOL의 준비된 구성에 편입한다. 같은 노드를 두 POOL의 현재 용량으로 세지 않는다.
- 준비 인스턴스가 없는 동안에도 실행 근거가 유효하면 Workload 유입 큐를 설정 범위에서 유지한다.
- 실제 관측 결과를 현재 구성 snapshot에 반영하고 POOL 조회 및 유효한 분배 대상을 함께 갱신한다.
- 노드 전력모드 변경은 모든 공유 Workload에 영향을 주며 현재 관측에 드러낸다. 자동 비용 비교와 구성 선택은 스케줄러 구현 과제다.

전환 도중 각 요청은 실제 배정 기록을 유지한다. backend에서 실행 중인 요청을 다른 인스턴스로 옮긴 것으로
표시하거나, 계획만 선택된 노드를 준비된 용량으로 계산하지 않는다.

## 10. 실행기 연결의 필수 계약

firstfloor 연결은 Workload UID·request ID·불변 모델/입력·개별 SLO·원래 경과 시간을
native actor에 전달한다. 같은 UID·정의의 호환 요청을 묶고 공유 instance의 다른 UID
running batch는 비선점 대기로 반영한다. mutable cap/wait/envelope는 immutable
measured bundle과 분리하며 actual state에 profile/decision/cap·적용 시각을 제공한다.

persistent runner는 원시 `BYTES` 필드를 전처리 module로 모델 입력으로 만들고, 요청별 출력 필드
또는 항목 실패와 실제 batch 크기를 반환한다. Profiling은 동일 actor/runner에서 fixed K를 순차 실행하고
warmup과 measured service/backend/native/client spans·memory를 구분한다.
CPU quota/threads와 actual affinity, CUDA GPU UUID, RKNN delivered capability를 연결한다.

queue/service/backend/native duration은 worker monotonic 기준이고 SDK `ObservedAt`은
local receipt다. remote UTC를 별도로 보존하며 서로 다른 host의 시각 차를 duration으로
해석하는 대신 local age·frozen elapsed·측정 transport envelope를 사용한다.
queued cancel은 제거되고 running cancel은 actual complete 또는 container 종료까지
physical ownership을 유지한다. persisted `executionComplete`로 재시작 drain proof도 보존한다.

## 11. 관측과 보존

요청 기록은 수신, 수락, 배정, 실행 및 응답 전달의 실제 사건을 request ID로 연결한다.
거절, backend 오류, timeout, 취소와 응답 전달 실패도 보존하며 완료 요청만으로 유입량을 계산하지 않는다.
유효한 완료 시각이 없는 요청에는 완료 지연을 만들어 넣지 않는다.

정의 기록은 Workload UID와 적용 정의 identity별로 모델 및 입력 정의와 SLO를 보존한다.
변경 및 삭제 기록에는 적용 전후 정의, 요청 저장과 관리 수락 및 실제 적용 시각, 진행 결과와 실패 원인을 남긴다.
이는 실험 해석용 기록이며 runtime의 다중 버전 정의 저장소가 아니다.

Serving과 Profiling은 각 관측 계약을 유지하며, 기존 JSONL 관측 기록과 Evidence Store의
immutable 보존 수단으로 원시 근거를 제공한다.

| 기록 | 필수 내용 |
|---|---|
| `definitions.jsonl` | Workload UID와 적용 identity, 서비스 이름, 모델 및 입력 정의와 SLO. 지우지 않는 루트 파일 |
| `requests` | 요청당 배정 행과 최종 행. 수신, 검증, 접수, 배정, 큐, 실행, 교환, 응답 단계의 수신 기준 오프셋, 마지막 batch 사실, 결과와 오류 |
| `management` | 대상 UID와 변경 종류, 적용 전후 정의, 요청 저장과 수락 및 실제 적용 시각, 단계와 최종 결과 |
| `scheduler` | 설정, snapshot, decision, action, 전력 표본 raw. 불변 본문은 catalog key로 참조한다 |
| `catalog` | 그 시간에 참조한 정의, profile, 비용, baseline, meter 본문 |

형식과 보존은 [관측 분리](observability-split.md)가 정한다. private persistent directory의
`v1alpha5/<시>/<stream>.<분>.jsonl`에 append하고, 지난 시간은 stream마다 gzip 하나로
`serving/observations.v1alpha5/<시>/`에 immutable S3 object로 보존한다. 로컬은
`observationStorage.retention`(양수 Go duration, 기본 `2h`)을 시간 디렉터리 시작부터 적용하며 기본은 현재 시간과 직전 시간을 남긴다. Workload 삭제는 이 기록을 삭제하지 않는다. 업로드는 요청 writer 잠금 밖에서
수행하고, 업로드 중에도 새 append와 조회가 진행된다. `Flush`와 `Close`는 같은 업로드 경계를 사용한다.
요청별 Kubernetes 객체나 요청마다 갱신하는 status는 만들지 않는다.

조회는 다음을 분리한다.

- Workload별 전체 유입과 미배정 대기 및 실제 결과.
- 인스턴스별 Workload 대기와 진행 중 실행, 전체 처리 부담.
- 현재 적용한 SLO 목표와 실제 접수 여부.
- 이동 관측 구간 또는 지정 실험 구간의 요청 수, 유효 지연 표본 수와 분포 및 백분위.

집계는 구간, 등록 UID, 적용 정의, outcome 필터와 지연 표본 모집단을 반환하며
전체 필터 모집단을 집계한 뒤 raw event page를 투영한다.
최종 행의 양수 monotonic elapsed만 사용하고 `delivery_failed`, `canceled`,
`incomplete`는 제외한다. error/rejected/timeout outcome은 각각 별도로 집계한다.
지연 표본이 없으면 `sampleCount:0`, `quantileNanos:null`이다.
백분위는 `gonum/stat.Empirical`이며 지연이나 완료 시각을 합성하지 않는다.
SLO 변경 전후와 같은 이름의 재등록을 묵시적으로 같은 목표의 통계로 합치지 않는다.

무영속 큐이므로 ChillServer 장애 후 수락 요청을 투명하게 재실행하지 않는다.
최종 행이 없는 배정 행은 재시작 뒤 RestartDrain이 맡아 실행 종료를 관측하고 미완료 최종 행을 쓴다.
재시작은 실제 인스턴스와 연결 상태를 다시 확인하며, 실행 종료가 불명인 상태를 드레인 성공으로 보고하지 않는다.
관측 저장 실패는 명시적인 오류와 기록 공백으로 드러내고, 실험 evidence가 보존된 것처럼 성공을 주장하지 않는다.
event append 또는 local sync/read 오류는 기록 무결성을 닫고 해당 window를 incomplete로 남긴다.
local 원시 기록이 온전한 archive-only 오류는 반환 및 로그에 남기며 기존 flush가 미게시 window를 다시 보낸다.
이를 영구적인 local 기록 손실로 취급하거나 새 recovery 상태를 추가하지 않는다.

## 12. 배포와 구현 검증 경계

ChillServer 설정은 [options.go](../../../cmd/chill-server/options.go)와
[Helm values](../../../charts/chill/values.yaml)의 `servingEntrypoint`가 원본이다.
기존 모델-only 공개 endpoint, 고정 테이블과 shape-only 호환 reader는 제거되었다.

```sh
helm upgrade --install chill charts/chill --namespace chill-system --create-namespace \
  --set servingEntrypoint.enabled=true \
  --set servingEntrypoint.tls.secretName=chill-serving-tls \
  --set servingEntrypoint.scheduler.configMap=serving-scheduler
```

TLS Secret은 release namespace에 미리 존재해야 한다. Serving namespace에도 scheduler ConfigMap을
먼저 만들어야 한다. CA 검증을 끄지 않고 certificate의
SAN과 맞는 HTTPS 주소를 사용한다. 스케줄러 후보와 비용은 `profileNamespaces`의 완료된 측정에서
조건별 최신 결과로 산출하며, Workload별 자율 제어는 외부 scheduler ConfigMap의
`settings.autonomousWorkloads`(Workload UID 목록)가 소유한다. Helm 값은 이를 선택하지 않는다.
`profileNamespaces`는 측정 조회·제출·객체 삭제와 프로파일 산출을 허용할 namespace 범위이며,
빈 목록은 serving namespace를 사용한다. ChillServer chart는 capacity나 측정 작업을 자동 생성하지 않는다.

`GOMAXPROCS`, `GOMEMLIMIT`, `GODEBUG`는 chart가 소유한다. live Deployment에는 `kubectl set env`로 넣은
`GOMEMLIMIT=3072MiB`와 `GODEBUG=gctrace=1`이 있고, helm이 소유하지 않는 필드라 upgrade 뒤에도 남는다.
lane 뒤 배포는 overlay에 `servingEntrypoint.goDebug: gctrace=1`을 두고, upgrade 직전에
`kubectl -n chill-system set env deploy/chill-serving-entrypoint GOMEMLIMIT- GODEBUG-`로 둘을 지운다. upgrade 뒤에는
Pod의 env에 `GOMEMLIMIT=3GiB`와 `GODEBUG=gctrace=1`이 한 번씩 있는지 확인한다.

| Helm 값 | 실제 flag 또는 배포 계약 |
|---|---|
| `enabled`, `tls.secretName`, `port:8080` | HTTPS ChillServer 활성화, `/etc/chill/tls/tls.crt`와 `tls.key`, `--bind-address` |
| `namespace`, `actuationNamespace` | `--serving-namespace`, `--actuation-namespace`; 빈 값은 release namespace |
| `profileNamespaces:[]` | 반복 `--profile-namespace`; 빈 목록은 serving namespace. ChillServer에 namespaced BEO `get/list/create/delete`, ProfilingRun `get`만 부여 |
| `profileConfidenceLevel:0.95` | `--profile-confidence-level`; measured idle baseline confidence |
| `resources.limits` cpu `6`, memory `4Gi`, `goMemLimit:3GiB` | env `GOMAXPROCS`는 `limits.cpu`, env `GOMEMLIMIT`는 `goMemLimit`(Go 형식, 필수)이다. `goMemLimit`은 `limits.memory`의 75%로 두고 limit을 바꿀 때 함께 바꾼다 |
| `goDebug:""` | env `GODEBUG`; 빈 값이면 넣지 않는다. E5의 GC 기록은 `gctrace=1`(GC마다 stderr 한 줄)로 한다 |
| `queueLimit:1024`, `requestTimeout:30s`, `drainTimeout:2m` | 각각 `--queue-limit`, `--request-timeout`, `--drain-timeout`; SLO timeout 아님 |
| `sourceInterval:1s`, `profileInterval:1m` | 현재 상태와 profile refresh 주기. 관측 업로드는 1분 주기다 |
| `maxRequestBytes:8388608`, `maxEvidenceBytes:67108864`, `maxQueryEvents:1000` | HTTP body, evidence object와 raw query page 상한 |
| `observationDefinitions:[]`, `observationOutcomes:[]` | 반복 `--observation-definition`, `--observation-outcome` 기본 집계 필터 |
| `observationStorage.size:1Gi`, `storageClassName:null` | `/data`의 유지되는 private RWO PVC; null은 cluster default, 빈 문자열은 no StorageClass |
| `observationStorage.directory:/data` | `--observation-directory`; stored-contract cutover는 같은 PVC의 새 경로를 선택하고 이전 raw 기록은 보존 |
| `observationStorage.retention:2h` | `--observation-retention`; 시간 디렉터리 시작부터 적용하는 양수 Go duration. 업로드 성공과 미완료 요청 보호 조건은 유지 |
| `kubeAPI.apiServer/tokenFile/caFile` | 명시적 `--kube-api-server`, `--kube-token-file`, `--kube-ca-file`; 기본 in-cluster |
| `kubeAPI.qps:50`, `kubeAPI.burst:100` | 항상 명시하는 `--kube-api-qps`, `--kube-api-burst`; ChillServer 시작 경계에서 양수·유한 float32 범위를 검증 |
| `terminationGracePeriodSeconds:150` | `drainTimeout`과 HTTP 종료 여유보다 커야 함 |

ChillServer의 Kubernetes API client budget은 `rest.Config`에 client 생성 전에 적용한다.
각 client는 같은 QPS와 burst의 limiter를 따로 가진다. 제어 typed clientset은 observer,
scheduler와 측정 경로의 typed API group 호출이 하나의 limiter를 공유한다. HTTP 경계 client는
TokenReview와 SAR, 설정 API의 scheduler ConfigMap 읽기와 쓰기를 보내는 별도 typed clientset이고,
uncached Store client도 독립된 limiter를 사용한다. 그래서 50 QPS는 ChillServer 전체 API 요청의 합산 상한이 아니다.
기본 50/100은 후보 운영 budget이며 측정된 최적값이나 API server/APF 처리 용량 보장이 아니다.
허용된 TokenReview·SAR 결과는 token hash, HTTP 동작과 경로별로 10초 동안 재사용한다.
kube-apiserver webhook 인증·인가 cache와 같은 방식이며 권한 회수는 10초 안에 반영된다.
만료된 항목을 동시에 찾는 요청들은 review 하나를 함께 기다리고 각자 자기 기한에 기다림을
멈춘다. 만료 한 번은 대기 요청 수와 관계없이 API 호출 두 번이다.
거절과 review 실패는 재사용하지 않고 매번 다시 검사한다.
M4 24.7 requests/s 실행에서 요청마다 두 review가 50 QPS limiter를 채웠다. 추론 접수는
약 25 requests/s에 묶였고(`validatedNanos` p95 2,591 ms), 같은 limiter를 쓰는 scheduler
capture의 각 API 읽기도 약 1.3초씩 기다려 tick이 15–19초가 되었다.
d9 병렬 단계(약 220 requests/s, 대기 요청 약 1,600개)에서는 항목이 만료될 때 기다리던
요청이 각자 review를 보냈다. 쌓인 review가 제어 호출과 같은 limiter를 수십 초 채웠고,
추론 접수와 함께 observer refresh, Workload 진행과 scheduler 호출이 30초 기한을 넘겼다.
모든 노드의 batch가 약 140초 멈췄다.
향후 각 실험에는 두 flag를 포함한 실제 실행 인자를 고정·보존한다.

실제 8 requests/s 실행에서 기본 5/10 limiter의 TokenReview 대기가 약 5초에 달해
명시적 budget을 추가했다. 실패 집계·미해명 decision 공백·재검증 결과는
[실클러스터 제어 기록](../../plans/2026-10-13-scheduler-live-evidence-campaign-05-closed-loop.md#고부하-실행-실패와-kubernetes-client-예산--2026-10-15)에 보존한다.

측정·프로파일 변경의 caller 권한은 ChillServer ServiceAccount와 별개다.
차트의 unbound `FULLNAME-serving-manager` ClusterRole은 허용 namespace별 정확한
`/chill/api/v1/namespaces/NS/batch-energy-optimizations` 및 해당 하위 경로에
`get/post/delete`를 제공한다. 같은 이름의 namespace Role은 BEO `get/list/create/delete`만
제공한다. 관리자가 선택한 caller에 필요한 ClusterRole과 namespace Role을 각각 binding한다.
스케줄러 설정 변경은 unbound `FULLNAME-scheduler-editor` ClusterRole의 `/chill/api/v1/scheduler/settings` PUT과
serving namespace의 같은 이름 Role이 허용하는 설정 ConfigMap 하나의 `update`가 모두 필요하다.
`GET /api/v1/scheduler/profiles`는 읽기 전용이다. ChillServer는 TokenReview 후 `/chill` nonresource SAR와
native resource SAR를 수행한다. Dashboard SA나 모든 사용자에 자동 binding하지 않는다.
측정 삭제는 UID와 `If-Match` resourceVersion을 모두 요구하며 귀속된 Evidence Store 원본·결과를
**영구 삭제**한다. 공유·현재 참조는 보호하며 S3와 native 객체 삭제 사이의 부분 실패를 명시한다.
ChillServer만 공유 근거 참조 검사를 위해 전체 namespace의 BEO·ProfilingRun·ModelLoadedIdleBaseline·
PowerModeTransitionCost에 `list`를 갖는다. 이 조회 권한은 caller에 부여하지 않으며, 허용 범위 밖의
native 측정/child 객체 create/update/delete 권한이나 별도 child 삭제 권한을 추가하지 않는다.
허용 profiling namespace에서는 ChillServer에 Job `get/list`와 Pod `list`만 추가해 owned Run Job의
UID/owner·종료 상태와 실제 Pod 참조를 검사한다. Job 생성 전 실패는 native Run 소유·생산자 이름의
Job이 없음을 조회로 확인한다. caller에는 Job/Pod 권한을 추가하지 않는다.
기존 ChillServer Evidence Store credential에는 해당 bucket의 귀속 key에 대한 S3 `DeleteObject`가
필요하다. 차트는 외부 S3/IAM 정책을 자동 확대하지 않으며 credential을 caller에 전달하지 않는다.
근거 삭제를 사용하기 전에는 ChillServer뿐 아니라 profiler/controller 이미지도 함께 갱신한다.
현재 terminal BEO의 재실행 방지와 BEO·Run의 uncached typed 조회가 반영되어야 삭제 직후
stale-cache reconcile이 근거를 다시 쓰는 창을 닫을 수 있다.

`requestTimeout`은 관리 요청 처리와 body 읽기의 상한이며, 추론에서는 H와 W가 모두 없을 때의
기존 기본 수명 D다. 추론의 실제 처리·body 읽기 상한은 5.1절의 L이고 129초를 넘지 않는다.
처리 만료 뒤 오류 응답을 전달할 수 있도록 기존 `requestTimeout` 길이의 유한 delivery 구간을
별도로 예약한다. body drain과 최종 flush 전에 connection deadline을 지우지 않으며,
실제 응답 완료 지연은 이 전달 구간을 포함해 관측한다.


배포는 `replicas:1`, `strategy:Recreate`, 전용 SA와 ClusterIP HTTPS Service다.
S3 연결은 기존 `evidenceStore.connection`과 credentials Secret을 사용하며
binary는 `--store-name`, `--store-endpoint`, `--store-bucket`, `--store-region`,
`--store-credentials-secret`, `--observation-directory`, `--tls-cert/key`를 받는다.
RWO PVC를 강제 해제하거나 Pod를 강제 삭제하여 ChillServer/drain 전제를 우회하지 않는다.

ChillServer의 read-only root filesystem은 유지하며 `/tmp`에는 512Mi bounded
`emptyDir`를 제공한다. immutable evidence publication의 임시 candidate는 이
writable scratch를 사용하고, 시간별 관측 archive의 gzip staging도 여기에 둔다. 원시 관측 기록은 retained PVC에 기록한다.

### 요청 수명 구현 후 검사 항목 (`WM-33`, `WM-34`)

다음은 구현 시 수행할 검사이며 이 문서 변경에서 실행한 검증 결과가 아니다.

- API/CRD: 생략, SLO와 같은 값, 129s는 수락; malformed·0·음수·SLO 미만·130s는 거부.
  SLO=60s/L=59s의 POST와 PUT, L=130s의 POST와 PUT에서 항목·사유를 확인하고
  PUT 실패 시 기존 desired/applied·resourceVersion·generation·진행 작업이 유지됨을 확인한다.
- 게이트웨이: H/W 모두 없음, 각각만 있음, H<W·H=W·H>W의 실제 L과 응답
  `X-Chill-Request-Lifetime`/관측 `lifetimeNanos` 일치, 잘못된 H 거부를 검사한다.
  D=30s, SLO=60s, W=120s인 헤더 없는 요청이 30초에서 잘리지 않고 120초를 적용하는지 확인한다.
- 변경/실행: 수명만 변경 시 무드레인 `200`, 수명 제거 시 fallback, 수신과 수락 사이 변경,
  기존 수락 요청의 deadline 유지, 재시작 applied snapshot 복원과 native 전달의 원래 수명을 확인한다.
- 부하 생성기: `request_lifetime_seconds=None`의 헤더 부재 및 명시 H 전송을 확인하고,
  120초보다 긴 socket budget으로 헤더 생략 요청의 응답/관측을 대조한다.
- 구현 후 `make manifests generate`, API·manager·HTTP·runtime 및 load-generator의 관련 focused tests,
  `make lint test`를 수행한다. 이 단계는 설계 문서만 변경하며 코드·생성물·실클러스터를 변경하지 않는다.

### 이전 serving cutover의 검증 기록

아래 표는 firstfloor scheduler 통합 이전 작업 단위의 실행 기록이다.
현재 firstfloor 구현의 검증 결과는 scheduler 구현 문서의 최종 실행 기록으로 구분한다. 원시 표본과
전처리 module 경로의 검증 기록은 [이기종 워크로드 구현 설계 9.1절](heterogeneous-workloads.md#91-검증-기록)에 있다.

| 실행 경계 | 관측된 결과 |
|---|---|
| 격리된 local kind와 verified HTTPS | TokenReview/SAR, Model/Workload `201`, 근거 없는 닫힌 admission, fractional percentile, stale `If-Match` `409`, 참조 모델 삭제 `409`, SLO-only `200`, shared model `202` barrier, Workload 삭제 `202` 후 실제 `404`, 재등록 새 UID |
| manager 권한 | 관리 API는 허용되고 managed Workload 직접 Kubernetes PATCH는 `403` |
| 실제 native runner | 공식 Triton Python/ORT CPU named multi-input numeric/bool 결과, 같은 priority의 두 UID batch guard, 여러 실제 batch 크기, native queue timeout과 실행 없는 `REQUEST_END`; 모델 반환 시의 조기 finished는 제거하고 unrelated native ID도 server를 종료하지 않음 |
| 실제 profiling runner smoke | fixed batch ACTIVE의 native inference/execution, warmup 제외, IDLE 실행 0과 model resident, 실제 cgroup lifetime memory peak, Docker root RAPL 읽기 |
| 실제 보존 및 loadgen | private local JSONL과 MinIO immutable windows, TLS/bearer/named tensor loadgen의 `503 MissingEvidence`; RWO PVC는 배포 manifest 계약 |
| 리뷰 회귀 | 실제 HTTP timeout 응답 전달, archive-only 오류의 local health, append 손실의 incomplete metadata가 수정 전 실패하고 수정 후 통과 |
| 최종 repository gate | `make lint test`, `make arch-check gopls-check`, default 및 owner/Profiler별 `make helm-lint helm-template` 통과. `make test`의 manifests/generate, Go vet와 integration도 통과 |
| 배포 계약 | default와 Profiler deployment graph check 통과. 별도 owner render의 단일 replica/Recreate, RWO PVC, status update 및 HTTP-only manager 권한을 manifest에서 확인 |

위 smoke는 full qualifying power campaign이나 모든 SRS/hardware acceptance를 증명하지 않는다.
**완전한 native campaign 근거 → Ready gateway 추론**, ARM/RKNN 실제 장비와 live testbed는
검증하지 않았다. live testbed 변경이나 image publish를 수행한 것으로 주장하지 않는다.
e2e와 위의 미검증 실행 경계는 최종 gate 통과와 구분한다. Go-only 후속 수정으로
추가 package edge가 생기면 architecture와 pinned gopls gate도 다시 적용한다.

## 13. 요구사항 대응

| 요구사항 | 구현 책임과 핵심 검증 |
|---|---|
| WM-01 | 5장, Kubernetes 신원과 동작별 권한 분리 및 직접 정의 쓰기 제한 |
| WM-02 | 3장, 객체 이름과 UID, 모델 및 SLO 필수 정보와 식별자 유지 |
| WM-03 | 3장과 7장, 같은 ModelSpec의 다른 Workload 등록 및 요청 문맥과 큐 분리 |
| WM-04 | 6장, 모델 및 측정 근거의 지원 입력 조회와 적용 정의 사용 |
| WM-05 | 3장과 5장, desired/applied/admission/operation 및 POOL 운영 조회 분리 |
| WM-06 | 5장과 6장, 등록 및 수정 검증과 단일 writer의 삭제 참조 경쟁 방지 |
| WM-07 | 6장, 근거 없이 등록 가능하며 측정은 운영자 요청으로 수행 |
| WM-08 | 6장과 9장, 호환성 및 profile 및 현재 자원의 별도 후보 필터 |
| WM-09 | 6장과 7장, 입력 및 근거 검증과 실제 접수 gate |
| WM-10 | 7장, Workload별 미배정 큐와 한도, 실제 준비 후 분배 |
| WM-11 | 6장과 7장 및 11장, 수락 가능성과 SLO 목표 및 실제 결과 분리 |
| WM-12 | 5장, 생존 등록 건의 상태별 목록과 상세 및 없는 객체 오류 |
| WM-13 | 3장과 8장, 관리 수락과 실제 적용 및 완료의 조회 |
| WM-14 | 8.1절, 새 수락 경계에서 SLO 교체와 기존 요청 snapshot 유지 |
| WM-15 | 8.1절과 10장 및 11장, 정책에 변경 전달과 적용 SLO별 기록 |
| WM-16 | 7장과 8.2절, gate 중지부터 드레인 및 새 모델 적용까지의 순서 |
| WM-17 | 8.2절, 실제 적용 단계와 실패 및 근거 없는 적용 후 준비 대기 |
| WM-18 | 5.2절과 8장, Workload별 한 관리 작업 및 공유 모델 변경 충돌 거부 |
| WM-19 | 7장과 8.3절, 삭제 수락 전 실제 gate 중지와 기존 결과 보존 |
| WM-20 | 8.3절, 연결 해제와 전용 자원의 실제 종료 후 삭제 완료 |
| WM-21 | 8.2절과 8.3절, 공유 인스턴스의 나머지 Workload 유지 |
| WM-22 | 3.1절과 8.3절 및 11장, 삭제 완료 후 서비스 제거와 UID별 기록 보존 |
| WM-23 | 5.2절과 8.4절, 원하는 참조 및 적용 참조 확인과 모델 삭제 거부 |
| WM-24 | 8.4절, 관련 Workload 전체와 공유 인스턴스의 모델 변경 barrier |
| WM-25 | 3.2절과 9장, 서빙 인스턴스에서 산출한 POOL 노드 및 서빙 참조 갱신과 Workload 정의 유지 |
| WM-26 | 6장과 10장, 입력 계약 검증과 Workload별 호환 배치 |
| WM-27 | 6장과 3.3절, 입력과 실행 근거의 대응 및 실제 선택 profile 기록 |
| WM-28 | 3.3절과 7장 및 9장, 수락 정의와 최초 수신 및 실제 배정 기록 유지 |
| WM-29 | 5.1절과 7장 및 11장, 실제 backend와 응답 경로 결과 및 미완료 보존 |
| WM-30 | 7장과 10장 및 11장, 공유 인스턴스에서도 Workload별 부하와 실행 상태 제공 |
| WM-31 | 8장과 11장, 변경 전후 정의와 실제 수락 및 적용 시각 및 결과 보존 |
| WM-32 | 11장, 구간과 표본 선택이 명시된 원시 기록 기반 통계 |
| WM-33 | 3.1절, 5.1절, 7장, 8.1절과 12장, 선택 수명의 적용 snapshot·H/W 최소값·기본 fallback·응답/관측·헤더 생략 부하 |
| WM-34 | 3.1절과 12장, CRD 및 등록/수정 경계의 SLO 이상·129초 이하 검증과 실패 시 정의 유지 |

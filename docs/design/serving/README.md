# Serving Design

## 현재 구현 범위

`cmd/serving-entrypoint`는 단일 HTTPS Workload owner다. Kubernetes bearer 신원을
TokenReview/SAR로 검증하고 모델 및 Workload 등록, 조회, 수정, 삭제와 추론을 제공한다.
`internal/serving`은 불변 적용 snapshot, 접수 gate, bounded Workload 큐,
인스턴스별 route와 physical drain 및 private JSONL 관측을 소유한다.
모델-only 투명 proxy와 시작 시 고정 테이블은 제거되었다.

공식 native Triton Python/ORT CPU serving과 같은 실행 경로의 profiling은 구현되어 있다.
local kind의 HTTPS 관리 계약과 실제 native runner smoke를 확인했지만 full qualifying
native campaign에서 Ready gateway까지, ARM/RKNN 장비와 live testbed는 검증하지 않았다.
정확한 API, Helm 설정과 실행 증거는 [Workload 계약](workload-management.md)에 기록한다.

## 설계 기준

이 문서는 [시스템 명세](../../spec/README.md)의 구현 설계다. 요구사항은
[라우팅과 배칭](../../spec/03_routing.md),
[구성 비용](../../spec/04_configuration_selection.md),
[전환 실행](../../spec/05_transition_control.md)을 따른다.
프로세스 수, CRD 필드, API 경로는 논리적인 역할과 별도로 정한다.

구성 유지·전환 정책의 문헌과 적용 한계는
[온라인 전환 정책 레퍼런스](online-switching-references.md)에 정리한다.
이 문헌 안내는 특정 알고리즘의 채택을 확정하지 않는다.

### Triton 재사용 원칙

CHILL은 공식 Triton의 모델 로딩, native 큐와 dynamic batching, dispatch 및 결과 반환을 재사용한다.
Python backend는 ORT CPU의 named multi-input numeric/bool 실행을 제공하며 공식 custom batching
API를 사용하는 CHILL guard가 한 배치에 **같은 Workload UID만** 포함한다. native priority를 Workload identity로
간주하지 않으며 실제 같은 priority의 두 UID로 혼합 방지를 확인했다.

사용자가 승인한 제외 범위는 **adaptive SLO batch wait와 configuration selection 알고리즘**이다.
공식 custom batching callback은 포함 조건을 다루지만 조절 가능한 delay/dispatch를
구현하지 못한다. 이를 위해 Triton core patch나 외부 HTTP 추론 proxy를 만들지 않는다.
현재 경로는 고정 frozen measured profile과 native 설정을 사용하고 추가 배치 대기를 넣지 않는다.
SLO는 불변 요청 문맥과 관측 목표로 유지하며 HTTP lifetime이나 native queue timeout과 같지 않다.

native admission에는 frozen 계획과 실제 측정 batch 모두 `1..maxBatchSize` 전체가 필요하다.
standalone ORT evidence를 Triton profile로 바꾸지 않는다. 선호 크기는 실제 batch 보장이
아니므로 실제 batch 크기와 요청별 결과, native `REQUEST_END`를 관측한다.

## 워크로드와 외부 요청

워크로드는 서비스 식별자, 기존 모델 등록정보 참조와 SLO 백분위 및 목표 시간을 정의한다.
POOL은 현재 소속 노드와 서빙 중인 워크로드를 운영 상태로 참조하며,
구성 선택과 실제 전환에 따라 이 관계를 갱신한다. 같은 모델이라도 SLO가 다르면
다른 워크로드로 표현할 수 있으므로 모델 이름만으로 서비스 정체성을 대신하지 않는다.

- 사용자는 워크로드 목록·상세에서 식별자, 모델, SLO, 입력 형식과 요청 가능한
  상태를 조회한다.
- 추론 요청에는 워크로드 식별자와 독립적으로 완료할 수 있는 추론 입력 1개를 보낸다.
  CHILL은 등록된 정의를 확인하며, 요청마다 모델·SLO 정의를 복사하거나 덮어쓰게 하지 않는다.
- 운영자는 모델 등록정보와 워크로드를 CRUD하고 모델 연결과 SLO를 관리한다.
  등록, 요청 접수 조건과 변경 및 삭제는 [워크로드 관리 SRS](../../spec/workloads/SRS.md)를 따른다.
  리소스와 API, 접수 중지와 드레인 및 동적 POOL 상태는 [워크로드 관리 구현 설계](workload-management.md)에서 구체화한다.

하나의 추론에 함께 필요한 여러 tensor나 파일은 입력 1개를 구성할 수 있다.
독립적인 추론 입력은 각각 별도 요청으로 보낸다. 응답과 SLO는 각 요청에 적용하며,
사용자는 내부 인스턴스 배치나 실행 배치 크기를 알 필요가 없다.
워크로드의 SLO 백분위·목표 시간은 스케줄러의 제어 목표다. 요청 지연은 CHILL
수신부터 응답 반환 완료까지 측정하며, 집계와 분석 책임은
[SLO 관측과 집계](../../spec/README.md#slo-관측과-집계)를 따른다.

## 노드 자원과 모델 인스턴스

노드와 서빙 파드는 1:N, 하나의 서빙 파드가 제공하는 모델 인스턴스와 워크로드도
1:N 관계를 허용한다. 노드 하나를 모델 하나나 워크로드 하나의 전용 장비로 취급하지 않는다.
한 파드는 하나의 모델 실행 인스턴스를 제공하며, 같은 산출물·런타임·입력 조건으로
처리할 수 있는 여러 워크로드가 이를 공유할 수 있다.

Triton 서버와 내부 model instance는 같은 단위가 아니다. 모델의
`instance_group.count`는 기본값에 의존하지 않고 **1로 명시해 고정**한다.
instance group과 대상 GPU 구성도 파드가 제공하는 실행 인스턴스의 총수가 하나가
되도록 맞춘다. `count`는 스케줄러가 동적으로 변경하거나 탐색하는 변수가 아니다.

| 조절 대상 | CHILL의 제어 단위 |
|---|---|
| 전체 모델 실행 인스턴스 수 | 서빙 Pod 추가·제거 |
| 인스턴스 하나에 줄 실행 자원 | Pod별 자원 할당과 그에 연동되는 backend 설정 |
| 한 번에 묶어 실행할 입력 수 | 배칭 정책 |

이는 Pod 단위 자원 할당이 Triton의 `count: 1`을 기술적으로 강제해서가 아니라,
CHILL의 자원 할당 단위·프로파일 실행 구성 단위·서빙 인스턴스를 맞추기 위한 선택이다.
`count: 1`은 단일 CPU나 단일 스레드를 뜻하지 않는다. CPU 추론 스레드 설정은
Pod의 CPU 할당량에 맞추며, 하나의 배치 내부에서 여러 CPU를 활용할 수 있다.
여러 Pod의 인스턴스는 각자 배치 하나씩 병렬 실행할 수 있다.

현재 구성은 실제 Node label과 자원, native 준비와 연결 관측으로 산출한다.
파드 수, 자원과 전력모드의 자동 configuration selection 알고리즘은 승인된 제외 범위다.
자원 변경과 전환 계약은 [전환 명세](../../spec/05_transition_control.md)의 기준으로 구분한다.
메모리는 프로파일에서 관측한 사용량으로 필요한 예산을 산정하는 가용성 제약이다.
시스템 예약분과 프로파일에 포함되지 않은 서빙 부가 사용량을 고려하며,
메모리 할당량 자체를 성능·비용 조절용 탐색 변수로 사용하지 않는다.

공존의 기준은 순간 CPU/GPU 사용률이 아니라 **배정 가능한 자원**이다. 이미 다른
서빙 파드에 할당한 연산 자원을 중복 할당하지 않는다. CPU 파드끼리는 분리된 CPU
할당, GPU 파드끼리는 서로 다른 물리 GPU를 사용한다. GPU 파드의 보조 CPU와
메모리도 계산하므로, CPU 파드와 GPU 파드의 공존에도 자원 배정이 필요하다.

별도 장치 할당기를 만들지 않고 Kubernetes의 기존 자원 할당을 사용한다. GPU는
운영자가 마련한 장치 자원 노출·할당을 활용하며, 한 물리 GPU를 여러 모델 파드가
time-slicing이나 MPS로 나누는 구성은 범위 밖이다. CPU의 request/limit만으로는
CPU 집합이 분리되지 않는다. 전용 CPU 배정에는 CPU Manager의 static 정책,
Guaranteed Pod와 정수 CPU 할당 등 플랫폼 조건이 필요하다. 프로파일링 Job의
관측 컨테이너를 포함한 Pod 자원 설정도 이 조건과 맞아야 한다. 실제 지원 여부와
배정 결과는 배포 시 확인하며, 이 문서가 현재 클러스터의 격리를 입증하지는 않는다.

공유 인스턴스에서도 적용 정의, 유입 큐와 대기 및 실행 관측은 Workload UID로 구분한다.
native custom batching API의 CHILL guard는 다른 UID를 같은 실행 배치에 넣지 않는다.
CPU 스레드와 `instance_group.count: 1`은 frozen profiling/serving 계약에 맞춘다.
한 실행 배치가 끝난 뒤 다음 backend 실행을 시작하며 실제 완료 경계는 native 관측으로 확인한다.
adaptive SLO 기반 추가 대기 알고리즘을 구현했다고 주장하지 않는다.

할당을 분리해도 캐시·메모리 대역폭·노드 전력·열은 영향을 주고받을 수 있다.
우선 자원이 맞는 공존 후보를 허용하고 실제 지연·전력 차이를 관측한다. 자원 적합성과
SLO 적합성은 별개의 판단이며, 관측 없이 무간섭을 보장하거나 모든 조합의 간섭
예측기를 만들지 않는다. 노드 전력모드 변경은 공존 중인 모든 인스턴스에 영향을 준다.

## 요청 처리 흐름

```text
워크로드 식별자 + 추론 입력 1개
            │
            ▼
워크로드 확인 · 입력 검증
            │
            ▼
워크로드별 유입 대기열
            │
            ▼
요청별 인스턴스 선택 · 시간 정보 전달
            │
            ▼
선택한 인스턴스의 워크로드별 큐
            │
            ▼
Triton native batching (same-UID guard, frozen 설정, 추가 대기 없음)
            │
            ▼
      Triton backend 추론
            │
            ▼
각 결과를 해당 요청에 반환
```

한 상자가 별도 서비스나 CRD 하나를 뜻하지 않는다. 유입 대기열도 우선 논리적인
요청 관리 구조이며, 별도 메시지 브로커나 영속 큐를 요구하지 않는다.

### 처리 단위와 추적 정보

| 단위 | 관리할 정보 |
|---|---|
| 사용자 요청 | 워크로드, CHILL 수신·분배·Triton 큐 진입·응답 반환 완료 시각, 배정 인스턴스, 대기·진행 상태와 실제 완료·오류·거절·timeout/cancel·미완료 결과 |
| 실행 배치 | 같은 워크로드의 호환되는 입력 묶음과 각 입력·결과에 대응하는 요청 |

각 요청은 모델 인스턴스 하나에 배정한다. 인스턴스는 같은 워크로드의 호환되는
요청들을 묶어 실행하고 각 결과를 해당 요청에 연결해 반환한다.
서로 다른 요청의 완료 순서는 독립적이다.

CHILL은 요청별 원시 시각과 처리 결과를 보존하고, 워크로드별 요청 수와 지연
분포·백분위를 제공한다. 실제 완료·오류·거절·timeout/cancel·미완료를 관측에
드러내며, 실험의 성공·실패 해석은 분석자가 수행한다. 관측 구간과 통계 부재의
표현은 [SLO 관측과 집계](../../spec/README.md#slo-관측과-집계)를 따른다.

### 시간 기준과 실행 종료

종단 지연은 entrypoint의 동일 monotonic 기준으로 최초 수신부터 응답 반환 완료까지 측정한다.
Triton 큐 진입과 compute 시작은 raw `CLOCK_MONOTONIC` nanoseconds로 보존한다.
collector `At`은 UTC 관측 시각이지 native 큐 진입 UTC가 아니다.
서로 다른 host의 시각을 단순 차감하거나 native 시각을 임의로 변환하지 않는다.

Triton의 [native queue policy](https://docs.nvidia.com/deeplearning/triton-inference-server/user-guide/docs/user_guide/batcher.html#queue-policy)는
frozen 큐 한도와 timeout을 적용한다. adaptive SLO batch waiting은 승인된 제외 범위이며
queue timeout이나 고정 설정을 그 알고리즘으로 설명하지 않는다.

`REQUEST_END`는 Python execute에 도달하지 않은 queue timeout/cancel도 실제 release로 제공한다.
caller나 transport가 종료되어도 실행 종료가 불명이면 physical capacity를 유지한다.
실제 backend 종료 또는 해당 physical container의 종료 증거를 확인해야 drain을 완료한다.
자동 inference 재실행이나 별도 recovery engine은 없다.

### 책임 분리

| 역할 | 책임 |
|---|---|
| 현재 구성 및 전환 관측 | 실제 Node label, 자원, Pod/native 준비와 연결을 반영. configuration selection은 제외 |
| 요청 진입 및 분배 | TLS/bearer 권한, Workload와 named tensor 확인, 불변 snapshot 수락, bounded 큐, 인스턴스 선택과 응답 관측 |
| CHILL native batching 연결 | frozen measured 설정과 same-UID include guard, batch/request identity와 관측 |
| Triton native batcher/backend | 모델 로딩, 큐와 timeout, dispatch, Python/ORT CPU 추론과 요청별 결과 및 trace 종료 |

요청마다 새 구성 계산이나 evidence 다운로드를 기다리지 않는다.
입력별 route와 가중치 합을 갱신 시 캐시하고 현재 준비와 physical capacity 안에서 분배한다.
수용 가능한 인스턴스가 있으면 즉시 전달하며 유입 큐가 실행 batch를 모으는 대기를 추가하지 않는다.

워크로드별로 유입 대기 상태를 구분하여 하나의 섞인 FIFO가 자원 할당 정책을
대신하지 않게 한다. 같은 모델 인스턴스를 공유하는 경우에도 이 경계를 유지한다.

부하는 워크로드별 inputs/s로 관측하며 요청률과 같다. 대기·완료량도 요청 수로
기록하고 내부 실행 배치 수와 구분한다. 관리 작업 중 요청과 공유 인스턴스의 처리 계약은
[워크로드 관리 SRS](../../spec/workloads/SRS.md)를 따른다.

## Why concealment forces routing

Some configuration changes cannot be made underneath live traffic. A power mode
applies to a whole node, and on a memory-constrained device a new model cannot
be resident before the old one leaves.

The [transition specification](../../spec/05_transition_control.md) requires
planning request handling while a node changes state. Routing can move traffic
to other ready nodes when the changing node cannot continue serving.

## 기존 전달 경로의 네트워크 관측

아래는 기존 전달 경로를 설계할 때 기록한 관측이다. 워크로드별 요청 처리·배칭의
검증이나 현재 라이브 상태 확인을 대신하지 않는다.

| Observation | Verified against |
|---|---|
| `kube-proxy` runs only on the three control-plane nodes | cluster |
| every `istio-system` workload runs only on the control plane, and no pod on any edge node carries an `istio-proxy` sidecar (0 of 100) | cluster |
| Gateway API CRDs are not installed; the EdgeMesh agent's RBAC covers `networking.istio.io` and not `gateway.networking.k8s.io` | cluster |
| `edgemesh-agent` runs on all ten nodes | cluster |
| EdgeMesh's load balancer implements round-robin, random, and consistent-hash selection, reads `DestinationRule` and not `VirtualService`, and applies no per-backend weight | [`pkg/loadbalancer`](https://github.com/kubeedge/edgemesh/tree/main/pkg/loadbalancer) |
| CHILL already depends on EdgeMesh: the operator labels the evidence-store Service with `service.edgemesh.kubeedge.io/service-proxy-name`, and edge measurement workers reach it on every Run | cluster |
| a cloud-side pod reaches a pod on `jetsono` through a single-endpoint Service carrying that same label, and the entry point forwards a request across that path | cluster |
| that forwarding costs about 1.4 ms on a connection the entry point keeps open: 6.0–6.7 ms end to end against 4.5–5.8 ms for the same request answered without leaving the cloud | cluster |

KServe and Knative do not ship a proxy. KServe declares Istio or an
`ingressClassName` as a prerequisite and recommends Gateway API; Knative defines
its own `KIngress` abstraction and ships three interchangeable implementations.
Both assume a networking layer that can weight.

## Where a request branches

EdgeMesh's proxy is a DaemonSet. It intercepts through the iptables rules of the
node the client runs on and selects an endpoint there. No point exists through
which all requests pass.

An external consumer has no such node, so its request enters the cluster
somewhere, and that entry is the one point every external request crosses.

## 기존 전달 경로의 배경

아래 전달 구조는 Workload cutover 전의 네트워크 설계 배경이다. 현재 고정 테이블이나
모델-only 공개 proxy를 유지한다는 뜻은 아니다.

Choosing a backend and reaching it pull in opposite directions: calling a shared
Service hands the choice to EdgeMesh, which applies no weight, and calling a pod
address directly does not resolve across the tunnel.

Each serving node gets its own Service with a single endpoint. A Service with
one endpoint leaves EdgeMesh nothing to select, so its transport applies and its
policy does not.

```
entry point ── weighted choice of Service ──▶ chill-serving-<node>
                                                    │  EdgeMesh tunnel
                                                    ▼
                                              the serving pod
```

The decision is CHILL's and the transport is EdgeMesh's, which is the division
that already governs power mode.

현재 목적지는 노드 이름만이 아니라 실제 Pod UID와 container identity까지 구분한다.
Kubernetes adapter가 현재 Node/Pod, native 모델 준비와 profile 조건을 관측하고
core에 준비된 인스턴스 및 입력별 route를 공급한다. 같은 Node의 서로 다른 모델 파드를
한 목적지로 합치거나 원하는 배포 설정만으로 준비된 용량으로 세지 않는다.
정적 routing table과 이를 렌더하던 노드별 Service는 현재 공개 설정 경로가 아니다.

## 구현 대응

| 현재 기능 | 구현 경계 |
|---|---|
| 모델 및 Workload CRUD | HTTPS owner와 private uncached Kubernetes writer, TokenReview/SAR, resourceVersion `If-Match`, 참조 삭제 보호 |
| 요청 접수 및 분배 | immutable applied snapshot, bounded Workload 큐, 준비된 인스턴스의 route와 가중치 합 |
| 모델 변경 및 삭제 | 모든 관련 admission 닫기, 전체 physical drain, 해제, 닫힌 새 snapshot 전체 게시 후 gate 열기 |
| 현재 POOL | 실제 Node label과 자원, native instance와 확인된 Workload 연결. 별도 Pool CRD나 합성 Node 없음 |
| 실행과 profiling | 공식 Triton Python/ORT CPU, same-UID batch guard, 전체 frozen `1..Max` measured profile |
| 관측 보존 | private persistent JSONL과 immutable S3 window, 전체 모집단 집계와 no-data null |

외부 요청은 `/api/v1/workloads/{id}/infer`의 named tensor 독립 샘플 한 건이다.
[Workload 관리 계약](workload-management.md)의 입력 검증과 SLO를 우회하는 alias는 없다.

## 프로파일 기반 지연 계산과 검증

프로파일은 frozen 자원, 전력모드와 native batch 조건별 raw 실행시간 분포,
incremental energy/item과 실제 memory peak를 제공한다. 완료 campaign과 원시 evidence의
identity 및 조건을 검증하고 idle-baseline confidence 설정을 profile과 배정 기록에 유지한다.
configuration selection이나 요청별 adaptive 추가 대기 예측 알고리즘은 제외되어 있다.

profiling과 serving은 같은 [실행 계약](../../spec/00_measurement.md#측정의-단위)을 사용한다.
같은 실행 조건의 근거는 SLO가 다른 Workload에도 재사용하되 다른 Node나 standalone ORT의
결과를 native Triton 근거처럼 바꾸지 않는다. 현재 준비와 측정 근거, SLO 관측은 별개 판단이다.

실행 구간 관측에는 [Triton의 지연 메트릭](https://docs.nvidia.com/deeplearning/triton-inference-server/user-guide/docs/user_guide/metrics.html#latencies)과
기존 추적 기능을 우선 활용한다. 종단 지연은 CHILL 수신부터 응답 반환 완료까지의
요청별 기록으로 집계한다.

같은 노드에 공존하는 파드들의 단독 실험에서 잰 노드 전체 전력을 단순 합산하지 않는다.
측정 경계는 [프로파일링 설계](../profiling/README.md#serving-resource-allocation)를 따른다.

## 구현 및 검증 경계

local isolated kind와 verified HTTPS로 등록, 조회, fractional SLO 수정, stale `If-Match`,
공유 모델 변경 barrier, 참조 삭제 보호와 실제 Workload 삭제 및 새 UID 재등록을 확인했다.
private RWO journal과 MinIO immutable window, TLS/bearer/named tensor loadgen도 실행했으며
근거 없는 admission은 `503 MissingEvidence`로 닫혔다.

실제 공식 native runner에서 다중 named numeric/bool tensor 결과, 같은 priority의 서로 다른
Workload UID batch guard, 여러 실현 batch 크기, native queue timeout과 `REQUEST_END`를 확인했다.
matching ACTIVE/IDLE runner의 fixed batch 실행, warmup 제외, model resident 및 실제
cgroup memory peak와 root RAPL 읽기도 smoke로 확인했다.

이는 full qualifying power campaign이 아니다. **완전한 native campaign → Ready gateway 추론**,
ARM/RKNN hardware와 live testbed는 검증하지 않았다. 이전 test나 Helm 실행을 최신 refactor의
모든 gate 통과로 설명하지 않는다. 정확한 배포 값과 변경 기록은
[Workload 관리 계약 12장](workload-management.md#12-배포와-구현-검증-경계)을 따른다.

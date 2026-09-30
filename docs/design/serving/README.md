# Serving Design

## 현재 구현 범위

`internal/serving`에는 모델별 가중치 선택이 있고, `internal/serving/entrypoint`는
선택한 백엔드로 HTTP 요청을 전달한다. `cmd/serving-entrypoint`가 시작 시 읽은
고정 테이블을 공급한다. 기존 이미지·차트·배포 경로와 노드별 Service는 이 전달
경로를 위한 구현 자산이다.

이 경로는 아직 워크로드 객체와 SLO를 해석하지 않는다. 요청 본문을 읽지 않고
한 백엔드로 전달하므로 단일 추론 입력 검증과 워크로드별 배칭도 수행하지 않는다.
워크로드 조회·관리, 동적 분배 기준 공급과 유입 대기열은 추가하거나 연결해야 한다.
추론 실행과 동적 배칭은 Triton을 연결하며, 자체 추론 서버·범용 batcher를 만들지 않는다.

이 문서의 요청 처리 흐름은 구현 목표이며, 현재 구현 또는 라이브 클러스터에서
완성된 동작을 뜻하지 않는다. 기존 전달 경로의 네트워크 관측은 뒤에서 구분한다.

## 설계 기준

이 문서는 [시스템 명세](../../spec/README.md)의 구현 설계다. 요구사항은
[라우팅과 배칭](../../spec/03_routing.md),
[구성 비용](../../spec/04_configuration_selection.md),
[전환 실행](../../spec/05_transition_control.md)을 따른다.
프로세스 수, CRD 필드, API 경로는 논리적인 역할과 별도로 정한다.

### Triton 재사용 원칙

CHILL 서빙의 기본 실행기는 Triton이다. 모델 로딩, backend 추론 실행, 동적 배치
구성과 실행 dispatch, 추론 결과 반환은 Triton의 기존 구현을 재사용한다.
CHILL은 evidence와 현재 부하·SLO에 근거한 운영·배칭 정책과 요청 추적을 담당한다.
POOL의 자원 할당·전력모드·라우팅·전환 판단도 CHILL의 책임으로 유지한다.

배칭 정책은 Triton의 기존 설정으로 표현하는 것을 우선하고, 부족한 조건은 공식
custom batching 확장 지점으로 연결한다. 이 확장은 기존 batcher에 배치 포함 조건을
추가하는 것이지 큐 선택·대기·dispatch 전체를 교체하는 API는 아니다. 실제 요구를
표현할 수 없는 부분이 확인되면 그 연결 범위만 설계하며, 자체 추론 서버나 범용
batcher를 개발하지 않는다. Triton 포크도 기본 전제로 두지 않는다.

Triton의 기본 모델 단위 큐나 priority를 CHILL의 워크로드별 큐·배치 분리와
동일시하지 않는다. 워크로드 정체성·수신 시각을 유지하고 큐 선택과 배치 혼합 방지를
Triton에 연결하는 방법은 별도로 정한다. 배칭 대기 설정은 종단 SLO 보장이 아니며,
선호 배치 크기는 실제 실행 크기를 보장하지 않는다. 실현된 배치와 대기·완료 상태를
관측해 정책과 evidence의 적용을 확인한다.

## 워크로드와 외부 요청

워크로드는 기존 모델 등록정보 참조, SLO 백분위·목표 시간, POOL 참조를 연결한다.
POOL 하나는 여러 워크로드를 지원한다. 같은 모델이라도 SLO가 다르면 다른
워크로드로 표현할 수 있으므로 모델 이름만으로 서비스 정체성을 대신하지 않는다.

- 사용자는 워크로드 목록·상세에서 식별자, 모델, SLO, 입력 형식과 요청 가능한
  상태를 조회한다.
- 추론 요청에는 워크로드 식별자와 독립적으로 완료할 수 있는 추론 입력 1개를 보낸다.
  CHILL은 등록된 정의를 확인하며, 요청마다 모델·SLO 정의를 복사하거나 덮어쓰게 하지 않는다.
- 운영자는 모델 등록정보와 워크로드를 CRUD하고, 모델 연결·SLO·POOL을 관리한다.
  구체적인 리소스 표현과 갱신·삭제 절차는 미결정이다.

하나의 추론에 함께 필요한 여러 tensor나 파일은 입력 1개를 구성할 수 있다.
독립적인 추론 입력은 각각 별도 요청으로 보낸다. 응답과 SLO는 각 요청에 적용하며,
사용자는 내부 인스턴스 배치나 실행 배치 크기를 알 필요가 없다.
SLO는 CHILL의 요청 수신부터 해당 추론 결과의 응답 반환 완료까지 적용한다.
통계 집계 구간과 실패 요청의 취급은 서비스 계약에서 정한다.

## 노드 자원과 모델 인스턴스

노드와 서빙 파드는 1:N, 하나의 서빙 파드가 제공하는 모델 인스턴스와 워크로드도
1:N 관계를 허용한다. 노드 하나를 모델 하나나 워크로드 하나의 전용 장비로 취급하지 않는다.
한 파드는 하나의 모델 실행 인스턴스를 제공하며, 같은 산출물·런타임·입력 조건으로
처리할 수 있는 여러 워크로드가 이를 공유할 수 있다.

Triton 서버와 내부 model instance는 같은 단위가 아니다. 위 계약에 맞게 파드가
제공하는 모델의 실행 인스턴스를 하나로 명시적으로 구성하며, backend의 추론 스레드
설정은 CPU 할당량에 맞춘다. instance 수를 스레드 수로 해석하지 않는다.

스케줄러는 측정한 실행 구성과 현재 부하를 근거로 배치와 인스턴스별 CPU·메모리
요청량을 결정한다. 기존 인스턴스의 자원 변경도 제어 대상이며, 적용 절차와 전환
비용은 [전환 명세](../../spec/05_transition_control.md)에 따라 다룬다.
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

모델 인스턴스를 공유해도 유입 측과 인스턴스 내부의 큐는 워크로드별로 구분한다.
처리량 계획에는 입력량을 합산하되, 각 큐의 대기·SLO를 유지하고 실행 배치는
워크로드별로 만든다. CPU 추론의 스레드 설정은 CPU 할당량에 연동하며, 각 인스턴스는
배치 하나의 실행을 마친 뒤 다음 배치를 시작한다. 배치 내부의 병렬 계산과
여러 인스턴스의 병렬 실행은 이 제한과 구분한다. 큐 선택은 서로 다른 SLO와
진행 중인 실행의 영향을 고려하지만, 특정 우선순위·공정성 알고리즘은 아직 정하지 않는다.

Triton의 동일 instance에 대한 Execute 콜백 직렬화만으로 위 완료 경계를
보장하지 않는다. 선택한 backend가 비동기 작업을 남기고 반환하는 경우도 있으므로,
backend와 실행 설정이 배치 완료 후 다음 실행이라는 CHILL 계약을 충족하는지 확인한다.

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
요청별 인스턴스 선택 · 전달
            │
            ▼
선택한 인스턴스의 워크로드별 큐
            │
            ▼
CHILL 정책을 적용한 Triton 동적 배칭
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
| 사용자 요청 | 워크로드, 수신 시각, 배정 인스턴스, 대기·진행·완료·실패 |
| 실행 배치 | 같은 워크로드의 호환되는 입력 묶음과 각 입력·결과에 대응하는 요청 |

각 요청은 모델 인스턴스 하나에 배정한다. 인스턴스는 같은 워크로드의 호환되는
요청들을 묶어 실행하고 각 결과를 해당 요청에 연결해 반환한다.
서로 다른 요청의 완료 순서는 독립적이다.

인스턴스에 전달하고 내부 실행 배치로 묶을 때도 각 요청의 수신 시각을 유지한다.
지연을 노드 도착 시각부터 새로 재지 않으며, 실행 배치의 지연을 요청별 지연으로
대신하지 않는다. 요청별 대기·완료 상태와 SLO를 관측한다.

### 책임 분리

| 역할 | 책임 |
|---|---|
| 스케줄러 | POOL 전체의 워크로드별 부하·SLO를 보고 구성과 자원 할당·분배 기준을 갱신 |
| 요청 진입·분배 경로 | 워크로드·입력 확인, 미배정 요청 관리, 현재 분배 기준과 준비 상태에 따른 인스턴스 선택·전달, 해당 요청에 응답 반환 |
| CHILL 배칭 정책 | evidence·현재 요청 상태·SLO에 근거한 워크로드별 큐 선택·배치·대기 결정과 Triton 연결 |
| Triton 실행기 | 모델 로딩, 정책을 적용한 동적 배치 구성·실행 dispatch, backend 추론과 요청별 결과 반환 |

요청마다 새 배포 계획의 계산·실행을 기다리지 않는다. 요청 분배 경로는 현재
유효한 분배 기준으로 준비된 인스턴스에 계속 전달하고, 스케줄러는 관측에 따라 그
기준을 갱신한다. 수용 가능한 인스턴스가 있으면 유입 측에서 불필요하게 기다리지 않는다.
유입 대기열은 실행 배치를 만들기 위한 별도의 대기를 추가하지 않는다.

워크로드별로 유입 대기 상태를 구분하여 하나의 섞인 FIFO가 자원 할당 정책을
대신하지 않게 한다. 같은 모델 인스턴스를 공유하는 경우에도 이 경계를 유지한다.

부하는 워크로드별 inputs/s로 관측하며 요청률과 같다. 대기·완료량도 요청 수로
기록하고 내부 실행 배치 수와 구분한다. 모델·워크로드의 수정·삭제가 이미 받은
요청의 의미를 조용히 바꾸거나 요청을 유실시키지 않아야 한다.

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

## 현재 전달 경로와 재사용

CHILL supplies the entry point, and the entry point distributes.

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

The chart renders those Services from the routing table itself, so a weight and
the address it names cannot disagree, and a node appears under one Service name
or the render fails. Each selects `app.kubernetes.io/component: serving` and
`edge.dacs.io/serving-node`, and carries the EdgeMesh proxy label the
demonstrated path ran through. Whatever carries those labels on that node is
what the Service reaches, which is how the address is settled without settling
what serves behind it.

[`internal/serving`](../../../internal/serving) carries that decision alone:
what the entry point reads from an inference request — the declared model, with
the payload left opaque — and which node's Service receives it, given the
weights. It touches no transport and holds no state.

[`internal/serving/entrypoint`](../../../internal/serving/entrypoint) serves
that decision over HTTP. It forwards the request to the chosen node's Service
with its method, path, and payload intact and returns the response unread, so
transport is the only thing it adds and neither end observes that a choice
happened. It reads the weights in force through an interface and takes no
position on what supplies them.

A table read once at start-up supplies those weights today. It revises nothing,
which is what makes it a development stand-in, and it lets the entry point be
deployed and reached before anything computes a weight. It lives in the binary
that assembles the entry point rather than beside it, so what replaces it is a
different value passed at the same seam.

## 구현 대응

| 현재 자산 또는 필요한 기능 | 구현 방향 |
|---|---|
| 모델별 경로 해석 | 워크로드 식별과 등록된 모델·SLO 해석을 연결 |
| 시작 시 읽는 고정 가중치 테이블 | 워크로드별로 갱신되는 분배 기준 공급으로 교체 |
| 한 백엔드로 본문을 그대로 전달하는 HTTP 경로 | 워크로드·단일 추론 입력 검증, 인스턴스 선택과 요청별 수신·응답 시각 관측을 연결. 가중치 교체만으로 완성되지 않음 |
| 노드별 Service와 전달 경로 | 노드뿐 아니라 선택한 모델 인스턴스까지 도달하도록 변경. 다른 모델 파드를 하나의 노드 단위 목적지로 혼동하지 않음 |
| 워크로드 조회·관리, 유입 대기열·요청 추적 | 위 요청 계약에 맞춰 추가 |
| 모델 로딩·추론 실행·동적 배칭 | Triton을 연결하고 기존 구현을 재사용. 자체 추론 서버·범용 batcher는 개발하지 않음 |
| 워크로드별 큐 선택·배치 분리·에너지 기반 배칭 정책 | Triton 설정과 공식 확장 지점에 우선 연결하며, 기본 모델 큐가 CHILL 계약을 대신하지 않게 함 |

현재 진입점은 모델을 경로에 담는 Open Inference Protocol 형태의 HTTP 요청을
전달한다. 워크로드 식별자와 단일 추론 입출력을 외부·인스턴스 API에 어떻게
표현할지는 별도의 결정이며, 기존 투명 프록시 계약을 요청 처리 흐름의 제약으로 삼지 않는다.

## 프로파일 기반 지연 계산과 검증

프로파일은 자원·전력모드·실행 배치 등의 조건별 실행시간 분포·처리량·메모리·
전력을 제공한다. 스케줄러는 측정한 실행 구성만 선택하고, 현재 요청의 도착·큐·
진행 상태와 분배·배칭·큐 선택 규칙을 적용해 요청별 예상 완료 시각을 계산한다.
프로파일이 배치 실행에 걸리는 시간을 제공하면, Serving 상태와 처리 규칙으로
그 배치를 언제 시작할 수 있는지 판단하는 구조다.

프로파일의 실행 조건은 실제 사용하는 Triton/backend·자원·스레드·배치 설정과
대응해야 한다. 직접 런타임을 호출한 측정값이 Triton 경로에도 그대로 적용된다고
가정하지 않으며, 실행 조건의 대응과 프로파일에 포함되지 않은 서빙 비용을 구분한다.

SLO는 CHILL 수신부터 응답 반환까지의 요청별 지연에 적용한다. 프로파일에 포함되지
않은 전달·응답 처리 비용은 Serving 관측으로 보완한다. 최대 처리량을 SLO 용량으로
대신하거나, 평균 실행시간만으로 지연 백분위를 결정하지 않는다.
같은 실행 조건의 프로파일은 SLO가 다른 워크로드에도 재사용하며, 별도의 부하별
Serving 성능표를 사전 측정하지 않는다. 기존 종단 평가에서 실제 SLO·에너지와
예측 오차를 확인한다.

같은 노드에 공존하는 파드들의 단독 실험에서 잰 노드 전체 전력을 단순 합산하지 않는다.
측정 경계는 [프로파일링 설계](../profiling/README.md#serving-resource-allocation)를 따른다.

## 미결정 사항과 검증 경계

- Workload의 CRD 여부와 필드, 조회·관리·추론 API 경로와 입출력 형식.
- 모델·워크로드 수정·삭제 시 진행 중 요청과 모델 참조를 다루는 절차.
- 요청 실패·과부하 처리 정책.
- SLO 통계 집계 구간과 실패 요청의 취급.
- Triton에 연결할 워크로드별 큐·배칭 정책, 적용할 설정·확장 지점과 갱신 시점.
- 워크로드 사이의 자원 할당 정책, 플랫폼의 비중복 할당 구현·검증과 공유 인스턴스의 큐 선택.
- 분배 기준의 갱신·반영 방식과 관찰·제어 주기.
- 대상 장비·모델의 Triton/backend 지원과 배포 구성, 단일 배치 완료 경계의 충족 여부.

워크로드 조회부터 요청 분배·내부 배칭·응답 반환까지의 경로는 구현 후 검증해야 한다.
동시에 도착한 요청들이 서로 다른 인스턴스에서 처리되거나 같은 실행 배치로 묶여도
각 결과가 해당 요청에 반환되는지, 수신 시각 기준으로 요청별 SLO가 집계되는지
확인한다. 같은 POOL의 여러 워크로드가 같은 모델을 사용하더라도 정의와 결과가
섞이지 않아야 한다.

현재의 고정 테이블은 동적으로 갱신되지 않으므로 가중치 반영 지연을 입증하지 않는다.
이 설계의 요청 처리·SLO·반영 지연에 대한 라이브 클러스터 검증을 완료했다고
주장하지 않는다.

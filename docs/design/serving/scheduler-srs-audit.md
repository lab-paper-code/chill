# 스케줄러 SRS 요구사항·구현 대조

## 1. 판정 범위와 증거

2026-10-08 UTC의 파일시스템을 읽어 [스케줄러 SRS](../../spec/scheduler/SRS.md)를
기존 `docs/spec/`와 현재 소스에 대조했다. 원본 명세의 목표·제약을 요구사항의
근거로 삼고, 실제 코드로 구현 상태를 판정했다. 기존 설계의 승인·완료·제외
표현은 요구사항을 변경하는 근거로 사용하지 않았다.

- 정방향: 원본의 목표·입력·선택·라우팅·배칭·전환·관측·운영 설정을 SRS에 연결했다.
- 역방향: 각 SRS 요구사항의 원본 근거와 현재 제공 경로를 확인했다.
- 경계: 스케줄러 판단과 Workload 관리·측정·실행기·물리 전환의 소유권을 구분했다.
- 검증: 문서의 추적성과 실제 구현의 실행 증거를 분리했다. 요구사항 ID의 연결은 행동 검증 통과를 뜻하지 않는다.

cutover 전 조사에서 pinned gopls와 해당 소스를 사용하여 기존 duration·energy 생산,
현재 분배와 pool 이동의 소비 경로를 확인했다. 그 조사 시점의 추가 연결 목록은
2절의 체크포인트로 보존한다. 통합 후의 책임·실제 경로와 실행 증거는
[구현 설계](scheduler-implementation.md)와 5절에서 구분한다.

## 2. 요구사항별 판정

‘유지’는 원본 명세가 요구하는 의미를 유지한다는 판정이다. ‘보완’은 명시적으로
추가한 연결이며, ‘간소화’는 구현 표현을 고수준 의미로 정리한 경우다.
구현 열은 재사용 가능한 현재 경로와 추가로 연결해야 할 행동을 구분한다.
실제 구현 열은 실행기 변경 전 소스의 체크포인트다. 이후 사용자가 firstfloor 채택을
선택하여 원본 명세와 SRS의 실행기 경계를 갱신했다. 아래 기존 Triton 소스의 행동은
firstfloor 통합의 완료 증거와 구분한다. 채택에 따라 필요한 연결은 4.3절에 정리한다.


| SRS | 원본 근거 | 요구사항 판정 | 채택 전 구현 체크포인트와 연결 과제 |
|---|---|---|---|
| SCH-01 | 시스템 명세의 목표·SLO·평가 | 유지 | Workload SLO와 요청 통계 존재. SLO 우선 후보 선택과 총에너지 비교 연결 필요 |
| SCH-02 | 시스템 명세의 노드 POOL; 구성 선택의 현재 상태·시간 변화 | 보완: 노드의 단일 POOL 소속과 실제 서빙 관계 갱신 | 실제 POOL 관측과 drain 후 membership 변경 존재. 유지·변경 비교와 반복 판단 연결 필요 |
| SCH-03 | 시스템 명세의 요청; Workload WM-03, WM-14~16, WM-28 | 유지 | UID·불변 Definition·최초 receipt 보존 존재. 판단 입력에도 같은 식별 의미 적용 |
| SCH-04 | 라우팅의 워크로드 전체·인스턴스별 부하와 결과 | 간소화: 수신 모집단과 완료 사건 집계의 의미 구분 | received/accepted/assigned/결과 기록 존재. 각 사건 구간의 운영 부하 계산 필요. 신규 endpoint와 모든 처리율 필드의 제공은 설계 선택 |
| SCH-05 | 라우팅의 유입·native 대기·실행; Workload WM-30 | 유지 | ingress queue와 waiting/running/assignedUnknown 관측 존재. native 현재 큐의 적시 관측 계약 연결 필요 |
| SCH-06 | 시스템 평가·라우팅의 inputs/s와 요청 수 | 유지 | 요청별 UID·배정 인스턴스 보존. 운영 요청률과 인스턴스·노드 합산 입력 구성 필요 |
| SCH-07 | 시스템 명세의 SLO 관측과 집계 | 유지 | Journal 보존·지정 구간 분석 존재. 운영 이동 구간과 제어·비교 기간의 독립 설정 필요 |
| SCH-08 | 측정 실패; 구성 적격성; Dashboard DB-FR-16, DB-FR-21 | 유지 | evidence/readiness 오류와 assignedUnknown 존재. 판단 입력의 확보 상태와 평가 대기 연결 필요 |
| SCH-09 | 측정의 조건·최신 성공 선택·근거 재사용 | 유지 | 선택 campaign의 검증된 native profile catalog 존재. 같은 조건의 최신 성공 선택·후보 적용 연결 필요 |
| SCH-10 | 시스템 자원·구성 선택의 배치 제약 | 유지 | 관측 Pod의 CPU와 memory 조건 확인 존재. 전체 할당 예산·CPU 분리·물리 GPU 전용 배치 선택 필요 |
| SCH-11 | 시스템 노드 자원과 모델 인스턴스; 라우팅의 순차 실행 | 유지 | native count=1과 측정 CPU/thread 조건 확인 존재. Pod 배치·증감·자원 변경을 소유하는 writer 필요 |
| SCH-12 | 시스템 공존 조건·메모리; 측정의 공존 오차 | 유지 | measured memory와 현재 Pod 조건 검증 존재. 후보별 가용 예산과 공존 오차를 판단에 연결 |
| SCH-13 | 구성 선택의 프로파일에서 요청 지연 계산 | 유지 | raw 실행시간 분포 존재. 도착·큐·진행 상태에서 요청별 완료 시각을 계산하고 Serving 부가 시간을 연결 |
| SCH-14 | 라우팅의 공유 인스턴스·SLO; 구성 선택의 지연 | 유지 | Workload 분리와 native 단일 인스턴스 실행 존재. 공유 실행의 비선점 대기를 포함한 Workload별 예상 지연 계산 필요 |
| SCH-15 | 구성 선택의 입력·실행 가능성 | 보완: 노드 변경에 영향받는 모든 Workload 평가 | 현재 readiness·drain 확인 존재. 전환 중·최종 용량과 같은 노드의 모든 영향 Workload 평가 필요 |
| SCH-16 | 구성 선택의 비교할 비용·측정의 전력과 에너지 | 유지 | active incremental energy/input 및 전력모드 전환 evidence 존재. 같은 시간축·전체 POOL의 baseline·전환·Serving 합성 필요 |
| SCH-17 | 구성 선택의 공유 비용·측정의 노드 전력 | 유지 | incremental energy와 원시 idle evidence 존재. node baseline·공유 instance 비용을 한 번 집계하는 후보 계산 필요 |
| SCH-18 | 구성 선택의 시간·부하와 전환 시간 의미 | 유지 | 전력모드 전환 tau/eta와 실제 상태 존재. 비교 기간·부하 가정 및 준비 시점을 계산에 연결 |
| SCH-19 | 구성 선택의 결정; Dashboard DB-FR-15~16 | 유지 | 현재 pool/instance 상태 조회 존재. 후보·목표·유지/변경·적격성·판단 이유의 결과 계약 필요 |
| SCH-20 | 라우팅의 준비 대상·분배·반영 관측 | 유지 | 실제 ready/capacity와 양수 weight 기반 분배 존재. 스케줄러 분배 정책 writer와 반영 피드백 필요 |
| SCH-21 | 라우팅의 요청별 시간·추가 대기 여유 | 유지 | 최초 receipt, SLO와 dispatch 시점 elapsed 전달 존재. native 현재 시간·큐·실행 및 실행/반환 비용에 정책 적용 필요 |
| SCH-22 | 시스템 실행기 방향·라우팅의 내부 batcher | 보완: 사용자 선택에 따른 firstfloor 실행 경계 | 변경 전 Triton guard와 별도 firstfloor 배처 존재. firstfloor 내부 정책 적용과 CHILL 계약의 실제 통합 필요 |
| SCH-23 | 라우팅의 워크로드 큐·배치·에너지 이득·수명 | 유지 | UID 배치 분리와 명시적 lifetime/queue timeout 존재. evidence·SLO 기반 다음 큐·배치·추가 대기 판단 필요 |
| SCH-24 | 전환 실행의 동작·완료·실패 계약 | 유지 | Workload operation, native readiness, NodeActuation read-back 존재. 구성 전환의 동작 의존성·부분 적용 결과 연결 필요 |
| SCH-25 | 전환 실행의 서비스 용량·진행 순서 | 유지 | 기존 요청의 실제 drain·연결 해제 존재. 신규 capacity 준비·분배 이동·독립 동작 진행을 구성 목표에 연결 |
| SCH-26 | 전환 실행의 실패·노드 소실·다음 판단 | 유지 | physical identity와 readiness/log-stream 소실 격리 존재. 실제 부분 완료에서 다음 후보를 선택하는 루프 필요 |
| SCH-27 | 구성 선택의 시간 변화·전환 재판단 | 유지 | owner별 진행 상태 존재. 기존 동작 재사용과 부하 변화의 동일 판단 로직 연결 필요 |
| SCH-28 | 시스템 SLO 한계·구성 선택의 실행 가능성 | 유지 | queue/lifetime/resource/evidence 오류와 실제 요청 결과 존재. 전체 한계·일시적 준비 제약·평가 대기를 분리한 대응 판단 필요 |
| SCH-29 | Workload WM-14~16, WM-19~22, WM-25 | 유지 | 관리 gate·기존 요청 마무리·공유 Workload 분리 존재. 관리 변화와 후보·현재 구성 갱신 연결 |
| SCH-30 | 라우팅의 반영 시점·전환 관측·Dashboard DB-FR-15~21 | 보완: 라우팅 결정 시각과 실제 반영 시각 구분 | request/management와 전환 evidence 존재. 판단·정책 적용·동작·power를 같은 시간축에 연결 |
| SCH-31 | 시스템 관측·평가; 라우팅 결과; Dashboard DB-FR-22~24 | 유지 | 원시 요청·지연 분포·결과·실제 batch 기록 존재. Serving 전체 POOL power와 판단/전환 기록을 연결해 종단 재계산 |
| SCH-32 | 시스템 평가·설계 선택; Dashboard DB-FR-17~18 | 유지 | queue/timeout/refresh/campaign 등의 실행 설정 존재. scheduler 설정의 권한 검증·변경·적용·실험 기록 연결 필요 |

기존 영역의 행동은 그 owner를 재사용하는 제약으로 유지한다. 워크로드 CRUD,
profiling campaign, 물리 actuation, 추론 서버를 스케줄러 내부에 재구현할 요구사항으로
해석하지 않는다. 특정 broker, Prometheus 배포, 별도 DB·CRD, 예측기, 플러그인 registry,
범용 복구 엔진, 모든 공존 조합의 선행 측정은 고수준 SRS가 요구하는 구현물이 아니다.

## 3. 실제 소스의 재사용 계약

| 소스 | 제공하는 현재 계약 | 구현 설계에서 구별할 의미 |
|---|---|---|
| [Serving core](../../../internal/serving/runtime.go)·[types](../../../internal/serving/types.go) | 불변 요청 정의·receipt, per-UID queue, observed instance·assignment·native progress·drain | 준비된 현재 capacity와 목표 capacity; assignment와 native queue entry |
| [Journal](../../../internal/serving/observations.go) | request/management 원시 기록, 지정 구간 결과, paging, incomplete 표시 | publication/rotation window와 운영 이동 구간; 분석 필터와 운영 전체 모집단 |
| [Catalog](../../../internal/serving/kube/catalog.go)·[derive](../../../internal/profiling/derivation/derive.go) | frozen 조건과 raw duration, incremental energy/input, measured memory | 최신 선택과 명시적 campaign 선택; incremental energy와 전체 node baseline |
| [Kubernetes observer](../../../internal/serving/kube/observer.go) | 외부 owner의 Pod 관측·native probe·실제 자원/전력 확인, 안전한 cleanup와 pool 이동 | 현재 Pod 관측과 신규 Pod/resource/policy writer |
| [NodeActuation API](../../../api/v1alpha1/nodeactuation_types.go)·[controller](../../../internal/nodeactuator/controller/) | immutable 대상·정책, 안전 확인·apply·독립 read-back·Ready/Failed | 선언/명령 성공과 실제 적용; 단일 물리 writer 유지 |
| [전환 derive](../../../internal/profiling/transitioncost/derive.go)·[summary](../../../internal/profiling/transitioncost/summary.go) | 측정한 power-mode 전환 tau와 target idle 기준 incremental eta, 원시 attempt 참조 | 전력모드 변경 측정과 node 편입·모델 준비·자원 변경의 별도 비용 근거 |
| [Native client](../../../internal/serving/firstfloor/client.go)·[actor](../../../internal/firstfloor/)·[runner](../../../runtime/firstfloor/) | frozen UID·정의·SLO·elapsed, typed tensor와 현재 native 큐·실제 batch·적용 policy·physical 완료 | actor/replay 공유 selector와 current/target/actually applied 구분 |

## 4. 사용자 선택에 따른 실행기 경계

### 4.1 변경 전 Triton 확장 지점의 확인

[r26.09 공식 API](https://github.com/triton-inference-server/core/blob/r26.09/include/triton/core/tritonbackend.h#L1725-L1783)는
기존 batching strategy 위에 포함 조건을 더하는 다섯 callback을 제공한다.
`ModelBatchIncludeRequest`의 결정 출력은 `bool* should_include`다.
변경 전 CHILL guard는 이 지점에서 UID 혼합을 막고 batch 고려 시점의 `queued`를 기록했다.
native 큐 진입 전체를 적시에 관측하는 hook과 구별했고, 완료 trace는 wrapper에서 읽었다.

변경 전 repository 생성은 `max_queue_delay_microseconds: 0`과 고정
preferred size/priority/queue policy를 설정했다. 해당 실행 경로는 firstfloor cutover로 제거했다.
공식 callback 계약에는 다음 큐 선택, 현재 큐·실행 상태 제공, 요청별 wait timer 또는
즉시 dispatch를 교체하는 제어가 없다. 이 경계를 SCH-21~23의 요구사항과 대조하여
사용자에게 실행기 선택을 제시했고 firstfloor 채택을 확정했다.

### 4.2 채택 전 firstfloor 소스·프로토콜 체크포인트

`/home/genesis1/ETRI/donghwan/firstfloor`의 실제 소스는 별도 Go HTTP 서버·EDF/batcher와
Python IPC runner를 제공한다. Triton 서버나 Triton 내부 확장 ABI를 실행하지 않는다.
프로토콜 차이는 현재 worker를 loopback에서 실행하여 확인했다.

| 실제 실행 입력 | 관측 결과 |
|---|---|
| 자체 versionless infer + receipt header + 단일 FP32 echo input | HTTP 200, 입력 결과 반환 |
| CHILL native client의 versioned infer 경로 | HTTP 404, `unknown inference path or model` |
| versionless 경로에 CHILL native JSON `parameters` 전달 | HTTP 400, `json: unknown field "parameters"` |
| 자체 응답의 native client 계약 검사 | `model_version`과 실제 batch 관련 `parameters` 부재 |

실행 명령은 `go run ./cmd/serving-worker --config config/echo.json
--bind-address 127.0.0.1:18686 --node-name local-contract-probe`였다.
제공된 synthetic echo 설정으로 HTTP 계약만 확인했고, probe 후 서버를 종료했다.
실제 모델·전력·클러스터 또는 Triton 내부 정책 적용의 증거로 사용하지 않는다.
소스·설정·동료 작업물은 수정하지 않았다.

### 4.3 확정한 계약과 구현 차이

사용자는 **firstfloor 실행기 채택**을 선택했다. 원본 시스템·측정·라우팅 명세와
스케줄러·Dashboard SRS의 실행기 책임을 이에 맞춰 갱신했다. CHILL은 evidence·
라우팅·배칭 정책과 요청별 정보를 제공하고, firstfloor 내부 큐·배처·모델 runner가
실제 배치 구성·추가 대기·실행을 맡는다. 동료 작업 디렉토리의 소스는 보존한다.

| 보존한 요구사항 | 채택 전 동료 firstfloor | CHILL에 연결한 구현 |
|---|---|---|
| UID·수신 기준·요청별 SLO·명시적 수명 | 전역 request budget와 deadline 정책 | 불변 요청 정의·UID·개별 SLO·경과 시간·취소/timeout을 전달하고 같은 UID의 호환 요청을 배치 |
| 다중 named tensor·numeric/bool Workload 입력 | 단일 FP32 입력·출력 IPC | 현재 공개 입력 계약과 실제 backend의 tensor·output 계약 연결 |
| evidence 기반 배치·대기·다음 큐 | EDF·평균 latency/energy 기반 선택 | raw 실행시간 분포와 요청별 여유·실제 energy gain·공유 비선점 대기를 정책에 적용 |
| 실제 상태·완료·분배 반영 관측 | aggregate batch log·HTTP 결과 | 요청별 수신·큐·실행·완료와 actual batch, 적용 시각, 현재 큐/실행 상태를 CHILL에 연결 |
| Profiling과 Serving의 같은 실행 조건 | standalone Python runner | 같은 firstfloor 실행 경로의 고정 배치 측정·warmup 제외·memory·duration·독립 wall-power evidence 연결 |
| 전체 POOL의 전환+Serving 비용 | 배치별 energy/item | 기존 NodeActuation·wall-power sampler·raw evidence를 재사용해 실제 자원 동작·준비 비용·baseline·전환을 연결 |

채택 전 CHILL의 `latencyMs`는 `session.run` 전체 호출 구간이었다. 실제 native
cutover는 client round trip·runner service·backend call·native elapsed를 각각 보존한다.
catalog는 raw runner-service 분포를 선택하며 원래 client 지연을 backend-only로 재해석하는
대신 새 native sample·batch identity와 측정 조건을 연결한다.

기존 model-loaded idle·power-mode directed raw evidence는 각 측정 의미를 유지한다.
unloaded baseline과 실제 준비·제거·membership 동작은 같은 owner의 원자 동작과
wall-power sampler로 측정한다. matching state의 baseline이 있으면 우선하고,
공존 상태에서는 explicit additive 예상과 그 가정·출처 및 실제 오차를 구분한다.
native/Pod before·after 상태와 inference counter 불변·independent host read-back을
retained cost에 연결하며, 샘플·조건이 충분한 actual 측정만 비교에 사용한다.

## 5. 최종 검증과 판정

### 5.1 요구사항의 과소·과대 대조

원본 명세와 실제 소스를 기준으로 SCH-01~32를 양방향 대조했다. 요구사항의
원본 근거는 2절, 현재 구현 경로는 [구현 설계 8절](scheduler-implementation.md#8-srs와-구현-확인-경로)에 연결했다.

- 보완한 연결: 단일 POOL 소속과 실제 membership(SCH-02), 거절을 포함한 전체 유입과 사건별 처리량(SCH-04~07), 노드 변경의 전체 영향 Workload(SCH-15), 승인한 firstfloor 실행 경계(SCH-22), 판단과 실제 적용 시각(SCH-30).
- 유지한 제약: SLO 우선·전체 POOL 에너지, 동일 실행 조건의 근거, 공유 비선점 실행, 원래 요청 정의·수명, 실제 준비·drain·전력 read-back, 권한과 raw 재계산.
- 설계로 옮긴 선택: endpoint·필드·상태 코드, 후보 탐색 한도·seed·replay, 관측 구현과 프로세스 배치. broker·새 DB·측정 CRD·범용 복구 엔진·공존 전 조합 campaign은 요구사항으로 채택하지 않았다.
- 대조한 원본 목표·제약은 32 요구사항에 대응한다. 실제 SLO·총에너지·예측 오차의 수용 판정은 hardware 종단 실험으로 확인한다.

SRS의 부정·대조형 서술 검사는 일치 항목 0건이다. 실패·미완료·관측 공백은
관측해야 하는 도메인 상태로 유지했다.

### 5.2 수용 시나리오의 확인 수준

| 시나리오 | 실행한 확인 | 증거의 경계 |
|---|---|---|
| AC-01, 02, 09, 10, 21 | `internal/serving/scheduler_test.go`의 SLO-first·공유 residual·node-wide 자원·전환 baseline·finite horizon regression | 계산·상태 전이 검증; 물리 에너지 절감은 종단 측정 대상 |
| AC-03, 08 | kube catalog/resource 및 native evidence·CPU 계약 regression | 같은 조건·최신 근거·자원 제약 검증; GPU/RKNN 실물 실행은 별도 |
| AC-04, 06, 18 | Journal window regression, 실제 HTTPS load 조회·raw cursor | 실제 유입 3·접수/배정/완료 1, 사건 구간·모집단·paging 확인 |
| AC-05, 07, 12, 16 | native/SDK/runtime regression, 실제 ORT CPU 다중 UID·typed inference·명시적 수명 smoke | SDK physical 완료와 native 상태, SLO 초과·수명 분리 확인 |
| AC-11, 13, 14, 15, 22 | kube 원자 동작·controller·membership/drain regression와 envtest 통합 suite | API·완료 조건·의존성 검증; kubelet·host 전력 전환은 별도 |
| AC-17 | Workload manager·Runtime의 정의 보존·관리 gate·공유 분리 regression | 접수 당시 정의와 기존 요청의 소유권 유지 |
| AC-19, 20 | 실제 TokenReview/SAR HTTPS settings/state/raw API와 controller tick | 전후 설정·검증/적용 시각·판단 journal; 미측정 local 상태는 `Pending`/`MissingPoolInventory`, physical action 0건 |

### 5.3 실행 결과

최종 Go regression·`make lint test`와 runtime suites, generated artifacts,
architecture·pinned gopls, default/Profiler Helm 및 deployment graph gate를 실행했다.
구체 명령·렌더 입력·실제 backend smoke는 [구현 설계 10절](scheduler-implementation.md#10-구현-완료와-실행-증거)에 기록한다.
Ponytail 검수는 actor/replay selector·기존 metric/store·원자 동작을 재사용하고
중복 검증·wiring-only test·별도 lifecycle 엔진을 제거하는 방향으로 반영했다.

동료 작업 디렉토리·live cluster·이미지 registry는 수정하지 않았다. local CPU
service/profile smoke는 wall-energy·실물 Node identity·frozen image campaign을
갖춘 scientific evidence와 구분한다. hardware 종단 SLO·총에너지·공존 오차,
GPU/RKNN 실물과 live/kind e2e는 이번 검증에 포함하지 않았다.


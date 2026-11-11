# Kueue, Volcano, KEDA의 코드 구조와 네이밍

세 프로젝트의 실제 소스에서 패키지 배치, 파일명, 변수명, 상태 소유권과 의존성 연결 방식을 살펴본다.
참고 링크는 조사한 commit에 고정했다. 유지보수상 이점은 소스 구조에서 도출한 해석 **[INFERENCE]**다.

## 1. 세 프로젝트를 읽는 관점

| 프로젝트 | 담당 기능 | 구조에서 살펴볼 특징 |
|---|---|---|
| Kueue | Kubernetes 작업의 대기와 실행 자원 할당 | 상태의 수명별 패키지 구분, 외부 조회와 계산의 분리 |
| Volcano | 배치 작업 스케줄링 | 실행 단계와 정책 확장의 구분, 같은 상태 소유자의 역할별 파일 분할 |
| KEDA | 이벤트 기반 오토스케일링 | 공급자별 구현 파일, 작은 공통 계약, 진입점의 명시적 조립 |

## 2. Kueue: 상태의 수명과 계산 입력을 구분한다

### 디렉토리 이름과 하위 분리 기준

대표 경로를 책임과 함께 읽으면 다음과 같다.

```text
apis/kueue/v1beta2/       버전별 Kubernetes API 모델
pkg/cache/
  queue/                 대기열과 Workload의 queue 배정 상태
  scheduler/             활성 quota reservation을 보유한 Workload의 상태
  hierarchy/             계층 관계 관리
pkg/scheduler/           스케줄링 판단과 실행 조정
pkg/workload/            Workload 해석과 자원 계산
pkg/controller/
  jobframework/          Job 연동의 공통 계약과 처리
  jobs/
    job/                 Kubernetes Job
    jobset/              JobSet
    rayjob/              RayJob
    statefulset/         StatefulSet
cmd/kueue/               실행 진입점
```

controller 영역은 **역할 → 구현군 → 구체 대상** 순서로 내려간다. `controller`는 역할, `jobs`는 연동 구현군, `job`과 `rayjob`은 대상을 표현한다.
각 대상 디렉토리 안에 controller, webhook, adapter 파일을 함께 둔다. 대상 하나의 동작을 따라가며 관련 역할을 가까이 읽을 수 있다.
cache 영역은 `queue`, `scheduler`, `hierarchy`처럼 상태의 사용 목적과 책임을 기준으로 형제를 나눈다.
참고: [대상별 controller 구조][Q10], [cache 하위 구조][Q11].

`queue.Manager`에는 대기열, Workload의 queue 배정, 완료된 Workload 관련 상태가 들어 있다.
`scheduler.Cache`는 자원을 예약한 Workload가 cache에 머무는 동안의 ClusterQueue 배정을 기록한다.
두 타입은 각자 mutex와 배정 상태를 보유한다. 공유 보조 데이터인 `draBackedResources`는 별도 작성자와 동기화 경계를 갖는다.
**배정 상태의 소유자는 보관 목적과 수명에 따라 구분하고, 공유 데이터의 작성 책임은 따로 명시한다.**
참고: [queue.Manager][Q1], [scheduler.Cache][Q2], [cache 편입 조건][Q12].

### 조회 결과를 계산의 입력으로 만든다

`effective_resources.go`는 자원 계산에 필요한 외부 정보를 `AdjustmentInputs`에 모은다.

| 소스의 이름 | 책임 |
|---|---|
| `ResolveAdjustmentInputs` | client로 RuntimeClass와 LimitRange 정보를 조회 |
| `AdjustmentInputs` | 조회가 끝난 Pod overhead와 LimitRange 요약 보관 |
| `EffectivePodSpecs` | Workload와 확정된 입력값으로 유효 PodSpec 계산 |

`EffectivePodSpecs`의 인자는 Workload와 `AdjustmentInputs`다. 계산 경로는 입력값으로 필요한 정보를 받는다.
이 구분은 같은 파일 안에 있다. 함수의 인자와 책임만으로도 입출력과 계산을 나눌 수 있는 사례다.
참고: [조회와 계산의 경계][Q3].

### 소비자가 필요한 능력만 계약으로 표현한다

queue 패키지의 `StatusChecker`는 다음 한 메서드를 선언한다.

```go
type StatusChecker interface {
    ClusterQueueActive(name kueue.ClusterQueueReference) bool
}
```

queue 생성자는 이 계약을 받는다. scheduler 생성자는 구체 queue manager, cache, client를 받으며 선택 설정에는 `Option`을 사용한다.
**의존성마다 필요한 범위에 맞춰 작은 계약과 구체 타입을 함께 사용한다.**
참고: [StatusChecker][Q4], [queue 생성자][Q1], [scheduler 생성자][Q5].

### 파일명과 변수명

| 실제 이름 | 이름에서 읽을 수 있는 정보 |
|---|---|
| `effective_resources.go`, `effective_resources_test.go` | 계산 대상과 대응 테스트 |
| `job_controller.go`, `job_webhook.go`, `job_multikueue_adapter.go` | 같은 Job을 다루는 서로 다른 역할 |
| `workloadAssignedQueues`, `finishedWorkloads` | 보관하는 대상과 상태 |
| `schedulingCycle`, `resourceFormatter` | 장수명 필드의 역할 |
| `opt`, `wo`, `s` | 생성 함수 내부의 짧은 지역 이름 |

Job 관련 세 파일은 같은 `package job`을 선언한다. 대상은 패키지로 묶고, 역할은 파일명으로 구분하는 방식이다.
참고: [자원 계산 테스트][Q6], [Job controller][Q7], [webhook][Q8], [adapter][Q9], [scheduler 이름][Q5].

## 3. Volcano: 실행 단계와 정책을 구분한다

### 디렉토리 이름과 하위 분리 기준

```text
pkg/scheduler/api/         스케줄러가 사용하는 도메인 데이터
pkg/scheduler/cache/       클러스터 상태와 snapshot
pkg/scheduler/framework/   확장 계약, 등록, 세션 생명주기와 callback 처리
pkg/scheduler/actions/     allocate 등의 실행 단계
pkg/scheduler/plugins/
  gang/                   gang 정책 구현과 대응 테스트
  numaaware/
    policy/
    provider/
  predicates/
    hintprovider/
  deviceshare/
    devices/
pkg/controllers/queue/     Queue controller
cmd/scheduler/             스케줄러 실행 진입점
```

`plugins` 아래는 `gang`, `numaaware`, `predicates`처럼 정책과 스케줄링 능력으로 구분한다.
`gang`은 구현 파일과 테스트를 함께 두고, `numaaware`는 `policy`, `provider`로 내부 역할을 나눈다.
`hintprovider`, `devices`는 더 구체적인 하위 책임을 표현한다. **각 구현에 필요한 깊이로 하위 구조를 구성하는 사례**다.
참고: [정책별 하위 구조][V10].

framework의 `Action`은 이름, 초기화, `Execute(*Session)`, 종료 메서드를 선언한다.
`Plugin`은 이름과 `OnSessionOpen`, `OnSessionClose`를 선언한다.
**스케줄링의 실행 단계와 세션에 참여하는 정책 확장에 서로 다른 계약을 부여한다.**
참고: [Action과 Plugin][V1].

### 같은 상태 소유자의 코드를 역할별 파일로 나눈다

Queue controller는 다음 파일로 나뉜다.

| 파일 | 소스에서 관찰되는 역할 |
|---|---|
| `queue_controller.go` | controller 타입과 기본 구성 |
| `queue_controller_handler.go` | 이벤트를 요청으로 바꾸고 queue에 추가 |
| `queue_controller_action.go` | Queue 관련 작업 처리 |

세 파일은 같은 `package queue`에 속하며 `queuecontroller`의 메서드를 담는다.
파일은 읽는 목적에 따라 구분하고, 상태와 메서드의 소유자는 유지한다.
참고: [controller 본체][V2], [이벤트 handler][V3], [작업 처리][V4].

### 네이밍은 도메인, 역할, 범위를 표현한다

`QueueID`, `QueueScope`, `QueueInfo`는 대상의 identity, 범위, 정보를 구분한다.
정책 구현은 `gangPlugin`, 설정 필드는 `pluginArguments`다. receiver는 `gp`, 세션 매개변수는 `ssn`, 내부 closure의 지역 변수는 `job`, `ok`다.
정책 파일 `gang.go`와 테스트 `gang_test.go`를 같은 위치에 둔다.
참고: [Queue 타입][V5], [gang 구현][V6], [대응 테스트][V7].

### 확장 구조에 함께 드는 비용

`Session`에는 client, cache, Jobs, Nodes, Queues와 여러 callback map이 모여 있다.
등록 코드는 전역 `pluginBuilders` map에 이름과 생성자를 저장한다.
정책을 추가할 때는 개별 정책 파일과 함께 **공유 Session의 상태 및 등록 과정**도 이해해야 한다.
이 사례는 plugin 단위 분리와 중앙 공유 상태가 함께 존재하는 구조를 보여준다.
참고: [Session 필드][V8], [plugin 등록 저장소][V9].

## 4. KEDA: 공급자 구현을 공통 계약으로 연결한다

### 디렉토리 이름과 하위 분리 기준

```text
apis/keda/v1alpha1/       ScaledObject 등의 API 모델
controllers/keda/        Kubernetes reconcile 처리
pkg/scaling/             scaling 조정과 scaler 생성
  executor/              scaling 실행 계약과 구현
pkg/scalers/
  prometheus_scaler.go
  prometheus_scaler_test.go
  aws_cloudwatch_scaler.go
  aws_sqs_queue_scaler.go
  aws/                   AWS 관련 지원 코드
  authentication/        공통 인증 모델과 처리
  scalersconfig/         공통 설정 모델과 해석
cmd/operator/            operator 실행 진입점
```

개별 연동 기능은 `prometheus_scaler.go`, `aws_sqs_queue_scaler.go`처럼 파일로 표현한다.
`aws`는 공급자 범위, `authentication`은 여러 구현에 걸친 기능, `scalersconfig`는 사용 대상과 책임을 함께 표현한다.
**개별 구현 파일과 재사용 하위 패키지를 함께 두며, 반복되는 지원 책임을 하위로 분리한다.**
참고: [scalers 하위 구조][K9].

operator 진입점은 `NewScaleHandler(...)`로 handler를 만들고 reconciler의 `ScaleHandler` 필드에 넣는다.
객체 생성과 의존성 전달을 같은 조립 위치에서 읽을 수 있다.
참고: [operator 조립 코드][K1].

### 공통 계약은 공급자에게 필요한 행동을 묶는다

`Scaler`는 세 메서드를 선언한다.

| 메서드 | 계약에 적힌 책임 |
|---|---|
| `GetMetricsAndActivity` | metric 값과 활성 여부 반환 |
| `GetMetricSpecForScaling` | scaling에 사용할 metric spec 반환 |
| `Close` | 사용한 자원 정리 |

Prometheus 고유 상태와 질의 처리는 `prometheus_scaler.go`에 모여 있다. 인증과 설정 해석은 공통 하위 패키지를 사용한다.
**공급자 고유 구현과 여러 구현에 걸친 지원 책임을 서로 다른 단위로 읽는 방식이다.**
참고: [Scaler 계약][K2], [상태와 생성자][K3], [질의 처리][K10], [하위 패키지 구조][K9].

### 대상과 역할을 파일명에 함께 쓴다

| 실제 이름 | 표현하는 정보 |
|---|---|
| `scaledobject_types.go` | ScaledObject API 구조 |
| `scaledobject_controller.go` | ScaledObject reconcile 처리 |
| `prometheus_scaler.go`, `prometheus_scaler_test.go` | 공급자 구현과 대응 테스트 |
| `prometheusScaler`, `prometheusMetadata` | 구현 객체와 입력 설정의 구분 |
| `httpClient`, `scalableObjectName`, `triggerName` | 객체가 보관하는 장수명 상태 |
| `httpClientTimeout` | 생성 과정에서 선택하는 timeout 값 |
| `meta`, `err` | 좁은 지역 범위의 값 |

참고: [API 타입][K4], [controller][K5], [공급자 이름][K3], [공급자 테스트][K6].

### 중앙 생성 지점의 변경 비용

`scalers_builder.go`에는 trigger 종류에 따라 생성자를 선택하는 switch가 있다.
공급자 선택 위치가 명확하고, 공급자 추가 시 중앙 생성 코드도 수정한다.
이 파일에는 `gocyclo` 검사 예외가 설정되어 있다.
개별 구현 파일의 분리와 중앙 dispatch의 복잡도를 함께 살펴볼 수 있는 사례다.
참고: [공급자 선택][K7], [복잡도 검사 예외][K8].

## 5. 사례에서 얻는 설계 기준

아래 기준은 세 프로젝트의 관찰에서 도출한 해석이다.

| 관점 | 배울 점 | 대표 사례 |
|---|---|---|
| 패키지 | 기능의 대상, 상태 보관 목적과 수명으로 책임을 구분한다. | Kueue의 queue manager와 scheduler cache |
| 디렉토리 이름 | 역할, 대상, 정책, 공급자, 공통 책임을 경로 안에서 구분한다. | `controller/jobs/rayjob`, `plugins/numaaware`, `scalers/authentication` |
| 하위 분리 | 각 구현의 실제 내부 역할에 맞춰 깊이를 선택한다. | `gang`의 파일 구성, `numaaware/policy`와 `provider` |
| 파일 | 같은 대상 안에서 계산, controller, handler, adapter의 역할을 드러낸다. | Job 파일군, Queue controller 파일군 |
| 변수 | 필드는 보관하는 역할을 드러내고, receiver와 매개변수 및 지역 변수는 범위와 맥락에 맞춰 이름을 쓴다. | `workloadAssignedQueues`, `httpClient`, `gp`, `ssn`, `opt` |
| 계산 분리 | 외부 조회 결과를 입력값으로 확정해 계산 함수에 전달한다. | `AdjustmentInputs`와 `EffectivePodSpecs` |
| 의존성 연결 | 생성과 연결을 조립 위치에 모으고 필요한 행동의 범위로 계약을 정한다. | operator 조립, `StatusChecker`, `Scaler` |
| 상태 소유권 | 파일을 나누어도 같은 상태의 작성자와 동기화 책임을 추적한다. | `queuecontroller`의 역할별 파일 |
| 확장 비용 | 개별 구현과 함께 중앙 등록, 선택 코드, 공유 상태도 읽는다. | Volcano Session, KEDA scaler builder |

## 6. 참고 버전

| 프로젝트 | 버전 | 소스 commit |
|---|---|---|
| Kueue | v0.20.5 | `7e71578095b353cedbfa63607b60c9739af9d501` |
| Volcano | v1.16.0 | `41f27d567fb5bdaf2cab01e1caaf48f7e266eefb` |
| KEDA | v2.21.0 | `626ded5d783108bedf647e20bbc83f8e458e02e4` |

[Q1]: https://github.com/kubernetes-sigs/kueue/blob/7e71578095b353cedbfa63607b60c9739af9d501/pkg/cache/queue/manager.go#L156-L230
[Q2]: https://github.com/kubernetes-sigs/kueue/blob/7e71578095b353cedbfa63607b60c9739af9d501/pkg/cache/scheduler/cache.go#L161-L182
[Q3]: https://github.com/kubernetes-sigs/kueue/blob/7e71578095b353cedbfa63607b60c9739af9d501/pkg/workload/effective_resources.go#L37-L145
[Q4]: https://github.com/kubernetes-sigs/kueue/blob/7e71578095b353cedbfa63607b60c9739af9d501/pkg/cache/queue/status_checker.go#L21-L25
[Q5]: https://github.com/kubernetes-sigs/kueue/blob/7e71578095b353cedbfa63607b60c9739af9d501/pkg/scheduler/scheduler.go#L87-L238
[Q6]: https://github.com/kubernetes-sigs/kueue/blob/7e71578095b353cedbfa63607b60c9739af9d501/pkg/workload/effective_resources_test.go#L17-L38
[Q7]: https://github.com/kubernetes-sigs/kueue/blob/7e71578095b353cedbfa63607b60c9739af9d501/pkg/controller/jobs/job/job_controller.go#L17-L90
[Q8]: https://github.com/kubernetes-sigs/kueue/blob/7e71578095b353cedbfa63607b60c9739af9d501/pkg/controller/jobs/job/job_webhook.go#L17
[Q9]: https://github.com/kubernetes-sigs/kueue/blob/7e71578095b353cedbfa63607b60c9739af9d501/pkg/controller/jobs/job/job_multikueue_adapter.go#L17
[V1]: https://github.com/volcano-sh/volcano/blob/41f27d567fb5bdaf2cab01e1caaf48f7e266eefb/pkg/scheduler/framework/interface.go#L30-L51
[V2]: https://github.com/volcano-sh/volcano/blob/41f27d567fb5bdaf2cab01e1caaf48f7e266eefb/pkg/controllers/queue/queue_controller.go#L17-L104
[V3]: https://github.com/volcano-sh/volcano/blob/41f27d567fb5bdaf2cab01e1caaf48f7e266eefb/pkg/controllers/queue/queue_controller_handler.go#L17-L44
[V4]: https://github.com/volcano-sh/volcano/blob/41f27d567fb5bdaf2cab01e1caaf48f7e266eefb/pkg/controllers/queue/queue_controller_action.go#L17-L60
[V5]: https://github.com/volcano-sh/volcano/blob/41f27d567fb5bdaf2cab01e1caaf48f7e266eefb/pkg/scheduler/api/queue_info.go#L36-L68
[V6]: https://github.com/volcano-sh/volcano/blob/41f27d567fb5bdaf2cab01e1caaf48f7e266eefb/pkg/scheduler/plugins/gang/gang.go#L41-L110
[V7]: https://github.com/volcano-sh/volcano/blob/41f27d567fb5bdaf2cab01e1caaf48f7e266eefb/pkg/scheduler/plugins/gang/gang_test.go#L17-L26
[V8]: https://github.com/volcano-sh/volcano/blob/41f27d567fb5bdaf2cab01e1caaf48f7e266eefb/pkg/scheduler/framework/session.go#L69-L167
[V9]: https://github.com/volcano-sh/volcano/blob/41f27d567fb5bdaf2cab01e1caaf48f7e266eefb/pkg/scheduler/framework/plugins.go#L33-L54
[K1]: https://github.com/kedacore/keda/blob/626ded5d783108bedf647e20bbc83f8e458e02e4/cmd/operator/main.go#L294-L305
[K2]: https://github.com/kedacore/keda/blob/626ded5d783108bedf647e20bbc83f8e458e02e4/pkg/scalers/scaler.go#L43-L54
[K3]: https://github.com/kedacore/keda/blob/626ded5d783108bedf647e20bbc83f8e458e02e4/pkg/scalers/prometheus_scaler.go#L32-L99
[K4]: https://github.com/kedacore/keda/blob/626ded5d783108bedf647e20bbc83f8e458e02e4/apis/keda/v1alpha1/scaledobject_types.go#L44-L54
[K5]: https://github.com/kedacore/keda/blob/626ded5d783108bedf647e20bbc83f8e458e02e4/controllers/keda/scaledobject_controller.go#L73-L87
[K6]: https://github.com/kedacore/keda/blob/626ded5d783108bedf647e20bbc83f8e458e02e4/pkg/scalers/prometheus_scaler_test.go#L142-L171
[K7]: https://github.com/kedacore/keda/blob/626ded5d783108bedf647e20bbc83f8e458e02e4/pkg/scaling/scalers_builder.go#L123-L291
[K8]: https://github.com/kedacore/keda/blob/626ded5d783108bedf647e20bbc83f8e458e02e4/.golangci.yml#L83-L85
[Q10]: https://github.com/kubernetes-sigs/kueue/tree/7e71578095b353cedbfa63607b60c9739af9d501/pkg/controller/jobs
[Q11]: https://github.com/kubernetes-sigs/kueue/tree/7e71578095b353cedbfa63607b60c9739af9d501/pkg/cache
[Q12]: https://github.com/kubernetes-sigs/kueue/blob/7e71578095b353cedbfa63607b60c9739af9d501/pkg/cache/scheduler/cache.go#L955-L987
[V10]: https://github.com/volcano-sh/volcano/tree/41f27d567fb5bdaf2cab01e1caaf48f7e266eefb/pkg/scheduler/plugins
[K9]: https://github.com/kedacore/keda/tree/626ded5d783108bedf647e20bbc83f8e458e02e4/pkg/scalers
[K10]: https://github.com/kedacore/keda/blob/626ded5d783108bedf647e20bbc83f8e458e02e4/pkg/scalers/prometheus_scaler.go#L223-L327

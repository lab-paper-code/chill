# Scheduler 인스턴스 전달과 수동 원자 실행

현재 physical instance transport와 수동 실행 계약이다. 자동 판단과 DAG는
[스케줄러 구현 설계](scheduler-implementation.md), 측정 적격성은
[실행기와 측정 계약](scheduler-runtime-measurement.md#baseline과-실제-원자-동작-측정),
과거 네트워크·배포 관측은 [실행 이력](scheduler-execution-history.md)에 둔다.
수동 실행 성공을 automatic 판단·power 측정·FULL 수용으로 해석하지 않는다.

## Why concealment forces routing

Some configuration changes cannot be made underneath live traffic. A power mode
applies to a whole node, and on a memory-constrained device a new model cannot
be resident before the old one leaves.

The [transition specification](../../spec/05_transition_control.md) requires
planning request handling while a node changes state. Routing can move traffic
to other ready nodes when the changing node cannot continue serving.

## Where a request branches

EdgeMesh's proxy is a DaemonSet. It intercepts through the iptables rules of the
node the client runs on and selects an endpoint there. No point exists through
which all requests pass.

An external consumer has no such node, so its request enters the cluster
somewhere, and that entry is the one point every external request crosses.

## 현재 인스턴스 전달 경로

고정 노드 테이블을 복원하지 않고, native physical instance의 현재 Kubernetes
Pod를 단일 목적지 Service에 연결한다.

Choosing a backend and reaching it pull in opposite directions: calling a shared
Service hands the choice to EdgeMesh, which applies no weight, and calling a pod
address directly does not resolve across the tunnel.

Each actual serving Pod gets `chill-executor-<Pod UID>`, a ClusterIP Service owned
by that Pod. Its selector is only `edge.dacs.io/serving-pod-uid=<actual Pod UID>`,
installed after Kubernetes assigns the UID; its sole TCP port is named `http`,
8080, targeting the isolated firstfloor container's named `http` port. The Service
carries `service.edgemesh.kubeedge.io/service-proxy-name: ""`, matching the
existing EdgeMesh proxy filter. One endpoint leaves EdgeMesh nothing to select:
CHILL chooses the physical instance, EdgeMesh transports the request.

```
entry point ── physical-instance choice ──▶ chill-executor-<Pod UID>:8080
                                                    │  EdgeMesh tunnel
                                                    ▼
                                       that Pod's firstfloor container
```

The decision is CHILL's and the transport is EdgeMesh's, which is the division
that already governs power mode.

This is an internal execution-instance transport, not a user-facing inference
service. Workload names describe the inference service and its SLO role; model
names identify the model artifact, not firstfloor or another execution engine.
The executor Service name is shared by all Workloads using that physical Pod.

목적지는 실제 Pod UID와 container identity를 구분한다. 생성 동작은 실제 UID label과
소유권·selector·port가 일치하는 Service만 생성/재사용한다. 같은 이름의 다른 binding은
수정하지 않고 거부한다. Observer와 Capture, policy read-back, native drain은 모두 이
Service의 ClusterIP를 사용하며 직접 PodIP·정적 노드 route fallback은 없다.
UID label은 실제 UID와 현재 label을 test하는 JSON Patch로 추가한다. CNI·kubelet의
status/resourceVersion 갱신은 이 metadata-only binding을 막지 않는다. 다른 label 변경이나
Pod 교체는 거부하며 재시도하지 않는다. 기존 preparation의 CPU·memory quantity는
stdlib `maps.EqualFunc(resource.Quantity.Equal)`로 값만 비교한다. API의 quantity 표기나
내부 문자열 cache 차이를 다른 자원 할당으로 취급하지 않는다.
GET으로 현재 Service의 소유권·selector·EdgeMesh label·port를 검증하고, selector가
현재 Pod/container 하나만 선택하는지 namespace Pod LIST로 확인한다. HTTP probe 뒤
Pod/container를 다시 읽어 교체된 executor를 이전 인스턴스로 게시하지 않는다.
Pod는 `RestartPolicyNever`여야 한다. 같은 Pod UID 뒤에서 container가 재시작하여
이미 게시한 physical instance의 실행 목적지가 바뀌는 것을 허용하지 않는다.
native model/state와 immutable profile/runtime evidence의 준비 검증은 그대로 유지한다.
Pod Ready나 Service 생성만으로 추론 준비를 완료하지 않는다.

prepare는 저장된 현재 정책을 유지하며 오래된 Pod 정책 annotation을 고친다.
annotation 수정 자체를 native acknowledgment로 간주하지 않는다.

제거는 native drain과 실제 container 종료 증거를 확인한 뒤 자기 Pod 소유의 Service를
resourceVersion/UID precondition으로 삭제하고 Pod finalizer 및 config를 해제한다.
이때는 namespace·이름·Pod owner identity만 확인한다. 자기 Service의 selector나 proxy
label drift가 종료된 Pod의 cleanup을 막게 하지 않는다. 다른 Pod 소유의 Service는
삭제하지 않는다. Pod owner reference는 Kubernetes GC에도 같은 수명을 연결한다.
Workload cleanup도 종료 증거 이후 자기 Pod의 Service를 해제한다.
Pod source finalizer의 추가/해제는 immutable Pod UID를 포함한 Kubernetes
strategic-merge primitive-list delta로 자기 항목만 변경한다. 다른 finalizer를
보존하며 unrelated kubelet status/resourceVersion 갱신은 충돌 원인이 아니다.
UID 변경이나 API의 실제 metadata 제약 오류는 그대로 반환한다. 해제 전
UID/container-bound 실제 종료 증거와 Pod 종료 확인은 유지한다.
기존 EdgeMesh 설정이나 다른 workload는 변경하지 않는다.
namespace의 ChillServer Role에는 Service GET/DELETE를 기본으로 허용하며,
scheduler/native operation 경로가 켜진 경우 CREATE를 추가한다.

## 수동 원자 실행 계약

원자 동작 측정 command의 membership은 detached Node와 명시적으로 선택한
`measurement-*` isolated POOL 사이의 이동만 허용한다. 다른 live POOL의 Node를
편입하지 않으며 동일 Node UID, 선택된 Workload 없음, 독점 관측 기록 조건을 유지한다.
같은 출발·도착 상태의 no-op는 membership 비용으로 측정하지 않는다.

`serving-operation --execute-config FILE`은 측정과 별개의 수동 원자 실행 경계다.
`--measurement-config`와 함께 사용하지 않으며, catalog 검증·현재 ChillServer에 Loaded인
`ort-cpu` 또는 `ort-cuda` profile의 기존 `preparations[profileID]`만 준비한다.
GPU prepare/remove도 기존 측정 profile 검증·정확한 physical Node·GPU UUID·whole-device
자원 예산·native runtime 일치 경계를 재사용하며 CPU fallback이나 GPU UUID 재매핑을 허용하지 않는다.
inline `PrepareSpec`, route/power/resize 동작, 임의 runner 명령을 실행 manifest로 받지 않는다.
입력은 `operation`(기존 `Operation`의 id/kind/node, remove의 정확한
podUID/containerID instance 또는 membership의 pool), `profileID`, `namespace`,
선택 `actuationNamespace`, 독점
`journalDirectory`, 별도 HTTPS 신뢰·caller 인증의 `ownerURL`/`ownerCAFile`/
`ownerTokenFile`, `schedulerConfigMap`/`schedulerConfigKey`/`sourceConfigRV`,
`expectedPool`, 명시 history 시작 시각 `routingHistoryFrom`이다. store는 별도 입력 없이
ChillServer가 로드한 preparation의 binding에서 가져온다. `before.pods`/`actual.pods`,
observer와 raw power/latency는 현재 모든 외부 Pod의 실제 identity와 소비를 포함한다.
Kubernetes credential은 Kubernetes 작업에만 사용하며 ChillServer로 자동 전달하지 않는다.
ChillServer는 명시한 CA 파일과 caller token을 모두 요구하며 ambient trust로 대체하지 않는다.
verified HTTPS origin 하나만 허용하고 redirect를 따르지 않는다.
startup bundle은 catalog 검증 profile의 공통 `Profile.NativeBundle()` producer에서
ChillServer와 CLI가 같은 key/declared size를 도출한다. ChillServer만 immutable publisher이며 CLI는
저장된 payload를 다시 읽어 size를 자가 증명하거나 bytes/digest 비교 gate를 추가하지 않는다.
기존 materializer가 producer-declared size를 확인한 뒤 native startup 파일을 게시한다.
이 trusted publisher 계약을 같은 크기의 임의 변조에 대한 암호학적 증명으로 해석하지 않는다.
선택된 `completionCalibration`이 있으면 기존 immutable raw loader로 다시 읽고
동일 profile/Node/allocation 조건의 검증된 최대 completion 관측과 전체 출처를 native
policy에 연결한다. 선택이 없으면 configured bootstrap policy를 그대로 유지한다.
planner의 calibration 선택과 기존 인스턴스에 실제 적용된 native policy를 혼동하지 않는다.

실행 전에 desired/current controls OFF, 진행 중 ChillServer action 없음, 같은 native
ConfigMap resourceVersion의 전체 `staticRouting` 성공 acknowledgement와 실제
route readback 일치를 요구한다. 모든 동작의 ChillServer snapshot은 해당 완료보다 늦고
현재 시각보다 미래가 아니어야 한다. remove는 전체 경로에서 이미 제외된 정확한 configured
source만 허용하며, complete ack 이후 ChillServer `snapshot.outstanding`에서 그 source의
execution·transport·response 소유권이 모두 사라졌음을 확인한다. controls OFF·진행 중 action 없음·
같은 ConfigMap revision에서 controlled routing은 새 UID나 instance 재발견으로 제외된 ingress를
다시 열지 않으므로 이 zero는 단조적이다. 별도 하한 age gate 대신 실행 직전 revision 재확인과
기존 native drain/termination/removal을 유지한다. prepare/membership은 현재 revision과
fresh Node/Pod 및 각 동작의 물리 검증에 의존한다. automatic freshness/SLO 정책은 변경하지 않는다.
ack은 일시적인 `state.results`가 아니라 기존 `/api/v1/observations/scheduler`의
`action.actual`에서 exact source revision의 최신 manual result를 찾는다. 명시
`routingHistoryFrom`부터 고정 query 종료 시각까지 명시 `limit=50`과 기존 cursor로
모든 page를 조회하고 최신 matching ack를 고른 뒤 새 ChillServer snapshot을 읽는다.
ChillServer의 큰 기본 page 한도에 기대지 않으며, page 응답은 기존 64MiB read bound를 유지한다.
보관 기간을 늘리거나 시작 이후 전체 history를 조회하지 않는다.
`/pools`의 waiting/running/assignedUnknown 0이나 native queue empty만으로 이를
대체하지 않는다. prepare/membership target은 ChillServer instance가 없어야 한다.
prepare는 fresh Node/모든 active Pod 요청·init/sidecar/overhead를 기존 capture 경로로
다시 합산하고 ChillServer의 inventory/reserved와 공통 `ValidateNodeBudget`으로 admission한다.
설정된 serving namespace의 기존 serving Pod, 미확인 GPU 소유권, CPU overlap/예약 부족, memory 초과와 실제 Node
MemoryPressure는 거절한다. 오래된 ChillServer aggregate의 Ready는 재사용하지 않는다.
이는 설정 reservation 기반 admission이지 실제 CPU 격리·외부 Pod의 성능 불변·SLO·에너지
절감 보장이 아니다. membership은 설정된 serving namespace의 CHILL serving/accepted-work
소유권과 drain을 확인하는 serving-pool label 변경이다. 다른 namespace의 동일 component label은
CHILL 소유권이 아니며, 모든 외부 Pod의 요청은 prepare 예산에 포함하고 그 Pod를 변경하지 않는다.
membership은 detached와 해당 configured preparation POOL 사이의 실제 변경만 허용하고
기존 sole writer `MoveNodePool`의 UID/resourceVersion patch/readback을 사용한다.
원복도 새 현재 revision·expectedPool witness로 같은 경계를 호출한다. CLI는 완전한
`OperationResult`를 stdout에 출력한다. 부모 실행자는 원래 두 Workload/SLO/model과
전체 route map을 보존하고, 기존 ChillServer `staticRouting`으로 한 UID만 바꾼 뒤 실제
새 instance 완료를 관측하며, 전체 원래 route를 복구·ack·완전 drain한 다음 제거한다.
이는 manual execution 근거이며 automatic scheduler judgment나 power 측정 근거가 아니다.

## 수동 실행 manifest와 CLI 예시

prepare 입력은 부모가 캡처한 현재 ConfigMap JSON(`$CM_CAPTURE`)에서 생성한다.
`PROFILE_ID`는 해당 revision에서 ChillServer가 Loaded라고 확인한 기존 CPU/GPU profile ID이며,
그 외 환경 변수는 부모가 확인한 namespace/HTTPS trust/caller token/독점 관측 기록이다.
이 예시는 manifest 생성만 하며 Workload·SLO·model·route map을 변경하지 않는다.

```sh
jq --arg key "$SCHEDULER_KEY" --arg profile "$PROFILE_ID" \
  --arg operation "$OPERATION_ID" --arg observationLog "$OBSERVATION_LOG_DIRECTORY" \
  --arg chillServer "$CHILL_SERVER_URL" --arg ca "$CHILL_SERVER_CA_FILE" --arg token "$CHILL_SERVER_TOKEN_FILE" \
  --arg history "$ROUTING_HISTORY_FROM" \
  '. as $cm | ($cm.data[$key] | fromjson) as $config |
   $config.preparations[$profile] as $prepare |
   {operation: {id: $operation, kind: "prepare", node: $prepare.instance.node},
    profileID: $profile, namespace: $cm.metadata.namespace,
    journalDirectory: $observationLog, ownerURL: $chillServer, ownerCAFile: $ca,
    ownerTokenFile: $token, schedulerConfigMap: $cm.metadata.name,
    schedulerConfigKey: $key, sourceConfigRV: $cm.metadata.resourceVersion,
    expectedPool: $prepare.instance.pool, routingHistoryFrom: $history}' "$CM_CAPTURE"
```

출력을 보존한 JSON 파일로 `serving-operation --execute-config FILE`을 호출한다.
remove는 같은 current-config binding에 `operation.kind="remove"`와 실제 관측한
정확한 `operation.instance`를 사용한다. membership은 `kind="membership"`와
실제 목적지 `operation.pool`(detach는 빈 문자열), 현재 실제 `expectedPool`을 사용한다.
각 단계마다 현재 revision·ack·snapshot을 새로 캡처하며, route는 이 CLI로 변경하지 않는다.
실행 flags는 `--execute-config FILE --timeout 10m`와 별도 native Kubernetes 연결의
`--kube-api-server URL --kube-token-file FILE --kube-ca-file FILE`이다.
API server를 생략하면 기존 `rest.InClusterConfig()`만 사용하며 로컬 kubeconfig는
읽지 않는다. 명시 API server에서 token/CA flags를 생략하면 표준 service-account
token/CA 경로를 사용한다. ChillServer CA/token은 manifest의 별도 명시 파일이며 자동 전달하지 않는다.

## 원자 동작 측정의 격리

measurement command는 제한된 `measurement-*` POOL과 관측된 요청 소유권·native
counter 불변 조건에서 동일 원자 동작을 실행한다. observation directory는 process
exclusive lock으로 소유하며, live Serving traffic과의 병행 측정은 이 측정 조건과 구분한다.

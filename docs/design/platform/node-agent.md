# Node agent와 inventory 자동화

Node agent는 hardware/online CPU 관측과 host 전력 적용을 소유한다.
전력 적용은 `PowerModeReconciler`, 요청/관측 metadata 계약은
`internal/metadata/power_mode.go`가 구현한다.

## Inventory와 배포

| 사실 | 출처와 사용 |
|---|---|
| Online CPU | `/sys/devices/system/cpu/online`을 `edge.dacs.io/cpus` annotation으로 기록; 실행 환경 관측 |
| GPU | 동일 Node UID의 검증된 profile GPU UUID; allocatable 수와 예약 GPU 검사는 유지 |
| 요청 가능 전력모드 | 현재 DeviceClass의 non-Fixed 모드와 configured actuation policy |
| Wall meter | ChillServer 설정의 전력계 선언(`servingops.MeterDeclaration`)이 가진 meter ID, Node 신원과 전력 source. 측정 세션은 대상 Node UID와 일치하는 선언을 계획의 전력계(`plan.meter`)로 쓰고 선언이 없으면 요청을 거절한다 |
| Reserved | 운영자 선언 memory/GPU 예산 |

현재 scheduler allocation은 memoryBytes와 GPUUUID를 사용한다.
GPU 미측정 장치는 후보에 나오지 않는다. Meter ID/source를 노드 이름으로 대체하면
기존 비용 근거와 연결할 수 없으므로 원래 신원을 보존한다.

자원 수집은 첫 Node 조회가 실패하면 종료하지 않고 다음 전송 주기마다 다시 조회한다.

바이너리 `cmd/node-agent`, 이미지와 operator 설정은 공통이고 권한별 DaemonSet은
분리한다.

| DaemonSet | 범위와 권한 |
|---|---|
| `chill-node-agent` | 모든 노드의 signature/online CPU 관측; 비root, 읽기 전용 host 경로, Node get/patch |
| `chill-node-agent-actuation` | Policy DeviceClass selector 대상; hostPID, SYS_BOOT, 쓰기 sysfs, 필요한 vendor mount와 Node get/patch |

Operator는 DeviceClass를 watch하고 selector를 actuation affinity로 옮긴다.
`nvidiaLockedGPUClock.nodeNames`는 DeviceClass selector와 AND로 결합하며 이름별
단일 matchFields term을 OR한다. 여러 이름을 하나의 matchFields values에 넣지 않는다.
빈 목록은 DeviceClass 범위를 유지한다. 두 DaemonSet은 measurement toleration을 갖는다.

Discovery agent와 actuation agent는 각자의 metadata만 쓴다. Measurement taint와
grant는 ChillServer 소유다. Metric/workload observer는 독립된 프로토콜/권한 경계다.
Serving Pod의 supplementalGroups와 runtime access는 측정된 actor 전달에서 재현한다.
Agent가 별도 장치 GID를 발견해 다른 실행조건을 만들지 않는다.

## Pod 메모리 관측

Agent는 노드 자원 관측에 컨테이너별 현재 메모리와 수명 전체 최댓값을 싣는다.
최댓값은 cgroup v2 `memory.peak`, v1 `memory.max_usage_in_bytes`다. 측정 세션은
자기 Pod UID의 컨테이너 표본 중 최댓값을 배치 크기의 메모리로 쓴다(`PodMemoryPeak`).
최댓값 계수기가 없는 커널에서는 현재값 표본의 최댓값을 쓴다.

## 노드 전력 요청과 관측

[Node Power Actuation Boundary](../../adr/0015-node-power-actuation-boundary.md)와
[Power-Mode Reachability Boundary](../../adr/0017-power-mode-reachability-boundary.md)를 따른다.
Agent만 host 전력을 바꾸고 독립 read-back이 도달을 판정한다.

| Metadata | Writer와 의미 |
|---|---|
| `chill.dacs.io/power-mode` label | 승인된 요청자; 원하는 DeviceClass 모드 |
| `chill.dacs.io/power-mode-requested-at` | 같은 요청 patch의 시각; 기한 기준 |
| `chill.dacs.io/power-mode.state` label | Agent; pending/success/failed |
| `chill.dacs.io/power-mode-generation` | Agent; 실제 관측 변경/무효화 횟수 |
| `edge.dacs.io/power-actuator` | 독립 read-back JSON |
| `chill.dacs.io/power-mode-boot` | 마지막 관측 Node bootID |
| `chill.dacs.io/power-mode-transition` | 요청, generation, 적용/검증/settling/Ready 시각과 failure |

Agent는 자기 Node를 watch한다. 요청 목표와 read-back이 다르면 pending과
applyStartedAt을 먼저 기록한다. 적용 뒤 독립 read-back과 generation, verifiedAt을
기록하고 configured settling 뒤 success/readyAt을 쓴다. 이미 목표면 물리 적용 없이
현재 관측을 기록한다. Label 삭제는 요청 state를 지운다.
시각은 host 관측 뒤 읽으며 적용, 검증, settling, Ready 순서를 보존한다.

주기적 read-back은 actuator/boot 변경을 세고 재시작 자체는 세지 않는다.
관측 주기 안에서 원상복구된 변경은 놓칠 수 있다. Host read 실패 시 기존 관측을
성공으로 바꾸지 않는다. Boot 변경이 확인되면 낡은 관측을 무효화한다.

Apply 오류 뒤에도 별도 bounded read-back으로 실제 상태 또는 Unknown과 generation을
기록한다. 이후 관측 복구가 원 실패를 success로 바꾸거나 Apply를 재시도하지 않는다.
NVIDIA 검증 mismatch/read 오류도 관측을 게시한 뒤 실패 transition을 기록한다.
요청 없는 NVIDIA 기본 정책의 동적 clock 변화는 read-back에만 남긴다.

Agent는 수동 모드 변경을 원하는 모드로 자동 복원하지 않는다. 새 요청자는 실패 또는
현재 generation과 다른 success를 새 requestedAt으로 다시 요청한다.
측정 요청자는 host가 떠난 success를 재요청하고 failed 전환은 실패로 종결한다.

재부팅이 필요한 모드는 변경 전 fence와 pending을 보존한다. 새 agent는 목표 관측과
boot 변경을 함께 확인한 뒤 verified/settling/success를 기록한다. 재부팅 뒤 읽기 실패는
기존 적용 기한 안에서 기다리며 기한 초과는 failed다. 요청자는 요청 시각부터 policy
maxActuationDuration 안의 success/read-back을 요구한다. 늦은 실제 변경도 generation에
반영되어 겹친 측정을 무효화할 수 있다.

## 요청자와 측정

Node grant가 한 시점의 요청자를 정한다. ResourceVersion 조건 patch로 grant 변경과의
경합을 거절한다.

| 동작 | 요청자 |
|---|---|
| 계획기 power와 반환 시 낮춤 | ChillServer |
| Operation/occupancy의 전력 단계 | 루트 측정 taint를 확인한 ChillServer |
| BEO 측정 세션 | 루트 측정 taint를 확인한 ChillServer |
| ProfilingRun, loaded-idle baseline, 전환 비용 | 자기 루트 taint를 확인한 Profiler |

아직 기한 안의 pending Node는 새 측정에 승인하지 않는다. Operator stop safety도
pending agent 종료를 막는다. 공통 RequestPowerMode/ServesRequest/PowerModeLeft/
PowerModeDeadline/PowerModeOutcome 의미는 metadata 패키지가 소유한다.

Generation 시작은 측정 상태를 확립한 적용에서, 끝은 관측/최종화 경계에서 기록한다.
고정 상태 구간 기대 차이는 0, 자기 전환 구간은 실제 모드 변경 수다.
다른 차이는 `PowerChangedDuringMeasurement`다. 관측 없는 Fixed Node는 generation을
합성하지 않는다.

## 저장과 인스턴스 경계

현재 실행 evidence는 v1alpha18이다. Actuation의 generation과 선택적 handoff는
provenance이며 terminal container/Node workload snapshot은 없다. 과거 스키마를
읽는다는 보장은 없다. Metric sources는 v1alpha6이며 meter/source 신원을 유지한다.
저장 교체는
[Durable Stored-Contract Compatibility](../../adr/0019-durable-stored-contract-compatibility.md)를 따른다.

인스턴스 공유는 연구 범위 밖이다. Serving routing annotation은 필수이며 Workload는
하나 이하만 허용한다. UID map과 workload별 native waiting/running은 그대로 보존한다.
수동 remove는 다른 Workload 소유/binding instance를 거절한다. Static routing 비교는
[운영 설정과 비교 실험 관리](../../spec/scheduler/SRS.md#운영-설정과-비교-실험-관리)와
[고정 배치 비교 실행](../../spec/scheduler/SRS.md#고정-배치-비교-실행)을 따른다.

관측 프로세스는 blocking 파일 잠금으로 이전 소유자 종료를 기다린다. 장애 시 storage
session timeout까지 대기할 수 있다. Process는 모든 stream을 올린 과거 시간대를 기억하며
실패하면 같은 시간대를 다시 시도한다. 기존 immutable winner는 덮어쓰지 않는다.

## 보류한 이미지 분리

Go worker 바이너리와 runtime image 분리는 보류한 설계다. 현재 Dockerfile은 worker와
서버 바이너리를 runtime image 안에 빌드/복사한다. 분리 완료나 기존 측정과의 신원
동등성을 주장하지 않는다. d 재측정 영향은 실행 변경 전에 결정한다.

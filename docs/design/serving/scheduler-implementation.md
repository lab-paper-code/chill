# CHILL 스케줄러 구현 설계

## 1. 기준과 적용 범위

[스케줄러 SRS](../../spec/scheduler/SRS.md)의 SCH-01~32와
[요구사항·구현 대조](scheduler-srs-audit.md)에 대응하는 현재 판단·제어 구조다.
요구사항과 수용 기준의 원본은 `docs/spec/`이며 이 문서는 구현 책임과 불변식을 정리한다.

firstfloor의 Go 큐·배처·persistent Python IPC runner를 `internal/firstfloor`,
`cmd/serving-worker`, `runtime/firstfloor`에 연결했다. 동료의
`/home/genesis1/ETRI/donghwan/firstfloor`는 보존한다. 실제 actor와 후보 replay는
같은 순수 선택 함수를 사용하고 ingress Runtime은 Workload 수용·분배·drain을 맡는다.

| 세부 계약 | 원본 문서 |
|---|---|
| 실행기·시간·CPU·fixed-batch·baseline·catalog | [실행기와 측정](scheduler-runtime-measurement.md) |
| 설정·completion margin·API·관측 조회 | [설정과 관측 API](scheduler-api.md) |
| Pod-bound 전달·수동 원자 실행 예시 | [인스턴스 전달과 수동 실행](scheduler-operations.md) |
| 과거 build·smoke·배포 결과 | [실행·배포 이력](scheduler-execution-history.md) |
| 최신 live evidence·수용 범위 | [라이브 증거 캠페인](../../plans/2026-10-13-scheduler-live-evidence-campaign.md) |

## 2. 책임과 패키지 방향

| 소유자 | 입력과 책임 | 실제 완료 근거 |
|---|---|---|
| `internal/firstfloor` | bounded per-UID queue, frozen request SLO, 같은 UID의 호환 batch, energy-gain 대기, 단일 runner 실행 | 요청별 progress·실제 batch·runner 종료·동기 응답 |
| `internal/serving` | 정의·receipt·수명·ingress 대기, 운영 부하, 후보 replay·SLO/energy 선택, 분배·판단 관측 기록 | 실제 native 상태와 반환 결과·분배 적용 기록 |
| `internal/serving/firstfloor` | HTTP/metadata/progress의 typed 경계, native span과 client span | prepared metadata, 정확한 request/UID/definition/physical instance 상관 |
| `internal/serving/kube` | 실제 자원 snapshot, profile/cost catalog, Pod·policy·power 원자 동작, 전환 의존성 | API 반환에 이어 실제 Pod/native readiness·drain·삭제·Node 상태 확인 |
| 기존 Profiling owner | 동일 firstfloor 경로의 fixed-batch 측정·idle·raw evidence·curve | 원래 Run/Job/Node/container/image/actuation와 raw trial binding |
| 기존 NodeActuator | 유일한 호스트 전력모드 변경 | 독립 read-back·settling·Ready/Failed |
| 기존 metric/evidence owner | Node별 wall-power·sample/failure·적분·immutable raw 보존 | 조건·구간·coverage가 확인된 watts/joules |

`firstfloor`는 Serving/Profiler 도메인에 역참조하지 않는 실행기 leaf다.
Serving core는 leaf를 사용하고 Kubernetes/API 호출은 kube adapter에 둔다.
프로파일러는 자체 runtime evidence 계약과 firstfloor 실행 경로를 연결한다.
`.go-arch-lint.yml`에 이 방향과 실제 생산 패키지를 함께 반영한다.

## 3. 실행기·요청 계약

외부 요청은 Workload별 독립 입력 1개와 불변 UID·definition·개별 SLO를 유지한다.
[Workload 요청 경로](workload-management.md#7-요청-경로와-큐)를 재사용하며,
[실행기·시간 계약](scheduler-runtime-measurement.md#요청과-실제-모델)에 원시 입력 필드,
IPC, deadline policy, 원래 monotonic elapsed와 physical release 조건을 둔다.

판단은 고정 historical `observationEnd`와 현재 outstanding의 decision origin `at`를
구분한다. source 시각을 재표시하지 않고 미래·오래된 물리 관측은 Pending으로 둔다.
완료 여유는 설정의 `completionMarginMS`를 요청마다 한 번 반영하며 이미 소모한 sender
elapsed는 원래 receipt 경과에서만 뺀다. 상세 window·margin·반환 milestone 규칙은
[시간과 수명](scheduler-runtime-measurement.md#시간과-수명)이 원본이다.

### 3.3 같은 선택 함수의 실행과 replay

순수 batch 선택 함수의 입력은 검증된 batch별 service duration sample·incremental
energy/input, 현재 per-UID 요청의 원래 경과 시간/SLO, queue age, 반환 envelope,
max wait, 관측 요청률이다. 출력은 선택 UID·batch size·대기 시간과 판단 이유다.
actual actor와 discrete-event replay가 이 함수를 공유한다.

1. normal 요청은 stable EDF로 선택하며 같은 UID/불변 정의의 호환 요청만 배치한다.
2. 요청별 p-quantile service 비용과 원래 elapsed·반환 비용으로 여유를 계산한다.
3. SLO 여유를 만족하는 현재 batch 중 energy/input을 비교한다.
4. 더 큰 measured batch의 **엄격한 energy gain**과 관측 요청률로 예상 fill time을
   확인한 경우에만 bounded wait를 선택한다.
5. feasible batch가 없으면 keep은 minimum-C fillable batch(동률은 큰 batch)를 즉시
   실행한다. drop은 head를 제거하고 defer는 head를 후순위 FIFO로 옮긴다.
6. normal 요청이 없으면 deferred FIFO의 호환 prefix를 minimum-C로 실행한다.
7. drop/defer/timeout은 성공 지연 표본과 구분하며 caller lifetime/cancel은 별도로 적용한다.

실제 적용되는 설정·profile과 진행 중 batch의 snapshot을 기록한다. accepted
요청의 정의·SLO를 config 변경 때문에 다시 해석하는 대신 기존 context를 유지한다.

## 4. 측정 계약과 catalog

Serving과 Profiling은 같은 firstfloor actor/runner·initialized batch 조건을 사용한다.
고정-K 측정은 fake profile 없이 실행하고 raw client/service/backend sample,
warmup 제외·자원·장치·image와 원래 trial binding을 보존한다. 실행 domain과 policy cap은
각각 초기화·실측 domain을 따른다.

profile은 incremental energy가 차감한 idle과 raw evidence를 보존한다. baseline은
실제 Node·mode·준비 집합·meter에 연결하며 공존 측정 전 additive 예상은 실측과 구별한다.
원자 동작 측정은 production primitive를 호출하고 실제 readiness/drain/read-back,
wall joules·부분 실패·불충분한 표본을 보존한다. 세부 실행 조건·catalog lifecycle·
baseline과 추론 제외 기준은 [측정 계약](scheduler-runtime-measurement.md#같은-firstfloor-fixed-batch-경로)에 둔다.

## 5. 판단 입력과 후보 계산

### 5.1 한 운영 snapshot

운영 observation window·control interval·comparison horizon을 독립 설정한다.
관측 기록의 보존/발행 구간과 지정 구간의 분석 필터는 운영 부하와 구분한다.
모든 UID의 received/accepted/assigned/completion 사건을 한 번 수집해 해당 사건
구간의 inputs/s를 구하고, 실제 malformed/거절 결과도 전체 유입에 남긴다.
계산 가능한 compute demand와 total inflow는 모집단으로 구별한다.

Runtime의 pending/assigned와 firstfloor 현재 queued/running·running elapsed·batch를
physical instance/UID/definition에 연결한다. assignment와 실제 queue entry를
구별한다. native queue에 아직 없는 배정 요청은 배정된 instance로 전달 중인 요청이며,
replay는 이를 시작 시점에 그 instance의 native queue 끝에 넣어 남은 service 전체를 계산한다.
unknown instance·관측 공백·오래된 자료는 판단의 입력 상태에 표시한다.
현재와 목표의 Node UID·단일 POOL 소속·ready/capacity/resource 상태를 함께 캡처한다.
완료/실패 Pod는 이미 제외하던 자원 예산 캡처와 steady source refresh에서
Kubernetes field selector로 제외하여 종료된 profiling 이력의 반복 전송·해석을 피한다.
source refresh의 최초/재시작 watch 복구는 전체 Pod를 읽고, 이후 빠진 retained
executor는 기존 exact Pod GET과 UID/container 종료 확인을 유지한다.

### 5.2 후보 집합과 재현 가능한 replay

현재 유지 후보와 matching measured configurations의 Pod 개수·자원·배치 정책·
node/pool 배치·power mode·분배를 구성한다. 설정된 bounded candidate/replay 수와
검색 범위를 결과에 고정한다. 제한 도달은 검토 범위로 표시한다. 현재 선택이 이
범위에서 가장 유리한 선택이라는 의미로 사용한다.

Sharing 제어가 OFF이면 현재 physical instance의 active UID 집합을 그대로 후보의
단위로 사용한다. placement/resources/replicas를 바꾸더라도 기존 UID 집합을
합치거나 쪼개지 않으며, replicas ON의 복제도 같은 UID 집합의 복제다. replicas
OFF는 집합별 현재 물리 인스턴스 수를 보존하고 ON에서도 UID별 replica 상한을
넘지 않는다. Resources OFF의 실행 자원 비교에서 프로파일 유래 memory request는
제외하되, 선택한 대상의 실제 measured memory request와 전체 Node 예산 검증은
그대로 유지한다. 메모리를 source 값으로 바꿔 붙이거나 독립 조절 변수로 삼지 않는다.
다른 Node로의 placement는 Node-local CPU IDs·GPU UUIDs 대신 같은 CPU 수·스레드와
GPU 할당 종류를 유지하며, 같은 Node에서는 기존 physical allocation을 그대로 비교한다.
OFF 제어 비교는 같은 active UID 집합의 기존 인스턴스에만 연결한다. 중첩된
singleton의 다른 배치·자원·위치를 공유 집합의 변경 허가로 사용하지 않는다.
Routing OFF의 논리 replacement는 대응하는 기존 cohort replica의 UID별 실제
weight를 이전하며, preparation option의 scalar weight로 바꾸지 않는다.

현재 queued/running 요청에서 시작해 observation window의 실제 arrival sequence를
comparison horizon에 반복한다. arrival 재사용 가정·raw service sample draws·seed·반복 수를 기록한다.
현재 ingress는 실제 Workload queue 순서, native queued 요청은 actor state의 FIFO
순서를 캡처한다. 원래 receipt 시각은 request 나이와 arrival 가정에 사용한다.
명시적 original lifetime의 남은 시간은 queued/pending 취소와 실행 후 결과 상태에
적용하며, 진행 중 batch의 물리 점유는 실제 completion까지 유지한다.
weighted routing은 actual Runtime과 replay가 같은 deterministic
smooth weighted rule을 사용하며, 한 UID에 여러 ready 대상과 집중 대상 모두를
비교한다. 현재 outstanding·진행 중 batch와 공유 instance의 다른 UID도 포함한다.
모든 후보는 active UID별 완전한 분배 map을 갖는다. keep은 실제 분배를 보존하며,
새 준비 slot과 새로 공유된 UID는 인스턴스 weight로 분배를 구성한다. routing
제어가 꺼진 경우 기존 physical instance가 이미 제공하던 UID의 실제 weight를
보존한다. 집중 후보는 한 UID의 분배만
교체하고 다른 UID의 분배를 유지한다. 적용 시 active UID의 분배 누락은 오류이며
암묵적 weight fallback으로 보완하지 않는다. 준비 slot은 실제 Ready physical ID로
해결하고 replay와 같은 smooth weighted rule로 요청을 분배한다.
UID/input-path별 smooth-weighted credit은 같은 physical ID의 weight가 그대로인
경우에만 유지한다. weight 변경·새 route entry는 0에서 시작하며 replay도 이미
진행한 credit과 snapshot에서 처음 읽는 credit에 같은 reset 조건을 적용한다.

각 반복은 실제 batch selector의 결정을 실행하면서 empirical service sample과
같은 trial의 transition readiness/wall cost를 적용한다. 이미 running인 batch의
잔여시간은 현재 elapsed를 넘는 관측 sample의 조건부 잔여분으로 계산한다.
범위 밖 running elapsed는 profile-range 이유를 남긴다. 각 request의 queue·실행·
전달/반환이 반영된 종료 시각에서 **전체 end-to-end 분포**를 만들고 gonum/stat로
Workload percentile을 계산한다. 평균 batch latency나 단계별 quantile의 합을
end-to-end percentile로 사용하지 않는다.

CPU/GPU/memory는 전체 allocations와 system/비서빙 예약분을 포함한 예산으로
검토한다. `CPUCount`는 측정/실제 Pod request의 올림값이며 quota·ORT threads·
허용 affinity 폭과 구분한다. 측정한 threads와 전체 affinity를 그대로 재현하고,
affinity 폭이 threads보다 큰 구성도 허용한다. 서빙 mask 사이의 중복과 예약 침범은
계속 차단한다.
GPU UUID와 whole-device exclusive allocation을 확인한다. measured peak memory와
Serving 부가 예산은 feasible 제약이다. Pod마다 한 prepared runner와 순차 batch를
사용하며 replica는 Pod 수로 나타낸다.

비서빙 Pod의 effective CPU request는 Node별 millicore 합계를 먼저 구한 뒤
한 번만 올림하여 quota 수량 예산으로 기록하고, effective memory request도 합산한다.
이 quota 합계는 OS affinity/exclusivity 관측의 증거가 아니다. 명시적 exclusive CPU
예약이 전체 millicore 합계를 덮지 못하면 Node는 ready가 아니며, 비서빙 Pod의
물리 GPU 소유권 미관측도 계속 차단한다. 현재 native 서빙 인스턴스는 이 합계에서 제외한다.
GPU 소유권의 미관측은 해당 Node의 `ready=false`와 사유로 남기며 그 Node를
사용하는 후보를 Pending으로 둔다. 다른 확인된 CPU Node의 후보를 전역 오류로
막지 않는다. 격리된 Node도 전체 POOL 에너지·meter 범위에서 제외하지 않고,
실제 자원 예산 및 전역 native/profile 관측 공백의 차단은 유지한다.

### 5.3 전환·에너지·선택

프로파일 유휴 전력과 물리 meter는 같은 Node와 node-exclusive-wall 범위,
power source kind·NodeName·endpoint·metric·label selector로 연결한다.
동반 thermal endpoint/metric은 별도 관측 identity로 보존하며 같은 벽전력량의
연결 조건으로 사용하지 않는다. 다른 power series나 Node·범위는 계속 거절한다.

모든 affected UID의 최종 상태와 전환 중 ready capacity를 같은 시간축으로 replay한다.
node mode 변경·제거는 해당 노드의 모든 instance와 UID에 영향을 준다. 새 allocation을
동시에 수용할 수 있으면 prepare→route 뒤 기존 source를 stop/drain/remove한다.
exclusive CPU/GPU 또는 실제 예산 때문에 동시 점유가 불가능하면 최종 target에 없는
blocking source만 기존 stop→drain→remove→prepare→route 의존성으로 교체한다.
accepted ingress는 미래 target의 호환성만으로 포기하지 않는다. 현재 Ready이며 실제
routing된 대체 source가 없으면 기존 source에서 먼저 dispatch하고 원래 identity로
drain한다. 측정된 remove/prepare 시간·joules와 그동안의 실제 capacity, 대기 및 SLO를
동일하게 replay하므로 합법적 DAG도 Pending/Overload일 수 있다.
독립 Node의 안전한 동작은 병렬로, 같은 Node의 비용 구간은 의존 순서대로 반영한다.

기존 logical cohort의 routing origin은 weight 보존 근거이며 새 physical slot을
강제하는 fence가 아니다. 같은 measured profile·allocation·Node의 Ready/unbound
executor가 있으면 검증된 scheduler slot의 ownerless executor 또는 정확히 같은
Workload owner UID에 한해 그 physical ID를 재사용하고 중복 prepare를 추가하지
않는다. 빈 routing bindings로 ownerlessness를 추정하지 않고 Runtime의 기존 owner
fence와 관측된 slot identity를 snapshot에 보존한다. Route 동작은 candidate 순번이나
rolling witness 문자열 대신 physical/logical target ID·UID별 weight와 소비되는
completion bound/capacity/batch/wait/deadline 조건으로 식별한다.
complete 표시만으로 동작을 재사용하지 않는다. 현재 snapshot에서 prepare의 Ready
identity/state, route의 실제 분배·대상 조건, stop의 실제 중지·분배 차단, drain의
실제 중지·분배 차단과 outstanding 부재, remove의 실제 부재·drain, power의 실제 Node 상태를
확인하며 효과가 사라졌으면 새로운 실행 가능한 동작을 생성한다. 검증된 run으로 다음
stop→drain→remove 의존성을 진행하며, 실제 target·소비 수치 변경은 다시 적용한다.
새 policy 수치 적용 중에도 이미 traffic이 옮겨진 이전 source의 정리는 굶기지 않는다.
단 gateway routing map 일치만으로 handoff를 인정하지 않는다. 기존 complete route의
Ready·profile·allocation·native policy ACK와 새 target의 typed completion proof 및
actual routing이 모두 일치할 때만 기존 handoff로 stop 의존성을 충족한다. 처음
handoff와 대상 변경은 원래 route 완료를 기다리고 accepted-context stop/drain/removal
검사는 그대로 유지한다.

break-before-make에서 routing/bindings가 잠시 비어도 기존 실행의 frozen
`Operation.Target`이 가진 UID cohort·replica 수·UID별 weight를 전환 의도의 근거로
사용한다. actual 점유·ownership·Ready는 계속 현재 snapshot에서만 가져온다.
Keep는 미완료 goal을 지우지 않고 남은 DAG를 진행하며 다른 일반 후보도 계속 평가한다.
관측된 `TargetID`가 같은 실제 physical slot은 중복 prepare하지 않는다. 새 완료 proof와
소비 수치는 현재 capture를 사용하고, 원래 실행의 frozen target을 덮어쓰거나 사라진
proof를 복원하지 않는다.

global route는 같은 policy ConfigMap/ACK 경로를 쓰므로 기존 running run을 통해
직렬화한다. source release는 이전 global route 완료를 기다리되 독립 Node의 합법적
준비를 전역 lock으로 막지 않는다. 진행 중 route는 자신의 frozen target으로 표시하며
관측하지 못한 native ACK/남은 시간을 새 goal의 즉시 적용으로 replay하지 않고 Pending으로
남긴다. running/native noncancelable 작업을 강제로 취소하지 않는다.
자율 제어 대상이 아닌 Workload의 `staticRouting`도 기존 running route가 있으면 덮어쓰지 않고
같은 관측 기록에 sourceConfigRV·대기 이유·actual readback을 남긴다.
route의 ConfigMap·annotation 저장은 desired 진행으로, `Actual.Native`에는 정확한
native ACK만 남긴다. 뒤 인스턴스의 적용이 실패하면 이미 받은 ACK와 실제 routing을
보존해 `partial`로 기록하며, 미확인 인스턴스를 적용 완료로 채우지 않는다.

동일 comparison horizon에서 모든 POOL Node의 baseline을 한 번 적분한다.
active/inactive/temporary Node를 포함한다. 원자 동작 구간의 wall joules는 그 구간의
baseline 소비를 **대체**하고, 실제 serving execution의 incremental energy만 더한다.
전환 eta를 사용할 때에는 원래 target baseline subtraction 의미를 맞춘다.
서로 독립된 meter의 Node만 합산한다. 시간 단위의 readiness와 joules를 구별한다.

`settings.algorithm`은 필수이며 `fixed-horizon`과 `projected-work-history`만 허용한다.
전자는 기존 비교 기준으로, 실제 SLO-feasible 후보 중 prospective total energy를 최소화한다.
후자는 같은 실제 SLO/resource/evidence/progress 평가를 통과한 후보의 energy 선택만 바꾼다.
모든 후보가 infeasible한 과부하는 기존 worst normalized SLO 초과→energy 순서를 유지한다.
근거가 부족한 판단은 Pending/reason으로 남긴다.
표본 0의 percentile은 값 없음이다. keep/target/rejected 후보·예상치·설정·근거를
원시 decision record에 남긴다. 실제 요청 완료와 whole-POOL 전력 기록을 같은
관측 구간·UID·decision에 연결하여 분석자가 예측과 실측 차이를 계산한다.

`projected-work-history`는 첫 실제 logical 구성의 work를 0으로 시작한다. 이후 첫 control
tick에서 완성된 observation window가 있으면 cursor 이후 `[Through, ObservationEnd)`를
한 번 소비한다. 지연된 tick도 전체 차이를 읽으며 rolling-H를 다시 누적하지 않는다.
추가 관측 기록 읽기는 authoritative Observer refresh와 최종 `SchedulerInputs(time.Now)` 이전에 끝낸다. interval과 선택 receipt/
definition dependency의 불완전 근거는 cursor를 진행시키지 않는다. 관련 없는 오래된 incomplete
metadata는 제외하되 실제 겹침·필수 dependency·불명 prefix를 무시하지 않는다.
선택 receipt의 validated/accepted 근거나 terminal outcome이 아직 없으면 history만 Pending으로
유지하고 cursor를 멈춘다. 이후 durable classification은 최초 receipt 구간에 합쳐 한 번 소비한다.

같은 replay/planner로 `K_k(q,s)`를 계산한다: hypothetical settled `q`에서 measured directed
DAG를 거쳐 `s`로 가는 동안과 이후 Serving의 전체 POOL energy를 같은 interval에 한 번 집계한다.
diagonal에는 전환이 없다. 완료하지 못한 전환·누락 primitive는 해당 edge가 unevaluable이다.
`W_k(s)=min_q(W_{k-1}(q)+K_k(q,s))`이고 공통 최솟값만 빼서 normalize한다.
실제 feasible 후보에서는 `W(s)+actual prospective joules`를 최소화하며 동률은 Keep,
낮은 prospective energy, logical key 순이다. history가 Pending이면 feasible Keep를 유지하고,
실제 SLO 대응이 필요하면 기존 선택을 `ActualSLOSafetyResponse`로 구분한다.

매 projection은 counterfactual queue를 비우므로 실제 queue 경로의 offline optimum이나
고전 WFA 경쟁비가 아니다. actual outstanding/진행 중 action/frozen route target은 기존 실행
평가와 DAG에서만 유지한다. raw whole-wall operation 비용을 full service 비용에 다시 더하지 않는다.
logical key는 물리 Pod/container·template 이름 대신 measured 조건과 replica별 전체 UID routing
vector의 multiset이다. 기존 finite history는 bounded search에서 빠졌다는 이유로 버리지 않는다.
retained universe가 `MaxCandidates`를 넘으면 명시적으로 Pending이며 임의 reset을 하지 않는다.
process 재시작 또는 settings 변경은 이유가 기록된 새 비교로 시작한다.

algorithm은 `decision.settings.algorithm`에서 제공하며 fixed-horizon에는 `policy`를 만들지 않는다.
projected decision/관측 기록의 `policy`는 startedAt/startReason, currentKey,
`candidates`와 같은 순서의 candidateKeys, history.through/values, intervalFrom/To,
kernels의 energy/completion/references/reasons,
pendingReason, selection과 projection assumption을 제공한다. 이것은 live automatic 실행이나
FULL 통과 증거가 아니다. 자율 제어는 `settings.autonomousWorkloads`에 나열한 Workload UID에만 적용한다.

## 6. 실제 적용과 관리 연동

`serving/kube` 제어 구현이 measured target별 worker profile bundle과 작은 mutable policy config를
제공한다. 큰 raw 분포는 immutable store/volume에서 startup에 한 번 로드하고
request마다 복사하거나 ConfigMap에 반복 전송하는 대신 공유한다. actual native
metadata/progress에서 profile·policy 적용과 readiness를 확인한다.

전환 기록은 desired/start/progress/complete/failed/partial과 prerequisite를 가진다.
실제 준비 Pod, NodeActuation, routing 적용, drain·remove 완료를 확인하여
다음 동작을 시작한다. resource 변경 후보는 같은 기존 원자 동작을 조합하되 실제
동시 allocation 예산에 따라 prepare-first 또는 drain/remove-first DAG를 사용한다.
기존 독립 writer를 중복 생성하는 대신 NodeActuation을 생성해 sole host actuator를 사용한다.

부하·Workload 관리 상태가 바뀌면 같은 snapshot/candidate 로직으로 재판단한다.
이미 수행된 결과는 실제 현재 상태이며, 진행 중 동일 동작은 재사용한다. cancel은
아직 시작 전/중단 가능한 동작에 적용하고 physical effect가 발생한 뒤에는 관측된
상태에서 다시 선택한다. 실패 Node·instance는 route에서 격리하고 실제 recovery
proof에 따라 재사용한다. 기존 관리 admission gate·accepted definition·공유 UID
보호·actual drain과 동일한 Workload 관리·요청 처리 구현을 사용한다.

평가 중 완료된 동작의 동일 ID·동일 계약은 live run의 terminal 결과가 stale snapshot의
running/cancel보다 우선한다. physical identity로 갱신된 목표에서 같은 prepare를 다시
구성·실행하지 않는다. 실제 effect가 소실되면 기존 reconciliation이 기록을 퇴역시키고
새 판단의 새 action identity로 준비하므로 logical slot을 영구 차단하는 규칙은 아니다.

### 초기화와 판단 발행 경계

시작 시 profile catalog와 scheduler 설정을 Observer에 제공하지만 생성자는 routing을
freeze하지 않는다. 첫 `observer.Refresh`·`manager.Initialize`가 성공하여 ready instance와
Workload 정의를 복원한 뒤 `settings.autonomousWorkloads`가 비어 있지 않을 때
`EnableSchedulerRouting`으로 실제 분포를 freeze한다. 첫 Tick·HTTPS serving보다 앞선
경계이며 live 설정에서 첫 자율 Workload가 추가될 때도 같은 freeze를 적용한다.

ChillServer의 기존 `--request-timeout`(기본 30초)은 한 control 호출의 refresh와 CPU 후보 평가·
interval projection에 같은 `callCtx`로 적용된다. replay/event·후보·방향별 kernel 반복은
취소를 확인하고 정상 Feasible/Pending 대신 context 오류로 반환한다. `o.lock`과 `tickMu`는
기존대로 snapshot 평가·설정 수락·publication을 직렬화한다. CPU 계산 도중 확인한 취소는
snapshot/decision 발행 전에 오류로 반환한다. publication/관측 기록 직전과 `OnDecision` 이후에도
취소를 확인하며, callback 중/직후 취소는 이미 발행·기록한 유효 판단을 보존하되 context 오류를
반환하고 history를 진행하거나 새 physical action을 시작하지 않는다. 이미 시작한 실제 동작을
소급해 취소한 것으로 바꾸지 않는다. 호출이 잠금을 반환하면 다음 full refresh가 canonical
settings와 나머지 구성을 함께 수락하며, 이전 계산을 비동기로 재사용하지 않는다.

## 7. 설정·권한·관측 API

`/api/v1/scheduler`에서 settings/current snapshot/decision/target/actions와 적용
상태를 제공한다. settings 변경은 기존 TokenReview·SubjectAccessReview 경계를
통과해 scheduler ConfigMap write 권한으로 검증하고 before/after·actor·검증 결과·
영향 범위·실제 적용 시간을 기록한다. observation/control/horizon, resource bounds,
replay/candidate limits·seed, completion margin, 자율 Workload 목록과 비자율 Workload의
`staticRouting` 부분 map을 설정·기록한다. 정적 구성 실험도 같은 관측을 사용한다.

ObservationLog는 request/management와 더불어 decision/settings/action/power 원시 기록을
보존한다. 수신 모집단과 completion-event rate를 구별하고 unknown/missing과 0을
각각 표현한다. node watts·whole energy·incremental energy·단위·meter/구간/coverage를
연결한다. 모든 오류·거절·timeout/cancel·미완료·late와 요청/batch 수를 각각 제공한다.

설정 필드·compact projection·관측 조회의 window/coverage·제외 규칙은
[설정과 관측 API](scheduler-api.md), manual CLI·manifest는
[인스턴스 전달과 수동 실행](scheduler-operations.md)이 원본이다.

## 8. SRS와 구현 확인 경로

| SRS | 구현 경로 | 확인할 소비자 행동 |
|---|---|---|
| SCH-01~02, 19, 28 | candidate selection + decision record + actual CHILL serving instances | keep/change·SLO-first·energy 비교·overload·Pending 구별 |
| SCH-03~08, 29, 31 | Runtime/ObservationLog/window/native state | UID·frozen context·전체 유입·0/unknown·window 경계·관리 중 drain |
| SCH-09~12 | model/runtime/profile catalog + resource snapshot | latest same-condition·같은 실행기·분포/memory·전체 할당·exclusive resources |
| SCH-13~15 | shared selector + per-request replay | 공유 비선점·현재 running residual·E2E percentile·node-wide impact |
| SCH-16~18 | idle/action/transition measurement + whole-pool integration | inactive/temp Node·wall/delta 의미·중복 제외·같은 horizon·실제 readiness |
| SCH-20~23 | Runtime routing + firstfloor actor | ready 즉시 전달·원래 elapsed·UID compatible batch·energy-gain wait·명시적 수명 |
| SCH-24~27, 30 | kube atomic actions + operation ledger | prepare-route-drain-remove·actual effect·partial failure·in-progress reuse·appliedAt |
| SCH-32 | authenticated settings + per-Workload autonomy + frozen record | 권한·변경 결과/영향/적용·동일 조건의 static 비교 |

### Ponytail 검수 반영

- firstfloor 배처의 실제 선택 함수는 actor와 replay의 두 소비자가 재사용한다.
- 공존 조합별 선행 campaign은 제거하고 측정/명시적 additive 예상과 실제 오차를 구분한다.
- 측정은 기존 sampler/store와 원자 동작을 호출하는 command로 구성한다. 별도 측정 CRD·범용 campaign/recovery engine은 추가하지 않는다.
- mutable policy는 작게 유지하고 raw profile은 startup에 로드하여 공유한다. 입력별 profile 복사와 별도 broker/DB는 추가하지 않는다.

## 9. 실행 증거와 현재 수용 범위

2026-10-08 local gate·ORT CPU smoke와 2026-10-09 배포 결과는
[실행·배포 이력](scheduler-execution-history.md)에 날짜별로 보존한다.
그때의 synthetic·fixture·wall-power 미측정·MissingEvidence 결과는 현재 검증 상태나
다음 실행 지시가 아니다. 최신 결과는 [실클러스터 검증 현황](../../plans/2026-10-13-scheduler-live-evidence-campaign.md)을 따른다.

기능 연결 검증과 연구적 SLO·에너지 개선 입증을 구분한다. 배포 버전·현재 차단·
다음 의존성은 라이브 증거 진입 문서 한 곳에서 관리하며, 여기서 전체 FULL 통과를 선언하지 않는다.

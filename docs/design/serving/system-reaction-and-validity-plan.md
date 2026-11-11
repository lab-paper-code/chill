# 측정 불능과 느린 반응의 시스템 원인 및 수정 계획

기준 소스: `33e69155`. 근거 루트: `Q7=ops/pools-deploy/deployB/control-377-control-latency-20261111`(Q6·Q5·Q4는 control376·375·374). 2026-11-11 06:0x UTC에 읽기 전용 `kubectl get`으로 cluster 상태를 다시 확인했다.

## 1. 목적과 현재 판정

CHILL 저장소의 목적은 `docs/spec/`의 측정표를 채우는 것이다. CHILL 시스템의 목적은 허용 자원 안에서 서비스별 설정 분위수 E2E SLO를 충족하는 배치를 고르고, 충족 후보 중 에너지를 최소화하며, 판단과 적용을 10초 제어 주기 안에 끝내는 것이다.

control374~377의 공식 측정은 모두 `INVALID/GeneratorInvalid` 또는 `NOT_EVALUATED/ObservationUnavailable`이다. Core를 고쳐도 측정표는 한 칸도 채워지지 않았다.

control377에서 IC SLO 실패는 거의 전부 부하 시작 후 30~50초 안에 몰렸고, 60초 이후 실패는 1건/0건이다. 배치 판단은 첫 부하 decision에서 이미 맞았다(genesis2 878 / JetsonO 122). 반응 지연 중 가장 큰 단일 항목은 매 refresh마다 6초씩 걸리는 Pod List였다.

## 2. 문제 정의

| ID | 시스템 차원 문제 | 목적에 주는 영향 | 우선순위 |
|---|---|---|---|
| P1 | 실험 호스트 genesis1의 DiskPressure가 cluster를 망가뜨리고 있다 | 측정 무효(gap), 반응 지연, edge 제어 정지의 공통 원인 | 1 |
| P2 | CHILL의 제어 시간이 클러스터에 쌓인 무관한 쓰레기 Pod 수에 비례한다 | Tick이 10초를 넘고 route가 느리다. SLO를 놓친다 | 2 |
| P3 | harness가 workload별 부하를 직렬로 release한다 | 모든 Run이 `GeneratorInvalid`가 된다 | 2 |
| P4 | action 관측 기록이 실행 객체를 그대로 저장한다 | 제어 경로와 조회 경로가 CPU·디스크를 놓고 경쟁한다 | 3 |
| P5 | 정책 비교 stage의 시작 상태가 서로 다르다 | 정책 비교가 시작 상태에 교란된다 | 사용자 결정 |
| P6 | Dashboard 라이브 갱신이 계약 오류로 멈춘다 | 운영 관찰이 중단된다. 측정표와는 무관하다 | 4 |

## 3. 근원 원인

### 3.1 P1 genesis1 DiskPressure가 세 가지 실패를 동시에 만든다 (확정)

genesis1 한 대가 control plane, CHILL 서버, KubeEdge `cloudcore`를 맡고, 동시에 실험 원문·worktree·이미지 빌드도 저장한다. kubelet 퇴거 임계값은 295,097,644,084 B(274.8 GiB)이다. 현재 `/`의 가용 공간은 303 GiB이므로 여유는 약 28 GiB다. `chill-assets`가 145G, worktree가 21G를 차지한다.

1. **cloudcore 거절 폭주:** `kubeedge/cloudcore-58b8b98d88` ReplicaSet의 Pod가 2026-11-08 20:30부터 11-11 01:56까지 genesis1에서 `Pod was rejected: The node had condition: [DiskPressure]`로 **12,344번** Failed가 됐다. 현재 cluster 전체 Pod는 12,728개이고 그중 Failed가 12,445개다. Kubernetes의 terminated Pod GC는 기본 임계값(12,500개)을 넘어야 동작하므로 이 Pod들은 지워지지 않고 남는다.
2. **CHILL 서버 퇴거:** 서버 Pod `chill-serving-entrypoint-6b64bf88b4-lfzgh`(11-10 17:36 생성)와 `-556f49d48c-2q4z2`(18:13:57 생성)가 `ephemeral-storage` 부족으로 퇴거됐다(`Q7/post-load/pods.json`). Q7의 열린 gap은 모두 `Boot=2026-11-10T18:14:07`의 `UnknownExecutorInfluence`다. 이 `Boot`는 다음 프로세스가 읽은 **이전 run의 시작 시각**이다(`observation_log_run.go:39-48`). 즉 18:14:07에 시작한 `-556f49d48c` 프로세스의 종료 tail이 unclean으로 판정됐다. 그 원인이 이 퇴거라는 연결은 시각상 일치한다 [INFERENCE: unclean marker와 퇴거를 직접 연결한 기록은 없다].
3. **edge Pod 종료 정지 (확정):** orangepi edgecore 로그(KST)에 따르면 cloud 연결이 01:50:17 UTC에 끊겼다(`websocket: close 1006`, "reconnect after 30s"). CHILL은 21초 뒤인 01:50:38에 orangepi에 executor Pod `serving-81a9e87d4ef4e0960a01`(`546fb520`)을 만들었다. Node 상태는 heartbeat 유예 때문에 아직 Ready였다. edge는 이 Pod를 받지 못했다. orangepi에는 sandbox가 없고(`crictl pods`), edgecore가 이 Pod를 처음 언급한 것은 재연결 뒤인 01:57:13의 executor Service(Endpoints) 한 건뿐이다. 그 사이 01:55:56에 Node가 NotReady가 되어 taint manager가 01:56:26에 Pod를 삭제했다. 재연결 뒤 edge는 삭제 중인 Pod를 만들지도, 상태를 보고하지도 않는다. 그래서 Pod는 phase `Pending`, 컨테이너 상태 없음으로 CHILL finalizer에 걸려 남는다. `PodTerminated`(`native_source.go:109-123`)는 terminal phase를 요구하므로 gap은 닫히지 않는다. 이 상태는 같은 Node가 정상이어도 영원히 바뀌지 않는다. 현행 계약에서 이 gap 하나가 모든 workload의 D1을 `NOT_EVALUATED`로 만든다(`hack/evaluation/raw.py:53-58`).

   이 Pod는 executor를 한 번도 실행하지 않았다는 사실을 Node 쪽에서 확인할 수 있다(sandbox 없음). 하지만 현행 계약에는 "한 번도 시작하지 않음"을 종료 증거로 받아들이는 경로가 없다. 해법은 둘 중 하나다. (a) 운영: edge가 terminal phase를 보고하게 한다. kubelet 표준 동작은 삭제된 미시작 Pod를 Failed로 끝내지만 edged는 받지 않은 Pod에 대해 그렇게 하지 않는다. (b) 계약: 컨테이너 ID가 한 번도 없었던 Pod는 실행 영향이 없으므로 gap을 만들지 않거나, 확인 즉시 닫는다. (b)는 관측 계약 변경이라 사용자 결정 사항이다.

관측 계약(물리 종료로만 gap을 닫는다)은 의도대로 동작했다. Q7의 genesis2 gap은 04:35:21에 실제로 닫혔다. 결함은 운영 환경에 있다.

### 3.2 P2 OnCluster 관측이 매 refresh마다 cluster 전체를 다시 받는다 (확정, 회귀)

- `observer.go:265-272`: 기본 경로는 watch가 살아 있으면 nonterminal selector로 List한다. 이 최적화는 `3e391fc8`("exclude unrelated terminal pods from steady discovery")에서 들어왔다. 이후 `c355e3cd`(SCH-69 cluster 사실 기록)가 조건에 `&& o.config.OnCluster == nil`을 추가했다. 그 결과 OnCluster 구성인 실배포에서는 매 refresh마다 terminal을 포함한 `NamespaceAll` 전체를 받는다.
- 지금 그 전체 목록은 **12,728개 Pod, JSON 172 MB**다. `kubectl`로 읽는 데 35초가 걸렸다. CHILL은 같은 목록을 매 refresh마다 5.5~7.3초에 받는다(부하와 무관하게 04:26부터 종료까지 일정, `refreshPodsNanos`). 프로세스 CPU의 27~35%가 Kubernetes JSON decode이고, GC 사용률은 약 17%다.
- 받은 목록 중 terminal Pod는 곧바로 버려진다. occupancy는 terminal을 제거하고(`scheduler_application_capture.go:73-78`), cluster 기록은 바뀌지 않은 행을 `reflect.DeepEqual`로 건너뛴다(`observer_cluster.go:110`). terminal 전이와 삭제는 같은 범위의 watch가 전달한다(`observer.go:317,1039`). watch 채널이 끝나면 `podWatch=nil`이 되고(`observer.go:1028-1036`), 다음 refresh가 전체 baseline을 다시 받는다. 따라서 watch가 정상이면 매번 전체를 다시 받아도 새로 얻는 사실이 없다. 예외는 하나다. `OnCluster` callback이 실패하면 `clusterPods`를 갱신하지 않고 다음 baseline에서 재시도한다(`observer_cluster.go:113-124`). 지금은 이 재시도가 매 refresh의 전체 List에 기대고 있다.
- 이 refresh는 `refreshMu`와 `o.mu`를 잡은 채 실행된다(`observer.go:223-231`). 2초 source loop(`cmd/chill-server/server.go:404-409`)가 매번 이 refresh를 호출하므로 refresh는 사실상 쉬지 않고 이어진다. Tick capture, Continue capture, route의 ACK 뒤 refresh(`operations_routing.go:79`)가 모두 그 뒤에 줄을 선다. excess route에서 마지막 정책 적용부터 완료까지 13.0초가 걸린 것은 대기 한 번과 refresh 한 번을 합한 값과 맞는다(내부 시각은 기록되지 않음).

반응 경로 분해(`Q7/native-control-pages/003·015`, `reaction-timing.json`):

| 구간(초) | excess | workload |
|---|---:|---:|
| 부하 시작 → 부하를 반영한 Tick 시작 | 4.0 | 8.6 |
| capture (Pod List 5.6~6.5 포함) | 5.9 | 6.5 |
| 평가·기록·launch | 7.0 | 7.0 |
| prepare | — (이미 Ready인 executor 사용) | 14.1 |
| prepare 완료 → route 시작 | — | 6.3 (진행 중 Tick 평가를 기다림) |
| route | 16.2 | 6.7 |
| 합계 | 33.0 | 49.7 |

**시스템 수준 해석:** CHILL의 제어 시간이 자기와 무관한 cluster 쓰레기에 비례한다. P1이 쓰레기를 만들었고, `c355e3cd`의 조건 하나가 그 쓰레기를 매 refresh마다 제어 경로로 끌어들였다. planner·SLO 판단·routing 의미는 원인이 아니다.

### 3.3 P3 시작 skew는 harness의 직렬 release 결함이다 (확정)

- `hack/evaluation/conductor.py:627-628`은 모든 producer가 ready가 된 뒤 Pod마다 `kubectl exec ... touch /tmp/evaluation-start`를 순서대로 실행한다. 각 producer는 파일을 보는 즉시 시작한다(`producer.py:74-84`).
- Q7 excess의 시작 시각은 IC 49.866 → PLC 50.414 → Smol 51.190이다. 합계 1.324초가 Run 설정 `maxStartSkewNanos` 1초를 넘었다. workload는 1.362초다.
- 세 producer는 모두 genesis1에서 실행됐다(`producer-*.json`). 이미지와 dataset 준비는 barrier 앞에서 이미 끝났다.

### 3.4 P4 action 기록에 저장 투영이 빠졌다 (확정)

- decision은 이미 `profileID`·catalog 참조로 투영해 저장한다(`observation_log_scheduler.go:154-215`). action은 `Operation`·`OperationResult`를 그대로 marshal한 RawMessage로 저장한다(`scheduler_service.go:285-306`). action 하나가 시작과 종료, 그리고 expected와 actual.operation으로 Operation 본문을 최대 4번 담는다.
- `route-operation-full/4.json`은 4.9 MB이고, 그중 `serviceSamples`가 96.5%다. scheduler JSONL은 시간당 1.15 GB다.
- heap의 `CloneServiceSamples` 약 450 MB는 catalog 복사 경로라서 이 단위와 별개다. 이 단위는 action 직렬화·저장·조회 비용만 줄인다.

### 3.5 P5 시작 상태가 선언되지 않는다 (확정)

- 명세 `docs/spec/README.md:206-216`은 실행 정의에 시작 상태를 넣으라고 요구한다. 시작 상태는 앞 단계 결과를 참조해도 된다(`experiments/SRS.md:32,37`). 설계 W1(`measurement-api.md:122-128`)은 start state를 삭제했다.
- Q7에서 excess는 Ready genesis2 GPU를 가진 채 시작했고, workload는 GPU를 새로 prepare해야 했다. 따라서 "excess 충족, workload 미충족"을 정책 차이로 귀속할 수 없다.

### 3.6 P6 Dashboard (부분 확정)

- 확정: 계약 오류 처리에서 `WebSocket.close(1002)`를 호출한다(`chill-dashboard/lib/live.ts:386-402`). 브라우저는 1000과 3000~4999만 허용하므로 `InvalidAccessError`가 난다. 같은 처리가 `blocked=true`로 자동 재접속도 막는다.
- 미확정: 실제로 거부된 프레임. 저장된 hello는 현행 계약을 만족한다. 현행 서버 producer와 TS parser의 모든 프레임 필드·타입·nullability를 대조했지만, 실제 producer 경로에서 거부를 만드는 불일치는 없었다. Q5의 dashboard 이미지(`036e00a`)와 서버(`4b2a25f1`)는 둘 다 마지막 live 계약 변경 `0321dae8`(queue gauge)을 포함하므로 버전 불일치도 배제된다. 코드상 남는 가능성은 이론적인 것뿐이다(nil slice를 가리키는 delta 포인터, uint64 counter 2^53 초과, `ExpectedSource.Reference` 비객체). 따라서 다음 발생 때 거부 프레임과 사유를 남기는 것이 원인을 확정하는 유일한 경로다.
- 504는 observation 조회가 BFF의 10초 한도를 넘어 생긴다. writer barrier와 JSONL 스캔을 거치므로 P4와 연결된다.

## 4. 수정 계획

원칙: 원인이 있는 층에서 고친다. SLO·profile·예산·판정 설정은 바꾸지 않는다. 새 knob이나 gate를 두지 않는다.

| 순위 | 단위 | 층·담당 | 변경 | 완료 기준 |
|---|---|---|---|---|
| 1 | E1 DiskPressure 제거 | 운영 (main, 사용자 승인) | genesis1 `/`의 여유를 퇴거 임계값보다 충분히 확보한다. 지난 campaign 원문은 외부에 보관한 뒤 정리하고, worktree·빌드 캐시도 정리한다. 일회성 정리로 끝내지 않고, 이후 원문을 genesis1 밖에 보관하는 절차를 둔다 | 가용 ≫ 274.8 GiB. Run 중 퇴거·DiskPressure 0 |
| 2 | E2 쓰레기 Pod 정리 | 운영 (main, 사용자 승인) | `kubeedge` namespace의 Failed `cloudcore` Pod 12,344개를 삭제한다. CHILL이 아닌 namespace이므로 사용자 승인이 필요하다 | cluster Pod ≈ 300개 |
| 3 | G1 열린 gap 해소 | 운영 (main) | 측정 전에 현재 열린 gap의 정확한 Pod/container 집합을 대상으로 처리한다. Running executor는 정상 경로로 교대한다. orangepi `546fb520`은 edge가 왜 terminal phase를 보고하지 않는지 확인하고, 실제 terminal 증거가 CHILL에 전달되게 한다. 원인 확인만으로는 완료가 아니다. CHILL finalizer를 강제로 제거하는 것은 종료 증명이 아니다 | gap이 실제로 닫히고, 측정 시작 시점 open gap 0 |
| 4 | O1 nonterminal steady List 복원 | Core 관측 (advisor-2) | `observer.go:266`의 `&& o.config.OnCluster == nil`을 지운다. watch가 살아 있는 동안은 OnCluster도 nonterminal만 List한다. 최초와 watch 복구 때의 전체 baseline은 그대로 유지한다. 지금처럼 fresh 직접 List이므로 occupancy·예약 반환·probe·ACK 뒤 관측 계약은 바뀌지 않는다. `OnCluster` callback 실패의 다음 baseline 재시도는 nonterminal Pod와 watch 복구 때만 남는다. 실배포 기록 오류는 sticky라 서버 재시작 전에는 어차피 복구되지 않으므로(`observation_log.go:377-383`) 실제로 잃는 재시도는 없다 | 로컬 회귀: OnCluster에서 최초·재연결은 전체 baseline, steady는 selector 적용, watch로 들어온 terminal 전이 기록. 실측: refreshPods < 1초 |
| 5 | H1 공통 시작 시각 | harness (advisor-2) | 모든 producer가 ready가 되면 conductor가 공통 미래 UTC 시작 시각 T 하나를 정해 release 파일에 쓴다. producer는 T까지 기다린 뒤 공유 clock으로 시작하고, 실제 시작 `at`·`monotonicNS`를 기록한다(T를 그대로 복사하지 않는다). release 쓰기가 T보다 늦게 끝나면 `BarrierTimeout`으로 실패한다. 기존 barrier 만료 규칙과 `maxStartSkewNanos`는 그대로 둔다 | producer 회귀: 직렬 release 지연과 무관하게 시작 시각이 T에 정렬됨 |
| 6 | S1 action 저장 투영 | Core 기록 (advisor-2) | RawMessage를 만들기 **전에** decision과 같은 profile·catalog 참조 투영을 적용하고, 읽을 때 복원한다. 서로 다른 expected·admitted Operation은 합치지 않는다. 저장 계약 변경이므로 campaign 밖에서 한 번에 바꾸고, 남는 객체는 지운다(ADR 0019) | action 저장 크기 수백 KB 이하, 조회 복원 회귀 통과 |
| 7 | D1 close code | Dashboard (advisor-2) | 계약 오류 종료에 application close code(4000번대)를 쓴다. parser 거부 사유(프레임 종류, 필드)를 화면 상태에 남긴다. 이 변경만으로 라이브 갱신이 복구됐다고 보지 않는다 | `InvalidAccessError` 0, 다음 발생 때 거부 사유 확보 |

E1~G1이 없으면 Core를 고쳐도 다음 Run은 `NOT_EVALUATED`다. main은 다음 Run 전에 E1~G1을 수행한다. O1·H1은 한 배포에 함께 넣는다. O1은 E2와 독립적으로 필요하다. E2만 하면 다음 쓰레기가 쌓일 때 같은 지연이 돌아온다.

### 보류하는 것과 근거

- **informer cache:** O1로 steady List가 nonterminal 약 300개가 된다. fresh-read 계약을 바꾸지 않고 비용을 없애므로 cache를 도입할 이유가 없다.
- **completion이 Tick 평가를 기다리는 문제(최대 약 7초), ACK 뒤 refresh 제거:** O1 뒤 실측으로 다시 판단한다.
- **gap을 `clearAt`에서 닫는 계약 변경:** 서버 퇴거와 edge 정지가 없어지면 필요하지 않다.
- **catalog profile 복사 heap(약 450 MB):** 원인이 다른 별개 항목이다.

## 5. 사용자 결정이 필요한 항목

1. **E1·E2 정리 대상 승인:** 무엇을 지울지(지난 campaign 원문, 다른 namespace의 Failed Pod 12,344개)는 사용자가 승인한다.
2. **P5 시작 상태:** 아래 둘 중 하나를 고른다.
   - (a) stage마다 시작 배치를 선언하고, 공개 인터페이스로 재구성·확인한다.
   - (b) 시작 상태를 "앞 stage 결과"로 선언하고 정책 우열은 주장하지 않는다.

   정책 비교 표를 채우려면 (a)가 필요하다.
3. **반응 시간 목표:** 시스템 목표는 판단과 적용을 10초 안에 끝내는 것이다. O1은 관측 병목을 고칠 뿐 이 목표를 완료하지 않는다. O1 뒤 예상은 Tick 약 7~8초, 이미 Ready인 공급을 쓰는 반응 약 15초, 새 prepare가 필요한 반응 약 30~40초다[추정]. 현재 경로에서는 평가 7초와 prepare 14초가 남는다. 이를 줄일지, SLO 실패 예산 안에서 미리 대비할지(헤드룸, 준비 재고)는 별도로 결정한다.

## 6. 검증

- 로컬: O1 observer 회귀, H1 producer 회귀, S1 저장·복원 회귀, D1 단위 확인.
- 독립 리뷰: 계획 1회(반영 완료). 코드는 정확성과 메모리·연산을 따로 1회씩 리뷰한다.
- gate: `make lint`, `make test`, `make arch-check`, `make gopls-check`.
- main 실측(같은 E5):
  - 측정 유효성: start skew ≤ 1초, open gap 0, 공식 판정 VALID, 퇴거 0.
  - 제어 시간: refreshPods, control round, Tick, decision → 실효 적용, 부하 시작 → 실효 공급을 각각 판정한다.
  - SLO: 구간별 IC 실패.

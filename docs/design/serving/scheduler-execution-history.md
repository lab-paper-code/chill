# Scheduler 실행·배포 이력

이 문서는 날짜별 historical checkpoint다. 명령·image·Pod·상태는 해당 실행 때의
관측이며 현재 배포나 다음 실행 지시가 아니다. 현재 판단·제어 설계는
[스케줄러 구현 설계](scheduler-implementation.md), 최신 live evidence와 수용 범위는
[라이브 증거 캠페인](../../plans/2026-10-13-scheduler-live-evidence-campaign.md)이 원본이다.
후속 검증으로 해소된 과거 한계를 현재 blocker로 재사용하지 않는다.

## 이전 Serving checkpoint

local isolated kind와 verified HTTPS로 등록, 조회, fractional SLO 수정, stale `If-Match`,
공유 모델 변경 barrier, 참조 삭제 보호와 실제 Workload 삭제 및 새 UID 재등록을 확인했다.
private RWO journal과 MinIO immutable window, TLS/bearer/named tensor loadgen도 실행했으며
근거 없는 admission은 `503 MissingEvidence`로 닫혔다.

이전 공식 Triton native runner에서 다중 named numeric/bool tensor 결과, 같은 priority의 서로 다른
Workload UID batch guard, 여러 실현 batch 크기, native queue timeout과 `REQUEST_END`를 확인했다.
matching ACTIVE/IDLE runner의 fixed batch 실행, warmup 제외, model resident 및 실제
cgroup memory peak와 root RAPL 읽기도 smoke로 확인했다.

당시 Triton native custom batching guard는 UID를 분리하고 CPU threads와
`instance_group.count: 1`을 frozen 조건에 맞췄다. backend는 한 batch씩 실행했고
adaptive SLO 기반 추가 대기는 그 checkpoint의 구현 범위가 아니었다.
현재 firstfloor의 실측 SLO·에너지 selector와 혼동하지 않는다.

이는 full qualifying power campaign이 아니다. **완전한 native campaign → Ready gateway 추론**,
ARM/RKNN hardware와 live testbed는 검증하지 않았다. 이전 test나 Helm 실행을 최신 refactor의
모든 gate 통과로 설명하지 않는다. 정확한 배포 값과 변경 기록은
[Workload 관리 계약 12장](workload-management.md#12-배포와-구현-검증-경계)을 따른다.

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

## 10. 구현 완료와 실행 증거

2026-10-08 UTC의 최신 소스에서 native 실행·동일 Profiling·후보 replay·
원자 동작/측정·관측/권한 API·Helm·Dashboard를 연결했다. 실제 소스의 소비자
경로와 수용 수준은 [최종 SRS 감사](scheduler-srs-audit.md#5-최종-검증과-판정)에 대응한다.
동료 firstfloor 디렉토리는 보존했고, Git staging·commit은 실행하지 않았다.

### 10.1 실행한 gate

| 명령·surface | 관측 결과 |
|---|---|
| `go test ./internal/firstfloor -run TestNativeLifetimeWitnessKeepsAcceptedDeadlineAcrossPolicyChange` | 변경한 완료 오버헤드와 독립된 원래 accepted deadline 유지 |
| `make lint test` | lint·vet·전체 non-e2e Go·envtest integration 통과 |
| `make manifests generate` | deepcopy·CRD·Helm CRD/RBAC 동기화; envtest에 실제 CRD 설치 성공 |
| runtime suites (`make test`에 포함) | ORT CPU 23, RKNNLite 20, firstfloor 6, artifact tool 6 tests 통과 |
| `make arch-check` | production package 65개가 component 43개에 매핑; dependency 및 negative fixture 통과 |
| `make gopls-check` | pinned Go 1.26.4/gopls 0.23.0, workspace/file errors 0; implementation·definition·reference·framework-derived event gate 통과 |
| `make helm-lint helm-template` | default release `chill`, namespace `chill-system`, 기본 values: resource 29, kubeconform valid 17/skipped 12 |
| `make deployment-graph-check` | default 29, 별도 Profiler profile 42, 추가 resource 13; manifest/resource·operand ref·RBAC gate 통과 |
| 아래 scheduler-owner 명시 렌더 | resource 42, kubeconform valid 30/invalid 0/errors 0/skipped 12 |
| firstfloor/serving-entrypoint Dockerfile의 실제 image build | 두 local image build 통과; registry publish 0건 |
| Dashboard `pnpm lint`, `pnpm typecheck`, `pnpm test`, `pnpm build` | 모두 통과; test 3개, build는 Node 24.21.0 |

architecture/gopls gate에는 임시 경로의 공식 ripgrep 14.1.1 바이너리를 PATH에
추가했다. repository 설정·외부 agent 설정은 변경하지 않았다. deployment SVG는
review view이며 desired manifest/resource 집합과 실제 runtime 상태를 구분한다.

명시 scheduler-owner 렌더는 기본 values에 다음 입력을 적용했다. Profiler render는
별도 profile이며 이 owner render의 자원과 합친 배포 상태로 해석하지 않는다.

```sh
helm template chill-local charts/chill --namespace chill-serving \
  --set evidenceStore.connection.bucket=chill-evidence \
  --set evidenceStore.connection.region=us-east-1 \
  --set servingEntrypoint.enabled=true \
  --set servingEntrypoint.scheduler.configMap=chill-scheduler \
  --set servingEntrypoint.tls.secretName=chill-owner-tls \
  --set servingEntrypoint.scheduler.powerCostSources[0].namespace=chill-measurement \
  --set servingEntrypoint.scheduler.powerCostSources[0].name=measured-power-cost
```

렌더를 `kubeconform -strict -summary -kubernetes-version 1.31.0
-skip CustomResourceDefinition,ChillSystem`에 전달하고 pipeline 실패도 확인했다.
이 입력은 existing ConfigMap·TLS Secret·cost source 참조의 desired-resource 검증이다.

### 10.2 실제 실행 smoke

- 실제 HTTPS entrypoint→SDK→persistent firstfloor→ORT CPU에서 named FP32·UINT64·BOOL 입력을 처리했다. FP32 `[1,2,3,4]`는 `[2,4,6,8]`, UINT64 `18446744073709551615`는 raw JSON에서 정확히 보존했다. 응답의 explicit physical 완료와 원래 request/UID/definition을 확인했다.
- 최종 native serving image에 두 UID의 요청 8개를 동시 전달했다. 모두 실제 추론·finished·완료를 반환했고 accepted/completed/executions 각각 8, 남은 native 요청 0이었다. 이미 소비한 500ms와 0.01ms SLO 목표도 명시적 10초 수명과 독립적으로 처리했다.
- 실제 fixed batch K=1·2·4 실행과 policy cap 1의 적용을 확인했다. immutable maxBatch 4와 실제 policy 적용을 구분했고, 명시적 만료 요청은 native 실행 진입 전에 408로 처리했다.
- 최종 production image의 `measurement.py`는 같은 actor/runner에서 K=4를 1.000254초 측정했다. warmup 3회 제외 후 actual execution 1,682개·input 6,728개, distinct batch ID·client/native/backend/service sample의 상관과 counter를 확인했다.
- 이 측정의 CPU quota는 `100000 100000`, 실제 affinity는 `[0]`, ORT intra/inter thread는 1이었다. memory 60,592,128 bytes는 `cgroup-v1-memory.max_usage_in_bytes:runtime-container-lifetime` 의미로 보존했다. wall-power·energy는 미측정 null이다.
- 실제 측정 종료 smoke에서 동시에 group에 TERM을 보내던 worker/runner 종료 경합을 발견했다. worker에 먼저 종료를 요청하여 owner가 runner를 정리하고 grace timeout에서 group KILL하는 경로로 수정했다. 수정 image의 실제 측정 종료는 exit 0·오류 로그 0건이었다.
- local envtest API server의 실제 TokenReview/SAR와 verified-CA HTTPS를 사용했다. 미인증 추론 401, malformed input 400, authorized 설정 PUT의 전후 정의·validatedAt/appliedAt, 상태와 raw cursor를 확인했다. 접수 전 거절을 포함한 actual load는 received 3·accepted/assigned/completed 1이었다.
- 실제 controller tick은 snapshot·decision을 journal에 보존했다. 미측정 inventory 상태는 `Pending`/`MissingPoolInventory`, action 0건이었다. static control-off에서도 관측과 keep 판단을 유지했다.
- 실제 Next Dashboard에서 인증 후 firstfloor/backend·maxBatchSize/maxQueueSize authoring과 최종 review를 브라우저로 확인했다. 실측 campaign 등록·Pod 준비를 이 화면 확인의 통과 근거로 사용하지 않았다.

### 10.3 수용 경계와 커밋

local service/profile의 Node·image identity는 명시한 fixture다. qualified campaign의
벽전력·물리 Node·frozen image evidence와 구분한다. GPU/RKNN 실제 장비,
독립 wall-meter, host 전력 전환·kubelet readiness를 포함한 full Serving campaign,
hardware SLO/총에너지/공존 예측 오차와 live/kind e2e는 실행하지 않았다.
동료 디렉토리·live cluster·image registry에 변경을 적용하지 않았다.

## 11. 2026-10-09 실클러스터 배포·확인

사용자의 실클러스터 확인 요청에 따라 `kubernetes-admin@kubernetes`의
`chill-system`, Helm release `chill`을 대상으로 실행했다. `fast-deploy`의
기본 reset은 ChillSystem을 삭제하므로 `--skip-deploy`로 현재 소스를 build/push하고,
기존 `helm get values`를 보존한 `helm-release-flow.sh install` upgrade로 적용했다.
image tag는 `scheduler-live-20261009`, Helm revision은 8에서 9로 변경했다.

### 배포 입력과 수정

- `fast-deploy --components all`의 operator·node-discovery·node-actuator·profiler·console과 별도 serving-entrypoint·dashboard를 build/push했다. 기본 `all`의 범위를 실제 script에서 확인했다.
- Docker container builder의 연결 오류와 Docker driver의 multi-platform/HTTP registry 제한을 확인했다. root/owner는 `edge-builder`, 최종 Dashboard는 `chill-builder`로 publish했다. 외부 Docker/agent 설정은 변경하지 않았다.
- 기존 PVC·ChillSystem UID `17722831-d04d-4d8f-9e63-1bc13d5eef07`와 사용자 Model/Workload를 보존했다. 새 저장 계약의 관측 기록은 `/data/scheduler-native-alpha2`를 사용한다.
- 기존 서버의 재시작 306회와 read-only `/tmp`의 evidence candidate 생성 실패를 관측했다. read-only root는 유지하고 Helm에 128Mi `/tmp` emptyDir를 연결했다. 관측 기록 경로는 `observationStorage.directory`로 선택한다.
- `serving-scheduler` ConfigMap은 실제 sample의 controls-off 설정으로 생성했다. inventory·profile·cost·meter를 작성한 측정값으로 대신 채우지 않았다.
- 보존한 live values의 preflight는 resource 70개, kubeconform valid 58/invalid 0/errors 0/skipped 12와 CRD ownership을 통과했다. 실제 upgrade의 `--wait`도 통과했다.

### 관측 결과

| surface | 실제 결과 |
|---|---|
| `ChillSystem/chill` | 동일 UID·generation 1, Ready 및 enabled component conditions True |
| node-discovery / node-actuator | 각각 10/10, 1/1 Ready |
| operator·profiler·console·dashboard·serving-entrypoint | 새 tag, 각각 1/1 available |
| serving-entrypoint | `chill-serving-entrypoint-98bb955b8-gh7qh`, 재시작 0; 실제 imageID `sha256:6502be6e9bad336c9b5bf297a2aa33682d72224768d9938ad43ffa6bb7f761c2` |
| verified-CA HTTPS `/readyz` | 200, `ready:true`; window publication 오류 로그 0건 |
| model·workload·load API | 기존 Dashboard bearer로 200 |
| scheduler settings/state/raw | scoped 임시 verifier의 TokenReview/SAR로 200; controls off, `Pending/MissingPoolInventory`, actions 0, raw decision과 nextCursor 확인 |
| 기존 Dashboard/owner bearer의 scheduler 조회 | non-resource 권한의 403; 임의 권한 확장 없이 구분 |
| isolated native real-artifact Model/Workload | 각각 201, frozen Definition 연결, `Waiting/MissingEvidence` |
| isolated Workload 추론 | malformed 입력 400, 실제 계약에 맞는 FP32 `[3,224,224]` 입력은 503 `MissingEvidence`; 실제 추론 완료로 해석하지 않음 |
| Dashboard | 실제 cluster Service tunnel에서 인증·서비스 목록·native 측정 대기 상태의 visual 확인 |

기존 `dashboard-validation-a/b`는 새 owner에서 `AppliedSnapshotUnavailable`/
`AppliedDefinitionUnavailable` 상태로 관측됐다. 기존 Model의 `onnxruntime/
CPUExecutionProvider` 계약과 이전 applied snapshot을 현재 native 등록 계약에
맞춘 운영 전환이 당시 필요한 상태였다. 해당 사용자 객체의 spec·UID는 유지했으며 상태를
숨기거나 compatibility reader를 추가하지 않았다.
새 관측 기록 경로에는 해당 이전 definition identity가 존재하지 않는다. 실제
`Manager.restoreDefinition`은 `RetainedDefinition(identity)`를 조회하고 확보된
snapshot에만 복구를 허용한다. 관측한 실패는 이 경계에서 발생했으며, 당시 새 운영
정의의 전환 경계는 explicit 관리 요청이었다.

qualified native campaign과 준비된 native Serving Pod는 이 클러스터에 확인되지
않았다. 성공 추론·SLO·총에너지·물리 전환의 종단 통과는 이번 결과에 포함하지 않는다.
동료 firstfloor 디렉토리와 기존 `plan10-serving-jetsono`는 변경하지 않았다.
isolated smoke Model/Workload는 실제 owner DELETE·drain을 통해 정리했고,
임시 verifier SA/RBAC·browser tab·port-forward도 정리했다. 운영 배포와
controls-off ConfigMap, 기존 저장 데이터는 유지한다.

## 2026-10-13 firstfloor 원본 비교와 synthetic 회귀

2026-10-13에 `/home/genesis1/ETRI/donghwan/firstfloor`의 현재 제공 소스와 비교했다.
CHILL 실행기는 단순한 Go module 교체가 아니라 UID/불변 정의별 큐, 여러 typed tensor,
ORT CUDA와 같은 경로의 profiling을 연결한 구현이다. 원본 전체를 덮어쓰지 않는다.
원본의 body 오류 분류를 inference/profile 공통 경계에 반영했다: 요청 취소 중 읽기
실패는 408, byte 한도 초과는 413을 우선하며 JSON 뒤의 읽기 오류 원인도 보존한다.
두 HTTP 경로의 회귀 검사와 실제 TCP body-read timeout의 408/no-execution을 확인했다.

실제 Python echo runner와 HTTP actor에서 세 모드를 확인했다: `drop`은 expired 요청을
실행하지 않고 408로 끝내며, `keep`은 expired 요청을 fresh 요청보다 먼저 처리하고,
`defer`는 fresh 요청을 먼저 처리한 뒤 expired 요청을 완료한다. active 요청은
SLO를 넘겨도 성공 결과를 반환했다. overdue 큐의 C1=10/C2=15에서는 batch 1,
C1=C2=10에서는 batch 2를 선택했고 SDK까지 실제 no-execution 완료 증거를 전달했다.
raw IPC의 UINT64 최대값·BOOL·FP16은 11-byte binary payload로 정확히 왕복했다.
고정-K 측정 조건, replay, CRD enum/default 및 UI의 세 모드 선택도 검증했다.
이는 로컬 synthetic 실행 증거이며 실제 CPU/CUDA/RKNN 측정이나 live-cluster 검증은 아니다.

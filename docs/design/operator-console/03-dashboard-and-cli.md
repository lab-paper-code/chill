# 운영자 콘솔 — 운영 의미와 현재 단면

상태: **active 설계**. 제품 요구사항은 [Dashboard SRS](../../spec/dashboard/SRS.md), 화면·웹 구현은 [Dashboard 설계](../dashboard/README.md), 알고리즘은 [시스템 명세](../../spec/README.md)가 원본이다. 여기서는 CLI 계약과 조회·요청·증거 계보의 구현 경계만 구체화한다.

## 운영 의미와 책임

콘솔은 제어기의 원격 저수준 조작판이 아니라 근거 검증·권한 부여·안전 개입 표면이다. Dashboard와 CLI는 같은 권한·검증·dry-run·비동기 작업 의미를 사용하며 숨은 강제 동작을 두지 않는다. 접수·검증·실행·완료·거부를 구분한다.
설치·업그레이드·제거는 Helm/GitOps의 별도 수명주기다. CHILL 운영자(SLO·정책·전환), 프로파일 관리자(측정·증거·승격/폐기), 관찰자·감사자(조회·내보내기)의 권한과 행위자는 구분하며 한 사람이 겸할 수 있다.
운영자 선언, 관측 사실, 측정·파생 모델, 제어 판단, 실행 결과는 서로 다른 층이다. 각 값의 생성 시각·주체·근거를 보존하고 현재·최적·목표 구성을 섞지 않는다. `Ready`만으로 서비스 정상·SLO 충족을 주장하지 않는다.
원본은 CHILL desired/status/decision, Kubernetes 객체·Event, 메트릭, 불변 증거다. 조회 projection은 이를 결합할 뿐 별도 영속 truth를 만들지 않는다.
정상 선언은 Workload·모델·SLO, 허용 노드·디바이스·모드·프로파일, 여유 용량·중단 예산·유지보수 제외다. 배치·큐 대기·가중치·최적 구성은 계산 결과이며 강제 변경은 권한·사유·영향·만료·감사를 갖춘 예외다.

### 운영 모드·안전 상태의 설계 의도

원본 명세가 확정하지 않은 다음 모드·트리거·중단 규칙은 **제품의 필수 요구사항이 아니다**. 실제 제어 동작과 지원 여부는 명세·API가 결정한다.

| 개념 | 의도 |
|---|---|
| Observe | 계산·설명만 하고 데이터 플레인을 변경하지 않음 |
| Supervised | 사전 승인된 SLO 보호는 자동, 경제적 전환은 승인 |
| Autonomous | 검증된 정책 안에서 자동 방아쇠·방어·전환 |
| Frozen | D1 관측 신선도 한계 초과 시 T2·T3 동결, 노드 로컬 큐 기반 T1 유지 |
| 열 할인 | D2 감지 노드는 실측 throttling 용량으로 평가 |
| override·보류·break-glass | 사유·행위자·영향·만료·감사; T1 emergency stop이나 상향 중단은 보장 상실을 지속 표시 |

### 운영 흐름과 요구사항 추적

중복 규범 목록은 Dashboard SRS를 따른다. 기존 ID는 아래 의도를 식별하며 별도 요구사항 원본을 만들지 않는다.

| Use Case | 운영 목적·개입 경계 | 관련 기존 ID / 권위 요구사항 |
|---|---|---|
| UC-01 전체 상태 판정 | 정상/관찰/즉시 개입을 구분하고 구간·표본·신선도로 근거 탐색 | FR-01, FR-10 / DB-FR-01~03, DB-FR-15, DB-FR-22~23 |
| UC-02 판단 설명·재구성 | 안정적 decision ID로 입력·신선도·프로파일 지문·정책 세대·알고리즘 버전·후보 제외·예상/실제를 연결; 과거 사실은 수정 불가, 구조화 내보내기 | FR-02~04 / DB-FR-16, DB-FR-26~27 |
| UC-03 전환 감독 | 이유·방향·단계·backlog·예상/실제 비용 추적; 설계 의도는 한 건씩 실행·상향 완수·안전한 하향만 중단, 승인·만료 보류 | FR-07~09, SR-01~02 / DB-FR-19~20, DB-OP-02~04 |
| UC-04 관측·열·노드 이상 | 입력 이상·손실 용량을 정상 취급하지 않음; 실패 노드는 라우팅/용량에서 제외, 건강·프로파일 유효성 확인 후 재편입 | SR-03~05 / DB-FR-11, DB-FR-21 |
| UC-05 과부하 관측 | 요청 결과·지연을 부하·자원·제어 시간축으로 해석; 자원 변경은 정책 변경 경로 | FR-10 / DB-FR-03, DB-FR-22~25 |
| UC-06 정책 사전 검증 | 현재 관측 dry-run으로 구성·용량·전력·보장 차이 확인 후 사유와 함께 적용/취소; 비동기 수렴 | FR-06 / DB-FR-11, DB-FR-17, DB-OP-01~04 |
| UC-07 프로파일 승격 | 조합 공백·격리 측정 증거·품질을 검토해 승격/거부/폐기/재측정; 완료·single-Run admission·운영 승격 구분 | FR-05, SR-06 / DB-FR-12~14, DB-FR-34 |

SR-01의 우선순위·전환 단일성은 controller 책임이며 클라이언트가 강제하지 않는다. SR-04~05의 보장 상실·구조 가정 위반은 영향 범위와 함께 표시한다. SR-07의 정상 저수준 직접 지정 금지와 예외 통제는 위 책임 경계를 따른다. FR-09의 공통 권한·작업 상태는 [공통 변경과 오류 처리](../../spec/dashboard/SRS.md#6-공통-변경과-오류-처리)를 따른다.

## Measurement task CLI

The existing `BatchEnergyOptimization` manifest remains the request contract:

```sh
kubectl chill measurements submit -f measurement.yaml -n chill-system
kubectl chill measurements submit -f measurement.yaml -n chill-system --dry-run=server -o json
kubectl chill measurements list -n chill-system
kubectl chill measurements show TASK_NAME -n chill-system -o json
kubectl chill measurements delete TASK_NAME -n chill-system --uid OBJECT_UID --if-match RESOURCE_VERSION -o json
kubectl chill profiles list -n chill-system -o json
kubectl chill profiles adopt TASK_NAME -n chill-system -o json
kubectl chill profiles unadopt TASK_NAME -n chill-system --if-match CONFIG_VERSION -o json
```

Measurements and profiles use the same verified ChillServer HTTPS API as Dashboard.
Submit sends the native manifest to ChillServer, which resolves the production Console
preflight before one native Kubernetes create. Rejected or unavailable preflight
creates nothing. Normal submit returns `202` with `request:"Accepted"` and the native
task immediately; it does not wait for trials or retry the request.
`--dry-run=server` uses `?dryRun=All`: preflight and native server validation still run,
but the response is `200` with `request:"NOT_SUBMITTED"`. It starts no measurement.
List returns native measurement resources in the requested namespace, including pending,
failed and complete tasks; `-A` is not supported. Show preserves the existing Console
result projection through the ChillServer gateway. Its response limit remains 64 MiB plus
4 MiB for the task/status envelope. JSON retains conditions, reasons, result details
and evidence references; table output summarizes progress.

| ChillServer route | Contract |
|---|---|
| `GET /api/v1/namespaces/{namespace}/batch-energy-optimizations` | Native `BatchEnergyOptimizationList`, including UID and resourceVersion |
| `POST /api/v1/namespaces/{namespace}/batch-energy-optimizations` | Native BEO manifest; production preflight then native create; `dryRun=All` validates without persistence |
| `DELETE /api/v1/namespaces/{namespace}/batch-energy-optimizations/{name}?uid={uid}` | Required native resourceVersion in `If-Match`; `202` with `request:"DeletionRequested"`, native task, `evidenceDeleted:true` and exact `{store,key}` locations in `deletedEvidence` |
| `DELETE /api/v1/scheduler/profiles/{namespace}/{name}` | Required scheduler ConfigMap resourceVersion (`configVersion`) in `If-Match`; `ProfileSelectionResult` |

Measurement deletion **irreversibly removes the measurement's owned Evidence Store/S3
raw objects and results**, then requests native measurement object deletion.
Shared/current references must be cleared first; safely unadopt an unused selected
profile before deletion. This is not an atomic transaction across S3 and Kubernetes:
an error may leave some evidence already removed while the native object remains.
The CLI preserves ChillServer's partial-failure reason and exits nonzero; it never
retries automatically or reports preserved evidence. Native finalizers and garbage
collection still govern object lifecycle; `DeletionRequested` does not claim final
disappearance. `measurements delete` requires both `--uid` and `--if-match` from
`measurements list -o json`; it never refreshes or overrides a stale caller version.
`deletedEvidence`/the table's `OWNED_KEYS` identify exact cleanup keys, not a count of
objects that previously existed: already-missing owned keys are idempotently handled.
Only current terminal, source-defined tasks may be deleted: completed measurements
with `MeasurementComplete=True`/`ResultWritten`, a stored result and current observed
generation, or terminal `TransitionFailed`/`RetryLimitExhausted` failures. Pending,
Measuring and recoverable `CurvePlanResolutionFailed` tasks cannot be cancelled by delete.
Global/shared artifacts and images are outside this owned-key cleanup. No S3 caller
credentials or new broad caller Kubernetes grants are required; the caller's authorized
BEO delete covers this operation.

Partial failures exit nonzero and are not automatically retried:
- `503 MeasurementEvidenceDeletionIncomplete`: some exact evidence keys may already
  be gone; the native object remains. Retrying the same UID/resourceVersion is safe.
- `503 MeasurementDeletionIncomplete`: owned-key cleanup finished, but native deletion
  failed, conflicted or could not be confirmed. Query the identity before retrying.
- `503 MeasurementAuditIncomplete`: the mutation may have been accepted, but its final
  audit record could not be confirmed. Inspect current state before another mutation.

Profile adoption/unadoption are separate, explicit ChillServer scheduler requests.
`profiles unadopt` requires `--if-match` from `profiles list -o json`'s `configVersion`.
ChillServer requires all controls OFF in persisted and accepted settings and rejects
active instances or frozen/in-progress transitions. It removes the selection and unused
associated preparation, preserving unrelated configuration, evidence and serving Pods.
`Removed` and already-absent `Unselected` succeed only with `persisted:false` and
`loaded:false`; they do not claim a physical serving instance was stopped.
If the ConfigMap removal commits but catalog refresh fails, `Pending` with
`code:"ProfileUnloadFailed"` retains the actual `loaded` state and exits nonzero.
A pending/rejected adoption also prints ChillServer's reason and exits nonzero.
A persisted adoption is not a physical-readiness claim: loaded state, profile ID and
config version remain separate. HTTP success with a rejected/invalid semantic result
is a command failure, not successful submission or unadoption.
ChillServer commands use direct, verified HTTPS and caller bearer authentication:

```sh
kubectl chill profiles list -n chill-system \
  --serving-url https://CHILL_SERVER_HOST:8080 --serving-ca-file chill-server-ca.pem \
  --token-file caller-token -o json
```

`--serving-url` defaults to `https://SERVICE.NAMESPACE.svc:8080`, with
`--serving-service chill-serving-entrypoint`. Use a reachable HTTPS origin whose
certificate matches its hostname (including for a local tunnel). The ChillServer CA is
independent of the API-server CA; without `--serving-ca-file`, system roots apply.
`--token-file` overrides kubeconfig bearer credentials. API-server client certificates
alone do not authenticate to ChillServer and produce a credential-required error.
The CLI does not forward API-server client certificates, impersonation or auth plugins,
does not disable TLS verification and does not follow redirects with credentials.
A ChillServer HTTP 401 is a command failure, not an empty successful list.

All measurement/profile commands and standalone `preflight -f FILE` accept these ChillServer
connection flags; none accept `--console-service` or mutate Kubernetes directly.
Standalone preflight retains its structured resolving/rejected plan and reason;
`resolves:false` prints that result and exits nonzero without submitting anything.
Only the separate `runs show` command retains its existing Console Service-proxy transport.
Commands honor `--kubeconfig`, `--context`, `-n` and `-o table|json`. Without a namespace
override, submit/preflight use the manifest namespace then the kubeconfig namespace;
other commands use the kubeconfig namespace.

## 첫 조회·요청·관찰 단면

### 목적과 현재 구현

첫 단면은 CRUD 화면이 아니라 **현재 지식 조회 → 단일 ProfilingRun 요청 → 의도 동결·실행 관찰 → 증거·admission 계보 추적**이다. 완성형 Scheduler의 추천·SLO/에너지 판단·정책 dry-run·전환 감독은 선행조건이 아니다.
화면 배치·프런트엔드 기술·별도 API 서버·새 CRD 형태는 이 단면에서 확정하지 않으며, 웹 구현 선택은 Dashboard 설계가 소유한다.

| 영역 | 현재 권위 있는 사실·구현 | 주장하지 않는 사실 |
|---|---|---|
| System | `ChillSystem.spec` 요청과 `status.conditions` availability를 `kubectl chill system`이 분리 조회 | Serving/SLO/에너지 정상, 종합 health score |
| Inventory | `internal/deviceclass`: `DeviceClass.spec + live Nodes` selector projection, Ready tri-state·`Node.spec.unschedulable`·원본 identity | `DeviceClass.status` 멤버/Ready 집계 복제, CHILL eligibility |
| 모델 | `ModelSpec` immutable artifact digest와 execution path 선언 | compatible-class index·운영 승격 profile |
| Run 요청 | immutable `ProfilingRun.spec`, Job-recorded RunConfig | Dashboard·CLI 공통 생성 표면 |
| 실행 결과 | owned Job·실제 Pod/Node·측정창 co-resident·검증된 evidence reference·single-Run admission | batch-energy campaign 조정·운영 profile 승격 |
| 상위 제어 | 이 첫 단면에는 없음 | 전력 곡선·선택 batch·current/optimal/target·decision/transition |

`internal/operatorconsole` Kubernetes query adapter는 `DeviceClassList`, `NodeList`, `ModelSpecList`를 읽는다. selector는 DeviceClass/Node에만 적용하며 모델 선언은 그대로 둔다. 세 LIST의 resourceVersion·조회 완료 시각은 각각 보존하고 원자적 cross-resource snapshot이라고 주장하지 않는다.
`kubectl chill inventory`의 table/JSON은 같은 snapshot으로 class capability·일치 Node·Ready·scheduling-disabled·artifact/runtime 요구사항·원본 resourceVersion을 표시하며 ModelSpec/DeviceClass compatibility를 계산하지 않는다.
`kubectl chill runs`는 kubeconfig 현재 namespace가 기본이며 `-n`/`-A`로 지정한다. namespace로 ChillSystem 소유를 추론하지 않고 Run immutable spec·status·resourceVersion을 보존한다.
Helm의 unbound console viewer ClusterRole은 이 조회 경로의 ChillSystem·DeviceClass·ModelSpec·ProfilingRun·Node get/list와 Console Service proxy만 제공하며 사용자·그룹 binding은 관리자가 소유한다.

### Evidence Passport와 전송 경계

`kubectl chill runs show`는 Kubernetes Service proxy로 in-cluster Console read API를 호출한다. Console은 Run exact-read 후 해당 Job에 동결된 Evidence Store binding으로 final/raw를 읽고 schema 및 Run·Job·Pod·Node 연결을 검증해 bounded Passport를 조회 시점에 만든다.
원시 latency/power 배열·저장소 endpoint·credential은 CLI로 보내지 않는다. WorkloadObserver evidence가 있으면 연속성·관찰 경계·초기/최종 active Pod 수·material event 수·co-resident identity를 투영한다.
frozen workload contract에 따라 동시 CHILL 측정과 측정 중 외부 Pod 변경을 구분하고 안정적으로 공존한 외부 Pod도 숨기지 않는다. admission verdict를 다시 계산하지 않는다.
기본 상태 조회는 kubeconfig·사용자 RBAC로 원본을 직접 읽고, credential·다중 원본 검증이 필요한 Passport만 Console read API를 사용한다. Dashboard도 같은 의미를 소비하며 별도 Evidence Store 경로나 영속 projection을 만들지 않는다.
이 **ProfilingRun 단면**에 아직 Dashboard consumer와 Run 생성 표면은 없다. 현재 read-only CLI System·Inventory·Run 목록·Passport 조회와 위 BEO 측정 요청 CLI는 서로 다른 계약이며 혼동하지 않는다.

### 공통 Run 요청과 상태 관찰의 설계

Dashboard guided form과 CLI가 동일한 `ProfilingRun.spec`을 만들며 Job·Pod·Evidence Store endpoint·런타임 내부 명령을 입력받지 않는다. 제출 전 다음을 보여준다.
- ModelSpec·artifact execution path·DeviceClass·target Node
- batch·warm-up·측정/전력 관찰 구간·CPU request와 제출될 immutable spec
- Kubernetes schema의 즉시 검증과 이후 Profiler의 사실 해석 경계

변경 경로는 **CLI/Dashboard → Kubernetes API ProfilingRun → Profiler controller → owned Job/execution → Run status/evidence**다. API create 성공은 실행 완료가 아니다.
관찰 단계는 **Requested → RunConfig Resolved → Execution → Evidence Verified → Single-Run Admission**이며 권위 있는 status·Job·Pod·evidence reference로 연결한다.
생산자가 없는 단계는 장기 Pending으로 꾸미지 않고 capability 미제공으로 표시한다. 실행 성공·evidence verification·scientific admission을 하나의 성공 배지로 합치지 않으며 단일 Run admission은 운영 승격이 아니다.
모든 결합 조회는 원본 종류·이름·UID/generation·관측 시각으로 추적된다. Unknown·미지원·오류를 정상/실패/무기한 진행으로 오해시키지 않는다.

### 의존성·완료 경계

구현 게이트는 (1) System/Inventory/Run/Passport의 원본·projection·RBAC 보존, (2) RunConfig→Job→실행 identity→불변 evidence verification→single-Run admission 연결, (3) 동일 spec·validation·권한·비동기 의미의 공통 Run 요청, (4) Kubernetes 인증·Console read API를 소비하는 Dashboard 단면 순서다.
의미 단위는 System(조회), Inventory(조회), Profiling Runs(조회·새 요청), Evidence Passport(조회) 네 가지다.
완료 조건은 controller 로그 없이 지원 단계를 구분하고, DeviceClass.status 복제 없이 Node 조회를 재현하며, 두 표면의 동일 요청·validation과 접수/실행/증거/admission 구분, 모든 원본·시각 추적을 제공하는 것이다.
첫 단면은 저수준 Job/Pod/batch/routing/power 편집, UI 자체 compatibility·eligibility·admission 계산, Console 전용 truth DB, campaign orchestration·selected-batch derivation, 정책 dry-run·승인·override·break-glass·transition control을 구현하거나 암시하지 않는다.
상위 producer와 권위 API가 생기면 Evidence Passport 계보 위에 Scientific Readiness Matrix → Decision Microscope → Transition Flight Recorder를 확장한다. 이 경계는 Dashboard SRS 전체 제품 범위를 대체하지 않는다.

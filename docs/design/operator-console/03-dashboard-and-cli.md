# Dashboard와 CLI 운영 능력

## 공통 계약

Dashboard와 CLI는 같은 운영 계약의 두 표현이다. Dashboard는 상황 인지와 관계 탐색에,
CLI는 반복 작업과 자동화에 적합해야 한다. 두 표면은 같은 권한, 입력 검증, dry-run과
작업 상태를 사용하며 한쪽에만 숨은 강제 동작을 두지 않는다.

장시간 작업은 요청 성공과 작업 완료를 구분한다. 두 표면 모두 접수, 검증, 실행,
완료 또는 거부 상태와 종료 사유를 제공해야 한다.

## 전체 상태 판정

운영자는 첫 조회에서 다음 질문에 답할 수 있어야 한다.

1. 워크로드별 지연과 SLO 목표의 관계, 실제 요청 결과는 어떠한가?
2. 판단에 쓰이는 관측과 프로파일은 신뢰할 수 있는가?
3. 현재·최적·목표 구성은 각각 무엇인가?
4. 시스템은 구성을 유지하거나 전환 중인가, 어떤 안전 제한이 적용되는가?
5. 사람의 개입이 필요한가?

이를 위해 워크로드별 SLO 목표, 요청 수·실제 결과와 지연 분포·백분위, 부하·큐,
총 벽전력, 운영 모드, 안전 상태, 데이터 신선도, 구성과 진행 중인 전환의 이유를
함께 제공한다. Kubernetes `Ready`만으로 시스템 정상을 표현하지 않는다.

관측과 집계는 [SLO 관측과 집계](../../spec/README.md#slo-관측과-집계)를 따른다.
각 통계에는 집계 구간과 표본 수를 표시하고, 분석자가 원시 요청 관측을 조회할 수
있게 한다.

## 공통 운영 능력

| 능력 | 제공해야 할 정보와 동작 |
|---|---|
| 상태 파악 | 서비스·SLO·자원·전력·관측 신뢰도와 주의가 필요한 사건의 요약 |
| 판단 설명 | 입력 시각, 프로파일, 후보 비교, 제외 제약, 방아쇠와 안전 상태 |
| 측정·프로파일 관리 | 측정 완료도와 증거 조회, 측정 요청, 검증, 승격·폐기 |
| 정책 변경 | 변경 전 dry-run, 보장과 예상 구성의 차이, 적용 후 비동기 수렴 |
| 전환 감독 | 방향, 이유, 단계, 예상·실제 시간과 비용, 중단 가능성 |
| 안전한 개입 | 승인, 하향 전환 중단, 만료되는 보류·override, 유지보수 제외 |
| 과부하 관측 | 워크로드별 부하·큐, 요청 수·실제 결과, 지연 분포·백분위와 가용 자원 |
| 감사 | 중요 decision의 입력·승인·실행·결과 조회와 구조화된 내보내기 |

각 결과는 값의 출처와 관측 시각을 식별할 수 있어야 한다. 노드 유지보수 제외처럼
용량과 전환 가능성을 바꾸는 동작도 정책 변경과 같은 dry-run 경로를 거친다.

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


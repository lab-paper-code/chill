# EvaluationRun → 대시보드 연결 계약 (대시보드 담당 전달용)

- 상태: 서버·게시 wire 구현 완료(`49f7ee9a`), CR·실행기 구현 중. 대시보드 변경은 이 문서의 계약만 따른다.
- 정본: 요구 [DB-FR-66](../spec/dashboard/SRS.md) · [실험 SRS EXP-14·15](../spec/experiments/SRS.md) ·
  설계 [§8 증거·게시·조회](../design/profiling/experiment-execution.md) · [ADR 0032](../adr/0032-experiment-execution-boundary.md).

## 1. 무엇이 바뀌나 (한 줄)

지금 대시보드는 단계 이름 접미사·요청률 유사성으로 "자동 vs 고정 짝", 정책, SLO를 **추측**한다.
앞으로는 게시물에 실린 **실험 정의(`definition`)를 그대로 읽는다**. 정의가 없는 과거 게시는 비교·정책을 **"미기록"**으로 표시한다.

## 2. 조회 API — 인증 변화 없음

| 경로 | 변화 |
|---|---|
| `GET /api/v1/experiments` | 각 항목에 선택 필드 `outcome`과 `comparisons`(= 저장된 `definition.spec.comparisons`를 서버가 그대로 잘라 낸 것) 추가. **`definition`은 목록에서 생략**(크기) |
| `GET /api/v1/experiments/{id}` | `definition`·`outcome` 모두 포함. `comparisons` 필드는 없음(`definition.spec.comparisons`에 이미 있음) |

- `schemaVersion`은 `serving-experiment.v1alpha1` 그대로. 단건에 `definition`이 없거나 `outcome`이 없으면 옛 게시다(해당 항목 "미기록").
- 정정(supersedes) 관계는 게시하지 않는다. 정정 흐름이 아직 없으므로 표시하지 않는다.
- 서버는 `definition`을 해석하지 않고 JSON object인지만 확인해 그대로 준다(`internal/serving/httpapi/experiments.go`).
- 대시보드는 결과를 **완료된 게시만** 다룬다. 진행 중 실험은 아래 §2.1의 작은 요약으로 "평가 실행 중" 표시만 한다(운영 보드 오판 방지).
- 게시 ID = EvaluationRun UID. 같은 실험의 정정은 새 UID로 게시된다(덮어쓰기 없음).
- 실험 조작은 §2.2(부하 정의 작성·실험 spec 편집·실행·중지·release 확인)를 대시보드에서 한다.

### 2.1 진행 중 평가 표시 — `GET /api/v1/evaluations/active`

부하 발생, 일부러 낸 장애(Pod 삭제·cordon), 제어 스위치 끔이 실제 클러스터에서 일어나는 동안 운영 보드가 이를 평범한 사건으로 오해하지 않도록 하는 읽기 전용 요약이다.

```json
{"items": [ {"name": "ab-weights-1", "uid": "…", "startedAt": "…", "stageIndex": 1, "stagesTotal": 3,
             "stage": {"id": "base", "mode": "autonomous", "startedAt": "…", "goAt": "…", "plannedSeconds": 300,
                       "injections": [ {"id": "kill-1", "action": "delete-serving-pod", "offsetSeconds": 120,
                                        "outcome": "Executed", "nodeName": "…", "podName": "…"} ]}} ]}
```

- 풀을 점유 중인(`PoolAcquired=True`) EvaluationRun만 나온다. 없으면 `items: []`.
- `goAt`은 부하 시작 시각(없으면 아직 시작 전), `plannedSeconds`는 그 단계 LoadScenario parts 길이 합. 남은 시간 = `goAt + plannedSeconds − now`(대시보드 계산).
- `injections[]`는 계획과 지금까지의 실행 결과. 운영 보드는 같은 노드·Pod 사건에 "주입" 태그를 붙일 수 있다(일치 판정은 nodeName/podName 정확 일치만).
- 정리 실패 시 남은 자원은 게시 `outcome.cleanup.residuals[]`(§3.1)에 실린다.

### 2.2 실험 조작 — LoadScenario·EvaluationRun 전체 CRUD + 중지·release 확인

권한은 **별도 역할 `chill-evaluation-operator`**(ClusterRole, 아래 쓰기 URL만 허용)로 준다. 실험은 Pod 삭제·cordon·제어 끔을 일으키므로 scheduler-editor 권한으로는 할 수 없다. 읽기(GET)는 기존 조회 권한. 인가 방식은 기존과 같이 토큰의 URL 권한 검사. 측정 요청 화면처럼 **사전 점검 → 제출** 2단계다.
두 CR의 spec은 불변(CEL)으로 유지한다 — 같은 UID는 언제나 같은 내용이어야 "같은 부하"·재현성이 성립하기 때문이다. 그래서 **수정(PUT)은 서버가 "같은 이름으로 교체"(UID 조건부 삭제 → 같은 이름 생성, 새 UID)로 수행**한다. 화면에는 일반 수정으로 보이고, 응답의 새 `uid`로 갱신한다. 교체가 안전하지 않은 상태에서는 거부한다(아래).

| 조작 | 요청 | 동작 |
|---|---|---|
| 부하 정의 목록·단건 | `GET /api/v1/evaluations/load-scenarios`, `GET .../load-scenarios/{name}` | `{items:[{name, uid, spec, inUse, createdAt}]}` / 단건 같은 형태 — `inUse`=이 정의를 참조하는 아직 끝나지 않은(게시 전) 실험 이름 목록 |
| 부하 정의 사전 점검 | `POST /api/v1/evaluations/load-scenarios/preflight` body `{name,spec}` | `{checks,liveWorkloads}`. 검사: absolute `parts[].rates`, workload별 uint64 10진 문자열 `seeds`(0..18446744073709551615), 정확히 `{name,uid}`인 `datasetRefs`, 두 map의 workload key coverage, Workload name+UID+generation, Dataset name+UID, 선택한 trace 증거의 크기·존재 |
| 부하 정의 만들기 | `POST /api/v1/evaluations/load-scenarios` body `{name,spec}` | 서버가 점검을 다시 하고 생성. 응답 `{name,uid}`. 구간 길이·절대 요청률·도착 분포·Workload·inline seeds·Dataset refs를 편집한다. 옛 내부 입력은 이주하지 않으며 영향을 받는 불변 객체는 소유 자원 정리 후 교체한다 |
| 부하 정의 수정 | `PUT /api/v1/evaluations/load-scenarios/{name}?uid=` body `{spec}` | 점검 후 같은 이름으로 교체, 응답 `{name, uid}`(새 UID). `inUse`가 비어 있을 때만. 이미 게시된 실험은 자기 snapshot을 갖고 있어 영향 없다 |
| 부하 정의 지우기 | `DELETE /api/v1/evaluations/load-scenarios/{name}?uid=` | `inUse`가 비어 있을 때만. 이미 게시된 실험은 정의 snapshot을 갖고 있어 영향이 없다 |
| 증거 객체 고르기 | `GET /api/v1/evaluations/inputs?kind=&set=&offset=&limit=` | `{items:[{store,key,size,kind,set,name}],page:{offset,limit,total,nextOffset?}}`. 현재 LoadScenario들의 선언된 `traceRefs`만 중복 제거하여 key 오름차순 반환한다. `kind=trace`, `set`은 LoadScenario 이름, `name`은 `<scenario> / <workload>`다. 과거 업로드 bundle은 열거하지 않는다. 페이지·정확 일치 필터 규칙은 유지한다 |
| 빈 템플릿 | `GET /api/v1/evaluations/template` | `{spec,sources,sourcePublication?}`. target component는 `name/resource`만 선언한다. CHILL image/source 입력은 없고 실제 imageID는 preflight 관측값이다. harness/loadGenerator의 pinned image/sourceSHA는 평가 도구 identity로 유지하며 generator에는 `nodeName`을 선언한다 |
| 실험 spec 편집 | (대시보드 내부) | 질문·단계(ID·row·부하 정의·요청된 장애 주입 계획)·비교·판정·`settingsPatch`를 편집한다. mode/control/start-state 입력은 없다. 기간은 LoadScenario parts에만 선언한다. Run의 선택적 `settingsPatch`를 상속하되 stage patch가 있으면 전체 대체한다. 공개 SchedulerSettings JSON 부분 객체(예: `{"controls":{"power":false}}`)로 merge하며 별도 평가자 settings 스키마는 없다 |
| 실험 목록·단건 | `GET /api/v1/evaluations`, `GET /api/v1/evaluations/{name}` | `{items:[{name, uid, spec, phase, conditions, currentStage, startedAt, finishedAt, publicationID, liveTarget}]}` — `phase` ∈ `Pending`(풀 대기·시작 전)·`Running`·`Finishing`·`Done`·`Blocked`(정리 실패로 풀 점유). 게시되면 `publicationID`(= uid) |
| 사전 점검 | `POST /api/v1/evaluations/preflight` body `{name,spec}` | `{checks:[{id,passed,reason}],liveTarget:[{component,resource:{apiVersion,kind,namespace,name},liveImageIDs:[…],ready}]}`. 대상 gate는 외부 PodReady이며 image/source/profile 일치나 restart 횟수는 gate가 아니다. imageID는 관측 이력이다. `settingsPatch`는 JSON object·64KiB 이하이며 현재 공개 settings snapshot에 merge한 결과를 SchedulerSettings strict decode와 Validate로 검사한다. 알 수 없는 키는 null이어도 거부한다. spec/LoadScenario UID/점유·generator·profiling 오염 안전 점검은 유지한다 |
| 실행 | `POST /api/v1/evaluations` body `{name,spec}` | 점검 후 생성, 응답 `{name,uid}`. 관측 `liveTarget` 배열을 Run annotation `edge.dacs.io/evaluation-live-target`에 JSON으로 기록한다. 사용자 CHILL image 입력이나 digest 반영은 요구하지 않는다 |
| 실험 수정 | `PUT /api/v1/evaluations/{name}?uid=` body `{spec}` | **시작 전(`Pending`)일 때만** 사전 점검 후 같은 이름으로 교체(새 UID). 시작한 실험은 거부 — 중지 후 다시 실행한다 |
| 다시 실행 | `POST /api/v1/evaluations` (실행과 같음) | 대시보드가 게시 `definition.spec`을 복사해 새 `name`으로 제출(과거 게시가 그대로 실행 정의다) |
| 중지·삭제 | `DELETE /api/v1/evaluations/{name}?uid=` | UID 조건부 삭제. evaluator-owned Job/writer 정리와 patch가 변경한 공개 settings 키 원복 후 반환한다. CHILL serving instances/routes의 소멸은 기다리거나 명령하지 않는다. 과거 게시·증거는 보존한다 |
| release 확인 | `POST /api/v1/evaluations/{name}/release` body `{uid,reason}` | 운영자가 명시적 settings 원복을 확인한 뒤 annotation을 단다. controller는 evaluation-owned writer 종료를 확인한 뒤 점유를 해제하며 기존 cleanup 실패 판정을 지우지 않는다 |

- 오류·결과 불명은 기존 측정 요청 화면과 같은 처리: 결과 불명이면 `GET /api/v1/evaluations/{name}`(또는 `/load-scenarios/{name}`)으로 생성 여부·uid를 확인한다.
- **요청자 기록**: 모든 쓰기(생성·수정·삭제·중지·release)는 서버의 기존 측정 요청 감사 기록 방식으로 요청자(토큰 주체)·시각·대상 name/uid·조작을 남긴다. 생성되는 객체에는 annotation `edge.dacs.io/requested-by`(주체), 중지는 삭제 직전 `edge.dacs.io/stop-requested-by`, release annotation JSON에는 `by` 필드를 넣는다. 감사 기록을 남기지 못하면 조작을 하지 않는다.
- 게시 목록·템플릿은 각 목록 조립에서 게시당 summary를 한 번만 읽고 템플릿 기본값에도 재사용한다. 동시 요청 간 결과를 캐시하지 않아 다음 요청의 최신성·저장 계약은 그대로다.
- 표시용 읽기는 명시적 `view=summary`로만 축소한다. workloads의 supportedInputs, measurement-coverage·batch-energy-optimizations·scheduler/profiles의 응답 내 조건·출처·assessment 공유 계약과 운영 크기 실측은 `docs/design/dashboard/README.md`의 “표시용 요약 읽기 계약” 및 `chill-dashboard/README.md`에 정리했다. 무쿼리·단건·저장 계약과 판정·체크 생성은 유지한다.

## 3. `definition` 형태 (`evaluation-definition.v1alpha1`)

```json
{
  "schema": "evaluation-definition.v1alpha1",
  "run": {"name": "ab-weights-1", "uid": "6f0c…"},
  "spec": { "question": "…", "target": {"components": [ … ]}, "settingsPatch": {"controls":{"power":false}}, "judgment": { … },
            "stages": [ {"id": "base", "row": "E5-3m",
                         "loadScenarioRef": {"name": "e53m", "uid": "…"},
                         "failureInjection": [ … ]} ],
            "comparisons": [ {"left": {"stageID": "base"},
                              "right": {"historical": {"publicationID": "control-222-223-first-20261030", "stageID": "full-E5-5m-26"}},
                              "reason": "명시한 공개 settings 비교"} ] },
  "loadScenarios": [ {"name": "e53m", "uid": "…", "spec": {"workloads": [ … ], "parts": [ … ], … }} ],
  "workloads": [ {"name": "image-classification-tight-slo", "uid": "…", "generation": 3,
                  "slo": {"percentile": 95, "targetLatencyNanos": 1000000000},
                  "model": "…", "preprocessor": "…", "dataset": "…"} ]
}
```

필드 전체 목록은 Go 타입이 정본이다: `api/v1alpha1/{evaluationrun,loadscenario}_types.go`.

Settings 적용은 운영자와 같은 GET/PUT `/api/v1/scheduler/settings`·인증·SAR만 쓴다. GET의 quoted `ETag`를 PUT의 `If-Match`로 보낸다. 누락은 428 SettingsPreconditionRequired, stale 또는 ConfigMap Update conflict는 409 SettingsConflict, 성공은 새 ETag다. ConfigMap 전체 버전이므로 routing/다른 블록 변경도 충돌한다. GET read-only projection `staticRouting/powerObservation/meters/requestTimeout`은 PUT에서 뺀다. controller는 opaque patch를 동결·전달하고 형태·크기만 검사한다. harness는 현재 값 읽기 → patch merge → 조건부 PUT → readback 비교 후 측정하며 patch가 건드린 키만 원복한다. 적용 충돌은 재시도 없이 실패로 기록한다. 원복 409만 최신 GET 후 경로를 다시 선택해 정확히 한 번 재시도하며 외부에서 바뀐 경로는 보존·보고한다. 기존 procedure `evidenceRef`가 `before/applied/readback/appliedAt/revertedAt/request/response/revertRequest/revertResponse/revertReadback`, 변경 경로와 요청 시각, ETag/If-Match 증거를 가리킨다.

평가 action SA의 release namespace 권한은 Jobs create/get/list/delete, Pod·log get, GO marker용 pods/exec create, LoadScenario get, Deployment get이다. cluster 범위는 Node get, 외부 fleet 확인용 Pod list, Workload/Dataset/Preprocessor get뿐이다. 공개 URL은 Workload GET, settings GET/PUT, power GET, raw inventory/request/resource export GET만 허용한다. action SA에 broad evidence-collector/scheduler-editor URL role을 연결하지 않는다. 공개 PUT의 운영자와 같은 SAR를 위해 지정 scheduler ConfigMap update 권한만 별도 RoleBinding으로 유지한다. controller에는 Chill HTTP transport·URL grant가 없고 외부 주입 선택용 Node list와 기존 Profiler Node get/patch를 사용한다.
기존 독립 `chill-evaluation-evidence-collector` SA·binding과 `servingEntrypoint.evidenceCollector` 옵션은 유일한 코드 소비자(round)의 삭제와 함께 제거한다. 새 하네스는 action Job의 projected token만 사용한다. 일반 운영자용 unbound evidence-collector role 자체는 바꾸지 않는다.
장애 주입은 실제 Workload ownerReference의 API version·kind·UID로 Pod를 고른다(controller bit는 요구하지 않으며 중복 Workload owner는 거부). 증거는 Pod/Node 이름·UID·Ready와 선택 결과만 담고, credentials가 있을 수 있는 원본 Pod spec·Node 본문은 직렬화하지 않는다.

Run 목록·단건의 `liveTarget`은 생성 시 저장한 `edge.dacs.io/evaluation-live-target` annotation 배열 `{component,resource,liveImageIDs,ready}`만 투영한다. annotation이 없거나 decode에 실패하면 `null`이며 현재 fleet을 조회해 채우지 않는다. 게시 목록의 `facts.stages[].settingsPatchKeys`는 동결된 patch의 정렬된 leaf 경로(예: `["controls.power","minimumDemand.x"]`)다. stage patch가 있으면 Run patch 전체를 대체하며, 없거나 `null`이면 Run patch를 상속한다. 선언 patch가 없으면 `[]`, 기록된 patch가 object로 해독되지 않으면 `null`이다. 정의 자체가 없는 과거 게시의 facts는 계속 생성하지 않는다. 기존 `facts.control`, `facts.stages[].control`·`mode`는 노출하지 않는다.

서버는 scheduler controller 초기화 전에 평가 route를 조립한다. 평가 settings 조회는 요청 시점의 현재 controller를 service lock 아래 읽으며 초기화 전에는 `503 CoverageWarming`을 반환한다. 준비 전 nil controller의 메서드 값을 동결하거나 기본 설정을 만들어 validation에 쓰지 않는다.



### 3.1 `outcome` 형태 (게시 요약의 선택 필드, 서버 비해석 object)

```json
{
  "execution":   {"status": "Complete|Failed", "reason": "…"},
  "measurement": {"verdict": "PASS|FAIL|INVALID|NOT_EVALUATED", "reason": "…"},
  "cleanup":     {"status": "Complete|Failed", "reason": "…"},
  "stages": [ {"id": "base", "execution": {…}, "measurement": {…}, "cleanup": {…},
               "injections": [ {"id": "kill-1", "action": "delete-serving-pod",
                                "outcome": "Executed|Cancelled|Invalid", "reason": "…",
                                "scheduledOffsetSeconds": 120, "actualOffsetSeconds": 120.4} ]} ]
}
```

`outcome.stages[].id`도 `summary.stages[].id`와 같은 키다. 값이 없는 축은 생략하지 않고 reason(`NotStarted` 등)으로 채워 게시한다.
정리가 실패하면 `outcome.cleanup.residuals[]` = `[{"kind": "Node|Pod|Job|SchedulerSettings", "name": "…", "reason": "…"}]`로 남은 자원을 싣는다(운영자가 kubectl로 찾을 대상).

생성기를 만든 conductor는 로그 EOF와 무관하게 기존 `flushSeconds` 간격으로 정확한 Job UID의 `Failed` 상태를 확인하고, 해당 Job 이름을 담은 `ProducerFailed` 실행 실패로 즉시 정리 단계에 넘긴다.
실행 실패(`ExecutionLost` 포함)는 정리 실패가 아니다. run 전체의 마지막 `finish`가 evaluator-owned Job/Pod 정리와 settings 원복을 재관측한 결과로 `CleanupComplete`와 pool 반환을 결정한다. 단, 결과를 모르는 injection(`Unknown`/`Requested`)은 영향을 받은 Node/Pod를 `RemoteEffectUnknown` 잔여물로 남기고 pool을 계속 잡는다.
옛 harness writer는 기록한 Job/Pod UID와 실제 owner UID로 확인한다(stage ID가 `run`이어도 finish 자신의 run-owned Job/Pod는 제외). Pod가 터미널 phase이거나 API에서 사라졌다는 사실만으로 정리됨을 추정하지 않는다. 해당 Pod의 container 종료 상태와 배치 노드의 현재 Ready를 모두 관측해야 반환할 수 있고, 기록한 Pod가 사라졌거나 노드/종료 상태를 확인하지 못하면 관측 불가 잔여물로 유지한다. 게시 `outcome.cleanup.residuals[]`는 이 마지막 정리에서 관측한 잔여물·미확인 효과를 담는다.
Go 실행 문맥의 nullable `childRefs[].injectionRecords`·`procedures`는 Python 소비 경계에서 빈 목록으로 취급한다. injection이나 기록된 procedure가 없는 단계도 실제 정리 경로를 반드시 실행한다.

## 4. 대시보드가 읽을 것 — 추측 대신

| 화면 개념 | 읽는 곳 | 비고 |
|---|---|---|
| 단계 공개 설정 비교 | `spec.stages[].settingsPatch`(없으면 `spec.settingsPatch`) | mode/preset API는 없다. 선언한 patch만 적용·복원한다 |
| 단계 ↔ KPI 연결 | `summary.stages[].id` == `spec.stages[].id` | **유일한 조인 키**. 순서·이름 패턴으로 맞추지 않는다 |
| 비교 짝 | `spec.comparisons[]` | **`left` = 후보, `right` = 기준, Δ = left − right.** 각 쪽은 같은 실험 `stageID` 또는 `historical{publicationID,stageID}` — `publicationID`는 기존 게시 id(캠페인 이름 포함)이며 `GET /api/v1/experiments/{publicationID}`로 찾는다. 못 찾으면 "대상 게시 없음". `reason` 표시 |
| 같은 부하 여부 | `stages[].loadScenarioRef.uid` 동일 + `spec.loadGenerator` 동일 | **의도된 보수적 기준**: 내용이 같아도 LoadScenario를 다시 만들면 다른 부하다. 요청률·내용 비교로 판단하지 않는다 |
| 부하 단계·요청률 | `loadScenarios[].spec.parts[].rates` | absolute req/s 표시. offered÷기간 역산 불필요 |
| SLO | `workloads[].slo` | 실험 정의에 SLO 복사본 없음. KPI 요청 행과는 **`workloadUID`로만** 잇는다. UID가 없으면 SLO "미기록"(이름 매칭 금지) |
| 판정 3축 | `outcome.execution`·`outcome.measurement`·`outcome.cleanup` (단계별은 `outcome.stages[]`) | 작업 완료 ≠ 평가 통과, 정리 실패도 게시될 수 있음 — 세 축을 섞지 않는다. `summary.status`는 measurement와 같은 값 |
| 장애 주입 결과 | `outcome.stages[].injections[]` | 계획은 `spec.stages[].failureInjection[]`, 결과는 여기. `id`로 잇는다 |

## 5. 지울 것 (`chill-dashboard/lib/experiments.ts`)

추측 함수: `stagePolicy`, `stagePairing`, `stageComparison`(요청률 1% fallback 포함), `campaignPairs`·`campaignGroups`·`policyStageGroups`의 추론 부분, `stageLoadLabel`의 상대 요청률 순위.
SLO는 `definition.workloads[].slo`에서, 절대 요청률은 `loadScenarios[].spec.parts[].rates`에서 읽는다. 과거 게시의 null/미기록 의미는 유지하고 재해석하지 않는다.

## 6. 과거 게시

- `definition`이 없는 게시(#213~, round 10~26 등)는 정책·비교·같은 부하를 **"미기록"**으로 표시한다. 추측으로 채우지 않는다.
- 이 때문에 지금 보이던 과거 실험의 비교 화면이 미기록으로 바뀐다. 의도된 가시적 변화다.
- 별도 과거 reader·재게시·소급 정의 부착은 없다.

## 7. 배포 순서와 수용 기준

1. 서버(이미 구현)·대시보드 reader 먼저 배포 → 2. 새 publisher(실험 실행기) 활성화. 새 게시가 생긴 뒤 구 reader로 되돌리는 것은 지원하지 않는다.
2. 수용(DB-FR-66): `definition` 있는 게시는 정책·비교·SLO가 정의와 일치, 없는 게시는 미기록, 단계 이름을 바꿔도 짝이 유지(조인 키가 id이므로), 진행 중 실험은 목록에 없음.
3. 테스트 픽스처: `cmd/chill-server/experiment_service_test.go`, `internal/serving/httpapi/experiments_test.go`에 definition 있음/없음 예가 있다.

## 8. 문의

계약 변경이 필요하면 이 문서와 설계 §8을 같은 커밋에서 고친다. 서버 wire는 바꾸지 않고(`definition`은 비해석 object) 정의 형태 변경은 publisher(`hack/evaluation/evaluation.py`)와 함께 맞춘다.

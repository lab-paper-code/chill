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

### 2.2 실험 조작 — 부하 정의 작성·spec 편집·실행·중지·release 확인

권한은 기존 scheduler-editor 역할과 같은 방식(토큰의 URL 권한 검사)을 쓴다. 측정 요청 화면처럼 **사전 점검 → 제출** 2단계다.
두 CR은 spec이 불변이다. 따라서 "편집"은 언제나 **복사해서 고친 뒤 새 이름으로 만드는 것**이다. 기존 객체를 고치는 API는 없다.

| 조작 | 요청 | 동작 |
|---|---|---|
| 부하 정의 목록 | `GET /api/v1/evaluations/load-scenarios` | `{items:[{name, uid, spec, inUse}]}` — `inUse`=이 정의를 참조하는 진행 중 실험 이름 목록 |
| 부하 정의 사전 점검 | `POST /api/v1/evaluations/load-scenarios/preflight` body `{name, spec}` | `{checks, liveWorkloads}`. 검사: spec 검증(rates/alpha 배타, alpha면 rateBasis·κ 참조 필수 등), 각 Workload name+UID+generation이 지금 클러스터와 같음, 참조한 증거 객체(seed·trace·payload 번들·rateBasis·κ)가 저장소에 존재. `liveWorkloads`는 현재 name→{uid, generation}(다르면 화면이 차이를 보여 주고 사용자가 반영 여부 선택) |
| 부하 정의 만들기 | `POST /api/v1/evaluations/load-scenarios` body `{name, spec}` | 서버가 점검을 다시 하고 생성. 응답 `{name, uid}`. 화면은 기존 정의를 복사해 parts(구간 길이·요청률 또는 alpha)·도착 분포·Workload를 고치는 폼과 JSON 편집을 함께 준다 |
| 부하 정의 지우기 | `DELETE /api/v1/evaluations/load-scenarios/{name}?uid=` | `inUse`가 비어 있을 때만. 이미 게시된 실험은 정의 snapshot을 갖고 있어 영향이 없다 |
| 증거 객체 고르기 | `GET /api/v1/evaluations/inputs` | `{items:[{store, key, size, kind}]}` — 저장소의 입력 번들·seed·trace·rateBasis·κ 목록(`kind`별). 새 입력 번들 자체를 만드는 일은 오프라인 도구(`hack/e5/evaluation_inputs.py`) 몫이다 — 원천 측정 기록에서 만들어지는 과학 입력이라 화면에서 지어내지 않는다 |
| 실험 spec 편집 | (대시보드 내부) | 게시 `definition.spec` 또는 빈 템플릿을 복사해 질문·단계(mode·부하 정의·시작 상태·장애 주입 계획)·비교(left 후보/right 기준)·판정 기준·제어 설정을 폼/JSON으로 고친다. 서버 저장 없음 — 아래 사전 점검이 검증 정본 |
| 사전 점검 | `POST /api/v1/evaluations/preflight` body `{name, spec}` | `{checks:[{id, passed, reason}], liveTarget}`. 검사: spec 검증, LoadScenario name+UID 존재, 대상 이미지 digest가 실제 실행 중 Pod과 같음, 다른 EvaluationRun이 풀 점유 중 아님, **기존 E5 측정(Job/producer) 실행 중 아님**. `liveTarget`은 spec의 대상 구성요소별 지금 실행 중 digest — 다르면 화면이 차이를 보여 주고 사용자가 반영 여부를 고른다(서버가 몰래 바꾸지 않음) |
| 실행 | `POST /api/v1/evaluations` body `{name, spec}` | 서버가 사전 점검을 다시 수행하고 통과 시 EvaluationRun 생성. 응답 `{name, uid}`. 이후 진행은 §2.1 |
| 다시 실행 | 위와 같음 | 대시보드가 게시 `definition.spec`을 복사해 새 `name`으로 제출(과거 게시가 그대로 실행 정의다) |
| 중지 | `DELETE /api/v1/evaluations/{name}?uid=` | UID 조건부 삭제(background). controller의 끝 정리가 원래 설정을 복구하고 풀을 반환한 뒤 사라진다. 중지한 실험도 그때까지의 결과로 게시될 수 있다 |
| release 확인 | `POST /api/v1/evaluations/{name}/release` body `{uid, reason}` | 정리 실패로 풀이 묶인 실험에 운영자 확인 annotation을 단다. controller는 실제 반환이 관측될 때만 풀을 놓는다(버튼이 상태를 지우지 않음). 기존 실패 판정은 그대로 |

- 오류·결과 불명은 기존 측정 요청 화면과 같은 처리(결과 불명이면 목록을 다시 읽어 확인).

## 3. `definition` 형태 (`evaluation-definition.v1alpha1`)

```json
{
  "schema": "evaluation-definition.v1alpha1",
  "run": {"name": "ab-weights-1", "uid": "6f0c…"},
  "spec": { "question": "…", "target": {"components": [ … ]}, "control": { … }, "judgment": { … },
            "stages": [ {"id": "base", "row": "E5-3m", "mode": "autonomous",
                         "loadScenarioRef": {"name": "e53m", "uid": "…"}, "control": { … },
                         "startState": { … }, "failureInjection": [ … ]} ],
            "comparisons": [ {"left": {"stageID": "base"},
                              "right": {"historical": {"publicationID": "control-222-223-first-20261030", "stageID": "full-E5-5m-26"}},
                              "reason": "κ 비례 배분 vs 꽉 채우기"} ] },
  "loadScenarios": [ {"name": "e53m", "uid": "…", "spec": {"workloads": [ … ], "parts": [ … ], … }} ],
  "workloads": [ {"name": "image-classification-tight-slo", "uid": "…", "generation": 3,
                  "slo": {"percentile": 95, "targetLatencyNanos": 1000000000},
                  "model": "…", "preprocessor": "…", "dataset": "…"} ]
}
```

필드 전체 목록은 Go 타입이 정본이다: `api/v1alpha1/{evaluationrun,loadscenario}_types.go`.

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

## 4. 대시보드가 읽을 것 — 추측 대신

| 화면 개념 | 읽는 곳 | 비고 |
|---|---|---|
| 단계 정책(자율/고정/제어요소 끔) | `spec.stages[].mode` (`autonomous`·`fixed`·`controls-disabled`) | 제어요소 끔 단계의 꺼진 스위치는 `spec.stages[].control.controls`(없으면 `spec.control`) |
| 단계 ↔ KPI 연결 | `summary.stages[].id` == `spec.stages[].id` | **유일한 조인 키**. 순서·이름 패턴으로 맞추지 않는다 |
| 비교 짝 | `spec.comparisons[]` | **`left` = 후보, `right` = 기준, Δ = left − right.** 각 쪽은 같은 실험 `stageID` 또는 `historical{publicationID,stageID}` — `publicationID`는 기존 게시 id(캠페인 이름 포함)이며 `GET /api/v1/experiments/{publicationID}`로 찾는다. 못 찾으면 "대상 게시 없음". `reason` 표시 |
| 같은 부하 여부 | `stages[].loadScenarioRef.uid` 동일 + `spec.loadGenerator` 동일 | **의도된 보수적 기준**: 내용이 같아도 LoadScenario를 다시 만들면 다른 부하다. 요청률·내용 비교로 판단하지 않는다 |
| 부하 단계·요청률 | `loadScenarios[].spec.parts[]`(rates 또는 alpha) | 표시용. offered÷기간 역산 불필요 |
| SLO | `workloads[].slo` | 실험 정의에 SLO 복사본 없음. KPI 요청 행과는 **`workloadUID`로만** 잇는다. UID가 없으면 SLO "미기록"(이름 매칭 금지) |
| 판정 3축 | `outcome.execution`·`outcome.measurement`·`outcome.cleanup` (단계별은 `outcome.stages[]`) | 작업 완료 ≠ 평가 통과, 정리 실패도 게시될 수 있음 — 세 축을 섞지 않는다. `summary.status`는 measurement와 같은 값 |
| 장애 주입 결과 | `outcome.stages[].injections[]` | 계획은 `spec.stages[].failureInjection[]`, 결과는 여기. `id`로 잇는다 |

## 5. 지울 것 (`chill-dashboard/lib/experiments.ts`)

추측 함수: `stagePolicy`, `stagePairing`, `stageComparison`(요청률 1% fallback 포함), `campaignPairs`·`campaignGroups`·`policyStageGroups`의 추론 부분, `stageLoadLabel`의 상대 요청률 순위.
`frozenP95SLO`·`alphaText`는 추측이 아니라 KPI 필드 읽기이므로, 출처를 `definition.workloads[].slo`·`loadScenarios`로 바꾸되 **과거 게시의 null/미기록 의미는 유지**한다.

## 6. 과거 게시

- `definition`이 없는 게시(#213~, round 10~26 등)는 정책·비교·같은 부하를 **"미기록"**으로 표시한다. 추측으로 채우지 않는다.
- 이 때문에 지금 보이던 과거 실험의 비교 화면이 미기록으로 바뀐다. 의도된 가시적 변화다.
- 별도 과거 reader·재게시·소급 정의 부착은 없다.

## 7. 배포 순서와 수용 기준

1. 서버(이미 구현)·대시보드 reader 먼저 배포 → 2. 새 publisher(실험 실행기) 활성화. 새 게시가 생긴 뒤 구 reader로 되돌리는 것은 지원하지 않는다.
2. 수용(DB-FR-66): `definition` 있는 게시는 정책·비교·SLO가 정의와 일치, 없는 게시는 미기록, 단계 이름을 바꿔도 짝이 유지(조인 키가 id이므로), 진행 중 실험은 목록에 없음.
3. 테스트 픽스처: `cmd/chill-server/experiment_service_test.go`, `internal/serving/httpapi/experiments_test.go`에 definition 있음/없음 예가 있다.

## 8. 문의

계약 변경이 필요하면 이 문서와 설계 §8을 같은 커밋에서 고친다. 서버 wire는 바꾸지 않고(`definition`은 비해석 object) 정의 형태 변경은 publisher(`hack/e5/evaluation.py`)와 함께 맞춘다.

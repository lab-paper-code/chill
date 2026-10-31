# EvaluationRun → 대시보드 연결 계약 (대시보드 담당 전달용)

- 상태: 서버·게시 wire 구현 완료(`49f7ee9a`), CR·실행기 구현 중. 대시보드 변경은 이 문서의 계약만 따른다.
- 정본: 요구 [DB-FR-66](../spec/dashboard/SRS.md) · [실험 SRS EXP-14·15](../spec/experiments/SRS.md) ·
  설계 [§8 증거·게시·조회](../design/profiling/experiment-execution.md) · [ADR 0032](../adr/0032-experiment-execution-boundary.md).

## 1. 무엇이 바뀌나 (한 줄)

지금 대시보드는 단계 이름 접미사·요청률 유사성으로 "자동 vs 고정 짝", 정책, SLO를 **추측**한다.
앞으로는 게시물에 실린 **실험 정의(`definition`)를 그대로 읽는다**. 정의가 없는 과거 게시는 비교·정책을 **"미기록"**으로 표시한다.

## 2. 조회 API — 경로·인증 변화 없음

| 경로 | 변화 |
|---|---|
| `GET /api/v1/experiments` | 각 항목에 선택 필드 `definition` 추가 |
| `GET /api/v1/experiments/{id}` | 같음 |

- `schemaVersion`은 `serving-experiment.v1alpha1` 그대로. `definition`이 없으면 옛 게시다.
- 서버는 `definition`을 해석하지 않고 JSON object인지만 확인해 그대로 준다(`internal/serving/httpapi/experiments.go`).
- 대시보드는 **완료된 게시만** 다룬다. 진행 중·미게시 실험은 범위 밖이다(운영자는 `kubectl get evaluationruns.edge.dacs.io`).
- 게시 ID = EvaluationRun UID. 같은 실험의 정정은 새 UID로 게시된다(덮어쓰기 없음).

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
                              "right": {"historical": {"runUID": "…", "stageID": "base", "resultRef": { … }}},
                              "reason": "κ 비례 배분 vs 꽉 채우기"} ] },
  "loadScenarios": [ {"name": "e53m", "uid": "…", "spec": {"workloads": [ … ], "parts": [ … ], … }} ],
  "workloads": [ {"name": "image-classification-tight-slo", "uid": "…", "generation": 3,
                  "slo": {"percentile": 95, "targetLatencyNanos": 1000000000},
                  "model": "…", "preprocessor": "…", "dataset": "…"} ]
}
```

필드 전체 목록은 Go 타입이 정본이다: `api/v1alpha1/{evaluationrun,loadscenario}_types.go`.

## 4. 대시보드가 읽을 것 — 추측 대신

| 화면 개념 | 읽는 곳 | 비고 |
|---|---|---|
| 단계 정책(자율/고정/제어요소 끔) | `spec.stages[].mode` (`autonomous`·`fixed`·`controls-disabled`) | 제어요소 끔 단계의 꺼진 스위치는 `spec.stages[].control.controls`(없으면 `spec.control`) |
| 단계 ↔ KPI 연결 | `summary.stages[].id` == `spec.stages[].id` | **유일한 조인 키**. 순서·이름 패턴으로 맞추지 않는다 |
| 비교 짝 | `spec.comparisons[]` | `left`/`right`는 같은 실험 `stageID` 또는 다른 실험 `historical{runUID,stageID}`. `reason` 표시 |
| 같은 부하 여부 | `stages[].loadScenarioRef.uid` 동일 + `spec.loadGenerator` 동일 | 요청률 비교로 판단하지 않는다 |
| 부하 단계·요청률 | `loadScenarios[].spec.parts[]`(rates 또는 alpha) | 표시용. offered÷기간 역산 불필요 |
| SLO | `workloads[].slo` | 실험 정의에 SLO 복사본 없음. 이 값이 정본 |
| 판정 3축 | 기존 `summary.status`·`stages[].kpis` | 작업 완료 ≠ 평가 통과, 정리 실패도 게시될 수 있음 — 세 축을 섞지 않는다 |

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

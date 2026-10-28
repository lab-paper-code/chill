# 대시보드 라이브 무대·결과 표 계획

2026-10-28 2차 검토 반영. 검토는 fusion으로 했다: 패널 3개(claude-opus-5-5, gpt-6-astra, gemini-4-argon), 판정 gpt-6.1-sol, 판정 결과 "수정 후 진행". 명세는 [대시보드 SRS](../spec/dashboard/SRS.md), 설계는 [대시보드 설계](../design/dashboard/README.md)를 따른다. 옛 제안의 화면별 요구와 메시지 필드를 여기서 다시 정본으로 만들지 않는다.

## 1. 위치와 선후

대시보드는 spec 표의 입력이 아니다(AGENTS.md). **E5 결과를 오해 없이 보여 주는 수단**이며, 표 기입을 대신하지 않는다. 순서는 다음과 같다.

1. 진행 중인 E5(#213) 종료. 대시보드 때문에 측정 이미지·설정·저장 계약·하네스를 바꾸지 않는다.
2. 승인된 판정 기준으로 결과 표를 기입한다.
3. 다음 서버 재빌드 전에 게이트웨이 분리 미사용 코드(`e947cd5e`)를 정리한다(decision-log 2026-10-28, ADR 0029 Deferred). T5와 `internal/serving/live`는 유지한다.
4. [E5 증거 API](2026-10-28-e5-evidence-api.md)를 진행한다. 연구 기여가 T6보다 크다. 다만 S1·S2는 `resources`·`cluster` 완성을 기다리지 않는다. 새 관측이 필요한 자원·장애 진단 화면만 그 작업에 의존한다.
5. **S1** 불변 결과 표·비교 → **S2** 최소 라이브 무대.

| 항목 | 상태 |
|---|---|
| T5 라이브 전달(서버 hub, 앞단 WebSocket 중계) | 코드 이행 완료(26c3bb76). 승인 배포와 실제 브라우저 수용은 아직이다 |
| S1 불변 결과 표·비교·선택 근거 | 다음 |
| S2 최소 라이브 무대 | S1 뒤, 선행 projection 수정 포함 |
| 제품용 연속 기록 재생(DB-FR-40), 운영자 영역 재편 | 연기(6장) |

## 2. 기존 기반 (재사용)

- 실험 결과 읽기: `cmd/chill-server/experiment_service.go`, `internal/serving/httpapi/experiments.go`
- 측정 결과 읽기: `measurement_service.go`
- 상태 summary: `internal/serving/httpapi/scheduler_summary.go`
- 작업 예상 시간과 출처: `Action.ExpectedSeconds` / `ExpectedSource`
- 판단 기록 조회와 상세 화면: `GET /api/v1/observations/scheduler`, `chill-dashboard/features/scheduler-evidence.tsx`
- 예약 장부: `SchedulerSnapshot.Reservations`(`internal/serving/scheduler.go:309-321`)와 Node `Ready`·`Measurement`·`Reasons`(`:115-129`). **새 장부 projection을 정의하지 않는다.**
- 이름 복원: 관측 응답 `definitions[]`와 판단 snapshot의 정의를 우선 쓰고, 찾지 못하면 UID를 표시한다.

대시보드는 가공 API만 쓴다. 증거 API의 `/observations/raw`는 하네스용 별도 권한이므로 앞단에 열지 않는다. router 식별자나 다중 source 합산은 넣지 않는다(게이트웨이 분리 연기).

## 3. S1: 불변 결과 표·비교와 선택 근거

- `/experiments` 화면(현재 "준비 중")을 실행·단계 목록, 단계 결과 표, 두 실행·두 단계 비교로 바꾼다.
- KPI 형식은 실제 두 가지만 명시적으로 처리한다.
  - 시연: `kpis.workloads`(`hack/e5/demo.py:907-925`)
  - E5: `kpis.requests`(`hack/e5/e5.py:1772-1787`)
- 표의 각 값에는 분자·분모·단위·출처·결손 사유를 보존한다. 없는 지표는 추정해서 만들지 않는다. publication에 값이 없으면 "미제공"으로 표시하고, 필요하면 E5 뒤에 publication을 한 번 보완한다.
- 생성기 offered(계획 trace), 서버 수신 cohort, 라이브 종결 버킷은 서로 다른 출처다. 서로 바꿔 부르지 않는다.
- 단계 에너지는 승인된 결과 값을 그대로 쓰고 **"공유 POOL 에너지 · 행 합산 금지"**를 표시한다. 대시보드에서 다른 정책으로 다시 계산하지 않는다.
- 기록 당시 UID·이름·정의를 쓴다. 현재 Workload에 이름으로 다시 조인하지 않는다.
- **선택 근거**: 판단을 고르면 원본 `decision.at`으로 `GET /observations/scheduler?from=<at>&to=<at+1ns>`를 조회한다.
  - 판단 identity를 확인하고, 같은 시각에 다른 행이 있으면 페이지를 넘긴다.
  - ns 정밀도를 보존한다.
  - 찾지 못하면 "기록 없음"으로 표시하고, 최신 판단으로 대체하지 않는다.
  - 기존 상세 컴포넌트를 재사용한다. 새 판단 ID나 저장소는 만들지 않는다.
- **호출자 권한 보완**: `serving-manager` ClusterRole에 다음 권한이 빠져 있다(`charts/chill/templates/serving_entrypoint_rbac.yaml:238-284`. 서버 SA에는 `:199-201`로 있다). 실제 principal의 추가 binding은 확인하지 않았다.
  - experiments 목록·단건 경로
  - 두 측정 projection 경로
  - 두 측정 CR의 get/list

## 4. S2: 최소 라이브 무대

### 선행 수정 (서버 projection, `cmd/chill-server/live.go`)

| 수정 | 이유 |
|---|---|
| 인스턴스 상태를 binding 수로 정하지 않는다. 준비 여부, 분배 가능(`routable`), 실제 분배(`routing`), native 실행(`running`), 드레인·제거 작업을 독립 사실로 보낸다 | 지금은 `len(instance.Workloads)`로 `serving/draining`을 정한다(`live.go:140-144`). binding은 잔여 호출 때문에도 유지된다(`runtime.go:1091-1100,1127-1130`). 그래서 준비됐지만 미분배인 인스턴스가 `draining`으로, 분배 해제 뒤 잔여 실행 중인 인스턴스가 `serving`으로 보인다 |
| `Workloads[0]` 한 행에만 귀속시키는 `Instance.WorkloadUID`를 없애고, 지도는 `routable`·`routing`·`running` 맵으로 배치한다 | 여러 워크로드에 묶인 인스턴스가 하나의 행에만 나온다 |
| 판단 snapshot의 `Reservations`를 작은 live DTO로 투영한다: owner, Node, Workload, Pod·instance 범위, reason. 후보 본문(target)은 반복 전송하지 않는다. Node `ready`·`measurement`·`reasons`도 함께 보낸다 | 예약이 T5 DTO와 summary에 빠져 있다(`live/types.go:121-145`) |
| 인스턴스 `Waiting`을 투영하고 `AssignedUnknown`과 구분한다 | 유입/대기/실행 구분에 필요하다. 수신−종결 차를 큐 길이로 쓰지 않는다 |
| 쓰이지 않는 필드를 삭제한다: `Decision.RecordRef`, `Node.Watts`(전력은 metrics 프레임), `Node.TargetPowerMode`(값을 넣는 곳 없음) | 죽은 계약이다. 근거 연결은 3장의 `decision.at` 조회로 한다 |
| 관련 상태를 한 delta로 묶고, 관측 시각만 다른 같은 상태는 다시 발행하지 않는다 | runtime·decision 투영이 변경 여부와 무관하게 여러 프레임으로 나뉘어 발행된다(`live.go:107-127,177-194`). 링 600프레임이 빨리 소진된다 |
| 첫 판단 전 Node 목록이 비어 있을 수 있음을 화면에서 다룬다 | Node는 판단 callback에서만 채워진다 |

이 서버 수정은 3번(분리 코드 정리)과 같은 재빌드에 묶는다. SRS와 설계(§4.3, §6.1)에 예약 표시와 "실패 후 잔류" 장면의 수용 조건을 먼저 넣는다.

### 화면 범위

- **상태 띠**: 실시간/fixture, 자동제어 여부, 마지막 관측과 경과 시간, UTC/KST. 라이브에는 시나리오 단계의 출처가 없으므로 "미지정"으로 둔다.
- **워크로드 카드**: 선택한 UID마다 1장(E5 사례에서는 3장). 모델명이나 샘플 필드에 따라 분기하지 않는다.
  - 라이브 지표 의미는 다음과 같다(`live/types.go:153-162`, `live/metrics.go:134-159`). 같은 초의 completed/received는 성공률이 아니다.

    | 지표 | 정의 |
    |---|---|
    | 수신 | 수신 req/s |
    | 종결 모집단 | `N = completed + failed` |
    | 성공률 | `completed / N` |
    | SLO 안 비율 | `withinSLO / N` |
    | `N = 0` | 비율 없음 |

  - 무유입(0), 비율 없음, 관측 결손을 구분한다.
- **배치 지도**: 준비·미분배, 분배 중(running 0 포함), 실제 처리, 분배 해제 뒤 잔여 실행, 제거를 서로 다르게 그린다. Node 전력은 metrics 프레임에서 가져온다.
- **예약**: owner, 영향 범위, 진행 중 또는 실패 후 잔류 사유. 서버가 주지 않은 유지 이유는 "미확인"으로 둔다. 작업 종료만 보고 예약 해제를 추정하지 않는다. 판단 주기마다 갱신된다고 표시한다. 사용할 수 없는 Node의 사유도 보여 준다.
- **시간축 4레인**(부하, 성공/SLO, 배치, 전력): 연결 이후 최대 최근 10분, 브라우저가 보유한 구간만 그린다. 600프레임은 10분 보존을 보장하지 않는다. 접속 이전과 재동기화 유실 구간은 공백으로 둔다. 준비·관측 공백을 성공률 0이나 전력 0으로 그리지 않는다.
- **이벤트 선택**: 3장의 선택 근거 패널을 연다.
- **상태 문구**: 등록됨 / 요청 받음 / 실행 준비됨 / 실제 처리 중을 구분한다. SLO 충족과 판단 근거 부족은 따로 표시한다.
- **폴링 금지의 적용 범위**: 무대에서 재사용하는 기존 화면도 이행 대상이다(`lib/client.tsx:140`, `features/scheduler.tsx:150,183`, `features/scheduler-settings.tsx:126`의 `poll:true`).
  - 실시간 상태는 WebSocket push, 과거 근거는 선택할 때 GET, 쓰기 결과는 명시적으로 다시 조회한다.
  - T5가 주지 않는 상태를 자동 갱신된다고 표시하지 않는다.
  - 연결이 끊겨도 폴링으로 바꾸지 않는다.

## 5. E5 증거 API와 맞물리는 지점

- 증거 API는 settings GET에 읽기 전용 `meters`를 추가한다. 그런데 현재 `putSchedulerSettings`(`chill-dashboard/features/scheduler-settings.tsx:78-83`)는 GET 객체를 복사한 뒤 routing 필드 두 개만 지우고 PUT으로 보낸다. 서버는 모르는 필드를 거부한다(`httpapi/handler.go:172-175`). 그래서 **증거 API와 같은 작업 단위에서 PUT이 쓰기 허용 필드만 보내도록 고친다.** 지금은 결함이 아니고, 필드를 추가하는 순간 생기는 문제다.
- 라이브 RBAC(`/chill/api/v1/live`), 대시보드·서버 이미지, S1 호출자 권한은 증거 API의 차트 반영과 **한 번의 승인 배포**로 묶는다. 옛 "`kubectl set image`만, Helm 금지" 절차는 쓰지 않는다.
- T5가 처음 들어가는 서버 빌드에서 live 집계의 CPU 몫을 증거 API 리허설 때 한 번 기록한다. 측정 대상 게이트웨이에 들어가는 코드이기 때문이다.

## 6. 연기 항목과 재개 조건

연기는 SRS 요구를 삭제하거나 완료로 바꾸는 것이 아니다. 해당 요구는 미완료로 남는다.

| 연기 항목 | 재개 조건 |
|---|---|
| 시연·E5의 제품용 연속 기록 재생(DB-FR-40) | 재생이 필요한 구체적 표·그림·설명 장면이 정해지고, 다음 세 가지가 확인됐을 때: 실제 보관본 출처·당시 정의·pre-state를 연결할 수 있음, 선택 구간 조회의 비용과 응답 범위, summary의 실험별 `sources/raw`와 관측 archive가 자동으로 연결되지 않는다는 점에 대한 처리. 그전까지 재생은 고정 fixture로 화면 의미만 검사한다 |
| E5 전 구간 raw를 브라우저에서 전수 재집계 | 필요한 그림을 기존 summary, 오프라인 산출물(`timeline.py`), 제한 조회로 만들 수 없다는 근거가 있을 때. 시간당 scheduler 382 MB, requests 175 MB 규모다 |
| 운영자 영역 경로·메뉴 재편(`/records`, `/settings` 등) | 실제 연구·운영 흐름에서 통합 대상이 정해졌을 때. 재개하면 이전 route·import·호출자를 한 번에 이행한다 |
| 후보 커버리지, 전환 비용 원본 등 추가 화면 | 연결할 측정 표나 운영 필요와 서버 조회 계약이 확정됐을 때. 동작 비용 측정과 전력모드 전환 비용은 다른 API다 |
| 다중 router/source | 게이트웨이 분리가 재개될 때(ADR 0029 재개 조건). 대시보드가 새 재개 조건을 만들지 않는다 |

## 7. 완료 기준

코드 이행, 로컬 검사, 승인 배포, 실제 브라우저 수용, 연구 평가 완료를 **각각 따로 기록**한다. 검사는 E5 보호 창 밖에서 한다. 대시보드는 예약 안전성, 과밀, 해시·바이트 일치를 다시 판정하지 않는다. 서버가 준 의미를 화면이 보존하는지만 본다.

**S1**
1. 실제 게시된 불변 실험으로 단계 표와 비교를 표시한다. 게시된 실험이 없으면 "API 구현됨"과 "사용 가능한 결과 있음"을 구분한다.
2. 시연·E5 두 KPI 형식에서 출처·분자·분모·단위·미제공 사유를 보존한다.
3. 생성기 offered, 서버 수신 cohort, 라이브 종결 버킷을 서로 바꿔 부르지 않는다.
4. 단계 에너지에 "공유 에너지 · 행 합산 금지"를 표시하고, 다시 계산하지 않는다.
5. 삭제 뒤 같은 이름으로 재등록된 사례에서도 당시 UID·이름·정의가 섞이지 않는다.
6. 선택한 과거 판단은 새 판단이 와도 바뀌지 않는다. 기록이 없으면 최신 상태로 대체하지 않는다.
7. 실제 사용할 호출자 권한으로 필요한 조회를 확인한다.

**S2**
1. projection 단위 검사 장면: 준비·미분배, 분배 중이나 running 0, 실제 처리, 분배 해제 뒤 잔여 실행, 제거, 예약 진행·실패 후 잔류·해제, Node 사용 불가 사유.
2. 수신 초와 종결 초가 다른 요청에서도 비율 분모가 유지된다. 무유입, 비율 없음, 결손이 구분된다.
3. fixture에서 빈 상태→첫 구성→확장→축소, 지도·4레인·선택 근거를 같은 화면에서 확인한다. 합성 장면은 검사용 변형이라고 표시한다.
4. fixture와 별도로 다음을 확인한다: 실제 인증 경로의 WebSocket 연결, 재개 범위 안과 밖, 로그아웃·신원 변경 시 이전 데이터 제거. 단절되어도 폴링으로 바꾸지 않는다.
5. 좁은 화면, 키보드로 사건 선택, focus 복원, reduced-motion.

## 8. 기록

- **보존 작업물**(`/home/genesis1/.cache/chill-assets/ops/pools-deploy/preserved-work-20261025/`)은 참고용이다.
  - 서버 hub→앞단 재개 순서는 T5로 끝났다.
  - 남은 UI WIP(`live.ts`, `live-stage.tsx`)는 최종 DTO 이전 버전이므로 통째로 복원하지 않는다.
- **2026-10-28 T5 코드 이행**
  - 서버 쪽 구성:
    - `internal/serving/live`: DTO, 원자 baseline·구독, `(seq,at)` 재개, bounded ring·queue와 snapshot barrier, 초별 metrics·지연 보정·누락 표시를 맡는다.
    - `httpapi/live.go`: Upgrade 전에 TokenReview/SAR를 거친다.
    - `cmd/chill-server/live.go`: 성공한 판단·작업 기록의 callback만 투영한다. REST polling은 없다.
    - `chill-dashboard/server/live-proxy.mjs`: 첫 auth, 동일 Origin, 고정 TLS 상류, 연결별 유한 쓰기를 처리한다.
  - 로컬 검증:
    - `nice -n 19 env GOMAXPROCS=2 go test -p 1`로 live·httpapi·serving·kube·cmd/chill-server 패키지를 통과했다.
    - 스모크 테스트에서 `hello seq=0 → snapshot seq=1 → delta seq=2 → decision event seq=3` 순서를 확인했다.
    - `node --test live-proxy.test.mjs`는 2/2 통과했다.
  - 하지 않은 것: Docker 이미지 빌드, 전체 lint/test, 클러스터 배포.
- 원시 근거: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/`의 `dashboard-redesign-20261025.md`, `dashboard-plan-20261025.md`, `t5-resume-plan-20261025.md`. 사용자 확정 사항은 WebSocket, 첫 메시지 인증, 근거 저장소 재생이다. 옛 폴링안은 대체됐다.

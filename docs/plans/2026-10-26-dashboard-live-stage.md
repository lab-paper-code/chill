# 대시보드 결과 표·라이브 무대 계획

명세는 [대시보드 SRS](../spec/dashboard/SRS.md), 화면 구조·엣지 출처 표는 [설계](../design/dashboard/README.md) §6.1을 따른다.

## 1. 순서

대시보드는 E5 결과를 보여 주는 수단이며, spec 표 기입을 대신하지 않는다.

1. E5(#213) 종료, 결과 표 기입
2. 다음 서버 재빌드 전에 `e947cd5e` 분리 코드 정리(T5는 유지)
3. [E5 증거 API](2026-10-28-e5-evidence-api.md)
4. **S1** 결과 표·비교 → **S2** 라이브 무대

T5 라이브 전달은 코드 이행만 끝났다(26c3bb76). 배포와 브라우저 수용은 아직이다.

대시보드는 가공 API만 쓴다. `/observations/raw`는 쓰지 않는다.

대시보드는 서버가 준 사실을 보여 주기만 한다. 예약 안전성, 과밀, 해시·바이트 일치를 다시 판정하지 않는다. 화면이 모델명이나 샘플 필드에 따라 갈라지지 않는다(워크로드 비종속). router나 다중 source는 넣지 않는다(게이트웨이 분리 보류).

## 2. S1: 결과 표·비교

- `/experiments`를 실행·단계 목록, 단계 결과 표, 두 실행·두 단계 비교로 바꾼다.
- KPI 형식: 시연 `kpis.workloads`, E5 `kpis.requests` 두 가지만 처리한다.
- 값마다 분자·분모·출처·결손 사유를 보존한다. 없는 값은 만들지 않는다.
- 생성기 offered, 서버 수신, 라이브 종결 버킷은 서로 다른 출처다. 이름을 섞지 않는다.
- 에너지:
  - 게시된 값을 그대로 쓰고 "공유 POOL 에너지 · 행 합산 금지"를 표시한다.
  - 오차가 있으면 "값 ± 상한"으로 보인다.
- 실행 간 비교: 측정 규칙 식별자가 다른 항목에 "측정 규칙 다름"을 붙인다. 숫자는 가리지 않는다.
- 이름은 기록 당시 UID·정의를 쓴다. 현재 Workload에 이름으로 다시 조인하지 않는다.
- 근거:
  - 판단의 `decision.at`으로 `/observations/scheduler`를 조회한다. 같은 시각 행이 여럿이면 판단 identity로 고른다.
  - "기록 없음"과 "조회 시간 초과"를 구분한다. 최신 판단으로 대체하지 않는다.
- 권한: `serving-manager`에 experiments와 두 측정 projection 조회 권한을 추가한다. 차트 기본값에 빠져 있는 것만 확인했고, 실제 계정에 따로 붙은 권한은 확인하지 않았다.

## 3. S2: 라이브 무대

### 서버 선행 수정 (`cmd/chill-server/live.go`)

| 수정 | 이유 |
|---|---|
| 인스턴스 상태를 binding 수로 정하지 않고 ready·routable·routing·running·작업을 따로 보낸다 | 지금은 준비·미분배가 "드레인"으로, 잔여 실행이 "정상"으로 보인다 |
| `Instance.WorkloadUID`를 삭제한다 | 여러 POOL에 묶인 인스턴스가 한 행에만 나온다 |
| 판단 snapshot의 `Reservations`와 Node `ready/measurement/reasons`를 투영한다. 새 장부나 조회 API는 만들지 않는다 | 예약과 사용 불가 사유를 보여 주기 위해. 화면은 작업이 끝났다는 것만으로 예약 해제를 추정하지 않는다. 서버가 주지 않은 유지 이유는 "미확인"으로 둔다 |
| `instances[].waiting`(배정 뒤)과 `workloads[].pending`(배정 전)을 투영한다 | 두 대기를 따로 보여 준다. 합치지 않고, 수신−종결 차로 대신하지 않는다 |
| `cluster` 사건(Pod·observer issue)을 라이브 event로 보낸다. 발행 지점은 증거 API 구현 3이다 | E5-7 장애 대응 장면을 보여 주기 위해 |
| `RecordRef`, `Node.Watts`, `Node.TargetPowerMode`를 삭제한다 | 값을 넣는 곳이 없다 |
| 관련 상태를 한 delta로 묶고, 바뀐 것이 없으면 발행하지 않는다 | 링 버퍼가 빨리 소진된다 |

이 수정은 2번 재빌드에 묶는다.

### 화면 (레벨 0~3)

- **상태 띠**(모든 레벨): 실시간/fixture, 자동제어, 마지막 관측 경과, UTC/KST.
- **레벨 0 `/` 흐름 보드**
  - CHILL 구조 그림 위에 라이브 값을 싣는다.
  - POOL(워크로드 UID)마다 레인 1개. 레인에는 수신, 성공률, 상태 문구, Node별 인스턴스 기호를 둔다.
  - 판단·작업·장애 사건은 사건 레인 1개에 쌓는다.
- **레벨 1 `/pools/[uid]`**: 카드 전체와 4레인(부하, 성공률·지연, 전력, 사건). 최근 10분 중 브라우저가 받은 구간만 그린다. 서버 버퍼(600프레임)는 10분 보존을 보장하지 않으며, 접속 이전과 재동기화 유실 구간은 공백이다.
- **레벨 2 `?open=`**: 인스턴스·Node·예약 펼침 카드.
- **레벨 3 `?at=`**: 판단 근거 옆 패널.

라이브 지표의 정의:
- 성공률 = `completed / N`, SLO 안 비율 = `withinSLO / N`. 여기서 `N = completed + failed`이다. 같은 초의 `completed / received`는 성공률이 아니다(요청을 받은 초와 끝난 초가 다르다).
- `N = 0`이면 "비율 없음"으로 표시한다.
- 공백은 0으로 그리지 않는다.

폴링을 쓰지 않는다. 재사용하는 기존 화면의 `poll:true`와 `/pools/[uid]`의 수동 폴링도 없앤다. 연결이 끊겨도 폴링으로 바꾸지 않는다.

## 4. 증거 API와의 경계

| 증거 API 계획 | 이 계획 |
|---|---|
| publication 형식(에너지 오차, 측정 규칙 식별자) | S1에서의 표시 |
| `resources`·`cluster` 기록, 라이브 callback 지점 | 라이브 event 형태와 화면 |
| 보관본 읽기·마감 정책 | 시간 초과 표시 |
| settings `meters`와 쓰기 클라이언트 두 곳 수정 | — |
| 승인 배포 단위 | 그 배포에 넣을 것: 라이브 RBAC, S1 권한, 대시보드·서버 이미지 |

- E5 측정 창 동안에는 라이브 보기만 쓴다. 과거 조회는 측정 대상 서버에 부하를 더한다. 앱이 강제하지 않는 운영 규칙이며, 운영 담당의 부하 창 방송을 따른다.
- T5의 CPU 몫은 증거 API 확인 리허설에서 함께 잰다.

## 5. 연기

연기한 항목의 SRS 요구는 미완료로 남는다.

| 항목 | 재개 조건 |
|---|---|
| 제품용 연속 기록 재생(DB-FR-40) | 재생이 필요한 그림이 정해지고, 보관본·당시 정의 연결과 조회 비용이 확인됐을 때. 그전에는 fixture로 화면 의미만 검사한다. fixture 통과를 재생 완료로 세지 않는다. E5 전 구간 원시를 브라우저에서 재집계하는 방식은 쓰지 않는다(시간당 수백 MB) |
| 운영자 메뉴 재편 | 통합 대상이 정해졌을 때 |
| 후보 커버리지 등 추가 화면 | 연결할 측정 표가 생겼을 때 |
| 레벨 2 Node 자원 표시 | 증거 API 구현 2가 끝나고 화면이 필요해졌을 때. 그때 라이브 요약으로 할지 가공 조회로 할지 정한다 |

## 6. 완료 기준

코드 이행, 배포, 브라우저 수용을 따로 기록한다. 검사는 측정 창 밖에서 한다.

**S1**
1. 게시된 실험의 단계 표·비교가 출처·분모·결손과 함께 보인다.
2. #213 에너지가 "값 ± 상한"으로 보이고, 규칙이 다른 실행 비교에는 표시가 붙는다.
3. 삭제 후 같은 이름으로 재등록된 경우에도 당시 이름이 섞이지 않는다.
4. 선택한 근거는 새 판단이 와도 바뀌지 않는다. #213 판단 1건이 보관본에서 열린다.
5. 실제 호출자 권한으로 조회된다.

**S2**
1. projection 검사:
   - 인스턴스: 준비 중, 준비·미분배, 분배 중, 처리, 잔여 실행, 제거
   - 예약: 진행, 잔류, 해제
   - Node 사용 불가 사유
   - 여러 POOL에 묶인 인스턴스
2. 비율 분모가 유지되고, 무유입·비율 없음·결손이 구분된다.
3. fixture에서 빈 상태→첫 구성→확장→축소를 레벨 0만으로 따라갈 수 있다. 합성 장면은 검사용이라고 표시한다.
4. 보드 값이 모두 설계 §6.1 출처 표의 필드에서 나온다. 미제공 값은 빈칸으로 둔다.
5. URL로 새로고침해도 같은 계층이 열린다. 닫으면 focus가 돌아온다.
6. 실제 인증 WebSocket으로 연결된다. 재개 범위 밖에서 재동기화된다. 로그아웃하면 데이터가 지워진다.
7. 증거 API 구현 3 뒤에 E5-7 장면이 사건 레인에 실시간으로 보인다.
8. 좁은 화면, 키보드 조작, reduced-motion이 동작한다.

### 브라우저 수용 기록 — 2026-10-30, rev110

**코드 이행·배포·브라우저 수용을 분리한다.** 이 기록은 코드 수정이나 배포 완료 선언이 아니라,
이미 배포된 화면을 실제 호출자 권한으로 검사한 결과다. FAIL은 수정하지 않았다.

| 구분 | 이번 기록의 범위 |
|---|---|
| 코드 이행 | 기존 구현을 사용했다. 대시보드·서버 코드는 변경하지 않았다. |
| 배포 | 기존 Helm **rev110**, `O/deployB/control-230-20261030/upgrade.log`의 `REVISION: 110`. 이 배포는 node-agent `d01ff31010cd227a3f4eebb7d1f4954da643085e`이며, 대시보드·ChillServer 이미지는 `pools-control-226-f8bc1eaeaad2`가 유지된다(`post-values.json`). 이번 검사에서는 배포하지 않았다. |
| 브라우저 수용 | 2026-10-30 **04:39 UTC부터** Chromium으로 `https://155.230.35.213:31104/`에 접속했다. 기존 `dashboard-user` 토큰의 subject는 `system:serviceaccount:chill-system:chill-dashboard-user`다. 관리자 토큰·새 토큰·권한 우회는 사용하지 않았다. |

`O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`.
증거 루트는 **`O/campaign/v2/dashboard-acceptance-20261030/`**다.
`results.json`에 시작·종료 시각과 기준별 결과, PNG에는 실제 화면, 같은 이름의 TXT에는 URL·화면 텍스트를 보존했다.
수동 촬영한 viewport PNG와 JSON 영수증도 같은 디렉터리에 있다. 토큰·Authorization 헤더는 증거 파일에 보존하지 않았다.
브라우저는 사설 인증서 예외로 열었고, 별도로 기존 CA를 사용하는 HTTPS 요청의 인증서·hostname 검증 성공
(`authorized=true`, HTTP 200)을 `tls-ca-verification.json`에 보존했다.
검사 프로세스는 nice 19로 실행했다. 측정 창 밖에서 조회만 했으며 부하 생성·클러스터 변경은 하지 않았다.
브라우저 요청 기록은 GET만 있으며 `/observations/raw` 호출도 없다.

**집계: PASS 6 / FAIL 2 / 미확인 5. 전체 S1/S2 수용 완료가 아니다.**
PASS의 관찰 범위를 아래에 명시하며, 빈 클러스터에서 만들 수 없는 장면을 통과로 세지 않는다.
표의 파일명은 위 증거 루트 기준이다.

| 기준 | 결과 | 실제 관찰·제한 | 주요 증거 |
|---|---|---|---|
| S1-1 단계 표·비교·출처·분모·결손 | **FAIL** | `demo-20261026-1/2`의 A 단계 표·비교 및 `분자·분모·출처·결손` 펼침은 열린다. 그러나 실제 `/experiments` 목록은 이 두 demo뿐이어서 요청한 **#213 단계 결과를 선택할 수 없다**. | `06-experiment-list-no213.png`, `07-demo-stage-provenance.png`, `08-demo-comparison.png`, `api-receipts.json` |
| S1-2 #213 에너지 ± 상한·규칙 차이 | **FAIL** | #213이 목록에 없어 **#213의 “값 ± 상한” 표시를 수용할 수 없다**. 두 demo의 에너지는 ± 없이 표시되며, 비교에는 `측정 규칙 식별자 미제공`이 나온다. 서로 다른 규칙의 배지는 미확인이다. | `06-experiment-list-no213.png`, `08-demo-comparison.png` |
| S1-3 같은 이름 재등록의 당시 신원 | **미확인** | 신원이 없는 두 demo의 같은 이름 행이 A/B 별도 행으로 보존되는 것은 보았다. 실제 삭제·재등록 사례를 새로 만들지는 않았다. | `08-demo-comparison.png` |
| S1-4 선택 고정·#213 보관 판단 열림 | **PASS** | 다음 라이브 판단이 도착해도 선택한 `at`와 근거 문자열이 동일했다. #213 보관본의 `2026-10-28T02:11:08.703423765Z`, `Feasible / keep / plan-80bfdcafcd0d`, 원본 행 0을 S1 identity 선택 후 원본 JSON까지 열었다. #213 게시 단계가 없으므로 demo A 화면의 `decision.at` 입력으로 보관 판단에 접근했으며, 이를 #213 단계 표가 열린 것으로 세지 않는다. | `selected-live-before.json`, `selected-live-after.json`, `20-s1-archived-identity.png`, `22-s1-archived-source-open.png` |
| S1-5 실제 호출자 권한 | **PASS** | dashboard-user로 실험 목록·demo 상세 및 정확한 시각의 보관 scheduler 조회가 모두 HTTP 200이다. | `api-receipts.json`, `browser-network-redacted.json` |
| S2-1 projection 상태들 | **미확인** | 라이브 snapshot의 instances 0·reservations 0, `예약 해제 · 현재 예약 없음`은 확인했다. 인스턴스 6상태, 예약 진행·잔류, Node 불가 사유, 여러 POOL의 공유 인스턴스는 빈 클러스터라 확인하지 못했다. | `01-live-level0.png`, `02-live-reservations.png`, `websocket-valid-outside-resume.json` |
| S2-2 분모·무유입·비율 없음·결손 구별 | **미확인** | `0 req/s · 무유입`, `completed 0 + failed 0 = N 0`, 성공·SLO `비율 없음`, p95/p99 빈칸을 확인했다. 양수 분모 및 명시적 계측 결손 장면은 만들지 않아 전체 기준은 미확인이다. | `03-live-level1.png`, `field-source-sample.json` |
| S2-3 fixture 빈→첫→확장→축소 | **미확인** | 배포의 `/api/config`는 `liveFixture=false`다. `/?fixture=expand`로 접속해도 실시간이고 장면 선택기가 없다. 서버 설정 변경이나 응답 대체 없이 네 장면·검사용 표시를 검사할 수 없었다. | `api-receipts.json`, `11-fixture-query-remains-live.png` |
| S2-4 설계 §6.1 출처·미제공 빈칸 | **PASS** | 현재 빈 라이브 보드의 수신·종결·비율, pending, 판단, 작업, 공유 POOL 전력을 실제 WebSocket DTO와 대조했다. null 지연은 빈칸이다. 인스턴스·Node가 있는 상태의 미노출 경로는 S2-1의 미확인 범위로 남긴다. | `field-source-sample.json`, `01-live-level0.png`, `03-live-level1.png` |
| S2-5 URL 복원·focus | **PASS** | L0→L1→예약 L2 및 판단 L3를 열고, 새로고침 후 토큰 재입력으로 같은 URL 계층을 복원했다. 닫기·Esc는 opener로, 새로고침으로 opener가 없으면 heading으로 focus가 돌아왔다. `◂ 보드로`는 UID 레인에 focus한다. 인스턴스·Node L2는 빈 클러스터라 미확인이다. | `04-level2-url-before-refresh.png`, `05-level2-url-restored.png`, `14-control213-url-restored.png`, `interaction-results.json` |
| S2-6 실제 인증 WebSocket·재동기화·로그아웃 | **PASS** | `/api/live` HTTP 101 뒤 기존 토큰 auth, 실제 snapshot·metrics·decision event를 받았다. 별도 native browser WebSocket에서 보관 하한 7246 밖의 `resume from=1`과 시각을 보내면 seq 7845 snapshot으로 재동기화했다. 로그아웃 뒤 연결이 닫히고 POOL·근거 DOM 및 local/session storage가 비었다. | `authenticated-websocket-log.json`, `websocket-valid-outside-resume.json`, `logout-cleared.json`, `23-logout-cleared.png` |
| S2-7 E5-7 실시간 사건 | **미확인** | 관찰 중 실제 판단 사건은 도착했으나 E5-7 장애 사건은 없었다. 장애·부하를 새로 만들지 않았고 합성 fixture도 비활성이다. | `authenticated-websocket-log.json` |
| S2-8 좁은 화면·키보드·reduced-motion | **PASS** | 390×844에서 document scrollWidth 390으로 가로 넘침이 없고 근거 패널은 보드 아래다. Enter·Tab·Esc로 POOL·예약·닫기·모바일 메뉴 및 focus 복귀를 확인했다. reduced-motion `reduce=true`에서 live-stage의 computed animation은 모두 `none`이다. UTC/KST 전환도 확인했다. | `16-mobile-top.png`, `17-mobile-evidence-below-board.png`, `18-mobile-keyboard-pool.png`, `19-mobile-menu-keyboard.png`, `mobile-reduced-motion.json` |

추가 관찰: 보관 scheduler를 30초·limit 100으로 넓게 조회하면 HTTP 502
`DashboardResponseTooLarge`였다(`historical-scheduler-receipt.json`).
정확한 `decision.at`의 S1·라이브 근거 조회는 HTTP 200이고 보관본이 열렸다.
첫 수동 WebSocket probe는 양수 `from`에 필요한 `at`를 빠뜨렸으므로 제품 실패 근거에서 제외했다
(`websocket-outside-resume.json`). 유효한 재개 요청의 결과는 별도 파일로 보존했다.
이번 문서 기록 외에 FAIL 수정·fixture 활성화·새 게시·실험 실행은 하지 않았다.

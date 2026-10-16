# CHILL 캠페인 과거 기록 — 배포·측정 운영 UX·Dashboard 종단 대조

[현재 목표·판정·다음 의존성](2026-10-13-scheduler-live-evidence-campaign.md) · [제어 기록 주제별 목록](2026-10-13-scheduler-live-evidence-campaign-04-control.md)

2026-10-14~16의 실행 이력이다. 아래 현재·미완료·보류는 각 실험 당시 상태이며 현재 작업 지시가 아니다. 실패 원본과 후속 복구를 구분한다. 상대 evidence 경로는 명시된 직전 prefix/루트를 따른다.

### 보고 시점 — 2026-10-14 실제 제어 연결

- genesis1/2·Latte·Rpi의 q1/q2 prepare/loaded idle/remove·membership을 완료했고 resize 비용은 primitive로 합성했다. 당시 실제 자원 변경은 미검증이었다. accepted reference39개만 ConfigMap에 연결했고 실패/Insufficient는 제외했다. 전력모드 외부 조율은 미완료였다.
- release21/native-full-uid-binding·새 journal/1분 archive에서 snapshot200/약40ms, power20초200/약71ms였다. 10노드 complete·4993.424198745157J/249.67120993725786W, active0이었다. 실제 Dashboard도 같은4.99kJ/249.67W·10노드·source timestamp 부재를 표시했고 무표본 구간은 값 없음이었다. 화면: `/tmp/omp-sshots-15a5904c2ccff7b1.webp`, `/tmp/omp-sshots-15a591b12acff7b2.webp`.
- 날짜 입력의 Invalid time value와 per-Pod CPU ceil 오판을 수정했다. focused Go/lint·Dashboard test11/typecheck/lint/build는 통과했지만 당시 배포·재검증 전이었다.
- `native-control-a/b`는 서로 다른 UID·Ready/admission=true였다. 첫 HTTPS 추론200·executionComplete·FP32[1,1000], native queue91.665185ms/service38.27635ms/elapsed130.358594ms와 외부 왕복1.3555289909709245s를 구분했다. 당시 bootstrap-zero-unmeasured-no-SLO-proof이며 자동 적용/8개 제어OFF였으므로 FULL은 아니었다.

### 역할 기반 명칭 교체와 입력 수정 검증 — 2026-10-14

- Workload는 사용자-facing 이미지 분류 서비스로 구분했다:
  `image-classification-tight-slo` UID `1cb0d9c5-48ad-4d7d-b925-440d129961e2`,
  `image-classification-relaxed-slo` UID `0d41e22a-085e-449d-88cc-db5e8821713a`.
  기존 API create 201 후 원래 UID drain/delete 202를 실행했고 옛 이름은 실제 404다.
  두 새 서비스는 200/Ready/admission=true이고 실제 추론을 각각 완료했다.
- 내부 전달 Service는 `chill-executor-<실제 Pod UID>`로 교체했다. release23 owner
  `full-executor-services` index `sha256:57ab1c134284f15407c64c5c2e1efb5bfa282bfac7402d1696f110f2e4f7802c`.
  이전 `chill-native-...` Service 삭제 후 relaxed 서비스의 추론 200/실행 완료를 확인했다.
  불필요한 alias/fallback은 없다. 구현 `a1d556e`, focused serving tests와 make lint 통과.
  근거: `/tmp/chill-native-campaign-executor-service-old-removed-smoke.json`.
- release23 Dashboard 날짜 수정 이미지를 배포했다. 실제 브라우저에서 DOM input/change
  event 및 Apply로 두 날짜 변경이 정상 적용됐고 빈 값/잘못된 날짜는 URL 유지·요청 0으로
  거부됐다. untouched 6자리·9자리 fractional ISO 보존을 확인했다. 키보드 입력 자체의
  증거는 아니며 helper fill 한계와 구분한다.
  화면: `/tmp/omp-sshots-15a599b19cba5f68.webp`, `/tmp/omp-sshots-15a599d2b03a5f69.webp`, `/tmp/omp-sshots-15a599dd2aba5f6a.webp`.
- 같은 실제 브라우저의 power query는 200이지만 scheduler history query는 504로
  실패했고 timeout 표시를 확인했다. 날짜 수정과 FULL/history 성공을 혼동하지 않는다.

### 실제 OOM과 owner 용량 보정 — 2026-10-15

- 128Mi 제한에서 owner가 실제 두 번 OOMKilled됐다. 92개 관측·18개 서로 다른 Kubelet
  시각의 최대 사용량은 127.609Mi였고, GC live heap은 관측 최대 55Mi에서 부하 후 5Mi로
  감소했다. 이 범위에서 단조 증가하는 retained heap은 확인하지 못했다.
- Helm owner limit만256Mi로 조정하고 CPU500m/request64Mi를 유지했다. enabled-owner render41개 중 valid29/skip12/errors0이었다.
- 동일한 60초 부하 30건이 실제 HTTP200·완료됐고 이후 idle을 포함한 100초·84개 관측에서
  restart0을 확인했다. 최대 RSS98.816Mi·working set167.582Mi·사용량255.574Mi였다.
  사용량에는 cache가 포함되므로 heap 여유나 무기한 무누수를 주장하지 않는다.
- 근거: `/tmp/chill-owner-memory-full-window-20261015/envelope-proof.json`,
  `/tmp/chill-owner-memory-256Mi-smoke-20261015/envelope-proof.json`,
  `/tmp/chill-owner-256Mi-unchanged-load-20261015/`. 최신 SDK 재측정·Serving 검증은 별도다.

## 측정 운영 API·CLI와 기존 GPU 결과 채택 — 2026-10-15

- `/tmp` 임시 exporter·Eval/Bash의 제출/조회/설정 조립을 기존 비동기 측정 API와
  `measurements submit/list/show`, `profiles list/adopt`로 연결했다. 새 측정은 실행하지 않았다.
- `52efda0`은 owner GET/POST `/api/v1/scheduler/profiles`, ConfigMap 선택, 읽기 namespace
  제한과 권한을 추가했다. `90c8e1a`는 CLI와 검증된 HTTPS·caller bearer 전송을 추가했다.
  startup `--profile-campaign`은 제거했고 선택 권한은 `profileCampaigns` 한 곳에만 둔다.
- 기존 genesis2 결과 UID `5fdb92da-6c7b-4deb-b598-7052b85255c2`로 실제 검증했다.
  미허용 GPU는 ConfigurationRequired·exit1·ConfigMap RV 무변경이었다. 운영자가 측정된 GPU
  하나를 명시적으로 허용한 뒤 채택은 Persisted·exit0, 후속 조회는 기존 4개와 모두 Loaded였다.
  반복 채택도 exit0·RV 무변경이며 기존 지정 설정·준비 설정을 보존했다.
- `measurements list/show`로 실제 Complete·9 trial 결과를 조회했다. 서버 dry-run은
  NOT_SUBMITTED였고 해당 이름의 측정 객체는 NotFound였다. GPU 물리 준비·자동 선택은 미검증이고
  모든 자동 제어는 OFF다. 기존 서빙 경로의 실제 요청 4건도 실행·결과를 보존했다.
- 리뷰의 부분 profile publication, 미지원 runtime access 누락, GPU Env가 다른 Pod 재사용,
  결과 크기 상한 오류를 수정했다. 전체 lint/test, arch-check/gopls-check, 관련 race와
  별도 Helm 검증은 통과했다. 독립 snapshot 전체 lint의 무관한 operatorconsole 테스트 포맷 문제는 보존하고 변경 패키지로 분리했다. owner image `aff40940cbb8b197b9081bc59536c0785a401c3cf25621458a9a88410d5fc771`의 readyz를 확인했고 Dashboard는 별도 Main에 인계했다.
- 증거: `/tmp/chill-completion-cpu-cost-assembly/profile-cli-*.json`,
  `measurement-cli-show-live.json`; gate 출력 `artifact://2293`, `artifact://2300`.

## Owner 측정 운영 네 흐름 종단 검증 — 2026-10-15

- `9b4a008`(terminal writer)·`e23dc82`(owner)·`7356e97`(CLI), owner `1b8dc604…`/profiler `2ce68f81…`를 배포했다. 전체 gate·7패키지 race(`artifact://2423`)·기본/owner/Profiler Helm(`artifact://2402`)은 통과했다.
- 실제 목록 25개의 Complete/Failed 상태를 조회했다. 유효 manifest POST는 native server dry-run
  `200 / NOT_SUBMITTED`이며 CR을 만들지 않았다. 존재하지 않는 Model의 production preflight는
  `400`으로 거절됐고 CR도 없었다. 신규 측정 실행이나 정상 제출 `202`를 실검증했다고 주장하지 않는다.
- 물리 Rpi profile 채택 해제는 `409 ProfileInUse`, stale version은 `409`로 거절됐다.
  미사용 GPU는 `200 Removed / persisted=false / loaded=false`였고 나머지 4개는 Loaded를 유지했다.
  재채택 후 5개가 다시 Loaded였으며 해당 GPU raw 7449 bytes는 그대로 보존됐다.
- 폐기 대상 `retired-native-genesis2-q2-current`의 귀속 key 21개가 삭제 전에 모두 존재함을 확인했다.
  DELETE는 `202 / evidenceDeleted=true`와 21개 key를 반환했고, 독립 S3 조회에서 모두 `NoSuchKey`였다.
  `kubectl wait`로 native BEO와 소유 Run 9개의 소멸을 확인했다. stale/선택된 측정 삭제는 `409`였다.
- 다른 측정 UID, 현재 5개 profile, 새 GPU raw 7449 bytes와 Rpi CPU2 Pod UID는 변경되지 않았다.
  두 Workload UID의 실제 요청 4건이 `HTTP200 / executionComplete`였고 controls는 모두 OFF다.
  부분 실패·공유 참조 거절은 회귀검사로 확인했으며 live fault injection은 하지 않았다.
  이는 운영 계약 검증이며 자동 선택·SLO 개선·에너지 절감이나 FULL 제어 완료의 증거가 아니다.
- 증거 루트: `/tmp/chill-completion-cpu-cost-assembly/`.
  `owner-extension-{live-list,submit-smoke,profile-removal-smoke,delete-smoke,delete-readback,final-serving}.json`,
  `owner-delete-owned-evidence-before.json`. Dashboard 소스·배포는 별도 Main 세션에 인계한다.

## 실제 사건 시각 census와 동일 축 조회 지연 — 2026-10-15

- `63521317b5bb`의 window별 in-memory scheduler census를 owner 이미지 `382f3eb2…`로 배포했다.
  실제 사건·표본·membership 시각과 counted prefix로 읽을 window만 줄이고, prefix 불일치·
  nil-policy·baseline 동률은 보수적으로 읽는다. 별도 저장 index·판정 완화는 없다.
- 기존 60초 축 `01:07:01.47873812Z..01:08:01.47873812Z`의 전력 조회는
  HTTP200·28.997초에서 HTTP200·0.306722초로 줄었다. 이전 계측은 477 window·57,713 record·
  453,098,612 bytes scan 28.941초, projection 21.316초였다. 임시 계측은 제거했다.
- 같은 10개 노드의 각 79표본·`sourceTimestampAbsent=true`·`maximum sample gap exceeds policy`,
  `complete=false`·전체/활성 POOL J/평균 W null·`incompleteWindows=[]`를 유지했다.
  응답 지연 개선이지 전력 품질 복구·완전한 에너지 계측의 증거는 아니다.
- 이전 BFF 504·10.010초의 1시간 축 `03:37:35.732Z..04:37:35.732Z`, `limit=50`은
  owner 직접 조회에서 HTTP200·0.180565초·50건·`nextCursor=50`이었다.
  BFF·실제 브라우저 성공은 아직 재검증하지 않았고 별도 Dashboard Main 완료를 기다린다.
- 독립 scoped/race·lint/test/arch/gopls gate는 통과했다. 무관한 operatorconsole 포맷은 임시 snapshot에서만 적용했다. 기존 자원·서빙과 controls OFF를 유지했다.
- 근거: `/tmp/chill-completion-cpu-cost-assembly/{power-census-live-after.json,history-census-live-after.json,power-phase-measured.log}`,
  독립 gate `artifact://2480`.

## FULL 조회 크기 차단과 실제 UI 추론 — 2026-10-15

- `785c944c`·`9365f9ae`는 현재/상태/프로파일/판단 이력 GET의 `view=compact`를 제공한다.
  응답 안에서 반복된 profile·completion 측정만 사전과 참조로 공유한다.
  전체 원본 객체·서로 다른 같은-ID 값·후보 순서·SLO·설정·사유·동작 ACK를 보존하며
  기본 raw API와 journal/archive는 그대로다. 별도 cache·호환 reader·digest 검증은 없다.
  전체 gate가 통과했다(`artifact://3597`, `artifact://3609`).
- owner `9365f9ae` / image `sha256:063d4a3714b20792553787ec2197079bcb922a53ad568ad180b31bb9c3fc8a79`
  실제 readyz를 확인했다. CPU2/512Mi·PVC·Model/SLO·Workload UID·Rpi 실제 route를 보존했다.
  위 FULL 구간의 compact limit50 전체 11페이지·523건의 최대 응답은 5330711B였다.
  17판단을 limit1로 각각 조회했고 최대 2831999B, 원본 값·순서 대조도 통과했다.
  잘못되거나 중복된 view 8요청은 모두 HTTP400이었다. 모든 미래 구간의 크기를 보장하지는 않는다.
  근거: `compact-scheduler-read-cutover/final-9365f9ae/result.json`.
- 4경로 query 허용·화면 파싱 전 참조 복원은 Dashboard Main에 인계했다. 당시 BFF/UI 완료 통지·최종 FULL 대조는 미완료였다.
- 실제 Dashboard에서 채택된 CUDA 측정의 batch1·6회차·1.79J 증분 에너지와 Loaded 의미를 확인했다.
  이어 built-in ARM 입력 예시를 전송해 실제 요청 `f20ecf92a988d141f020e64e720cd085`의
  HTTP200, Rpi 실제 할당·native finished(batch143/1)·응답 완료 128.359063ms를 화면에서 연결했다.
  당시 9개 controls는 OFF, 판단 ID는 `manual-static-routing:353571935`였다.
  따라서 실제 UI 추론 증거이지 FULL ON 자동 판단으로 보낸 요청이라는 주장은 아니다.
  근거: `integrated-three-profile-cpu2-rootfixed/dashboard-ui-serving-proof.json`과 연결 텍스트·screenshot.
- 당시1159 Eval·745 Bash 및 `/tmp` 회고에서 확인된 운영 공백은 반복 응답이었다. 기존 측정 CRUD·채택·bounded CLI·native OnDelete를 재구현하거나 일반 명령 wrapper를 추가하지 않았다.

### 운영 API·UX 회고 입력 — 2026-10-16

- 당시 반복 공백은 불확실 소유권의 정확한 실행기 종료·종결, 측정 Job 메모리와 준비 할당의 대응, Workload0일 때 CLI empty-route ACK였다. 이는 운영 개선 후보 관측이지 현재 구현 지시가 아니다.
- Eval 사이48초 공백·임시 script 문법 오류는 단일 독립 controller·기존 emitter 재사용으로 해결했다. 빌드·lint·브라우저 조작이나 새 캠페인 엔진·전체 명령 wrapper의 API화 근거로 삼지 않았다.

## Main compact 배포 후 독립 브라우저 종단 대조 — 2026-10-16

- Main `bb67eb0` 이미지에서 인증 후 정확03:36:41.636416–03:37:21.636416Z를
  실제 페이지0→50→100으로 열어50+50+14건을 확인했다. 기존502는 재발하지 않았다.
  첫 판단의 유지안·선택 목표·실제 구성 근거를 펼쳤고 profile/완료 오버헤드가 복원됐다.
- 3쪽 offset103의 Latte 제거 완료 시각03:37:18.76444766Z·physical4417/f785·원본 expected/actual이
  owner114건 및 실제 Pod 부재와 일치한다. GPU 채택 화면은1.79J·6회차·CPU1/core3/fixed를 표시하며
  loaded를 실행기 Ready로 표시하지 않는다.
- 같은40초의 전력 화면과 API는 complete/whole8896.215690533672J·active196.96873142725255J로 일치했다.
  whole10노드와 activeRpi를 구분하고 원본 시각 없음·수집 시각 사용 caveat를 표시했다. 절감 비교는 아니다.
- 역사 요청2건씩의 p90/p95와 ID·Rpi physical·native finished를 대조했다. 추가 실제 UI 추론은 단1건
  `42cd4ceaa6c72382cea2aec1de22dc82`이며129.103566ms·native batch488로 완료했다.
  자동화 결과 대기 predicate가 맞지 않아 도구가 탭을 종료했지만 재제출하지 않았다.
  API의 유일 요청·finished 원본과 새 브라우저 관측 창으로 완료를 확인했다. 기존40초 비교에 합치지 않는다.
- 04:26:05.708707Z 최종 snapshot은 owner9490/CPU2/1Gi/Go768·PVC8Gi·Dashboard bb67,
  실제9OFF/fixed·두 Workload접수Ready·outstanding0·Rpi488/488/requests[]이다.
- 남은 실제 UI 결함: 제거 기록의 “이 변경 동작 자세히 보기”가 `/transitions/...`에서
  “기록을 확인해야 해요”로 간다. query는 정상 파싱되고 decoded path ID와도 일치한다.
  경로 인코딩 불일치는 아직 추정이며 Main에 정확한 재현 URL·출처를 전달했다. 전체 완료는 보류한다.
  근거: `dashboard-final-e2e-progress.json`, `dashboard-final-*.png`,
  `final-current-readonly-20261016T041406Z/`, `final-post-ui-readonly-20261016T042545Z/`.

## ChillServer 리팩토링 이미지 배포·새 Dashboard E2E — 2026-10-16

- `dad2aec`의 amd64 ChillServer를 build/push하여
  `sha256:fb5348b80d54f74cf4ff1d00d49eebe2d7881740bff1c44567b88c4b6ef08e0f`로 배포했다.
  firstfloor CPU 이미지도 로컬 build·실제 materializer/worker 실행·ORT1.27.0/NumPy2.2.6을 확인했으나
  고정된 측정 이미지·프로파일은 교체하지 않았다.
- Deployment는 이미지 한 필드만 바꿨다. CPU2/1Gi·Go768MiB·SA·PVC8Gi·Dashboard bb67·
  설정 UID/RV353984654·fixed/9OFF를 보존했다. 새 서버 Pod `2e85cc88-0763-4792-86a7-42305f7ee0d4`는
  restart0이며 verified-CA HTTPS `/readyz`와 설정·상태·Workload API가200이었다.
- 07:59:45Z의 시작 단계에서 UID map/실행기 준비 미충족2건은 실패로 남았다. 이후
  07:59:55.581137959–07:59:56.479005085Z의 기존 static routing은 complete였다.
  이력 offset24 원본에 Rpi의 정확한 native ACK·적용 전후 상태·실제 routing이 보존됐고
  Dashboard의 실행 중→완료 및 원본과 일치했다. 다른 제어를 새로 활성화한 실험은 아니다.
- 실제 Dashboard에서 측정25개·채택3개, GPU 측정1.79J/6회차와 loaded 의미,
  fixed-horizon 판단·예상 에너지·실제 Rpi 구성, `/pools`200을 확인했다.
  실제 UI 추론 POST는 단1건이며 `b2b9ffb87a6b7caf49a3a6e4e9a52735`가 완료됐다.
  화면 대기140.30ms·서비스126.417525ms, 같은9개 사건·Rpi UID/container·측정 profile·native batch492/finished를
  owner API와 대조했다. SLO 통계나 에너지 절감의 입증은 아니다.
- 같은08:09:00–08:10:00Z의 화면/API는 complete, 전체10노드15442.435242944202J,
  활성Rpi293.6367253111J였다. 원본 시각 없음·수집 시각 사용 caveat를 유지했다.
  정상 API 조회에서502/504는 관측되지 않았다. 브라우저 대기 predicate 실패로 닫힌 탭은
  재개했으며 추론을 재제출하지 않았다.
- 잔여 UI 결함은 새 `manual-static-routing:353984654` 완료 기록에서도 동일하다.
  `/transitions/manual-static-routing%3A353984654`의 from07:59:00Z/to08:01:00Z,
  offset24·kind action·at07:59:56.479322726Z·startedAt07:59:55.581137959Z를 가진 실제 링크가
  “기록을 확인해야 해요”로 간다. 이력 창의 완료·원본은 정상이며 Main 수정 전 전체 완료는 보류한다.
- 원본: `/tmp/chill-refactor-deploy-dad2aec-before.json`, `...-e2e.json`, `...-server-build.json`,
  `...-firstfloor-build.json`과 실제 브라우저 스크린샷. 최종9OFF·접수Ready·outstanding0·기존Rpi restart0을 확인했다.

## 상세 링크 수정 후 독립 최종 대조 — 2026-10-16

- Main `7b43c7c` 배포 뒤 전달받은 전용 URL을 새 브라우저에서 직접 열었다.
  offset24는 “설정한 요청 분배 적용 / 전체 노드 / 완료”로 표시되며 BFF 조회는
  해당 구간의 `limit=1&cursor=24` 한 건200이었다. 현재 상태 fallback 요청은 없었다.
- “변경 내용 자세히”의 변경 후 실행기 원본은 ready=true,
  ACK시각07:59:56.374698422Z, decision `manual-static-routing:353984654`,
  Rpi q2 profile 및 기존 fc63/a38c physical과 일치했다.
  표시 JSON을 앞서 보존한 실제 `actual.native`와 구조적으로 대조해 동일함을 확인했다.
  사건·시작·완료 시각도 offset24 원본과 일치했다. 실제 화면을 보존하고 탭을 닫았다.
- 이로써 마지막 Dashboard 링크 차단은 해소됐다. 기존 버스트 실패·전력 원본 시각 부재·
  알고리즘 계산 한계·에너지 절감 및 자동 전력모드 최적화 미입증을 통과로 바꾸지는 않는다.

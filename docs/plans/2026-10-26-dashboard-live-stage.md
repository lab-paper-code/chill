# 대시보드 결과 표·라이브 무대

계약은 [Dashboard SRS](../spec/dashboard/SRS.md), 현행 아키텍처·API·추적은 [설계](../design/dashboard/README.md)가 권위다.

## 결과 요약

- S1 결과 표·비교(`9e98194a`), S2 서버 projection(`afbf531b`)과 화면/proxy(`d65a18ee`), T5 라이브 전달을 배포했다. 가공 API만 사용하며 판정·안전 검사를 화면에서 반복하지 않는다.
- rev110 브라우저 수용 PASS6/FAIL2/미확인5 → rev114 첫 게시 뒤 **PASS8/FAIL0/미확인5**. 전체 S1/S2 수용 완료는 아니다.
- 첫 게시4건: `control-213-first-20261030`(14단계), `control-216-first-20261030`(14), `control-222-223-first-20261030`(14), `control-222-r3-first-20261030`(4). 각각 #213 full/static, factor12+당시full2, #222+#223 합성, #224 E5-5/6+#225 E5-1/7이다. 무효 #224 E5-1/7 제외.
- 게시용 KPI와 응답46/46 일치·4/4 HTTP200, 기존 demo2건 digest 불변. #213 E5-1 MNV 기한 안 성공45754/57495, whole173062.73965024325±516.5136059839J를 표시했다. 과학적 FAIL·UNKNOWN을 게시/화면 PASS로 바꾸지 않았다.
- 행별 실제 archive SHA·host-s3/gctrace-cycles·coverage·공백상한을 보존했다. 2/3회차 condition과 campaignRound 차이는 숫자를 숨기지 않고 표시한다. source 정본은 당시 tables/summary이며 재집계·재판정·원시 덮어쓰기는 없었다.
- 근거: `O/campaign/v2/dashboard-acceptance-20261030/{first-publication-receipts.json,publication-recheck-results.json,final-publication-api-receipts.json}`. `O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`.
- 수용 대상 rev114: server `pools-control-234-502e4ed86542`, dashboard `pools-control-232-30e3a6d690c8`. HTTP NodePort 31300 공개는 Origin 검사(https 강제)로 라이브 연결 403 → 사용자 요청으로 제거, ClusterIP 복귀; 접속 URL은 https://155.230.35.213:31104/ 로 통일.

## 남은 브라우저 수용

rev110에서 미확인인 live projection·실제 첫 구성/확장/축소·장애 장면은 다음 측정 창에서 확인한다. 빈 클러스터/fixture off로 확인하지 못한5건은 통과로 바꾸지 않는다. 실제 단위별 기준은 SRS에 남긴다.

## 5. 연기

연기한 항목의 SRS 요구는 미완료로 남는다.

| 항목 | 재개 조건 |
|---|---|
| 제품용 연속 기록 재생(DB-FR-40) | 재생이 필요한 그림이 정해지고, 보관본·당시 정의 연결과 조회 비용이 확인됐을 때. 그전에는 fixture로 화면 의미만 검사한다. fixture 통과를 재생 완료로 세지 않는다. E5 전 구간 원시를 브라우저에서 재집계하는 방식은 쓰지 않는다(시간당 수백 MB) |
| 운영자 메뉴 재편 | 통합 대상이 정해졌을 때 |
| 후보 커버리지 등 추가 화면 | 연결할 측정 표가 생겼을 때 |
| 레벨 2 Node 자원 표시 | 증거 API 구현 2가 끝나고 화면이 필요해졌을 때. 그때 라이브 요약으로 할지 가공 조회로 할지 정한다 |

라이브는 폴링으로 대체하지 않는다. E5 부하 창에는 라이브만 사용하고 raw/과거 조회는 측정 창 밖에서 수행한다. API 소유 범위는 [증거 API 결과](2026-10-28-e5-evidence-api.md), 측정 두 종류·κ 화면은 [API coverage 결과](2026-10-30-dashboard-api-coverage.md).

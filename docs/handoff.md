# CHILL 인수인계

기준: **2026-10-30 저장소에 보존된 마지막 관측**. 실시간 접속·배포 상태를 뜻하지 않는다. 목표는 [north-star](intent/north-star.md), 작업·승인 경계는 [사용자 의도](intent/user-intent.md), 세부 결정은 [현행 결정 표](intent/decision-log.md)다.

## 현재 상태

| 구분 | 마지막 확인과 해석 경계 |
|---|---|
| 저장소 | main에 workload-pools 통합·spec 전체 추적, push 사용자 승인. 승인과 개별 원격 반영 증거는 구분 |
| 연구 결과 | d 새 규칙34/34·정확 비용69/69와 E5 full/static/factor·3회차 표적 결과 확정. 연구 FAIL·UNKNOWN·비교 한계는 [E5 보고서](reports/e5-evaluation-20261030.md)에 유지 |
| 배포 기록 | rev114, server `pools-control-234-502e4ed86542`, dashboard `pools-control-232-30e3a6d690c8`. 대시보드는 ClusterIP(HTTP NodePort 31300은 Origin/https 강제로 제거); 접속은 https://155.230.35.213:31104/ |
| 증거 경로 | CHILL 사실은 API-only 하네스, raw는 하네스 전용·화면은 가공 조회. 옛/API KPI 일치 리허설과 #213 재판정 완료([결과](plans/2026-10-28-e5-evidence-api.md)) |
| 대시보드 | S1/S2 배포·첫 게시4건, KPI46/46 일치. 브라우저 PASS8/FAIL0/미확인5, 전체 수용 완료 아님([미확인·연기](plans/2026-10-26-dashboard-live-stage.md)) |
| 구성 정리 | A/B 기존 소유자로 이전 `58348057`…`30e3a6d6`. 옛 Run tolerance 기본 비교 회귀는 `75127da9` 수정. ?10·역사 script 삭제는 미승인 |
| 분리 기능 | 미사용 control/router split removed @e8344b20. 단일 source·T5 live 유지, G2 배선은 할 일이 아님([ADR0029](adr/0029-gateway-control-data-plane-split.md)) |

## 남은 것과 운영 경계

현재 우선순위·미해결 과제는 [로드맵](plans/2026-10-26-remaining-roadmap.md) 한 곳에서 관리한다. C3 측정 row 부재·반복 rate limiter/예약 잔류·자원 수집 identity·환경 절차의 실제 적용·하네스 메모리/재개·TLS 재사용·factor 재실험·화면 미확인 항목을 완료로 세지 않는다.

- live 변경은 한 운영 담당이 직렬 수행한다. 측정·부하 창에 build/test·raw 내보내기·대시보드 과거 조회를 겹치지 않는다.
- runtime 7종 digest 변경은 측정·d·비용·비교 기준의 재측정 영향과 승인을 먼저 확인한다. 설정은 실행 기록에 동결한다.
- 관측 결손은 UNKNOWN, 원시·실패·Accepted는 불변. `tolerateArchive`는 진단 전용이며 E5에서는 거부한다.
- 자동 변경의 시작·종결 live callback은 같은 `StartedAt`을 유지한다. 실행기가 반환한 별도 시작 시각으로 바꾸면 live 보드의 진행 항목과 종결 결과가 분리된다.
- 옛 완료 Job은 evidence binding 의존 확인과 삭제 승인 전 보존한다([ADR0026](adr/0026-finished-measurement-pods.md)).
- 배포/측정 개시는 이 문서가 허가하지 않는다. context·namespace·release·자격 증명은 [운영 안내](operations/ops-agent-guide.md)와 [hack 안내](../hack/README.md)로 확인한다.

진입점: [문서 목차](README.md) → [근원 명세·평가 표](spec/README.md) → 해당 SRS/설계. 원시는 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/{intent,campaign/v2,audit-20261024,perf,deployB}/`에 보관한다.

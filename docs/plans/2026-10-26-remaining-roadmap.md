# 남은 길: E5 뒤 과제

기준: 2026-10-30 저장소 문서의 마지막 기록. 실시간 클러스터 상태가 아니다. E5 결과는 [확정 보고서](../reports/e5-evaluation-20261030.md), 승인 경계는 [사용자 의도](../intent/user-intent.md)를 따른다.

## 완료 경계

- d 새 규칙34/34·정확 비용69/69, E5 full/static/factor와 3회차 표적 결과 확정. 분모와 연구 PASS는 구분한다.
- CloudCore/디스크 운영 절차 `94a32e9a`, 미사용 게이트웨이 분리 코드 removed @e8344b20. U11 G2 배선은 재등록하지 않는다.
- 증거 API 및 하네스 API-only 전환 리허설, dashboard S1/S2 배포와 첫 게시 완료. 브라우저는 PASS8/FAIL0/미확인5이며 전체 수용 완료가 아니다.
- 하드코딩 A/B 소유권 이전 `58348057`…`30e3a6d6`; 과거 Run tolerance 해석 수정 `75127da9`. [계획](2026-10-30-configuration-hardcoding.md)의 열린 ?10과 삭제 미승인 범위는 별개다.

## 남은 항목

| 주제 | 남은 일·승인 경계 |
|---|---|
| C3 용량 | 빈 Node mobilenet 실측 또는 더 큰 profile 측정. E5-3 add-only 413후보 CapacityRejected는 측정 row 부재로 코드 보정 대상이 아니다 |
| kube client limiter·예약 | prepare194s timeout 및 preflight finish 재발의 호출/설정 원인과 R4b 보상 미발동 조사. timeout으로 예약을 삭제하지 않는다 |
| 운영 환경 | 외부 Helm CloudCore의 nodeName drift를 승인 절차로 nodeSelector 전환; genesis1 디스크 점유·보존 규칙 실행과 PodGC 임계 권고. 운영 문서 작성은 live 적용 증거가 아니다 |
| 하네스 | summarize5.6GB JSON 메모리/출력, 단계 재개 필요성. 새 재개 장치는 승인 전 추가하지 않고 기존 pre-state 복원 경로를 사용한다 |
| E5 후속 실험 | 활성 경로 E5-7 자동 주입, 생성기 TLS keep-alive, R6 factor 재실험은 미완. R6 회귀와 factor 인과는 구분한다 |
| 관측 수집 | jetsonx identity·orin-nano(POOL 외) resources0. edge-metrics 간헐 timeout 원인은 미확정이며 전력 결측0 대체·근거 없는 timeout 상향 금지 |
| U2/U5 | jetsonx PLC4CORE d 두 번 실패·자원 Rejected로 미완. Smol x·nano5W는 Infeasible이며 조건 완화하지 않는다. native 단발 유실 정확 단계는 미확정, 측정 중 Pod 로그 동시 캡처 금지 |
| 하드웨어 | genesis2 Xid79 반복으로 E5 제외. 카드/전원/PCIe 점검과 재부팅 뒤24h 무재발은 복구 확인 과제이지 완료 사실이 아니다 |
| 대시보드 | [라이브 무대 §5 연기와 미확인5](2026-10-26-dashboard-live-stage.md). fixture/게시 PASS를 실제 장애·재생 수용으로 확대하지 않는다 |
| 구조 잔여 | [구조 검토](../reports/structure-review-20261026.md)의 K1~K5 미해결(정책 이중 쓰기, movingNodes/정리 주소, 종료/기록 오류 복구, 신선도, deadline·부분 가중치·나이·backlog·release proof). Tick/ACK 병렬화·additive prepare는 승인 범위 재확인 |
| 기존 Job | 옛1012 Job의 evidence binding 의존 확인·사용자 승인 전 삭제 보류. 새 Run 증거 확정 후 정리와는 별개([ADR0026](../adr/0026-finished-measurement-pods.md)) |
| 호스트 패치 | edge-metrics thermal-eagain 영구 반영·Helm 값은 별도 저장소 과제([일반화 결과](2026-10-15-heterogeneous-workload-generalization.md)) |

## 보류

- κ 대기 Workload의 Node 선점 금지, reset 자동 재시도/rollback, 관측 writer 추가 최적화·통계 교정·known bias 조사·전체 감사는 직접 차단 근거와 승인 없이 시작하지 않는다.
- gateway 분리는 [ADR0029](../adr/0029-gateway-control-data-plane-split.md)의 재개 조건 충족 시에만 새 최소 설계를 검토한다.
- 과거 rasp5 끊김 원인·orangepi 네 번째 trial 생성 경위는 로그 부재로 미확정이다. 추측으로 완료 처리하지 않는다.
- 원격 push는 승인됐지만 승인 자체는 실행 증거가 아니다. 자격 증명·대용량 원시는 제외한다.

클러스터 변경은 운영 담당 한 명이 직렬 수행한다. build/test와 측정 부하 창을 겹치지 않는다. 원시 위치: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/{intent,campaign/v2,perf,deployB}/`.

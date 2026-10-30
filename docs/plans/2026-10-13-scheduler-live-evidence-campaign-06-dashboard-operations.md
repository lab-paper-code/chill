# CHILL 캠페인 — 배포와 Dashboard 결과

- 상태: completed(2026-10-16 기능 연결). owner 측정 운영 API/CLI와 compact 조회를 연결했고 큰 FULL 응답의 502/504 차단을 해소했다.
- ChillServer `dad2aec` 배포 후 측정25개·채택3개, 실제 단건 UI 추론과 동일 9개 사건·Rpi UID/container·profile·native 완료를 대조했다. 단건 확인은 SLO 통계가 아니다.
- 같은 60초의 전체10노드 15442.435242944202J·활성Rpi 293.6367253111J를 API/화면에서 대조했다. 전력 원본 시각 부재와 acquisition timestamp 사용은 유지한다.
- `7b43c7c` 뒤 offset24 상세 링크는 단건 200으로 당시 actual.native·ACK와 일치했고 현재 상태 fallback은 없었다.
- 근거: `/tmp/chill-refactor-deploy-dad2aec-e2e.json` 및 당시 브라우저 스크린샷. 최종 9OFF·접수Ready·outstanding0이었다.
- 이 결과로 기존 버스트 실패·계산 한계·에너지 절감 및 자동 전력 최적화 미입증을 PASS로 바꾸지 않는다. 현재는 [E5 보고서](../reports/e5-evaluation-20261030.md)를 읽는다.

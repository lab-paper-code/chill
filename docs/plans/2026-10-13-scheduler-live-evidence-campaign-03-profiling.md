# CHILL 캠페인 — 정식 측정 결과

- 상태: completed(2026-10-13~14). `/measurement-worker` entrypoint와 producer/reader를 맞춰 native Run 및 evidence 재조회를 연결했다.
- rasp5 첫 Run `91ba053b-afc6-4af8-80f4-0623fc8c9a52`: 10.02555초, 56건, client-call p95 188.328473ms. backend service latency와는 다르다.
- genesis1/2/3·lattepanda·rasp5 q1에서 loaded-idle·K1·K2 각 3 trial, 노드별 9 trial Complete. 후속 q1/q2 10 profile·90 trial은 [제어 결과](2026-10-13-scheduler-live-evidence-campaign-04-measured-control.md).
- orangepi의 `/sys/fs/cgroup/memory.peak` 부재는 실패로 보존했으며 다른 Node 결과로 대체하지 않았다.
- 당시 전력 acquisition 시각 사용·sourceTimestampAbsent와 다른 상주 작업 조건을 유지한다. 운영 POOL 에너지 절감이나 SLO 보장의 근거로 확대하지 않는다.

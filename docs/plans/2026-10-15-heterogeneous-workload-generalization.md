# 이질 워크로드 일반화 — 결과 요약

- 상태: completed. `1132c50` 병합으로 mobilenet·SmolVLM2·PLC가 원시 요청부터 응답까지 공통 경로를 사용한다.
- 원시 샘플 1개, 분배 전 입력 계약 검사, 워크로드별 큐·요청 수 기준 정적 batching, 전처리/추론/후처리와 구간 시간 기록을 채택했다. 생성은 greedy이고 최대 토큰 수를 Run에 고정한다.
- 계약 권위: [워크로드 SRS](../spec/workloads/SRS.md), [일반화 설계](../design/serving/heterogeneous-workloads.md), [ADR 0021](../adr/0021-raw-sample-preprocessing-boundary.md).
- 호스트 잔여: edge-metrics `thermal-eagain.patch`의 원 저장소 반영·Helm 값 갱신은 별도이며 당시 `OnDelete` 임시 배포를 영구 완료로 세지 않는다.
- Jetson nvpmodel→nvidia-cdi-refresh boot 순서는 node-actuator가 관리한다(ADR 0015). 호스트 변경·rollback 원시는 `~/.cache/chill-assets/ops/`의 `orin-nano-nvpmodel/`, `orin-nano-thermal/`, `jetson-device-plugin/`, `pushed-images.md`.
- 수정 전 orin-nano CUDA 캠페인의 “25W” 기록은 실제 25W 증거가 아니다. 당시 jetsonx Smol fp32 CUDA 적재 메모리 고갈도 보존한다.
- 실제 혼합 평가와 한계는 [E5 결과](../reports/e5-evaluation-20261030.md), 후속은 [로드맵](2026-10-26-remaining-roadmap.md).

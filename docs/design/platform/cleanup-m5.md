# M5 정리 — 결과 요약

상태: completed(정리 계획·결정). 미완료 이행은 아래 D4와 [미해결 확인](node-agent.md#7-미해결-확인과-범위-밖)에 남긴다. [이행 결정](../architecture-rethink/README.md).

- **D1:** 측정 kind 여섯은 각각 유지한다. 미사용 `DeviceProfile`·`ClusterEnergyModel` 정리 근거와 정적 비교 경로 **D3**의 유지 이유는 [현행 결정](node-agent.md#61-측정-kind와-수동-동작).
- **D2:** NodeActuation CRD·Lease·phase를 Node label 요청·generation·독립 read-back으로 교체했다. 적용·writer·측정 무효 판정·호환 계약은 [노드 전력모드](node-agent.md#3-노드-전력모드-node-label과-generation)와 [저장 계약](node-agent.md#4-저장-계약-adr-0019)이 권위다.
- **D4, 미완료:** worker 수정 직전에 Go 바이너리와 runtime image를 분리한다. image·identity·선택 필드·배포 조건과 d 재측정의 운영자 결정은 [분리 결정](node-agent.md#62-worker-image-분리d4-미완료).
- **공유:** `e018057`은 routing annotation 필수·Workload 하나 이하 경계를 만들고 중복 공유 검사를 없앴다. 유지한 UID map·Dashboard·배포 전제는 [공유 경계](node-agent.md#5-인스턴스-공유-경계).
- **D5 / D-R7-5:** `bca2da3`은 재시작 때 관측 잠금을 기다린다. **D-R6-7:** `bfb4563`은 지난 시간의 모든 stream을 process마다 한 번 올린다. 실패·재시작 처리와 남은 대기 한계는 [관측 계약](node-agent.md#63-관측-잠금과-s3d5).
- 수동 prepare·remove·`staticRouting` 사용법은 [정적 배치 운영](../serving/scheduler-operations.md#정적-배치-사용법), 측정 전용 경로 제거는 [측정 API](../serving/measurement-api.md)가 맡는다.
- **Q1·Q2 확정:** agent는 수동 변경을 되돌리지 않으며, 기존 profile을 살리려고 실행 evidence `handoff`는 선택 필드로 유지한다. 근거는 현행 적용·저장 계약에 통합했다.
- E5 재검토: 다중 Workload 계획 탐색 시간·게이트웨이 간섭, 같은 노드 instance 간섭, 전환 중 SLO. 이 계획의 완료로 해결된 것으로 보지 않는다.

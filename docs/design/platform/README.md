# Platform Design

노드에서 도는 CHILL component와 노드별 사실(inventory, 전력 적용 대상, meter)의 출처를 다룬다.

| 문서 | 내용 |
|---|---|
| [Node agent와 inventory 자동화 (M2)](node-agent.md) | node agent DaemonSet, 노드별 scheduler 설정 필드의 자동 도출, NodeActuator에 남는 것, M3 준비 |
| [M5 정리](cleanup-m5.md) | 측정 kind, Node label과 generation으로 하는 전력모드 요청, Go 바이너리와 runtime image 분리, 인스턴스 공유 정리, 정적 배치 사용법, 재시작 잠금, S3 재설정 |

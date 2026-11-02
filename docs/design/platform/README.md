# Platform Design

노드 component와 inventory·전력 적용·meter의 출처 및 M5 유지 결정을 다룬다.

| 문서 | 내용 | 상태 |
|---|---|---|
| [Node agent와 inventory 자동화](node-agent.md) | inventory·DaemonSet·Node label/generation·저장 계약, 공유 경계와 미완료 worker 분리·확인 항목 | active |
| [M5 정리](cleanup-m5.md) | 완료된 정리 계획의 결정·commit 요약; 현행 계약은 node-agent로 통합 | completed |

## CPU 공유 배포

모든 CHILL 연산(플랫폼 controller/server, profiling/serving, 관측, 부하 생성)은
CPU request/limit과 명시 CPU pinning 없이 실행한다. Helm 기본값과 독립 operator
manifest는 메모리 request/limit을 유지하며, Helm은 custom resources의 CPU 키도
거절한다(0도 허용하지 않는다). ChillServer의 GOMAXPROCS는 limits.cpu에서
유도하지 않고 Go runtime 기본값을 따른다. Node affinity/selector는 장비 배치
조건이지 CPU affinity가 아니므로 유지한다. 실제 CPU/cgroup/thread 관측은 보존한다.

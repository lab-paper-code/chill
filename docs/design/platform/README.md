# Platform Design

[Node agent와 inventory 자동화](node-agent.md)는 노드 관측, 전력 요청과 독립
read-back의 소유 경계를 설명한다.

## CPU 공유 배포

CHILL의 controller, server, profiling, serving, 관측과 부하 생성은 CPU
request/limit과 명시 CPU pinning 없이 실행한다. 메모리 request/limit은 유지한다.
Helm은 custom resources의 CPU 키를 거절하며 값이 0이어도 허용하지 않는다.
ChillServer의 GOMAXPROCS는 Go runtime 기본값을 따른다.

Node affinity와 selector는 장비 배치 조건이다. 실제 CPU, cgroup과 thread 상태는
실행 관측으로 보존하며 CPU 할당 신원이나 배치 적격성으로 사용하지 않는다.

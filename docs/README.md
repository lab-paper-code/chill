# CHILL 문서

현재 상태는 [인수인계](handoff.md), 다음 작업은 [남은 로드맵](plans/2026-10-26-remaining-roadmap.md)에서 시작한다. 연구 성적과 조건은 [E5 보고서](reports/e5-evaluation-20261030.md)가 권위다.

| 디렉터리 | 역할·진입점 | 상태 |
|---|---|---|
| [intent](intent/README.md) | 연구 목표·사용자 선호·현행 결정과 근거 | active |
| [spec](spec/README.md) | 시스템 계약·분야별 SRS·요구사항 ID·평가 표의 권위 | active |
| [design](design/README.md) | 현행 구현 구조·API 소유·SRS 추적 | active |
| [adr](adr/README.md) | 선택한 결정·근거·결과와 폐기 상태 | active |
| [plans](plans/README.md) | 완료 결과 ledger와 미해결 과제 | active/completed/superseded |
| [reports](reports/README.md) | E5 확정 결과와 시연·구조 검토 결론 | completed |
| [postmortems](postmortems/README.md) | 사고 원인·재발 방지·남은 불확실성 | completed |
| [operations](operations/ops-agent-guide.md) | 운영 절차·권한·안전 경계 | active |

## 문서 유지 규칙

1. 의도·현행 결정·근거(측정·커밋·ADR)·API/필드/규칙/수치·미해결 항목을 보존한다. spec 표·SRS/AC ID와 golden query 원형은 바꾸지 않는다.
2. 같은 사실은 정본 한 곳에 두고 나머지는 상대 링크로 연결한다. 날짜별 누적 결정은 현행 표+최근30일 로그만 유지한다.
3. 완료 계획은 결과 요약≤15줄 또는 ledger 한 행으로 바꾸고 단계 절차·체크리스트·검증 로그를 제거한다. 삭제·통합 시 모든 링크를 갱신한다.
4. 제거된 기능은 removed @commit 한 줄과 git 이력으로 남긴다. 대체안·과거 버전 서사·장황한 예시를 지우되 애매하면 압축하고 수치·SHA를 만들지 않는다.
5. 설계≤300줄·README≤80줄·디렉터리 목차≤40줄을 목표로 분할보다 중복 삭제를 택한다. 사용자 미커밋 변경은 보존하고 상태/승인/실제 검증을 구분한다.

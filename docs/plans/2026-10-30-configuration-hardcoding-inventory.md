# 하드코딩 정리 — 인벤토리 결과

- 상태: completed(슬라이스2~5 코드). 세부 소비자·동결 계약은 [설계](../design/serving/configuration-hardcoding.md), 남은 ?10과 삭제 미승인 목록은 [계획 요약](2026-10-30-configuration-hardcoding.md)에 통합했다.
- 원 조사 go82+harness175+chart133=390행. 이동 표143행은 A26/A-미동결17/B5/C53/D32/제외10, 이동·기록48행이다. 정책 필드 수와 조사 행 수는 다르다.
- 원 유지·재분류 포함 현재 범위 A26/A-미동결17/B5/C89/D114/전환제외10이며 기존 제외122·열린?10은 별도다.
- cutover 뒤 슬라이스5의29행은 유지·구현, 새 제외0. 우회 수집12개 후보 중10개 제외·2개는 현재 API collector 시간 support와 적분 방법론(C)으로 유지했다.
- A/B만 기존 settings/CR/annotation/binding 소유자에게 이전하고 C는 상수·D는 기존 설정을 유지한다. 이름 없는 수식·sentinel·enum의 일괄 상수화는 하지 않는다.
- `sample_cgroups.py`의 외부 producer client 수집 및 `demo.py` offline import 회귀는 유지한다. 범위 제외는 파일 삭제 승인이 아니다.
- 근거: `58348057`…`30e3a6d6`, [ADR 0031](../adr/0031-configuration-ownership-and-freezing.md). 이전 Run의 duration tolerance 해석은 `75127da9`로 수정했다.

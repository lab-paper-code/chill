# CHILL 운영자 콘솔

상태: **active 설계**. 콘솔은 자동 제어의 근거를 검증하고 권한을 부여하며 예외에 안전하게 개입하는 운영 표면이다.

| 문서 | 내용·상태 |
|---|---|
| [운영 의미와 현재 단면](03-dashboard-and-cli.md) | active — 역할·운영 흐름/ID 추적, 현재 measurement/profile CLI 계약, ProfilingRun 조회·요청·증거 계보 경계 |
| [Dashboard SRS](../../spec/dashboard/SRS.md) | active — 웹 제품 범위·기능·안전·수용 요구사항의 권위 원본 |
| [Dashboard 구현 설계](../dashboard/README.md) | active — 웹 기술·화면·API 소유권과 구현 준비도 |
| [시스템 명세](../../spec/README.md) | active — 알고리즘·제어·SLO 관측 규칙의 권위 원본 |

규범 목록을 재정의하지 않는다. 명세에 확정되지 않은 Observe/Supervised/Autonomous, T1/T2/T3·D1/D2, 중단 규칙은 설계 의도이지 필수 제품 계약이 아니다.
Dashboard·CLI는 같은 권한·검증·비동기 결과 의미를 사용하되 각 현재 capability를 사실대로 표시한다. 첫 ProfilingRun 단면은 전체 Dashboard 요구사항이나 별도 BEO CLI 계약을 대체하지 않는다.

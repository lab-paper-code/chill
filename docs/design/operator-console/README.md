# CHILL 운영자 콘솔 설계 및 Use Case

웹서비스의 제품 요구사항은 [Dashboard SRS](../../spec/dashboard/SRS.md)를 따른다.
이 디렉터리는 운영 흐름과 구현 단면을 다루는 설계 자료다. 알고리즘과 제어 규칙의
정의는 `docs/spec/`을 따르며, 원본 명세가 확정하지 않은 고정 모드·트리거·중단
규칙을 웹서비스의 필수 요구사항으로 취급하지 않는다.

## 문서 구성

| 문서 | 책임 |
|---|---|
| [01-scope-and-principles.md](01-scope-and-principles.md) | 제품 범위, 핵심 개념, 설계 원칙 |
| [02-operating-model.md](02-operating-model.md) | 시스템 경계, 운영 역할·모드·안전 상태, 명세 층별 책임 |
| [03-dashboard-and-cli.md](03-dashboard-and-cli.md) | Dashboard와 CLI가 제공할 공통 운영 능력 |
| [04-use-cases.md](04-use-cases.md) | 운영자의 핵심 판단·승인·개입 흐름 |
| [05-requirements.md](05-requirements.md) | 기능 및 안전 요구사항의 규범적 목록 |
| [06-first-delivery-slice.md](06-first-delivery-slice.md) | 현재 구현에서 시작하는 첫 조회·요청·관찰 수직 단면과 의존성 순서 |

운영 흐름의 설계는 `01 → 02 → 03 → 04 → 05` 순서로 읽는다. 현재 코드에서 시작하는
첫 구현 경계는 그 다음 `06`에서 확인한다. 첫 단면이 Dashboard SRS의 전체 범위를
대신하지 않는다.

## 문서 원칙

- 같은 규범적 내용은 한 파일에만 둔다.
- 알고리즘 용어와 동작 순서는 `docs/spec/`을 권위 있는 원본으로 삼는다.
- Use Case와 요구사항 ID는 문서 세트 전체에서 유일하게 유지한다.
- 화면 배치, API 객체·필드, CLI 명령 문법은 여기서 확정하지 않는다.

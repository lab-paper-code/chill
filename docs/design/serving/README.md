# Serving Design

단일 HTTPS `cmd/chill-server`가 인증과 권한 확인, 카탈로그와 Workload 관리, 추론 접수와 분배, 물리 실행기 수명, 관측을 소유한다. CHILL이 목적 인스턴스를 선택하고 EdgeMesh가 해당 Pod로 전달한다. 계획기는 노드당 서빙 인스턴스 하나를 구성하며 인스턴스는 Workload 하나를 맡는다.

원시 JPEG와 UTF8 입력은 firstfloor의 전처리 module이 모델 입력으로 바꾼다. Serving과 Profiling은 같은 actor, persistent runner, 측정 이미지와 실행 조건을 사용한다. 수치 예측, 실행 준비 확인, 실물 반환, 실클러스터 수용은 각각의 근거로 판정한다.

## 설계 문서

| 문서 | 책임 |
|---|---|
| [워크로드 관리](workload-management.md) | CRUD, 불변 정의, 인증, 접수, 변경과 삭제 |
| [스케줄러 구현](scheduler-implementation.md) | 책임 경계, snapshot, 실물 장부, 예약과 전환 |
| [실행기와 측정](scheduler-runtime-measurement.md) | 요청 시간과 수명, 배칭, 실행 조건과 프로파일 |
| [스케줄러 API](scheduler-api.md) | 설정, 권한, 현재 상태와 판단 기록 조회 |
| [설정 소유권](configuration-hardcoding.md) | 설정 수락, 불변 입력 동결과 명시적 정책 |
| [이기종 워크로드](heterogeneous-workloads.md) | 원시 표본, 전처리, Dataset, 다중 모델 파일 |
| [워크로드별 풀](workload-pools.md) | 자동제어, 활성 풀, 측정 노드와 전체 전력 범위 |
| [측정 노드 승인](measurement-admission.md) | 노드별 순서, 거절, 단일 writer와 정상 반환 |
| [측정 API](measurement-api.md) | 동작 비용, 디스패치 점유, 재사용과 커버리지 |
| [관측과 증거 API](observability-split.md) | 요청과 catalog 기록, 보존, 원시 내보내기 |
| [피드백 기반 계획기](analytic-planner.md) | 시간 회계, 후보 평가, 탐색과 에너지 비교 |
| [인스턴스 전달과 수동 작업](scheduler-operations.md) | Pod-bound Service, 원자 작업과 운영 수용 |
| [프로파일과 서빙 경로 동등성 확인](profile-serving-equivalence-plan.md) | 대표 측정의 운영 수용 절차 |
| [프로파일 측정 세션](profile-session.md) | 프로파일의 내용, 서빙 동작으로 하는 측정, 전력 모델 |

## 초기화와 준비 상태

TLS와 HTTP 조립 후 listener를 열고 scheduler refresh와 Workload 복원을 백그라운드에서 수행한다. 완성된 handler를 게시하기 전 `/api/v1` 요청은 `503 CoverageWarming`과 `Retry-After: 2`를 받는다. Dashboard와 `/healthz`는 먼저 응답한다. 초기화 실패는 서버 종료로 이어지며 부분 상태에서 Tick이나 관리 작업을 실행하지 않는다.

배포의 readiness probe는 `/healthz`로 listener 도달 가능성을 확인한다. `/readyz`는 관측 기록 건강 상태를 나타낸다. 실제 추론 준비는 정확한 Pod와 container 신원, 측정 프로파일, native Ready와 정책 ACK로 확인한다. 취소되거나 기한이 지난 probe 응답은 준비 근거로 쓰지 않는다.

요구사항 원본은 [시스템 명세](../../spec/README.md), [스케줄러 명세](../../spec/scheduler/SRS.md), [워크로드 명세](../../spec/workloads/SRS.md)다.

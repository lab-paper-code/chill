# 첫 운영 수직 단면

## 상태와 목적

이 문서는 운영자 콘솔의 첫 구현 경계를 정의한다. 화면 배치, 프런트엔드 기술,
별도 API 서버의 도입 여부와 새로운 CRD 형태는 확정하지 않는다.

첫 단면의 목적은 Kubernetes 리소스 CRUD 화면을 만드는 것이 아니다. 운영자가 현재
CHILL이 알고 있는 사실을 확인하고, 측정 의도 하나를 제출하고, 그 의도가 CHILL의
책임 경계를 따라 어디까지 진행됐는지 사실대로 추적하게 하는 것이다.

```text
현재 지식 조회
    -> 단일 ProfilingRun 요청
    -> 의도 동결과 실행 상태 관찰
    -> 증거와 admission 계보 추적
```

완성형 Scheduler의 추천, SLO·에너지 판단, 정책 dry-run과 전환 감독은 이 단면의
선행조건이 아니다. 해당 능력은 권위 있는 decision·profile·transition 계약이 생긴
뒤 같은 Console에 추가한다.

## 현재 구현 상태

첫 번째 조회 계약인 `DeviceClass.spec + live Nodes` inventory projection은
`internal/deviceclass`에 구현되어 있다. selector 매칭, Node Ready의 tri-state,
`Node.spec.unschedulable`과 원본 identity를 보존하며 어떤 status에도 결과를 저장하지
않는다.

아직 이 projection을 소비하는 CLI, Dashboard 또는 query adapter는 없다. 따라서 현재
완료된 것은 조회 의미와 순수 조합 로직이며, 운영자가 호출할 수 있는 read-only 수직
단면은 다음 구현 단계다.

## 현재 구현의 사실 경계

첫 구현은 다음 상태보다 강한 주장을 만들지 않는다.

| 영역 | 현재 권위 있는 사실 | 아직 없는 사실 |
|---|---|---|
| CHILL 설치 | `ChillSystem`의 구성요소 availability Conditions | 서비스 SLO, 에너지 제어와 Scheduler 정상성 |
| 디바이스 인벤토리 | `DeviceClass.spec`과 selector에 일치하는 현재 Kubernetes Nodes | `DeviceClass.status`의 멤버 목록·Ready 집계, CHILL 실행 eligibility |
| 모델 카탈로그 | `ModelSpec`의 artifact digest와 execution path 요구사항 | 운영 승격 profile, 물질화된 compatible-class index |
| 프로파일 요청 | immutable `ProfilingRun.spec`, frozen intent digest와 intent record | 현재 연결된 controller가 생성·관찰하는 측정 Job |
| 실행 결과 | `ProfilingRun.status`가 예약한 Job, actual execution, evidence, admission 경계 | Job 생성부터 evidence admission까지 연결된 end-to-end reconcile 경로 |
| 상위 제어 | 빈 `DeviceProfile`·`ClusterEnergyModel` API scaffold | 전력 곡선, `b_sat`, current/optimal/target 구성, decision과 transition 상태 |

`ChillSystem.Ready`는 필요한 CHILL 운영 구성요소의 availability다. Console은 이를
전체 serving 정상이나 개입 불필요로 번역하지 않는다.

`DeviceClass` 멤버십과 Kubernetes Node 상태는 조회 시점에 결합한다. Console 편의를
위해 Node 이름, Ready 수 또는 schedulability를 `DeviceClass.status`에 복제하지 않는다.

## 첫 사용자 흐름

### 1. 현재 지식을 조회한다

운영자는 첫 화면이나 첫 CLI 조회에서 다음을 구분할 수 있어야 한다.

- 어떤 CHILL 구성요소가 활성화됐고 Kubernetes 관점에서 available한가
- 어떤 `DeviceClass`가 선언됐고 현재 어떤 Nodes가 selector에 일치하는가
- 각 Node의 Kubernetes Ready·schedulability가 무엇인가
- 어떤 `ModelSpec`과 immutable artifact·execution path가 선언됐는가
- 어떤 `ProfilingRun`이 존재하고 현재 구현이 실제로 관찰한 마지막 단계는 무엇인가

조회 결과는 원본의 종류, 이름, UID 또는 generation과 관측 시각을 통해 원본으로
내려갈 수 있어야 한다. 여러 원본을 결합한 결과는 조회 projection이며 별도 권위
상태가 아니다.

### 2. 측정 의도 하나를 요청한다

Dashboard의 guided form과 CLI는 동일한 `ProfilingRun.spec`을 만든다. 사용자는 Job,
Pod, Evidence Store endpoint 또는 런타임 내부 명령을 지정하지 않는다.

요청 표면은 최소한 다음을 요청 전에 보여준다.

- 선택한 ModelSpec, artifact execution path, DeviceClass와 target Node
- batch, warm-up, 측정·전력 관찰 구간과 CPU request
- 제출되는 immutable spec
- Kubernetes schema가 즉시 검증할 항목과 이후 Profiler가 사실을 해석할 항목의 구분

요청 접수는 실행 완료가 아니다. API create 성공, intent 동결, Job 실행, evidence
verification과 admission은 서로 다른 단계로 표현한다.

### 3. 진행 상태와 계보를 관찰한다

`ProfilingRun` 상세는 구현된 단계만 활성 상태로 표현한다.

```text
Requested
  -> Intent Frozen
  -> Execution
  -> Evidence Verified
  -> Single-Run Admission
```

현재 controller가 의도 동결까지만 연결된 배포에서는 이후 단계를 장시간 `Pending`으로
꾸미지 않는다. 해당 capability가 아직 제공되지 않음을 명시한다. 실행 경로가 연결된
후에는 상위 단계마다 권위 있는 status와 원본 Job·Pod·evidence reference를 사용한다.

실행 성공, evidence verification과 scientific admission을 하나의 성공 배지로 합치지
않는다. 단일 Run admission 또한 운영 가능한 `DeviceProfile` 승격과 동일하지 않다.

## 조회와 변경의 의존성 구조

```text
권위 있는 원본
  +-- ChillSystem / DeviceClass / ModelSpec / ProfilingRun
  +-- Kubernetes Node / Job / Pod / Event
  `-- immutable evidence reference
             |
             v
조회 시점 projection
  - 원본을 결합하고 출처와 관측 시각을 보존
  - 제어 판단을 새로 만들지 않음
  - 별도 영속 truth를 소유하지 않음
             |
             v
공통 운영 의미
  - list / inspect / request / watch
  - 동일한 validation과 비동기 단계 의미
        |                    |
        v                    v
       CLI                Dashboard

변경 경로:
CLI 또는 Dashboard
  -> Kubernetes API의 ProfilingRun
  -> Profiler controller
  -> owned Job / execution plane
  -> ProfilingRun status와 evidence reference
```

여기서 `조회 시점 projection`은 논리적 책임이다. CLI 내부 조합, Dashboard backend의
read-through 조합 또는 이후 별도 query service 중 무엇으로 배치할지는 아직 결정하지
않는다. 어느 형태든 원본을 대체하거나 Scheduler 판단을 클라이언트에서 계산해서는
안 된다.

Dashboard가 Browser의 Kubernetes 접근을 중계해야 하는지, 별도 CHILL API가 필요한지는
인증, RBAC, watch, evidence 접근과 다중 원본 조회 요구를 확인한 뒤 결정한다. 이 transport
선택은 `ProfilingRun`의 의미와 controller 책임을 바꾸지 않는다.

## 첫 단면의 화면·명령 단위

정확한 이름과 문법은 구현 시 확정하지만, 첫 단면은 다음 네 의미 단위로 제한한다.

| 의미 단위 | 제공하는 답 | 변경 권한 |
|---|---|---|
| System | 설치된 CHILL 구성요소가 available한가 | 없음 |
| Inventory | 어떤 class·Node·artifact·execution path가 존재하는가 | 없음 |
| Profiling Runs | 어떤 측정 의도가 요청됐고 어디까지 진행됐는가 | 새 Run 요청 |
| Evidence Passport | 한 Run의 intent·실제 실행·증거·admission 계보가 무엇인가 | 없음 |

빈 `DeviceProfile`과 `ClusterEnergyModel`은 리소스 목록을 채우기 위해 노출하지 않는다.
실제 생산자와 소비자 계약이 생길 때 Scientific Readiness Matrix와 Decision Microscope의
입력으로 추가한다.

## 구현 게이트와 순서

읽기 경로와 실행 경로는 서로 다른 준비도를 갖는다.

1. **조회 의미 고정**: 권위 있는 원본과 projection 규칙을 fixture 기반으로 검증한다.
2. **read-only 수직 확인**: CLI 또는 작은 진단 도구로 System, Inventory와 기존 Run
   조회가 중복 status 없이 성립하는지 확인한다.
3. **ProfilingRun 실행 경로 완성**: Profiler가 frozen intent에서 owned Job을 생성하고,
   실제 Job·Pod·Node와 evidence 결과를 상위 status에 투영하는 책임을 연결한다.
4. **공통 요청·관찰 계약 확인**: create, inspect, watch의 validation·권한·종료 의미를
   CLI와 Dashboard가 공유할 수 있는지 검증한다.
5. **Dashboard transport 결정**: Browser 인증과 RBAC, watch, evidence 접근 요구를
   근거로 Kubernetes proxy와 별도 CHILL API 중 필요한 경계를 선택한다.
6. **Dashboard 수직 단면**: 같은 원본과 요청 계약으로 Inventory, Run request와
   Evidence Passport를 제공한다.

2단계는 3단계를 기다리지 않아도 되지만, 실행 완료를 약속하는 `profile request` UX는
3단계 없이 완료로 간주하지 않는다. 5단계의 transport 선택은 화면 개발 편의가 아니라
권한과 데이터 경계에서 결정한다.

## 명시적 비목표

첫 단면은 다음을 구현하거나 암시하지 않는다.

- current/optimal/target 구성과 Scheduler recommendation
- SLO·에너지 전체 정상 판정 또는 단일 health score
- profile campaign orchestration과 `b_sat` derivation
- 저수준 Job·Pod·batch·routing weight·power mode 직접 편집
- UI에서 계산한 compatibility, eligibility 또는 admission
- Console 전용 decision·inventory truth database
- 정책 dry-run, 승인, override, break-glass와 transition control

이후 능력은 완성형 운영 계약에 남아 있다. 해당 producer와 권위 있는 API가 생기면
Evidence Passport의 계보 위에 Scientific Readiness Matrix, Decision Microscope와
Transition Flight Recorder를 순서대로 확장한다.

## 첫 단면의 완료 조건

- 운영자가 controller 로그 없이 현재 지원되는 profiling 단계를 구분한다.
- class별 Node 조회가 `DeviceClass.status` 복제 없이 재현된다.
- Dashboard와 CLI의 Run 요청이 동일한 `ProfilingRun.spec`과 validation 의미를 사용한다.
- API 접수, intent 동결, 실행, evidence와 admission을 서로 다른 상태로 표현한다.
- 모든 결합 조회가 권위 있는 원본과 관측 시각으로 추적된다.
- 미구현 capability와 `Unknown`을 정상·실패·무기한 진행 상태로 오해시키지 않는다.

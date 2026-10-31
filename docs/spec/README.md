# CHILL 시스템 명세

상태: 2026-10-30 **2차 검토(fusion: 패널 3모델+판정) 반영**(설정 원칙, 구현 전).

CHILL은 이기종 워커노드 POOL에서 추론 서비스를 제공하는 시스템이다.
**워크로드의 지연 SLO를 우선 만족시키면서, 현재 구성에서 다음 구성으로 전환하는
비용과 전환 이후 Serving 비용을 함께 고려해 에너지 사용량을 최소화한다.**
요청 부하와 실제 구성 상태를 관측하고, 노드 배치·전력모드·라우팅·배칭을 조정한다.

이 디렉터리는 CHILL의 목표와 요구사항을 정하는 기준이다. `docs/design`은 이를
구현하기 위한 계획이며 수정·교체·삭제할 수 있다. 기존 API, 코드, 구현 계획이
이 명세의 요구사항을 대신 정하지 않는다. 여기서 정하지 않은 알고리즘이나
구현 방법은 확정된 것으로 취급하지 않는다.

CHILL 서빙의 기본 실행기로 **firstfloor**를 채택한다. firstfloor의 요청 큐·배처와
모델 runner를 재사용하고, CHILL 스케줄러는 evidence와 현재 부하·SLO에 근거한
구성·라우팅·배칭 정책 및 요청별 시간 정보를 제공한다. firstfloor 내부 배처는
CHILL 수신 이후 총 경과 시간과 현재 큐·실행 상태에 그 정책을 적용해 배치 구성과
추가 대기·실행 시점을 판단한다. 각 배치의 실행 판단은 firstfloor가 소유한다.
프로파일링과 Serving은 같은 firstfloor/backend 실행 조건을 사용한다.
실행기 채택과 실제 통합·요구사항의 검증 상태는 각각 기록한다.

측정·판정 값은 설정이며 선택한 실제값과 출처가 실행 기록에 남아야 한다.
운영 중 serving 용량 정책은 기존 설정 API로 선택하고 판단 당시 적용값은 Decision.Settings로 해석한다. PUT 시도와 적용 증거를 구별하며 첫 판단 전 모든 적용 이벤트 복원이나 별도 저장소는 요구하지 않는다. 측정 CI는 측정 CR, 재사용 정책은 요청 annotation이 소유한다.
실제 환경 고유값만 기존 배포 설정·실험 환경 입력으로 제공한다. 이미 설정된 값과 내부 패키징/예제 계약은 유지한다. 저장 계약은 ADR0030의 v1alpha5·과학 원시 보존과 ADR0019의 영향 작업용 객체 정리를 구별한다.

## 워크로드와 SLO

워크로드는 서비스 식별자, 기존 모델 등록정보, 전처리와 지연 SLO를 연결하는 서비스 객체다.
모델, 전처리, 비용 구조가 서로 다른 이기종 서비스들은 하나의 서빙 경로로 동시에
제공되며, 각 워크로드는 자기 활성 POOL에서 서빙된다.

| 항목 | 의미 | 예시 |
|---|---|---|
| 워크로드 식별자 | 조회와 추론 요청에서 사용할 서비스 식별자 | 워크로드 A |
| 모델 참조 | 기존에 등록한 모델과의 연결 | 등록된 모델 X |
| 전처리 참조 (필수) | 원시 표본을 모델 입력으로 바꾸고 추론 결과를 응답으로 바꾸는 등록된 전처리 | 등록된 전처리 P |
| 데이터셋 참조 (선택) | 프로파일링과 평가 부하에 사용할 원시 표본을 담은 등록된 데이터셋 | 등록된 데이터셋 D |
| 지연 SLO | 지연시간 백분위와 목표 시간 | p99 ≤ 100ms, p95 ≤ 50ms |

같은 모델을 참조하면서 서로 다른 SLO를 가진 워크로드를 정의할 수 있다.
SLO는 p99나 p95로 고정하지 않고 지정한 백분위와 `n ms`로 표현한다.
요청 수명은 워크로드가 정하며 SLO 목표 시간 이상이다.

추론 요청은 **워크로드 식별자 + 독립적으로 완료할 수 있는 원시 표본 1개**다.
하나의 추론에 함께 필요한 여러 원시 필드는 표본 1개를 구성하며, 모델 입력으로의
변환은 워크로드의 전처리가 수행한다. 독립적인 표본이 여러 개면 각각 별도 요청으로
보낸다. 모델·SLO 정의를 요청마다 복사하거나 요청자가 덮어쓰지 않고, CHILL이
등록된 워크로드를 확인한다.

CHILL은 같은 워크로드의 호환되는 요청들을 내부 실행 배치로 묶을 수 있다.
응답과 SLO는 각 요청에 적용한다. SLO 지연은 CHILL이 요청을 수신한 시점부터
해당 추론 결과의 응답 반환이 완료될 때까지의 경과 시간이다. 유입 대기,
분배·전달, 노드 대기·실행, 결과 반환을 포함하며, 인스턴스 전달이나 내부 배칭으로
시작 시각을 재설정하지 않는다.
외부 클라이언트에서 관측한 왕복 지연은 별도로 구분한다.

SLO와 에너지를 임의의 가중치로 섞지 않는다. SLO를 만족할 수 있는 운영 선택 중
에너지 비용이 가장 유리한 선택을 찾는다. 처리량은 부하를 감당할 수 있는지
판단하는 프로파일·제어 메트릭이지, 독립적인 최적화 목표나 주 평가 지표가 아니다.

갑작스러운 버스트, 전환 소요 시간, 전체 POOL의 처리 한계 때문에 SLO를 지킬 수
없는 상황도 있다. 이는 실행 오류와 구분해 관측한다. 스케줄러는 가용 자원으로
SLO 충족을 우선하는 대응을 계속하며, 만족 가능한 조건과 위반이 발생한 조건을
실험으로 확인한다. 모든 부하에서의 무조건적인 SLO 보장을 주장하지 않는다.

### SLO 관측과 집계

CHILL은 워크로드별 요청의 시간 정보와 실제 처리 결과를 보존하고, 요청 수와
요청별 지연 분포·백분위를 계산한다. 완료·실패·거절·취소와 관측 시점의 미완료
상태를 구분해 기록한다. 실험의 성패와 관측 결과의 해석은 분석자가 담당한다.
스케줄러는 워크로드의 SLO를 구성 선택·라우팅·배칭의 목표로 사용한다.

운영 관측은 시간 기반 이동 구간으로, 실험 분석은 지정한 실험 구간으로 집계한다.
집계 구간의 길이는 관측·분석 설정으로 기록한다. 제어 주기와 비용 비교 기간은
각각의 제어 설정으로 관리한다. 집계 방식은
[Prometheus의 시간 구간별 분포 조회](https://prometheus.io/docs/practices/histograms/)를 참고한다.

측정은 요청량과 표본 수에 관계없이 동일한 절차를 적용한다. 지연 표본이 없는
구간에는 표본 수 0과 값이 없는 지연 통계를 기록한다. 원시 요청 기록과 집계
조건을 함께 보존하여 같은 구간의 통계를 재계산할 수 있게 한다.

요청의 timeout·취소·큐 제한은 [라우팅·배칭 명세](03_routing.md)의 Serving
처리 계약을 따른다.

## 워크로드 조회와 관리

- 서비스 사용자는 지원하는 워크로드의 목록과 상세를 조회하고, 식별자를 지정해
  추론을 요청할 수 있다.
- 조회 결과는 워크로드 식별자, 제공 모델, SLO, 지원 입력 형식과 요청 가능한
  상태를 알려준다. 사용자가 내부 노드 배치나 실행 배치 크기를 알아야 할 필요는 없다.
- 운영자는 모델 등록정보, 전처리, 데이터셋과 워크로드를 생성, 조회, 수정, 삭제하고
  모델 연결과 SLO를 관리할 수 있다. 현재 서빙 배치는 POOL의 운영 상태에서 조회한다.

모델과 워크로드의 갱신 및 삭제는 [워크로드 관리 SRS](workloads/SRS.md)를 따른다.
리소스 표현, API 경로와 적용 지점은 구현 설계에서 정한다.

## 웹 기반 조회와 제어

`chill-dashboard`는 CHILL의 조회·제어 API를 사용자 작업 흐름으로 연결하는 별도
웹서비스다. 모델·워크로드뿐 아니라 시스템 상태, 자원·POOL, 측정과 Evidence,
구성 판단과 전환, 관측·실험을 다룬다. 웹서비스의 기능, 권한, API 래핑 경계와
수용 기준은 [Dashboard SRS](dashboard/SRS.md)를 따른다.

## 노드 POOL과 구성

- **전체 POOL:** CHILL이 사용할 수 있는 클러스터의 전체 워커노드 범위.
- **활성 POOL:** 한 워크로드를 현재 서빙하는 노드 집합. 워크로드마다 하나씩 있다.
- **측정 POOL:** 프로파일링, 전환 비용과 디스패치 점유 측정을 수행하는 노드 집합.
- **가용 노드:** 활성 POOL과 측정 POOL 바깥의 준비된 노드.
- **구성:** 워크로드별 활성 POOL의 노드, 노드별 전력모드, 서빙 파드별 모델과
  실행 경로 및 자원 할당, 요청 분배와 배칭을 묶은 운영 상태.

노드는 측정 POOL, 활성 POOL, 가용 노드 중 한 상태에 있다. 노드마다 서빙 인스턴스는
하나이므로 활성 노드는 그 인스턴스를 둔 워크로드의 활성 POOL 하나에 속한다.
측정 POOL의 노드는 측정 POOL에만 속한다. 소속은 구성 선택, 전환과 측정 요청에
따라 바뀐다. 한 활성 POOL에서 다른 활성 POOL로 노드를 바로 옮기는 것은 전환 하나로
실행하고, 그 비용을 후보 비교에 포함한다.

자동제어는 워크로드별로 켜고 끈다. 자동제어가 켜진 워크로드에 대해 스케줄러는
가용 노드, 그 워크로드의 활성 POOL 노드, 같은 등록 모델을 참조하는 다른 워크로드의
활성 POOL 노드 중 해당 워크로드의 측정 근거가 있는 노드를 후보로 삼는다. 그리고
켜진 워크로드 전체와, 공유 노드의 전력모드 변경에 영향받는 꺼진 워크로드의 SLO를
만족하면서 전체 POOL의 에너지가 가장 작은 구성을 고른다. 첫 구성도 같은 방식으로
선택해 만들고, 이후 부하에 따라 주기적으로 다시 판단한다. 켜진 워크로드는 인스턴스를
하나 이상 유지한다. 측정 근거가 있는 노드가 없는 켜진 워크로드는 측정 근거 대기
상태로 표시하고, 다른 워크로드의 판단은 계속한다. 자동제어가 꺼진 워크로드는 마지막
구성을 유지하며, 운영자의 수동 변경은 그 워크로드에만 적용한다.

실행 경로는 해당 노드의 CPU·GPU 등 연산 자원과 런타임을 포함한다. 선택은 그
조건에 맞는 프로파일을 근거로 한다. 가용 노드도 전력을 소비하는 노드로 다룬다.
스케줄러가 그 노드의 전력모드를 가장 낮은 모드로 정한다. 그 전력 소비는 전체 POOL
에너지 보고에 포함하고, 후보 비교에서는 후보가 그 노드 상태를 바꿀 때 그 차이를 포함한다.

CHILL은 자신이 배치한 파드만 관리한다. 다른 파드가 있는 노드도 가용 노드로 사용하고
전력모드를 바꾸며, 그 노드의 전력은 노드 전체 측정값을 그대로 사용한다.

### 노드 자원과 모델 인스턴스

서빙 파드는 할당된 연산 자원으로 하나의 모델 실행 인스턴스를 제공한다.
파드마다 준비된 모델 실행기 하나를 유지하며, 한 실행 배치씩 순차 처리한다.
전체 모델 인스턴스 수는 서빙 파드의 추가·제거로 조절한다. Pod 내부 실행기 수는
하나로 유지한다.
필요한 CPU·GPU·메모리를 할당할 수 있으면 한 노드에 여러 서빙 파드를 배치할 수 있다.
스케줄러는 측정한 실행 구성과 현재 부하·SLO를 근거로 서빙 파드 수·배치와 파드별
자원 할당을 결정하며, 기존 인스턴스의 자원 변경도 대상으로 삼는다.
전력모드는 인스턴스별 설정이 아니라 같은 노드의 인스턴스들이 함께 영향을 받는
호스트 설정이다.

메모리는 프로파일의 사용량을 근거로 배치 가능 여부를 판단하는 제약으로 사용하며,
할당량을 바꿔 성능·비용을 최적화하는 독립 변수로 두지 않는다. CPU 추론의 스레드
설정은 CPU 할당량에 연동하고, 각 인스턴스는 한 번에 실행 배치 하나씩 처리한다.
순간 사용률이 낮은 자원을 미할당 자원으로 간주하지 않는다.

- 서로 다른 서빙 파드의 연산 자원 할당은 겹치지 않게 한다. CPU는 분리된 CPU
  할당, GPU는 물리 장치 단위의 비공유 할당을 기준으로 한다. 같은 GPU를 여러
  모델 파드가 시간 분할하는 형태는 지원 범위에 넣지 않는다.
- GPU 파드의 CPU·메모리도 할당에 포함한다. 분리된 자원이 있으면 CPU 파드와
  GPU 파드가 같은 노드에서 공존할 수 있다.
- 모델 산출물, 런타임, 전처리 산출물과 데이터셋이 모두 같은 워크로드들은 같은
  측정 프로파일을 쓴다. 인스턴스 하나는 워크로드 하나의 요청을 처리한다.

자원 할당 가능성은 배치 후보의 조건이지 SLO 충족의 보장은 아니다. 자원 분리로
공유 메모리 대역폭·전력·열의 영향까지 사라진다고 가정하지 않는다. 실제로 사용할
할당 조건에 맞춰 프로파일링하고, 공존 시 오차는 우선 관측한다. 모든 공존 조합의
사전 측정이나 별도 간섭 예측·제어기를 기본 요구사항으로 두지 않는다.

## 시스템 흐름

```text
운영자의 측정 요청
    → 측정 POOL로 노드 이동 → 프로파일링 → 가용 노드로 반환
    → Evidence Store: 측정 조건·원시 관측·계산 결과
    → 스케줄러: 프로파일 + 현재 부하 + 실제 구성 + 워크로드 SLO
    → 구성 선택과 전환 실행
    → Serving: 요청별 인스턴스 분배 → 워크로드별 큐 → firstfloor 배칭
      → [전처리 → 추론 → 후처리] → 각 요청에 결과 반환
    → 부하·지연·전력·실행 상태 관측 → 다음 판단
```

프로파일링은 판단 근거를 생산하고, Evidence Store는 그 근거를 보존한다.
스케줄러는 근거를 현재 상황에 적용해 결정을 내리고, 실행 계층은 그 결정을
수행한다. 목표 구성을 선언한 것과 실제 전환이 완료된 것은 구분한다.

## 상세 명세

| 문서 | 답하는 질문 |
|---|---|
| [워크로드 관리 SRS](workloads/SRS.md) | 워크로드를 어떻게 등록하고 조회하며 변경과 삭제를 적용하는가? |
| [데이터셋과 전처리 관리 SRS](catalog/SRS.md) | 전처리와 데이터셋을 어떻게 등록하고 조회하며 변경과 삭제를 적용하는가? |
| [Dashboard SRS](dashboard/SRS.md) | 사용자가 CHILL 전반을 웹에서 어떻게 조회·제어하고 결과와 근거를 확인하는가? |
| [스케줄러 SRS](scheduler/SRS.md) | 어떤 입력으로 무엇을 판단하고, 실행과 피드백에 어떤 결과를 제공해야 하는가? |
| [프로파일링과 Evidence](00_measurement.md) | 어떤 판단 근거를 측정하고 보존하는가? |
| [구성 선택과 비용](04_configuration_selection.md) | 현재 상태에서 유지 또는 전환 중 무엇을 선택하는가? |
| [라우팅과 배칭](03_routing.md) | 요청을 어느 인스턴스에 보내고 어떻게 묶어 실행하는가? |
| [전환 실행과 피드백](05_transition_control.md) | 결정을 어떤 동작으로 실행하고 결과에 어떻게 대응하는가? |

## 실클러스터 검증과 평가

외부 요청 클라이언트가 워크로드별 부하 시나리오를 재생하고, 실제 클러스터에서
프로파일 기반 판단부터 배포·전력모드·라우팅·배칭까지 연결되는 흐름을 검증한다.
부하의 기본 단위는 워크로드별 inputs/s이며 요청률과 같다. 독립 입력 100개의
동시 유입은 같은 도착 시각을 가진 요청 100개로 표현하고, 각 요청의 지연을 기록한다.

각 평가 실행은 시작 전에 답하려는 질문, 배포된 시험 대상 구성요소의 실제 신원,
제어 설정, 대상 워크로드, 부하 시나리오(요청 trace와 씨앗), 시작 상태,
단계(자율 운영·고정 구성·제어요소 비활성 등)와 단계 사이 비교 관계, 장애 주입 계획을 정의한다.
실행이 시작된 정의는 바꾸지 않으며 정정은 새 정의로 한다.
정의 하나만으로 같은 조건의 실행을 다시 구성할 수 있어야 한다.

실험을 수행하는 주체는 시험 대상인 서빙·제어 구성요소와 생명주기와 자원을 공유하지 않고,
시험 대상에는 공개 인터페이스로만 작용한다. 시험 대상을 배포·변경하지 않으며,
실제 신원이 정의와 다르면 부하를 시작하지 않는다.
같은 자원 풀에서는 한 번에 한 실험만 수행한다.

실행 기록에는 단계별 수행 결과와 실패 사유를 남기고, 작업 완료·측정 판정·정리 완료를
구분한다. 작업 완료는 평가 통과를 뜻하지 않는다.
측정을 자동으로 다시 하지 않는다.

| 시나리오 | 관찰할 내용 | 1회차 단계별 수신 p95·결과 수·D1 (MNV/PLC/Smol) | 2회차 단계별 수신 p95·결과 수·D1 (MNV/PLC/Smol) | 3회차 단계별 수신 p95·결과 수·D1 (MNV/PLC/Smol) | 1회차 CHILL 전체/활성 POOL·오차·coverage | 2회차 CHILL 전체/활성 POOL·오차·coverage | 3회차 CHILL 전체/활성 POOL·오차·coverage | 1회차 대응 고정 E5-S 전체/활성 POOL·오차·coverage | 2회차 대응 고정 E5-S 전체/활성 POOL·오차·coverage | 3회차 대응 고정 E5-S 전체/활성 POOL·오차·coverage | 1회차 Δ 고정−CHILL 전체 POOL | 2회차 Δ 고정−CHILL 전체 POOL | 3회차 Δ 고정−CHILL 전체 POOL | 1회차 근거(Run ID, 원시 경로) | 2회차 근거(Run ID, 원시 경로) | 3회차 근거(Run ID, 원시 경로) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 일정 부하 | 유지되는 구성의 요청 지연 분포와 에너지 | E5-1: MNV: p95 395.003 ms; 계획 57,495; 완료 47,268 / 실패 10,225 (BackendUnavailable=9,092; RequestTimeout=1,133) / 거절 0 / 취소 0 / 미완료·불명 2; D1 FAIL (45754/54621)<br>PLC: p95 17.519 ms; 계획 20,605; 완료 20,605 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (20320/19575)<br>Smol: p95 3,280.336 ms; 계획 301; 완료 301 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (301/286)<br>E5-6: MNV: p95 394.530 ms; 계획 114,558; 완료 105,294 / 실패 9,264 (BackendUnavailable=8,417; RequestTimeout=847) / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (103903/108831)<br>PLC: p95 15.905 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (41150/39542)<br>Smol: p95 3,290.431 ms; 계획 617; 완료 617 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (617/587) | E5-1: MNV: p95 299.334 ms; 계획 57,495; 완료 57,199 / 실패 296 (BackendUnavailable=272; RequestTimeout=24) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (55983/54621)<br>PLC: p95 16.278 ms; 계획 20,605; 완료 20,605 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (20381/19575)<br>Smol: p95 2,557.590 ms; 계획 301; 완료 301 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (301/286)<br>E5-6: MNV: p95 147.477 ms; 계획 114,558; 완료 114,558 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (114558/108831)<br>PLC: p95 16.079 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (41257/39542)<br>Smol: p95 1,916.040 ms; 계획 617; 완료 617 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (617/587) | E5-1: MNV: p95 274.882 ms; 계획 57,495; 완료 57,428 / 실패 67 (BackendUnavailable=67) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (56650/54621)<br>PLC: p95 17.094 ms; 계획 20,605; 완료 20,605 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (20377/19575)<br>Smol: p95 3,276.135 ms; 계획 301; 완료 301 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (301/286)<br>E5-6: MNV: p95 305.084 ms; 계획 114,558; 완료 114,458 / 실패 100 (BackendUnavailable=93; RequestTimeout=7) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (114099/108831)<br>PLC: p95 16.952 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (41226/39542)<br>Smol: p95 1,962.304 ms; 계획 617; 완료 617 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (617/587) | E5-1: 173,062.740 / 148,610.058 J; ±516.514 J; gapΣNode=37.043s, max=2.999s, maxNode=0.645%<br>E5-6: 342,989.174 / 307,660.371 J; ±328.993 J; gapΣNode=20.693s, max=4.811s, maxNode=0.238% | E5-1: 179,783.315 / 160,425.068 J; ±6.261 J; gapΣNode=1.841s, max=1.841s, maxNode=0.102%<br>E5-6: 369,924.973 / 336,437.807 J; ±302.348 J; gapΣNode=14.153s, max=2.156s, maxNode=0.111% | E5-1: 176,890.024 / 153,851.839 J; ±13.133 J; gapΣNode=5.263s, max=1.872s, maxNode=0.104%<br>E5-6: 351,301.405 / 317,219.260 J; ±395.014 J; gapΣNode=39.022s, max=3.994s, maxNode=0.282% | E5-1: 202,214.814 / 184,924.811 J; ±531.703 J; gapΣNode=46.537s, max=6.537s, maxNode=0.657%<br>E5-6: 405,232.339 / 371,636.855 J; ±67.083 J; gapΣNode=14.787s, max=2.010s, maxNode=0.105% | E5-1: 184,964.392 / 168,219.474 J; ±159.922 J; gapΣNode=9.445s, max=2.262s, maxNode=0.126%<br>E5-6: 367,778.969 / 334,305.722 J; ±146.063 J; gapΣNode=12.874s, max=3.024s, maxNode=0.133% | E5-1: 184,964.392 / 168,219.474 J; ±159.922 J; gapΣNode=9.445s, max=2.262s, maxNode=0.126%<br>E5-6: 367,778.969 / 334,305.722 J; ±146.063 J; gapΣNode=12.874s, max=3.024s, maxNode=0.133% | E5-1: 29,152.074 ±1,048.217 J<br>E5-6: 62,243.166 ±396.077 J | E5-1: 5,181.077 ±166.182 J<br>E5-6: -2,146.003 ±448.411 J | E5-1: 8,074.368 ±173.054 J<br>E5-6: 16,477.565 ±541.077 J | FAIL; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` | MEASURED_DESCRIPTIVE; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` | #225; Run=`/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-3b.txt`, `comparison-225.txt/.json`; `full-E5-1-3b/`<br>#224; Run=`/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-3.txt`, `comparison-224.txt/.json`; `full-E5-6-3/` |
| 점진적인 증가·감소 | 구성 확장·축소와 요청 재분배 | E5-3: MNV: p95 1,917.629 ms; 계획 134,156; 완료 115,060 / 실패 19,095 (BackendUnavailable=14,084; RequestTimeout=5,011) / 거절 0 / 취소 0 / 미완료·불명 1; D1 FAIL (102509/127449)<br>PLC: p95 16.128 ms; 계획 48,053; 완료 48,053 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (47202/45651)<br>Smol: p95 12,008.491 ms; 계획 682; 완료 682 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (561/648) | E5-3: MNV: p95 1,654.981 ms; 계획 134,156; 완료 120,593 / 실패 13,562 (BackendUnavailable=13,368; RequestTimeout=194) / 거절 0 / 취소 0 / 미완료·불명 1; D1 FAIL (113590/127449)<br>PLC: p95 16.031 ms; 계획 48,053; 완료 48,053 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (47349/45651)<br>Smol: p95 3,257.114 ms; 계획 682; 완료 682 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (682/648) | 3회차 미실행 | E5-3: 211,449.733 / 193,442.322 J; ±516.091 J; gapΣNode=53.626s, max=3.183s, maxNode=0.742% | E5-3: 220,081.631 / 203,078.172 J; ±86.364 J; gapΣNode=10.886s, max=3.006s, maxNode=0.264% | 3회차 미실행 | E5-3: 239,190.398 / 222,443.845 J; ±23.823 J; gapΣNode=2.002s, max=2.002s, maxNode=0.111% | E5-3: 218,442.310 / 201,706.164 J; ±268.924 J; gapΣNode=10.676s, max=2.018s, maxNode=0.206% | 3회차 미실행 | E5-3: 27,740.665 ±539.914 J | E5-3: -1,639.320 ±355.288 J | 3회차 미실행 | FAIL; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` | FAIL; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` | 3회차 미실행 |
| 갑작스러운 버스트 | 대응 시작, 용량 준비, 전환 중 요청 지연 | E5-2: MNV: p95 1,721.845 ms; 계획 28,643; 완료 26,689 / 실패 1,954 (BackendUnavailable=1,816; RequestTimeout=138) / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (23818/27211)<br>PLC: p95 16.195 ms; 계획 10,374; 완료 10,374 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (10252/9856)<br>Smol: p95 3,963.959 ms; 계획 337; 완료 337 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (337/321) | E5-2: MNV: p95 236.091 ms; 계획 28,643; 완료 28,643 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (28643/27211)<br>PLC: p95 16.073 ms; 계획 10,374; 완료 10,374 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (10265/9856)<br>Smol: p95 3,416.503 ms; 계획 337; 완료 337 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (337/321) | 3회차 미실행 | E5-2: 98,316.669 / 87,536.871 J; ±35.737 J; gapΣNode=9.731s, max=2.146s, maxNode=0.408% | E5-2: 100,255.448 / 91,732.549 J; ±3.525 J; gapΣNode=1.958s, max=1.958s, maxNode=0.217% | 3회차 미실행 | E5-2: 104,571.577 / 96,191.559 J; ±0.000 J; gapΣNode=0.000s, max=0.000s, maxNode=0.000% | E5-2: 102,597.859 / 94,212.984 J; ±122.848 J; gapΣNode=3.507s, max=1.770s, maxNode=0.197% | 3회차 미실행 | E5-2: 6,254.908 ±35.737 J | E5-2: 2,342.410 ±126.373 J | 3회차 미실행 | FAIL; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` | MEASURED_DESCRIPTIVE; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` | 3회차 미실행 |
| 전환 직후 급감·재급증 | 반복 전환의 비용과 대응 | E5-5: MNV: p95 286.946 ms; 계획 76,632; 완료 71,169 / 실패 5,463 (BackendUnavailable=4,507; RequestTimeout=956) / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (70317/72801)<br>PLC: p95 16.646 ms; 계획 27,523; 완료 27,523 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (27075/26147)<br>Smol: p95 14,318.976 ms; 계획 382; 완료 382 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (300/363) | E5-5: MNV: p95 1,895.488 ms; 계획 76,632; 완료 65,848 / 실패 10,780 (BackendUnavailable=10,636; RequestTimeout=144) / 거절 0 / 취소 0 / 미완료·불명 4; D1 FAIL (62176/72801)<br>PLC: p95 17.674 ms; 계획 27,523; 완료 27,523 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (27078/26147)<br>Smol: p95 3,730.820 ms; 계획 382; 완료 382 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (382/363) | E5-5: MNV: p95 226.188 ms; 계획 76,632; 완료 75,573 / 실패 1,059 (BackendUnavailable=1,026; RequestTimeout=33) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (75059/72801)<br>PLC: p95 16.189 ms; 계획 27,523; 완료 27,523 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (27142/26147)<br>Smol: p95 14,092.610 ms; 계획 382; 완료 382 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (300/363) | E5-5: 142,065.495 / 125,443.003 J; ±0.000 J; gapΣNode=0.000s, max=0.000s, maxNode=0.000% | E5-5: 139,317.646 / 128,142.974 J; ±755.285 J; gapΣNode=34.949s, max=2.757s, maxNode=0.772% | E5-5: 141,731.555 / 130,480.129 J; ±0.000 J; gapΣNode=0.000s, max=0.000s, maxNode=0.000% | E5-5: UNKNOWN / UNKNOWN J; ±UNKNOWN J; gapΣNode=77.900s, max=3.511s, maxNode=1.637% | E5-5: 139,031.237 / 127,874.805 J; ±15.373 J; gapΣNode=1.767s, max=1.767s, maxNode=0.147% | E5-5: 139,031.237 / 127,874.805 J; ±15.373 J; gapΣNode=1.767s, max=1.767s, maxNode=0.147% | E5-5: UNKNOWN ±UNKNOWN J | E5-5: -286.409 ±770.658 J | E5-5: -2,700.318 ±15.373 J | FAIL; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` | FAIL; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` | #224; Run=`/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-3.txt`, `comparison-224.txt/.json`; `full-E5-5-3/` |
| 이기종 워크로드 동시 혼합 부하 | 모델과 비용 구조가 다른 워크로드들의 요청 지연, 처리 결과와 에너지 | E5-1: MNV: p95 395.003 ms; 계획 57,495; 완료 47,268 / 실패 10,225 (BackendUnavailable=9,092; RequestTimeout=1,133) / 거절 0 / 취소 0 / 미완료·불명 2; D1 FAIL (45754/54621)<br>PLC: p95 17.519 ms; 계획 20,605; 완료 20,605 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (20320/19575)<br>Smol: p95 3,280.336 ms; 계획 301; 완료 301 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (301/286) | E5-1: MNV: p95 299.334 ms; 계획 57,495; 완료 57,199 / 실패 296 (BackendUnavailable=272; RequestTimeout=24) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (55983/54621)<br>PLC: p95 16.278 ms; 계획 20,605; 완료 20,605 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (20381/19575)<br>Smol: p95 2,557.590 ms; 계획 301; 완료 301 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (301/286) | E5-1: MNV: p95 274.882 ms; 계획 57,495; 완료 57,428 / 실패 67 (BackendUnavailable=67) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (56650/54621)<br>PLC: p95 17.094 ms; 계획 20,605; 완료 20,605 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (20377/19575)<br>Smol: p95 3,276.135 ms; 계획 301; 완료 301 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (301/286) | E5-1: 173,062.740 / 148,610.058 J; ±516.514 J; gapΣNode=37.043s, max=2.999s, maxNode=0.645% | E5-1: 179,783.315 / 160,425.068 J; ±6.261 J; gapΣNode=1.841s, max=1.841s, maxNode=0.102% | E5-1: 176,890.024 / 153,851.839 J; ±13.133 J; gapΣNode=5.263s, max=1.872s, maxNode=0.104% | E5-1: 202,214.814 / 184,924.811 J; ±531.703 J; gapΣNode=46.537s, max=6.537s, maxNode=0.657% | E5-1: 184,964.392 / 168,219.474 J; ±159.922 J; gapΣNode=9.445s, max=2.262s, maxNode=0.126% | E5-1: 184,964.392 / 168,219.474 J; ±159.922 J; gapΣNode=9.445s, max=2.262s, maxNode=0.126% | E5-1: 29,152.074 ±1,048.217 J | E5-1: 5,181.077 ±166.182 J | E5-1: 8,074.368 ±173.054 J | FAIL; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` | MEASURED_DESCRIPTIVE; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` | #225; Run=`/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-3b.txt`, `comparison-225.txt/.json`; `full-E5-1-3b/` |
| 전체 POOL의 한계를 넘는 부하 | 요청 지연·처리 결과와 가용 자원의 활용 | E5-4: MNV: p95 1,961.366 ms; 계획 114,558; 완료 101,318 / 실패 13,239 (BackendUnavailable=13,046; RequestTimeout=193) / 거절 0 / 취소 0 / 미완료·불명 1; D1 OVERLOAD_DESCRIPTIVE (77524/108831)<br>PLC: p95 16.410 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (40728/39542)<br>Smol: p95 74,482.013 ms; 계획 617; 완료 499 / 실패 118 (BackendUnavailable=118) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (55/587) | E5-4: MNV: p95 1,970.691 ms; 계획 114,558; 완료 59,501 / 실패 55,040 (BackendUnavailable=53,719; RequestTimeout=1,320; ResponseDeliveryFailed=1) / 거절 0 / 취소 0 / 미완료·불명 17; D1 OVERLOAD_DESCRIPTIVE (45077/108831)<br>PLC: p95 15.897 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (40901/39542)<br>Smol: p95 6,308.208 ms; 계획 617; 완료 617 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (609/587) | 3회차 미실행 | E5-4: 133,012.645 / 124,613.068 J; ±206.141 J; gapΣNode=12.187s, max=2.251s, maxNode=0.673% | E5-4: 128,282.988 / 119,920.115 J; ±0.886 J; gapΣNode=1.771s, max=1.771s, maxNode=0.197% | 3회차 미실행 | E5-4: 133,396.452 / 125,018.421 J; ±48.755 J; gapΣNode=13.593s, max=2.227s, maxNode=0.704% | E5-4: 128,565.990 / 120,191.187 J; ±32.030 J; gapΣNode=7.023s, max=3.008s, maxNode=0.334% | 3회차 미실행 | E5-4: 383.807 ±254.896 J | E5-4: 283.002 ±32.915 J | 3회차 미실행 | MEASURED_DESCRIPTIVE; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` | MEASURED_DESCRIPTIVE; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` | 3회차 미실행 |
| 노드·서빙·제어 동작 실패 | 격리, 대체 선택, 복구 상태 | E5-7: MNV: p95 401.963 ms; 계획 28,793; 완료 28,793 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (28789/27354)<br>PLC: p95 17.780 ms; 계획 10,310; 완료 10,310 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (10177/9795)<br>Smol: p95 2,971.640 ms; 계획 140; 완료 140 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (140/133) | E5-7: MNV: p95 145.595 ms; 계획 28,793; 완료 28,793 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (28793/27354)<br>PLC: p95 15.668 ms; 계획 10,310; 완료 10,310 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (10219/9795)<br>Smol: p95 1,775.231 ms; 계획 140; 완료 140 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (140/133) | E5-7: MNV: p95 93.983 ms; 계획 28,793; 완료 28,793 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (28793/27354)<br>PLC: p95 16.053 ms; 계획 10,310; 완료 10,310 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (10226/9795)<br>Smol: p95 1,255.905 ms; 계획 140; 완료 26 / 실패 114 (RequestTimeout=114) / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (26/133) | E5-7: 83,877.847 / 75,449.598 J; ±1.948 J; gapΣNode=1.498s, max=1.498s, maxNode=0.166% | E5-7: 91,617.432 / 83,235.477 J; ±132.328 J; gapΣNode=3.695s, max=1.929s, maxNode=0.214% | E5-7: 85,096.635 / 76,740.712 J; ±3.414 J; gapΣNode=2.008s, max=2.008s, maxNode=0.223% | E5-7: N/A (E5-S 대응 단계 없음) | E5-7: N/A (E5-S 대응 단계 없음) | E5-7: N/A (E5-S 대응 단계 없음) | E5-7: N/A | E5-7: N/A | E5-7: N/A | MEASURED_DESCRIPTIVE; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json` | MEASURED_DESCRIPTIVE; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json` | #225; Run=`/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-3b.txt`, `comparison-225.txt/.json`; `full-E5-7-3b/` |
| 정적 배포 비교 | E5-S: E5-3 끝 α=0.9 구성·모드·route 고정, 같은 trace/seed | E5-1 고정: MNV: p95 138.124 ms; 계획 57,495; 완료 57,495 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (57495/54621)<br>PLC: p95 17.245 ms; 계획 20,605; 완료 20,605 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (20346/19575)<br>Smol: p95 3,310.022 ms; 계획 301; 완료 301 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (301/286)<br>E5-2 고정: MNV: p95 137.532 ms; 계획 28,643; 완료 28,643 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (28643/27211)<br>PLC: p95 16.336 ms; 계획 10,374; 완료 10,374 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (10268/9856)<br>Smol: p95 16,208.841 ms; 계획 337; 완료 337 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (254/321)<br>E5-3 고정: MNV: p95 132.675 ms; 계획 134,156; 완료 134,156 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (134156/127449)<br>PLC: p95 15.873 ms; 계획 48,053; 완료 48,053 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (47342/45651)<br>Smol: p95 13,027.826 ms; 계획 682; 완료 682 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (549/648)<br>E5-4 고정: MNV: p95 1,964.475 ms; 계획 114,558; 완료 95,171 / 실패 19,386 (BackendUnavailable=19,250; RequestTimeout=136) / 거절 0 / 취소 0 / 미완료·불명 1; D1 OVERLOAD_DESCRIPTIVE (75448/108831)<br>PLC: p95 17.663 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (40705/39542)<br>Smol: p95 74,407.736 ms; 계획 617; 완료 497 / 실패 120 (BackendUnavailable=120) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (58/587)<br>E5-5 고정: MNV: p95 147.741 ms; 계획 76,632; 완료 76,632 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (76632/72801)<br>PLC: p95 16.296 ms; 계획 27,523; 완료 27,523 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (27049/26147)<br>Smol: p95 14,213.074 ms; 계획 382; 완료 382 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 FAIL (300/363)<br>E5-6 고정: MNV: p95 136.058 ms; 계획 114,558; 완료 114,558 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (114558/108831)<br>PLC: p95 16.425 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (41203/39542)<br>Smol: p95 3,317.283 ms; 계획 617; 완료 617 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (617/587) | E5-1 고정: MNV: p95 142.116 ms; 계획 57,495; 완료 57,495 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (57495/54621)<br>PLC: p95 16.268 ms; 계획 20,605; 완료 20,605 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (20397/19575)<br>Smol: p95 2,108.634 ms; 계획 301; 완료 301 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (301/286)<br>E5-2 고정: MNV: p95 141.463 ms; 계획 28,643; 완료 28,643 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (28643/27211)<br>PLC: p95 16.879 ms; 계획 10,374; 완료 10,374 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (10242/9856)<br>Smol: p95 3,654.120 ms; 계획 337; 완료 337 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (337/321)<br>E5-3 고정: MNV: p95 281.794 ms; 계획 134,156; 완료 123,365 / 실패 10,787 (BackendUnavailable=10,643; RequestTimeout=144) / 거절 0 / 취소 0 / 미완료·불명 4; D1 FAIL (119667/127449)<br>PLC: p95 16.417 ms; 계획 48,053; 완료 48,053 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (47395/45651)<br>Smol: p95 3,292.177 ms; 계획 682; 완료 682 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (682/648)<br>E5-4 고정: MNV: p95 1,970.866 ms; 계획 114,558; 완료 61,326 / 실패 53,218 (BackendUnavailable=52,079; RequestTimeout=1,139) / 거절 0 / 취소 0 / 미완료·불명 14; D1 OVERLOAD_DESCRIPTIVE (46146/108831)<br>PLC: p95 16.138 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (40922/39542)<br>Smol: p95 6,337.105 ms; 계획 617; 완료 617 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OVERLOAD_DESCRIPTIVE (607/587)<br>E5-5 고정: MNV: p95 1,890.595 ms; 계획 76,632; 완료 65,747 / 실패 10,883 (BackendUnavailable=10,684; RequestTimeout=199) / 거절 0 / 취소 0 / 미완료·불명 2; D1 FAIL (62133/72801)<br>PLC: p95 15.894 ms; 계획 27,523; 완료 27,523 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (27197/26147)<br>Smol: p95 3,620.171 ms; 계획 382; 완료 382 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (382/363)<br>E5-6 고정: MNV: p95 170.689 ms; 계획 114,558; 완료 114,519 / 실패 39 (RequestTimeout=38; BackendUnavailable=1) / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (114519/108831)<br>PLC: p95 16.262 ms; 계획 41,623; 완료 41,623 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (41210/39542)<br>Smol: p95 1,976.317 ms; 계획 617; 완료 617 / 실패 0 / 거절 0 / 취소 0 / 미완료·불명 0; D1 OBSERVED_THRESHOLD_MET (617/587) | 3회차 미실행 | E5-1: 173,062.740 / 148,610.058 J; ±516.514 J; gapΣNode=37.043s, max=2.999s, maxNode=0.645%<br>E5-2: 98,316.669 / 87,536.871 J; ±35.737 J; gapΣNode=9.731s, max=2.146s, maxNode=0.408%<br>E5-3: 211,449.733 / 193,442.322 J; ±516.091 J; gapΣNode=53.626s, max=3.183s, maxNode=0.742%<br>E5-4: 133,012.645 / 124,613.068 J; ±206.141 J; gapΣNode=12.187s, max=2.251s, maxNode=0.673%<br>E5-5: 142,065.495 / 125,443.003 J; ±0.000 J; gapΣNode=0.000s, max=0.000s, maxNode=0.000%<br>E5-6: 342,989.174 / 307,660.371 J; ±328.993 J; gapΣNode=20.693s, max=4.811s, maxNode=0.238% | E5-1: 179,783.315 / 160,425.068 J; ±6.261 J; gapΣNode=1.841s, max=1.841s, maxNode=0.102%<br>E5-2: 100,255.448 / 91,732.549 J; ±3.525 J; gapΣNode=1.958s, max=1.958s, maxNode=0.217%<br>E5-3: 220,081.631 / 203,078.172 J; ±86.364 J; gapΣNode=10.886s, max=3.006s, maxNode=0.264%<br>E5-4: 128,282.988 / 119,920.115 J; ±0.886 J; gapΣNode=1.771s, max=1.771s, maxNode=0.197%<br>E5-5: 139,317.646 / 128,142.974 J; ±755.285 J; gapΣNode=34.949s, max=2.757s, maxNode=0.772%<br>E5-6: 369,924.973 / 336,437.807 J; ±302.348 J; gapΣNode=14.153s, max=2.156s, maxNode=0.111% | 3회차 미실행 | E5-1: 202,214.814 / 184,924.811 J; ±531.703 J; gapΣNode=46.537s, max=6.537s, maxNode=0.657%<br>E5-2: 104,571.577 / 96,191.559 J; ±0.000 J; gapΣNode=0.000s, max=0.000s, maxNode=0.000%<br>E5-3: 239,190.398 / 222,443.845 J; ±23.823 J; gapΣNode=2.002s, max=2.002s, maxNode=0.111%<br>E5-4: 133,396.452 / 125,018.421 J; ±48.755 J; gapΣNode=13.593s, max=2.227s, maxNode=0.704%<br>E5-5: UNKNOWN / UNKNOWN J; ±UNKNOWN J; gapΣNode=77.900s, max=3.511s, maxNode=1.637%<br>E5-6: 405,232.339 / 371,636.855 J; ±67.083 J; gapΣNode=14.787s, max=2.010s, maxNode=0.105% | E5-1: 184,964.392 / 168,219.474 J; ±159.922 J; gapΣNode=9.445s, max=2.262s, maxNode=0.126%<br>E5-2: 102,597.859 / 94,212.984 J; ±122.848 J; gapΣNode=3.507s, max=1.770s, maxNode=0.197%<br>E5-3: 218,442.310 / 201,706.164 J; ±268.924 J; gapΣNode=10.676s, max=2.018s, maxNode=0.206%<br>E5-4: 128,565.990 / 120,191.187 J; ±32.030 J; gapΣNode=7.023s, max=3.008s, maxNode=0.334%<br>E5-5: 139,031.237 / 127,874.805 J; ±15.373 J; gapΣNode=1.767s, max=1.767s, maxNode=0.147%<br>E5-6: 367,778.969 / 334,305.722 J; ±146.063 J; gapΣNode=12.874s, max=3.024s, maxNode=0.133% | 3회차 미실행 | E5-1: 29,152.074 ±1,048.217 J<br>E5-2: 6,254.908 ±35.737 J<br>E5-3: 27,740.665 ±539.914 J<br>E5-4: 383.807 ±254.896 J<br>E5-5: UNKNOWN ±UNKNOWN J<br>E5-6: 62,243.166 ±396.077 J | E5-1: 5,181.077 ±166.182 J<br>E5-2: 2,342.410 ±126.373 J<br>E5-3: -1,639.320 ±355.288 J<br>E5-4: 283.002 ±32.915 J<br>E5-5: -286.409 ±770.658 J<br>E5-6: -2,146.003 ±448.411 J | 3회차 미실행 | INCOMPLETE; 총에너지 UNKNOWN; Run ID `control-213-full`; 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`; `tables.txt`, `e5-summary.json`; `static-selection.json` | MEASURED_DESCRIPTIVE; Run ID `control-222-full` (#222+#223 합성, rev104); 원시 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full`; `tables-223.txt`, `e5-summary.json`, `comparison-control-213-223.txt/.json`; `static-selection.json`, `static-replay-restore.json` | 3회차 미실행 |

**조건**

- genesis2는 하드웨어 결함(Xid 79)으로 제외했으며 서빙 Node는 7개다.
- 생성기는 요청마다 새 TLS 연결을 열며(keep-alive 없음), 입구 지연과 게이트웨이 CPU에는 핸드셰이크 비용이 포함된다.
- 수신 p95는 성공·executionComplete HTTP final의 완료-only 분포다. MNV/PLC/Smol SLO는 각각 p95≤1,000/36/7,034 ms다. D1은 BackendUnavailable, 오류·거절·취소·무발행·미완료를 포함한 전체 planned 분모를 유지하며 deadline 성공/필요 rank와 기존 판정을 표시한다.
- 결과 수는 완료/실패/거절/취소/미완료·불명을 모두 보존한다. 상위 오류 코드는 실패 괄호 안에 표시한다. 수신 p95 표본에 실패를 섞지 않는다.
- 표의 전체 POOL 에너지는 7개 관측 Node 합으로 전체 구성 meter 합(`power-configured-whole-meter.json`)과 구분한다. 에너지는 전환 포함 동일 단계 공유 전체/활성 POOL 값이며 워크로드별 귀속값이 아니다. 반복 행을 합산하지 않는다. ±는 기존 E5 공백 보간 오차 상한, coverage는 gapΣNode(노드 공백초 합), max(최대 공백), maxNode(최대 노드 공백 비율)이다. Δ=고정−CHILL이며 양수는 CHILL 절감이다.
- 1회차 E5-S S5의 전력 공백 1.637%>1%로 S5 및 E5-S 에너지 합과 총 차이는 UNKNOWN이다. 대응 CHILL 170분 전체 POOL은 1,100,896.456 J(원시 1100896.4557965863, 소수 셋째 자리 반올림)이며 UNKNOWN을 0으로 대체하지 않는다.
- 표 수치는 1회차 tables.txt·2회차 tables-223.txt의 소수 셋째 자리 표시를 그대로 옮겼다(반올림 표시); 새 판정 규칙은 적용하지 않는다. MEASURED_DESCRIPTIVE/결과 연결은 PASS나 완전 커버리지를 뜻하지 않는다.
- E5-3은 증가만 평가했으며 E5-5는 급감·재급증으로 점진 감소는 미평가다. 버스트는 Smol만, 실패 주입은 서빙 손실/cordon 대리 실험만이다. 물리 네트워크·전원 손실과 모든 제어 실패 유형은 미측정이다. 한 paired seed 집합이며 독립 반복은 없다.
- 1회차 CHILL 결함 후보의 수정은 1회차 결과 확정 뒤이며, 2회차는 위 수정 후 소스를 사용했다. 아래 제어요소 비교는 제한된 조건의 case study이며 모든 요소의 독립 기여를 증명하지 않는다.
- 1회차는 수정 전 `control-213-full`, 2회차는 수정 후 소스 rev104(`e3f28f23`: R1~R6+R4b)다. 결함 카드 근거는 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/analysis/P2-synthesis.md`다. 이 문서 갱신의 제품 코드 변경은 0이다.
- 2회차는 #222+#223 합성이다. #222의 full E5-0~3·static E5-1~2에 이어, static E5-3~6·full E5-4~7은 #223에서 factor pre-state 복원 경로로 측정했다. `static-replay-restore.json`의 accepted=true와 fingerprint 일치를 확인했으며 prepare ACK의 물리 ID만 재결합했다. 중단 없는 단일 실행으로 표현하지 않는다.
- static 기준 배치는 각 회차 E5-3 종료 상태에서 독립적으로 선택되어 MNV 공급 κ가 119.601→91.907 req/s로 달랐다. E5-4·E5-5의 실패·에너지 차이는 이 배치 차이와 함께 해석해야 하며, 수정된 CHILL이 E5-3 종료 시 MNV 공급을 수요 미만(κ 91.907 < 약 95.5 req/s)으로 남긴 원인은 **조사 중**(사이클 2)이다. 근거는 각 회차 `static-selection.json` 및 2회차 `comparison-control-213-223.txt/.json`이다.
- E5-7 주입은 1회차 실제 형식인 UID 사전조건부 Pod 삭제(강제 삭제·gracePeriodSeconds=0 아님), 180초 삭제·360초 cordon+삭제·600초 uncordon을 재사용했다. 대상은 1회차 genesis3/lattepanda → 2회차 jetson-nano/orangepi로 달랐다(원래 대상에 현재 서빙 Pod 없음). 각 회차 `failure-seven[/ -223]/targets.json` 및 명령·응답·복구 read-back이 근거이며 물리 전원·네트워크 장애나 완전 복구 보장은 아니다.
- 2회차 중단 이력(#217 collector 경합, #218 예약 잔류→R4b, #221 디스크 압박, #222 로그 혼입)은 하네스/환경 요인으로 구분한다. 중단된 단계는 정본의 선택 결과에 합산하지 않았으며, 하네스/환경 중단을 제품 성능 판정으로 바꾸지 않는다.
- 2회차 정본은 위 Run의 `tables-223.txt`, `comparison-control-213-223.txt/.json`, `e5-summary.json`이다. 두 회차는 각각 한 paired seed 집합이며 독립 반복 통계나 동일 배치 A/B 실험이 아니다. 이 문서 갱신의 클러스터 조작·재측정은 0이다.
- **3회차 = 표적 재실행, 시작 상태 복원(fingerprint 일치), 소스 R1~R6+R4b+C1+C2(rev105 `5f1dc4f1`)**. E5-1·E5-7은 #225 `tables-3b.txt`·`comparison-225.txt/.json`, E5-5·E5-6은 #224 `tables-3.txt`·`comparison-224.txt/.json`이 정본이다(위 `control-222-full` Run). 표적 외 행은 3회차 미실행이며 전체 회차나 독립 반복으로 세지 않는다. 수치는 정본의 소수 셋째 자리 표시를 그대로 옮겼다. 고정 E5-S 대응값은 기존 2회차 기준선 재사용이며 3회차 정적 재측정이 아니다.
- #224 E5-1은 하네스 release 훼손, E5-7은 selector TTL로 첫 주입의 stale snapshot이 거절되어 mutation·후속 주입이 없었으므로 무효다. 두 행은 #225 재실행 값만 사용하며 무효 실행을 제품 성능 판정에 합산하지 않는다.
- E5-6/7은 2회차 시작 예약 1개+owner-only 3개를 복원에서 제외했다. 따라서 개선을 C1 효과로 귀속하지 않는다. C1 효과 판정 근거는 E5-5 내 신규 예약 6개 전부 행 내 반환 관측, Fixed tick 0(2회차 560 tick)이다. 반환은 관측된 예약이 처음 사라진 archived tick 기준이며 근거는 `full-E5-5-3/c1-reservation-return.json`이다.
- #225 E5-7 FAIL은 Smol D1(용량)만이다. 주입은 180.7/360.7/600.1s에 활성 Pod 대상으로 실행되어 exit0이며 주입 실패와 구분한다. 완료-only Smol p95가 낮아도 실패 114(RequestTimeout)와 D1 FAIL(26/133)을 배제하지 않는다. 물리 전원·네트워크 장애나 완전 복구 보장은 아니다. 이 문서 갱신의 클러스터 조작·재측정 0, 제품 코드 변경 0.

두 회차 각각 평가 시나리오 결과 연결은 7/7이며 정적 비교 1행은 별도다. 이는 모든 시나리오의 완전 평가나 PASS 수가 아니다.
실행 계획과 제어요소 비교는 [E5 계획표](../design/serving/workload-pools.md)를 따른다.

보고 기준은 **d 새 규칙 9/34, 정확 비용 69/69, 평가 시나리오 결과 연결 7/7**이다.
d는 2026-10-24T11:42:19.448243Z 반환 확인을 포함한
`/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-d-followthrough.json`,
비용은 2026-10-24T12:49:27.350175648Z snapshot을 연결한 같은 디렉터리의
`cost-coverage-after-055.json` 기준이다. 평가 결과는 위 7행의 실행 근거로 집계한다.

분석자는 **워크로드별 지연 분포·요청 처리 결과와 동일한 부하 시나리오의 총에너지**를
SLO 목표와 함께 해석한다. 에너지는 전환 구간을 포함한 전체 POOL 범위로 집계하고,
활성 POOL 소비를 별도로 제공한다. 완료·실패·거절·취소·미완료 요청 수를 함께
기록하여 처리한 작업량과 에너지를 연결한다.

### 제어요소 비교

제어요소를 하나씩 비활성화한 `control-216` 결과다. 정적 배포 비교 결과는 위 E5-S 행에 연결한다.

| factor | stage | 판정 | M/P/S 완료 p95 ms | 완료/실패/미완료 (M;P;S) | whole POOL J ± gap J | full J ± gap J | Δ J ± 합산 gap J | Δ p95 ms (M/P/S) |
|---|---|---|---|---|---|---|---|---|
| membership-off | E5-2 | FAIL | 1873.910/16.237/16442.460 | 26266/2377/0; 10374/0/0; 337/0/0 | 87482.049 ±6.537 | 98316.669 ±35.737 | -10834.621 ±42.274 | 152.065/0.042/12478.501 |
| membership-off | E5-5 | FAIL | 1952.014/16.636/14549.599 | 20380/56252/12; 27523/0/0; 382/0/0 | 117935.117 ±95.157 | 142065.495 ±0.000 | -24130.378 ±95.157 | 1665.069/-0.010/230.622 |
| replicas-off | E5-2 | FAIL | 660.805/16.652/16343.464 | 28548/95/0; 10374/0/0; 337/0/0 | 94693.407 ±125.736 | 98316.669 ±35.737 | -3623.262 ±161.473 | -1061.040/0.457/12379.505 |
| replicas-off | E5-5 | FAIL | 164.425/17.044/14475.377 | 75458/1174/0; 27523/0/0; 382/0/0 | 135117.952 ±60.594 | 142065.495 ±0.000 | -6947.543 ±60.594 | -122.521/0.398/156.401 |
| power-off | E5-2 | FAIL | 332.523/15.896/2897.453 | 26733/1910/0; 10374/0/0; 337/0/0 | 97588.787 ±0.000 | 98316.669 ±35.737 | -727.883 ±35.737 | -1389.322/-0.299/-1066.506 |
| power-off | E5-5 | FAIL | 1970.804/14.910/2788.570 | 23999/52633/17; 27523/0/0; 382/0/0 | 129255.999 ±0.000 | 142065.495 ±0.000 | -12809.495 ±0.000 | 1683.858/-1.736/-11530.406 |
| routing-off | E5-2 | FAIL | 1688.082/15.526/2979.623 | 24642/4001/0; 10374/0/0; 337/0/0 | 95237.598 ±146.614 | 98316.669 ±35.737 | -3079.072 ±182.352 | -33.763/-0.669/-984.336 |
| routing-off | E5-5 | FAIL | 301.424/15.064/14411.334 | 49106/27526/9; 27523/0/0; 382/0/0 | 124579.162 ±170.154 | 142065.495 ±0.000 | -17486.333 ±170.154 | 14.478/-1.583/92.358 |
| resources-off | E5-2 | FAIL | 355.956/15.890/2777.320 | 26753/1890/0; 10374/0/0; 337/0/0 | 95481.953 ±0.000 | 98316.669 ±35.737 | -2834.716 ±35.737 | -1365.889/-0.306/-1186.639 |
| resources-off | E5-5 | FAIL | 1949.342/24.142/2394.628 | 20306/56326/8; 27467/56/0; 382/0/0 | 126604.774 ±330.508 | 142065.495 ±0.000 | -15460.720 ±330.508 | 1662.396/7.495/-11924.348 |
| batching-off | E5-2 | NOT_READY | 1970.030/219.843/17286.651 | 9662/18981/3; 10361/13/0; 337/0/0 | UNKNOWN ±UNKNOWN | 98316.669 ±35.737 | UNKNOWN ±UNKNOWN | 248.185/203.647/13322.692 |
| batching-off | E5-5 | NOT_READY | 1972.644/158.506/14147.736 | 16959/59673/20; 27286/237/0; 382/0/0 | UNKNOWN ±UNKNOWN | 142065.495 ±0.000 | UNKNOWN ±UNKNOWN | 1685.698/141.860/-171.240 |

**제어요소 비교 조건**

- **factor 행 = rev102(48c1a44f)**; source SHA는 `48c1a44f5ef5a92aa9645e06e0dc82c9f37acf68`이다. 대응 full은 원본 `control-213-full` 행이며 원본 binding을 보존했다.
- 제어 이미지는 `155.230.35.213:5000/chill/chill-serving-entrypoint@sha256:433265c86aba897fd6f122cc4461a18adb729ef713c24a63e17c90c31bcc3d23`이다. published manifest digest는 같은 `sha256:433265c86aba897fd6f122cc4461a18adb729ef713c24a63e17c90c31bcc3d23`, platform digest는 `sha256:39d2b6afdfea222734048728aa7c8a5b730ec15b7190a02e7d91526ef807f98f`다.
- M/P/S는 MNV/PLC/Smol이며 p95는 완료-only다. 결과 수와 기존 판정은 정본 그대로이며, 완료 수 감소·실패·미완료를 함께 읽는다. 에너지 감소만으로 개선을 주장하지 않는다.
- 단일 paired seed다. Δ는 위 정적 비교와 달리 **factor − full**이며, 에너지 ±는 기록된 gap bound, Δ의 ±는 두 gap bound의 합이다. 통계적 신뢰구간이나 독립 반복 오차가 아니다. 전체 POOL 에너지는 워크로드별 귀속값이 아니며 행끼리 합산하지 않는다.
- placement-off는 offline replay에서 membership-off와 동일 목표이므로 제외했다.
- batching-off는 **고정-cap 대조**다. 전력 gap으로 에너지와 에너지 Δ는 **UNKNOWN**이며 0이나 추정 절감으로 대체하지 않는다.
- E5-5 factor 행은 원본 예약으로 시작 구성을 동결했다. 따라서 원본 예약·초기 구성의 영향과 해당 요소의 독립 기여를 분리하는 식별력에 한계가 있다.
- power-off는 1-step replay에서 차이가 없으며 시간축 효과만 관찰한다. 표의 차이를 단일 판단의 효과로 해석하지 않는다.
- `Main #216 revised order`에 따라 membership-off(#215 보존 결과) → replicas-off → power-off → routing-off → resources-off → batching-off 순서다.
- 정본 수치는 [factor-tables.txt](/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-216-rev102-factors/factor-tables.txt) 그대로다. 상세는 같은 디렉터리 `tables.txt`, 행별 판정·원시는 `<factor>-<stage>/judgment.json`, 대응 full은 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-213-full`에 있다. 이 문서 갱신은 클러스터 조작 0, 코드 변경 0이다.

정적 구성은 선택 근거를 명시하며, 일부러 불리한 구성만을 기준으로 삼지 않는다.
다른 연구의 정책으로 제어요소를 대체해 비교할 수 있도록 판단과 실행의 경계를
유지하되, 특정 플러그인 구조를 요구하지는 않는다.

각 실행에는 부하 시나리오, 사용한 프로파일과 설정, 초기 구성, 제어 동작과 실제
상태 변화, 요청 결과와 전력 관측을 남긴다. 분석자는 이 기록으로 시간축에 따라
요청 지연과 에너지를 해석하고, 프로파일과 조건이 대응하는 Serving 구간의
예측값과 실측값 차이를 확인한다.

## 설계에서 결정할 사항

- 비용을 비교할 기간과 그동안의 부하를 다루는 방법.
- 후보 구성의 탐색·비교 알고리즘, 관찰·제어 주기, 전환 중 재판단 규칙.
- firstfloor 내부에서 적용할 요청별 시간 예산·에너지 기반 배칭·큐 선택 규칙과 정책 갱신 방식, 라우팅 갱신 방식.
- 워크로드의 리소스와 API 표현, 관리 절차의 구현 및 같은 노드를 공유하는 워크로드 사이의 자원 할당.

이 선택들은 SLO 우선과 전환·Serving 비용의 공동 고려라는 목표를 구현해야 한다.
프로파일링의 세부 측정 항목과 절차는 선택한 제어 로직에 필요한 질문에서 도출한다.

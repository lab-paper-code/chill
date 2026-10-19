# 04. B3 — 완전 분산: Workload별 autoscaler, 노드 전력 governor, 느린 consolidator

상태: **주 정책으로 권고하지 않음. 비교 실험 기준선으로 가치 있음.** 주 정책으로 쓰려면 명세 변경 ⑤가
필요하다([06](06-spec-change-proposals.md)).

## 핵심 아이디어

세 개의 독립 루프로 나눈다.

- **Workload별 autoscaler:** 프로파일에서 얻은 인스턴스당 목표 처리율 `ρ·μ_i`로 인스턴스 수와 라우팅 가중치를
  정한다. Ray Serve `target_ongoing_requests`, Knative stable/panic window, KEDA 계열과 같은 방식.
- **노드 governor:** 그 노드 *모든* 인스턴스의 SLO 여유로 가장 낮은 전력모드를 고른다(DVFS governor와 유사).
  꺼진 Workload도 로컬 정보만으로 자연스럽게 보호된다(같은 노드 인스턴스를 모두 보므로).
- **consolidator(분 단위):** "노드 X를 비우고 재배치했을 때 `ΔE(H) > E_trans`인가"를 판정해 노드를 반환한다.
  Karpenter consolidation과 disruption budget, descheduler HighNodeUtilization 방식.

## 지우는 것

전역 후보 열거, 재생, snapshot, 판단 기록의 대부분.

## 성능

루프당 O(1)~O(N), 판단 µs~ms. 기록은 동작 사건뿐.

## 요구사항 변경 폭이 큼

- `SCH-15/16`의 "켜진 모든 Workload를 함께 만족하는 후보 중 전체 POOL 총에너지 최소"가
  "로컬 결정 + 주기적 consolidation"으로 약해진다.
- 04 명세의 "A 노드의 전력모드를 올리는 것과, A를 유지하면서 B를 추가하는 것을 비교" 같은 결합 비교는
  consolidator의 쌍 이동으로만 근사된다.
- `SCH-02`의 유지·변경 비교도 로컬로 축소된다.

## 위험

- autoscaler와 governor가 서로 반대로 움직이며 진동할 수 있다(하나는 모드를 내리고 하나는 인스턴스를 늘림).
- 전역 최적에서 멀어질 수 있다.

## 검증

버스트·급감 시나리오에서 진동 횟수와 에너지를 B1과 비교한다.

## 용도

명세 README "실클러스터 검증과 평가"의 "제어요소를 하나씩 비활성화해 기여를 비교", "다른 연구의 정책으로
제어요소를 대체해 비교"와 `SCH-32`에 맞는 **기준선 정책**이다. B1의 기여를 보이는 비교 대상으로 쓴다.

## 선례 (링크는 [references](references.md))

Ray Serve autoscaling, Knative KPA, KEDA, Karpenter disruption/consolidation, Kubernetes descheduler,
DynamoLLM(인스턴스 단위 주파수 제어).

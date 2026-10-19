# CHILL 아키텍처 재검토 제안 (A → B1·B2·B3)

## 상태

**제안(미채택).** 2026-10-19 작성. 이 디렉터리는 결정이 아니라 *제안과 그 근거*다.
명세(`docs/spec/`) 변경 제안은 사용자 승인 전까지 효력이 없다. 채택되면 해당 부분을 ADR과
각 도메인 설계로 옮기고 이 문서의 상태를 갱신한다.

## 왜 이 문서가 있는가

워크로드별 POOL 설계(branch `feat/workload-pools`의 `docs/design/serving/workload-pools.md`,
`docs/adr/0022-workload-pool-membership-boundary.md`)를 두 차례(정확성, 과설계) 리뷰한 뒤,
"현재 요구사항에 묶이지 말고, 요구사항을 바꾸면 훨씬 단순·빠른 구조가 되는지"를 senior
distributed-systems architect 관점에서 다시 본 결과다. 외부 선례(서빙 논문, Kubernetes 패턴,
시계열 저장, queueing 모델)를 1차 출처로 확인해 인용했다.

실운영에서 관측된 문제(사용자 보고, ground truth):

- 스케줄러 결정 기록 1건 ~10MB → ChillServer(128Mi) OOM, 이력 조회 15분 초과
- 관측 PVC 8Gi 가득 참
- staticRouting/calibration 수동 부트스트랩
- 노드 소속(annotation, NodeActuation, serving Pod) 주변의 다수 경쟁 조건
- 다중 CRD·controller 난립
- Go 소량 변경에도 무거운 runtime image 재빌드
- 노드별 JSON ConfigMap 수작업 편집

## 한 문단 결론

**B1(두 시간척도 분리: 빠른 데이터 경로 + 분석형 용량 계획기)과 B2(노드 상태 단일 writer +
native fence + 측정 admission)를 함께 채택하고, B3(완전 분산 autoscaler/governor/consolidator)는
비교 실험 기준선으로 둔다.** ~10MB 결정 기록은 명세가 요구한 것이 아니라 구현의 선택이므로
명세 변경 없이 즉시 줄일 수 있다(M0). workload-pools 설계의 commit 1–4는 유지하고, commit 5
(측정 POOL annotation + 4개 실행 주체의 Acquire/Release/Check)는 B2로 대체한다.

## 의도 보존 메모 (다음 에이전트가 오해하기 쉬운 지점)

1. **B1은 재생(replay)을 없애자는 제안이 아니다.** 재생을 *탐색 루프*에서 빼고, 상위 K개(예: 3개 × 20회)
   후보의 **검증기**와 오프라인 실험 simulator로 남긴다. `scheduler_replay.go`는 유지 대상이다.
2. **B1과 B2는 직교한다.** B2(상태 소유·actuation 메커니즘)는 현재 판단 알고리즘(A)과도 조합 가능하며
   명세 변경이 필요 없다. B1만 명세 변경(①, ①', ②, ③, ④)이 필요하다.
3. **M0(결정 기록 다이어트)과 M1(관측 분리)은 명세 변경 없이 바로 할 수 있다.** `SCH-30`은 "연결해 제공"이라
   불변 근거의 ID 참조로 충족된다. 10MB 기록과 JSONL 선형 scan은 명세가 강제한 것이 아니다.
4. **명세 변경 ②(공통 항 소거, 차이 비교)는 순위를 바꾸지 않는다.** 전체 POOL 총에너지는 *실측으로 보고*하고,
   *후보 비교*에서만 후보 간 동일 항을 소거한다. 이것으로 ADR 0022/§3.4의 측정 노드 공통 항,
   가용 노드 내림 `Fixed` 제외 assumption, "영향 없는 노드의 baseline 누락 → 전체 Pending"이 사라진다.
   ②는 A에도 바로 적용할 수 있다.
5. **B3는 주 정책으로 권고하지 않는다.** 명세의 "제어요소 기여 비교·다른 연구 정책 대체"용 기준선이다.
   B3를 주 정책으로 쓰려면 `SCH-02/15/16`을 약화해야 한다(⑤).
6. **활성 POOL을 인스턴스에서 도출하는 workload-pools §3.2는 B1·B2와 맞으므로 유지한다.**
7. **B1의 가장 큰 위험은 edge CPU 노드(RPi5, LattePanda, RK3588)의 꼬리 지연·열 throttling과
   SmolVLM2의 입력 단위 수(타일) 변동이다.** 그래서 shadow 비교(M4)와 실측 검증(07 문서)을 통과해야
   전환한다. 통과 전에는 재생이 주 평가기다.
8. 수치 중 측정하지 않은 것은 모두 `[추론]`으로 표시했다. 코드 근거는 파일:줄로, 외부 근거는 링크로 남겼다.

## 출처 기준점 (provenance)

- 코드 근거: `/home/genesis1/ETRI/chill-pools` worktree, branch `feat/workload-pools` @ `42a7f98`.
  이 commit은 main `99621a6`에 spec 문서만 더한 것이라 **코드 줄 번호는 main과 같다**.
- 명세 인용: `42a7f98`의 `docs/spec/` (POOL·자동제어 개정 포함). main의 spec에는 이 개정이 아직 없다.
- 리뷰 대상 설계: 위 worktree의 **미커밋** 파일 `docs/design/serving/workload-pools.md`,
  `docs/adr/0022-workload-pool-membership-boundary.md`. 절 번호(§3.3, §3.4, §4.5, §7 등)는 이 파일 기준이다.
- 읽은 명세: `docs/spec/README.md`, `scheduler/SRS.md`, `00_measurement.md`, `03_routing.md`,
  `04_configuration_selection.md`, `05_transition_control.md`.

## 문서 구성 (읽는 순서)

| 문서 | 내용 |
|---|---|
| [01-current-premise.md](01-current-premise.md) | 현재 전제 A: 무엇이 구조적으로 복잡·느리게 만드는가, 코드 근거와 수치 |
| [02-b1-two-timescale-planner.md](02-b1-two-timescale-planner.md) | B1: 두 시간척도 분리와 분석형 용량 계획기 |
| [03-b2-single-writer-node-state.md](03-b2-single-writer-node-state.md) | B2: 노드 상태 단일 writer, native fence, 측정 admission |
| [04-b3-decentralized-baseline.md](04-b3-decentralized-baseline.md) | B3: 완전 분산 구조(기준선 용도) |
| [05-recommendation-migration.md](05-recommendation-migration.md) | 권고, 단계적 이행 M0–M5, 남는 코드, workload-pools commit과의 관계 |
| [06-spec-change-proposals.md](06-spec-change-proposals.md) | 바꿀 명세 문장(현재 문장 원문 → 제안), 바꾸지 않는 문장 |
| [07-validation.md](07-validation.md) | 제안을 확정·기각할 측정 |
| [references.md](references.md) | 외부 출처 목록과 원문 확인 여부 |

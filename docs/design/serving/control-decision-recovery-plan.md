# SLO 개선을 위한 배치 제어 복구 계획

## 1. 문제와 목표

CHILL은 허용된 자원·물리 제약 안에서 E2E SLO를 충족하는 배치를 선택하고, 충족 후보끼리는 에너지를 줄인다. 충족이 어려우면 기존 실패·지연 비교 기준으로 개선할 배치를 선택한다. 이 판단은 실제 제어에 유효한 시간 안에 나와야 한다.

control374에서는 IC 과부하를 관측했지만 작은 예측 개선만 선택했고, 전체 Tick 중앙값은 약 45~47초였다. 먼저 왜 더 나은 배치로 대응하지 못했는지 같은 decision 입력으로 확인한다. 계산 지연과 선택·실제 적용을 구분하고, 원인이 확인된 경계만 수정한다. 요청 잠금 재발 근거는 없다.

기준 소스: `4f2d97ca6a9ae09539aa205934d7784c4b2365aa`(Runtime `4fac6584`, 후속 배포 도구 수정 포함).
근거: `ops/pools-deploy/deployB/control-374-control-loop-20261110/final-report.md`와 `intent/control374-followup-20261110/findings.json`.

## 2. 확인한 사실과 미확정 원인

| 경계 | 확인한 사실 | 판단·다음 행동 |
|---|---|---|
| exact 입력 | 서로 다른 후보마다 동일 arrival 반복·최소수요 보충·정렬을 재구성한다. 같은 후보 결과의 memo는 이미 존재한다. | 불변 입력 재사용을 별도 비용 개선으로 검증한다. 배치 실패 원인의 증명으로 쓰지 않는다. |
| 실패 potential | 종료 집계에서 상태가 변하지 않는 queue를 실패 요청마다 다시 훑는다. | 같은 집계 pass의 중복 제거 효과를 측정한다. expiry/drop의 변화하는 queue 의미는 유지한다. |
| 측정 집계 | 자식 `INVALID/GeneratorInvalid`를 summary/publication이 서술적 SLO FAIL로 덮는다. | 별도 결과 전달 결함으로 수정한다. 배치 원인 분석·수정의 선행조건으로 두지 않는다. |
| 후보 선택 | 실제 평가기로 만든 누락 가설 사례에서 기존 Funnel이 빠른 대안을 이미 선택했다. | 가설을 근거로 ranking·fallback을 변경하지 않는다. |

보존 CPU 표본은 exact 경로에 cohort 초기화와 potential 계산 비용을 보여 준다. 누적 CPU 시간은 중첩되므로 합산하거나 Tick 절감량으로 환산하지 않는다. 실제 `ExactK1` 비용도 존재하므로 모든 overload에 전체 K1 탐색을 추가하는 수정은 하지 않는다.

과거 06:50:51 decision과 로컬 compact snapshot 06:51:47은 서로 다른 입력이다. 현재 확보본만으로 그 Tick을 재현할 수 없다. 기존 scheduler archive·catalog·request stream의 원문 회수 가능성은 아직 확인하지 않았다. prepare 기본값 852초를 모든 후보의 실제 비용으로 간주하지 않는다.

## 3. 우선순위와 책임별 작업

### 먼저: 배치 판단의 근원 경계 확정

1. **기존 원문 복원:** 해당 decision의 scheduler archive·catalog·request export를 기존 full reader와 `hack/kappa` 복원 경로로 결합한다. Options·Outstanding·선택 DispatchOccupancy·profile/비용 참조는 scheduler 기록에, arrivals는 request stream에 저장된다. 로컬 compact 누락과 저장 원문 부재를 구분한다.
2. **기준 결과 재현:** 같은 설정·seed·물리 조건에서 keep와 선택 후보의 exact 결과를 먼저 재현한다. 입력 차이나 복원 불가능한 필드는 명시한다.
3. **대안과 손실 경계 비교:** 허용되는 IC 대안의 기존 objective·에너지·전환 비용을 비교한다. 개선 후보가 생성·축약·정밀 평가·최종 선택·실제 적용 중 어디서 탈락하거나 달라졌는지 확인한다. 16 finalists를 전체 후보로 간주하지 않는다.
4. **확정 경계 수정:** 개선 후보가 손실된 소비자 사례를 남기고 그 책임 경계만 수정한다. 검토한 대안에서 개선이 없으면 해당 범위의 제약을 설명한다. 이를 전체 최적성이나 비연산 병목의 증명으로 확대하지 않는다.

필요 원문 부재가 확인된 경우에는 누락 항목과 역사적 재현 한계를 기록한다. 다음 승인된 관측에서 main이 기존 경로로 새 decision 입력을 확보하도록 인계한다. 새 입력은 과거 원인을 증명하지 않는다. 별도 기록 프레임워크나 런타임 거절 관문은 추가하지 않는다.

### 독립 보조 작업과 커밋 경계

| 단위 | 책임·소유 파일 | 선행·완료 기준 |
|---|---|---|
| P 배치 경계 수정 | 재현으로 특정한 planner/adapter/실행 책임 | 위 1–3의 근거에 의존한다. 더 나은 적격 대안의 선택 또는 정상 적용을 소비자로 검증한다. |
| R1 불변 입력 재사용 | Serving planner와 replay 입력 구성 | 동일 입력·seed의 결과 보존과 시간·할당 개선으로 유지 여부를 결정한다. |
| R2 종료 집계 경량화 | Serving feedback potential 집계 | 고정 상태의 중복 계산만 줄인다. frozen 요청·후보 격리·취소와 수치 의미를 보존한다. |
| V1 측정 유효성 전달 | evaluation judgment/publication | 기존 `INVALID > NOT_EVALUATED > FAIL > PASS`와 원 reason 전달. 서술적 SLO·execution·cleanup은 별도 유지한다. |
| I 통합 | advisor-2 | 배치 효과와 계산 비용을 각각 판정하고 정확 source·남은 한계를 main에 인계한다. |

R1/R2는 한 소유자가 상태 수명을 통합하며, 배치 원인 규명의 선행조건으로 두지 않는다. 현재 구현은 체크포인트에 보존하고 효과 검증 전 확대하지 않는다. V1도 독립 커밋으로 분리한다. 이들 완료만으로 배치 제어 문제를 종료하지 않는다.

알고리즘·예산·SLO·분위수·seed·minimumDemand·비교기간·물리 admission은 비용 최적화로 바꾸지 않는다. 배치 규칙 변경이 필요하면 재현된 원인과 기존 목표에 근거한다. 과거 원문과 published 결과는 보존한다.

## 4. 로컬 확인

- 기존 direct evaluator와 planner가 같은 후보의 population·outcome·실패율·분위수·potential·conditional evidence·선택을 반환하는지 비교한다.
- 동시각 arrival 순서, 최소수요 보충, 기존 outstanding의 frozen 조건, capacity용 별도 Poisson 경로, 취소 및 병렬 후보의 격리를 검사한다.
- 동일 입력의 기준/수정본에서 실패가 많은 replay의 시간·할당을 측정한다. 속도 자체를 영구 테스트 단언으로 고정하지 않는다.
- 보존된 두 stage를 실제 summary/publication 소비자로 처리해 INVALID 전달과 서술적 SLO FAIL 보존을 확인한다. 과거 결과를 다시 게시하지 않는다.
- 독립 리뷰에서 책임 분리·과구현·추가 거절 관문과 메모리 보유·경쟁·연산 중복을 따로 검토한다.
- 영향 회귀 후 lint, 전체 test, architecture 및 pinned gopls를 실행한다. 실패·skip·재사용 범위를 구분한다.

## 5. main의 배포·실수용

정확 source와 변경 역할을 공식 fast-deploy로 배포한다. 기존 공급·설정·profile/raw를 보존하고 기존 동일 두 정책 E5에서 다음을 확인한다.

1. **배치 효과:** 같은 입력에서 확인된 개선 대안이 기존 목표에 따라 선택되고 실제 적용·실행되는지 연결한다. SLO 충족 후보끼리는 에너지를, 미충족 후보끼리는 기존 실패·지연 비교와 동률 에너지를 확인한다. 예측 개선과 offered 전체의 on-time/late/error/timeout을 구분한다.
2. **제어 시간:** 전체 round/Tick·capture/exact/기록 시간과 주기 초과 횟수·최대값을 연결한다. 현재 10초 제어 주기 충족 여부를 별도 판정한다. 경량화만으로 충족을 미리 선언하지 않는다.
3. **보존과 비용:** 실제 실행·frozen POST·공급·설정 보존과 heap·allocation·GC·working set·재시작을 확인한다. Tick-local 입력이 다음 Tick에 누적 보유되지 않아야 한다.
4. **결과 전달:** workload의 실제 SLO 결과와 stage/전체 측정 유효성을 별도로 확인한다. 시작 skew와 요청별 지각률은 독립 조건이다.

완료 보고는 배치 원인·수정 효과, 제어 시간, 측정 유효성을 각각 판정한다. 원인이 미확정이면 그대로 남기며, 계산 가속이나 집계 수정의 성공으로 대신 닫지 않는다.

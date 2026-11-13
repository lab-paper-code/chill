# 측정 노드 승인과 단일 writer

ChillServer가 `chill.dacs.io/measurement:NoSchedule` taint와 측정 예약을 소유한다. 승인, 자동제어와 수동 작업은 같은 physical 변경 권한을 사용한다. host 전력은 [플랫폼 노드 제어](../platform/node-agent.md)를 따른다.

## 요청 루트와 terminal

| 루트 | 대상 | terminal |
|---|---|---|
| BatchEnergyOptimization | `spec.state.targetNode` | Complete, Failed. CurvePlanResolutionFailed는 재시도 |
| ModelLoadedIdleBaseline | `spec.state.targetNode` | Complete, Insufficient |
| 독립 ProfilingRun | `spec.targetNode` | ExecutionComplete True 또는 False |
| PowerModeTransitionCost | `spec.targetNode` | Complete, Failed |
| OperationCostMeasurement | `spec.node` | Complete, Insufficient, Failed |
| DispatchOccupancyMeasurement | `spec.node` | Complete, Insufficient, Failed |

BEO와 idle 자식 ProfilingRun은 승인된 root 아래 생성한다. 일시 Kubernetes와 evidence 오류를 측정 조건의 terminal 실패로 바꾸지 않는다.

## 직렬화와 현재 입력

Admit는 `tickMu`로 Node 승인과 제어 진행을 직렬화한다. 긴 runner 준비, 측정과 반환은 범위 예약된 actionRun으로 등록한 뒤 잠금 밖에서 실행한다. 같은 범위에 다른 writer를 허가하지 않는다.

namespace별 여섯 kind를 metadata-only List로 읽는다. UID와 opaque resourceVersion이 같으면 decoded payload와 terminal 판정을 재사용한다. 새 객체와 변경 객체만 GET하며 사라진 identity를 제거한다. 초기화는 종류별 full List 뒤 metadata boundary를 검증한다.

모든 boundary 완료 뒤 creationTimestamp, namespace/name 순으로 정렬한다. 읽기 실패나 취소이면 그 round의 승인과 반환 판단을 하지 않는다. Node taint와 RV, power pending, serving Pod와 DeviceClass는 매 Admit 직접 읽는다. Tick은 window 조회와 비동기 반환 뒤 live 사실을 새로 읽는다.

## 승인과 거절

노드당 root 하나다. 앞 root 대기는 같은 노드의 뒤 root를 기다리게 한다. 거절은 terminal이므로 다음 root를 같은 round에서 평가할 수 있다.

| 조건 | 처리 |
|---|---|
| 예약, taint, power pending과 앞 root 대기 | 다음 round 대기 |
| Node 없음 | Refused NodeMissing |
| CHILL serving Pod 존재 | Refused NodeServing. terminal Pod도 삭제 전 점유 |
| executor recovery unknown | Refused NodeServing |
| root별 profile와 runner 부적격 | 해당 사유 거절 또는 catalog 갱신 대기 |
| 나머지 | root UID taint와 Granted 시각. 전환 비용은 runner 준비 뒤 Granted |

CPU affinity/quota와 online−reserved는 gate가 아니다. Node identity, 실제 power, owner와 격리 proof는 유지한다. 외부 Pod는 삭제하지 않고 census에 남긴다.

taint는 resourceVersion merge patch이며 충돌은 재관측한다. 승인 Node는 capture와 실행 직전 검사에서 서빙 배치와 전력 변경 대상에서 제외한다. power pending 기한은 요청과 정책에서 구하고 누락 시 임의 추정하지 않는다.

## 전환 비용 runner와 operation

전환 비용 root는 정확한 Node와 실행 경로, 시작 모드의 최신 준비 가능 profile이 필요하다. RunnerProfileMissing, RunnerContainerUnsupported, RunnerNamespace와 RunnerSourceStateMismatch를 구분한다.

taint 뒤 runner를 준비하고 Node UID label의 Service를 정확한 serving Pod UID selector로 연결한다. profile ID와 Granted 시각은 준비 결과에 고정한다. 실패는 RunnerPrepareFailed이며 같은 owner가 반환한다. root 삭제 뒤에도 Node UID로 회수한다.

opcost는 meter와 요청 가능한 power mode, BEO 상태와 대상 Node를 확인한다. catalog에 profile와 issue 모두 없거나 준비 근거가 아직 없으면 기다린다. ProfileMissing, NodeMismatch, PowerModeUnknown, MeterUndeclared를 사유로 남긴다.

steps는 production Measure로 순서 실행한다. ID는 `<CR UID>.<순번>.<단계이름>`이다. prepare의 정확한 실물을 remove에 쓴다. 첫 오류나 non-Accepted에서 중단하고 partial과 개별 verdict를 보존한다.

미완료 sequence는 `serving/operation-sequences/<CR UID>.json`이다. 재시작으로 끊긴 Granted/nonterminal opcost는 ChillServerRestart와 저장 step 목록을 남기고 반환한다. terminal 결과는 다시 쓰지 않는다.

DOM은 비자율 Workload, serving 충돌, batch 근거와 frozen native queue/부하 한도를 확인한다. burst 판정, signed 통계와 scheduler 채택은 [측정 API](measurement-api.md)의 별도 경계다.

## 정상 반환과 무효 근거

terminal이나 삭제 root, 살아 있는 root/run이 없는 taint와 종료 operation은 hand-back을 만든다.

1. power pending을 실제 정책 기한까지 기다린다.
2. 해당 load Job과 잔류 CHILL serving Pod를 정상 ClearNode로 제거한다.
3. Node UID label의 runner Service를 정리한다.
4. Node UID와 taint holder를 재확인한다.
5. 요청 가능한 최신 unloaded baseline의 최저 모드로 변경하고 독립 readback을 확인한다. Fixed나 근거 부재는 전력 단계를 건너뛴다.
6. 현재 RV로 taint를 제거하고 예약을 반환한다.

실패하면 taint와 미확인 책임을 유지하고 다음 round에서 반환을 재시도한다. goroutine 종료나 root 삭제만으로 반환을 주장하지 않는다. root 삭제는 Interrupted holder다.

record의 power generation과 자기 전환 수로 예상 밖 power 변경을 판단한다. MeasurementInvalid는 catalog, cost, baseline과 curve 소비에서 제외하고 이유를 노출한다.

## 저장 경계

taint와 admission annotation은 승인 상태를 나타낸다. operation은 `chill.dacs.io/serving-operation-measurement.v1alpha2`, sequence는 `serving-operation-sequence.v1alpha1`이다. legacy scalar DOM과 신규 burst DOM은 별도 prefix와 schema다. 상세는 [저장 계약](measurement-api.md#저장-계약)을 따른다.

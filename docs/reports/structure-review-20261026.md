# 구조 검토: 다섯 본질과 현재 처리 상태

2026-10-26 07:34Z 기준. 시연 2회차의 중복 준비 사고를 개별 예외로 봉합하지 않고 약25개 발견을 다섯 묶음(K1–K5)으로 조사했다.
현재 코드·[scheduler SRS](../spec/scheduler/SRS.md)·[구현 설계 §5.4](../design/serving/scheduler-implementation.md)가 정본이다.
원 보고서의 file:line은 **발견 당시 위치**이며 현재 소스 줄 번호와 다를 수 있다.
현재 판정은 해당 경계의 코드 읽기와 checkpoint 후속 기록을 구분해 적었다. 새 실행·검증은 하지 않았다.
미해결은 후속 수정 완료 근거가 없는 남은 경계이며 모든 시나리오가 운영에서 실제 발생했다는 뜻이 아니다.

## 결정과 판정 기준

- 사용자 결정(10월26일): 안 ‘나’, 시연 3회차 전에 자원 장부 도입.
- Pod 기준 물리 실재와 최근 native 관측을 분리한다.
- 작업별 실제 영향 Node·Workload를 먼저 예약한다.
- 진행 작업의 범위만 고정하고 무관한 자원의 판단을 계속한다.
- 새 서비스·저장소·사용자 정의 자원(CRD)을 만들지 않는다.
- [ADR 0023](../adr/0023-resource-ledger.md)과 [사고 보고서](../postmortems/0002-demo2-duplicate-preparation.md)에 채택 근거를 연결한다.
- 해결됨은 해당 발견의 범위를 뜻하며 묶음 전체 해결을 뜻하지 않는다.
- 보류는 의도된 정책 또는 별도 승인·근거가 필요한 변경이다.
- 코드상 위험과 실측 피해를 구분하며 수치 없는 인과 효과는 주장하지 않는다.

## K1. 앎의 상태: 모름·없음·전환 중·낡음을 분리

본질: 지금 확인하지 못했다는 사실은 물리적으로 없다는 사실이 아니다.
존재·관측 신선도·Ready·정책 적용은 동시에 다른 값을 가질 수 있다.

### K1-1 정책 응답 대기를 실물 소실로 해석

- 발견: 정상 정책 변경 중 ACK(수락 응답)를 기다리는 인스턴스가 오류 목록으로 빠졌다.
- 피해: 빈 자리로 계산해 같은 CPU에 두 번째 Pod를 준비했다.
- 근거: 사고 경로 `observer.go`, `scheduler_capacity.go`; 상세는 postmortem을 따른다.
- **현재: 해결됨.** 실물 장부가 관측 실패에도 점유를 보존하고 정책 대기를 별도 이유로 드러낸다.
- 후속: rev77 Ready 전체 초기화도 제거했으며 metadata/native not-ready는 dispatch를 차단한다.

### K1-2 시작 사건은 기억하지만 종료 사건 유실을 회복하지 못함

- 발견 당시: `internal/serving/runtime.go`의 `finishAbsentLocked`.
- 현재 근거: `runtime.go:919–939`, `c.progress != nil`이면 부재 readback으로 종료하지 않는다.
- **현재: 미해결.** 과거 queued/running 기록이 있는 요청은 현재 native 부재만으로 소유권을 해제하지 않는다.
- 국소안: 완전한 현재 상태의 시각·실행 신분과 종료 계약을 확인하는 제한적 회복.
- 주의: 단순 timeout이나 목록 누락만으로 실행이 끝났다고 선언하면 안 된다.

### K1-3 전력 모드의 저장값과 물리 관측 신선도

- 발견 당시: node-agent가 같은 boot의 이전 PowerActuator를 보존하고 readBackMode가 나이를 검사하지 않음.
- 근거 범위: node-agent/전력 readback 및 scheduler capture 경계.
- **현재: 미해결.** 구조 장부 도입만으로 장치 원천 관측 시각·만료 계약을 완성한 것은 아니다.
- 국소안: 마지막 성공 물리 관측 시각과 API 읽기 시각을 구분한다.
- 주의: 현재 Node API 조회 성공을 장치의 현재 전력 확인으로 바꾸지 않는다.

### K1-4 독립 실행원의 관측을 전체 lock 아래 직렬 수집

- 발견 당시: `observer.go:190–219,253–263`; probe 최대2초가 순차로 누적.
- 현재 근거: `observer.go:254–289`는 goroutine별 probe 후 짧은 lock 아래 개별 게시하고 대기 중 lock을 해제한다.
- **현재: 해결됨(직렬 native probe와 일괄 게시).** Ready 초기화도 제거돼 다른 probe의 미완료가 기존 Ready를 지우지 않는다.
- 초기 전체 메타데이터 조회 실패 영역은 K4-6에 별도로 남는다.
- 실증 경계: 코드의 병렬 게시 계약 확인이며 모든 관측 의존성의 장애 격리 완료는 아니다.

### K1-5 로그 전달 장애를 추론 실행원 장애로 번역

- 발견 당시: `followLogs`의 stream 실패가 실행원 disable로 연결.
- 현재 근거: `observer.go:887–904`, `runtime.DisableInstance(id, err.Error())`.
- **현재: 미해결.** 로그 통로의 실패가 Ready에 영향을 주는 경로는 남는다.
- 국소안: 진행 증거 통로의 건전성과 추론 서비스 readback을 구분한다.
- 주의: 로그를 버리면 finality가 보장된다는 뜻이 아니며 요청 소유 증거를 보존해야 한다.

### 조사한 외부 해법과 선택

- [Kubernetes API conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties): 조건의 부재는 false가 아니라 Unknown.
- [OPC UA DataValue](https://reference.opcfoundation.org/specs/OPC-10000-4/7.11): 값·품질·원천/전달 시각을 함께 운반.
- [Envoy xDS](https://www.envoyproxy.io/docs/envoy/latest/api-docs/xds_protocol#ack-and-nack-semantics-summary): 요청·수락·실제 활성 상태를 구분.
- [Job replacement KEP-3939](https://github.com/kubernetes/enhancements/tree/master/keps/sig-apps/3939-allow-replacement-when-fully-terminated): 종료 중과 물리 반환 완료를 구분.
- 구조안은 성공 probe 목록을 세계 모델로 쓰는 계약을 없애는 것이다. 채택한 장부가 이 부분을 수행했다.
- 남은 국소 문제를 하나의 거대한 상태 enum으로 합치거나 모든 Unknown을 실패로 바꾸지 않는다.

## K2. 쓰기와 권위: 중간 실패 뒤 누가 끝맺는가

본질: 하나의 변경이 여러 흔적에 나뉘고, 끊긴 절차 뒤 책임과 재발견 주소가 사라진다.
객체별 낙관적 잠금은 이미 있으며 다중 객체 원자성까지 제공하는 것은 아니다.

### K2-1 정책을 ConfigMap과 Pod에 순차 기록

- 발견 당시: `operations.go:1087–1108`, `firstfloor/config.go:177–206`.
- 현재 근거: `operations.go:1152–1168`, ConfigMap patch 뒤 Pod 기대 DecisionID patch.
- **현재: 미해결.** 첫 쓰기 성공 뒤 실패를 지속적으로 완성할 권위·재조정 책임이 별도 확정되지 않았다.
- 국소안: 부분 성공과 기대값을 재판독해 미완료 쓰기를 끝낸다.
- 구조안: 실제 설정의 권위를 하나로 하고 Pod는 투영·신분 역할만 맡긴다. 별도 승인 없이 새 저장소를 추가하지 않는다.

### K2-2 movingNodes 실패 경로 누수

- 발견 당시: `operations.go:786–794`.
- 현재 근거: `operations.go:829–836`, 표시 등록→detach 오류 반환 뒤에 defer 등록.
- **현재: 미해결.** detach 실패 시 임시 표시가 남을 수 있다.
- 국소안: 표시 취득 직후 해제 defer를 등록한다.
- 이는 같은 프로세스의 예외 안전성 문제이며 분산 합의·영속 작업 엔진은 필요 없다.

### K2-3 정리 주소를 잔재보다 먼저 삭제

- 발견 당시: `operations.go:708–745`, Pod 제거 후 ConfigMap 삭제.
- 현재 근거: `operations.go:724` 이후 finishInstanceRemoval의 종료 확인·Service·finalizer 처리.
- **현재: 미해결.** 물리 종료 증거 보존은 강화됐으나 Pod 뒤 잔재 정리의 재발견 책임까지 닫힌 근거는 없다.
- 국소안: 남은 종속 자원 정리를 끝내기 전 책임 주소를 유지하거나 ownerReference를 활용한다.
- 주의: 이미 Service ownerReference와 finalizer가 있으므로 ‘GC/finalizer 전면 부재’로 기술하지 않는다.

### K2-4 실행원 생성 전 Pod를 일반 정리 경로가 받지 못함

- 발견 당시: `operations.go:417–440,1206–1230`, `manual_reset.go:158–160`.
- 현재 근거: `removeInstance(..., podUID, id, ...)`는 빈 실행 ID와 Pod 신분을 분리한다.
- **현재: 해결됨.** Pending 준비 Pod hand-back 차단 수정과 late-probe 정리가 반영됐다.
- Pod UID를 생성 의도의 신분으로 유지하고 종료 증거 없이 가동 Pod를 지우지 않는다.
- 이 해결은 K2-3의 모든 ConfigMap 잔재 수명 문제까지 해결했다는 뜻이 아니다.

### 조사한 외부 해법과 선택

- [Kubernetes Controllers](https://kubernetes.io/docs/concepts/architecture/controller/): 사건 절차가 아니라 현재 desired/actual을 반복 조정.
- [AWS transactional outbox](https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html): 상태와 전달 책임을 같은 원자 경계에 둠. 별도 DB 도입 처방은 아니다.
- [Finalizers](https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/)와 [소유 관계](https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/): 뒤처리까지 주소 보존.
- [Crossplane creation annotations](https://docs.crossplane.io/v1.20/concepts/managed-resources/#creation-annotations): 외부 실행원 생성 전에도 책임 신분 필요.
- [Go defer](https://go.dev/blog/defer-panic-and-recover): 임시 자원은 획득 직후 반납 등록.
- 국소 실패 반환 수정과 장기 권위 cutover는 별개다. 후자를 전자에 끼워 넣지 않는다.

## K3. 실행 소유와 승인: 허가·영향 범위·신분 일치

본질: 과거에 안전했다는 사실은 지금의 허가가 아니며, 시작한 작업은 실제 영향 범위를 끝날 때까지 맡아야 한다.

### K3-1 마지막 Tick 스냅샷으로 수동 삭제·배분 승인

- 발견 당시: `measurement_sequence.go:275–290,440–461`.
- 현재 근거: `measurement_sequence.go:303–307`가 허가 직전 `c.capture`를 수행한다.
- **현재: 해결됨.** rev84에 반영됐고 과거 snapshot이면 통과했을 거절2건을 기록했다.
- 작업 완료 뒤 다음 Tick 전의 route/remove도 현재 상태로 승인한다.
- 효과 실행 전체의 보호는 별도 예약이 맡는다.

### K3-2 route가 영향 Node를 검사하지만 실행 중 소유하지 않음

- 발견 당시: `StructActions2` 발견2, Workload만 있고 Operation.Node가 비어 있었음.
- 현재 근거: `kube/reservation.go`, `target_scope.go`, `reservation_tick_test.go`.
- **현재: 해결됨.** 공통 영향 범위 예약으로 source/destination Node와 Workload를 보호한다.
- 종료 partial route는 fresh UID/container·Ready·정책 효과 재확인 뒤 해제한다.
- reset의 종료 owner 인계도 범위 내 원자 인계로 제한된다.

### K3-3 같은 Workload를 다른 Node에서 동시 측정

- 발견 당시: `StructActions2` 발견5, Node만 다른 측정의 Workload 간섭.
- 현재 근거: 공통 예약의 Workload 충돌 검사와 측정 범위.
- **현재: 해결됨.** Node별 비충돌만으로 Workload 공유를 허용하지 않는다.
- 살아 있는 측정 owner를 reset이 인계하지 못한다.
- 측정 컨트롤러 프로세스 이중 실행 여부는 별도 운영 확인으로 남는다.

### K3-4 논리 자리와 물리 인스턴스 대응 파괴

- 사고: 같은 slot·CPU의 Pod 둘, reset도 같은 자리의 복수 실물로 실패.
- 현재 근거: Pod UID/container 기준 실물 장부와 source 종료 확인.
- **현재: 해결됨.** 관측 소실로 점유를 반납하지 않고 물리 신분을 유지한다.
- 늦은 probe가 퇴역 source를 되살리는 후속 결함도 수정됐다.
- API 객체 부재만으로 아직 실행 중인 물리가 종료됐다고 간주하지 않는다.

### 조사한 외부 해법과 선택

- [Kubernetes Scheduling Framework Reserve/Permit](https://kubernetes.io/docs/concepts/scheduling-eviction/scheduling-framework/#reserve): 짧은 판단과 느린 실행 사이 예약.
- [PostgreSQL locking](https://www.postgresql.org/docs/18/explicit-locking.html#LOCKING-ROWS): 읽은 안전 조건의 대상을 변경 완료까지 보호.
- [Kubernetes API concepts](https://kubernetes.io/docs/reference/using-api/api-concepts/#updates-to-existing-resources): resourceVersion과 UID는 변경 버전/객체 생애로 서로 다름.
- [StatefulSet force deletion 경고](https://kubernetes.io/docs/tasks/run-application/force-delete-stateful-set-pod/): 이름 반환과 물리 종료를 혼동하지 않음.
- [Chubby §2.4](https://research.google.com/archive/chubby-osdi06.pdf): 늦은 명령 fencing은 수신자가 실제 거절해야 유효.
- 채택 구조는 기존 tickMu 아래 영향집합 승인·예약 등록이다. 범용 잠금 프레임워크는 만들지 않는다.

## K4. 장애 격리와 진행성

본질: 한 대상의 불확실성·대기 때문에 무관한 대상의 판단권까지 회수한다.

### K4-1 전역 CPU 검사·긴 목표 장벽

- 발견 당시: `scheduler_capacity.go:146–175`; 1회차 B 판단36개 중32개가 전이 지속.
- **현재: 해결됨.** 예약된 범위만 Fixed로 남기고 독립 자원 판단을 계속한다.
- 확장 시험(나)는 긴 Smol 준비 중 rasp5 MNV 준비18초 완료로 실증했다.
- 남은 한계: 같은 진행 목표에 나중에 풀린 Node를 보충 확장하는 경로는 미해결이다.

### K4-2 끝없는 route/제어 State 조회

- 발견 당시: `scheduler.go:999–1005`, `operations.go:1105–1140`.
- 현재 근거: `scheduler.go:1026` 기본 action context는 WithCancel, 정책 State는 작업 ctx 사용.
- **현재: 미해결.** 독립 범위의 진행성은 회복됐지만 모든 작업의 유한 종료를 보장하지 않는다.
- 국소안: 제어 RPC와 작업 전체 deadline을 구분해 제한한다.
- timeout은 물리 효과 부재가 아니므로 예약 자동 해제 근거로 쓰지 않는다.

### K4-3 한 Workload의 κ 계산 불능이 전체 대기로 합쳐짐

- 발견 당시: `scheduler_capacity.go:192–194,451–463`.
- **현재: 해결됨(기존 전역 대기).** leximin과 예약 범위별 판단으로 불가능 Workload가 나머지 후보를 동점으로 만들지 않는다.
- 빈 출발은 후보 κ 전체를 기본10분/0–1시간 상한으로 기다리는 의도된 예외다.
- **보류:** κ 대기 Workload를 위한 Node 선점 금지(B안)는 알려진 한계이며 미구현이다.
- 빈 출발 정보 장벽과 전역 실행 완료 장벽을 같은 것으로 보지 않는다.

### K4-4 증거 저장소 갱신 실패와 기존 판단의 결합

- 발견 당시: `cmd/chill-server/scheduler_service.go:423–446`, `kube/cost.go:103–114`.
- **현재: 미해결.** 새 갱신 실패와 마지막 승인 설정의 명시적 무효화를 구별할 필요가 남는다.
- 기존 추론이 S3 LIST 실패만으로 즉시 중단된다는 주장은 아니다. 해당 Admit/Tick 진행의 결합이다.
- 국소안: 이미 승인된 설정으로 안전하게 가능한 판단과 새 증거가 필수인 판단을 분리한다.

### K4-5 기록 오류 래치가 복구 뒤에도 admission 차단

- 현재 근거: `runtime.go:176,260,367,721,1056`의 observationError.
- **현재: 미해결.** 실패 기록 재시도와 사용 가능 상태 복구의 책임이 분리돼 있다.
- 국소안: 미완료 final 기록·sync를 끝낸 복구 조건으로만 차단을 해제한다.
- ENOSPC는 소스상 가능한 조건이며 이번 실제 발생 사례로 주장하지 않는다.
- 임의 성공 append 한 번으로 과거 오류를 지우는 안은 채택하지 않는다.

### K4-6 전체 관측 메타데이터 실패 확산

- 현재 근거: `observer.go:197–217`의 Nodes/Pods/resources/DeviceClass 실패→refreshFailure.
- **현재: 미해결.** 초기 메타데이터의 전체 실패 영역은 남는다. K1-4의 native probe 병렬화와는 별개다.
- UID selector 성능 수정은 이 장애 의미를 바꾸지 않았다.
- 국소안은 대상별 마지막 검증 결과와 유효기간을 제한적으로 보존하는 것이다.

### 조사한 외부 해법과 선택

- [controller-runtime reconcile](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.19.0/pkg/reconcile/reconcile.go): 정상 미완료는 재큐, worker 반환.
- [Google SRE cascading failures](https://sre.google/sre-book/addressing-cascading-failures/): deadline과 공유 자원 점유 제한.
- [AWS static stability](https://docs.aws.amazon.com/whitepapers/latest/aws-fault-isolation-boundaries/static-stability.html): 새 제어면 자료 없이 기존 승인 상태 유지.
- [RocksDB error recovery](https://github.com/facebook/rocksdb/wiki/Background-Error-Handling): 복구 책임으로 오류 래치 해제.
- 구조안은 전역 최적화기는 유지하고 전역 완료 장벽만 삭제하는 것이며 이미 채택됐다.

## K5. 제어 동역학: 늦은 공급과 늙는 요청

본질: 현재 용량·준비 중 약속·새 유입·이미 밀린 일을 같은 값으로 계산하면 제어 자신이 만든 지연을 새 수요로 오해한다.

### K5-1 중간 배분에 최종 비율을 재정규화

- 발견 당시: `scheduler.go:949–995`, 작은 목적지만 Ready면 기존 정상 source 제외 가능.
- 현재 근거: `scheduler.go:967` readySubset은 여전히 목표 상대 가중치를 보존한다.
- **현재: 미해결.** 부분 목표 의존성 장벽 수정은 비대칭 용량 배분의 안전성 증명이 아니다.
- 국소안: 기존 Ready source와 새 목적지 합집합에서 κ·현재 rate로 중간 배분을 제한한다.
- 예비 용량 비율 설정은 별도 안전 여유이며 이 발견 전체를 자동 해결하지 않는다.

### K5-2 backlog의 나이·남은 수명 무시

- 발견 당시: `runtime_scheduler.go:373–410`, `scheduler_capacity.go:1019–1047`.
- **현재: 미해결.** 집계 B/W 계산은 개별 요청이 route 이전 만료될 가능성을 전부 표현하지 않는다.
- 국소안: 아직 compute가 필요한 큐/실행 중/반환 중과 남은 수명을 구분한다.
- 준비100초 뒤 큰 κ가 생긴다는 사실로 수명2초 요청의 실패를 회복했다고 세지 않는다.

### K5-3 outstanding을 수요와 backlog에 중복 반영

- 발견 당시: `scheduler_capacity.go:384–400,1019–1020,1250–1256`.
- **현재: 미해결.** 외부 ingress와 제어·반환 지연의 재고를 분리하는 후속 수정 근거가 없다.
- 국소안: λ는 유입 추정, Q는 잔여 compute로 한 번 계산한다.
- 실제 과잉 생성 인과가 입증됐다는 주장은 아니며 식의 되먹임 위험이다.

### K5-4 noisy 수요 비교로 축소 보호 즉시 반전

- 발견 당시: 최근10초>60초 평균이라는 한 부등식으로 growing 보호.
- **현재: 해결됨.** 빠른 증설·느린 축소/에너지 교체, 같은 안정화 기간을 채택했다.
- 기본300초·시연120초, 전환 reserve비율0, 모든 값은 설정·범위 검사·실행 기록에 고정.
- 하강 baseline 미만 유지 수정 후 실제127.96초에 축소 허용을 확인했다.

### K5-5 선택하지 않은 후보가 에너지 비교기간을 늘림

- 발견 당시: `scheduler_capacity.go:500–517`의 모든 eligible 후보 pre/post 최대.
- **현재: 해결됨.** 후보 무관 comparisonPeriodNanos600초 고정, 기간 초과 전환은 제외한다.
- 근거: Jetson 전력 전환 약150초, Smol 준비270–290초, 보수적 직렬 조합 약490초.
- 후보마다 다른 기간을 쓰는 안이 아니라 같은 설정 기간으로 공정 비교한다.

### 조사한 외부 해법과 선택

- [Little 1961](https://doi.org/10.1287/opre.9.3.383), [AWS SQS scaling](https://docs.aws.amazon.com/autoscaling/ec2/userguide/as-using-sqs-queue.html): 점유량과 유입률은 다름.
- [Google SRE managing load](https://sre.google/workbook/managing-load/): deadline 이후 무용해진 작업을 증설로 되살리지 못함.
- [HPA stabilization](https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/), [Knative stable/panic](https://knative.dev/docs/serving/autoscaling/kpa-specific/): 상승·하강 비대칭과 기억.
- [Deployment rolling update](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#rolling-update-deployment): 최종 구성보다 전환 내내 가용 하한 유지.
- [AWS warmup](https://docs.aws.amazon.com/autoscaling/ec2/userguide/as-scaling-target-tracking.html#as-target-tracking-scaling-warmup): 준비 중 약속과 지금 쓸 용량 분리.
- 국소안은 위 각 계산 경계 수정이며 구조안은 현재 공급/준비 중 공급/요청 나이를 분리한 제어다. 새로운 예측 모델 전체 교체는 승인되지 않았다.

## 보류와 후속 실행

- reset 자동 재시도·rollback 부재는 의도된 중단 정책이다. 자동 복구 요구 없이 버그로 바꾸지 않는다.
- release는 Before.Native 미저장으로 Pod 유지 중 효과 재확인 해제가 제한된다. **미해결**이며 route 예약 복구 완료와 구분한다.
- 진행 목표의 additive prepare, Tick 대기 축소, ACK 병렬화는 별도 승인 후보다.
- 미해결 국소 항목은 [남은 길](../plans/2026-10-26-remaining-roadmap.md)에 의존 순서로 연결했다.
- [시연 실증](demos/README.md)은 범위 격리와 축소 제어를 확인했지만 위 모든 오류 경계를 재현한 것은 아니다.

원시 근거: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/structure-20261026/`의 `clusters.md`, `Research-K1.md`–`Research-K5.md`, `StructTruth.md`, `StructFailure.md`, `StructControl.md`, `ControlDynamics.md`, `StructActions2.md`; 운영 루트 `intent/checkpoints.md` 1931–2001행. 중복 조사·에이전트별 보고·원시 스냅샷은 복제하지 않았다.

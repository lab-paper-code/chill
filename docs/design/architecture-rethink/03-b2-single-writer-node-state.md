# 03. B2 — 노드 상태의 단일 writer, native fence, 측정 admission

상태: **채택(B1과 함께, 이행 단계 M3)**. **명세 변경 불필요**(메커니즘만 바뀜). 판단 알고리즘과 독립적이라 A나 B1
어느 쪽과도 조합할 수 있다. workload-pools 설계 §3.3에서 운영자가 손으로 붙이고 떼는 taint `chill.dacs.io/measurement`를
단일 writer가 측정 객체에 따라 다루게 한다.

## 핵심 아이디어

- 노드 **의도**(역할: available / serving / measuring, 측정 grant, 목표 전력모드)를 쓰는 주체를 **하나**
  (ChillServer의 제어 루프)로 정한다.
- 노드 **상태**(inventory, 현재 모드, 전력, Pod census)는 **노드 agent 하나**가 쓴다.
- 이 분담은 Server-Side Apply field manager로 강제한다(다른 manager가 같은 field를 쓰면 conflict).

## 측정 POOL 진입 순서

1. 측정 controller가 Measurement CR에 노드를 요청한다.
2. 단일 writer가 노드가 비어 있는지(CHILL serving 인스턴스 없음) 확인한 뒤 taint
   `chill.dacs.io/measurement=<owner>:NoSchedule`과 grant generation을 쓴다. 서빙 중이면 거절하고 사유를 남긴다
   (`SCH-34`, `DB-FR-34`). taint key는 M3 전의 운영자 절차와 같다.
3. 측정 Job Pod는 toleration을 갖고, kube-scheduler가 다른 Pod의 배치를 거부한다. CHILL serving Pod는 `spec.nodeName`으로
   만들어 kube-scheduler를 거치지 않으므로, 이미 workload-pools §3.3대로 ChillServer가 taint 노드를 배치, 내림, 전력
   변경에서 뺀다. 단일 writer는 같은 프로세스에서 grant와 prepare를 정렬하므로 확인과 동작 사이의 경쟁 구간이 없다.
4. 전력 변경에는 grant generation을 **fencing token**으로 실어 보내고, agent는 오래된 generation을 거절한다.
   만료 뒤 늦게 도착한 요청도 generation이 맞지 않아 거절된다.
5. 측정이 terminal이 되면 controller가 요청을 지우고, writer가 taint를 지운다(반환).
6. 측정 객체가 삭제되면 ownerRef GC가 동작하고, writer가 "요청자 없음"을 보고 taint를 지운다.
   운영자의 수동 정리가 필요 없다.

단일 writer에 필요한 것은 두 가지다. `serving-operation`을 ChillServer에 요청하는 클라이언트로 바꿔 serving Pod
생성 경로를 하나로 줄이는 것과, 측정 controller의 "요청 field → 승인 status" 한 쌍(Kueue AdmissionCheck 패턴)이다.
여러 실행 주체가 각자 점유를 쓰고 확인하는 방식보다 작고, check-then-act 경쟁 구간이 없다.

## 부수 정리

- node-actuator, node-discovery, metric-observer, workload-observer의 노드 기능을 DaemonSet 하나
  (node agent)로 합친다.
- inventory(CPU 목록, GPU UUID, 지원 전력모드, meter)는 agent가 자동 발견해 status로 올린다. 사람이 고치는
  scheduler ConfigMap 필드(`cpuInventory`, `gpuInventory`, `meters`, `powerRequests`)를 없앤다.
  운영자는 정책(예약 CPU 등)만 정한다.
- 측정 CRD(BatchEnergyOptimization, ModelLoadedIdleBaseline, PowerModeTransitionCost, `serving-operation` 측정)를
  `Measurement{spec.kind}` 하나와 Job runner로 합친다. 결과는 지금처럼 S3 불변 객체.
- NodeActuation CRD는 노드 의도의 `spec.powerMode`와 agent reconcile로 대체한다. 동작 관측은
  status.observedGeneration과 Event로 한다(05 명세의 "동작별 관측" 유지).
- Go 바이너리는 init container 복사나 ImageVolume으로 runtime image와 분리한다. runtime image(ORT, RKNN,
  TensorRT, L4T)는 runtime 변경 때만 빌드한다.

## 현재 설계에서 지우는 것

- workload-pools §3.3의 운영자 taint 절차(붙이고 떼는 `kubectl taint`, serving 인스턴스 없음 확인). taint key,
  toleration과 ChillServer의 taint 노드 제외는 그대로 쓴다.
- 운영자가 준비하는 전환 비용 runner와 Service. 단일 writer가 측정 소유로 만들고 반환 전에 지운다.
- NodeActuation의 phase 기계.
- `serving-operation`이 직접 Pod를 만드는 두 번째 생성 경로.

## 자동제어, 전환, 에너지, 관측

- 판단 알고리즘과 독립. 자동제어·전환·에너지 규칙은 A 또는 B1을 따른다.
- 관측: agent가 전력과 inventory metric을 직접 `/metrics`로 내보낸다(B1의 TSDB와 연결).
- 측정 결과의 노드 Pod 목록 기록(CHILL 배치 여부 포함)은 유지한다(`SCH-34`).

## 성능

- 판단 지연에는 영향 없다.
- 측정 진입 대기는 10초 polling에서 watch 사건 기반으로 바뀌어 수 초 → 1초 미만 `[추론]`.

## 실패 처리

- writer가 하나뿐이라 경쟁이 구조적으로 없다.
- agent가 죽으면 노드 Lease가 만료되고 노드가 격리된다.
- writer 재시작 시 taint와 grant가 API 서버에 있으므로 level-based로 복원된다.

## 요구사항

전부 유지(`SCH-34`, `SCH-AC-26` 의미 동일). 메커니즘만 바뀐다.

## 위험

- 단일 writer가 지연되면 측정 진입도 지연된다. 노드 ~10개 규모에서는 무시할 수 있다 `[추론]`.
- CRD 통합은 저장 계약 변경을 한 번 더 일으킨다(ADR 0019 절차를 따른다).

## 검증

- envtest에서 측정 요청과 스케줄러 prepare를 같은 노드에 1000회 동시에 넣어 겹침 0.
- agent kill, 측정 객체 삭제 후 자동 반환.

## 선례 (링크는 [references](references.md))

- Kueue AdmissionCheck/ClusterQueue: Job admission을 단일 주체가 판정.
- Kubernetes taints/tolerations: scheduler가 강제하는 native fence.
- Kubernetes Leases, API conventions(spec/status, level-based).
- Server-Side Apply field ownership.
- Kleppmann, fencing token.
- Kubernetes ImageVolume(KEP-4639).

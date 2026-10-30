# 노드 전력 제어 SRS

상태: 2026-10-30 **2차 검토(fusion: 패널 3모델+판정) 반영**(설정 절, 구현 전).

## U3. NVIDIA discrete GPU 클럭

일반 전력 모드의 제어 범위는 [구성 선택](../04_configuration_selection.md)을 따른다. 이 절은 [ADR 0015](../../adr/0015-node-power-actuation-boundary.md)의 NVIDIA 호스트 어댑터 계약이다.

- `PA-NV-01` 승인된 DeviceClass·모드·Node에서만 GPU 인덱스 0에 `nvidia-smi -lgc min,max`를 적용한다. 최초 구현은 단일 GPU, min=max, mode 생략(드라이버 기본 알고리즘 0)만 지원한다. 다중 GPU·범위·명시적 mode 필드는 변경 전에 거부한다.
- `PA-NV-02` 새 요청은 순간 MHz가 같아도 잠금 명령을 적용하고, 명령 종료와 독립적인 조회로 graphics와 SM의 현재 MHz가 모두 요청한 값인지 확인한 뒤 기존 settling을 거쳐 성공을 보고한다. 이미 성공한 동일 요청의 agent 재시작만 재적용하지 않는다. N/A, GPU 수 불일치, 값 불일치는 성공이 아니다. 현재 클럭 관측은 잠금 레지스터의 읽기가 아니며, 잠금 정책 자체의 증명을 주장하지 않는다. 모드 요청이 없는 상태는 원시 MHz 관측과 별개로 `fixed`로 판별한다. GPU 클럭 모드의 CPU 집합은 현재 Node의 온라인 집합 그대로다.
- `PA-NV-03` 적용 명령 실패·read-back 불일치 시 적용 전 현재 클럭이 선언된 잠금 모드이면 그 모드로 복원하고 다시 읽는다. 그 외에는 `-rgc`로 드라이버 기본 정책을 요청하고 실제 현재 클럭을 보존한다. 실패·복원 실패를 함께 보고하고 측정을 허용하지 않는다. 드라이버가 잠금 조회를 제공하지 않으므로 `-rgc` exit만으로 잠금 해제 검증 성공을 주장하지 않는다.
- `PA-NV-04` 이미 성공한 요청의 agent 재시작은 호스트를 변경하지 않고 재조회하며, 미완료 새 요청은 내구 기록에 따라 적용을 진행한다. 재부팅은 드라이버 기본 정책으로 돌아오며 기존 성공 상태를 재적용하지 않는다. bootID·새 관측으로 기존 측정 근거와 구분하고, 새 모드를 요구하면 새 요청을 제출한다. 이전 모드 복원은 ADR 0020의 별도 전환 요청이며 런타임 컨테이너 종료에 의존하지 않는다.
- `PA-NV-05` NVIDIA 호스트 접근은 Jetson과 분리한다. node-agent와 필요 시 operator 이미지만 재빌드한다. firstfloor/runtime 7 digest는 바꾸지 않는다. 실제 배포·호스트 변경·전환 비용 측정은 CampaignOps 한 명만 직렬로 수행한다.

## 수용 시나리오

| 시나리오 | 결과 | 요구사항 |
|---|---|---|
| 1500 → 900 MHz | graphics=SM=900 독립 조회 및 settling 뒤 성공; 역방향 새 요청으로 1500 복원 | PA-NV-01,02 |
| 명령 권한 거절·지원 안 되는 값·N/A | 실패와 복원 결과 보존, 측정 시작 금지 | PA-NV-01,02,03 |
| 적용 후 1500 대신 300 조회 | 성공 금지, 이전 선언 모드 복원 또는 기본 정책 reset, 복원 실패도 보존 | PA-NV-02,03 |
| agent 재시작·호스트 재부팅 | 재시작은 적용 0회, 재부팅은 새 bootID와 실제 MHz 기록; 암묵 재적용 금지 | PA-NV-04 |
| genesis2 한 대 운영 실증 | 배포 digest, 전·후 조회, 전환·복원 Node 기록 및 1500↔900 비용 CR 보존 | PA-NV-05 |

## 설정 소유권과 관측 정책 기록

- `PA-NV-06` 실제 registry·Node/runner·외부 endpoint·잔존 실행 namespace만 B다. 기존 values의 repository·region·장비 catalog·signature·mode·clock은 D로 유지한다. 내부 mount/port8080·9000/UID65532와 예제·artifact 경로는 패키징/예제 계약으로 둔다. 기본 values는 비우지 않는다. 배포값은 기존 해당 Helm 키 하나, 잔존 helper namespace는 환경 JSON d2.namespace→binding.config.namespace, 운영 샘플은 samples["<기존 상대 경로>"]의 완전한 문서로 이동한다. immutable runtime declaration은 image와 inspection 사실 전체를 이동하며 주소만 치환하지 않는다.
- `PA-NV-07` 기존 Node sampleInterval/batchInterval 배포 설정과 원시 sampledAt/receivedAt을 유지한다. 새 ResourceBatch/resources cadence payload 요구는 삭제한다(기존 설정 D·strict decoder 계약 유지). 두 시각만으로 선택 주기와 실패 누락을 구별한다고 주장하지 않는다. cadence 출처는 기존 배포 설정이며 command timeout·Job overhead/deadline도 기존 설정을 유지한다.

| 수용 시나리오 | 결과 | 요구사항 |
|---|---|---|
| 승인 overlay 경로·revision·release·namespace를 지정해 비교 | default/profiler 별도 render의 의미 있는 delta 확인. values-operating.yaml rev74를 현재 배포값으로 부르지 않음; 기본 testbed profile 유지 | PA-NV-06 |
| 실제 운영 샘플/namespace 선택 | 기존 요청 절차가 환경 JSON 완전 문서를 선택하며 정적 YAML 참조 자동 해석 없음. 실행 중 환경 파일 재조회 없음 | PA-NV-06 |
| 기존 sample1s/batch10s 및 다른 선택 cadence | 기존 배포 설정과 관측 시각/Node identity 유지. 새 payload 필드나 완전 cadence provenance 주장은 없음 | PA-NV-07 |

2026-10-30: PA-NV-06~07 추가. 상세 경계는
[설정 소유권 설계](../../design/serving/configuration-hardcoding.md)를 따른다.

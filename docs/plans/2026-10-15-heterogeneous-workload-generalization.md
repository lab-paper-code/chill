# 이질 워크로드 동시 운영 일반화

## 궁극적 목표

서로 다른 비용 구조를 가진 추론 서비스들이 하나의 일반화된 경로로 같은 POOL에서 동시에
운영되도록 CHILL을 확장한다. 두 번째 서비스는 VQAv2 데이터셋의 이미지와 질문에 짧은 답을
생성하는 SmolVLM2-500M-Video-Instruct이며, 기존 mobilenet 서비스는 이 경로의 가장 단순한 경우다.

브랜치 `feat/heterogeneous-workloads`(worktree `../chill-hetero`)에서 진행하고, 진행 중인
scheduler live evidence 캠페인이 끝난 뒤 main에 병합한다.

## 진행 방식

각 단계는 앞 단계 문서를 권위 있는 기준으로 삼는다. 단계 사이에는 독립 에이전트 검수를 둔다.

1. spec 갱신: 승인 결정 중 시스템 명세 수준의 내용만 기존 문서에 최소 변경으로 반영한다.
2. 카테고리별 SRS: 영향을 받는 SRS를 갱신하고 새 카테고리 SRS를 작성한다. spec과 현재 구현을
   기준으로 누락과 과잉 요구사항을 검증하고, Ponytail 리뷰로 과한 요구를 덜어낸다.
3. 구현 설계: SRS를 만족하는 구현 설계 문서를 기존 설계 문서 형식으로 작성한다. SRS 의도
   반영 여부와 과설계를 독립 에이전트가 검수한다.
4. 구현: 설계 문서에 맞춰 커밋 단위로 구현한다. 각 커밋은 리뷰 에이전트가 spec과 SRS 기준으로
   확인한다. 저장소 gate를 실행하고, 실클러스터 검증은 병합 후 사용자 요청으로 진행한다.

문서 작성 규칙:

- 대체되는 내용은 지우고, 처음부터 그렇게 쓰인 문서처럼 고친다.
- 변경은 최소로 하고 중복 표현을 줄인다.
- 긍정형 서술을 쓴다. "A다", "A해야 한다"로 적는다.
- 새로 쓰는 문장에는 가운뎃점을 쓰지 않는다.

## 승인된 결정

- 클라이언트는 요청마다 원시 샘플 1개를 보낸다. 모델 텐서는 CHILL 내부에서만 만든다.
- 노드 분배 전 입구에서 원시 입력 계약을 한 번 검사한다. 본문만 손상된 항목은 배치 안에서
  해당 요청만 오류로 끝내고 나머지는 완료한다.
- 서빙 파드는 워크로드별 큐, 배치 형성, 전처리와 추론과 후처리로 이루어진 하나의 실행 단위,
  요청별 응답 순으로 처리한다. 배치 크기는 요청 수다.
- 입력 검사, 큐 대기, 전처리, 추론(생성형은 첫 토큰 시점 포함), 후처리, 응답 반환의 구간 시간을
  기록한다.
- `Preprocessor`는 원시 입력과 원시 출력 계약을 정의하고, `artifacts[]`에 모델 산출물별 전처리
  코드 파일을 둔다. 각 항목은 `target: {model, artifact}`를 가진다. 후처리도 같은 코드가 맡는다.
- `Dataset`은 CHILL이 보유하는 원시 샘플 모음이다. 워크로드는 전처리를 참조하고 데이터셋을
  선택적으로 참조한다. 두 객체는 등록, 조회, 수정, 삭제를 제공하고, 참조 중인 객체의 삭제는 거부한다.
- 프로파일 조건은 모델 산출물, 런타임, 전처리 산출물, 데이터셋이다. 네 조건이 같은 워크로드끼리
  인스턴스와 프로파일을 공유한다.
- 프로파일링은 실행마다 데이터셋의 프로파일링용 부분집합에서 요청 수만큼 새로 뽑아 전처리부터
  후처리까지 측정한다. 평가 트래픽용 부분집합과 분리한다. 난수 텐서 입력은 삭제한다.
- 전처리 코드는 요청별 단위 수(텐서 첫 축 기여량)를 돌려주고 CHILL은 이를 기록한다. 단위 수는
  관측값이며 제어 입력으로 쓰지 않는다.
- 생성은 greedy와 정적 배칭이다. 최대 생성 토큰 수는 측정 조건으로 Run에 고정한다.
- 실행 경로는 여러 모델 파일을 사용할 수 있다.
- mobilenet도 같은 변경에서 원시 JPEG 입력과 전처리로 전환한다.
- 채택하지 않은 안: 단위 수 합을 배치 축으로 쓰는 방식, 선언형 단위 규칙, 텐서별 축 역할 표시,
  단위 비용 제어 모델, continuous batching, 비디오 입력, 입구에서의 전체 디코딩, 범용 파이프라인 엔진.

## 병합 시 주의

`docs/spec/*.md`는 README와 `03_routing.md`를 제외하고 git이 추적하지 않는 로컬 문서다.
worktree에는 main 작업 디렉토리의 사본을 두고 편집한다. 병합할 때 main 사본이 아래 기준값에서
바뀌었는지 확인한 뒤 수동으로 합친다.

|파일|복사 시점 sha256|
|---|---|
|`00_measurement.md`|`5218fc548666d3c325f3b9b15bf11dc4ec5c5e73835dee3909d5f17c8567c9a8`|
|`04_configuration_selection.md`|`54d394819d44be09bfcdddd76eaf578330167fc3fb5e9fbe2cda232cde3e9000`|
|`05_transition_control.md`|`8113e19fd14374ef5bd19e86e4bf546e3b2a1c0c44c2fa1130f075adf5bc334f`|

## 현재 상태

- 1단계 spec, 2단계 SRS, 3단계 구현 설계와 ADR 0021을 독립 검수 후 커밋했다.
- worktree의 `docs/spec/00_measurement.md`에 측정 조건, 표본 추출, 원시 trial 보존을 반영했다.
  이 파일은 추적되지 않으므로 병합 시 main 사본에 수동으로 옮긴다.
- 4단계 구현은 [구현 설계](../design/serving/heterogeneous-workloads.md) 9장의 단위 1-8과 7.1절의
  DeviceClass별 후보로 이 브랜치에 들어왔다. 검증 기록과 미검증 범위는 구현 설계 9.1절에 있다.
- 병합 시 절차:
  1. worktree의 `docs/spec/00_measurement.md`를 main 사본에 옮긴다.
  2. 구현 설계 8장의 정리 대상(ModelSpec, Workload, 측정 객체, Evidence Store 객체, serving ObservationLog)을 삭제한다.
  3. CHILL 시스템 image(operator, serving, profiler, measurement worker, node actuator, dashboard)를 이 브랜치로
     빌드하고 push한 뒤 배포한다. carrier, firstfloor runtime 변형, observer image와 `executionDeclarations`는
     이미 push하고 고정했다(`~/.cache/chill-assets/ops/pushed-images.md`).
  4. ModelSpec, Preprocessor, Dataset, Workload를 다시 등록한다.
  5. 다시 측정하고 scheduler 설정의 campaign과 profile을 다시 선택한다.
  6. live cluster의 serving 경로와 혼합 부하 평가를 검증한다.

## 호스트와 클러스터 변경 기록

이 작업이 레포 밖에 남긴 변경이다. 상세 기록과 rollback은 `~/.cache/chill-assets/ops/<항목>/`에 있다.

|대상|변경|이유|rollback|
|---|---|---|---|
|`orin-nano`, `jetsono` 호스트|`/etc/systemd/system/nvidia-cdi-refresh.service.d/10-after-nvpmodel.conf`에 `[Unit] After=nvpmodel.service`. CHILL node-actuator가 관리한다(init container, `nodeActuator.jetsonNVPModel.ensureBootOrdering`, ADR 0015)|`nvidia-cdi-refresh`의 `nvidia-smi`가 GPU golden context를 먼저 만들어 부팅 시 `nvpmodel`이 모드를 적용하지 못했다. 모드 이름만 바뀌고 클럭과 게이팅은 그대로였다|`ensureBootOrdering: false`로 바꾼 뒤 drop-in 디렉터리 삭제, `daemon-reload`, 재부팅 (`orin-nano-nvpmodel/ROLLBACK.txt`)|
|`monitoring/edge-metrics-exporter-jetson` DaemonSet|image `edge-metrics-exporter:sha-63abb94-thermal-eagain`, updateStrategy `OnDelete`. `orin-nano` 파드만 교체|L4T R36.4의 열 센서 EAGAIN이 Python 3.12에서 `TypeError`로 올라와 열 지표 수집이 멈췄다. 영구 수정은 edge-metrics 저장소에 `thermal-eagain.patch` 반영과 Helm 값 갱신이 필요하다|`orin-nano-thermal/` 의 patch 명령|
|`kube-system/nvidia-device-plugin-jetson` DaemonSet|env `DEVICE_ID_STRATEGY=index`, `DEVICE_LIST_STRATEGY=envvar` (jetsono, jetsonx, jetson-nano)|CUDA Pod가 CDI `unsupported device id: tegra`로 생성되지 못했다|`jetson-device-plugin/`|
|`chill-system/chill-node-actuator-config`|전력모드 기록을 위해 임시 정책을 두 번 열었다(08:16~08:45Z, 11:07~11:18Z). 원본 바이트로 복원하고 DaemonSet 원래 revision 복귀를 확인했다|actuator 허용 목록에 없는 모드는 적용되지 않는다|복원 완료|
|`hetero-*` NodeActuation 16개|기록용으로 남겼다(Ready, 더 이상 동작 없음)|전력모드 근거|`power-modes/SUMMARY.md`, `orin-nano-nvpmodel/SUMMARY.md`의 삭제 명령|
|registry `155.230.35.213:5000`|`hetero-` 태그 image와 carrier만 새로 push했다. 기존 태그는 그대로다|실기기 검증과 sample digest 고정|`pushed-images.md`|
|namespace `chill-ops-check`|테스트 Pod를 만들고 모두 지웠다|실기기 runner smoke|비어 있음|

- jetsonx는 08:00Z 무렵 SmolVLM fp32 CUDA 적재 중 unified memory 고갈로 멈췄고 사용자가 재시작했다.
- 캠페인의 `mobilenet-v2-050-orin-nano-cuda-q1/q2` 측정은 호스트 수정 전 "25W"로 기록되었으므로 실제 25W 상태가 아니다.
- `jetsono`에도 `nvidia-cdi-refresh`가 켜져 있다. 지금은 `nvidia-smi`가 없어 건너뛰고, node-actuator가 같은 drop-in을
  써 두었으므로 설치되어도 `nvpmodel.service` 다음에 실행된다.

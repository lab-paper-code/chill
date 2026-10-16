# CHILL 캠페인 과거 기록 — 측정 근거 채택·원자 제어·GPU와 전력 왕복

[현재 목표·판정·다음 의존성](2026-10-13-scheduler-live-evidence-campaign.md) · [제어 기록 주제별 목록](2026-10-13-scheduler-live-evidence-campaign-04-control.md)

2026-10-14~16의 실행 이력이다. 아래 현재·미완료·보류는 각 실험 당시 상태이며 현재 작업 지시가 아니다. 실패 원본과 후속 복구를 구분한다. 상대 evidence 경로는 명시된 직전 prefix/루트를 따른다.

### 연속 실행 — 현행 evidence 연결과 원자 동작 실측

- 성공한 5노드의 q1/q2가 모두 Complete다. 각 조건의 3 loaded-idle·3 K1·3 K2,
  합계 90 trial을 production Catalogue로 읽었다. 10 profile, insufficiencies=[].
  memoryBytes는 각 batch의 실측값을 사용했다.
- 기존 owner journal은 9,700 window 중 닫힌 미게시 2,027개였다. 기존 데이터는
  남겨 두고 이번 campaign을 `/data/native-full-campaign-20261013`으로 분리했다.
  기존 설정으로 readiness는 실제 200/5.46ms였다. 범용 retention/recovery 구현 없음.
- 실제 Node UID·전력계·논리 예약 CPU·10개 preparation을 owner에 연결했다.
  CPU 예약은 다른 Pod의 실제 요청량을 고려한 논리 예산이며 OS 전용 할당의 증거가 아니다.
  genesis3는 다른 GPU 소유권이 확인되지 않아 ready=false다. 우회하지 않았다.
- 정확히 같은 20초 구간의 실제 10노드 power API는 complete=true,
  whole=4950.248292767765J·247.51241463838824W, active=0이었다.
  당시 실제 활성 serving membership이 없었다. source timestamp 없는 전력계는
  acquisition timestamp라는 사실을 유지했다. 이것만으로 SLO/FULL을 입증하지 않는다.
- 5노드의 CHILL-unloaded idle은 기존 다른 작업을 유지한 채 production operation
  command로 Accepted 및 immutable receipt를 얻었다. meanW: genesis1 104.02484527841905,
  genesis2 78.64609652097815, genesis3 50.58388752166097,
  lattepanda 9.209640656611796, rasp5 4.846040886636294.
  bare-board 전력이나 반복 실험 평균으로 해석하지 않는다.
- 세 Jetson의 실제 power operation은 다른 baseline-fleet/fastdds Pod 때문에
  WorkloadSafetyBlocked였다. 사용자는 작업 소유자 조율 후 측정을 선택했다.
  허용 namespace 확대·다른 작업 삭제 없이 해당 측정은 blocked로 유지한다.
- rasp5 q1 prepare의 첫 실제 실행은 60초 후 partial/Failed였다. raw evidence와
  90,412-byte immutable 실패 객체를 보존했다. 실행 Pod 자체는 Ready·Guaranteed였고
  main 및 두 init 모두 CPU request=limit=1·memory=279Mi였다.
  Pod Ready를 native 준비 완료나 Accepted 전환 비용으로 대신 기록하지 않았다.
- 실행 호스트에서 Pod IP와 일반 ClusterIP native HTTP 요청은 모두 timeout됐다.
  기존 설계의 EdgeMesh proxy label과 named http port를 가진 단일 endpoint Service는
  실제 readiness 200/2.65ms 및 native state ready=true를 반환했다.
  actual profile·ORT·affinity=[3]·threads=1·initialized batches=[1,2]를 확인했다.
  기존 SDK의 direct Pod IP를 물리 인스턴스별 단일 endpoint Service로 교정했다. 외부 EdgeMesh 설정은 변경하지 않았다.
- 관련 판정은 `docs/spec/00_measurement.md`의 실행 조건·누락 오류와
  `docs/spec/05_transition_control.md`의 실제 추론 준비·부분 실패 원칙에 따른다.
- 근거 prefix `/tmp/chill-native-campaign`: `current-production-catalogue.json`,
  `operating-ten-node-power-window.json`, `*-unloaded-idle-*-stdout.json`,
  `rpi-prepare-q1-1-stdout.json`, `rpi-prepared-pod-evidence.json`,
  `rpi-standard-service-network-probe.json`, `rpi-edgemesh-service-probe.json`,
  `rpi-edgemesh-native-state.json`.
- production loader가 accepted immutable idle 객체 5개를 실제로 읽어 fixed-mode
  baseline 5개를 반환했다. costReferences에는 이 5개만 연결했고 실패 prepare는 제외했다.
  동시에 10개 실제 bundle identity를 읽었다. 일시적인 Go smoke source는 제거했다.
- 실제 Dashboard의 해당20초 조회는 당시 owner stall로 504/DashboardRelayTimeout이었다.
  화면은 timeout을 표시했으며 이전 성공 숫자를 대입하지 않았다. screenshot:
  `/tmp/chill-dashboard-power-exact-interval-timeout-20261014.png`.
  기존 dashboard-validation Workload 2개는 원래 모델을 참조하는 타 작업이며 그대로 유지한다.
- membership CLI의 `pool=""` 요청은 수행 전 거절됐다. measurement POOL↔detached만 허용하고 동일 상태 no-op는 비용 evidence에서 제외했다. 일반 제어 idempotency는 유지했다.

### 연속 실행 — 실제 원자 제어 완료와 공통 경로 수정

- rasp5 cold q1 prepare는 실제 7.067661287초·38.086800664500004J/Accepted다.
  기존 실패 Pod를 transport에 연결한 repair는 operation complete였지만 prior native
  관측이 없으므로 measurement Failed로 보존했다. cold 비용으로 넣지 않았다.
- 실제 q1 remove에서 SDK zero outstanding·container 종료·Pod/UID Service/config 제거를
  확인했다. 두 Accepted remove 원본과 loaded idle mean 4.841479841412628W를 보존했다.
- genesis2 fresh q1/q2 prepare, loaded idle, remove와 실제 label 탈퇴·편입이 모두
  Accepted다. q1=[3]/1thread/280Mi, q2=[2,3]/2threads/285Mi 및 Guaranteed,
  실제 UID-owned Service/native readiness를 확인했다. 최종 다른 작업을 유지한 채
  원래 measurement-native-a이며 자기 native 객체는 없다.
- Latte q1/q2 prepare·loaded idle·remove와 join은 configured coverage에 따라 Accepted다.
  q1 remove와 q2 prepare의 각 1개 source timeout은 raw에 남았다. 명세·선택 정책의
  coverage 통과 위에 zero-failure 요구를 새 validation gate로 추가하지 않는다.
  50ms/40ms sampler의 detach는 실제 gap 초과/Insufficient였다. 성공 비용에서 제외한다.
- 실제 API의 279Mi와 생성한 292552704-byte Quantity는 값이 같지만 reflect 비교는 false였다.
  공통 준비 검증을 stdlib maps.EqualFunc(resource.Quantity.Equal)로 교정했다.
- genesis2 첫 생성은 CNI Pod status/resourceVersion 갱신 때문에 metadata-only patch가
  conflict였다. UID·현재 label만 atomic JSON Patch test 후 UID label을 추가하도록 수정했다.
  다른 label/Pod 교체는 거부하고 status 갱신에 대한 범용 retry를 만들지 않았다.
  동일 source에서 실패 전/수정 후 fresh cold prepare 완료를 확인했다.
- `f96a38f`는 native 원자 제어 9개 파일과 해당 RBAC만 포함했다. focused tests·lint·arch/gopls/default Helm gate는 통과했다. live Profiler render 74개(valid62/invalid0/errors0/skipped12)의 uninstall hook 4개는 helm get manifest에 없었으며 새 리소스 증가는 아니었다.
- make test는 untouched `TestObserverAdvancesAbsenceAndFreezesFirstPresence`의
  5ms timing/strict ordering assertion에서 실패했다. marker absent와 다른 marker의 first
  presence가 같은 receipt였다. 전체 pass로 적거나 실패 확인을 위해 다시 실행하지 않았다.
- owner DNS S3 read 0.319초·production immutable Put Existing/0.00307초만으로 publication timeout의 네트워크 원인을 확정하지 않았다. read-only PVC 조회는 closed/unpublished=0·history201MB였다. 검사 Pod·임시 소스는 제거했다.
- 근거: `genesis2-atomic-summary.json`, `lattepanda-atomic-summary.json`,
  `lattepanda-policy-accepted-operation-summary.json`, `rpi-prepare-q1-2-stdout.json`,
  `rpi-loaded-idle-q1-1-stdout.json`, `rpi-remove-q1-*-stdout.json`,
  `owner-dns-immutable-read-probe.json`, `current-journal-metadata-probe.json`.

### 이전 진입 요약에서 옮긴 고유 이력 — 2026-10-14

- 첫 실제 추론은 완료됐지만 owner 종단 지연 `1.298374879s`는 frozen SLO `1s`를
  초과했다. **첫 표본 SLO 위반**이며 통과가 아니다. ingress validation/accepted
  elapsed `915.432437ms`, 실행 엔진 elapsed `130.358594ms`, 외부 client 왕복
  `1.3555289909709245s`는 서로 다른 범위다. 근거:
  `/tmp/chill-native-campaign-first-inference-owner-observations.json`.
- 대형 journal 전체 scan/shared lock은 연구 규모 한계였다. 별도 제어 시간창·기본 1분 archive window는 bounded workaround이며 범용 성능 해결이나 S3 네트워크 원인 확정이 아니었다. 당시 3,504 window·201,299,805 JSONL bytes, closed/unpublished=0이었다.
- 당시 firstfloor(실행 엔진)와 모델명의 혼동 정정은 미완료였다. 원래 `mobilenet-v2-050` UID `b8bc6efa..`는 타 dashboard-validation Workload2개, `mobilenet-v2-050-firstfloor` UID `545ef2c0-b931-4c96-b978-b4cef1195210`는 tight/relaxed가 참조했다. 과거 evidence의 UID는 유지했다. 이후 모델 책임 분리는 아래 2026-10-15 기록에 있다.
- owner CPU 예산 수정은 실제 snapshot smoke에서 5개 노드 Ready를 확인했다.
- orangepi memory peak 부재와 Orin Nano actuator readiness 문제도 성공으로 대체하지 않는다.

Orin 경로 준비와 저장 실패·복구의 당시 기록:

당시 사용자 검토 후 재개하면서 genesis1/2/3 측정·제어를 후순위로 보류했다. 진행 중이던 genesis2 자기 캠페인은 8 trial을 보존하고 종료했으며 완료로 세지 않았다.
Orin Nano의 최초 GPU 컨테이너 생성은 Toolkit 1.20 CSV/CDI가 플러그인의 `tegra` ID를
거부하여 실패했다. 사용자가 제한된 수정에 동의한 뒤, NVIDIA가 지원하는 index 방식으로
**Orin의 플러그인만** `nvidia-device-plugin-orin-nano`에 분리했다. 공유
`nvidia-device-plugin-jetson`은 Orin을 제외하고 `OnDelete`로 두어 다른 3노드 Pod의
자동 롤링을 막았다. 당시 공유 플러그인의 자동 롤링은 꺼진 상태였으며 호스트 패키지·containerd·전력은 변경하지 않았다.
실제 GPU request 1의 컨테이너 생성과 CUDA Driver API 초기화가 성공했고,
`NVIDIA_VISIBLE_DEVICES=0`, GPU UUID `GPU-31ec0c91-d383-5ca3-8772-28981cf737aa`,
Driver API `12060`을 확인했다. 다른 3노드 플러그인과 기존 Orin baseline Pod의
UID·container ID 보존은 `/tmp/chill-edge-orin-plugin-cutover-proof.json`에 기록했다.
자기 테스트 Pod는 삭제했다. GPU 식별/할당만으로 프로파일 완료를 세지 않는다.
Xavier NX도 실제 UUID/명시적 선택은 확인했으나 CPython3.8용 호환 GPU 컨테이너가
`ImagePullBackOff`여서 추론하지 못했다. 불필요하게 이 경로를 확장하지 않는다.
Orin의 같은 모델은 ONNX GPU 1.23/NumPy1.26.4에서 CUDA 노드만 실행하고 CPU fallback을
차단한 실제 추론을 통과했다. 공유 runner의 SMI 조회는 실제 실패(exit6)했고, CUDA Driver
API로 교체한 `1c59650`은 비-root UID65532에서 초기화와 배치1/2 binary 추론을 통과했다.
잘못 지정한 다른 노드의 UUID는 오류로 거부했다. 증거:
`/tmp/chill-edge-orin-sdk-actual-binary-inference.json`,
`/tmp/chill-edge-orin-sdk-wrong-physical-gpu-rejection.json`.
Orin에 설치된 CUDA12.6/cuDNN9.20의 실제 runtime library를 이미지에 고정했다.
최종 image `4b316e0b..`는 수동 host mount/pip 없이 UID65532에서 같은 SDK 초기화와
배치1/2 실제 추론을 통과했다. 증거는
`/tmp/chill-edge-orin-vendor-complete-image-actual-sdk-inference.json`이다.
범용 ARM64 CUDA image `3dcc21dd..`의 실제 CUBLAS 실패는 보존하고 채택하지 않았다.
host 호환 계층 없이 기존 실행 선언·정책 API로 등록했다. 최초 캠페인 UID `1fe633d1..`은
Orin에 없는 `fixed` 전력모드로 계획 해석에 실패해 측정을 시작하지 못했다. 자기 객체를
기록 후 교체한 `mobilenet-v2-050-orin-nano-cuda-q1`(UID
`94446d2b-e24d-4c38-a4c1-63654a70485f`)은 당시25W read-back과60초 settling을
확인했지만, loaded-idle 두 시도가 `memory.peak` 부재와 workload-observer의 API
접속 timeout으로 실패했다. 실제 전력모드 변경·성공 프로파일·채택은 없었다. 근거:
`/tmp/chill-edge-orin-current-mode-campaign-child-failure.json`.
계획 해석 실패는 이제 실제 resolver 오류와 `Failed/CurvePlanResolutionFailed`로
공개되며 오류 반환을 유지해 재시도할 수 있다. 로컬 API 서버의 실패→복구 회귀는
통과했고, 실제 캠페인 `orin-gpu-plan-resolution-check`는 같은 오류 상태와 자식 0개를
확인했다(`/tmp/chill-campaign-visible-plan-failure-live-proof.json`). Dashboard result
API는 결과 참조가 없다는 이유로 404를 반환했으나, 현재는 실제 캠페인 상태와 조건을
200으로 제공하도록 수정했고, 아직 없는 storedResult/result는 null로 구분했다. 실제 Dashboard에서
인증 후 `Failed`, 완료 0건, 실제 power-mode 해석 오류, 완료 결과 없음을 확인했다.
API 증거: `/tmp/chill-campaign-visible-plan-failure-dashboard-api-proof.json`;
화면: `/tmp/omp-sshots-15a6197993ee87ba.webp`. console image `73bace12..`,
Dashboard image `4e0c0d7b..`를 실제 rollout했다. 이는 실패 공개 경로의 검증이며
GPU 측정 성공이나 FULL 제어 검증은 아니었다. focused operatorconsole tests·Dashboard typecheck/lint·두 image build는 통과했다.
profiler ConfigMap의 image를 `7771c693..`으로 바꿔 실제 rollout했다. Deployment만
바꾼 첫 시도는 operator가 선언대로 되돌렸으며, Helm values에는 아직 반영되지 않았다.
첫 `make lint`는 통과했지만 `make test`는 timeout 응답 테스트가 기대한 504 대신
408/RequestCanceled를 받은 것으로 실패했다(`artifact://898`). 비어 있거나 이미 소비한
request body의 transport read deadline이 작업 timeout과 경합하는 원인을 수정했다.
실제 TCP GET/POST 회귀와 통합 후 `make lint && make test`는 통과했다(`artifact://950`).
Claude 에이전트의 고정 계획 수정·독립 리뷰·owner/Node UID 보완을 통합했다.
실제 profiler `4663d8d4..`로 rollout 후, 설정 변경 때문에 Failed가 됐던 기존 GPU
캠페인 두 개는 각각 기존 9 trial·stored result를 유지하며 Complete로 복구됐다.
새 Genesis 프로파일링이 아니라 저장된 근거의 상태 복구다. 근거:
`/tmp/chill-stored-plan-controller-before-fix.json`,
`/tmp/chill-stored-plan-controller-after-fix.json`.
Orin image `99f5ddfa..`는 실제 GPU 할당·UUID·설치 package 조회와 cgroup memory.current
관측을 통과했다. 실제 4개의 시각별 sample과 관측 최대값을 보존했고 테스트 Pod는 삭제했다.
근거: `/tmp/chill-orin-window-memory-actual-image-inspection.json`.
이는 kernel high-water가 아닌 측정 창의 sampled maximum이며 짧은 spike를 놓칠 수 있다.
캠페인 UID `22cbf028..`은 GPU loaded-idle 10초·전력 12/12·workload 연속 관측에
성공한 뒤 S3 commit에서 실패했다. 실제 probe는 Store DNS 해석 실패, 기존 Service
ClusterIP `10.97.177.19:9000` HTTP403 도달, 직접 Pod IP timeout을 확인했다
(`/tmp/chill-orin-evidence-actual-route-probe.json`). 에이전트의 공통 binding 수정은
새 노출·fallback 없이 기존 Service API의 할당 IP를 사용한다. profiler/console에는
자기 namespace의 그 Service 하나에 get만 허용했고 실제 can-i를 확인했다.
profiler `1b190380..`, console `905240dd..`를 rollout하고 동일25W 조건의 자기
캠페인 UID `9946bf74-4bf1-4c6e-b5f6-ab8f975633f4`를 새로 실행했다.

### Orin GPU 근거 채택과 journal 공간 소진 해소 — 2026-10-14

- CPU1/2 캠페인은 각각 정식 9 trial을 완료했다. 실제 결과·순위:
  `/tmp/chill-orin-q2-completed-dashboard-result.json`,
  `/tmp/chill-orin-measured-gpu-allocation-ranking.json`. CPU2의 증분 에너지는
  낮았지만 처리량·지연도 달랐다. 이 순위를 SLO·전체 POOL 총에너지 판정으로 대체하지 않는다.
- PVC 실제 가용 공간 0 bytes를 확인하고 기존 StorageClass의 확장으로 1Gi→8Gi를
  적용했다. 이전 raw directory는 보존했다. journal은 metadata와 stream header를
  private staging에 만든 뒤 한 번의 rename으로 공개한다. 실패는 드러내고 이미
  완료한 window의 완료 시각을 다시 쓰지 않는다. 호환 reader·recovery machine·test hook 없음.
- 완료 시각 덮어쓰기 회귀·focused serving tests·lint는 통과했다. 마지막 `make test`는
  `TestExecutePublishesConfiguredCompositeMetricObservation`와
  `TestObserverAdvancesAbsenceAndFreezesFirstPresence`에서 실패했다(`artifact://1029`).
  이를 전체 gate 성공으로 보고하지 않는다.
- owner `5d38d3ce..` rollout과 동일 최신 journal directory 재시작 후
  readyz/scheduler HTTP200·제어 모두 OFF를 확인했다. 자기 이름 제한 Role의 get 목록에
  두 새 캠페인만 추가했고 실제 can-i는 yes다. 근거:
  `/tmp/chill-orin-gpu-owner-scoped-profile-read-proof.json`,
  `/tmp/chill-atomic-journal-retained-restart-proof.json`.
- 실제 owner가 생성한 GPU bundle은 CPU1 3009 bytes, CPU2 2523 bytes이며 해당
  profileID·batch1/2 duration samples·실측 energy를 확인했다:
  `/tmp/chill-orin-gpu-owner-materialized-bundles.json`. 최신 owner의 근거 채택 증거다.
  당시 실제 prepare/remove·Serving envelope·FULL Dashboard 연결은 미완료였다.

### 원자 비용 연결·불변 정의 재적용과 최신 SDK 준비 — 2026-10-15

- Orin membership join과 CPU1/2 prepare/remove의 실측 Accepted receipt 5개를
  실제 owner에 연결했다. 채택 비용은 총 31개이며 Orin ready·CPU inventory·GPU UUID를
  확인했다: `/tmp/chill-orin-gpu-cost-bound-owner-api.json`.
  첫 CPU2 prepare는 meter boundary 공백으로 Insufficient였다. 판정을 완화하지 않고
  실제 제거 후 새 실행을 측정했다. 전력모드 방향 전환의 증거는 아니다.
- 새 journal directory에 없던 이번 세션의 두 Workload 불변 정의는 이전 완료 window
  3개의 원시 파일을 그대로 복사하여 확보했다. 기존 Model의 UID/generation을 확인한
  뒤 현재 resourceVersion의 If-Match를 사용해 기존 Workload PUT API로 명시적 재적용했다.
  두 Workload는 실제 `Ready / admission=true`다. 다른 세션의 두 Workload는 변경하지 않았다.
  `/tmp/chill-definition-transplant/after-explicit-reopen-workloads.json`.
  임시 scan/copy Pod·ConfigMap은 제거했고 원본 window는 보존했다.
- 기존 incidental/mock-echo 검증 제거 뒤 lint/test(`artifact://1101`)·Dashboard typecheck/lint는 통과했다. 최신 CPU·Orin SDK/owner image는 빌드됐지만 새 overhead 계약의 실제 배포·Serving smoke는 미검증이었다. 기존 프로파일 image를 덮어쓰지 않고 같은 조건을 재측정했으며 Genesis 신규 측정·외부 workload·power allowlist는 변경하지 않았다.

### 최신 완료 관측 계약·모델 책임 분리 — 2026-10-15

- 리뷰에서 반환 중 요청의 교환 잔여 지연 중복 계산과 현재 인스턴스/UID에 묶인
  측정 재사용의 순환 의존을 확인하고 에이전트가 수정했다. 실제 관측한 교환 잔여와
  이미 경과한 응답 시간만 차감하며, 같은 실행 조건의 불변 정의에 근거를 재사용한다.
  중복 UID map·하류 재검증 제거 뒤 전체 gate(`artifact://1210`)와 focused serving/firstfloor tests가 통과했다.
- `mobilenet-v2-050-image-classifier` 하나에 기존 CPU/CUDA·amd64/arm64 실행 경로를
  등록했다(동일 UID, generation2). 모델명에 실행기를 섞지 않고 기존 artifact·입력을
  그대로 사용했다. `/tmp/chill-completion-model-assembly/model-api-applied.json`.
- 이번 세션의 두 기존 Workload는 정상 관리 API로 수용 중지·삭제를 완료했고,
  해당 Rpi SDK 컨테이너는 실제 exit0·종료 시각을 확인했다. 다른 Workload는 보존했다.
  Pod 객체의 source-termination finalizer는 남아 있으며 강제로 제거하지 않았다.
- 새 CPU 경로의 execution policy 누락으로 두 새 캠페인이 Failed였다. 원시 상태·
  owned Run 원인을 보존하고 기존 정책의 제한값을 그대로 재사용하는 명시적 selector를
  추가한 뒤 실패한 이번 세션 객체만 교체했다. retry/timeout/판정 기준을 완화하지 않았다.
- 최신 strict cost loader로 원본 참조를 직접 읽었다. 호환 reader 없이 unloaded idle
  5개·image-independent membership 10개가 통과했고, 이미지 종속 구계약 29개는
  unknown-field 오류로 거부됐다. `/tmp/chill-completion-cpu-cost-assembly/retained-independent-sources.jsonl`.
  이는 Genesis 신규 profiling/control이 아니다. 임시 Go helper는 실행 후 삭제했다.
- Orin read-only 조회는 mode25W/id1이지만 실제 CPU/GPU cap·mask와 설정값이
  달랐다. 모드명만으로 물리적 적용 완료를 주장하지 않고 새 GPU 측정은 보류했다.
  `/tmp/chill-completion-profile-assembly/orin-current-power-verbose.json`.
- Dashboard 현재·보관 상세에 완료 관측 여부와 반환 잔여 미확보/실제0을 구분해 배포했다. 당시 실제 상세 시각 검증은 미실행이었다.
- 기존 power-only 원자 동작 API로 Orin의 현재 상태 unloaded wall idle을 측정했다.
  실제 Accepted·complete, 평균 5.6468W와 13539-byte receipt를 보존했다:
  `/tmp/chill-completion-cpu-cost-assembly/orin-current-unloaded-idle-result.json`.
  전력 쓰기·GPU 프로파일링이 아니며 25W 설정의 실제 cap 일치 증거로 사용하지 않는다.
  측정 입력은 기존 power-series 계약으로 만들었고 API 확장은 남기지 않았다.

### 최신 CPU 프로파일 연결·실제 준비 차단점 — 2026-10-15

- `d8d444c`에서 각 원시 trial을 불변 실행 조건에 직접 검증하도록 고쳤다.
  외부 Pod의 출입 기록은 보존하되 환경 digest 일치로 과학적 수용을 결정하지 않는다.
  기존 두 캠페인의 성공한 Run을 재측정하지 않고 같은 UID로 최종 Complete를 확인했다.
- 최신 CPU SDK의 LattePanda·Rpi, CPU1/2 캠페인 4개가 총 36 trial로 완료됐다.
  실제 owner bundle 4개와 unloaded idle 6개·membership 10개를 owner API에서 확인했다.
  전체 제어 스위치는 OFF이며, 완료 overhead 미측정은 0ms 관측으로 간주하지 않는다.
- 정상 원자 prepare 두 건은 5분 후 partial/Failed였다. Pod Running/Ready만으로 완료를
  주장하지 않는다. 실제 SDK loopback metadata는 200이며 owner는 아직 두 인스턴스를
  `actual native/profile snapshot unavailable`로 제외했다. Service 경로·내부 모델 식별자 바인딩이 차단점이었으며 timeout·검증은 완화하지 않았다. lint/test는 통과했다(`artifact://1340`); 당시 Serving/FULL/Dashboard 종단은 미완료였다.

### 최신 owner 참조·Serving bootstrap·resize 결과 — 2026-10-15

- 최신 owner `a97d9764..`의 실제 scheduler GET은 HTTP 200이며 28개 실측 참조를 채택했다:
  idle baseline 10개(미적재 6·적재 4), 비용 18개(membership 10·prepare 4·remove 4),
  거부 이유 없음. 전체 제어 9개는 OFF:
  `/tmp/chill-completion-cpu-cost-assembly/owner-real-cpu-costs-proof.json`.
- 최신 CPU SDK의 producer가 내부 프로토콜 식별자 `chill_model`을 business ModelSpec으로
  잘못 사용해 실제 profile 404를 냈다. producer root fix가 business ModelSpec을 분리해
  이를 해결했다. 호환 alias는 추가하지 않았다.
- 두 fresh normal CPU1 Serving bootstrap prepare는 실제 Accepted/complete·Ready이고
  두 instance가 live다. 이는 prepare 완료일 뿐이며 Serving completion/SLO 증거는 아니다.
- Ponytail-reviewed resize fix `33a17b1`은 3개 source file에 한정됐다. fresh cold
  CPU2→CPU1 resize 두 건 모두 실제 Accepted/complete, retained `DrainNative`와 zero
  SDK counters를 확인했다. focused Go·live proof 및 parent가 수행한 전체 gate는 통과했다.
- 당시 Genesis 신규 profiling/control과 Orin 전력 작업은 보류였다. mode25W/id1과 물리 cap 불일치·baseline-fleet/fastdds 외부 작업을 유지했고 최신 SDK로 구 GPU raw를 채택하지 않았다. Serving 완료 관측·OFF 비교·개별/FULL/Dashboard 종단은 이 시점 미완료였다.

## 합법적 자원 전환과 손 검증 — 2026-10-15

- `d8a1d30`: 겹치는 CPU/GPU 할당은 stop/drain/remove 후 prepare/route, 나머지는 prepare-first다.
  global route 직렬화와 진행 목표 보존을 포함한다. 리뷰에서 찾은 partial-failure nil panic은
  수정 전 독립 snapshot에서 재현하고 수정 후 회귀로 확인했다(`artifact://2145`).
- owner 이미지 `f1d0b1f4..` 배포 후 CPU1 실제 추론 4건이 완료됐다. 실제 resources-only 후보에서
  합법적 DAG를 확인했지만 최신 판단은 `ExplicitRequestLifetimeCapacityExceeded`로 변경을
  탈락시키고 Keep를 선택했다. 모델·SLO·비교 기간을 완화하지 않았고 이후 controls 모두 OFF다.
- 손 검증은 기존 API로 이번 캠페인의 두 Workload만 정상 close/drain한 뒤 CPU1 source를
  원자 remove했다. 실제 이전 Pod가 없어진 후에만 기존 CPU2 profile로 prepare했다.
  두 원자 동작은 Accepted/complete였다. 다른 dashboard-validation Workload는 보존했다.
- 같은 spec으로 두 Workload를 복원해 CPU2 `[2,3]`·threads2·Pod CPU request/limit2와
  실제 routing을 확인했다. 두 UID에서 총 4건이 HTTP200·executionComplete였고 q2 profile을 사용했다.
  이는 수동 원자 동작 조합의 실적이며 자동 선택·무중단 resize의 증거가 아니다.
- 이 실험의 물리 source UID는 `86336df7-573b-4728-9d70-6bbe9d5128c7`였다.
  Workload UID는 relaxed `0718c1f3-ca2d-4912-a446-20a603354609`,
  tight `9ec18029-00e2-4987-a323-b5af36b46e10`으로 바뀌었으며 모델 UID·generation2는 유지했다.
- 증거는 `/tmp/chill-completion-cpu-cost-assembly/resources-hand-*` 및
  `legal-resource-planner-fresh-loaded-state.json`에 있다.
- cross-window reader 배포 후 같은 과거 구간 이력은 200·3.382초·776022 bytes였다.
  전력 조회는 여전히 15초 timeout이다(`cross-window-buffer-live-smoke.json`).

## 최신 CUDA 근거·승인된 Jetson 왕복·Dashboard 부분 e2e — 2026-10-15

- 구 CUDA prepare는 `completionOverheadMS` 미지원으로 실제 실패했고 생성 자원은 정리했다.
  이후 CUDA ABI12·ORT1.26·현재 Go SDK의 runnable child image
  `sha256:52d81b5028d22e2c9c7f1ef4c3bd1ab2d4b41bb5d6421d69938b5c02d1100136`으로
  새 BEO `97a02e77-e075-47ca-9ed5-e0327413007d`의 실제 6 trial을 측정했다.
  현재 profiler 의미 gate와 scoped Service GET RBAC를 맞춘 뒤 **같은 6 Run**을 Complete로 봉인했다.
  재실행·구 raw 덮어쓰기가 아니며 새 profile은 configRV `353471711`에서 Persisted·Loaded다.
- 구 profiler는 세 번째 idle의 실제 foreign baseline-fleet 종료로 달라진 EnvironmentIdentity를
  equality gate로 거절했다. [측정 명세](../spec/00_measurement.md)에 따라
  공존·간섭 관측을 보존한다. 실제 차이를 삭제하거나 격리·동일 배경·인과 에너지 증거로 바꾸지 않는다.
  batch1만 측정했고 fresh completion calibration·방향별 비용·자동 GPU 적격성은 입증하지 않았다.
- owner는 CPU1/512Mi에서 `16:14:52Z` OOMKilled 후 connection refused를 냈다. 원인은 **미입증**이다.
  `16:22Z` 자연 복구·ready를 확인했고 당시 60초 incomplete observation window는 일시적이었다.
  영구 calibration 장애로 해석하지 않는다. owner `6ad78…`·CPU1/512Mi·store8Gi,
  ModelSpec·CPU business route는 유지했다.
- CLI 기본 limit10000의 실제64MiB 초과는 `7ca66ddd`의 limit50·기존 cursor로 수정했다(`artifact://3334`). 기존 bounded reader를 유지했다. 당시 GPU prepare는 fresh 완료 증거 전이었고 채택을 실제 준비·routing·추론으로 세지 않았다.
- CUDA 근거:
  `/tmp/chill-control-execution-authority-20261015/gpu-actual-control/current-cuda-sdk-build-ort126/result.json`,
  `current-cuda-measurement/result.json` 및 같은 디렉터리의 원시 Run·실패 로그·profile read-back.

- 사용자 승인 범위에서 Jetson 3대의 서로 다른 target→restore **6동작 모두 Ready**를 확인했다.
  `912e948`의 native OnDelete 설정 지원은 부모 gate `artifact://3180` 후 실제 배포에 사용했고,
  원래 config/policy/safety·RollingUpdate(maxUnavailable1/maxSurge0)를 복원했다. 임시 config는 삭제했다.
  AGX actuator UID `9119e4d1-37d1-4ba0-beae-a86304783043`·args는 그대로다.

|노드|실제 target → restore|선택 노드 wall J (target / restore)|
|---|---|---:|
|Orin Nano|15W → 25W|978.2288465042501 / 968.4982465201132|
|Xavier NX|MODE_20W_6CORE → MODE_15W_6CORE|577.227660411873 / 604.440551876338|
|Nano|5W → MAXN|113.84855875779998 / 133.6415211550057|

- Orin은 실제 두 번 reboot했고 시스템 container restart도 raw에 남겼다. Nano CPU2/3은
  offline 후 복원됐다. mode ID/name·일부 CPU online/cap read-back은 확인했지만 Orin CPU cap은
  두 모드 모두 1728000kHz였으며 모든 설정 hardware limit의 일치를 주장하지 않는다.
  선택 노드 계측은 이유 누락 없이 확보했지만 Xavier/Nano target의 전체 POOL은 genesis2·AGX
  meter timeout으로 incomplete다(restore는 complete). 60초 settling·외부/시스템 작업을 포함한
  node-wall J이며 고립된 전환 증분 에너지·절감·자동 후보 채택 증거가 아니다.
  비특권 query permission 진단도 보존했고 별도 privileged before/final query는 성공했다.
- 근거: `/tmp/chill-completion-cpu-cost-assembly/approved-jetson-power/completed-summary.json`,
  `parent-final-native-check.json`. 마지막 native 확인은 세 노드·actuator Ready, 원래 RollingUpdate와 AGX UID 유지다.

- Dashboard Main `103506d` 전달을 받았다. 실제 브라우저에서 projected→fixed PUT200 후 다음 판단의
  알고리즘을 API와 대조했고 projected 복원 PUT200 후 다음 UI 문구·API도 일치했다.
  실제 기록 DETAIL의 fixed→projected 전후 값을 확인했다. 모든 9개 controls는 OFF,
  전체 settings는 의미상 원복(`settingsRestoredDifferences=[]`)됐다.
  근거: `/tmp/chill-completion-cpu-cost-assembly/dashboard-algorithm-e2e-proof.json` 및 연결 screenshot.
- 별도 OFF 동시 요청 16건은 모두 완료했지만 p90/p95 3325/3421ms로 SLO를 초과했다.
  앞선 190건 trace 성공과 구분하고 자동 과부하 대응·통계적 개선으로 세지 않는다.
  당시 fresh GPU prepare·owner routing/요청·FULL 통합·최종 Dashboard 종단은 미완료였다. UI 부분 통과·수동 Jetson 왕복은 FULL Dashboard 완료가 아니었다.

## CUDA 실제 완료·비용 연결과 3 profile FULL 통합 — 2026-10-15

- `644c7122`는 SDK 초기화 domain `[1,2]`와 실제 측정·dispatch domain `[1]`을 구분했다.
  측정하지 않은 batch2 dispatch를 허용한 것이 아니다. 새 GPU prepare와 owner 실제 24건은
  모두 GPU 할당·executionComplete였다. 첫 4건 catch-up burst를 보존했으며 정밀 고정 간격을 주장하지 않는다.
- GPU remove/prepare 실측 1쌍을 기존 경계로 채택했다: 각각 3.843947756s·209.3917868505J,
  7.473265159s·582.9832510703J다. n=1이며 분산·미래 지연 보장은 없다.
  측정 격리 중 두 CHILL workload를 정상 해제·재등록해 UID는 변경됐고 이름·SLO·ModelSpec은 보존했다.
  원시 과거 UID를 고치거나 호환 reader를 만들지 않았다.
  근거: `current-cuda-measurement/gpu-cost-actual-pair/result.json`과 `owner-route-proof/owner-gpu-final-summary.json`.
- 최초 3 profile 통합은 FULL tight p95 1142.160ms로 실패했고 실제 제어가 없었다.
  같은 벽전력 source에 추가된 thermal 관측 metadata 때문에 GPU idle meter 연결이 누락됐다.
  `6d9cc7cd`는 기존 power-series 의미 비교를 재사용하되 Node·scope를 계속 검증한다.
  thermal 원시 관측·이전 실패는 보존했다. focused kube와 `make lint test arch-check gopls-check` 통과(`artifact://3495`).
- 새 비교는 owner `6d9cc7cd` / image `sha256:8c976eeb0e751a15dcb0a754da531130a03e6cb6bcf7d2f7fc7acaeb04615047`,
  CPU2/512Mi다. 두 arm 모두 동일 조건이며 앞선 CPU1 실패의 단일 원인을 CPU로 단정하지 않는다.
  동일 3 ready 실행기·Latte 초기 routing·190건/150초 trace와 공통 lead-in30초·tail60초를
  **실행 전에 선언**했다. 실제 설정 ACK는 첫 요청보다 빨랐고 전체 10 meter의 240초가 complete다.

|조건|완료|tight p95 / 목표 1000ms|relaxed p90 / 목표 1500ms|전체 POOL wall J|
|---|---:|---:|---:|---:|
|OFF|190/190|258.711ms|243.321ms|54123.251|
|FULL|190/190|425.123ms|270.998ms|57495.104|

- FULL은 스스로 Rpi를 선택해 route·stop·drain·remove 8동작을 완료했다. 기존 Latte/GPU의
  실제 Pod/native source 소멸까지 확인했다. 판단 17건은 9개 controls ON이며 Keep10·Feasible5·
  Overload1·Pending1이다. 모든 판단의 후보 탐색 truncation과 연속 비중첩 projected history를 보존한다.
  전환 포함 gross energy는 증가했으며 고립된 CHILL 에너지·절감·인과·통계적 개선을 주장하지 않는다.
  고정 전력 profile의 제어 통합이 자동 전력모드 최적화 증거는 아니다.
  종료 후 9개 OFF/projected·Rpi 두 workload route ACK RV353571935·미완료 요청/동작 없음이다.
- 근거 루트: `/tmp/chill-completion-cpu-cost-assembly/integrated-three-profile-cpu2-rootfixed/`.
  `comparison-summary.json`, `full-operation-results.json`, `full-policy-history-summary.json`,
  frozen plans·원시 client/server/history·power·native snapshot을 함께 보존한다.
- 실제 Dashboard의 같은 FULL 이력 기본 limit50 요청은 owner200/19447961B지만
  BFF502 `DashboardResponseTooLarge`다. 후보마다 반복된 실행기 자료를 포함해 단일 판단이 약9.68MB다.
  `dashboard-default-page-size-proof.json`에 크기 근거를 남겼다. 최종 Dashboard 종단은 이 차단 해소 전 미완료다.

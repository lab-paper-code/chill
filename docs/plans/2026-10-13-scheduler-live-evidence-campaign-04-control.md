# CHILL 캠페인 진행 기록 — 측정 근거 연결·원자 제어와 실제 서비스 관측

[현재 목표·규칙·상태와 다음 의존성](2026-10-13-scheduler-live-evidence-campaign.md)

아래는 기록 당시의 진행 상태다. 현재 상태는 캠페인 진입 문서를 따른다.

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
  기존 SDK의 direct Pod IP 연결을 물리 인스턴스별 단일 endpoint Service로 교정한다.
  공유 Service에 라우팅 결정을 맡기거나 외부 EdgeMesh 설정을 변경하지 않는다.
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
- 실제 Dashboard의 해당 20초 조회는 현재 owner stall로 504/DashboardRelayTimeout이었다.
  화면은 timeout을 표시했으며 이전 성공 숫자를 대입하지 않았다. screenshot:
  `/tmp/chill-dashboard-power-exact-interval-timeout-20261014.png`.
  기존 dashboard-validation Workload 2개는 원래 모델을 참조하는 타 작업이며 그대로 유지한다.
- membership CLI는 실제 `pool=""` 요청을 수행 전 거절했다. 선택한 measurement POOL과
  detached 상태 사이에서만 실제 편입·탈퇴를 허용하도록 수정한다. 동일 상태 no-op는
  비용 evidence로 받지 않는다. owner의 일반 제어 idempotency는 유지한다.

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
- ponytail 리뷰의 restart alias·cleanup 과검증·반복 Service 검증·fake wiring 테스트 지적을
  수정했다. RestartPolicyNever와 자기 Pod Service 소유권 삭제를 유지하며 복사/옵션 echo
  테스트는 삭제했다. 필요한 회귀는 allocation 의미·unique destination·잘못된 소유권과
  실제 detach/drain을 확인한다. 최종 리뷰는 Lean already.
- commit `f96a38f`: 위 native 원자 제어 수정 9개 파일과 해당 RBAC hunk만 포함했다.
  독립적인 power API·routing·Dashboard 변경은 포함하지 않았다.
- focused serving/operation/owner Go tests 및 make lint 통과. arch/gopls/default Helm gate 통과.
  live Profiler render 74 resources valid62/invalid0/errors0/skipped12. 4개 uninstall hook
  resource가 helm get manifest에는 없는 차이를 확인했다. 새 deployable resource 증가는 아니다.
- make test는 untouched `TestObserverAdvancesAbsenceAndFreezesFirstPresence`의
  5ms timing/strict ordering assertion에서 실패했다. marker absent와 다른 marker의 first
  presence가 같은 receipt였다. 전체 pass로 적거나 실패 확인을 위해 다시 실행하지 않았다.
- 실제 owner의 DNS S3 read는 0.319초, production adapter의 기존 immutable 객체 Put은
  Existing/0.00307초였다. 이것만으로 publication timeout 원인을 네트워크라고 단정하지 않는다.
  실제 현재 journal의 closed/unpublished=0 및 201MB history를 read-only PVC Pod로 확인했다.
  검사 Pod와 throwaway Go source를 제거했다.
- 근거: `genesis2-atomic-summary.json`, `lattepanda-atomic-summary.json`,
  `lattepanda-policy-accepted-operation-summary.json`, `rpi-prepare-q1-2-stdout.json`,
  `rpi-loaded-idle-q1-1-stdout.json`, `rpi-remove-q1-*-stdout.json`,
  `owner-dns-immutable-read-probe.json`, `current-journal-metadata-probe.json`.

### 보고 시점 — 2026-10-14 실제 제어 연결

- genesis1/2, lattepanda, rasp5의 q1/q2 prepare·loaded idle·remove와 실제
  membership detach/join을 완료했다. resize 비용은 준비·제거 primitive로 합성한다.
  실제 자원 변경 제어의 종단 검증은 아직 별도 pending이다.
- accepted reference 39개를 현재 scheduler ConfigMap에 연결했다. 실패/Insufficient
  원본은 비용 참조에 넣지 않았다. 전력모드 측정의 외부 조율 blocker는 유지한다.
- release21/native-full-uid-binding과 새 control journal/기본 1분 archive window에서
  timestamped 실제 snapshot이 200/약40ms였다. 전체/활성 power 20초 조회는 200/약71ms,
  10노드 complete, 4993.424198745157J·249.67120993725786W였다.
- 실제 Dashboard에서 동일 구간 4.99kJ/249.67W·active0·10노드·원본 시각 없음 안내를
  확인했다. 표본 없는 구간은 측정값 없음이었다. screenshot:
  `/tmp/omp-sshots-15a5904c2ccff7b1.webp`, `/tmp/omp-sshots-15a591b12acff7b2.webp`.
- 실제 입력 수정에서 Invalid time value 화면 실패를 발견했다. 모든 관련 date handler의
  guard 및 CPU 예약의 per-Pod ceil 오판을 수정·리뷰했다. focused Go/lint,
  Dashboard test11/typecheck/lint/build 통과. 두 수정의 새 image 배포와 실제 재검증은 pending.
- native-control-a/b를 기존 API로 등록했다. 서로 다른 실제 UID, Ready/admission=true.
  첫 HTTPS 추론은 200·executionComplete=true·FP32 [1,1000] 실제 결과였다.
  native queue91.665185ms/service38.27635ms/native elapsed130.358594ms,
  외부 client 왕복1.3555289909709245초다. 서로 다른 지연 범위를 섞지 않는다.
- 이 인스턴스는 bootstrap-zero-unmeasured-no-SLO-proof envelope다. 실제 전달/반환
  보정과 SLO 검증 전이며, 자동 적용과 8개 제어는 OFF다. FULL 완료로 보고하지 않는다.

### 역할 기반 명칭 교체와 입력 수정 검증 — 2026-10-14

- Workload는 사용자-facing 이미지 분류 서비스로 구분했다:
  `image-classification-tight-slo` UID `1cb0d9c5-48ad-4d7d-b925-440d129961e2`,
  `image-classification-relaxed-slo` UID `0d41e22a-085e-449d-88cc-db5e8821713a`.
  기존 API create 201 후 원래 UID drain/delete 202를 실행했고 옛 이름은 실제 404다.
  두 새 서비스는 200/Ready/admission=true이고 실제 추론을 각각 완료했다.
- 내부 전달 Service는 `chill-executor-<실제 Pod UID>`로 교체했다. release23 owner
  `full-executor-services` index `sha256:57ab1c134284f15407c64c5c2e1efb5bfa282bfac7402d1696f110f2e4f7802c`.
  이전 `chill-native-...` Service 삭제 후 relaxed 서비스의 추론 200/실행 완료를 확인했다.
  불필요한 alias/fallback은 없다. 구현 `a1d556e`, focused serving tests와 make lint 통과.
  ponytail 리뷰: prefix 한 곳을 재사용하는 최소 변경이며 추가 abstraction/호환 분기 없음.
  근거: `/tmp/chill-native-campaign-executor-service-old-removed-smoke.json`.
- release23 Dashboard 날짜 수정 이미지를 배포했다. 실제 브라우저에서 DOM input/change
  event 및 Apply로 두 날짜 변경이 정상 적용됐고 빈 값/잘못된 날짜는 URL 유지·요청 0으로
  거부됐다. untouched 6자리·9자리 fractional ISO 보존을 확인했다. 키보드 입력 자체의
  증거는 아니며 helper fill 한계와 구분한다.
  `/tmp/omp-sshots-15a599b19cba5f68.webp`,
  `/tmp/omp-sshots-15a599d2b03a5f69.webp`,
  `/tmp/omp-sshots-15a599dd2aba5f6a.webp`.
- 같은 실제 브라우저의 power query는 200이지만 scheduler history query는 504로
  실패했고 timeout 표시를 확인했다. 날짜 수정과 FULL/history 성공을 혼동하지 않는다.

### 이전 진입 요약에서 옮긴 고유 이력 — 2026-10-14

- 첫 실제 추론은 완료됐지만 owner 종단 지연 `1.298374879s`는 frozen SLO `1s`를
  초과했다. **첫 표본 SLO 위반**이며 통과가 아니다. ingress validation/accepted
  elapsed `915.432437ms`, 실행 엔진 elapsed `130.358594ms`, 외부 client 왕복
  `1.3555289909709245s`는 서로 다른 범위다. 근거:
  `/tmp/chill-native-campaign-first-inference-owner-observations.json`.
- 대형 journal의 전체 scan/shared lock은 알려진 연구 규모 한계다. 원본을 보존한
  별도 제어 시간창과 기본 1분 archive window는 그 한계의 bounded workaround이며,
  범용 성능 해결이나 일반적인 S3 네트워크 원인 확정이 아니다.
- **모델과 실행 엔진 이름의 혼동 정정은 pending**이다. firstfloor는 실행 엔진이지
  모델 정체성이 아니다. 원래 `mobilenet-v2-050`(UID `b8bc6efa..`)은 다른
  dashboard-validation Workload 2개가 참조하고, 이번에 등록한
  `mobilenet-v2-050-firstfloor`(UID `545ef2c0-b931-4c96-b978-b4cef1195210`)는
  `image-classification-tight-slo` / `image-classification-relaxed-slo`가 참조한다.
  과거 이름·measuredUID·측정 근거를 이미 이관한 것처럼 바꾸지 않는다.
- 모델·준비 Pod 명칭 정리는 pending이며 과거 evidence는 원래 UID로 보존한다.
- owner CPU 예산 수정은 실제 snapshot smoke에서 5개 노드 Ready를 확인했다.
- orangepi memory peak 부재와 Orin Nano actuator readiness 문제도 성공으로 대체하지 않는다.

Orin 경로 준비와 저장 실패·복구의 당시 기록:

사용자 검토를 통과해 실행을 재개했다. **genesis1/2/3 측정·제어는 후순위로 보류한다.**
진행 중이던 genesis2 자기 캠페인은 8 trial 상태를 보존한 뒤 종료했으며 완료로 세지 않는다.
Orin Nano의 최초 GPU 컨테이너 생성은 Toolkit 1.20 CSV/CDI가 플러그인의 `tegra` ID를
거부하여 실패했다. 사용자가 제한된 수정에 동의한 뒤, NVIDIA가 지원하는 index 방식으로
**Orin의 플러그인만** `nvidia-device-plugin-orin-nano`에 분리했다. 공유
`nvidia-device-plugin-jetson`은 Orin을 제외하고 `OnDelete`로 두어 다른 3노드 Pod의
자동 롤링을 막았다. **공유 플러그인의 향후 자동 롤링도 꺼진 상태**이며 다음 운영 변경 때
이 사실을 반영해야 한다. 호스트 패키지·containerd·전력은 변경하지 않았다.
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
Ponytail 리뷰와 focused Go/Python7개 테스트, `make lint && make test`는 통과했다.
Orin에 설치된 CUDA12.6/cuDNN9.20의 실제 runtime library를 이미지에 고정했다.
최종 image `4b316e0b..`는 수동 host mount/pip 없이 UID65532에서 같은 SDK 초기화와
배치1/2 실제 추론을 통과했다. 증거는
`/tmp/chill-edge-orin-vendor-complete-image-actual-sdk-inference.json`이다.
범용 ARM64 CUDA image `3dcc21dd..`의 실제 CUBLAS 실패는 보존하고 채택하지 않았다.
host 호환 계층 없이 기존 실행 선언·정책 API로 등록했다. 최초 캠페인 UID `1fe633d1..`은
Orin에 없는 `fixed` 전력모드로 계획 해석에 실패해 측정을 시작하지 못했다. 자기 객체를
기록 후 교체한 `mobilenet-v2-050-orin-nano-cuda-q1`(UID
`94446d2b-e24d-4c38-a4c1-63654a70485f`)은 현재 25W read-back과 60초 settling을
확인했지만, loaded-idle 두 시도가 `memory.peak` 부재와 workload-observer의 API
접속 timeout으로 실패했다. 실제 전력모드 변경·성공 프로파일·채택은 없었다. 근거:
`/tmp/chill-edge-orin-current-mode-campaign-child-failure.json`.
계획 해석 실패는 이제 실제 resolver 오류와 `Failed/CurvePlanResolutionFailed`로
공개되며 오류 반환을 유지해 재시도할 수 있다. 로컬 API 서버의 실패→복구 회귀는
통과했고, 실제 캠페인 `orin-gpu-plan-resolution-check`는 같은 오류 상태와 자식 0개를
확인했다(`/tmp/chill-campaign-visible-plan-failure-live-proof.json`). Dashboard result
API는 결과 참조가 없다는 이유로 404를 반환했으나, 현재는 실제 캠페인 상태와 조건을
200으로 제공하고 아직 없는 storedResult/result는 null로 구분한다. 실제 Dashboard에서
인증 후 `Failed`, 완료 0건, 실제 power-mode 해석 오류, 완료 결과 없음을 확인했다.
API 증거: `/tmp/chill-campaign-visible-plan-failure-dashboard-api-proof.json`;
화면: `/tmp/omp-sshots-15a6197993ee87ba.webp`. console image `73bace12..`,
Dashboard image `4e0c0d7b..`를 실제 rollout했다. 이는 실패 공개 경로의 검증이며
GPU 측정 성공이나 FULL 제어 검증이 아니다. focused `go test ./internal/operatorconsole/...`,
Dashboard `pnpm typecheck && pnpm lint`, 두 image build는 통과했다.
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
현재 profiler `1b190380..`, console `905240dd..`를 rollout하고 동일 25W 조건의 자기
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
- 독립 ponytail/correctness 리뷰 후 완료 시각 덮어쓰기 회귀를 실패→통과로 확인했다.
  focused serving tests와 `make lint`는 통과했다. 마지막 `make test`는
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
  실제 prepare/remove·Serving envelope·FULL Dashboard 연결은 다음 작업이며 아직 미완료다.

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
- 기존 incidental/mock-echo 검증만 제거한 뒤 `make lint && make test`가 통과했다
  (`artifact://1101`). Dashboard typecheck/lint도 통과했다. 새로운 종단 완료 overhead
  계약은 아직 실제 배포·Serving smoke 전이며 gate 성공을 FULL 검증으로 보지 않는다.
- 최신 CPU·Orin SDK와 owner 이미지는 빌드 완료했다. 새 SDK는 기존 프로파일의
  runtime image를 덮어쓰지 않고 같은 조건을 새 캠페인으로 측정한 뒤 연결한다.
  Genesis 신규 측정·외부 워크로드·power allowlist는 변경하지 않는다.

### 최신 완료 관측 계약·모델 책임 분리 — 2026-10-15

- 리뷰에서 반환 중 요청의 교환 잔여 지연 중복 계산과 현재 인스턴스/UID에 묶인
  측정 재사용의 순환 의존을 확인하고 에이전트가 수정했다. 실제 관측한 교환 잔여와
  이미 경과한 응답 시간만 차감하며, 같은 실행 조건의 불변 정의에 근거를 재사용한다.
  ponytail 리뷰 후 중복 UID map·하류 재검증을 삭제했다. 전체 gate는
  `artifact://1210`, 최종 삭제 후 focused serving/firstfloor tests도 통과했다.
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
- Dashboard 현재·보관 상세에 완료 관측 여부와 반환 잔여 미확보/실제0을 구분했다.
  typecheck/lint·이미지 빌드·pony 리뷰 후 최신 이미지를 배포했다. 실제 상세 시각 검증은 아직이다.
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
  `actual native/profile snapshot unavailable`로 제외한다. Service 경로와 내부 모델
  프로토콜 식별자 바인딩을 두 에이전트가 분리 조사 중이다. timeout·검증은 완화하지 않는다.
- 최신 `make lint && make test` 통과: `artifact://1340`.
  FULL 제어·실제 추론 종단·최종 Dashboard 연결 검증은 아직 완료하지 않았다.

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
- Genesis 신규 프로파일링·제어는 보류다. Orin mode25W/id1 read-back과 물리 cap 불일치,
  baseline-fleet/fastdds 외부 작업 때문에 power hold는 유지한다. 최신 SDK로 기존 GPU raw
  evidence를 채택하지 않았다.
- 다음은 전체 제어 OFF에서 실제 Serving completion 관측을 확보하는 일이다. 같은 부하의
  OFF/SLO·whole-POOL 판단, 8개 개별 제어, FULL 시나리오와 최종 Dashboard e2e는 미완료다.

### 실제 OOM과 owner 용량 보정 — 2026-10-15

- 128Mi 제한에서 owner가 실제 두 번 OOMKilled됐다. 92개 관측·18개 서로 다른 Kubelet
  시각의 최대 사용량은 127.609Mi였고, GC live heap은 관측 최대 55Mi에서 부하 후 5Mi로
  감소했다. 이 범위에서 단조 증가하는 retained heap은 확인하지 못했다.
- 기존 Helm owner memory limit만 256Mi로 조정했다. CPU500m·memory request64Mi는
  유지했다. 명시적 enabled-owner render는 41개 중 29개 유효·12개 skip·오류0이었다.
- 동일한 60초 부하 30건이 실제 HTTP200·완료됐고 이후 idle을 포함한 100초·84개 관측에서
  restart0을 확인했다. 최대 RSS98.816Mi·working set167.582Mi·사용량255.574Mi였다.
  사용량에는 cache가 포함되므로 heap 여유나 무기한 무누수를 주장하지 않는다.
- 근거: `/tmp/chill-owner-memory-full-window-20261015/envelope-proof.json`,
  `/tmp/chill-owner-memory-256Mi-smoke-20261015/envelope-proof.json`,
  `/tmp/chill-owner-256Mi-unchanged-load-20261015/`. 최신 SDK 재측정·Serving 검증은 별도다.

### 최신 SDK·개별 근거와 정직한 OFF 판정 — 2026-10-15

- SDK `1496c4dd..`의 LattePanda/Rpi CPU1/2 네 캠페인을 새 UID로 순차 재측정해
  36 trial Complete를 확인했다. 실제 bundle 크기는 3276/3302/2781/2959 bytes다.
  `/tmp/chill-completion-cpu-cost-assembly/final-sdk-campaign-proof.json`.
- 준비·적재 유휴·제거 12개를 다시 측정했다. Latte CPU2 제거의 실제 resourceVersion
  충돌은 실패 raw를 보존하고, UID-fenced finalizer delta로 고친 뒤 정상 terminal cleanup,
  새로운 cold prepare와 제거를 수행해 Accepted/complete·drain counter를 확인했다.
  실패 receipt를 비용으로 쓰지 않았다. 독립 16개와 새 12개가 28개 참조다.
- 210개 Pod/6.89MB 반복 조회는 steady nonterminal 선택으로 0.34MB가 됐다.
  최초·watch 복구는 전체 조회하고 기존 executor는 exact UID/container GET을 유지했다.
  실제 동일 부하에서 기존 20초 post-load freshness 조건을 통과했다.
- `/tmp/chill-bounded-discovery-ready-off-baseline-20261015/`의 선택 구간은
  `23:45:03.020325097Z–23:46:03.020325097Z`다. 요청 30건이 HTTP200·완료됐고,
  해당 반개구간의 tight 표본 14건 p95는 2.359148466s로 1s 목표를 초과했다.
  알고리즘도 Overload/SLOCapacityExceeded로 판정했다. SLO 성공이나 절감으로 세지 않는다.
- 같은 구간 전체 10개 노드 전력은 complete·14168.1667566J, 활성 POOL은
  863.0517206J였다. 다른 작업 소비도 포함한 wall energy이며 CHILL 증분 소비가 아니다.
- 이전 실제 구간은 고친 API에서 9개 노드가 수치로 복구됐지만 Genesis1의 진짜 timeout과
  2.004520749s 공백은 계속 null이다. 새로운 완전 구간과 실패 구간을 구분해 보존했다.
- 현재 개별 제어의 선행조건은 원시 불변 참조로 선택하는 완료 지연 calibration과 명시적
  정적 초기 routing이다. 재현된 60초 유입 창을 늘리거나 측정 여부 boolean을 신뢰해
  우회하지 않는다. FULL·8개 실제 제어·최종 Dashboard 연결은 아직 미완료다.

## 진입 문서에서 이관한 상태 기록 — 2026-10-15

- resources-only 실험은 30건을 완료했으나 자원 변경은 발생하지 않았다. CPU 할당이
  겹치는 전환의 실행 순서를 수정 중이며 마지막 설정 변경에서 자동 제어를 모두 OFF로 했다.
  최신 SDK `1496c4dd..`의 네 조건·36 trial과 이미지 종속 prepare/loaded-idle/remove
  12개를 완료했다. 독립 근거 16개와 합친 28개를 실제 owner가 채택했다
  (10 baselines·18 costs). 이전 raw와 실패 receipt는 보존했다.
- LattePanda·Rpi만 병렬 측정했으며 같은 노드의 CPU1/2 실험은 순차 실행했다.
- `mobilenet-v2-050-image-classifier` UID `556e9ee8-bb87-40e9-a481-ce1ce44b309b`,
  generation2·artifact·CPU/CUDA 경로는 바꾸지 않았다. CPU execution facts의 SDK만
  교체했다. 과거 GPU raw evidence를 최신 SDK 근거로 채택하지 않는다.
- SDK 조회 404는 producer가 내부 식별자 `chill_model`을 business ModelSpec 이름으로
  덮어쓴 원인이었다. 두 책임을 분리해 고쳤으며 호환 alias는 추가하지 않았다.
- `33a17b1`은 resize의 기존 제거 경로에서 실제 drain counter 증거를 보존한다(3파일).
  fresh cold CPU2→CPU1 두 건 모두 Accepted/complete이며 제거된 actor의 counter가
  변하지 않았음을 확인했다. 삭제만으로 추론 부재를 가정하지 않는다.
- 종단 완료 시간 계산 `f10e818`, 전체 POOL 전력 `dd55fd7`, UID-bound finalizer
  `6c83539`, 반복 Pod 조회 축소 `3e391fc`를 각각 리뷰·실제 실행 후 커밋했다.
- 최신 SDK의 동일 입력 60초 OFF 부하 30건이 완료됐다. 같은 구간의 10개 노드 전력은
  complete·14168.167J이고, 1초 SLO의 실제 p95 2.359s는 정직하게 Overload로 판정했다.
- 실제 두 번 OOMKilled된 128Mi owner는 기존 설정만 256Mi로 보정했다(`c0f5009`).
  동일한 60초 부하 30건·100초 관측에서 restart0을 확인했다. 무기한 무누수 주장은 아니다.
- 완료 지연은 기존 raw 9개·고정 receipt 구간을 명시 선택해 네 조건 모두 실제 owner에서
  관측 근거로 채택됐다. 60초 유입 창은 그대로다. full 출처 518 bytes를 SDK에 직접 보내
  정책이 거절된 원인은 bounded witness로 고쳤고, Rpi 실제 준비 상태도 복구했다.
- 수동 독점 분배는 configRV `352752363`의 complete read-back과 실제 요청으로 확인했다.
  추가 60초 OFF 30건도 완료했고, 같은 구간 전체 전력은 complete·15748.940J다.
  이 구간 tight 14표본 p95는 3.483s로 1s SLO를 초과했다.
- 노드별 미확인이 다른 CPU 후보까지 막던 범위 오류는 `a20e2ec`로 격리했다. 회귀와
  실제 Serving을 확인했지만, 배포 시 Genesis3 미확인 조건은 외부 상태 변화로 사라져 있었다.
- placement-only는 실제 적용을 확인했다. 30건 모두 완료됐고, Latte 8건→Rpi 22건으로 이동했다.
  Latte의 stop/drain/remove와 실제 Pod 소멸을 확인했으며, 현재 Rpi CPU1 하나가 두 Workload를 처리한다.
- `46f62f8`은 journal 읽기를 writer mutex 밖으로 옮겼고, `10178af`/`2226d25`는
  공유 관계 보존·물리 대상 재사용·실제 전환 후 정리를 고쳤다. 실제 Dashboard에서도 Rpi 연결을 확인했다.
- **다음 단일 작업:** 겹치는 CPU 할당의 합법적인 자원 전환을 수정·검증하고,
  기존 API로 실제 자원 변경·완료·잔여 요청 처리를 확인한다. 판단을 유도하려고
  SLO나 비교 기간을 조정하는 대신 실행 계약을 먼저 검증한다.
  최소 폐루프의 실행·관측 근거 확보 후 위 판단 알고리즘 단계로 진행한다.
  전체 전력 API의 15초 timeout과 20초 fresh-snapshot 수집 실패는 미해소이며 실험 전체 성공으로 세지 않는다.
- Genesis 신규 프로파일링·제어는 보류한다. Orin mode25W/id1 read-back과 실제 물리 cap이
  불일치해 전력 작업은 보류 중이며 baseline-fleet/fastdds 등 외부 Workload를 보존한다.
- 이전 SDK 실행은 정상 정리했고 최신 SDK CPU1 및 같은 모델의 두 Workload를 사용한다.
  static/automatic 적용을 구분하며 무관한 dashboard-validation-a/b는 보존했다.
- `/pools`의 null collection crash는 `be475ba`로 별도 수정·배포했다. 실제 인증 브라우저에서
  목록·빈 그룹 상세/관계 팝업·미준비 실행기 행을 검증했다. FULL Dashboard 검증은 아니다.
- 개별 제어는 placement 실적용을 확인했다. 나머지 7개 제어, FULL 5개 시나리오와 최종 Dashboard 종단 검증은 미완료다.


## 합법적 자원 전환과 손 검증 — 2026-10-15

- `d8a1d30`: 겹치는 CPU/GPU 할당은 stop/drain/remove 후 prepare/route, 나머지는 prepare-first다.
  global route 직렬화와 진행 목표 보존을 포함한다. 리뷰에서 찾은 partial-failure nil panic은
  수정 전 독립 snapshot에서 재현하고 수정 후 회귀로 확인했다(`artifact://2145`).
  focused·독립 staged snapshot, make lint/test/gopls-check 및 serving/kube race 검사가 통과했다.
- owner 이미지 `f1d0b1f4..` 배포 후 CPU1 실제 추론 4건이 완료됐다. 실제 resources-only 후보에서
  합법적 DAG를 확인했지만 최신 판단은 `ExplicitRequestLifetimeCapacityExceeded`로 변경을
  탈락시키고 Keep를 선택했다. 모델·SLO·비교 기간을 완화하지 않았고 이후 controls 모두 OFF다.
- 손 검증은 기존 API로 이번 캠페인의 두 Workload만 정상 close/drain한 뒤 CPU1 source를
  원자 remove했다. 실제 이전 Pod가 없어진 후에만 기존 CPU2 profile로 prepare했다.
  두 원자 동작은 Accepted/complete였다. 다른 dashboard-validation Workload는 보존했다.
- 같은 spec으로 두 Workload를 복원해 CPU2 `[2,3]`·threads2·Pod CPU request/limit2와
  실제 routing을 확인했다. 두 UID에서 총 4건이 HTTP200·executionComplete였고 q2 profile을 사용했다.
  이는 수동 원자 동작 조합의 실적이며 자동 선택·무중단 resize의 증거가 아니다.
- 현재 물리 source UID는 `86336df7-573b-4728-9d70-6bbe9d5128c7`다.
  Workload UID는 relaxed `0718c1f3-ca2d-4912-a446-20a603354609`,
  tight `9ec18029-00e2-4987-a323-b5af36b46e10`으로 바뀌었으며 모델 UID·generation2는 유지했다.
- 증거는 `/tmp/chill-completion-cpu-cost-assembly/resources-hand-*` 및
  `legal-resource-planner-fresh-loaded-state.json`에 있다.
- cross-window reader 배포 후 같은 과거 구간 이력은 200·3.382초·776022 bytes였다.
  전력 조회는 여전히 15초 timeout이다(`cross-window-buffer-live-smoke.json`).

## 측정 운영 API·CLI와 기존 GPU 결과 채택 — 2026-10-15

- `/tmp` 임시 exporter·Eval/Bash의 제출/조회/설정 조립을 기존 비동기 측정 API와
  `measurements submit/list/show`, `profiles list/adopt`로 연결했다. 새 측정은 실행하지 않았다.
- `52efda0`은 owner GET/POST `/api/v1/scheduler/profiles`, ConfigMap 선택, 읽기 namespace
  제한과 권한을 추가했다. `90c8e1a`는 CLI와 검증된 HTTPS·caller bearer 전송을 추가했다.
  startup `--profile-campaign`은 제거했고 선택 권한은 `profileCampaigns` 한 곳에만 둔다.
- 기존 genesis2 결과 UID `5fdb92da-6c7b-4deb-b598-7052b85255c2`로 실제 검증했다.
  미허용 GPU는 ConfigurationRequired·exit1·ConfigMap RV 무변경이었다. 운영자가 측정된 GPU
  하나를 명시적으로 허용한 뒤 채택은 Persisted·exit0, 후속 조회는 기존 4개와 모두 Loaded였다.
  반복 채택도 exit0·RV 무변경이며 기존 지정 설정·준비 설정을 보존했다.
- `measurements list/show`로 실제 Complete·9 trial 결과를 조회했다. 서버 dry-run은
  NOT_SUBMITTED였고 해당 이름의 측정 객체는 NotFound였다. GPU 물리 준비·자동 선택은 미검증이고
  모든 자동 제어는 OFF다. 기존 서빙 경로의 실제 요청 4건도 실행·결과를 보존했다.
- 리뷰의 부분 profile publication, 미지원 runtime access 누락, GPU Env가 다른 Pod 재사용,
  결과 크기 상한 오류를 수정했다. 전체 lint/test, arch-check/gopls-check, 관련 race와
  기본/owner/Profiler 별도 Helm 검증을 통과했다. 독립 snapshot 전체 lint의 기존 무관한
  operatorconsole 테스트 포맷 문제는 보존하고 변경 패키지 lint·테스트로 분리 확인했다.
- owner 이미지 `aff40940cbb8b197b9081bc59536c0785a401c3cf25621458a9a88410d5fc771`로 배포하고
  실제 readyz를 확인했다. Dashboard 소스·배포는 별도 Main 세션에 맡기고 사용자에게 연결 프롬프트를 전달한다.
- 증거: `/tmp/chill-completion-cpu-cost-assembly/profile-cli-*.json`,
  `measurement-cli-show-live.json`; gate 출력 `artifact://2293`, `artifact://2300`.


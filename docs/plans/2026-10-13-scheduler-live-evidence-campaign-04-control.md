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


## Owner 측정 운영 네 흐름 종단 검증 — 2026-10-15

- 부모 통합에서 `9b4a008`(terminal writer), `e23dc82`(owner), `7356e97`(CLI)를 커밋했다.
  owner `1b8dc604…`와 profiler `2ce68f81…` 이미지를 함께 배포해 Ready를 확인했다.
  전체 lint/test/arch-check/gopls-check와 관련 7패키지 race가 통과했다(`artifact://2423`).
  기본/owner/Profiler Helm 검증도 통과했다(`artifact://2402`).
- 실제 목록 25개의 Complete/Failed 상태를 조회했다. 유효 manifest POST는 native server dry-run
  `200 / NOT_SUBMITTED`이며 CR을 만들지 않았다. 존재하지 않는 Model의 production preflight는
  `400`으로 거절됐고 CR도 없었다. 신규 측정 실행이나 정상 제출 `202`를 실검증했다고 주장하지 않는다.
- 물리 Rpi profile 채택 해제는 `409 ProfileInUse`, stale version은 `409`로 거절됐다.
  미사용 GPU는 `200 Removed / persisted=false / loaded=false`였고 나머지 4개는 Loaded를 유지했다.
  재채택 후 5개가 다시 Loaded였으며 해당 GPU raw 7449 bytes는 그대로 보존됐다.
- 폐기 대상 `retired-native-genesis2-q2-current`의 귀속 key 21개가 삭제 전에 모두 존재함을 확인했다.
  DELETE는 `202 / evidenceDeleted=true`와 21개 key를 반환했고, 독립 S3 조회에서 모두 `NoSuchKey`였다.
  `kubectl wait`로 native BEO와 소유 Run 9개의 소멸을 확인했다. stale/선택된 측정 삭제는 `409`였다.
- 다른 측정 UID, 현재 5개 profile, 새 GPU raw 7449 bytes와 Rpi CPU2 Pod UID는 변경되지 않았다.
  두 Workload UID의 실제 요청 4건이 `HTTP200 / executionComplete`였고 controls는 모두 OFF다.
  부분 실패·공유 참조 거절은 회귀검사로 확인했으며 live fault injection은 하지 않았다.
  이는 운영 계약 검증이며 자동 선택·SLO 개선·에너지 절감이나 FULL 제어 완료의 증거가 아니다.
- 증거 루트: `/tmp/chill-completion-cpu-cost-assembly/`.
  `owner-extension-{live-list,submit-smoke,profile-removal-smoke,delete-smoke,delete-readback,final-serving}.json`,
  `owner-delete-owned-evidence-before.json`. Dashboard 소스·배포는 별도 Main 세션에 인계한다.

## 실제 사건 시각 census와 동일 축 조회 지연 — 2026-10-15

- `63521317b5bb`의 window별 in-memory scheduler census를 owner 이미지 `382f3eb2…`로 배포했다.
  실제 사건·표본·membership 시각과 counted prefix로 읽을 window만 줄이고, prefix 불일치·
  nil-policy·baseline 동률은 보수적으로 읽는다. 별도 저장 index·판정 완화는 없다.
- 기존 60초 축 `01:07:01.47873812Z..01:08:01.47873812Z`의 전력 조회는
  HTTP200·28.997초에서 HTTP200·0.306722초로 줄었다. 이전 계측은 477 window·57,713 record·
  453,098,612 bytes scan 28.941초, projection 21.316초였다. 임시 계측은 제거했다.
- 같은 10개 노드의 각 79표본·`sourceTimestampAbsent=true`·`maximum sample gap exceeds policy`,
  `complete=false`·전체/활성 POOL J/평균 W null·`incompleteWindows=[]`를 유지했다.
  응답 지연 개선이지 전력 품질 복구·완전한 에너지 계측의 증거는 아니다.
- 이전 BFF 504·10.010초의 1시간 축 `03:37:35.732Z..04:37:35.732Z`, `limit=50`은
  owner 직접 조회에서 HTTP200·0.180565초·50건·`nextCursor=50`이었다.
  BFF·실제 브라우저 성공은 아직 재검증하지 않았고 별도 Dashboard Main 완료를 기다린다.
- scoped/race 및 독립 snapshot의 lint/test/arch-check·gopls-check는 부모가 통과를 확인했다.
  기존 무관한 operatorconsole 테스트 포맷은 임시 snapshot에만 적용하고 커밋하지 않았다.
  owner Ready와 기존 자원·서빙 구성을 유지했으며 controls OFF·FULL 미완료 경계는 같다.
- 근거: `/tmp/chill-completion-cpu-cost-assembly/{power-census-live-after.json,history-census-live-after.json,power-phase-measured.log}`,
  독립 gate `artifact://2480`.

## 공존 replica·실제 분배·원복·membership과 알고리즘 GO — 2026-10-15

- 원래 Rpi CPU2 UID `86336df7-573b-4728-9d70-6bbe9d5128c7`를 유지한 채 configured
  Latte CPU1 replica를 준비했다. CLI 09:52:38–55Z, 원자 결과 09:52:47.575–55.422Z/complete다.
  새 Pod UID는 `62e09e6f-df08-4509-bc1c-83600ade4094`, container는 `61572e81…`이며 두 replica가 공존했다.
- 전체 map의 CM RV `353175227`에서 relaxed UID `0718c1f3-ca2d-4912-a446-20a603354609`만
  Latte CPU1로 바꾸고 tight `9ec18029-00e2-4987-a323-b5af36b46e10`는 Rpi CPU2에 유지했다.
  실제 요청 총 4건(각 profile 2건)이 HTTP200·executionComplete였고 native batch는 모두 1이었다.
- CM RV `353176000`으로 두 UID의 전체 원래 경로를 복구했다. complete ack 이후 exact target
  outstanding 0과 닫힌 ingress를 근거로 정확한 Latte source를 drain/remove했다.
  CLI 10:04:35–57Z, 원자 결과 10:04:54.579–56.900Z/complete 및 native Pod 부재를 확인했다.
- 빈 Latte serving target의 detach와 `measurement-native-a` join이 모두 complete였다.
  공존 fastdds Pod UID `8aa8ad07…`는 그대로 Ready였고 외부 Pod를 중지·삭제·이동하지 않았다.
  최종 두 UID가 같은 원래 Rpi CPU2 instance를 다시 공유하며 요청 4건을 HTTP200·executionComplete로 마쳤다.
- firstfloor 실제 in-flight 문맥에서 tight UID, SLO p95/1000ms, 원래 receivedAt
  `10:08:01.247242079Z`, senderElapsed `186.298436ms`, lifetime `5000ms`,
  native running/batch1과 `manual-static-routing:353176000`을 관측했다.
  실제 적용 policy는 completionOverhead `1702.92632ms`, maxBatch2/wait0/deadline keep다.
  이전 2 trial의 명시 provenance를 가진 관측 maximum이며 이전 bootstrap0 상태나 미래 보장이 아니다.
- namespace/7-node 예외 기능은 제거했다(`c797…`, `5310…`). 닫힌 ingress의 monotone-zero
  소유권 경계 최종 CLI는 `e2733606`이며, owner는 계측 없는 `ffdea…` 기반 `0ecc95…` 이미지다.
  부모 전체 gate `artifact://2659`, census race `artifact://2631` PASS다. 수동 실행 실적이며
  batch2 실행·자동 선택·SLO 개선·에너지 절감·FULL 성공으로 세지 않는다.
- **알고리즘 설계·구현 GO:** 검증된 Latte/Rpi CPU 부분집합으로 진행하며 전 하드웨어를 기다리지 않는다.
  `minimum-loop-decision-gate.json`의 controls OFF/KeepFeasible·expectedJ 약 15177.35는
  최근 60초 도착 표본 0건의 판정이지 부하 중 SLO 증거가 아니다. 현재 whole-power GET은
  HTTP200이나 Jetsonx sourceReadTimeout으로 complete=false다. 합계를 만들거나 노드를 빼지 않는다.
  이전 08:03의 10노드 complete=true도 passive 관측이었다. 실제 입력 freshness ≤5초·
  loaded SLO feasibility·fresh observed energy는 자동 실행 전 남은 gate다.
  pairwise virtual transition 비용·service energy 비중첩의 WFA interface 차이는 다음 알고리즘 선택의
  검토 대상이며 WFA 채택을 주장하지 않는다.
- 증거 루트 `/tmp/chill-completion-cpu-cost-assembly/`:
  `replica-prepare-operation-specific.json`, `replicas-two-live.json`,
  `routing-two-replicas-inference.json`, `routing-restore-original-readback.json`,
  `replica-remove-closed-ingress.json`, `replica-removed-native-readback.json`,
  `membership-{detach,join}-result.json`, `control-execution-final-readback.json`,
  `firstfloor-{policy-control-readback,request-context-live}.json`, `minimum-loop-decision-gate.json`.

- `714d101` 시간 계약 수정을 owner `7e69d7f…`로 배포해 63초에 Ready를 확인했다.
  실제 snapshot `At=11:05:43.502208255Z`, `ObservationEnd=11:05:40.117877936Z`는 capture lag
  약 3.385초를 드러내며 Rpi 원래 `ObservedAt=11:05:40.156511315Z`는 덮어쓰지 않았다.
  decision-origin source age는 Rpi 약 3.346초·Node 약 3.385초로 5초 이내였다. HTTP에서
  보인 retained snapshot age 9.398초는 이 자동 source-age gate와 다른 값이다.
  같은 두 UID/Rpi q2의 실제 요청 4건이 다시 HTTP200·executionComplete였다. live 한 iteration
  통과이지 지속 부하 freshness·SLO 개선·AUTO ON/FULL 통과가 아니다. lint·변경 패키지는 통과했고
  전체 gate의 별도 transition timing 실패는 `ffd77e1` focused PASS 후 후속 소스 통합과 함께 남은 gate를 재실행한다.
  근거: `/tmp/chill-completion-cpu-cost-assembly/{decision-clock-live-smoke.json,decision-clock-serving-smoke.json}`.

- 고정 24건·2초 간격·controls OFF·owner CPU500m의 임시 계측 cohort가 모두 완료됐다.
  원인은 numeric validation이었다: p95 numeric `1289.768645ms`/Accept `1290.354712ms`,
  decode `266.481506ms`, Receive `0.043697ms`, Authorize `2.256781ms`이고 두 auth RPC는 모두 5ms 미만이었다.
  계측은 `/tmp/chill-admission-phase-source`에만 두었으며 production에는 logging을 넣지 않았다.
  `input.go`의 numeric 경계만 전체 JSON 문법 검사+`bytes.Cut`+동일 scalar 범위 검사로 바꿨다.
  Go1.23.4에서 같은 FP32 `[3,224,224]`의 full `validateInput` benchmark
  (`BenchmarkValidateInputImageFP32`, `-benchtime=3x -count=3`) 중앙값은
  `164.192045→16.269690ms/op`(약 10.09배), `20474304→626B/op`, `1204230→1alloc/op`이었다.
  이는 host microbenchmark이며 배포 후 live SLO 개선은 아직 검증하지 않았다.
  앞선 선언 cohort 24건의 relaxed p90 `2024.804838ms > 1500ms`, tight p95
  `1585.373869ms > 1000ms` 위반과 immutable completion maximum `2111.623881ms`는
  필터·재해석·채택 없이 그대로 보존한다. CPU/QPS/auth/SLO 변경이나 AUTO ON/FULL 통과의 증거가 아니다.
  근거: `/tmp/chill-completion-cpu-cost-assembly/admission-diagnostic-{plan,results}.json`,
  `admission-diagnostic-phases.log`, `completion-calibration-declared-{observations,spans,selector,loaded}.json`;
  benchmark·경계 회귀는 `internal/serving/input_test.go`에 있다.

## 판단 구현 배포와 입력 검증 수정의 실제 재측정 — 2026-10-15

- 입력 검증 `84374e4f`, 판단 통합 `4bf8be28`을 별도 커밋했다. lint·test·arch-check·gopls-check와
  serving/kube race가 모두 통과했다(`artifact://2789`). 다른 세션의 미커밋 코드는 배포에서 제외했다.
- 해당 커밋의 깨끗한 소스로 owner `e37fdde8…`를 빌드·배포했고 HTTPS ready를 확인했다.
  임시 진단 이미지는 교체했다. 설정은 필수 `algorithm`을 명시하며 자동제어 9개는 모두 OFF다.
- 같은 조건의 24건을 11:57:02.977–54.977Z에 재측정해 전부 HTTP200·executionComplete였다.
  relaxed p90은 `796.633320ms < 1500ms`, tight p95는 `1869.227867ms > 1000ms`다.
  최악 tight 요청의 admission `1278.546938ms`, exchange `590.339165ms`, native `182.696078ms`를
  보존한다. 개선된 중앙 성능을 전체 SLO 통과로 바꾸지 않으며 남은 admission tail은 원인 확인 중이다.
- 실제 owner에서 `projected-work-history` 설정 수락과 `policy.selection=ControlsOff`, 실행 동작 0개를
  확인했다(CM RV `353274534`). 이는 구현·설정 smoke이며 history 선택·자동 전환·에너지 비교 통과가 아니다.
- 근거: `/tmp/chill-completion-cpu-cost-assembly/input-validation-live-after-{plan,results,observations,spans}.json`,
  `clean-policy-owner-ready.json`, `projected-policy-controls-off-{config,smoke}.json`.

- 후속 CPU 제한 비교는 진단 이미지·SLO·메모리·요청 조건을 유지하고 owner CPU limit만
  500m→1로 바꿨다. 24건 중 23건 완료·1건 explicit lifetime 504여서 개선으로 판정하지 않았다.
  원래 500m와 계측 없는 `e37fdde8…` 이미지로 복구했지만, 재기동 시 scheduler 초기화가
  timeout됐고 S3 PUT은 Service `10.97.177.19:9000`의 connection reset을 기록했다.
  직접 Pod `192.168.109.171:9000`는 같은 Host header에서도 S3 403을 약 1ms에 반환했으나
  Service 경유는 reset/timeout이었다. 저장 데이터 삭제나 Pod IP 고정 우회 없이 통신 경로를 조사한다.
  근거: `owner-quota-one-{plan,results}.json`, `owner-restore-previous.log`,
  `evidence-store-{health-during-owner-startup,host-header-comparison}.json`.
- 실제 genesis1 NAT는 이 Service를 EdgeMesh `169.254.96.16:45881`로 전달했고 해당 listener도
  존재했다. genesis1/3 양쪽 EdgeMesh 로그는 상대 peer 주소가 없는 `routing: not found`를
  기록했으며 CoreDNS 교차 노드 전달도 같은 오류였다. 공유 EdgeMesh peer 경로까지 장애를
  좁혔지만 peer discovery가 깨진 원인·복구 방법은 미확정이다. 다른 사용자의 통신에 영향을
  줄 수 있어 공유 agent 재시작·설정 변경은 하지 않았다.
  근거: `evidence-service-edgemesh-finding.json`, `genesis1-service-nat-rules.txt`,
  `edgemesh-genesis{1-service-failure,3-peer-state}.log`.

## 공유 통신 복구와 최신 CPU 쌍 Serving 근거 — 2026-10-15

- 사용자 승인 후 genesis1 EdgeMesh agent 하나만 재시작했다(`frkds`→`vxnm5`).
  Service VIP는 S3 403을 약 2.8ms에 반환했고 cluster DNS도 정상 응답했다.
  공유 설정·genesis3 agent·외부 Pod는 변경하지 않았다. owner의 기존 crash backoff를 해제하기 위해
  owner Pod만 재생성한 뒤 HTTPS ready와 실제 요청 복구를 확인했다.
- 정상 통신에서 owner CPU limit만 500m→1로 바꾼 깨끗한 이미지로 다시 측정했다.
  메모리 256Mi·CPU request 10m·모델·입력·SLO·자동제어 OFF를 유지했다. 이 1코어 설정을 유지한다.

|실행기·코호트|완료|relaxed p90 (목표 1500ms)|tight p95 (목표 1000ms)|
|---|---:|---:|---:|
|Rpi CPU2 · 12:44:55.189–45:47.189Z|24/24|559.836440ms|315.618723ms|
|Latte CPU1 · 12:51:43.357–52:35.357Z|24/24|334.883330ms|356.757858ms|

- 기존 API·원자 동작으로 Latte를 준비하고 전체 두 UID를 분배해 측정한 뒤, 원래 Rpi로 복귀하고
  exact Latte source를 제거했다. prepare 12:49:49.816–56.622Z, remove 12:54:38.316–42.429Z가
  complete였고 native Latte Pod 부재를 확인했다. 원래 Rpi physical ID는 유지했다.
- 비교 후보만 기존 Rpi CPU2/Latte CPU1로 제한했다. 다른 프로파일·근거는 삭제하지 않았다.
  두 코호트 전부를 채택해 owner option에서 Rpi completion maximum 386.677458ms,
  Latte 497.248462ms와 각각 requests=24를 확인했다(CM RV `353315832`).
  관측 maximum이며 미래 보장이 아니다. 아직 자동 정책 선택·전환·에너지 비교 통과는 아니다.
- 근거 루트 `/tmp/chill-completion-cpu-cost-assembly/`의 `edgemesh-authorized-repair-result.json`,
  `owner-recovered-after-edgemesh.json`, `healthy-network-owner-cpu-one-{plan,results,observations}.json`,
  `latte-fresh-serving-{plan,results,observations}.json`, `latte-fresh-calibration-{prepare,remove}-output.json`,
  `fresh-cpu-pair-adoption-readback.json`, `healthy-cpu-pair-calibrations-config.json`.

## 190건 기준선과 저장·owner 자원 한계 복구 — 2026-10-15

- Rpi CPU2/controls OFF의 150초 short·sustained·repeated trace 190건이 모두 완료됐다.
  각 UID 95건의 relaxed p90 362.045322ms, tight p95 360.037347ms였고 고정 같은 구간의
  전체 10노드 wall energy는 36968.953392J, complete=true였다.
  `closed-loop-off-{plan,results,observations,power}.json`에 보존한다.
- 측정 프로파일상 Rpi CPU2가 Latte CPU1보다 빠르고 건당 추가 에너지도 낮았다
  (batch1: 0.342430J 대 0.454214J). 실제 자동 전환은 초기 상태를 한 Latte로 맞춘 별도
  OFF/fixed/projected 동일 trace로 검증한다. 앞선 Rpi 기준선을 다른 초기 조건과 직접 비교하지 않는다.
- Latte 준비는 complete였지만 종료 시 publication이 QuotaExceeded로 실패했다.
  실제 evidence-store 전용 PVC 2Gi가 100%여서 기존 operator config의 storageSize를 8Gi로
  변경했다. CSI 온라인 확장 후 25% 사용과 실제 S3 쓰기·읽기를 확인했다. 기존 근거는 삭제하지 않았다.
  같은 journal의 다음 동작에서 이전 pending prepare 근거도 정상 publication됐음을 확인했다.
- owner는 기존 256Mi limit에서 OOMKilled(exit137)됐다. 호스트 여유 약18GB를 확인한 뒤
  CPU1은 유지하고 limit만 512Mi로 변경했다. 새 Pod ready와 restart0, 약87Mi의 한 시점 사용량을
  확인했으며 peak/누수 없음의 증거로 해석하지 않는다. 아직 자동제어는 전부 OFF다.
- 현재 한 Latte로 전체 UID를 분배한 뒤 원래 Rpi exact source를 drain/remove했고
  13:22:46.046–50.511Z complete와 native Pod 부재를 확인했다. 이 상태에서 세 비교 arm을 시작한다.
  근거: `evidence-volume-expansion-smoke.json`, `evidence-store-config-expanded.json`,
  `rpi-latte-comparison-initial-remove-result.json`, `closed-loop-owner-resource-envelope.json`.

## 자동 준비 이후 freshness 정지의 live read 경계 수정 — 2026-10-15

- 고정 190건 trace `13:28:16.837–13:30:46.837Z`는 모두 완료됐으나, 실제 Rpi CPU2
  prepare `13:28:37.810–52.674Z` 이후 capture lag 12.127초·7.651초와
  `UnknownOrStaleNode`로 다음 동작이 Pending이었다. 두 replica가 Ready여도 두 UID는
  Latte에 남았으며 자동 handoff 완료로 세지 않는다. 부모는 `13:32:37.468Z` canonical
  settings PUT HTTP200으로 자동제어 9개를 모두 OFF로 동결했다.
- 원인은 historical cutoff를 새 Node 관측 시각으로 사용하고, native refresh 뒤에 journal
  arrival·completion 읽기를 수행한 순서였다. 기존 projected interval·arrival·overhead
  reducer를 모두 먼저 읽고 shared capture에서 Observer refresh를 수행하도록 수정했다.
  Node 시각은 실제 List 요청 시작 하한, native instance 시각은 기존 probe receipt를 보존한다.
  고정 history from/to·최종 Runtime mutex의 현재 요청/정의/routing/credits read·판정 영속화
  선행 순서와 5초 freshness 정책은 바꾸지 않았다. 느린 live HTTP는 계속 실제 age로 Pending이다.
- 기존 kube scheduler 테스트에 실제 journal publication lock 지연 뒤 새 Node/native 관측을
  소비하는 회귀와 느린 live Node HTTP의 stale/Pending 회귀를 추가했다. 원래 receipt의
  elapsed/lifetime·half-open history 구간·native timestamp 보존을 함께 확인하도록 작성했다.
  자식은 build/test/lint/format·배포·live action을 실행하지 않았다. 부모의 freeze 이후 gate와
  같은 실제 trace 재측정 전이며, 이 소스 수정은 live 전환·SLO·에너지 비교 성공 증거가 아니다.

## CPU 세 arm 폐루프 완료와 사후 공통 정리 구간 — 2026-10-15

- 동일 Latte 초기 상태·동일 150초 유입 trace의 OFF/`fixed-horizon`/`projected-work-history`는
  각각 190건 전부 완료했다. relaxed p90 목표 1500ms·tight p95 목표 1000ms를 모두 충족했다.
  앞선 실패·Rpi 초기 기준선과 구분하며 원래 계획·150초 결과는 그대로 보존한다.

|arm|완료 요청|relaxed p90 ms|tight p95 ms|원래 150초 전체 wall J|공통 210초 전체 wall J|
|---|---:|---:|---:|---:|---:|
|OFF|190/190|240.308122|330.160767|37128.978887644975|50836.0479995581|
|fixed-horizon|190/190|369.484564|360.629529|38491.37120872878|52270.85701988178|
|projected-work-history|190/190|276.391084|317.793699|37064.469521596126|50974.11639520777|

- 실제 자동 Latte → Rpi prepare·route·stop·drain·remove가 완료됐고 이전 Latte Pod가
  사라졌다. projected drain은 시작 후 150.063초에 완료됐으며, 이미 시작된 cleanup은
  예정된 controls OFF 이후에도 계속됐다. 따라서 150초만으로 전체 전환 완료 에너지를 주장하지 않는다.
- 세 arm에 동일한 추가 60초 무유입 tail을 붙인 210초 비교는 **사후 선언**이다(`predeclared=false`).
  OFF `14:23:35.364–14:27:05.364Z`, fixed `14:28:29.182–14:31:59.182Z`,
  projected `14:40:46.870–14:44:16.870Z`를 사용했다. 요청·source 제외 없이 전체 10개 meter
  에너지가 complete다. 다른 작업을 포함한 whole-pool wall energy이며 CHILL 증분 에너지나
  에너지 절감·통계적 유의성·인과적 효과를 주장하지 않는다.
- policy-summary는 실제 min-plus history의 연속 비중첩 두 interval
  `[14:40:48.930960883Z,14:41:57.338345347Z)`와
  `[14:41:57.338345347Z,14:43:01.935011579Z)`의 전진을 보인다.
  kernelRows는 1→2, finiteStates는 2→4다. 단순 설정 smoke가 아닌 history 소비 증거지만,
  중간 `Pending:NoFullyEvaluableCandidateWithinReportedBounds`도 보존한다.
  queue-reset projection을 실제 queue 이력·고전 WFA 보장으로 해석하지 않는다.
- 이 비교 직후 모든 controls는 OFF이고 Rpi가 서빙했다. 당시 GPU·Jetson 실제 제어와
  Dashboard Main 완료 통지는 남아 있었다. 이후 Jetson 왕복·Main 전달·알고리즘 UI 부분 e2e의
  갱신은 아래 기록을 따른다. CPU 비교 자체는 FULL·최종 Dashboard e2e 완료 증거가 아니다.
- 근거 루트 `/tmp/chill-completion-cpu-cost-assembly/`:
  `final-comparison-summary.json`, `final-comparison-common-settling-analysis.json`,
  `final-comparison-common-settling-power.json`, `final-comparison-projected-policy-summary.json`,
  `final-comparison-projected-full-completion-history.json`.
  위 freshness 실패와 기존 raw·한계는 남기되 현재 다음 작업으로 재사용하지 않는다.

## 최신 CUDA 근거·승인된 Jetson 왕복·Dashboard 부분 e2e — 2026-10-15

- 구 CUDA prepare는 `completionOverheadMS` 미지원으로 실제 실패했고 생성 자원은 정리했다.
  이후 CUDA ABI12·ORT1.26·현재 Go SDK의 runnable child image
  `sha256:52d81b5028d22e2c9c7f1ef4c3bd1ab2d4b41bb5d6421d69938b5c02d1100136`으로
  새 BEO `97a02e77-e075-47ca-9ed5-e0327413007d`의 실제 6 trial을 측정했다.
  현재 profiler 의미 gate와 scoped Service GET RBAC를 맞춘 뒤 **같은 6 Run**을 Complete로 봉인했다.
  재실행·구 raw 덮어쓰기가 아니며 새 profile은 configRV `353471711`에서 Persisted·Loaded다.
- 구 profiler는 세 번째 idle의 실제 foreign baseline-fleet 종료로 달라진 EnvironmentIdentity를
  equality gate로 거절했다. 현재 경계는 [측정 명세](../spec/00_measurement.md)의 47–50·102–104행대로
  공존·간섭 관측을 보존한다. 실제 차이를 삭제하거나 격리·동일 배경·인과 에너지 증거로 바꾸지 않는다.
  batch1만 측정했고 fresh completion calibration·방향별 비용·자동 GPU 적격성은 입증하지 않았다.
- owner는 CPU1/512Mi에서 `16:14:52Z` OOMKilled 후 connection refused를 냈다. 원인은 **미입증**이다.
  `16:22Z` 자연 복구·ready를 확인했고 당시 60초 incomplete observation window는 일시적이었다.
  영구 calibration 장애로 해석하지 않는다. owner `6ad78…`·CPU1/512Mi·store8Gi,
  ModelSpec·CPU business route는 유지했다.
- CLI 기본 `limit=10000` 페이지의 실제 64MiB 초과를 확인해 `7ca66ddd`에서 명시적 limit50과
  기존 cursor를 사용하도록 고쳤다. 부모 gate는 `artifact://3334` 통과다. reader는 이미 bounded였으며
  새 reader/recovery framework를 추가한 것이 아니다. GPU native prepare는 bounded CLI의 fresh
  완료 증거 전까지 보류했고, profile 채택을 prepare·owner routing·실제 추론 완료로 세지 않는다.
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
  다음 의존성은 fresh GPU prepare·owner routing/요청 완료, 남은 FULL 통합과 최종 Dashboard e2e다.
  알고리즘 선택 UI 부분 통과·개별 Jetson 수동 왕복으로 FULL Dashboard 완료를 선언하지 않는다.

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

## FULL 조회 크기 차단과 실제 UI 추론 — 2026-10-15

- `785c944c`·`9365f9ae`는 현재/상태/프로파일/판단 이력 GET의 `view=compact`를 제공한다.
  응답 안에서 반복된 profile·completion 측정만 사전과 참조로 공유한다.
  전체 원본 객체·서로 다른 같은-ID 값·후보 순서·SLO·설정·사유·동작 ACK를 보존하며
  기본 raw API와 journal/archive는 그대로다. 별도 cache·호환 reader·digest 검증은 없다.
  과구현 리뷰 후 임시 fixture size 테스트·단순 forwarding assertions를 제거했다.
  focused entrypoint와 전체 `make lint test arch-check gopls-check`가 통과했다(`artifact://3597`, `artifact://3609`).
- owner `9365f9ae` / image `sha256:063d4a3714b20792553787ec2197079bcb922a53ad568ad180b31bb9c3fc8a79`
  실제 readyz를 확인했다. CPU2/512Mi·PVC·Model/SLO·Workload UID·Rpi 실제 route를 보존했다.
  위 FULL 구간의 compact limit50 전체 11페이지·523건의 최대 응답은 5330711B였다.
  17판단을 limit1로 각각 조회했고 최대 2831999B, 원본 값·순서 대조도 통과했다.
  잘못되거나 중복된 view 8요청은 모두 HTTP400이었다. 모든 미래 구간의 크기를 보장하지는 않는다.
  근거: `compact-scheduler-read-cutover/final-9365f9ae/result.json`.
- Main에는 4경로 query 허용과 기존 화면 파싱 전 공통 참조 복원 프롬프트를 전달했다.
  Dashboard 자체는 수정·배포하지 않았다. 실제 BFF/UI 연결 완료 통지와 최종 FULL 화면 대조는 남아 있다.
- 실제 Dashboard에서 채택된 CUDA 측정의 batch1·6회차·1.79J 증분 에너지와 Loaded 의미를 확인했다.
  이어 built-in ARM 입력 예시를 전송해 실제 요청 `f20ecf92a988d141f020e64e720cd085`의
  HTTP200, Rpi 실제 할당·native finished(batch143/1)·응답 완료 128.359063ms를 화면에서 연결했다.
  당시 9개 controls는 OFF, 판단 ID는 `manual-static-routing:353571935`였다.
  따라서 실제 UI 추론 증거이지 FULL ON 자동 판단으로 보낸 요청이라는 주장은 아니다.
  근거: `integrated-three-profile-cpu2-rootfixed/dashboard-ui-serving-proof.json`과 연결 텍스트·screenshot.
- 이번 세션 도구 기록 1159 Eval·745 Bash와 `/tmp` 흐름 회고에서 남은 핵심은 위 반복 응답이었다.
  측정 CRUD·채택·bounded CLI·native OnDelete 지원을 다시 만들 필요는 없었다.
  일반 명령 wrapper를 추가하지 않고 실제 실패가 확인된 조회 계약만 고쳤다.

## 고부하 실행 실패와 Kubernetes client 예산 — 2026-10-15

- `full-load-reversal/summary.json`의 실제 구간은 19:01:43.679027–19:06:23.679122Z다.
  916건 outcome은 완료134·거절286·timeout496, 별도 오류 코드는
  AuthorizationUnavailable285·RequestTimeout497이다. 1건 차이를 합치거나 재분류하지 않는다.
  high 720건의 client 결과는 HTTP200 74·HTTP504 307·연결 reset/broken pipe 339다.
  baseline SLO는 통과했지만 high p95/p90은 약5.01초로 실패했고 burst·recovery도 timeout이다.
- 실제 자동 route는 19:02:51.146628182Z에 완료됐다. 관측기 46회 timeout으로 trigger를 놓쳐
  drop이 82.532494초 늦었다. 원시 `NoTransition`은 보존하되 의미는 **UNOBSERVED**다.
  즉시 반전 시나리오 통과가 아니며 판단22건 중 Overload는 없었다(Keep3·Pending18·Feasible1).
  3실행기 ready·capacity24와 최대 outstanding18만으로 native 용량 초과를 주장하지 않는다.
- 10노드 각각394표본의 정확한280.000095초 wall energy는64617.894352491J다.
  비교 arm·인과 절감 주장은 없다. OFF API의 실제 timeout과 사후 관리 복원을 분리했고,
  재시작 없이 19:09:24.267Z의 ACK RV353603758·9OFF·Rpi 실제 route·미완료 동작0을 확인했다.
- owner 로그의 TokenReview client-side throttle 4.79–5.00초와 client-go 기본5QPS/burst10을 확인했다.
  기본 REST API group별5QPS보다 높은8requests/s를 보냈으며 APF·CPU 포화의 증거는 아니다.
  약109초 판단 공백의 전체 원인은 여전히 미입증이다. lock·인증 cache·오류 suppression을 추가하지 않았다.
- `ce47d416`는 owner의 `--kube-api-qps=50`, `--kube-api-burst=100`과 기존 Helm `kubeAPI` 값을
  두 client 생성 전에 적용한다. typed clientset은 한 limiter를 공유하며 별도 Store 예산은 독립이다.
  stdlib numeric flag·Go 시작 경계 검증만 사용하고 중복 Helm 범위 gate·임의 int32 상한은 제거했다.
  focused cmd, `make lint test arch-check gopls-check helm-lint helm-template` 및 별도 owner-enabled
  default/Profiler 렌더가 통과했다(`artifact://3659`). 기존 권한 거절과 materialize 독립 경계는 유지한다.
- 실제 client-go/Authorizer의 로컬128건·8/s smoke는 기본111완료/17인증불가·평균2.185초,
  50/100은128완료/0인증불가·평균1.277ms였으며 denied SAR는 계속 Forbidden이었다(`artifact://3651`).
  로컬 재현은 cluster/APF 처리량·FULL 재검증 성공의 증거가 아니다. 실패 원본은 보존한다.

## 후속 실패 보존·새 작업 단위와 준비 정책 정합성 — 2026-10-15

- API50/100 실제 인증 smoke128건은 모두200이었다. 후속 고부하는 owner512Mi OOMKilled와
  불완전 JSONL로 실패했다(`full-load-reversal-api-budget/bounded-report.json`).
  원본을 잘라내거나 성공으로 재해석하지 않았고 전체 power 합계도 미완성으로 남겼다.
- 남은 Rpi8건·Latte6건은 각각 해당 실제 컨테이너의 종료 확인 뒤 기존 observer가
  `error/InstanceStopped`로 종결했다. 원래 timeout/불완전 응답은 보존했다.
  근거는 `retained-native-ownership/{physical-stop-closure,second-physical-stop-closure}/result.json`이다.
- `95451116`은 관측 복원 전에 경로를 비우던 시작 순서를 고쳤다. CPU2/1Gi·Go soft768MiB,
  API50/100·동일PVC에서 새 journal 작업 단위를 열었다. 원래 Workload UID·Model·SLO를 유지했다.
  9ON 재시작 후 실제 경로 재구성과 추론은 확인했지만 이전 route weight의 영속 복원은 아니다.
  근거는 `startup-954-clean-workunit-cutover/result.json`이다.
- 새 실행의 Latte 준비는 실제 native policy와 Pod bootstrap 기대 ACK가 달라 PARTIAL이었다.
  `b200966e`는 재사용 ConfigMap의 현재 정책 ID를 사용하며 준비된 동일 Pod의 오래된 annotation을
  낙관적 잠금으로 정합화한다. ConfigMap의 측정 정책을 bootstrap 값으로 덮지 않는다.
  독립 native readback·observer readiness는 계속 필요하다. focused 회귀와
  `make lint test arch-check gopls-check`가 통과했고 correctness·ponytail review를 반영했다.
- b200 owner와 동일 source CLI를 배포했다. 이미 활성화된 Pod의 정상 Prepare는 거절됐으므로
  실제 CM/native 정책 일치·Pod UID/RV·이전 annotation을 확인한 기존 Kubernetes patch만 사용했다.
  같은 Latte UID/container·restart0·처리0에서 owner/native Ready가 됐으며 CM UID/RV·497.248462ms
  측정 정책은 그대로다. 과거 PARTIAL 결과는 바꾸지 않았다.
  `prepare-policy-b200-cutover/result.json`에서 9OFF·Rpi ACK353693522·outstanding0을 확인했다.
  이 시점에는 새 부하/T0/GPU 준비를 시작하지 않았다. 이후 실행은 별도 실제 결과로 기록한다.

## GPU 실제 측정 조건 복원과 새 FULL 실패 — 2026-10-16

- 새 GPU 준비는414212096B cap에서 OOMKilled였다. 동일 SDK·CPU/GPU·PodSpec의 이전 성공만으로
  원인을 확정하지 않았다. 원래 성공 측정 Job `2fc2c37c-45c2-4fab-bb6a-d7231552cd39`의 실제
  request/limit와 frozen 정책은 모두2Gi였다. lifetime highwater는 실행 상한의 보증이 아니다.
- 사용자 승인으로 두 세션 소유 Workload를 해제해 기존 격리 경로를 그대로 사용했다.
  2Gi prepare8.071496611초/613.08790080215J, remove4.306534710초/420.35326433845006J가
  각각 Accepted/Complete로 새 directed key에 채택됐다. 단일 trial이며 원래 관측 메모리는 변경하지 않았다.
  같은 이름·Model·SLO로 재등록한 UID는 relaxed8194ef04-cb2a-4c87-9947-6d2f91baa410,
  tight15dbfd9e-7f7a-4ce8-a2af-96cf7d780ee5다.
  `gpu-2gi-new-measurement-unit/final-new-measurement-unit-summary.json`에 복원 근거를 남겼다.
- 새 FULL 구간00:30:31.523653–00:35:11.523780Z는916건 중185완료/731timeout으로 실패했다.
  클라이언트와 received/response_finished ID 집합은 같다. 전력은 jetsono timeout으로 불완전하여
  whole-pool 합계는null이다. 자동 동작 완료 기록이 없고 관측 timeout도 있으므로 물리적 부재로 확대하지 않는다.
- 실제 native per-ID finished185건은 완료 client185건과 정확히 같다. 나머지 cached assigned8건은
  BackendUnavailable·executionComplete=false이며 native progress가 없어 소유권을 유지한다.
  해당 ledger에는 inner error detail이 저장되지 않아 원인을 복원할 수 없다.
- 단 한 번의 승인된 gdb attach00:41:05.866368–00:41:10.745880Z 후 정상 detach했다.
  실제 실행 스택은 replay→projection kernels→Tick이고 refresh·PUT은 동일 owner mutex에서 대기했다.
  기존 timeout30초가 pure CPU에 전달되지 않았다. retained21×21×MC100의 유한 연산량과
  무한 반복을 구분한다. canonical9OFF·Rpi revision353849182 저장과 실제 반영 완료도 구분한다.
  근거는 `full-load-reversal-gpu2gi-new-uids/`와 `stale-owner-2gi-full-readonly-forensics/`다.

## 기존 CPU 판단 기한 복구 — 2026-10-16

- `ef175ee3`는 기존30초 call context를 후보 열거·재귀 배치·평가·투영·replay로 전달한다.
  owner/controller 잠금은 유지한다. 계산 중 취소되면 부분 decision/kernel/history를 발행하지 않는다.
  유효한 판단을 이미 기록한 뒤 callback에서 취소되면 그 기록은 남기되 history commit·새 제어 시작은 막는다.
- 보존된 마지막 완료 입력을 실제 SDK 이벤트로 재구성한 smoke는441 eligible edge에서1339 replay를
  실제 진입한 뒤30.000623736초에 `context deadline exceeded`로 반환했고 부분 kernel은0이었다.
  미발행 정체 tick의 정확한 입력 재현이나 계산량 최적화로 주장하지 않는다(`artifact://3953`).
- focused3package 및 `make lint test arch-check gopls-check`가 통과했다.
  correctness review의 후보 열거 취소 누락을 수정한 뒤 NoFindings, ponytail review도 NoFindings였다.
  이 시점은 배포·실제 OFF 반영 확인 전이며 전체 FULL 통과가 아니다.

- ef175 실제 배포 이미지 `sha256:6d93714857e25524341e3972b9a2b7fe655e59fc5da19a9e646a3ebe2ae6aa51`에서
  같은 journal을 정상 읽고01:16:22.292506554Z에 기존Rpi revision353849182를 실제 완료 ACK했다.
  GET128/128·8/s는 모두200이었다. 재시작은 새 work-history 비교 기간이며 unknown8건의 완료 근거가 아니다.
- 정확한 이전 Latte의 단 한 번 SIGTERM 후 kubelet의 Terminated/Completed/exit0을 확인했고,
 01:24:02.128492124–01:24:02.128707864Z에 기존 observer가 정확한8건을 `error/InstanceStopped`로 종결했다.
  원래 RequestTimeout 기록은 보존했다. 정상 CLI remove/prepare로 새 Latte를 복구했고 두 Workload의
  접수·세 실행기 Ready·outstanding0·9OFF/Rpi 실제 경로를 확인했다.
  근거는 `latte-exact-physical-boundary-eight-closure/final-eight-closure-and-three-ready-handoff.json`이다.
- 다음 기능 검증은 기존 고정 기간 비교 방식으로 별도 선언한다. 실제 Dashboard에서 알고리즘만
  변경 검토·저장했으며01:34:12.859569246Z의 판단에도 fixed-horizon을 확인했다.
  제어9OFF·100회·128후보·원래 SLO와 부하는 변경하지 않았다.
  `fixed-horizon-dashboard-setting-proof.json`과 실제 화면을 보존했다. 이 선택은 이력 예측 계산량 개선의 증거가 아니다.

## 소유 실행기 장애·격리·복구 — 2026-10-16

- 첫 단위는 ON 이후 다음 Eval 프로세스 시작까지48.29초가 지연돼 안전 조건을 잃었고
  신호를 보내지 않았다. NOT_INJECTED로 보존했으며120건 HTTP200과 정상90초 전력은 장애 검증이 아니다.
- 별도 선언한 단일 독립 controller 단위는02:29:51.436032–02:31:21.436032Z다.
  실제 ON02:29:57.020951975Z→manual map 제거57.190580Z→정확한 Latte에 단 한 번 SIGTERM57.703177Z였다.
  kubelet Terminated/Completed/exit0을 확인했다. 신호 순간 in-flight0이므로 실행 중 요청 중단은 미검증이다.
- 118건 정상 완료·2건 BackendUnavailable 오류였다. 두 오류는 `error/InstanceStopped`로 종결했고,
  이후 각 Workload53건이 Rpi에서 정상 완료됐다. p90/p95 목표 수치와 오류2건을 분리한다.
  whole10 정확90초 에너지는21493.613048035484J이며 비교·절감 주장은 없다.
- 자동 대체4417/f785는 실제 Ready였지만 같은 prepare ID를 오래된 running snapshot으로 다시 실행해
  COMPLETE 뒤 PARTIAL이 기록됐다. physicalized target에서 logical slot을 못 찾아 두 번째 Prepare가nil이었다.
  Node/pool은 정상이었고 validator는 유지한다. 공유 launch의 live terminal identity 우선으로 수정·검증 중이다.
- 최종 실제9OFF·Rpi ACK353935226·접수Ready·outstanding0을 확인했고 새 자동 대체는 건드리지 않았다.
  상세 시각·오류·action/cursor는 `owned-latte-fault-protocol/new-declared-single-controller-20261016T022127Z/integrated-final-report.json`에 둔다.

## 완료 action 재실행 수정·최종 실동작·Dashboard 차단 — 2026-10-16

- `20e5a759`는 동일 action ID·계약의 live terminal receipt를 오래된 running/cancel snapshot보다
  우선한다. 효과 상실 후 기존 reconciliation의 새 identity는 허용한다. guard·저장 계약은 완화하지 않았다.
  과한 native 프로토콜 test fixture는 제거하고 상태 전이 회귀만 남겼다. correctness·ponytail NoFindings,
  focused 회귀와 `make lint test arch-check gopls-check`가 통과했다.
- 이미지 `sha256:9490e8ecf941d3b6c5c54de90cacd17d01da3a4c7e7ba178bba817cd71d8cc80`를 배포한
  03:36:41.636416–03:37:21.636416Z 실제40초에서 ALL9ON 판단4건과 요청4/4 완료를 확인했다.
  route/stop/drain/remove는 각각 시작1·완료1·partial0·중복0이며 이후에도 완료 receipt가 유지됐다.
  이는 일반 제어 smoke이고 결정적 race 재현은 별도 회귀 테스트의 근거다. 전력 비교는 하지 않았다.
- 최종9OFF/fixed·Rpi ACK353984654·접수Ready·outstanding0·owner restart0이다.
  유휴 Latte는 정상 자동 제거됐고 GPU도 없다. 관련 근거·페이지·event/action 참조는
  `terminal-receipt-replacement-smoke-20e5a759-20261016T033504Z/`에 둔다.
- 같은40초를 실제 Dashboard에서 다시 열어 **502 DashboardResponseTooLarge**를 확인했다.
  owner compact4경로는 모두200이고 첫 이력50건은2042133B·nextCursor50이었다.
  `dashboard-latest-full-history-blocker.json`과 실제 화면을 보존했다. Main 참조 복원·조회 연결 후
  화면 대조가 남아 있으며 전체 목표를 완료하지 않았다.

### 운영 API·UX 회고 입력 — 종단 완료 전 후보만 정리

- 부모 세션 Eval/Bash 메타데이터와 실행별 `/tmp` 원본을 함께 확인했다. 빌드·lint·브라우저 조작은 운영 API 후보가 아니다.
- 반복된 실제 공백은 불확실 소유권의 정확한 실행기 종료·종결 절차, 측정 Job의 메모리 제한과 준비 할당의 대응,
  등록 Workload0일 때의 CLI empty-route ACK 판정이다. 기존 격리·설정·조회 경계로 줄일 수 있는지 먼저 본다.
- Eval 턴 사이48초 공백과 임시 스크립트 문법 오류는 단일 독립 controller·기존 emitter 재사용으로 해결했다.
  새 캠페인 엔진·전체 명령 래퍼를 만들 근거는 아니다. 새 운영 기능의 범위는 Dashboard 종단 검증 뒤 확정한다.

## Main compact 배포 후 독립 브라우저 종단 대조 — 2026-10-16

- Main `bb67eb0` 이미지에서 인증 후 정확03:36:41.636416–03:37:21.636416Z를
  실제 페이지0→50→100으로 열어50+50+14건을 확인했다. 기존502는 재발하지 않았다.
  첫 판단의 유지안·선택 목표·실제 구성 근거를 펼쳤고 profile/완료 오버헤드가 복원됐다.
- 3쪽 offset103의 Latte 제거 완료 시각03:37:18.76444766Z·physical4417/f785·원본 expected/actual이
  owner114건 및 실제 Pod 부재와 일치한다. GPU 채택 화면은1.79J·6회차·CPU1/core3/fixed를 표시하며
  loaded를 실행기 Ready로 표시하지 않는다.
- 같은40초의 전력 화면과 API는 complete/whole8896.215690533672J·active196.96873142725255J로 일치했다.
  whole10노드와 activeRpi를 구분하고 원본 시각 없음·수집 시각 사용 caveat를 표시했다. 절감 비교는 아니다.
- 역사 요청2건씩의 p90/p95와 ID·Rpi physical·native finished를 대조했다. 추가 실제 UI 추론은 단1건
  `42cd4ceaa6c72382cea2aec1de22dc82`이며129.103566ms·native batch488로 완료했다.
  자동화 결과 대기 predicate가 맞지 않아 도구가 탭을 종료했지만 재제출하지 않았다.
  API의 유일 요청·finished 원본과 새 브라우저 관측 창으로 완료를 확인했다. 기존40초 비교에 합치지 않는다.
- 04:26:05.708707Z 최종 snapshot은 owner9490/CPU2/1Gi/Go768·PVC8Gi·Dashboard bb67,
  실제9OFF/fixed·두 Workload접수Ready·outstanding0·Rpi488/488/requests[]이다.
- 남은 실제 UI 결함: 제거 기록의 “이 변경 동작 자세히 보기”가 `/transitions/...`에서
  “기록을 확인해야 해요”로 간다. query는 정상 파싱되고 decoded path ID와도 일치한다.
  경로 인코딩 불일치는 아직 추정이며 Main에 정확한 재현 URL·출처를 전달했다. 전체 완료는 보류한다.
  근거: `dashboard-final-e2e-progress.json`, `dashboard-final-*.png`,
  `final-current-readonly-20261016T041406Z/`, `final-post-ui-readonly-20261016T042545Z/`.

## ChillServer 리팩토링 이미지 배포·새 Dashboard E2E — 2026-10-16

- `dad2aec`의 amd64 ChillServer를 build/push하여
  `sha256:fb5348b80d54f74cf4ff1d00d49eebe2d7881740bff1c44567b88c4b6ef08e0f`로 배포했다.
  firstfloor CPU 이미지도 로컬 build·실제 materializer/worker 실행·ORT1.27.0/NumPy2.2.6을 확인했으나
  고정된 측정 이미지·프로파일은 교체하지 않았다.
- Deployment는 이미지 한 필드만 바꿨다. CPU2/1Gi·Go768MiB·SA·PVC8Gi·Dashboard bb67·
  설정 UID/RV353984654·fixed/9OFF를 보존했다. 새 서버 Pod `2e85cc88-0763-4792-86a7-42305f7ee0d4`는
  restart0이며 verified-CA HTTPS `/readyz`와 설정·상태·Workload API가200이었다.
- 07:59:45Z의 시작 단계에서 UID map/실행기 준비 미충족2건은 실패로 남았다. 이후
  07:59:55.581137959–07:59:56.479005085Z의 기존 static routing은 complete였다.
  이력 offset24 원본에 Rpi의 정확한 native ACK·적용 전후 상태·실제 routing이 보존됐고
  Dashboard의 실행 중→완료 및 원본과 일치했다. 다른 제어를 새로 활성화한 실험은 아니다.
- 실제 Dashboard에서 측정25개·채택3개, GPU 측정1.79J/6회차와 loaded 의미,
  fixed-horizon 판단·예상 에너지·실제 Rpi 구성, `/pools`200을 확인했다.
  실제 UI 추론 POST는 단1건이며 `b2b9ffb87a6b7caf49a3a6e4e9a52735`가 완료됐다.
  화면 대기140.30ms·서비스126.417525ms, 같은9개 사건·Rpi UID/container·측정 profile·native batch492/finished를
  owner API와 대조했다. SLO 통계나 에너지 절감의 입증은 아니다.
- 같은08:09:00–08:10:00Z의 화면/API는 complete, 전체10노드15442.435242944202J,
  활성Rpi293.6367253111J였다. 원본 시각 없음·수집 시각 사용 caveat를 유지했다.
  정상 API 조회에서502/504는 관측되지 않았다. 브라우저 대기 predicate 실패로 닫힌 탭은
  재개했으며 추론을 재제출하지 않았다.
- 잔여 UI 결함은 새 `manual-static-routing:353984654` 완료 기록에서도 동일하다.
  `/transitions/manual-static-routing%3A353984654`의 from07:59:00Z/to08:01:00Z,
  offset24·kind action·at07:59:56.479322726Z·startedAt07:59:55.581137959Z를 가진 실제 링크가
  “기록을 확인해야 해요”로 간다. 이력 창의 완료·원본은 정상이며 Main 수정 전 전체 완료는 보류한다.
- 원본: `/tmp/chill-refactor-deploy-dad2aec-before.json`, `...-e2e.json`, `...-server-build.json`,
  `...-firstfloor-build.json`과 실제 브라우저 스크린샷. 최종9OFF·접수Ready·outstanding0·기존Rpi restart0을 확인했다.

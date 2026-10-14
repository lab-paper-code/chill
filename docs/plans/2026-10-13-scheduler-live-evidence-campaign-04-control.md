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

# 하드코딩 정리 이동 인벤토리

상태: 2026-10-30 · **2차 검토(fusion: 패널 3모델+판정) 반영**. 슬라이스 2~5 구현 완료; live 측정 수용 주장이 아니다.

원 조사 go82+harness175+chart133=390행의 의미 묶음을 보존한다. 아래 기존140행은 삭제하지 않고 재분류했고 누락3행을 추가했다. 슬라이스 5 위치는 cutover 후 현재 소스로 갱신했다. 나머지 위치는 조사 당시 줄이며 심볼로 찾는다. 같은 정책의 Go/Chart 행은 중복 조사 행이므로 필드 수가 아니다.

| 분류 | file:line | 현재값 | 이동처/처리 | 새 슬라이스 |
|---|---|---|---|---|
| A | internal/serving/capacity.go:27 | 20 | SchedulerSettings.capacityGridSteps | 3 |
| A | internal/serving/capacity.go:28 | 0.005 | SchedulerSettings.capacityBisectionWidth | 3 |
| A | internal/serving/capacity.go:29 | 100 | SchedulerSettings.capacityTailSamples | 3 |
| A | internal/profiling/run/evidence.go:216 | 0.5초 | ProfilingRun.spec.measurement.durationToleranceSeconds | 2 |
| A | internal/profiling/run/idle_evidence.go:108 | 0.5초 | ProfilingRun.spec.measurement.durationToleranceSeconds | 2 |
| A | internal/serving/kube/catalog.go:360 | 0.95 | BEO.spec.pointPolicy.confidenceLevel 단일 정책; CLI/values/생성자 구 경로 삭제 | 2 |
| A | internal/profiling/campaign/controller/curve_deriver.go:19 | 0.95 | BEO.spec.pointPolicy.confidenceLevel 단일 정책; CLI/values/생성자 구 경로 삭제 | 2 |
| A | internal/serving/kube/catalog_reuse.go:138 | 30초 | profile-reuse annotation.minimumMeasurementSeconds (기본30) | 3 |
| A | internal/serving/kube/catalog_reuse.go:138 | 3회 | profile-reuse annotation.minimumTrials (기본3, ≥2); ProfileReuseCheck와 identity | 3 |
| A | internal/profiling/derivation/policy.go:14; derive.go:67,230; energy_optimization.go:89-95,129-132; internal/profiling/campaign/energy_optimization.go:35-37; internal/profiling/idlebaseline/result.go:128-133; api/v1alpha1/batchenergyoptimization_types.go:28-31; modelloadedidlebaseline_types.go:22-23,56-59 | 최소 3회 | 기존 BEO.trialsPerBatch·idle.pointPolicy.minimumRuns/attempt limit 하한2 종단 지원; 새 minimumRuns 없음 | 2 |
| A | internal/profiling/transitioncost/summary.go:90,279,338; api/v1alpha1/powermodetransitioncost_types.go:57-63 | 최소 5 accepted/방향 | 기존 acceptedCountTarget·attemptLimit 하한2; 방향별 적용, 기본5 유지 | 2 |
| A | api/v1alpha1/dispatchoccupancymeasurement_types.go:16 | 60초 | 기존 durationSeconds 양수, 현재60초 입력 유지 | 2 |
| A | api/v1alpha1/dispatchoccupancymeasurement_types.go:41 | 1.2 | 기존 rateFactor open-loop finite >1, 현재1.2 입력 유지; closed-loop 제외 | 2 |
| A | internal/serving/kube/occupancy.go:722 | seed 0 | DispatchOccupancyMeasurement.spec.seed | 2 |
| C | internal/serving/observation_log_power.go:135 | 앞뒤 1분 | 앞뒤1분 읽기 support 유지; settings/Decision 신설 없음 | 유지 |
| A | cmd/chill-server/options.go:134 | .95, --profile-confidence-level | BEO.spec.pointPolicy.confidenceLevel 단일 정책; CLI/values/생성자 구 경로 삭제 | 2 |
| D | internal/serving/scheduler.go:490 | 10분, bootstrapCapacityWaitNanos | 기존 설정·기록 유지; 새 payload/frozen 필드 없음 | 유지 |
| D | internal/serving/scheduler.go:494 | 300초, scaleDownStabilizationNanos | 기존 설정·기록 유지; 새 payload/frozen 필드 없음 | 유지 |
| D | internal/serving/scheduler.go:498 | 0, replacementDwellNanos | 기존 설정·기록 유지; 새 payload/frozen 필드 없음 | 유지 |
| A-미동결 | cmd/chill-server/options.go:124,132-133 | queue 1024 / source 1초 / profile 1분 | 기존 cluster/server-start에 queueLimit/sourceIntervalNanos/profileIntervalNanos 최소 보강 | 4 |
| C | internal/nodeactuator/jetson/bootorder.go:16-17 | /host/etc/systemd/system, /host/lib/systemd/system | 기존 내부 mount·port·이미지 사용자 패키징 계약 유지 | 유지 |
| C | internal/operator/evidencestore/deployment.go:18,91 | /data, fsGroup 1000 | 기존 내부 mount·port·이미지 사용자 패키징 계약 유지 | 유지 |
| C | internal/component/names.go:17,20 | 9000, /var/run/chill/evidence | 기존 내부 mount·port·이미지 사용자 패키징 계약 유지 | 유지 |
| C | internal/serving/kube/native_service.go:40,117,141,191; runner.go:102; operations.go:431,963 | 8080 | 기존 내부 mount·port·이미지 사용자 패키징 계약 유지 | 유지 |
| C | internal/profiling/profiler/controller/execution_job_spec.go:349; internal/profiling/transitioncost/controller/attempt_job.go:321 | UID/GID/FSGroup 65532 | 기존 내부 mount·port·이미지 사용자 패키징 계약 유지 | 유지 |
| C | internal/nodeactuator/jetson/nvpmodel.go:39; internal/nodeactuator/nvidia/clock.go:24; cmd/node-agent/options.go:115 | 1초~5분 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | internal/nodeactuator/policy.go:55,58; internal/profiling/profiler/execution_policy_validation.go:82 | 86400초 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | internal/profiling/transitioncost/derive.go:152; controller/preparation_record.go:226; preparer.go:276 | 3600초 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | internal/serving/httpapi/handler.go:142,563,571; cmd/chill-server/options.go:168; api/v1alpha1/workload_types.go:169 | 129초 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | internal/firstfloor/config.go:76,91,108; server.go:25; internal/modelcatalog/preprocessor.go:115 | 256자 / queue 최대 2^20 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | internal/firstfloor/ipc.go:65,69,300,324; server.go:65,68 | 4 bytes | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | internal/metricobserver/edgemetrics/source.go:200; internal/nodeactuator/nvidia/clock.go:167 | 4096 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | internal/serving/observation_log.go:423; observation_log_raw.go:227; kube/observer.go:932 | 4096 / 8192 / 64KiB | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | internal/serving/kube/operations.go:545; internal/profiling/transitioncost/kube/native.go:137,142 | 64KiB / 4MiB | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | internal/metricobserver/observer.go:52,101; internal/profiling/profiler/execution_policy.go:74; internal/profiling/transitioncost/controller/preparer.go:337 | +2 / +3 events | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | internal/serving/kube/occupancy.go:174 | 1e−12 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | api/v1alpha1/batchenergyoptimization_types.go:115; modelloadedidlebaseline_types.go:89; internal/serving/httpapi/measurements.go:103 | name 200 / 243 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | internal/profiling/profiler/kube/metric_source_resolver.go:227; internal/nodeprobe/probe.go:188; internal/nodeactuator/policy.go:39 | port 65535 / name 63 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | load-generator/src/chill_loadgen/cli.py:20-21,104,142,232 | DELAY_TOLERANCE_NS=10,000,000 | 표시/대기 방법론 유지; 새 CLI·poll knob 없음 | 유지 |
| A | hack/e5/e5.py:235-249,1425-1444 | canonical workload 3개; seeds 101/202/303; poisson | binding.config.workloads / seeds / arrivals; workload별 seed·arrivals 동결, producer가 선택값 소비 | 5 |
| A | hack/e5/e5.py:224-233,280-315,1425-1445 | 600,1800,900,600×3,900,600×2,3600,900초; α=0,.3,.5,.7,.9,1.2 | binding.config.stages; 기존 실행 함수에서 생략 기본값 생성, 선택 part ID/seconds/alpha 보존 | 5 |
| A | hack/e5/e5.py:107-151,996-1033; hack/e5/demo.py:125-146 | G0 α=1.2; 1개 G-check; 120초 | binding.config.g0Evidence.durationSeconds/alpha; one-shot, stageCount 없음 | 5 |
| A | hack/e5/e5.py:139-151,1021-1033 | CPU peak/limit<1; PLC percentile=95 | binding.config.g0Evidence.maximumCPUFraction / workload SLO percentile; 선택 분위수는 raw elapsedNanos에서 계산 | 5 |
| A | hack/e5/e5.py:261,386-417,1321 | idleNanosPerRequest≤1e6 | binding.config.inputs.maximumIdleNanosPerRequest → closure eligibility 판정 | 5 |
| A | hack/e5/e5.py:1109-1118 | closures 정확히 25개 | 선언 target 목록 길이와 실제 모집단 비교; expectedClosureCount 없음 | 5 |
| A | hack/e5/e5.py:259-266,1302-1304; hack/e5/e5-wire.py:57-61; hack/e5/demo.py:488 | burstLowSeconds<900; setup+최장 stage+drain+flush≤4200초 | binding.config.burstLowMaximumSeconds / collection.maximumDurationSeconds; 선택 schedule row로 검사 | 5 |
| A | hack/e5/e5.py:1456,2828-2832 | full=11700, static=10200, factor=2100×N초 | binding schedule/선택 seeds에서 full/static/factor·check 합계 유도 | 5 |
| C | hack/e5/e5.py:1369,1470,1609,1655,1737,1977,2026 | 상태 poll 1초; barrier/select .2초 | 표시/대기 방법론 유지; 새 CLI·poll knob 없음 | 유지 |
| C | hack/e5/e5.py:1769-1770,1817,1824 | sampler/log follower 종료 대기 15초 | 표시/대기 방법론 유지; 새 CLI·poll knob 없음 | 유지 |
| A | hack/e5/e5.py:270-274,2182-2183 | maxActuationSeconds=900; subprocess 960초 | binding.config.timeboxes.maxActuationSeconds / nativeCallerSeconds → native release/caller | 5 |
| C | hack/e5/d2/conduct_before.py:56,59,65,869 | producer barrier 300초; .1초 poll | 표시/대기 방법론 유지; 새 CLI·poll knob 없음 | 유지 |
| A | hack/e5/d2/conduct_before.py:174-176; hack/e5/e5.py:271 | future allowance 15초; observed-start≤915초 | binding.config.timeboxes.nativeFutureAllowanceSeconds / maxActuationSeconds (freshness는 합계 유도) | 5 |
| D | hack/e5/d2/conduct_before.py:246,286,425,538,954,1106 | api=129초; operations=1800초; native state=10초 | 기존 입력·안전 예산 유지; 경로별 timeout/poll 통합 금지 | 유지 |
| D | hack/e5/d2/conduct_before.py:315,347,332,458,635,712,738 | publication/drain/ACK/Ready=194초 | 기존 입력·안전 예산 유지; 경로별 timeout/poll 통합 금지 | 유지 |
| C | hack/e5/d2/conduct_before.py:333,352,457,628,634,713,739 | poll=5초 또는 2초 | 표시/대기 방법론 유지; 새 CLI·poll knob 없음 | 유지 |
| C | hack/e5/d2/conduct_before.py:794-805 | delete request=75초; owned deletion wait=60초; poll=1초 | 표시/대기 방법론 유지; 새 CLI·poll knob 없음 | 유지 |
| D | hack/e5/d2/native_mode_request.py:29,60-62,97 | command timeout=60초; poll=5초 | 기존 안전 예산 유지; actuation은 위 A행의 binding 정책 소비(기본900), 경로별 timeout/poll 통합 없음 | 유지 |
| 제외 | 삭제 전 hack/e5/d2/sample_cgroups.py:27,89,105-106 | crictl=10초; inventory=15초; thermal=5초; cgroup=1초 | CHILL 내부 수집은 cutover에서 제외 확정; 외부 producer client 수집에 남은 방법론 C는 유지 | 1 |
| C | hack/e5/e5-producer.py:69,115-116 | GO/종료 marker poll=.05초 | 표시/대기 방법론 유지; 새 CLI·poll knob 없음 | 유지 |
| D | hack/e5/e5-wire.py:158,190,215 | poll=1초; GOMAXPROCS=6 | 기존 입력·안전 예산 유지; 경로별 timeout/poll 통합 금지 | 유지 |
| 제외 | 삭제 전 hack/e5/e5-archive.py:38 | host-reader command timeout=20초 | host-reader 수집 삭제 확인, API collector로 대체 | 1 |
| 제외 | 삭제 전 hack/e5/e5.py:822 | host-usage command timeout=20초 | CHILL host-usage 수집 삭제 확인 | 1 |
| 제외 | 삭제 전 hack/e5/e5-archive.py:361-365 | 에너지 구간 양쪽 ±60초 | 옛 로컬 archive 에너지 구간 수집 삭제 확인; API support는 아래 C | 1 |
| C | hack/e5/e5-archive.py:365-380 | drained+60초 support; 시작 hour−1/끝 hour+1 범위 | API collector에도 잔존하는 증거 support 방법론; 설정화하지 않음(슬라이스1 제외 후보에서 유지로 재분류) | 유지 |
| A-미동결 | load-generator/src/chill_loadgen/cli.py:286; hack/e5/e5.py:235-249,1690 | --arrivals=fixed; E5 선택 poisson | 기존 arrivals 선택을 workload별 binding에 동결, producer segment도 소비; delay CLI 없음 | 5 |
| A-미동결 | load-generator/src/chill_loadgen/cli.py:289; hack/e5/e5.py:235-249,1690 | --seed=0; E5 선택101/202/303 | 기존 seed 선택을 workload별 binding에 동결 | 5 |
| A-미동결 | load-generator/src/chill_loadgen/cli.py:290; hack/e5/e5.py:1335-1344,1690 | --workers=8 | 기존 필수 workload workers 선택을 동결; E5 필수값을 CLI 기본8로 대체하지 않음 | 5 |
| A-미동결 | load-generator/src/chill_loadgen/cli.py:291-295,321-325; hack/e5/e5.py:249,1342-1344,1690-1691 | delivery=.01초; timeout fallback30초 또는 lifetime+delivery(+delivery) | 기존 필수 delivery/socket·선택 lifetime를 workload별 동결; absent lifetime=None, CLI flag 미전달 유지 | 5 |
| A-미동결 | hack/e5/e5.py:50-69,275 | producerResources cpu2/memory1Gi; shards MNV3/PLC1/Smol1 | 기존 실험 config 기본값 정상화 → binding.config → 생성기 envelope·분할 | 5 |
| A-미동결 | hack/e5/e5.py:277,989-994,1418-1419 | poolShares MNV.45/PLC.1/Smol.45 | 기존 실험 config 기본값 정상화 → binding.config → workload별 부하 배분 | 5 |
| A-미동결 | hack/e5/e5.py:257,318-321 | failureSelectSnapshotTTLSeconds=15초 | 기존 실험 config 기본값 정상화 → binding.config → 장애 대상 freshness | 5 |
| A-미동결 | hack/e5/e5.py:268,2151 | finishResetWaitSeconds=600초 | 기존 실험 config 기본값 정상화 → binding.config → post-off zero-state 대기 | 5 |
| A-미동결 | hack/e5/e5.py:258,1079-1083; hack/e5/demo.py:455,484 | tokenSafetyMarginSeconds=60초 | 기존 실험 config 기본값 정상화 → binding.config → 인증 expiry 사전 점검 여유 | 5 |
| D | hack/e5/auth.py:27 | expiry probe timeout=10초 | 기존 입력·안전 예산 유지; 경로별 timeout/poll 통합 금지 | 유지 |
| A-미동결 | hack/e5/e5.py:268-269,1591-1596 | powerDownSeconds=1800초; apiSeconds fallback30초 | 기존 실험 config 기본값 정상화 → binding.config → reset HTTP 총 대기; 필수 경로129초 유지 | 5 |
| A-미동결 | hack/e5/e5.py:269,2084 | factorAckWaitSeconds=120초 | 기존 실험 config 기본값 정상화 → binding.config → 복원 prepare ACK 관측 대기 | 5 |
| A-미동결 | hack/e5/e5-archive.py:158-170; hack/e5/demo.py:478 | energyCoverage maxGapFraction=.01,maxSingleGapSeconds=30 | 기존 실험 config 기본값 정상화 → binding.config.collection.energyCoverage → 에너지 row 판정 | 5 |
| A-미동결 | hack/e5/d2/analyze_before.py:73-84; hack/e5/demo.py:471-474 | generatorQuality lateness50ms,maxExceedingFraction=.01 | 기존 실험 config 기본값 정상화 → binding.config.generatorQuality → 생성기 품질 판정 | 5 |
| 제외 | 삭제 전 hack/e5/e5.py:824; hack/e5/e5-archive.py:46-52 | /usr/bin/crictl; namespace chill-system; serving-entrypoint | CHILL host collector 삭제 확인; 외부 client sampler는 별도 유지 | 1 |
| B | hack/e5/d2/conduct_before.py:35,780-785,808 | chill-system; SA token=/var/run/secrets/kubernetes.io/serviceaccount/token; CA=/ca/tls.crt; dataset=/data | CHILL_EXPERIMENT_CONFIG.d2.namespace → binding.config.namespace → helper Job/delete/kubectl; token/CA/data 경로 C 유지 | 5 |
| 제외 | 삭제 전 hack/e5/d2/sample_cgroups.py:69-72 | chill-system/kubeedge; serving-entrypoint/edgemesh-agent/firstfloor/load; prefix d2-before- | E5 원격 역할 선택에서 CHILL 내부 수집 제외 확인; client 역할만 유지 | 1 |
| B | hack/e5/d2/native_mode_request.py:63-68; hack/e5/e5.py:2182-2183 | -n chill-system | CHILL_EXPERIMENT_CONFIG.d2.namespace → binding.config.namespace → native release.namespace | 5 |
| C | load-generator/src/chill_loadgen/http_client.py:21-23 | DNS total253,label63(정규식 내부61) | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | load-generator/src/chill_loadgen/http_client.py:50,95,100 | control chars<32/127; minimum1e-9초; .9f | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | load-generator/src/chill_loadgen/http_client.py:136,145-152 | BYTES,base64,JSON,HTTPS,POST,/api/v1/workloads/{id}/infer,X-Chill-* | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | load-generator/src/chill_loadgen/cli.py:47,52,113-115,184; load-generator/src/chill_loadgen/schedule.py:65 | 1e9 ns/s;1e6 ns/ms | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | load-generator/src/chill_loadgen/cli.py:61,180,225 | item_count=1 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | load-generator/src/chill_loadgen/schedule.py:19-22,30,52,55 | offset≥0,item_count/worker≥1,tolerance≥0,previous=-1 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | hack/e5/auth.py:36-38 | JWT 3부분; base64 padding mod4 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | hack/e5/auth.py:49-56; hack/e5/d2/analyze_before.py:446 | API /api/v1/scheduler/state; HTTP200; X-Token-Expires-At | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | hack/e5/e5.py:87,95,112,842,1231 | Git SHA40hex; numeric tolerance1e-9/1e-12 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | hack/e5/e5.py:890,942,1294; hack/e5/demo.py:525 | e5-binding.v1,demo-e5-binding.v1,e5-private-*.v1 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| 제외 | 삭제 전 hack/e5/e5.py:931; hack/e5/e5-archive.py:147 | stream read chunk=1024×1024 | 옛 host-stream 소비자 삭제 확인; 현재 `_ref`:69-74의 digest chunk C는 별개 | 1 |
| 제외 | 삭제 전 hack/e5/e5-archive.py:96,133-134,182 | permission0600; metadata≤1MiB; nofollow | 옛 host archive metadata/nofollow 소비자 삭제 확인; 현재 `_open` 파일 보호는 유지 | 1 |
| 제외 | 삭제 전 hack/e5/e5-archive.py:55,223; hack/e5/e5.py:828 | v1alpha5; serving/observations.v1alpha5/ | 옛 host archive 경로 소비자 삭제 확인; 저장 version 계약 자체는 유지 | 1 |
| C | hack/e5/e5-archive.py:171-185; hack/measure/phase-a/measurement-generalization-20261024/cost-budget/arithmetic_check.py:18-19 | trapezoid .5 또는 /2 | API collector의 실제 표본 적분에도 잔존하는 수식 C; 역사 arithmetic script는 범위 밖(슬라이스1 제외 후보에서 관련 C로 재분류) | 관련 |
| C | hack/e5/e5-raw.py:22-34; hack/e5/d2/analyze_before.py:39-44 | RFC3339 1~9자리; epoch1970;86400s/day;1e9 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | hack/e5/d2/conduct_before.py:539-540,595 | native state64MiB; DecisionID≤256 ASCII | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| 제외 | 삭제 전 hack/e5/d2/sample_cgroups.py:44-56,80,102 | /sys/fs/cgroup*,/proc/{pid}/cgroup,/sys/class/thermal; usec×1000;v1/v2 | E5 원격 프로그램에서 CHILL 내부 역할·thermal 제외 확인; 외부 client cgroup 방법론 유지 | 1 |
| C | hack/e5/d2/analyze_before.py:52-53; hack/e5/e5.py:2144,2155,2167 | ceil(n×q)-1;rank=ceil(offered×pct/100) | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| A | charts/chill/values.yaml:503; charts/chill/templates/serving_entrypoint.yaml:127 | profileConfidenceLevel=0.95 | BEO.spec.pointPolicy.confidenceLevel 단일 정책; CLI/values/생성자 구 경로 삭제 | 2 |
| D | charts/chill/values.yaml:495; charts/chill/templates/serving_entrypoint.yaml:112 | requestTimeout=129s | 기존 설정·기록 유지; 새 payload/frozen 필드 없음 | 유지 |
| D | charts/chill/values.yaml:498; charts/chill/templates/serving_entrypoint.yaml:124 | drainTimeout=164s | 기존 설정·기록 유지; 새 payload/frozen 필드 없음 | 유지 |
| A-미동결 | charts/chill/values.yaml:501; charts/chill/values-operating.yaml:586; charts/chill/templates/serving_entrypoint.yaml:125 | sourceInterval=1s; operating=2s | 기존 cluster/server-start에 queueLimit/sourceIntervalNanos/profileIntervalNanos 최소 보강 | 4 |
| A-미동결 | charts/chill/values.yaml:502; charts/chill/templates/serving_entrypoint.yaml:126 | profileInterval=1m | 기존 cluster/server-start에 queueLimit/sourceIntervalNanos/profileIntervalNanos 최소 보강 | 4 |
| D | charts/chill/values.yaml:514; charts/chill/templates/serving_entrypoint.yaml:120 | directoryInterval=1m | 기존 설정·기록 유지; 새 payload/frozen 필드 없음 | 유지 |
| D | charts/chill/values.yaml:516; charts/chill/templates/serving_entrypoint.yaml:121-122 | controlComponents=[operator, profiler, evidence-store, serving-entrypoint, node-agent, node-agent-actuation, dashboard, operator-console, metric-observer, workload-observer, transition-observer] | 기존 설정·기록 유지; 새 payload/frozen 필드 없음 | 유지 |
| D | charts/chill/values.yaml:815 | sampleInterval=1s | 기존 설정·기록 유지; 새 payload/frozen 필드 없음 | 유지 |
| D | charts/chill/values.yaml:816 | batchInterval=10s | 기존 설정·기록 유지; 새 payload/frozen 필드 없음 | 유지 |
| D | charts/chill/values.yaml:106; charts/chill/templates/profiler_config.yaml:39 | transitionObserver.activeDeadlineOverheadSeconds=180 | 기존 설정·기록 유지; 새 payload/frozen 필드 없음 | 유지 |
| D | charts/chill/values.yaml:18 | region=us-east-1 | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:33,69,429,460,559,567,931 | daclab/chill-{operator,profiler,console,serving-entrypoint,load-generator,dashboard,node-agent} | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:619 | registry.k8s.io/kubectl | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:39,75 | 1ba2121f.dacs.io; profiler.edge.dacs.io | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:653-795 | Jetson 4종, OrangePi 16Gi, RPi5 8Gi, LattePanda 8Gi, RTX2080Ti 64Gi의 label/architecture/memory/accelerator·mode ID·CPU mask | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:290-423 | xavier/nano/orin/rtx2080ti DeviceClass·mode·actuator 결합 | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:807-808 | /host; /etc/chill/node-agent/signatures.yaml | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:813-814 | /etc/chill/resources-ca/tls.crt; /var/run/secrets/kubernetes.io/serviceaccount/token | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:822-823 | serviceaccount/token; serviceaccount/ca.crt | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:825-838 | proc/device-tree/{model,compatible}; sys/firmware/devicetree/base/*; etc/nv_tegra_release; sys/{devices/virtual/dmi/id,class/dmi/id}/{sys_vendor,product_name} | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:840-929 | NVIDIA Jetson, Raspberry Pi 5, Orange Pi 5 Plus, LattePanda 3 Delta, Gigabyte Z370/Z390 signature→fact 매핑 | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:941-950 | /proc; /sys; /etc | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:980-983,985-1020 | aarch64 loader·library path·/usr/sbin/nvpmodel; /etc/nvpmodel[.conf]; /var/lib/nvpmodel; /sys | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:1030,1033-1036,1039-1053 | runtimeClassName=nvidia; x86_64 loader; /usr/bin/nvidia-smi; /lib/x86_64-linux-gnu; /usr/lib/x86_64-linux-gnu | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:952-954,1067-1070 | node-role.kubernetes.io/edge:NoSchedule | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| C | charts/chill/templates/serving_entrypoint.yaml:7,101,104; charts/chill/templates/_helpers.tpl:210 | svc.cluster.local; evidence store :9000 | 기존 패키징·예제·산출물 계약 유지 | 유지 |
| C | charts/chill/templates/serving_entrypoint.yaml:89-90,213,216,218 | /etc/chill/tls/{tls.crt,tls.key}; /data; /tmp | 기존 패키징·예제·산출물 계약 유지 | 유지 |
| C | charts/chill/templates/dashboard.yaml:61,81,83,86,102 | /etc/chill/ca/ca.crt; /app/.next/cache; /tmp; /etc/chill/ca | 기존 패키징·예제·산출물 계약 유지 | 유지 |
| C | charts/chill/templates/console.yaml:212 | /var/run/chill/evidence | 기존 패키징·예제·산출물 계약 유지 | 유지 |
| C | config/samples/profiling/edge_v1alpha1_batchenergyoptimization.yaml:5,10,14; config/samples/profiling/edge_v1alpha1_modelloadedidlebaseline.yaml:5,10,14; config/samples/profiling/edge_v1alpha1_powermodetransitioncost.yaml:5,7-8,14; config/samples/manual/edge_v1alpha1_deviceclass.yaml:4-7 | chill-system; example-node; example-device-class; example-transition-*; example-serving | 기존 패키징·예제·산출물 계약 유지 | 유지 |
| B | config/samples/manual/edge_v1alpha1_deviceclass_nvidia.yaml:1-12; config/samples/profiling/edge_v1alpha1_powermodetransitioncost_nvidia.yaml:9-19; config/samples/README.md:85-110 | 이전 genesis2/runner/실제 DeviceClass | 환경 JSON samples["<기존 상대 파일 경로>"]에 원본 완전 문서 이동 완료; 기존 jq 선택→kubectl apply 절차. 정적 sample은 example 환경 | 5 |
| B | config/samples/catalog/README.md; config/samples/README.md:26-46,113-137 | 이전155.230.35.213:5000/chill/* | 환경 JSON samples["<기존 상대 파일 경로>"]의 catalog8개와 runtime declaration 전체 이동 완료. HTTPS 등록 전 jq 명시 선택; runtime image+inspection 함께 이동, 정적 declaration 제거 | 5 |
| C | config/samples/catalog/edge_v1alpha1_modelspec.yaml:15,22,29; config/samples/catalog/edge_v1alpha1_modelspec_plc.yaml:15,22; config/samples/catalog/edge_v1alpha1_modelspec_smolvlm2.yaml:15-17,24-26,33-35,42-44; config/samples/catalog/edge_v1alpha1_preprocessor_mobilenet-imagenet.yaml:19,24,29; config/samples/catalog/edge_v1alpha1_preprocessor_plc-extruder-anomaly.yaml:20,25; config/samples/catalog/edge_v1alpha1_preprocessor_smolvlm2-vqa.yaml:21,26,31,36; config/samples/catalog/edge_v1alpha1_dataset.yaml:10; config/samples/catalog/edge_v1alpha1_dataset_plc.yaml:10 | /artifact/*.onnx; *.rknn; mobilenet-{nchw,nhwc}; plc-extruder-anomaly; smolvlm2-vqa[-1tile]; dataset 디렉터리 | 기존 패키징·예제·산출물 계약 유지 | 유지 |
| C | config/samples/firstfloor/echo.json:6,13 | runtime/firstfloor/runner.py; runtime/firstfloor/testdata/echo; config/samples/firstfloor/echo-profile.json | 기존 패키징·예제·산출물 계약 유지 | 유지 |
| D | charts/chill/values.yaml:487 | observationStorage.directory=/data | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| D | charts/chill/values.yaml:1038 | clocks=[1500,1200,900] MHz | 기존 해당 Helm values 키 유지; 기본 profile 비우지 않음 | 유지 |
| C | charts/chill/templates/_validation.tpl:16,25,39,53,56,97,123,160,163,178 | positive≥1; TCP port≤65535 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | charts/chill/templates/_helpers.tpl:2,7,11,13,32,58,62,66,121,125,129,133,167,187,199,215 | trunc 63 | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | charts/chill/templates/profiler_config.yaml:4-5 | retention∈[1,604800] seconds | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | charts/chill/templates/predelete_cleanup.yaml:12-14,25-27,55-57,76-78,88,101-102 | pre-delete; hook weight -20/-10; before-hook-creation,hook-succeeded; restart Never; ignore-not-found/wait=true | 고정 계약 유지; 관련 변경에서 필요한 의미 이름만. 자명한 수식·sentinel·enum 기계 치환 금지 | 관련 |
| C | cmd/chill-server/live.go:64-80 | 1초 | resources 저장도 구동; 증거 API 고정1초 수집 방법론 유지 | 유지 |
| B | hack/e5/e5.py:254-256,1478,1657,1682,2057; hack/e5/e5-wire.py:44,58,108; hack/e5/demo.py:334 | 고정 namespace | CHILL_EXPERIMENT_CONFIG.d2.namespace → binding.config.namespace; producer namespace·operator delete URI·외부 client sampler 포함 | 5 |
| A-미동결 | hack/e5/e5.py:1414-1416; hack/e5/demo.py:459-470 | failureInjectionRunbook example 덮어쓰기 | 입력 우선·누락 때만 example 기본 → binding 보존 완료 | 5 |

## 집계와 원 조사 대응

이 표는 143행: A 26, C 53, D 32, A-미동결 17, 제외 10, B 5. 이동/기록 대상은 **48행**(A·A-미동결·B 중 유지/제외 아님)이다. C는 독립 선행 슬라이스가 아니다. 슬라이스별 표 행: 3=5, 2=11, 유지=54, 4=3, 관련=31, 5=29, 1=10.

슬라이스 5 cutover 재산정(2026-10-30): **기존 29행 유지, 새 제외 0행**, 모두 구현했다. 선행 슬라이스1의 CHILL 우회 수집12개 후보 중 **10행은 제외 유지, 2행은 잔존 C로 재분류**했다. e5-archive의 host-reader/SSH·기존 ±60초 로컬 archive 수집, e5 host-usage·crictl 경로는 증거 API로 대체됐다. 반면 API collector의 drained+60초/hour support와 trapezoid 적분은 현재도 실행하므로 C 유지다. `sample_cgroups.py` 전체 삭제는 하지 않는다. E5가 원격 역할을 외부 producer client만 남기고 thermal 구간을 제거하므로 그 파일의 잔존 cgroup/crictl은 CHILL 내부 증거가 아닌 외부 client 방법론이다. 기존 host-stream chunk 소비자는 삭제됐지만 새 `_ref`의 파일 digest chunk 같은 C를 설정화하지 않는다. 역사 경로/상수도 건드리지 않았다. 원143행과5의29행은 보존한다. 현재 하네스 실행 경로는 측정 CR을 생성하지 않아 CI/tolerance/occupancy seed 새 전달 대상은 없다. 제외 행의 위치는 삭제 전 조사 좌표다.

원390행 대응은 원 이동140행 + 원 유지D79/C23 + 원 재분류16 + 원 범위제외122 + 열린?10 =390이다. 원 재분류16은 C13/D3으로 유지하며 capacity PCG 표현은 **고정한 두 번째 seed word**로 정정한다. 기존140행의 새 분류와 추가3행을 위에 분리해 제시했으므로 원 조사 총량에 신규 발견을 소급 합치지 않는다. 원 유지/재분류를 포함한 cutover 후 현재 범위 집계는 A 26, C 89, D 114, A-미동결 17, 제외 10, B 5; 기존 범위제외122와 열린?10은 별도다.

원 C 재분류13: derive 고정p99, observer 지연 표시, passport 분위수, live 분위수/창/발행, PCG 두 번째 seed word, serving/cmd 진단 capture, E5 분별/열 표시, analyze 요약/issuance이다. 원 D 재분류3: node read-back cadence, Jetson/NVIDIA command safety envelope다. 이름 없는 수식·sentinel·Helm enum을 일괄 상수화하지 않는다.

하한2의 나머지 종단은 transitioncost/summary.go:90,279,338과 위 API/derivation 행을 함께 처리한다. 비영분산 active2/idle2의 유도→저장→재로딩→serving이 끝나야 완료다. 운영 overlay는 기존 해당 Helm values 키 하나만 사용한다. 이미 설정된 실제 endpoint도 그 키를 유지하며 환경 JSON과 선택지로 쓰지 않는다.

범위 제외는 삭제 승인이 아니다. demo.py offline import 회귀는 유지한다. 역사 script와 numbers.py 비활성 경로는 제외한다. 상세 소유권·소비자·기본값 증명·저장 계약은 [설계](../design/serving/configuration-hardcoding.md), 시작 조건은 [계획](2026-10-30-configuration-hardcoding.md)을 따른다.

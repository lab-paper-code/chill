# 하드코딩 정리 이동 인벤토리

2026-10-30 문서 기준. 조사 원본 go.md 82행 + harness.md 175행 + chart.md 133행 = **390행**. 토큰 수가 아니라 의미 묶음 행 수다. 한 행의 여러 위치는 같은 계약이며, 범위 밖 위치가 섞였으면 현재 E5 호출 부분만 적용한다. 원본 위치는 조사 당시 줄 번호다. 구현 직전 심볼로 다시 찾는다.

이 표의 A-미동결은 이동이 아니라 **기존 소유자 유지 + 기록 추가**도 포함한다. 슬라이스 4가 이에 해당한다. D/C는 행을 재기재하지 않는다. config/samples는 사용자 요청의 환경값 제거에 한해 포함; Dashboard 구현·개발 도구·역사 측정 스크립트는 제외한다.

| 분류 | file:line | 현재값 | 이동처/처리 | 슬라이스 |
|---|---|---|---|---|
| A | internal/serving/capacity.go:27 | 20 | SchedulerSettings.capacityGridSteps | 3 |
| A | internal/serving/capacity.go:28 | 0.005 | SchedulerSettings.capacityBisectionWidth | 3 |
| A | internal/serving/capacity.go:29 | 100 | SchedulerSettings.capacityTailSamples | 3 |
| A | internal/profiling/run/evidence.go:216 | 0.5초 | ProfilingRun.spec.measurement.durationToleranceSeconds | 5 |
| A | internal/profiling/run/idle_evidence.go:108 | 0.5초 | ProfilingRun.spec.measurement.durationToleranceSeconds | 5 |
| A | internal/serving/kube/catalog.go:360 | 0.95 | SchedulerSettings.energyConfidenceLevel | 3 |
| A | internal/profiling/campaign/controller/curve_deriver.go:19 | 0.95 | BatchEnergyOptimization.spec.pointPolicy.confidenceLevel | 5 |
| A | internal/serving/kube/catalog_reuse.go:138 | 30초 | SchedulerSettings.reuseMeasurementDurationNanos | 3 |
| A | internal/serving/kube/catalog_reuse.go:138 | 3회 | SchedulerSettings.reuseMinimumTrials | 3 |
| A | internal/profiling/derivation/policy.go:14; derive.go:67; energy_optimization.go:130; internal/profiling/idlebaseline/result.go:132 | 최소 3회 | BEO.spec.pointPolicy.minimumRuns; idle 기존 pointPolicy 재사용 | 5 |
| A | internal/profiling/transitioncost/summary.go:90,279,338 | 최소 5 accepted/방향 | 기존 PowerModeTransitionCost.spec.acceptedCountTarget; 5 floor 제거 | 5 |
| A | api/v1alpha1/dispatchoccupancymeasurement_types.go:16 | 60초 | DispatchOccupancyMeasurement.spec.minimumDurationSeconds | 5 |
| A | api/v1alpha1/dispatchoccupancymeasurement_types.go:41 | 1.2 | DispatchOccupancyMeasurement.spec.minimumRateFactor | 5 |
| A | internal/serving/kube/occupancy.go:722 | seed 0 | DispatchOccupancyMeasurement.spec.seed | 5 |
| A | internal/serving/observation_log_power.go:135 | 앞뒤 1분 | SchedulerSettings.powerSupportWindowNanos | 3 |
| A-미동결 | cmd/chill-server/options.go:134 | .95, --profile-confidence-level | SchedulerSettings.profileConfidenceLevel; CLI/values 구 경로 제거 | 3 |
| A-미동결 | internal/serving/scheduler.go:490 | 10분, bootstrapCapacityWaitNanos | 기존 settings 유지; 초기 settings snapshot + 변경 행 기록 | 4 |
| A-미동결 | internal/serving/scheduler.go:494 | 300초, scaleDownStabilizationNanos | 기존 settings 유지; 초기 settings snapshot + 변경 행 기록 | 4 |
| A-미동결 | internal/serving/scheduler.go:498 | 0, replacementDwellNanos | 기존 settings 유지; 초기 settings snapshot + 변경 행 기록 | 4 |
| A-미동결 | cmd/chill-server/options.go:124,132-133 | queue 1024 / source 1초 / profile 1분 | 기존 flags/values 유지; scheduler.startup 행 | 4 |
| B | internal/nodeactuator/jetson/bootorder.go:16-17 | /host/etc/systemd/system, /host/lib/systemd/system | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | internal/operator/evidencestore/deployment.go:18,91 | /data, fsGroup 1000 | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | internal/component/names.go:17,20 | 9000, /var/run/chill/evidence | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | internal/serving/kube/native_service.go:40,117,141,191; runner.go:102; operations.go:431,963 | 8080 | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | internal/profiling/profiler/controller/execution_job_spec.go:349; internal/profiling/transitioncost/controller/attempt_job.go:321 | UID/GID/FSGroup 65532 | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| C-이름없음 | internal/nodeactuator/jetson/nvpmodel.go:39; internal/nodeactuator/nvidia/clock.go:24; cmd/node-agent/options.go:115 | 1초~5분 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | internal/nodeactuator/policy.go:55,58; internal/profiling/profiler/execution_policy_validation.go:82 | 86400초 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | internal/profiling/transitioncost/derive.go:152; controller/preparation_record.go:226; preparer.go:276 | 3600초 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | internal/serving/httpapi/handler.go:142,563,571; cmd/chill-server/options.go:168; api/v1alpha1/workload_types.go:169 | 129초 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | internal/firstfloor/config.go:76,91,108; server.go:25; internal/modelcatalog/preprocessor.go:115 | 256자 / queue 최대 2^20 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | internal/firstfloor/ipc.go:65,69,300,324; server.go:65,68 | 4 bytes | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | internal/metricobserver/edgemetrics/source.go:200; internal/nodeactuator/nvidia/clock.go:167 | 4096 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | internal/serving/observation_log.go:423; observation_log_raw.go:227; kube/observer.go:932 | 4096 / 8192 / 64KiB | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | internal/serving/kube/operations.go:545; internal/profiling/transitioncost/kube/native.go:137,142 | 64KiB / 4MiB | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | internal/metricobserver/observer.go:52,101; internal/profiling/profiler/execution_policy.go:74; internal/profiling/transitioncost/controller/preparer.go:337 | +2 / +3 events | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | internal/serving/kube/occupancy.go:174 | 1e−12 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | api/v1alpha1/batchenergyoptimization_types.go:115; modelloadedidlebaseline_types.go:89; internal/serving/httpapi/measurements.go:103 | name 200 / 243 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | internal/profiling/profiler/kube/metric_source_resolver.go:227; internal/nodeprobe/probe.go:188; internal/nodeactuator/policy.go:39 | port 65535 / name 63 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| A | load-generator/src/chill_loadgen/cli.py:20-21,104,142,232 | DELAY_TOLERANCE_NS=10,000,000 | 기존 loadgen CLI 유지; delayToleranceNanos CLI 추가(10ms); E5 binding.config.workloads에 실제 arrivals/seed/workers/budget 기록 | 6 |
| A | hack/e5/e5.py:807-808,1268-1269 | canonical workload 3개; seeds 101/202/303; poisson | binding.config.workloads / seeds / arrivals | 6 |
| A | hack/e5/e5.py:191-199 | 600,1800,900,600×3,900,600×2,3600,900초; α=0,.3,.5,.7,.9,1.2 | binding.config.stages | 6 |
| A | hack/e5/e5.py:116-126,866 | G0 α=1.2; 1개 G-check; 120초 | binding.config.g0Evidence.durationSeconds / alpha / stageCount | 6 |
| A | hack/e5/e5.py:134-145 | CPU peak/limit<1; PLC percentile=95 | binding.config.g0Evidence.maximumCPUFraction / workload SLO percentile | 6 |
| A | hack/e5/e5.py:292-296 | idleNanosPerRequest≤1e6 | binding.config.inputs.maximumIdleNanosPerRequest | 6 |
| A | hack/e5/e5.py:949 | closures 정확히 25개 | binding.config.inputs.expectedClosureCount | 6 |
| A | hack/e5/e5.py:1137-1138; hack/e5/e5-wire.py:56-57; hack/e5/demo.py:478 | burstLowSeconds<900; setup+3600+drain+flush≤4200초 | binding.config.burstLowMaximumSeconds / collection.maximumDurationSeconds | 6 |
| A | hack/e5/e5.py:1294 | full=11700, static=10200, factor=2100×N초 | binding.config.schedule에서 full/static/factorSeconds 유도 (새 knob 없음) | 6 |
| A | hack/e5/e5.py:1369,1470,1609,1655,1737,1977,2026 | 상태 poll 1초; barrier/select .2초 | binding.config.timeboxes.statePollSeconds / selectPollSeconds | 6 |
| A | hack/e5/e5.py:1769-1770,1817,1824 | sampler/log follower 종료 대기 15초 | binding.config.timeboxes.collectorStopSeconds | 6 |
| A | hack/e5/e5.py:2030-2031 | maxActuationSeconds=900; subprocess 960초 | binding.config.timeboxes.maxActuationSeconds / nativeCallerSeconds | 6 |
| A | hack/e5/d2/conduct_before.py:56,59,65,869 | producer barrier 300초; .1초 poll | binding.config.timeboxes.barrierSeconds / barrierPollSeconds | 6 |
| A | hack/e5/d2/conduct_before.py:173 | future allowance 15초; observed-start≤915초 | binding.config.timeboxes.nativeFutureAllowanceSeconds / maxActuationSeconds (freshness 유도) | 6 |
| A | hack/e5/d2/conduct_before.py:246,286,425,538,954,1106 | api=129초; operations=1800초; native state=10초 | binding.config.timeboxes.apiSeconds / operationSeconds / nativeReadSeconds (E5 호출 부분만) | 6 |
| A | hack/e5/d2/conduct_before.py:315,347,332,458,635,712,738 | publication/drain/ACK/Ready=194초 | binding.config.기존 timeboxes.settleSeconds (E5 publication_end 호출만) | 6 |
| A | hack/e5/d2/conduct_before.py:333,352,457,628,634,713,739 | poll=5초 또는 2초 | binding.config.E5 호출 없는 D2 대기 메서드는 제외; 호출된 부분만 timeboxes.statePollSeconds | 6 |
| A | hack/e5/d2/conduct_before.py:794-805 | delete request=75초; owned deletion wait=60초; poll=1초 | binding.config.timeboxes.deleteRequestSeconds / deleteWaitSeconds / deletePollSeconds | 6 |
| A | hack/e5/d2/native_mode_request.py:29,60,94 | command timeout=60초; maxActuationSeconds 반드시900; poll=5초 | binding.config.timeboxes.nativeCommandSeconds / maxActuationSeconds / nativePollSeconds | 6 |
| A | hack/e5/d2/sample_cgroups.py:27,89,105-106 | crictl=10초; inventory=15초; thermal=5초; cgroup=1초 | binding.config.collection.crictlTimeoutSeconds / inventoryIntervalSeconds / thermalIntervalSeconds / cgroupIntervalSeconds | 6 |
| A | hack/e5/e5-producer.py:69,115-116 | GO/종료 marker poll=.05초 | binding.config.timeboxes.markerPollSeconds | 6 |
| A | hack/e5/e5-wire.py:158,190,215 | poll=1초; GOMAXPROCS=6 | binding.config.timeboxes.statePollSeconds / capacityGOMAXPROCS | 6 |
| A | hack/e5/e5-archive.py:38 | host-reader command timeout=20초 | binding.config.collection.hostCommandTimeoutSeconds | 6 |
| A | hack/e5/e5.py:822 | host-usage command timeout=20초 | binding.config.collection.hostCommandTimeoutSeconds | 6 |
| A | hack/e5/e5-archive.py:361-365 | 에너지 구간 양쪽 ±60초 | binding.config.collection.energySupportSeconds | 6 |
| A | hack/e5/e5-archive.py:508-519 | drained+60초 support; 시작 hour−1/끝 hour+1 범위 | binding.config.collection.energySupportSeconds (hour 범위는 support에서 유도) | 6 |
| A-미동결 | load-generator/src/chill_loadgen/cli.py:286 | --arrivals=fixed | 기존 loadgen CLI 유지; delayToleranceNanos CLI 추가(10ms); E5 binding.config.workloads에 실제 arrivals/seed/workers/budget 기록 | 6 |
| A-미동결 | load-generator/src/chill_loadgen/cli.py:289 | --seed=0 | 기존 loadgen CLI 유지; delayToleranceNanos CLI 추가(10ms); E5 binding.config.workloads에 실제 arrivals/seed/workers/budget 기록 | 6 |
| A-미동결 | load-generator/src/chill_loadgen/cli.py:290 | --workers=8 | 기존 loadgen CLI 유지; delayToleranceNanos CLI 추가(10ms); E5 binding.config.workloads에 실제 arrivals/seed/workers/budget 기록 | 6 |
| A-미동결 | load-generator/src/chill_loadgen/cli.py:291-295,321-325 | delivery=.01초; timeout fallback30초 또는 lifetime+delivery(+delivery) | 기존 loadgen CLI 유지; delayToleranceNanos CLI 추가(10ms); E5 binding.config.workloads에 실제 arrivals/seed/workers/budget 기록 | 6 |
| A-미동결 | hack/e5/e5.py:51,66-69 | producerResources cpu2/memory1Gi; shards MNV3/PLC1/Smol1 | 기존 실험 config 키 유지·누락 기본값 정상화 후 binding.config에 기록: 생성기 resource envelope·프로세스 분할 | 6 |
| A-미동결 | hack/e5/e5.py:841-845; hack/e5/numbers.py:191 | poolShares MNV.45/PLC.1/Smol.45 | 기존 실험 config 키 유지·누락 기본값 정상화 후 binding.config에 기록: workload별 부하 배분 | 6 |
| A-미동결 | hack/e5/e5.py:204,1130 | failureSelectSnapshotTTLSeconds=15초 | 기존 실험 config 키 유지·누락 기본값 정상화 후 binding.config에 기록: 장애 주입 대상 증거 freshness | 6 |
| A-미동결 | hack/e5/e5.py:916,1999 | finishResetWaitSeconds=600초 | 기존 실험 config 키 유지·누락 기본값 정상화 후 binding.config에 기록: post-off zero-state 이후 reset까지 허용 대기 | 6 |
| A-미동결 | hack/e5/e5.py:918; hack/e5/demo.py:453; hack/e5/auth.py:19 | tokenSafetyMarginSeconds=60초 | 기존 실험 config 키 유지·누락 기본값 정상화 후 binding.config에 기록: 인증 expiry 사전 점검의 여유 | 6 |
| A-미동결 | hack/e5/auth.py:27 | expiry probe timeout=10초 | 기존 실험 config 키 유지·누락 기본값 정상화 후 binding.config에 기록: 토큰 유효성 증거 수집 대기 | 6 |
| A-미동결 | hack/e5/e5.py:1437-1441 | powerDownSeconds=1800초; apiSeconds fallback30초 | 기존 실험 config 키 유지·누락 기본값 정상화 후 binding.config에 기록: reset HTTP 총 대기 | 6 |
| A-미동결 | hack/e5/e5.py:1932-1933 | factorAckWaitSeconds=120초 | 기존 실험 config 키 유지·누락 기본값 정상화 후 binding.config에 기록: 복원 prepare ACK 후 관측 대기 | 6 |
| A-미동결 | hack/e5/e5-archive.py:300-310; hack/e5/demo.py:468 | energyCoverage maxGapFraction=.01,maxSingleGapSeconds=30 | 기존 실험 config 키 유지·누락 기본값 정상화 후 binding.config에 기록: E5 에너지 row 허용 공백 | 6 |
| A-미동결 | hack/e5/d2/analyze_before.py:73-84; hack/e5/demo-config.e5.example.json:8 | generatorQuality lateness50ms,maxExceedingFraction=.01 | 기존 실험 config 키 유지·누락 기본값 정상화 후 binding.config에 기록: 생성기 품질 판정 | 6 |
| B | hack/e5/e5.py:824; hack/e5/e5-archive.py:46-52 | /usr/bin/crictl; namespace chill-system; serving-entrypoint | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | hack/e5/d2/conduct_before.py:35,768,773 | chill-system; SA token=/var/run/secrets/kubernetes.io/serviceaccount/token; CA=/ca/tls.crt; dataset=/data | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | hack/e5/d2/sample_cgroups.py:69-72 | chill-system/kubeedge; serving-entrypoint/edgemesh-agent/firstfloor/load; prefix d2-before- | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | hack/e5/d2/native_mode_request.py:63-65 | -n chill-system | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| C-이름없음 | load-generator/src/chill_loadgen/http_client.py:21-23 | DNS total253,label63(정규식 내부61) | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | load-generator/src/chill_loadgen/http_client.py:50,95,100 | control chars<32/127; minimum1e-9초; .9f | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | load-generator/src/chill_loadgen/http_client.py:136,145-152 | BYTES,base64,JSON,HTTPS,POST,/api/v1/workloads/{id}/infer,X-Chill-* | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | load-generator/src/chill_loadgen/cli.py:47,52,113-115,184; load-generator/src/chill_loadgen/schedule.py:65 | 1e9 ns/s;1e6 ns/ms | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | load-generator/src/chill_loadgen/cli.py:61,180,225 | item_count=1 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | load-generator/src/chill_loadgen/schedule.py:19-22,30,52,55 | offset≥0,item_count/worker≥1,tolerance≥0,previous=-1 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | hack/e5/auth.py:36-38 | JWT 3부분; base64 padding mod4 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | hack/e5/auth.py:49-56; hack/e5/d2/analyze_before.py:446 | API /api/v1/scheduler/state; HTTP200; X-Token-Expires-At | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | hack/e5/e5.py:87,95,112,842,1231 | Git SHA40hex; numeric tolerance1e-9/1e-12 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | hack/e5/e5.py:890,942,1294; hack/e5/demo.py:525 | e5-binding.v1,demo-e5-binding.v1,e5-private-*.v1 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | hack/e5/e5.py:931; hack/e5/e5-archive.py:147 | stream read chunk=1024×1024 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | hack/e5/e5-archive.py:96,133-134,182 | permission0600; metadata≤1MiB; nofollow | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | hack/e5/e5-archive.py:55,223; hack/e5/e5.py:828 | v1alpha5; serving/observations.v1alpha5/ | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | hack/e5/e5-archive.py:318,326; hack/measure/phase-a/measurement-generalization-20261024/cost-budget/arithmetic_check.py:18-19 | trapezoid .5 또는 /2 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | hack/e5/e5-raw.py:22-34; hack/e5/d2/analyze_before.py:39-44 | RFC3339 1~9자리; epoch1970;86400s/day;1e9 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | hack/e5/d2/conduct_before.py:539-540,595 | native state64MiB; DecisionID≤256 ASCII | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | hack/e5/d2/sample_cgroups.py:44-56,80,102 | /sys/fs/cgroup*,/proc/{pid}/cgroup,/sys/class/thermal; usec×1000;v1/v2 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | hack/e5/d2/analyze_before.py:52-53; hack/e5/e5.py:2144,2155,2167 | ceil(n×q)-1;rank=ceil(offered×pct/100) | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| A-미동결 | charts/chill/values.yaml:503; charts/chill/templates/serving_entrypoint.yaml:127 | profileConfidenceLevel=0.95 | SchedulerSettings.profileConfidenceLevel; CLI/values 구 경로 제거 | 3 |
| A-미동결 | charts/chill/values.yaml:495; charts/chill/templates/serving_entrypoint.yaml:112 | requestTimeout=129s | 기존 serving flags/values 유지; scheduler.startup 실제값 기록 | 4 |
| A-미동결 | charts/chill/values.yaml:498; charts/chill/templates/serving_entrypoint.yaml:124 | drainTimeout=164s | 기존 serving flags/values 유지; scheduler.startup 실제값 기록 | 4 |
| A-미동결 | charts/chill/values.yaml:501; charts/chill/values-operating.yaml:586; charts/chill/templates/serving_entrypoint.yaml:125 | sourceInterval=1s; operating=2s | 기존 serving flags/values 유지; scheduler.startup 실제값 기록 | 4 |
| A-미동결 | charts/chill/values.yaml:502; charts/chill/templates/serving_entrypoint.yaml:126 | profileInterval=1m | 기존 serving flags/values 유지; scheduler.startup 실제값 기록 | 4 |
| A-미동결 | charts/chill/values.yaml:514; charts/chill/templates/serving_entrypoint.yaml:120 | directoryInterval=1m | 기존 serving flags/values 유지; scheduler.startup 실제값 기록 | 4 |
| A-미동결 | charts/chill/values.yaml:516; charts/chill/templates/serving_entrypoint.yaml:121-122 | controlComponents=[operator, profiler, evidence-store, serving-entrypoint, node-agent, node-agent-actuation, dashboard, operator-console, metric-observer, workload-observer, transition-observer] | 기존 serving flags/values 유지; scheduler.startup 실제값 기록 | 4 |
| A-미동결 | charts/chill/values.yaml:815 | sampleInterval=1s | 기존 nodeAgent.resourceObservation 유지; 수신 resources 행에 적용 sample/batch 주기 기록 | 4 |
| A-미동결 | charts/chill/values.yaml:816 | batchInterval=10s | 기존 nodeAgent.resourceObservation 유지; 수신 resources 행에 적용 sample/batch 주기 기록 | 4 |
| A-미동결 | charts/chill/values.yaml:106; charts/chill/templates/profiler_config.yaml:39 | transitionObserver.activeDeadlineOverheadSeconds=180 | 기존 transition observer 배포값 유지; attempt의 frozen execution 입력에 overhead=180 기록 | 5 |
| B | charts/chill/values.yaml:18 | region=us-east-1 | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:33,69,429,460,559,567,931 | daclab/chill-{operator,profiler,console,serving-entrypoint,load-generator,dashboard,node-agent} | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:619 | registry.k8s.io/kubectl | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:39,75 | 1ba2121f.dacs.io; profiler.edge.dacs.io | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:653-795 | Jetson 4종, OrangePi 16Gi, RPi5 8Gi, LattePanda 8Gi, RTX2080Ti 64Gi의 label/architecture/memory/accelerator·mode ID·CPU mask | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:290-423 | xavier/nano/orin/rtx2080ti DeviceClass·mode·actuator 결합 | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:807-808 | /host; /etc/chill/node-agent/signatures.yaml | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:813-814 | /etc/chill/resources-ca/tls.crt; /var/run/secrets/kubernetes.io/serviceaccount/token | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:822-823 | serviceaccount/token; serviceaccount/ca.crt | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:825-838 | proc/device-tree/{model,compatible}; sys/firmware/devicetree/base/*; etc/nv_tegra_release; sys/{devices/virtual/dmi/id,class/dmi/id}/{sys_vendor,product_name} | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:840-929 | NVIDIA Jetson, Raspberry Pi 5, Orange Pi 5 Plus, LattePanda 3 Delta, Gigabyte Z370/Z390 signature→fact 매핑 | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:941-950 | /proc; /sys; /etc | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:980-983,985-1020 | aarch64 loader·library path·/usr/sbin/nvpmodel; /etc/nvpmodel[.conf]; /var/lib/nvpmodel; /sys | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:1030,1033-1036,1039-1053 | runtimeClassName=nvidia; x86_64 loader; /usr/bin/nvidia-smi; /lib/x86_64-linux-gnu; /usr/lib/x86_64-linux-gnu | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:952-954,1067-1070 | node-role.kubernetes.io/edge:NoSchedule | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/templates/serving_entrypoint.yaml:7,101,104; charts/chill/templates/_helpers.tpl:210 | svc.cluster.local; evidence store :9000 | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/templates/serving_entrypoint.yaml:89-90,213,216,218 | /etc/chill/tls/{tls.crt,tls.key}; /data; /tmp | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/templates/dashboard.yaml:61,81,83,86,102 | /etc/chill/ca/ca.crt; /app/.next/cache; /tmp; /etc/chill/ca | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/templates/console.yaml:212 | /var/run/chill/evidence | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | config/samples/profiling/edge_v1alpha1_batchenergyoptimization.yaml:5,10,14; config/samples/profiling/edge_v1alpha1_modelloadedidlebaseline.yaml:5,10,14; config/samples/profiling/edge_v1alpha1_powermodetransitioncost.yaml:5,7-8,14; config/samples/manual/edge_v1alpha1_deviceclass.yaml:4-7 | chill-system; example-node; example-device-class; example-transition-*; example-serving | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | config/samples/profiling/edge_v1alpha1_powermodetransitioncost_nvidia.yaml:8-19; config/samples/manual/edge_v1alpha1_deviceclass_nvidia.yaml:4-10 | genesis2; chill-u3-genesis2-clock-runner; edge-server-rtx-2080-ti-64g; amd64;64Gi | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | config/samples/catalog/edge_v1alpha1_modelspec.yaml:12,19,26; config/samples/catalog/edge_v1alpha1_modelspec_plc.yaml:12,19; config/samples/catalog/edge_v1alpha1_modelspec_smolvlm2.yaml:12,21,30,39; config/samples/catalog/edge_v1alpha1_preprocessor_mobilenet-imagenet.yaml:17,22,27; config/samples/catalog/edge_v1alpha1_preprocessor_plc-extruder-anomaly.yaml:18,23; config/samples/catalog/edge_v1alpha1_preprocessor_smolvlm2-vqa.yaml:19,24,29,34; config/samples/catalog/edge_v1alpha1_dataset.yaml:9; config/samples/catalog/edge_v1alpha1_dataset_plc.yaml:9; config/samples/firstfloor/ortcpu-arm64-runtime-declaration.json:3 | 155.230.35.213:5000/chill/* | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | config/samples/catalog/edge_v1alpha1_modelspec.yaml:15,22,29; config/samples/catalog/edge_v1alpha1_modelspec_plc.yaml:15,22; config/samples/catalog/edge_v1alpha1_modelspec_smolvlm2.yaml:15-17,24-26,33-35,42-44; config/samples/catalog/edge_v1alpha1_preprocessor_mobilenet-imagenet.yaml:19,24,29; config/samples/catalog/edge_v1alpha1_preprocessor_plc-extruder-anomaly.yaml:20,25; config/samples/catalog/edge_v1alpha1_preprocessor_smolvlm2-vqa.yaml:21,26,31,36; config/samples/catalog/edge_v1alpha1_dataset.yaml:10; config/samples/catalog/edge_v1alpha1_dataset_plc.yaml:10 | /artifact/*.onnx; *.rknn; mobilenet-{nchw,nhwc}; plc-extruder-anomaly; smolvlm2-vqa[-1tile]; dataset 디렉터리 | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | config/samples/firstfloor/echo.json:6,13 | runtime/firstfloor/runner.py; runtime/firstfloor/testdata/echo; config/samples/firstfloor/echo-profile.json | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:487 | observationStorage.directory=/data | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| B | charts/chill/values.yaml:1038 | clocks=[1500,1200,900] MHz | charts/chill/values-operating.yaml 또는 CHILL_EXPERIMENT_CONFIG 환경 JSON; 배포→생성자→소비자 연결 | 2 |
| C-이름없음 | charts/chill/templates/_validation.tpl:16,25,39,53,56,97,123,160,163,178 | positive≥1; TCP port≤65535 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | charts/chill/templates/_helpers.tpl:2,7,11,13,32,58,62,66,121,125,129,133,167,187,199,215 | trunc 63 | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | charts/chill/templates/profiler_config.yaml:4-5 | retention∈[1,604800] seconds | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |
| C-이름없음 | charts/chill/templates/predelete_cleanup.yaml:12-14,25-27,55-57,76-78,88,101-102 | pre-delete; hook weight -20/-10; before-hook-creation,hook-succeeded; restart Never; ignore-not-found/wait=true | 원래 파일의 의미 있는 상수; 동일 계약만 공유 | 1 |

## 개수와 재분류

- 실행 범위 원분류: 252행. 이동/기록 대상 **140행**: A 41, A-미동결 29, B 35, C-이름없음 35.
- 원분류 유지 D 79행, C 23행; ? 10행은 설계 열린 점에서 처리. 범위 제외 122행. 재분류 16행. 합계 390행.
- 재분류 후 C/D 개수: C 36, D 82.

| 원본 위치 | 원분류 → 새 분류·이유 |
|---|---|
| internal/profiling/derivation/derive.go:149 | A → C: p99는 저장 요약의 고정 이름/방법론이며 수용 정책 아님 |
| internal/metricobserver/observer.go:279 | A → C: observer 지연 진단 표시이며 증거 수용 아님 |
| internal/operatorconsole/evidence_passport_view.go:322 | A → C: passport 표시 선택 |
| internal/serving/live/metrics.go:217 | A → C: live 표시 분위수 |
| internal/serving/live/metrics.go:102,200,314 | A → C: live 표시 정정/보존 창; 저장 원시 증거는 별도 |
| internal/serving/live/hub.go:467 | A → C: live 발행 cadence; 저장/판정과 분리 |
| internal/serving/capacity.go:305 | A → C: PCG 두 번째 stream 선택자 1은 고정 알고리즘 방법론; 첫 seed와 혼동 금지 |
| internal/serving/diagnostic.go:13 | A → C: opt-in 성능 진단 capture 길이; 연구 수용 정책 아님 |
| cmd/chill-server/diagnostic.go:20 | A → C: opt-in Go trace capture 길이 |
| hack/e5/e5.py:2130-2132,2158 | A → C: 분별 표시 집계; D1은 전체/단계 offered 모집단으로 별도 계산 |
| hack/e5/e5.py:2196 | A → C: 열 분포 descriptive 표시 bin; D1/energy 판정 불변 |
| hack/e5/d2/analyze_before.py:49-53 | A → C: stats 표시 요약 집합; SLO는 workload percentile 사용 |
| hack/e5/d2/analyze_before.py:102-106 | A → C: issuance descriptive 계수; generatorQuality 별도 동결 threshold 사용 |
| charts/chill/values.yaml:806 | A-미동결 → D: node-agent read-back 운영 cadence; 측정 창/actuation policy와 별개 |
| charts/chill/values.yaml:984 | A-미동결 → D: 이미 배포 입력인 command safety envelope |
| charts/chill/values.yaml:1037 | A-미동결 → D: 이미 배포 입력인 command safety envelope |

범위 제외는 삭제 승인이 아니다. 역사/독립 실행부의 구체 목록과 ?의 처리 원칙은 [계획](2026-10-30-configuration-hardcoding.md), 인터페이스는 [설계](../design/serving/configuration-hardcoding.md)를 따른다.

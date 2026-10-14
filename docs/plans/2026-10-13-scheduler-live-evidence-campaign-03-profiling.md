# CHILL 캠페인 진행 기록 — 정식 측정·노드 조사와 반복 trial

[현재 목표·규칙·상태와 다음 의존성](2026-10-13-scheduler-live-evidence-campaign.md)

아래는 기록 당시의 진행 상태다. 현재 상태는 캠페인 진입 문서를 따른다.

### 연속 실행 — 정식 native Run과 evidence 재조회 완료

- `docs/spec/00_measurement.md`의 동일 firstfloor 실행 조건·raw 보존·실패 명시와
  scheduler SRS의 SCH-08~12를 기준으로 확인했다. 이 결과는 실행/evidence 경로의
  통과이며, 반복 trial·유효 프로파일·SLO 보장·에너지 절감 검증을 대신하지 않는다.
- 첫 Run은 runtime image의 기본 serving-worker entrypoint에 `--run-config`가
  전달되어 실패했다. Job이 `/measurement-worker`를 명시하도록 한 줄 수정했다.
  기존 observer 이미지도 native 계약을 읽지 못했으므로 최신 producer를 배포했다.
- operator가 관리하는 Profiler의 실제 rollout을 기다리기 전 만든 재시도는
  이전 controller가 Job을 만들어 동일 실패했다. 복구 상태기계는 추가하지 않았고,
  실제 deployment rollout 완료 후 새 Run을 만들었다.
- `native-rasp5-ortcpu-q1-k1-current`, UID
  `91ba053b-afc6-4af8-80f4-0623fc8c9a52`: ExecutionComplete/JobSucceeded 및
  EvidenceVerified=True. runtime·metric-observer·workload-observer 모두 exit 0.
- frozen 조건: rasp5, ORT CPU firstfloor, K=1, CPU quota/request=1,
  affinity=[3], warmup=2, saturated fixed-batch 10초. quota/affinity는
  Kubernetes의 독점 CPU 할당 증거가 아니다.
- 실제 측정 10.02555초, 입력 56개 완료, client-call p95 188.328473ms,
  출력 shape 검증 통과. 이 client latency를 backend service latency로 바꾸어 부르지 않는다.
- 벽전력 표본 12개·수집 실패 0, 6.4~7.0W, HTTP acquisition 시각 유지,
  sourceTimestampAbsent=true. thermal 12표본, active/latched throttle 0,
  33.65~36.95°C. 동시 상주 workload 변화 0; 기존 8개 co-resident는 유지했다.
- raw `profiling-runs/<UID>/raw.json` 24428바이트 및 finalized
  `execution.json` schema v1alpha15 10979바이트를 evidence store에 보존했다.
- 이전 Console reader의 passport 조회는 502였다. 이전 계약 reader를 추가하지 않고
  최신 Console 이미지를 배포했다(release revision 16, 실제 rollout 성공).
  같은 durable evidence passport의 production API 재조회가 성공했다.
- focused `go test ./internal/profiling/profiler/controller` 통과.
  수정의 ponytail 리뷰는 `Lean already. Ship.` 추가 추상화/검증 없음.
- 근거: `/tmp/chill-native-campaign-first-run-success.json`,
  `...-first-run-success-pod.json`, `...-first-run-passport.json`,
  `...-first-run-failed.json`, `...-corrected-run-old-controller.json`.
- 다음 의존성 단위: 활성 노드 전체의 실제 자원·상주 작업·물리 meter 연결 및
  실행 가능한 경로 확인. gateway `/devices`에서 서로 다른 Shelly 10개가 연결되어
  있고, source 확인상 scrape마다 `Switch.GetStatus` RPC를 실행한다.

### 연속 실행 — 10노드 조사와 latest 실행/조회 연결

- 10노드 모두 Ready인 timestamped API snapshot을 보존했다. 각 Node UID,
  allocatable·상주 Pod별 resource request를 기록했고 기존 서비스를 유지했다.
  genesis2 GPU allocatable=0; CPU 경로와 GPU 지원 여부를 혼동하지 않는다.
- gateway 설정에서 서로 다른 Shelly 10개와 hostname/type 대응을 보존했다.
  노드별 필터 endpoint 실제 GET은 각 1개 벽전력 series를 반환했다.
  source는 매 scrape마다 실제 `Switch.GetStatus` RPC를 실행한다.
  장치측 power timestamp는 없으며, 물리 배선/다른 부하의 연결 여부를 새로 측정한
  것은 아니다. configured scope와 실제 series/plug identity 관측을 구분한다.
- thermal endpoint는 10개 모두 응답했다. orin-nano에는 공통 thermal/throttle
  지표가 없어 관측 부재로 남겼다. 이를 0이나 정상 thermal로 대체하지 않는다.
- AMD64 native image에서 LattePanda 실제 Pod로 ORT 1.27.0/NumPy 2.2.6 및
  CPU provider를 확인했다. 검사 Pod는 제거했다.
- 기존 ONNX를 그대로 담은 ARM64/AMD64 carrier를 OCI index로 연결했고,
  우리 native ModelSpec만 owner PUT/If-Match로 generation 2로 갱신했다.
  현재 모델 하나에 두 실제 architecture 경로가 있으며, 구 버전 reader/alias가 아니다.
  기존 사용자 모델·Workload와 모델 가중치는 변경하지 않았다.
- native declaration 12개, metric source 10개, 실행 policy 10개를 기존 release values에
  병합했다. latest owner와 Dashboard image 및 좁은 power read RBAC를 배포했다.
  release revision 17 및 실제 owner/Dashboard rollout 통과; owner는 기존 Recreate 전략이다.
- explicit live Profiler render: 72 resources, valid 60, invalid/errors 0, skipped 12.
  default Helm gates, make lint/test, arch-check, gopls-check 통과. arch 첫 실행은
  host에 rg가 없어 실패했다; isolated /tmp tool 경로를 제공한 뒤 실제 architecture
  gate가 65/65 package mapping을 확인했다. lint를 suppress하지 않았다.
- routing keep/disabled/full 분배와 replay credit parity, 선택 CPU/memory를 쓰는
  두 init container 및 idempotent 검증, owner same-interval 전체/활성 소비 API를 구현했다.
  리뷰의 interpolation/실패값/기존 init 검증 지적을 수정했다. 추가 retry/호환 계층 없음.
  backend 및 Dashboard의 최종 ponytail 리뷰: `Lean already. Ship.`
- Dashboard native security tests 10개, typecheck/lint/build 통과. 기존 Asta Sans fallback
  warning은 남아 있다. 실제 HTTPS Dashboard를 기존 단기 manager identity로 인증했다.
  current snapshot 링크의 선택/재선택 경로를 확인했고 중복 상세 렌더링은 제거했다.
- 새 operating power API의 첫 실제 호출은 timeout됐다. 실제 current snapshot도
  zero-time/비어 있고 owner log에 control interval 및 S3 publication timeout이 있다.
  따라서 core 테스트/화면 로딩만으로 API/e2e 통과를 주장하지 않는다.
- `docs/spec/00_measurement.md`와 scheduler SRS SCH-08~12, 16~17, 24, 30~32로
  조건·실패·실측과 예측/목표의 구분을 대조했다.
- 정식 worker command 수정은 전체 Go gate와 live failure-before/pass-after 후
  commit `a6f8c72`에 controller 1줄 및 해당 profiling 문서만 포함했다.
- 근거 prefix `/tmp/chill-native-campaign`: `active-node-workload-snapshot.json`,
  `all-node-wall-meter-snapshot.json`, `all-node-thermal-snapshot.json`,
  `meter-device-map.json`, `gateway-source.py`, `amd64-runtime-declaration.json`,
  `model-native-all-architectures-update.json`, `all-node-current-render.yaml`.

### 연속 실행 — 반복 trial 확보와 실제 미지원 조건

- read-only preflight는 8노드 resolves=true. Nano/Orin Nano는 각각 MAXN/25W의
  actuation policy 부재를 구체 이유로 반환했다. 최초 잘못 놓은 cpuQuota/affinity
  요청은 400이었으며, current API의 campaign spec-level 필드로 수정해 조회했다.
- 고정 모드 6노드에 서로 다른 노드별 q1/affinity=[3], K=[1,2],
  active·idle 각 batch trial=3의 campaign을 실행했다. native 최초 모델 사용,
  warmup=2·active=10초, idle settling=2초·idle=10초, meter 1Hz·12초다.
- genesis1/2/3, lattepanda, rasp5는 각 9개 trial, ResultWritten/Complete,
  결과 v1alpha4와 실행 evidence v1alpha15를 보존했다. 노드별 연속된 상태를
  같은 노드의 동시 측정으로 바꾸지 않았다.
- rasp5 q1 result의 measured batch=[1,2], 3개 loaded-idle mean 약4.874W,
  incremental energy/item K1=0.34892959496605563J, K2=0.3316833311082276J.
  이것은 해당 profiling 조건의 값이며 운영 POOL 총소비/실제 SLO의 입증이 아니다.
- orangepi idle 첫 실행 및 retry는 `/sys/fs/cgroup/memory.peak` 파일 부재로
  runtime exit3였다. observer timeout은 시작 신호가 없던 후속 실패다.
  메모리 값을 꾸며 성공시키거나 나머지 5노드 결과로 대체하지 않았다.
- 성공 5노드만 q1 terminal 이후 q2/affinity=[2,3] campaign을 시작했다.
  active campaign 중 stored evidence 계약은 바꾸지 않는다.
- 네 Jetson의 public host config/status/online CPU를 read-only Pod로 읽고 검사 Pod를
  모두 제거했다. 현재 mode: Nano/MAXN id0·CPU0-3, AGX/MAXN id0·0-11,
  Xavier/MODE_15W_6CORE id2·0-5, Orin Nano/25W id1·0-5.
  class의 name/id와 실제 config가 일치한다. mode 변경은 이 검사에서 하지 않았다.
- 근거: `fixed-q1-created.json`, `rasp-q1-result.json`,
  `orange-q1-failed-runs.json`, 각 `power-host-<node>.json`, `*-q2-created.json`.


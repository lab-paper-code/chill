# Hack Scripts

Use a repo-local kind kubeconfig for development and e2e tests:

```sh
./hack/kind-up.sh
export KUBECONFIG="$PWD/.kube/chill-kind.config"
```

The tracked `.envrc` points `KUBECONFIG` at `.kube/chill-kind.config` when direnv is enabled.

Do not use the testbed kubeconfig as the default shell context inside this repo. Access the testbed only with an explicit prefix:

```sh
KUBECONFIG=/path/to/testbed.kubeconfig kubectl get nodes
```

Before installing the Helm chart with `crds.enabled=true` on a shared cluster,
check CRD ownership:

```sh
make helm-crd-check
```

Use `make helm-adopt-crds` only for an intentional release migration.

Helm install and cleanup is implemented behind the repo Make targets:

```sh
make helm-install
make helm-uninstall
```

When `HELM_VALUES` names a values file, that file owns component image values;
Makefile-generated image defaults do not override it. An exact component image
can still be overridden explicitly through `OPERATOR_IMG`, `NODE_AGENT_IMG`,
`PROFILER_IMG`, or `CONSOLE_IMG` in the command environment or Make command
line.

`make helm-uninstall` treats runtime cleanup as CHILL's responsibility: it
deletes the root `ChillSystem` and waits for finalizers before calling Helm
with hooks disabled. The chart hook remains only as a safety net for direct
`helm uninstall`.

`make helm-purge-crds` is intentionally guarded and requires
`CONFIRM_PURGE_CRDS=chill`.

## Control-only upgrade

`hack/fast-deploy.sh upgrade --source <commit> --control <NNN> --dry-run`
prints a local-only deployment plan. Remove `--dry-run` to publish changed
control images and upgrade the existing release without uninstall/reset.
`--images auto` (default) compares live image source commits; an explicit
subset of `server,profiler,dashboard` is also accepted. Runtime image builds
are forbidden. See [the operations guide](../docs/operations/ops-agent-guide.md)
for the shared lock, evidence files, prerequisites and stop conditions.
Upgrade and E5/demo start/run reject tokens shorter-lived than their stage/timebox budget plus safety margin (`--token-safety-margin-seconds`, default 120s for upgrade; `tokenSafetyMarginSeconds`, default 60s for E5/demo); JWT `exp` or authenticated `X-Token-Expires-At` evidence and expiry UTC are retained without token bytes.

### E5 #222/#223 static replay

Reuse `e5.py factor --mode static --stage E5-3 --pre-state <static-selection.json>
--row-tag 223`; this is the existing paired pre-state prepare/ACK path, not a
campaign resume engine. Both a factor `pre-state.json` snapshot and the frozen
selection's `state.snapshot` are read by the same baseline reader. The selected
state must match its recorded fingerprint and the Run's original selection.
Prepare retains the selected maxBatch and polls fresh instances[] ACKs.
After route publication, compare the fingerprint once with only old physical IDs
rebound to actual prepare-ACK IDs; any profile/allocation/cap/wait/route/mode
difference exits 2. `static-replay-restore.json` retains that comparison and mapping.

`--row-tag 223` appends static E5-3..6 and full E5-4..7 directories; previous
rows, the failed unsuffixed static E5-3, original freeze and finish evidence remain
untouched. Tagged cleanup touches tagged rows only. No prior row is rerun.
Each tagged row's entry/exit records available filesystem bytes in
`disk-boundaries.jsonl`; below 310 GB (310,000,000,000 bytes) stops before another
row. `summarize` and `tables` select appended rows explicitly and display
**#222+#223 합성, 같은 rev104 소스**, not a fresh independent campaign.

Offline only: add `--dry-run` to that factor command, with the preserved #222
binding and Run. It never constructs CPO or contacts the cluster; it prints
restoration request bodies, original fingerprint, ACK-only ID rebinding rule and
execution order, and writes `Run/static-restore-dry-run-223.json`.

CampaignOps sequence below requires `CHILL_E5_RELEASE_223` to point to the actual
Main-approved #223 `e5-execute` release with the unchanged #222 binding/component
sources. Run each line only after the preceding one succeeds; `set -e` stops
the loops on a failed stage or invalid-generator judgment. These are existing
stage/restore/finish commands, with no automatic retry or factor experiment.

```sh
set -e; export CHILL_EXPERIMENT_CONFIG=/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/inputs/environment-212.json; Run=/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full; Release=${CHILL_E5_RELEASE_223:?Main-approved release required}; e5() { nice -n 19 python3 -B hack/e5/e5.py "$@" --binding "$Run/binding.json" --release "$Release"; }; e5 factor --out "$Run" --mode static --stage E5-3 --pre-state "$Run/static-selection.json" --row-tag 223
for n in 3 4 5 6; do e5 stage --out "$Run" --mode static --stage "E5-$n" --row-tag 223; e5 judge --out "$Run/static-E5-$n-223"; done
e5 restore --out "$Run" --row-tag 223
for n in 4 5 6 7; do e5 stage --out "$Run" --mode full --stage "E5-$n" --row-tag 223; e5 judge --out "$Run/full-E5-$n-223"; done
e5 finish --out "$Run" --row-tag 223; e5 cleanup-jobs --out "$Run" --row-tag 223; e5 summarize --out "$Run"; nice -n 19 python3 -B hack/e5/e5.py tables "$Run" | tee "$Run/tables-223.txt"
```


### E5 micro rows

**미니 행은 반복 검증용, 정본 표는 전체 행.** `micro=true` 결과는 별도 비교
표/`e5-summary.json.microRows`에만 표시하며 정본 E5-0..7·E5-S 및 시나리오
완주 수에는 포함하지 않는다. D1·완료-only p95·POOL 에너지/공백 오차 판정은
동일한 raw 규칙을 사용한다. 동일 길이 고정 trace가 없으면 에너지 Δ는 N/A다.
전체 길이 정본 에너지와 짧은 행 에너지를 빼서 절감으로 표시하지 않는다.

`hack/e5/micro-stages.example.json`의 `stages`를 기존 전체 행 뒤에 추가한다.
실제 bind 입력은 3회차의 R·payload·seed·arrival·poolShares를 그대로 사용하고,
기존 `stages`에 이 미니 행 정의만 덧붙인다
(`rate_w = alpha_w × s_w × R_w`). `CHILL_EXPERIMENT_CONFIG`의 `e5.stages`도
bind 입력에 `stages`가 없을 때만 적용된다. 운영 복사본은
`inputs/environment-micro.json`이며 기존 `environment-212.json`은 바꾸지 않는다.

| 행 | 부하 창 | 복원 원본 |
|---|---|---|
| E5-5m | α=.3 180s → α=.9 300s | full-E5-5-223/pre-state.json |
| E5-7m | α=.3 360s; 120s 후 serving Pod 주입 1건 | full-E5-7-223/pre-state.json |
| E5-3m | α=.5 180s → α=.9 300s | full-E5-3-223/pre-state.json |
| E5-6m | α=.3 600s soak | full-E5-6-223/pre-state.json |

행별 `factor --mode full --stage E5-5m --pre-state "$Run/full-E5-5-223/pre-state.json"
--row-tag <fresh-tag>` 후 같은 stage/tag의 `stage`·`judge`를 사용한다.
micro 행은 성공한 해당 복원 receipt가 필수이며 기존 재시도/덮어쓰기 금지는 그대로다.
E5-7m은 stage의 `failureInjectionRunbook.steps`를 사용하며 `failure-select`는
양의 route/assigned 및 실제 UID가 확인된 Pod 1건의 명령만 출력한다. 실행은
운영자만 수행하고 실제 명령·시각·응답을 `failure-events.json`에 기록한다.
이 설정은 측정을 실행하지 않는다. 네트워크 없는 산식·허용 목록·복원 원본·주입
자가 검사는 `python3 -B hack/e5/e5.py self-check-micro`, 표 격리는
`python3 -B hack/e5/e5.py tables --self-check`다.

### E5 targeted full replay

`factor --mode full --stage E5-{1,3,5,6,7} --pre-state <original-row/pre-state.json>
--row-tag 3` reuses the same reset/prepare/native-mode/route ACK path (#237 adds E5-3).
It reads the original row's sibling `pre-settings.json` (or embedded settings), including
full autonomy, and restores served canonical instances with exact maxBatch,
allocation, profile, wait, routing and native modes. Only physical IDs returned
by prepare ACKs are rebound. One final fingerprint comparison is retained in
`Run/full-E5-N-3-restore.json`; mismatch exits 2 before any targeted stage.
Autonomy stays off while producers prepare; the saved full settings are enabled
at producer GO after releasing restored routing. Targeted stages require their
own accepted receipt, not completion of intervening historical rows.
Targeted E5-3 retains the original campaign's static selection; only the first
untagged full E5-3 freezes a static baseline at its offered-window boundary.
Sampler shutdown recognizes intentional SIGTERM after drain; cleanup errors remain
visible and do not trigger measurement replay.

The existing fingerprint intentionally excludes reservations and owner-only
non-serving preparations. They are **not** reconstructed. Every dry-run/restore
receipt and the rendered conditions table reports the original reservation count
and excluded owner-only count, including E5-5's original C1 reservation presence.
The preserved #222/#223 inputs currently record E5-5: 0 reservations/0 owner-only;
E5-6 and E5-7: 1 reservation/3 owner-only each. These counts are input observations,
not an assertion of reproduced reservation behavior. Existing rows are untouched;
new directories are `full-E5-{1,5,6,7}-3`. Tables label them
**3회차 = 표적 재실행, 시작 상태 복원, 소스 R1~R6+R4b+C1+C2**.
Retained static/other full rows are historical, not a new independent campaign.

Offline planning: add `--dry-run` to each full factor command. It does not
construct CPO or contact the cluster. Output is
`Run/full-E5-N-restore-dry-run-3.json`, with original pre-state/pre-settings hashes,
request bodies, exclusions, expected fingerprint and factor → stage → judge order.
Use the original `full-E5-1/pre-state.json` and
`full-E5-{5,6,7}-223/pre-state.json` inputs; no copied or fabricated start state.

E5-7 injection is **operator-only**, as in the first actual run. During its
offered window, immediately before the recommended 180s delete and 360s
cordon/delete, run `e5 failure-select --out "$Run" --mode full --stage E5-7
--row-tag 3 --failure-kind delete-serving-pod` and then the same command with
`--failure-kind cordon-delete`. Selection only performs GETs and emits
`operatorCommands`/`operatorArgv`; it never executes them. Execute the emitted
commands immediately (before the recorded `expiresAtUTC`), then append
actual command/result records to `full-E5-7-3/failure-events.json`'s `events`.
Each deletion record requires `kind`, actual `atUTC`, `exitCode`, `podUID`,
`nodeUID`, `commandRef`, `responseRef`. The first command reference records
`argv` (or `deleteArgv`); the cordon-delete reference records `deleteArgv` plus
the actual cordon command/result. At recommended 600s the operator uncordons the
second selected Node and records the third `uncordon` event. Recovery evidence
comes from the preserved `scheduler` and `cluster` API rows, not operator readbacks.

The selector intersects the latest fresh scheduler state's **routing weight > 0**
with **instanceAssignments > 0 in the immediately preceding observation window**
and the current scheduler state's Ready serving instance, Pod name/UID and Node
reference. The first selection requires candidates on two distinct Nodes; the
second excludes the first Node. The record includes weight, assigned count,
Pod/Node UIDs, window bounds and captured API source paths.
Deletes use raw Kubernetes `DeleteOptions` with UID-only preconditions and no
grace-period override, matching the first actual run. Missing candidates, stale
state or read failure writes `status: INVALID`, emits no injection command and
cancels injection; do not override it manually. Judge validates the saved
selection/source/command/options evidence; INVALID or UNMEASURED remains visible
instead of a captured failure scenario. Expired selection is INVALID, not a
license to use a stale target.

CampaignOps order (five shell lines; existing frozen component release/binding
for R1~R6+R4b+C1+C2 must be supplied, never reuse the old rev104 source release).
Use a separate operator shell for the E5-7 selection/injections while line 3
runs. Every row must finish before advancing; no automatic retries:

```sh
set -e; export CHILL_EXPERIMENT_CONFIG=/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/inputs/environment-212.json; Run=/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full; Binding=${CHILL_E5_BINDING_3:?Main-approved C1+C2 binding required}; Release=${CHILL_E5_RELEASE_3:?Main-approved release required}; e5() { nice -n 19 python3 -B hack/e5/e5.py "$@" --binding "$Binding" --release "$Release"; }
for n in 1 5 6; do suffix=-223; test "$n" != 1 || suffix=; e5 factor --out "$Run" --mode full --stage "E5-$n" --pre-state "$Run/full-E5-$n$suffix/pre-state.json" --row-tag 3; e5 stage --out "$Run" --mode full --stage "E5-$n" --row-tag 3; e5 judge --out "$Run/full-E5-$n-3"; done
e5 factor --out "$Run" --mode full --stage E5-7 --pre-state "$Run/full-E5-7-223/pre-state.json" --row-tag 3; e5 stage --out "$Run" --mode full --stage E5-7 --row-tag 3
e5 judge --out "$Run/full-E5-7-3"
e5 finish --out "$Run" --row-tag 3; e5 cleanup-jobs --out "$Run" --row-tag 3; e5 summarize --out "$Run"; nice -n 19 python3 -B hack/e5/e5.py tables "$Run" | tee "$Run/tables-3.txt"
```



## Profiler runtime declarations

`charts/chill/values-profiler.yaml` takes `profiling.executionDeclarations`
only as exact-image inspection output. After the firstfloor runtime images are
pushed, list each per-platform child digest and run the generator against the
testbed:

```sh
docker manifest inspect --insecure 155.230.35.213:5000/chill/chill-firstfloor:TAG \
  | jq -r '.manifests[] | "\(.platform.os)/\(.platform.architecture) \(.digest)"'
CPU_ARM64_IMAGE=155.230.35.213:5000/chill/chill-firstfloor@sha256:... \
CPU_AMD64_IMAGE=155.230.35.213:5000/chill/chill-firstfloor@sha256:... \
CUDA_AMD64_IMAGE=155.230.35.213:5000/chill/chill-firstfloor@sha256:... \
CUDA_L4T_R32_IMAGE=155.230.35.213:5000/chill/chill-firstfloor@sha256:... \
CUDA_L4T_R35_IMAGE=155.230.35.213:5000/chill/chill-firstfloor@sha256:... \
CUDA_L4T_R36_IMAGE=155.230.35.213:5000/chill/chill-firstfloor@sha256:... \
RKNN_IMAGE=155.230.35.213:5000/chill/chill-firstfloor@sha256:... \
  hack/runtime-declarations.py > /tmp/runtime-declarations.yaml
```

The tags are `TAG` for CPU (`linux/arm64` and `linux/amd64` children),
`TAG-cuda`, `TAG-l4t-r32`, `TAG-l4t-r35`, `TAG-l4t-r36`, and `TAG-rknn`. The generator runs
one probe Pod per image in `chill-system` on a Node of the target class, with
the measurement Job's user, groups, privilege, and `nvidia.com/gpu` request. It
builds each payload from the architecture, ONNX Runtime providers, and package
versions read inside the image, reads the GPU UUID through the CUDA driver on
every GPU Node (`jetson-nano`, `jetsono`, `jetsonx`, `orin-nano`, `genesis2`, `genesis3`), and
reads the RKNPU driver version and device-tree token on `orangepi`. Every
DeviceClass receives one class-level CPU declaration; each GPU Node receives its
own CUDA declaration with `node` set, because the GPU UUID names that Node's
device, and `orangepi-5-plus-16g` adds its RKNN declaration, each when the image
variable is set. `genesis1` runs the transition observer and has no metric
source, so it is not probed.

Replace the `executionDeclarations` list in the values file with the output's
list and validate the render with
`make helm-lint helm-template HELM_VALUES=charts/chill/values-profiler.yaml`.
`hack/runtime-declarations.py --self-check` checks the payload builder offline.

## Offline observation tables

`hack/observations/` builds measurement tables from the hourly S3 observation segments: `fetch.sh <from-hour> <to-hour> <dir>` downloads them plus one hour on each side, `duckdb < hack/observations/e1.sql` in `<dir>` gives the E1 request and Tick tables, and `go run ./hack/observations/power --dir <dir>/obs --scheduler-config <scheduler ConfigMap value> --from --to` gives whole and active POOL energy with the ChillServer reducer.

`go run ./hack/kappa` prototypes the per-option capacity table with the existing scheduler replay: `-mode table` builds kappa for every option and Workload SLO, `-mode validate` replays chosen options at 0.8 to 1.2 kappa, and `-mode compare` replays recorded decisions with the kappa-sum node DP planner (`-sweep` adds the replay planner at swept loads). Results: `docs/design/serving/capacity-table-evidence.md`.

`go run ./hack/kappa -mode slo -scheduler <GET /api/v1/scheduler dump> -workloads <names>` proposes an SLO target per named Workload: per Node the lowest p99 b1 latency (execution + dispatch occupancy + `completionMarginMS`), then the target that passes at least half the Nodes and fails the two slowest, at the widest latency gap. It builds the Workload's rows at that target with p95 and p99 and prints the largest kappa per Node, the kappa sum, whether the rule holds on kappa, and each table's build time. A Workload need not be autonomous; any Workload with the same model, preprocessor and dataset selects the same options. `go test ./hack/kappa` checks the proposal on synthetic Nodes.

### Required pre-deployment decision + guard replay gate

**배포 전 필수:** `hack/e5/replay-gate.py`를 보존한 E5-3/5/7 및 회귀
stage 전체에 실행하고 `summary.json`의 `verdict=PASS`를 배포 증거에 첨부한다.
판정은 **기준 소스 대비**다: 첫 `--source`(또는 `--reference`)가 기준이며 후보는 stage마다 나빠진 tick·공급 증가 거부가 기준을 넘지 않으면 PASS(기록 자체의 population mismatch로 생기는 잔여 bad tick은 기준에도 같이 나타난다). 불일치 자체는 실패가 아니다. 배포 전 기준은 **현재 live 서버 소스**로 둔다.
나빠진 tick은 (1) keep κ가 수요보다 부족한 상황의 기록된 공급 증가 target을
재평가가 Keep/no target으로 바꿈, 또는 (2) guard가 공급 증가 target을
당시/다음 N tick 중 한 번이라도 거부함이다. 공동 owner는 workload 하나라도
공급이 증가하면 포함하고, origin tick당 한 번만 센다.

```sh
python3 -B hack/e5/replay-gate.py --source HEAD \
  --stage /ops/campaign/full-E5-3-7 --stage /ops/campaign/full-E5-5-7 \
  --stage /ops/campaign/full-E5-7-7 --stage /ops/campaign/full-E5-5-8d \
  --rows /ops/deployB/control/kappa-rows.json \
  --profiles /ops/deployB/control/profiles-gate.json \
  --scheduler /ops/deployB/control/scheduler.json \
  --out /ops/campaign/v2/replay-gate/YYYY-MM-DD
```

`--source`는 반복 가능하며 SHA/ref 또는 `tree`(현재 tracked diff와 untracked
파일 포함)다. `git archive` 소스에 현재 `hack/kappa`만 계측기로 덧입히고
제품 planner/guard는 해당 소스 그대로 빌드한다. 캐시는 소스·tree patch·계측기·
Go 버전/플랫폼 해시로 구분한다. guard가 없던 역사적 소스는
`guardAvailable=false`/`GuardNotPresentInSource`로 명시하며 현 guard를 이식하지 않는다.
입력 파일 SHA256, 빌드 provenance, 실행 시간, 모든 tick의 compare/guard JSONL과
요약 JSON/MD를 보존한다. 종료값은 PASS=0, 문턱 초과=2, 실행 오류=1이다.
출력 디렉터리는 새 경로여야 하며, 실패/오류도 배포 불가다.

`kappa -mode guard -scheduler ... -profiles ... -records records.jsonl.gz -rows ...
-catalog catalog.jsonl.gz -arrivals arrivals.jsonl.gz [-next 3] [-format jsonl]`는
admission의 `SchedulerReductionWorkloads`를
동결한 뒤 `SchedulerReductionCommit`을 당시/다음 snapshot에 호출한다.
표는 at/status/target key/거부 여부·사유/수요와 keep·target κ를 포함한다.
`-mode compare -format jsonl`는 각 decision을 재평가하고 route 정수 weight,
instance ID, profile, CPU, batch와 wait까지 비교한다.
원본 `SchedulerRecord`는 workload/cost/baseline을 catalog 참조로 바꾸고 arrivals를
제거한다. runner는 stage의 `obs/*/catalog.jsonl*`을 서버의
`DecodeSchedulerLine`으로 복원하고, `requests.jsonl*`의 실제 ingress를
`requestArrival`과 동일한 accepted/compute 규칙으로 복원한다(assigned보다 final 우선,
native supplement 제외). 창은 `[observationEnd-observationWindow, observationEnd)`다.
참조/arrival이 누락된 원본을 빈 수요로 평가하지 않고 오류로 닫는다.
compare JSONL의 recordedCounts/replayCounts 및 populationMismatch를 함께 보존한다.
재구성 요청의 final 상태는 capture 순간과 다를 수 있으므로 mismatch는 감춰서는 안 된다.
수요는 max(minimumDemand, snapshot.scaleDown.rate), 공급은 Ready positive-route
인스턴스의 built row κ 합이다(target의 준비 전 인스턴스도 예상 공급에 포함).
이는 backlog/SLO 전체 판정이나 실측 처리율이 아니며 guard의 내부 거부 predicate를
추측하지 않는다. 기록된 stage의 setup/teardown을 포함한 모든 decision을 사용한다.
다음 tick 검증은 동결 target+기록 관측의 반사실 진단이며 completion callback,
실행기 owner 취소 또는 closed-loop 클러스터 시뮬레이션을 대체하지 않는다.
끝부분은 남은 tick까지만 검증한다. 클러스터 접근은 하지 않는다.
최소 회귀 검사는 `python3 -B hack/e5/replay-gate.py --self-check`와
`go test ./hack/kappa`다.


## Experiment controllers

New experiment Python starts under `hack/`. `hack/e5/` owns the campaign
controller and its analysis helpers, `hack/e5/d2/` owns the D2 binding,
conductor, cgroup sampler, native-mode request and offline analyzer, and
`hack/measure/` owns measurement preparation, generation and analysis.
CampaignOps owns testbed mutations and image publication.

Set `CHILL_EXPERIMENT_CONFIG` to an absolute external JSON file before invoking
these tools. Its top-level objects are `paths`, `d2`, `e5` and `measure`.
Optional `samples` holds complete environment-specific registration/measurement
documents keyed by their existing repository-relative sample paths; select them
explicitly using the [sample procedures](../config/samples/README.md#environment-specific-inputs).
E5 binds `d2.namespace` (default `chill-system`) and the selected experiment
policies into `binding.config`; later execution/analysis consumes that binding.
The [configuration ownership table](../docs/design/serving/configuration-hardcoding.md#슬라이스-5-구현과-기본값)
lists the stage/arrival/G0/timebox keys and their unchanged defaults.
`paths.repository` identifies this checkout; `paths.ops`, `paths.campaign` and
`paths.d2State` identify external operational state and retained evidence.
Each tool reads its section for machine identities, endpoints, credential-file
paths, kubeconfig and operational inputs. Keep credential contents in their
existing external files. Store results, raw records, backups and temporary
verification output outside the checkout.

Invoke repository entrypoints with absolute paths and retain the existing
explicit CLI inputs. For example:

```sh
export CHILL_EXPERIMENT_CONFIG=/path/to/external/environment.json
python3 /path/to/checkout/hack/e5/d2/analyze_before.py \
  --phase-dir /path/to/retained/phase --out /path/to/external/summary.json
python3 /path/to/checkout/hack/e5/d2/bind_before.py \
  --scheduler /path/to/scheduler.json --capacity /path/to/capacity.json \
  --nodes /path/to/nodes.json --pods /path/to/pods.json \
  --out /path/to/external/binding.json
```

`hack/measure/soak_ledger.py` is a CampaignOps-only manual ledger soak. Disable
all autonomous workloads and manually prepare 2–3 Ready instances owned by one
Workload before running:

```sh
python3 hack/measure/soak_ledger.py --self-check
CHILL_EXPERIMENT_CONFIG=/absolute/environment.json \
  python3 hack/measure/soak_ledger.py --workload-uid WORKLOAD_UID \
  --instances INSTANCE_ID_1 INSTANCE_ID_2 --out /external/soak-evidence \
  --duration 1800 --interval 20
```

The runner reuses the D2 conductor's authenticated API and kubectl helpers
(`d2` endpoint, CA/token and kubectl configuration; `measure.namespace` for
serving Pods). It cycles route/unroute/release, removes and re-prepares one
instance halfway through, and leaves the manually prepared instances in place.
The replacement target stays fixed: after its successful removal, a refused
prepare retries only preparation, never removal of another original instance.
It compares all scheduler instance Pod UIDs with the serving-label Pod list;
each mismatching UID must persist into the next cycle before it is reported.
After each completed ACK, the runner waits up to 129 seconds for a strictly
newer scheduler snapshot before another operation or invariant check; POOL
observations must also be newer than the ACK. The server wire timestamps are
`snapshot.at` and ACK `endedAt` (`observedAt`/`completedAt` are also accepted).
RFC3339Nano timestamps use the existing D2 parser for Python 3.8 compatibility.
Replacement reads the live instance's `profile.id` or retained `profileID`.
Ready flicker requires Pod Ready with no native failure/reason; otherwise the
transition is counted separately as `nativeUnavailable`, not a violation.
Removed-ID reappearance in fresh state or POOL is an immediate violation.
Operation request/ACK files, raw violation snapshots and
`summary.json` remain outside the checkout; violations do not stop the soak.
Exit status is nonzero if any violation or incomplete replacement was recorded.


`conduct_before.py` is an imported helper module, not a standalone command.
E5 and demo construct its `Conductor` without the retired D2 constructor and
provide the approved binding, output, authentication and journal context.
The shared API, publication, producer and cleanup helpers remain available.
The D2 cleanup helper, E5 finish and demo resume commands submit one explicit scheduler
`reset` with their captured Workload UIDs and a required reason, retain the returned
step/native ACK evidence, and stop on failure without a client retry or resume
state machine. The server owns autonomy removal, unroute/drain/removal, release,
and minimum supported idle-power readback. Cleanup does not recreate original
physical modes. Subsequent input-settings restoration excludes the read-only
`staticRouting` and `staticRoutingValidatedAt` fields; static selections use
`route`/`unroute`/`release` operations and preserve unrelated UID declarations.
Request lifetime is optional: an unset value omits `--request-lifetime`, rather
than synthesizing zero or a client deadline. Socket budgets remain explicit.
E5 and demo `publish --experiment-id ID --store-config /private/store.json`
upload private raw evidence first through the existing PutImmutable bridge, then
the final `serving/experiments.v1alpha1/ID/summary.json`. Existing IDs cannot be
overwritten; corrections require a new ID. `native_mode_request.py` remains the
separately approved baseline-preparation path, not cleanup sequencing.
`native_mode_request.py` consumes the explicit native-mode `--release` and
`--out`. `sample_cgroups.py` consumes an external host inventory plus
`--duration`, `--phase` and `--out`. The `smoke_*.py` scripts exercise retained
records and bounded local fixtures; the component-throttle smoke writes to
`d2.smokeOutput`.

E5 collects CHILL facts only through authenticated APIs using the collector
service-account token at `paths.ops/.token-collector`. After drain, resource-batch
support and the next closed-minute boundary, the raw exporter supplies
`definitions`, `requests`, `management`, `scheduler`, `catalog`, `resources` and
`cluster`; the collector builds `raw-source-index.json`. Settings `meters` and
state Node references feed the unchanged power integration, without ConfigMap,
S3, host filesystem or Kubernetes evidence reads.
Bindings record `collectorTokenFile` for evidence GETs and the existing
`tokenFile` for approved operator PUT/POST actions; collector RBAC grants no
writes. The additional collector processed-GET permissions, `requestTimeout`
and `podName` require the next approved deployment, not a live RBAC patch.

The external producer-only `client` cgroup sampler retains stage-entry identity,
start readiness and post-end counter release. STOP monitoring and operator E5-7
commands remain unchanged. Missing counters remain unknown, not zero. Capture
receipts (`capture-identity`, `s3-hour-list-*`, `upload.log`, `collector-status`,
`*-collector.stderr`) are no longer produced or judged.

Reports identify GC as `runtime-metrics-delta` (window cumulative CPU/cycle
difference, not a gctrace cycle distribution), and E5-7 recovery as
`scheduler`/`cluster` evidence. D1, latency and energy formulas are unchanged.
Never export raw observations or query historical dashboard data during an E5
measurement window. Historical publications are not rewritten.

`hack/e5/e5.py check-preparation --out /path/to/external/preparation.json`
checks the retained preparation inputs. `hack/e5/e5-wire.py execute` takes
the approved `--numbers`, `--release` and fresh external `--out` directory;
its archive, raw-collection and producer helpers live alongside it.

The approved numeric input is supplied externally; the retired exploratory
numeric-table CLI is removed. Original numeric inputs and published results
remain immutable. Kappa reads only current single-Workload snapshots.

For measurement preparation, `hack/measure/gen.py --output-dir` takes a fresh
external directory under the current measurement campaign.
`hack/measure/phase-a/measurement-generalization-20261024/post-d2-25-20261025/prepare.py`
takes `--output-dir` and supports `--check` and `--refresh-timing`.
`hack/measure/feeder.py --manifest /path/to/manifest.json --stages 7
--check-manifest` checks the generated manifest offline. Positional rerun and
unstaged lane modes are removed; approved holds, terminal checks and Node handback
still gate each lane. The
`meter-cadence/analyze.py` helper takes `--input-dir` for retained telemetry
and `--output-dir` for its external tables.

`hack/measure/smoke/smoke.py NODE OUTDIR WL=PROFILE:DATASET` retains prepare,
route and cleanup-reset requests/results in an external output directory.
A failed or partial reset propagates as an error and triggers no further
mutations; read-only workload observations (`observations-WL.json`) and
`scheduler-actions.json` are still collected. The offline regression is
`python3 -m unittest discover -s hack/measure/smoke -p 'test_smoke.py'`.

Record the repository harness commit alongside the actual control-image
source and immutable image identities in operational execution metadata.
CampaignOps switches active callers after the current conductor exits and
confirms that every consumer uses repository entrypoints. Historical receipts
and raw records retain their original provenance. At the approved E5 pilot
stage, run the existing `smokeSeconds` lifecycle through cleanup with the
repository controller, retain its logs and handback proof, and exclude that
smoke from scientific results before full E5.

### Short autonomous demo (not the normative E5 study)

`hack/e5/demo.py` reuses `CPO.start/run/collect/finish`, the existing producer
Job template, Poisson arrival generator/seeds, authenticated API, raw archive
collector and prebuilt power reducer. It does not call the R/G/10800-second
scientific binding/judgment gates. The example
`hack/e5/demo-config.example.json` selects E0/A/B/C and explicit request timing.
With no `e5Config`, `prepare` reads the existing environment's operational
recipe and captures scheduler state, nodes, pods, Ready serving-image digest
and gateway cap. It reuses `e5-wire.Prepare.canonical()` to create missing
canonical Workloads from the recipe's exact model/preprocessor/dataset
references and configured SLO, preserving existing definitions/SLOs without
PUTs. Registration ACKs and applied UID/generation/SLO are retained before
binding. It reuses `e5-wire.template()` plus existing D2 producer arguments and
explicit `requestTiming` overrides, saving `demo-e5-config.json` and
`binding.json`. This registration is CampaignOps-only, under Main's release.
`controlSourceSHA` is Main's helm-revision source claim, resolved in git and
kept distinct from the observed image digest; it is not inferred from image
bytes. `demoSkipped` names every bypassed scientific gate. An optional
`e5Config` can still name an already assembled raw E5 config.
Copy the example outside the repository if changing its inputs. Rates and
reporting-only `measurementSLO` are demonstration choices, not capacity claims.
Existing scheduler SLOs are captured and never changed; new canonical
definitions use configured SLOs. Lifetime/socket cover the actual applied
SLOs within the live gateway cap. No image builds occur.

CampaignOps alone executes live commands, with the existing safety monitor
running and all instances/autonomy/static routes initially empty:

```sh
DEMO=/absolute/path/to/checkout/hack/e5/demo.py
CONFIG=/absolute/path/to/external/demo-config.json
OUT=/absolute/path/below/paths.campaign/demo-run
RELEASE=/absolute/path/to/external/demo-release.json
python3 "$DEMO" prepare --config "$CONFIG" --out "$OUT"
# Main issues the existing CPO release JSON:
# {"executor":"CampaignOps","command":"e5-execute",
#  "bindingPath":"<OUT>/binding.json","sourceSHA":"<binding config approval.sourceSHA>",
#  "mainInstructionUTC":"<actual authorization UTC>"}
python3 "$DEMO" start --out "$OUT" --release "$RELEASE"
python3 "$DEMO" run --out "$OUT" --release "$RELEASE"
# Alternatively execute one configured stage at a time:
# python3 "$DEMO" stage --stage E0 --out "$OUT" --release "$RELEASE"
# then A, B, C. Every stage already collects before advancing.
# Show already collected evidence, or collect a drained, not-yet-captured stage:
python3 "$DEMO" collect --stage C --out "$OUT" --release "$RELEASE"
python3 "$DEMO" summary --out "$OUT"
python3 "$DEMO" finish --out "$OUT" --release "$RELEASE"
```

E0 runs without producers after autonomy is enabled. Before the first loaded
stage the driver requires each canonical Workload's owned Ready positive route.
Each loaded stage gets new producer Jobs; the measured gap from the previous
offered window includes drain, collection and setup. No requests are offered in
that gap, so the demo cannot measure its request failures. STOP stages retain
partial evidence; there is no implicit retry. `finish` restores original
settings/modes using the existing owned-unroute/remove/publication contract,
then deletes exact owned producer Jobs. Only after that cleanup completes,
it deletes the three captured canonical definitions using exact UID/generation
checks and `If-Match` resource versions, retaining ACKs and waiting for actual
definition disappearance.

Each `smoke-<stage>/` contains pre/post state, per-Workload `*-client.jsonl`,
producer lifecycle evidence, raw collection, `power*.json` and `summary.json`.
Top-level `summary.json` and `summary.md` combine stages. HTTP success is
status 200 / frozen offered count; p95/p99 are nearest-rank successful-client
round-trip latency, compared with configured `measurementSLO` (or captured
scheduler SLO when omitted), not server execution latency. Both criteria and
the unchanged scheduler SLO are retained in each workload summary.
Pool/active/drain energy comes from existing complete power integrals and is
shared, never attributed to workloads. Placement events compare raw scheduler
snapshots and boundary snapshots (instance creation/deletion/node/profile/
allocation, node modes and routing); event times are observed timestamps with
the preceding observation retained, not exact native creation times.
Transition failures count failed/unsent client requests overlapping those
observation brackets or raw action `startedAt..completedAt` intervals.
Incomplete scheduler/client evidence is unknown, not zero.
The scheduler's explicit JSON `results: null` is an empty Go result slice for
consumer iteration/concatenation; retained raw state keeps the original null.
Demo Jobs omit `--workload-slo` and `--gateway-timeout`, which the pinned D2
producer predates; prepare still checks actual SLO/lifetime/gateway/socket
budgets on the host. Normative E5 Jobs retain their flags. Self-check compares
generated demo options with immutable e18ac/4fb CLI parser option sets using
AST, without running an image. On producer failure, exact Job-owned Pod logs,
stderr and identity receipts are saved under `startup-<workload>/` before
owned Job deletion, including failures before log attachment.
Final producer cleanup skips a retained creation record only when its phase
already records `stopped: true` for that exact Job name and UID. Missing or
mismatched deletion ACKs retain the existing exact-UID fail-closed behavior.
Demo stage boundaries and resume wait only for zero scheduler outstanding
requests and zero per-Workload pending requests. They retain
`demoRequestDrain.inProgressTarget/actions` and `request-drain-state.json`
without waiting for the planner to become idle. Resume additionally requires
owned Ready instances and positive routes. Normative E5 and final owned
cleanup retain their original global-idle waits.

`tolerateArchive` is **diagnostic-only (진단 전용, E5 금지)** in `demo.py`.
Its top-level or stage-level list may name only `power` and
`power-drain-inclusive`; a stage list overrides the top-level list (`[]`
restores strict capture). Missing archives retain original NOT_READY evidence,
explicit `missingArchives` and null energy, never a successful energy result.
Normative `e5.py` rejects the key anywhere in its config or binding, including
empty/false/null values, rather than silently ignoring it.

For an otherwise completed producer window stopped only by the former
planner-idle timeout, `collect --stage A` can collect without replay. It requires
all original start/drained/end-counter and terminal Complete proofs. Original
`phase.json` (including STOP), client logs and offered start/end stay unchanged;
a separate `collected-phase.json` records the original failure and the actual
late request-drain observation, with `recovery-post-*` boundaries. Only a
successful collection makes this derived phase eligible for the next stage.
Reports explicitly label recovery and the longer drain-inclusive energy
interval. After collection, use `resume --stage B` if failure restored autonomy
off, then stage B/C, summary and finish; do not replay A.

After an explicitly released stage failure, CampaignOps preserves the failed
stage directory and explicitly re-freezes changed harness source references;
there is no automatic retry or source refresh. `demo.py resume --out "$OUT"
--release "$RELEASE"` reuses the existing settings PUT to enable canonical
autonomy, adopts only owned current instances and waits for Ready positive
routes using the same `CPO.missing_ready_routes` predicate as bootstrap and the
Ready gate. It preserves original `initial.json` and `start-ack.json`, recording a
new `resume-*/resume-ack.json`. If no instances remain, it first reuses the
configured zero-load E0 through CPO.run in a fresh `smoke-E0-resume-*/` folder
(180 seconds in the example), preserving original E0 results. Then execute
`stage --stage A`, B, C, collect, summary and finish with the same out/release.

Fresh demo `finish` submits one server `reset` for the owned Workload UIDs and
retains its full ordered step/result evidence before restoring input settings
or deleting definitions. Active global routing or affected-Node operations
are refused before mutation; the client does not assemble cleanup, retry,
rollback, or resume an old unroute acknowledgment. A failed reset stops later
steps and preserves the observed partial state. A later explicit reset reads
current state and skips already-satisfied changes.

New campaign/demo bindings freeze the full `schedulerSettings`, including
per-UID `minimumDemand`. JSON summaries and immutable published stage KPIs
retain that frozen input, not current settings. Older bindings without it show
null and an explicit missing reason; their immutable records are not rewritten.

`python3 hack/e5/demo.py --help` and `--self-check` work without environment
configuration. The latter runs the real CPO producer stage loop against local
pipes and mocked API/kubectl, with no cluster access, image builds or full tests.

`python3 hack/e5/e5.py self-check` needs `CHILL_EXPERIMENT_CONFIG` pointing at an
environment JSON (module constants) but makes no cluster or network calls.

### One-command E5 round

`python3 -B hack/e5/round.py --round /absolute/private/round.json` runs the
released server-only upgrade, initial owned reset, headroom PUT/readback, κ,
ordered factor → headroom GET → stage/first-load-tick check → judge → guards →
cleanup, comparison tables, and final finish/empty-state readback. It reuses
`e5.CPO`, `e5.py` commands, token lifetime checks, and table formatters; raw
latency/D1/energy judgments are not recomputed or weakened. Run only outside an
active measurement window and under an actual Main-approved release.

The private input JSON has these fields (all filesystem paths are absolute;
replace the illustrative paths/SHAs with actual approved inputs):

```json
{
  "sourceSHA": "0123456789abcdef0123456789abcdef01234567",
  "rows": ["E5-5", "E5-7", "E5-6", "E5-3"],
  "rowTag": "9",
  "restoreOriginTag": "223",
  "h": 0.25,
  "rateBasis": {"tag": "3", "binding": "/ops/campaign/run/binding-3.json"},
  "compareTags": ["3", "7"],
  "environment": "/ops/campaign/inputs/environment.json",
  "binding": "/ops/campaign/run/approved-input-binding.json",
  "release": "/ops/campaign/run/release-round-9.json",
  "run": "/ops/campaign/run",
  "evidence": "/ops/deployB/round-9",
  "control": "265",
  "publication": "/ops/prebuilt/server-publication.json",
  "minimumFreeBytes": 310000000000,
  "kappa": {"reuseRows": "/ops/deployB/prior/kappa-rows.json"},
  "takeoverAfter600Seconds": false,
  "failureInjection": true
}
```

`environment.paths.ops` is the actual external ops root, **not** literal `/ops`.
`run` is the existing campaign containing original snapshots and `initial.json`.
Do **not** create `evidence` or any `full-E5-*-TAG` directories: the runner and
E5 controller own creation. The runner writes `run/binding-round-9.json` from
the supplied binding, preserving R3 payloads, seeds, rates, traces, and κ while
freezing the selected harness package references and new server provenance.
The pre-existing approved release must name that exact generated `bindingPath`,
`executor: "CampaignOps"`, `command: "e5-execute"`, `mainInstructionUTC`, and
explicit `sources: {server, loadgen, power}`; legacy single `sourceSHA` releases
are rejected. It never fabricates approval. The server publication must have
the matching SHA, only `roles.server`, verified digest/platform receipts, and
`runtimeBuilds: 0`; fast-deploy uses `--prebuilt-images` and never builds on the
host. A matching live digest skips upgrade, not Ready/API/restartCount gates.
Any chart drift stops; the runner does not accept chart-change approval.

Optional `harnessSHA` selects a different full commit for the frozen
`ops/harness/e5-<sha>` tree (default `sourceSHA`). Missing trees are created
atomically from `git archive`; existing Python files are compared with that
commit before use. Bind micro schedules beforehand; rows `E5-{3,5,6,7}m` must
carry `micro: true` and `restoreStage` in the supplied binding, with their own
bound failure runbook for E5-7m. Original micro restore snapshots use tag 223.
For normal E5-3, the historical default is the unsuffixed `full-E5-3`; other
rows use `restoreOriginTag`. Override with e.g.
`"restoreOrigins": {"E5-3": "223"}` when the actual original exists there.
R3 comparisons default to unsuffixed E5-3 and E5-7 tag `3b`; explicit
`"compareOrigins": {"3": {"E5-7": "3b"}}` maps historical exceptions.

To replay κ once, use `"kappa": {"executable": "/absolute/prebuilt/kappa"}`.
Its fixed argv is `-mode table -workloads
image-classification-tight-slo,smolvlm2-vqa,plc-anomaly -margin 29 -window 60
-windows 20 -passing 19 -workers 3 -scheduler <capture> -rows <output>`.
The result is recorded, never applied to R3 offered rates. `reuseRows` instead
records a hash/reference and zero replays. Both require positive Built rows
for all canonical workloads.

Preflight renews insufficient-lifetime/expired credentials using only the
existing `chill-dashboard-user` and `chill-e5-evidence-collector` service
accounts in the configured namespace. `.token`/`.token-collector` are replaced
atomically with mode 0600; originals remain in `.bak` (later backups receive a
timestamp). Tokens never enter evidence or argv. Initial reservation/action
wait is 600s; only `takeoverAfter600Seconds: true` authorizes one direct reset
after the original timeout evidence is preserved. No other recovery or retry
is implicit. E5-7 requires `failureInjection: true`, executes only fresh
`failure-select` operator argv, and retains actual commands/results/timestamps;
STOP does not silently uncordon or reset physical state.

Each attempt retains its own STOP, command logs, tables/comparison JSON/text,
and final summary under `evidence/attempt-*`; `round-state.json` tracks completed
steps and effective row tags. Disk, restartCount/Pod identity and deployment
template guards stop during load as well as at row boundaries. Missing rows
are UNMEASURED, never zero or PASS. Final success requires instances 0,
reservations 0, autonomy off. `finish` restores initial settings, so the summary
records actual final h, including h=0; another round always PUTs h again.

Resume explicitly with the same JSON and `--resume-from 0..7` (also
`preflight`, `deploy`, `reset`, `headroom`, `kappa`, `rows`, `tables`, `finish`).
Row boundaries use `--resume-from 5:E5-6`; individual substeps accept
`5:E5-6:factor|headroom|stage|judge|guard|cleanup`. Incomplete prerequisites
cannot be skipped. A pre-load input correction can use
`--resume-from 5:E5-6 --next-row-tag` to increment the suffix for that row and
remaining rows, preserving previous attempts. A row with `startUTC` can
**never** be replayed by this option; a new tag is not measurement-retry
authorization. Existing factor/reset/stage evidence is never adopted as an
implicit retry. Correct changed top-level JSON in a separately released round
rather than rewriting prior round history.

Use `--until <step>` to exit successfully after that step, recording its canonical
identifier in `round-summary.json` as `stoppedAfter`. It accepts the same names
and substeps as resume; `5`/`rows` includes every configured row, and `5:E5-6`
includes that row's cleanup. The boundary cannot precede `--resume-from`.
For example, `--until 1` stops after deployment; continue with the identical
round JSON and `--resume-from 2`. Early stops do not run later steps or `finish`
and therefore do not imply final empty-state verification.

`python3 -B hack/e5/round.py --self-check` checks input validation, ordering,
STOP boundaries and compressed judgment projection entirely offline, without
environment configuration, Go, image builds, deployment or network requests.


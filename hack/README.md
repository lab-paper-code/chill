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

## Experiment controllers

New experiment Python starts under `hack/`. `hack/e5/` owns the campaign
controller and its analysis helpers, `hack/e5/d2/` owns the D2 binding,
conductor, cgroup sampler, native-mode request and offline analyzer, and
`hack/measure/` owns measurement preparation, generation and analysis.
CampaignOps owns testbed mutations and image publication.

Set `CHILL_EXPERIMENT_CONFIG` to an absolute external JSON file before invoking
these tools. Its top-level objects are `paths`, `d2`, `e5` and `measure`.
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

`conduct_before.py` takes the exact approved `--command`, `--binding`,
`--release`, `--out`, `--phase` and, for collection, `--hosts` inputs.
The `autonomous` command starts from the prepared eight-instance binding.
The `autonomous-empty-start` command records a fresh empty lifecycle, enables
the target Workload, and waits within the existing 194-second preparation bound
for owned Ready instances, positive target routing and the existing global
idle conditions before creating the producer Job. It uses the same autonomous
rate, 660-second replay and producer release barrier. Its release names
`command=autonomous-empty-start` and the actual deployed control `sourceSha`;
execution metadata records the repository harness commit separately.
Both commands retain diagnostic raw records before owned Job deletion.
The D2 cleanup and E5 finish commands consume the recorded lifecycle. Their
empty owned routing declarations hold draining instances through removal,
empty publication and owned Pod disappearance. Final settings restoration
releases owned routing keys and submits an explicit `staticRouting: {}` when
the remaining declaration is empty. Other Workload routing and explicit empty
stop declarations retain their values. Native mode return
uses the separately approved native-mode release and its 900-second bound.
`native_mode_request.py` consumes the explicit native-mode `--release` and
`--out`. `sample_cgroups.py` consumes an external host inventory plus
`--duration`, `--phase` and `--out`. The `smoke_*.py` scripts exercise retained
records and bounded local fixtures; the component-throttle smoke writes to
`d2.smokeOutput`.

`hack/e5/e5.py check-preparation --out /path/to/external/preparation.json`
checks the retained preparation inputs. `hack/e5/e5-wire.py execute` takes
the approved `--numbers`, `--release` and fresh external `--out` directory;
its archive, raw-collection and producer helpers live alongside it.

For measurement preparation, `hack/measure/gen.py --output-dir` takes a fresh
external directory under the current measurement campaign.
`hack/measure/phase-a/measurement-generalization-20261024/post-d2-25-20261025/prepare.py`
takes `--output-dir` and supports `--check` and `--refresh-timing`.
`hack/measure/feeder.py --manifest /path/to/manifest.json --stages 7
--check-manifest` checks the generated manifest offline. The
`meter-cadence/analyze.py` helper takes `--input-dir` for retained telemetry
and `--output-dir` for its external tables.

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
`hack/e5/demo-config.example.json` points to an existing raw E5 config for all
Workload UIDs/SLOs, endpoints, datasets, templates, timeboxes and collection.
Copy it outside the repository, set `e5Config` to that existing absolute path,
and choose actual offered rates (example values are illustrative, not a
capacity claim). Do not rebuild images.

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
#  "bindingPath":"<OUT>/binding.json","sourceSHA":"<existing config approval.sourceSHA>",
#  "mainInstructionUTC":"<actual authorization UTC>"}
python3 "$DEMO" start --out "$OUT" --release "$RELEASE"
python3 "$DEMO" run --out "$OUT" --release "$RELEASE"
# Alternatively execute one configured stage at a time:
# python3 "$DEMO" stage --stage E0 --out "$OUT" --release "$RELEASE"
# then A, B, C. Every stage already collects before advancing.
# Explicit collection of retained completed intervals, if needed:
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
then deletes exact owned producer Jobs.

Each `smoke-<stage>/` contains pre/post state, per-Workload `*-client.jsonl`,
producer lifecycle evidence, raw collection, `power*.json` and `summary.json`.
Top-level `summary.json` and `summary.md` combine stages. HTTP success is
status 200 / frozen offered count; p95/p99 are nearest-rank successful-client
round-trip latency, compared with the frozen SLO (not server execution latency).
Pool/active/drain energy comes from existing complete power integrals and is
shared, never attributed to workloads. Placement events compare raw scheduler
snapshots and boundary snapshots (instance creation/deletion/node/profile/
allocation, node modes and routing); event times are observed timestamps with
the preceding observation retained, not exact native creation times.
Transition failures count failed/unsent client requests overlapping those
observation brackets or raw action `startedAt..completedAt` intervals.
Incomplete scheduler/client evidence is unknown, not zero.

`python3 hack/e5/demo.py --help` and `--self-check` work without environment
configuration. The latter runs the real CPO producer stage loop against local
pipes and mocked API/kubectl, with no cluster access, image builds or full tests.

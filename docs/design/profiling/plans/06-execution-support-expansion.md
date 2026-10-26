# Plan 6: Execution-Support Expansion

> Navigation: [Profiling design index](../README.md)
>
> Row status: `BatchEnergyOptimization` status and the Run evidence it references

## Status

Active implementation plan. Raspberry Pi 5 CPU ORT is L2 pending a physical
power-supply correction, and Orange Pi CPU ORT is L3. The recorded Orange Pi
RKNNLite campaign used one artifact for three active Runs at each of batches
one, two, and four plus three batch-one model-loaded-idle Runs. All 12 trials
were accepted, and the campaign produced `Stopped/NoFurtherImprovement`;
production reconstruction matched every stored incremental-work value exactly.
Its execution evidence uses v1alpha4, and the campaign and Run objects are
archived outside the testbed Evidence Store and cannot be reloaded by the
strict current readers. The recorded L4 result is not current live or
re-verifiable evidence and does not establish a universal `b_sat=1` or
statistical confirmation.
[ADR 0016](../../../adr/0016-declared-batch-size-artifact-capability.md) governs
the declared-batch-size artifact capability used by future campaigns.

The NodeActuator transition path is implemented under
[ADR 0018](../../../adr/0018-transition-ownership-boundary.md). A
`NodeActuation` records one state change toward a declared target, independently
reads that target back, settles it, releases the Node Lease at terminal `Ready`,
and leaves the established state in place. Execution evidence uses
`chill.dacs.io/profiling-execution-evidence.v1alpha14`, retains the confirming
transition identity, target observation, and completion time, and validates
the target against RunConfig. A permanent semantic contradiction produces
terminal `EvidenceVerified=False` with reason `EvidenceInvalid` instead of an
error requeue.
[ADR 0017](../../../adr/0017-power-mode-reachability-boundary.md) makes every
catalogued mode reachable, restarting the Node into one the device establishes
at boot, so the AGX Orin power-mode axis is four points rather than two.
[ADR 0015](../../../adr/0015-node-power-actuation-boundary.md) remains
authoritative for the sole-writer, safety, and host-adapter boundary.

One `jetson-agx-orin-64g` CPU ORT comparison measured
`mobilenet-v2-050`, batch one, for 75 seconds, with three admitted active and
three admitted model-loaded-idle Runs in each state, and recorded these
aggregates:

| Power mode | `P_idle` (W) | `P_act` (W) | Throughput (items/s) | Incremental energy (J/item) |
|---|---:|---:|---:|---:|
| `MODE_50W` | 10.3989 ± 0.0015 | 19.151 ± 0.072 | 200.18 ± 0.33 | 0.04372 |
| `MAXN` | 10.4323 ± 0.0147 | 30.326 ± 0.078 | 265.50 ± 0.49 | 0.07493 |

They give `MAXN` 1.326x the throughput at 1.714x the incremental energy per
item relative to `MODE_50W`, while mean and p99 latency fall to 0.754x and
0.763x. The idle means differ by 0.0334 W, so the knob is not characterizable
without load. Both exact power-mode slices have recorded L3 results; their
source Runs use superseded v1alpha4 and are archival rather than
current-code-reloadable proof. They contain Run measurement evidence rather
than the separate transition evidence governed by ADR 0018, and neither slice
has the multiple-batch evidence required for L4. No result transfers to another
Node, model, execution path, batch size, RKNN path, GPU path, or TensorRT row.

The testbed Evidence Store holds current-schema objects only. Measurements
whose source objects use superseded schemas remain archival scientific records,
not live or current-code-reloadable proof. Later slices remain deferred until
their stated entry condition is met or a separately approved scope authorizes
them.

This file owns work order, decisions, and completion conditions. Exact row
level and blocking reason belong in the support matrix. Live execution,
verification, and admission results remain in the immutable evidence object
referenced by `ProfilingRun.status.evidence` and in the Run status.
`kubectl chill runs show NAME` joins those sources into an Evidence Passport at
read time; do not copy them into design Markdown.

## Question

> Which exact DeviceClass, model, artifact, runtime/backend, power mode, and
> workload-mode rows does production support, and what is the next smallest
> vertical slice that raises one row to measurement-ready?

## Support levels

| Level | Required evidence |
|---|---|
| `L0 Catalogued` | A live Node is discovered into the exact `DeviceClass`. Repository catalog presence alone is recorded separately. |
| `L1 Compatible` | The exact artifact and trusted runtime-image declaration evaluate `Compatible` for the class. |
| `L2 Executable` | A production `ProfilingRun` Job succeeds on the exact Node, runtime path, and power mode. |
| `L3 Measurement-ready` | Both active and model-loaded-idle evidence are durably reloaded, verified, and admitted. |
| `L4 Campaign-ready` | Repeated trials and multiple batches can be produced for one verified curve plan under Plan 5 or 5a, with the shared artifact execution state retained exactly. |

Levels are cumulative. Catalog presence, standalone smoke tests, and successful
Jobs without admitted active and idle evidence do not promote a row beyond the
corresponding level.

## Support matrix and queue

| Order | Slice | Recorded level | Current standing or next gate |
|---|---|---|---|
| paused | Raspberry Pi 5 CPU ORT / fixed | L2 | Correct the electrical condition before collecting new campaign evidence. |
| completed | Orange Pi CPU ORT / fixed | L3 | Retained active and model-loaded-idle evidence is admitted under stable derivation identities. |
| completed | Orange Pi RKNNLite / declared-batch-size base slice | L3 foundation | The admitted active and model-loaded-idle result is archival; its superseded-schema objects are not reloadable by current code. |
| completed | AGX Orin CPU ORT / MODE_50W | L3 | Three active and three model-loaded-idle Runs were admitted at batch one; their v1alpha4 evidence is archival, and multiple-batch evidence would be required for L4. |
| completed | AGX Orin CPU ORT / MAXN | L3 | Three active and three model-loaded-idle Runs were admitted at batch one; their v1alpha4 evidence is archival, and multiple-batch evidence would be required for L4. |
| completed | RKNNLite model and batch expansion | L4 | One artifact produced 12 accepted trials across active batches one, two, and four and batch-one model-loaded idle, ending `Stopped/NoFurtherImprovement`; a current-schema campaign is required for live re-verification. |

## Stable decisions

- The row identity is the exact DeviceClass, ModelSpec, immutable artifact,
  execution path, runtime/backend, power mode, workload mode, and measurement
  policy recorded in the support matrix.
- The first model remains `mobilenet-v2-050` until its owning execution path
  reaches L3.
- CPU ORT, RKNNLite, ORT CUDA, and ORT TensorRT remain explicit adapters. Do
  not introduce a generic runtime plugin or device-specific copy of CPU ORT.
- Stable supported power modes belong in `DeviceClass`; the Run declares its
  required mode, while observed target and previous mode belong to the
  transition.
- [ADR 0015](../../../adr/0015-node-power-actuation-boundary.md) assigns
  Node-wide mutation exclusively to a dedicated per-Node actuator operand;
  Profiler orchestrates the durable Run lifecycle, while discovery and the
  measurement worker remain unprivileged.
- Exact runnable child OCI image references, not manifest-list references, are recorded in
  RunConfig runtime, artifact, MetricObserver, and WorkloadObserver facts.
- Execution-path names select catalog entries and configured envelopes; the
  realized runtime family/backend selects the production adapter. A
  declared-batch-size artifact declares its planned batch size set for active RunConfigs and uses
  the first declared batch for its model-loaded-idle RunConfig, while idle still
  performs no inference.
- Installed facts are configuration. A row advances only from retained
  production evidence at the applicable level.
- The AGX Orin CPU ORT MODE_50W rows are a bounded actuation-proof substrate.
  They reuse the shared arm64 CPU ORT implementation but neither implement nor
  promote the separately tracked ORT TensorRT rows.

## P1: Raspberry Pi 5 CPU ORT / fixed — paused

The implementation, chart facts, metric source, thermal producer correction,
and live L2/idle-admission proof are complete. Do not weaken admission or pool
pre-repair evidence.

- [ ] Correct the `rasp5` power supply/cable condition and prove
  `get_throttled=0` throughout one 30-second active diagnostic.
- [ ] Run one clean active `ProfilingRun` and require execution complete,
  evidence verified, and admission accepted before paying for repetitions.
- [ ] Under the unchanged post-repair RunConfig and environment identities, run
  three new active and three new model-loaded-idle trials sequentially.
- [ ] Reload all six retained evidence objects, compare identities within and
  across modes, and promote the two matrix rows to L3 only if all admissions
  pass.

Completion: `raspberry-pi-5-8g / mobilenet-v2-050 / ort-cpu / fixed` is L3 for
both workload modes on current HEAD.

## P2: Orange Pi CPU ORT / fixed — completed

Reuse the explicit arm64 CPU ORT path. Device-specific work remains limited to
installation facts and a justified execution policy until source evidence
proves a shared implementation change is necessary.

- [x] Add and install the exact runtime and artifact declarations.
- [x] Add and install the Node-specific metric source; record the current
  physical wall-meter association as live corroboration, not a configured identity.
- [x] Verify the direct CPU thermal/throttle channel against live raw sysfs and
  the fail-closed admission contract.
- [x] Bind CPU request, ORT thread count, runtime memory, and workspace to the
  live Node's allocatable resources without claiming CPU exclusivity.
- [x] Run and admit three active and three model-loaded-idle Runs.
- [x] Confirm that Raspberry Pi and Orange Pi use the same CPU ORT production
  path, with device differences confined to installed facts and Run resource
  configuration.
- [x] Keep the demonstrated shared behavior in the existing CPU ORT adapter;
  no device-specific Go behavior required extraction.

Completion: `orangepi-5-plus-16g / mobilenet-v2-050 / ort-cpu / fixed` is L3.

## P2 parallel: Node power-mode actuation

### Ownership and protocol

- [x] Select the dedicated per-Node `NodeActuator` operand as the only process
  allowed to mutate host mode; the Profiler orchestrates and the runtime worker
  remains unprivileged.
- [x] Retain previous and target mode observations; actuator kind/target;
  policy identity; lifecycle stage; microsecond-resolution apply, verification,
  settling, and ready timestamps; and the successful state change's end-to-end,
  apply-and-read-back, and settling durations in integer milliseconds in the
  durable transition.
- [x] Record the selected target and configured actuation-policy name in
  RunConfig.
- [x] Keep the current RunConfig and Go/Python decoders on one strict schema
  before wiring any selectable-mode producer.
- [x] Define one independently submitted durable transition and verify its exact
  local-Node consumer projection before host mutation.
- [x] Define and test the Node-UID-keyed Lease acquire, renew, conflict,
  expiration takeover, and exact-holder release primitive; keep the production
  serialization gate open until the actuator controller consumes it.
- [x] Fix the pure transition order with independent target read-back; no
  command result directly proves state.
- [x] Implement the internal status reconciler with an exact local-Node filter,
  a durable pre-mutation fence, independent read-back, and one terminal
  outcome; keep its production gate open until an operand wires it.
- [x] Actuate regardless of Pods on the target Node; measurement results record
  the Node's Pods and whether CHILL placed each one
  ([ADR 0022](../../../adr/0022-workload-pool-membership-boundary.md)).
- [x] Reject any transition whose actuation-policy name is absent from
  the actuator's explicit authorization set before crossing a Lease or host
  port.
- [x] Add an opt-in command and Operator-owned/Helm-configured DaemonSet
  lifecycle with exact Node/self-Pod identity, shared Go policy naming,
  scoped API permissions, and a stop gate that retains the operand for every
  unfinished transition or unreleased Lease.
- [x] Wire the first Jetson NVPModel adapter with a bounded configured argv and
  explicit narrow host mounts; keep the chart disabled until live apply and
  read-back proof closes the mutation gate.
- [x] Serialize every runnable transition with the exact Node-UID Lease;
  terminal `Ready` and `Failed` release that Lease.
- [x] Admit every authorized transition before any host operation without a
  per-Pod admission check.
- [x] Fix `apply -> read-back -> settling -> Ready` as the transition lifecycle
  and require a matching completed transition before measurement.
- [x] Reconstruct the durable transition after controller restart without
  introducing a recovery transition.
- [x] End a Run with `RequiredStateNotConfirmed` without a Job when no successful
  transition confirms its required state.
- [ ] Connect that terminal condition to an operator alert sink.
- [x] Admit one exact AGX Orin CPU ORT MODE_50W active Run after verified
  target settling.
- [x] Admit one exact model-loaded-idle Run through the same actuation and
  target-read-back boundary.
- [x] Complete two additional active and idle trials and verify stable
  derivation identities across all six retained objects.

The current RunConfig v1alpha14 retains the optional `actuation` object with the
selected power mode, exact typed actuator target, and configured-policy name,
records the configured power and typed thermal metric identities, and binds
either the Node-allocatable or explicitly configured whole-core CPU quota.
Fixed Runs omit the actuation object. The current execution-state-identity v1alpha5
binds both the exact metric mapping and, for an actuated Run, the whole
actuation object while leaving measurement-protocol identity unchanged. Older
schemas are not accepted by the current execution boundary. CPU ORT and
RKNNLite Go/Python decoders pair only with current RunConfig v1alpha14. The candidate
report carries the exact
selected target. The Profiler selects a configured policy by exact DeviceClass,
mode, and actuator kind, binds its name into RunConfig, and retains its typed
record and ConfigMap source in the owned Job. Its first policy fields bind
settling and maximum actuation duration.
A non-fixed Run lists successful completed `NodeActuation` objects in its
namespace for the exact target Node name and UID and selects the greatest
`status.readyAt`. The transition's class, mode, typed actuator target, and
target read-back must match RunConfig. The Run records the selected name
and UID before creating its measurement Job. If no completed transition exists,
or the newest successful transition established another mode, the Run ends
with `RequiredStateNotConfirmed` without creating a Job. Fixed Runs execute
directly. The Run never creates, owns, or repairs a transition.

This confirmation can be stale and that limitation is accepted. Nothing else
changes Node power state on this testbed, and operating procedure allows no
transition during a measurement sweep. No re-observation, epoch, fence, claim,
state-generation counter, or tenure protocol enforces the assumption. Until a
campaign emits transition atoms, each required `NodeActuation` is submitted
manually.

The transition keeps Node UID, selected class/mode/target, policy name, policy
timing, request timestamp, and absolute deadline immutable in `spec`; the
actuator alone owns previous and target observations, phase, timestamps,
successful transition cost, Lease identity, and terminal failure in `status`.
Its consumer projection rejects a foreign Node name or UID before any Lease or
host adapter can receive the request.

The internal layer keeps Node locking and lifecycle decisions separate
from host tooling. Its Lease name is derived from exact Node UID, holder
identity from exact handoff UID, and takeover is allowed only after a trusted
renew/acquire timestamp plus configured duration expires. Release uses object
preconditions and refuses a foreign holder. The pure lifecycle requires
`observe previous -> compare target -> apply only when different -> settle ->
Ready`; deadline, target mismatch, and port failure all
produce one terminal failure without a recovery chain. `Ready` and `Failed`
release the Lease, and returning to an earlier state requires another
`NodeActuation` with that state as its target. The internal status reconciler
drives these primitives from durable `NodeActuation` state. It records the
apply phase before invoking the host port, separates command completion from
target read-back, and reconciles Lease status after restart. The reconciler
independently requires the handoff's policy name to be
in its installed authorization set before crossing any port. The runnable
operand consumes the Lease and lifecycle paths. The
archived AGX Orin campaign contains apply/read-back and restoration evidence
under a superseded stored contract; it does not prove the current v1alpha9
one-way lifecycle. Live validation of the current contract and bounded host
failure injection remain open.

The runnable layer composes the controller with installation-owned host access.
Helm renders a disabled-by-default ConfigMap, ServiceAccount,
namespaced handoff/Lease Role, cluster-scoped Node/Pod read permissions, and the
`ChillSystem` opt-in reference. The Operator owns the per-eligible-Node
DaemonSet, reports its availability, and refuses disable or system finalization
while any transition is non-terminal or still records a held Lease. The command
resolves exact Node UID from the API, excludes only its own downward-API Pod UID
in addition to explicitly configured infrastructure namespaces, and derives
authorized policy names through the same validated `internal/nodeactuator`
type used by the Profiler. Its Kubernetes client reuses the shared explicit
endpoint plus projected ServiceAccount token/CA path so KubeEdge edge Pods do
not depend on missing Kubernetes service environment variables. The Jetson adapter invokes the configured host
`nvpmodel` argv with a bounded deadline, parses the query's exact name and ID,
and never treats an apply exit as convergence. The chart mounts only the
declared binary, runtime libraries, NVPModel configuration/state, and sysfs;
it does not request privileged mode or added capabilities. The exact AGX Orin
MODE_50W measurement result is archival; no current-contract live proof or
other Node, mode, or runtime inherits that result.

### Jetson NVPModel

- [x] Read-only probe every Ready Jetson's complete NVPModel ID/name set.
- [x] Compare each reachable host with its Helm catalog row and correct every
  mismatch before enabling actuation.
- [ ] Recover `orin-nano`, then probe and compare its complete NVPModel set;
  neither Kubernetes nor the configured SSH path could reach it during the
  2026-08-06 audit.
- [x] Implement explicit bounded `Apply(ID)` and `Observe()` operations.
- [x] Judge success in the controller by a separate parsed name/ID read-back,
  never command exit alone.
- [x] Retain the exact Run and confirming-transition identities, target mode
  observation, and transition completion time in finalized execution evidence.
- [x] Configure the post-read-back settling duration in installation policy.
- [ ] Define a thermal-stability criterion only if non-throttled admission and
  the configured settling duration are insufficient; elapsed settling time does
  not itself prove thermodynamic equilibrium.
- [x] Test read-back mode drift, Lease conflict, and partial apply failure in
  the pure and controller paths.
- [ ] Run bounded live failure injection for an unsupported NVPModel ID and a
  target read-back mismatch; local error-path tests do not prove host behavior.

### GPU clock

Contract: [PA-NV-01–05](../../../spec/platform/SRS.md), ADR 0015 and ADR 0020.
ADR 0020 supersedes the archival per-Run/Lease protocol above: the Node's
desired-mode label plus requested-at annotation is the durable request;
returning to an earlier mode is a separate request.

#### Read-only host inspection (2026-10-27)

SSH probes ran only `hostname`, `id`, `nvidia-smi -q -d CLOCK,SUPPORTED_CLOCKS`,
`nvidia-smi -q -x`, query/help, `stat`, and `sudo -n -l`; no mutation ran.
Raw output is retained under `O/u3-nvidia/` as
`genesis2-readonly-inspection.txt`, `genesis3-readonly-inspection.txt`, and
`genesis2-readback-permissions.txt` (full query/help/permission output).

| Host | GPU UUID | Driver | max graphics/SM | default / min / max power |
|---|---|---|---|---|
| genesis2 | GPU-7ba62428-ecac-9bd9-a546-40e2a748daed | 555.58.02 | 2100 MHz | 250 / 100 / 280 W |
| genesis3 | GPU-24ddd1e2-68a6-ca12-34d6-448c2697f325 | 555.58.02 | 2175 MHz | 300 / 100 / 360 W |

Both are single RTX 2080 Ti / Turing, max memory 7000 MHz. Both supported-clock
lists include 1500, 1200, 900 MHz (15 MHz steps); idle graphics=SM=300 MHz.
Application/default application clocks return N/A. `--help` exposes
`-lgc min,max`, optional `-m 0|1`, and `-rgc`; host users have passwordless
sudo, and `/dev/nvidia0`/`nvidiactl` are 0666. This proves tooling/permission
availability, **not successful mutation**. RuntimeClass `nvidia` is installed.

Recommended exact operating points: `GPU_1500` (reference), `GPU_1200`
(middle), `GPU_900` (low). These common supported points are below both board
maxima. Lowering core MHz is expected to reduce core dynamic power, but watts
and energy/request remain unmeasured and require CUDA-load measurements.
Memory clocks and power limits remain unchanged. Existing `fixed` means
uncontrolled driver defaults, not 1500 MHz, and remains unactuated.
Different board power ceilings mean U4 profile sharing still requires
confirmation; UUID and measured board envelope remain Node-specific.

#### Implementation and deployment boundary

`internal/nodeactuator/nvidia` uses the existing Observe/Apply port: bounded
argv (no shell), exactly one GPU, declared exact MHz/default algorithm,
`-i 0 -lgc MHz,MHz`, independent graphics+SM query, then a separate controller
read before settling. Current graphics=SM is the limited read-back contract;
`clocks.max.sm` is a board ceiling, **not the lock**. No intent/cached command
value is an observation. Driver 555.58.02 has no locked-range getter.

Apply failure/read-back mismatch restores a previously observed declared
point and verifies it, or uses `-rgc` when the previous point is undeclared.
Cleanup has a fresh bounded context even after timeout/cancellation; cleanup
errors remain joined to the original failure. Cleanup never publishes success.
Default reset cannot prove an unlocked policy with this driver: retain raw
operating clocks and the limitation. Agent restart only observes; reboot loses
the non-persistent lock, changes bootID/actual clocks, and does not reapply old
intent. No host startup/systemd hook is installed.

The Operator creates `chill-node-agent-actuation-nvidia` separately from the
unchanged Jetson operand. Initial operating values restrict it to `genesis2`;
expansion is an explicit installation change after proof. Both operands
participate in stop/finalization and aggregate readiness. NVIDIA uses root,
`SYS_ADMIN` only, no escalation, no hostPID, read-only rootfs, RuntimeClass
`nvidia`, `NVIDIA_VISIBLE_DEVICES=all`, driver capability `utility`, and no GPU
resource request (so the measurement can allocate the GPU). The runtime grants
device/cgroup access. Host mounts are read-only binary, x86 loader and
driver/runtime libraries; no Jetson sysfs/state/systemd mounts. Runtime denial
is failure, not permission to use privileged mode.

Rebuild/publish **chill-node-agent** (`build/docker/node-agent.Dockerfile`,
amd64+arm64) and **chill-operator**. No profiler or firstfloor/runtime-7 rebuild
is required; preserve runtime 7's digest. Checked-in image tags stay unchanged
until CampaignOps deploys reviewed immutable references. Mode declarations do
not themselves establish live support or measurement evidence.

#### CampaignOps-only live proof

1. Wait until ongoing measurements release genesis2. Snapshot current Helm
   values, Node JSON/bootID, DeviceClass, Pods, UUID and query output in
   `O/u3-nvidia/`. Confirm no co-tenant GPU workload. Retain
   `nvidia-smi -q -d CLOCK,SUPPORTED_CLOCKS` and
   `--query-gpu=uuid,clocks.current.graphics,clocks.current.sm,clocks.max.sm,clocks.applications.graphics,power.limit --format=csv`.
2. Build/publish the two reviewed images and record digests. Deploy the existing
   release using its **current complete retained values**, adding the NVIDIA
   catalog/config/policies from `charts/chill/values-operating.yaml` and changing
   only nodeAgent/operator image references. Do not overwrite unrelated live
   values with that historical snapshot. Inspect render: Jetson unchanged,
   NVIDIA affinity genesis2 only, SYS_ADMIN without SYS_BOOT, RuntimeClass
   nvidia. Wait for both operands and ChillSystem readiness; retain agent logs.
3. Submit **one atomic Node metadata patch** for `GPU_1500`: desired label
   `chill.dacs.io/power-mode` and fresh UTC RFC3339Nano annotation
   `chill.dacs.io/power-mode-requested-at`. Example (replace timestamp):
   `kubectl patch node genesis2 --type=merge -p '{"metadata":{"labels":{"chill.dacs.io/power-mode":"GPU_1500"},"annotations":{"chill.dacs.io/power-mode-requested-at":"<fresh UTC timestamp>"}}}'`.
   Require state `success`, host graphics=SM=1500, verifiedAt/readyAt, stable
   bootID and no failure. Retain Node JSON and host query. Repeat `GPU_900`,
   then **fresh** `GPU_1500` and verify restoration. Stop on any failure.
4. Before pair costs, obtain an admitted `GPU_1500` MobileNet CUDA profile
   (`BatchEnergyOptimization`, runtime-7 digest unchanged), and Complete
   exact-state `ModelLoadedIdleBaseline` objects for **both** GPU_1500/GPU_900.
   Use the existing baseline sample with genesis2/class/CUDA path/mode and
   the source profile's exact CPU quota/batch; do not reuse fixed-mode idle.
   Submit `config/samples/profiling/edge_v1alpha1_powermodetransitioncost_nvidia.yaml`
   in the serving namespace. Measurement admission creates a real runner and
   its uniquely named Service (`chill-u3-genesis2-clock-runner`); container must
   be `firstfloor`, not `runtime`. Admission requires the source-mode profile
   and grants a frozen runner-profile annotation; do not prepare a mock Service.
   One CR measures `GPU_1500 ↔ GPU_900`: acceptedCountTarget=5, attemptLimit=8
   means five Accepted trials **per direction**, not shell-command durations.
   Preserve coverage/boundary policy; retain CR/directions, observer Jobs/raw
   evidence and seconds/joules. Never force Complete or allow missing witnesses.
5. Once the CR is terminal, request/verify `GPU_1500` again. To recover the
   original driver-default policy, atomically remove desired label and
   requested-at (agent then only observes), run the CampaignOps-only host
   `sudo nvidia-smi -i 0 -rgc` once, and retain command result, query and Node
   JSON. Record **default-policy cleanup, not exact lock-register proof**.
   Do not label it successful GPU_1500 or reuse locked-mode profiles afterward.
   Compare original power limit/UUID/bootID; no unrelated setting may change.
6. Restart only the NVIDIA Pod while holding a clock mode: the new process must
   query, not apply. Reboot is separately disruptive: only CampaignOps in an
   idle window may test new bootID/default clocks and stale-state rejection,
   then submit a fresh desired request. Never reboot during measurements.

The live gate remains open until CampaignOps records deployment digests,
apply/read-back/return evidence and Accepted direction costs. Local tests and
read-only inspection do not close it.

## Remaining vertical slices

### P3: Orange Pi RKNNLite / fixed — completed

Entry condition: met by the retained Orange Pi CPU ORT active/idle admissions.

- [x] Bind an immutable RKNN artifact to its source ONNX, target platform,
  conversion identity, and declared batch sizes.
- [x] Keep host `librknnrt`, driver, image-owned package, device visibility, and
  NPU allocation/exclusivity as separate facts.
- [x] Add an explicit RKNN adapter, artifact projection, `/dev/dri`/host-library
  contract, and active/idle evidence path.
- [x] Retain core mask, input layout, init/load result, selected batch, and output
  shape; fail closed on batch, ABI, device, and initialization mismatch.

Execution-facts v1alpha6 and candidate-report v1alpha10 keep image-owned
packages separate from the expected host library, driver, platform-compatible
token, four exact device paths, and Kubernetes allocation/exclusivity claim.
Privileged mode and supplemental groups are a separate runtime-access contract,
and the report also retains target platform, declared batch sizes, and the explicit
non-batch runtime tensor contract. Their decoders accept the current schemas
only.
RunConfig v1alpha14 retains the normalized physical host contract and optional
typed actuation binding, records configured power and typed thermal metric
identities, and supports either the Node-allocatable CPU quota or an explicitly
selected whole-core Run quota. RKNNLite shares that CPU limit but omits ORT-only
thread settings.
Host-bound derivation identities use execution-state-identity v1alpha5. The
installed RKNN declaration pins the arm64-only runtime image, exact host
ABI/device contract, and runtime access contract. `ModelSpec` pins the
declared-batch-size artifact supply image, architecture, and path.
The production Job consumes the verified report once and projects the
runtime-specific artifact destination, exact file/device hostPaths,
Pod-level supplemental groups, runtime-only privilege, and an optional extended
resource. Observer containers receive neither host mounts nor privilege. The
worker reads the projected candidate record at its process boundary, selects
the explicit RKNNLite adapter, and compares the producer's package, host-library
path, driver, platform, device, batch, and active input-shape observations with
that sealed candidate delivery. The runtime-result validator independently
requires the RKNN implementation's observed input layout. The Python producer
emits the corresponding active or model-loaded-idle runtime result.

The current active and idle runtime results discriminate the common backend
from CPU ORT provider vocabulary and carry an optional RKNN observation
containing package, host-library path, driver, platform token,
exact character devices, core mask, input layout, initialization/load outcome,
and selected batch. Active raw v1alpha7 and idle raw v1alpha5 require those
current nested results. The RKNNLite Python producer strictly decodes the current
RunConfig, verifies the artifact and mutable
host contract, runs only `NPU_CORE_0`, and emits active or inference-free
model-loaded-idle results under the same bounded handoff protocol. The image
recipe publishes a direct arm64 manifest so the recorded runtime OCI reference
and Kubernetes `imageID` describe the same runnable image.

Preparation and the selected execution-policy record accept only the
explicit `rknnlite` / `rknnlite` / `not-applicable` tuple on the current RunConfig
schema. They retain the same bounded worker envelope without fabricating ORT
thread settings. Job materialization, the Go adapter, the Python producer, and
the arm64-only worker were exercised by three retained active and three
retained model-loaded-idle Runs. All six evidence objects were reloaded and
projected through the production identity boundary; both modes share one state
and environment identity while retaining distinct, mode-stable protocol
identities.

Completion: one exact Orange Pi RKNN row reaches L3.

### P4: Orin Nano ORT TensorRT / one NVPModel

Entry condition: Orin Nano is available at Ready/L0 and one power-mode protocol
has proven apply/read-back.

- Freeze ORT, TensorRT, CUDA, L4T, driver, artifact, GPU allocation, and exact
  NVPModel identities.
- Keep TensorRT separate from CPU ORT, retain requested and registered provider
  chains, and leave graph fallback or effective precision Unknown when they
  cannot be observed.
- Separate engine build from admitted measurement and define cache creation,
  reuse, invalidation, and identity.

Completion: one exact Orin Nano TensorRT row and power mode reaches L3.

### P5: Remaining Jetson modes and classes

Entry condition: P4 reaches L3.

- Verify each mode independently, including actuator read-back and a separate
  model-loaded-idle baseline.
- Do not reduce transition pairs before target-state independence is measured.
- Do not generalize one Jetson family's result to another.

### P6: RTX server execution paths

Entry condition: fixed-mode paths retain their existing support. Each new
GPU-clock CUDA row additionally requires the GPU-clock live gate above,
per-mode active/idle/d/preparation/removal evidence and Accepted pair costs
before planner admission.

- Keep ORT CPU, ORT CUDA, and ORT TensorRT as separate paths with exact GPU,
  device-plugin, runtime, wall-source, and contamination-observer facts.
- Keep Kubernetes GPU allocation distinct from Node-wide exclusivity.
- Admit active and model-loaded-idle evidence independently per path.

### P7: Model and batch expansion — completed

Entry condition: the owning execution path reaches L3 for
`mobilenet-v2-050`.

- [x] Record the RKNN batch-two measurement from the separately converted
  immutable artifact and execution path then in use.
- [x] Admit three active and three model-loaded-idle batch-two Runs under one
  stable execution state and record that row independently at L3.
- [x] Keep the batch-one and batch-two rows separate; do not promote the class,
  runtime, model family, or a multi-batch curve from either row alone.
- [x] Preserve exact execution-state identity and define a recorded
  `CurvePlanName`, a Loader-verified `VerifiedCurvePlan`, and idle
  baselines keyed by the historical artifact states. ADR 0016 governs current
  declared-batch-size artifact capability; Plans 5 and 5a own its consumers.
- [x] Reconvert batch one with the current repository recipe, record batch one
  and batch two under one catalog/image generation, and admit three active plus
  three model-loaded-idle Runs for each historical artifact state.
- [x] Implement Plan 5a discovery over the verified curve plan and use Plan 5 only
  for confirmatory L4 claims.
- [x] Execute the recorded campaign, retain and re-verify all 12 active/idle
  Runs, and record the selected batch and the typed boundary of the historical
  artifacts used by that campaign.

Completion: the two exact Orange Pi RKNN measurements compose as one
Loader-verified Plan 5a historical curve plan and retain their L4 result. New RKNN
campaigns use one declared-batch-size artifact, one state key, and one idle baseline.

## Common completion gate

Every L3 vertical slice must prove all applicable items:

- exact artifact, runtime, MetricObserver, and WorkloadObserver child-image
  OCI references are recorded in RunConfig;
- the selected Node matches the exact DeviceClass snapshot;
- desired and observed power mode match and remain stable;
- execution validates artifact identity and runtime/backend facts;
- active and model-loaded-idle evidence are reloaded from durable storage;
- missing thermal/throttle evidence does not fail open;
- architecture, backend, mode, artifact, and host-ABI negative tests exist;
- the exact execution-state and external-environment identities, plus the
  mode-specific measurement-protocol identity, admit three sequential trials
  per mode; full RunConfig-only audit fields may differ only when the delta is
  recorded and deliberately excluded from all three derivation identities;
- the required Node state is independently read back before measurement; and
- package/kind evidence and testbed live evidence are reported separately.

Repository e2e is never run against the testbed kubeconfig. Live validation
uses explicitly captured snapshots and focused `ProfilingRun` objects.

## Progress discipline

- A live-evidence checkbox closes only when the support matrix identifies the
  applicable retained `ProfilingRun` and that Run references admitted evidence.
- Keep completed checkbox text short. Do not copy measurements, object
  identities, command output, or deployment snapshots into this plan.
- Do not promote a row from repository configuration or package tests alone.
- A new production package or dependency updates `.go-arch-lint.yml` and its
  owning component/design record in the same commit.

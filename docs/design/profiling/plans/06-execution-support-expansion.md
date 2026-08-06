# Plan 6: Execution-Support Expansion

> Navigation: [Profiling design index](../README.md)
>
> Row status: [execution support matrix](../foundations/runtime-execution/05-support-matrix.csv)
>
> Completed proof: [execution-support validation ledger](../foundations/runtime-execution/validation-ledger.md)

## Status

Active implementation plan. Raspberry Pi 5 CPU ORT remains at L2 pending a
physical power-supply correction. Orange Pi CPU ORT is L3, while the batch-one
and batch-two RKNNLite rows have reached L4. ADR 0014 preserves those distinct execution states
and composes them through a frozen curve family with one exact idle baseline per
member state. Batch one has now been regenerated, and both members have aligned
three-active plus three-idle L3 evidence under one conversion recipe, catalog
snapshot, runtime, and helper-image set. The pure Plan 5a family/stop campaign
then produced 12 new post-freeze Accepted Runs, reproduced both exact-state
baselines, selected batch one, and stopped at the batch-three capability
boundary. This closes P7 without retroactively promoting the earlier diagnostic
Runs. A read-only NVPModel audit now matches every mode on the three Ready
Jetsons after correcting the Jetson Nano ID 0 name from `10W` to the host's
`MAXN`; the unreachable `orin-nano` row remains unverified. ADR 0015 selects a
dedicated per-Node actuator as the sole host-state writer while retaining the
Profiler as Run orchestrator. The durable `NodeActuation` API, Profiler
producer, strict local-Node consumer projection, pure lifecycle decision core,
exact Node-UID Lease primitive, internal status reconciler, and fail-closed
workload-safety observer are implemented. The opt-in command and Operator/Helm
DaemonSet lifecycle and first Jetson NVPModel host adapter are also implemented.
The Profiler now gates measurement-Job creation on independently verified and
settled target state, requests restoration after a terminal Job, and withholds
evidence finalization until restoration and its causal ordering are proven.
Finalized actuated execution evidence v1alpha2 retains the exact Run and
handoff identities, previous/target/restored typed observations, and lifecycle
timestamps; fixed Runs retain execution evidence v1alpha1. The chart remains
disabled by default. The opt-in AGX Orin CPU ORT MODE_50W slice has now produced
three sequential accepted active Runs and three sequential accepted
model-loaded-idle Runs on the current runtime/observer image set. Retained
reload and production projection proved stable shared execution-state and
owner-bound environment identities across all six while active and idle kept
their distinct stable protocol identities. The two exact CPU ORT rows are L3;
every TensorRT row remains below measurement-ready.
Later slices remain deferred until their stated entry condition is met or this
plan is explicitly revised.

This file owns work order, decisions, and completion conditions. It does not
accumulate command transcripts, image digests, Run statistics, or historical
live snapshots. Those belong in the validation ledger; exact row level and
blocking reason belong in the support matrix.

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
| `L4 Campaign-ready` | Repeated trials and multiple batches can be produced for one verified curve family under Plan 5 or 5a, while each member retains its exact execution state. |

Levels are cumulative. Catalog presence, standalone smoke tests, and successful
Jobs without admitted active and idle evidence do not promote a row beyond the
corresponding level.

## Current queue

| Order | Slice | State | Next gate |
|---|---|---|---|
| paused | Raspberry Pi 5 CPU ORT / fixed | L2 | Correct the electrical condition before collecting new campaign evidence. |
| completed | Orange Pi CPU ORT / fixed | L3 | Retained active and model-loaded-idle evidence is admitted under stable derivation identities. |
| completed | Orange Pi RKNNLite / fixed base slice | L3 foundation | The execution path independently admits active and model-loaded-idle evidence before family composition. |
| active | AGX Orin CPU ORT / MODE_50W | L3 | Execute the pre-frozen Plan 5 `1..2` campaign without treating batch 3 as a capability boundary. |
| completed | RKNNLite model and batch expansion | L4 | Post-freeze Plan 5a campaign selected batch 1 and stopped at the batch-3 capability boundary. |

## Stable decisions

- The row identity is the exact DeviceClass, ModelSpec, immutable artifact,
  execution path, runtime/backend, power mode, workload mode, and measurement
  policy recorded in the support matrix.
- The first model remains `mobilenet-v2-050` until its owning execution path
  reaches L3.
- CPU ORT, RKNNLite, ORT CUDA, and ORT TensorRT remain explicit adapters. Do
  not introduce a generic runtime plugin or device-specific copy of CPU ORT.
- Stable supported power modes belong in `DeviceClass`; mutable desired,
  observed, and previous mode belong to Run/actuator state.
- [ADR 0015](../../../adr/0015-node-power-actuation-boundary.md) assigns
  Node-wide mutation exclusively to a dedicated per-Node actuator operand;
  Profiler orchestrates the durable Run lifecycle, while discovery and the
  measurement worker remain unprivileged.
- Exact child-image digests, not manifest-list digests, are frozen into
  runtime, artifact, MetricObserver, and WorkloadObserver facts.
- Execution-path names select catalog entries and configured envelopes; the
  realized runtime family/backend selects the production adapter. A fixed-batch
  artifact retains its positive batch capability in active and model-loaded-idle
  intents, while idle still performs no inference.
- Installed facts are configuration. A row advances only from retained
  production evidence at the applicable level.
- The AGX Orin CPU ORT MODE_50W rows are a bounded actuation-proof substrate.
  They reuse the shared arm64 CPU ORT implementation but neither implement nor
  promote the separately tracked ORT TensorRT rows.

## P1: Raspberry Pi 5 CPU ORT / fixed — paused

The implementation, chart facts, metric source, thermal producer correction,
and live L2/idle-admission proof are complete and recorded in the validation
ledger. Do not weaken admission or pool pre-repair evidence.

- [ ] Correct the `rasp5` power supply/cable condition and prove
  `get_throttled=0` throughout one 30-second active diagnostic.
- [ ] Run one clean active `ProfilingRun` and require execution complete,
  evidence verified, and admission accepted before paying for repetitions.
- [ ] Under the unchanged post-repair intent and environment identities, run
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
  physical wall-meter association as live corroboration, not a frozen serial.
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
- [x] Separate desired, observed, previous, and restored mode; actuator
  kind/target; policy and Run identity; lifecycle stage; and apply,
  verification, settling, and restoration timestamps in the durable handoff.
- [x] Freeze the selected target and configured actuation-policy digest into
  immutable Run intent.
- [x] Fix intent v1alpha9 and simultaneous Go/Python decoder compatibility
  before wiring any selectable-mode producer.
- [x] Materialize one owned durable handoff and verify its exact local-Node
  consumer projection without enabling a measurement Job or host mutation.
- [x] Define and test the Node-UID-keyed Lease acquire, renew, conflict,
  expiration takeover, and exact-holder release primitive; keep the production
  serialization gate open until the actuator controller consumes it.
- [x] Fix the pure happy-path and fail-safe decision order with separate target
  and restoration read-back phases; no command result directly proves state.
- [x] Implement the internal status reconciler with an exact local-Node filter,
  a durable pre-mutation fence, independent read-back, and restoration-aware
  finalization; keep its production gate open until an operand wires it.
- [x] Implement a fail-closed Kubernetes workload-safety observation that
  retains every active blocking Pod identity outside explicit namespace and
  exact Pod-UID allowances; install no implicit default policy.
- [x] Reject any handoff whose canonical actuation-policy digest is absent from
  the actuator's explicit authorization set before crossing a Lease, safety,
  or host port; policy drift after retained state enters restoration.
- [x] Add an opt-in command and Operator-owned/Helm-configured DaemonSet
  lifecycle with exact Node/self-Pod identity, shared Go policy digesting,
  scoped API permissions, and a stop gate that retains the operand for every
  unfinished or unrestored handoff.
- [x] Wire the first Jetson NVPModel adapter with a bounded configured argv and
  explicit narrow host mounts; keep the chart disabled until live restoration
  proof closes the mutation gate.
- [x] Serialize every runnable-operand handoff with the exact Node-UID Lease;
  the first live MODE_50W lifecycle released that Lease after proven restore.
- [x] Apply the fail-closed workload-safety policy from explicit namespace and
  exact self-Pod allowances before any host operation.
- [x] Fix `apply -> read-back -> settling -> measurement -> restore` in the
  Profiler lifecycle and prove that ordering in one retained live Run.
- [x] Reconstruct the durable handoff after controller restart and request
  restoration after a recorded Job failure or loss; an active-deadline worker
  timeout follows the same `JobFailed` path.
- [x] Surface restoration failure as a terminal `ProfilingRun` execution
  condition without finalizing evidence.
- [ ] Connect that terminal condition to an operator alert sink.
- [x] Admit one exact AGX Orin CPU ORT MODE_50W active Run after verified
  target settling and before proven restoration.
- [x] Admit one exact model-loaded-idle Run through the same actuation and
  restoration boundary.
- [x] Complete two additional active and idle trials and verify stable
  derivation identities across all six retained objects.

Intent v1alpha9 adds one optional `actuation` object with the selected power
mode, exact typed actuator target, and canonical configured-policy digest.
Fixed Runs omit the object and retain their existing execution-state identity.
An actuated Run uses execution-state-identity v1alpha3, which binds the whole
actuation object while leaving measurement-protocol identity unchanged. Older
schemas reject the new field. CPU ORT accepts retained v1alpha8/v1alpha7/v1alpha6
and RKNNLite accepts retained v1alpha8; the current Python request decoders pair
only with current v1alpha9, while their retained envelopes pair only with the
corresponding retained intent family. Candidate-report v1alpha6 carries the
exact selected target. The Profiler selects a configured policy by exact
DeviceClass, mode, and actuator kind, binds its semantic digest into the
intent, and retains its canonical bytes and ConfigMap source in the immutable
record. Its first policy fields bind settling and maximum handoff duration.
The Profiler now creates one owned `NodeActuation`, records its exact UID in
Run status, and reports `NodeActuatorPending` until the target is independently
verified and settled. Only then may it create the measurement Job. A terminal
Job advances the handoff once from `Apply` to `Restore`; evidence finalization
is withheld until `Restored`, and a successful Job is accepted by that gate
only when its completion timestamp does not follow the Profiler-owned restore
request. The handoff keeps Run/Node UID, selected class/mode/target, policy
digest, policy timing, absolute deadline, and Profiler-owned desired stage in
immutable or monotonic `spec`; the actuator alone owns
previous/target/restored observations, phase, timestamps, and terminal failure
in `status`. Its consumer projection rejects a foreign Node name or UID before
any Lease or host adapter can receive the request. This closes the durable API
boundary without enabling host mutation or selectable-mode profiling.

The internal layer keeps Node locking and lifecycle decisions separate
from host tooling. Its Lease name is derived from exact Node UID, holder
identity from exact handoff UID, and takeover is allowed only after a trusted
renew/acquire timestamp plus configured duration expires. Release uses object
preconditions and refuses a foreign holder. The pure lifecycle requires
`observe previous -> apply -> observe target -> settle -> hold -> restore ->
observe restored`; deadline, safety rejection, target mismatch, and primary
failure all enter the same restore path. `VerifyingRestore` and
`restoreAppliedAt` make command application and independent read-back
reconstructable after restart. A restore read-back mismatch keeps the failure
terminal and retains or reacquires the Lease. The internal status reconciler
now drives these primitives from durable `NodeActuation` state. It records the
apply phase before invoking the host port, separates command completion from
target and restoration read-back, retains the Lease and finalizer when
restoration cannot be proven, and reconciles Lease status after restart. Its
workload-safety adapter lists active Pods on the exact target Node and allows
only explicitly configured namespaces or exact Pod UIDs; this permits the
per-Node DaemonSet to exclude only its own Pod without trusting every workload in
the release namespace. An unapproved Pod or an incomplete blocker set fails
closed. The reconciler independently requires the handoff's canonical policy
digest to be in its installed authorization set before crossing any port; if
that authorization drifts after a previous state was retained, it durably
starts restoration instead of abandoning the Node. The runnable operand
now selects these allowances and consumes the Lease and lifecycle paths. The
first opt-in AGX Orin Run closed the target apply/read-back and restoration
gate. Three current-image active and three current-image model-loaded-idle Runs
then closed admission and the sequential identity audit. Live host-failure
injection and an operator alert sink remain open.

The runnable layer composes the controller with installation-owned host access.
Helm renders a disabled-by-default ConfigMap, ServiceAccount,
namespaced handoff/Lease Role, cluster-scoped Node/Pod read permissions, and the
`ChillSystem` opt-in reference. The Operator owns the per-eligible-Node
DaemonSet, reports its availability, and refuses disable or system finalization
while any handoff is non-terminal or lacks proven restoration. The command
resolves exact Node UID from the API, excludes only its own downward-API Pod UID
in addition to explicitly configured infrastructure namespaces, and derives
authorized policy digests through the same `internal/nodeactuator` canonical
type used by the Profiler. Its Kubernetes client reuses the shared explicit
endpoint plus projected ServiceAccount token/CA path so KubeEdge edge Pods do
not depend on missing Kubernetes service environment variables. The Jetson adapter invokes the configured host
`nvpmodel` argv with a bounded deadline, parses the query's exact name and ID,
and never treats an apply exit as convergence. The chart mounts only the
declared binary, runtime libraries, NVPModel configuration/state, and sysfs;
it does not request privileged mode or added capabilities. The exact AGX Orin
MODE_50W lifecycle is live-proven; no other Node, mode, or runtime inherits
that result.

### Jetson NVPModel

- [x] Read-only probe every Ready Jetson's complete NVPModel ID/name set.
- [x] Compare each reachable host with its Helm catalog row and correct every
  mismatch before enabling actuation.
- [ ] Recover `orin-nano`, then probe and compare its complete NVPModel set;
  neither Kubernetes nor the configured SSH path could reach it during the
  2026-08-06 audit.
- [x] Implement explicit bounded `Apply(ID)` and `Observe()` operations.
- [x] Judge success in the controller by a separate parsed name/ID read-back,
  never command exit alone; the first MODE_50W live lifecycle proved both
  target and restored read-backs.
- [x] Retain exact Run/handoff identities and previous, target, and restored
  mode observations in finalized execution evidence.
- [x] Configure the post-read-back settling duration in installation policy.
- [ ] Define a thermal-stability criterion only if non-throttled admission and
  the configured settling duration are insufficient; elapsed settling time does
  not itself prove thermodynamic equilibrium.
- [x] Test read-back mode drift, Lease conflict, partial apply failure, and
  restore mismatch in the pure and controller paths.
- [ ] Run bounded live failure injection for an unsupported NVPModel ID and a
  restoration failure; local error-path tests do not prove host behavior.

### GPU clock

- [ ] Probe apply/read-back feasibility before adding support.
- [ ] Keep Jetson and discrete NVIDIA tooling, permissions, and read-back
  contracts separate.
- [ ] Add no composite actuator until an experiment proves NVPModel and clock
  must be one combined knob.

## Remaining vertical slices

### P3: Orange Pi RKNNLite / fixed — completed

Entry condition: met by the retained Orange Pi CPU ORT active/idle admissions.

- [x] Bind an immutable RKNN artifact to its source ONNX, target platform,
  conversion identity, and fixed-batch capability.
- [x] Keep host `librknnrt`, driver, image-owned package, device visibility, and
  NPU allocation/exclusivity as separate facts.
- [x] Add an explicit RKNN adapter, artifact projection, `/dev/dri`/host-library
  contract, and active/idle evidence path.
- [x] Retain core mask, input layout, init/load result, selected batch, and output
  shape; fail closed on batch, ABI, device, and initialization mismatch.

Execution-facts v1alpha4 and candidate-report v1alpha6 now keep image-owned
packages separate from the expected host library, driver, platform-compatible
token, four exact device paths, and Kubernetes allocation/exclusivity claim.
Privileged mode and supplemental groups are a separate runtime-access contract,
and the report also retains target platform, fixed batch, and the explicit
runtime input tensor. Its decoder retains strict v1alpha2-v1alpha4
compatibility through v1alpha5 for already frozen evidence.
Intent v1alpha9 retains the order-normalized physical host-contract digest,
omits ORT-only thread settings from RKNNLite's common CPU quota, and adds the
optional typed actuation binding. V1alpha8/v1alpha7/v1alpha6 CPU bytes remain
readable by both Go and Python, and v1alpha8 RKNNLite remains readable.
Host-bound derivation identities use execution-state-identity v1alpha2 while
CPU identities remain unchanged. The installed RKNN declaration pins the
arm64-only runtime manifest, fixed-batch artifact manifest and bytes, exact
host ABI/device contract, and runtime access contract.
The production Job now consumes the verified report once and projects the
runtime-specific artifact destination, exact file/device hostPaths,
Pod-level supplemental groups, runtime-only privilege, and an optional extended
resource. Observer containers receive neither host mounts nor privilege. The
worker now reads the exact projected candidate bytes at its process boundary,
selects the explicit RKNNLite adapter, and compares the producer's package,
host-library digest, driver, platform, device, batch, layout, and active input-
shape observations with that sealed candidate delivery. The Python producer
emits the corresponding active or model-loaded-idle runtime result.

Active runtime-result v1alpha2 and idle runtime-result v1alpha2 now discriminate
the common backend from CPU ORT provider vocabulary and carry an optional RKNN
observation containing package, host-library digest, driver, platform token,
exact character devices, core mask, input layout, initialization/load outcome,
and selected batch. Active raw v1alpha4 and idle raw v1alpha2 require those
current nested results, while the immediately retained CPU raw/runtime pairs
remain readable. The RKNNLite Python producer now strictly decodes the current
intent and verified candidate projection, verifies the artifact and mutable
host contract, runs only `NPU_CORE_0`, and emits active or inference-free
model-loaded-idle results under the same bounded handoff protocol. The image
recipe publishes a direct arm64 manifest so the frozen runtime digest and
Kubernetes `imageID` remain the same verification identity.

Preparation and the frozen execution-policy selector accept only the
explicit `rknnlite` / `rknnlite` / `not-applicable` tuple on the current intent
schema. They retain the same bounded worker envelope without fabricating ORT
thread settings. Job materialization, the Go adapter, the Python producer, and
the arm64-only worker were exercised by three retained active and three
retained model-loaded-idle Runs. All six evidence objects were reloaded and
projected through the production identity boundary; both modes share one state
and environment identity while retaining distinct, mode-stable protocol
identities.

Completion: one exact Orange Pi RKNN row reaches L3.

### P4: Orin Nano ORT TensorRT / one NVPModel

Entry condition: Orin Nano is restored to Ready/L0 and one power-mode protocol
has proven apply/read-back/restore.

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

Entry condition: Node-wide GPU clock ownership and restoration are proven.

- Keep ORT CPU, ORT CUDA, and ORT TensorRT as separate paths with exact GPU,
  device-plugin, runtime, wall-source, and contamination-observer facts.
- Keep Kubernetes GPU allocation distinct from Node-wide exclusivity.
- Admit active and model-loaded-idle evidence independently per path.

### P7: Model and batch expansion — completed

Entry condition: the owning execution path reaches L3 for
`mobilenet-v2-050`.

- [x] Bind the RKNN batch-two capability to a separately converted immutable
  artifact and execution path; do not infer it from ONNX shape flexibility.
- [x] Admit three active and three model-loaded-idle batch-two Runs under one
  stable execution state and record that row independently at L3.
- [x] Keep the batch-one and batch-two rows separate; do not promote the class,
  runtime, model family, or a multi-batch curve from either row alone.
- [x] Preserve exact execution-state identity and define a pre-frozen
  `CurveFamilyContractDigest`, a Loader-verified `CurveFamilyIdentity`, and idle
  baselines keyed by member state. ADR 0014 owns the cross-plan contract; Plans
  5 and 5a own its consumers.
- [x] Reconvert batch one with the current repository recipe, freeze batch one
  and batch two under one catalog/image generation, and admit three active plus
  three model-loaded-idle Runs for each member state.
- [x] Implement Plan 5a discovery over the verified family and use Plan 5 only
  for confirmatory L4 claims.
- [x] Execute the pre-frozen campaign, retain and re-verify all 12 active/idle
  Runs, and record the selected batch and typed capability-boundary stop.

Completion: the two exact Orange Pi RKNN fixed-artifact rows compose as one
Loader-verified Plan 5a family and reach L4 while retaining distinct execution
states and idle baselines.

## Common completion gate

Every L3 vertical slice must prove all applicable items:

- exact artifact, runtime, MetricObserver, and WorkloadObserver child-image
  digests are frozen in the intent;
- the selected Node matches the exact DeviceClass snapshot;
- desired and observed power mode match and remain stable;
- execution re-verifies artifact bytes and runtime/backend facts;
- active and model-loaded-idle evidence are reloaded from durable storage;
- missing thermal/throttle evidence does not fail open;
- architecture, backend, mode, artifact, and host-ABI negative tests exist;
- the exact execution-state and external-environment identities, plus the
  mode-specific measurement-protocol identity, admit three sequential trials
  per mode; full intent-only audit fields may differ only when the delta is
  recorded and deliberately excluded from all three derivation identities;
- changed Node state is restored and read back; and
- package/kind evidence and testbed live evidence are reported separately.

Repository e2e is never run against the testbed kubeconfig. Live validation
uses explicitly captured snapshots and focused `ProfilingRun` objects.

## Progress discipline

- A checkbox closes only when the support matrix and validation ledger point
  to the applicable authoritative proof.
- Keep completed checkbox text short. Put dated measurements, commands,
  digests, sample counts, and deployment revisions in the validation ledger.
- Do not promote a row from repository configuration or package tests alone.
- Do not copy live evidence back into this plan after it is recorded in the
  ledger.
- A new production package or dependency updates `.go-arch-lint.yml` and its
  owning component/design record in the same commit.

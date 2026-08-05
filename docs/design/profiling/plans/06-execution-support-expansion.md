# Plan 6: Execution-Support Expansion

> Navigation: [Profiling design index](../README.md)
>
> Row status: [execution support matrix](../foundations/runtime-execution/05-support-matrix.csv)
>
> Completed proof: [execution-support validation ledger](../foundations/runtime-execution/validation-ledger.md)

## Status

Active implementation plan. Raspberry Pi 5 CPU ORT remains at L2 pending a
physical power-supply correction. Orange Pi CPU ORT and the batch-one and
batch-two RKNNLite rows have reached L3. RKNNLite batch expansion is now
at the L4 campaign boundary: ADR 0014 preserves those distinct execution states
and composes them through a frozen curve family with one exact idle baseline per
member state. Batch one has now been regenerated, and both members have aligned
three-active plus three-idle L3 evidence under one conversion recipe, catalog
snapshot, runtime, and helper-image set. The next gate is the pure Plan 5a
family/stop campaign. Its constructors and first pre-registered record are now
complete, while the aligned diagnostic Runs are not retroactively promoted to
L4. No post-freeze campaign Run has been submitted, so the support rows remain
L3 and execution of that record is the current gate. Later slices remain
deferred until their stated entry condition is met or this plan is explicitly
revised.

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
| completed | Orange Pi RKNNLite / fixed | L3 | Three active and three model-loaded-idle Runs are retained, reloaded, and admitted under stable derivation identities. |
| parallel | Node power-mode actuation | design only | Select one Node-wide owner and prove apply/read-back/restore on one mode. |
| current | RKNNLite model and batch expansion | Plan 5a core and campaign record frozen; rows remain L3 | Execute only the post-freeze Run set, verify the realized family, and record the typed stop result. |

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
- Exact child-image digests, not manifest-list digests, are frozen into
  runtime, artifact, MetricObserver, and WorkloadObserver facts.
- Execution-path names select catalog entries and configured envelopes; the
  realized runtime family/backend selects the production adapter. A fixed-batch
  artifact retains its positive batch capability in active and model-loaded-idle
  intents, while idle still performs no inference.
- Installed facts are configuration. A row advances only from retained
  production evidence at the applicable level.

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

- [ ] Select the owner process for Node-wide actuation; an unprivileged runtime
  container must not change host mode directly.
- [ ] Separate desired, observed, and previous mode; actuator kind/target; and
  apply/verification timestamps.
- [ ] Freeze the selected target and configured actuation-policy digest into
  immutable Run intent.
- [ ] Plan the intent schema revision and simultaneous Go/Python decoder
  deployment.
- [ ] Serialize Node-wide changes with a Lease or equivalent lock.
- [ ] Define actuation safety in the presence of other workloads.
- [ ] Fix `apply -> read-back -> settling -> measurement -> restore`.
- [ ] Restore after Run failure, controller restart, and worker timeout.
- [ ] Surface restoration failure as a terminal condition and operator alert.

### Jetson NVPModel

- [ ] Read-only probe every live Jetson's NVPModel IDs and names.
- [ ] Compare the probe with every Helm catalog mode before enabling it.
- [ ] Implement explicit `Apply(ID)` and `Observe()` operations.
- [ ] Judge success by read-back identity, never command exit alone.
- [ ] Retain start/end mode observations in execution evidence.
- [ ] Configure settling time and thermal stability in installation policy.
- [ ] Test mode drift, unsupported ID, lock conflict, and restore failure.

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

Execution-facts v1alpha4 and candidate-report v1alpha5 now keep image-owned
packages separate from the expected host library, driver, platform-compatible
token, four exact device paths, and Kubernetes allocation/exclusivity claim.
Privileged mode and supplemental groups are a separate runtime-access contract,
and the report also retains target platform, fixed batch, and the explicit
runtime input tensor. Its decoder retains strict v1alpha2-v1alpha4
compatibility for already frozen evidence.
Intent v1alpha8 binds only an order-normalized digest of the physical host
contract into state and omits ORT-only thread settings from RKNNLite's common
CPU quota; v1alpha7/v1alpha6 CPU bytes remain readable by both Go and Python.
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

### P7: Model and batch expansion — current

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

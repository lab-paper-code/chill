# Plan 6: Execution-Support Expansion

> Navigation: [Profiling design index](../README.md)

## Status

Active implementation plan. P0, the support vocabulary and initial matrix, was
recorded on 2026-08-05. P1, Raspberry Pi 5 CPU ORT at fixed power mode, is the
current implementation slice. Later slices remain unauthorized until their
preceding completion condition is met or this plan is revised with stronger
evidence.

This plan expands the set of Nodes, power modes, runtime paths, and models that
the production `ProfilingRun` path can measure. It does not treat catalog
presence or a historical standalone smoke as production support. It does not
create a generic runtime plugin or put mutable current power mode in
`DeviceClass.status`.

## Question

> Which exact DeviceClass, model, artifact, runtime/backend, power mode, and
> workload-mode rows does the current production path support, and what is the
> next smallest vertical slice that raises one row to measurement-ready?

## Support levels

The word "supported" is always qualified by one of these levels:

| Level | Required evidence |
|---|---|
| `L0 Catalogued` | A live Node is discovered into the exact `DeviceClass`. Repository catalog presence alone is recorded separately. |
| `L1 Compatible` | The exact artifact and trusted runtime-image declaration evaluate `Compatible` for the class. |
| `L2 Executable` | A production `ProfilingRun` Job succeeds on the exact Node, runtime path, and power mode. |
| `L3 Measurement-ready` | Both active and model-loaded-idle evidence are durably reloaded, verified, and admitted. |
| `L4 Campaign-ready` | Repeated trials and multiple batches can be produced for one stable execution state under Plan 5 or 5a. |

Levels are cumulative. A historical RKNN or TensorRT script is not L2. A
successful Job without both admission modes is L2, not L3. A retained live
result produced by an older binary is retained evidence, but a changed HEAD is
not called live-reverified until it executes again.

## P0 support matrix

The row-level source of truth is
[`../foundations/runtime-execution/05-support-matrix.csv`](../foundations/runtime-execution/05-support-matrix.csv).
Every row binds at least:

```text
DeviceClass
ModelSpec
artifact name and content digest
execution path
runtime family and backend
runtime image digest
power mode
workload mode
measurement policy
```

It also separates repository catalog presence, the 2026-08-05 live Node
snapshot, retained non-production smoke, current production level, and the
blocking reason. `unresolved` is an explicit missing fact and never a wildcard.

### Snapshot used to seed the matrix

Read-only cluster evidence on 2026-08-05 showed:

- ten Nodes: nine `Ready`; `orin-nano` was `Unknown`, lacked the CHILL
  DeviceClass label, and therefore remained below live L0;
- seven live `DeviceClass` objects; the repository catalog also declares
  `jetson-orin-nano-8g`;
- one live `ModelSpec`, `mobilenet-v2-050`, with the `canonical-onnx` artifact
  and `ort-cpu` path;
- the installed Profiler fact ConfigMap declared only the Lattepanda amd64 CPU
  ORT path and only the Lattepanda metric source; and
- no live `ProfilingRun` objects remained. Plan 3 retains the earlier
  Lattepanda L3 evidence, but current HEAD has not been live-reverified.

The installed Profiler image (`sha-ad7b84f`) predates current HEAD. Installed
state and current repository behavior are therefore separate columns, not
silently merged.

### P0 decisions

- [x] Define L0 through L4 and require their distinct evidence.
- [x] Record exact row identities and a blocker for every target row.
- [x] Separate repository catalog, live discovery, historical smoke, and
  production support.
- [x] Keep the first model slice fixed to `mobilenet-v2-050`.
- [x] Keep desired runtime decisions separate from observed runtime evidence,
  following the runtime-execution provenance records.
- [x] Retain explicit CPU ORT, RKNNLite, ORT CUDA, and ORT TensorRT adapters;
  do not introduce a generic plugin registry.
- [x] Keep stable supported power modes in `DeviceClass`; keep mutable desired,
  observed, and previous mode in Run/actuator state, never
  `DeviceClass.status`.

## Delivery order

| Priority | Vertical slice | Completion condition |
|---|---|---|
| P1 | Raspberry Pi 5 CPU ORT / fixed | Both workload modes reach L3. |
| P2 | Orange Pi CPU ORT / fixed | Both workload modes reach L3; common code is extracted only after this second path proves repetition. |
| P2 parallel | Node power-mode actuation | One Jetson mode can apply, read back, settle, remain stable, and restore under failure. |
| P3 | Orange Pi RKNNLite / fixed | One exact RKNN artifact and fixed batch reach L3. |
| P4 | Orin Nano ORT TensorRT / one NVPModel | One exact mode reaches L3 including restoration. |
| P5 | Remaining Jetson modes and classes | Each mode is independently verified and receives its own idle baseline. |
| P6 | RTX server ORT CPU/CUDA/TensorRT and GPU clock | Each execution path reaches L3 independently. |
| P7 | Model and batch expansion | Added only after the owning execution path is L3; Plan 5/5a establishes L4. |

## P1: Raspberry Pi 5 CPU ORT / fixed

Do not create a new runtime abstraction. Reuse the explicit CPU ORT path and
change only facts, image platforms, and device-specific execution policy until
source evidence proves a shared change is required.

- [x] Build a linux/arm64 measurement-worker/ORT runtime image. The local OCI
  verification on 2026-08-05 produced runnable child digest
  `sha256:42ff8d90d01324ed9cbb5a77be12c62360d57adf485f61ea8c23fee7c84dc773`;
  the same child is published under the immutable development tag
  `sha-fb80fa3` and was pulled on `rasp5` with an identical image ID.
- [x] Verify linux/arm64 child digests for MetricObserver and
  WorkloadObserver. Local OCI builds on 2026-08-05 produced MetricObserver
  `sha256:12bb583bee67761d3089ead9f6296be40aff1159e161cf35261f4ce949eb261d`
  and WorkloadObserver
  `sha256:94053b7789e79332e0e0efd9e927909718a5399e65467d0ce8897c79773dbd28`;
  both children are published under `sha-fb80fa3`.
- [x] Record the selected child image digest, not only a manifest-list digest,
  in the trusted runtime and observer declarations. The opt-in chart profile is
  `charts/chill/values-profiler-rasp5.yaml`.
- [x] Produce and verify an arm64 declaration containing ONNX Runtime,
  `CPUExecutionProvider`, and normalized architecture. The exact runtime child
  reported `aarch64`, ONNX Runtime `1.23.2`, and the CPU provider on `rasp5`;
  the rendered declaration payload digest is
  `sha256:2094a677bafeafaaaf07ce5eef1734ae03a7b8c10725f1f78ce629cb93a27ee3`.
- [x] Register the exact arm64 artifact-supply image, digest, and path for
  `mobilenet-v2-050`. Child
  `sha256:1174f5266a0a4f8b968f2a8745671cb2546934373766797b256fe97d38088721`
  reproduced the ModelSpec content digest on `rasp5`.
- [x] Add the Raspberry Pi wall-plug source to the Profiler metric-source
  catalog: `http://155.230.34.243:9102/metrics`.
- [x] Verify that its endpoint provides wall power, temperature, observation
  availability, and direct active/latched throttling signals. Missing thermal
  evidence remains fail-closed in admission.
- [x] Add the Raspberry Pi execution declaration to opt-in installation facts.
  This records deployable chart configuration; live installation is still an
  L1/L2 gate below.
- [x] Review CPU request, ORT thread count, runtime memory, and workspace
  against the live Node's four allocatable CPUs and `8032080Ki` allocatable
  memory. The current wildcard CPU-ORT policy fits this envelope without a
  Raspberry Pi-specific policy fork.
- [x] Add fail-closed tests for architecture, backend, artifact digest, and
  selected image-child drift. Candidate-resolution tests reject runtime
  architecture/backend and artifact architecture/digest mismatches;
  terminal-evidence tests reject runtime, MetricObserver, and
  WorkloadObserver child `imageID` drift.
- [ ] Run three active and three model-loaded-idle Runs sequentially.
- [ ] Verify all six Runs report execution complete, evidence verified, and
  evidence admitted.

### P1 current-HEAD live result

The opt-in Raspberry Pi profile was installed on 2026-08-05 with the Operator,
Profiler, and Console aligned to repository commit `333457e`. The existing
class-neutral CPU ORT implementation required no Raspberry Pi-specific
controller, worker, or `hack/` producer:

- active Run `rasp5-active-bs1-a1` completed on `rasp5`, durably reloaded and
  verified evidence `sha256:b5bff1c4284e56077b67eb0c2492fe67353aa79ee914ea535274a8cf6c6ec3fe`,
  and executed 2,304 batch-one calls in 30.001 seconds with ONNX Runtime
  `1.23.2` and `CPUExecutionProvider`;
- its wall-power window contained 31 successful samples with 8.668 W mean,
  but 17 samples reported current throttling, so `wall-power-v2` correctly
  rejected the Run with `ThermalOrRuntimeThrottling`;
- a separate diagnostic Run reproduced 16 active samples. A read-only host
  trace observed Raspberry Pi firmware values alternating between `50005` and
  `50000` during load. These are hexadecimal values: `0x50005` asserts current
  undervoltage and current throttling as well as their history bits. This is a
  testbed power-supply blocker, not a CHILL admission defect; and
- idle Run `rasp5-idle-model-loaded-i1` used the same retained candidate after
  a 60-second observer-independent settling period. It completed, durably
  reloaded, and was admitted with evidence
  `sha256:30c6870681ebbffac300b3ab959a2767e7be16909045de57db38717246cd8f38`.
  All 31 thermal samples were observable and none was active.

The active diagnostic also exposed a metric-source defect outside this
repository: the deployed edge-metrics exporter parsed the prefix-free
hexadecimal `get_throttled` text as decimal. The producer fix was committed as
edge-metrics `a7b3a63`, including regression cases for `50000` and `50005`, and
deployed as release revision 23 with image tag `sha-a7b3a63` and registry index
digest `sha256:565c9d4a15787d548341ec7d32fc3af80e3e04e0991e69d381684d8829d7967e`.
A simultaneous 90-sample raw-sysfs and exporter trace around diagnostic Run
`rasp5-active-thermal-parser-diag-v2` observed 68 `0`, 10 `50000`, and 12
`50005` values. Eighty-nine samples matched the required mapping exactly:
`0 -> active=0,latched=0`, `50000 -> active=0,latched=1`, and
`50005 -> active=1,latched=1`. The sole mismatch was one sequential raw-read /
HTTP-scrape pair during a state transition; it retained `latched=1` and does
not contradict the parser mapping. The retained Run evidence
`sha256:7b1b9951ed66f9add284fc85d0b8e7f6ac8a9263030b6469e053c6f6dfb28ba1`
was still correctly rejected with `ThermalOrRuntimeThrottling`, confirming that
the remaining failure is the electrical condition rather than the producer
parser.

This raises the Raspberry Pi slice to L2 and proves the idle admission path.
It does not satisfy L3 until the electrical throttling is removed and both
active and idle evidence are admitted. The diagnostic Runs had an additional
ownerless trace Pod in their environment identity and are not repeated campaign
trials.

### Remaining P1 TODO, in order

- [x] Fix the prefix-free Raspberry Pi `get_throttled` hexadecimal parser in
  the edge-metrics producer repository, add `50000`/`50005` regression cases,
  deploy it, and verify active/latched gauges against a simultaneous raw sysfs
  trace. This is an external metric-producer change, not CHILL admission code.
- [ ] Correct the `rasp5` power supply/cable condition and prove
  `get_throttled=0` throughout one 30-second active diagnostic. Do not weaken
  `wall-power-v2` to accommodate electrical throttling.
- [ ] Run one clean active `ProfilingRun` and require execution complete,
  evidence verified, and admission accepted before paying for repetitions.
- [ ] Under the resulting unchanged intent and environment identities, run
  three new active and three new model-loaded-idle trials sequentially. Do not
  pool the pre-repair idle trial with the post-repair campaign.
- [ ] Reload all six retained evidence objects, compare the three identity
  strings within each mode and the shared state/environment identities across
  modes, then promote the two matrix rows to L3 only if all admissions pass.

Completion: `raspberry-pi-5-8g / mobilenet-v2-050 / ort-cpu / fixed` is L3 for
both workload modes on current HEAD.

The P1 values profile is deliberately arm64-only. Execution and artifact facts
are declaration-scoped, but MetricObserver and WorkloadObserver image facts are
currently document-global. Terminal evidence requires each requested digest to
match the pulled child image ID, so one mixed amd64/arm64 profile cannot use a
manifest-list digest as a shortcut. This does not block the P1 single-class
slice; it remains an explicit blocker for simultaneous cross-architecture
support.

## P2: Orange Pi CPU ORT / fixed

- [ ] Add exact Orange Pi runtime and artifact declarations.
- [ ] Add its Node-specific metric source and wall-meter identity.
- [ ] Verify CPU thermal/throttle metric completeness.
- [ ] Bind CPU request and ORT threads to observed allocatable CPU.
- [ ] Run and admit three active and three model-loaded-idle Runs.
- [ ] Review whether Raspberry Pi and Orange Pi differ only by declarations
  and configured execution policy.
- [ ] Extract only behavior that has now repeated on both real arm64 paths.

Completion: `orangepi-5-plus-16g / mobilenet-v2-050 / ort-cpu / fixed` is L3.

## P2 parallel: Node power-mode actuation

### Ownership and protocol

- [ ] Select the owner process for Node-wide actuation; the unprivileged
  runtime container must not change host mode directly.
- [ ] Separate desired, observed, and previous mode; actuator kind/target; and
  apply/verification timestamps.
- [ ] Freeze the selected target and configured actuation-policy digest into
  the immutable Run intent.
- [ ] Plan the intent schema revision and simultaneous Go/Python decoder
  deployment.
- [ ] Serialize Node-wide changes with a Lease or equivalent lock.
- [ ] Define when actuation is allowed in the presence of other workloads.
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
- [ ] Revisit the current one-of actuator API only if an experiment proves
  NVPModel plus clock must be one combined knob.
- [ ] Do not add a speculative composite actuator.

## P3: Orange Pi RKNNLite / fixed

- [ ] Add the RKNN artifact/RKNNLite relation to static compatibility.
- [ ] Select the authoritative producer of each RKNN artifact's fixed-batch
  capability.
- [ ] Retain source ONNX digest, RKNN digest, target platform, and conversion
  identity.
- [ ] Compare RKNNLite package, host `librknnrt`, and driver identities without
  collapsing image-owned and host-owned facts.
- [ ] Replace the hard-coded `/model/model.onnx` projection with an explicit
  RKNN artifact projection for this path.
- [ ] Project `/dev/dri` and required host libraries explicitly.
- [ ] Keep device visibility distinct from NPU allocation or exclusivity.
- [ ] Add an explicit RKNN runtime adapter and runner.
- [ ] Retain core mask, input layout, init/load result, selected batch, and
  output shape as RKNN-specific evidence.
- [ ] Do not use the ORT term `provider` for RKNN evidence.
- [ ] Implement active and model-loaded-idle modes.
- [ ] Fail closed on artifact-batch mismatch, ABI mismatch, missing device,
  and init failure.
- [ ] Run and admit three trials per workload mode.

Completion: one exact Orange Pi RKNN row reaches L3. No arbitrary-batch or NPU
exclusivity claim follows from it.

## P4: Orin Nano ORT TensorRT / one NVPModel

- [ ] Restore Orin Nano to `Ready` and live L0 before runtime work.
- [ ] Add the ORT TensorRT path and trusted declaration producer.
- [ ] Retain ORT, TensorRT, CUDA, L4T, and driver component identities.
- [ ] Require `nvidia.com/gpu: 1` in the explicit Job policy.
- [ ] Add an ORT TensorRT adapter separate from CPU ORT.
- [ ] Retain requested and registered provider chains.
- [ ] Separate engine build from the admitted measurement window.
- [ ] Define engine-cache creation, reuse, invalidation, and identity.
- [ ] Leave graph partition/fallback `Unknown` when it cannot be observed.
- [ ] Keep requested FP16/INT8 distinct from effective precision.
- [ ] Raise one verified NVPModel mode to L3 first.
- [ ] Test power mode, GPU allocation, provider, artifact, output shape, and
  restoration drift.

## P5: Jetson modes and classes

- [ ] Add the remaining verified Orin Nano modes.
- [ ] Verify actuator read-back on Xavier NX, AGX Orin, then Jetson Nano.
- [ ] Treat every mode as an independent execution state.
- [ ] Produce a separate model-loaded-idle baseline per device type, model,
  and mode.
- [ ] Do not reduce transition combinations before target-state independence
  is experimentally established.
- [ ] Do not generalize one Jetson family's success to another.

## P6: RTX server execution paths

- [ ] Keep ORT CPU, ORT CUDA, and ORT TensorRT as three paths.
- [ ] Add exact GPU, device-plugin, and runtime declarations per path.
- [ ] Separate Kubernetes GPU allocation from Node-wide exclusivity.
- [ ] Implement GPU-clock apply/read-back/restore.
- [ ] Connect the exact server wall source and workload-contamination observer.
- [ ] Run active and model-loaded-idle admission independently per path.

## P7: Model and batch expansion

- [ ] Add a model only after its execution path has reached L3 with
  `mobilenet-v2-050`.
- [ ] Bind every new logical model to exact immutable artifacts and per-runtime
  execution paths.
- [ ] Record runtime-specific batch capability rather than assuming ONNX shape
  flexibility applies to RKNN or TensorRT engines.
- [ ] Run active and model-loaded-idle admission before any batch campaign.
- [ ] Use Plan 5a for candidate discovery and Plan 5 for confirmation when an
  L4 statistical claim is required.
- [ ] Update the support matrix row-by-row; never promote an entire class,
  runtime family, or model family from one successful row.

## Common completion gate

Every L3 vertical slice must prove all applicable items:

- exact artifact, runtime, MetricObserver, and WorkloadObserver child-image
  digests are frozen in the intent;
- the selected Node matches the exact DeviceClass snapshot;
- desired and observed power mode match and remain stable;
- execution re-verifies artifact bytes and runtime/backend facts;
- active and model-loaded-idle evidence are reloaded from the durable store;
- missing thermal/throttle evidence does not fail open;
- architecture, backend, mode, artifact, and host-ABI negative tests exist;
- the exact intent admits three sequential trials per mode;
- changed Node state is restored and read back; and
- package/kind evidence and testbed live evidence are reported separately.

Repository e2e is never run against the testbed kubeconfig. Live validation
uses explicitly captured snapshots and focused `ProfilingRun` objects.

## Progress discipline

A checkbox closes only when the plan or matrix cites its authoritative
evidence: source and tests for behavior, a digest and rendered manifest for an
image/configuration fact, or a captured live object/evidence record for L2-L4.
Work proceeds in the delivery order above. A later slice may be investigated in
parallel, but it does not inherit a previous slice's support level.

# Execution-Support Validation Ledger

> Navigation: [Profiling design index](../../README.md)
>
> Work order: [Plan 6](../../plans/06-execution-support-expansion.md)
>
> Row status: [execution support matrix](05-support-matrix.csv)

## Status and ownership

Evidence record. This file owns dated repository and live-testbed observations
that justify Plan 6 checkbox closure. It does not authorize new work and does
not replace retained `ProfilingRun` evidence. Plan 6 owns work order and
completion gates; the support matrix owns the current row level and blocker.

Record only evidence still needed to explain a current level or closed gate.
Do not paste full command transcripts or duplicate implementation rationale.

## 2026-08-05 baseline snapshot

- Nine of ten Nodes were Ready; `orin-nano` was Unknown and below live L0.
- Seven live DeviceClasses and one live ModelSpec, `mobilenet-v2-050`, were
  present.
- The initial installed Profiler facts contained only the Lattepanda amd64 CPU
  ORT declaration and metric source.
- Current support vocabulary and all row blockers were seeded in the support
  matrix before expanding arm64 execution.

## Shared arm64 CPU ORT facts

| Fact | Verified identity |
|---|---|
| Measurement worker | arm64 child `sha256:42ff8d90d01324ed9cbb5a77be12c62360d57adf485f61ea8c23fee7c84dc773`; ONNX Runtime 1.23.2; `CPUExecutionProvider` |
| MetricObserver | arm64 child `sha256:12bb583bee67761d3089ead9f6296be40aff1159e161cf35261f4ce949eb261d` |
| WorkloadObserver | arm64 child `sha256:94053b7789e79332e0e0efd9e927909718a5399e65467d0ce8897c79773dbd28` |
| Model artifact image | arm64 child `sha256:1174f5266a0a4f8b968f2a8745671cb2546934373766797b256fe97d38088721` |
| Model bytes | `sha256:8645e5d6511cf0f78fa4a451e3bd86b3ab6b39bb5f9216ba32d2d9aebc852ee2` at `/artifact/mobilenet-v2-050.onnx` |
| Chart profile | `charts/chill/values-profiler-arm64.yaml` |

The runtime and artifact declarations are architecture-scoped facts. They do
not by themselves prove execution or admission on every arm64 DeviceClass.

## Raspberry Pi 5 CPU ORT / fixed

### Production execution and admission

- `rasp5-active-bs1-a1` completed and retained verified evidence
  `sha256:b5bff1c4284e56077b67eb0c2492fe67353aa79ee914ea535274a8cf6c6ec3fe`.
  It executed 2,304 batch-one calls in 30.001 seconds. Seventeen of 31 samples
  reported active throttling, so admission rejected it with
  `ThermalOrRuntimeThrottling`.
- `rasp5-idle-model-loaded-i1` completed after a 60-second settling period and
  retained admitted evidence
  `sha256:30c6870681ebbffac300b3ab959a2767e7be16909045de57db38717246cd8f38`.
  All 31 thermal samples were observable and none was active.
- The active and idle paths therefore prove L2 execution and the idle
  admission path, but not an L3 active/idle pair.

### Electrical blocker and producer correction

- A host trace during active load alternated between Raspberry Pi firmware
  values `50005` and `50000`. Interpreted as prefix-free hexadecimal,
  `0x50005` asserts current undervoltage/current throttling and history bits.
- edge-metrics commit `a7b3a63` corrected prefix-free hexadecimal parsing and
  was deployed as exporter image `sha-a7b3a63`, registry index
  `sha256:565c9d4a15787d548341ec7d32fc3af80e3e04e0991e69d381684d8829d7967e`,
  Helm revision 23.
- A 90-sample raw/exporter trace around
  `rasp5-active-thermal-parser-diag-v2` observed 68 `0`, 10 `50000`, and 12
  `50005` values. Eighty-nine pairs matched exactly; one sequential raw/HTTP
  pair crossed a state transition. Retained evidence
  `sha256:7b1b9951ed66f9add284fc85d0b8e7f6ac8a9263030b6469e053c6f6dfb28ba1`
  remained correctly rejected.
- Both diagnostics included an ownerless trace Pod and are not campaign
  trials. The remaining blocker is physical power supply/cable condition.

## Orange Pi CPU ORT / fixed

### Installed facts and wall source

- Commit `e4e3188` added the Orange Pi declaration to the shared arm64 profile;
  CHILL Helm revision 5 installed it without changing workload images.
- Commit `8f476ea` added Node `orangepi`, endpoint
  `http://155.230.34.143:9102/metrics`, and metric
  `shelly_power_total_watts`; CHILL Helm revision 6 installed the source.
- The live edge-metrics registry exposed exactly one attached meter,
  `shellyplugsg3-d0cf13d81a88`. The current v1alpha1 frozen identity binds the
  selected Node/kind/endpoint/metric entry and its digest; the physical serial
  is live topology corroboration, not a separately verified intent field.

### Thermal completeness

- The live 5.10 Rockchip kernel exposed seven thermal zones, three
  `thermal-cpufreq` cooling devices, and one `thermal-devfreq` cooling device.
- A 31-second simultaneous trace matched all 31 raw cooling states to
  `observation_available=1` and `active=0`. The producer test maps a nonzero
  cooling state to `active=1`.
- Latched and cumulative counters are absent, but CHILL admission accepts a
  complete direct-active channel and remains fail-closed if that channel is
  missing.
- Maximum temperature is diagnostic, not a threshold: 26 of 31 sequential
  raw/gauge pairs matched exactly; five differed by 0.924 C across the
  exporter's asynchronous refresh boundary.
- The device-specific `OrangePiCollector` remains an unrelated noisy stub.
  Wall power and typed thermal facts are produced independently by the Shelly
  and `ThermalCollector` paths.

### CPU and worker envelope

- The live Node exposed 8 allocatable CPUs, `16234812Ki` allocatable memory,
  and 227017156233 bytes of allocatable ephemeral storage. CPUs 0--3 reported
  capacity 422 and CPUs 4--7 capacity 1024; the contract therefore freezes a
  thread count, not homogeneous cores or per-core placement.
- Running Pods requested 605m CPU at the captured snapshot. The selected Run
  `cpuRequest` is 7 cores; including the two 5m observer requests, the Node
  total becomes 7.615 cores and leaves 385m of scheduling headroom. The same
  request is used for active and model-loaded-idle trials.
- `AllocatableCPUQuota/v1` freezes runtime CPU limit 8, ORT intra-op threads 8,
  inter-op threads 1, and `exclusivityClaimed=false`. The runtime checks the
  cgroup quota and requires an effective affinity of at least eight CPUs before
  constructing the ORT session.
- The shared CPU ORT policy requests 128Mi and limits runtime memory to 1Gi;
  observer requests are 16Mi and 32Mi with 64Mi and 128Mi limits. The 128Mi
  workspace and 1Mi coordination volume fit the live Node envelope. No
  Orange-Pi-specific execution-policy copy is required.
- The exact `cpuRequest` remains immutable Run configuration rather than a Go
  constant. Server-side CRD dry-run accepted 7 cores, and the focused Go and
  Python CPU-contract tests passed.

### Production admission and L3 promotion

- `orangepi-active-bs1-a1` was a fail-fast diagnostic under a 500 ms source
  timeout. Its retained evidence
  `sha256:ac53c550e6e1209dbb9810352feebacfc0f902ddc185b0a17f17bbba3ef390d9`
  remained `Insufficient`: all six failed reads were retained as
  `context deadline exceeded` at 0.503--0.510 seconds, producing
  `PowerReadFailures` and `PowerWindowCoverage`. It is not part of the admitted
  repeated set.
- The Run-level source timeout was raised to 900 ms while retaining the
  one-second interval and unchanged admission policies. Active Runs
  `orangepi-active-bs1-a2`, `a3`, and `a4` then retained distinct evidence
  objects with digests
  `sha256:b8955645f2c6f18b144fad63d50a82288d78be0469a9a00483877c4aa491a6b1`,
  `sha256:0a1e60544ccb0e98c15b1ac52b322e9aefc7a34486175818c2d72236f1aa35f5`,
  and `sha256:4090d88233c54d10838447b2b392023035000f6e6c221c43bd86a11df44badfb`.
  Each had 31 successful power/thermal samples, no failed reads, no active
  thermal sample, and an `Accepted` `wall-power-v2` verdict.
- Model-loaded-idle Runs `orangepi-idle-model-loaded-i1`, `i2`, and `i3` used
  the same 7-core CPU contract, 60-second observer-independent settling, and
  900 ms source timeout. Their retained evidence digests were
  `sha256:ea14ceb262c0ad7447e70c7d39316d7f566577952d4d122c08f96821b568f96b`,
  `sha256:785ab5a48ec135715b99d5e24c71ac4b2b4e5dcebec08924fe023cc3f327a6eb`,
  and `sha256:d6bc5844d38aa5009c726b28bd66bb48c9cfe3c36a8ab5b299bcea9ea4d50c95`.
  Each had 31 successful power/thermal samples, no failed reads, no active
  thermal sample, and an `Accepted` `idle-wall-power-v1` verdict.
- All six finalized and raw objects were re-read from the retained Evidence
  Store and matched their recorded size and SHA-256 identity. A read-only
  diagnostic replay of the source-defined projection produced one
  `execution-state-identity.v1alpha1` digest
  `sha256:76af5e787e39b6d3b75b99f2cf68d0fdad98955edeb78d472b587fcc7a81a509`
  and one owner-bound `external-environment-identity.v1alpha1` digest
  `sha256:5cf2ea9b25f2fe839b9a73833cb11f79046aa4b70954910c038d2748536c886a`
  across all six. The `measurement-protocol-identity.v1alpha1` digests
  `sha256:6a529e0937982d30d0148cda363fae6a3df41e1d1c6e69b833773b5e69bae42a`
  for active and
  `sha256:ed345c51d0d3d00f10848fb0fa5a960b13cac1e2f455a0a1432734b4d544172b`
  for idle were each stable within their mode.
- The first idle full intent digest differed from the latter two only because
  the frozen Node `resourceVersion` advanced from `277617801` to `277621852`.
  That value remains an exact audit snapshot but is deliberately absent from
  state, environment, and measurement-protocol identities; it is therefore
  not a pooling key. Plan 6's common gate now states the actual derivation
  boundary instead of requiring byte-identical full intents.
- `gopls` references and current source confirmed that both production arm64
  classes enter the same `ort-cpu` candidate, preparation, execution-policy,
  and worker adapter path. The chart uses the same digest-pinned runtime and
  artifact declarations plus wildcard active/idle execution policies; only
  installed DeviceClass/metric facts and immutable Run resource values differ.
  No Orange-Pi-specific Go path was added or extracted.

These retained active and idle admissions promote both Orange Pi CPU ORT rows
to L3. Multiple batches remain untested, so this is not L4 campaign evidence.

## Orange Pi RKNNLite / fixed

### Selected artifact and conversion identity

- The selected source is the canonical `mobilenet-v2-050` ONNX artifact,
  `sha256:8645e5d6511cf0f78fa4a451e3bd86b3ab6b39bb5f9216ba32d2d9aebc852ee2`.
  The derived target is `rk3588`, batch 1, non-quantized, with input shape
  `[1,3,224,224]` and output shape `[1,1000]`.
  That is the source-framework NCHW contract. The retained restricted runtime
  Pod invoked RKNNLite with float32 NHWC `[1,224,224,3]`; the two layouts are
  recorded separately rather than inferred from one another.
- The converter is the official x86-64 CPython 3.8 RKNN Toolkit2 v2.3.2 wheel
  from tag `v2.3.2` (`42aa1d426c0a9e0869b6374edba009f7208a1926`), wheel digest
  `sha256:13508573068e6011434ac64858a082234f783aeec2aee68fb9c140b65e1bf9bc`.
  The exact exporter recipe digest is
  `sha256:0d4bf28be3c14f1ecc5662a326a273441d9e057e3dfb9102dcdf143c14e2a234`.
- Two consecutive conversions under that source/tool/recipe/target/batch
  identity produced output digests
  `sha256:918fee5efba66f5fa8b3c960a857be2791912439e032f789b57e8a95960bf550`
  and
  `sha256:aa6afc1305754a76f17074a69bbc461cd4ee6487244776620be64efffef3b814`.
  RKNN output is therefore treated as nondeterministic: conversion identity
  never substitutes for the selected output-byte digest.
- The first output is selected and packaged by
  `build/docker/derived-model-artifact.Dockerfile`. Registry index
  `sha256:d241fe041c2b32fd25166a2cc6ea20eb88116861b2fed24103c7e66f79ac3856`
  contains arm64 runnable child
  `sha256:e33c6874d73c104e12329ab5752d4f6933189c8b4dc42e54986edb711328b50c`.
  Diagnostic Pod `rknn-artifact-diag-v1` on `orangepi` pulled that exact child
  and read back the selected 4,559,409-byte RKNN digest and the canonical ONNX
  digest. This closes the immutable artifact/conversion/fixed-batch catalog
  boundary, not execution or admission.

### Current host/runtime diagnostic snapshot

- Diagnostic Pod `rknn-host-contract-diag-v1`, UID
  `7b7fd03b-774e-4e6c-a0cb-2a23463a92dc`, ran on `orangepi` from exact arm64
  image child
  `sha256:8c82932ec275cf8557bb27d2a36c6e7282ff42ffdb4a86e0514d32f9e98aa193`.
  It observed RKNNLite package 2.3.2, driver 0.9.6, and host
  `/usr/lib/librknnrt.so` digest
  `sha256:d31fc19c85b85f6091b2bd0f6af9d962d5264a4e410bfb536402ec92bac738e8`
  with size 7,726,232 bytes.
- The same snapshot observed DRM character devices `card0`, `card1`,
  `renderD128`, and `renderD129`. HostPath visibility does not prove NPU
  allocation or exclusivity, and the diagnostic did not initialize a session.
- These two Pods are diagnostic cluster objects, not retained
  `ProfilingRun` evidence. The repository now has strict source and retained-
  report fields that separate the image-owned package from the expected host
  library, driver, device visibility, and allocation/exclusivity claim. New
  intents bind its order-normalized digest into physical state identity. No
  exact RKNN declaration is installed and no production Job verifies the
  expected host values, so both RKNN rows remain L0.

### Restricted-access diagnostic

- Diagnostic Pod `rknn-restricted-diag-v6`, UID
  `cd61f194-3e96-4f36-9f77-3210efa8dd77`, used the same exact RKNNLite image
  child and selected RKNN artifact on `orangepi`. It ran as non-root UID/GID
  65532 with supplemental groups 44 and 110, a read-only root filesystem, and
  exact host mounts for `card0`, `card1`, `renderD128`, `renderD129`,
  `librknnrt.so`, driver version, and platform-compatible bytes.
- With the runtime container privileged, the exact artifact loaded,
  `init_runtime(NPU_CORE_0)` succeeded, and one `(1, 1000)` inference passed.
  Render nodes alone had failed NPU open, and the cluster normalized a requested
  unmasked proc mount back to the default, so neither two-device exposure nor
  `procMount: Unmasked` is claimed as sufficient.
- This proves a narrower diagnostic access envelope than the historical root
  Pod with the whole `/dev/dri` directory. Privileged mode remains an explicit
  delivery requirement, not a physical host identity or proof of NPU
  exclusivity. The Pod is still diagnostic rather than retained
  `ProfilingRun` evidence, so the RKNN rows remain L0.

### Production worker image smoke

- Commit `1986d3a` produced the arm64-only RKNNLite worker index
  `sha256:2279f43c9a47679bb7e54489d35f911cb073792f6855c2e698e5415183345674`.
  Its arm64 runnable child is
  `sha256:35d71555f3c122c87adb4cc0318be25d9f983d87006b79d2c2e5c0e7e3559e91`.
- Pod `rknnlite-worker-image-smoke-1986d3a`, UID
  `23d33dcf-d855-4449-9dd4-b698efd15432`, pulled that index on `orangepi` and
  confirmed the arm64 worker binary, RKNNLite runner, package 2.3.2, and
  `NPU_CORE_0` API are present. Its Kubernetes `imageID` is the same index
  digest.
- This is image-delivery evidence only. It did not mount the mutable host
  contract, initialize the NPU, execute the coordinated measurement window, or
  produce retained raw evidence; the support rows therefore remain L0.

## Orange Pi RKNNLite / fixed

### Live corrections before the retained identity family

- `orangepi-rknnlite-active-bs1-a1` failed before runtime start because the
  installed artifact source path was `/artifact/model.rknn`; the already
  retained restricted diagnostic proved the image-owned path is
  `/artifact/mobilenet-v2-050-rk3588-bs1.rknn`. Commit `ef3ce41` corrected only
  that installation fact.
- `orangepi-rknnlite-active-bs1-a2` then copied and verified the artifact but
  exposed two independent delivery gaps. The privileged runtime could see the
  host cgroup namespace, so its own cgroup-v2 scope had to be resolved from
  `/proc/self/cgroup`; the previously installed observers also predated intent
  v1alpha8 and rejected `hostContractDigest`. Commit `ab17ef1` corrected the
  runtime scope resolution, and current observer images were rebuilt.
- Active `a3` and idle `i1` were retained and admitted with the corrected
  implementation, but their facts named buildx manifest-list digests. They are
  successful pre-finalization evidence, not members of the final L3 identity
  family because Plan 6 requires runnable child manifests.
- `orangepi-rknnlite-active-bs1-a4` declared the runtime child manifest and
  executed successfully, but containerd reported the cached parent-index
  digest for that repository. Evidence finalization rejected the mismatch.
  Commit `502787a` therefore publishes the arm64-only worker as a direct OCI
  manifest in the architecture-specific repository. Smoke Pod
  `rknnlite-arm64-manifest-smoke-ab17ef1`, UID
  `01730ba7-42b9-4008-8ba3-edf6933bc79a`, proved declared and observed
  `imageID` equality before the final Runs.

### Final installed execution identities

| Fact | Verified identity |
|---|---|
| RKNNLite worker | arm64 manifest `sha256:88ec18422621a844f2a169a291087a2a334cb8bac32a1a211296bf48cb2245a9`; package 2.3.2; `NPU_CORE_0` |
| MetricObserver | arm64 manifest `sha256:81661c7cec84d29de96ea58426382eb5f47e09687a21315138bb4a1cf9960288` |
| WorkloadObserver | arm64 manifest `sha256:872446ddfed212300296f610bfcc3dc98490be9ff4975af8f626a4ea95d45f6a` |
| RKNN artifact image | arm64 manifest `sha256:e33c6874d73c104e12329ab5752d4f6933189c8b4dc42e54986edb711328b50c` |
| RKNN artifact bytes | `sha256:918fee5efba66f5fa8b3c960a857be2791912439e032f789b57e8a95960bf550` at `/artifact/mobilenet-v2-050-rk3588-bs1.rknn` |
| Host contract | `librknnrt.so` `sha256:d31fc19c85b85f6091b2bd0f6af9d962d5264a4e410bfb536402ec92bac738e8`; driver 0.9.6; RK3588 token; four exact `/dev/dri` character devices |

The final active and idle Pods reported exactly these three runtime/helper
manifest digests as their Kubernetes `imageID` values. Runtime evidence also
reported package and library versions, driver, platform, devices, core mask,
input layout, model load/init success, selected batch, and active output shape.

### Retained L3 admissions and identity audit

| Mode | Run | Final evidence digest |
|---|---|---|
| active | `orangepi-rknnlite-active-bs1-a5` | `sha256:f3e85cdd238ac9dde1dfbadd631d759ac097b04c4d86680348f883d47c4dece6` |
| active | `orangepi-rknnlite-active-bs1-a6` | `sha256:1d329c8d358e8cf08df215e40e30a5d5a8f69ca07db84f8895c2635a0de6fd2b` |
| active | `orangepi-rknnlite-active-bs1-a7` | `sha256:47a9c45546a972c2049c67eb2630efe2b674c584f77688cc89f6ec70a6ee21c9` |
| model-loaded idle | `orangepi-rknnlite-idle-model-loaded-i2` | `sha256:a7a9b106ccfedd0ea477a29c22cc7e31d3043788f6c40e29575ccdfce59c0615` |
| model-loaded idle | `orangepi-rknnlite-idle-model-loaded-i3` | `sha256:0cddf43208dbfebfcc32415eabb467cfe525eb177e2c4910725f76df361a0d00` |
| model-loaded idle | `orangepi-rknnlite-idle-model-loaded-i4` | `sha256:0e72a81a13fda890d213f7314e01badfb9589a009a0df02ac07fdbea2c7fe0e7` |

All six Runs completed, durably reloaded, independently verified, and admitted
`Accepted`. A read-only live audit used `RetainedLoader` plus the production
active/idle projectors and proved:

- one shared execution-state identity,
  `execution-state-identity.v1alpha2@sha256:dec774c084f4da8fef51feb78208bc402f7aa5068dfa164d9185f88b01cf6905`;
- one shared owner-bound environment identity,
  `external-environment-identity.v1alpha1@sha256:1e462e5d2549ac2be0a98c2e74e8a8d07f9365766280cf052cc840227cb2ff31`;
- one active protocol identity,
  `measurement-protocol-identity.v1alpha1@sha256:b6a7ffd93c8d21c6908534ea1f14995611f3021509e7c880751f96644583e8ff`;
- one distinct idle protocol identity,
  `measurement-protocol-identity.v1alpha1@sha256:59cbd81ac19b738ee739d6aef26ee2353bcb6b818dc4b8d1232ee7d53920513f`;
- six unique Pod-derived Trial IDs and six unique verified raw-evidence
  digests.

Full intent digests changed only when the frozen Node
`resourceVersion` advanced (`277795071`, `277799068`, and `277803120`). Direct
intent diffs found no other within-mode drift. Node `resourceVersion` is an
audit field deliberately excluded from all three derivation identities, while
the Node UID remained fixed. The two support rows therefore satisfy Plan 6 L3;
multiple fixed-batch artifacts remain P7/L4 work.

## Orange Pi RKNNLite batch two / fixed

### Artifact and installed execution facts

- The repository-owned conversion recipe used canonical ONNX bytes
  `sha256:8645e5d6511cf0f78fa4a451e3bd86b3ab6b39bb5f9216ba32d2d9aebc852ee2`,
  RKNN Toolkit2 2.3.2 wheel
  `sha256:13508573068e6011434ac64858a082234f783aeec2aee68fb9c140b65e1bf9bc`,
  target `rk3588`, batch 2, and recipe identity
  `sha256:e70198b219f306c0939b4fcd36713dc26c77b381851e6c210fdb43d9554773ed`.
- The result is 4,943,089 bytes with digest
  `sha256:59462967d0461f0e926dae9a07a48900774b39189eba1d8e026d24b251ac884d`
  at `/artifact/mobilenet-v2-050-rk3588-bs2.rknn`. Artifact image manifest
  `sha256:c8cb0ac52e441377b5c7eed40c1db14995eadbac539ce6fd1dc51138e5c74bed`
  was read back on `orangepi` before installation.
- The final installed images are direct arm64 manifests: RKNNLite worker
  `sha256:94cb0627bc8bc6837c27f65438d29b1967cd0af68ea0ae0a5d3a15808c19aff3`,
  MetricObserver
  `sha256:aebc8edeb18cee92eef4ef28761543fd97579be46bb9597cf2404d7d2e15affe`,
  and WorkloadObserver
  `sha256:8dabc29500bda81cc04d4aebe56489293f193281a99a583d8d027bfff65488d7`.
  All six final Pods reported those exact declarations as Kubernetes
  `imageID` values.

### Admission corrections and final identity family

- The first batch-two active Run was admitted. The first idle request then
  exposed a stale API CEL rule that globally required idle batch one; commit
  `4295440` left artifact/batch matching to the frozen runtime boundary and
  retained only the mode-specific idle protocol validation in the API.
- `orangepi-rknnlite-idle-model-loaded-bs2-i1` subsequently failed because the
  already installed observer images still carried the old shared intent
  validator. The failed immutable Run remains diagnostic evidence and has no
  finalized evidence object.
- After rebuilding the observers, active `a2..a4` and idle `i2..i4` were all
  admitted. They used multi-architecture observer indexes, so they are valid
  pre-finalization measurements but not members of the Plan 6 L3 identity
  family, which requires direct runnable child manifests.
- Helm revision 9 installed the three direct arm64 runtime/helper manifests.
  The final retained family is:

| Mode | Run | Run UID | Pod-derived Trial ID | Final evidence digest |
|---|---|---|---|---|
| active | `orangepi-rknnlite-active-bs2-a5` | `aba5c5ad-6ea3-43d1-94dc-ddd9e5011212` | `3afa297f-c465-4c19-9a35-c03d475124d8` | `sha256:6dab9d576b6140c1a2638ac45507a2d6c4d931aba24207188f8e0e77f753a16a` |
| active | `orangepi-rknnlite-active-bs2-a6` | `364c6d42-8388-4399-8731-5edc2975a0be` | `cfd697cc-5748-47e6-b160-bcd73557a72a` | `sha256:0c6b075f019be2aef8acc599746b10b6a96517866237ba2d4e1abb65b9bf3b18` |
| active | `orangepi-rknnlite-active-bs2-a7` | `3a1252a3-f1af-4e5f-b723-f815aa2ec4c8` | `d41fc4fb-c56b-4b5d-87b4-c99c266b1097` | `sha256:188786598f8943a952e888b97d45c296118693a11b642aedd61ee53eb8f27d1c` |
| model-loaded idle | `orangepi-rknnlite-idle-model-loaded-bs2-i5` | `d7c270df-917f-42d0-9b30-c6c9f9f24f8a` | `112906a2-d8d3-4acf-93b4-922210a37f7d` | `sha256:53baca364527e8773183bf8318a4c7b242e8fbf00abb84d6ba4ad468e43c0b51` |
| model-loaded idle | `orangepi-rknnlite-idle-model-loaded-bs2-i6` | `0ca298be-ffed-44c8-a1c9-b52d80b9afe3` | `348e104e-b50e-4207-95a2-1993cf033936` | `sha256:7f842798cb95df5270c87c81a663c67dafc9675cb46e317e231f631e3312e92f` |
| model-loaded idle | `orangepi-rknnlite-idle-model-loaded-bs2-i7` | `74fa476c-561e-4f8a-a149-9981446a8fac` | `25a63803-416e-4308-a1d1-eda728f70ab4` | `sha256:6c4aa542b9bf2d850e87b7ccf098869e457d88988d222fdaedc4d39e7fd1a8a2` |

All six evidence objects were independently reloaded through the retained Job
binding, credentials, S3 reader, digest verifier, and production active/idle
projectors. The three active Runs share protocol identity
`measurement-protocol-identity.v1alpha1@sha256:5812007de5098d3f393f109b0e1006d4a0173ba70d5e54f26c7eef22710d41ad`;
the three idle Runs share the distinct protocol identity
`measurement-protocol-identity.v1alpha1@sha256:edec97456323f32fb3367821a88d7b2692c2a0f77fd4ef1d57775dd3569c35a3`.
Both modes share execution-state identity
`execution-state-identity.v1alpha2@sha256:941106399af8e860fd16b766f24166284172584442034d29e565e69419800501`
and owner-bound environment identity
`external-environment-identity.v1alpha1@sha256:1e462e5d2549ac2be0a98c2e74e8a8d07f9365766280cf052cc840227cb2ff31`.

### Diagnostic point and L4 blocker

A read-only diagnostic call used the existing `DeriveIdleBaseline`,
`IncrementalTrial`, and `Aggregate` functions with three independent trials per
mode. It produced:

| Quantity | Result |
|---|---:|
| model-loaded idle power | `2.7477521 +/- 0.0256116 W` |
| incremental energy at batch 2 | `0.00871756 +/- 0.00017566 J/item` |
| active latency p99 | `10.218714 ms` |

The intervals are the current 95% Student-t construction. The active Runs
completed 8,234, 8,288, and 8,264 items. The diagnostic point policy was
`live-diagnostic-min3-v1`; no production caller or persistence was added, so
this proves the L3 row and current derivation path, not a Plan 5/5a L4 claim.

The live audit also confirmed that the batch-one state identity is
`execution-state-identity.v1alpha2@sha256:dec774c084f4da8fef51feb78208bc402f7aa5068dfa164d9185f88b01cf6905`,
which differs from batch two. Current source binds the full execution state,
including artifact digest and path, into state identity; it separately binds
the artifact and observer images into protocol identity. Consequently the two
fixed-artifact rows cannot share the one execution state and one idle baseline
required by Plans 5 and 5a. Resolving that contract, without erasing physical
artifact identity or assuming equal idle power, is the remaining P7-to-L4 gate.

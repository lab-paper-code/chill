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
  library, driver, device visibility, and allocation/exclusivity claim. No
  exact RKNN declaration is installed, no physical state identity binds that
  host contract, and no production Job verifies it, so both RKNN rows remain
  L0.

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

# Plan 6: Execution-Support Expansion

## 결과 요약 — active
- Decision: promote only exact DeviceClass/model/artifact/path/runtime/backend/power/workload/protocol rows from retained production evidence; catalog, installed facts and standalone smoke are not execution proof.
- Completed: Orange Pi CPU ORT L3; Orange Pi RKNN base L3 and recorded multi-batch L4; AGX Orin CPU ORT MODE_50W/MAXN L3. Raspberry Pi 5 remains L2 on a physical electrical blocker. Historical evidence below is not current-schema live proof.
- Current cutovers: scientific admission removed @`788bcf93`; `NodeActuation` CRD/actuation Lease removed @`f4b0b685`. Node request generation/read-back is authoritative, not old owned-transition/Lease procedures.
- Evidence: [Plan 3](03-profile-admission-derivation.md), [Plan 7](07-batch-energy-optimization-control-path.md), [Plan 8](08-declared-batch-size-artifacts.md), [Plan 10](10-transition-cost-measurement.md). Retain old experiment conclusions without generalizing them.
- Remaining: Raspberry Pi electrical repair/re-measurement, NVIDIA deployment/apply/read-back/pair-cost proof, unavailable Orin Nano, bounded Jetson failure injection and new exact runtime/model rows.

## Support levels and recorded queue

| Level | Required evidence |
|---|---|
| L0 Catalogued | Live Node discovered into exact DeviceClass; repository catalog presence recorded separately. |
| L1 Compatible | Exact artifact and trusted runtime declaration evaluate Compatible. |
| L2 Executable | Production ProfilingRun Job succeeds on exact Node/path/mode. |
| L3 Measurement-ready | Active and model-loaded-idle evidence durably reloaded/verified and numerically usable; old records called this admitted. No current scientific admission verdict. |
| L4 Campaign-ready | Repeated trials/multiple batches under one verified frozen curve plan and exact shared artifact state; current optimizer is Plan 7, not saturation confirmation. |

Levels are cumulative. A successful Job without retained verified evidence does not promote a row.

| Slice | Recorded level | Current standing / next gate |
|---|---|---|
| Raspberry Pi 5 CPU ORT / fixed | L2 | Paused until electrical correction and fresh active/idle evidence. |
| Orange Pi CPU ORT / fixed | L3 | Three active + three idle production trials; shared arm64 CPU adapter, device differences in facts/resources only. |
| Orange Pi RKNNLite / fixed base | L3 | Six retained active/idle Runs; archived superseded-schema evidence is not current-code-reloadable. |
| AGX Orin CPU ORT / MODE_50W | L3 | Three active + three idle, batch 1; v1alpha4 archival evidence, not L4. |
| AGX Orin CPU ORT / MAXN | L3 | Three active + three idle, batch 1; v1alpha4 archival evidence, not L4. |
| Orange Pi RKNN model/batch expansion | L4 | One artifact, active batches 1/2/4 × three trials + three batch-1 idle = 12 Accepted, `Stopped/NoFurtherImprovement`; production recomputation matched incremental work exactly. Current-schema remeasurement needed. |

The L4 campaign/Run objects are archived outside the testbed Evidence Store; strict current readers cannot reload execution evidence v1alpha4. The result establishes neither universal `b_sat=1` nor statistical confirmation. Separate historical batch-1/batch-2 artifact diagnostics were L3; new campaigns use one declared-batch-size artifact, one state key and one idle baseline (ADR 0016).

## Stable execution boundary

- Keep CPU ORT, RKNNLite, ORT CUDA and ORT TensorRT explicit adapters; no generic runtime plugin or device-specific CPU ORT fork. First model remains `mobilenet-v2-050` until its path reaches L3.
- DeviceClass owns stable supported modes; the request selects a mode, the actuator owns independent actual read-back. Node-wide mutation belongs only to the per-Node actuator; workers/discovery remain unprivileged ([ADR 0015](../../../adr/0015-node-power-actuation-boundary.md)).
- Freeze exact runnable child OCI references for runtime/artifact/metric/workload images, not image-list identities. Execution path selects the catalog envelope; realized family/backend selects the runtime adapter.
- Expected image packages, mutable host library/driver/platform/devices, access privileges/groups and Kubernetes allocation/exclusivity remain separate facts. No CPU affinity-only binding claims exclusivity; device visibility does not prove NPU isolation.
- RKNN production delivery uses runtime-only hostPaths/privilege and Pod supplemental groups; observers receive neither. Worker compares image package/host ABI/device/batch/input observations to the sealed delivery. `NPU_CORE_0`, NHWC and load/init outcomes are retained; ORT-only thread settings are absent for RKNN.
- Active and idle share exact execution state but retain distinct mode-specific protocols. Idle uses the first planned batch and executes no inference. Only current Go/Python schemas pair; [worker contract](../components/measurement-worker.md) owns their versions.
- Required state must be independently read back before Job creation. Mismatch ends `RequiredStateNotConfirmed` without a Job; structural evidence contradiction ends `EvidenceInvalid`, not indefinite error requeue. Current request/bootID/generation rules: [Node agent §3](../../platform/node-agent.md#3-노드-전력모드-node-label과-generation).
- Live results belong to immutable evidence referenced by Run/campaign status, joined by `kubectl chill runs show NAME`; do not duplicate deployment/output logs here. Package/kind evidence is distinct from live testbed evidence, and repository e2e never uses the testbed kubeconfig.

## Retained AGX Orin comparison

Exact `jetson-agx-orin-64g / mobilenet-v2-050 / ort-cpu`, batch 1, 75 s, three active + three loaded-idle trials per state:

| Mode | P_idle (W) | P_act (W) | Throughput (items/s) | Incremental energy (J/item) |
|---|---:|---:|---:|---:|
| MODE_50W | 10.3989 ± 0.0015 | 19.151 ± 0.072 | 200.18 ± 0.33 | 0.04372 |
| MAXN | 10.4323 ± 0.0147 | 30.326 ± 0.078 | 265.50 ± 0.49 | 0.07493 |

MAXN gives 1.326× throughput at 1.714× incremental energy; mean/p99 latency ratios 0.754/0.763. Idle means differ by 0.0334 W, so idle alone does not characterize the knob. These v1alpha4 Run measurements are archival, not separate current transition evidence or proof for another Node/model/batch/RKNN/GPU/TensorRT path.

## Open gates

### Raspberry Pi 5 CPU ORT / fixed

Correct `rasp5` supply/cable and observe `get_throttled=0` throughout a 30-second active diagnostic. Then retain a clean active Run and three new active + three new idle trials under the unchanged post-repair execution/protocol conditions, reload all six and compare state/identities before L3. Do not pool pre-repair evidence or reintroduce the retired admission policy.

### Jetson NVPModel

Recover `orin-nano` and probe its complete mode ID/name set; Kubernetes and configured SSH were unreachable during the 2026-08-06 audit. Host mutation success requires separate parsed ID/name read-back, not command exit. Current-contract live proof and bounded failure injection for unsupported NVPModel ID/read-back mismatch remain open. Connect terminal `RequiredStateNotConfirmed` to an operator alert sink. Elapsed settling is not thermodynamic equilibrium; add a thermal-stability criterion only if measured evidence requires it.

<a id="gpu-clock"></a>
### NVIDIA GPU-clock implementation and live gate

Contract: [PA-NV-01–05](../../../spec/platform/SRS.md), [ADR 0015](../../../adr/0015-node-power-actuation-boundary.md), [ADR 0020](../../../adr/0020-transition-duration-measurement-boundary.md). Read-only inspection on 2026-10-27 retained `O/u3-nvidia/{genesis2-readonly-inspection,genesis3-readonly-inspection,genesis2-readback-permissions}.txt`; this proves tooling/permissions, **not mutation**.

| Host | GPU UUID | Driver | max graphics/SM | default/min/max power |
|---|---|---|---|---|
| genesis2 | GPU-7ba62428-ecac-9bd9-a546-40e2a748daed | 555.58.02 | 2100 MHz | 250/100/280 W |
| genesis3 | GPU-24ddd1e2-68a6-ca12-34d6-448c2697f325 | 555.58.02 | 2175 MHz | 300/100/360 W |

Both are single RTX 2080 Ti/Turing, max memory 7000 MHz; supported graphics include 1500/1200/900 MHz in 15 MHz steps, idle graphics=SM=300 MHz. Application clocks are N/A. Tooling exposes `-lgc min,max`, `-m 0|1`, `-rgc`; passwordless sudo, `/dev/nvidia0`/`nvidiactl` 0666 and RuntimeClass `nvidia` exist. `GPU_1500` reference, `GPU_1200` middle, `GPU_900` low are common points; memory/power limits stay unchanged. `fixed` means uncontrolled driver defaults, not 1500 MHz. Watts/energy remain unmeasured; differing board ceilings require separate U4 sharing evidence.

- Adapter uses bounded argv/no shell, exactly one GPU, `-i 0 -lgc MHz,MHz`, independent graphics+SM query and separate controller read before settling. `clocks.max.sm` is a ceiling, not a lock; driver 555.58.02 has no locked-range getter. With no request, report uncontrolled `fixed` without rewriting raw MHz.
- Fresh GPU-mode requests apply and settle even if instantaneous MHz matches. Restart only observes an already successful request; unfinished requests still apply. Reboot loses the nonpersistent lock, changes bootID/actual clocks and does not reapply successful old intent. No startup/systemd hook.
- GPU-only modes preserve per-Node CPU sets (genesis2 `0-5`, genesis3 `0-11`), not one class mask. Pre/post apply facts accumulate in one local snapshot before patching, retaining final generation.
- Apply/read-back failure restores an observed declared point and verifies it, or `-rgc` for an undeclared previous point. Cleanup uses a fresh bounded context after timeout/cancellation, joins errors and never publishes success. Default reset cannot prove the unlocked register; retain raw clocks and that limitation.
- Separate `chill-node-agent-actuation-nvidia` starts on genesis2 only; Jetson operand is unchanged. Both aggregate readiness/stop behavior. NVIDIA uses root, `SYS_ADMIN` only, no escalation/hostPID, read-only rootfs, RuntimeClass nvidia, `NVIDIA_VISIBLE_DEVICES=all`, `utility`, no GPU resource request; read-only binary/loader/driver mounts, no Jetson sysfs/state/systemd mounts. Runtime denial is failure, not privileged fallback.
- CampaignOps owns deployment/live proof after genesis2 measurements stop. Build/publish node-agent amd64+arm64 and operator; preserve runtime-7 digest (no Profiler/firstfloor rebuild for actuation). Use current complete retained Helm values, reviewed immutable digests and genesis2-only affinity; do not overwrite unrelated live values with a historical snapshot.
- Preserve Node/bootID/UUID/power-limit/Pods/driver query and no-co-tenant proof. Use the current atomic **mode+generation** request contract (not the retired requested-at-only patch). Prove `GPU_1500→GPU_900→fresh GPU_1500` with success, graphics=SM read-back, verifiedAt/readyAt, stable bootID; stop on failure.
- Before pair costs, retain an exact admitted runner profile at GPU_1500 and Complete exact-state idle baselines at both GPU_1500/GPU_900 with source CPU quota/batch. Use [NVIDIA sample](../../../../config/samples/profiling/edge_v1alpha1_powermodetransitioncost_nvidia.yaml) in serving namespace, fresh CR/Service, real container `firstfloor`, and frozen runner-profile annotation; no mock Service. `acceptedCountTarget=5`, `attemptLimit=8` means five Accepted trials **per direction**, not command durations. Keep all evidence policies; [Plan 10](10-transition-cost-measurement.md) now requires the declared settling revision/fresh measurement.
- After terminal measurement, request/verify GPU_1500. Driver-default cleanup removes the desired request per the current generation protocol, performs one CampaignOps-only `sudo nvidia-smi -i 0 -rgc`, and records result/query/Node/UUID/power-limit/bootID. Report default-policy cleanup, not exact lock-register proof or GPU_1500 success; do not reuse locked profiles afterward. NVIDIA Pod restart must query, not reapply. Reboot proof needs a separate idle window, stale-state rejection and fresh request—never reboot during measurements.

The live gate stays open until deployment digests, apply/read-back/return evidence and accepted direction costs exist. Read-only inspection and local tests do not close it.

## Deferred vertical slices

| Slice | Entry gate | Required boundary / completion |
|---|---|---|
| Orin Nano ORT TensorRT / one mode | Ready/L0 and proven power-mode protocol | Freeze ORT/TensorRT/CUDA/L4T/driver/artifact/GPU/mode; requested vs registered provider chain; fallback/effective precision Unknown unless observed. Separate engine build/cache creation/reuse/invalidation/identity from measurement. One exact row reaches L3. |
| Remaining Jetson modes/classes | Previous TensorRT row L3 | Independent read-back and idle baseline per mode; do not reduce pairs before source-state independence is measured or transfer results across families. |
| RTX CPU/CUDA/TensorRT paths | Existing fixed support retained; new clocks pass live gate | Separate adapters; exact GPU/device-plugin/runtime/wall/observer facts, GPU allocation distinct from exclusivity, per-mode active/idle/d/preparation/removal and accepted pair-cost evidence before planner use. |
| Model/batch expansion | Owning path L3 | New exact artifacts/models/batches need their own frozen evidence; old diagnostics/campaign do not promote the class/runtime/model family. |

[Profiling index](../README.md) · [runtime boundary](../foundations/runtime-execution/README.md)

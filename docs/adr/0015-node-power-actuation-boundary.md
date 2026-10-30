# 0015 Node Power Actuation Boundary

## Status

Accepted. Its per-Run actuation state machine is narrowed by [0018 Transition Ownership Boundary](0018-transition-ownership-boundary.md); the NodeActuator boundary defined here is unchanged.

## Context

`DeviceClass.spec.powerModes` declares stable mode names and typed actuator targets. It does not report a Node's current mode or convergence. ADR 0011 already requires future per-Node desired and observed execution state to live outside the class-level capability resource.

Jetson NVPModel and GPU-clock changes are Node-wide. They can affect workloads other than the profiling Job, survive a worker process failure, and require an independent read-back and restoration. The current measurement worker is a bounded, unprivileged one-shot execution plane. The Profiler is a reconstructable `ProfilingRun` control plane, while node-discovery is a read-only inventory process. Giving any of those existing execution paths an incidental host shell would erase their reviewed responsibility and privilege boundaries.

## Decision

Introduce a dedicated `NodeActuator` process as the only CHILL component that may mutate Node-wide power state. It runs once per eligible Node as a narrowly privileged DaemonSet operand. The CHILL Operator owns that operand's lifecycle under `ChillSystem`; Helm owns its ServiceAccount, RBAC, configuration, and host-access declaration. Node-discovery remains read-only and is not extended into the actuator.

The Profiler owns the per-Run orchestration state machine. It selects and freezes the requested `DeviceClass` mode, creates a durable actuation handoff, waits for independently observed convergence and settling, starts the measurement Job, and waits for verified restoration before completing the actuated Run. It never invokes a host command, uses SSH, or receives actuator privileges. The measurement worker consumes only the already verified target state and remains unprivileged.

The durable handoff introduced with the first implementation must separate:

- desired mode name, typed actuator kind and target, selected Node name and UID, configured actuation-policy name, and the owning Run UID;
- the previously observed mode, target observation, and final restored observation;
- apply, verification, settling, restoration, and failure timestamps; and
- desired lifecycle stage from observed phase, reason, and terminal failure.

RunConfig binds the selected mode, typed target, and policy name. Mutable convergence and restoration observations do not rewrite it. The exact Kubernetes API shape for the transition is delivered with its controller and consumer; it must follow the separate per-Node state rule in ADR 0011 rather than adding mutable state to `DeviceClass.status`.

The NodeActuator serializes Node-wide changes with one `coordination.k8s.io/Lease` keyed by Node UID. A holder captures the previous state through `Observe()` before `Apply(target)`, verifies success through a second independent `Observe()`, retains the lock through measurement and restoration, and releases it only after the previous state is observed again. Command exit alone never proves apply or restore success.

The initial safety policy is fail-closed. Before mutation, the Profiler and actuator must agree on the exact Node and policy identity, the actuator must hold the Node Lease, and configured workload-exclusivity checks must find no unapproved workload that would be affected by the change. A missing or ambiguous observation, lock loss, policy drift, unexpected workload, or target read-back mismatch prevents measurement and triggers restoration.

Restoration is an actuator responsibility, not a best-effort Profiler cleanup callback. The durable handoff contains a bounded deadline so the actuator can restore after Profiler failure or restart. Run failure, cancellation, worker timeout, and normal completion also request the same idempotent restoration path. Restoration failure remains visible as a terminal actuation condition, retains the Node from new CHILL actuation, and requires operator attention.

The Jetson NVPModel and discrete NVIDIA clock adapters share the explicit `Observe()` / `Apply(target)` port, but not host tooling or privileges. The NVIDIA adapter selects one GPU (index 0, rejecting multiple query rows), uses `nvidia-smi -lgc` for exact MHz targets and `-rgc` for default-policy cleanup, and independently queries current graphics and SM clocks. Driver 555.58.02 on the two genesis hosts exposes no locked-range getter and reports application clocks as N/A: current MHz is an observable operating point, **not proof of a lock register**. Exact targets only (min=max, mode 0) are admitted; N/A, mismatched SM/graphics, and unsupported targets fail closed.

On apply/read-back failure the adapter restores the previously observed declared clock mode, or requests driver defaults when the previous point was not a declared mode. Cleanup errors remain joined to the original failure; reset command exit never proves an unlocked policy. Returning to an earlier successful mode uses a new desired-mode request under ADR 0020, not a hidden per-Run restoration lifecycle. Restart observes successful requests without resetting; host reboot resets the non-persistent NVIDIA lock to driver defaults and the new bootID/read-back invalidates old operating-state evidence. No startup reapply or host systemd service is installed. See [PA-NV-01–05](../spec/platform/SRS.md) and the [GPU-clock procedure](../design/profiling/plans/06-execution-support-expansion.md#gpu-clock). No composite actuator is added.

The NodeActuator also owns the host boot-ordering prerequisite of Jetson power modes. A mode that sets GPU power-gating masks applies at boot only when `nvpmodel.service` runs before `nvidia-cdi-refresh.service` (nvidia-container-toolkit), whose `nvidia-smi` call creates the GPU golden context and fixes the masks. With `nodeAgent.actuation.jetsonNVPModel.ensureBootOrdering` (default `true`), an init container of the actuator Pod, using the actuator image and holding zero capabilities, ensures exactly one file: `/etc/systemd/system/nvidia-cdi-refresh.service.d/10-after-nvpmodel.conf` containing `[Unit]` and `After=nvpmodel.service`. It writes that file only on a Node whose `/etc/systemd/system` or `/lib/systemd/system` holds `nvidia-cdi-refresh.service`, rewrites it only when its content differs, and leaves every other Node unchanged. It mounts `/lib/systemd/system` read-only and `/etc/systemd/system` writable, and reaches only those two directories. systemd reads the drop-in at the next boot, the only boot the ordering affects, so the container performs no `daemon-reload`, unit restart, or Node restart.

## Consequences

Power actuation adds a separate operand boundary: the `chill-node-agent-actuation` DaemonSet with its own ServiceAccount. It is not folded into the Operator, Profiler Deployment, the unprivileged `chill-node-agent` DaemonSet, or measurement Job. The eventual production package and its allowed imports must be mapped in `.go-arch-lint.yml` in the same implementation change.

The first implementation requires an opt-in `ChillSystem`/Helm lifecycle, least-privilege host access, a durable handoff and Node Lease, a pure typed actuator core, and a Jetson host adapter. API, generated manifests, Helm, package, envtest, failure-recovery, and focused live apply/read-back/restore gates remain mandatory before any selectable mode becomes profiling support.

Existing fixed-mode Runs and retained intents are unchanged. Catalogued Jetson mode names remain capabilities only; the read-only catalog audit does not enable actuation or promote any Jetson row.

# Node Power Actuation Boundary

## Status

Accepted

## Context

DeviceClass power modes declare capabilities. Node-wide power changes need a dedicated host writer and independently observed convergence, following [DeviceClass Status Boundary](0011-deviceclass-status-boundary.md).

## Decision

Run the NodeActuator as a separate privileged DaemonSet operand for each configured actuator family. The operator owns its lifecycle under `ChillSystem`; Helm supplies configuration, RBAC and host-access declarations. Discovery and measurement workers remain unprivileged host-state consumers.

The typed host port exposes `Observe()` and `Apply(target)`. The controller reads desired Node metadata, validates the selected mode and policy, persists transition state, and accepts convergence only after independent read-back and configured settling. Node labels and annotations hold the observed mode, boot ID, generation and last transition.

ProfilingRun consumes established state. Campaigns and serving operations request independent transitions under [Transition Ownership Boundary](0018-transition-ownership-boundary.md). There is no Run-owned hold/restore lifecycle or actuator coordination Lease.

Jetson NVPModel and NVIDIA clock adapters have separate host tooling and privileges. Jetson restart behavior follows [Power-Mode Reachability Boundary](0017-power-mode-reachability-boundary.md).

The NVIDIA adapter selects GPU index 0 and rejects multiple query rows. It admits a declared exact MHz target with equal minimum and maximum and omitted mode. It applies `nvidia-smi -lgc`, independently reads graphics and SM clocks, and fails closed on unavailable or mismatched observations. Observed MHz describes an operating point; it does not prove a lock-register value.

On NVIDIA apply/read-back failure, the adapter restores the previously observed declared point or requests driver defaults with `-rgc`. It retains cleanup errors and independently observes the result. Agent restart observes successful requests without reset; a host reboot returns the non-persistent lock to driver defaults. Boot identity and fresh read-back invalidate stale state evidence.

With `nodeAgent.actuation.jetsonNVPModel.ensureBootOrdering` enabled, the Jetson operand's capability-free init container ensures `/etc/systemd/system/nvidia-cdi-refresh.service.d/10-after-nvpmodel.conf` contains `[Unit]` and `After=nvpmodel.service`. It acts only where the refresh service exists and only rewrites differing content. The service directories are the sole mounts: `/lib/systemd/system` read-only and `/etc/systemd/system` writable. The next boot reads the drop-in; the container performs no daemon reload, service restart or Node restart.

## Consequences

Host mutation stays outside the operator, profiler, discovery process and measurement Jobs. Configured host access is family-specific and validated; Jetson reboot access is limited to host PID and `SYS_BOOT`.

The platform contracts cover [승인된 단일 GPU 고정 클럭](../spec/platform/SRS.md#승인된-단일-gpu-고정-클럭), [독립 클럭 조회와 안정화](../spec/platform/SRS.md#독립-클럭-조회와-안정화), [클럭 실패 복원과 결과](../spec/platform/SRS.md#클럭-실패-복원과-결과), [재시작과 재부팅의 상태 구분](../spec/platform/SRS.md#재시작과-재부팅의-상태-구분), and [NVIDIA 호스트 접근과 운영 권한](../spec/platform/SRS.md#nvidia-호스트-접근과-운영-권한).


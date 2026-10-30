# 0002 Demonstration Two Duplicate Preparation

- What happened: in the second end-to-end demonstration, a programmable logic   controller (PLC) routing-policy change temporarily hid its existing genesis2   instance from the scheduler snapshot. The planner prepared its slot again;   two Pods occupied the same CPU `[2]`.

- Impact: all **36 subsequent decisions** stopped at   `ExclusiveCPUInfeasible`, with **zero candidates examined and zero actions**.   MobileNet could not expand: phase-B success was **0.67% (243/36,112)** with   p95 **2.0 s**. PLC and SmolVLM2 remained 100%; phase C was 100% for all three.   Reset also initially failed because the slot had multiple physical instances.

- Root cause: the native client returned policy acknowledgment (ACK) pending as   an error; observer unavailability set `Native=nil`; snapshot construction   interpreted that as absence. Preparation then interpreted unobserved CPU   occupancy as spare capacity. The global exclusive-CPU check correctly refused   the resulting unsafe state; minimum demand and route serialization were not   the cause. The initial observation gap was later traced to ACK semantics,   superseding the first report's unknown-cause diagnosis.

- Fix: treat ACK pending as an unapplied-policy observation, retain the last   confirmed instance, prohibit unsafe changes on uncertain Nodes, and reset all   exactly owned physical instances even when several share one slot. Then adopt   the Pod UID ledger, common scope reservations and planning outside reservations   in [ADR 0023](../adr/0023-resource-ledger.md), rather than stop at local guards.

- Prevention: retain physical identity/allocation across observation failures;   distinguish sent policy from acknowledged policy; require exact termination   proof for removal. Existing coverage is anchored in   `internal/serving/firstfloor/client_test.go`, `internal/serving/kube/observer_test.go`   and `internal/serving/kube/reset_test.go`; normative cases are scheduler   SRS AC-34–39 and [implementation §5.4](../design/serving/scheduler-implementation.md#54-자원-장부--sch-5052-ac-3739).

- Raw evidence: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/demo-20261026-1/`   and `intent/checkpoints.md` under the same operations root. Later demonstration   three reached **85.36%** MobileNet phase-B success, not zero transition loss.

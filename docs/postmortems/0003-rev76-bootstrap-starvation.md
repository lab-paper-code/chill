# 0003 Revision 76 Bootstrap Starvation

- What happened: the first expansion test after the resource-ledger deployment
  failed its empty-start gate. For **282 s**, all three Workloads lacked an owned
  Ready instance with a positive route; an early SmolVLM2 placement reserved the
  only Node available to the programmable logic controller (PLC) Workload.
- Impact: startup failed before the intended independent MobileNet expansion
  could be demonstrated. Passing ledger reviews had not established successful
  bootstrap under the actual asynchronous capacity-row completion order.
- Root cause: **two defects**, not a generic slow start. Candidate ranking compared
  only the worst exceedance, so an impossible PLC Workload made every candidate
  tie at infinity and energy favored keep. Separately, removing the global barrier
  also removed its accidental information-gathering delay: the first SmolVLM2
  FP32 capacity row could commit a **268 s** preparation before other rows arrived.
  The comparison defect predated the ledger and had been concealed by the barrier.
- Fix: [ADR 0024](../adr/0024-candidate-comparison-and-bootstrap.md) adopts
  lexicographic minimization of descending Workload exceedances and an empty-pool
  capacity wait, default **10 min**, configurable **0..1 h**. Wait only with no
  physical instances and no reservations; timeout proceeds with current evidence
  and records what is missing. Do not restore a normal-operation global barrier.
- Prevention: retain the actual arrival-order fixture
  `internal/serving/testdata/rev76-bootstrap.json`, including no-wait reproduction,
  all-row joint placement (**five prepares**), and the impossible-Workload case.
  Normative cases are [scheduler SRS](../spec/scheduler/SRS.md) AC-40–41.
  Known limit: after timeout or in normal operation, pending Workloads do not own
  otherwise usable Nodes; the unadopted anti-monopolization option is not implied
  by this fix.
- Recovery evidence: revision 77 reached three Ready/routed Workloads in
  **166.85 s**. The next gate failed on an independent refresh-time Ready erasure;
  that observer defect was fixed separately and must not be counted as bootstrap
  success through the whole demonstration.
- Raw evidence: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/exp-20261026-ga/`
  and `intent/checkpoints.md` under the same operations root.

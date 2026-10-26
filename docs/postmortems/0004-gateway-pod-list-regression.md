# 0004 Gateway Per-Probe Pod List Regression

- What happened: after the [resource-ledger cutover](../adr/0023-resource-ledger.md),
  demonstration three's programmable logic controller (PLC) p95 reached **67 ms**
  against its **50 ms** objective. ChillServer approached its six-core limit;
  prior comparable operation had used about **0.6–1.6 cores**. The user required
  profiling and a root-cause fix, explicitly not scaling the server to hide it.
- Impact: gateway latency obscured native performance and invalidated using this
  configuration for evaluation scenario E5. Controlled fixed-placement windows
  at approximately **60/140 requests/s** had PLC p95 **56.3/58.3 ms**, even with
  100% PLC request success. Native elapsed p95 was about **8 ms**.
- Root cause: `internal/serving/kube/observer.go:refreshLocked` revalidated each
  native probe by listing the **entire namespace**, without a selector: **1,053
  Pods**, **981 Succeeded**, about **24 MiB** per list. Profiling attributed
  **82.9%/76.1% CPU**, **95% of allocations (~735 MiB/s)** and **68% of live heap**
  to this path. The new ledger publication itself was not the cause:
  `InstallInstances` used **0.02% CPU**, with about **68 ms** measured mutex delay.
- Fix: select by the exact Pod UID label for per-source revalidation, still
  retaining UID/container verification and same-label duplicate detection.
  Do not merely filter terminal Pods: they can carry relevant duplicate identity.
  Keep the distinct lifecycle inventory path. The current selector is in
  `observer.go:refreshLocked`; regression coverage is in
  `internal/serving/kube/observer_budget_test.go`.
- Recovery evidence: the same low/high-rate fixed-placement measurements reduced
  ChillServer CPU from **4.71/4.72 to 0.47/0.63 cores**, charged memory from
  **3,965 to 2,006 MiB**, and PLC p95 from **56.3/58.3 to 15.6/10.7 ms**.
  This removed the need to change ObservationLog writer locking; JSON marshalling
  outside its lock was deferred because Flush/RequestClosed ordering is shared.
- Prevention: measure the real response size and selector, not only API-call
  counts. Do not add capacity before profiling CPU/allocation/mutex/block paths.
  Fix finished-measurement retention independently using
  [ADR 0026](../adr/0026-finished-measurement-pods.md); cleanup is not a substitute
  for making a per-instance query per-instance. A later 30-minute ledger soak
  recorded **0 invariant violations** across **90 cycles / 93 operations**.
- Raw evidence: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/perf/20261026T040607Z/`,
  `perf/20261026T052424Z/` and `campaign/v2/soak-20261027/` under that operations
  root. These are observed windows, not a newly established gateway upper bound.

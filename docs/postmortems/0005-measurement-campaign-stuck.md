# 0005 Measurement Campaign Stuck on Policy-Mismatched Evidence

- What happened: Batch Energy Optimization (BEO) campaign   `orangepi-017-plc-cpu-q4` remained `Measuring` from **2026-10-23 06:48:47Z**,   although its **16 Jobs** had succeeded and all associated Run evidence was   verified. The Job-retention inventory exposed that this was not live measurement.

- Impact: status stayed at **15 completed trials**, no valid final profile was   published, and ordinary reconciliation repeated the same deterministic failure.   A `Measuring` label alone therefore overstated ongoing physical activity.

- Root cause: batch `active/b8` contained **four successful trials (a1–a4)**,   while the recorded energy-objective policy required **three per batch**.   Derivation correctly rejected `search points do not match the recorded   energy-objective policy`, but returned the error before recording a terminal   parent status. The origin of the fourth successful trial is **unknown** because   the creation history was unavailable; the terminal-state fix does not claim   to establish that historical cause.

- Fix: classify contradictory evidence as   **`Failed / EnergyObjectivePolicyMismatch`**, with `MeasurementComplete=False`,   current observed generation, diagnostic reason and no successful result.   Detect excess successful trials before creating another trial or deriving a   curve. Preserve all **16 Runs and their evidence**; repeated reconciliation of   the same terminal generation must not resume derivation or measurement.   Current anchors are `internal/profiling/controller/reconciler.go`,   `internal/profiling/campaign/energy_optimization.go` and   `internal/profiling/derivation/energy_optimization.go`.

- Prevention: enforce the recorded successful-trial count as a scientific   contract, not a retryable infrastructure error. Do not arbitrarily discard one   of four trials, patch the campaign to Complete, or select the favorable three:   that would introduce selection bias. If new measurement is needed, create a   newly named BEO under an explicitly recorded policy. The authoritative contract   is [the BEO control-path design](../design/profiling/plans/07-batch-energy-optimization-control-path.md).

- Related decision: [ADR 0026](../adr/0026-finished-measurement-pods.md) preserves   legacy Job/evidence access and separates cleanup from scientific completion.   No additional ADR changes the scientific policy for this incident.

- Deployment boundary: as of the **2026-10-26 07:34Z handoff**, this terminal-failure   fix existed in repository source but was **not deployed**; revision 84 still   ran the preceding controller. Revision 85 deployment and observing orangepi-017   become Failed were the next operational steps, not completed recovery evidence.

- Raw evidence: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/checkpoints.md`   and `intent/measurement-job-inventory-20261027.csv` under the same operations   root. Per-Job inventory and raw retained evidence remain outside the repository.

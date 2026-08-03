# Retained paired-transition evidence

This directory is the retained evidence bundle for
[`../../jetson-xavier-nx-engine-cache-pair-v1.md`](../../jetson-xavier-nx-engine-cache-pair-v1.md).

- `*-power.tsv` contains direct wall, rail, temperature, and throttle samples.
- `*-boundaries.tsv` contains the canonical transition and runtime phase epochs.
- `*-source-runtime.log` and `*-target-runtime.log` retain process events.
- `*-target-artifact.log` and `*-target-engine-cache.log` retain init evidence.
- `*-source-pod.json`, `*-target-pod.json`, `*-pre-transition-node.json`, and
  `*-power-mode.txt` retain observed material facts.
- `*-environment-normalized.json` is the normalized comparison of the
  non-experiment running-Pod set and resources.
- `*-target-rendered.yaml` retains each realized target manifest, while
  `*-target-spec-semantic.json` provides the sorted live Pod spec comparison.
- `prometheus-*.json` contains the lower-resolution diagnostic cross-checks.
- `paired-analysis.json` contains the derived trapezoidal integrals, coverage
  gates, phase attribution, deltas, pair-validity booleans, and the semantic
  correction mapping legacy `idleReferenced` names to `nodeStandby`.
- `SHA256SUMS` covers every retained file other than itself.

The four Prometheus JSON response bodies gained a terminal newline when
retained in Git; their parsed JSON values are unchanged. All direct samples,
phase boundaries, live-object snapshots, and logs are byte-identical to the
capture files.

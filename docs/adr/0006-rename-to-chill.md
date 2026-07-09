# 0006 Rename Project to CHILL

## Status

Accepted

## Context

The initial project name, GearEdge, described edge-oriented operating-point control but did not clearly expose the current research framing in `docs/spec/spec_v0.2_plain.md`: cost-aware heterogeneous inference under latency and load constraints, with energy minimization as the primary objective.

The repository is still in the early scaffold phase, so changing user-facing names, module paths, chart names, and development defaults is still cheaper than carrying a weaker name into later artifacts.

## Decision

Rename the project to `CHILL`, expanded as `Cost-aware Heterogeneous Inference for Latency and Load`.

Use `CHILL` for display text and `chill` for code/package identifiers, including the Go module path, Helm chart, default image repositories, Kubernetes namespace, kind cluster, and local kubeconfig.

Use `lab-paper-code/chill` as the GitHub repository slug and `/home/genesis1/ETRI/chill` as the local checkout path.

Keep the API group `edge.dacs.io/v1alpha1`. The API group represents the CRD domain and existing alpha resource surface, not the product name.

## Consequences

Local imports, Helm paths, README examples, default image repositories, and e2e namespaces move from `gearedge` to `chill`.

The local checkout and Git remote should move to the `chill` slug. The GitHub repository itself must be renamed before pushes to `git@github.com:lab-paper-code/chill.git` can succeed.

Existing local kind clusters and kubeconfig paths created under the old name are not reused automatically.

Any future project rename requires a new ADR because it will churn generated manifests, import paths, chart paths, and user-facing documentation.

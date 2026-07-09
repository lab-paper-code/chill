# 0003 Helm CRD Upgrade Strategy

## Status

Accepted

## Context

CHILL will be wrapped as a Helm chart. During `v1alpha1`, the CRD schemas are expected to change often. Helm's top-level `crds/` directory installs CRDs only on first install and does not manage normal upgrades, which is a poor fit for an alpha research repo.

## Decision

Keep `v1alpha1` CRDs under `charts/chill-operator/templates/crds/` so the operator release manages alpha CRD updates.

Add `helm.sh/resource-policy: keep` to CRDs to reduce accidental deletion risk.

Keep `crds.enabled` available for install-only smoke tests and migration windows where existing CRDs are owned by another release.

Use `make helm-crd-check` before a full Helm install against a real cluster. Use `make helm-adopt-crds` only when an operator has confirmed that the previous Helm release should no longer own the CRDs.

Move CRDs to the chart-level `crds/` directory only when the API is promoted toward `v1beta1` and schema churn is no longer routine.

## Consequences

Alpha installs are easier to iterate and test through Helm.

The chart must be reviewed carefully because templated CRDs can be deleted by Helm if resource policy or lifecycle handling is wrong.

CRD ownership is an explicit operational step during release renames. The chart should not silently take over CRDs from another Helm release.

# 0003 Helm CRD Upgrade Strategy

## Status

Accepted

## Context

GearEdge will be wrapped as a Helm chart. During `v1alpha1`, the CRD schemas are expected to change often. Helm's top-level `crds/` directory installs CRDs only on first install and does not manage normal upgrades, which is a poor fit for an alpha research repo.

## Decision

When the chart is introduced, keep `v1alpha1` CRDs under `charts/gearedge/templates/crds/` so Helm manages alpha CRD updates.

Add `helm.sh/resource-policy: keep` to CRDs to reduce accidental deletion risk.

Move CRDs to the chart-level `crds/` directory only when the API is promoted toward `v1beta1` and schema churn is no longer routine.

## Consequences

Alpha installs are easier to iterate and test through Helm.

The chart must be reviewed carefully because templated CRDs can be deleted by Helm if resource policy or lifecycle handling is wrong.

# 0001 Go and Kubebuilder Scaffold

## Status

Accepted

## Context

GearEdge is a Kubernetes-native scheduler/operator project. The repo needs CRD type generation, controller-runtime integration, envtest support, and a path to Helm packaging without adding OLM as a required layer.

The testbed API server is Kubernetes `v1.31.13`.

## Decision

Use Go with Kubebuilder. Scaffold with Kubebuilder `v4.2.0`, which generates the tested Kubernetes `1.31` dependency set:

- Go module: `github.com/lab-paper-code/gearedge`
- Go directive: `1.22.12`
- Kubernetes libraries: `k8s.io/* v0.31.0`
- controller-runtime: `v0.19.0`
- envtest assets: `1.31.0`

Use Kubebuilder's generated `config/` kustomize tree for development, but treat Helm as the eventual deployment interface.

## Consequences

The first implementation follows the Kubernetes `1.31` API surface. CI starts with this single supported minor version; a minimum/latest matrix is deferred until MVP-2.

Changing the module path or the Kubernetes minor version after code grows will require import-path or generated-manifest churn, so both require a new ADR.

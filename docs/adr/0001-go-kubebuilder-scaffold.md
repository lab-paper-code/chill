# Go and Kubebuilder Scaffold

## Status

Accepted

## Decision

Use Go and Kubebuilder for Kubernetes API generation, controllers and development manifests. Helm is the supported deployment interface; OLM is not required.

The repository pins:

- Go module: `github.com/lab-paper-code/chill`
- Go directive: `1.22.12`
- Kubernetes libraries: `k8s.io/* v0.31.0`
- controller-runtime: `v0.19.0`
- envtest assets: `1.31.0`

Use the generated `config/` kustomize tree for development and `charts/chill` for deployment.

## Consequences

Module-path and Kubernetes-minor changes affect imports, generated manifests and verification assets. Review those boundaries together when changing them.


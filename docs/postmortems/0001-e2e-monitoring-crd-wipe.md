# 0001 e2e Monitoring CRD Wipe

- What happened: running `go test ./...` executed the scaffolded e2e suite against the active testbed kubeconfig.
- Impact: the scaffolded teardown deleted Prometheus Operator and cert-manager bundles; monitoring CRDs had to be restored.
- Recovery: reapplied kube-prometheus-stack `79.6.1` CRDs with server-side apply, then ran `helm upgrade monitoring ... --reuse-values` and verified Prometheus/Alertmanager availability.
- Root cause: `go test ./...` bypassed the Makefile split between `make test` and `make test-e2e`.
- Prevention: e2e now requires a `kind-*` context, third-party install/uninstall helpers were removed, and development kubeconfig defaults to repo-local kind state.

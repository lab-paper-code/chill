# Test Layout

CHILL separates tests by execution boundary rather than placing every test in
one central directory.

| Location | Responsibility |
|---|---|
| package `*_test.go` files | Unit and package-level tests that belong to one Go package; these stay next to the implementation |
| package `testdata/` directories | Fixtures owned by one package and ignored as packages by the Go toolchain |
| `integration/` | Cross-package or Kubernetes API tests that run against envtest without requiring a live cluster |
| `e2e/` and package files with the `e2e` build tag | End-to-end tests that require the guarded kind test cluster; `internal/serving/kube/kind_e2e_test.go` runs the scheduler controller against kind for `SCH-AC-24` to `SCH-AC-26` |
| `utils/` | Shared support code for tests under this tree |

`make test` runs package and integration tests while excluding `e2e/`.
`make test-e2e` runs the live-cluster suite and the `e2e`-tagged package tests separately.
`hack/kind-up.sh` pins the kind Node image to the repository's Kubernetes 1.31
baseline; `CHILL_KIND_NODE_IMAGE` is the explicit override for a deliberate
compatibility run.

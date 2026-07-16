# Test Layout

CHILL separates tests by execution boundary rather than placing every test in
one central directory.

| Location | Responsibility |
|---|---|
| package `*_test.go` files | Unit and package-level tests that belong to one Go package; these stay next to the implementation |
| package `testdata/` directories | Fixtures owned by one package and ignored as packages by the Go toolchain |
| `integration/` | Cross-package or Kubernetes API tests that run against envtest without requiring a live cluster |
| `e2e/` | End-to-end tests that require the guarded kind test cluster |
| `utils/` | Shared support code for tests under this tree |

`make test` runs package and integration tests while excluding `e2e/`.
`make test-e2e` runs the live-cluster suite separately.

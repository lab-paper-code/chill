# Final whole-branch review fixes

Starting point: clean `main` at
`be4e1d02ac5db8a3e78558b00f5194b06acc10fc`.

Design authority read before implementation:
`docs/design/profiling/plans/08-batch-profile-artifacts.md`.
Task ledger read before implementation:
`.superpowers/sdd/2026-08-10-batch-profile-artifacts/progress.md`.

## Findings and changes

### Critical 1 — batch value used as a slice index

`projectedBSatPrecision` now searches `points` for the entry whose `Batch`
equals the candidate. The resolved index selects the reference point, and the
contrast loop starts at the following point. If the candidate is absent, the
function returns `candidatePointNotFoundError`; a focused `errors.As` test
proves the typed error.

The regression fixture freezes eligible batches `[1,2,4,5,6]`, deliberately
sparse below candidate batch 4. Its means select batch 4, whose projected
zero-variance lower bound is `0.94`. The old code silently selected batch 5 and
returned `0.9300000000000002`.

Before the production fix:

```text
$ go test ./internal/profilederivation -run '^TestBSatConfirmatorySizingResolvesSparseCandidatePosition$' -count=1
--- FAIL: TestBSatConfirmatorySizingResolvesSparseCandidatePosition (0.00s)
    sizing_test.go:133: projected reference lower bound = 0.9300000000000002, want batch 4 lower bound 0.94
FAIL
FAIL	github.com/lab-paper-code/chill/internal/profilederivation	0.002s
FAIL
```

After the production fix:

```text
$ go test ./internal/profilederivation -run '^TestBSatConfirmatorySizingResolvesSparseCandidatePosition$' -count=1
ok  	github.com/lab-paper-code/chill/internal/profilederivation	0.002s
```

### Important 2 — half-removed Makefile variable

Replaced the unused `RKNN_FIXED_BATCH ?=` declaration with
`RKNN_BATCH_PROFILES ?=`, matching the input consumed by
`rknn-convert-artifact`.

The required case-insensitive sweep was:

```text
$ rg -n -i 'fixedbatch|fixed_batch' api internal runtime build config charts Makefile
internal/profilingrun/types.go:14:	SaturatedSerialFixedBatchWorkloadMode = "saturated-serial-fixed-batch"
internal/profilingrun/intent.go:148:		(i.Measurement.WorkloadMode == SaturatedSerialFixedBatchWorkloadMode ||
internal/profiler/execution_policy_validation.go:53:	if p.WorkloadMode != profilingrun.SaturatedSerialFixedBatchWorkloadMode &&
internal/profiler/controller/execution_policy_resolver_test.go:105:	active := controllerExecutionPolicySpec(profilingrun.SaturatedSerialFixedBatchWorkloadMode)
internal/profiler/execution_policy_codec_test.go:148:	activeSpec := testExecutionPolicySpec(profilingrun.SaturatedSerialFixedBatchWorkloadMode)
```

All remaining matches name the saturated serial fixed-batch workload mode.
There is no remaining artifact `FixedBatch`/`FIXED_BATCH` field or Makefile
input in the swept paths.

### Important 3 — set exhaustion reported a continue outcome

When the curve says continue but `nextEligibleBatch` reports exhaustion, the
sequential method now authors `Stopped/CapabilityBoundary` and copies the
curve's `Detail`. The test curve returns the plausible continue state with an
empty outcome and `Detail: "curve detail"`; assertions prove both the
method-authored outcome and detail preservation.

### Important 4 — run-budget arithmetic counted batches by KMax

Both discovery-run terms now use
`len(curve.family.contract.record.Shared.EligibleBatches)`:

- remaining-budget calculation before confirmatory trial allocation; and
- `ProjectedTotalActiveRuns` in the frozen sizing result.

A regression with `[1,2,4,5,6]`, `KMax=6`, three discovery trials, candidate 4,
and budget 24 proves three confirmatory trials fit and the projected total is
24. `validateBSatPolicy`'s policy-level `KMax * DiscoveryTrials` floor was left
unchanged. The union-bound loops still enumerate the integer superset
`1..KMax-1`; only their feasible-trial budget input now uses the actual
discovery-set cardinality.

### Minor 5 — ADR named the wrong freeze

ADR 0016 now says family freeze, not intent freeze, canonicalizes eligible
batches into strictly increasing order.

### Minor 6 — stale envelope description

Plan 01 now describes live candidate-report `v1alpha8` and its batch-profile
contract. The separate historical readability statement is unchanged.

## Verification

Touched packages:

```text
$ go test ./internal/profilederivation ./internal/batchsaturation -count=1
ok  	github.com/lab-paper-code/chill/internal/profilederivation	1.521s
ok  	github.com/lab-paper-code/chill/internal/batchsaturation	0.002s
```

Pinned semantic diagnostics:

```text
$ ./hack/gopls-chill.sh check internal/profilederivation/sizing.go internal/profilederivation/sizing_test.go internal/batchsaturation/sequential.go internal/batchsaturation/sequential_test.go
```

Exit 0 with no output.

Lint:

```text
$ make lint
/home/genesis1/ETRI/chill/bin/golangci-lint run
```

Full test gate:

```text
$ make test
/home/genesis1/ETRI/chill/bin/controller-gen rbac:roleName=operator-role crd paths="./..." output:crd:artifacts:config=config/crd/bases
make helm-sync-crds
make[1]: Entering directory '/home/genesis1/ETRI/chill'
./hack/sync-helm-crds.sh
make[1]: Leaving directory '/home/genesis1/ETRI/chill'
make helm-sync-rbac
make[1]: Entering directory '/home/genesis1/ETRI/chill'
./hack/sync-helm-rbac.sh
make[1]: Leaving directory '/home/genesis1/ETRI/chill'
/home/genesis1/ETRI/chill/bin/controller-gen object paths="./..."
go fmt ./...
go vet ./...
python3 -m unittest discover -s runtime/ortcpu -p 'test_*.py'
....2026-07-16T00:01:31.300000Z INFO  [result      ] completed model-loaded idle measurement | elapsedSeconds=30.0
.2026-07-16T00:00:00.000000Z INFO  [warmup      ] starting frozen warm-up | iterations=2 inputShape=[1,3,224,224]
2026-07-16T00:00:01.400000Z INFO  [result      ] completed frozen CPU ORT measurement | inferences=1 elapsedSeconds=1.1
............
----------------------------------------------------------------------
Ran 17 tests in 0.026s

OK
python3 -m unittest discover -s runtime/rknnlite -p 'test_*.py'
...2026-08-11T04:23:50.179821Z INFO  [warmup      ] starting frozen RKNNLite warm-up | iterations=2 inputShape=[1,224,224,3]
2026-08-11T04:23:50.180468Z INFO  [result      ] completed frozen RKNNLite measurement | inferences=1 elapsedSeconds=1.0
.2026-08-11T04:23:50.183469Z INFO  [result      ] completed RKNNLite model-loaded idle measurement | elapsedSeconds=30.0
...........
----------------------------------------------------------------------
Ran 15 tests in 0.011s

OK
python3 -m unittest discover -s build/model-artifacts/rknn -p 'test_*.py'
....
----------------------------------------------------------------------
Ran 4 tests in 0.000s

OK
KUBEBUILDER_ASSETS="/home/genesis1/ETRI/chill/bin/k8s/1.31.0-linux-amd64" go test $(go list ./... | grep -v /e2e) -coverprofile cover.out
	github.com/lab-paper-code/chill/api/v1alpha1		coverage: 0.0% of statements
	github.com/lab-paper-code/chill/cmd		coverage: 0.0% of statements
ok  	github.com/lab-paper-code/chill/cmd/chill	0.033s	coverage: 67.2% of statements
ok  	github.com/lab-paper-code/chill/cmd/console	0.022s	coverage: 8.8% of statements
ok  	github.com/lab-paper-code/chill/cmd/measurement-worker	0.007s	coverage: 69.5% of statements
	github.com/lab-paper-code/chill/internal/component		coverage: 0.0% of statements
	github.com/lab-paper-code/chill/internal/metadata		coverage: 0.0% of statements
ok  	github.com/lab-paper-code/chill/cmd/metric-observer	2.115s	coverage: 64.1% of statements
ok  	github.com/lab-paper-code/chill/cmd/node-actuator	0.025s	coverage: 30.2% of statements
ok  	github.com/lab-paper-code/chill/cmd/node-discovery	0.016s	coverage: 39.3% of statements
ok  	github.com/lab-paper-code/chill/cmd/profiler	0.024s	coverage: 25.9% of statements
ok  	github.com/lab-paper-code/chill/cmd/workload-observer	0.065s	coverage: continuous
ok  	github.com/lab-paper-code/chill/internal/batchsaturation	0.004s	coverage: 87.3% of statements
ok  	github.com/lab-paper-code/chill/internal/batchsaturation/controller	0.007s	coverage: 61.4% of statements
ok  	github.com/lab-paper-code/chill/internal/deviceclass	0.014s	coverage: 73.0% of statements
ok  	github.com/lab-paper-code/chill/internal/evidencestore	0.003s	coverage: 85.3% of statements
ok  	github.com/lab-paper-code/chill/internal/evidencestore/kube	0.067s	coverage: 88.1% of statements
ok  	github.com/lab-paper-code/chill/internal/evidencestore/s3	0.009s	coverage: 85.8% of statements
ok  	github.com/lab-paper-code/chill/internal/kubeconfig	0.006s	coverage: 50.0% of statements
ok  	github.com/lab-paper-code/chill/internal/metricobserver	0.004s	coverage: 77.6% of statements
ok  	github.com/lab-paper-code/chill/internal/metricobserver/edgemetrics	0.045s	coverage: 90.5% of statements
ok  	github.com/lab-paper-code/chill/internal/modelcompat	0.017s	coverage: 98.1% of statements
ok  	github.com/lab-paper-code/chill/internal/nodeactuator	0.020s	coverage: 66.1% of statements
ok  	github.com/lab-paper-code/chill/internal/nodeactuator/controller	0.087s	coverage: 74.9% of statements
ok  	github.com/lab-paper-code/chill/internal/nodeactuator/jetson	0.016s	coverage: 83.8% of statements
ok  	github.com/lab-paper-code/chill/internal/nodeprobe	0.022s	coverage: 89.2% of statements
	github.com/lab-paper-code/chill/internal/operator/ownership		coverage: 0.0% of statements
	github.com/lab-paper-code/chill/internal/operator/watch		coverage: 0.0% of statements
	github.com/lab-paper-code/chill/test/utils		coverage: 0.0% of statements
ok  	github.com/lab-paper-code/chill/internal/operator/discovery	7.073s	coverage: 67.4% of statements
ok  	github.com/lab-paper-code/chill/internal/operator/evidencestore	0.117s	coverage: 75.9% of statements
ok  	github.com/lab-paper-code/chill/internal/operator/nodeactuator	0.073s	coverage: 49.0% of statements
ok  	github.com/lab-paper-code/chill/internal/operator/nodediscovery	0.032s	coverage: 30.4% of statements
ok  	github.com/lab-paper-code/chill/internal/operator/profiler	0.071s	coverage: 72.5% of statements
ok  	github.com/lab-paper-code/chill/internal/operator/system	0.041s	coverage: 67.1% of statements
ok  	github.com/lab-paper-code/chill/internal/operatorconsole	0.024s	coverage: 91.4% of statements
ok  	github.com/lab-paper-code/chill/internal/profilederivation	1.639s	coverage: 85.8% of statements
ok  	github.com/lab-paper-code/chill/internal/profiler	0.027s	coverage: 78.7% of statements
ok  	github.com/lab-paper-code/chill/internal/profiler/admission	0.009s	coverage: 72.7% of statements
ok  	github.com/lab-paper-code/chill/internal/profiler/controller	0.186s	coverage: 73.2% of statements
ok  	github.com/lab-paper-code/chill/internal/profiler/evidence	0.018s	coverage: 76.0% of statements
ok  	github.com/lab-paper-code/chill/internal/profiler/evidence/kube	0.037s	coverage: 73.7% of statements
ok  	github.com/lab-paper-code/chill/internal/profiler/worker	0.333s	coverage: 73.8% of statements
ok  	github.com/lab-paper-code/chill/internal/profilingrun	0.014s	coverage: 68.7% of statements
ok  	github.com/lab-paper-code/chill/internal/workloadobserver	0.003s	coverage: 75.5% of statements
ok  	github.com/lab-paper-code/chill/internal/workloadobserver/kube	1.015s	coverage: 82.6% of statements
ok  	github.com/lab-paper-code/chill/test/integration/operator	6.168s	coverage: [no statements]
ok  	github.com/lab-paper-code/chill/test/integration/profiler	29.714s	coverage: [no statements]
```

Architecture gate:

```text
$ make arch-check
./hack/architecture-coverage.sh
architecture.production_packages=45
architecture.mapped_packages=45
architecture.components=27
architecture.component.api=1
architecture.component.batch-saturation-core=1
architecture.component.batch-saturation-controller=1
architecture.component.entrypoints=9
architecture.component.platform-core=4
architecture.component.device-class=1
architecture.component.operator-console=1
architecture.component.operator=8
architecture.component.evidence-core=1
architecture.component.evidence-kube=1
architecture.component.evidence-s3=1
architecture.component.metric-core=1
architecture.component.metric-edgemetrics=1
architecture.component.workload-core=1
architecture.component.workload-kube=1
architecture.component.model-compat=1
architecture.component.node-actuator-core=1
architecture.component.node-actuator-controller=1
architecture.component.node-actuator-jetson=1
architecture.component.profile-derivation=1
architecture.component.profiling-run=1
architecture.component.profiler-app=1
architecture.component.profiler-controller=1
architecture.component.profiler-admission=1
architecture.component.profiler-evidence=1
architecture.component.profiler-evidence-kube=1
architecture.component.profiler-worker=1
architecture.test_files_enforced=false
architecture.production_package_test_only_internal_edges=7
architecture.excluded_test_packages=4
architecture.excluded_test_internal_edges=15
/home/genesis1/ETRI/chill/bin/go-arch-lint check --project-path /home/genesis1/ETRI/chill --output-color=false
module: github.com/lab-paper-code/chill
linters:
   On | Base: component imports # always on
  Off | Advanced: vendor imports # switch 'allow.depOnAnyVendor = false' (or delete) to on
  Off | Advanced: method calls and dependency injections # switch 'allow.deepScan = true' (or delete) to on

OK - No warnings found
CHILL_GO_ARCH_LINT=/home/genesis1/ETRI/chill/bin/go-arch-lint ./hack/architecture-negative-test.sh
architecture.negative_fixture=pass
architecture.expected_rejection=metric-core->api
```

No generated CRD or Helm drift remained after `make test`. No live-cluster
validation was run; this change is pure derivation/progress logic, Makefile
declaration cleanup, and documentation, and no live-cluster action was
requested.

# Immutable CPU ORT Profiling Run Spike

This isolated path materializes one Plan 1 `Compatible` candidate into one
digest-pinned Kubernetes Job and collects one immutable raw evidence bundle.

```sh
go run ./spikes/profilingrun/cmd/materialize \
  -intent spikes/profilingrun/fixtures/lattepanda-ort-cpu-bs1.json \
  -candidate-report spikes/profilingrun/fixtures/candidate-report.json | kubectl apply -f -

kubectl -n chill-profiling-run wait --for=condition=complete \
  job/cpu-ort-526040beceec --timeout=5m

go run ./spikes/profilingrun/cmd/collect \
  -intent spikes/profilingrun/fixtures/lattepanda-ort-cpu-bs1.json \
  -candidate-report spikes/profilingrun/fixtures/candidate-report.json \
  -output-dir spikes/profilingrun/observations/raw
```

The collector validates the exact runtime and PowerObserver image identities,
actual Node and Pod identities, artifact bytes, runtime/backend, CPU contract,
selected batch, output shape, and raw latency distribution. A KubeEdge/containerd
init-container `imageID` may expose cached tag identity; the bundle preserves it,
while artifact authority comes from the digest-pinned Pod spec and runtime-side
SHA-256 byte check.

The resulting bundle is valid Run evidence, not an admitted energy profile.
Power samples are observer receipt timestamps, co-resident workload is retained,
and no energy or `b_sat` is derived here.

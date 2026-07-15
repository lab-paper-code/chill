# CHILL Evidence Store Spike

This spike remains the isolated deployment and regression harness for the
promoted `internal/evidencestore` contract and its S3-compatible adapter.

The resources run in `chill-system`, use a dedicated credential Secret and PVC,
and do not reuse Kubeflow MinIO or another system's data lifecycle. The selected
server is only a replaceable S3-compatible test engine. The checked-in client
image is pinned to one amd64/arm64 manifest list.

## Current boundary

- The manifests model resources that the CHILL Operator may reconcile later.
- They are applied manually during the spike; the current Operator does not own
  them yet.
- The Profiler does not deploy or reconcile the store.
- The writer and verifier Jobs consume the internal contract through the small
  `evidence-store-client` binary; they do not reimplement immutable storage in
  shell.
- No credential value is stored in Git.
- The first Service is `ClusterIP`. Edge reachability is a separate observation,
  not an assumption embedded in the deployment.
- The base operand manifests do not embed a writer, verifier, bucket
  initializer, retention policy, or garbage collector.

## Create credentials

Create the Secret locally before applying the manifests. Use independently
generated values and do not record them in shell history or repository files.

```sh
kubectl -n chill-system create secret generic \
  chill-evidence-store-spike-credentials \
  --from-literal=access-key='<generated access key>' \
  --from-literal=secret-key='<generated secret key>'
```

## Render and apply

Rendering is safe and does not change the cluster:

```sh
kubectl kustomize spikes/evidence-store/manifests
kubectl apply -k spikes/evidence-store/manifests
```

## ClusterIP reachability probe

`scripts/run-clusterip-probe.sh` resolves the current Service ClusterIP through
the caller's Kubernetes access and creates
`tests/clusterip-reachability-job.yaml` on the Lattepanda edge Node. The edge
Pod itself has no Kubernetes API token. It calls the unauthenticated health
endpoint by both ClusterIP and Service DNS, receives no S3 credentials, and
writes no object.

## Authenticated evidence round trip

The round-trip fixtures keep three responsibilities separate:

- `bucket-setup-job.yaml` idempotently ensures the spike bucket through the
  same internal client contract on a cloud Node;
- `evidence-put-job.yaml` runs on Lattepanda and performs one immutable create;
- `evidence-verify-job.yaml` independently downloads and verifies exact bytes
  on a cloud Node.

The writer and verifier image is pinned to:

`155.230.35.213:5000/chill/evidence-store-client@sha256:5df44fc881a8e3e155aaa316c5714f483ef029593a0ed2730c021374135bdd8d`

The input is the existing 389,364-byte profiling evidence bundle with digest
`sha256:165d50e316de2b3d7da0728df1245aaf56f8f5a407668db1c24a756b6ba3fe42`.
`prepare-evidence-fixture.sh` exposes that repository fixture to the writer as a
ConfigMap; it is test input, not a proposed evidence persistence mechanism.

The writer handles a conditional-create conflict by downloading the existing
object. Exact bytes produce `existing-exact-match`; different bytes remain an
immutable conflict. `evidence-conflict-job.yaml` verifies the latter without
changing the retained object.

# CHILL Evidence Store Spike

This spike validates the smallest CHILL-managed evidence-store operand before
any storage code or API is promoted into `internal/`.

The resources run in `chill-system`, use a dedicated credential Secret and PVC,
and do not reuse Kubeflow MinIO or another system's data lifecycle. The selected
server is only a replaceable S3-compatible test engine. The checked-in image is
pinned to the exact amd64 manifest currently used by this spike.

## Current boundary

- The manifests model resources that the CHILL Operator may reconcile later.
- They are applied manually during the spike; the current Operator does not own
  them yet.
- The Profiler does not deploy or reconcile the store.
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

- `bucket-setup-job.yaml` prepares the spike bucket on a cloud Node;
- `evidence-put-job.yaml` runs on Lattepanda and performs one immutable create;
- `evidence-verify-job.yaml` independently downloads and verifies exact bytes
  on a cloud Node.

The input is the existing 389,364-byte profiling evidence bundle with digest
`sha256:165d50e316de2b3d7da0728df1245aaf56f8f5a407668db1c24a756b6ba3fe42`.
`prepare-evidence-fixture.sh` exposes that repository fixture to the writer as a
ConfigMap; it is test input, not a proposed evidence persistence mechanism.

The writer handles a conditional-create conflict by downloading the existing
object. Exact bytes produce `existing-exact-match`; different bytes remain an
immutable conflict. `evidence-conflict-job.yaml` verifies the latter without
changing the retained object.

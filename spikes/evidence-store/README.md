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
- No writer, verifier, bucket initialization, retention, or garbage collection
  is included in this first step.

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

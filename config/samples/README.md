# CHILL samples

`kustomization.yaml` contains catalog resources only. The profiling request
samples below are deliberately excluded because creating them starts Jobs and,
for mutable power modes, may create a `NodeActuation`:

- `edge_v1alpha1_batchsaturation.yaml`
- `edge_v1alpha1_modelloadedidlebaseline.yaml`
- `edge_v1alpha1_powermodetransitioncost.yaml`

`edge_v1alpha1_modelspec.yaml` is the complete registration record for the
already-published model artifacts used by these examples. Each artifact owns
its immutable OCI image, carrier architecture, and in-container path. To
register another model, replace those source fields together with the model's
format and input contract; no model-specific Helm execution declaration is
required.

Before applying one, replace the `example-*` identities and namespace with
resources visible to the Profiler. `BatchSaturation`,
`ModelLoadedIdleBaseline`, and `PowerModeTransitionCost` are normal user entry
points. Create one independent Complete `ModelLoadedIdleBaseline` for each
exact state selected by a `PowerModeTransitionCost`; the transition request
resolves those results and the installation actuation policies automatically.

Apply experiment requests individually:

```sh
kubectl apply -f config/samples/edge_v1alpha1_batchsaturation.yaml
kubectl apply -f config/samples/edge_v1alpha1_modelloadedidlebaseline.yaml
kubectl apply -f config/samples/edge_v1alpha1_powermodetransitioncost.yaml
```

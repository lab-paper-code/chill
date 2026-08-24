# CHILL samples

## Catalog

`kustomization.yaml` registers only the catalog objects:

- `DeviceClass`: where a workload can run.
- `ModelSpec`: what model artifacts exist and how CHILL can execute them.

```sh
kubectl apply -k config/samples
```

## ModelSpec structure

```text
ModelSpec
├── artifacts[]       immutable model files
│   ├── source        OCI image, architecture, and path
│   ├── input         sample shape without the batch axis
│   └── derivation    conversion facts for a derived artifact
└── executionPaths[]  artifact-to-runtime bindings
```

Use immutable image digests. Input data type and layout are runtime-owned, not
registration fields. A canonical artifact needs no `derivation`; a converted
artifact records its source, target platform, supported batches, and converter.

## Profiling requests

These samples are not part of `kustomization.yaml` because applying one starts
an experiment and may actuate a device:

| Kind | Purpose |
|---|---|
| `BatchSaturation` | Measure a batch-size curve. |
| `ModelLoadedIdleBaseline` | Measure model-loaded idle power for one state. |
| `PowerModeTransitionCost` | Measure directed cost between two power modes. |

Replace the `example-*` names with catalog and cluster resources visible to the
Profiler. Run each request separately:

```sh
kubectl apply -f config/samples/edge_v1alpha1_batchsaturation.yaml
kubectl apply -f config/samples/edge_v1alpha1_modelloadedidlebaseline.yaml
kubectl apply -f config/samples/edge_v1alpha1_powermodetransitioncost.yaml
```

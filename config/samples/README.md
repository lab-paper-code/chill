# CHILL samples

```text
config/samples
├── catalog/    normal model registration
├── manual/     optional manual DeviceClass registration
└── profiling/  experiment requests; apply one at a time
```

## Registration

`DeviceClass` is normally created automatically from Node facts and the Helm
device catalog. `manual/edge_v1alpha1_deviceclass.yaml` is only for an
intentionally manual installation.

Before editing a `ModelSpec`, follow the
[model artifact preparation guide](catalog/README.md) to produce its immutable
OCI reference and collect the input, architecture, and runtime facts.

The default Kustomize path registers only the model catalog:

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

The `profiling/` samples are excluded from Kustomize because applying one starts
an experiment and may actuate a device:

| Kind | Purpose |
|---|---|
| `BatchEnergyOptimization` | Find the measured batch with minimum incremental energy per item. |
| `ModelLoadedIdleBaseline` | Measure model-loaded idle power for one state. |
| `PowerModeTransitionCost` | Measure directed cost between two power modes. |

Replace the `example-*` names with catalog and cluster resources visible to the
Profiler. Run each request separately:

```sh
kubectl apply -f config/samples/profiling/edge_v1alpha1_batchenergyoptimization.yaml
kubectl apply -f config/samples/profiling/edge_v1alpha1_modelloadedidlebaseline.yaml
kubectl apply -f config/samples/profiling/edge_v1alpha1_powermodetransitioncost.yaml
```

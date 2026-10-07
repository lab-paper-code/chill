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

The Kustomize path is a structural model-catalog input, not a management writer.
Convert `metadata.name` and `spec` from the sample to JSON and submit them to
`POST /api/v1/models` over verified HTTPS as shown in the
[registration guide](catalog/README.md#validate-and-register).
Do not give manager credentials direct ModelSpec or Workload write access.

## ModelSpec structure

```text
ModelSpec
├── artifacts[]       immutable model files
│   ├── source        OCI image, architecture, and path
│   ├── input.tensors  name, Triton datatype, and sampleShape without the batch axis
│   └── derivation    conversion facts for a derived artifact
└── executionPaths[]  artifact-to-runtime bindings
```

Use immutable image digests and declare every input's exact name, uppercase
Triton datatype and sample shape. Multiple named numeric/bool tensors are
supported; layout is not a separate registration field. A canonical artifact
needs no `derivation`; a converted artifact records its source, target platform,
supported batches, and converter. Shape-only compatibility readers are absent.

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

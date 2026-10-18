# CHILL samples

```text
config/samples
├── catalog/    normal model registration
├── manual/     optional manual DeviceClass registration and Workload samples
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
│   ├── source        OCI image, architecture, and model files
│   ├── input.tensors  RKNN only: name, uppercase datatype, and sampleShape without the batch axis
│   └── derivation    conversion facts for a derived artifact
└── executionPaths[]  artifact-to-runtime bindings
```

Use immutable image digests and list every model file the runtime loads.
An RKNN artifact declares its input's exact name, uppercase datatype and sample
shape; a Preprocessor artifact defines the raw inputs of every other format. A
canonical artifact needs no `derivation`; a converted artifact records its
source, target platform, supported batches, and converter.

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

## Firstfloor runtime declaration

[`firstfloor/ortcpu-arm64-runtime-declaration.json`](firstfloor/ortcpu-arm64-runtime-declaration.json)
retains exact-image inspection output from the native ARM64 ORT CPU image on
`rasp5`. The same image's persistent worker/runner also completed real fixed-K
inference. It declares supplied runtime facts, not a measured execution profile.

Use its JSON producer output as `runtime.payload` in a
`profiling.executionDeclarations` entry for a matching ARM64 DeviceClass. Merge
the entry into the existing declaration list; do not replace unrelated entries.
The fact-source envelope and Helm renderer use
`chill.dacs.io/profiler-execution-facts.v1alpha8`. An older envelope is rejected
by the current resolver, even when its contained runtime declarations are valid.

Adding this entry does not add a ModelSpec execution path or start a measurement.
After a direct ConfigMap update, carry the merged list into the reviewed Helm
values before a later upgrade; Helm does not retain such an update automatically.

## Serving scheduler configuration

`serving-scheduler.json` is a controls-off deployment configuration with positive
bounded evaluation settings and no invented physical inventory or measurements.
Create a ConfigMap from it in the serving namespace:

```sh
kubectl -n "$SERVING_NAMESPACE" create configmap serving-scheduler \
  --from-file=scheduler.json=config/samples/serving-scheduler.json
```

Set Helm `servingEntrypoint.scheduler.configMap=serving-scheduler`; the default
key is `scheduler.json`. Populate inventories, reservations, measured profile
preparations, evidence-cost references and exact meter identities before
enabling any controls. Mirror configured power measurement namespace/name pairs
in `servingEntrypoint.scheduler.powerCostSources` for named read permissions.
No Profiler experiment is started by this configuration.

`settings.algorithm` is required: `fixed-horizon` retains the named comparison
baseline; `projected-work-history` enables the directed interval-kernel history
choice, not the physical controls. Update the ConfigMap/settings payload before
deploying this contract; omitted/other values are rejected. Keep controls OFF
until the independent actual-input/SLO/energy gates pass. The algorithm is exposed
in decision settings; projected decisions additionally expose `policy` cursor,
finite work, kernel sources, pending reasons and actual safety-response versus
projected-policy selection. Baseline decisions have no projected diagnostics.

Keep profile selection in the scheduler ConfigMap JSON's `profileCampaigns` array
as `{namespace,name}` entries; this is the only selection authority, not a Helm
campaign list. The ChillServer API's `POST /api/v1/scheduler/profiles` adds selections.
Helm `servingEntrypoint.profileNamespaces` limits read-only `get` access to
BatchEnergyOptimization and ProfilingRun resources in allowed namespaces; an
empty list defaults to the serving namespace. It grants no measurement writes.

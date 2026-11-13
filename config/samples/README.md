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

Convert `metadata.name` and `spec` from a sample to JSON and submit them to
`POST /api/v1/models` over verified HTTPS as shown in the
[registration guide](catalog/README.md#validate-and-register).
Manager accounts use the management API's path/verb permissions.

## Environment-specific inputs

Checked-in samples use illustrative registry references. Select complete
operational documents from the external JSON named by `CHILL_EXPERIMENT_CONFIG`
under `samples["<repository-relative sample path>"]`. Populate each document you
intend to submit with actual immutable images and hardware facts. A missing
entry is an error.

The environment may also hold exact-image inspection output under
`samples["config/samples/firstfloor/ortcpu-arm64-runtime-declaration.json"]`.
That string identifies an external JSON entry; no checked-in file supplies it.

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

Apply profiling samples individually: creating a request starts an experiment
and may actuate a device.

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

For discrete NVIDIA clock modes, the structural manual class sample is
`manual/edge_v1alpha1_deviceclass_nvidia.yaml`; do not apply a manual class
alongside a Helm-owned class of the same name. The CampaignOps-only pair-cost
sample is `profiling/edge_v1alpha1_powermodetransitioncost_nvidia.yaml`. Select
the actual environment documents. Do not apply these example files:

```sh
set -e
Sample=config/samples/manual/edge_v1alpha1_deviceclass_nvidia.yaml
Document=$(jq -e --arg path "$Sample" \
  '.samples[$path] // error("environment sample missing: " + $path)' \
  "${CHILL_EXPERIMENT_CONFIG:?external environment JSON required}")
printf '%s\n' "$Document" | kubectl apply -f -
# Separately, only after the measurement prerequisites below are satisfied:
Sample=config/samples/profiling/edge_v1alpha1_powermodetransitioncost_nvidia.yaml
Document=$(jq -e --arg path "$Sample" \
  '.samples[$path] // error("environment sample missing: " + $path)' \
  "$CHILL_EXPERIMENT_CONFIG")
printf '%s\n' "$Document" | kubectl apply -f -
```

The NVIDIA transition request requires an admitted GPU_1500 CUDA profile,
exact-state idle baselines for both modes, and the separate NVIDIA node-agent
operand. Verify these prerequisites against current resources before submission.
Use a fresh request name and serving Service for a new campaign.

## Firstfloor runtime declaration

Supply complete exact-image ARM64 ORT CPU inspection output in the external
environment entry below. Keep its image, architecture, packages and verification
claim together. Each different image needs its own inspection.

```sh
jq -e --arg path config/samples/firstfloor/ortcpu-arm64-runtime-declaration.json \
  '.samples[$path] // error("environment sample missing: " + $path)' \
  "${CHILL_EXPERIMENT_CONFIG:?external environment JSON required}"
```

Use this complete JSON producer output as `runtime.payload` in a
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
key is `scheduler.json`. Populate inventories, reservations and exact meter
identities before enabling any controls.
No Profiler experiment is started by this configuration.

`settings.planSearchStrategy` selects `v1` or `v2`; the sample selects `v2`.
Keep controls disabled until the installation's measurement and safety
prerequisites are satisfied.

Profiles, operation costs, unloaded baselines and power mode transition costs come
from completed measurements in Helm `servingEntrypoint.profileNamespaces`; an empty
list defaults to the serving namespace. A measurement record or power mode
transition cost contributes only when its meter equals an entry of `meters`.

# CPU ONNX on LattePanda

## Result

Date: 2026-07-11

The isolated Job completed on Kubernetes Node `lattepanda` through the
`lattepanda-3-delta-8g` DeviceClass selector. A clean reproduction deleted the
namespace and then ran `build-push.sh`, `run.sh`, and `verify.sh` using only the
isolated workspace; it completed again without relying on the first Job.

```text
artifact digest check: PASS
model load: PASS
requested provider: CPUExecutionProvider
actual provider: CPUExecutionProvider
runtime: onnxruntime 1.23.2
batch size: 1
warm-up: PASS
inferences: 10
Job completion: 1/1
```

Observed model evidence:

```json
{
  "model": "mobilenet-v2-050",
  "artifactDigest": "sha256:8645e5d6511cf0f78fa4a451e3bd86b3ab6b39bb5f9216ba32d2d9aebc852ee2",
  "architecture": "x86_64",
  "runtime": "onnxruntime",
  "runtimeVersion": "1.23.2",
  "requestedProvider": "CPUExecutionProvider",
  "actualProvider": "CPUExecutionProvider",
  "batchSize": 1,
  "inputShape": [1, 3, 224, 224],
  "warmupCompleted": true,
  "inferenceCount": 10,
  "modelLoadMs": 40.062,
  "latencyMeanMs": 36.534,
  "latencyP95Ms": 53.071,
  "status": "Succeeded"
}
```

The timing values prove execution only. They are not admitted as a CHILL
`DeviceProfile`; this spike does not implement the EEP measurement contract.

## Problems exposed

### DeviceClass membership did not imply schedulability

The first Pod remained Pending even though its DeviceClass selector matched
LattePanda:

```text
0/10 nodes are available:
7 node(s) had untolerated taint {node-role.kubernetes.io/edge: }
```

Adding an explicit `node-role.kubernetes.io/edge:NoSchedule` toleration allowed
the Job to schedule. Taints and tolerations belong to the runtime workload and
Kubernetes eligibility layer, not artifact compatibility or `ModelSpec`.

### Architecture has canonical and raw vocabularies

Kubernetes and `DeviceClass.spec.architecture` use `amd64`; Python
`platform.machine()` reported `x86_64`. Compatibility must compare a canonical
Kubernetes architecture vocabulary. Raw runtime architecture remains useful
provenance but must not be compared directly without normalization.

### Three different digests exist

The successful Pod exposed three independent identities:

| Object | Digest |
|---|---|
| model artifact | `sha256:8645e5d6511cf0f78fa4a451e3bd86b3ab6b39bb5f9216ba32d2d9aebc852ee2` |
| artifact delivery image | `sha256:046d9c5b09bf8efe0bb27698badfe9d4afc81e1c8dec1a2fdff925dc0feda9d8` |
| runtime image, first run | `sha256:c3a6e8645868ea8703e04edf4e332778f4ab80891abd2a65c5ee39334e51d2b6` |
| runtime image, clean rebuild | `sha256:36cea0e2193113d8f41b0501885fe600368a086097d553aa6c2dfd2a4fcca3bb` |

The model digest identifies executable model content. The artifact image digest
identifies one delivery package. The runtime image digest identifies the
software environment. They cannot be represented truthfully by one `image`
field.

The clean rebuild also showed that reusing the mutable `cpu-v1` tag did not
preserve runtime-image identity: the resolved digest changed even though the
build inputs were intended to be equivalent. Reproducible workloads and
profiles must record the resolved image digest or pin the workload by digest;
an image tag is only a lookup reference.

### Job completion is not domain evidence

Kubernetes correctly reported scheduling, image pull, init completion, process
exit, and Job completion. It did not turn the structured model-load result into
a durable domain object. A future observer or profiler must own that evidence;
`ModelSpec.status` is not an appropriate global execution-history store.

### Runtime capability came from the runtime image

The runtime image exposed `AzureExecutionProvider` and
`CPUExecutionProvider`; the execution path required CPU and the runner verified
the exact selected provider. DeviceClass did not need installed runtime or
provider fields. This supports the ADR 0012 separation.

### The Job requires no Kubernetes API credentials

The workload only consumes images, an `emptyDir`, environment variables, and
local inference. Service-account token automount is disabled in the final
manifest. Runtime capability probing does not inherently require controller or
cluster credentials.

## Boundary conclusions

| Concern | Owner shown by the spike |
|---|---|
| stable Node class selector | `DeviceClass` |
| taint and current placement eligibility | Kubernetes workload and scheduler |
| model content identity | artifact digest |
| artifact transport | artifact delivery image and init container |
| runtime/provider/version | runtime image |
| load and inference result | runtime probe; future profile/workload observer |
| Job lifecycle | Kubernetes Job |

The CPU baseline validates the `artifacts` and `executionPaths` separation. It
also shows that artifact delivery and runtime-image binding are deployment
concerns that should not be forced into the first catalog-only `ModelSpec`
schema without an owning workload API.

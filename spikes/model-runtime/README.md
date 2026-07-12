# Kubernetes Model Runtime Spike

This workspace tests model delivery and runtime ownership before CHILL commits
to a `ModelSpec` CRD schema. It is isolated from the operator, controllers,
generated CRDs, Helm release, and `internal/` packages.

The first case runs the measured MobileNet-V2-050 ONNX artifact on the
`lattepanda-3-delta-8g` DeviceClass with ONNX Runtime
`CPUExecutionProvider`:

```text
artifact image init container
  -> copy model into emptyDir
  -> verify model SHA256
runtime image
  -> verify SHA256 again
  -> require CPUExecutionProvider
  -> load model, warm up, run ten inferences
  -> emit readable stage logs and one structured result
Kubernetes Job
  -> report completion and retain events/logs for inspection
```

The runtime logging contract separates two consumers:

- Operators get timestamped `LEVEL [stage] message` lines and a final result
  table. The stable stages are `artifact`, `runtime`, `load`, `warmup`, and
  `inference`.
- Automation gets exactly one compact line prefixed with `RESULT_JSON `. The
  verification script extracts this line instead of depending on human log
  layout or line position.

The init container reports artifact delivery separately, so delivery failures
are not confused with runtime compatibility or inference failures.

Build and push the two amd64 images:

```sh
./spikes/model-runtime/scripts/build-push.sh
```

Deploy and wait:

```sh
./spikes/model-runtime/scripts/run.sh
```

Verify evidence:

```sh
./spikes/model-runtime/scripts/verify.sh
```

Remove only the isolated spike namespace:

```sh
./spikes/model-runtime/scripts/cleanup.sh
```

The manifests intentionally do not use the current empty `ModelSpec` CRD.
The first run and the ownership conclusions are recorded in
[`observations/cpu-onnx-lattepanda.md`](observations/cpu-onnx-lattepanda.md).

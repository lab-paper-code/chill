#!/usr/bin/env bash
set -euo pipefail

namespace=chill-model-spike
job=mobilenet-v2-050-ort-cpu

kubectl -n "${namespace}" get job "${job}" -o wide
kubectl -n "${namespace}" get pods -l job-name="${job}" -o wide
printf '\n==> artifact delivery log\n'
kubectl -n "${namespace}" logs job/"${job}" -c artifact
printf '\n==> runtime log\n'
runtime_log="$(kubectl -n "${namespace}" logs job/"${job}" -c runtime)"
printf '%s\n' "${runtime_log}"
result="$(printf '%s\n' "${runtime_log}" | sed -n 's/^RESULT_JSON //p' | tail -n 1)"
if [[ -z "${result}" ]]; then
  echo "runtime log does not contain RESULT_JSON" >&2
  exit 1
fi
printf '\n==> verified result\n'
python3 -c 'import json,sys; value=json.loads(sys.argv[1]); assert value["status"] == "Succeeded"; assert value["actualProvider"] == "CPUExecutionProvider"; assert value["artifactDigest"] == "sha256:8645e5d6511cf0f78fa4a451e3bd86b3ab6b39bb5f9216ba32d2d9aebc852ee2"; assert value["batchSize"] == 1; assert value["inferenceCount"] == 10; print(json.dumps(value, indent=2, sort_keys=True))' "${result}"
printf '\n==> Kubernetes events\n'
kubectl -n "${namespace}" get events --sort-by=.lastTimestamp

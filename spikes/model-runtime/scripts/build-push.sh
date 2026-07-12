#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
eep_root="${EEP_ROOT:-${repo_root}/../eep-profiler}"
registry="${SPIKE_REGISTRY:-155.230.35.213:5000/chill}"
artifact_image="${registry}/spike-mobilenet-v2-050-artifact:cpu-v1"
runtime_image="${registry}/spike-ort-runtime:cpu-v1"

docker buildx build --platform linux/amd64 \
  --file "${repo_root}/spikes/model-runtime/images/artifact.Dockerfile" \
  --tag "${artifact_image}" \
  --output type=registry,registry.insecure=true \
  "${eep_root}"

docker buildx build --platform linux/amd64 \
  --file "${repo_root}/spikes/model-runtime/images/runtime.Dockerfile" \
  --tag "${runtime_image}" \
  --output type=registry,registry.insecure=true \
  "${repo_root}"

printf 'artifact image: %s\nruntime image: %s\n' "${artifact_image}" "${runtime_image}"

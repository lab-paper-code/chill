#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
kubectl apply -f "${repo_root}/spikes/model-runtime/manifests/namespace.yaml"
kubectl -n chill-model-spike delete job mobilenet-v2-050-ort-cpu --ignore-not-found --wait=true
kubectl apply -f "${repo_root}/spikes/model-runtime/manifests/cpu-onnx-job.yaml"
kubectl -n chill-model-spike wait --for=condition=complete job/mobilenet-v2-050-ort-cpu --timeout=3m

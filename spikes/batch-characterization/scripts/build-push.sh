#!/usr/bin/env bash
# TODO(internal): Move spike images to the repository image pipeline with
# immutable tags/digests, SBOM/provenance, and multi-architecture publication.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
registry="${SPIKE_REGISTRY:-155.230.35.213:5000/chill}"
runtime_image="${registry}/spike-batch-characterization:cpu-v1"
observer_image="${registry}/spike-power-observer:v0"
probe_runner="${RUNTIME_PROBE_RUNNER:-ctr-plain-http}"
declaration_dir="${repo_root}/spikes/batch-characterization/results/runtime-declarations"
declaration_helper="${repo_root}/spikes/batch-characterization/scripts/runtime_declaration.py"

metadata_file="$(mktemp)"
index_file="$(mktemp)"
probe_file="$(mktemp)"
probe_namespace=""
probe_image=""
probe_container=""

cleanup() {
  if [[ -n "${probe_namespace}" ]]; then
    if [[ -n "${probe_container}" ]]; then
      sudo -n ctr -n "${probe_namespace}" tasks kill --signal SIGKILL "${probe_container}" >/dev/null 2>&1 || true
      sudo -n ctr -n "${probe_namespace}" tasks remove --force "${probe_container}" >/dev/null 2>&1 || true
      sudo -n ctr -n "${probe_namespace}" containers remove "${probe_container}" >/dev/null 2>&1 || true
    fi
    sudo -n ctr -n "${probe_namespace}" images rm "${probe_image}" >/dev/null 2>&1 || true
    namespace_removed=false
    for _ in $(seq 1 60); do
      if sudo -n ctr namespaces remove "${probe_namespace}" >/dev/null 2>&1; then
        namespace_removed=true
        break
      fi
      sleep 0.25
    done
    if [[ "${namespace_removed}" != true ]]; then
      printf 'warning: containerd probe namespace remains: %s\n' "${probe_namespace}" >&2
    fi
  fi
  rm -f "${metadata_file}" "${index_file}" "${probe_file}"
}
trap cleanup EXIT

docker buildx build --platform linux/amd64 \
  --file "${repo_root}/spikes/batch-characterization/images/runtime.Dockerfile" \
  --tag "${runtime_image}" \
  --metadata-file "${metadata_file}" \
  --output type=registry,registry.insecure=true \
  "${repo_root}"

runtime_repository="${runtime_image%:*}"
index_digest="$(python3 "${declaration_helper}" metadata-digest --metadata "${metadata_file}")"
docker manifest inspect --insecure "${runtime_repository}@${index_digest}" > "${index_file}"
manifest_digest="$(python3 "${declaration_helper}" resolve-manifest --index "${index_file}")"
probe_image="${runtime_repository}@${manifest_digest}"

probe_code='import json, platform; import onnxruntime as ort; print(json.dumps({"machine": platform.machine(), "runtimeFamily": "onnxruntime", "runtimeVersion": ort.__version__, "availableProviders": ort.get_available_providers()}, sort_keys=True))'
case "${probe_runner}" in
  docker)
    docker run --rm --platform linux/amd64 \
      --entrypoint python3 "${probe_image}" -c "${probe_code}" > "${probe_file}"
    ;;
  ctr-plain-http)
    command -v ctr >/dev/null
    sudo -n true
    probe_namespace="chill-runtime-probe-$$"
    probe_container="chill-runtime-probe-$$"
    sudo -n ctr namespaces create "${probe_namespace}" >/dev/null
    sudo -n ctr -n "${probe_namespace}" images pull \
      --plain-http --platform linux/amd64 "${probe_image}" >/dev/null
    timeout 30s sudo -n ctr -n "${probe_namespace}" run --rm "${probe_image}" \
      "${probe_container}" python3 -c "${probe_code}" > "${probe_file}"
    ;;
  *)
    printf 'unsupported RUNTIME_PROBE_RUNNER: %s\n' "${probe_runner}" >&2
    exit 1
    ;;
esac

declaration_path="$(python3 "${declaration_helper}" emit \
  --repository "${runtime_repository}" \
  --manifest-digest "${manifest_digest}" \
  --probe "${probe_file}" \
  --output-dir "${declaration_dir}")"

docker buildx build --platform linux/amd64 \
  --file "${repo_root}/spikes/power-observer/images/observer.Dockerfile" \
  --tag "${observer_image}" \
  --output type=registry,registry.insecure=true \
  "${repo_root}"

printf 'runtime image tag: %s\n' "${runtime_image}"
printf 'runtime image index: %s@%s\n' "${runtime_repository}" "${index_digest}"
printf 'runtime image manifest: %s\n' "${probe_image}"
printf 'runtime declaration: %s\n' "${declaration_path}"
printf 'power observer image: %s\n' "${observer_image}"

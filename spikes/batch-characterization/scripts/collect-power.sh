#!/usr/bin/env bash
# TODO(internal): Remove local port-forwarding. The profiler controller should
# bind each immutable Run to a resolved PowerSource and retained observation.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
results="${1:-${repo_root}/spikes/batch-characterization/results/latest/results.jsonl}"
output="${2:-${repo_root}/spikes/batch-characterization/results/latest/power-results.jsonl}"
local_port="${PROMETHEUS_LOCAL_PORT:-19090}"
forward_log="$(mktemp)"

cleanup() {
  if [[ -n "${forward_pid:-}" ]]; then
    kill "${forward_pid}" 2>/dev/null || true
    wait "${forward_pid}" 2>/dev/null || true
  fi
  rm -f "${forward_log}"
}
trap cleanup EXIT

kubectl port-forward --namespace monitoring \
  service/monitoring-kube-prometheus-prometheus \
  "${local_port}:9090" >"${forward_log}" 2>&1 &
forward_pid=$!

for _ in {1..50}; do
  if curl --silent --fail "http://127.0.0.1:${local_port}/-/ready" >/dev/null; then
    break
  fi
  if ! kill -0 "${forward_pid}" 2>/dev/null; then
    cat "${forward_log}" >&2
    exit 1
  fi
  sleep 0.1
done

curl --silent --fail "http://127.0.0.1:${local_port}/-/ready" >/dev/null
"${repo_root}/spikes/batch-characterization/scripts/collect-power.py" \
  "${results}" --output "${output}" \
  --prometheus-url "http://127.0.0.1:${local_port}"

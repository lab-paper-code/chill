#!/usr/bin/env bash
# TODO(internal): Replace log scraping and completion-index ordering with
# structured Run status/evidence keyed by immutable Run identity.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
output_dir="${repo_root}/spikes/batch-characterization/results/latest"
mkdir -p "${output_dir}"
: > "${output_dir}/results.jsonl"
: > "${output_dir}/power-observations.jsonl"

mapfile -t pods < <(kubectl get pods --namespace chill-batch-characterization \
  --selector job-name=mobilenet-v2-050-ort-cpu-batch-sweep \
  -o jsonpath='{range .items[*]}{.metadata.annotations.batch\.kubernetes\.io/job-completion-index}{"\t"}{.metadata.name}{"\n"}{end}' \
  | sort -n | cut -f2)

if ((${#pods[@]} == 0)); then
  echo "no batch characterization Pods found" >&2
  exit 1
fi

for pod in "${pods[@]}"; do
  log_file="${output_dir}/${pod}.log"
  kubectl logs --namespace chill-batch-characterization "${pod}" --container runtime > "${log_file}"
  result="$(sed -n 's/^EXPERIMENT_RESULT_JSON //p' "${log_file}" | tail -n 1)"
  if [[ -z "${result}" ]]; then
    echo "${pod} has no EXPERIMENT_RESULT_JSON record" >&2
    exit 1
  fi
  printf '%s\n' "${result}" >> "${output_dir}/results.jsonl"
  observer_log="${output_dir}/${pod}-power-observer.log"
  kubectl logs --namespace chill-batch-characterization "${pod}" --container power-observer > "${observer_log}"
  power_result="$(sed -n 's/^POWER_OBSERVATION_JSON //p' "${observer_log}" | tail -n 1)"
  if [[ -z "${power_result}" ]]; then
    echo "${pod} has no POWER_OBSERVATION_JSON record" >&2
    exit 1
  fi
  printf '%s\n' "${power_result}" >> "${output_dir}/power-observations.jsonl"
done

printf 'collected %d runtime and power results in %s\n' "${#pods[@]}" "${output_dir}"

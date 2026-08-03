#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <metrics-endpoint> <output.tsv>" >&2
  exit 2
fi

metrics_endpoint=$1
output_path=$2

printf '%s\n' \
  $'request_start_epoch_ns\tsample_epoch_ns\trequest_end_epoch_ns\tobserved_at_utc\tshelly_wall_watts\tjetson_rail_watts\tthermal_observation_available\tthermal_throttle_active\ttemperature_celsius' \
  >"$output_path"

while true; do
  request_start_ns=$(date +%s%N)
  if ! payload=$(curl -fsS --max-time 2 "$metrics_endpoint"); then
    sleep 0.5
    continue
  fi
  request_end_ns=$(date +%s%N)
  sample_ns=$((request_start_ns + (request_end_ns - request_start_ns) / 2))
  observed_at=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
  read -r wall rail thermal_available throttle temperature < <(
    awk '
      /^shelly_power_total_watts\{/ { wall=$2 }
      /^jetson_power_vdd_in_watts\{/ { rail=$2 }
      /^device_thermal_throttle_observation_available\{/ { available=$2 }
      /^device_thermal_throttle_active\{/ { throttle=$2 }
      /^shelly_temperature_celsius\{/ { temperature=$2 }
      END { print wall, rail, available, throttle, temperature }
    ' <<<"$payload"
  )
  if [[ -n ${wall:-} && -n ${rail:-} && -n ${thermal_available:-} &&
        -n ${throttle:-} && -n ${temperature:-} ]]; then
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
      "$request_start_ns" "$sample_ns" "$request_end_ns" "$observed_at" \
      "$wall" "$rail" "$thermal_available" "$throttle" "$temperature" \
      >>"$output_path"
  fi
  sleep 0.5
done

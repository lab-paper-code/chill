#!/bin/sh

set -eu

repo_root="$(CDPATH= cd -- "$(dirname "$0")/../../.." && pwd)"
fixture="$repo_root/spikes/profilingrun/observations/raw/sha256-165d50e316de2b3d7da0728df1245aaf56f8f5a407668db1c24a756b6ba3fe42.json"

kubectl create configmap chill-evidence-store-spike-fixture \
  -n chill-system \
  --from-file=evidence.json="$fixture" \
  --dry-run=client -o yaml | kubectl apply --server-side \
    --field-manager=evidence-store-spike -f -

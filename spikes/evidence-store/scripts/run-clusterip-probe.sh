#!/bin/sh

set -eu

namespace=chill-system
service=chill-evidence-store-spike
manifest="$(dirname "$0")/../tests/clusterip-reachability-job.yaml"

cluster_ip="$(
  kubectl get service "$service" -n "$namespace" \
    -o jsonpath='{.spec.clusterIP}'
)"

if [ -z "$cluster_ip" ] || [ "$cluster_ip" = None ]; then
  echo "service $namespace/$service has no ClusterIP" >&2
  exit 1
fi

sed "s/__STORE_CLUSTER_IP__/$cluster_ip/" "$manifest" |
  kubectl create -f - -o name

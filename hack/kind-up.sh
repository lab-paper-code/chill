#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-chill}"
KUBECONFIG_PATH="${CHILL_KIND_KUBECONFIG:-${ROOT_DIR}/.kube/chill-kind.config}"
NODE_IMAGE="${CHILL_KIND_NODE_IMAGE:-kindest/node:v1.31.0@sha256:53df588e04085fd41ae12de0c3fe4c72f7013bba32a20e7325357a1ac94ba865}"

mkdir -p "$(dirname "${KUBECONFIG_PATH}")"

if kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  kind export kubeconfig --name "${CLUSTER_NAME}" --kubeconfig "${KUBECONFIG_PATH}"
else
  kind create cluster \
    --name "${CLUSTER_NAME}" \
    --image "${NODE_IMAGE}" \
    --kubeconfig "${KUBECONFIG_PATH}"
fi

kubectl --kubeconfig "${KUBECONFIG_PATH}" config use-context "kind-${CLUSTER_NAME}" >/dev/null

cat <<EOF
kind cluster is ready.

export KUBECONFIG=${KUBECONFIG_PATH}
kubectl config current-context
EOF

#!/usr/bin/env bash
# TODO(internal): Replace this hard-coded Lattepanda/kubectl orchestration with
# profiler reconciliation: select an eligible Node, resolve edge-metrics,
# create an owned Run Job, and surface failures through status conditions.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
manifests="${repo_root}/spikes/batch-characterization/manifests"

kubectl apply -f "${manifests}/namespace.yaml"
target_node="lattepanda"
batch_sizes="${BATCH_SIZES:-1,2,4,8,16}"
work_dir="$(mktemp -d)"
cleanup_work_dir() {
  rm -rf "${work_dir}"
}
trap cleanup_work_dir EXIT

kubectl get node "${target_node}" -o json \
  | "${repo_root}/spikes/batch-characterization/scripts/derive-execution-contract.py" - \
      --scheduling-request 100m \
  > "${work_dir}/execution-contract.json"
kubectl create configmap batch-execution-contract \
  --namespace chill-batch-characterization \
  --from-file="execution-contract.json=${work_dir}/execution-contract.json" \
  --dry-run=client -o yaml | kubectl apply -f -
source_endpoint="$(kubectl get pods --namespace monitoring \
  --selector 'app.kubernetes.io/name=exporter,app.kubernetes.io/instance=edge-metrics' \
  --field-selector "spec.nodeName=${target_node}" -o json | python3 -c '
import json,sys
pods=json.load(sys.stdin)["items"]
ready=[]
for pod in pods:
    conditions={item["type"]: item["status"] for item in pod.get("status",{}).get("conditions",[])}
    if (not pod["metadata"].get("deletionTimestamp")
            and pod.get("status",{}).get("phase") == "Running"
            and conditions.get("Ready") == "True"
            and pod.get("status",{}).get("podIP")):
        ready.append(pod)
if len(ready) != 1:
    raise SystemExit(f"expected one Ready edge-metrics exporter, found {len(ready)}")
print("http://{}:9102/metrics".format(ready[0]["status"]["podIP"]))
')"
kubectl create configmap power-observer-target \
  --namespace chill-batch-characterization \
  --from-literal="endpoint=${source_endpoint}" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl delete job mobilenet-v2-050-ort-cpu-batch-sweep \
  --namespace chill-batch-characterization --ignore-not-found --wait=true
"${repo_root}/spikes/batch-characterization/scripts/render-job.py" \
  "${manifests}/batch-sweep-job.yaml" "${work_dir}/execution-contract.json" \
  --batch-sizes "${batch_sizes}" \
  | kubectl apply -f -
kubectl wait --for=condition=complete job/mobilenet-v2-050-ort-cpu-batch-sweep \
  --namespace chill-batch-characterization --timeout=30m
kubectl get pods --namespace chill-batch-characterization \
  --selector app.kubernetes.io/name=chill-batch-characterization \
  -L batch.kubernetes.io/job-completion-index

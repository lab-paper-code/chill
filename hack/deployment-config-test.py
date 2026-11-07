#!/usr/bin/env python3
"""Run ChillServer against Helm renders and explicit, defaulted, or invalid settings."""
import argparse
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--binary", default="bin/chill-server")
parser.add_argument("--configmap", help="Selected current scheduler ConfigMap JSON outside the repository")
args = parser.parse_args()
root = Path(__file__).resolve().parents[1]
configmap = {
    "apiVersion": "v1", "kind": "ConfigMap",
    "metadata": {"name": "scheduler-check", "namespace": "config-check-runtime"},
    "data": {"scheduler.json": json.dumps({"settings": {
        "observationWindowNanos": 1000000000, "controlIntervalNanos": 1000000000,
        "comparisonPeriodNanos": 1000000000,
        "maxReplicas": 1, "maxInstances": 1, "completionMarginMS": 0,
        "defaultDispatchOccupancyNanos": 0,
        "defaultOperationCost": {
            "prepare": {"seconds": 852, "joules": 36371},
            "remove": {"seconds": 13, "joules": 1014},
            "power": {"seconds": 166, "joules": 2229},
        },
        "defaultIdlePower": {"nodeWatts": 54, "perInstanceWatts": 8},
        "capacityWindowNanos": 1000000000, "capacityWindows": 1,
        "capacityPassingWindows": 1, "capacityMinimumRequests": 1, "planSearchWorkers": 3,
        "planSearchStrategy": "v1", "planSearchEvaluations": 500000,
        "planExactFinalists": 16, "planPolishActive": 8,
        "planPostFilterFinalists": False, "planPricedStart": True,
    }})},
}
if args.configmap:
    configmap = json.loads(Path(args.configmap).read_text())
assert configmap["kind"] == "ConfigMap"
namespace = configmap["metadata"]["namespace"]
name = configmap["metadata"]["name"]
assert len(configmap["data"]) == 1, "Supply only the selected scheduler data key"
key = next(iter(configmap["data"]))
render = subprocess.check_output([
    os.environ.get("HELM", "helm"), "template", "config-check", str(root / "charts/chill"),
    "--namespace", "config-check-release", "--set", "servingEntrypoint.enabled=true",
    "--set-string", "servingEntrypoint.tls.secretName=config-check-tls",
    "--set-string", "evidenceStore.connection.bucket=config-check",
    "--set-string", "evidenceStore.connection.region=config-check",
    "--set-string", f"servingEntrypoint.namespace={namespace}",
    "--set-string", f"servingEntrypoint.scheduler.configMap={name}",
    "--set-string", f"servingEntrypoint.scheduler.key={key}",
    "--set-string", "nodeAgent.image.repository=chill/node-agent",
    "--set-string", "nodeAgent.image.tag=latest",
    "--set-string", "nodeAgent.image.pullPolicy=Always",
    "--set-string", "nodeAgent.excludeNodeNames[0]=bad-edge",
    "--set", "nodeAgent.affinity.nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].weight=100",
    "--set-string", "nodeAgent.affinity.nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].preference.matchExpressions[0].key=kubernetes.io/hostname",
    "--set-string", "nodeAgent.affinity.nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].preference.matchExpressions[0].operator=In",
    "--set-string", "nodeAgent.affinity.nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].preference.matchExpressions[0].values[0]=good-edge",
], cwd=root, text=True)
print("chart accepts node-agent image spelling and preferred affinity with exclusions")
# Reject CPU re-injection at the chart boundary, including explicit zero quantities.
for component in ("operator", "profiler", "query", "servingEntrypoint", "dashboard", "nodeAgent", "nodeAgent.actuation"):
    for kind in ("requests", "limits"):
        result = subprocess.run([
            os.environ.get("HELM", "helm"), "template", "cpu-check", str(root / "charts/chill"),
            "--set-string", f"{component}.resources.{kind}.cpu=0",
        ], cwd=root, capture_output=True, text=True)
        assert result.returncode != 0 and "CPU without reservations or limits" in result.stderr, result.stderr
print("chart rejects CPU requests/limits for all seven platform components")
binary = str(Path(args.binary).resolve())
with tempfile.TemporaryDirectory(prefix="chill-config-check-") as temporary:
    manifest = Path(temporary) / "render.yaml"
    selected = Path(temporary) / "scheduler.json"
    manifest.write_text(render)
    for case in ("explicit", "defaults", "missing-workers"):
        candidate = copy.deepcopy(configmap)
        configuration = json.loads(candidate["data"][key])
        if case == "defaults":
            for field in ("planSearchStrategy", "planSearchEvaluations", "planExactFinalists",
                          "planPolishActive", "planPostFilterFinalists", "planPricedStart"):
                configuration["settings"].pop(field, None)
        elif case == "missing-workers":
            del configuration["settings"]["planSearchWorkers"]
        candidate["data"][key] = json.dumps(configuration)
        selected.write_text(json.dumps(candidate))
        result = subprocess.run([
            binary, f"--check-deployment-config={manifest}",
            f"--scheduler-configmap-file={selected}",
        ], capture_output=True, text=True, timeout=30)
        if case == "missing-workers":
            assert result.returncode != 0 and "planSearchWorkers" in result.stderr, result.stderr
        else:
            assert result.returncode == 0, result.stderr
        print(f"actualbinary {case}: {'rejected' if case == 'missing-workers' else 'accepted'}")

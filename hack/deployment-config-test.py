#!/usr/bin/env python3
"""Run the actual ChillServer against a Helm render and valid/missing-worker settings."""
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
        "comparisonPeriodNanos": 1000000000, "maxObservationAgeNanos": 1000000000,
        "maxReplicas": 1, "maxInstances": 1, "maxCPUs": 2, "completionMarginMS": 0,
        "capacityWindowNanos": 1000000000, "capacityWindows": 1,
        "capacityPassingWindows": 1, "capacityMinimumRequests": 1, "planSearchWorkers": 3,
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
], cwd=root, text=True)
binary = str(Path(args.binary).resolve())
with tempfile.TemporaryDirectory(prefix="chill-config-check-") as temporary:
    manifest = Path(temporary) / "render.yaml"
    selected = Path(temporary) / "scheduler.json"
    manifest.write_text(render)
    for missing in (False, True):
        candidate = copy.deepcopy(configmap)
        if missing:
            configuration = json.loads(candidate["data"][key])
            del configuration["settings"]["planSearchWorkers"]
            candidate["data"][key] = json.dumps(configuration)
        selected.write_text(json.dumps(candidate))
        result = subprocess.run([
            binary, f"--check-deployment-config={manifest}",
            f"--scheduler-configmap-file={selected}",
        ], capture_output=True, text=True, timeout=30)
        if missing:
            assert result.returncode != 0 and "planSearchWorkers" in result.stderr, result.stderr
        else:
            assert result.returncode == 0, result.stderr
        print(f"actualbinary {'missing planSearchWorkers rejected' if missing else 'valid settings accepted'}")

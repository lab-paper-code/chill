#!/usr/bin/env python3
"""Run all supported MobileNet-V2-100 RKNN artifacts on Orange Pi."""

# TODO(internal): Remove this model/node-specific campaign driver. The profiler
# controller should enumerate compatible ModelArtifacts, schedule Runs, retain
# evidence, and request scientific acceptance independently of artifact type.

import json
import subprocess
import time
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[3]
MANIFEST = ROOT / "spikes/batch-characterization/manifests/rknn-smoke-job.yaml"
NAMESPACE = "chill-batch-characterization"
ARTIFACTS = {
    1: "43e68150fef2a9001f140f526cc7c178843502ab12028672e0d245a5df5df599",
    4: "fa12fea1e44c3cc1c1039a41567bed4cb3ea7303d82a0dba4dfa71c39d71b34e",
    16: "57c9cb5cc1b8d4bb516f26525077657577bf072bcc1db151e0bb794891338ec6",
}


def env(container, name, value):
    item = next(item for item in container["env"] if item["name"] == name)
    item["value"] = str(value)


def main():
    campaign = ROOT / "spikes/batch-characterization/results" / f"rknn-{time.strftime('%Y%m%dT%H%M%S')}"
    campaign.mkdir(parents=True)
    measurements = []
    for batch, digest in ARTIFACTS.items():
        job = yaml.safe_load(MANIFEST.read_text())
        name = f"mobilenet-v2-100-rknn-bs{batch}"
        job["metadata"]["name"] = name
        pod = job["spec"]["template"]["spec"]
        pod["initContainers"][0]["command"][-1] = f"cp /artifact/bs{batch}.rknn /model/model.rknn"
        runtime = next(container for container in pod["containers"] if container["name"] == "runtime")
        env(runtime, "BATCH_SIZE", batch)
        env(runtime, "ARTIFACT_DIGEST", "sha256:" + digest)
        env(runtime, "ARTIFACT_SHAPE_MODE", "static")
        env(runtime, "SUPPORTED_BATCHES", batch)
        env(runtime, "WARMUP_ITERATIONS", 20)
        env(runtime, "MEASUREMENT_DURATION_SECONDS", 30)
        observer = next(container for container in pod["containers"] if container["name"] == "power-observer")
        observer["args"] = [arg.replace("-duration=10s", "-duration=30s") for arg in observer["args"]]

        subprocess.run(["kubectl", "delete", "job", name, "-n", NAMESPACE,
                        "--ignore-not-found", "--wait=true"], check=True)
        subprocess.run(["kubectl", "apply", "-f", "-"], input=yaml.safe_dump(job),
                       text=True, check=True)
        subprocess.run(["kubectl", "wait", "--for=condition=complete", f"job/{name}",
                        "-n", NAMESPACE, "--timeout=5m"], check=True)
        pod_name = subprocess.check_output(
            ["kubectl", "get", "pod", "-n", NAMESPACE, "-l", f"job-name={name}",
             "-o", "jsonpath={.items[0].metadata.name}"], text=True)
        runtime_log = subprocess.check_output(
            ["kubectl", "logs", "-n", NAMESPACE, pod_name, "-c", "runtime"], text=True)
        power_log = subprocess.check_output(
            ["kubectl", "logs", "-n", NAMESPACE, pod_name, "-c", "power-observer"], text=True)
        runtime_prefix = "EXPERIMENT_RESULT_JSON "
        power_prefix = "POWER_OBSERVATION_JSON "
        runtime_result = json.loads([line[len(runtime_prefix):]
                                     for line in runtime_log.splitlines()
                                     if line.startswith(runtime_prefix)][-1])
        power_result = json.loads([line[len(power_prefix):]
                                   for line in power_log.splitlines()
                                   if line.startswith(power_prefix)][-1])
        watts = sum(sample["watts"] for sample in power_result["samples"]) / len(power_result["samples"])
        energy = watts * runtime_result["latencyMs"]["mean"] / 1000 / batch
        measurement = {"batchSize": batch, "meanWatts": watts,
                       "energyJPerRequest": energy, "runtime": runtime_result,
                       "power": power_result}
        measurements.append(measurement)
        print(f"batch={batch} throughput={runtime_result['throughputItemsPerSecond']:.3f} "
              f"meanW={watts:.3f} energy={energy:.6f} J/request", flush=True)

    (campaign / "measurements.json").write_text(json.dumps(measurements, indent=2, sort_keys=True) + "\n")
    best = min(measurements, key=lambda item: item["energyJPerRequest"])
    print(json.dumps({"supportedBatches": list(ARTIFACTS),
                      "candidateBatch": best["batchSize"],
                      "acceptedBSat": None,
                      "evidence": str(campaign)}, indent=2))


if __name__ == "__main__":
    main()

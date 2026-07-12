#!/usr/bin/env python3
"""Render one RKNN spike Job from an explicit artifact capability."""

# TODO(internal): Read capability from ModelArtifact status and the execution
# contract from the selected Node/DeviceClass. CLI values are spike inputs and
# must not become user-maintained duplicate sources of truth.

import argparse
from pathlib import Path

import yaml


def set_env(container, name, value):
    item = next(item for item in container["env"] if item["name"] == name)
    item["value"] = str(value)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--name", required=True)
    parser.add_argument("--artifact-image", required=True)
    parser.add_argument("--artifact-path", default="/artifact/model.rknn")
    parser.add_argument("--artifact-digest", required=True)
    parser.add_argument("--shape-mode", choices=("static", "enumerated-dynamic"), required=True)
    parser.add_argument("--supported-batches", required=True)
    parser.add_argument("--batch", type=int, required=True)
    args = parser.parse_args()
    supported = [int(value) for value in args.supported_batches.split(",")]
    if args.batch not in supported:
        raise SystemExit("selected batch is outside artifact capability")
    if args.shape_mode == "static" and supported != [args.batch]:
        raise SystemExit("static artifact must support exactly the selected batch")

    job = yaml.safe_load(args.manifest.read_text())
    job["metadata"]["name"] = args.name
    pod = job["spec"]["template"]["spec"]
    artifact = pod["initContainers"][0]
    artifact["image"] = args.artifact_image
    artifact["command"] = ["sh", "-ec", f"cp {args.artifact_path} /model/model.rknn"]
    runtime = next(container for container in pod["containers"] if container["name"] == "runtime")
    set_env(runtime, "BATCH_SIZE", args.batch)
    set_env(runtime, "ARTIFACT_DIGEST", args.artifact_digest)
    set_env(runtime, "ARTIFACT_SHAPE_MODE", args.shape_mode)
    set_env(runtime, "SUPPORTED_BATCHES", ",".join(map(str, supported)))
    print(yaml.safe_dump(job, sort_keys=False))


if __name__ == "__main__":
    main()

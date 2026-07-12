#!/usr/bin/env python3
"""Offline validator for the pre-CRD ModelSpec candidate."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
from pathlib import Path

import yaml


SPIKE_DIR = Path(__file__).resolve().parent
REPO_ROOT = SPIKE_DIR.parents[1]
DEFAULT_FIXTURE = SPIKE_DIR / "fixtures" / "mobilenet-v2-050.yaml"
DEFAULT_EEP_ROOT = REPO_ROOT.parent / "eep-profiler"
SHA256_RE = re.compile(r"^sha256:[0-9a-f]{64}$")


def load_yaml(path: Path) -> dict:
    with path.open() as stream:
        value = yaml.safe_load(stream)
    if not isinstance(value, dict):
        raise ValueError(f"{path}: expected one YAML object")
    return value


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return "sha256:" + digest.hexdigest()


def unique_index(items: list[dict], field: str, owner: str) -> dict[str, dict]:
    result: dict[str, dict] = {}
    for index, item in enumerate(items):
        name = item.get(field)
        if not isinstance(name, str) or not name:
            raise ValueError(f"{owner}[{index}].{field}: non-empty string required")
        if name in result:
            raise ValueError(f"{owner}: duplicate {field} {name!r}")
        result[name] = item
    return result


def artifact_file(model: str, artifact: dict, eep_root: Path) -> Path:
    if artifact["format"] == "onnx":
        return eep_root / "models" / f"{model}.onnx"
    if artifact["format"] == "rknn":
        build = artifact.get("build") or {}
        target = build.get("targetPlatform")
        batch = build.get("batchSize")
        if not target or not isinstance(batch, int) or batch < 1:
            raise ValueError(f"artifact {artifact['name']!r}: RKNN build target and batch required")
        return eep_root / "models" / "rknn" / f"{model}-{target}-bs{batch}.rknn"
    raise ValueError(f"artifact {artifact['name']!r}: unsupported format {artifact['format']!r}")


def validate(fixture: Path, eep_root: Path, chart_values: Path) -> list[str]:
    candidate = load_yaml(fixture)
    if candidate.get("apiVersion") != "edge.dacs.io/v1alpha1":
        raise ValueError("apiVersion must be edge.dacs.io/v1alpha1")
    if candidate.get("kind") != "ModelSpec":
        raise ValueError("kind must be ModelSpec")

    model = (candidate.get("metadata") or {}).get("name")
    if not isinstance(model, str) or not model:
        raise ValueError("metadata.name is required")
    spec = candidate.get("spec") or {}
    artifacts = unique_index(spec.get("artifacts") or [], "name", "spec.artifacts")
    paths = unique_index(spec.get("executionPaths") or [], "name", "spec.executionPaths")
    if not artifacts or not paths:
        raise ValueError("at least one artifact and execution path are required")

    summaries: list[str] = []
    for name, artifact in artifacts.items():
        digest = artifact.get("digest", "")
        if not SHA256_RE.fullmatch(digest):
            raise ValueError(f"artifact {name!r}: invalid sha256 digest")
        source = artifact_file(model, artifact, eep_root)
        if not source.is_file():
            raise ValueError(f"artifact {name!r}: evidence file missing: {source}")
        actual = sha256(source)
        if actual != digest:
            raise ValueError(f"artifact {name!r}: digest mismatch: {actual}")

        parent = ((artifact.get("derivedFrom") or {}).get("artifact"))
        if parent:
            if parent == name:
                raise ValueError(f"artifact {name!r}: cannot derive from itself")
            if parent not in artifacts:
                raise ValueError(f"artifact {name!r}: unknown parent {parent!r}")

        if artifact["format"] == "rknn":
            metadata_path = source.with_suffix(".json")
            metadata = json.loads(metadata_path.read_text())
            if "sha256:" + metadata["output_rknn_sha256"] != digest:
                raise ValueError(f"artifact {name!r}: RKNN metadata output digest mismatch")
            if parent and "sha256:" + metadata["source_onnx_sha256"] != artifacts[parent]["digest"]:
                raise ValueError(f"artifact {name!r}: RKNN source lineage mismatch")
        summaries.append(f"artifact {name}: {source.name} digest verified")

    values = load_yaml(chart_values)
    classes = values["discovery"]["catalog"]["classes"]
    catalog_pairs = {(item["architecture"], item["accelerator"]) for item in classes}
    devices = load_yaml(eep_root / "configs" / "devices.yaml")["devices"]
    observed_providers = {
        provider
        for device in devices.values()
        for provider in device.get("ep", [])
    }

    for name, path in paths.items():
        artifact_name = path.get("artifact")
        if artifact_name not in artifacts:
            raise ValueError(f"execution path {name!r}: unknown artifact {artifact_name!r}")
        runtime = path.get("runtime") or {}
        if not runtime.get("family") or not runtime.get("provider"):
            raise ValueError(f"execution path {name!r}: runtime family and provider required")
        if runtime["provider"] not in observed_providers:
            raise ValueError(f"execution path {name!r}: provider absent from EEP device inventory")

        requirements = path.get("requirements") or {}
        architectures = requirements.get("architectures") or []
        accelerators = requirements.get("accelerators") or []
        if not architectures or not accelerators:
            raise ValueError(f"execution path {name!r}: architecture and accelerator required")
        missing = {
            (architecture, accelerator)
            for architecture in architectures
            for accelerator in accelerators
            if (architecture, accelerator) not in catalog_pairs
        }
        if missing:
            raise ValueError(f"execution path {name!r}: requirements absent from DeviceClass catalog: {sorted(missing)}")

        batching = path.get("batching") or {}
        mode = batching.get("mode")
        if mode not in {"dynamic", "fixed"}:
            raise ValueError(f"execution path {name!r}: batching mode must be dynamic or fixed")
        if mode == "fixed":
            size = batching.get("size")
            if not isinstance(size, int) or size < 1:
                raise ValueError(f"execution path {name!r}: positive fixed batch size required")
            build_batch = (artifacts[artifact_name].get("build") or {}).get("batchSize")
            if build_batch is not None and build_batch != size:
                raise ValueError(f"execution path {name!r}: artifact and execution batch mismatch")
        elif "size" in batching:
            raise ValueError(f"execution path {name!r}: dynamic batching must not set size")
        summaries.append(f"execution path {name}: references and observed vocabulary verified")

    return summaries


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--fixture", type=Path, default=DEFAULT_FIXTURE)
    parser.add_argument("--eep-root", type=Path, default=DEFAULT_EEP_ROOT)
    parser.add_argument("--chart-values", type=Path, default=REPO_ROOT / "charts" / "chill" / "values.yaml")
    args = parser.parse_args()

    for summary in validate(args.fixture, args.eep_root, args.chart_values):
        print(summary)
    print("ModelSpec spike: PASS")


if __name__ == "__main__":
    main()

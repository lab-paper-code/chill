#!/usr/bin/env python3
"""Validate the isolated CPU ORT ModelSpec candidate and artifact identity."""

from __future__ import annotations

import argparse
import hashlib
import re
from pathlib import Path

import yaml


API_VERSION = "edge.dacs.io/v1alpha1"
KIND = "ModelSpec"
SHA256_RE = re.compile(r"^sha256:[0-9a-f]{64}$")


class UniqueKeyLoader(yaml.SafeLoader):
    """Safe YAML loader that rejects duplicate mapping keys."""


def construct_unique_mapping(loader: UniqueKeyLoader, node: yaml.MappingNode, deep: bool = False) -> dict:
    result: dict = {}
    for key_node, value_node in node.value:
        key = loader.construct_object(key_node, deep=deep)
        if key in result:
            raise yaml.constructor.ConstructorError(
                "while constructing a mapping",
                node.start_mark,
                f"duplicate key {key!r}",
                key_node.start_mark,
            )
        result[key] = loader.construct_object(value_node, deep=deep)
    return result


UniqueKeyLoader.add_constructor(
    yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG,
    construct_unique_mapping,
)


def require_keys(value: dict, required: set[str], allowed: set[str], owner: str) -> None:
    missing = required - value.keys()
    unknown = value.keys() - allowed
    if missing:
        raise ValueError(f"{owner}: missing fields: {sorted(missing)}")
    if unknown:
        raise ValueError(f"{owner}: unknown fields: {sorted(unknown)}")


def require_nonempty_string(value: object, owner: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{owner}: non-empty string required")
    return value


def load_mapping(path: Path) -> dict:
    try:
        value = yaml.load(path.read_text(), Loader=UniqueKeyLoader)
    except (OSError, yaml.YAMLError) as error:
        raise ValueError(f"read {path}: {error}") from error
    if not isinstance(value, dict):
        raise ValueError(f"{path}: expected one YAML mapping")
    return value


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    try:
        with path.open("rb") as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(chunk)
    except OSError as error:
        raise ValueError(f"read artifact {path}: {error}") from error
    return "sha256:" + digest.hexdigest()


def unique_items(items: object, owner: str) -> dict[str, dict]:
    if not isinstance(items, list) or not items:
        raise ValueError(f"{owner}: non-empty list required")
    result: dict[str, dict] = {}
    for index, item in enumerate(items):
        if not isinstance(item, dict):
            raise ValueError(f"{owner}[{index}]: mapping required")
        name = require_nonempty_string(item.get("name"), f"{owner}[{index}].name")
        if name in result:
            raise ValueError(f"{owner}: duplicate name {name!r}")
        result[name] = item
    return result


def validate_structure(fixture: Path) -> tuple[dict, dict[str, dict], dict[str, dict]]:
    candidate = load_mapping(fixture)
    require_keys(
        candidate,
        {"apiVersion", "kind", "metadata", "spec"},
        {"apiVersion", "kind", "metadata", "spec"},
        "ModelSpec",
    )
    if candidate["apiVersion"] != API_VERSION:
        raise ValueError(f"apiVersion: expected {API_VERSION!r}")
    if candidate["kind"] != KIND:
        raise ValueError(f"kind: expected {KIND!r}")

    metadata = candidate["metadata"]
    if not isinstance(metadata, dict):
        raise ValueError("metadata: mapping required")
    require_keys(metadata, {"name"}, {"name"}, "metadata")
    require_nonempty_string(metadata["name"], "metadata.name")

    spec = candidate["spec"]
    if not isinstance(spec, dict):
        raise ValueError("spec: mapping required")
    require_keys(spec, {"artifacts", "executionPaths"}, {"artifacts", "executionPaths"}, "spec")

    artifacts = unique_items(spec["artifacts"], "spec.artifacts")
    paths = unique_items(spec["executionPaths"], "spec.executionPaths")

    for index, artifact in enumerate(artifacts.values()):
        owner = f"spec.artifacts[{index}]"
        require_keys(artifact, {"name", "format", "digest"}, {"name", "format", "digest"}, owner)
        if artifact["format"] != "onnx":
            raise ValueError(f"{owner}.format: CPU path requires 'onnx'")
        digest = require_nonempty_string(artifact["digest"], f"{owner}.digest")
        if not SHA256_RE.fullmatch(digest):
            raise ValueError(f"{owner}.digest: canonical sha256 digest required")

    for index, path in enumerate(paths.values()):
        owner = f"spec.executionPaths[{index}]"
        require_keys(path, {"name", "artifact", "runtime"}, {"name", "artifact", "runtime"}, owner)
        artifact_reference = require_nonempty_string(path["artifact"], f"{owner}.artifact")
        if artifact_reference not in artifacts:
            raise ValueError(f"execution path {path['name']!r}: unknown artifact {artifact_reference!r}")
        runtime = path["runtime"]
        if not isinstance(runtime, dict):
            raise ValueError(f"execution path {path['name']!r}.runtime: mapping required")
        require_keys(runtime, {"family", "backend"}, {"family", "backend"}, f"execution path {path['name']!r}.runtime")
        require_nonempty_string(runtime["family"], f"execution path {path['name']!r}.runtime.family")
        require_nonempty_string(runtime["backend"], f"execution path {path['name']!r}.runtime.backend")

    return candidate, artifacts, paths


def select_cpu_path(
    candidate: dict,
    artifacts: dict[str, dict],
    paths: dict[str, dict],
    execution_path: str,
) -> tuple[str, str, str]:
    if execution_path not in paths:
        raise ValueError(f"execution path {execution_path!r} not found")
    path = paths[execution_path]
    path_name = path["name"]
    artifact_name = path["artifact"]
    artifact = artifacts[artifact_name]
    runtime = path["runtime"]
    if runtime["family"] != "onnxruntime":
        raise ValueError(f"execution path {path_name!r}: runtime family must be 'onnxruntime'")
    if runtime["backend"] != "CPUExecutionProvider":
        raise ValueError(f"execution path {path_name!r}: backend must be 'CPUExecutionProvider'")

    return candidate["metadata"]["name"], artifact_name, artifact["digest"]


def verify_artifact(artifact_file: Path, declared_digest: str) -> str:
    actual_digest = sha256(artifact_file)
    if actual_digest != declared_digest:
        raise ValueError(
            f"artifact digest mismatch: declared={declared_digest} actual={actual_digest}"
        )

    return actual_digest


def validate(
    fixture: Path,
    execution_path: str,
    artifact_file: Path | None = None,
) -> list[str]:
    candidate, artifacts, paths = validate_structure(fixture)
    model_name, artifact_name, declared_digest = select_cpu_path(
        candidate, artifacts, paths, execution_path
    )
    summaries = [
        f"model {model_name}: minimal structure verified",
        f"execution path {execution_path}: onnxruntime/CPUExecutionProvider verified",
    ]
    if artifact_file is not None:
        actual_digest = verify_artifact(artifact_file, declared_digest)
        summaries.append(f"artifact {artifact_name}: {actual_digest} verified")
    return summaries


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--fixture", type=Path, required=True)
    parser.add_argument("--execution-path", required=True)
    parser.add_argument("--artifact-file", type=Path)
    args = parser.parse_args()
    try:
        summaries = validate(args.fixture, args.execution_path, args.artifact_file)
    except ValueError as error:
        parser.exit(1, f"ModelSpec CPU fixture: FAIL: {error}\n")
    for summary in summaries:
        print(summary)
    print("ModelSpec CPU fixture: PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

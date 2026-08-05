#!/usr/bin/env python3
"""Inspect one exact CPU ORT image and emit its trusted declaration."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import tempfile
from pathlib import Path
from typing import Callable


SCHEMA_VERSION = "chill.dacs.io/runtime-declaration.v1alpha1"
VERIFICATION = "exact-image-runtime-inspection-v1"
CPU_BACKEND = "CPUExecutionProvider"
SHA256_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
INDEX_MEDIA_TYPES = {
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
}
MANIFEST_MEDIA_TYPES = {
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.docker.distribution.manifest.v2+json",
}
PROBE_CODE = (
    "import json, platform; import onnxruntime as ort; "
    "print(json.dumps({"
    '"machine": platform.machine(), '
    '"runtimeFamily": "onnxruntime", '
    '"runtimeVersion": ort.__version__, '
    '"availableProviders": ort.get_available_providers()'
    "}, sort_keys=True))"
)


def load_object(path: Path) -> dict:
    try:
        value = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError(f"read JSON {path}: {error}") from error
    if not isinstance(value, dict):
        raise ValueError(f"{path}: expected one JSON object")
    return value


def require_digest(value: object, owner: str) -> str:
    if not isinstance(value, str) or not SHA256_RE.fullmatch(value):
        raise ValueError(f"{owner}: canonical sha256 digest required")
    return value


def require_repository(value: str) -> str:
    repository = value.strip()
    if not repository or "@" in repository or any(character.isspace() for character in repository):
        raise ValueError("repository: unpinned repository name required")
    return repository


def require_architecture(value: str) -> str:
    if value not in {"amd64", "arm64"}:
        raise ValueError(f"architecture: unsupported value {value!r}")
    return value


def runnable_manifest(index: dict, os_name: str, architecture: str) -> str:
    if index.get("schemaVersion") != 2:
        raise ValueError("OCI index: schemaVersion 2 required")
    if index.get("mediaType") not in INDEX_MEDIA_TYPES:
        raise ValueError("OCI index: supported index mediaType required")
    manifests = index.get("manifests")
    if not isinstance(manifests, list):
        raise ValueError("OCI index: manifests list required")

    matches = []
    for descriptor in manifests:
        if not isinstance(descriptor, dict):
            raise ValueError("OCI index: manifest descriptor must be an object")
        platform = descriptor.get("platform")
        if not isinstance(platform, dict):
            continue
        annotations = descriptor.get("annotations") or {}
        if annotations.get("vnd.docker.reference.type") == "attestation-manifest":
            continue
        if descriptor.get("mediaType") not in MANIFEST_MEDIA_TYPES:
            continue
        if platform.get("os") == os_name and platform.get("architecture") == architecture:
            matches.append(require_digest(descriptor.get("digest"), "manifest.digest"))

    if len(matches) != 1:
        raise ValueError(
            f"OCI index: expected one runnable {os_name}/{architecture} manifest, "
            f"found {len(matches)}"
        )
    return matches[0]


def canonical_architecture(machine: object) -> str:
    if machine in {"x86_64", "amd64"}:
        return "amd64"
    if machine in {"aarch64", "arm64"}:
        return "arm64"
    raise ValueError(f"probe.machine: unsupported CPU architecture {machine!r}")


def inspect_exact_image(
    repository: str,
    manifest_digest: str,
    architecture: str,
    container_tool: str = "docker",
) -> dict:
    manifest_digest = require_digest(manifest_digest, "manifest digest")
    image = f"{repository}@{manifest_digest}"
    command = [
        container_tool,
        "run",
        "--rm",
        "--platform",
        f"linux/{architecture}",
        "--entrypoint",
        "python3",
        image,
        "-c",
        PROBE_CODE,
    ]
    try:
        result = subprocess.run(command, check=True, capture_output=True, text=True)
    except (OSError, subprocess.CalledProcessError) as error:
        detail = getattr(error, "stderr", "") or str(error)
        raise ValueError(f"inspect exact runtime image {image}: {detail.strip()}") from error
    try:
        probe = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise ValueError(f"inspect exact runtime image {image}: invalid probe JSON") from error
    if not isinstance(probe, dict):
        raise ValueError(f"inspect exact runtime image {image}: expected one JSON object")
    return probe


def declaration(
    repository: str,
    manifest_digest: str,
    expected_architecture: str,
    expected_runtime_version: str,
    probe: dict,
) -> dict:
    repository = require_repository(repository)
    manifest_digest = require_digest(manifest_digest, "manifest digest")
    expected_architecture = require_architecture(expected_architecture)
    if not expected_runtime_version or expected_runtime_version.strip() != expected_runtime_version:
        raise ValueError("expected runtime version is required without surrounding whitespace")

    architecture = canonical_architecture(probe.get("machine"))
    if architecture != expected_architecture:
        raise ValueError(
            f"probe.machine: expected {expected_architecture!r}, observed {architecture!r}"
        )
    if probe.get("runtimeFamily") != "onnxruntime":
        raise ValueError("probe.runtimeFamily: expected 'onnxruntime'")
    if probe.get("runtimeVersion") != expected_runtime_version:
        raise ValueError(
            f"probe.runtimeVersion: expected {expected_runtime_version!r}, "
            f"got {probe.get('runtimeVersion')!r}"
        )
    providers = probe.get("availableProviders")
    if not isinstance(providers, list) or not all(
        isinstance(provider, str) and provider for provider in providers
    ):
        raise ValueError("probe.availableProviders: non-empty string list required")
    if CPU_BACKEND not in providers:
        raise ValueError(f"probe.availableProviders: {CPU_BACKEND} is absent")

    return {
        "schemaVersion": SCHEMA_VERSION,
        "image": f"{repository}@{manifest_digest}",
        "architecture": architecture,
        "runtime": {
            "family": "onnxruntime",
            "backends": [CPU_BACKEND],
        },
        "verification": VERIFICATION,
    }


def atomic_json(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = json.dumps(value, indent=2, sort_keys=True) + "\n"
    if path.exists():
        try:
            existing = path.read_text()
        except OSError as error:
            raise ValueError(f"read existing output {path}: {error}") from error
        if existing != payload:
            raise ValueError(f"immutable output collision: {path}")
        return

    descriptor, temporary_name = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w") as stream:
            stream.write(payload)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    except BaseException:
        temporary.unlink(missing_ok=True)
        raise


def emit(output_dir: Path, manifest_digest: str, probe: dict, value: dict) -> Path:
    stem = require_digest(manifest_digest, "manifest digest").replace(":", "-")
    declaration_path = output_dir / f"{stem}.json"
    probe_path = output_dir / f"{stem}.probe.json"
    existing = (probe_path.exists(), declaration_path.exists())
    if existing[0] != existing[1]:
        raise ValueError(f"incomplete immutable output pair for {stem}")
    if existing == (True, True):
        atomic_json(probe_path, probe)
        atomic_json(declaration_path, value)
        return declaration_path
    atomic_json(probe_path, probe)
    try:
        atomic_json(declaration_path, value)
    except BaseException:
        probe_path.unlink(missing_ok=True)
        raise
    return declaration_path


def produce(
    repository: str,
    index: dict,
    architecture: str,
    expected_runtime_version: str,
    output_dir: Path,
    inspector: Callable[[str, str, str], dict],
) -> Path:
    repository = require_repository(repository)
    architecture = require_architecture(architecture)
    manifest_digest = runnable_manifest(index, "linux", architecture)
    probe = inspector(repository, manifest_digest, architecture)
    value = declaration(
        repository,
        manifest_digest,
        architecture,
        expected_runtime_version,
        probe,
    )
    return emit(output_dir, manifest_digest, probe, value)


def content_digest(path: Path) -> str:
    return "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repository", required=True)
    parser.add_argument("--index", type=Path, required=True)
    parser.add_argument("--architecture", choices=("amd64", "arm64"), required=True)
    parser.add_argument("--expected-runtime-version", required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--container-tool", default="docker")
    args = parser.parse_args()

    try:
        inspector = lambda repository, digest, architecture: inspect_exact_image(
            repository, digest, architecture, args.container_tool
        )
        output = produce(
            args.repository,
            load_object(args.index),
            args.architecture,
            args.expected_runtime_version,
            args.output_dir,
            inspector,
        )
    except ValueError as error:
        parser.exit(1, f"runtime declaration: FAIL: {error}\n")
    print(json.dumps({"contentDigest": content_digest(output), "path": str(output)}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

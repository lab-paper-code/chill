#!/usr/bin/env python3
"""Resolve and emit a declaration for one exact CPU ORT runtime image."""

from __future__ import annotations

import argparse
import json
import os
import re
import tempfile
from pathlib import Path


SHA256_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
EXPECTED_ORT_VERSION = "1.23.2"
CPU_BACKEND = "CPUExecutionProvider"
INDEX_MEDIA_TYPES = {
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
}


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


def pushed_index_digest(metadata: dict) -> str:
    return require_digest(metadata.get("containerimage.digest"), "containerimage.digest")


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
        media_type = descriptor.get("mediaType")
        if media_type not in {
            "application/vnd.oci.image.manifest.v1+json",
            "application/vnd.docker.distribution.manifest.v2+json",
        }:
            continue
        if platform.get("os") == os_name and platform.get("architecture") == architecture:
            matches.append(require_digest(descriptor.get("digest"), "manifest.digest"))
    if len(matches) != 1:
        raise ValueError(
            f"OCI index: expected one runnable {os_name}/{architecture} manifest, found {len(matches)}"
        )
    return matches[0]


def canonical_architecture(machine: object) -> str:
    if machine in {"x86_64", "amd64"}:
        return "amd64"
    raise ValueError(f"probe.machine: unsupported CPU architecture {machine!r}")


def declaration(repository: str, manifest_digest: str, probe: dict) -> dict:
    repository = repository.strip()
    if not repository or "@" in repository:
        raise ValueError("repository: unpinned repository name required")
    manifest_digest = require_digest(manifest_digest, "manifest digest")
    architecture = canonical_architecture(probe.get("machine"))
    if probe.get("runtimeFamily") != "onnxruntime":
        raise ValueError("probe.runtimeFamily: expected 'onnxruntime'")
    if probe.get("runtimeVersion") != EXPECTED_ORT_VERSION:
        raise ValueError(
            f"probe.runtimeVersion: expected {EXPECTED_ORT_VERSION!r}, got {probe.get('runtimeVersion')!r}"
        )
    providers = probe.get("availableProviders")
    if not isinstance(providers, list) or not all(
        isinstance(provider, str) and provider for provider in providers
    ):
        raise ValueError("probe.availableProviders: non-empty string list required")
    if CPU_BACKEND not in providers:
        raise ValueError(f"probe.availableProviders: {CPU_BACKEND} is absent")
    return {
        "schemaVersion": "spikes.chill.dacs.io/runtime-declaration.v1alpha1",
        "image": f"{repository}@{manifest_digest}",
        "architecture": architecture,
        "runtime": {
            "family": "onnxruntime",
            "backends": [CPU_BACKEND],
        },
        "verification": "exact-image-runtime-inspection-v1",
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


def emit(repository: str, manifest_digest: str, probe_file: Path, output_dir: Path) -> Path:
    probe = load_object(probe_file)
    value = declaration(repository, manifest_digest, probe)
    stem = manifest_digest.replace(":", "-")
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


def main() -> int:
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)

    metadata_command = commands.add_parser("metadata-digest")
    metadata_command.add_argument("--metadata", type=Path, required=True)

    resolve_command = commands.add_parser("resolve-manifest")
    resolve_command.add_argument("--index", type=Path, required=True)
    resolve_command.add_argument("--os", default="linux")
    resolve_command.add_argument("--architecture", default="amd64")

    emit_command = commands.add_parser("emit")
    emit_command.add_argument("--repository", required=True)
    emit_command.add_argument("--manifest-digest", required=True)
    emit_command.add_argument("--probe", type=Path, required=True)
    emit_command.add_argument("--output-dir", type=Path, required=True)

    args = parser.parse_args()
    try:
        if args.command == "metadata-digest":
            print(pushed_index_digest(load_object(args.metadata)))
        elif args.command == "resolve-manifest":
            print(runnable_manifest(load_object(args.index), args.os, args.architecture))
        else:
            print(emit(args.repository, args.manifest_digest, args.probe, args.output_dir))
    except ValueError as error:
        parser.exit(1, f"runtime declaration: FAIL: {error}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

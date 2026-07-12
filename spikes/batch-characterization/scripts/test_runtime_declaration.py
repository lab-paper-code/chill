#!/usr/bin/env python3

from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from runtime_declaration import (  # noqa: E402
    CPU_BACKEND,
    atomic_json,
    canonical_architecture,
    declaration,
    emit,
    pushed_index_digest,
    runnable_manifest,
)


DIGEST = "sha256:" + "a" * 64
OTHER_DIGEST = "sha256:" + "b" * 64


def index(*descriptors: dict) -> dict:
    return {
        "schemaVersion": 2,
        "mediaType": "application/vnd.oci.image.index.v1+json",
        "manifests": list(descriptors),
    }


def descriptor(digest: str, os_name: str, architecture: str) -> dict:
    return {
        "mediaType": "application/vnd.oci.image.manifest.v1+json",
        "digest": digest,
        "platform": {"os": os_name, "architecture": architecture},
    }


def attestation(digest: str) -> dict:
    value = descriptor(digest, "linux", "amd64")
    value["annotations"] = {"vnd.docker.reference.type": "attestation-manifest"}
    return value


def probe() -> dict:
    return {
        "machine": "x86_64",
        "runtimeFamily": "onnxruntime",
        "runtimeVersion": "1.23.2",
        "availableProviders": ["AzureExecutionProvider", CPU_BACKEND],
    }


class RuntimeDeclarationTest(unittest.TestCase):
    def test_reads_push_digest(self) -> None:
        self.assertEqual(pushed_index_digest({"containerimage.digest": DIGEST}), DIGEST)

    def test_selects_runnable_manifest_and_ignores_attestation(self) -> None:
        value = index(
            descriptor(DIGEST, "linux", "amd64"),
            attestation(OTHER_DIGEST),
        )
        self.assertEqual(runnable_manifest(value, "linux", "amd64"), DIGEST)

    def test_rejects_missing_or_ambiguous_platform(self) -> None:
        with self.assertRaisesRegex(ValueError, "found 0"):
            runnable_manifest(index(descriptor(DIGEST, "linux", "arm64")), "linux", "amd64")
        with self.assertRaisesRegex(ValueError, "found 2"):
            runnable_manifest(
                index(
                    descriptor(DIGEST, "linux", "amd64"),
                    descriptor(OTHER_DIGEST, "linux", "amd64"),
                ),
                "linux",
                "amd64",
            )

    def test_rejects_unsupported_top_document(self) -> None:
        for value in (
            {"schemaVersion": 1, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": []},
            {"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "manifests": []},
        ):
            with self.assertRaisesRegex(ValueError, "OCI index"):
                runnable_manifest(value, "linux", "amd64")

    def test_normalizes_only_supported_machine(self) -> None:
        self.assertEqual(canonical_architecture("x86_64"), "amd64")
        with self.assertRaisesRegex(ValueError, "unsupported"):
            canonical_architecture("aarch64")

    def test_builds_minimum_declaration(self) -> None:
        value = declaration("registry.example/chill/runtime", DIGEST, probe())
        self.assertEqual(value["image"], f"registry.example/chill/runtime@{DIGEST}")
        self.assertEqual(value["architecture"], "amd64")
        self.assertEqual(value["runtime"]["backends"], [CPU_BACKEND])
        self.assertNotIn("runtimeVersion", json.dumps(value))
        self.assertNotIn("availableProviders", json.dumps(value))

    def test_rejects_unexpected_runtime_or_missing_cpu_backend(self) -> None:
        for field, value in (
            ("runtimeFamily", "other"),
            ("runtimeVersion", "1.22.0"),
            ("availableProviders", ["AzureExecutionProvider"]),
        ):
            with self.subTest(field=field):
                actual = probe()
                actual[field] = value
                with self.assertRaises(ValueError):
                    declaration("registry.example/chill/runtime", DIGEST, actual)

    def test_emit_preserves_raw_probe_and_digest_named_declaration(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            probe_path = root / "probe.json"
            atomic_json(probe_path, probe())
            output = emit("registry.example/chill/runtime", DIGEST, probe_path, root / "out")
            self.assertEqual(output.name, "sha256-" + "a" * 64 + ".json")
            self.assertTrue(output.is_file())
            self.assertTrue((root / "out" / ("sha256-" + "a" * 64 + ".probe.json")).is_file())

    def test_atomic_output_rejects_changed_content(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "declaration.json"
            atomic_json(path, {"value": 1})
            atomic_json(path, {"value": 1})
            with self.assertRaisesRegex(ValueError, "immutable output collision"):
                atomic_json(path, {"value": 2})

    def test_emit_rejects_incomplete_output_pair(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            probe_path = root / "probe.json"
            atomic_json(probe_path, probe())
            output = root / "out"
            output.mkdir()
            (output / ("sha256-" + "a" * 64 + ".probe.json")).write_text("{}\n")
            with self.assertRaisesRegex(ValueError, "incomplete immutable output pair"):
                emit("registry.example/chill/runtime", DIGEST, probe_path, output)


if __name__ == "__main__":
    unittest.main()

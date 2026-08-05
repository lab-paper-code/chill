#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock


MODULE_PATH = Path(__file__).with_name("runtime_declaration.py")
SPEC = importlib.util.spec_from_file_location("runtime_declaration", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
runtime_declaration = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runtime_declaration)

DIGEST = "sha256:" + "a" * 64
OTHER_DIGEST = "sha256:" + "b" * 64


def descriptor(digest: str, architecture: str) -> dict:
    return {
        "mediaType": "application/vnd.oci.image.manifest.v1+json",
        "digest": digest,
        "platform": {"os": "linux", "architecture": architecture},
    }


def index(*descriptors: dict) -> dict:
    return {
        "schemaVersion": 2,
        "mediaType": "application/vnd.oci.image.index.v1+json",
        "manifests": list(descriptors),
    }


def probe(machine: str = "aarch64") -> dict:
    return {
        "machine": machine,
        "runtimeFamily": "onnxruntime",
        "runtimeVersion": "1.23.2",
        "availableProviders": ["AzureExecutionProvider", runtime_declaration.CPU_BACKEND],
    }


class RuntimeDeclarationTest(unittest.TestCase):
    def test_selects_one_runnable_manifest_and_ignores_attestation(self) -> None:
        attestation = descriptor(OTHER_DIGEST, "arm64")
        attestation["annotations"] = {
            "vnd.docker.reference.type": "attestation-manifest"
        }
        self.assertEqual(
            runtime_declaration.runnable_manifest(
                index(descriptor(DIGEST, "arm64"), attestation), "linux", "arm64"
            ),
            DIGEST,
        )

    def test_rejects_missing_and_ambiguous_manifest(self) -> None:
        with self.assertRaisesRegex(ValueError, "found 0"):
            runtime_declaration.runnable_manifest(
                index(descriptor(DIGEST, "amd64")), "linux", "arm64"
            )
        with self.assertRaisesRegex(ValueError, "found 2"):
            runtime_declaration.runnable_manifest(
                index(descriptor(DIGEST, "arm64"), descriptor(OTHER_DIGEST, "arm64")),
                "linux",
                "arm64",
            )

    def test_normalizes_supported_machine_vocabulary(self) -> None:
        for machine, expected in (
            ("x86_64", "amd64"),
            ("amd64", "amd64"),
            ("aarch64", "arm64"),
            ("arm64", "arm64"),
        ):
            self.assertEqual(runtime_declaration.canonical_architecture(machine), expected)
        with self.assertRaisesRegex(ValueError, "unsupported"):
            runtime_declaration.canonical_architecture("armv7l")

    def test_builds_current_consumer_schema_for_arm64(self) -> None:
        value = runtime_declaration.declaration(
            "registry.example/chill/measurement-worker",
            DIGEST,
            "arm64",
            "1.23.2",
            probe(),
        )
        self.assertEqual(value["schemaVersion"], runtime_declaration.SCHEMA_VERSION)
        self.assertEqual(value["architecture"], "arm64")
        self.assertEqual(value["runtime"]["backends"], [runtime_declaration.CPU_BACKEND])
        self.assertNotIn("runtimeVersion", json.dumps(value))
        self.assertNotIn("availableProviders", json.dumps(value))

    def test_rejects_architecture_runtime_and_backend_drift(self) -> None:
        cases = (
            ("machine", "x86_64"),
            ("runtimeFamily", "other"),
            ("runtimeVersion", "1.22.0"),
            ("availableProviders", ["AzureExecutionProvider"]),
        )
        for field, value in cases:
            with self.subTest(field=field):
                actual = probe()
                actual[field] = value
                with self.assertRaises(ValueError):
                    runtime_declaration.declaration(
                        "registry.example/chill/measurement-worker",
                        DIGEST,
                        "arm64",
                        "1.23.2",
                        actual,
                    )

    @mock.patch.object(runtime_declaration.subprocess, "run")
    def test_inspects_the_exact_child_image(self, run: mock.Mock) -> None:
        run.return_value = subprocess.CompletedProcess([], 0, json.dumps(probe()), "")
        actual = runtime_declaration.inspect_exact_image(
            "registry.example/chill/measurement-worker", DIGEST, "arm64"
        )
        self.assertEqual(actual, probe())
        command = run.call_args.args[0]
        self.assertIn(f"registry.example/chill/measurement-worker@{DIGEST}", command)
        self.assertIn("linux/arm64", command)
        self.assertEqual(run.call_args.kwargs["check"], True)

    def test_produce_preserves_raw_probe_and_immutable_declaration(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output_dir = Path(directory)
            inspector = mock.Mock(return_value=probe())
            output = runtime_declaration.produce(
                "registry.example/chill/measurement-worker",
                index(descriptor(DIGEST, "arm64")),
                "arm64",
                "1.23.2",
                output_dir,
                inspector,
            )
            self.assertEqual(output.name, "sha256-" + "a" * 64 + ".json")
            self.assertTrue(
                output.with_name("sha256-" + "a" * 64 + ".probe.json").is_file()
            )
            self.assertTrue(runtime_declaration.content_digest(output).startswith("sha256:"))
            runtime_declaration.produce(
                "registry.example/chill/measurement-worker",
                index(descriptor(DIGEST, "arm64")),
                "arm64",
                "1.23.2",
                output_dir,
                inspector,
            )

    def test_rejects_incomplete_or_changed_output_pair(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output_dir = Path(directory)
            stem = "sha256-" + "a" * 64
            (output_dir / f"{stem}.probe.json").write_text("{}\n")
            with self.assertRaisesRegex(ValueError, "incomplete immutable output pair"):
                runtime_declaration.emit(output_dir, DIGEST, probe(), {})

    def test_rejects_invalid_inputs_before_inspection(self) -> None:
        inspector = mock.Mock(return_value=probe())
        with tempfile.TemporaryDirectory() as directory:
            for repository, architecture in (
                ("registry.example/runtime@sha256:" + "c" * 64, "arm64"),
                ("registry.example/runtime", "armv7"),
            ):
                with self.subTest(repository=repository, architecture=architecture):
                    with self.assertRaises(ValueError):
                        runtime_declaration.produce(
                            repository,
                            index(descriptor(DIGEST, "arm64")),
                            architecture,
                            "1.23.2",
                            Path(directory),
                            inspector,
                        )
        inspector.assert_not_called()


if __name__ == "__main__":
    unittest.main()

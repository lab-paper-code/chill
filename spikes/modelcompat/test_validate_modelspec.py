#!/usr/bin/env python3

from __future__ import annotations

import hashlib
import tempfile
import unittest
from pathlib import Path

import yaml

from validate_modelspec import validate, validate_structure, verify_artifact


def candidate(digest: str) -> dict:
    return {
        "apiVersion": "edge.dacs.io/v1alpha1",
        "kind": "ModelSpec",
        "metadata": {"name": "mobilenet-v2-050"},
        "spec": {
            "artifacts": [
                {"name": "canonical-onnx", "format": "onnx", "digest": digest}
            ],
            "executionPaths": [
                {
                    "name": "ort-cpu",
                    "artifact": "canonical-onnx",
                    "runtime": {
                        "family": "onnxruntime",
                        "backend": "CPUExecutionProvider",
                    },
                }
            ],
        },
    }


class ValidateModelSpecTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.artifact = self.root / "model.onnx"
        self.artifact.write_bytes(b"model-bytes")
        self.digest = "sha256:" + hashlib.sha256(b"model-bytes").hexdigest()

    def write_fixture(self, value: dict) -> Path:
        path = self.root / "model.yaml"
        path.write_text(yaml.safe_dump(value, sort_keys=False))
        return path

    def test_accepts_minimum_cpu_ort_fixture_without_external_artifact(self) -> None:
        summaries = validate(self.write_fixture(candidate(self.digest)), "ort-cpu")
        self.assertEqual(len(summaries), 2)

    def test_optionally_verifies_selected_artifact_bytes(self) -> None:
        summaries = validate(
            self.write_fixture(candidate(self.digest)), "ort-cpu", self.artifact
        )
        self.assertEqual(len(summaries), 3)

    def test_accepts_multiple_unique_artifacts_and_paths(self) -> None:
        value = candidate(self.digest)
        value["spec"]["artifacts"].append(
            {"name": "other-onnx", "format": "onnx", "digest": self.digest}
        )
        value["spec"]["executionPaths"].append(
            {
                "name": "other-path",
                "artifact": "other-onnx",
                "runtime": {
                    "family": "onnxruntime",
                    "backend": "CPUExecutionProvider",
                },
            }
        )
        validate(self.write_fixture(value), "ort-cpu")

    def test_rejects_fields_from_future_or_run_owners(self) -> None:
        value = candidate(self.digest)
        value["spec"]["executionPaths"][0]["batching"] = {"mode": "dynamic"}
        with self.assertRaisesRegex(ValueError, "unknown fields.*batching"):
            validate_structure(self.write_fixture(value))

    def test_rejects_unknown_fields_at_each_boundary(self) -> None:
        mutations = (
            lambda value: value.update({"status": {}}),
            lambda value: value["metadata"].update({"namespace": "default"}),
            lambda value: value["spec"].update({"compatibleClasses": []}),
            lambda value: value["spec"]["artifacts"][0].update({"size": 1}),
            lambda value: value["spec"]["executionPaths"][0]["runtime"].update(
                {"version": "1.23.2"}
            ),
        )
        for index, mutate in enumerate(mutations):
            with self.subTest(index=index):
                value = candidate(self.digest)
                mutate(value)
                with self.assertRaisesRegex(ValueError, "unknown fields"):
                    validate_structure(self.write_fixture(value))

    def test_rejects_duplicate_local_names(self) -> None:
        for collection in ("artifacts", "executionPaths"):
            with self.subTest(collection=collection):
                value = candidate(self.digest)
                value["spec"][collection].append(value["spec"][collection][0].copy())
                with self.assertRaisesRegex(ValueError, "duplicate name"):
                    validate_structure(self.write_fixture(value))

    def test_rejects_duplicate_yaml_mapping_key(self) -> None:
        fixture = self.root / "duplicate.yaml"
        fixture.write_text(
            "apiVersion: edge.dacs.io/v1alpha1\n"
            "kind: ModelSpec\n"
            "kind: DeviceClass\n"
            "metadata: {name: duplicate}\n"
            "spec: {}\n"
        )
        with self.assertRaisesRegex(ValueError, "duplicate key"):
            validate_structure(fixture)

    def test_rejects_wrong_api_identity(self) -> None:
        for field, value in (("apiVersion", "other/v1"), ("kind", "DeviceClass")):
            with self.subTest(field=field):
                document = candidate(self.digest)
                document[field] = value
                with self.assertRaisesRegex(ValueError, field):
                    validate_structure(self.write_fixture(document))

    def test_rejects_empty_lists_and_invalid_runtime_shape(self) -> None:
        document = candidate(self.digest)
        document["spec"]["artifacts"] = []
        with self.assertRaisesRegex(ValueError, "non-empty list"):
            validate_structure(self.write_fixture(document))

        document = candidate(self.digest)
        document["spec"]["executionPaths"][0]["runtime"] = []
        with self.assertRaisesRegex(ValueError, "runtime: mapping required"):
            validate_structure(self.write_fixture(document))

    def test_rejects_invalid_digest_syntax(self) -> None:
        for digest in ("8645", "sha256:ABC", "sha256:" + "0" * 63):
            with self.subTest(digest=digest):
                with self.assertRaisesRegex(ValueError, "canonical sha256"):
                    validate_structure(self.write_fixture(candidate(digest)))

    def test_rejects_dangling_artifact_reference(self) -> None:
        value = candidate(self.digest)
        value["spec"]["executionPaths"][0]["artifact"] = "missing"
        with self.assertRaisesRegex(ValueError, "unknown artifact"):
            validate_structure(self.write_fixture(value))

    def test_rejects_artifact_digest_mismatch(self) -> None:
        with self.assertRaisesRegex(ValueError, "artifact digest mismatch"):
            verify_artifact(self.artifact, "sha256:" + "0" * 64)

    def test_rejects_non_cpu_selected_path(self) -> None:
        value = candidate(self.digest)
        value["spec"]["executionPaths"][0]["runtime"]["backend"] = (
            "TensorrtExecutionProvider"
        )
        with self.assertRaisesRegex(ValueError, "CPUExecutionProvider"):
            validate(self.write_fixture(value), "ort-cpu")


if __name__ == "__main__":
    unittest.main()

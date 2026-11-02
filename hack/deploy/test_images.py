"""Local image publication tests; no Docker daemon, registry or cluster needed."""
import copy
import hashlib
import io
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import images


SOURCE = Path(__file__).resolve().parents[2]
SHA = "a" * 40


def encoded(value):
    return json.dumps(value).encode()


def digest(raw):
    return "sha256:" + hashlib.sha256(raw).hexdigest()


class Registry:
    """Serve actual hashed OCI documents and emulate only build metadata output."""
    def __init__(self, spec, revision=SHA, config_platform=None, advertised=None):
        self.calls = []
        self.documents = {}
        descriptors = []
        for platform in spec["platforms"]:
            os_name, architecture = (config_platform or platform).split("/")
            config = encoded({"os": os_name, "architecture": architecture,
                              "config": {"Labels": {images.REVISION_LABEL: revision}}})
            config_digest = digest(config)
            self.documents["blobs/" + config_digest] = config
            manifest = encoded({"schemaVersion": 2, "config": {"digest": config_digest}, "layers": []})
            manifest_digest = digest(manifest)
            self.documents["manifests/" + manifest_digest] = manifest
            descriptor_os, descriptor_arch = (advertised or platform).split("/")
            descriptors.append({"digest": manifest_digest,
                                "platform": {"os": descriptor_os, "architecture": descriptor_arch, "variant": "v8" if descriptor_arch == "arm64" else ""}})
        index = encoded({"schemaVersion": 2, "manifests": descriptors})
        self.digest = digest(index)
        tag = spec["image"].split("@", 1)[0].rsplit(":", 1)[1]
        self.documents["manifests/" + tag] = index

    def __call__(self, argv, name, timeout=120):
        self.calls.append((argv, name, timeout))
        if argv[:3] == ["docker", "buildx", "build"]:
            metadata = Path(argv[argv.index("--metadata-file") + 1])
            metadata.write_text(json.dumps({"containerimage.digest": self.digest}))
            return b""
        if argv[0] == "curl":
            url = argv[-1]
            for kind in ("manifests", "blobs"):
                marker = "/" + kind + "/"
                if marker in url:
                    return self.documents[kind + "/" + url.split(marker, 1)[1]]
        if argv[0] == "git":
            data = io.BytesIO()
            with tarfile.open(fileobj=data, mode="w") as archive:
                raw = b"module example.test/power\n"
                member = tarfile.TarInfo("go.mod")
                member.size = len(raw)
                archive.addfile(member, io.BytesIO(raw))
            return data.getvalue()
        raise AssertionError("unexpected process: " + repr(argv))


class ImageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.out = Path(self.temp.name)
        self.recipe = {"registry": "localhost:5000/chill", "registryScheme": "http", "builder": "test-builder", "images": ["server"]}

    def plan(self, selected=None):
        recipe = dict(self.recipe)
        if selected is not None:
            recipe["images"] = selected
        return images.plan(recipe, SHA, SOURCE, self.out)


    def test_build_and_verify_actual_multi_platform_receipts(self):
        plans = self.plan(["node-agent"])
        registry = Registry(plans[0])
        publication = images.build(plans, SHA, SOURCE, self.out, registry)
        entry = publication["roles"]["node-agent"]
        self.assertEqual(set(entry["platforms"]), set(images.MULTI))
        self.assertEqual(len(entry["platformDigests"]), 2)
        self.assertEqual(entry["platformReceipts"][1]["variant"], "v8")
        self.assertTrue(entry["image"].endswith("@" + registry.digest))
        argv = registry.calls[0][0]
        self.assertIn("registry.insecure=true", argv[argv.index("--output") + 1])
        self.assertIn(images.REVISION_LABEL + "=" + SHA, argv)
        self.assertEqual(registry.calls[0][2], 1800)
        self.assertEqual(json.loads((self.out / "publication.json").read_text()), publication)
        self.assertEqual(json.loads((self.out / "overlay.json").read_text()), images.overlay(publication))
        self.assertFalse(any("prune" in argv for argv, _, _ in registry.calls))

    def test_harness_named_context(self):
        plans = self.plan(["evaluation-harness"])
        registry = Registry(plans[0])
        publication = images.build(plans, SHA, SOURCE, self.out, registry)
        build_argv = next(argv for argv, _, _ in registry.calls if argv[0] == "docker")
        self.assertEqual(build_argv[build_argv.index("--build-context") + 1], "power-source=" + str(self.out / "power-source"))
        self.assertTrue(any(argv[-1] == images.POWER_SOURCE_SHA for argv, _, _ in registry.calls if argv[0] == "git"))
        self.assertEqual(publication["roles"]["evaluation-harness"]["powerSourceSHA"], images.POWER_SOURCE_SHA)
        self.assertEqual(images.overlay(publication), {})

    def test_registry_rejects_wrong_digest_revision_and_platform(self):
        spec = self.plan()[0]
        for kwargs in ({"revision": "b" * 40}, {"config_platform": "linux/arm64"}, {"advertised": "linux/arm64"}):
            with self.subTest(kwargs=kwargs):
                registry = Registry(spec, **kwargs)
                with self.assertRaises(ValueError):
                    images.verify(spec, SHA, registry.digest, registry)
        registry = Registry(spec)
        with self.assertRaises(ValueError):
            images.verify(spec, SHA, "sha256:" + "0" * 64, registry)
        tag_key = next(key for key in registry.documents if key.startswith("manifests/sha-"))
        registry.documents[tag_key] = b"[]"
        with self.assertRaises(ValueError):
            images.verify(spec, SHA, digest(b"[]"), registry)

    def test_overlay_component_and_scalar_observer_mappings(self):
        publication = {"roles": {}}
        for role in ("server", "metric-observer", "workload-observer", "load-generator", "firstfloor", "evaluation-harness"):
            spec = self.plan([role])[0]
            registry = Registry(spec)
            publication["roles"][role] = images.verify(spec, SHA, registry.digest, registry)
        overlay = images.overlay(publication)
        server = publication["roles"]["server"]
        self.assertEqual(overlay["servingEntrypoint"]["image"]["repository"], "localhost:5000/chill/chill-serving-entrypoint")
        self.assertEqual(overlay["servingEntrypoint"]["image"]["tag"], "sha-" + SHA[:12] + "@" + server["digest"])
        self.assertEqual(overlay["profiling"]["metricObserverImage"], publication["roles"]["metric-observer"]["image"])
        self.assertEqual(overlay["profiling"]["workloadObserverImage"], publication["roles"]["workload-observer"]["image"])
        self.assertEqual(set(overlay), {"servingEntrypoint", "profiling", "loadGenerator"})

    def test_prebuilt_verifies_without_docker_and_freezes_input(self):
        spec = self.plan(["firstfloor-cuda"])[0]
        registry = Registry(spec)
        entry = images.verify(spec, SHA, registry.digest, registry)
        receipt = self.out / "prebuilt.json"
        receipt.write_text(json.dumps({"sourceSHA": SHA, "roles": {"firstfloor-cuda": entry}}))
        plans = self.plan([{"role": "firstfloor-cuda", "prebuilt": str(receipt)}])
        receipt.write_text("invalid after freeze")
        registry.calls.clear()
        publication = images.build(plans, SHA, SOURCE, self.out, registry)
        self.assertEqual(publication["runtimeBuilds"], 0)
        self.assertEqual(publication["roles"]["firstfloor-cuda"], entry)
        self.assertTrue(registry.calls)
        self.assertTrue(all(argv[0] == "curl" for argv, _, _ in registry.calls))

    def test_prebuilt_wrong_provenance_is_rejected(self):
        spec = self.plan()[0]
        registry = Registry(spec)
        entry = images.verify(spec, SHA, registry.digest, registry)
        receipt = self.out / "prebuilt.json"
        changes = [("image", "foreign.test/chill/chill-serving-entrypoint:other"),
                   ("digest", "not-a-digest"), ("platforms", ["linux/arm64"]),
                   ("dockerfile", "build/docker/other.Dockerfile"),
                   ("buildArgs", {"SOURCE_SHA": "b" * 40}), ("platformDigests", [])]
        for key, value in changes:
            with self.subTest(key=key):
                invalid = copy.deepcopy(entry)
                invalid[key] = value
                receipt.write_text(json.dumps({"sourceSHA": SHA, "roles": {"server": invalid}}))
                with self.assertRaises(ValueError):
                    self.plan([{"role": "server", "prebuilt": str(receipt)}])
        receipt.write_text(json.dumps({"sourceSHA": "b" * 40, "roles": {"server": entry}}))
        with self.assertRaises(ValueError):
            self.plan([{"role": "server", "prebuilt": str(receipt)}])

    def test_prebuilt_requires_full_platform_receipts(self):
        spec = self.plan()[0]
        registry = Registry(spec)
        entry = images.verify(spec, SHA, registry.digest, registry)
        path = self.out / "prebuilt.json"
        invalid_receipts = [None, []]
        for key, value in (("revision", "b" * 40), ("platform", "linux/arm64"),
                           ("digest", "sha256:" + "b" * 64),
                           ("configDigest", "invalid"), ("variant", None)):
            receipts = copy.deepcopy(entry["platformReceipts"])
            receipts[0][key] = value
            invalid_receipts.append(receipts)
        for receipts in invalid_receipts:
            with self.subTest(receipts=receipts):
                invalid = copy.deepcopy(entry)
                invalid["platformReceipts"] = receipts
                path.write_text(json.dumps({"sourceSHA": SHA, "roles": {"server": invalid}}))
                with self.assertRaises(ValueError):
                    self.plan([{"role": "server", "prebuilt": str(path)}])

    def test_plan_rejects_unknown_duplicate_and_vendor_platform_override(self):
        for selected in (["unknown"], ["server", "server"], [{"role": "firstfloor-cuda", "platforms": ["linux/arm64"]}], [{"role": "server", "buildArgs": {"SECRET": "forbidden"}}]):
            with self.subTest(selected=selected), self.assertRaises(ValueError):
                self.plan(selected)


if __name__ == "__main__":
    unittest.main()

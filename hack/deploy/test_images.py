"""Local image publication tests; no Docker daemon, registry or cluster needed."""
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
                              "config": {"Labels": {images.REVISION_LABEL: revision} if revision is not None else {}}})
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
        self.documents["manifests/" + self.digest] = index

    def __call__(self, argv, name, timeout=120):
        self.calls.append((argv, name, timeout))
        if argv[:3] == ["docker", "buildx", "build"]:
            metadata = Path(argv[argv.index("--metadata-file") + 1])
            exporter = argv[argv.index("--output") + 1]
            image = exporter.split("name=", 1)[1].split(",", 1)[0]
            metadata.write_text(json.dumps({"containerimage.digest": self.digest, "image.name": image}))
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
        self.assertEqual(entry["sourceSHA"], SHA)
        self.assertEqual(entry["action"], "build")
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

    def test_registry_requires_selected_artifact_and_requested_platform(self):
        spec = self.plan()[0]
        for kwargs in ({"config_platform": "linux/arm64"}, {"advertised": "linux/arm64"}):
            with self.subTest(kwargs=kwargs):
                registry = Registry(spec, **kwargs)
                with self.assertRaises(ValueError):
                    images.verify(spec, SHA, registry.digest, registry)
        registry = Registry(spec)
        del registry.documents["manifests/" + registry.digest]
        with self.assertRaises(KeyError):
            images.verify(spec, SHA, registry.digest, registry)
        registry.documents["manifests/" + registry.digest] = b"[]"
        with self.assertRaises(ValueError):
            images.verify(spec, SHA, registry.digest, registry)

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
        del entry["sourceSHA"]  # Existing explicit prebuilt evidence remains valid.
        receipt = self.out / "prebuilt.json"
        receipt.write_text(json.dumps({"sourceSHA": SHA, "roles": {"firstfloor-cuda": entry}}))
        plans = self.plan([{"role": "firstfloor-cuda", "prebuilt": str(receipt)}])
        receipt.write_text("invalid after freeze")
        registry.calls.clear()
        publication = images.build(plans, SHA, SOURCE, self.out, registry)
        self.assertEqual(publication["runtimeBuilds"], 0)
        self.assertIsNone(publication["roles"]["firstfloor-cuda"]["sourceSHA"])
        self.assertEqual(publication["roles"]["firstfloor-cuda"]["platformReceipts"][0]["revision"], SHA)
        self.assertTrue(registry.calls)
        self.assertTrue(all(argv[0] == "curl" for argv, _, _ in registry.calls))

    def test_reuse_reverifies_old_origins_without_docker_or_power_archive(self):
        for role in ("node-agent", "firstfloor", "evaluation-harness"):
            with self.subTest(role=role):
                spec = self.plan([role])[0]
                registry = Registry(spec)
                previous = images.verify(spec, SHA, registry.digest, registry)
                del previous["sourceSHA"]  # Existing publications predate per-role origins.
                frozen = images.receipt_entry(previous, role, spec)
                spec.update({"reuse": {"sourceSHA": SHA, "receipt": frozen},
                             "action": "reuse", "reason": "tracked image inputs unchanged",
                             "inputPaths": [spec["dockerfile"]]})
                previous["digest"] = "invalid after freeze"
                for _ in range(2):
                    registry.calls.clear()
                    publication = images.build([spec], "b" * 40, SOURCE, self.out, registry)
                    entry = publication["roles"][role]
                    self.assertEqual(publication["sourceSHA"], "b" * 40)
                    self.assertEqual(publication["runtimeBuilds"], 0)
                    self.assertEqual(entry["sourceSHA"], SHA)
                    self.assertEqual(entry["action"], "reuse")
                    self.assertEqual(entry["reason"], spec["reason"])
                    self.assertEqual(entry["buildArgs"], spec["buildArgs"])
                    self.assertEqual(entry["image"], frozen["image"])
                    self.assertTrue(registry.calls)
                    self.assertTrue(all(argv[0] == "curl" for argv, _, _ in registry.calls))
                    if role == "evaluation-harness":
                        self.assertEqual(entry["buildArgs"]["SOURCE_SHA"], SHA)
                        self.assertEqual(entry["powerSourceSHA"], images.POWER_SOURCE_SHA)

    def test_reuse_ignores_moved_tag_and_stale_receipt_bookkeeping(self):
        spec = self.plan(["node-agent"])[0]
        registry = Registry(spec)
        previous = images.verify(spec, SHA, registry.digest, registry)
        previous["platformDigests"] = None
        previous["platformReceipts"] = []
        spec["reuse"] = {"sourceSHA": SHA, "receipt": previous}
        tag = spec["image"].rsplit(":", 1)[1]
        registry.documents["manifests/" + tag] = b"[]"
        publication = images.build([spec], "b" * 40, SOURCE, self.out, registry)
        entry = publication["roles"]["node-agent"]
        self.assertEqual(entry["sourceSHA"], SHA)
        self.assertEqual(entry["digest"], registry.digest)
        self.assertEqual({r["platform"] for r in entry["platformReceipts"]}, set(images.MULTI))

    def test_registry_keeps_raw_revision_separate_from_build_origin(self):
        spec = self.plan()[0]
        for revision in ("b" * 40, None, ""):
            with self.subTest(revision=revision):
                registry = Registry(spec, revision=revision)
                previous = images.verify(spec, SHA, registry.digest, registry)
                spec["reuse"] = {"sourceSHA": SHA, "receipt": previous}
                publication = images.build([spec], "c" * 40, SOURCE, self.out, registry)
                entry = publication["roles"]["server"]
                self.assertEqual(entry["sourceSHA"], SHA)
                self.assertEqual(entry["platformReceipts"][0]["revision"], revision)

    def test_prebuilt_checks_selected_repository_and_artifact(self):
        spec = self.plan()[0]
        registry = Registry(spec)
        entry = images.verify(spec, SHA, registry.digest, registry)
        for key, value in (("image", "foreign.test/chill/chill-serving-entrypoint:other"), ("digest", None)):
            with self.subTest(key=key):
                invalid = dict(entry, **{key: value})
                with self.assertRaises(ValueError):
                    images.receipt_entry(invalid, "server", spec)

    def test_prebuilt_uses_registry_platforms_not_historic_provenance(self):
        spec = self.plan()[0]
        registry = Registry(spec)
        entry = images.verify(spec, SHA, registry.digest, registry)
        entry.update(platformReceipts=None, platformDigests="stale", platforms=[],
                     dockerfile="old", buildArgs={"old": "args"})
        path = self.out / "prebuilt.json"
        path.write_text(json.dumps({"sourceSHA": "b" * 40, "roles": {"server": entry}}))
        plans = self.plan([{"role": "server", "prebuilt": str(path)}])
        publication = images.build(plans, "c" * 40, SOURCE, self.out, registry)
        self.assertEqual(publication["roles"]["server"]["sourceSHA"], SHA)
        self.assertEqual(publication["roles"]["server"]["platformReceipts"][0]["platform"], "linux/amd64")

    def test_registry_selects_requested_platform_among_extras(self):
        spec = self.plan()[0]
        registry = Registry(dict(spec, platforms=images.MULTI))
        entry = images.verify(spec, SHA, registry.digest, registry)
        self.assertEqual([r["platform"] for r in entry["platformReceipts"]], ["linux/amd64"])

    def test_plan_rejects_unknown_duplicate_and_vendor_platform_override(self):
        for selected in (["unknown"], ["server", "server"], [{"role": "firstfloor-cuda", "platforms": ["linux/arm64"]}], [{"role": "server", "buildArgs": {"SECRET": "forbidden"}}]):
            with self.subTest(selected=selected), self.assertRaises(ValueError):
                self.plan(selected)


if __name__ == "__main__":
    unittest.main()

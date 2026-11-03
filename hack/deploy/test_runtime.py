"""Pure local staging fixtures: no subprocesses, Kubernetes or image probes."""
import copy
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parent))
import runtime


SOURCE = Path(__file__).resolve().parents[2]
SHA = "a" * 40


def publication_fixture():
    roles = {}
    for index, (role, platform) in enumerate(runtime.RUNTIME_IMAGES.values()):
        entry = roles.setdefault(role, {"image": "registry.test:5000/chill/chill-" + role + ":source@sha256:" + "f" * 64,
                                        "sourceSHA": SHA, "platformReceipts": [], "platformDigests": []})
        child = "sha256:" + str(index + 1) * 64
        entry["platformReceipts"].append({"platform": platform, "digest": child, "revision": SHA})
        entry["platformDigests"].append(child)
    return {"sourceSHA": SHA, "roles": roles}


def inspection_fixture(publication, source=SOURCE):
    _, inspector = runtime._inspector(source)
    references = runtime._references(publication)
    declarations = []

    def append(device_class, architecture, backend, image, node=""):
        payload = {"schemaVersion": inspector.SCHEMA, "verification": inspector.VERIFICATION,
                   "image": image, "architecture": architecture,
                   "runtime": {"family": "firstfloor", "backends": [backend]},
                   "packages": [{"name": "fixture-observed-package", "version": "fixture-observed-version"}]}
        declaration = {"deviceClass": device_class, "runtime": {"payload": json.dumps(payload)}}
        if node:
            declaration["node"] = node
        if backend == "ort-cuda":
            declaration["access"] = {"gpuUUID": "GPU-fixture-observed-" + node}
        if backend == "rknnlite":
            declaration["host"] = {"driver": {"version": "fixture-observed-driver"}}
        declarations.append(declaration)

    for device_class, architecture, cuda_variable, nodes, _ in inspector.CLASSES:
        cpu_variable, _ = inspector.CPU[architecture]
        append(device_class, architecture, "ort-cpu", references[cpu_variable])
        if cuda_variable:
            for node in nodes:
                append(device_class, architecture, "ort-cuda", references[cuda_variable], node)
    append(inspector.RKNN_CLASS, "arm64", "rknnlite", references["RKNN_IMAGE"])
    return yaml.safe_dump({"profiling": {"executionDeclarations": declarations}}).encode()


def nodes_fixture(source=SOURCE):
    _, inspector = runtime._inspector(source)
    names = {inspector.CPU[architecture][1] for _, architecture, _, _, _ in inspector.CLASSES}
    names.add(inspector.RKNN_NODE)
    cuda_nodes = {node for _, _, variable, nodes, _ in inspector.CLASSES if variable for node in nodes}
    return {"items": [{"metadata": {"name": name},
                       "status": {"conditions": [{"type": "Ready", "status": "True"}],
                                  "allocatable": {"nvidia.com/gpu": "1" if name in cuda_nodes else "0"}}}
                      for name in sorted(names | cuda_nodes)]}


class RuntimeStageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.out = Path(self.temp.name)
        self.publication = publication_fixture()
        self.kube = ["/usr/bin/kubectl", "--kubeconfig", "/tmp/explicit config", "--context", "fixture-context", "-n", "fixture-namespace"]

    def test_mixed_role_origins_probe_the_same_exact_platform_digests(self):
        expected = {}
        for index, (role, entry) in enumerate(self.publication["roles"].items()):
            entry["sourceSHA"] = format(index + 1, "040x")
            for receipt in entry["platformReceipts"]:
                receipt["revision"] = entry["sourceSHA"]
        self.publication["sourceSHA"] = "b" * 40
        for variable, (role, platform) in runtime.RUNTIME_IMAGES.items():
            entry = self.publication["roles"][role]
            receipt = next(r for r in entry["platformReceipts"] if r["platform"] == platform)
            expected[variable] = entry["image"].split("@", 1)[0].rsplit(":", 1)[0] + "@" + receipt["digest"]
        raw = inspection_fixture(self.publication)
        probes = []

        def run(argv, name, timeout=120, env=None):
            if "nodes" in argv:
                return json.dumps(nodes_fixture()).encode()
            probes.append(argv)
            self.assertEqual({key: env[key] for key in expected}, expected)
            return raw

        result = runtime.stage(self.publication, SOURCE, self.out, self.kube, run)
        self.assertEqual(len(probes), 1)
        self.assertEqual(result.read_bytes(), raw)


    def test_rejects_foreign_output_image_without_freezing_values(self):
        document = yaml.safe_load(inspection_fixture(self.publication))
        declaration = document["profiling"]["executionDeclarations"][0]
        payload = json.loads(declaration["runtime"]["payload"])
        payload["image"] = "foreign.test/runtime@sha256:" + "b" * 64
        declaration["runtime"]["payload"] = json.dumps(payload)
        raw = yaml.safe_dump(document).encode()
        def run(argv, name, timeout=120, env=None):
            return json.dumps(nodes_fixture()).encode() if "nodes" in argv else raw

        with self.assertRaises(ValueError):
            runtime.stage(self.publication, SOURCE, self.out, self.kube, run)
        self.assertFalse((self.out / "runtime-values.yaml").exists())

    def test_timeout_cleans_only_this_invocations_exact_pod_names(self):
        owned_name = "chill-runtime-inspect-" + "a" * 32
        calls = []

        def run(argv, name, timeout=120, env=None):
            calls.append((argv, name, timeout))
            if "nodes" in argv:
                return json.dumps(nodes_fixture()).encode()
            if env is not None:
                (self.out / "runtime-owned-pods.txt").write_text(owned_name + "\n")
                raise TimeoutError("fixture inspector timeout")
            return b""

        with self.assertRaises(TimeoutError):
            runtime.stage(self.publication, SOURCE, self.out, self.kube, run, timeout=11)
        self.assertFalse((self.out / "runtime-values.yaml").exists())
        deletes = [argv for argv, _, _ in calls if "delete" in argv]
        self.assertEqual(deletes, [self.kube + ["--request-timeout=20s", "delete", "pod",
                                               owned_name, "--ignore-not-found=true", "--wait=false"]])

    def test_wrapper_records_exact_name_before_create_request(self):
        wrapper, owned = runtime._wrapper(self.out, self.kube)
        pod = {"kind": "Pod", "metadata": {"generateName": "chill-runtime-inspect-"},
               "spec": {"containers": [{"image": "fixture/runtime@sha256:" + "b" * 64}]}}
        observed = []

        def create(argv, input=None):
            names = owned.read_text().splitlines()
            self.assertEqual(len(names), 1)
            self.assertRegex(names[0], r"^chill-runtime-inspect-[0-9a-f]{32}$")
            sent = json.loads(input)
            self.assertEqual(sent["metadata"], {"name": names[0]})
            self.assertEqual(sent["spec"], pod["spec"])
            observed.append(names[0])
            return mock.Mock(returncode=0)

        args = [str(wrapper), "-n", "fixture-namespace", "create", "-f", "-", "-o", "name"]
        __import__("uuid")  # Load the stdlib's platform probe before mocking kubectl processes.
        with mock.patch.object(sys, "argv", args), mock.patch.object(sys, "stdin", io.StringIO(json.dumps(pod))), mock.patch("subprocess.run", side_effect=create):
            with self.assertRaises(SystemExit) as result:
                exec(compile(wrapper.read_text(), str(wrapper), "exec"), {})
        self.assertEqual(result.exception.code, 0)
        self.assertEqual(len(observed), 1)

    def test_requires_all_six_roles_and_correct_receipt_revision(self):
        for broken in ("missing", "revision", "missing-source", "invalid-source", "empty-source"):
            publication = copy.deepcopy(self.publication)
            if broken == "missing":
                del publication["roles"]["firstfloor-rknn"]
            elif broken == "missing-source":
                del publication["roles"]["firstfloor"]["sourceSHA"]
            elif broken == "invalid-source":
                publication["roles"]["firstfloor"]["sourceSHA"] = "not-a-commit"
            elif broken == "empty-source":
                publication["roles"]["firstfloor"]["sourceSHA"] = None
                publication["roles"]["firstfloor"]["platformReceipts"][0]["revision"] = None
            else:
                publication["roles"]["firstfloor"]["platformReceipts"][0]["revision"] = "b" * 40
            with self.subTest(broken=broken), self.assertRaises(ValueError):
                runtime.stage(publication, SOURCE, self.out, self.kube,
                              lambda *args, **kwargs: self.fail("must reject before inspector"))

    def test_preflight_to_stage_node_transitions_stop_before_inspection(self):
        _, inspector = runtime._inspector(SOURCE)
        cuda_node = next(node for _, _, variable, nodes, _ in inspector.CLASSES if variable for node in nodes)
        cpu_node = next(iter(inspector.CPU.values()))[1]
        for failure in ("not-ready", "gpu-zero", "missing"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                out = Path(directory)
                nodes = nodes_fixture()
                calls = []

                def run(argv, name, timeout=120, env=None):
                    calls.append(argv)
                    self.assertIn("nodes", argv, "must stop before any inspector or Pod command")
                    return json.dumps(nodes).encode()

                report = runtime.check_nodes(SOURCE, out, self.kube, run, "preflight")
                self.assertEqual(report["failures"], [])
                target = next(item for item in nodes["items"]
                              if item["metadata"]["name"] == (cpu_node if failure == "not-ready" else cuda_node))
                if failure == "not-ready":
                    target["status"]["conditions"][0]["status"] = "False"
                elif failure == "gpu-zero":
                    target["status"]["allocatable"]["nvidia.com/gpu"] = "0"
                else:
                    nodes["items"].remove(target)
                with self.assertRaises(ValueError):
                    runtime.stage(self.publication, SOURCE, out, self.kube, run)
                self.assertEqual(len(calls), 2, "no retry or probe after failed readiness")
                evidence = json.loads((out / "runtime-stage-nodes.json").read_text())
                self.assertTrue(evidence["failures"])
                self.assertFalse((out / "runtime-values.yaml").exists())

    def test_cpu_and_rknn_need_ready_but_not_gpu(self):
        nodes = nodes_fixture()
        report = runtime.check_nodes(SOURCE, self.out, self.kube,
                                     lambda *args, **kwargs: json.dumps(nodes).encode(), "cpu")
        cpu_targets = [node for node in report["nodes"] if not node["gpuRequired"]]
        self.assertTrue(cpu_targets)
        self.assertTrue(all(node["gpuAllocatable"] == "0" for node in cpu_targets))
        nodes["items"][0]["status"]["conditions"] = []
        with self.assertRaises(ValueError):
            runtime.check_nodes(SOURCE, self.out, self.kube,
                                lambda *args, **kwargs: json.dumps(nodes).encode(), "unknown")

    def test_node_query_failure_records_evidence_and_stops(self):
        def run(*args, **kwargs):
            raise TimeoutError("fixture Node read timeout")

        with self.assertRaises(TimeoutError):
            runtime.check_nodes(SOURCE, self.out, self.kube, run, "failed")
        evidence = json.loads((self.out / "failed-nodes.json").read_text())
        self.assertEqual(evidence["nodes"], [])

    def test_source_declaration_scope_is_exact(self):
        _, inspector = runtime._inspector(SOURCE)
        classes = copy.deepcopy(inspector.CLASSES)
        removed = "fixture-excluded-cuda-node"
        for index, (device_class, architecture, variable, nodes, groups) in enumerate(classes):
            if nodes:
                removed = nodes[0]
                classes[index] = (device_class, architecture, variable, nodes[1:], groups)
                break
        source = self.out / "source"
        (source / "hack").mkdir(parents=True)
        constants = {"CPU": inspector.CPU, "CLASSES": classes, "RKNN_NODE": inspector.RKNN_NODE,
                     "RKNN_CLASS": inspector.RKNN_CLASS, "SCHEMA": inspector.SCHEMA,
                     "VERIFICATION": inspector.VERIFICATION}
        (source / "hack/runtime-declarations.py").write_text(
            "\n".join(key + " = " + repr(value) for key, value in constants.items()) + "\n")
        _, reduced = runtime._inspector(source)
        references = runtime._references(self.publication)
        raw = inspection_fixture(self.publication, source)
        document = yaml.safe_load(raw)
        declarations = document["profiling"]["executionDeclarations"]
        runtime._validate(raw, reduced, references)
        nodes = nodes_fixture(source)
        report = runtime.check_nodes(source, self.out, self.kube,
                                     lambda *args, **kwargs: json.dumps(nodes).encode(), "reduced")
        self.assertNotIn(removed, [node["name"] for node in report["nodes"]])
        for broken in ("missing", "duplicate", "foreign", "synthetic"):
            invalid = copy.deepcopy(document)
            entries = invalid["profiling"]["executionDeclarations"]
            if broken == "missing":
                entries.pop()
            elif broken == "duplicate":
                entries[1] = copy.deepcopy(entries[0])
            elif broken == "foreign":
                entries[0]["deviceClass"] = "foreign-class"
            else:
                payload = json.loads(entries[0]["runtime"]["payload"])
                payload["verification"] = "synthetic"
                entries[0]["runtime"]["payload"] = json.dumps(payload)
            with self.subTest(broken=broken), self.assertRaises(ValueError):
                runtime._validate(yaml.safe_dump(invalid), reduced, references)
        extra = copy.deepcopy(next(entry for entry in declarations if entry.get("node")))
        extra["node"] = removed
        declarations.append(extra)
        with self.assertRaises(ValueError):
            runtime._validate(yaml.safe_dump(document), reduced, references)


if __name__ == "__main__":
    unittest.main()

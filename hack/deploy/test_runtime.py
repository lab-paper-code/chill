"""Pure local staging fixtures: no subprocesses, Kubernetes or image probes."""
import ast
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
                                        "platformReceipts": [], "platformDigests": []})
        child = "sha256:" + str(index + 1) * 64
        entry["platformReceipts"].append({"platform": platform, "digest": child, "revision": SHA})
        entry["platformDigests"].append(child)
    return {"sourceSHA": SHA, "roles": roles}


def inspection_fixture(publication):
    _, inspector = runtime._inspector(SOURCE)
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


class RuntimeStageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.out = Path(self.temp.name)
        self.publication = publication_fixture()
        self.kube = ["/usr/bin/kubectl", "--kubeconfig", "/tmp/explicit config", "--context", "fixture-context", "-n", "fixture-namespace"]

    def test_stage_binds_child_images_and_explicit_kube_environment(self):
        raw = inspection_fixture(self.publication)
        calls = []

        def run(argv, name, timeout=120, env=None):
            calls.append((argv, name, timeout, env))
            return raw

        path = runtime.stage(self.publication, SOURCE, self.out, self.kube, run, timeout=73)
        self.assertEqual(path, self.out / "runtime-values.yaml")
        self.assertEqual(path.read_bytes(), raw)
        argv, name, timeout, environment = calls[0]
        self.assertEqual(argv, [sys.executable, str(SOURCE / "hack/runtime-declarations.py")])
        self.assertEqual(name, "runtime-inspection.yaml")
        self.assertEqual(timeout, 73)
        self.assertEqual(environment["NAMESPACE"], "fixture-namespace")
        self.assertEqual(environment["INSPECT_TIMEOUT_SECONDS"], "73")
        wrapper = Path(environment["KUBECTL"])
        self.assertEqual(wrapper.stat().st_mode & 0o777, 0o700)
        assignments = {node.targets[0].id: ast.literal_eval(node.value)
                       for node in ast.parse(wrapper.read_text()).body
                       if isinstance(node, ast.Assign) and isinstance(node.targets[0], ast.Name)
                       and node.targets[0].id in ("KUBE", "OWNED")}
        self.assertEqual(assignments["KUBE"], self.kube)
        self.assertEqual(assignments["OWNED"], str(self.out / "runtime-owned-pods.txt"))
        for variable, (role, platform) in runtime.RUNTIME_IMAGES.items():
            receipt = next(r for r in self.publication["roles"][role]["platformReceipts"] if r["platform"] == platform)
            repository = self.publication["roles"][role]["image"].split("@", 1)[0].rsplit(":", 1)[0]
            self.assertEqual(environment[variable], repository + "@" + receipt["digest"])
            self.assertNotEqual(environment[variable].split("@", 1)[1], "sha256:" + "f" * 64)

    def test_rejects_foreign_output_image_without_freezing_values(self):
        document = yaml.safe_load(inspection_fixture(self.publication))
        declaration = document["profiling"]["executionDeclarations"][0]
        payload = json.loads(declaration["runtime"]["payload"])
        payload["image"] = "foreign.test/runtime@sha256:" + "b" * 64
        declaration["runtime"]["payload"] = json.dumps(payload)
        raw = yaml.safe_dump(document).encode()
        with self.assertRaises(ValueError):
            runtime.stage(self.publication, SOURCE, self.out, self.kube,
                          lambda argv, name, timeout, env: raw)
        self.assertFalse((self.out / "runtime-values.yaml").exists())

    def test_timeout_cleans_only_this_invocations_exact_pod_names(self):
        owned_name = "chill-runtime-inspect-" + "a" * 32
        calls = []

        def run(argv, name, timeout=120, env=None):
            calls.append((argv, name, timeout))
            if env is not None:
                (self.out / "runtime-owned-pods.txt").write_text(owned_name + "\n")
                raise TimeoutError("fixture inspector timeout")
            return b""

        with self.assertRaisesRegex(TimeoutError, "fixture inspector timeout"):
            runtime.stage(self.publication, SOURCE, self.out, self.kube, run, timeout=11)
        self.assertEqual(len(calls), 2)
        self.assertEqual(calls[1], (self.kube + ["--request-timeout=20s", "delete", "pod",
                                              owned_name, "--ignore-not-found=true", "--wait=false"],
                                    "runtime-inspection-cleanup.log", 30))
        self.assertFalse((self.out / "runtime-values.yaml").exists())

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
        for broken in ("missing", "revision"):
            publication = copy.deepcopy(self.publication)
            if broken == "missing":
                del publication["roles"]["firstfloor-rknn"]
            else:
                publication["roles"]["firstfloor"]["platformReceipts"][0]["revision"] = "b" * 40
            with self.subTest(broken=broken), self.assertRaises(ValueError):
                runtime.stage(publication, SOURCE, self.out, self.kube,
                              lambda *args, **kwargs: self.fail("must reject before inspector"))


if __name__ == "__main__":
    unittest.main()

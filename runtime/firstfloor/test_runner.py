import importlib.util
from io import BytesIO
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock
import uuid

HERE = Path(__file__).resolve().parent
FIXTURE = HERE / "testdata" / "echo"
_spec = importlib.util.spec_from_file_location("chill_firstfloor_runner_under_test", HERE / "runner.py")
runner = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(runner)


def encode(header, data=b""):
    buffer = BytesIO()
    runner.write_frame(buffer, header, data)
    return buffer.getvalue()


def infer_frame(batch_id, values):
    payload = runner.frame_elements(values)
    tensor = {"name": "input", "datatype": "BYTES", "shape": [len(values)], "bytes": len(payload)}
    return encode({"type": "infer", "batch_id": batch_id, "tensors": [tensor]}, payload)


def serve(batches, max_new_tokens=0, preprocessor=FIXTURE, tail=b"", warmup_batch=None, initialized=(1, 2)):
    init = encode({"type": "init", "inputs": ["input"], "outputs": ["output"], "batch_sizes": list(initialized),
                   "max_new_tokens": max_new_tokens})
    source = BytesIO(init + b"".join(infer_frame(i + 1, values) for i, values in enumerate(batches)) + tail)
    destination = BytesIO()
    options = SimpleNamespace(runtime="echo", preprocessor=str(preprocessor), model_dir=None, delay_ms=0,
                              warmup_iterations=1, gpu_uuid=None,
                              delivery_json=None, warmup_batch=warmup_batch)
    error = None
    try:
        runner.run(options, source, destination)
    except Exception as raised:
        error = raised
    destination.seek(0)
    frames = []
    while True:
        message = runner.read_frame(destination)
        if message is None:
            return frames, error
        frames.append(message)


class RunnerContractTest(unittest.TestCase):
    def test_warms_only_requested_batches_without_changing_capabilities(self):
        for selected, expected in ((None, [1, 2, 3, 4]), ([1], [1]), ([2], [2]), ([1, 4], [1, 4])):
            with self.subTest(selected=selected), mock.patch.object(runner, "execute", wraps=runner.execute) as execute:
                frames, error = serve([], warmup_batch=selected, initialized=[1, 2, 3, 4])
                self.assertIsNone(error)
                self.assertEqual([len(call.args[2]) for call in execute.call_args_list], expected)
                self.assertEqual(frames[0][0]["metadata"]["initializedBatches"], [1, 2, 3, 4])
        frames, error = serve([], warmup_batch=[1, 3])
        self.assertIsInstance(error, ValueError)
        self.assertIn("warmup batch", str(error))

    def test_rknn_validates_each_batch_only_before_its_first_execution(self):
        events = []

        def validate_batch(model, candidate, batch):
            events.append(("validate", batch))
            if batch == 4:
                raise ValueError("unsupported actual RKNN batch 4")

        def execute(module, sessions, samples, outputs, max_new_tokens, delay_s):
            events.append(("execute", len(samples)))
            return [{"units": 1} for _ in samples], {"output": [b"ok" for _ in samples]}, {}

        native = mock.Mock()
        native.load_rknn.return_value = native.init_runtime.return_value = 0
        lite = mock.Mock(return_value=native, NPU_CORE_0=1)
        helper = SimpleNamespace(
            validate_candidate=mock.Mock(),
            observe_host_contract=lambda candidate, batch: {"package": {"version": "test"}, "selectedBatch": batch},
            validate_runtime_inputs=validate_batch,
        )
        cpu = SimpleNamespace(read_cpu_environment=lambda: ("", [0], ""))
        init = encode({"type": "init", "inputs": ["input"], "outputs": ["output"],
                       "batch_sizes": [1, 2, 4], "max_new_tokens": 0})
        source = BytesIO(init + b"".join(infer_frame(i + 1, [b"x"] * batch)
                                        for i, batch in enumerate([1, 1, 2, 2, 4])))
        destination = BytesIO()
        with tempfile.TemporaryDirectory() as directory:
            delivery = Path(directory) / "delivery.json"
            delivery.write_text('{"artifactCapability":{"tensors":[{"datatype":"FP32"}]}}')
            options = SimpleNamespace(runtime="rknnlite", preprocessor=str(FIXTURE), model_dir=directory,
                                      delay_ms=0, warmup_iterations=1, warmup_batch=[1],
                                      delivery_json=str(delivery))
            with mock.patch.dict(sys.modules, {"numpy": SimpleNamespace(__version__="test"),
                                              "rknnlite": mock.Mock(), "rknnlite.api": SimpleNamespace(RKNNLite=lite)}), \
                    mock.patch.object(runner, "import_runtime_contract", side_effect=lambda family: helper if family == "rknnlite" else cpu), \
                    mock.patch.object(runner, "model_files", return_value=[Path(directory) / "model.rknn"]), \
                    mock.patch.object(runner, "execute", side_effect=execute):
                with self.assertRaisesRegex(ValueError, "unsupported actual RKNN batch 4"):
                    runner.run(options, source, destination)
        self.assertEqual(events, [("validate", 1), ("execute", 1), ("execute", 1), ("execute", 1),
                                  ("validate", 2), ("execute", 2), ("execute", 2), ("validate", 4)])
        destination.seek(0)
        ready, _ = runner.read_frame(destination)
        self.assertEqual(ready["type"], "ready")
        self.assertEqual(ready["metadata"]["initializedBatches"], [1, 2, 4])
        self.assertEqual(ready["metadata"]["rknn"]["selectedBatch"], 1)
        for batch_id in range(1, 5):
            output, _ = runner.read_frame(destination)
            self.assertEqual((output["type"], output["batch_id"]), ("output", batch_id))
        failure, _ = runner.read_frame(destination)
        self.assertEqual((failure["type"], failure["batch_id"]), ("error", 5))
        self.assertIn("unsupported actual RKNN batch 4", failure["error"])
        self.assertIsNone(runner.read_frame(destination))
        native.release.assert_called_once_with()

    def test_repeatable_warmup_flag_checks_every_requested_batch(self):
        init = encode({"type": "init", "inputs": ["input"], "outputs": ["output"], "batch_sizes": [1, 2],
                       "max_new_tokens": 0})
        for first, success in ((2, True), (3, False)):
            with self.subTest(first=first):
                result = subprocess.run(
                    [sys.executable, str(HERE / "runner.py"), "--echo", "--preprocessor", str(FIXTURE),
                     "--warmup-iterations", "1", "--warmup-batch", str(first), "--warmup-batch", "1"],
                    input=init, capture_output=True, check=False)
                self.assertEqual(result.returncode == 0, success, result.stderr)
                header, _ = runner.read_frame(BytesIO(result.stdout))
                self.assertEqual(header["type"], "ready" if success else "error")

    def test_item_failure_ends_only_its_request(self):
        frames, error = serve([[b"tile+tile", b"fail:bad jpeg"]])
        self.assertIsNone(error)
        self.assertEqual(frames[0][0]["metadata"]["modelFiles"], [])
        self.assertEqual(frames[0][0]["metadata"]["outputs"], ["output"])
        header, payload = frames[1]
        self.assertEqual(header["type"], "output")
        self.assertEqual(header["items"], [{"units": 2}, {"error": "bad jpeg"}])
        self.assertEqual(header["tensors"], [{"name": "output", "datatype": "BYTES", "shape": [1], "bytes": len(payload)}])
        self.assertEqual(runner.unframe(payload, 0, len(payload), 1), [b"tile+tile"])
        self.assertNotIn("first_token_ms", header)
        stages = header["preprocess_ms"] + header["backend_ms"] + header["postprocess_ms"]
        self.assertLessEqual(stages, header["service_ms"])

    def test_batch_errors_keep_serving(self):
        frames, error = serve([[b"short", b"x"], [b"raise"], [b"bad-output"], [b"ok"]])
        self.assertIsNone(error)
        self.assertEqual([h["type"] for h, _ in frames], ["ready", "batch_error", "batch_error", "batch_error", "output"])
        self.assertEqual([h["batch_id"] for h, _ in frames[1:]], [1, 2, 3, 4])
        self.assertIn("RuntimeError", frames[2][0]["error"])

    def test_generative_first_token_rule(self):
        frames, error = serve([[b"no-first-token"], [b"two-first-tokens"], [b"ok"]], max_new_tokens=4)
        self.assertIsNone(error)
        self.assertEqual([h["type"] for h, _ in frames], ["ready", "batch_error", "batch_error", "output"])
        header = frames[3][0]
        self.assertLessEqual(header["first_token_ms"], header["backend_ms"])

    def test_bytes_framing(self):
        values = [b"", b"\x00\xff", b"abc"]
        data = runner.frame_elements(values)
        self.assertEqual(runner.unframe(data, 0, len(data), 3), values)
        for count, end in ((2, len(data)), (3, len(data) - 1)):
            with self.assertRaises(ValueError):
                runner.unframe(data, 0, end, count)
        broken = encode({"type": "infer", "batch_id": 1, "tensors": [
            {"name": "input", "datatype": "BYTES", "shape": [1], "bytes": 3}]}, b"\x05\x00\x00")
        frames, error = serve([], tail=broken)
        self.assertIsInstance(error, ValueError)
        self.assertEqual(frames[-1][0]["type"], "error")

    def test_incomplete_module_is_a_startup_error(self):
        with tempfile.TemporaryDirectory() as directory:
            (Path(directory) / "preprocessor.py").write_text("WARMUP_SAMPLE = {'input': b''}\ndef preprocess(s): pass\n")
            frames, error = serve([], preprocessor=directory)
        self.assertIsNotNone(error)
        self.assertEqual([h["type"] for h, _ in frames], ["error"])
        self.assertIn("infer", frames[0][0]["error"])

    def test_ort_provider_chains(self):
        gpu = uuid.UUID(int=1)
        cuda, cpu = "CUDAExecutionProvider", "CPUExecutionProvider"
        state = {}

        class Options:
            def __init__(self):
                self.entries = state["entries"] = {}
                self.intra_op_num_threads = 0
                self.inter_op_num_threads = 0

            def add_session_config_entry(self, key, value):
                self.entries[key] = value

        class Session:
            def __init__(self, path, sess_options, providers):
                self.settings = sess_options

            def disable_fallback(self):
                pass

            def get_providers(self):
                return state["chain"]

            def get_session_options(self):
                return self.settings

        class Driver:
            def cuInit(self, flags):
                return 0

            def cuDeviceGetCount(self, count):
                count._obj.value = 1
                return 0

            def cuDeviceGet(self, device, index):
                return 0

            def cuDeviceGetUuid(self, actual, device):
                actual.raw = state.get("actual", gpu).bytes
                return 0

        ort = SimpleNamespace(__version__="test", get_available_providers=lambda: [cuda, cpu],
                              SessionOptions=Options, InferenceSession=Session)
        modules = {"onnxruntime": ort}
        uuid_text = f"GPU-{gpu}"

        def sessions(runtime, chain):
            state["chain"] = chain
            options = SimpleNamespace(runtime=runtime, gpu_uuid=uuid_text)
            metadata = {}
            runner.ort_sessions(options, [Path("model.onnx")], metadata)
            return metadata

        with mock.patch.dict(sys.modules, modules), mock.patch.dict(os.environ, {"CUDA_VISIBLE_DEVICES": uuid_text}), \
                mock.patch("ctypes.CDLL", return_value=Driver()):
            # CUDA sessions keep ORT's CPU-preferred shape nodes; no CPU-fallback ban is configured.
            self.assertEqual(sessions("ort-cuda", [cuda, cpu])["providers"], [cuda, cpu])
            self.assertEqual(sessions("ort-cpu", [cpu])["intraOpThreads"], 0)
            self.assertEqual(sessions("ort-cpu", [cpu])["interOpThreads"], 0)
            self.assertNotIn("session.disable_cpu_ep_fallback", state["entries"])
            self.assertEqual(sessions("ort-cpu", [cpu])["providers"], [cpu])
            for runtime, chain in (("ort-cuda", [cuda]), ("ort-cuda", [cpu]), ("ort-cpu", [cuda, cpu])):
                with self.subTest(runtime=runtime, chain=chain), self.assertRaises(ValueError):
                    sessions(runtime, chain)
            state["actual"] = uuid.UUID(int=2)
            with self.assertRaisesRegex(ValueError, "UUID could not be verified"):
                sessions("ort-cuda", [cuda, cpu])


if __name__ == "__main__":
    unittest.main()

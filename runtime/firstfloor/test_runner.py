import importlib.util
from io import BytesIO
import os
from pathlib import Path
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


def serve(batches, max_new_tokens=0, preprocessor=FIXTURE, tail=b""):
    init = encode({"type": "init", "inputs": ["input"], "outputs": ["output"], "batch_sizes": [1, 2],
                   "max_new_tokens": max_new_tokens})
    source = BytesIO(init + b"".join(infer_frame(i + 1, values) for i, values in enumerate(batches)) + tail)
    destination = BytesIO()
    options = SimpleNamespace(runtime="echo", preprocessor=str(preprocessor), model_dir=None, delay_ms=0,
                              warmup_iterations=1, cpu_affinity=None, intra_op_threads=1, gpu_uuid=None,
                              delivery_json=None)
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

            def add_session_config_entry(self, key, value):
                self.entries[key] = value

        class Session:
            def __init__(self, path, sess_options, providers):
                pass

            def disable_fallback(self):
                pass

            def get_providers(self):
                return state["chain"]

        driver = SimpleNamespace(CUresult=SimpleNamespace(CUDA_SUCCESS=0), cuInit=lambda flags: (0,),
                                 cuDeviceGetCount=lambda: (0, 1), cuDeviceGet=lambda index: (0, 0),
                                 cuDeviceGetUuid=lambda device: (0, SimpleNamespace(bytes=gpu.bytes)))
        ort = SimpleNamespace(__version__="test", get_available_providers=lambda: [cuda, cpu],
                              SessionOptions=Options, InferenceSession=Session)
        modules = {"onnxruntime": ort, "cuda": SimpleNamespace(), "cuda.bindings": SimpleNamespace(driver=driver)}
        uuid_text = f"GPU-{gpu}"

        def sessions(runtime, chain):
            state["chain"] = chain
            options = SimpleNamespace(runtime=runtime, gpu_uuid=uuid_text, intra_op_threads=1)
            metadata = {}
            runner.ort_sessions(options, [Path("model.onnx")], metadata)
            return metadata

        with mock.patch.dict(sys.modules, modules), mock.patch.dict(os.environ, {"CUDA_VISIBLE_DEVICES": uuid_text}):
            # CUDA sessions keep ORT's CPU-preferred shape nodes; no CPU-fallback ban is configured.
            self.assertEqual(sessions("ort-cuda", [cuda, cpu])["providers"], [cuda, cpu])
            self.assertNotIn("session.disable_cpu_ep_fallback", state["entries"])
            self.assertEqual(sessions("ort-cpu", [cpu])["providers"], [cpu])
            for runtime, chain in (("ort-cuda", [cuda]), ("ort-cuda", [cpu]), ("ort-cpu", [cuda, cpu])):
                with self.subTest(runtime=runtime, chain=chain), self.assertRaises(ValueError):
                    sessions(runtime, chain)


if __name__ == "__main__":
    unittest.main()

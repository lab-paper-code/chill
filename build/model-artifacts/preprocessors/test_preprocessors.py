import importlib.util
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from PIL import Image

# Keep __pycache__ out of the carrier directories.
sys.dont_write_bytecode = True

ROOT = Path(__file__).resolve().parent


def load(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def jpeg(size, color=(124, 116, 104)):
    buffer = io.BytesIO()
    Image.new("RGB", size, color).save(buffer, "JPEG", quality=95)
    return buffer.getvalue()


CORRUPT = jpeg((300, 200))[:400]


class FakeORT:
    def __init__(self, logits):
        self.logits, self.feeds = logits, None

    def run(self, names, feeds):
        self.feeds = feeds
        return [self.logits]


class FakeRKNN:
    def __init__(self, logits):
        self.logits, self.inputs = logits, None

    def inference(self, inputs, data_format):
        self.inputs = (inputs, data_format)
        return [self.logits]


class MobilenetTest(unittest.TestCase):
    def check(self, directory, shape):
        module = load(ROOT / directory / "preprocessor.py", directory.replace("-", "_"))
        inputs, report = module.preprocess([{"image": jpeg((300, 200))}, {"image": CORRUPT}, module.WARMUP_SAMPLE])
        self.assertEqual(report[0::2], [1, 1])
        self.assertIsInstance(report[1], str)
        self.assertEqual((inputs.shape, inputs.dtype), ((2, *shape), np.float32))
        expected = (np.array([124, 116, 104]) / 255 - module.MEAN) / module.STD
        channels = inputs[0].reshape(3, -1).mean(axis=1) if shape[0] == 3 else inputs[0].reshape(-1, 3).mean(axis=0)
        np.testing.assert_allclose(channels, expected, atol=0.05)
        self.assertEqual(module.preprocess([{"image": CORRUPT}])[0], None)
        logits = np.zeros((2, 1000), np.float32)
        logits[0, 7], logits[1, 999] = 1, 1
        return module, inputs, logits

    def test_nchw_module_classifies_raw_jpegs_and_reports_a_corrupt_one(self):
        module, inputs, logits = self.check("mobilenet-nchw", (3, 224, 224))
        session = FakeORT(logits)
        outputs = module.infer({"mobilenet-v2-050": session}, inputs, 0, lambda: self.fail("not generative"))
        self.assertIs(session.feeds["input"], inputs)
        self.assertEqual(module.postprocess(outputs), [{"label": b"7"}, {"label": b"999"}])

    def test_nhwc_module_classifies_raw_jpegs_and_reports_a_corrupt_one(self):
        module, inputs, logits = self.check("mobilenet-nhwc", (224, 224, 3))
        rknn = FakeRKNN(logits)
        outputs = module.infer({"mobilenet-v2-050-rk3588": rknn}, inputs, 0, lambda: self.fail("not generative"))
        self.assertEqual(rknn.inputs, ([inputs], ["nhwc"]))
        self.assertEqual(module.postprocess(outputs), [{"label": b"7"}, {"label": b"999"}])


try:
    import huggingface_hub  # noqa: F401
    import jinja2  # noqa: F401
    import num2words  # noqa: F401
    from transformers.models.smolvlm import processing_smolvlm  # noqa: F401
except ImportError:
    SMOLVLM = False
else:
    SMOLVLM = True


class FakeSmolVLM:
    """Records decoder steps and emits one scripted greedy token per request and step."""

    def __init__(self, module, script):
        self.module, self.script, self.steps = module, script, []

    def run_embed(self, names, feeds):
        return [np.zeros((*feeds["input_ids"].shape, 960), np.float32)]

    def run_vision(self, names, feeds):
        self.vision = feeds
        return [np.ones((int(feeds["pixel_attention_mask"].any(axis=(2, 3)).sum()), 64, 960), np.float32)]

    def run_decoder(self, names, feeds):
        module, step = self.module, len(self.steps)
        self.steps.append(feeds)
        embeds, mask = feeds["inputs_embeds"], feeds["attention_mask"]
        batch, length = embeds.shape[:2]
        past = feeds["past_key_values.0.key"].shape[2]
        assert names == module.DECODER_OUTPUTS and mask.shape == (batch, past + length)
        assert all(feeds[name].shape == (batch, 5, past, 64) for name in module.PAST)
        logits = np.zeros((batch, length, 49280), np.float32)
        for row, tokens in enumerate(self.script):
            logits[row, -1, tokens[min(step, len(tokens) - 1)]] = 1
        return [logits] + [np.zeros((batch, 5, past + length, 64), np.float32)] * len(module.PAST)

    def sessions(self):
        def session(run):
            return type("Session", (), {"run": staticmethod(run)})()

        return {"embed_tokens": session(self.run_embed), "vision_encoder": session(self.run_vision),
                "decoder_model_merged": session(self.run_decoder)}


@unittest.skipUnless(SMOLVLM, "runtime-image SmolVLM dependencies (transformers>=4.50, num2words, jinja2) absent")
class SmolVLMTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        stage = load(ROOT.parent / "smolvlm2" / "stage.py", "smolvlm2_stage")
        cls.directory = tempfile.TemporaryDirectory()
        stage.stage_preprocessor(Path(cls.directory.name))
        cls.module = load(Path(cls.directory.name) / "preprocessor.py", "smolvlm2_vqa")

    @classmethod
    def tearDownClass(cls):
        cls.directory.cleanup()

    def test_preprocess_counts_tiles_and_reports_failed_samples(self):
        module = self.module
        self.assertEqual(set(module.WARMUP_SAMPLE), {"image", "question"})
        inputs, report = module.preprocess([
            {"image": jpeg((300, 300)), "question": b"What color is it?"},
            {"image": CORRUPT, "question": b"What is it?"},
            module.WARMUP_SAMPLE,
            {"image": jpeg((300, 300)), "question": b"\xff"},
        ])
        # Both images resize to longest edge 2048: 4x4 + global and 4x3 + global tiles.
        self.assertEqual(report[0::2], [17, 13])
        self.assertTrue(all(isinstance(item, str) for item in report[1::2]))
        self.assertEqual(inputs["pixel_values"].shape, (2, 17, 3, 512, 512))
        self.assertEqual(len(inputs["input_ids"]), 2)
        self.assertEqual(module.preprocess([{"image": CORRUPT, "question": b"q"}])[0], None)

    def test_infer_merges_image_features_and_decodes_greedily_until_eos_or_limit(self):
        module = self.module
        inputs, _ = module.preprocess([module.WARMUP_SAMPLE, module.WARMUP_SAMPLE])
        words = module.TOKENIZER.encode(" two cats sleep on a red sofa", add_special_tokens=False)[:4]
        fake = FakeSmolVLM(module, [[words[0], module.EOS], words])
        calls = []
        tokens = module.infer(fake.sessions(), inputs, 4, lambda: calls.append(len(fake.steps)))
        self.assertEqual(calls, [1])
        self.assertEqual(len(fake.steps), 4)
        self.assertEqual(fake.vision["pixel_attention_mask"].dtype, np.bool_)
        prefill = fake.steps[0]
        np.testing.assert_array_equal(prefill["inputs_embeds"][inputs["input_ids"] == module.IMAGE_TOKEN], 1)
        self.assertEqual([step["inputs_embeds"].shape[1] for step in fake.steps[1:]], [1, 1, 1])
        self.assertEqual(module.postprocess(tokens), [
            {"answer": module.TOKENIZER.decode(words[:1]).strip().encode()},
            {"answer": module.TOKENIZER.decode(words).strip().encode()},
        ])

        fake = FakeSmolVLM(module, [[words[0], module.EOS], [module.EOS]])
        tokens = module.infer(fake.sessions(), inputs, 16, lambda: None)
        self.assertEqual((len(fake.steps), tokens.shape), (2, (2, 2)))


class PLCTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        carrier = Path(self.directory.name)
        carrier.joinpath("preprocessor.py").write_bytes((ROOT / "plc-extruder-anomaly" / "preprocessor.py").read_bytes())
        carrier.joinpath("stats.json").write_text(json.dumps(
            {"features": ["A", "B"], "window": 3, "mean": [1, 10], "std": [2, 5], "threshold": 0.5}))
        self.module = load(carrier / "preprocessor.py", "plc_extruder_anomaly")

    def tearDown(self):
        self.directory.cleanup()

    def test_module_scores_raw_windows_and_reports_malformed_ones(self):
        module = self.module
        good = json.dumps([{"A": 3, "B": 20.0, "MOLD_TEMP": 0, "stats": None}] * 3).encode()
        bad = [b"\xff", b"{}", json.dumps([{"A": 1, "B": 1}] * 2).encode(),
               json.dumps([{"A": 1, "B": 1}, {"A": "1", "B": 1}, {"A": 1, "B": 1}]).encode(),
               json.dumps([{"A": 1}, {"A": 1, "B": 1}, {"A": 1, "B": 1}]).encode(),
               json.dumps([{"A": 1e300, "B": 1}] + [{"A": 1, "B": 1}] * 2).encode()]
        inputs, report = module.preprocess([{"records": good}, *({"records": b} for b in bad), module.WARMUP_SAMPLE])
        self.assertEqual(report[0::len(bad) + 1], [1, 1])
        self.assertTrue(all(isinstance(item, str) for item in report[1:-1]))
        self.assertEqual((inputs.shape, inputs.dtype), ((2, 3, 2), np.float32))
        np.testing.assert_allclose(inputs[0], [[1, 2]] * 3)
        self.assertEqual(module.preprocess([{"records": bad[0]}])[0], None)
        session = FakeORT(np.array([0.25, 0.75], np.float32))
        outputs = module.infer({"plc-lstm-ae": session}, inputs, 0, lambda: self.fail("not generative"))
        self.assertIs(session.feeds["records"], inputs)
        self.assertEqual(module.postprocess(outputs), [{"score": b"0.25", "anomaly": b"false"},
                                                       {"score": b"0.75", "anomaly": b"true"}])


if __name__ == "__main__":
    unittest.main()

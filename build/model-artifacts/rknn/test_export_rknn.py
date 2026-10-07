import importlib.util
import unittest
from pathlib import Path
from types import SimpleNamespace


MODULE_PATH = Path(__file__).with_name("export_rknn.py")
SPEC = importlib.util.spec_from_file_location("export_rknn", MODULE_PATH)
EXPORT_RKNN = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EXPORT_RKNN)


def source_model(*, count=1, datatype=1, shape=("batch", 3, 12, 20)):
    dimensions = [
        SimpleNamespace(dim_param=value if isinstance(value, str) else "", dim_value=value if isinstance(value, int) else 0)
        for value in shape
    ]
    inputs = [
        SimpleNamespace(
            name="images" if index == 0 else f"input{index}",
            type=SimpleNamespace(tensor_type=SimpleNamespace(
                elem_type=datatype, shape=SimpleNamespace(dim=dimensions),
            )),
        )
        for index in range(count)
    ]
    return SimpleNamespace(graph=SimpleNamespace(input=inputs, initializer=[]))


class ExportRKNNTest(unittest.TestCase):
    def test_export_preserves_name_fp32_and_nhwc_sample_shape(self):
        sample, tensor = EXPORT_RKNN.model_input_contract(source_model())
        self.assertEqual(sample, [3, 12, 20])
        self.assertEqual(tensor, {
            "name": "images", "datatype": "FP32", "sampleShape": [12, 20, 3],
        })

    def test_export_rejects_unsupported_source_inputs(self):
        for model, message in [
            (source_model(count=2), "exactly one"),
            (source_model(datatype=7), "FP32"),
            (source_model(shape=("batch", 3, 12)), "rank4"),
            (source_model(shape=("batch", 3, "height", 20)), "positive"),
        ]:
            with self.subTest(message=message), self.assertRaisesRegex(ValueError, message):
                EXPORT_RKNN.model_input_contract(model)

    def test_dynamic_input_enumerates_sorted_unique_profiles(self):
        self.assertEqual(
            EXPORT_RKNN.build_dynamic_input([3, 224, 224], [4, 1, 2, 1]),
            [[[1, 3, 224, 224]], [[2, 3, 224, 224]], [[4, 3, 224, 224]]],
        )

    def test_dynamic_input_rejects_a_non_positive_profile(self):
        with self.assertRaisesRegex(ValueError, "positive"):
            EXPORT_RKNN.build_dynamic_input([3, 224, 224], [1, 0])

    def test_input_sample_shape_drops_the_leading_axis(self):
        self.assertEqual(EXPORT_RKNN.input_sample_shape(["batch", 3, 224, 224]), [3, 224, 224])

    def test_input_sample_shape_rejects_a_concrete_batch_axis(self):
        with self.assertRaisesRegex(ValueError, "symbolic"):
            EXPORT_RKNN.input_sample_shape([1, 3, 224, 224])


if __name__ == "__main__":
    unittest.main()

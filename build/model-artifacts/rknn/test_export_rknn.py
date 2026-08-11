import importlib.util
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("export_rknn.py")
SPEC = importlib.util.spec_from_file_location("export_rknn", MODULE_PATH)
EXPORT_RKNN = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EXPORT_RKNN)


class ExportRKNNTest(unittest.TestCase):
    def test_dynamic_input_enumerates_sorted_unique_profiles(self):
        self.assertEqual(
            EXPORT_RKNN.build_dynamic_input([3, 224, 224], [4, 1, 2, 1]),
            [[[1, 3, 224, 224]], [[2, 3, 224, 224]], [[4, 3, 224, 224]]],
        )

    def test_dynamic_input_rejects_a_non_positive_profile(self):
        with self.assertRaisesRegex(ValueError, "positive"):
            EXPORT_RKNN.build_dynamic_input([3, 224, 224], [1, 0])

    def test_non_batch_shape_drops_the_leading_axis(self):
        self.assertEqual(EXPORT_RKNN.non_batch_shape(["batch", 3, 224, 224]), [3, 224, 224])

    def test_non_batch_shape_rejects_a_concrete_batch_axis(self):
        with self.assertRaisesRegex(ValueError, "symbolic"):
            EXPORT_RKNN.non_batch_shape([1, 3, 224, 224])


if __name__ == "__main__":
    unittest.main()

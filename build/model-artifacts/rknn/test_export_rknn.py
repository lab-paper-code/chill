import importlib.util
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("export_rknn.py")
SPEC = importlib.util.spec_from_file_location("export_rknn", MODULE_PATH)
EXPORT_RKNN = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EXPORT_RKNN)


class FakeDimension:
    def __init__(self):
        self.dim_param = "dynamic"
        self.dim_value = 0

    def ClearField(self, name):
        if name != "dim_param":
            raise AssertionError(name)
        self.dim_param = ""


class FakeTensorType:
    def __init__(self, has_shape=True):
        self.shape = type("Shape", (), {"dim": [FakeDimension()]})()
        self._has_shape = has_shape

    def HasField(self, name):
        return name == "shape" and self._has_shape


def fake_value_info(has_shape=True):
    tensor_type = FakeTensorType(has_shape)
    return type(
        "ValueInfo",
        (),
        {"type": type("Type", (), {"tensor_type": tensor_type})()},
    )()


class ExportRKNNTest(unittest.TestCase):
    def test_set_fixed_batch_updates_every_value_info_group(self):
        values = [fake_value_info(), fake_value_info(), fake_value_info()]
        model = type(
            "Model",
            (),
            {
                "graph": type(
                    "Graph",
                    (),
                    {
                        "input": [values[0]],
                        "output": [values[1]],
                        "value_info": [values[2]],
                    },
                )()
            },
        )()

        EXPORT_RKNN.set_fixed_batch(model, 2)

        for value in values:
            dimension = value.type.tensor_type.shape.dim[0]
            self.assertEqual(dimension.dim_value, 2)
            self.assertEqual(dimension.dim_param, "")

    def test_set_fixed_batch_rejects_non_positive_value(self):
        with self.assertRaisesRegex(ValueError, "positive"):
            EXPORT_RKNN.set_fixed_batch(type("Model", (), {})(), 0)


if __name__ == "__main__":
    unittest.main()

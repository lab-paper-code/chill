from __future__ import annotations

import unittest
from pathlib import Path
from unittest import mock

import runner


def metadata_library(
    *, name: bytes = b"input", datatype: int = 0, shape=(1, 3, 224, 224), count: int = 1, layout: int = 0
):
    library = mock.Mock()
    library.rknn_init.return_value = 0
    library.rknn_destroy.return_value = 0

    def query(_context, command, pointer, _size):
        value = pointer._obj
        if command == 0:
            value.n_input = count
        elif command == 1:
            value.name = name
            value.type = datatype
            value.fmt = layout
            value.n_dims = len(shape)
            value.dims[:len(shape)] = shape
        elif command == 13:
            value.name = name
            value.fmt = 0
            value.n_dims = 4
            value.shape_number = 3
            for index, batch in enumerate((1, 2, 4)):
                value.dyn_range[index][:4] = (batch, 3, 224, 224)
        else:
            raise AssertionError(f"unexpected metadata query {command}")
        return 0

    library.rknn_query.side_effect = query
    return library


def valid_candidate() -> dict[str, object]:
    return {
        "artifactCapability": {
            "targetPlatform": "rk3588",
            "declaredBatchSizes": [1, 2, 4],
            "tensors": [{"name": "input", "datatype": "FP32", "sampleShape": [224, 224, 3]}],
        },
        "runtimePackages": [{"name": "rknn-toolkit-lite2", "version": "2.3.2"}],
        "hostContract": {
            "libraries": [
                {
                    "name": "librknnrt",
                    "hostPath": "/usr/lib/librknnrt.so",
                    "containerPath": "/usr/lib/librknnrt.so",
                }
            ],
            "driver": {
                "name": "rknpu",
                "versionPath": "/sys/module/rknpu/version",
                "version": "0.9.6",
            },
            "devices": [{"path": "/dev/dri/card0"}, {"path": "/dev/dri/renderD128"}],
            "platform": {
                "compatiblePath": "/proc/device-tree/compatible",
                "compatibleToken": "rockchip,rk3588",
            },
            "allocation": {"resourceName": "", "exclusivityClaimed": False},
        },
        "runtimeAccess": {"privileged": True, "supplementalGroups": [44, 110]},
    }


class RunnerTest(unittest.TestCase):
    def test_candidate_accepts_current_contract(self) -> None:
        runner.validate_candidate(valid_candidate())

    def test_candidate_rejects_unsupported_inputs(self) -> None:
        for tensors in [
            [],
            [{"name": "input", "datatype": "INT64", "sampleShape": [224, 224, 3]}],
            [{"name": "input", "datatype": "FP32", "sampleShape": [224, 3]}],
            [{"name": name, "datatype": "FP32", "sampleShape": [224, 224, 3]} for name in ("one", "two")],
        ]:
            with self.subTest(tensors=tensors):
                candidate = valid_candidate()
                candidate["artifactCapability"]["tensors"] = tensors
                with self.assertRaisesRegex(runner.ContractError, "one FP32 NHWC rank3"):
                    runner.validate_candidate(candidate)

    def test_candidate_requires_positive_declared_batch_sizes(self) -> None:
        for batches, message in [
            ([], "^candidate declaredBatchSizes must be a non-empty array$"),
            ([1, 0, 4], "^candidate declaredBatchSizes item must be an integer >= 1$"),
        ]:
            with self.subTest(batches=batches):
                candidate = valid_candidate()
                candidate["artifactCapability"]["declaredBatchSizes"] = batches
                with self.assertRaisesRegex(runner.ContractError, message):
                    runner.validate_candidate(candidate)

    def test_metadata_mismatch_is_rejected(self) -> None:
        cases = [
            ("name", {"name": b"wrong"}),
            ("datatype", {"datatype": 2}),
            ("sampleShape", {"shape": (1, 3, 112, 224)}),
            ("rank4", {"shape": (1, 224, 3)}),
            ("exactly one", {"count": 2}),
            ("layout", {"layout": 2}),
        ]
        for message, metadata in cases:
            with self.subTest(mismatch=message):
                library = metadata_library(**metadata)
                with mock.patch.object(runner.ctypes, "CDLL", return_value=library):
                    with self.assertRaisesRegex(runner.ContractError, message):
                        runner.validate_runtime_inputs(Path("/model.rknn"), valid_candidate(), 1)
                library.rknn_destroy.assert_called_once()

    def test_metadata_accepts_float16_internal_input(self) -> None:
        library = metadata_library(datatype=1)
        with mock.patch.object(runner.ctypes, "CDLL", return_value=library):
            runner.validate_runtime_inputs(Path("/model.rknn"), valid_candidate(), 1)

    def test_metadata_validates_selected_dynamic_batch(self) -> None:
        candidate = valid_candidate()
        library = metadata_library()
        with mock.patch.object(runner.ctypes, "CDLL", return_value=library):
            runner.validate_runtime_inputs(Path("/model.rknn"), candidate, 4)
        self.assertEqual([call.args[1] for call in library.rknn_query.call_args_list], [0, 1, 13])
        library.rknn_destroy.assert_called_once()
        with self.assertRaisesRegex(runner.ContractError, "not declared"):
            runner.validate_runtime_inputs(Path("/model.rknn"), candidate, 3)
        candidate["artifactCapability"]["declaredBatchSizes"].append(8)
        with mock.patch.object(runner.ctypes, "CDLL", return_value=metadata_library()):
            with self.assertRaisesRegex(runner.ContractError, "does not provide the selected"):
                runner.validate_runtime_inputs(Path("/model.rknn"), candidate, 8)

    def test_metadata_query_failure_is_not_a_shape_fallback(self) -> None:
        library = metadata_library()
        library.rknn_query.side_effect = None
        library.rknn_query.return_value = -1
        with mock.patch.object(runner.ctypes, "CDLL", return_value=library):
            with self.assertRaisesRegex(runner.ContractError, "query RKNN input count returned -1"):
                runner.validate_runtime_inputs(Path("/model.rknn"), valid_candidate(), 1)
        library.rknn_destroy.assert_called_once()

    def test_observe_host_contract_rejects_runtime_package_version_drift(self) -> None:
        with mock.patch.object(runner.importlib.metadata, "version", return_value="2.3.1"):
            with self.assertRaisesRegex(runner.ContractError, "package version"):
                runner.observe_host_contract(valid_candidate(), 1)


if __name__ == "__main__":
    unittest.main()

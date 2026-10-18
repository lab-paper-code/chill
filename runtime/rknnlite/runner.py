"""RKNNLite delivery and host contract helpers shared by the firstfloor runner."""

from __future__ import annotations

import ctypes
import importlib.metadata
import os
import stat
from pathlib import Path
from typing import Any


SUPPORTED_RUNTIME_PACKAGE = "rknn-toolkit-lite2"
SUPPORTED_TARGET_PLATFORM = "rk3588"
SUPPORTED_INPUT_LAYOUT = "NHWC"
CORE_MASK_NAME = "NPU_CORE_0"


class ContractError(Exception):
    """The RKNNLite delivery or host contract is not satisfied."""


def require_fields(value: dict[str, Any], fields: set[str], name: str) -> None:
    actual = set(value)
    if actual != fields:
        raise ContractError(
            f"{name} fields mismatch: missing={sorted(fields - actual)} unknown={sorted(actual - fields)}"
        )


def require_object(value: Any, name: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ContractError(f"{name} must be an object")
    return value


def require_string(value: Any, name: str) -> str:
    if not isinstance(value, str) or not value or value.strip() != value:
        raise ContractError(
            f"{name} must be a non-empty string without surrounding whitespace"
        )
    return value


def require_integer(value: Any, name: str, minimum: int = 0) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < minimum:
        raise ContractError(f"{name} must be an integer >= {minimum}")
    return value


def require_boolean(value: Any, name: str) -> bool:
    if not isinstance(value, bool):
        raise ContractError(f"{name} must be a boolean")
    return value


def require_absolute_file_path(value: Any, name: str) -> Path:
    raw = require_string(value, name)
    path = Path(raw)
    if not path.is_absolute() or path == Path("/") or os.path.normpath(raw) != raw:
        raise ContractError(f"{name} must be a clean absolute file path")
    return path


def validate_candidate(value: Any) -> None:
    candidate = require_object(value, "candidate")
    require_fields(
        candidate,
        {"artifactCapability", "runtimePackages", "hostContract", "runtimeAccess"},
        "candidate",
    )
    capability = require_object(
        candidate["artifactCapability"], "candidate.artifactCapability"
    )
    require_fields(
        capability,
        {"targetPlatform", "declaredBatchSizes", "tensors"},
        "candidate.artifactCapability",
    )
    declared_batch_sizes = capability["declaredBatchSizes"]
    if not isinstance(declared_batch_sizes, list) or not declared_batch_sizes:
        raise ContractError("candidate declaredBatchSizes must be a non-empty array")
    for declared_batch_size in declared_batch_sizes:
        require_integer(declared_batch_size, "candidate declaredBatchSizes item", 1)
    if declared_batch_sizes != sorted(set(declared_batch_sizes)):
        raise ContractError("candidate declaredBatchSizes must be strictly increasing")
    tensors = capability["tensors"]
    if not isinstance(tensors, list) or len(tensors) != 1:
        raise ContractError("RKNNLite supports exactly one FP32 NHWC rank3 input tensor")
    tensor = require_object(tensors[0], "candidate tensor")
    require_fields(tensor, {"name", "datatype", "sampleShape"}, "candidate tensor")
    require_string(tensor["name"], "candidate tensor name")
    shape = tensor["sampleShape"]
    if tensor["datatype"] != "FP32" or not isinstance(shape, list) or len(shape) != 3:
        raise ContractError("RKNNLite supports exactly one FP32 NHWC rank3 input tensor")
    for value in shape:
        require_integer(value, "candidate tensor sampleShape item", 1)
        if value > 2147483647:
            raise ContractError("candidate tensor sampleShape item exceeds int32")
    if capability["targetPlatform"] != SUPPORTED_TARGET_PLATFORM:
        raise ContractError("candidate artifact capability is unsupported")

    packages = candidate["runtimePackages"]
    if not isinstance(packages, list) or len(packages) != 1:
        raise ContractError(
            "candidate must declare exactly one RKNNLite runtime package"
        )
    package = require_object(packages[0], "candidate.runtimePackages item")
    require_fields(package, {"name", "version"}, "candidate.runtimePackages item")
    if package["name"] != SUPPORTED_RUNTIME_PACKAGE:
        raise ContractError("candidate runtime package is unsupported")
    require_string(package["version"], "candidate runtime package version")

    host = require_object(candidate["hostContract"], "candidate.hostContract")
    require_fields(
        host,
        {"libraries", "driver", "devices", "platform", "allocation"},
        "candidate.hostContract",
    )
    libraries = host["libraries"]
    if not isinstance(libraries, list) or not libraries:
        raise ContractError("candidate host libraries must be a non-empty array")
    library_names: set[str] = set()
    for item in libraries:
        library = require_object(item, "candidate host library")
        require_fields(
            library, {"name", "hostPath", "containerPath"}, "candidate host library"
        )
        name = require_string(library["name"], "candidate host library name")
        if name in library_names:
            raise ContractError("candidate host library names must be unique")
        library_names.add(name)
        require_absolute_file_path(
            library["hostPath"], "candidate host library hostPath"
        )
        require_absolute_file_path(
            library["containerPath"], "candidate host library containerPath"
        )

    driver = require_object(host["driver"], "candidate host driver")
    require_fields(driver, {"name", "versionPath", "version"}, "candidate host driver")
    require_string(driver["name"], "candidate host driver name")
    require_absolute_file_path(
        driver["versionPath"], "candidate host driver versionPath"
    )
    require_string(driver["version"], "candidate host driver version")
    platform_contract = require_object(host["platform"], "candidate host platform")
    require_fields(
        platform_contract,
        {"compatiblePath", "compatibleToken"},
        "candidate host platform",
    )
    require_absolute_file_path(
        platform_contract["compatiblePath"], "candidate host platform compatiblePath"
    )
    require_string(
        platform_contract["compatibleToken"], "candidate host platform compatibleToken"
    )
    devices = host["devices"]
    if not isinstance(devices, list) or not devices:
        raise ContractError("candidate host devices must be a non-empty array")
    device_paths = [
        str(
            require_absolute_file_path(
                require_object(item, "candidate host device").get("path"),
                "candidate host device path",
            )
        )
        for item in devices
    ]
    for item in devices:
        require_fields(
            require_object(item, "candidate host device"),
            {"path"},
            "candidate host device",
        )
    if len(set(device_paths)) != len(device_paths):
        raise ContractError("candidate host device paths must be unique")
    allocation = require_object(host["allocation"], "candidate host allocation")
    require_fields(
        allocation, {"resourceName", "exclusivityClaimed"}, "candidate host allocation"
    )
    if (
        not isinstance(allocation["resourceName"], str)
        or allocation["resourceName"].strip() != allocation["resourceName"]
    ):
        raise ContractError("candidate host allocation resourceName is invalid")
    require_boolean(
        allocation["exclusivityClaimed"], "candidate host allocation exclusivityClaimed"
    )

    access = require_object(candidate["runtimeAccess"], "candidate.runtimeAccess")
    require_fields(
        access, {"privileged", "supplementalGroups"}, "candidate.runtimeAccess"
    )
    require_boolean(access["privileged"], "candidate.runtimeAccess.privileged")
    groups = access["supplementalGroups"]
    if not isinstance(groups, list):
        raise ContractError("candidate supplementalGroups must be an array")
    normalized_groups = [
        require_integer(group, "candidate supplemental group", 1) for group in groups
    ]
    if len(set(normalized_groups)) != len(normalized_groups):
        raise ContractError("candidate supplementalGroups must be unique")


# ABI from Rockchip rknpu2/runtime/Linux/librknn_api/include/rknn_api.h.
# Lite2 2.3.2 has no public tensor query method; use the delivered host library.
class RKNNInputOutputNum(ctypes.Structure):
    _fields_ = [("n_input", ctypes.c_uint32), ("n_output", ctypes.c_uint32)]


class RKNNTensorAttr(ctypes.Structure):
    _fields_ = [
        ("index", ctypes.c_uint32), ("n_dims", ctypes.c_uint32),
        ("dims", ctypes.c_uint32 * 16), ("name", ctypes.c_char * 256),
        ("n_elems", ctypes.c_uint32), ("size", ctypes.c_uint32),
        ("fmt", ctypes.c_int), ("type", ctypes.c_int), ("qnt_type", ctypes.c_int),
        ("fl", ctypes.c_int8), ("zp", ctypes.c_int32), ("scale", ctypes.c_float),
        ("w_stride", ctypes.c_uint32), ("size_with_stride", ctypes.c_uint32),
        ("pass_through", ctypes.c_uint8), ("h_stride", ctypes.c_uint32),
    ]


class RKNNInputRange(ctypes.Structure):
    _fields_ = [
        ("index", ctypes.c_uint32), ("shape_number", ctypes.c_uint32),
        ("fmt", ctypes.c_int), ("name", ctypes.c_char * 256),
        ("dyn_range", (ctypes.c_uint32 * 16) * 512), ("n_dims", ctypes.c_uint32),
    ]


def nhwc_shape(dimensions: list[int], layout: int) -> list[int]:
    if len(dimensions) != 4 or any(value < 1 for value in dimensions):
        raise ContractError("RKNN artifact input must have positive rank4 metadata")
    if layout == 0:  # RKNN_TENSOR_NCHW
        batch, channels, height, width = dimensions
        return [batch, height, width, channels]
    if layout == 1:  # RKNN_TENSOR_NHWC
        return dimensions
    raise ContractError("RKNN artifact input has unsupported layout metadata")


def validate_runtime_inputs(
    model_path: Path, candidate: dict[str, Any], batch: int
) -> None:
    capability = candidate["artifactCapability"]
    if batch not in capability["declaredBatchSizes"]:
        raise ContractError("selected batch is not declared by the RKNN artifact")
    libraries = [
        item["containerPath"] for item in candidate["hostContract"]["libraries"]
        if item["name"] == "librknnrt"
    ]
    if len(libraries) != 1:
        raise ContractError("RKNN metadata requires the delivered librknnrt library")
    context = ctypes.c_uint64()
    try:
        library = ctypes.CDLL(libraries[0])
        library.rknn_init.argtypes = [
            ctypes.POINTER(ctypes.c_uint64), ctypes.c_void_p, ctypes.c_uint32,
            ctypes.c_uint32, ctypes.c_void_p,
        ]
        library.rknn_init.restype = ctypes.c_int
        library.rknn_query.argtypes = [
            ctypes.c_uint64, ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32,
        ]
        library.rknn_query.restype = ctypes.c_int
        library.rknn_destroy.argtypes = [ctypes.c_uint64]
        library.rknn_destroy.restype = ctypes.c_int
        # size=0 loads by filename without another Python copy of artifact bytes.
        result = library.rknn_init(
            ctypes.byref(context), ctypes.c_char_p(os.fsencode(model_path)), 0, 0, None
        )
        if result != 0:
            raise ContractError(f"initialize RKNN metadata context returned {result}")
        try:
            count = RKNNInputOutputNum()
            result = library.rknn_query(context, 0, ctypes.byref(count), ctypes.sizeof(count))
            if result != 0:
                raise ContractError(f"query RKNN input count returned {result}")
            if count.n_input != 1:
                raise ContractError("RKNN artifact does not have exactly one input")
            attribute = RKNNTensorAttr(index=0)
            result = library.rknn_query(
                context, 1, ctypes.byref(attribute), ctypes.sizeof(attribute)
            )
            if result != 0:
                raise ContractError(f"query RKNN input metadata returned {result}")
            tensor = capability["tensors"][0]
            if attribute.name.decode("utf-8") != tensor["name"]:
                raise ContractError("RKNN runtime input name does not match declaration")
            # A float artifact built without quantization reports RKNN_TENSOR_FLOAT16
            # (1); with pass_through off the runtime converts the FP32 input.
            if attribute.type not in (0, 1) or attribute.pass_through:  # RKNN_TENSOR_FLOAT32/FLOAT16
                raise ContractError("RKNN runtime input datatype does not match FP32 declaration")
            shape = nhwc_shape(list(attribute.dims[:attribute.n_dims]), attribute.fmt)
            expected = [batch, *tensor["sampleShape"]]
            if shape[1:] != expected[1:]:
                raise ContractError("RKNN runtime input sampleShape does not match declaration")
            if shape != expected:
                profiles = RKNNInputRange(index=0)
                result = library.rknn_query(
                    context, 13, ctypes.byref(profiles), ctypes.sizeof(profiles)
                )
                if result != 0:
                    raise ContractError(f"query RKNN declared batch metadata returned {result}")
                if not 1 <= profiles.shape_number <= 512 or profiles.n_dims != 4:
                    raise ContractError("RKNN runtime has invalid dynamic input metadata")
                if profiles.name.decode("utf-8") != tensor["name"]:
                    raise ContractError("RKNN dynamic input name does not match declaration")
                shapes = [
                    nhwc_shape(list(row[:profiles.n_dims]), profiles.fmt)
                    for row in profiles.dyn_range[:profiles.shape_number]
                ]
                if expected not in shapes:
                    raise ContractError("RKNN runtime does not provide the selected declared batch")
        finally:
            result = library.rknn_destroy(context)
            if result != 0:
                raise ContractError(f"destroy RKNN metadata context returned {result}")
    except (OSError, AttributeError, UnicodeError) as error:
        raise ContractError(f"read RKNN runtime input metadata: {error}") from error


def validate_regular_file(path: Path, name: str = "file") -> None:
    try:
        info = path.lstat()
    except OSError as error:
        raise ContractError(f"inspect {name}: {error}") from error
    if not stat.S_ISREG(info.st_mode):
        raise ContractError(f"{name} is not a regular file")


def read_required_text(path: Path, name: str) -> str:
    try:
        value = path.read_text(encoding="utf-8").strip()
    except OSError as error:
        raise ContractError(f"read {name}: {error}") from error
    if not value:
        raise ContractError(f"{name} is empty")
    return value


def observe_host_contract(
    candidate: dict[str, Any], selected_batch: int
) -> dict[str, Any]:
    host = candidate["hostContract"]
    package = candidate["runtimePackages"][0]
    try:
        package_version = importlib.metadata.version(package["name"])
    except importlib.metadata.PackageNotFoundError as error:
        raise ContractError(
            f"runtime package {package['name']} is not installed"
        ) from error
    if package_version != package["version"]:
        raise ContractError(
            "installed RKNNLite package version does not match the candidate"
        )

    libraries: list[dict[str, Any]] = []
    for library in host["libraries"]:
        path = Path(library["containerPath"])
        validate_regular_file(path, f"host library {library['name']}")
        libraries.append({"name": library["name"], "path": str(path)})

    driver = host["driver"]
    driver_version = read_required_text(
        Path(driver["versionPath"]), "RKNN driver version"
    )
    if driver_version != driver["version"]:
        raise ContractError("RKNN driver version does not match the candidate")

    platform_contract = host["platform"]
    compatible_path = Path(platform_contract["compatiblePath"])
    try:
        compatible = compatible_path.read_bytes()
    except OSError as error:
        raise ContractError(f"read platform compatible bytes: {error}") from error
    token = platform_contract["compatibleToken"].encode("utf-8")
    matched = token in compatible.split(b"\x00")
    if not matched:
        raise ContractError("platform compatible token does not match the candidate")

    devices: list[dict[str, Any]] = []
    for device in host["devices"]:
        path = Path(device["path"])
        try:
            info = path.lstat()
        except OSError as error:
            raise ContractError(f"inspect RKNN device {path}: {error}") from error
        character_device = stat.S_ISCHR(info.st_mode)
        if not character_device:
            raise ContractError(f"RKNN device {path} is not a character device")
        descriptor: int | None = None
        try:
            descriptor = os.open(path, os.O_RDWR | os.O_NONBLOCK)
        except OSError as error:
            raise ContractError(f"open RKNN device {path}: {error}") from error
        finally:
            if descriptor is not None:
                os.close(descriptor)
        devices.append({"path": str(path), "characterDevice": True, "opened": True})

    return {
        "package": {"name": package["name"], "version": package_version},
        "hostLibraries": libraries,
        "driver": {
            "name": driver["name"],
            "versionPath": driver["versionPath"],
            "version": driver_version,
        },
        "platform": {
            "compatiblePath": platform_contract["compatiblePath"],
            "compatibleToken": platform_contract["compatibleToken"],
            "matched": matched,
        },
        "devices": devices,
        "coreMask": CORE_MASK_NAME,
        "inputLayout": SUPPORTED_INPUT_LAYOUT,
        "initSucceeded": False,
        "loadSucceeded": False,
        "selectedBatch": selected_batch,
    }

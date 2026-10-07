"""Build one declared-batch-size RKNN artifact and semantic conversion record."""

from __future__ import annotations

import argparse
import importlib.metadata
import json
import os
import platform
import tempfile
from pathlib import Path


RECORD_SCHEMA = "chill.dacs.io/rknn-conversion-record.v1alpha4"


def input_sample_shape(shape):
    if not shape or len(shape) != 4:
        raise ValueError("RKNN export supports one FP32 NCHW rank4 ONNX input")
    if isinstance(shape[0], int):
        raise ValueError("input batch axis must be symbolic, not a concrete size")
    for dimension in shape[1:]:
        if not isinstance(dimension, int) or dimension < 1:
            raise ValueError("non-batch dimensions must be positive integers")
    return list(shape[1:])


def model_input_contract(model):
    initializer_names = {value.name for value in model.graph.initializer}
    inputs = [value for value in model.graph.input if value.name not in initializer_names]
    if len(inputs) != 1:
        raise ValueError("RKNN export supports exactly one ONNX input")
    value = inputs[0]
    if not value.name or value.name.strip() != value.name:
        raise ValueError("ONNX input must have a non-empty tensor name")
    tensor_type = value.type.tensor_type
    if tensor_type.elem_type != 1:  # onnx.TensorProto.FLOAT
        raise ValueError("RKNN export supports only FP32 ONNX inputs")
    sample = input_sample_shape([
        dimension.dim_param or dimension.dim_value for dimension in tensor_type.shape.dim
    ])
    channels, height, width = sample
    return sample, {
        "name": value.name,
        "datatype": "FP32",
        "sampleShape": [height, width, channels],
    }


def build_dynamic_input(input_sample, declared_batch_sizes):
    if not declared_batch_sizes:
        raise ValueError("at least one declared batch size is required")
    for declared_batch_size in declared_batch_sizes:
        if declared_batch_size <= 0:
            raise ValueError("declared batch sizes must be positive")
    return [
        [[declared_batch_size, *input_sample]]
        for declared_batch_size in sorted(set(declared_batch_sizes))
    ]


def parse_args():
    parser = argparse.ArgumentParser(
        description="Build a declared-batch-size RKNN artifact from a selected ONNX model"
    )
    parser.add_argument("--source-onnx", required=True)
    parser.add_argument("--tool-wheel", required=True)
    parser.add_argument("--tool-version", required=True)
    parser.add_argument("--target-platform", required=True)
    parser.add_argument(
        "--declared-batch-size",
        dest="declared_batch_sizes",
        required=True,
        action="append",
        type=int,
    )
    parser.add_argument("--output", required=True)
    parser.add_argument("--record", required=True)
    return parser.parse_args()


def main():
    args = parse_args()
    source = Path(args.source_onnx).resolve()
    wheel = Path(args.tool_wheel).resolve()
    output = Path(args.output).resolve()
    record = Path(args.record).resolve()

    if output.exists() or record.exists():
        raise SystemExit("refusing to overwrite an existing artifact or record")
    if not source.is_file() or not wheel.is_file():
        raise SystemExit("source ONNX and RKNN Toolkit2 wheel must be regular files")
    try:
        import onnx
        from rknn.api import RKNN
    except ImportError as exc:
        raise SystemExit(
            "the selected interpreter must provide onnx and rknn-toolkit2"
        ) from exc

    installed_version = importlib.metadata.version("rknn-toolkit2")
    if installed_version != args.tool_version:
        raise SystemExit(
            "rknn-toolkit2 version mismatch: "
            f"expected {args.tool_version}, observed {installed_version}"
        )

    output.parent.mkdir(parents=True, exist_ok=True)
    record.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="chill-rknn-export-") as directory:
        temporary_output = Path(directory) / "artifact.rknn"
        model = onnx.load(str(source))
        input_sample, input_tensor = model_input_contract(model)
        dynamic_input = build_dynamic_input(input_sample, args.declared_batch_sizes)

        converter = RKNN(verbose=False)
        try:
            if converter.config(
                target_platform=args.target_platform, dynamic_input=dynamic_input
            ) != 0:
                raise SystemExit("RKNN config failed")
            if converter.load_onnx(model=str(source)) != 0:
                raise SystemExit("RKNN ONNX load failed")
            if converter.build(do_quantization=False) != 0:
                raise SystemExit("RKNN build failed")
            if converter.export_rknn(str(temporary_output)) != 0:
                raise SystemExit("RKNN export failed")
        finally:
            converter.release()

        if not temporary_output.is_file() or temporary_output.stat().st_size == 0:
            raise SystemExit("RKNN converter produced no artifact bytes")
        os.replace(temporary_output, output)

    conversion_record = {
        "schemaVersion": RECORD_SCHEMA,
        "source": {
            "name": source.name,
        },
        "conversion": {
            "tool": "rknn-toolkit2",
            "version": installed_version,
            "toolArtifact": wheel.name,
            "recipe": Path(__file__).name,
            "pythonVersion": platform.python_version(),
            "onnxVersion": onnx.__version__,
            "targetPlatform": args.target_platform,
            "declaredBatchSizes": sorted(set(args.declared_batch_sizes)),
            "tensors": [input_tensor],
            "quantized": False,
        },
        "output": {
            "name": output.name,
            "sizeBytes": output.stat().st_size,
        },
    }
    record.write_text(
        json.dumps(conversion_record, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    print(output)
    print(record)


if __name__ == "__main__":
    main()

"""Build one batch-fixed RKNN artifact and semantic conversion record."""

from __future__ import annotations

import argparse
import importlib.metadata
import json
import os
import platform
import tempfile
from pathlib import Path


RECORD_SCHEMA = "chill.dacs.io/rknn-conversion-record.v1alpha2"


def set_fixed_batch(model, batch_size):
    if batch_size <= 0:
        raise ValueError("batch size must be positive")
    value_infos = (
        list(model.graph.input)
        + list(model.graph.output)
        + list(model.graph.value_info)
    )
    for value_info in value_infos:
        tensor_type = value_info.type.tensor_type
        if tensor_type.HasField("shape") and tensor_type.shape.dim:
            first_dimension = tensor_type.shape.dim[0]
            first_dimension.ClearField("dim_param")
            first_dimension.dim_value = batch_size


def parse_args():
    parser = argparse.ArgumentParser(
        description="Build a batch-fixed RKNN artifact from a selected ONNX model"
    )
    parser.add_argument("--source-onnx", required=True)
    parser.add_argument("--tool-wheel", required=True)
    parser.add_argument("--tool-version", required=True)
    parser.add_argument("--target-platform", required=True)
    parser.add_argument("--batch-size", required=True, type=int)
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
    if args.batch_size <= 0:
        raise SystemExit("batch size must be positive")

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
        fixed_onnx = Path(directory) / "batch-fixed.onnx"
        temporary_output = Path(directory) / "artifact.rknn"
        model = onnx.load(str(source))
        set_fixed_batch(model, args.batch_size)
        onnx.save(model, str(fixed_onnx))

        converter = RKNN(verbose=False)
        try:
            if converter.config(target_platform=args.target_platform) != 0:
                raise SystemExit("RKNN config failed")
            if converter.load_onnx(model=str(fixed_onnx)) != 0:
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
            "fixedBatch": args.batch_size,
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

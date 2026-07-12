#!/usr/bin/env python3
"""Build one RK3588 RKNN artifact supporting an explicit batch set."""

# TODO(internal): Artifact conversion belongs to an artifact build/import
# pipeline, not the runtime profiler Pod. Record source digest, toolkit version,
# target platform, shape capability, output digest, and build logs in ModelArtifact.

import argparse
import tempfile
from pathlib import Path

import onnx
from rknn.api import RKNN


def fixed_batch_model(source, batch):
    model = onnx.load(source)
    for value in list(model.graph.input) + list(model.graph.output) + list(model.graph.value_info):
        tensor = value.type.tensor_type
        if tensor.HasField("shape") and tensor.shape.dim:
            tensor.shape.dim[0].ClearField("dim_param")
            tensor.shape.dim[0].dim_value = batch
    temporary = tempfile.NamedTemporaryFile(suffix=f"-bs{batch}.onnx", delete=False)
    temporary.close()
    onnx.save(model, temporary.name)
    return Path(temporary.name)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--batches", default="1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16")
    parser.add_argument("--mode", choices=("dynamic", "static"), default="dynamic")
    args = parser.parse_args()
    batches = [int(value) for value in args.batches.split(",")]
    if args.mode == "static" and len(batches) != 1:
        raise SystemExit("static mode requires exactly one batch")
    dynamic_input = [[[batch, 3, 224, 224]] for batch in batches]

    rknn = RKNN(verbose=True)
    config = {"target_platform": "rk3588"}
    if args.mode == "dynamic":
        config["dynamic_input"] = dynamic_input
    ret = rknn.config(**config)
    if ret != 0:
        raise SystemExit(f"config failed: {ret}")
    model_path = Path(args.model)
    temporary = None
    if args.mode == "static":
        temporary = fixed_batch_model(str(model_path), batches[0])
        model_path = temporary
    ret = rknn.load_onnx(model=str(model_path))
    if ret != 0:
        raise SystemExit(f"load_onnx failed: {ret}")
    ret = rknn.build(do_quantization=False)
    if ret != 0:
        raise SystemExit(f"build failed: {ret}")
    ret = rknn.export_rknn(args.output)
    if ret != 0:
        raise SystemExit(f"export failed: {ret}")
    rknn.release()
    if temporary:
        temporary.unlink(missing_ok=True)
    print(f"exported {args.output} mode={args.mode} batches={batches}")


if __name__ == "__main__":
    main()

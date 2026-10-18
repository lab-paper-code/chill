"""ImageNet classification module for mobilenet-v2-050-rk3588 RKNN (one NHWC FP32 input).

Pixels follow timm mobilenetv2_050.lamb_in1k pretrained_cfg: bicubic resize of
the short side to 256 (crop_pct 0.875), 224 center crop, ImageNet mean/std.
The RKNN conversion sets no mean/std, so normalization stays here.
"""

import io

import numpy as np
from PIL import Image

MEAN = np.array([0.485, 0.456, 0.406], np.float32)
STD = np.array([0.229, 0.224, 0.225], np.float32)


def _warmup_jpeg():
    buffer = io.BytesIO()
    Image.new("RGB", (320, 240), (124, 116, 104)).save(buffer, "JPEG")
    return buffer.getvalue()


WARMUP_SAMPLE = {"image": _warmup_jpeg()}


def pixels(data):
    with Image.open(io.BytesIO(data)) as image:
        image = image.convert("RGB")
    width, height = image.size
    size = (256, height * 256 // width) if width <= height else (width * 256 // height, 256)
    left, top = round((size[0] - 224) / 2), round((size[1] - 224) / 2)
    image = image.resize(size, Image.BICUBIC).crop((left, top, left + 224, top + 224))
    return (np.asarray(image, np.float32) / 255 - MEAN) / STD


def preprocess(samples):
    arrays, report = [], []
    for sample in samples:
        try:
            arrays.append(pixels(sample["image"]))
        except Exception as error:
            report.append(f"{type(error).__name__}: {error}")
            continue
        report.append(1)
    if not arrays:
        return None, report
    # RKNN runs only its compiled batch shapes, so failed items are zero-padded
    # back to the requested batch and their logits dropped after inference.
    batch = np.zeros((len(samples), 224, 224, 3), np.float32)
    batch[:len(arrays)] = arrays
    return (batch, len(arrays)), report


def infer(sessions, inputs, max_new_tokens, first_token):
    (rknn,) = sessions.values()
    batch, count = inputs
    return rknn.inference(inputs=[batch], data_format=["nhwc"])[0][:count]


def postprocess(logits):
    return [{"label": str(int(index)).encode()} for index in logits.argmax(axis=1)]

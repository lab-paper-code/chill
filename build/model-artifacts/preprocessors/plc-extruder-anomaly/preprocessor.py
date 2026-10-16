"""PLC_EXTRUDER_01 window anomaly scoring module for the firstfloor runner.

The carrier directory also holds stats.json, written by
build/model-artifacts/plc-anomaly/train_export.py: feature order, window length,
train-split mean and std, and the anomaly threshold. ONNX I/O of plc-lstm-ae:

  plc-lstm-ae  records f32 [b, 60, 10] -> score f32 [b]
"""

import json
from pathlib import Path

import numpy as np

STATS = json.loads((Path(__file__).resolve().parent / "stats.json").read_text())
FEATURES, WINDOW = STATS["features"], STATS["window"]
MEAN, STD = np.array(STATS["mean"], np.float32), np.array(STATS["std"], np.float32)
THRESHOLD = STATS["threshold"]

WARMUP_SAMPLE = {"records": json.dumps([{key: 0.0 for key in FEATURES}] * WINDOW).encode("utf-8")}


def _window(data):
    records = json.loads(data.decode("utf-8"))
    if not isinstance(records, list) or len(records) != WINDOW:
        raise ValueError(f"records must be a JSON array of {WINDOW} objects")
    rows = []
    for index, record in enumerate(records):
        values = [record.get(key) if isinstance(record, dict) else None for key in FEATURES]
        if not all(isinstance(v, (int, float)) and not isinstance(v, bool) for v in values):
            raise ValueError(f"record {index} lacks numeric {', '.join(FEATURES)}")
        rows.append(values)
    window = np.array(rows, np.float32)
    if not np.isfinite(window).all():
        raise ValueError("records hold values outside the finite float32 range")
    return window


def preprocess(samples):
    windows, report = [], []
    for sample in samples:
        try:
            windows.append(_window(sample["records"]))
        except Exception as error:
            report.append(f"{type(error).__name__}: {error}")
            continue
        report.append(1)
    if not windows:
        return None, report
    return (np.stack(windows) - MEAN) / STD, report


def infer(sessions, inputs, max_new_tokens, first_token):
    return sessions["plc-lstm-ae"].run(None, {"records": inputs})[0]


def postprocess(scores):
    return [{"score": repr(float(score)).encode("utf-8"), "anomaly": b"true" if score > THRESHOLD else b"false"}
            for score in scores]

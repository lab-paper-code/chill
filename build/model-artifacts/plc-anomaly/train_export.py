"""Train the PLC_EXTRUDER_01 LSTM autoencoder and stage its carriers.

Uses the train and validation windows of build_windows.py. Writes
<model-dir>/plc-lstm-ae.onnx, whose graph maps records f32 [batch, 60, 10]
(z-scored FEATURES) to score f32 [batch], the per-window reconstruction MSE,
and <preprocessor-dir>/stats.json with the feature order, the train-split
mean and std, and the threshold: the THRESHOLD_PERCENTILE of validation scores.
Both use only normal windows, without the sensor-fault sentinel.
Needs torch, numpy, onnx, onnxruntime and pyarrow; trains on CPU.
"""

import argparse
import importlib.util
import json
from pathlib import Path

import numpy as np
import onnxruntime as ort
import torch
from torch import nn

SPEC = importlib.util.spec_from_file_location("build_windows", Path(__file__).with_name("build_windows.py"))
BUILD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BUILD)

SEED, HIDDEN, EPOCHS, BATCH, LR = 0, 32, 30, 256, 1e-3
THRESHOLD_PERCENTILE = 99
# Sensor-fault sentinel: train and threshold use only windows without it.
FAULT_FEATURES, FAULT_VALUE = ("BILLET_TEMP_NO1", "BILLET_TEMP_NO2", "BILLET_TEMP_NO3"), 1000
# Opset 16 is the newest ONNX Runtime 1.11 loads, the last CUDA build for JetPack 4.
OPSET = 16


class Scorer(nn.Module):
    """LSTM autoencoder whose output is the per-window reconstruction MSE."""

    def __init__(self, features):
        super().__init__()
        self.encoder = nn.LSTM(features, HIDDEN, batch_first=True)
        self.decoder = nn.LSTM(HIDDEN, HIDDEN, batch_first=True)
        self.output = nn.Linear(HIDDEN, features)

    def reconstruct(self, x):
        _, (latent, _) = self.encoder(x)
        repeated = latent[-1].unsqueeze(1).expand(-1, x.shape[1], -1)
        return self.output(self.decoder(repeated)[0])

    def forward(self, records):
        return ((self.reconstruct(records) - records) ** 2).mean(dim=(1, 2))


def normal(window):
    return all(json.loads(raw)[key] < FAULT_VALUE for _, raw in window for key in FAULT_FEATURES)


def features(group):
    return np.array([[[json.loads(raw)[key] for key in BUILD.FEATURES] for _, raw in window] for window in group],
                    np.float32)


def scores(model, x):
    with torch.no_grad():
        return torch.cat([model(torch.from_numpy(x[i:i + 1024])) for i in range(0, len(x), 1024)]).numpy()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", default=BUILD.SOURCE)
    parser.add_argument("--model-dir", type=Path, required=True)
    parser.add_argument("--preprocessor-dir", type=Path, required=True)
    args = parser.parse_args()
    torch.manual_seed(SEED)
    torch.use_deterministic_algorithms(True)

    groups = BUILD.split(BUILD.windows(BUILD.load(args.source)))
    train = features([window for window in groups["train"] if normal(window)])
    validation = features([window for window in groups["validation"] if normal(window)])
    mean = train.reshape(-1, train.shape[-1]).mean(axis=0)
    std = train.reshape(-1, train.shape[-1]).std(axis=0)
    std[std == 0] = 1
    train, validation = (train - mean) / std, (validation - mean) / std

    model = Scorer(len(BUILD.FEATURES))
    optimizer = torch.optim.Adam(model.parameters(), lr=LR)
    data = torch.from_numpy(train)
    generator = torch.Generator().manual_seed(SEED)
    for epoch in range(EPOCHS):
        model.train()
        total = 0.0
        for index in torch.randperm(len(data), generator=generator).split(BATCH):
            loss = model(data[index]).mean()
            optimizer.zero_grad()
            loss.backward()
            optimizer.step()
            total += loss.item() * len(index)
        print(f"epoch {epoch + 1}: train loss {total / len(data):.6f}")
    model.eval()
    validation_scores = scores(model, validation)
    threshold = float(np.percentile(validation_scores, THRESHOLD_PERCENTILE))

    args.model_dir.mkdir(parents=True, exist_ok=True)
    args.preprocessor_dir.mkdir(parents=True, exist_ok=True)
    path = args.model_dir / "plc-lstm-ae.onnx"
    torch.onnx.export(model, (torch.from_numpy(validation[:2]),), path, input_names=["records"],
                      output_names=["score"], dynamic_axes={"records": {0: "batch"}, "score": {0: "batch"}},
                      opset_version=OPSET, dynamo=False)
    session = ort.InferenceSession(str(path), providers=["CPUExecutionProvider"])
    for batch in (1, 32, 1000):
        np.testing.assert_allclose(session.run(None, {"records": validation[:batch]})[0], validation_scores[:batch],
                                   rtol=1e-4, atol=1e-6)
    (args.preprocessor_dir / "stats.json").write_text(json.dumps({
        "features": BUILD.FEATURES, "window": BUILD.WINDOW, "mean": mean.tolist(), "std": std.tolist(),
        "threshold": threshold}, indent=2) + "\n")
    print(json.dumps({"windows": {name: len(group) for name, group in groups.items()},
                      "normal_windows": {"train": len(train), "validation": len(validation)},
                      "train_loss": total / len(data), "threshold": threshold}))


if __name__ == "__main__":
    main()

"""Build the PLC_EXTRUDER_01 window Dataset carrier directory.

Reads the edge_poc2 collection zip, keeps port_type 9006 (PLC_EXTRUDER_01),
orders records by measurement_time and cuts non-overlapping windows of WINDOW
consecutive records that contain no gap above MAX_GAP_SECONDS. Windows are split
by measurement date into disjoint train, validation, profiling and evaluation
ranges. Writes <output>/profiling/<recordID>/records and
<output>/evaluation/<recordID>/records: a UTF-8 JSON array of the window's raw
records exactly as collected. recordID is the first record's time, YYYYMMDDTHHMMSSfff.
"""

import argparse
import json
import random
from datetime import datetime
from pathlib import Path

SOURCE = "/mnt/dacslab/ETRI/[26.10.14]PLC_Sensor_Data/edge_poc2.zip"
PORT_TYPE = 9006
FEATURES = ["SPEED", "EXTRUSION_PRESSURE", "BILLET_TEMP_NO1", "BILLET_TEMP_NO2", "BILLET_TEMP_NO3",
            "BILLET_TEMP_NO4", "CONTAINER_TEMP_HIGH", "CONTAINER_TEMP_LOW", "BILLET_LEN", "BUTT_LEN"]
WINDOW, MAX_GAP_SECONDS = 60, 10
# Inclusive measurement-date ranges; train and validation stay out of the carrier.
SPLITS = {"train": ("2026-07-28", "2026-08-13"), "validation": ("2026-08-14", "2026-08-16"),
          "profiling": ("2026-08-17", "2026-08-18"), "evaluation": ("2026-08-19", "2026-08-20")}
SEED = 0
SIZES = {"profiling": 256, "evaluation": 1024}
# Field bound declared by config/samples/catalog/edge_v1alpha1_dataset_plc.yaml.
MAX_RECORDS_BYTES = 65536


def numeric(raw):
    record = json.loads(raw)
    return all(isinstance(record.get(key), (int, float)) and not isinstance(record[key], bool) for key in FEATURES)


def windows(records):
    """Cut (measurement_time, raw_json) records into gap-free non-overlapping windows.

    A gap above MAX_GAP_SECONDS or a record without the numeric features restarts
    the window, so every window holds WINDOW usable consecutive records.
    """
    out, current, previous = [], [], None
    for time, raw in sorted(records):
        stamp = datetime.fromisoformat(time)
        if previous is not None and (stamp - previous).total_seconds() > MAX_GAP_SECONDS:
            current = []
        previous = stamp
        if not numeric(raw):
            current = []
            continue
        current.append((time, raw))
        if len(current) == WINDOW:
            out.append(current)
            current = []
    return out


def split(all_windows):
    """Group windows whose first and last records fall in the same date range."""
    groups = {name: [] for name in SPLITS}
    for window in all_windows:
        first, last = window[0][0][:10], window[-1][0][:10]
        for name, (start, end) in SPLITS.items():
            if start <= first and last <= end:
                groups[name].append(window)
    return groups


def sample(groups):
    rng = random.Random(SEED)
    return {name: sorted(rng.sample(groups[name], size)) for name, size in SIZES.items()}


def record_id(window):
    return datetime.fromisoformat(window[0][0]).strftime("%Y%m%dT%H%M%S%f")[:-3]


def body(window):
    return ("[" + ",".join(raw for _, raw in window) + "]").encode("utf-8")


def write(output, subsets):
    for subset, chosen in subsets.items():
        for window in chosen:
            data = body(window)
            if len(data) > MAX_RECORDS_BYTES:
                raise ValueError(f"window {record_id(window)}: records exceed {MAX_RECORDS_BYTES} bytes")
            record = output / subset / record_id(window)
            record.mkdir(parents=True)
            (record / "records").write_bytes(data)


def load(source=SOURCE):
    """Return (measurement_time, raw_json) of every PORT_TYPE row in the zip."""
    import io
    import zipfile

    import pyarrow.compute as pc
    import pyarrow.parquet as pq

    records = []
    with zipfile.ZipFile(source) as archive:
        for name in sorted(n for n in archive.namelist() if n.endswith(".parquet")):
            table = pq.read_table(io.BytesIO(archive.read(name)), columns=["measurement_time", "port_type", "raw_json"])
            table = table.filter(pc.equal(table["port_type"], PORT_TYPE))
            records += zip(table["measurement_time"].to_pylist(), table["raw_json"].to_pylist())
    return records


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", default=SOURCE)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    groups = split(windows(load(args.source)))
    print({name: len(group) for name, group in groups.items()})
    write(args.output, sample(groups))


if __name__ == "__main__":
    main()

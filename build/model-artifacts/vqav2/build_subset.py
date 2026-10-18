"""Build the VQAv2 validation subset Dataset carrier directory.

Writes <output>/profiling/<question_id>/{image,question} and
<output>/evaluation/<question_id>/{image,question}: JPEG bytes and UTF-8 text.
"""

import argparse
import random
from pathlib import Path

REPO = "lmms-lab-encoder/VQAv2"
REVISION = "32665d35052eb4a6d4414851c3c829a72754915a"
SHARD = "data/validation-00000-of-00068.parquet"
SEED = 0
SIZES = {"profiling": 64, "evaluation": 256}
# Field bounds declared by config/samples/catalog/edge_v1alpha1_dataset.yaml.
MAX_IMAGE_BYTES, MAX_QUESTION_BYTES = 1048576, 512


def split(question_ids):
    """Draw disjoint subsets of distinct question IDs with the fixed seed."""
    chosen = iter(random.Random(SEED).sample(sorted(set(question_ids)), sum(SIZES.values())))
    return {subset: sorted(next(chosen) for _ in range(size)) for subset, size in SIZES.items()}


def write(output, subsets, records):
    for subset, question_ids in subsets.items():
        for question_id in question_ids:
            image, question = records[question_id]
            if not image.startswith(b"\xff\xd8\xff") or len(image) > MAX_IMAGE_BYTES:
                raise ValueError(f"question {question_id}: image is not a JPEG within {MAX_IMAGE_BYTES} bytes")
            question = question.encode("utf-8")
            if not question or len(question) > MAX_QUESTION_BYTES:
                raise ValueError(f"question {question_id}: question outside 1..{MAX_QUESTION_BYTES} bytes")
            record = output / subset / str(question_id)
            record.mkdir(parents=True)
            (record / "image").write_bytes(image)
            (record / "question").write_bytes(question)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    from huggingface_hub import hf_hub_download
    import pyarrow.parquet as pq

    table = pq.read_table(hf_hub_download(REPO, SHARD, repo_type="dataset", revision=REVISION),
                          columns=["question_id", "question", "image"])
    records = {row["question_id"]: (row["image"]["bytes"], row["question"]) for row in table.to_pylist()}
    write(args.output, split(records), records)


if __name__ == "__main__":
    main()

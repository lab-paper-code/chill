"""Stage the SmolVLM2 model and preprocessor carrier directories from one pinned revision."""

import argparse
import json
import shutil
from pathlib import Path

REPO = "HuggingFaceTB/SmolVLM2-500M-Video-Instruct"
REVISION = "7b375e1b73b11138ff12fe22c8f2822d8fe03467"
# The module keys sessions by these stems, so every variant is staged under them.
MODELS = ["vision_encoder", "embed_tokens", "decoder_model_merged"]
VARIANTS = {"model": "", "model-int8": "_int8"}
PROCESSOR_FILES = [
    "added_tokens.json", "chat_template.json", "preprocessor_config.json", "processor_config.json",
    "special_tokens_map.json", "tokenizer.json", "tokenizer_config.json",
]
MODULE = Path(__file__).resolve().parents[1] / "preprocessors" / "smolvlm2-vqa" / "preprocessor.py"


def download(name):
    from huggingface_hub import hf_hub_download

    return hf_hub_download(REPO, name, revision=REVISION)


def stage_model(directory, suffix=""):
    directory.mkdir(parents=True, exist_ok=True)
    for stem in MODELS:
        shutil.copyfile(download(f"onnx/{stem}{suffix}.onnx"), directory / f"{stem}.onnx")


def stage_preprocessor(directory, one_tile=False):
    directory.mkdir(parents=True, exist_ok=True)
    for name in PROCESSOR_FILES:
        shutil.copyfile(download(name), directory / name)
    shutil.copyfile(MODULE, directory / "preprocessor.py")
    if one_tile:
        # No image splitting: each image becomes one 512 x 512 tile of 64 tokens.
        config = directory / "preprocessor_config.json"
        config.write_text(json.dumps(dict(json.loads(config.read_text()), do_image_splitting=False), indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("kind", choices=[*VARIANTS, "preprocessor", "preprocessor-1tile"])
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    if args.kind in VARIANTS:
        stage_model(args.directory, VARIANTS[args.kind])
    else:
        stage_preprocessor(args.directory, args.kind == "preprocessor-1tile")


if __name__ == "__main__":
    main()

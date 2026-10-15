"""Stage the SmolVLM2 model and preprocessor carrier directories from one pinned revision."""

import argparse
import shutil
from pathlib import Path

REPO = "HuggingFaceTB/SmolVLM2-500M-Video-Instruct"
REVISION = "7b375e1b73b11138ff12fe22c8f2822d8fe03467"
MODEL_FILES = ["onnx/vision_encoder.onnx", "onnx/embed_tokens.onnx", "onnx/decoder_model_merged.onnx"]
PROCESSOR_FILES = [
    "added_tokens.json", "chat_template.json", "preprocessor_config.json", "processor_config.json",
    "special_tokens_map.json", "tokenizer.json", "tokenizer_config.json",
]
MODULE = Path(__file__).resolve().parents[1] / "preprocessors" / "smolvlm2-vqa" / "preprocessor.py"


def fetch(names, directory):
    from huggingface_hub import hf_hub_download

    directory.mkdir(parents=True, exist_ok=True)
    for name in names:
        shutil.copyfile(hf_hub_download(REPO, name, revision=REVISION), directory / Path(name).name)


def stage_preprocessor(directory):
    fetch(PROCESSOR_FILES, directory)
    shutil.copyfile(MODULE, directory / "preprocessor.py")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("kind", choices=["model", "preprocessor"])
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    if args.kind == "model":
        fetch(MODEL_FILES, args.directory)
    else:
        stage_preprocessor(args.directory)


if __name__ == "__main__":
    main()

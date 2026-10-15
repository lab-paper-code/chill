"""SmolVLM2 VQA preprocessing module for the firstfloor runner.

The carrier directory also holds the processor and tokenizer files of
HuggingFaceTB/SmolVLM2-500M-Video-Instruct at the revision pinned in
build/model-artifacts/smolvlm2/stage.py. Sessions are keyed by model file
basename. ONNX I/O read from that revision's fp32 graphs:

  vision_encoder        pixel_values f32 [b, k, 3, 512, 512],
                        pixel_attention_mask bool [b, k, 512, 512]
                        -> image_features f32 [real tiles, 64, 960]
  embed_tokens          input_ids i64 [b, s] -> inputs_embeds f32 [b, s, 960]
  decoder_model_merged  inputs_embeds f32 [b, s, 960], attention_mask i64 [b, past + s],
                        position_ids i64 [b, s], past_key_values.{0..31}.{key,value} f32 [b, 5, past, 64]
                        -> logits f32 [b, s, 49280], present.{0..31}.{key,value} f32 [b, 5, past + s, 64]
"""

import io
from pathlib import Path

import numpy as np
from PIL import Image
# The package-level SmolVLMProcessor is a placeholder that demands torch; the
# module class itself needs only numpy, PIL, tokenizers, num2words and jinja2.
from transformers.models.smolvlm.processing_smolvlm import SmolVLMProcessor

LAYERS, KV_HEADS, HEAD_DIM = 32, 5, 64
PAST = [f"past_key_values.{i}.{kv}" for i in range(LAYERS) for kv in ("key", "value")]
DECODER_OUTPUTS = ["logits"] + [f"present.{i}.{kv}" for i in range(LAYERS) for kv in ("key", "value")]
# lmms-eval VQAv2 post prompt: VQAv2 scores single-word or short-phrase answers.
QUESTION_SUFFIX = "\nAnswer the question using a single word or phrase."

PROCESSOR = SmolVLMProcessor.from_pretrained(Path(__file__).resolve().parent)
TOKENIZER = PROCESSOR.tokenizer
IMAGE_TOKEN = TOKENIZER.convert_tokens_to_ids(PROCESSOR.image_token)
EOS = TOKENIZER.convert_tokens_to_ids(PROCESSOR.end_of_utterance_token)


def _warmup_jpeg():
    buffer = io.BytesIO()
    Image.new("RGB", (640, 480), (124, 116, 104)).save(buffer, "JPEG")
    return buffer.getvalue()


WARMUP_SAMPLE = {"image": _warmup_jpeg(), "question": b"What is in the image?"}


def preprocess(samples):
    images, prompts, report = [], [], []
    for sample in samples:
        try:
            with Image.open(io.BytesIO(sample["image"])) as image:
                image = image.convert("RGB")
            question = sample["question"].decode("utf-8")
        except Exception as error:
            report.append(f"{type(error).__name__}: {error}")
            continue
        images.append([image])
        prompts.append(PROCESSOR.apply_chat_template([{"role": "user", "content": [
            {"type": "image"}, {"type": "text", "text": question + QUESTION_SUFFIX}]}], add_generation_prompt=True))
        report.append(None)
    if not images:
        return None, report
    batch = PROCESSOR(text=prompts, images=images, padding=True, padding_side="left", return_tensors="np")
    # One unit is one encoded tile: the split grid plus the global image.
    units = iter(((batch["input_ids"] == IMAGE_TOKEN).sum(axis=1) // PROCESSOR.image_seq_len).tolist())
    return batch, [next(units) if item is None else item for item in report]


def infer(sessions, inputs, max_new_tokens, first_token):
    embed, decoder = sessions["embed_tokens"], sessions["decoder_model_merged"]
    ids, mask = inputs["input_ids"], inputs["attention_mask"]
    embeds = embed.run(None, {"input_ids": ids})[0]
    features = sessions["vision_encoder"].run(None, {
        "pixel_values": inputs["pixel_values"],
        "pixel_attention_mask": inputs["pixel_attention_mask"].astype(bool),
    })[0]
    embeds[ids == IMAGE_TOKEN] = features.reshape(-1, features.shape[-1])
    batch = len(ids)
    past = {name: np.zeros((batch, KV_HEADS, 0, HEAD_DIM), np.float32) for name in PAST}
    positions = mask.cumsum(axis=1) - 1
    positions[mask == 0] = 1
    done = np.zeros(batch, bool)
    tokens = []
    for step in range(max_new_tokens):
        logits, *present = decoder.run(DECODER_OUTPUTS, {
            "inputs_embeds": embeds, "attention_mask": mask, "position_ids": positions, **past})
        token = np.where(done, EOS, logits[:, -1].argmax(axis=-1))
        if step == 0:
            first_token()
        tokens.append(token)
        done |= token == EOS
        if done.all():
            break
        past = dict(zip(PAST, present))
        mask = np.concatenate([mask, np.ones((batch, 1), mask.dtype)], axis=1)
        positions = positions[:, -1:] + 1
        embeds = embed.run(None, {"input_ids": token[:, None]})[0]
    return np.stack(tokens, axis=1)


def postprocess(tokens):
    return [{"answer": answer.strip().encode("utf-8")}
            for answer in TOKENIZER.batch_decode(tokens, skip_special_tokens=True)]

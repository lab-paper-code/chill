"""Synthetic preprocessor for runner tests: field `input` echoes to field `output`.

Sample values steer the contract paths: `fail:<reason>` fails that item, `raise`
raises in preprocess, `short` drops one report, `no-first-token` and
`two-first-tokens` break the generative callback rule, `bad-output` returns a
wrong output field. Units are one plus the count of `+` bytes.
"""

WARMUP_SAMPLE = {"input": b"warmup"}

# Reproduce ORT's exception contract without loading ORT or exhausting memory.
RuntimeException = type("RuntimeException", (Exception,), {
    "__module__": "onnxruntime.capi.onnxruntime_pybind11_state"})
ALLOCATION_DIAGNOSTIC = (
    "[ONNXRuntimeError] : 6 : RUNTIME_EXCEPTION : Non-zero status code returned while running Softmax node. "
    "Name:'/vision_model/encoder/layers.0/self_attn/Softmax' Status Message: "
    "/onnxruntime_src/onnxruntime/core/framework/bfc_arena.cc:359 "
    "void* onnxruntime::BFCArena::AllocateRawInternal(size_t, bool, onnxruntime::Stream*) "
    "Failed to allocate memory for requested buffer of size 1308622848\n")


def preprocess(samples):
    values = [s["input"] for s in samples]
    if b"raise" in values:
        raise RuntimeError("synthetic preprocess exception")
    reports = [v[5:].decode() if v.startswith(b"fail:") else 1 + v.count(b"+") for v in values]
    if b"short" in values:
        reports.pop()
    return [v for v, r in zip(values, reports) if isinstance(r, int)], reports


def infer(sessions, inputs, max_new_tokens, first_token):
    if b"allocation-failure" in inputs:
        raise RuntimeException(ALLOCATION_DIAGNOSTIC)
    if b"ort-shape-failure" in inputs:
        raise RuntimeException("shape mismatch, not an allocator rejection")
    if b"fake-allocation-text" in inputs:
        raise RuntimeError(ALLOCATION_DIAGNOSTIC)
    if max_new_tokens > 0:
        calls = 0 if b"no-first-token" in inputs else 2 if b"two-first-tokens" in inputs else 1
        for _ in range(calls):
            first_token()
    return inputs


def postprocess(outputs):
    return [{"wrong" if v == b"bad-output" else "output": v} for v in outputs]

"""Synthetic preprocessor for runner tests: field `input` echoes to field `output`.

Sample values steer the contract paths: `fail:<reason>` fails that item, `raise`
raises in preprocess, `short` drops one report, `no-first-token` and
`two-first-tokens` break the generative callback rule, `bad-output` returns a
wrong output field. Units are one plus the count of `+` bytes.
"""

WARMUP_SAMPLE = {"input": b"warmup"}


def preprocess(samples):
    values = [s["input"] for s in samples]
    if b"raise" in values:
        raise RuntimeError("synthetic preprocess exception")
    reports = [v[5:].decode() if v.startswith(b"fail:") else 1 + v.count(b"+") for v in values]
    if b"short" in values:
        reports.pop()
    return [v for v, r in zip(values, reports) if isinstance(r, int)], reports


def infer(sessions, inputs, max_new_tokens, first_token):
    if max_new_tokens > 0:
        calls = 0 if b"no-first-token" in inputs else 2 if b"two-first-tokens" in inputs else 1
        for _ in range(calls):
            first_token()
    return inputs


def postprocess(outputs):
    return [{"wrong" if v == b"bad-output" else "output": v} for v in outputs]

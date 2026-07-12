# CPU ORT Static-Compatibility Fixture

This directory contains the isolated CPU-only input for Plan 1. It is not the
installed `ModelSpec` CRD, a compatibility controller, or a profiling Run.

The fixture declares only:

- logical model identity
- one immutable ONNX artifact
- one execution path requiring ONNX Runtime and `CPUExecutionProvider`

Validate its portable structure and selected CPU path:

```sh
python3 spikes/modelcompat/validate_modelspec.py \
  --fixture spikes/modelcompat/fixtures/mobilenet-v2-050-ort-cpu.yaml \
  --execution-path ort-cpu
```

When the artifact bytes are available, additionally bind the declared digest
to them with `--artifact-file PATH`. This optional evidence check is separate
from structural validation and verifies only the artifact selected by the
named execution path.

The older multi-runtime material under `spikes/modelspec` remains background
evidence. It is not an API source for this CPU-first path.

## Static compatibility report

Run the one-shot read-only consumer with three exact files and one selected
path:

```sh
go run ./spikes/modelcompat/cmd/candidate-report \
  -model-spec spikes/modelcompat/fixtures/mobilenet-v2-050-ort-cpu.yaml \
  -device-class spikes/modelcompat/fixtures/lattepanda-3-delta-8g.yaml \
  -runtime-declaration PATH/sha256-CHILD-DIGEST.json \
  -execution-path ort-cpu
```

Stdout is one deterministic JSON report; the human claim boundary is written
to stderr. `Compatible`, `Incompatible`, and `Unknown` are all successful
domain reports and exit zero. Input/identity failure exits one, and CLI usage
failure exits two.

The recognized declaration `verification` string is method metadata for this
isolated, reviewed producer path. It is not a signature, attestation, or proof
of authorship. The report establishes static declaration agreement only; it
does not establish Node availability, scheduling, image pull, model load,
inference, performance, power, or SLO feasibility.

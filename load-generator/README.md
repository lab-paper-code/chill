# CHILL Load Generator

A small Python project for generating external open-loop inference workloads.

## Workload model

- schedule individual inference requests independently of responses;
- allow each request to carry one or more inference items; and
- record offered, sent, rejected, and completed counts at both request and item
  granularity.

The target cluster handles routing, power-mode decisions, and any additional
server-side batching.

## Development

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install .
chill-loadgen
```

Run the dependency-free test suite with:

```sh
PYTHONPATH=src python -m unittest discover -s tests -v
```

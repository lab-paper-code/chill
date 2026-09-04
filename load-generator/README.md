# CHILL Load Generator

A small Python project for generating external open-loop inference workloads.

## Workload model

- schedule individual inference requests independently of responses;
- send one logical inference item per request; and
- record offered, sent, rejected, and completed requests separately.

The target cluster handles batching, routing, and power-mode decisions.

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

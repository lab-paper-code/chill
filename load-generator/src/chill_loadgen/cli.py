"""Command-line entry point for the external load generator."""

from __future__ import annotations

import argparse
from collections.abc import Sequence
from typing import Optional

from chill_loadgen import __version__


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="chill-loadgen",
        description="Generate an external open-loop inference workload.",
    )
    parser.add_argument("--version", action="version", version=__version__)
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = build_parser()
    parser.parse_args(argv)
    parser.print_help()
    return 0

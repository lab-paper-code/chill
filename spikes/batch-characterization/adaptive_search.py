#!/usr/bin/env python3
"""Adaptive batch search: exponential bracketing followed by binary refinement."""

# TODO(internal): Move this policy behind the profiler campaign controller.
# Persist measured points and search phase in a CR status so controller restarts
# do not repeat work. This spike returns a candidate only; uncertainty-aware
# acceptance and the non-unimodal fallback remain unimplemented.

from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass


@dataclass(frozen=True)
class SearchResult:
    candidate: int | None
    bracket: tuple[int, int] | None
    measured_batches: tuple[int, ...]
    stopped_reason: str


def search(measure: Callable[[int], float], maximum_batch: int) -> SearchResult:
    """Return the energy-minimum candidate while caching every measurement.

    Coarse batches double from one until the first strict energy increase.
    The preceding/current pair brackets a discrete binary search that compares
    epsilon(mid) and epsilon(mid + 1). No scientific acceptance is performed.
    """
    if maximum_batch < 1:
        raise ValueError("maximum_batch must be positive")

    cache: dict[int, float] = {}

    def energy(batch: int) -> float:
        if batch not in cache:
            cache[batch] = measure(batch)
        return cache[batch]

    previous = 1
    previous_energy = energy(previous)
    bracket: tuple[int, int] | None = None
    while previous < maximum_batch:
        current = min(previous * 2, maximum_batch)
        current_energy = energy(current)
        if current_energy > previous_energy:
            bracket = (previous, current)
            break
        previous, previous_energy = current, current_energy

    if bracket is None:
        return SearchResult(None, None, tuple(cache), "no-rise-before-maximum")

    left, right = bracket
    while left < right:
        middle = (left + right) // 2
        if energy(middle) <= energy(middle + 1):
            right = middle
        else:
            left = middle + 1

    return SearchResult(left, bracket, tuple(cache), "candidate-found")

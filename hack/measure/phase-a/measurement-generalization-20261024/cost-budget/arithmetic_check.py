#!/usr/bin/env python3
"""Planning arithmetic only; no network, source, CR, or physical action."""
from fractions import Fraction as F


def trace_value(points, time):
    """Endpoint hold exists ONLY for labelled old-data diagnostics."""
    if time <= points[0][0]:
        return points[0][1]
    for (a, p), (b, q) in zip(points, points[1:]):
        if time <= b:
            return p + (q - p) * (time - a) / (b - a)
    return points[-1][1]


def trace_integral(points, start, end):
    cuts = [start] + [t for t, _ in points if start < t < end] + [end]
    return sum((trace_value(points, a) + trace_value(points, b)) * (b - a) / 2
               for a, b in zip(cuts, cuts[1:]))


def trace_shift(points, duration, age, *, diagnostic_hold=False):
    """Exact extrema of a shifted piecewise-linear integral; no phase selection."""
    points = [(F(str(t)), F(str(p))) for t, p in points]
    duration, age = F(str(duration)), F(str(age))
    if duration <= 0 or age < 0 or len(points) < 2:
        raise ValueError('invalid duration/age/trace')
    if any(p < 0 for _, p in points) or any(a[0] >= b[0] for a, b in zip(points, points[1:])):
        raise ValueError('invalid power or original timestamp ordering')
    if not diagnostic_hold and (points[0][0] > -age or points[-1][0] < duration + age):
        raise ValueError('real trace does not support every allowed source-time shift')
    cuts = sorted({-age, age} | {t - b for t, _ in points for b in (0, duration)
                                        if -age < t - b < age})
    candidates = list(cuts)
    for a, b in zip(cuts, cuts[1:]):
        da = trace_value(points, duration + a) - trace_value(points, a)
        db = trace_value(points, duration + b) - trace_value(points, b)
        if da * db < 0:
            candidates.append(a - da * (b - a) / (db - da))
    energy = trace_integral(points, F(0), duration)
    shift = max(candidates, key=lambda d: abs(trace_integral(points, d, duration + d) - energy))
    return energy, shift, abs(trace_integral(points, shift, duration + shift) - energy)


if __name__ == '__main__':
    assert trace_shift([(-3, 7), (13, 7)], 10, 2)[2] == 0
    sloped = trace_shift([(-3, 97), (13, 113)], 10, 2)[2]
    assert sloped == 20
    assert trace_shift([(-3, 197), (13, 213)], 10, 2)[2] == sloped
    try:
        trace_shift([(0, 7), (10, 7)], 10, 2)
    except ValueError:
        pass
    else:
        raise AssertionError('missing real shift support must not silently hold')
    print('PASS: trace extrema/offset cancellation/support')

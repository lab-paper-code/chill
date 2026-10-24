#!/usr/bin/env python3
"""Planning arithmetic only; no network, source, CR, or physical action."""
from fractions import Fraction as F


def uncertain_seconds(duration, left, right, interior=()):
    """Union clipped uncertainty intervals; never count a physical instant twice."""
    spans = [(F(0), F(left)), (F(duration) - F(right), F(duration))]
    spans += [(F(a), F(b)) for a, b in interior]
    spans = sorted((max(F(0), a), min(F(duration), b)) for a, b in spans)
    total = F(0)
    end = F(0)
    for a, b in spans:
        if b > max(a, end):
            total += b - max(a, end)
        end = max(end, b)
    return total


def bound(power_envelope, duration, refresh, response, left_gap, right_gap, interior=()):
    age = F(refresh) + F(response)
    return F(power_envelope) * uncertain_seconds(
        duration, age + F(left_gap), age + F(right_gap), interior)


def accepted(energy, error, relative=F(1, 10)):
    """10% of recorded positive physical total, not a certified true-energy ratio."""
    return F(energy) > F(error) >= 0 and F(error) <= relative * F(energy)


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
    assert uncertain_seconds(1, 2, 2, [(0, 1)]) == 1
    assert uncertain_seconds(10, 1, 1, [(0, 3), (2, 4), (8, 10)]) == 6
    assert bound(12, 10, 1, 0, 0, 0) == 24
    assert bound(12, 1, 1, 0, 0, 0) == 12
    assert accepted(100, 10)
    assert not accepted(100, F(10000001, 1000000))
    assert not accepted(0, 0)
    assert not accepted(1, 1)
    assert not accepted(-100, 1)
    # No accidental cancellation of empirical intervals in composition.
    remove, prepare = F('36.3183156012'), F('920.24978404525')
    assert not accepted(remove, 24)
    assert accepted(remove + prepare, 87)
    assert not accepted(remove + prepare, 174)
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
    print('PASS: exact union, 10% threshold, composition, trace extrema/offset cancellation/support')

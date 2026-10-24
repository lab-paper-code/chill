#!/usr/bin/env python3
"""Read-only old-evidence arithmetic; writes only adjacent new analysis outputs."""
import datetime
from fractions import Fraction as F
import hashlib
import json
import os
import argparse
from pathlib import Path
import runpy

CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
ROOT = Path(ENV["costShiftOutput"])
BASE = Path(CONFIG["paths"]["campaign"]) / "phase-a/measurement-generalization-20261024"
HELPER = Path(__file__).resolve().parent.parent / "cost-budget/arithmetic_check.py"
# Reuse only the exact shift primitives, never the obsolete quality/envelope policy.
helper = runpy.run_path(str(HELPER))
trace_shift = helper['trace_shift']
trace_integral = helper['trace_integral']


def load(path):
    return json.loads(Path(path).read_text(), parse_float=F)


def stamp(text):
    date, time = text.rstrip('Z').split('T')
    hour, minute, second = time.split(':')
    return F(datetime.date.fromisoformat(date).toordinal() * 86400
             + int(hour) * 3600 + int(minute) * 60) + F(second)


def union_width(intervals):
    end = None
    total = F(0)
    for a, b in sorted(intervals):
        if end is None or a > end:
            total += b - a
        elif b > end:
            total += b - end
        end = b if end is None else max(end, b)
    return total


def check():
    constant = trace_shift([(-3, 7), (13, 7)], 10, 2)
    assert constant[0] == 70 and constant[2] == 0
    slope = trace_shift([(-3, 97), (13, 113)], 10, 2)
    offset = trace_shift([(-3, 197), (13, 213)], 10, 2)
    assert slope[2] == offset[2] == 20
    # Interior stationary optimum, not merely boundary shifts or a phase grid.
    stationary = trace_shift([(-2, 0), (0, 2), (2, 0), (4, 2)], 2, 2)
    assert stationary[2] == 1 and abs(stationary[1]) == 1
    assert union_width([(0, 2), (1, 3), (3, 4)]) == 4
    assert min(F(2), union_width([(-5, 5)])) == 2
    try:
        trace_shift([(0, 7), (10, 7)], 10, 2)
    except ValueError:
        pass
    else:
        raise AssertionError('real expanded support cannot be silently fabricated')


def main():
    check()
    raw_input = BASE / 'cost-budget/raw-inputs.json'
    cadence_path = BASE / 'meter-cadence/summary.json'
    inputs, cadence = load(raw_input), load(cadence_path)
    records_path = Path(inputs['sourceRecords'])
    records = {r['operationID']: r for r in load(records_path)['records']}
    refs = [HELPER, raw_input, BASE / 'cost-budget/report.txt',
            BASE / 'cost-budget/range-aware-arithmetic.json', cadence_path, records_path]
    age = {}
    for node, data in cadence['nodes'].items():
        p95 = data['inter_change_seconds']['p95']
        request = data['request_latency_seconds']['max']
        receipt = data['successful_completion_interarrival_seconds']['max']
        age[node] = {'phaseAllowanceSeconds': p95 + request + receipt,
                     'p95ChangeIntervalSeconds': p95, 'maximumRequestSeconds': request,
                     'maximumReceiptGapSeconds': receipt, 'family': data['family'],
                     'provenance': str(cadence_path) + '#nodes.' + node,
                     'label': 'empirical estimate from approved completed 120s observation'}
    rows = []
    for source in inputs['currentInputs']:
        if not ('rasp5-945-raw' in source['raw'] or
                (source['node'] == 'genesis2' and source['action'] == 'prepare')):
            continue
        path = Path(source['raw'])
        refs.append(path)
        raw = load(path)
        record = records[source['operationID']]
        a, b = stamp(raw['result']['startedAt']), stamp(raw['result']['endedAt'])
        duration = b - a
        allowance = age['genesis2']['phaseAllowanceSeconds']
        points = [(stamp(p['observedAt']) - a, F(p['watts'])) for p in raw['power']['samples']]
        curve, phase, sensitivity = trace_shift(points, duration, allowance, diagnostic_hold=True)
        gap_limit = F(raw['request']['coverage']['MaximumGap'], 1000000000)
        failures = raw['power']['failures'] or []
        # These four records have no failure events: do not guess an unknown event schema.
        assert not failures, 'explicit failure event mapping needed before adding another trace'
        gaps = [(left[0], right[0]) for left, right in zip(points, points[1:])]
        flagged = [(left, right) for left, right in gaps
                   if right - left > gap_limit and right > -allowance and left < duration + allowance]
        missing = min(duration, union_width(flagged))
        pmax = max(p for _, p in points)
        correction = pmax * missing
        uncertainty = sensitivity + correction
        stored = record['storedWallJoules']
        denominator = curve
        rows.append({'node': source['node'], 'action': source['action'],
                     'operationID': source['operationID'], 'rawSource': str(path),
                     'originalVerdict': raw['verdict'], 'storedNominalJoules': stored,
                     'priorEstimatorDiagnosticJoules': record['currentEstimatorJoules'],
                     'curveNominalEndpointContinuationJoules': curve,
                     'curveIntegralWithinActualSupportJoules': trace_integral(
                         points, max(F(0), points[0][0]), min(duration, points[-1][0])),
                     'sensitivityJoules': sensitivity, 'missingGapJoules': correction,
                     'uncertaintyJoules': uncertainty, 'relativeB': uncertainty / denominator,
                     'relativeBPercent': 100 * uncertainty / denominator,
                     'relativeBDenominator': 'diagnostic piecewise-linear curve nominal, consistently for every row',
                     'relativeToCurvePercent': 100 * uncertainty / curve,
                     'worstShiftSeconds': phase, 'phaseAllowanceSeconds': allowance,
                     'durationSeconds': duration, 'sampleCount': len(points),
                     'firstSampleRelativeSeconds': points[0][0],
                     'lastSampleRelativeToEndSeconds': points[-1][0] - duration,
                     'missingLeftExpandedSupportSeconds': max(F(0), points[0][0] + allowance),
                     'missingRightExpandedSupportSeconds': max(F(0), duration + allowance - points[-1][0]),
                     'actualExpandedSupport': False, 'actualPostBracket': False,
                     'endpointContinuationDiagnosticOnly': True, 'retroactiveAccepted': False,
                     'minimumOriginalGapSeconds': min(y-x for x,y in gaps),
                     'maximumOriginalGapSeconds': max(y-x for x,y in gaps),
                     'abnormallyLongGapThresholdSeconds': gap_limit,
                     'reportedRequestFailures': 0, 'flaggedOriginalGapIntervals': flagged,
                     'missingGapUnionSecondsCapped': missing, 'actualRecordMaximumWatts': pmax})
    result = {'label': 'old trace diagnostic endpoint continuation, NOT new measurement acceptance',
              'formula': 'B=S+G; exact all-phase knot/stationary shift sensitivity, no multiplier',
              'age': age, 'rows': rows,
              'missingGapFormula': 'G=actual-record max watts * min(T, union width of FULL original flagged intervals intersecting [a-A,b+A]); no interval clipping or repeated A expansion',
              'missingGapAssumption': 'unobserved power in flagged intervals lies between zero and observed actual-record maximum; empirical conditional model, not certified bound',
              'sourcesSHA256': {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in refs},
              'checks': 'PASS constant S=0; offset invariance; interior stationary extremum; union/cap; absent-support rejection'}
    encoded = json.dumps(result, indent=2, default=lambda x: float(x) if isinstance(x, F) else str(x))
    (ROOT / 'arithmetic.json').write_text(encoded + '\n')
    print(encoded)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output-dir', type=Path, default=ROOT)
    args = parser.parse_args()
    ROOT = args.output_dir.resolve()
    repository = Path(CONFIG["paths"]["repository"]).resolve()
    if repository == ROOT or repository in ROOT.parents:
        parser.error("output must remain outside the repository source tree")
    ROOT.mkdir(parents=True, exist_ok=True)
    main()

#!/usr/bin/env python3
"""C1(a): raw profiling holdouts, shared Go derivation/replay, diagnostic reports only."""
import argparse
from bisect import bisect_right
from collections import Counter
import copy
import json
import math
from pathlib import Path
import random
import statistics
import subprocess

import evidence_io
from profile_comparison import number

ACTIVE_SCHEMA = 'chill.dacs.io/profiling-raw-measurement.v1alpha11'
IDLE_SCHEMA = 'chill.dacs.io/model-loaded-idle-raw-measurement.v1alpha8'


def quantiles(values, percentile):
    values = sorted(values)
    def rank(p):
        return values[max(0, math.ceil(p * len(values) / 100) - 1)] if values else None
    return {'p50': statistics.median(values) if values else None,
            'p95': rank(95), 'p99': rank(99), 'slo': rank(percentile)}


def interval(values, independent_trials, confidence):
    if independent_trials < 2 or not values or any(v is None for v in values):
        return None
    values = sorted(values)
    alpha = (1 - confidence) / 2
    return [values[max(0, math.ceil(alpha * len(values)) - 1)],
            values[min(len(values) - 1, math.ceil((1 - alpha) * len(values)) - 1)]]


def bootstrap_indices(indices, repetitions, seed):
    rng = random.Random(seed)
    return [[rng.choice(indices) for _ in indices] for _ in range(repetitions)]


def samples(raw):
    points = raw['metrics']['samples']
    previous = None
    if not points:
        raise ValueError('meter samples required')
    for point in points:
        at = evidence_io.timestamp_ns(point['observedAt'])
        number(point['watts'], 'meter watts')
        if point['watts'] < 0 or (previous is not None and at <= previous):
            raise ValueError('meter samples must be nonnegative and strictly ordered')
        previous = at
    return points


def validate_runs(runs):
    if not runs:
        raise ValueError('raw evidence required')
    seen = set()
    reference = None
    for raw in runs:
        if raw.get('schemaVersion') != ACTIVE_SCHEMA:
            raise ValueError('active raw v1alpha11 required')
        trial = raw['podUID']
        if not trial or trial in seen:
            raise ValueError('missing or duplicate physical trial ID')
        seen.add(trial)
        config, runtime = raw['runConfig'], raw['runtime']
        if runtime.get('schemaVersion') != 'chill.dacs.io/batch-characterization.v1alpha11':
            raise ValueError('runtime v1alpha11 required')
        measurement = config['measurement']
        batch = measurement['batch']
        if type(batch) is not int or batch < 1:
            raise ValueError('positive fixed batch required')
        percentile = number(measurement['sloPercentile'], 'SLO percentile', positive=True)
        if percentile >= 100:
            raise ValueError('SLO percentile must be below 100')
        number(measurement['sloTargetLatencyNanos'], 'SLO target', positive=True)
        operating = measurement['operatingPoints']
        number(operating['requestLifetimeSeconds'], 'request lifetime', positive=True)
        number(operating['deliveryMarginMilliseconds'], 'delivery margin')
        phases = runtime['phases']
        if ([p['kind'] for p in phases[:2]] != ['sparse', 'saturation'] or
                len(phases) < 3 or any(p['kind'] != 'utilization' for p in phases[2:])):
            raise ValueError('sparse, saturation and utilization phases required')
        condition = (config.get('state'), config.get('targetNode', {}).get('uid'),
                     config.get('images', {}).get('runtime'), measurement,
                     [(p['id'], p['kind'], p.get('utilization')) for p in phases])
        if reference is not None and condition != reference:
            raise ValueError('raw trials must share execution, node, runtime, measurement and phase conditions')
        reference = condition
        batch_ids, request_ids, phase_ids = set(), set(), set()
        samples(raw)
        previous_end = None
        for phase in phases:
            start = evidence_io.timestamp_ns(phase['measurementStartedAt'])
            end = evidence_io.timestamp_ns(phase['measurementEndedAt'])
            if end <= start or (previous_end is not None and start < previous_end):
                raise ValueError('phase windows must be positive and ordered')
            previous_end = end
            if phase['id'] in phase_ids:
                raise ValueError('duplicate phase ID')
            phase_ids.add(phase['id'])
            native, requests = phase['nativeSamples'], phase['requests']
            if not native or phase['completedInputs'] != len(native) * batch or len(requests) != phase['completedInputs']:
                raise ValueError('phase request count must match complete physical batches')
            if len(phase['latencyMs']) != len(native):
                raise ValueError('batch latency count must match native samples')
            number(phase['measurementElapsedSecondsMonotonic'], 'phase monotonic duration', positive=True)
            for sample in native:
                if sample['batchID'] in batch_ids or sample['trialID'] != trial or sample['batchSize'] != batch:
                    raise ValueError('native sample identity mismatch or duplicate batch ID')
                batch_ids.add(sample['batchID'])
                if len(sample['recordIDs']) != batch or len(sample['units']) != batch:
                    raise ValueError('native member count mismatch')
                number(sample['serviceDurationNanos'], 'native service', positive=True)
                if sample.get('precedingIdleNanos') is not None:
                    gap = number(sample['precedingIdleNanos'], 'preceding idle')
                    if gap < 0:
                        raise ValueError('preceding idle cannot be negative')
                for key in ('startedAt', 'finishedAt', 'earliestEnqueuedAt', 'latestEnqueuedAt'):
                    evidence_io.timestamp_ns(sample[key])
            for request in requests:
                if not request['requestID'] or request['requestID'] in request_ids:
                    raise ValueError('missing or duplicate request ID')
                request_ids.add(request['requestID'])
                for key in ('arrivalAt', 'sentAt', 'completedAt'):
                    evidence_io.timestamp_ns(request[key])
                number(request['latencyMs'], 'request latency', positive=True)


def folds(runs):
    phases = runs[0]['runtime']['phases']
    result = [{'kind': 'phase', 'id': p['id'], 'testTrials': list(range(len(runs))),
               'trainTrials': list(range(len(runs)))} for p in phases if p['kind'] == 'utilization']
    if len(runs) > 1:
        result.extend({'kind': 'trial', 'id': raw['podUID'], 'testTrials': [i],
                       'trainTrials': [j for j in range(len(runs)) if j != i]} for i, raw in enumerate(runs))
    return result


def training_trials(runs, fold, indices):
    counts = Counter(indices)
    result = []
    for draw, index in enumerate(indices):
        raw = runs[index]
        phases = [p for p in raw['runtime']['phases'] if not (fold['kind'] == 'phase' and p['id'] == fold['id'])]
        # Bootstrap copies keep physical samples intact; distinct sampler trial IDs
        # represent multiplicity of whole-trial draws, never request resampling.
        trial_id = raw['podUID'] + (f'#draw{draw}' if counts[index] > 1 else '')
        native = [dict(sample, trialID=trial_id) for phase in phases for sample in phase['nativeSamples']]
        offset = 0
        projected = []
        for phase in phases:
            count = len(phase['nativeSamples'])
            projected.append({'id': phase['id'], 'kind': phase['kind'],
                              'windowStart': phase['measurementStartedAt'], 'windowEnd': phase['measurementEndedAt'],
                              'completedItems': phase['completedInputs'], 'samples': raw['metrics']['samples'],
                              'nativeSamples': native[offset:offset + count]})
            offset += count
        result.append({'trialID': trial_id, 'batch': raw['runConfig']['measurement']['batch'],
                       'windowStart': phases[0]['measurementStartedAt'], 'windowEnd': phases[-1]['measurementEndedAt'],
                       'completedItems': sum(p['completedInputs'] for p in phases), 'completedCalls': len(native),
                       'latencyMilliseconds': [v for p in phases for v in p['latencyMs']],
                       'nativeSamples': native, 'phases': projected, 'samples': raw['metrics']['samples']})
    return result


def idle_observations(raws):
    result = []
    seen = set()
    for raw in raws:
        if raw.get('schemaVersion') != IDLE_SCHEMA:
            raise ValueError('model-loaded idle raw v1alpha8 required')
        if not raw['podUID'] or raw['podUID'] in seen:
            raise ValueError('missing or duplicate idle trial ID')
        seen.add(raw['podUID'])
        runtime = raw['runtime']
        result.append({'trialID': raw['podUID'], 'windowStart': runtime['measurementStartedAt'],
                       'windowEnd': runtime['measurementEndedAt'], 'samples': samples(raw),
                       'inferenceCountAfterModelLoad': runtime['inferenceCountAfterModelLoad'],
                       'modelResidentAtEnd': runtime['modelResidentAtEnd']})
    if not result:
        raise ValueError('independent model-loaded idle raw required; active raw does not contain idle baseline')
    return result


def test_phases(runs, fold):
    return [(i, phase) for i in fold['testTrials'] for phase in runs[i]['runtime']['phases']
            if phase['kind'] == 'utilization' and (fold['kind'] != 'phase' or phase['id'] == fold['id'])]


def replay_input(raw, phase):
    measurement = raw['runConfig']['measurement']
    operating = measurement['operatingPoints']
    first = phase['nativeSamples'][0]
    idle = first.get('precedingIdleNanos')
    if idle is not None:
        # Both timestamps are measurement-node native facts. Never subtract clocks
        # from load-node arrivals and measurement-node native spans.
        formation = evidence_io.timestamp_ns(first['startedAt']) - evidence_io.timestamp_ns(first['earliestEnqueuedAt'])
        idle = max(0, idle - formation)
    return {'arrivals': [r['arrivalAt'] for r in phase['requests']], 'initialIdleNanos': idle,
            'percentile': measurement['sloPercentile'], 'targetNanos': measurement['sloTargetLatencyNanos'],
            'lifetimeSeconds': operating['requestLifetimeSeconds'], 'marginMS': operating['deliveryMarginMilliseconds']}


class GoBridge:
    def __init__(self, executable):
        self.process = subprocess.Popen([str(Path(executable).resolve())], stdin=subprocess.PIPE,
                                        stdout=subprocess.PIPE, text=True)

    def __enter__(self):
        return self

    def call(self, request):
        self.process.stdin.write(json.dumps(request, allow_nan=False, separators=(',', ':')) + '\n')
        self.process.stdin.flush()
        line = self.process.stdout.readline()
        if not line:
            raise ValueError('Go derivation/replay bridge exited without a result')
        result = json.loads(line)
        if 'error' in result:
            raise ValueError('Go derivation/replay: ' + result['error'])
        return result

    def __exit__(self, *_):
        self.process.stdin.close()
        self.process.stdout.close()
        if self.process.wait() != 0:
            raise ValueError('Go derivation/replay bridge failed')


def evaluate_fold(runs, idle, bridge, fold, train_indices, seed):
    tests = test_phases(runs, fold)
    result = bridge.call({'trials': training_trials(runs, fold, train_indices), 'idle': idle_observations(idle),
                          'tests': [replay_input(runs[i], p) for i, p in tests], 'seed': seed})
    result['testPhases'] = tests
    return result


def phase_metrics(runs, evaluated, selected, powers):
    result = {}
    for phase_id in dict.fromkeys(p['id'] for _, p in evaluated['testPhases']):
        measured, predicted, service, predicted_service, watts, rates = [], [], [], [], [], []
        raw_client_latency = []
        outcomes, invalid, support = [], [], []
        for trial_index in selected:
            for (index, phase), trace in zip(evaluated['testPhases'], evaluated['traces']):
                if index != trial_index or phase['id'] != phase_id:
                    continue
                # Model arrival clock is the retained arrivalAt. Add same-load-node
                # send delay to monotonic sent-to-completed latency, preserving the
                # producer's scheduled-to-completed population without clock mixing.
                measured.extend(r['latencyMs'] + (evidence_io.timestamp_ns(r['sentAt']) -
                                evidence_io.timestamp_ns(r['arrivalAt'])) / 1e6 for r in phase['requests'])
                raw_client_latency.extend(r['latencyMs'] for r in phase['requests'])
                predicted.extend(v / 1e6 for values in (trace['latencyNanos'] or {}).values() for v in values)
                service.extend(s['serviceDurationNanos'] / 1e6 for s in phase['nativeSamples'])
                predicted_service.extend(v / 1e6 for v in (trace['batchServiceNanos'] or []))
                watts.append(powers[(runs[index]['podUID'], phase_id)])
                rates.append(phase['completedInputs'] / ((evidence_io.timestamp_ns(phase['measurementEndedAt']) -
                                                        evidence_io.timestamp_ns(phase['measurementStartedAt'])) / 1e9))
                outcomes.append(trace['outcomes'])
                invalid.extend(trace['invalidReasons'] or [])
                support.append(trace['conditionalService'])
        percentile = runs[0]['runConfig']['measurement']['sloPercentile']
        profile = evaluated['profile']
        result[phase_id] = {'id': phase_id, 'latencyMS': {'measured': quantiles(measured, percentile),
                                  'measuredSentToCompleted': quantiles(raw_client_latency, percentile),
                                  'predicted': quantiles(predicted, percentile), 'measuredN': len(measured), 'predictedN': len(predicted)},
                            'serviceMS': {'measured': quantiles(service, percentile),
                                  'predicted': quantiles(predicted_service, percentile), 'measuredN': len(service), 'predictedN': len(predicted_service)},
                            'power': {'measuredWatts': statistics.mean(watts), 'completedRatePerSecond': statistics.mean(rates),
                                      'predictedWatts': profile['idleWatts'] + profile['energyPerInputJoules'] * statistics.mean(rates)},
                            'outcomesByTrial': outcomes, 'invalidReasons': invalid, 'conditionalServiceByTrial': support}
        if invalid:
            for metric in ('latencyMS', 'serviceMS'):
                result[phase_id][metric]['predicted'] = dict.fromkeys(('p50', 'p95', 'p99', 'slo'))
    return result


def time_mean(points, start, end):
    times = [t for t, _ in points]
    def value(at):
        i = bisect_right(times, at) - 1
        if i == len(points) - 1:
            return points[i][1]
        a, x = points[i]
        b, y = points[i + 1]
        return x + (y - x) * (at - a) / (b - a)
    knots = [start] + [t for t in times if start < t < end] + [end]
    return sum((value(a) + value(b)) * (b - a) / 2 for a, b in zip(knots, knots[1:])) / (end - start)


def stabilization(raw_samples, stopped_at, uncertainty, window_seconds, final_seconds):
    stopped = evidence_io.timestamp_ns(stopped_at)
    raw_samples = [p for p in raw_samples if evidence_io.timestamp_ns(p['observedAt']) >= stopped]
    points = [(evidence_io.timestamp_ns(p['observedAt']), number(p['watts'], 'post-load watts')) for p in raw_samples]
    result = {'loadStoppedAt': stopped_at, 'stabilizationSeconds': None, 'finalIdleWatts': None,
              'meterUncertaintyWatts': uncertainty, 'rollingWindowSeconds': window_seconds,
              'finalIdleWindowSeconds': final_seconds, 'samples': raw_samples,
              'reason': 'no post-load samples', 'observedThrough': raw_samples[-1]['observedAt'] if points else None}
    if len(points) < 2:
        return result
    if any(a[0] >= b[0] for a, b in zip(points, points[1:])):
        raise ValueError('post-load samples must be strictly ordered')
    window, final = round(window_seconds * 1e9), round(final_seconds * 1e9)
    if points[-1][0] - points[0][0] < max(window, final) + window:
        result['reason'] = 'post-load window too short'
        return result
    last = points[-1][0]
    idle = time_mean(points, last - final, last)
    result['finalIdleWatts'] = idle
    rolling = [(t, time_mean(points, t - window, t)) for t, _ in points if t - window >= points[0][0]]
    result['rollingMeans'] = [{'secondsAfterLoad': (t - stopped) / 1e9, 'watts': w} for t, w in rolling]
    stable = True
    first = None
    for t, watts in reversed(rolling):
        stable = stable and abs(watts - idle) <= uncertainty
        if stable and last - t >= window:
            first = t
    if first is not None:
        result['stabilizationSeconds'] = (first - stopped) / 1e9
        result['reason'] = None
    else:
        result['reason'] = 'not stabilized within observed window'
    return result


def analyze(runs, idle, bridge, repetitions, seed, confidence, uncertainty, rolling_seconds, final_seconds, post):
    validate_runs(runs)
    for raw in idle:
        config = raw.get('runConfig', {})
        active = runs[0]['runConfig']
        for key in ('state', 'targetNode', 'images'):
            if key == 'targetNode':
                matches = config.get(key, {}).get('uid') == active.get(key, {}).get('uid')
            elif key == 'images':
                matches = config.get(key, {}).get('runtime') == active.get(key, {}).get('runtime')
            else:
                matches = config.get(key) == active.get(key)
            if not matches:
                raise ValueError('idle evidence must share active execution, node and runtime condition')
    all_fold = {'kind': 'all', 'id': None}
    full = bridge.call({'trials': training_trials(runs, all_fold, list(range(len(runs)))),
                        'idle': idle_observations(idle), 'tests': [], 'seed': seed})
    powers = {(trial['trialID'], phase['id']): phase['meanWallWatts']
              for trial in full['trials'] for phase in trial['phasePower']}
    report = {'schema': 'profile-holdout-report.v1', 'confidenceLevel': confidence, 'bootstrapReplicates': repetitions,
              'seed': seed, 'sloPercentile': runs[0]['runConfig']['measurement']['sloPercentile'],
              'bootstrapUnit': 'independent physical trial; no request or batch resampling',
              'latencyOrigin': 'arrivalAt to completion: monotonic latencyMs + same-load-node sentAt-arrivalAt',
              'replayModel': 'single ready fixed-B instance; native policy/admission and conditional service sampler; finite trace drains',
              'powerRateBasis': 'completed inputs / measurement-node wall-clock phase seconds, matching Go EPI integration',
              'limitations': ['One trial cannot identify between-trial uncertainty; its CI is null.',
                              'Replay success distributions exclude rejected/expired/unfinished requests; outcomes are retained.',
                              'Fixed-batch trace starts empty; measured native preceding-idle anchor supplies initial idle.',
                              'No transport fit: configured delivery margin is retained.',
                              'Stabilization stays in band only through the recorded suffix, not indefinitely.'],
              'folds': [], 'postLoadPower': []}
    for fold_index, fold in enumerate(folds(runs)):
        if fold['kind'] == 'phase' and len(runs[0]['runtime']['phases']) == 3:
            percentile = runs[0]['runConfig']['measurement']['sloPercentile']
            heldout = [p for _, p in test_phases(runs, fold)]
            report['folds'].append({'kind': fold['kind'], 'id': fold['id'], 'profile': None,
                'trainingTrialIDs': [runs[i]['podUID'] for i in fold['trainTrials']],
                'heldoutTrialIDs': [runs[i]['podUID'] for i in fold['testTrials']],
                'unavailableReason': 'excluding the only utilization phase leaves no EPI training population',
                'phases': [{'id': fold['id'], 'latencyMS': {
                    'measuredSentToCompleted': quantiles([r['latencyMs'] for p in heldout for r in p['requests']], percentile)},
                    'serviceMS': {'measured': quantiles([s['serviceDurationNanos'] / 1e6 for p in heldout
                                                       for s in p['nativeSamples']], percentile)}}]})
            continue
        evaluated = evaluate_fold(runs, idle, bridge, fold, fold['trainTrials'], seed)
        center = phase_metrics(runs, evaluated, fold['testTrials'], powers)
        boot = {phase_id: [] for phase_id in center}
        bootstrap_profiles = []
        train_draws = bootstrap_indices(fold['trainTrials'], repetitions, seed + fold_index * 3)
        test_draws = bootstrap_indices(fold['testTrials'], repetitions, seed + fold_index * 3 + 1)
        idle_draws = bootstrap_indices(list(range(len(idle))), repetitions, seed + fold_index * 3 + 2)
        for draw, (train, test, idle_indices) in enumerate(zip(train_draws, test_draws, idle_draws)):
            # Idle draws also represent whole independent idle trials.
            resampled_idle = [copy.deepcopy(idle[i]) for i in idle_indices]
            for i, raw in enumerate(resampled_idle):
                raw['podUID'] += f'#draw{i}'
            replicate = evaluate_fold(runs, resampled_idle, bridge, fold, train, seed + draw + 1)
            bootstrap_profiles.append({key: replicate['profile'][key] for key in
                                       ('idleWatts', 'energyPerInputJoules', 'operationalDispatchNanos')})
            values = phase_metrics(runs, replicate, test, powers)
            for phase_id, value in values.items():
                boot[phase_id].append(value)
        for phase_id, phase in center.items():
            for metric in ('latencyMS', 'serviceMS'):
                item = phase[metric]
                item['predictedCI'], item['measuredCI'], item['measuredWithinPredictedCI'] = {}, {}, {}
                for q in ('p50', 'p95', 'p99', 'slo'):
                    ci = interval([v[metric]['predicted'][q] for v in boot[phase_id]], len(fold['trainTrials']), confidence)
                    item['predictedCI'][q] = ci
                    item['measuredCI'][q] = interval([v[metric]['measured'][q] for v in boot[phase_id]], len(fold['testTrials']), confidence)
                    item['measuredWithinPredictedCI'][q] = ci[0] <= item['measured'][q] <= ci[1] if ci else None
            phase['latencyMS']['measuredSentToCompletedCI'] = {
                q: interval([v['latencyMS']['measuredSentToCompleted'][q] for v in boot[phase_id]],
                            len(fold['testTrials']), confidence) for q in ('p50', 'p95', 'p99', 'slo')}
            power = phase['power']
            power['predictedCIWatts'] = interval([v['power']['predictedWatts'] for v in boot[phase_id]], len(fold['trainTrials']), confidence)
            power['measuredCIWatts'] = interval([v['power']['measuredWatts'] for v in boot[phase_id]], len(fold['testTrials']), confidence)
            power['errorWatts'] = power['predictedWatts'] - power['measuredWatts']
            ci = power['predictedCIWatts']
            power['measuredWithinPredictedCI'] = ci[0] <= power['measuredWatts'] <= ci[1] if ci else None
        saturation = copy.deepcopy(full.get('saturation') or [])
        for row in saturation:
            row['predictedCycleMS'] = row['precedingServiceMS'] + evaluated['profile']['batch'] * evaluated['profile']['operationalDispatchNanos'] / 1e6
            row['errorMS'] = row['predictedCycleMS'] - row['measuredCycleMS']
            cycle_ci = interval([row['precedingServiceMS'] + evaluated['profile']['batch'] *
                                 p['operationalDispatchNanos'] / 1e6 for p in bootstrap_profiles],
                                len(fold['trainTrials']), confidence)
            row['predictedCycleCI'] = cycle_ci
            row['measuredWithinPredictedCI'] = cycle_ci[0] <= row['measuredCycleMS'] <= cycle_ci[1] if cycle_ci else None
            row['isTrainingEvidence'] = row['trialID'] in [runs[i]['podUID'] for i in fold['trainTrials']]
        phase_power = []
        for trial in full['trials']:
            for phase in trial['phasePower']:
                seconds = (evidence_io.timestamp_ns(phase['windowEnd']) - evidence_io.timestamp_ns(phase['windowStart'])) / 1e9
                rate = phase['completedItems'] / seconds
                watts = evaluated['profile']['idleWatts'] + evaluated['profile']['energyPerInputJoules'] * rate
                ci = interval([p['idleWatts'] + p['energyPerInputJoules'] * rate for p in bootstrap_profiles],
                              len(fold['trainTrials']), confidence)
                phase_power.append(dict(phase, completedRatePerSecond=rate, predictedWatts=watts,
                                        errorWatts=watts - phase['meanWallWatts'], predictedCIWatts=ci,
                                        measuredWithinPredictedCI=ci[0] <= phase['meanWallWatts'] <= ci[1] if ci else None,
                                        isTrainingEvidence=trial['trialID'] in [runs[i]['podUID'] for i in fold['trainTrials']]
                                        and not (fold['kind'] == 'phase' and phase['id'] == fold['id'])))
        report['folds'].append({'kind': fold['kind'], 'id': fold['id'],
                               'trainingTrialIDs': [runs[i]['podUID'] for i in fold['trainTrials']],
                               'heldoutTrialIDs': [runs[i]['podUID'] for i in fold['testTrials']],
                               'profile': evaluated['profile'], 'phases': list(center.values()),
                               'phasePower': phase_power, 'saturation': saturation})
    for raw in runs:
        extra = post.get(raw['podUID'], {})
        stopped = extra.get('loadStoppedAt', raw['runtime']['measurementEndedAt'])
        retained = extra.get('samples', raw['metrics']['samples'])
        result = stabilization(retained, stopped, uncertainty, rolling_seconds, final_seconds)
        result['trialID'] = raw['podUID']
        report['postLoadPower'].append(result)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--raw', action='append', required=True, help='active raw.json; repeat for independent trials at one fixed batch')
    parser.add_argument('--idle-raw', action='append', required=True, help='same-condition model-loaded idle raw.json')
    parser.add_argument('--go-cli', required=True, help='built hack/evaluation/profile-holdout executable')
    parser.add_argument('--post-load-power', help='JSON object keyed by active podUID: {loadStoppedAt,samples:[{observedAt,watts}]}')
    parser.add_argument('--meter-uncertainty-watts', type=float, required=True)
    parser.add_argument('--rolling-window-seconds', type=float, required=True)
    parser.add_argument('--final-idle-seconds', type=float, required=True)
    parser.add_argument('--bootstrap-replicates', type=int, default=200)
    parser.add_argument('--confidence', type=float, default=.95)
    parser.add_argument('--seed', type=int, default=1)
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    try:
        if args.bootstrap_replicates < 2 or not 0 < args.confidence < 1 or args.seed < 0:
            raise ValueError('positive seed, at least two bootstrap replicates and confidence in (0,1) required')
        for value, name in ((args.rolling_window_seconds, 'rolling window'), (args.final_idle_seconds, 'final idle window')):
            number(value, name, positive=True)
        if number(args.meter_uncertainty_watts, 'meter uncertainty') < 0:
            raise ValueError('meter uncertainty cannot be negative')
        runs, idle = [evidence_io.load(p) for p in args.raw], [evidence_io.load(p) for p in args.idle_raw]
        post = evidence_io.load(args.post_load_power) if args.post_load_power else {}
        with GoBridge(args.go_cli) as bridge:
            report = analyze(runs, idle, bridge, args.bootstrap_replicates, args.seed, args.confidence,
                             args.meter_uncertainty_watts, args.rolling_window_seconds, args.final_idle_seconds, post)
        report['sources'] = {'raw': [evidence_io.ref(p) for p in args.raw], 'idleRaw': [evidence_io.ref(p) for p in args.idle_raw],
                             'goCLI': evidence_io.ref(args.go_cli),
                             'postLoadPower': evidence_io.ref(args.post_load_power) if args.post_load_power else None}
        evidence_io.save(args.out, report)
    except (ValueError, KeyError, OSError, TypeError) as exc:
        parser.error(str(exc))


if __name__ == '__main__':
    main()

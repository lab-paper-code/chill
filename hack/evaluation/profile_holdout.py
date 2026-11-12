#!/usr/bin/env python3
"""C1(a): offline profiling holdouts using shared Go derivation; report only."""
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

ACTIVE_SCHEMA = 'chill.dacs.io/profiling-raw-measurement.v1alpha14'
IDLE_SCHEMA = 'chill.dacs.io/model-loaded-idle-raw-measurement.v1alpha8'




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
            raise ValueError('active raw v1alpha14 required')
        trial = raw['podUID']
        if not trial or trial in seen:
            raise ValueError('missing or duplicate physical trial ID')
        seen.add(trial)
        config, runtime = raw['runConfig'], raw['runtime']
        if runtime.get('schemaVersion') != 'chill.dacs.io/batch-characterization.v1alpha14':
            raise ValueError('runtime v1alpha14 required')
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
                     config.get('images', {}).get('runtime'), config.get('images', {}).get('artifact'),
                     runtime['runtimeVersion'], measurement,
                     [(p['id'], p['kind'], p.get('utilization')) for p in phases])
        if reference is not None and condition != reference:
            raise ValueError('raw trials must share execution, node, runtime/artifact images, runtimeVersion, measurement and phase conditions')
        reference = condition
        batch_ids, request_ids, phase_ids = set(), set(), set()
        post = runtime['postLoad']
        if (not post['modelResidentAtEnd'] or post['countersBefore'] != post['countersAfter'] or
                type(post['actorPID']) is not int or post['actorPID'] <= 0):
            raise ValueError('post-load must retain the same resident actor with zero inference')
        number(post['durationSecondsMonotonic'], 'post-load monotonic duration', positive=True)
        if (evidence_io.timestamp_ns(post['startedAt']) < evidence_io.timestamp_ns(runtime['measurementEndedAt']) or
                evidence_io.timestamp_ns(post['endedAt']) <= evidence_io.timestamp_ns(post['startedAt'])):
            raise ValueError('post-load window must follow measurement and be positive')
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
            gc = phase['driverGC']
            cycles = gc['cycles']
            total = number(gc['pauseTotalMilliseconds'], 'driver GC total pause')
            maximum = number(gc['maxPauseMilliseconds'], 'driver GC maximum pause')
            if (type(cycles) is not int or not 0 <= cycles <= 2**32 - 1 or
                    not 0 <= maximum <= total or (cycles == 0 and total != 0)):
                raise ValueError('driverGC cycles and pauses disagree')
            if phase['kind'] == 'saturation':
                if any(key in phase for key in ('scheduledInputs', 'sentInputs', 'arrivalSeed', 'issueLagMs',
                                               'issueElapsedSecondsMonotonic', 'achievedArrivalRatePerSecond')):
                    raise ValueError('saturation must omit Poisson issue evidence')
            if phase['kind'] != 'saturation':
                sent = phase['sentInputs']
                if (type(sent) is not int or phase['scheduledInputs'] != sent or
                        sent != phase['completedInputs']):
                    raise ValueError('scheduled, sent and completed input counts must agree')
                exposure = number(phase['issueElapsedSecondsMonotonic'], 'offering window', positive=True)
                if exposure > phase['measurementElapsedSecondsMonotonic']:
                    raise ValueError('offering window cannot include response drain')
                if phase['arrivalSeed'] != f"{trial}/{phase['id']}":
                    raise ValueError('arrivalSeed must identify physical trial/phase')
                if not math.isclose(number(phase['achievedArrivalRatePerSecond'], 'sent rate', positive=True),
                                    sent / exposure, rel_tol=1e-12):
                    raise ValueError('achieved rate must equal sent inputs / offering window')
                lag = phase['issueLagMs']
                values = [number(lag[key], 'issue lag') for key in ('min', 'p50', 'p95', 'p99', 'max')]
                mean = number(lag['mean'], 'mean issue lag')
                if (lag['count'] != sent or values[0] < 0 or values != sorted(values) or
                        not values[0] <= mean <= values[-1]):
                    raise ValueError('issue lag counts or statistics disagree')
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
                if not (evidence_io.timestamp_ns(request['arrivalAt']) <= evidence_io.timestamp_ns(request['sentAt'])
                        < evidence_io.timestamp_ns(request['completedAt'])):
                    raise ValueError('request schedule, actual send and completion must be ordered')
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
        # Bootstrap copies keep physical samples intact; distinct trial IDs
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
                              'measurementElapsedSecondsMonotonic': phase['measurementElapsedSecondsMonotonic'],
                              'nativeSamples': native[offset:offset + count],
                              **({key: phase[key] for key in ('sentInputs', 'issueElapsedSecondsMonotonic')}
                                 if phase['kind'] != 'saturation' else {})})
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


def postload_idle(runs, indices):
    result = []
    for draw, index in enumerate(indices):
        raw = runs[index]
        post = raw['runtime']['postLoad']
        result.append({'schemaVersion': IDLE_SCHEMA, 'podUID': f"{raw['podUID']}#postload{draw}",
                       'runtime': {'measurementStartedAt': post['startedAt'], 'measurementEndedAt': post['endedAt'],
                                   'inferenceCountAfterModelLoad': 0, 'modelResidentAtEnd': True},
                       'metrics': raw['metrics']})
    return result




class GoBridge:
    def __init__(self, executable, scheduler_settings=None):
        self.process = subprocess.Popen([str(Path(executable).resolve())], stdin=subprocess.PIPE,
                                        stdout=subprocess.PIPE, text=True)
        self.scheduler_settings = scheduler_settings

    def __enter__(self):
        return self

    def call(self, request):
        if self.scheduler_settings is not None:
            request = dict(request, schedulerSettings=self.scheduler_settings)
        self.process.stdin.write(json.dumps(request, allow_nan=False, separators=(',', ':')) + '\n')
        self.process.stdin.flush()
        line = self.process.stdout.readline()
        if not line:
            raise ValueError('Go derivation bridge exited without a result')
        result = json.loads(line)
        if 'error' in result:
            raise ValueError('Go derivation: ' + result['error'])
        return result

    def __exit__(self, *_):
        self.process.stdin.close()
        self.process.stdout.close()
        if self.process.wait() != 0:
            raise ValueError('Go derivation bridge failed')


def evaluate_fold(runs, idle, bridge, fold, train_indices):
    heldout = []
    for index in fold.get('testTrials', []):
        raw = runs[index]
        measurement = raw['runConfig']['measurement']
        for phase in raw['runtime']['phases']:
            if fold['kind'] == 'phase' and phase['id'] != fold['id']:
                continue
            heldout.append({'trialID': raw['podUID'], 'phaseID': phase['id'],
                            'arrivals': [r['sentAt'] for r in phase['requests']],
                            'sloPercentile': measurement['sloPercentile'],
                            'sloTargetLatencyNanos': measurement['sloTargetLatencyNanos'],
                            **measurement['operatingPoints']})
    support_runs = getattr(bridge, 'support_runs', [])
    support_indices = getattr(bridge, 'support_indices', list(range(len(support_runs))))
    support_trials = []
    for batch in sorted({r['runConfig']['measurement']['batch'] for r in support_runs}):
        indices = [i for i in support_indices if support_runs[i]['runConfig']['measurement']['batch'] == batch]
        if indices:
            support_trials.extend(training_trials(support_runs, {'kind': 'all'}, indices))
    return bridge.call({'trials': training_trials(runs, fold, train_indices),
                        'idle': idle_observations(postload_idle(runs, train_indices)), 'heldout': heldout,
                        'supportTrials': support_trials,
                        'supportIdle': idle_observations(postload_idle(support_runs, support_indices)) if support_indices else []})


def phase_metrics(runs, evaluated, selected, powers, fold):
    result = {}
    phases = [p for p in runs[0]['runtime']['phases']
              if fold['kind'] != 'phase' or p['id'] == fold['id']]
    for phase in phases:
        phase_id = phase['id']
        watts, rates, predicted, measured = [], [], [], []
        estimates = {(row['trialID'], row['phaseID']): row for row in evaluated.get('latency', [])}
        for index in selected:
            raw = runs[index]
            actual = next(p for p in raw['runtime']['phases'] if p['id'] == phase_id)
            rates.append(actual['completedInputs'] / actual['measurementElapsedSecondsMonotonic'])
            watts.append(powers[(raw['podUID'], phase_id)])
            estimate = estimates.get((raw['podUID'], phase_id), {})
            predicted.append(estimate.get('expectedNanos'))
            latencies = sorted(r['latencyMs'] * 1e6 for r in actual['requests'])
            percentile = raw['runConfig']['measurement']['sloPercentile']
            measured.append(latencies[math.ceil(percentile / 100 * len(latencies)) - 1])
        profile = evaluated['profile']
        rate = statistics.mean(rates)
        result[phase_id] = {'id': phase_id, 'kind': phase['kind'],
                           'latency': {'percentile': percentile,
                                       'predictedNanos': statistics.mean(predicted) if all(v is not None for v in predicted) else None,
                                       'unavailableReasons': sorted({reason for i in selected
                                                                    for reason in estimates.get((runs[i]['podUID'], phase_id), {}).get('reasons', [])
                                                                    if estimates.get((runs[i]['podUID'], phase_id), {}).get('expectedNanos') is None}),
                                       'measuredNanos': statistics.mean(measured)},
                           'power': {'measuredWatts': statistics.mean(watts), 'sentRatePerSecond': rate,
                                     'predictedWatts': profile['idleWatts'] + profile['energyPerInputJoules'] * rate}}
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


def analyze(runs, idle, bridge, repetitions, seed, confidence, uncertainty, rolling_seconds, final_seconds):
    if not runs:
        raise ValueError('raw evidence required')
    groups = {}
    reference = None
    seen = set()
    for raw in runs:
        if raw['podUID'] in seen:
            raise ValueError('duplicate physical trial ID')
        seen.add(raw['podUID'])
        config = copy.deepcopy(raw['runConfig'])
        batch = config['measurement'].pop('batch')
        condition = (config, raw['runtime']['runtimeVersion'])
        if reference is not None and condition != reference:
            raise ValueError('campaign batches must share execution, artifact, runtimeVersion and measurement conditions except batch')
        reference = condition
        groups.setdefault(batch, []).append(raw)
    reports = []
    for batch, selected in sorted(groups.items()):
        bridge.support_runs = [r for other, members in groups.items() if other != batch for r in members]
        bridge.support_indices = list(range(len(bridge.support_runs)))
        report = analyze_batch(selected, idle, bridge, repetitions, seed, confidence, uncertainty, rolling_seconds, final_seconds)
        report['batch'] = batch
        reports.append(report)
    if len(reports) == 1:
        return reports[0]
    return {'schema': 'profile-holdout-campaign-report.v1', 'batches': reports}


def analyze_batch(runs, idle, bridge, repetitions, seed, confidence, uncertainty, rolling_seconds, final_seconds):
    validate_runs(runs)
    for raw in idle:
        config = raw.get('runConfig', {})
        active = runs[0]['runConfig']
        if raw['runtime']['runtimeVersion'] != runs[0]['runtime']['runtimeVersion']:
            raise ValueError('idle evidence must share active runtimeVersion')
        for key in ('state', 'targetNode', 'images'):
            if key == 'targetNode':
                matches = config.get(key, {}).get('uid') == active.get(key, {}).get('uid')
            elif key == 'images':
                matches = all(config.get(key, {}).get(image) == active.get(key, {}).get(image)
                              for image in ('runtime', 'artifact'))
            else:
                matches = config.get(key) == active.get(key)
            if not matches:
                raise ValueError('idle evidence must share active execution, node and runtime/artifact image conditions')
    all_fold = {'kind': 'all', 'id': None}
    full = bridge.call({'trials': training_trials(runs, all_fold, list(range(len(runs)))),
                        'idle': idle_observations(postload_idle(runs, list(range(len(runs)))))})
    powers = {(trial['trialID'], phase['id']): phase['meanWallWatts']
              for trial in full['trials'] for phase in trial['phasePower']}
    separate_idle = (bridge.call({'trials': training_trials(runs, all_fold, list(range(len(runs)))),
                                  'idle': idle_observations(idle)})['profile']['idleWatts'] if idle else None)
    report = {'schema': 'profile-holdout-report.v3', 'confidenceLevel': confidence, 'bootstrapReplicates': repetitions,
              'seed': seed, 'bootstrapUnit': 'independent physical trial; no request or batch resampling',
              'separateIdleEstimateWatts': separate_idle,
              'analysisBoundary': 'offline task outside CHILL; existing exported derivation and scheduler APIs only',
              'replayExecuted': True,
              'retainedMetrics': ['held-out SLO percentile vs keep scheduler expectedNanos',
                                  'phase power vs idle + EPI * sent rate',
                                  'saturation cycle vs S + B * d', 'post-load power stabilization'],
              'removedMetrics': ['batch service distribution prediction'],
              'removedMetricsReason': 'existing product exports do not expose batch-service replay populations',
              'powerRateBasis': 'completed sent inputs / total measurement window; response drain included in both power and rate, including saturation',
              'limitations': ['One trial cannot identify between-trial uncertainty; its CI is null.',
                              'Stabilization stays in band only through the recorded suffix, not indefinitely.'],
              'folds': [], 'postLoadPower': []}
    for fold_index, fold in enumerate(folds(runs)):
        if fold['kind'] == 'phase' and len(runs[0]['runtime']['phases']) == 3:
            measured = statistics.mean(powers[(runs[i]['podUID'], fold['id'])] for i in fold['testTrials'])
            report['folds'].append({'kind': fold['kind'], 'id': fold['id'], 'profile': None,
                'trainingTrialIDs': [runs[i]['podUID'] for i in fold['trainTrials']],
                'heldoutTrialIDs': [runs[i]['podUID'] for i in fold['testTrials']],
                'unavailableReason': 'excluding the only utilization phase leaves no EPI training population',
                'phases': [{'id': fold['id'], 'power': {'measuredWatts': measured, 'predictedWatts': None}}]})
            continue
        bridge.support_indices = list(range(len(getattr(bridge, 'support_runs', []))))
        evaluated = evaluate_fold(runs, idle, bridge, fold, fold['trainTrials'])
        center = phase_metrics(runs, evaluated, fold['testTrials'], powers, fold)
        boot = {phase_id: [] for phase_id in center}
        bootstrap_profiles = []
        train_draws = bootstrap_indices(fold['trainTrials'], repetitions, seed + fold_index * 3)
        test_draws = train_draws if fold['kind'] == 'phase' else bootstrap_indices(
            fold['testTrials'], repetitions, seed + fold_index * 3 + 1)
        support_draws = []
        support_runs = getattr(bridge, 'support_runs', [])
        for batch in sorted({r['runConfig']['measurement']['batch'] for r in support_runs}):
            indices = [i for i, raw in enumerate(support_runs) if raw['runConfig']['measurement']['batch'] == batch]
            support_draws.append(bootstrap_indices(indices, repetitions, seed + fold_index * 3 + 2 + batch))
        for draw, (train, test) in enumerate(zip(train_draws, test_draws)):
            bridge.support_indices = [i for draws in support_draws for i in draws[draw]]
            replicate = evaluate_fold(runs, [], bridge, fold, train)
            bootstrap_profiles.append({key: replicate['profile'][key] for key in
                                       ('idleWatts', 'energyPerInputJoules', 'operationalDispatchNanos')})
            values = phase_metrics(runs, replicate, test, powers, fold)
            for phase_id, value in values.items():
                boot[phase_id].append(value)
        for phase_id, phase in center.items():
            power = phase['power']
            power['predictedCIWatts'] = interval([v['power']['predictedWatts'] for v in boot[phase_id]], len(fold['trainTrials']), confidence)
            power['measuredCIWatts'] = interval([v['power']['measuredWatts'] for v in boot[phase_id]], len(fold['testTrials']), confidence)
            power['errorWatts'] = (power['predictedWatts'] - power['measuredWatts']
                                   if power['predictedWatts'] is not None else None)
            ci = power['predictedCIWatts']
            power['measuredWithinPredictedCI'] = ci[0] <= power['measuredWatts'] <= ci[1] if ci else None
            latency = phase['latency']
            latency['predictedCINanos'] = interval([v['latency']['predictedNanos'] for v in boot[phase_id]], len(fold['trainTrials']), confidence)
            latency['measuredCINanos'] = interval([v['latency']['measuredNanos'] for v in boot[phase_id]], len(fold['testTrials']), confidence)
            latency['errorNanos'] = (latency['predictedNanos'] - latency['measuredNanos']
                                     if latency['predictedNanos'] is not None else None)
            latency['errorCINanos'] = interval(
                [v['latency']['predictedNanos'] - v['latency']['measuredNanos']
                 if v['latency']['predictedNanos'] is not None else None for v in boot[phase_id]],
                min(len(fold['trainTrials']), len(fold['testTrials'])), confidence)
            ci = latency['predictedCINanos']
            latency['measuredWithinPredictedCI'] = ci[0] <= latency['measuredNanos'] <= ci[1] if ci else None
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
                rate = phase['sentRatePerSecond']
                watts = (evaluated['profile']['idleWatts'] + evaluated['profile']['energyPerInputJoules'] * rate
                         if rate is not None else None)
                ci = (interval([p['idleWatts'] + p['energyPerInputJoules'] * rate for p in bootstrap_profiles],
                               len(fold['trainTrials']), confidence) if rate is not None else None)
                phase_power.append(dict(phase, predictedWatts=watts,
                                        errorWatts=watts - phase['meanWallWatts'] if watts is not None else None,
                                        predictedCIWatts=ci,
                                        measuredWithinPredictedCI=ci[0] <= phase['meanWallWatts'] <= ci[1] if ci else None,
                                        isTrainingEvidence=trial['trialID'] in [runs[i]['podUID'] for i in fold['trainTrials']]
                                        and not (fold['kind'] == 'phase' and phase['id'] == fold['id'])))
        report['folds'].append({'kind': fold['kind'], 'id': fold['id'],
                               'trainingTrialIDs': [runs[i]['podUID'] for i in fold['trainTrials']],
                               'heldoutTrialIDs': [runs[i]['podUID'] for i in fold['testTrials']],
                               'profile': evaluated['profile'], 'phases': list(center.values()),
                               'phasePower': phase_power, 'saturation': saturation})
    for raw in runs:
        window = raw['runtime']['postLoad']
        stopped = window['startedAt']
        retained = [p for p in raw['metrics']['samples']
                    if evidence_io.timestamp_ns(window['startedAt']) <= evidence_io.timestamp_ns(p['observedAt'])
                    <= evidence_io.timestamp_ns(window['endedAt'])]
        result = stabilization(retained, stopped, uncertainty, rolling_seconds, final_seconds)
        result['trialID'] = raw['podUID']
        report['postLoadPower'].append(result)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--raw', action='append', required=True, help='active raw.json; repeat for independent trials at one fixed batch')
    parser.add_argument('--idle-raw', action='append', default=[], help='separate-protocol idle estimate only; never pooled')
    parser.add_argument('--go-cli', required=True, help='built hack/evaluation/profile-holdout executable')
    parser.add_argument('--scheduler-settings', required=True, help='same-run frozen exported SchedulerSettings JSON')
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
        with GoBridge(args.go_cli, evidence_io.load(args.scheduler_settings)) as bridge:
            report = analyze(runs, idle, bridge, args.bootstrap_replicates, args.seed, args.confidence,
                             args.meter_uncertainty_watts, args.rolling_window_seconds, args.final_idle_seconds)
        report['sources'] = {'raw': [evidence_io.ref(p) for p in args.raw], 'idleRaw': [evidence_io.ref(p) for p in args.idle_raw],
                             'goCLI': evidence_io.ref(args.go_cli),
                             'schedulerSettings': evidence_io.ref(args.scheduler_settings)}
        evidence_io.save(args.out, report)
    except (ValueError, KeyError, OSError, TypeError) as exc:
        parser.error(str(exc))


if __name__ == '__main__':
    main()

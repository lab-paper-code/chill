#!/usr/bin/env python3
"""API raw export, strict source reduction and offline E5 row energy."""
import argparse
from bisect import bisect_right
import datetime as dt
import gzip
import hashlib
import json
import math
import os
from pathlib import Path
import runpy
import subprocess
import time
import urllib.request

HERE = Path(__file__).resolve().parent
ENVIRONMENT = json.loads(Path(os.environ['CHILL_EXPERIMENT_CONFIG']).read_text())
PACKAGE = Path(ENVIRONMENT['paths']['campaign'])
NODES = tuple(ENVIRONMENT['e5']['nodes'])
STREAMS = ('requests', 'management', 'scheduler', 'catalog', 'resources', 'cluster')
PRIVATE = 'PRIVATE_OPERATIONAL_ONLY'
RAW_PATH = '/api/v1/observations/raw'
_RAW = runpy.run_path(str(HERE / 'e5-raw.py'), run_name='e5_archive_time')
_time_ns = _RAW['_time']



def _utc(value):
    # Hour buckets only: preserve the real source instant without float rounding
    # into the next hour. Exact interval comparisons below use integer ns.
    stamp = _time_ns(value)
    return dt.datetime.fromtimestamp(stamp // 1000000000, dt.timezone.utc) + dt.timedelta(microseconds=(stamp % 1000000000) // 1000)


def _load(path):
    def invalid(value):
        raise ValueError('Nonfinite JSON value')
    return json.loads(Path(path).read_text(), parse_constant=invalid)


def _folder(folder):
    path = Path(folder)
    if not path.is_absolute():
        raise ValueError('Absolute private output folder required')
    path = path.resolve()
    if PACKAGE not in path.parents:
        raise ValueError('Output must be below the approved campaign/v2 directory')
    path.mkdir(parents=True, exist_ok=True)
    return path


def _open(path, mode='wb'):
    path = Path(path)
    if PACKAGE not in path.resolve().parents:
        raise ValueError('Output outside approved private package')
    path.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    os.fchmod(fd, 0o600)
    return os.fdopen(fd, mode)


def _save(path, data):
    with _open(path, 'w') as output:
        json.dump(data, output, indent=2, allow_nan=False)
        output.write('\n')


def _ref(path):
    digest = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    return {'path': str(Path(path).resolve()), 'bytes': Path(path).stat().st_size,
            'sha256': digest.hexdigest(), 'visibility': PRIVATE}


def _remaining(deadline):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise TimeoutError('Finite collection deadline exhausted')
    return remaining


def _hours(first, last):
    result = []
    while first <= last:
        result.append(first.strftime('%Y%m%dT%H'))
        first += dt.timedelta(hours=1)
    return result


def _raw_get(io, path, deadline):
    request = urllib.request.Request(
        ENVIRONMENT['d2']['apiBase'] + path,
        headers={'Authorization': 'Bearer ' + (Path(ENVIRONMENT['paths']['ops']) / '.token-collector').read_text().strip()})
    return io.http.open(request, timeout=_remaining(deadline))


def _download(folder, io, path, target, deadline, definitions=False):
    """Keep archived gzip bytes; gzip local JSONL only on the collector."""
    partial = target.with_suffix(target.suffix + '.partial')
    try:
        with _raw_get(io, path, deadline) as response:
            content_type = response.headers.get_content_type()
            if response.status != 200 or content_type not in (('application/x-ndjson',) if definitions else ('application/x-ndjson', 'application/gzip')):
                raise ValueError('Unexpected raw API response: ' + path)
            expected = response.headers.get('Content-Length')
            if expected is None or not expected.isdecimal():
                raise ValueError('Raw API response lacks exact byte length')
            copied = 0
            with _open(partial) as output:
                compressor = gzip.GzipFile(filename='', mode='wb', fileobj=output, mtime=0) if not definitions and content_type == 'application/x-ndjson' else None
                try:
                    while True:
                        _remaining(deadline)
                        block = response.read(1024 * 1024)
                        if not block:
                            break
                        copied += len(block)
                        (compressor or output).write(block)
                finally:
                    if compressor is not None:
                        compressor.close()
            if copied != int(expected):
                raise ValueError('Raw API response length mismatch: ' + path)
        if not definitions:
            with gzip.open(partial, 'rb') as source:
                for _ in iter(lambda: source.read(1024 * 1024), b''):
                    _remaining(deadline)
        if target.is_symlink():
            raise ValueError('Unsafe output symlink')
        partial.replace(target)
        return dict(_ref(target), origin='api', endpoint=path, contentType=content_type)
    finally:
        if partial.exists() and not partial.is_symlink():
            partial.unlink()


def _scope(folder):
    original = folder / 'pre-state.json'
    nodes = _load(original).get('snapshot', {}).get('nodes')
    if not isinstance(nodes, list):
        raise ValueError('Actual captured state snapshot nodes required')
    refs = [node.get('node') for node in nodes if isinstance(node, dict)]
    if len(refs) != len(nodes) or any(not isinstance(ref, dict) or not ref.get('name') or not ref.get('uid') for ref in refs):
        raise ValueError('State contains unidentified Node references')
    identities = [ref for ref in refs if ref['name'] in NODES]
    if len(identities) != len(NODES) or {ref['name'] for ref in identities} != set(NODES):
        raise ValueError('Actual state Node references do not cover exact normative eight')
    scope = {'visibility': PRIVATE, 'predeclaredNames': list(NODES), 'source': _ref(original),
             'identities': identities,
             'selection': 'Exact predeclared eight; original state Node references, no synthesized NodeList'}
    _save(folder / 'power-node-scope.json', scope)
    return scope, len(refs) != len(identities)


def energy_policy(collection):
    """E5 row-only gap allowance; never changes the recorded d/cost policy."""
    policy = dict(collection.get('energyCoverage', {}))
    policy.setdefault('maxGapFraction', .01)
    policy.setdefault('maxSingleGapSeconds', 30)
    if (type(policy['maxGapFraction']) not in (int, float) or not math.isfinite(policy['maxGapFraction'])
            or not 0 <= policy['maxGapFraction'] <= 1
            or type(policy['maxSingleGapSeconds']) not in (int, float)
            or not math.isfinite(policy['maxSingleGapSeconds']) or policy['maxSingleGapSeconds'] <= 0):
        raise ValueError('E5 energyCoverage requires 0<=maxGapFraction<=1 and finite maxSingleGapSeconds>0')
    return policy


def _energy_integral(points, start, end, active):
    """Integrate real-sample trapezoids at query and ownership boundaries."""
    times = [at for at, _ in points]
    accumulated = [0.]
    for (left, first), (right, last) in zip(points, points[1:]):
        accumulated.append(accumulated[-1] + (first + last) * .5 * ((right - left) / 1e9))
    def area(at):
        index = bisect_right(times, at) - 1
        if index == len(points) - 1:
            return accumulated[-1]
        left, first = points[index]
        right, last = points[index + 1]
        watts = first + (last - first) * ((at - left) / (right - left))
        return accumulated[index] + (first + watts) * .5 * ((at - left) / 1e9)
    return area(end) - area(start), sum(area(right) - area(left) for left, right in active)


def e5_power_windows(folder, collection, powers):
    """Re-evaluate stored row energy without modifying raw or strict Go results.

    Existing native source/identity/boundary errors remain authoritative. Only
    the strict maximum-gap verdict is replaced by the approved E5 row policy.
    """
    policy = energy_policy(collection)
    start = min(_time_ns(power['from']) for power in powers.values())
    end = max(_time_ns(power['to']) for power in powers.values())
    wanted = {(node['node']['uid'], node['meter']) for power in powers.values() for node in power.get('nodes', [])}
    histories = {key:{} for key in wanted}
    failures = {key:set() for key in wanted}
    meters, snapshots = {}, {}
    _, sources, errors = _RAW['_manifest'](Path(folder), {'scheduler', 'catalog'})
    for stream, row, reference in _RAW['_rows'](sources, errors):
        if stream == 'catalog':
            if row.get('kind') == 'meter':
                meters[row['key']] = row['body'].get('coverage') or {}
            continue
        snapshot = row.get('snapshot')
        if snapshot:
            at = _time_ns(snapshot['at'])
            if at <= end:
                snapshots[at] = {instance['node']['uid'] for instance in snapshot.get('instances') or []
                                 if instance.get('workloadUID') or instance.get('ownerWorkloadUID')}
        key = ((row.get('node') or {}).get('uid'), row.get('meter'))
        if key not in histories:
            continue
        history = histories[key]
        for sample in row.get('samples') or []:
            at, watts = _time_ns(sample['observedAt']), sample['watts']
            if start - 60e9 <= at <= end + 60e9:
                history[at] = watts
        for failure in row.get('failures') or []:
            at = _time_ns(failure['observedAt'])
            if start - 60e9 <= at <= end + 60e9:
                failures[key].add(at)
    ordered_snapshots = sorted(snapshots.items())
    result = {}
    gap_reason = 'maximum sample gap exceeds policy'
    for name, original in powers.items():
        value = dict(original)
        value['strictReasons'] = original.get('reasons', [])
        value['reasons'] = [reason for reason in original.get('reasons', []) if not reason.endswith(': ' + gap_reason)]
        value['nodes'] = []
        first, last = _time_ns(original['from']), _time_ns(original['to'])
        seconds = (last - first) / 1e9
        whole = active_total = error_bound = 0.
        intervals = {}
        for index, (at, members) in enumerate(ordered_snapshots):
            left = max(first, at)
            right = min(last, ordered_snapshots[index + 1][0] if index + 1 < len(ordered_snapshots) else last)
            if left < right:
                for uid in members:
                    intervals.setdefault(uid, []).append((left, right))
        for source in original.get('nodes', []):
            node = dict(source)
            node['reasons'] = [reason for reason in source.get('reasons', []) if reason != gap_reason]
            key = (source['node']['uid'], source['meter'])
            points = sorted(histories[key].items())
            times = [at for at, _ in points]
            lo, hi = bisect_right(times, first), bisect_right(times, last)
            points = points[max(0, lo - 1):min(len(points), hi + 1)]
            recorded = meters.get(source['meter'], {})
            threshold = recorded.get('MaximumGap')
            failed = sorted(failures[key])
            gaps = [(min(last, b) - max(first, a)) / 1e9
                    for (a, _), (b, _) in zip(points, points[1:])
                    if ((threshold is not None and b - a > threshold) or bisect_right(failed, a) < bisect_right(failed, b))
                    and min(last, b) > max(first, a)]
            inside = [watts for at, watts in points if first <= at <= last]
            gap_seconds, max_gap = sum(gaps), max(gaps, default=0.)
            range_watts = max(inside) - min(inside) if inside else None
            bound = gap_seconds * range_watts if range_watts is not None else (0. if not gaps else None)
            node['energyCoverage'] = {'gapSeconds':gap_seconds, 'maximumGapSeconds':max_gap,
                'gapFraction':gap_seconds / seconds, 'gapDetectionThresholdSeconds':threshold / 1e9 if threshold is not None else None,
                'observedMinimumWatts':min(inside) if inside else None, 'observedMaximumWatts':max(inside) if inside else None}
            node['gapErrorBoundJoules'] = bound
            if threshold is None or threshold <= 0 or bound is None or len(points) < 2:
                node['reasons'].append('E5 gap coverage lacks recorded policy or actual window samples')
            if gap_seconds / seconds > policy['maxGapFraction'] or max_gap > policy['maxSingleGapSeconds']:
                node['reasons'].append('E5 row energy gap allowance exceeded')
            node['joules'] = node['meanWatts'] = None
            if not node['reasons'] and not errors:
                # Native boundary checks already admitted these edge distances.
                if points[0][0] > first:
                    points.insert(0, (first, points[0][1]))
                if points[-1][0] < last:
                    points.append((last, points[-1][1]))
                joules, active_j = _energy_integral(points, first, last, intervals.get(source['node']['uid'], []))
                node.update(joules=joules, meanWatts=joules / seconds)
                whole += joules
                active_total += active_j
                error_bound += bound
            if node['reasons']:
                value['reasons'].append('Node '+source['node']['name']+': '+ '; '.join(node['reasons']))
            value['nodes'].append(node)
        if errors:
            value['reasons'].append('Preserved scheduler/catalog raw incomplete: '+json.dumps(errors))
        value['energyCoverage'] = {'policy':policy, 'meaning':'Per-Node clipped real-sample intervals containing recorded failures or exceeding recorded MaximumGap; linear interpolation, no synthetic source samples',
            'nodeGapSecondsSum':sum(node['energyCoverage']['gapSeconds'] for node in value['nodes']),
            'maximumGapSeconds':max((node['energyCoverage']['maximumGapSeconds'] for node in value['nodes']), default=0.),
            'maximumNodeGapFraction':max((node['energyCoverage']['gapFraction'] for node in value['nodes']), default=0.)}
        known = bool(value['nodes']) and not value['reasons'] and all(node['joules'] is not None for node in value['nodes'])
        value.update(complete=known, energyStatus='MEASURED' if known else 'UNKNOWN',
            wholePoolJoules=whole if known else None, activePoolJoules=active_total if known else None,
            wholePoolMeanWatts=whole / seconds if known else None, activePoolMeanWatts=active_total / seconds if known else None,
            gapErrorBoundJoules=error_bound if known else None)
        result[name] = value
    return result


def _reduce(folder, phase, executable, deadline, scope, include_configured):
    executable = Path(executable)
    if not executable.is_absolute() or not executable.is_file() or not os.access(executable, os.X_OK):
        raise ValueError('Actual prebuilt bound powerExecutable required; no runtime build')
    execution = {'visibility': PRIVATE, 'executable': _ref(executable), 'results': [], 'errors': []}
    expected = {(n['name'], n['uid']) for n in scope['identities']}
    for configured in (False, True) if include_configured else (False,):
        names = [] if configured else ['--node-names', ','.join(NODES)]
        for drain in (False, True):
            name = 'power' + ('-configured-whole-meter' if configured else '') + ('-drain-inclusive' if drain else '')
            target = folder / (name + '.json')
            end = phase['drainedUTC'] if drain else phase['endUTC']
            argv = [str(executable), '--dir', str(folder / 'obs'), '--settings', str(folder / 'pre-settings.json'),
                    '--state', str(folder / 'pre-state.json'), *names, '--from', phase['startUTC'], '--to', end]
            item = {'window': 'drain-inclusive' if drain else 'offered',
                    'scope': 'configured-whole-meter' if configured else 'normative-eight', 'argv': argv}
            try:
                result = subprocess.run(argv, capture_output=True, text=True, timeout=_remaining(deadline))
                item['exitCode'] = result.returncode
                with _open(folder / (name + '.stderr'), 'w') as output:
                    output.write(result.stderr)
                item['stderr'] = _ref(folder / (name + '.stderr'))
                if result.returncode:
                    raise RuntimeError('Existing source power reducer exited ' + str(result.returncode))
                power = json.loads(result.stdout, parse_constant=lambda value: (_ for _ in ()).throw(ValueError('Nonfinite result')))
                _save(target, power)
                item['result'] = _ref(target)
                actual = {(n.get('node', {}).get('name'), n.get('node', {}).get('uid')) for n in power.get('nodes', [])}
                if not configured and (actual != expected or len(power.get('nodes', [])) != len(NODES)):
                    raise ValueError('Power result does not cover exact normative eight identities')
                if _time_ns(power['from']) != _time_ns(phase['startUTC']) or _time_ns(power['to']) != _time_ns(end):
                    raise ValueError('Power reducer result interval mismatch')
                if power.get('complete') is not True:
                    raise ValueError('Actual power window incomplete: ' + json.dumps(power.get('reasons', [])))
                if any(power.get(key) is None for key in ('wholePoolJoules', 'wholePoolMeanWatts', 'activePoolJoules', 'activePoolMeanWatts')):
                    raise ValueError('Power result lacks actual whole/active integrals')
                item['complete'] = True
            except Exception as exc:
                item.update(complete=False, error=str(exc))
                if not configured:
                    execution['errors'].append(name + ': ' + str(exc))
            execution['results'].append(item)
    _save(folder / 'power-reduction.json', execution)
    return execution


def collect(folder, phase, collection, io, timeout_seconds):
    """Collect once after drain; return PRIVATE manifest, explicitly NOT_READY on gaps."""
    folder = _folder(folder)
    seconds = float(timeout_seconds)
    if not math.isfinite(seconds) or seconds <= 0:
        raise ValueError('Positive finite collection timeout required')
    deadline = time.monotonic() + seconds
    index = {'visibility': PRIVATE, 'complete': False, 'status': 'NOT_READY', 'sources': [], 'errors': [], 'absentStreams': [],
             'requestPopulation': {key: phase.get(key) for key in ('startUTC', 'endUTC', 'drainedUTC')}}
    manifest = {'visibility': PRIVATE, 'status': 'NOT_READY', 'errors': [], 'raw': [],
                'science': 'UNMEASURED; collection and reducer completeness are not E5 judgment'}
    if (folder / 'obs').exists() or (folder / 'raw-source-index.json').exists():
        manifest['errors'].append('Existing raw outputs: refuse stale-hour mixing or evidence overwrite')
        return manifest
    scope = None
    try:
        instants = [_time_ns(phase[key]) for key in ('startUTC', 'endUTC', 'drainedUTC')]
        if not instants[0] < instants[1] <= instants[2]:
            raise ValueError('Actual start < end <= drained interval required')
        start, end, drained = (_utc(phase[key]) for key in ('startUTC', 'endUTC', 'drainedUTC'))
        support = drained + dt.timedelta(minutes=1)
        support_ns = instants[2] + 60000000000
        index['powerSupportThroughUTC'] = support.strftime('%Y-%m-%dT%H:%M:%S') + '.{:09d}+00:00'.format(support_ns % 1000000000)
        # Only finite source support; never wait for a whole future archive hour.
        closed_ns = (support_ns // 60000000000 + 1) * 60000000000
        delay = (closed_ns - time.time_ns()) / 1000000000
        if delay > 0:
            if delay >= _remaining(deadline):
                raise TimeoutError('Closed to+1m support lies beyond finite collection deadline')
            time.sleep(delay)
        hour = lambda value: value.replace(minute=0, second=0, microsecond=0)
        first = hour(start) - dt.timedelta(hours=1)
        last = max(hour(support), min(hour(drained) + dt.timedelta(hours=1), hour(dt.datetime.now(dt.timezone.utc))))
        wanted = _hours(first, last)
        index['requestedHours'] = _hours(hour(start), hour(drained))
        index['supportingHours'] = [h for h in wanted if h not in index['requestedHours']]
        definitions = folder / 'obs/definitions.jsonl'
        index['sources'].append(dict(_download(folder, io, RAW_PATH + '/definitions', definitions, deadline, definitions=True),
                                     stream='definitions'))
        index['hours'] = []
        for h in wanted:
            try:
                with _raw_get(io, RAW_PATH + '?hour=' + h, deadline) as response:
                    inventory = json.load(response)
                if inventory.get('hour') != h or inventory.get('status') not in ('open', 'closed-local', 'archived'):
                    raise ValueError('Invalid exact-hour raw API inventory: ' + h)
                _time_ns(inventory.get('at'))
                kinds = inventory.get('kinds')
                if not isinstance(kinds, list) or len(kinds) != len(STREAMS) or {item.get('kind') for item in kinds if isinstance(item, dict)} != set(STREAMS):
                    raise ValueError('Incomplete raw API stream inventory: ' + h)
                index['hours'].append(inventory)
                for item in kinds:
                    kind = item['kind']
                    if type(item.get('available')) is not bool:
                        raise ValueError('Invalid raw API stream availability: ' + h + '/' + kind)
                    if not item['available']:
                        if kind == 'scheduler':
                            index['errors'].append('Required actual scheduler stream missing: ' + h)
                        else:
                            index['absentStreams'].append({
                                'visibility': PRIVATE, 'hour': h, 'stream': kind, 'origin': 'api',
                                'reason': 'source-hour-has-no-stream-segments'})
                        continue
                    target = folder / 'obs' / h / (kind + '.jsonl.gz')
                    source = _download(folder, io, RAW_PATH + '/' + h + '/' + kind, target, deadline)
                    index['sources'].append(dict(source, hour=h, stream=kind))
            except Exception as exc:
                index['errors'].append('Raw API hour ' + h + ': ' + str(exc))
        index['complete'] = not index['errors']
        index['status'] = 'CAPTURED_NOT_EVALUATED' if index['complete'] else 'NOT_READY'
        settings = _load(folder / 'pre-settings.json')
        if not isinstance(settings.get('meters'), list) or not settings['meters']:
            raise ValueError('Actual settings meters required')
        scope, configured = _scope(folder)
        reduction = _reduce(folder, phase, collection['powerExecutable'], deadline, scope, configured)
        manifest['errors'].extend(reduction['errors'])
        manifest['powerReduction'] = _ref(folder / 'power-reduction.json')
    except Exception as exc:
        manifest['errors'].append(str(exc))
        if not index['sources']:
            index['errors'].append('Raw capture incomplete: ' + type(exc).__name__)
    _save(folder / 'raw-source-index.json', index)
    manifest['errors'] = index['errors'] + manifest['errors']
    manifest['sourceIndex'] = _ref(folder / 'raw-source-index.json')
    manifest['raw'] = index['sources']
    if scope is not None:
        manifest['normativeScope'] = _ref(folder / 'power-node-scope.json')
    if not manifest['errors'] and index['complete']:
        manifest['status'] = 'CAPTURED_NOT_EVALUATED'
    _save(folder / 'archive-collection.json', manifest)
    return manifest


def _self_check():
    """Offline API-index regression; no credentials, live calls or builds."""
    entry = {'hour': '20261030T00', 'stream': 'requests', 'origin': 'api',
             'reason': 'source-hour-has-no-stream-segments'}
    index = {'absentStreams': [entry]}
    errors = []
    absent = _RAW['_observed_absences'](None, index, {'requests'}, {('20261030T00', 'scheduler')}, errors)
    assert not errors and absent[('20261030T00', 'requests')] == entry
    errors = []
    _RAW['_observed_absences'](None, index, {'requests'}, {('20261030T00', 'scheduler'), ('20261030T00', 'requests')}, errors)
    assert errors
    errors = []
    _RAW['_observed_absences'](None, index, {'requests'}, set(), errors)
    assert errors


def main():
    parser = argparse.ArgumentParser(description='Finite PRIVATE actual-input power re-reduction; no live calls/builds')
    parser.add_argument('--reduce-existing', type=Path, required=True)
    parser.add_argument('--power-executable', type=Path, required=True)
    parser.add_argument('--timeout-seconds', type=float, default=120)
    args = parser.parse_args()
    folder = _folder(args.reduce_existing)
    if not math.isfinite(args.timeout_seconds) or args.timeout_seconds <= 0:
        parser.error('Positive finite timeout required')
    index = _load(folder / 'raw-source-index.json')
    if index.get('complete') is not True:
        parser.error('Actual raw-source-index must be complete; no mock or synthetic inputs accepted')
    scope, configured = _scope(folder)
    result = _reduce(folder, _load(folder / 'phase.json'), args.power_executable,
                     time.monotonic() + args.timeout_seconds, scope, configured)
    print(json.dumps({'visibility': PRIVATE, 'status': 'NOT_READY' if result['errors'] else 'CAPTURED_NOT_EVALUATED',
                      'errors': result['errors']}))
    return 1 if result['errors'] else 0


if __name__ == '__main__':
    raise SystemExit(main())

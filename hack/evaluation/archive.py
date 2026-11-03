#!/usr/bin/env python3
"""Bounded raw HTTP/request collection and declared-meter energy projection."""
import datetime as dt
import gzip
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import time
import urllib.parse
import urllib.request

HERE = Path(__file__).resolve().parent
RAW_PATH = '/api/v1/observations/raw'
STREAMS = ('requests', 'resources')
PRIVATE = 'PRIVATE_OPERATIONAL_ONLY'


def _time_ns(value):
    spec = importlib.util.spec_from_file_location('archive_time', HERE / 'raw.py')
    raw = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(raw)
    return raw._time(value)


def _save(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, allow_nan=False) + '\n')


def _ref(path):
    digest = hashlib.sha256()
    with Path(path).open('rb') as source:
        for block in iter(lambda: source.read(1024 * 1024), b''):
            digest.update(block)
    return {'path': str(path), 'bytes': Path(path).stat().st_size, 'sha256': digest.hexdigest()}


def _remaining(deadline):
    seconds = deadline - time.monotonic()
    if seconds <= 0:
        raise TimeoutError('Finite collection deadline exhausted')
    return seconds


def _hours(first, last):
    result = []
    while first <= last:
        result.append(first.strftime('%Y%m%dT%H'))
        first += dt.timedelta(hours=1)
    return result


def _raw_get(io, path, deadline):
    request = urllib.request.Request(io.config['endpoint'].rstrip('/') + path,
                                     headers={'Authorization': 'Bearer ' + Path(io.collector_token_file).read_text().strip()})
    return io.http.open(request, timeout=_remaining(deadline))


def _download(folder, io, endpoint, target, deadline):
    if target.exists() or target.is_symlink() or folder not in target.resolve().parents:
        raise ValueError('Unsafe raw output path')
    target.parent.mkdir(parents=True, exist_ok=True)
    partial = target.with_suffix(target.suffix + '.partial')
    try:
        with _raw_get(io, endpoint, deadline) as response:
            kind = response.headers.get_content_type()
            length = response.headers.get('Content-Length')
            if response.status != 200 or kind not in ('application/x-ndjson', 'application/gzip') or length is None or not length.isdecimal():
                raise ValueError('Raw API requires HTTP 200, NDJSON/gzip and exact byte length')
            copied = 0
            with partial.open('xb') as output:
                compressor = gzip.GzipFile(filename='', mode='wb', fileobj=output, mtime=0) if kind == 'application/x-ndjson' else None
                try:
                    while True:
                        _remaining(deadline)
                        block = response.read(1024 * 1024)
                        if not block:
                            break
                        copied += len(block)
                        (compressor or output).write(block)
                finally:
                    if compressor:
                        compressor.close()
            if copied != int(length):
                raise ValueError('DataLoss: raw API byte length mismatch')
        with gzip.open(partial, 'rb') as source:
            for _ in iter(lambda: source.read(1024 * 1024), b''):
                _remaining(deadline)
        partial.replace(target)
        return {**_ref(target), 'origin': 'api', 'endpoint': endpoint, 'contentType': kind}
    finally:
        if partial.exists() and not partial.is_symlink():
            partial.unlink()


def energy_policy(collection):
    policy = collection['energyCoverage']
    if set(policy) != {'maxGapFraction', 'maxSingleGapSeconds'} or any(type(value) not in (int, float) or not math.isfinite(value) or value < 0 for value in policy.values()) or policy['maxGapFraction'] > 1:
        raise ValueError('FrozenInputInvalid: energy coverage policy')
    return policy


def meter_energy(window, measurement, policy, start, end):
    """No top-level completeness or ownership-dependent node fields are consumed."""
    if _time_ns(window['from']) != _time_ns(start) or _time_ns(window['to']) != _time_ns(end):
        raise ValueError('IdentityMismatch: power result interval')
    meters = {meter['id']: meter for meter in measurement['meters']}
    wanted = {(node['name'], node['uid']) for node in measurement['nodes']}
    if len(wanted) != len(measurement['nodes']) or not wanted:
        raise ValueError('FrozenInputInvalid: declared measurement nodes')
    selected, reasons = [], []
    for name, uid in sorted(wanted):
        candidates = [node for node in window.get('nodes', []) if node.get('node') == {'name': name, 'uid': uid} and node.get('meter') in meters]
        if len(candidates) != 1:
            reasons.append('Declared Node ' + name + ': expected exactly one declared meter')
            continue
        source = candidates[0]
        meter = meters[source['meter']]
        expected = json.loads(meter['sourceIdentity'])
        gaps = [source.get(key) for key in ('gapSeconds', 'gapFraction', 'maximumSampleGapSeconds')]
        joules = source.get('meterJoules')
        valid_numbers = all(type(x) in (int, float) and math.isfinite(x) and x >= 0 for x in gaps)
        attributable = (type(joules) in (int, float) and math.isfinite(joules) and joules >= 0
                        and source.get('sourceIdentity') == expected and expected.get('metric') == measurement['series']
                        and type(source.get('samples')) is int and source['samples'] > 0)
        policy_met = valid_numbers and gaps[1] <= policy['maxGapFraction'] and gaps[2] <= policy['maxSingleGapSeconds']
        complete = attributable and source.get('meterComplete') is True and valid_numbers and gaps[1] <= 1 and policy_met
        if not complete:
            reasons.append('Declared Node ' + name + ': partial/unavailable source or coverage below reporting policy')
        selected.append({'node': {'name': name, 'uid': uid}, 'meter': source['meter'], 'sourceIdentity': source.get('sourceIdentity'),
                         'joules': joules if attributable else None, 'reportedMeterJoules': joules,
                         'meterComplete': source.get('meterComplete'), 'policyMet': policy_met,
                         **{key: source.get(key) for key in ('gapSeconds', 'gapFraction', 'maximumSampleGapSeconds', 'samples', 'readFailures', 'firstObservedAt', 'lastObservedAt')}})
    complete = len(selected) == len(wanted) and not reasons
    observed = [node['joules'] for node in selected if node['joules'] is not None]
    return {'from': start, 'to': end, 'failurePolicy': 'v2', 'scope': 'declared-node-meters', 'nodes': selected, 'complete': complete,
            'energyStatus': 'MEASURED' if complete else 'PARTIAL' if observed else 'UNAVAILABLE',
            'observedJoules': sum(observed) if observed else None,
            'wholePoolJoules': sum(observed) if complete else None,
            'energyCoverage': {'policy': policy, 'nodeGapSecondsSum': sum(node.get('gapSeconds') or 0 for node in selected),
                               'maximumGapSeconds': max((node.get('maximumSampleGapSeconds') or 0 for node in selected), default=0),
                               'maximumNodeGapFraction': max((node.get('gapFraction') or 0 for node in selected), default=0)}, 'reasons': reasons}


def _collect_streams(folder, io, hours, streams, index, deadline):
    """One inventory/download path for final and provisional raw inputs."""
    for value in hours:
        with _raw_get(io, RAW_PATH + '?hour=' + value, deadline) as response:
            inventory = json.load(response)
        if inventory.get('hour') != value or inventory.get('status') not in ('open', 'closed-local', 'archived'):
            raise ValueError('Invalid exact-hour raw API inventory')
        kinds = {entry['kind']: entry for entry in inventory['kinds']}
        for stream in streams:
            entry = kinds[stream]
            if type(entry.get('available')) is not bool:
                raise ValueError('Invalid raw availability')
            if not entry['available']:
                index['absentStreams'].append({'visibility': PRIVATE, 'hour': value, 'stream': stream, 'origin': 'api', 'reason': 'source-hour-has-no-stream-segments'})
                continue
            target = folder / 'obs' / value / (stream + '.jsonl.gz')
            source = _download(folder, io, RAW_PATH + '/' + value + '/' + stream, target, deadline)
            index['sources'].append({**source, 'hour': value, 'stream': stream})


def collect(folder, phase, collection, io, timeout_seconds):
    folder = Path(folder).resolve()
    approved = Path(json.loads(Path(os.environ['CHILL_EXPERIMENT_CONFIG']).read_text())['paths']['campaign']).resolve()
    if approved not in folder.parents or folder.is_symlink():
        raise ValueError('Private campaign output directory required')
    if (folder / 'obs').exists() or (folder / 'raw-source-index.json').exists():
        raise ValueError('No stale raw collection mixing/overwrite')
    if type(timeout_seconds) not in (int, float) or not math.isfinite(timeout_seconds) or timeout_seconds <= 0:
        raise ValueError('Positive finite collection timeout required')
    maximum = collection.get('maximumDurationSeconds', timeout_seconds)
    if type(maximum) not in (int, float) or not math.isfinite(maximum) or maximum <= 0:
        raise ValueError('Positive finite collection duration bound required')
    deadline = time.monotonic() + min(timeout_seconds, maximum)
    first, end, drained = (_time_ns(phase[key]) for key in ('startUTC', 'endUTC', 'drainedUTC'))
    if not first < end <= drained:
        raise ValueError('Actual start < end <= drained interval required')
    index = {'visibility': PRIVATE, 'complete': False, 'sources': [], 'errors': [], 'absentStreams': [],
             'requestPopulation': {key: phase[key] for key in ('startUTC', 'endUTC', 'drainedUTC')}, 'supportingHours': []}
    manifest = {'visibility': PRIVATE, 'status': 'NOT_READY', 'errors': []}
    try:
        # The raw API exports closed minutes. Bound the wait; do not retry measurements.
        closed = ((drained // 60_000_000_000) + 2) * 60_000_000_000
        delay = (closed - time.time_ns()) / 1e9
        if delay > 0:
            if delay >= _remaining(deadline):
                raise TimeoutError('Closed drain support exceeds collection deadline')
            time.sleep(delay)
        hour = lambda stamp: dt.datetime.fromtimestamp(stamp / 1e9, dt.timezone.utc).replace(minute=0, second=0, microsecond=0)
        hours = _hours(hour(first), hour(drained))
        index['requestedHours'] = hours
        _collect_streams(folder, io, hours, STREAMS, index, deadline)
        index['complete'] = True
    except Exception as error:
        manifest['errors'].append(str(error))
        if not index['complete']:
            index['errors'].append(str(error))
    # Raw provenance and each energy window are independent collection results.
    measurement, policy = collection['powerMeasurement'], energy_policy(collection)
    manifest['energy'] = {}
    for name, stop in (('power', phase['endUTC']), ('power-drain-inclusive', phase['drainedUTC'])):
        try:
            query = urllib.parse.urlencode({'from': phase['startUTC'], 'to': stop})
            window = io.api('GET', '/api/v1/observations/power?' + query, timeout=min(timeout_seconds, maximum))
            _save(folder / (name + '-source.json'), window)
            result = meter_energy(window, measurement, policy, phase['startUTC'], stop)
        except Exception as error:
            result = {'from': phase['startUTC'], 'to': stop, 'failurePolicy': 'v2', 'energyStatus': 'UNAVAILABLE',
                      'complete': False, 'observedJoules': None, 'wholePoolJoules': None, 'nodes': [], 'reasons': [str(error)]}
        _save(folder / (name + '.json'), result)
        manifest['energy'][name] = result['energyStatus']
    _save(folder / 'raw-source-index.json', index)
    manifest.update(raw=index['sources'], sourceIndex=_ref(folder / 'raw-source-index.json'))
    manifest['failurePolicy'] = 'v2'
    if index['complete'] and not manifest['errors']:
        manifest['status'] = 'CAPTURED_NOT_EVALUATED'
    _save(folder / 'archive-collection.json', manifest)
    return manifest


def collect_requests_snapshot(folder, phase, io, timeout_seconds):
    """Capture closed-minute requests separately from immutable final evidence."""
    folder = Path(folder).resolve()
    deadline = time.monotonic() + timeout_seconds
    current = io.api('GET', RAW_PATH + '?hour=' + dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H'),
                     timeout=_remaining(deadline))
    closed = _time_ns(current['at']) // 60_000_000_000 * 60_000_000_000
    cutoff = dt.datetime.fromtimestamp(closed / 1e9, dt.timezone.utc).isoformat()
    if closed <= _time_ns(phase['startUTC']):
        raise ValueError('No closed stage minute available')
    hour = lambda stamp: dt.datetime.fromtimestamp(stamp / 1e9, dt.timezone.utc).replace(minute=0, second=0, microsecond=0)
    hours = _hours(hour(_time_ns(phase['startUTC'])), hour(closed))
    index = {'visibility': PRIVATE, 'complete': True, 'sources': [], 'errors': [], 'absentStreams': [],
             'requestPopulation': {'startUTC': phase['startUTC'], 'endUTC': cutoff, 'drainedUTC': cutoff},
             'requestedHours': hours, 'supportingHours': []}
    _collect_streams(folder, io, hours, ('requests',), index, deadline)
    # A minute rollover could mix cutoffs between inventory and download.
    final = io.api('GET', RAW_PATH + '?hour=' + hour(closed).strftime('%Y%m%dT%H'), timeout=_remaining(deadline))
    if _time_ns(final['at']) // 60_000_000_000 * 60_000_000_000 != closed:
        raise ValueError('Closed-minute snapshot changed during capture')
    _save(folder / 'raw-source-index.json', index)
    return cutoff

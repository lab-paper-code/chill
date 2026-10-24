#!/usr/bin/env python3
"""Private postdrain raw capture and the existing source OperatingPower CLI only."""
import argparse
import datetime as dt
import gzip
import hashlib
import json
import math
import os
from pathlib import Path, PurePosixPath
import re
import runpy
import shutil
import subprocess
import tarfile
import time
import zlib

HERE = Path(__file__).resolve().parent
ENVIRONMENT = json.loads(Path(os.environ['CHILL_EXPERIMENT_CONFIG']).read_text())
PACKAGE = Path(ENVIRONMENT['paths']['campaign'])
AWS = Path(ENVIRONMENT['e5']['awsExecutable'])
AWS_ENV = Path(ENVIRONMENT['e5']['awsEnvFile'])
NODES = tuple(ENVIRONMENT['e5']['nodes'])
STREAMS = ('requests', 'management', 'scheduler', 'catalog')
PRIVATE = 'PRIVATE_OPERATIONAL_ONLY'
HOUR = re.compile(r'\d{8}T\d{2}')
SEGMENT = re.compile(r'(requests|management|scheduler|catalog)\.([0-5][0-9])\.jsonl')
_time_ns = runpy.run_path(str(HERE / 'e5-raw.py'), run_name='e5_archive_time')['_time']

# Runs on the existing host Python, never inside the distroless container.
HOST_READER = r'''
import datetime as dt, io, json, os, pathlib, re, stat, subprocess, sys, tarfile

def command(argv):
    p = subprocess.run(argv, capture_output=True, text=True, timeout=20, check=True)
    return json.loads(p.stdout)

directory, hours = sys.argv[1], json.loads(sys.argv[2])
if not directory.startswith('/') or '..' in pathlib.Path(directory).parts:
    raise ValueError('Absolute observation directory required')
if not hours or any(not re.fullmatch(r'\d{8}T\d{2}', h) for h in hours):
    raise ValueError('Exact observation hours required')
containers = command(['/usr/bin/crictl', 'ps', '-o', 'json'])['containers']
found = [c for c in containers if c.get('metadata', {}).get('name') == 'serving-entrypoint'
         and c.get('labels', {}).get('io.kubernetes.pod.namespace') == 'chill-system']
if len(found) != 1:
    raise RuntimeError('Exact running host server container ambiguous')
c = found[0]
pid = command(['/usr/bin/crictl', 'inspect', c['id']])['info']['pid']
if int(pid) <= 0:
    raise RuntimeError('Invalid server PID')
root = pathlib.Path('/proc') / str(int(pid)) / 'root' / directory.lstrip('/') / 'v1alpha5'
meta = {'at': dt.datetime.now(dt.timezone.utc).isoformat(), 'containerID': c['id'],
        'podUID': c.get('labels', {}).get('io.kubernetes.pod.uid'), 'pid': int(pid),
        'directory': directory, 'visibility': 'PRIVATE_OPERATIONAL_ONLY',
        'retainedHours': [], 'missingHours': [], 'segments': [], 'errors': []}
opened = []
def retain(path, name):
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        f = os.fdopen(fd, 'rb')
        info = os.fstat(f.fileno())
        if not stat.S_ISREG(info.st_mode):
            f.close()
            raise ValueError('Not a regular retained file')
        opened.append((name, f, info.st_size))
        meta['segments'].append({'path': name, 'bytes': info.st_size})
    except (OSError, ValueError) as exc:
        meta['errors'].append(name + ': ' + type(exc).__name__)
retain(root / 'definitions.jsonl', 'definitions.jsonl')
for hour in hours:
    directory_path = root / hour
    if not directory_path.is_dir() or directory_path.is_symlink():
        meta['missingHours'].append(hour)
        continue
    meta['retainedHours'].append(hour)
    try:
        # Open every file before streaming so the normal archiver may prune safely.
        for path in sorted(directory_path.iterdir()):
            if re.fullmatch(r'(requests|management|scheduler|catalog)\.[0-5][0-9]\.jsonl', path.name):
                retain(path, hour + '/' + path.name)
    except OSError as exc:
        meta['errors'].append(hour + ': ' + type(exc).__name__)
try:
    with tarfile.open(fileobj=sys.stdout.buffer, mode='w|gz') as archive:
        data = json.dumps(meta).encode()
        item = tarfile.TarInfo('capture-identity.json')
        item.size = len(data)
        archive.addfile(item, io.BytesIO(data))
        for name, f, size in opened:
            item = tarfile.TarInfo(name)
            item.size = size
            item.mode = 0o600
            archive.addfile(item, f)
finally:
    for _, f, _ in opened:
        f.close()
'''


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


def _unpack(folder, archive_path, wanted):
    """Stream allowlisted regular members; never extract a path from a tar."""
    sources, seen, writers = [], set(), {}
    identity = None
    try:
        with tarfile.open(archive_path, mode='r|gz') as archive:
            for member in archive:
                name = member.name
                path = PurePosixPath(name)
                if not member.isreg() or path.is_absolute() or '..' in path.parts or name in seen:
                    raise ValueError('Unsafe or duplicate raw tar member')
                seen.add(name)
                stream = archive.extractfile(member)
                if name == 'capture-identity.json':
                    if identity is not None or member.size > 1024 * 1024:
                        raise ValueError('Invalid capture metadata')
                    identity = json.load(stream)
                    _save(folder / name, identity)
                elif name == 'definitions.jsonl':
                    target = folder / 'obs/definitions.jsonl'
                    with _open(target) as output:
                        shutil.copyfileobj(stream, output)
                    sources.append(dict(_ref(target), stream='definitions', origin='host-retained',
                                        captureIdentity=str(folder / 'capture-identity.json')))
                else:
                    if len(path.parts) != 2 or path.parts[0] not in wanted:
                        raise ValueError('Unexpected raw tar member')
                    match = SEGMENT.fullmatch(path.parts[1])
                    if not match:
                        raise ValueError('Unexpected retained stream name')
                    hour, kind = path.parts[0], match.group(1)
                    key = (hour, kind)
                    if key not in writers:
                        target = folder / 'obs' / hour / (kind + '.jsonl.gz')
                        output = _open(target)
                        compressor = gzip.GzipFile(filename='', mode='wb', fileobj=output, mtime=0)
                        writers[key] = (compressor, output, target, [])
                    compressor, _, _, segments = writers[key]
                    if segments and name <= segments[-1]['path']:
                        raise ValueError('Retained minute files not in source filename order')
                    shutil.copyfileobj(stream, compressor)
                    segments.append({'path': name, 'bytes': member.size})
    finally:
        for compressor, output, _, _ in writers.values():
            compressor.close()
            output.close()
    if identity is None:
        raise ValueError('Capture identity missing')
    for (hour, kind), (_, _, target, segments) in sorted(writers.items()):
        sources.append(dict(_ref(target), hour=hour, stream=kind, origin='host-retained',
                            retainedSegments=segments, captureIdentity=str(folder / 'capture-identity.json')))
    return identity, sources


def _list_hour(folder, hour, deadline):
    prefix = 'serving/observations.v1alpha5/' + hour + '/'
    script = ('set -a; . "$1"; set +a; '
              ': "${CHILL_S3_ENDPOINT:?}" "${CHILL_S3_BUCKET:?}"; '
              'exec "$2" s3api list-objects-v2 --endpoint-url "$CHILL_S3_ENDPOINT" '
              '--bucket "$CHILL_S3_BUCKET" --prefix "$3" --no-paginate --output json')
    try:
        result = subprocess.run(['/bin/bash', '-c', script, 'archive-list', str(AWS_ENV), str(AWS), prefix],
                                capture_output=True, text=True, timeout=_remaining(deadline))
        if result.returncode:
            raise RuntimeError('S3 exact-hour listing failed: ' + prefix + ' (exit ' + str(result.returncode) + ')')
        response = json.loads(result.stdout)
        proof = folder / ('s3-hour-list-' + hour + '.json')
        _save(proof, {'visibility': PRIVATE, 'prefix': prefix,
                      'at': dt.datetime.now(dt.timezone.utc).isoformat(), 'response': response})
        if response.get('IsTruncated') is not False:
            raise ValueError('S3 exact-hour listing is truncated or lacks completeness: ' + prefix)
        keys = {item['Key'] for item in response.get('Contents', [])}
        if prefix + 'scheduler.jsonl.gz' not in keys:
            raise ValueError('Required archived scheduler object absent: ' + prefix)
        return keys, str(proof)
    except (OSError, subprocess.SubprocessError) as exc:
        raise RuntimeError('S3 exact-hour listing failed: ' + prefix + ' (' + type(exc).__name__ + ')') from None


def _fetch(folder, hour, kind, deadline):
    key = 'serving/observations.v1alpha5/' + hour + '/' + kind + '.jsonl.gz'
    target = folder / 'obs' / hour / (kind + '.jsonl.gz')
    partial = target.with_suffix(target.suffix + '.partial')
    # Shell is local, only to reuse the already-installed credential setup. Never journal it.
    script = ('set -a; . "$1"; set +a; '
              ': "${CHILL_S3_ENDPOINT:?}" "${CHILL_S3_BUCKET:?}"; '
              'exec "$2" s3 cp --only-show-errors --endpoint-url "$CHILL_S3_ENDPOINT" '
              '"s3://$CHILL_S3_BUCKET/$3" -')
    try:
        with _open(partial) as output:
            result = subprocess.run(['/bin/bash', '-c', script, 'archive-fetch', str(AWS_ENV), str(AWS), key],
                                    stdout=output, stderr=subprocess.PIPE, timeout=_remaining(deadline))
        if result.returncode:
            raise RuntimeError('S3 object unavailable: ' + key + ' (exit ' + str(result.returncode) + ')')
        # Read the actual gzip through EOF/CRC, without parsing or rewriting any source row.
        with gzip.open(partial, 'rb') as source:
            for _ in iter(lambda: source.read(1024 * 1024), b''):
                pass
        if target.is_symlink():
            raise ValueError('Unsafe output symlink')
        partial.replace(target)
        return dict(_ref(target), hour=hour, stream=kind, origin='s3', objectKey=key,
                    immutableHourlyObject=True)
    except (OSError, EOFError, zlib.error, subprocess.SubprocessError) as exc:
        # TimeoutExpired includes argv: never expose the credential setup path.
        raise RuntimeError('S3 object fetch failed: ' + key + ' (' + type(exc).__name__ + ')') from None
    finally:
        if partial.exists() and not partial.is_symlink():
            partial.unlink()


def _scope(folder):
    original = folder / 'pre-nodes.json'
    nodes = _load(original)
    if nodes.get('kind') not in ('NodeList', 'List') or not isinstance(nodes.get('items'), list):
        raise ValueError('Actual captured Kubernetes NodeList/List envelope required')
    if nodes.get('apiVersion') != 'v1' or any(not isinstance(node, dict) or node.get('kind') not in (('Node',) if nodes['kind'] == 'List' else (None, 'Node')) or node.get('apiVersion') not in (('v1',) if nodes['kind'] == 'List' else (None, 'v1')) or not node.get('metadata', {}).get('name') or not node.get('metadata', {}).get('uid') for node in nodes['items']):
        raise ValueError('Captured Kubernetes list contains a non-Node/unknown-version or unidentified item')
    items = [node for node in nodes['items'] if node.get('metadata', {}).get('name') in NODES]
    identities = [(n.get('metadata', {}).get('name'), n.get('metadata', {}).get('uid')) for n in items]
    if len(items) != len(NODES) or {name for name, _ in identities} != set(NODES) or any(not uid for _, uid in identities):
        raise ValueError('Actual captured Node identities do not cover exact normative eight')
    subset = dict(nodes, items=items)
    target = folder / 'nodes-normative.json'
    _save(target, subset)
    scope = {'visibility': PRIVATE, 'predeclaredNames': list(NODES), 'source': _ref(original),
             'subset': _ref(target), 'identities': [{'name': name, 'uid': uid} for name, uid in identities],
             'selection': 'Exact predeclared eight; original captured Node objects, not fabricated Nodes'}
    _save(folder / 'power-node-scope.json', scope)
    return scope, len(nodes['items']) != len(items)


def _reduce(folder, phase, executable, deadline, scope, include_configured):
    executable = Path(executable)
    if not executable.is_absolute() or not executable.is_file() or not os.access(executable, os.X_OK):
        raise ValueError('Actual prebuilt bound powerExecutable required; no runtime build')
    execution = {'visibility': PRIVATE, 'executable': _ref(executable), 'results': [], 'errors': []}
    expected = {(n['name'], n['uid']) for n in scope['identities']}
    for configured in (False, True) if include_configured else (False,):
        node_file = folder / ('pre-nodes.json' if configured else 'nodes-normative.json')
        for drain in (False, True):
            name = 'power' + ('-configured-whole-meter' if configured else '') + ('-drain-inclusive' if drain else '')
            target = folder / (name + '.json')
            end = phase['drainedUTC'] if drain else phase['endUTC']
            argv = [str(executable), '--dir', str(folder / 'obs'), '--metric-sources', str(folder / 'metric-sources.yaml'),
                    '--nodes', str(node_file), '--from', phase['startUTC'], '--to', end]
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
        delay = (support_ns - time.time_ns()) / 1000000000
        if delay > 0:
            if delay >= _remaining(deadline):
                raise TimeoutError('to+1m support lies beyond finite collection deadline')
            time.sleep(delay)
        hour = lambda value: value.replace(minute=0, second=0, microsecond=0)
        first = hour(start) - dt.timedelta(hours=1)
        last = max(hour(support), min(hour(drained) + dt.timedelta(hours=1), hour(dt.datetime.now(dt.timezone.utc))))
        wanted = _hours(first, last)
        index['requestedHours'] = _hours(hour(start), hour(drained))
        index['supportingHours'] = [h for h in wanted if h not in index['requestedHours']]
        argv = collection['serverHostReadArgv']
        if not isinstance(argv, list) or not argv or not Path(argv[0]).is_absolute() or argv[-1] != '-':
            raise ValueError('Approved host privileged Python stdin argv required')
        raw = folder / 'observation-raw.tar.gz'
        with _open(raw) as output:
            captured = subprocess.run(argv + [collection['observationDirectory'], json.dumps(wanted)],
                                      input=HOST_READER.encode(), stdout=output, stderr=subprocess.PIPE,
                                      timeout=_remaining(deadline))
        # Host diagnostics contain no credential setup; keep them private as well.
        with _open(folder / 'raw-copy.stderr') as output:
            output.write(captured.stderr)
        if captured.returncode:
            raise RuntimeError('Privileged retained capture failed (exit ' + str(captured.returncode) + ')')
        identity, sources = _unpack(folder, raw, set(wanted))
        index['captureIdentity'] = _ref(folder / 'capture-identity.json')
        index['retainedArchive'] = _ref(raw)
        index['sources'].extend(sources)
        index['errors'].extend(identity.get('errors', []))
        present = {(s.get('hour'), s['stream']) for s in sources}
        captured_hour = hour(_utc(identity['at']))
        for h in wanted:
            retained = h in identity.get('retainedHours', [])
            keys, proof = set(), str(folder / 'capture-identity.json')
            if not retained:
                if dt.datetime.strptime(h, '%Y%m%dT%H').replace(tzinfo=dt.timezone.utc) >= captured_hour:
                    index['errors'].append('Required current retained hour missing: ' + h)
                    continue
                try:
                    keys, proof = _list_hour(folder, h, deadline)
                except Exception as exc:
                    index['errors'].append(str(exc))
                    continue
            for kind in STREAMS:
                if (h, kind) in present:
                    continue
                key = 'serving/observations.v1alpha5/' + h + '/' + kind + '.jsonl.gz'
                if not retained and key in keys:
                    try:
                        source = _fetch(folder, h, kind, deadline)
                        source['hourListing'] = proof
                        index['sources'].append(source)
                    except Exception as exc:
                        index['errors'].append(str(exc))
                elif kind == 'scheduler':
                    index['errors'].append('Required actual scheduler stream missing: ' + h)
                else:
                    # Source upload() emits no object for a stream with no minute files.
                    index['absentStreams'].append({
                        'visibility': PRIVATE, 'hour': h, 'stream': kind,
                        'origin': 'host-retained' if retained else 's3',
                        'reason': 'source-hour-has-no-stream-segments', 'proof': proof})
        if not any(s['stream'] == 'definitions' for s in sources):
            index['errors'].append('Actual global definitions.jsonl missing')
        index['complete'] = not index['errors']
        index['status'] = 'CAPTURED_NOT_EVALUATED' if index['complete'] else 'NOT_READY'
        facts_name = collection.get('metricSourcesConfigMap', 'chill-profiler-facts')
        facts_key = collection.get('metricSourcesKey', 'metric-sources.yaml')
        facts = io.k(['get', 'configmap', facts_name, '-o', 'json'], timeout=_remaining(deadline))
        yaml = facts.get('data', {}).get(facts_key)
        if not isinstance(yaml, str) or not yaml.strip():
            raise ValueError('Actual ConfigMap metric-sources data missing')
        with _open(folder / 'metric-sources.yaml', 'w') as output:
            output.write(yaml)
        _save(folder / 'metric-sources-provenance.json', {
            'visibility': PRIVATE, 'kind': 'ConfigMap', 'name': facts.get('metadata', {}).get('name'),
            'namespace': facts.get('metadata', {}).get('namespace'), 'uid': facts.get('metadata', {}).get('uid'),
            'resourceVersion': facts.get('metadata', {}).get('resourceVersion'), 'dataKey': facts_key,
            'value': _ref(folder / 'metric-sources.yaml')})
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

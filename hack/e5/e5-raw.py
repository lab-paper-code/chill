#!/usr/bin/env python3
"""PRIVATE_OPERATIONAL_ONLY: read archived v1alpha5 facts, never live APIs.

RequestRow, schedulerLine and CatalogRow follow internal/serving/observation_log*.go.
HTTP finals are returned verbatim; late native records never amend them. All refs
are one-based uncompressed JSONL line numbers in the immutable collected files.
"""
import argparse
from collections import Counter, defaultdict
from datetime import datetime, timedelta, timezone
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import sys

_PRIVATE = 'PRIVATE_OPERATIONAL_ONLY'
_REQUEST_CACHE = None
_SCHEDULER_CACHE = None
_STAMP = re.compile(r'^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$')
_EPOCH = datetime(1970, 1, 1, tzinfo=timezone.utc)


def _time(value):
    """Parse source RFC3339Nano without rounding cohort boundaries to floats."""
    match = _STAMP.fullmatch(value) if isinstance(value, str) else None
    if not match:
        raise ValueError('missing/invalid RFC3339Nano timestamp')
    base, fraction, zone = match.groups()
    instant = datetime.fromisoformat(base + ('+00:00' if zone == 'Z' else zone))
    delta = instant.astimezone(timezone.utc) - _EPOCH
    return (delta.days * 86400 + delta.seconds) * 1000000000 + int((fraction or '').ljust(9, '0'))


def _root(folder, approved_root=None):
    root = Path(folder).resolve()
    ops = Path(approved_root).resolve() if approved_root is not None else Path(json.loads(Path(os.environ['CHILL_EXPERIMENT_CONFIG']).read_text())['paths']['campaign'])
    if root != ops and ops not in root.parents:
        raise ValueError('archive folder is outside approved operational directory')
    return root


def _signature(root):
    try:
        stat = (root / 'raw-source-index.json').stat()
        return str(root), stat.st_mtime_ns, stat.st_size
    except OSError:
        return str(root), None, None


def _error(errors, message, ref=None):
    errors.append({'error': message, **(ref or {})})


def _observed_absences(root, index, streams, available, errors):
    """Accept omitted empty streams only from the collector's real inventories."""
    observed, proofs = {}, {}
    entries = index.get('absentStreams', [])
    if not isinstance(entries, list):
        _error(errors, 'absentStreams is not a list')
        return observed
    for entry in entries:
        if not isinstance(entry, dict):
            _error(errors, 'invalid absentStreams entry')
            continue
        stream = entry.get('stream')
        if stream not in streams:
            continue
        try:
            hour = entry.get('hour')
            if not isinstance(hour, str) or not re.fullmatch(r'\d{8}T\d{2}', hour):
                raise ValueError('observed absence has invalid hour')
            if stream == 'scheduler':
                raise ValueError('scheduler stream cannot be replaced by an absence proof')
            if (hour, stream) in available:
                raise ValueError('stream declared both present and absent')
            if (hour, 'scheduler') not in available:
                raise ValueError('absence proof lacks an actual scheduler source for its hour')
            if entry.get('reason') != 'source-hour-has-no-stream-segments':
                raise ValueError('observed absence lacks source-grounded reason')
            raw_path = Path(entry['proof'])
            path = raw_path.resolve()
            if not raw_path.is_absolute() or root not in path.parents:
                raise ValueError('absence proof must be an absolute file inside this archive')
            if path not in proofs:
                proofs[path] = json.loads(path.read_text(encoding='utf-8'))
            proof = proofs[path]
            if not isinstance(proof, dict):
                raise ValueError('absence inventory is not an object')
            if entry.get('origin') == 'host-retained':
                segments = proof.get('segments')
                if hour not in proof.get('retainedHours', []) or proof.get('errors') != []:
                    raise ValueError('retained-hour absence inventory is incomplete or reports errors')
                if not isinstance(segments, list) or any(not isinstance(s, dict) or not isinstance(s.get('path'), str) for s in segments):
                    raise ValueError('retained absence proof lacks a valid complete segment inventory')
                if any(s['path'].startswith(hour + '/' + stream + '.') for s in segments):
                    raise ValueError('retained inventory contains the purportedly absent stream')
            elif entry.get('origin') == 's3':
                prefix = 'serving/observations.v1alpha5/' + hour + '/'
                response = proof.get('response')
                if proof.get('prefix') != prefix or not isinstance(response, dict) or response.get('IsTruncated') is not False:
                    raise ValueError('S3 absence inventory is not a complete exact-hour listing')
                objects = response.get('Contents', [])
                if not isinstance(objects, list) or any(not isinstance(obj, dict) or not isinstance(obj.get('Key'), str) or not obj['Key'].startswith(prefix) for obj in objects):
                    raise ValueError('S3 absence proof has invalid object inventory')
                keys = {obj['Key'] for obj in objects}
                if prefix + 'scheduler.jsonl.gz' not in keys or prefix + stream + '.jsonl.gz' in keys:
                    raise ValueError('S3 listing does not prove stream absence alongside scheduler')
            else:
                raise ValueError('unknown absence inventory origin')
            observed[(hour, stream)] = dict(entry)
        except (KeyError, TypeError, OSError, ValueError) as exc:
            _error(errors, 'invalid observed stream absence: ' + str(exc), {'hour': entry.get('hour'), 'stream': stream, 'proof': entry.get('proof')})
    return observed


def _manifest(root, streams):
    errors, sources = [], []
    path = root / 'raw-source-index.json'
    try:
        index = json.loads(path.read_text(encoding='utf-8'))
        if not isinstance(index, dict):
            raise ValueError('raw source index is not an object')
    except (OSError, ValueError) as exc:
        _error(errors, str(exc), {'path': str(path)})
        return {}, sources, errors
    if index.get('complete') is not True:
        _error(errors, 'raw source index does not declare complete collection', {'path': str(path)})
    for key in ('errors', 'gaps'):
        values = index.get(key, [])
        if values:
            _error(errors, 'raw source index reports ' + key, {'path': str(path), 'details': values})
    entries = index.get('sources')
    if not isinstance(entries, list):
        _error(errors, 'raw source index sources is not a list', {'path': str(path)})
        entries = []
    seen = set()
    for entry in entries:
        if not isinstance(entry, dict):
            _error(errors, 'invalid raw source entry', {'path': str(path)})
            continue
        if entry.get('stream') not in streams | {'scheduler'}:
            continue
        try:
            raw_path = Path(entry['path'])
            source = raw_path.resolve()
            if not raw_path.is_absolute() or root / 'obs' not in source.parents:
                raise ValueError('raw source must be an absolute path inside this archive obs directory')
            hour, stream = entry.get('hour'), entry['stream']
            if not isinstance(hour, str) or not re.fullmatch(r'\d{8}T\d{2}', hour):
                raise ValueError('missing/invalid raw source hour')
            if source != root / 'obs' / hour / (stream + '.jsonl.gz'):
                raise ValueError('raw source path does not match its stream/hour')
            stat = source.stat()
            if not source.is_file():
                raise ValueError('raw source is not a regular file')
            if not isinstance(entry.get('bytes'), int) or entry['bytes'] != stat.st_size:
                raise ValueError('raw source byte count differs from collection index')
            if str(source) in seen:
                continue
            seen.add(str(source))
            sources.append(dict(entry))
        except (KeyError, TypeError, OSError, ValueError) as exc:
            _error(errors, str(exc), {'path': str(entry.get('path', path))})
    hours = []
    for key in ('requestedHours', 'supportingHours'):
        values = index.get(key)
        if not isinstance(values, list) or (key == 'requestedHours' and not values):
            _error(errors, 'missing/invalid ' + key, {'path': str(path)})
            continue
        for hour in values:
            if not isinstance(hour, str) or not re.fullmatch(r'\d{8}T\d{2}', hour):
                _error(errors, 'invalid required hour in ' + key, {'path': str(path)})
            else:
                hours.append(hour)
    available = {(s['hour'], s['stream']) for s in sources}
    absent = _observed_absences(root, index, streams, available, errors)
    for hour in sorted(set(hours)):
        for stream in sorted(streams | {'scheduler'}):
            if (hour, stream) not in available and (hour, stream) not in absent:
                _error(errors, 'missing required ' + stream + ' hourly stream or authenticated absence proof', {'hour': hour})
    index['_observedAbsences'] = list(absent.values())
    sources = [source for source in sources if source['stream'] in streams]
    if not sources and not absent:
        _error(errors, 'no archived streams or proven observed absences available', {'path': str(path)})
    return index, sorted(sources, key=lambda s: (s['hour'], s['stream'], s['path'])), errors


def _reject_constant(value):
    raise ValueError('non-JSON numeric constant ' + value)


def _rows(sources, errors):
    for source in sources:
        ref = {'path': source['path']}
        try:
            with gzip.open(source['path'], 'rt', encoding='utf-8') as handle:
                for number, text in enumerate(handle, 1):
                    ref = {'path': source['path'], 'line': number}
                    try:
                        row = json.loads(text, parse_constant=_reject_constant)
                        if not isinstance(row, dict):
                            raise ValueError('raw JSONL record is not an object')
                    except (ValueError, RecursionError) as exc:
                        _error(errors, 'JSONL parse: ' + str(exc), ref)
                        continue
                    yield source['stream'], row, ref
        except (OSError, EOFError, UnicodeError) as exc:
            _error(errors, 'reading gzip stream: ' + str(exc), ref)


def _digest(row):
    return hashlib.sha256(json.dumps(row, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode('utf-8')).digest()


def _new_request():
    return {'rows': 0, 'kinds': Counter(), 'refs': {}, 'finals': {},
            'received': {}, 'nativeOutcomes': Counter(), 'nativeRefs': {}}


def _population(index, start, end, errors):
    population = index.get('requestPopulation')
    try:
        if not isinstance(population, dict):
            raise ValueError('missing requestPopulation provenance')
        captured_start = _time(population.get('startUTC'))
        captured_end = _time(population.get('endUTC'))
        drained = _time(population.get('drainedUTC'))
        if not captured_start <= start < end <= captured_end or drained < captured_end:
            raise ValueError('requested interval is not contained in the collected receivedAt population/drain')
        # Stored hour is append time, not receipt time: include every hour through drain.
        for stamp in (captured_start, drained):
            if stamp < 0:
                raise ValueError('invalid operational population timestamp')
        first = _EPOCH + timedelta(seconds=captured_start // 1000000000)
        last = _EPOCH + timedelta(seconds=drained // 1000000000)
        hour = first.replace(minute=0, second=0, microsecond=0)
        required = set(index.get('requestedHours', [])) | set(index.get('supportingHours', []))
        while hour <= last:
            if hour.strftime('%Y%m%dT%H') not in required:
                raise ValueError('collection hour coverage does not extend from receipt start through actual drain')
            hour += timedelta(hours=1)
    except (ValueError, TypeError, OverflowError) as exc:
        _error(errors, str(exc))


def _request_indexes(folder, uids, startUTC, endUTC, actual_response_ids=None):
    global _REQUEST_CACHE
    root = _root(folder)
    start, end = _time(startUTC), _time(endUTC)
    if start >= end:
        raise ValueError('request interval must be nonempty [startUTC,endUTC)')
    wanted = None if uids is None else frozenset(uids)
    if wanted is not None and any(not isinstance(uid, str) or not uid for uid in wanted):
        raise ValueError('workload UIDs must be nonempty strings')
    additions = {} if actual_response_ids is None else {uid: frozenset(ids) for uid, ids in actual_response_ids.items()}
    if any(not isinstance(uid, str) or not uid or any(not isinstance(rid, str) or not rid for rid in ids) for uid, ids in additions.items()):
        raise ValueError('Actual returned workload/request IDs must be nonempty strings')
    key = (_signature(root), start, end, frozenset(additions.items()))
    if _REQUEST_CACHE is not None:
        previous, scope, results, _, _, _ = _REQUEST_CACHE
        if previous == key and (scope is None or wanted is not None and wanted <= scope):
            return results
    index, sources, errors = _manifest(root, {'requests'})
    _population(index, start, end, errors)
    groups = defaultdict(dict)
    seen, totals = set(), Counter()
    unkeyed, uid_errors = defaultdict(list), defaultdict(list)
    boundary_matches = defaultdict(dict)
    for _, row, ref in _rows(sources, errors):
        uid = row.get('workloadUID')
        if not isinstance(uid, str) or not uid:
            _error(errors, 'request row has missing/invalid workloadUID', ref)
            continue
        if wanted is not None and uid not in wanted:
            continue
        totals[uid] += 1
        kind, rid = row.get('kind'), row.get('requestID')
        if actual_response_ids is not None and rid not in additions.get(uid, ()):
            continue
        if kind != 'native':
            try:
                received = _time(row.get('receivedAt'))
                if actual_response_ids is None and not start <= received < end:
                    continue
                if actual_response_ids is not None:
                    if received <= 0:
                        raise ValueError('Exact returned-ID HTTP row has nonpositive receivedAt')
                    if not start <= received < end:
                        boundary_matches[uid].setdefault(rid, {'requestID': rid, 'receivedAt': row['receivedAt'], 'ref': ref})
            except ValueError as exc:
                _error(uid_errors[uid], 'HTTP row receivedAt: ' + str(exc), ref)
                continue
        signature = _digest(row)
        if signature in seen:
            totals[(uid, 'duplicates')] += 1
            continue
        seen.add(signature)
        if not isinstance(rid, str) or not rid:
            unkeyed[uid].append({'kind': kind, **ref})
            _error(uid_errors[uid], 'request row has missing/invalid requestID', ref)
            continue
        request = groups[uid].get(rid)
        if request is None:
            request = groups[uid][rid] = _new_request()
        request['rows'] += 1
        if not isinstance(kind, str):
            _error(uid_errors[uid], 'request row has missing/invalid kind', ref)
            kind = '<invalid>'
        request['kinds'][kind] += 1
        request['refs'].setdefault(kind, ref)
        if kind == 'native':
            outcome = row.get('nativeOutcome')
            if outcome is not None and not isinstance(outcome, str):
                _error(uid_errors[uid], 'nativeOutcome is not a string', ref)
            elif outcome:
                request['nativeOutcomes'][outcome] += 1
                request['nativeRefs'].setdefault(outcome, ref)
            # Source deliberately has zero receivedAt here; never treat it as HTTP receipt.
            continue
        request['received'].setdefault(received, ref)
        if kind == 'final':
            request['finals'][signature] = (row, ref)
        elif kind != 'assigned':
            _error(uid_errors[uid], 'unexpected archived HTTP request kind ' + repr(kind), ref)
    results = {}
    for uid in set(groups) | set(unkeyed) | set(uid_errors) | set(wanted or ()):
        finals, conflicts, unfinished, native_only = {}, [], [], []
        kinds, outcomes, http_errors, execution_complete = Counter(), Counter(), Counter(), Counter()
        native_outcomes, native_ids = Counter(), defaultdict(list)
        final_refs, native_rows, known_requests, population_rows = {}, 0, 0, 0
        for rid, request in groups[uid].items():
            kinds.update(request['kinds'])
            native_rows += request['kinds']['native']
            native_outcomes.update(request['nativeOutcomes'])
            for outcome in request['nativeOutcomes']:
                native_ids[outcome].append(rid)
            refs = [{'kind': kind, **ref} for kind, ref in request['refs'].items()]
            if not request['received']:
                native_only.append({'requestID': rid, 'rows': request['rows'],
                                    'nativeOutcomes': dict(request['nativeOutcomes']), 'refs': refs,
                                    'nativeOutcomeRefs': request['nativeRefs']})
                continue
            known_requests += 1
            population_rows += request['rows']
            candidates = list(request['finals'].values())
            ambiguous = len(candidates) > 1 or len(request['received']) > 1
            if ambiguous:
                conflict = {'requestID': rid, 'finalRefs': [ref for _, ref in candidates],
                            'receivedAtRefs': list(request['received'].values())}
                conflicts.append(conflict)
                _error(uid_errors[uid], 'conflicting HTTP final/receipt facts', {'requestID': rid, 'refs': conflict})
            if len(candidates) == 1 and not ambiguous:
                row, ref = candidates[0]
                finals[rid], final_refs[rid] = row, ref
                if isinstance(row.get('outcome'), str) and row['outcome']:
                    outcomes[row['outcome']] += 1
                else:
                    _error(uid_errors[uid], 'HTTP final has missing/invalid outcome', ref)
                if type(row.get('elapsedNanos')) is not int or row['elapsedNanos'] < 0:
                    _error(uid_errors[uid], 'HTTP final has missing/invalid elapsedNanos', ref)
                if row.get('error') is not None:
                    if isinstance(row['error'], str):
                        http_errors[row['error']] += 1
                    else:
                        _error(uid_errors[uid], 'HTTP final error is not a string', ref)
                # Absence is unknown here, not a fabricated false/zero fact.
                if 'executionComplete' in row:
                    if isinstance(row['executionComplete'], bool):
                        execution_complete[str(row['executionComplete']).lower()] += 1
                    else:
                        _error(uid_errors[uid], 'HTTP final executionComplete is not boolean', ref)
            else:
                unfinished.append({'requestID': rid, 'kinds': dict(request['kinds']),
                                   'refs': refs, 'nativeOutcomes': dict(request['nativeOutcomes']),
                                   'nativeOutcomeRefs': request['nativeRefs']})
        own_errors = errors + uid_errors[uid]
        summary = {'classification': _PRIVATE, 'rawRows': totals[uid],
                   'populationUniqueRows': population_rows, 'duplicateRows': totals[(uid, 'duplicates')],
                   'rowCountMeaning': 'rawRows scans UID across captured hours before receipt filtering; populationUniqueRows is deduped selected HTTP rows plus their linked native rows; kinds/native outcomes additionally retain unknown-receipt native-only rows.',
                   'kinds': dict(kinds), 'uniqueRequests': known_requests,
                   'uniqueFinalRows': len(finals), 'outcomes': dict(outcomes),
                   'errors': dict(http_errors), 'executionComplete': dict(execution_complete),
                   'completed': outcomes['completed'],
                   'withoutUnambiguousHTTPFinal': known_requests - len(finals),
                   'finalConflicts': conflicts, 'unkeyedRows': unkeyed[uid],
                   'unfinished': unfinished, 'unresolvedNativeOnly': native_only,
                   'nativeSupplementRows': native_rows, 'nativeOutcomes': dict(native_outcomes),
                   'nativeOutcomeRequestIDs': {k: sorted(v) for k, v in native_ids.items()},
                   'observedAbsences': index.get('_observedAbsences', []),
                   'population': {'workloadUID': uid, 'startUTC': startUTC, 'endUTC': endUTC,
                                  'predicate': 'receivedAt >= startUTC and receivedAt < endUTC' if actual_response_ids is None else 'Exact actual responseIDs returned for originally offered tasks; separate from the unchanged half-open receivedAt cohort',
                                  'actualResponseIDBoundaryMatches': list(boundary_matches[uid].values()),
                                  'nativeOnlyMeaning': 'HTTP receipt unknown; separately unfinished, not promoted by native completion'},
                   'complete': not own_errors,
                   'completenessMeaning': 'complete covers archive provenance and parsing; withoutUnambiguousHTTPFinal/unfinished and unresolvedNativeOnly separately expose unsettled HTTP/unknown-receipt identities.',
                   'timingMeaning': 'HTTP final fields are unmodified; native supplements are separate physical facts, never replacements.'}
        results[uid] = {'finals': finals, 'finalRefs': final_refs, 'summary': summary,
                        'sources': sources, 'errors': own_errors}
    _REQUEST_CACHE = key, wanted, results, sources, errors, index.get('_observedAbsences', [])
    return results


def iter_request_indexes(folder, uids, startUTC, endUTC, actual_response_ids=None):
    """Index all supplied UIDs in one streaming pass; return UID -> result."""
    wanted = tuple(uids)
    results = _request_indexes(folder, wanted, startUTC, endUTC, actual_response_ids)
    return {uid: results[uid] for uid in wanted}


def index_requests(folder, uid, startUTC, endUTC, actual_response_ids=None):
    """Return actual unambiguous HTTP finals and compact raw population evidence.

    Repeated per-UID calls for the same folder/interval reuse one all-UID scan.
    iter_request_indexes restricts retained identities when UIDs are known upfront.
    """
    if not isinstance(uid, str) or not uid:
        raise ValueError('workload UID must be a nonempty string')
    results = _request_indexes(folder, None if actual_response_ids is None else (uid,), startUTC, endUTC, None if actual_response_ids is None else {uid: actual_response_ids})
    if uid not in results:
        # An empty UID still inherits provenance/parse errors, not empty-success.
        _, _, _, sources, errors, absences = _REQUEST_CACHE
        return {'finals': {}, 'finalRefs': {}, 'sources': sources, 'errors': errors,
                'summary': {'classification': _PRIVATE, 'rawRows': 0, 'kinds': {},
                            'uniqueRequests': 0, 'uniqueFinalRows': 0, 'outcomes': {},
                            'finalConflicts': [], 'unkeyedRows': [], 'unfinished': [],
                            'unresolvedNativeOnly': [], 'withoutUnambiguousHTTPFinal': 0,
                            'observedAbsences': absences,
                            'complete': not errors}}
    return results[uid]


def _catalog_refs(value):
    if isinstance(value, dict):
        for field, item in value.items():
            kind = {'profileID': 'profile', 'definitionID': 'definition', 'meter': 'meter'}.get(field)
            if kind and isinstance(item, str) and item:
                yield kind, item
            list_kind = {'workloadIDs': 'definition', 'costRefs': 'cost', 'baselineRefs': 'baseline'}.get(field)
            if list_kind and isinstance(item, list):
                for key in item:
                    if isinstance(key, str) and key:
                        yield list_kind, key
            yield from _catalog_refs(item)
    elif isinstance(value, list):
        for item in value:
            yield from _catalog_refs(item)


def scheduler_events(folder, *, approved_root=None):
    """Return raw schedulerLine dictionaries with refs, never expanded catalog bodies."""
    global _SCHEDULER_CACHE
    root = _root(folder, approved_root)
    signature = _signature(root)
    if _SCHEDULER_CACHE is not None and _SCHEDULER_CACHE[0] == signature:
        return _SCHEDULER_CACHE[1]
    index, sources, errors = _manifest(root, {'scheduler', 'catalog'})
    catalog, seen, events = {}, {}, []
    raw_counts = Counter()
    duplicates = 0
    for stream, row, ref in _rows(sources, errors):
        raw_counts[stream] += 1
        try:
            _time(row.get('at'))
            if stream == 'catalog':
                kind, key = row.get('kind'), row.get('key')
                if not isinstance(kind, str) or not kind or not isinstance(key, str) or not key or 'body' not in row:
                    raise ValueError('catalog row requires kind, key, at and body')
                identity = kind, key
                stamp = _time(row['at'])
                digest = _digest({'body': row['body']})
                versions = catalog.setdefault(identity, {})
                if stamp in versions and versions[stamp][0] != digest:
                    _error(errors, 'conflicting catalog body at the same kind/key/time', ref)
                else:
                    versions.setdefault(stamp, (digest, ref))
                continue
            if '_rawRefs' in row:
                raise ValueError('reserved _rawRefs field already exists in raw scheduler record')
            digest = _digest(row)
            if digest in seen:
                duplicates += 1
                events[seen[digest]]['_rawRefs'].append(ref)
            else:
                seen[digest] = len(events)
                events.append({**row, '_rawRefs': [ref]})
        except (ValueError, RecursionError) as exc:
            _error(errors, 'invalid ' + stream + ' record: ' + str(exc), ref)
    missing = defaultdict(list)
    for event in events:
        for identity in set(_catalog_refs(event)):
            if identity not in catalog:
                missing[identity].extend(event['_rawRefs'])
    for (kind, key), refs in sorted(missing.items()):
        _error(errors, 'missing referenced catalog body', {'kind': kind, 'key': key, 'refs': refs})
    provenance = {'classification': _PRIVATE, 'complete': not errors,
                  'sources': sources, 'errors': errors, 'rawRows': dict(raw_counts),
                  'uniqueEvents': len(events), 'duplicateSchedulerRows': duplicates,
                  'catalogReferences': [{'kind': kind, 'key': key,
                                         'refs': [ref for _, ref in versions.values()]}
                                        for (kind, key), versions in sorted(catalog.items())],
                  'requestPopulation': index.get('requestPopulation'),
                  'observedAbsences': index.get('_observedAbsences', []),
                  'meaning': 'Raw schedulerLine fields retained; _rawRefs identifies original records. Catalog bodies remain in archived catalog streams; no profiles or power results expanded/synthesized.'}
    result = events, provenance
    _SCHEDULER_CACHE = signature, result
    return result


def _main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('kind', choices=('requests', 'scheduler'))
    parser.add_argument('--folder', required=True)
    parser.add_argument('--uid', action='append', default=[])
    parser.add_argument('--start')
    parser.add_argument('--end')
    args = parser.parse_args()
    if args.kind == 'requests':
        if not args.uid or not args.start or not args.end:
            parser.error('requests requires --uid (repeatable), --start and --end')
        results = iter_request_indexes(args.folder, args.uid, args.start, args.end)
        output = {uid: {key: result[key] for key in ('summary', 'sources', 'errors')}
                  for uid, result in results.items()}
        bad = any(result['errors'] for result in results.values())
    else:
        events, output = scheduler_events(args.folder)
        output = {**output, 'returnedEvents': len(events)}
        bad = bool(output['errors'])
    json.dump(output, sys.stdout, indent=2, ensure_ascii=False)
    sys.stdout.write('\n')
    return 2 if bad else 0


if __name__ == '__main__':
    sys.exit(_main())

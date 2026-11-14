"""Scientific profile/coverage captures and preservation diagnostics."""
import json
from decimal import Decimal
from pathlib import Path
import shutil
import time
import urllib.parse

import api_checks


def _numeric(value):
    if type(value) not in (int, Decimal) or not Decimal(value).is_finite():
        raise ValueError('Measurement fields must be finite JSON numbers')
    return Decimal(value)


def _estimate(value):
    if not isinstance(value['status'], str):
        raise ValueError('Missing estimate status')
    if value['status'] == 'Identified':
        _numeric(value['value'])
    for bound in value['range']:
        _numeric(bound)


def _index(rows, field):
    if not isinstance(rows, list):
        raise ValueError('Expected an array of ' + field + ' identities')
    result = {}
    for row in rows:
        key = row[field]
        if field == 'id' and (not isinstance(key, str) or not key):
            raise ValueError('Profile/cell ID must be a nonempty string')
        if field == 'batch' and (type(key) is not int or key < 1):
            raise ValueError('Batch identity must be a positive integer')
        if key in result:
            raise ValueError('Duplicate ' + field + ' identity')
        result[key] = row
    return result


def _profiles(snapshot):
    envelope = snapshot['profiles']
    rows = _index(envelope['profiles'], 'id')
    page = envelope.get('page')
    if page and (page.get('offset') != 0 or page.get('total') != len(rows)
                 or page.get('nextOffset') is not None):
        raise ValueError('Incomplete paginated profile snapshot')
    for profile in rows.values():
        if not isinstance(profile['assessment']['displayStatus'], str):
            raise ValueError('Missing profile display status')
        for field in ('idleWatts', 'awakeStepWatts', 'holdSeconds'):
            _estimate(profile['power'][field])
        batches = _index(profile['batches'], 'batch')
        for batch in batches.values():
            _estimate(batch['inputSlopeJoules'])
            for field in ('saturationInputRate', 'memoryBytes'):
                _numeric(batch[field])
    return rows


def _coverage(snapshot):
    coverage = snapshot['coverage']
    usable = coverage['summary']['countsByDisplayStatus']['usable']
    if usable is not None and (type(usable) is not int or usable < 0):
        raise ValueError('Coverage usable count must be null or a nonnegative integer')
    sources = coverage.get('sources', [])
    if not isinstance(sources, list):
        raise ValueError('Coverage sources must be an array')
    for source in sources:
        if (not isinstance(source, dict) or type(source.get('complete')) is not bool
                or (source.get('error') is not None and not isinstance(source['error'], dict))):
            raise ValueError('Malformed measurement coverage source')
    return usable


def _sources_complete(snapshot):
    return all(source['complete'] and source.get('error') is None
               for source in snapshot['coverage'].get('sources', []))


def capture(config, namespace, out, timeout=60):
    """Capture the complete unpaginated profile catalog and namespaced coverage."""
    if not isinstance(namespace, str) or not namespace:
        raise ValueError('Coverage namespace required')
    out = Path(out)
    snapshot = {
        'coverage': api_checks.request_json(config, '/api/v1/namespaces/' + urllib.parse.quote(namespace, safe='')
                                 + '/measurement-coverage', out / 'coverage.json', timeout),
        'profiles': api_checks.request_json(config, '/api/v1/scheduler/profiles', out / 'profiles.json', timeout),
    }
    try:
        _profiles(snapshot)
        _coverage(snapshot)
    except (KeyError, TypeError, ValueError) as error:
        api_checks._report(out / 'snapshot-status.json', {'valid': False, 'error': str(error)})
        raise ValueError('Invalid acceptance snapshot: ' + str(error)) from None
    api_checks._report(out / 'snapshot-status.json', {
        'valid': True, 'coverageSourcesComplete': _sources_complete(snapshot),
        'coverageSources': snapshot['coverage'].get('sources', [])})
    return snapshot


def wait_capture(config, namespace, out, deadline):
    """Wait only for startup within the supplied monotonic readiness deadline."""
    attempt = 0
    out = Path(out)
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise RuntimeError('API readiness/coverage warming deadline exceeded')
        attempt_out = out / ('attempt-%03d' % attempt)
        try:
            # Include setup and validation in the shared elapsed budget, not just sockets.
            with api_checks._elapsed_deadline(min(120, remaining)):
                snapshot = capture(config, namespace, attempt_out, timeout=min(60, remaining / 2))
            for name in ('coverage.json', 'profiles.json', 'coverage.http-status',
                         'profiles.http-status', 'snapshot-status.json'):
                shutil.copy2(attempt_out / name, out / name)
            print('[readiness] attempt=%d ready remaining=%.1fs' %
                  (attempt + 1, max(0, deadline - time.monotonic())), flush=True)
            return snapshot
        except api_checks.ReadinessPending as error:
            print('[readiness] attempt=%d pending=%s remaining=%.1fs' %
                  (attempt + 1, error, max(0, deadline - time.monotonic())), flush=True)
            attempt += 1
            time.sleep(min(5, max(0, deadline - time.monotonic())))


def load_baseline(directory):
    """Read a retained public API baseline without rewriting its numeric data."""
    directory = Path(directory)
    snapshot = {name: json.loads((directory / (name + '.json')).read_text(), parse_float=Decimal,
                                  parse_constant=api_checks._invalid_constant)
                for name in ('profiles', 'coverage')}
    _profiles(snapshot)
    _coverage(snapshot)
    return snapshot


def compare(before, after, allowed_lost_ids=(), out=None, expected_coverage_usable=None,
            expected_profile_count=None):
    """Report scientific preservation diagnostics independently of readiness."""
    report = {'checks': {}, 'errors': []}
    try:
        if not isinstance(allowed_lost_ids, (list, tuple, set)) or any(
                not isinstance(value, str) or not value for value in allowed_lost_ids):
            raise ValueError('allowed_lost_ids must contain profile ID strings')
        old, new = _profiles(before), _profiles(after)
        lost, added = set(old) - set(new), set(new) - set(old)
        allowed = set(allowed_lost_ids)
        changes, unusable = [], []
        for identity in sorted(set(old) & set(new)):
            a, b = old[identity], new[identity]
            if a['assessment']['displayStatus'] != 'usable' or b['assessment']['displayStatus'] != 'usable':
                unusable.append(identity)
            if not _same_record(a['power'], b['power']):
                changes.append({'id': identity, 'field': 'power',
                                'before': a['power'], 'after': b['power']})
            aa, bb = _index(a['batches'], 'batch'), _index(b['batches'], 'batch')
            if set(aa) != set(bb):
                changes.append({'id': identity, 'error': 'batch identities changed'})
                continue
            for batch in aa:
                for field in ('inputSlopeJoules', 'saturationInputRate', 'memoryBytes'):
                    if not _same_record(aa[batch][field], bb[batch][field]):
                        changes.append({'id': identity, 'batch': batch, 'field': field,
                                        'before': aa[batch][field], 'after': bb[batch][field]})
        report.update({'before': len(old), 'after': len(new), 'actualLost': sorted(lost),
                       'allowedLost': sorted(allowed), 'unexpected': sorted(added),
                       'numericChanges': changes, 'notUsable': unusable})
        old_usable, new_usable = _coverage(before), _coverage(after)
        report.update({'coverageUsableBefore': old_usable, 'coverageUsableAfter': new_usable,
                       'coverageSourcesBefore': before['coverage'].get('sources', []),
                       'coverageSourcesAfter': after['coverage'].get('sources', [])})
        report['checks']['coverageSourcesComplete'] = _sources_complete(before) and _sources_complete(after)
        if old_usable is None or new_usable is None:
            raise ValueError('Coverage usable count is unknown; scientific comparison unavailable')
        if expected_coverage_usable is not None and (
                type(expected_coverage_usable) is not int or expected_coverage_usable < 0):
            raise ValueError('expected_coverage_usable must be a nonnegative integer')
        coverage_ok = (new_usable >= old_usable if expected_coverage_usable is None
                       else new_usable == expected_coverage_usable)
        report['expectedCoverageUsable'] = expected_coverage_usable
        report['checks'].update({'profileIdentitiesPreserved': lost == allowed and not added,
                                 'batchMeasurementsPreserved': not changes,
                                 'profilesUsable': not unusable, 'coverageUsablePreserved': coverage_ok})
    except (KeyError, TypeError, ValueError) as error:
        report['errors'].append(str(error))
    report['passed'] = bool(report['checks']) and all(report['checks'].values()) and not report['errors']
    report['checks']['expectedProfileCount'] = (
        expected_profile_count is None or report.get('after') == expected_profile_count)
    report['passed'] = report['passed'] and report['checks']['expectedProfileCount']
    if out is not None:
        api_checks._report(Path(out) / 'acceptance.json', report)
    return report


def accept(config, namespace, baseline, out, allowed_lost_ids=(),
           expected_coverage_usable=None, timeout=60):
    after = capture(config, namespace, out, timeout)
    return compare(baseline, after, allowed_lost_ids, out, expected_coverage_usable)


def _same_record(a, b):
    if type(a) in (int, Decimal) and type(b) in (int, Decimal):
        return _numeric(a) == _numeric(b)
    if type(a) is not type(b):
        return False
    if isinstance(a, dict):
        return a.keys() == b.keys() and all(_same_record(a[key], b[key]) for key in a)
    if isinstance(a, list):
        return len(a) == len(b) and all(_same_record(x, y) for x, y in zip(a, b))
    return a == b


def compare_records(before, after, out=None):
    """Report retained-record drift; authorized migrations may intentionally differ."""
    report = {'passed': False, 'recordChanges': [], 'errors': []}
    required = ('reference', 'evidence', 'nodeName', 'condition', 'power',
                'measuredAt', 'metricSource')
    optional = ('runtimeMetadata', 'reuse')
    try:
        old, new = _profiles(before), _profiles(after)
        report['lost'] = sorted(set(old) - set(new))
        report['added'] = sorted(set(new) - set(old))
        for identity in sorted(set(old) & set(new)):
            a, b = old[identity], new[identity]
            for field in required:
                if not _same_record(a[field], b[field]):
                    report['recordChanges'].append({'id': identity, 'field': field})
            for field in optional:
                if (field in a) != (field in b) or not _same_record(a.get(field), b.get(field)):
                    report['recordChanges'].append({'id': identity, 'field': field})
            aa, bb = _index(a['batches'], 'batch'), _index(b['batches'], 'batch')
            if not _same_record(aa, bb):
                report['recordChanges'].append({'id': identity, 'field': 'batches'})
        report['passed'] = not (report['lost'] or report['added'] or report['recordChanges'])
    except (KeyError, TypeError, ValueError) as error:
        report['errors'].append(str(error))
    if out is not None:
        api_checks._report(Path(out) / 'record-preservation.json', report)
    return report

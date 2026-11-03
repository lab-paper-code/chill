"""Optimistic public settings patches and path-scoped restoration receipts."""
import copy
from pathlib import Path

import evidence_io


SETTINGS = '/api/v1/scheduler/settings'
# GET projects these read-only fields beside the writable SchedulerSettings.
SETTINGS_PROJECTIONS = {'staticRouting', 'staticRoutingValidatedAt', 'powerObservation', 'meters', 'requestTimeout'}


def merge_patch(current, patch):
    """RFC 7396: objects recurse, arrays/scalars replace, null deletes keys."""
    if not isinstance(patch, dict):
        return copy.deepcopy(patch)
    result = copy.deepcopy(current) if isinstance(current, dict) else {}
    for key, value in patch.items():
        if value is None:
            result.pop(key, None)
        else:
            result[key] = merge_patch(result.get(key), value)
    return result


def path_value(value, path):
    for key in path:
        if not isinstance(value, dict):
            return {'exists': False, 'blockedBy': copy.deepcopy(value)}
        if key not in value:
            return {'exists': False}
        value = value[key]
    return {'exists': True, 'value': copy.deepcopy(value)}


def patch_changes(before, after, patch, prefix=(), created_parents=()):
    """Track patched members, including members of newly created objects."""
    changed = []
    for key, value in patch.items():
        path = (*prefix, key)
        left, right = path_value(before, (key,)), path_value(after, (key,))
        if isinstance(value, dict) and (not left['exists'] or isinstance(before.get(key), dict)):
            parents = (*created_parents, path) if not left['exists'] else created_parents
            members = patch_changes(before.get(key, {}), after[key], value, path, parents)
            if members:
                changed.extend(members)
                continue
            if left == right:
                continue
        if left != right:
            changed.append({'path': list(path), 'before': left, 'applied': right,
                            'createdParents': [list(parent) for parent in created_parents]})
    return changed


def restore_paths(current, changed):
    result = copy.deepcopy(current)
    for change in changed:
        target = result
        for key in change['path'][:-1]:
            if key not in target:
                target[key] = {}
            if not isinstance(target[key], dict):
                raise RuntimeError('ControlConflict: patched settings parent changed')
            target = target[key]
        key, original = change['path'][-1], change['before']
        if original['exists']:
            target[key] = copy.deepcopy(original['value'])
        else:
            target.pop(key, None)
    parents = {tuple(parent) for change in changed for parent in change['createdParents']}
    for path in sorted(parents, key=len, reverse=True):
        if path_value(result, path) == {'exists': True, 'value': {}}:
            target = result
            for key in path[:-1]:
                target = target[key]
            target.pop(path[-1])
    return result


def settings_get(api):
    metadata = {}
    current = api('GET', SETTINGS, response=metadata)
    etag = metadata.get('etag')
    if metadata.get('statusCode') != 200 or not isinstance(etag, str) or len(etag) < 3 or not etag.startswith('"') or not etag.endswith('"'):
        raise RuntimeError('ObservationUnavailable: public settings GET lacks HTTP 200 and quoted ETag')
    return current, metadata


def settings_put(api, request, etag, attempt):
    attempt.update(ifMatch=etag, request=request, requestedAt=evidence_io.now())
    metadata = {}
    try:
        value = api('PUT', SETTINGS, request, headers={'If-Match': etag}, response=metadata)
        if metadata.get('statusCode') != 200 or not metadata.get('etag') or not isinstance(value, dict) or not isinstance(value.get('after'), dict) or not value.get('appliedAt'):
            raise RuntimeError('RemoteEffectUnknown: settings PUT lacks application/ETag evidence')
        return value
    finally:
        attempt.update(statusCode=metadata.get('statusCode'), responseETag=metadata.get('etag'), response=metadata.get('body'))


def factor_apply(api, declared, path):
    """Apply exactly once with the operator's public optimistic concurrency API."""
    path = Path(path)
    if path.exists():
        raise RuntimeError('ControlConflict: settings receipt already exists')
    if not isinstance(declared, dict) or set(declared) & SETTINGS_PROJECTIONS:
        raise ValueError('FrozenInputInvalid: settingsPatch requires writable SchedulerSettings fields')
    current, metadata = settings_get(api)
    writable = {key: value for key, value in current.items() if key not in SETTINGS_PROJECTIONS}
    request = merge_patch(writable, declared)
    changed = patch_changes(current, request, declared)
    receipt = {'settingsPatch': declared, 'before': current, 'beforeETag': metadata['etag'], 'changed': changed,
               'request': request, 'requestedAt': evidence_io.now(), 'effectUncertain': bool(changed), 'put': {}}
    evidence_io.save(path, receipt)
    if changed:
        receipt['put'] = {'ifMatch': metadata['etag'], 'request': request, 'requestedAt': evidence_io.now()}
        evidence_io.save(path, receipt)
        try:
            receipt['response'] = settings_put(api, request, metadata['etag'], receipt['put'])
            receipt['applied'] = receipt['response']['after']
            for change in changed:
                change['applied'] = path_value(receipt['applied'], change['path'])
            receipt['appliedAt'] = receipt['response']['appliedAt']
            receipt['effectUncertain'] = False
        except Exception as error:
            rejected = receipt['put'].get('statusCode') in (400, 401, 403, 404, 405, 409, 422, 428)
            receipt.update(error=str(error), failedAt=evidence_io.now(), effectUncertain=not rejected)
            evidence_io.save(path, receipt)
            raise
        evidence_io.save(path, receipt)
    receipt['readback'], readback_meta = settings_get(api)
    receipt['readbackETag'] = readback_meta['etag']
    receipt['accepted'] = all(path_value(receipt['readback'], change['path']) == change['applied'] for change in changed)
    evidence_io.save(path, receipt)
    if not receipt['accepted']:
        raise RuntimeError('ControlConflict: public settings patch readback differs')
    return receipt


def factor_revert(api, path):
    path = Path(path)
    if not path.exists():
        return
    receipt = evidence_io.load(path)
    if receipt.get('effectUncertain'):
        raise RuntimeError('RemoteEffectUnknown: settings PUT outcome not confirmed; no rollback or retry')
    if receipt.get('revertedAt') or not receipt.get('appliedAt'):
        return
    attempts = receipt.setdefault('revertAttempts', [])
    if attempts and attempts[-1].get('statusCode') != 409:
        if attempts[-1].get('statusCode') != 200:
            raise RuntimeError('RemoteEffectUnknown: prior settings revert outcome unconfirmed')
        # Successful PUT already has server-side application evidence. Readback only;
        # never repeat it after a lost local write or an external subsequent change.
        receipt['revertReadback'], metadata = settings_get(api)
        receipt['revertReadbackETag'] = metadata['etag']
        receipt['revertedAt'] = receipt['revertResponse']['appliedAt']
        evidence_io.save(path, receipt)
        return
    while len(attempts) < 2:
        current, metadata = settings_get(api)
        eligible, skipped = [], []
        for change in receipt['changed']:
            observed = path_value(current, change['path'])
            if observed == change['applied']:
                eligible.append(change)
            else:
                skipped.append({'path': change['path'], 'observed': observed, 'reason': 'external-change-revert-skipped'})
        receipt['revertSkipped'] = skipped
        if not eligible:
            receipt.update(revertReadback=current, revertReadbackETag=metadata['etag'], revertedAt=evidence_io.now(), revertedPaths=[])
            evidence_io.save(path, receipt)
            return
        writable = {key: value for key, value in current.items() if key not in SETTINGS_PROJECTIONS}
        request = restore_paths(writable, eligible)
        attempt = {'ifMatch': metadata['etag'], 'request': request, 'requestedAt': evidence_io.now()}
        attempts.append(attempt)
        receipt.update(revertRequest=request, revertRequestedAt=attempt['requestedAt'])
        evidence_io.save(path, receipt)
        try:
            receipt['revertResponse'] = settings_put(api, request, metadata['etag'], attempt)
        except Exception as error:
            receipt.update(revertError=str(error), revertFailedAt=evidence_io.now())
            evidence_io.save(path, receipt)
            if attempt.get('statusCode') == 409:
                continue
            raise
        evidence_io.save(path, receipt)
        receipt['revertReadback'], readback_meta = settings_get(api)
        receipt['revertReadbackETag'] = readback_meta['etag']
        receipt['revertedPaths'] = [change['path'] for change in eligible]
        receipt['revertReadbackExternalChanges'] = [
            {'path': change['path'], 'observed': path_value(receipt['revertReadback'], change['path']),
             'reason': 'external-change-after-revert'} for change in eligible
            if path_value(receipt['revertReadback'], change['path']) != change['before']]
        receipt['revertedAt'] = receipt['revertResponse']['appliedAt']
        evidence_io.save(path, receipt)
        return
    raise RuntimeError('SettingsConflict: second revert If-Match conflict; Returned blocked')



#!/usr/bin/env python3
"""Fail-closed token lifetime check; never emit token bytes or mint credentials."""
import base64
import datetime as dt
import json
import math
from pathlib import Path
import ssl
import time
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def lifetime_budget(stages, timeboxes, margin=60):
    """Budget every stage's bounded setup/drain/collection, plus one safety margin."""
    values = [stage['seconds'] for stage in stages] + list(timeboxes.values()) + [margin]
    if not stages or any(type(v) not in (int, float) or not math.isfinite(v) or v < 0 for v in values):
        raise ValueError('Finite nonnegative token duration inputs required')
    return sum(stage['seconds'] for stage in stages) + len(stages) * sum(timeboxes.values()) + margin


def check(token_file, expected_seconds, *, endpoint=None, ca_file=None, timeout=10, clock=time.time):
    if type(expected_seconds) not in (int, float) or not math.isfinite(expected_seconds) or expected_seconds < 0:
        raise ValueError('Finite nonnegative expected token duration required')
    token = Path(token_file).read_text().strip()
    if not token:
        raise ValueError('Empty authentication token')
    expires = None
    source = 'ServiceAccount JWT exp claim (lifetime only; API still authenticates)'
    parts = token.split('.')
    if len(parts) == 3:
        try:
            claims = json.loads(base64.urlsafe_b64decode(parts[1] + '=' * (-len(parts[1]) % 4)))
            expires = claims.get('exp')
        except (ValueError, UnicodeError, AttributeError):
            pass
    if type(expires) not in (int, float) or not math.isfinite(expires):
        # Opaque/legacy tokens need authenticated server evidence, not a guessed TTL.
        if not endpoint or not ca_file:
            raise ValueError('Token has no usable JWT exp; authenticated expiry response required')
        origin = urllib.parse.urlsplit(endpoint)
        if origin.scheme != 'https' or not origin.hostname or origin.username or origin.password or origin.path not in ('', '/') or origin.query or origin.fragment:
            raise ValueError('Authenticated expiry probe requires an HTTPS origin')
        request = urllib.request.Request(endpoint.rstrip('/') + '/api/v1/scheduler/state',
                                         headers={'Authorization': 'Bearer ' + token})
        opener = urllib.request.build_opener(NoRedirect(), urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(ca_file))))
        try:
            with opener.open(request, timeout=timeout) as response:
                if response.status != 200:
                    raise ValueError('Authenticated token expiry probe requires HTTP 200')
                value = response.headers.get('X-Token-Expires-At')
            instant = dt.datetime.fromisoformat((value or '').replace('Z', '+00:00'))
            if instant.tzinfo is None:
                raise ValueError('Timezone required')
            expires = instant.timestamp()
        except Exception:
            raise ValueError('Token lacks proven expiry: authenticated HTTP 200 with X-Token-Expires-At UTC required') from None
        source = 'Authenticated scheduler response X-Token-Expires-At'
    current = clock()
    remaining = expires - current
    record = {'checkedUTC': dt.datetime.fromtimestamp(current, dt.timezone.utc).isoformat(),
              'expiresUTC': dt.datetime.fromtimestamp(expires, dt.timezone.utc).isoformat(),
              'remainingSeconds': remaining, 'requiredSeconds': expected_seconds, 'source': source}
    if remaining < expected_seconds:
        raise ValueError('Authentication token expires at ' + record['expiresUTC'] +
                         '; remaining %.1fs < required %.1fs; refresh before starting' % (remaining, expected_seconds))
    return record


def self_check():
    import tempfile
    from unittest.mock import patch
    from types import SimpleNamespace
    assert lifetime_budget([{'seconds': 120}, {'seconds': 60}], {'setupSeconds': 30, 'drainSeconds': 90}, 60) == 480
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory) / 'token'
        def jwt(exp):
            return 'e30.' + base64.urlsafe_b64encode(json.dumps({'exp': exp}).encode()).decode().rstrip('=') + '.signature'
        path.write_text(jwt(200))
        assert check(path, 100, clock=lambda: 100)['remainingSeconds'] == 100
        for value in (jwt(199), jwt(99), '', 'opaque', 'e30.invalid.signature'):
            path.write_text(value)
            try:
                check(path, 100, clock=lambda: 100)
            except ValueError:
                pass
            else:
                raise AssertionError('Insufficient/unproven token accepted')
        path.write_text('opaque')
        class Response:
            status = 200
            headers = {'X-Token-Expires-At': '1970-01-01T00:03:20Z'}
            def __enter__(self): return self
            def __exit__(self, *args): pass
        opener = SimpleNamespace(open=lambda *args, **kwargs: Response())
        with patch.object(ssl, 'create_default_context'), patch.object(urllib.request, 'build_opener', return_value=opener):
            assert check(path, 100, endpoint='https://fixture.invalid', ca_file='fixture', clock=lambda: 100)['remainingSeconds'] == 100
            Response.headers = {}
            try:
                check(path, 100, endpoint='https://fixture.invalid', ca_file='fixture', clock=lambda: 100)
            except ValueError:
                pass
            else:
                raise AssertionError('Opaque token without expiry accepted')
    print('token lifetime self-check passed (local only)')


if __name__ == '__main__':
    self_check()

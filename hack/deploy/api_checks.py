"""Authenticated HTTP transport and operational deployment checks."""
from contextlib import contextmanager
import errno
import math
import threading
import importlib.util
import json
import os
from decimal import Decimal
from pathlib import Path
import re
import selectors
import signal
import subprocess
import time
import ssl
import urllib.error
import urllib.parse
import urllib.request


_spec = importlib.util.spec_from_file_location(
    'chill_deploy_auth', str(Path(__file__).resolve().parents[1] / 'evaluation' / 'auth.py'))
auth = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(auth)


def _save(path, data):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd = os.open(str(path), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'wb') as stream:
        os.fchmod(stream.fileno(), 0o600)
        stream.write(data)


def _report(path, value):
    _save(path, (json.dumps(value, indent=2, ensure_ascii=False, default=str) + '\n').encode())


def _origin(value):
    if not isinstance(value, str):
        raise ValueError('API origin must be a string')
    parsed = urllib.parse.urlsplit(value)
    if (parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.password
            or parsed.path not in ('', '/') or parsed.query or parsed.fragment):
        raise ValueError('API requires an HTTPS origin without credentials, path, query or fragment')
    return value.rstrip('/')


def load_config(recipe_config=None):
    """Load explicit recipe apiEnvironment, otherwise CHILL_EXPERIMENT_CONFIG."""
    path = recipe_config or os.environ.get('CHILL_EXPERIMENT_CONFIG')
    if not path:
        raise ValueError('CHILL_EXPERIMENT_CONFIG or recipe apiEnvironment is required')
    path = Path(path).expanduser().resolve()
    value = json.loads(path.read_text())['d2']
    config = {'apiBase': _origin(value['apiBase'])}
    for field in ('caFile', 'tokenFile'):
        filename = Path(value[field]).expanduser()
        if not filename.is_absolute():
            filename = path.parent / filename
        if not filename.is_file():
            raise ValueError('Missing API configuration file: ' + field)
        config[field] = str(filename.resolve())
    return config


class ReadinessPending(ValueError):
    """Only startup transport failures and explicit coverage warming may wait."""


def request_json(config, route, out, timeout=60, max_bytes=None):
    """GET one authenticated route; retain raw body/status, forbid redirects."""
    origin = _origin(config['apiBase'])
    if not isinstance(route, str):
        raise ValueError('API route must be a string')
    parsed = urllib.parse.urlsplit(route)
    if (not route.startswith('/api/v1/') or parsed.scheme or parsed.netloc or parsed.fragment
            or any(ord(char) < 33 for char in route)):
        raise ValueError('API route must be an origin-relative /api/v1/ route')
    if max_bytes is not None and (type(max_bytes) is not int or max_bytes < 1):
        raise ValueError('API response byte limit must be a positive integer')
    token = _token(config)
    request = urllib.request.Request(origin + route, headers={'Authorization': 'Bearer ' + token})
    opener = urllib.request.build_opener(
        auth.NoRedirect(), urllib.request.HTTPSHandler(
            context=ssl.create_default_context(cafile=str(config['caFile']))))
    status, headers, body = _fetch(request, opener, out, timeout, max_bytes)
    if status != 200:
        if status == 503:
            try:
                error = json.loads(body)
            except (ValueError, UnicodeError):
                error = None
            if isinstance(error, dict) and error.get('code') == 'CoverageWarming':
                raise ReadinessPending('API coverage is warming')
        raise ValueError('Authenticated API requires HTTP 200; received %s' % status)
    try:
        return json.loads(body, parse_float=Decimal, parse_constant=_invalid_constant)
    except (ValueError, UnicodeError):
        raise ValueError('API response is not valid finite JSON') from None


def _token(config):
    token = Path(config['tokenFile']).read_text().strip()
    if not token or any(ord(char) < 33 or ord(char) == 127 for char in token):
        raise ValueError('Empty or unsafe authentication token')
    return token


@contextmanager
def _elapsed_deadline(seconds):
    """Bound DNS, TLS, headers and slow-drip bodies without background workers."""
    if threading.current_thread() is not threading.main_thread():
        raise ValueError('Elapsed HTTP deadlines require the POSIX main thread')
    if type(seconds) not in (int, float) or not math.isfinite(seconds) or seconds <= 0:
        raise ValueError('A finite positive elapsed HTTP timeout is required')
    if not hasattr(signal, 'setitimer'):
        raise ValueError('Elapsed HTTP deadlines require POSIX interval timers')
    previous_handler = signal.getsignal(signal.SIGALRM)
    previous_timer = signal.getitimer(signal.ITIMER_REAL)
    started = time.monotonic()
    def expire(signum, frame):
        raise TimeoutError('Elapsed HTTP deadline exceeded')
    signal.signal(signal.SIGALRM, expire)
    seconds = min(seconds, previous_timer[0]) if previous_timer[0] else seconds
    signal.setitimer(signal.ITIMER_REAL, seconds)
    try:
        yield
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous_handler)
        if previous_timer[0]:
            signal.setitimer(signal.ITIMER_REAL,
                             max(0.000001, previous_timer[0] - (time.monotonic() - started)),
                             previous_timer[1])


def _fetch(request, opener, out, timeout, max_bytes=8388608):
    out = Path(out)
    try:
        with _elapsed_deadline(timeout):
            try:
                response = opener.open(request, timeout=timeout)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                status = response.status
                headers = response.headers
                body = response.read() if max_bytes is None else response.read(max_bytes)
    except (OSError, urllib.error.URLError) as error:
        reason = error.reason if isinstance(error, urllib.error.URLError) else error
        pending = (isinstance(reason, ssl.SSLEOFError) or
                   not isinstance(reason, ssl.SSLError) and
                   (isinstance(reason, (ConnectionError, TimeoutError)) or
                    isinstance(reason, OSError) and reason.errno in
                    (errno.ECONNREFUSED, errno.ECONNRESET, errno.ECONNABORTED,
                     errno.ETIMEDOUT, errno.EHOSTUNREACH, errno.ENETUNREACH)))
        _save(out.with_suffix('.http-status'), b'transport-error\n')
        _report(out.with_suffix('.transport-error.json'), {
            'type': type(reason).__name__, 'errno': getattr(reason, 'errno', None),
            'readinessPending': pending})
        failure = ReadinessPending if pending else ValueError
        raise failure('HTTP request failed; inspect transport evidence') from error
    _save(out.with_suffix('.http-status'), (str(status) + '\n').encode())
    _save(out, body)
    if max_bytes is not None and len(body) >= max_bytes:
        raise ValueError('API response reached or exceeded its configured byte limit')
    return status, headers, body


def check_dashboard(config, out, timeout=60):
    """Require verified HTTPS HTML delivery; this does not claim browser acceptance."""
    out = Path(out)
    receipt = {'passed': False}
    try:
        request = urllib.request.Request(_origin(config['apiBase']) + '/',
                                         headers={'Authorization': 'Bearer ' + _token(config)})
        opener = urllib.request.build_opener(
            auth.NoRedirect(), urllib.request.HTTPSHandler(
                context=ssl.create_default_context(cafile=str(config['caFile']))))
        status, headers, body = _fetch(request, opener, out / 'dashboard.html', timeout)
        content_type = headers.get('Content-Type', '').split(';')[0].strip().lower()
        receipt.update({'status': status, 'contentType': content_type, 'bytes': len(body)})
        if (status != 200 or content_type != 'text/html' or
                not re.search(br'<(?:!doctype\s+html|html)(?:\s|>)', body, re.I)):
            raise ValueError('Dashboard requires HTTP 200 with a nonempty HTML document')
        receipt['passed'] = True
        return receipt
    finally:
        _report(out / 'dashboard-check.json', receipt)


def _forward_port(process, deadline, log_path):
    data = bytearray()
    with selectors.DefaultSelector() as selector:
        selector.register(process.stdout, selectors.EVENT_READ)
        try:
            while time.monotonic() < deadline:
                events = selector.select(min(0.2, max(0, deadline - time.monotonic())))
                if not events:
                    if process.poll() is not None:
                        break
                    continue
                chunk = os.read(process.stdout.fileno(), 4096)
                if not chunk:
                    break
                data.extend(chunk)
                match = re.search(br'Forwarding from 127\.0\.0\.1:(\d+) -> 8080', data)
                if match:
                    port = int(match.group(1))
                    if 0 < port < 65536:
                        return port
                if len(data) > 65536:
                    raise ValueError('Port-forward output exceeded its bounded startup log')
        finally:
            _save(log_path, bytes(data))
    raise ValueError('Query port-forward did not become ready before its deadline')


def check_query(config, kube, namespace, out, timeout=60, query_service='chill-query'):
    """Prove direct Query rejects absent bearer and accepts the original token."""
    if (not isinstance(kube, (list, tuple)) or not kube or
            not re.fullmatch(r'[a-z0-9](?:[-a-z0-9]*[a-z0-9])?', query_service) or
            not isinstance(namespace, str) or not namespace):
        raise ValueError('Explicit kubectl base argv, namespace and service required')
    out = Path(out)
    receipt = {'passed': False, 'service': query_service}
    process = None
    deadline = time.monotonic() + timeout
    def remaining():
        seconds = deadline - time.monotonic()
        if seconds <= 0:
            raise ValueError('Query check deadline exceeded')
        return seconds
    try:
        process = subprocess.Popen(
            list(kube) + ['-n', namespace, 'port-forward', 'service/' + query_service,
                          ':8080', '--address', '127.0.0.1'],
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            start_new_session=True, bufsize=0)
        port = _forward_port(process, deadline, out / 'query-port-forward.log')
        url = 'http://127.0.0.1:%d/api/v1/scheduler/profiles?view=summary&limit=1' % port
        opener = urllib.request.build_opener(auth.NoRedirect(), urllib.request.ProxyHandler({}))
        for name, headers, expected in (
                ('query-unauthenticated', {}, 401),
                ('query-authenticated', {'Authorization': 'Bearer ' + _token(config)}, 200)):
            status, response_headers, body = _fetch(
                urllib.request.Request(url, headers=headers), opener, out / (name + '.json'), remaining())
            receipt[name] = {'status': status, 'bytes': len(body)}
            if status != expected:
                raise ValueError('Direct Query %s requires HTTP %s; received %s' % (name, expected, status))
        receipt['passed'] = True
        return receipt
    finally:
        if process is not None:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=2)
            if process.stdout:
                process.stdout.close()
        _report(out / 'query-check.json', receipt)


def _invalid_constant(value):
    raise ValueError('Nonfinite JSON number')



def check_routes(config, checks, out, timeout=60):
    """Exercise declared first-use routes with strict response size bounds."""
    out = Path(out)
    receipts = []
    for number, check in enumerate(checks):
        path, limit = check['path'], check['maxBytes']
        if type(limit) is not int or limit < 1:
            raise ValueError('API route maxBytes must be a positive integer')
        target = out / ('route-%03d.json' % number)
        receipt = {'path': path, 'maxBytes': limit, 'passed': False}
        receipts.append(receipt)
        try:
            request_json(config, path, target, timeout=timeout, max_bytes=limit)
            receipt['passed'] = True
        finally:
            if target.exists():
                receipt['bytes'] = target.stat().st_size
            status = target.with_suffix('.http-status')
            receipt['status'] = status.read_text().strip() if status.exists() else 'not-requested'
            _report(out / 'api-checks.json', receipts)
    return receipts

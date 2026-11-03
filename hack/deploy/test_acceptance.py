"""Local acceptance regression checks; transport tests use loopback only."""
import copy
from decimal import Decimal
import http.server
import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

import api_checks


_spec = importlib.util.spec_from_file_location('deploy_acceptance', Path(__file__).with_name('acceptance.py'))
acceptance = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(acceptance)


def snapshot():
    return {
        'profiles': {'profiles': [
            {'id': identity, 'assessment': {'displayStatus': 'usable'},
             'batches': [{'batch': 1, 'energyPerInputJoules': Decimal('0.123456789012345678901'),
                          'meanWallWatts': Decimal('5.000000000000000001')}]}
            for identity in ('p1', 'p2')]},
        'coverage': {'summary': {'countsByDisplayStatus': {'usable': 2}},
                     'sources': [{'complete': True, 'error': None}]},
    }


class AcceptanceChecks(unittest.TestCase):
    def test_decimal_preservation(self):
        before = snapshot()
        after = copy.deepcopy(before)
        after['profiles']['profiles'][0]['batches'][0]['energyPerInputJoules'] = Decimal('0.1234567890123456789010')
        self.assertTrue(acceptance.compare(before, after)['passed'])
        after['profiles']['profiles'][0]['batches'][0]['energyPerInputJoules'] = Decimal('0.123456789012345678902')
        self.assertFalse(acceptance.compare(before, after)['passed'])

    def test_duplicate_identities(self):
        for kind in ('profile', 'batch'):
            with self.subTest(kind=kind):
                after = snapshot()
                rows = after['profiles']['profiles']
                if kind == 'batch':
                    rows = rows[0]['batches']
                rows.append(copy.deepcopy(rows[0]))
                report = acceptance.compare(snapshot(), after)
                self.assertFalse(report['passed'])
                self.assertTrue(report['errors'])

    def test_exact_declared_loss(self):
        before = snapshot()
        after = snapshot()
        after['profiles']['profiles'].pop()
        self.assertFalse(acceptance.compare(before, after)['passed'])
        self.assertTrue(acceptance.compare(before, after, ['p2'])['passed'])
        self.assertFalse(acceptance.compare(before, before, ['p2'])['passed'])
        self.assertFalse(acceptance.compare(before, after, ['p1', 'p2'])['passed'])

    def test_missing_measurement_and_status(self):
        for field in ('energyPerInputJoules', 'meanWallWatts'):
            after = snapshot()
            del after['profiles']['profiles'][0]['batches'][0][field]
            report = acceptance.compare(snapshot(), after)
            self.assertFalse(report['passed'])
            self.assertTrue(report['errors'])
        after = snapshot()
        after['profiles']['profiles'][0]['assessment']['displayStatus'] = 'unknown'
        self.assertFalse(acceptance.compare(snapshot(), after)['passed'])

    def test_coverage_completeness_and_target(self):
        after = snapshot()
        after['coverage']['sources'][0]['complete'] = False
        report = acceptance.compare(snapshot(), after)
        self.assertFalse(report['passed'])
        self.assertFalse(report['checks']['coverageSourcesComplete'])
        self.assertEqual(report['coverageSourcesAfter'], after['coverage']['sources'])
        after = snapshot()
        after['coverage']['summary']['countsByDisplayStatus']['usable'] = 1
        self.assertFalse(acceptance.compare(snapshot(), after)['passed'])
        self.assertTrue(acceptance.compare(snapshot(), after, expected_coverage_usable=1)['passed'])

    def test_expected_profile_count_is_a_comparison_diagnostic(self):
        for expected, passes in ((None, True), (2, True), (1, False)):
            with self.subTest(expected=expected):
                report = acceptance.compare(snapshot(), snapshot(), expected_profile_count=expected)
                self.assertEqual(report['checks']['expectedProfileCount'], passes)
                self.assertEqual(report['passed'], passes)
        partial = snapshot()
        partial['coverage']['summary']['countsByDisplayStatus']['usable'] = None
        report = acceptance.compare(snapshot(), partial, expected_profile_count=2)
        self.assertTrue(report['checks']['expectedProfileCount'])
        self.assertFalse(report['passed'])

    def test_explicit_environment_precedence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'ca').write_text('fixture')
            (root / 'token').write_text('fixture')
            config = root / 'environment.json'
            config.write_text(json.dumps({'d2': {'apiBase': 'https://fixture.invalid',
                                                  'caFile': 'ca', 'tokenFile': 'token'}}))
            with patch.dict(os.environ, {'CHILL_EXPERIMENT_CONFIG': str(root / 'missing')}):
                self.assertEqual(api_checks.load_config(config)['caFile'], str(root / 'ca'))

    def test_bounded_private_route_evidence(self):
        class Response(io.BytesIO):
            status = 200
            headers = {'Content-Type': 'application/json'}
            def read(self, size=-1):
                if size < 0:
                    raise AssertionError('Unbounded route response read')
                return super().read(size)

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            token = root / 'token'
            token.write_text('original-bearer')
            config = {'apiBase': 'https://fixture.invalid', 'caFile': 'unused', 'tokenFile': token}
            with patch.object(api_checks.ssl, 'create_default_context'), patch.object(
                    api_checks.urllib.request, 'build_opener') as opener:
                for limit, passes in ((3, True), (2, False), (1, False)):
                    opener.return_value.open.return_value = Response(b'{}')
                    out = root / str(limit)
                    checks = [{'path': '/api/v1/workloads?view=summary', 'maxBytes': limit}]
                    if passes:
                        api_checks.check_routes(config, checks, out)
                    else:
                        with self.assertRaises(ValueError):
                            api_checks.check_routes(config, checks, out)
                    receipt = json.loads((out / 'api-checks.json').read_text())[0]
                    self.assertEqual(receipt['passed'], passes)
                    self.assertEqual(receipt['status'], '200')
                    self.assertEqual((out / 'route-000.json').stat().st_mode & 0o777, 0o600)
                    request = opener.return_value.open.call_args[0][0]
                    self.assertEqual(request.get_header('Authorization'), 'Bearer original-bearer')
                with self.assertRaises(ValueError):
                    api_checks.request_json(config, '//other.invalid/api/v1/workloads', root / 'bad.json')

    def test_query_statuses_and_process_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            token = root / 'token'
            token.write_text('original-bearer')
            config = {'tokenFile': token}
            for statuses, passes in (((401, 200), True), ((200,), False), ((401, 401), False)):
                with self.subTest(statuses=statuses), patch.object(
                        api_checks.subprocess, 'Popen') as popen, patch.object(api_checks, '_forward_port', return_value=12345), patch.object(api_checks, '_fetch', side_effect=[(s, {}, b'{}') for s in statuses]) as fetch, patch.object(
                        api_checks.os, 'killpg') as killpg:
                    out = root / '-'.join(map(str, statuses))
                    if passes:
                        result = api_checks.check_query(config, ['kubectl', '--context', 'fixture'],
                                                        'chill-system', out)
                        self.assertTrue(result['passed'])
                        unauthenticated = fetch.call_args_list[0][0][0]
                        authenticated = fetch.call_args_list[1][0][0]
                        self.assertIsNone(unauthenticated.get_header('Authorization'))
                        self.assertEqual(authenticated.get_header('Authorization'), 'Bearer original-bearer')
                    else:
                        with self.assertRaises(ValueError):
                            api_checks.check_query(config, ['kubectl'], 'chill-system', out)
                    killpg.assert_called_once_with(popen.return_value.pid, api_checks.signal.SIGTERM)
                    popen.return_value.wait.assert_called_once_with(timeout=2)
                    self.assertTrue(popen.call_args.kwargs['start_new_session'])
                    self.assertNotIn('original-bearer', str(popen.call_args))
                    self.assertEqual(json.loads((out / 'query-check.json').read_text())['passed'], passes)

    def test_dashboard_requires_html_http_200(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            token = root / 'token'
            token.write_text('original-bearer')
            config = {'apiBase': 'https://fixture.invalid', 'caFile': 'unused', 'tokenFile': token}
            cases = [(200, 'text/html; charset=utf-8', b'<!doctype html><html></html>', True),
                     (200, 'application/json', b'{}', False),
                     (200, 'text/html', b'', False),
                     (302, 'text/html', b'<html></html>', False)]
            for number, (status, content_type, body, passes) in enumerate(cases):
                with patch.object(api_checks.ssl, 'create_default_context'), patch.object(api_checks, '_fetch', return_value=(status, {'Content-Type': content_type}, body)):
                    out = root / str(number)
                    if passes:
                        self.assertTrue(api_checks.check_dashboard(config, out)['passed'])
                    else:
                        with self.assertRaises(ValueError):
                            api_checks.check_dashboard(config, out)
                    self.assertEqual(json.loads((out / 'dashboard-check.json').read_text())['passed'], passes)

    def test_capture_waits_for_coverage_before_fetching_profiles(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory)
            with patch.object(api_checks, 'request_json', side_effect=ValueError('CoverageWarming')) as request:
                with self.assertRaisesRegex(ValueError, 'CoverageWarming'):
                    acceptance.capture({}, 'chill-system', out, timeout=7)
                request.assert_called_once_with(
                    {}, '/api/v1/namespaces/chill-system/measurement-coverage', out / 'coverage.json', 7)
            expected = snapshot()
            with patch.object(api_checks, 'request_json', side_effect=[
                    expected['coverage'], expected['profiles']]) as request:
                self.assertEqual(acceptance.capture({}, 'chill-system', out, timeout=7), expected)
                self.assertEqual([call.args[1] for call in request.call_args_list], [
                    '/api/v1/namespaces/chill-system/measurement-coverage', '/api/v1/scheduler/profiles'])

    def test_partial_scientific_snapshot_is_not_an_operational_failure(self):
        partial = snapshot()
        partial['profiles']['profiles'][0]['batches'] = []
        partial['coverage']['summary']['countsByDisplayStatus']['usable'] = None
        for complete, error in ((False, None), (True, {'code': 'Unavailable', 'retryable': True})):
            with self.subTest(complete=complete, error=error), tempfile.TemporaryDirectory() as directory:
                partial['coverage']['sources'][0].update({'complete': complete, 'error': error})
                with patch.object(api_checks, 'request_json', side_effect=[
                        partial['coverage'], partial['profiles']]):
                    self.assertEqual(acceptance.capture({}, 'chill-system', directory), partial)
                status = json.loads((Path(directory) / 'snapshot-status.json').read_text())
                self.assertTrue(status['valid'])
                self.assertFalse(status['coverageSourcesComplete'])
                report = acceptance.compare(snapshot(), partial)
                self.assertFalse(report['passed'])
                self.assertIsNone(report['coverageUsableAfter'])
                self.assertTrue(report['errors'])

    def test_strict_current_record_preservation(self):
        before = snapshot()
        for profile in before['profiles']['profiles']:
            profile.update({
                'reference': {'store': 'store', 'key': 'result', 'size': 3},
                'evidence': [{'store': 'store', 'key': 'raw', 'size': 5}],
                'nodeName': 'measured-node', 'condition': {
                    'runtimeImage': 'registry/runtime@sha256:historical',
                    'cpu': {'policy': 'ConfiguredCPUQuota', 'request': '3'},
                    'artifact': {'image': 'registry/artifact@sha256:historical'}},
                'confidenceLevel': Decimal('0.95'), 'idleBaselineWatts': Decimal('7.1234567890123456789'),
                'measuredAt': '2026-11-02T01:33:29Z', 'metricSource': {'kind': 'edge-metrics'},
                'runtimeMetadata': {'cpuAffinity': [0, 1], 'intraOpThreads': 3},
            })
            profile['batches'][0]['memoryBytes'] = 1234
            profile['batches'][0]['durationNanos'] = [Decimal('1234.56789')]
        after = copy.deepcopy(before)
        after['profiles']['profiles'][0]['assessment']['scope'] = {'schedulerUsed': False}
        after['profiles']['profiles'][0]['assessment']['currentCompatibility'] = 'changed'
        self.assertTrue(acceptance.compare_records(before, after)['passed'])
        paths = [('condition', 'runtimeImage'), ('condition', 'artifact', 'image'),
                 ('reference', 'key'), ('evidence', 0, 'key'), ('idleBaselineWatts',),
                 ('batches', 0, 'memoryBytes'), ('batches', 0, 'durationNanos', 0),
                 ('runtimeMetadata', 'intraOpThreads'), ('measuredAt',)]
        for path in paths:
            with self.subTest(path=path):
                changed = copy.deepcopy(before)
                row = changed['profiles']['profiles'][0]
                for key in path[:-1]:
                    row = row[key]
                value = row[path[-1]]
                row[path[-1]] = value + 1 if isinstance(value, (int, Decimal)) else value + '-changed'
                self.assertFalse(acceptance.compare_records(before, changed)['passed'])
        migrated = copy.deepcopy(before)
        for profile in migrated['profiles']['profiles']:
            del profile['condition']['cpu']
            profile['evidence'][0]['key'] = 'raw.sharedcpu'
        self.assertTrue(acceptance.compare(before, migrated)['passed'])
        self.assertFalse(acceptance.compare_records(before, migrated)['passed'])

    def test_elapsed_deadline_restores_existing_alarm(self):
        signal = api_checks.signal
        handler = signal.getsignal(signal.SIGALRM)
        timer = signal.getitimer(signal.ITIMER_REAL)
        def previous(signum, frame):
            raise AssertionError('Previous timer unexpectedly fired')
        try:
            signal.signal(signal.SIGALRM, previous)
            signal.setitimer(signal.ITIMER_REAL, 5, 2)
            with api_checks._elapsed_deadline(0.5):
                pass
            self.assertIs(signal.getsignal(signal.SIGALRM), previous)
            remaining, interval = signal.getitimer(signal.ITIMER_REAL)
            self.assertGreater(remaining, 4)
            self.assertLessEqual(remaining, 5)
            self.assertEqual(interval, 2)
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, handler)
            signal.setitimer(signal.ITIMER_REAL, *timer)
        errors = []
        def unsupported_thread():
            try:
                with api_checks._elapsed_deadline(1):
                    pass
            except ValueError as error:
                errors.append(str(error))
        worker = threading.Thread(target=unsupported_thread)
        worker.start()
        worker.join(timeout=2)
        self.assertFalse(worker.is_alive())
        self.assertEqual(len(errors), 1)
        self.assertIn('main thread', errors[0])


    def test_tls_listener_startup_warming_and_fail_closed_readiness(self):
        # Bind without listen: the first real HTTPS GET must receive ECONNREFUSED.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cert, key = root / 'cert.pem', root / 'key.pem'
            subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                            '-keyout', str(key), '-out', str(cert), '-days', '1',
                            '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost'],
                           check=True, capture_output=True)
            token = root / 'token'
            token.write_text('local-test-token')
            routes = []
            responses = []
            class Handler(http.server.BaseHTTPRequestHandler):
                def log_message(self, *args):
                    pass
                def do_GET(self):
                    routes.append(self.path)
                    status, body = responses.pop(0)
                    if status is None:
                        self.close_connection = True
                        return
                    data = body if isinstance(body, bytes) else json.dumps(body, default=str).encode()
                    self.send_response(status)
                    self.end_headers()
                    self.wfile.write(data)
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            context.load_cert_chain(cert, key)
            class Server(http.server.HTTPServer):
                drop_handshake = False
                wrong_protocol = False
                def get_request(self):
                    peer, address = super().get_request()
                    if self.drop_handshake or self.wrong_protocol:
                        peer.settimeout(2)
                        peer.recv(65536)  # Consume ClientHello so closure is EOF, not unread-data RST.
                        if self.wrong_protocol:
                            peer.sendall(b'HTTP/1.1 400 Bad Request\r\n\r\n')
                        self.drop_handshake = self.wrong_protocol = False
                        peer.shutdown(socket.SHUT_RDWR)
                        peer.close()
                        raise ConnectionResetError('local fixture closed handshake')
                    return context.wrap_socket(peer, server_side=True), address
            server = Server(('127.0.0.1', 0), Handler, bind_and_activate=False)
            server.server_bind()
            config = {'apiBase': 'https://localhost:%d' % server.server_port,
                      'caFile': str(cert), 'tokenFile': str(token)}
            expected = snapshot()
            for row in expected['profiles']['profiles']:
                row['batches'] = [{'batch': 1, 'energyPerInputJoules': 3, 'meanWallWatts': 5}]
            healthy = [(200, expected['coverage']), (200, expected['profiles'])]
            clock = [0]
            worker = threading.Thread(target=server.serve_forever)
            def advance(seconds):
                clock[0] += seconds
                if not worker.is_alive():
                    server.server_activate()
                    worker.start()
            responses[:] = [(503, {'code': 'CoverageWarming'}), *healthy]
            try:
                def elapse(seconds):
                    clock[0] += seconds
                with patch.object(acceptance.time, 'monotonic', side_effect=lambda: clock[0]), patch.object(
                        acceptance.time, 'sleep', side_effect=elapse):
                    with self.assertRaises(RuntimeError):
                        acceptance.wait_capture(config, 'review', root / 'unreachable', deadline=3)
                self.assertEqual(clock[0], 3)
                clock[0] = 0
                with patch.object(acceptance.time, 'monotonic', side_effect=lambda: clock[0]), patch.object(
                        acceptance.time, 'sleep', side_effect=advance):
                    result = acceptance.wait_capture(config, 'review', root / 'startup', deadline=30)
                self.assertTrue(acceptance.compare(expected, result)['passed'])
                self.assertEqual((root / 'startup/attempt-000/coverage.http-status').read_text().strip(),
                                 'transport-error')
                transport = json.loads((root / 'startup/attempt-000/coverage.transport-error.json').read_text())
                self.assertTrue(transport['readinessPending'])
                self.assertEqual((root / 'startup/attempt-001/coverage.http-status').read_text().strip(), '503')
                self.assertEqual((root / 'startup/attempt-002/profiles.http-status').read_text().strip(), '200')
                self.assertEqual(sum(route.endswith('/profiles') for route in routes), 1)

                # A TCP peer can disappear during TLS, before any HTTP status exists.
                server.drop_handshake = True
                responses[:] = healthy
                with patch.object(acceptance.time, 'sleep'):
                    result = acceptance.wait_capture(config, 'review', root / 'tls-eof', time.monotonic() + 30)
                self.assertTrue(acceptance.compare(expected, result)['passed'])
                transport = json.loads((root / 'tls-eof/attempt-000/coverage.transport-error.json').read_text())
                self.assertEqual(transport['type'], ssl.SSLEOFError.__name__)
                self.assertTrue(transport['readinessPending'])

                server.wrong_protocol = True
                with patch.object(acceptance.time, 'sleep') as sleep:
                    with self.assertRaises(ValueError) as error:
                        acceptance.wait_capture(config, 'review', root / 'tls-protocol', time.monotonic() + 30)
                    self.assertNotIsInstance(error.exception, api_checks.ReadinessPending)
                    sleep.assert_not_called()

                # A profile connection loss after healthy coverage also waits, without
                # ever downloading profiles while the coverage route is warming.
                routes.clear()
                responses[:] = [(200, expected['coverage']), (None, None), *healthy]
                with patch.object(acceptance.time, 'sleep'):
                    result = acceptance.wait_capture(config, 'review', root / 'profiles', time.monotonic() + 30)
                self.assertTrue(acceptance.compare(expected, result)['passed'])
                self.assertEqual(len(routes), 4)
                self.assertTrue(json.loads(
                    (root / 'profiles/attempt-000/profiles.transport-error.json').read_text())['readinessPending'])

                # Every non-startup failure must stop after its first request.
                invalid = copy.deepcopy(expected['coverage'])
                invalid['sources'] = [{'complete': 'invalid'}]
                failures = [(401, {}), (403, {}), (503, {'code': 'CoverageUnavailable'}),
                            (503, b'not json'), (200, b'not json'),
                            (200, invalid)]
                for index, response in enumerate(failures):
                    responses[:] = [response, (200, expected['profiles'])]
                    with patch.object(acceptance.time, 'sleep') as sleep:
                        with self.assertRaises(ValueError) as error:
                            acceptance.wait_capture(config, 'review', root / ('failure-%d' % index),
                                                    time.monotonic() + 30)
                        self.assertNotIsInstance(error.exception, api_checks.ReadinessPending)
                        sleep.assert_not_called()

                # A real hostname/certificate verification failure is never readiness.
                untrusted = dict(config, apiBase=config['apiBase'].replace('localhost', '127.0.0.1'))
                with patch.object(acceptance.time, 'sleep') as sleep:
                    with self.assertRaises(ValueError) as error:
                        acceptance.wait_capture(untrusted, 'review', root / 'tls', time.monotonic() + 30)
                    self.assertNotIsInstance(error.exception, api_checks.ReadinessPending)
                    sleep.assert_not_called()
                self.assertFalse(json.loads(
                    (root / 'tls/attempt-000/coverage.transport-error.json').read_text())['readinessPending'])

                clock[0] = 0
                responses[:] = [(503, {'code': 'CoverageWarming'})]
                with patch.object(acceptance.time, 'monotonic', side_effect=lambda: clock[0]), patch.object(
                        acceptance.time, 'sleep', side_effect=advance):
                    with self.assertRaises(RuntimeError):
                        acceptance.wait_capture(config, 'review', root / 'deadline', deadline=3)
                self.assertEqual(clock[0], 3)
                self.assertEqual((root / 'deadline/attempt-000/coverage.http-status').read_text().strip(), '503')
            finally:
                if worker.is_alive():
                    server.shutdown()
                    worker.join(timeout=2)
                server.server_close()

    def test_slow_drip_body_hits_elapsed_deadline(self):
        stop = threading.Event()
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass
            def do_GET(self):
                self.send_response(200)
                self.send_header('Content-Length', '1024')
                self.end_headers()
                try:
                    while not stop.wait(0.01):
                        self.wfile.write(b' ')
                        self.wfile.flush()
                except OSError:
                    pass
        server = http.server.HTTPServer(('127.0.0.1', 0), Handler)
        worker = threading.Thread(target=server.serve_forever)
        worker.start()
        try:
            with tempfile.TemporaryDirectory() as directory:
                opener = api_checks.urllib.request.build_opener(
                    api_checks.auth.NoRedirect(), api_checks.urllib.request.ProxyHandler({}))
                request = api_checks.urllib.request.Request('http://127.0.0.1:%s/' % server.server_port)
                started = time.monotonic()
                with self.assertRaises(ValueError):
                    api_checks._fetch(request, opener, Path(directory) / 'drip.json', timeout=0.15)
                self.assertLess(time.monotonic() - started, 1.5)
        finally:
            stop.set()
            server.shutdown()
            server.server_close()
            worker.join(timeout=2)
            self.assertFalse(worker.is_alive())


if __name__ == '__main__':
    unittest.main()

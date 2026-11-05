"""D44 raw API recovery facts survive collection, judgment and publication."""
import json
import os
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import urllib.request

import archive
import conductor
import evidence_io
import judgment
import publication
import raw
import tables
from test_evaluation import fixture, power_window, NAME, START, END


class RecoveryGapTests(unittest.TestCase):
    def test_recovery_gaps_and_unaffected_failures(self):
        tail = {'from': START, 'to': '2026-11-03T00:00:05Z', 'stream': '',
                'reason': 'UncleanObservationTail', 'boot': 'unknown-predecessor'}
        cases = [
            ('unknown-start', tail, True, 'NOT_EVALUATED'),
            ('known-tail', {**tail, 'boot': START}, True, 'NOT_EVALUATED'),
            ('post-clear-open', {**tail, 'to': START, 'reason': 'UnknownExecutorInfluence',
                                 'open': True, 'clearAt': '2026-11-02T23:59:59Z',
                                 'instance': 'pod/container', 'podUID': 'pod', 'containerID': 'container',
                                 'node': {'name': 'serving', 'uid': 'serving-uid'}}, True, 'NOT_EVALUATED'),
            ('gap-only', tail, False, 'NOT_EVALUATED'),
            ('zero-offer', tail, True, 'NOT_OFFERED'),
            ('ordinary-missing-final', None, True, 'FAIL'),
            ('ordinary-missing-final-empty', None, True, 'FAIL'),
            ('ordinary-slo-failure', None, True, 'FAIL'),
            ('gap-ends-at-start', {**tail, 'from': '2026-11-02T23:59:55Z', 'to': START}, True, 'FAIL'),
            ('gap-starts-at-end', {**tail, 'from': END, 'to': '2026-11-03T00:00:15Z'}, True, 'FAIL'),
            ('gap-in-second-part', {**tail, 'from': '2026-11-03T00:00:05Z', 'to': END}, True, 'NOT_EVALUATED'),
        ]
        for label, gap, available, expected in cases:
            with self.subTest(label=label), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                folder = root / 'measured'
                folder.mkdir()
                _, _, _, _, config = fixture(root)
                config['traceOffsets'] = {NAME: [] if label == 'zero-offer' else [1, 6_000_000_000]}
                config['stages'][0]['parts'] = [
                    {'id': 'first', 'seconds': 5, 'rates': {NAME: 1}},
                    {'id': 'second', 'seconds': 5, 'rates': {NAME: 1}}]
                evidence_io.save(root / 'config.json', config)
                bound = conductor.bind(root / 'config.json')
                evidence_io.save(root / 'binding.json', bound)
                phase = {'stage': bound['schedule'][0], 'startUTC': START, 'endUTC': END, 'drainedUTC': END,
                         'starts': {} if label == 'zero-offer' else {NAME: {'at': START, 'monotonicNS': 100}},
                         'producerCapture': {NAME: {'drained': True}}}
                evidence_io.save(folder / 'phase.json', phase)
                clients = [{'requestIndex': i, 'requestID': 'r' + str(i), 'scheduledOffsetNs': offset,
                            'issued': True, 'sentMonotonicNS': 100 + offset,
                            'finishedMonotonicNS': 100_000_100 + offset}
                           for i, offset in enumerate(config['traceOffsets'][NAME])]
                (folder / (NAME + '-client.jsonl')).write_text(''.join(json.dumps(row) + '\n' for row in clients))
                final = {'workloadUID': 'workload', 'definitionID': 'definition', 'kind': 'final', 'requestID': 'r0',
                         'receivedAt': '2026-11-03T00:00:01Z', 'outcome': 'completed',
                         'executionComplete': True, 'elapsedNanos': 100_000_000}
                if label == 'ordinary-slo-failure':
                    final['elapsedNanos'] = 2_000_000_000
                inventory = {'hour': '20261103T00', 'at': '2026-11-03T00:02:00Z', 'status': 'closed-local',
                             'kinds': [{'kind': stream, 'available': available if stream == 'requests' else False,
                                        'segments': [], 'archive': None} for stream in ('requests', 'resources', 'scheduler')],
                             'gaps': [gap] if gap else [] if label == 'ordinary-missing-final-empty' else None}
                class Handler(BaseHTTPRequestHandler):
                    def do_GET(self):
                        if self.path.startswith('/api/v1/observations/raw?'):
                            data, kind = json.dumps(inventory).encode(), 'application/json'
                        elif self.path.endswith('/requests'):
                            data, kind = (json.dumps(final) + '\n').encode(), 'application/x-ndjson'
                        elif self.path.startswith('/api/v1/observations/power?'):
                            data, kind = json.dumps(power_window()).encode(), 'application/json'
                        else:
                            self.send_error(404)
                            return
                        self.send_response(200)
                        self.send_header('Content-Type', kind)
                        self.send_header('Content-Length', str(len(data)))
                        self.end_headers()
                        self.wfile.write(data)
                    def log_message(self, *args):
                        pass
                server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
                thread = threading.Thread(target=server.serve_forever)
                thread.start()
                token = root / 'token'
                token.write_text('test-collector')
                class IO:
                    config = {'endpoint': 'http://127.0.0.1:' + str(server.server_port)}
                    collector_token_file = str(token)
                    http = urllib.request.build_opener()
                    def api(self, method, path, timeout):
                        with self.http.open(self.config['endpoint'] + path, timeout=timeout) as response:
                            return json.load(response)
                env = root / 'environment.json'
                evidence_io.save(env, {'paths': {'campaign': str(root)}})
                try:
                    with patch.dict(os.environ, CHILL_EXPERIMENT_CONFIG=str(env)):
                        archive.collect(folder, phase, config['collection'], IO(), 5)
                        raw.release_request_cache()
                        judged = judgment.judge(root / 'binding.json', folder)
                finally:
                    server.shutdown()
                    thread.join()
                    server.server_close()
                indexed = evidence_io.load(folder / 'raw-source-index.json')
                self.assertEqual(len(indexed['gaps']), int(gap is not None))
                if gap:
                    stored = indexed['gaps'][0]
                    self.assertEqual({key: stored[key] for key in gap}, gap)
                    self.assertEqual(stored['hour'], inventory['hour'])
                    self.assertEqual(stored['endpoint'], '/api/v1/observations/raw?hour=20261103T00')
                    self.assertEqual(stored['origin'], 'api')
                projected = tables.publication_requests(judged)[NAME]
                self.assertEqual(projected['D1'], expected)
                self.assertEqual(len(projected['observationGaps']), int(expected in ('NOT_EVALUATED', 'NOT_OFFERED')))
                self.assertEqual(publication.verdict(judged)['verdict'], 'NOT_EVALUATED' if expected == 'NOT_OFFERED' else expected)
                if label == 'zero-offer':
                    self.assertEqual([part['D1'] for part in projected['substages']], ['NOT_OFFERED', 'NOT_OFFERED'])
                if label == 'ordinary-slo-failure':
                    self.assertEqual(projected['substages'][0]['D1'], 'FAIL')
                self.assertEqual(judged['power']['energyStatus'], 'MEASURED')
                rendered = tables.render(root, evaluation={'status': judged['status'],
                    'stages': [{'id': 'one', 'kpis': {**judged, 'wholePoolJoules': 300}}]})
                self.assertIn(expected, rendered)
                if label == 'gap-in-second-part':
                    self.assertEqual([part['D1'] for part in projected['substages']],
                                     ['OBSERVED_THRESHOLD_MET', 'NOT_EVALUATED'])
                if expected in ('NOT_EVALUATED', 'NOT_OFFERED'):
                    self.assertIn(gap['reason'], rendered)
                    self.assertEqual(projected['observationGaps'][0], stored)


if __name__ == '__main__':
    unittest.main()

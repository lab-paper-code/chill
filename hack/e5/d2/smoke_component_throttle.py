#!/usr/bin/env python3
"""One local analyzer check; fixtures never contact live services."""
import contextlib
import datetime
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent
CONFIG = json.loads(Path(os.environ['CHILL_EXPERIMENT_CONFIG']).read_text())
OUT = Path(CONFIG['d2']['smokeOutput'])
RAW = Path(CONFIG['paths']['d2State']) / 'primary-static-final-g2/before-primary-static-075/static-01-50'

def main():
    start = datetime.datetime.now(datetime.timezone.utc).isoformat()
    OUT.mkdir(parents=True, exist_ok=True)
    spec = importlib.util.spec_from_file_location('analysis', ROOT / 'analyze_before.py')
    analysis = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(analysis)
    original_telemetry = analysis.telemetry
    before_hash = hashlib.sha256((RAW / 'summary.json').read_bytes()).hexdigest()
    conduct_hash = hashlib.sha256((ROOT / 'conduct_before.py').read_bytes()).hexdigest()
    checks = {}
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary)
        for folder in ('request-pages', 'scheduler-pages'):
            (root / folder).mkdir()
        phase = dict(kind='static-staircase', rate=50, startUTC='2026-10-24T00:00:00Z', endUTC='2026-10-24T00:05:00Z', warmupSeconds=60, offeredRequests=1)
        (root / 'phase.json').write_text(json.dumps(phase))
        for name in ('pre-state', 'post-state', 'pre-pools', 'post-pools'):
            (root / (name + '.json')).write_text('{}')
        (root / 'server.log').write_text('')
        (root / 'scheduler-pages/1.json').write_text('{"events":[]}')
        binding = {'snapshot': {'workloads': [dict(name=CONFIG['d2']['workloadName'], uid='workload', slo={'percentile': 95, 'targetLatencyNanos': 1000000000})]}}
        final = dict(workloadUID='workload', requestID='one', kind='final', outcome='completed', receivedAt='2026-10-24T00:01:01Z', elapsedNanos=1000000)
        client = dict(requestID='one', status=200, latencyMs=1)
        roles = ('server', 'client', 'native', 'edgemesh', 'unknown')
        def run_case(name, throttled='native', unknown=False, failure=False, latency=False, elapsed=None, missing_slo=False, missing_latency=False):
            row = dict(final)
            c = dict(client)
            for boundary in ('pre-state', 'post-state'):
                (root / (boundary + '.json')).write_text(json.dumps({} if missing_slo else binding))
            if elapsed is not None:
                row['elapsedNanos'] = elapsed
            if missing_latency:
                row.pop('elapsedNanos')
            if failure:
                c['status'] = 500
            if latency:
                row['outcome'] = 'deadline_exceeded'
                row['elapsedNanos'] = 2000000001
                c['status'] = 504
            (root / 'request-pages/1.json').write_text(json.dumps({'requests': [row]}))
            (root / 'client.jsonl').write_text(json.dumps(c) + '\n')
            def telemetry(*args):
                return dict(roles={role: {'nrThrottledDelta': 1 if role == throttled else 0} for role in roles}, series=[dict(role=role, nrThrottledWhollyWithinWindowDelta=1 if role == throttled else 0, nrThrottledBoundaryIntervalDelta=0, counterResets=[], missingCounterIntervals=[1] if unknown and role == 'client' else [], identity=role, bracketsWindow=True) for role in roles], unparsed=[], collectorErrors=[])
            analysis.telemetry = telemetry
            sys.argv = ['analyze_before.py', '--phase-dir', str(root), '--out', str(root / 'result.json')]
            analysis.main()
            verdict = json.loads((root / 'result.json').read_text())['staticStaircase']
            expected = 'unknown' if unknown or missing_slo or missing_latency else 'ineligible' if failure or latency or throttled != 'native' or (elapsed is not None and elapsed > 1000000000) else 'eligible'
            assert verdict['status'] == expected, (name, verdict)
            checks[name] = verdict
        run_case('allocated-native-only')
        for role in ('server', 'edgemesh', 'client', 'unknown'):
            run_case(role + '-throttle', throttled=role)
        run_case('unknown-evidence', unknown=True)
        run_case('request-failure', failure=True)
        run_case('fixed-L2-deadline-failure', latency=True)
        run_case('p95-SLO-equality-allowed', elapsed=1000000000)
        run_case('p95-SLO-exceeded-stop', elapsed=1000000001)
        run_case('missing-SLO-unknown', missing_slo=True)
        run_case('missing-latency-unknown', missing_latency=True)
    analysis.telemetry = original_telemetry
    subprocess.run([sys.executable, str(ROOT / 'analyze_before.py'), '--phase-dir', str(RAW), '--out', str(OUT / 'before-new-summary.json')], check=True)
    assert hashlib.sha256((RAW / 'summary.json').read_bytes()).hexdigest() == before_hash
    assert hashlib.sha256((ROOT / 'conduct_before.py').read_bytes()).hexdigest() == conduct_hash
    receipt = dict(startUTC=start, endUTC=datetime.datetime.now(datetime.timezone.utc).isoformat(), checks=checks, originalSummarySHA256=before_hash, unchangedConductSHA256=conduct_hash, analyzerSHA256=hashlib.sha256((ROOT / 'analyze_before.py').read_bytes()).hexdigest(), liveCalls=0, latencyContract='Main082 user latency-stop decision consumes matching frozen canonical Workload p95 SLO; existing postwarm elapsedMS nearest-rank p95, strictly above bound stops, equality allowed, missing evidence unknown. L2/socket2.02 separate and unchanged.')
    (OUT / 'smoke-result.json').write_text(json.dumps(receipt, indent=2) + '\n')
    print(json.dumps(receipt))

if __name__ == '__main__':
    main()

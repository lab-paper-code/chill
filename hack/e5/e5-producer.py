#!/usr/bin/env python3
"""Injected into an approved load Job; delegates actual loading/replay/HTTP to chill_loadgen."""
import datetime as dt
import base64
import hashlib
import json
from pathlib import Path
import sys
import time
import threading
import zlib
from chill_loadgen import cli


def identity(records):
    return [{'recordID': rid, 'fields': {k: {'bytes': len(v), 'sha256': hashlib.sha256(v).hexdigest()} for k, v in inputs.items()}} for rid, inputs in records]


def main():
    binding = json.loads(zlib.decompress(base64.b64decode(sys.argv[1],validate=True)))

    def frozen(records, **kw):
        actual = identity(records)
        if actual != binding['payload']:
            raise ValueError('Frozen evaluation payload IDs/order/bytes differ; no inference sent')
        offsets = []
        base = 0
        for segment in binding['segments']:
            if segment['rate'] > 0:
                offsets.extend(base+x for x in cli.arrival_offsets_ns('poisson', segment['rate'], segment['seconds'], kw['seed']))
            base += round(segment['seconds']*1e9)
        encoded = json.dumps(offsets, separators=(',', ':')).encode()
        if hashlib.sha256(encoded).hexdigest() != binding['arrivalSHA256'] or len(offsets) != binding['offered']:
            raise ValueError('Installed CLI arrival generation differs; no inference sent')
        ready_at=dt.datetime.now(dt.timezone.utc);deadline=time.monotonic()+binding['barrierSeconds']
        print(json.dumps({'e5Event':'ready','at':ready_at.isoformat(),'barrierSeconds':binding['barrierSeconds'],'payload':actual,'offered':len(offsets),'arrivalSHA256':binding['arrivalSHA256']}),flush=True)
        while not Path('/tmp/e5-start').exists():
            if time.monotonic() >= deadline:
                raise RuntimeError('Finite producer barrier expired; no inference sent')
            time.sleep(.05)
        if time.monotonic()>=deadline:raise RuntimeError('Actual producer barrier expired before release; no inference sent')
        start = dt.datetime.now(dt.timezone.utc).isoformat()
        start_ns = time.monotonic_ns()
        print(json.dumps({'e5Event': 'start', 'at': start, 'monotonicNS': start_ns}), flush=True)
        def window_end():
            remaining = (start_ns+base-time.monotonic_ns())/1e9
            if remaining > 0: time.sleep(remaining)
            sys.stdout.write(json.dumps({'e5Event': 'window-end', 'at': dt.datetime.now(dt.timezone.utc).isoformat(), 'monotonicNS': time.monotonic_ns()}) + '\n')
            sys.stdout.flush()
        boundary = threading.Thread(target=window_end)
        boundary.start()
        try:
            cli.replay_open_loop(
                records, workload=kw['workload'], offsets=offsets,
                new_client=kw['new_client'], out=sys.stdout, start_ns=start_ns,
            )
        finally:
            boundary.join()
        print(json.dumps({'e5Event': 'drained', 'at': dt.datetime.now(dt.timezone.utc).isoformat(), 'monotonicNS': time.monotonic_ns()}), flush=True)
        # Preserve the same process/cgroup until CPO saves the real post-end
        # counter and removes the existing start marker. Original Job/watchdog
        # limits still bound this hold; the expired setup barrier is not reused.
        while Path('/tmp/e5-start').exists():
            time.sleep(.05)

    cli.replay = frozen
    return cli.main(sys.argv[2:])


if __name__ == '__main__':
    sys.exit(main())

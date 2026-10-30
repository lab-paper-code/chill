#!/usr/bin/env python3
"""Injected into an approved load Job; delegates actual loading/replay/HTTP to chill_loadgen."""
import datetime as dt
import base64
import hashlib
import json
import multiprocessing
from pathlib import Path
import sys
import time
import zlib
from chill_loadgen import cli


def identity(records):
    return [{'recordID': rid, 'fields': {k: {'bytes': len(v), 'sha256': hashlib.sha256(v).hexdigest()} for k, v in inputs.items()}} for rid, inputs in records]

class LockedOutput:
    def __init__(self, stream, lock):
        self.stream, self.lock = stream, lock

    def write(self, text):
        with self.lock:
            self.stream.write(text)
            self.stream.flush()

    def flush(self):
        with self.lock:
            self.stream.flush()


def replay_shards(records, offsets, duration_ns, binding, kw):
    """One Pod, independent interpreter shards, one shared release/start clock."""
    count = binding['shards']
    if type(count) is not int or count < 1:
        raise ValueError('Positive frozen producer shard count required')
    context = multiprocessing.get_context('fork')
    ready = context.Barrier(count + 1)
    release = context.Event()
    start_ns = context.Value('q', 0)
    output = LockedOutput(sys.stdout, context.Lock())
    deadline = time.monotonic() + binding['barrierSeconds']
    def emit(row):
        output.write(json.dumps(row) + '\n')
    def shard(index):
        ready.wait(timeout=binding['barrierSeconds'])
        if not release.wait(timeout=binding['barrierSeconds']):
            raise RuntimeError('Shard release barrier expired; no inference sent')
        cli.replay_open_loop(
            records, workload=kw['workload'], offsets=offsets,
            new_client=kw['new_client'], out=output, start_ns=start_ns.value,
            shard_index=index, shard_count=count,
        )
    children = []
    try:
        # Fork before any request/boundary thread exists; TLS and records are prepared.
        for index in range(count):
            child = context.Process(target=shard, args=(index,))
            child.start()
            children.append(child)
        ready.wait(timeout=max(0, deadline-time.monotonic()))
        ready_at = dt.datetime.now(dt.timezone.utc)
        emit({'e5Event':'ready','at':ready_at.isoformat(),'barrierSeconds':binding['barrierSeconds'],
              'payload':binding['payload'],'offered':len(offsets),
              'arrivalSHA256':binding['arrivalSHA256'],'shards':count})
        while not Path('/tmp/e5-start').exists():
            if time.monotonic() >= deadline:
                raise RuntimeError('Finite producer barrier expired; no inference sent')
            time.sleep(.05)
        if time.monotonic() >= deadline:
            raise RuntimeError('Actual producer barrier expired before release; no inference sent')
        start_ns.value = time.monotonic_ns()
        emit({'e5Event':'start','at':dt.datetime.now(dt.timezone.utc).isoformat(),
              'monotonicNS':start_ns.value,'shards':count})
        release.set()
        remaining = (start_ns.value + duration_ns-time.monotonic_ns()) / 1e9
        if remaining > 0:
            time.sleep(remaining)
        emit({'e5Event':'window-end','at':dt.datetime.now(dt.timezone.utc).isoformat(),
              'monotonicNS':time.monotonic_ns()})
        for child in children:
            child.join()
        if any(child.exitcode != 0 for child in children):
            raise RuntimeError('Producer shard failed; frozen planned population is incomplete')
        emit({'e5Event':'drained','at':dt.datetime.now(dt.timezone.utc).isoformat(),
              'monotonicNS':time.monotonic_ns(),'shards':count})
    finally:
        for child in children:
            if child.is_alive():
                child.terminate()
            child.join()



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
                offsets.extend(base+x for x in cli.arrival_offsets_ns(kw['arrivals'], segment['rate'], segment['seconds'], kw['seed']))
            base += round(segment['seconds']*1e9)
        encoded = json.dumps(offsets, separators=(',', ':')).encode()
        if hashlib.sha256(encoded).hexdigest() != binding['arrivalSHA256'] or len(offsets) != binding['offered']:
            raise ValueError('Installed CLI arrival generation differs; no inference sent')
        replay_shards(records, offsets, base, binding, kw)
        # Preserve the same process/cgroup until CPO saves the real post-end
        # counter and removes the existing start marker. Original Job/watchdog
        # limits still bound this hold; the expired setup barrier is not reused.
        while Path('/tmp/e5-start').exists():
            time.sleep(.05)

    cli.replay = frozen
    return cli.main(sys.argv[2:])


if __name__ == '__main__':
    sys.exit(main())

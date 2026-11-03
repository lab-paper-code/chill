"""Plain-text command progress; complete byte-for-byte evidence stays on disk."""
import codecs
import os
from pathlib import Path
import signal
import subprocess
import threading
import time


HEARTBEAT_SECONDS = 5


def _signal_group(process, sig):
    try:
        os.killpg(process.pid, sig)
    except ProcessLookupError:
        pass


def _stop(process):
    _signal_group(process, signal.SIGTERM)
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        pass
    finally:
        # A wrapper may have exited while a worker still ignores TERM.
        _signal_group(process, signal.SIGKILL)
        process.wait()


def _tee(reader, decoder, pending, label, channel, final=False):
    # Snapshot the end: an inherited descriptor must not make final draining wait.
    remaining = os.fstat(reader.fileno()).st_size - reader.tell() if final else 65536
    while True:
        raw = reader.read(min(65536, remaining))
        remaining -= len(raw)
        pending += decoder.decode(raw, final=final and not remaining)
        lines = pending.replace('\r', '\n').split('\n')
        pending = lines.pop()
        for line in lines:
            if line:
                print('[%s %s] %s' % (label, channel, line), flush=True)
        # Bound memory for tools printing enormous lines without newlines.
        if len(pending) >= 4096 or (final and not remaining and pending):
            print('[%s %s] %s' % (label, channel, pending), flush=True)
            pending = ''
        if not final or not remaining or not raw:
            return pending


def command(argv, stdout_path, stderr_path, *, cwd, env, timeout, label, stream=False):
    """Run a process group, optionally teeing human logs; return its exit code.

    Only the supplied evidence filename is displayed, never argv or environment.
    Reading evidence files avoids pipe-reader threads blocked by descendants.
    """
    label = Path(label).name.replace('\r', '?').replace('\n', '?')
    started = time.monotonic()
    process = None
    previous_term = None

    def event(message):
        print('[%s] %s elapsed=%.1fs' % (label, message, time.monotonic() - started), flush=True)

    def terminate(signum, frame):
        raise SystemExit(128 + signum)

    if threading.current_thread() is threading.main_thread():
        previous_term = signal.signal(signal.SIGTERM, terminate)
    try:
        event('BEGIN')
        with Path(stdout_path).open('wb') as output, Path(stderr_path).open('wb') as errors:
            with Path(stdout_path).open('rb') as out_reader, Path(stderr_path).open('rb') as err_reader:
                readers = [(out_reader, 'stdout'), (err_reader, 'stderr')] if stream else []
                decoders = [codecs.getincrementaldecoder('utf-8')(errors='replace') for _ in readers]
                pending = ['' for _ in readers]

                def tee(final=False):
                    for index, (reader, channel) in enumerate(readers):
                        pending[index] = _tee(reader, decoders[index], pending[index], label, channel, final)

                process = subprocess.Popen(argv, cwd=cwd, env=env, stdout=output,
                                           stderr=errors, start_new_session=True)
                deadline = started + timeout
                heartbeat = started + HEARTBEAT_SECONDS
                try:
                    while True:
                        tee()
                        now = time.monotonic()
                        if process.poll() is not None:
                            break
                        if now >= deadline:
                            raise subprocess.TimeoutExpired(argv, timeout)
                        if now >= heartbeat:
                            event('WAIT remaining=%.1fs' % max(0, deadline - now))
                            heartbeat = now + HEARTBEAT_SECONDS
                        try:
                            process.wait(timeout=min(0.1, deadline - now))
                        except subprocess.TimeoutExpired:
                            pass
                except BaseException:
                    _stop(process)
                    try:
                        tee(final=True)
                    except BaseException:
                        pass  # Preserve the original timeout, signal or output failure.
                    raise
                finally:
                    # Do not leave background workers behind when their wrapper exits.
                    _signal_group(process, signal.SIGKILL)
                tee(final=True)
        event('END exit=%d' % process.returncode)
        return process.returncode
    except BaseException as error:
        # Exception text may contain argv, credentials or large response bodies.
        try:
            event('END ERROR type=%s exit=%s' % (type(error).__name__,
                                               process.returncode if process else 'unknown'))
        except BaseException:
            pass
        raise
    finally:
        if previous_term is not None:
            signal.signal(signal.SIGTERM, previous_term)

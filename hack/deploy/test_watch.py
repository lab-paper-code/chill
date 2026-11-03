"""Consumer-visible watch checks using real local processes and output pipes."""
import json
import os
from pathlib import Path
import selectors
import signal
import subprocess
import sys
import tempfile
import time
import unittest


DRIVER = r'''
import json, pathlib, sys
import watch
root = pathlib.Path(sys.argv[1])
try:
    code = watch.command(json.loads(sys.argv[2]), root / 'stdout', root / 'stderr',
                         cwd=root, env=None, timeout=float(sys.argv[3]),
                         label='command.log', stream=sys.argv[4] == 'yes')
    result = {'code': code}
except BaseException as error:
    result = {'exception': type(error).__name__}
(root / 'result.json').write_text(json.dumps(result))
'''


class WatchCommands(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.processes = []

    def tearDown(self):
        for process in self.processes:
            if process.poll() is None:
                process.terminate()
            try:
                process.communicate(timeout=8)
            except subprocess.TimeoutExpired:
                process.kill()
                process.communicate()
        self.tmp.cleanup()

    def start(self, source, *, stream=False, timeout=15):
        process = subprocess.Popen(
            [sys.executable, '-c', DRIVER, str(self.root),
             json.dumps([sys.executable, '-c', source]), str(timeout), 'yes' if stream else 'no'],
            cwd=Path(__file__).resolve().parent, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.processes.append(process)
        return process

    def until(self, process, token, timeout=8):
        output = b''
        deadline = time.monotonic() + timeout
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            while token not in output:
                remaining = deadline - time.monotonic()
                self.assertGreater(remaining, 0, 'live output was not flushed: %r' % output)
                self.assertTrue(selector.select(remaining), 'no live output: %r' % output)
                chunk = os.read(process.stdout.fileno(), 65536)
                self.assertTrue(chunk, 'watch exited before progress: %r' % output)
                output += chunk
        return output

    def finish(self, process):
        output, errors = process.communicate(timeout=10)
        self.assertEqual(process.returncode, 0, errors.decode(errors='replace'))
        return output, json.loads((self.root / 'result.json').read_text())

    def wait_file(self, name):
        deadline = time.monotonic() + 5
        while not (self.root / name).exists():
            self.assertLess(time.monotonic(), deadline, 'child did not become ready')
            time.sleep(0.01)
        return (self.root / name).read_text()

    def assert_stopped(self, pid):
        deadline = time.monotonic() + 3
        while True:
            try:
                # Orphan zombies may await the host's reaper, but cannot do work.
                state = Path('/proc/%d/stat' % pid).read_text().rsplit(')', 1)[1].split()[0]
            except FileNotFoundError:
                return
            if state == 'Z':
                return
            self.assertLess(time.monotonic(), deadline, 'process %d survived cleanup' % pid)
            time.sleep(0.02)

    def test_streams_before_exit_and_preserves_both_byte_logs(self):
        source = r'''
import os, pathlib, time
os.write(1, b'first-live-line\n')
os.write(2, b'first-live-error\n')
while not pathlib.Path('release').exists(): time.sleep(.01)
os.write(1, b'\xe2'); time.sleep(.2); os.write(1, b'\x98\x83\rfinished\n' + b'x' * 90000)
os.write(2, b'\xff\x00unterminated')
'''
        process = self.start(source, stream=True)
        first = self.until(process, b'first-live-error')
        self.assertIn(b'first-live-line', first)
        self.assertIsNone(process.poll(), 'progress must arrive before the child exits')
        (self.root / 'release').touch()
        rest, result = self.finish(process)
        self.assertEqual(result, {'code': 0})
        self.assertIn('\u2603'.encode(), rest)
        self.assertEqual((self.root / 'stdout').read_bytes(),
                         b'first-live-line\n\xe2\x98\x83\rfinished\n' + b'x' * 90000)
        self.assertEqual((self.root / 'stderr').read_bytes(),
                         b'first-live-error\n\xff\x00unterminated')

    def test_quiet_capture_does_not_dump_inventory_or_argv(self):
        source = "import os; os.write(1, b'PRIVATE-INVENTORY' * 100000); os.write(2, b'PRIVATE-ERROR')"
        process = self.start(source)
        output, result = self.finish(process)
        self.assertEqual(result, {'code': 0})
        self.assertNotIn(b'PRIVATE', output)
        self.assertNotIn(source.encode(), output)
        self.assertEqual((self.root / 'stdout').read_bytes(), b'PRIVATE-INVENTORY' * 100000)
        self.assertEqual((self.root / 'stderr').read_bytes(), b'PRIVATE-ERROR')
        self.assertIn(b'exit=0', output)

    def test_silent_wait_emits_live_heartbeat(self):
        process = self.start("import pathlib,time\nwhile not pathlib.Path('release').exists(): time.sleep(.01)")
        output = self.until(process, b'remaining=')
        self.assertIn(b'elapsed=', output)
        self.assertIsNone(process.poll())
        self.assertEqual((self.root / 'stdout').read_bytes(), b'')
        (self.root / 'release').touch()
        _, result = self.finish(process)
        self.assertEqual(result, {'code': 0})

    def worker_source(self, *, fail=False, ignore_term=False):
        worker = ("import os,pathlib,signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); "
                  "pathlib.Path('worker').write_text(str(os.getpid())); time.sleep(60)")
        return '\n'.join([
            'import os,pathlib,signal,subprocess,sys,time',
            'signal.signal(signal.SIGTERM, signal.SIG_IGN)' if ignore_term else '',
            'subprocess.Popen([sys.executable, "-c", %r])' % worker,
            'while not pathlib.Path("worker").exists(): time.sleep(.01)',
            'pathlib.Path("wrapper").write_text(str(os.getpid()))',
            'raise SystemExit(23)' if fail else 'time.sleep(60)',
        ])

    def test_failed_wrapper_does_not_leave_worker_running(self):
        process = self.start(self.worker_source(fail=True))
        output, result = self.finish(process)
        self.assertEqual(result, {'code': 23})
        self.assertIn(b'exit=23', output)
        self.assert_stopped(int(self.wait_file('worker')))
        self.assert_stopped(int(self.wait_file('wrapper')))

    def test_timeout_kills_worker_even_if_wrapper_exits_or_ignores_term(self):
        for ignore_term in (False, True):
            with self.subTest(ignore_term=ignore_term):
                for name in ('worker', 'wrapper', 'result.json'):
                    (self.root / name).unlink(missing_ok=True)
                started = time.monotonic()
                process = self.start(self.worker_source(ignore_term=ignore_term), timeout=1)
                worker = int(self.wait_file('worker'))
                wrapper = int(self.wait_file('wrapper'))
                output, result = self.finish(process)
                self.assertEqual(result, {'exception': 'TimeoutExpired'})
                self.assertIn(b'TimeoutExpired', output)
                self.assertLess(time.monotonic() - started, 9)
                self.assert_stopped(worker)
                self.assert_stopped(wrapper)

    def test_termination_signal_cleans_up_process_group(self):
        process = self.start(self.worker_source())
        worker = int(self.wait_file('worker'))
        wrapper = int(self.wait_file('wrapper'))
        process.send_signal(signal.SIGTERM)
        _, result = self.finish(process)
        self.assertEqual(result, {'exception': 'SystemExit'})
        self.assert_stopped(worker)
        self.assert_stopped(wrapper)


if __name__ == '__main__':
    unittest.main()

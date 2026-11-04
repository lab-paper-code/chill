#!/usr/bin/env python3
"""Offline 3-7 capture regression: bounded rotating logs and a sleeping reducer."""
import io
import json
from pathlib import Path
import tempfile
import threading
import time
import unittest

from conductor import ProducerLog


class RotatingFollow(io.RawIOBase):
    """A follow cursor loses EOF's suffix if its 10Mi segment is evicted."""
    segment_bytes = 10 * 1024 * 1024
    retained_files = 5

    def __init__(self, data):
        super().__init__()
        self.data = data
        self.emitted = self.cursor = 0
        self.lost = False
        self.finished = threading.Event()
        self.condition = threading.Condition()
        self.writer = threading.Thread(target=self.emit)
        self.writer.start()

    def readable(self):
        return True

    def emit(self):
        for stop in range(65536, len(self.data) + 65536, 65536):
            with self.condition:
                self.emitted = min(stop, len(self.data))
                self.condition.notify_all()
            time.sleep(.00625)  # About 10MiB/s; logging does not wait for the reader.
        with self.condition:
            self.finished.set()
            self.condition.notify_all()

    def readinto(self, buffer):
        with self.condition:
            self.condition.wait_for(lambda: self.emitted > self.cursor or self.finished.is_set())
            oldest = max(0, (self.emitted // self.segment_bytes - self.retained_files + 1) * self.segment_bytes)
            if self.cursor < oldest:
                self.lost = True
                return 0
            count = min(len(buffer), self.emitted - self.cursor)
            buffer[:count] = self.data[self.cursor:self.cursor + count]
            self.cursor += count
            return count


class ProducerCaptureTests(unittest.TestCase):
    def test_80mb_rotation_during_slow_reducer(self):
        planned = 134156
        data = b''.join((json.dumps({'requestIndex': index, 'scheduledOffsetNs': index,
                                    'finishedMonotonicNS': index + 1, 'padding': 'x' * 540}) + '\n').encode()
                        for index in range(planned))
        data += b'{"evaluationEvent":"drained"}\n'
        self.assertGreater(len(data), 80_000_000)
        results = {}
        for threaded in (False, True):
            with self.subTest(threaded=threaded), tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / 'client.jsonl'
                source = RotatingFollow(data)
                if threaded:
                    capture = ProducerLog(source, path)
                else:
                    with path.open('wb') as output:
                        output.write(source.read(65536))
                # Model synchronous provisional reduction: no event parsing here.
                self.assertTrue(source.finished.wait(30))
                if threaded:
                    self.assertTrue(capture.done.wait(10))
                    events = [json.loads(line) for line in capture.lines()]
                    self.assertTrue(capture.eof())
                    capture.close()
                else:
                    with path.open('ab') as output:
                        output.write(source.read())
                    events = [json.loads(line) for line in path.read_bytes().splitlines()[:-1]]
                source.writer.join()
                rows = sum('scheduledOffsetNs' in row for row in events)
                drained = any(row.get('evaluationEvent') == 'drained' for row in events)
                results['threaded' if threaded else 'legacy'] = {'bytes': path.stat().st_size,
                                                                'rows': rows, 'planned': planned,
                                                                'drained': drained, 'rotationLoss': source.lost}
                if threaded:
                    self.assertEqual(path.stat().st_size, len(data))
                    self.assertEqual(rows, planned)
                    self.assertTrue(drained)
                    self.assertFalse(source.lost)
                else:
                    self.assertLess(rows, planned)
                    self.assertFalse(drained)
                    self.assertTrue(source.lost)
        print(json.dumps({'rotation': '10Mi x 5', 'slowReducer': True, 'results': results}, sort_keys=True))


if __name__ == '__main__':
    unittest.main()

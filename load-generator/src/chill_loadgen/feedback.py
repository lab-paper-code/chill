"""One best-effort feedback connection, independent of inference arrivals."""

from __future__ import annotations

import queue
import threading
from typing import Callable

from chill_loadgen.http_client import WorkloadHTTPClient


class ClientObservationReporter:
    def __init__(self, workload: str, new_client: Callable[[], WorkloadHTTPClient], emit: Callable) -> None:
        self._workload = workload
        self._new_client = new_client
        self._emit = emit
        self._queue = queue.SimpleQueue()
        self._done = threading.Event()
        self._failure = None
        self._errors = []
        self._thread = threading.Thread(target=self._run, name="chill-client-feedback", daemon=True)
        self._thread.start()

    def enqueue(self, observation: dict) -> None:
        self._queue.put(observation)

    def _run(self) -> None:
        client = None
        try:
            while True:
                observation = self._queue.get()
                if observation is None:
                    break
                try:
                    if client is None:
                        client = self._new_client()
                    client.report_observation(self._workload, observation)
                except Exception as error:
                    self._errors.append({"clientFeedbackEvent": "failed", "workload": self._workload,
                                         "observationID": observation["observationID"], "error": type(error).__name__})
                    if client is not None:
                        client.close()
        except BaseException as error:
            self._failure = error
        finally:
            try:
                if client is not None:
                    client.close()
            except BaseException as error:
                if self._failure is None:
                    self._failure = error
            finally:
                self._done.set()

    def __enter__(self) -> ClientObservationReporter:
        return self

    def __exit__(self, kind, error, traceback) -> None:
        try:
            self.close()
        except BaseException:
            if kind is None:
                raise

    def close(self) -> None:
        self._queue.put(None)
        self._done.wait()
        for event in self._errors:
            self._emit(event)
        if self._failure is not None:
            raise RuntimeError("Client feedback output failed") from self._failure

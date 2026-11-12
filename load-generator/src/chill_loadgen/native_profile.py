"""Off-node traffic and raw request evidence for fixed-batch native profiling."""
from __future__ import annotations

import base64
import json
import math
import random
import threading
import time
from datetime import datetime, timezone
from http.client import HTTPConnection
from pathlib import Path
from urllib.parse import urlsplit

from chill_loadgen.schedule import ScheduledRequest, run_schedule

FRAME_BYTES = 64 << 20


def timestamp(nanos):
    seconds, fraction = divmod(nanos, 1_000_000_000)
    return datetime.fromtimestamp(seconds, timezone.utc).strftime("%Y-%m-%dT%H:%M:%S") + f".{fraction:09d}Z"


def timestamp_ns(value):
    if not isinstance(value, str) or not value.endswith("Z"):
        raise ValueError("native timestamps must be UTC RFC3339Nano")
    whole, _, fraction = value[:-1].partition(".")
    if len(fraction) > 9 or fraction and not fraction.isdigit():
        raise ValueError("native timestamp fraction is invalid")
    seconds = datetime.strptime(whole, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
    epoch = datetime(1970, 1, 1, tzinfo=timezone.utc)
    delta = seconds - epoch
    return (delta.days * 86400 + delta.seconds) * 1_000_000_000 + int(fraction.ljust(9, "0") or "0")


def positive(value, name, zero=False):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or value < 0 or not zero and value == 0:
        raise ValueError(f"{name} must be finite and {'nonnegative' if zero else 'positive'}")
    return value


def integer(value, name, minimum=1):
    if type(value) is not int or value < minimum:
        raise ValueError(f"{name} must be an integer >= {minimum}")
    return value


def connection(endpoint, timeout):
    parsed = urlsplit(endpoint)
    if parsed.scheme != "http" or not parsed.hostname or parsed.username or parsed.password or parsed.path not in ("", "/") or parsed.query or parsed.fragment:
        raise ValueError("native/control endpoint must be an HTTP origin")
    return HTTPConnection(parsed.hostname, parsed.port, timeout=timeout)


def control_call(endpoint, method, path, payload=None, max_bytes=FRAME_BYTES):
    client = connection(endpoint, None)
    try:
        body = None if payload is None else json.dumps(payload, allow_nan=False, separators=(",", ":")).encode()
        client.request(method, path, body, {"Content-Type": "application/json"})
        response = client.getresponse()
        data = response.read(max_bytes + 1)
        if len(data) > max_bytes:
            raise ValueError("measurement control response exceeds result budget")
        if response.status != 200:
            raise ValueError(f"measurement control HTTP {response.status}: {data[:1024]!r}")
        value = json.loads(data)
        if not isinstance(value, dict):
            raise ValueError("measurement control response must be an object")
        return value
    finally:
        client.close()


class NativeClient:
    def __init__(self, plan):
        points = plan["operatingPoints"]
        self.client = connection(plan["nativeEndpoint"], points["requestLifetimeSeconds"] + points["deliveryMarginMilliseconds"] / 1000)
        self.path = plan["inferPath"]

    def infer(self, body):
        self.client.request("POST", self.path, body, {"Content-Type": "application/json"})
        response = self.client.getresponse()
        data = response.read(FRAME_BYTES + 1)
        if len(data) > FRAME_BYTES:
            raise ValueError("native response exceeds IPC frame budget")
        if response.status != 200:
            try:
                failure = json.loads(data)
            except ValueError:
                failure = {}
            error = ValueError(f"native inference HTTP {response.status}: {data[:1024]!r}")
            if isinstance(failure, dict) and failure.get("executionComplete") is True and failure.get("failureReason") == "MemoryAllocationFailed":
                error.failure_reason = "MemoryAllocationFailed"
            raise error
        result = json.loads(data)
        if not isinstance(result, dict) or result.get("executionComplete") is not True:
            raise ValueError("native inference did not complete")
        return result

    def close(self):
        self.client.close()


def request_body(plan, request_id, inputs, arrival_at, sender_elapsed_ms):
    points = plan["operatingPoints"]
    return json.dumps({"id": request_id, "context": {
        "requestID": request_id, "workloadUID": plan["podUID"], "definition": plan["workloadResourceVersion"],
        "admissionPolicy": "Delay", "receivedAt": arrival_at, "senderElapsedMS": sender_elapsed_ms,
        "completionMarginMS": points["deliveryMarginMilliseconds"],
        "slo": {"percentile": plan["sloPercentile"], "targetMS": plan["sloTargetLatencyNanos"] / 1_000_000},
        "lifetimeMS": points["requestLifetimeSeconds"] * 1000,
    }, "inputs": inputs}, allow_nan=False, separators=(",", ":")).encode()


def native_samples(requests, batch, pod_uid):
    """Deduplicate actual batches; preserve native request membership order."""
    members = {}
    batches = {}
    for row in requests:
        request_id = row["requestID"]
        if request_id in members:
            raise ValueError("duplicate native request ID")
        response = row["response"]
        progress = response["progress"]
        if response.get("executionComplete") is not True or response.get("id") != request_id or progress.get("requestID") != request_id:
            raise ValueError("native response request identity/completion mismatch")
        batch_id = integer(progress.get("batchID"), "native batch ID")
        if progress.get("batchSize") != batch:
            raise ValueError("native batch size differs from fixed batch")
        ids = progress.get("batchRequestIDs")
        if not isinstance(ids, list) or len(ids) != batch or len(set(ids)) != batch or request_id not in ids:
            raise ValueError("native batch membership does not align with fixed batch")
        pair = progress.get("serviceSample")
        if not isinstance(pair, dict) or set(pair) != {"serviceDurationNanos", "precedingIdleNanos", "trialID"} or pair.get("trialID") != pod_uid:
            raise ValueError("native service pair/provenance is invalid")
        duration = integer(pair["serviceDurationNanos"], "service duration")
        gap = pair["precedingIdleNanos"]
        if gap is not None:
            integer(gap, "preceding idle", 0)
        if duration > (1 << 63) - 1 or gap is not None and gap > (1 << 63) - 1 or batch_id > (1 << 64) - 1:
            raise ValueError("native timing or batch ID exceeds integer contract")
        members[request_id] = row
        signature = (ids, pair, progress.get("preprocessMS"), progress.get("backendMS"), progress.get("postprocessMS"),
                     progress.get("firstTokenMS"), progress.get("batchStartedAt"), progress.get("batchFinishedAt"))
        if batch_id in batches and batches[batch_id][0] != signature:
            raise ValueError("member responses disagree on native batch evidence")
        batches[batch_id] = (signature, progress)
    samples, latencies = [], []
    for batch_id in sorted(batches):
        _, progress = batches[batch_id]
        ids = progress["batchRequestIDs"]
        if any(request_id not in members for request_id in ids):
            raise ValueError("native batch membership is incomplete")
        rows = [members[request_id] for request_id in ids]
        if any(row["response"]["progress"]["batchID"] != batch_id for row in rows):
            raise ValueError("native batch membership disagrees with member batch IDs")
        pair = progress["serviceSample"]
        preprocess = positive(progress.get("preprocessMS"), "preprocess span", True)
        backend = positive(progress.get("backendMS"), "backend span")
        postprocess = positive(progress.get("postprocessMS"), "postprocess span", True)
        service = pair["serviceDurationNanos"] / 1_000_000
        native = max(positive(row["response"]["progress"].get("nativeElapsedMS"), "native request span") for row in rows)
        latency = max(positive(row["latencyMs"], "client request span") for row in rows)
        if preprocess + backend + postprocess > service or service > native or native > latency:
            raise ValueError("native/client timing spans are not nested")
        enqueued = []
        for row in rows:
            timing = row["response"]["progress"].get("nativeHTTP", {})
            enqueued.append(timestamp_ns(timing.get("handlerEnteredAt")) + integer(timing.get("enqueueNanos"), "native enqueue offset", 0))
        sample = dict(pair, batchID=batch_id, batchSize=batch,
                      recordIDs=[row["recordID"] for row in rows],
                      units=[integer(row["response"]["progress"].get("units"), "native units") for row in rows],
                      runnerServiceMs=service, preprocessMs=preprocess, backendCallMs=backend,
                      postprocessMs=postprocess, nativeRequestMs=native,
                      earliestEnqueuedAt=timestamp(min(enqueued)), latestEnqueuedAt=timestamp(max(enqueued)))
        for source, target in (("batchStartedAt", "startedAt"), ("batchFinishedAt", "finishedAt")):
            if progress.get(source) is not None:
                timestamp_ns(progress[source])
                sample[target] = progress[source]
        if "firstTokenMS" in progress:
            sample["firstTokenMs"] = positive(progress["firstTokenMS"], "first token span")
        samples.append(sample)
        latencies.append(latency)
    return samples, latencies


def phase_complete(elapsed, duration, known_gaps, minimum, issued, batch):
    return elapsed >= duration and known_gaps >= minimum and issued % batch == 0


def phase_specs(plan, saturation_throughput):
    positive(saturation_throughput, "measured saturation input throughput")
    points = plan["operatingPoints"]
    return [{"id": f"utilization-{index}", "kind": "utilization", "utilization": value,
             "arrivalRatePerSecond": value * saturation_throughput}
            for index, value in enumerate(points["utilizations"])]


def run_phase(plan, records, spec, new_client=None, clock_ns=time.monotonic_ns):
    """Keep saturation K closed-loop; issue Poisson arrivals independently of responses."""
    if new_client is None:
        new_client = lambda: NativeClient(plan)
    points = plan["operatingPoints"]
    start_ns, start_wall = clock_ns(), time.time_ns()
    batch = plan["batch"]
    condition = threading.Condition()
    rows, seen, errors = [], {}, []
    issued, active = 0, 0
    known_gaps = 0
    stopped = False
    request_limit = plan["maxLatencySamples"] * batch

    def reserve():
        nonlocal issued, stopped
        with condition:
            if stopped or errors or phase_complete((clock_ns() - start_ns) / 1e9, points["phaseDurationSeconds"],
                                                   known_gaps, plan["minServiceSamples"], issued, batch):
                stopped = True
                return None
            if issued >= request_limit:
                # Outstanding full batches can still satisfy the stop rule; drain before failing.
                return None
            index = issued
            issued += 1
            return index

    def send(index, scheduled, client):
        nonlocal known_gaps
        record_id, inputs = records[index % len(records)]
        request_id = f"{spec['id']}-{index}"
        arrival = timestamp(start_wall + scheduled - start_ns)
        body = request_body(plan, request_id, inputs, arrival, max(0, clock_ns() - scheduled) / 1e6)
        sent = clock_ns()
        response = client.infer(body)
        completed = clock_ns()
        progress = response.get("progress", {})
        batch_id = integer(progress.get("batchID"), "native batch ID")
        pair = progress.get("serviceSample")
        if not isinstance(pair, dict) or "precedingIdleNanos" not in pair:
            raise ValueError("native response lacks measured service pair")
        with condition:
            rows.append({"requestID": request_id, "recordID": record_id, "arrivalAt": arrival,
                         "sentAt": timestamp(start_wall + sent - start_ns),
                         "completedAt": timestamp(start_wall + completed - start_ns),
                         "latencyMs": (completed - sent) / 1e6, "response": response})
            if batch_id not in seen and pair["precedingIdleNanos"] is not None:
                known_gaps += 1
            seen[batch_id] = pair["precedingIdleNanos"]
            condition.notify_all()

    def run_request(index, scheduled):
        nonlocal active
        client = None
        try:
            client = new_client()
            send(index, scheduled, client)
        except BaseException as error:
            with condition:
                errors.append(error)
        finally:
            if client is not None:
                client.close()
            with condition:
                active -= 1
                condition.notify_all()

    def closed_worker():
        client = None
        try:
            client = new_client()
            while True:
                index = reserve()
                if index is None:
                    return
                send(index, clock_ns(), client)
        except BaseException as error:
            with condition:
                errors.append(error)
                condition.notify_all()
        finally:
            if client is not None:
                client.close()

    if spec["kind"] == "saturation":
        threads = [threading.Thread(target=closed_worker) for _ in range(spec["workers"])]
        started = []
        try:
            for thread in threads:
                thread.start()
                started.append(thread)
        finally:
            for thread in started:
                thread.join()
    else:
        rng = random.Random(0)
        offset = 0

        def wait_arrival(seconds):
            deadline = clock_ns() + round(seconds * 1e9)
            with condition:
                while True:
                    now = clock_ns()
                    elapsed = (now - start_ns) / 1e9
                    if errors or phase_complete(elapsed, points["phaseDurationSeconds"], known_gaps,
                                                plan["minServiceSamples"], issued, batch):
                        return
                    remaining = (deadline - now) / 1e9
                    if remaining <= 0:
                        return
                    if elapsed < points["phaseDurationSeconds"]:
                        remaining = min(remaining, points["phaseDurationSeconds"] - elapsed)
                    condition.wait(remaining)

        def submit(request, delayed):
            nonlocal active
            index = reserve()
            if index is None:
                return
            with condition:
                active += 1
            try:
                threading.Thread(target=run_request, args=(index, start_ns + request.scheduled_offset_ns), daemon=True).start()
            except BaseException:
                with condition:
                    active -= 1
                raise

        try:
            while True:
                with condition:
                    if errors or phase_complete((clock_ns() - start_ns) / 1e9, points["phaseDurationSeconds"],
                                                known_gaps, plan["minServiceSamples"], issued, batch) or issued >= request_limit:
                        break
                offset += max(1, round(rng.expovariate(spec["arrivalRatePerSecond"]) * 1e9))
                run_schedule([ScheduledRequest(str(issued), offset, 1)], submit, start_ns=start_ns,
                             delay_tolerance_ns=10_000_000, clock_ns=clock_ns, sleep=wait_arrival)
        finally:
            with condition:
                condition.wait_for(lambda: active == 0)
    if errors:
        raise errors[0]
    elapsed = (clock_ns() - start_ns) / 1e9
    samples, latencies = native_samples(rows, batch, plan["podUID"])
    known = sum(sample["precedingIdleNanos"] is not None for sample in samples)
    if not phase_complete(elapsed, points["phaseDurationSeconds"], known, plan["minServiceSamples"], issued, batch):
        raise ValueError("latency sample budget exhausted before phase duration and known-gap minimum")
    return dict(spec, measurementStartedAt=timestamp(start_wall), measurementEndedAt=timestamp(start_wall + round(elapsed * 1e9)),
                measurementElapsedSecondsMonotonic=elapsed, completedInputs=len(rows),
                requests=sorted(rows, key=lambda row: int(row["requestID"].rsplit("-", 1)[1])),
                nativeSamples=samples, latencyMs=latencies)


def run_profile(plan, records, control_endpoint):
    integer(plan.get("batch"), "fixed batch")
    points = plan["operatingPoints"]
    positive(points["phaseDurationSeconds"], "phase duration")
    positive(points["sparseMeanGapMilliseconds"], "sparse mean gap")
    for utilization in points["utilizations"]:
        if positive(utilization, "utilization") >= 1:
            raise ValueError("utilizations must be below one")
    prepared = []
    for record_id, inputs in records:
        if sum(4 + len(data) for data in inputs.values()) > points["framedDecodedRequestUpperBytes"]:
            raise ValueError("dataset record exceeds frozen framed decoded request upper bytes")
        prepared.append((record_id, [{"name": name, "data": [base64.b64encode(inputs[name]).decode("ascii")]}
                                     for name in plan["fields"]]))
    if not prepared:
        raise ValueError("profiling dataset contains no records")
    def measured_phase(spec):
        started = control_call(control_endpoint, "POST", "/measurement/phase", {"id": spec["id"], "action": "start"})["at"]
        phase = run_phase(plan, prepared, spec)
        ended = control_call(control_endpoint, "POST", "/measurement/phase", {"id": spec["id"], "action": "end"})["at"]
        phase["measurementStartedAt"], phase["measurementEndedAt"] = started, ended
        return phase

    sparse = measured_phase({"id": "sparse", "kind": "sparse",
                             "arrivalRatePerSecond": 1000 / points["sparseMeanGapMilliseconds"]})
    sizing = control_call(control_endpoint, "POST", "/measurement/supply", {"nativeSamples": sparse["nativeSamples"]})
    workers = integer(sizing.get("workers"), "saturation workers", plan["batch"])
    if workers > 64:
        raise ValueError("native saturation sizing exceeds worker cap")
    saturation = measured_phase({"id": "saturation", "kind": "saturation", "workers": workers})
    throughput = saturation["completedInputs"] / saturation["measurementElapsedSecondsMonotonic"]
    return {"status": "Succeeded", "phases": [sparse, saturation] + [
        measured_phase(phase) for phase in phase_specs(plan, throughput)], "supplySizing": sizing}


def main(control_endpoint, dataset_dir):
    from chill_loadgen.cli import load_records

    plan = control_call(control_endpoint, "GET", "/measurement/load")
    try:
        records = load_records(Path(dataset_dir), plan["fields"])
        result = run_profile(plan, records, control_endpoint)
        if len(json.dumps(result, allow_nan=False, separators=(",", ":")).encode()) > plan["maxResultBytes"]:
            raise ValueError("native profile result exceeds frozen runtime result budget")
    except Exception as error:
        failure = {"status": "Failed", "error": str(error)}
        if getattr(error, "failure_reason", None):
            failure["failureReason"] = error.failure_reason
        control_call(control_endpoint, "POST", "/measurement/load-result", failure)
        raise
    control_call(control_endpoint, "POST", "/measurement/load-result", result)
    return 0

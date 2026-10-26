#!/usr/bin/env python3
"""Read-only: start before CR creation; wait for READY; Ctrl-C saves results.
  capture-progress.py --node NODE --measurement NAME --namespace chill-system --output-dir /tmp/NEW --capture-load
  capture-progress.py --self-check  # offline
--kubectl accepts executable/global argv; KUBECONFIG works normally. Output must
be new/outside source. Polling: 250ms + API time (2s/request). occupancy.go deletes
terminal load Jobs before drain/evidence/status; admission.go handback removes
firstfloor later. Name-addressed logs cannot guarantee atomic binding/completeness.
"""
import argparse, copy, hashlib, json, math, shlex, subprocess, sys, tempfile, time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from unittest.mock import patch

RESOURCE = "dispatchoccupancymeasurements.edge.dacs.io"
SELECTOR = "app.kubernetes.io/component=serving-instance"

def job_name(uid):
    return "occupancy-" + hashlib.sha256(uid.encode()).hexdigest()[:20]

def identity(pod, container):
    for s in pod.get("status", {}).get("containerStatuses", []):
        if s["name"] == container and s.get("containerID"):
            return [pod["metadata"]["uid"], s["containerID"], s.get("restartCount", 0)]

def owned(pod, uid):
    return any(o.get("kind") == "Job" and o.get("uid") == uid and o.get("controller") is True
               for o in pod["metadata"].get("ownerReferences", []))

def firstfloor(pod, node):
    return (pod.get("spec", {}).get("nodeName") == node
            and pod["metadata"].get("labels", {}).get("app.kubernetes.io/component") == "serving-instance"
            and any(c["name"] == "firstfloor" for c in pod["spec"].get("containers", [])))

def job_event(uid, job):
    if job is None:
        return "job-disappeared" if uid else None
    if uid and job["metadata"]["uid"] != uid:
        return "job-identity-changed"
    if any(c.get("type") in {"Complete", "Failed"} and c.get("status") == "True"
           for c in job.get("status", {}).get("conditions", [])):
        return "job-terminal"

class Capture:
    def __init__(self, args):
        self.a, self.root, self.base = args, args.output_dir, shlex.split(args.kubectl)
        self.deadline, self.states = time.monotonic() + args.timeout, {}
        self.pool = ThreadPoolExecutor(max_workers=4)
        self.m = {"started_unix_ns": time.time_ns(), "node": args.node, "measurement_uid": None,
                  "job_name": None, "job_uid": None, "captures": [], "errors": [], "completeness_claim": False,
                  "limitations": "Polling can miss resources; name-addressed logs are not atomic physical binding. "
                  "Follow/snapshot overlap; snapshot success does not prove completeness. Node-scoped firstfloor "
                  "candidates do not prove measurement workload membership; deletion/rotation can lose bytes."}

    def command(self, argv, path=None):
        r = {"argv": argv, "started_unix_ns": time.time_ns(), "returncode": None}
        out = None
        try:
            if path:
                r["stdout_file"], out = str(path.relative_to(self.root)), path.open("xb")
            p = subprocess.run(argv, stdout=out or subprocess.PIPE, stderr=subprocess.PIPE,
                               timeout=min(2, max(.01, self.deadline-time.monotonic())))
            r.update(returncode=p.returncode, stderr=p.stderr.decode(errors="replace"))
            if not path:
                r["stdout"] = p.stdout.decode(errors="replace")
                if p.returncode == 0:
                    r["data"] = json.loads(r["stdout"] or "null")
                    del r["stdout"]
        except subprocess.TimeoutExpired as e:
            r.update(error="timeout", stderr=(e.stderr or b"").decode(errors="replace"))
            if not path:
                r["stdout"] = (e.stdout or b"").decode(errors="replace")
        except (OSError, ValueError) as e:
            r["error"] = str(e)
        finally:
            if out:
                out.close()
        if path and path.exists():
            r["stdout_bytes"] = path.stat().st_size
        r["ended_unix_ns"] = time.time_ns()
        r["ok"] = r["returncode"] == 0 and not r.get("error")
        return r

    def get(self, ns, *args):
        return self.command(self.base + ["--request-timeout=2s", "-n", ns, "get", *args, "--ignore-not-found", "-o", "json"])

    def read(self, ns, *args):
        r = self.get(ns, *args)
        if not r["ok"]:
            self.m["errors"].append(r)
        return r["ok"], r.get("data")

    def save(self):
        path = self.root / "manifest.json.tmp"
        path.write_text(json.dumps(self.m, indent=2) + "\n")
        path.replace(self.root / "manifest.json")

    def checked(self, r):
        result = self.get(r["namespace"], "pod", r["name"])
        p = result.get("data")
        result["matches"] = bool(result["ok"] and p and identity(p, r["container"]) == r["identity"]
                                 and (not r.get("owner_job_uid") or owned(p, r["owner_job_uid"])))
        return result

    def add(self, pod, role, container):
        physical = identity(pod, container)
        if physical is None or tuple(physical) in self.states:
            return
        folder = role + "-" + physical[0] + "-" + hashlib.sha256(physical[1].encode()).hexdigest()[:12] + "-" + str(physical[2])
        (self.root / folder).mkdir()
        r = {"role": role, "name": pod["metadata"]["name"], "namespace": pod["metadata"]["namespace"],
             "container": container, "identity": physical, "initial_pod": pod, "folder": folder,
             "owner_job_uid": self.m["job_uid"] if role == "load" else None,
             "snapshot": {"status": "not-triggered", "success": False}}
        s = {"r": r, "process": None, "future": None}
        self.states[tuple(physical)] = s
        self.m["captures"].append(r)
        f = r["follow"] = {"status": "not-started", "returncode": None, "started_unix_ns": time.time_ns(),
                            "stdout_file": folder + "/follow.log", "stderr_file": folder + "/follow.stderr", "before": self.checked(r)}
        if not f["before"]["matches"]:
            f["status"] = "identity-unavailable-before-start"
            return
        f["argv"] = self.base + ["--request-timeout=0", "-n", r["namespace"], "logs", r["name"], "-c", container,
                                 "--timestamps=true", "--tail=-1", "--follow=true"]
        try:
            with (self.root / f["stdout_file"]).open("xb") as out, (self.root / f["stderr_file"]).open("xb") as err:
                s["process"] = subprocess.Popen(f["argv"], stdout=out, stderr=err)
            f["status"], f["after_start"] = "running", self.checked(r)
            if not f["after_start"]["matches"]:
                self.reap(s, "identity-unavailable-after-start")
        except OSError as e:
            f.update(status="start-failed", error=str(e))

    def reap(self, s, stop=None):
        p, r = s["process"], s["r"]
        if p is not None and stop and p.poll() is None:
            r["follow"]["stop_reason"] = stop
            p.terminate()
            try:
                p.wait(timeout=1)
            except subprocess.TimeoutExpired:
                p.kill()
                p.wait(timeout=1)
        if p is not None and p.poll() is not None and r["follow"]["returncode"] is None:
            r["follow"].update(status="exited", returncode=p.returncode, ended_unix_ns=time.time_ns(),
                               stdout_bytes=(self.root / r["follow"]["stdout_file"]).stat().st_size)
        if s["future"] and s["future"].done():
            r["snapshot"] = s["future"].result()

    def snapshot(self, r, reason, prior):
        result = {"status": "unavailable", "success": False, "reason": reason,
                  "prior_follow": prior, "requested_unix_ns": time.time_ns(), "before": self.checked(r)}
        if not result["before"]["matches"]:
            result["error"] = "terminal snapshot unavailable: Pod/container identity absent or changed"
            return result
        result["logs"] = self.command(self.base + ["--request-timeout=2s", "-n", r["namespace"], "logs", r["name"],
                                      "-c", r["container"], "--timestamps=true", "--tail=-1"], self.root / r["folder"] / "snapshot.log")
        result["after"] = self.checked(r)
        result["success"] = result["logs"]["returncode"] == 0 and result["after"]["matches"]
        result["status"] = "success" if result["success"] else "unavailable-or-unverified"
        return result

    def trigger(self, s, reason):
        self.reap(s)
        if s["future"] is None:
            s["r"]["snapshot"] = {"status": "pending", "success": False, "reason": reason}
            s["future"] = self.pool.submit(self.snapshot, s["r"], reason, copy.deepcopy(s["r"]["follow"]))

    def pods(self, items, role, container):
        for p in items:
            self.add(p, role, container)
        current = {tuple(identity(p, container) or []): p for p in items}
        for key, s in self.states.items():
            if s["r"]["role"] != role:
                continue
            self.reap(s)
            p = current.get(key)
            if p is None:
                self.trigger(s, "pod-disappeared-or-container-changed")
                self.reap(s, "identity-left-selection")
            elif p["metadata"].get("deletionTimestamp") or p.get("status", {}).get("phase") in {"Succeeded", "Failed"} or any(
                    c["name"] == container and "terminated" in c.get("state", {}) for c in p.get("status", {}).get("containerStatuses", [])):
                self.trigger(s, "pod-terminal-or-deleting")

    def run(self):
        ok, cr = self.read(self.a.namespace, RESOURCE, self.a.measurement)
        if not ok or cr:
            raise RuntimeError("CR must be absent: start capture before CR creation")
        self.m["cr_absent_at_start"] = True
        ok, pods = self.read(self.a.runtime_namespace, "pods", "-l", SELECTOR, "--field-selector=spec.nodeName=" + self.a.node)
        if not ok or pods is None:
            raise RuntimeError("initial native Pod discovery failed; capture is not armed")
        self.pods([p for p in pods["items"] if firstfloor(p, self.a.node)], "firstfloor", "firstfloor")
        if any(s["process"] is None or s["process"].poll() is not None for s in self.states.values()):
            raise RuntimeError("initial native log follow failed; capture is not armed")
        print("READY: capture armed; create measurement separately", flush=True)
        terminal = None
        while time.monotonic() < self.deadline:
            ok, pods = self.read(self.a.runtime_namespace, "pods", "-l", SELECTOR, "--field-selector=spec.nodeName=" + self.a.node)
            if ok and pods:
                self.pods([p for p in pods["items"] if firstfloor(p, self.a.node)], "firstfloor", "firstfloor")
            ok, cr = self.read(self.a.namespace, RESOURCE, self.a.measurement)
            event = None
            if ok and cr:
                uid = cr["metadata"]["uid"]
                if self.m["measurement_uid"] not in (None, uid) or cr["spec"]["node"] != self.a.node:
                    raise RuntimeError("CR UID or target node changed; refusing replacement")
                self.m.update(measurement_uid=uid, measurement=cr, job_name=job_name(uid))
                if cr.get("status", {}).get("phase") in {"Complete", "Insufficient", "Failed"}:
                    event = "cr-terminal-job-may-already-be-deleted"
            elif ok and self.m["measurement_uid"]:
                event = "cr-disappeared"
            if event and terminal is None:
                terminal = time.monotonic()
            if event:
                for s in self.states.values():
                    self.trigger(s, event)
            if self.m["job_name"]:
                ok, job = self.read(self.a.load_namespace, "job", self.m["job_name"])
                if ok:
                    event = job_event(self.m["job_uid"], job) or event
                    self.m["last_job"] = job
                    if job and self.m["job_uid"] is None:
                        self.m["job_uid"] = job["metadata"]["uid"]
                if event:
                    for s in self.states.values():
                        self.trigger(s, event)
                if self.a.capture_load and self.m["job_uid"]:
                    ok, pods = self.read(self.a.load_namespace, "pods", "-l", "job-name=" + self.m["job_name"])
                    if ok and pods:
                        self.pods([p for p in pods["items"] if owned(p, self.m["job_uid"])], "load", "load")
            for s in self.states.values():
                self.reap(s)
                if event:
                    self.trigger(s, event)
            self.save()
            if terminal is not None and time.monotonic()-terminal >= 30:
                self.m["stop_reason"] = "CR terminal/disappeared plus 30-second handback grace"
                return 0
            time.sleep(min(.25, max(0, self.deadline-time.monotonic())))
        self.m["stop_reason"] = "finite timeout reached"
        return 2

    def close(self):
        for s in self.states.values():
            self.reap(s, "capture-stopped")
        self.pool.shutdown(wait=True)
        for s in self.states.values():
            self.reap(s)
            if s["future"] is None:
                s["r"]["snapshot"] = {"status": "unavailable", "success": False, "reason": "no terminal trigger before stop",
                                       "prior_follow": copy.deepcopy(s["r"]["follow"])}
        self.m["missing_capture_roles"] = [role for role in (["firstfloor", "load"] if self.a.capture_load else ["firstfloor"])
                                           if not any(s["r"]["role"] == role for s in self.states.values())]
        if self.m["job_uid"] is None:
            self.m["load_snapshot_unavailable"] = "Job UID never observed; cannot authenticate loader owner; Job may have disappeared between polls"
        self.m.update(lifecycle_finished=True, ended_unix_ns=time.time_ns())
        self.save()

def self_check():
    from types import SimpleNamespace
    p = {"metadata": {"uid": "pod", "name": "p", "namespace": "ns", "labels": {"app.kubernetes.io/component": "serving-instance"},
                      "ownerReferences": [{"kind": "Job", "uid": "job", "controller": True}]},
         "spec": {"nodeName": "target", "containers": [{"name": "firstfloor"}]},
         "status": {"containerStatuses": [{"name": "firstfloor", "containerID": "containerd://one"}]}}
    assert firstfloor(p, "target") and not firstfloor(p, "other") and owned(p, "job") and not owned(p, "wrong")
    changed = copy.deepcopy(p)
    changed["status"]["containerStatuses"][0]["containerID"] = "containerd://two"
    assert identity(p, "firstfloor") != identity(changed, "firstfloor")
    assert job_name("abc") == "occupancy-ba7816bf8f01cfea4141" and job_event("job", None) == "job-disappeared"
    assert job_event("job", {"metadata": {"uid": "job"}, "status": {"conditions": [{"type": "Complete", "status": "True"}]}}) == "job-terminal"
    with tempfile.TemporaryDirectory() as directory:
        c = Capture(argparse.Namespace(output_dir=Path(directory), kubectl="never-executed", timeout=10, node="target"))
        r = {"namespace": "ns", "name": "p", "container": "firstfloor", "identity": identity(p, "firstfloor"), "folder": "."}
        (c.root / "follow.log").write_bytes(b"2026-10-27T00:00:00Z original progress\n")
        def gone(argv, **kwargs):
            assert "get" in argv and "logs" not in argv
            return subprocess.CompletedProcess(argv, 0, stdout=b"", stderr=b"")
        with patch.object(subprocess, "run", side_effect=gone):
            result = c.snapshot(r, job_event("job", None), {"status": "exited", "returncode": 0})
        c.pool.shutdown(wait=True)
        assert result["status"] == "unavailable" and not result["success"] and result["prior_follow"]["returncode"] == 0
        assert (c.root / "follow.log").read_bytes().endswith(b"original progress\n")
        events, cr_reads = [], 0
        c.a.namespace = c.a.runtime_namespace = c.a.load_namespace = "ns"
        c.a.measurement, c.a.capture_load = "measurement", True
        c.states = {"known": {"process": SimpleNamespace(poll=lambda: None)}}
        def read_fixture(ns, resource, *args):
            nonlocal cr_reads
            if resource == RESOURCE:
                cr_reads += 1
                return True, None if cr_reads == 1 else {"metadata": {"uid": "measurement"}, "spec": {"node": "target"}}
            if resource == "job":
                return True, {"metadata": {"uid": "job"}, "status": {"conditions": [{"type": "Complete", "status": "True"}]}}
            if any(str(arg).startswith("job-name=") for arg in args):
                assert "trigger" in events
                raise StopIteration("end offline capture")
            events.append("discovery")
            return True, {"items": [p]}
        c.read = read_fixture
        c.pods = lambda *args: events.append("follow")
        c.trigger = lambda *args: events.append("trigger")
        with patch("builtins.print", side_effect=lambda *args, **kwargs: events.append("ready")):
            try:
                c.run()
            except StopIteration:
                pass
            else:
                raise AssertionError("terminal fixture did not reach loader discovery")
        assert events.index("follow") < events.index("ready") < events.index("trigger")
        cr_reads, events = 0, []
        c.states["known"]["process"] = SimpleNamespace(poll=lambda: 1)
        with patch("builtins.print", side_effect=lambda *args, **kwargs: events.append("ready")):
            try:
                c.run()
            except RuntimeError as error:
                assert "not armed" in str(error)
            else:
                raise AssertionError("exited initial follow was declared READY")
        assert "ready" not in events
    print("capture-progress offline self-check passed")

def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--self-check", action="store_true")
    for flag in ("node", "measurement", "runtime-namespace", "load-namespace"):
        parser.add_argument("--" + flag)
    parser.add_argument("--namespace", default="chill-system")
    parser.add_argument("--kubectl", default="kubectl", help="quoted executable/global argv; no shell")
    parser.add_argument("--output-dir", type=Path)
    parser.add_argument("--capture-load", action="store_true")
    parser.add_argument("--timeout", type=float, default=1800)
    a = parser.parse_args()
    if a.self_check:
        self_check()
        return 0
    if not a.node or not a.measurement or not a.output_dir or not shlex.split(a.kubectl):
        parser.error("--node, --measurement, --output-dir and nonempty --kubectl are required")
    if not math.isfinite(a.timeout) or a.timeout <= 0:
        parser.error("--timeout must be finite and positive")
    a.runtime_namespace, a.load_namespace = a.runtime_namespace or a.namespace, a.load_namespace or a.namespace
    a.output_dir = a.output_dir.resolve()
    repository = Path(__file__).resolve().parents[2]
    if a.output_dir == repository or repository in a.output_dir.parents:
        parser.error("output directory must be outside the repository")
    a.output_dir.mkdir(parents=True, exist_ok=False)
    capture, code = Capture(a), 1
    try:
        capture.save()
        code = capture.run()
    except KeyboardInterrupt:
        capture.m["stop_reason"], code = "interrupted", 130
    except Exception as e:
        capture.m["stop_reason"] = str(e)
    finally:
        capture.m["exit_code"] = code
        capture.close()
    return code

if __name__ == "__main__":
    sys.exit(main())

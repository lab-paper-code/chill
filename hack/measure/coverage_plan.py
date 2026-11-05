"""Read-only live coverage collection, eligibility, planning and manifest generation."""
import copy
import hashlib
import json
import math
import os
import ssl
import subprocess
import urllib.error
import urllib.request
from collections import Counter, defaultdict
from decimal import Decimal, InvalidOperation
from pathlib import Path

import yaml

HERE = Path(__file__).resolve().parent
CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
NS = ENV["namespace"]


def load(path):
    return json.loads(Path(path).read_text())



def identity(path):
    path = Path(path)
    return {"path": str(path), "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}



def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n")



def write_docs(path, docs):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("".join("---\n" + json.dumps(d, ensure_ascii=False) + "\n" for d in docs))



def coverage_api(path, body=None):
    """Only GET and the audited read-only preflight POST are available here."""
    namespace = "/api/v1/namespaces/" + NS
    if body is not None and path != namespace + "/batch-energy-optimizations/preflight":
        raise ValueError("only read-only preflight POST is allowed")
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None
    d2 = CONFIG["d2"]
    opener = urllib.request.build_opener(
        urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=d2["caFile"])),
        NoRedirect())
    request = urllib.request.Request(
        d2["apiBase"].rstrip("/") + path,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Authorization": "Bearer " + Path(d2["tokenFile"]).read_text().strip(),
                 "Content-Type": "application/json"})
    with opener.open(request, timeout=120) as response:
        return json.load(response)


def coverage_runtime_images(path):
    import yaml
    declarations = yaml.safe_load(path.read_text())["profiling"]["executionDeclarations"]
    images = defaultdict(set)
    for declaration in declarations:
        images[declaration["deviceClass"]].add(json.loads(declaration["runtime"]["payload"])["image"])
    if not images or any(not image for values in images.values() for image in values):
        raise ValueError("current runtime declarations must contain image identities")
    return {device: sorted(values) for device, values in images.items()}


def coverage_declared_on_node(cell, node, device, model, declarations, has_policy):
    """Resolve only a representative Node's missing runtime declaration."""
    reasons = {r["code"] for r in cell["support"].get("reasons", [])}
    if not node or reasons != {"RuntimeDeclarationMissing"}:
        return False
    path = cell["executionPath"]
    if (device["name"], path) not in has_policy and ("*", path) not in has_policy:
        return False
    execution = next(p for p in model["spec"]["executionPaths"] if p["name"] == path)
    artifact = next(a for a in model["spec"]["artifacts"] if a["name"] == execution["artifact"])
    if artifact["source"]["architecture"] != device["architecture"]:
        return False
    for declaration in declarations:
        if declaration["deviceClass"] != device["name"] or declaration.get("node", node) != node:
            continue
        payload = json.loads(declaration["runtime"]["payload"])
        if (payload["architecture"] == device["architecture"] and
                payload["runtime"]["family"] == execution["runtime"]["family"] and
                execution["runtime"]["backend"] in payload["runtime"]["backends"]):
            return True
    return False


SHARED_CURVE_SCHEMA = "chill.dacs.io/curve-plan.v1alpha11"
SHARED_EVIDENCE_SCHEMA = "chill.dacs.io/profiling-execution-evidence.v1alpha17"
EXECUTION_CONDITIONS_ANNOTATION = "chill.dacs.io/coverage-execution-conditions"


def coverage_shared_result(result):
    """Current finalized evidence seals RunConfig v1alpha19; old contracts never cover v3 cells."""
    record = result.get("result") or {}
    trials = record.get("completedTrials") or []
    return (record.get("curvePlan", {}).get("schemaVersion") == SHARED_CURVE_SCHEMA and bool(trials) and
            all(t.get("evidence", {}).get("schemaVersion") == SHARED_EVIDENCE_SCHEMA for t in trials))


def coverage_condition_matches(observed, required):
    """Compare execution validity; image identity remains historical metadata."""
    return bool(observed and required) and all(observed.get(key) == required.get(key)
        for key in ("curveSchema", "evidenceSchema"))


def coverage_required_condition(cell, node, model, declarations):
    execution = next(p for p in model["spec"]["executionPaths"] if p["name"] == cell["executionPath"])
    runtime = execution["runtime"]
    image = None
    for declaration in declarations:
        if (declaration["deviceClass"] != cell["deviceClass"]["name"] or
                declaration.get("node", node) != node):
            continue
        payload = json.loads(declaration["runtime"]["payload"])
        if (payload["runtime"]["family"] == runtime["family"] and
                runtime["backend"] in payload["runtime"]["backends"]):
            image = payload["image"]  # last matching declaration overwrites this scope
    if image is None:
        return None
    return {"runtimeImage": image, "curveSchema": SHARED_CURVE_SCHEMA,
            "evidenceSchema": SHARED_EVIDENCE_SCHEMA}


def coverage_targets(coverage, policy, required_conditions, node_declared=(), profile_conditions=None):
    targets, unknown, stale, measured_current = [], [], [], []
    if coverage["completeness"] != "complete":
        raise ValueError("incomplete coverage cannot define a complete plan")
    for cell in coverage["cells"]:
        verdict = cell["support"]["verdict"]
        if "currentMeasurement" not in cell:
            unknown.append(dict(cell, selectionReason="currentMeasurement unavailable; cell held"))
        elif verdict == policy["unknownVerdict"] and cell["id"] not in node_declared:
            unknown.append(cell)
        elif verdict == policy["supportedVerdict"] or cell["id"] in node_declared:
            required = required_conditions.get(cell["id"])
            selected = cell["currentMeasurement"]
            observed = (profile_conditions or {}).get(selected["id"]) if selected else None
            if coverage_condition_matches(observed, required):
                measured_current.append(cell)
                continue
            if selected and observed:
                stale.append(cell)
            # Coverage presentation (usable/problem/insufficient) is diagnostic,
            # not an execution gate or permission to revive historical success.
            targets.append(cell)
    return targets, unknown, stale, measured_current


def coverage_defaults(cell, node, plans, profiles):
    """Inherit protocols and inputs, never CPU allocation, even on the same node."""
    model, device, path, mode = (cell["model"]["name"], cell["deviceClass"]["name"],
                                 cell["executionPath"], cell["powerMode"])
    newest = sorted(plans, key=lambda p: p["metadata"]["name"])
    newest.sort(key=lambda p: p["metadata"]["creationTimestamp"], reverse=True)
    def exact(candidate, target):
        return (candidate["modelSpec"], candidate["deviceClass"], target,
                candidate["executionPath"], candidate["powerMode"]) == (model, device, node, path, mode)
    for plan in newest:
        if exact(plan["spec"]["state"]["candidate"], plan["spec"]["state"]["targetNode"]):
            spec = copy.deepcopy(plan["spec"])
            for field in ("cpuAffinity", "cpuQuota", "batchSizes", "retryLimit"):
                spec.pop(field, None)
            spec["state"].pop("cpuRequest", None)
            return spec, "request:" + plan["metadata"]["name"]
    matches = [p for p in profiles if
               (p["condition"]["logicalModel"], p["condition"]["deviceClass"], p["nodeName"],
                p["condition"]["artifact"]["executionPath"], p["condition"]["powerMode"]) ==
               (model, device, node, path, mode)]
    matches.sort(key=lambda p: p["id"])
    matches.sort(key=lambda p: p["measuredAt"], reverse=True)
    successful = [p for p in newest if p.get("status", {}).get("phase") == "Complete"]
    source = next((p for p in successful if p["spec"]["state"]["candidate"]["modelSpec"] == model), None)
    if source is None:
        source = next((p for p in successful if p["spec"]["state"]["candidate"]["executionPath"] == path), None)
    if matches:
        profile = matches[0]
        c = profile["condition"]
        spec = {"state": {"candidate": {"preprocessor": c["preprocessor"]["name"]}}}
        for field in ("activeProtocol", "idleProtocol"):
            protocol = c[field]
            spec[field] = {key: protocol[key] for key in
                           ("warmupIterations", "settlingDurationSeconds", "durationSeconds")
                           if key in protocol}
            if field == "activeProtocol":
                spec[field].pop("settlingDurationSeconds", None)
                spec[field]["dataset"] = protocol["dataset"]["name"]
            spec[field]["metricObservation"] = {
                "intervalMilliseconds": protocol["metricIntervalMilliseconds"],
                "timeoutMilliseconds": protocol["metricTimeoutMilliseconds"],
                "durationSeconds": protocol["metricDurationSeconds"]}
        return spec, "profile:" + profile["id"]
    if source is None:
        raise ValueError("no inherited protocol for " + cell["id"])
    spec = copy.deepcopy(source["spec"])
    for field in ("cpuQuota", "cpuAffinity", "batchSizes", "retryLimit"):
        spec.pop(field, None)
    spec["state"].pop("cpuRequest", None)
    if source["spec"]["state"]["candidate"]["modelSpec"] != model:
        spec["state"]["candidate"].pop("preprocessor", None)
        spec["activeProtocol"].pop("dataset", None)
    return spec, "request:" + source["metadata"]["name"]




def coverage_inputs(spec, model, artifact, preprocessors, datasets):
    candidates = [p for p in preprocessors if any(
        a["target"]["model"] == model and a["target"]["artifact"] == artifact
        for a in p["spec"]["artifacts"])]
    pre = next((p for p in candidates if p["metadata"]["name"] ==
                spec["state"]["candidate"].get("preprocessor")), None)
    if pre is None and len(candidates) == 1:
        pre = candidates[0]
    if pre is None:
        raise ValueError("no unambiguous compatible preprocessor for " + model + "/" + artifact)
    compatible = []
    for dataset in datasets:
        fields = {f["name"]: f for f in dataset["spec"]["fields"]}
        if all(f["name"] in fields and fields[f["name"]]["format"] == f["format"] and
               fields[f["name"]]["maxBytes"] <= f["maxBytes"] for f in pre["spec"]["input"]["fields"]):
            compatible.append(dataset["metadata"]["name"])
    dataset = spec["activeProtocol"].get("dataset")
    if dataset not in compatible:
        if len(compatible) != 1:
            raise ValueError("no unambiguous compatible dataset for " + model)
        dataset = compatible[0]
    spec["state"]["candidate"]["preprocessor"] = pre["metadata"]["name"]
    spec["activeProtocol"]["dataset"] = dataset


def coverage_workload(spec, workloads):
    """Resolve the real catalog Workload whose SLO the controller will freeze."""
    candidate = spec["state"]["candidate"]
    active = spec["activeProtocol"]
    matches = [view["desired"] for view in workloads if view.get("desired") and
               view["desired"]["spec"]["modelRef"]["name"] == candidate["modelSpec"] and
               view["desired"]["spec"]["preprocessorRef"]["name"] == candidate["preprocessor"] and
               view["desired"]["spec"].get("datasetRef", {}).get("name") == active["dataset"]]
    selected = next((workload for workload in matches if
                     workload["metadata"]["name"] == active.get("workload")), None)
    if selected is None:
        if len(matches) != 1:
            raise ValueError("no unambiguous source Workload for " + candidate["modelSpec"])
        selected = matches[0]
    percentile = selected["spec"]["slo"]["percentile"]
    if (type(percentile) not in (int, float) or not math.isfinite(percentile) or
            not 0 < percentile < 100):
        raise ValueError("source Workload SLO requires a finite percentile in (0, 100)")
    active["workload"] = selected["metadata"]["name"]
    return math.ceil(math.log(.05) / math.log(percentile / 100))


def coverage_timings(results):
    timings = []
    for result in results:
        facts = result.get("measurementFacts")
        if not facts or not coverage_shared_result(result):
            continue
        condition = facts["condition"]
        execution_conditions = {"runtimeImage": condition.get("runtimeImage"),
                                "curveSchema": SHARED_CURVE_SCHEMA,
                                "evidenceSchema": SHARED_EVIDENCE_SCHEMA}
        for batch in facts["batches"]:
            active = [q for q in batch["quality"] if q["workloadMode"] == "empirical-cadence-fixed-batch"]
            calls = batch.get("completedCalls", 0)
            seconds = sum(q["windowSeconds"] for q in active)
            if calls > 0 and active and math.isfinite(seconds) and seconds > 0:
                timings.append({"model": condition["logicalModel"], "deviceClass": condition["deviceClass"],
                                "batch": batch["batch"], "secondsPerCall": seconds / calls,
                                "executionPath": condition["artifact"]["executionPath"],
                                "powerMode": condition["powerMode"], "sourceUID": facts["request"]["uid"],
                                "source": facts["request"]["name"], "executionConditions": execution_conditions})
    return timings


def coverage_existing(cell, plans, policy, required, completed_conditions=None):
    expected = (cell["model"]["name"], cell["deviceClass"]["name"], cell["executionPath"], cell["powerMode"])
    for plan in plans:
        spec = plan.get("spec", {})
        state = spec.get("state", {})
        # A Failed request measured nothing; its cell is planned again once the cause is fixed.
        phase = plan.get("status", {}).get("phase")
        if (phase == "Failed" or any(key in spec for key in ("cpuQuota", "cpuAffinity")) or
                "cpuRequest" in state):
            continue
        selected = cell.get("currentMeasurement")
        if phase == "Complete" and (not selected or selected["id"] != "/".join(
                plan["metadata"].get(key, "") for key in ("namespace", "name", "uid"))):
            continue
        if phase == "Complete":
            observed = (completed_conditions or {}).get(plan["metadata"].get("uid"))
        else:
            encoded = plan["metadata"].get("annotations", {}).get(EXECUTION_CONDITIONS_ANNOTATION, "")
            try:
                observed = json.loads(encoded)
            except (TypeError, ValueError):
                observed = None
            if not isinstance(observed, dict):
                observed = None
        if not coverage_condition_matches(observed, required):
            continue
        metadata = plan["metadata"]
        if metadata.get("annotations", {}).get(policy["cellAnnotation"]) == cell["id"]:
            return plan
        candidate = plan.get("spec", {}).get("state", {}).get("candidate", {})
        if metadata["name"].startswith(policy["requestNamePrefix"]) and tuple(
                candidate.get(key) for key in ("modelSpec", "deviceClass", "executionPath", "powerMode")) == expected:
            return plan
    return None


def coverage_lane_order(lane, policy):
    return sorted(lane, key=lambda pair: (
        pair[1]["spec"]["state"]["candidate"]["executionPath"] in policy["laneLastExecutionPaths"] or
        pair[1]["spec"]["state"]["candidate"]["executionPath"] in
        policy["laneLastModelExecutionPaths"].get(pair[0]["model"], []),
        pair[0]["powerMode"] not in policy["laneFirstPowerModes"],
        pair[0]["powerMode"], pair[0]["model"], pair[0]["cellId"]))


def coverage_manifest(output, lanes, policy):
    sequence = []
    priorities = sorted({r["priority"] for lane in lanes.values() for r, _ in lane})
    for stage, priority in enumerate(priorities, 1):
        paths = []
        for node, lane in sorted(lanes.items()):
            ordered = coverage_lane_order([(r, d) for r, d in lane if r["priority"] == priority], policy)
            if ordered:
                path = output / "lanes" / f"{node}-stage{stage}.yaml"
                write_docs(path, [d for _, d in ordered])
                paths.append(str(path))
        sequence.append({"stage": stage, "priority": priority, "paths": paths})
    cells = [{"name": r["name"], "batches": r["batches"]} for lane in lanes.values() for r, _ in lane]
    write_json(output / "resume-plan.json", {
        "schemaVersion": "coverage-feeder-plan.v1", "sequence": sequence, "cells": cells})


def coverage_runtime_family(candidate, device):
    path, arch = candidate["executionPath"].lower(), device["architecture"]
    if "rknn" in path:
        return "RKNN"
    if "cuda" in path:
        if arch == "amd64":
            return "CUDA amd64"
        if device.get("accelerator") == "nvidia-jetson-nano":
            return "L4T r32"
        if device.get("accelerator", "").startswith("nvidia-jetson-"):
            return "L4T r35"
    elif "cpu" in path and arch in ("arm64", "amd64"):
        return "CPU " + arch
    raise ValueError("unknown pilot runtime family: " + str(candidate))


def coverage_memory(value):
    for base, suffixes in ((1024, ("Ki", "Mi", "Gi", "Ti", "Pi", "Ei")),
                           (1000, ("k", "M", "G", "T", "P", "E"))):
        for exponent, suffix in enumerate(suffixes, 1):
            if value.endswith(suffix):
                return Decimal(value[:-len(suffix)]) * base ** exponent
    return Decimal(value)


def coverage_pilot(lanes, policy, classes):
    pairs = [pair for lane in lanes.values() for pair in lane]
    if not pairs:
        return {}
    families = defaultdict(list)
    for row, doc in pairs:
        candidate = doc["spec"]["state"]["candidate"]
        family = coverage_runtime_family(candidate, classes[candidate["deviceClass"]])
        families[family].append((row, doc))
    chosen = {}
    for family in sorted(families):
        pair = min(families[family], key=lambda pair: (pair[0]["estimatedSeconds"], pair[0]["cellId"]))
        chosen[pair[0]["cellId"]] = pair
    # Model priority orders light models before the heaviest model in this plan.
    heavy = max(policy["models"][row["model"]]["priority"] for row, _ in pairs)
    stress = min((pair for pair in pairs if policy["models"][pair[0]["model"]]["priority"] == heavy),
                 key=lambda pair: (coverage_memory(classes[pair[1]["spec"]["state"]["candidate"]["deviceClass"]]["memory"]),
                                   pair[0]["estimatedSeconds"], pair[0]["cellId"]))
    chosen[stress[0]["cellId"]] = stress
    selected = defaultdict(list)
    for row, doc in sorted(chosen.values(), key=lambda pair: (pair[0]["priority"], pair[0]["cellId"])):
        selected[row["node"]].append((row, doc))
    return dict(selected)


def coverage_memory_rule(cell, execution_conditions, policy):
    matches = [rule for rule in policy["memoryMaxBatch"] if
               rule["model"] == cell["model"]["name"] and
               rule["deviceClass"] == cell["deviceClass"]["name"] and
               rule["executionPath"] in (None, cell["executionPath"])]
    if not matches:
        return None, None
    exact = [rule for rule in matches if rule["executionPath"] == cell["executionPath"]]
    selected = exact or matches
    if len(selected) != 1:
        raise ValueError("ambiguous memory exclusion for " + cell["id"])
    rule = selected[0]
    observed = rule["conditions"]
    current = dict(execution_conditions or {}, executionPath=cell["executionPath"])
    if any(observed[key] is not None and current.get(key) is not None and
           observed[key] != current[key] for key in observed):
        return rule, "conditions-changed"
    if any(observed[key] is None or current.get(key) is None for key in observed):
        return rule, "conditions-unknown"
    return rule, "applied"


def coverage_memory_limit(cell, policies, node, memory_fraction):
    """Mirror ExecutionJobs' min(policy, int64(float64(allocatable.Value()) * fraction))."""
    matches = [p for p in policies if p["deviceClass"] in ("*", cell["deviceClass"]["name"]) and
               p["executionPath"] == cell["executionPath"] and
               p["workloadMode"] == "empirical-cadence-fixed-batch"]
    exact = [p for p in matches if p["deviceClass"] == cell["deviceClass"]["name"]]
    selected = exact or matches
    if len(selected) != 1:
        return None
    limit = selected[0].get("runtimeMemoryLimitBytes")
    if (type(limit) is not int or limit <= 0 or type(memory_fraction) not in (int, float) or
            not math.isfinite(memory_fraction) or not 0 < memory_fraction < 1):
        return None
    memory = node.get("status", {}).get("allocatable", {}).get("memory")
    if not isinstance(memory, str):
        return None
    try:
        allocatable = coverage_memory(memory)
        if not allocatable.is_finite() or allocatable <= 0:
            return None
        # Quantity.Value rounds positive fractional bytes up; Go then uses float64 and truncates.
        allocatable_bytes = math.ceil(allocatable)
        node_limit = int(float(allocatable_bytes) * memory_fraction)
    except (InvalidOperation, ValueError, OverflowError):
        return None
    return min(limit, node_limit) if node_limit > 0 else None


def coverage_batch_plan(cell, batches, timings, policy, declared_batches=(), execution_conditions=None):
    model, device = cell["model"]["name"], cell["deviceClass"]["name"]
    rule, memory_status = coverage_memory_rule(cell, execution_conditions, policy)
    memory_max = rule["maxBatch"] if memory_status == "applied" else None
    review = rule is not None and memory_status != "applied"
    # Preserve approved cell exclusions; an allowed cell always explores the server domain.
    reason = policy["memoryReviewReason"] if review else (
        policy["memoryExcludedBatchReason"] if memory_max == 0 else None)
    if reason is None and declared_batches and memory_max is not None and any(
            batch > memory_max for batch in declared_batches):
        reason = policy["declaredBatchConflictReason"]
    estimates = []
    # Native sample coverage extends the window; slow historical calls never remove K.
    for batch in sorted(batches):
        # CPU and accelerator paths of one model differ by orders of magnitude; never borrow across paths.
        measured = [r for r in timings if r["model"] == model and r["deviceClass"] == device and
                    r["batch"] == batch and r.get("executionPath") == cell["executionPath"] and
                    r.get("powerMode") == cell["powerMode"] and
                    coverage_condition_matches(r.get("executionConditions"), execution_conditions)]
        source = max(measured, key=lambda r: r["secondsPerCall"]) if measured else None
        calls = math.floor(policy["windowSeconds"] / source["secondsPerCall"]) if source else None
        estimates.append({"batch": batch, "secondsPerCall": source["secondsPerCall"] if source else None,
                          "estimatedCallsInWindow": calls, "timingSource": source, "included": reason is None,
                          "reason": reason, "memoryPolicy": None if rule is None else {
                              "status": memory_status, "rule": rule, "currentConditions": dict(
                                  execution_conditions or {}, executionPath=cell["executionPath"])}})
    return sorted(batches) if reason is None else [], estimates, reason


def coverage_active_protocol(active, policy):
    seconds = policy["windowSeconds"]
    padding = max(0, active["metricObservation"]["durationSeconds"] - active["durationSeconds"])
    active.update(durationSeconds=seconds, warmupIterations=policy["warmupIterations"])
    active["metricObservation"]["durationSeconds"] = seconds + padding


def generate_coverage(output, policy_path, preflight=False, pilot=False):
    output = output.resolve()
    policy = load(policy_path)
    if policy["schemaVersion"] != "coverage-plan.v2":
        raise ValueError("coverage-plan.v2 required; migrate the policy in one cutover")
    for rule in policy["memoryMaxBatch"]:
        if (type(rule["maxBatch"]) is not int or rule["maxBatch"] < 0 or
                set(rule["conditions"]) != {"runtimeImage", "executionPath", "memoryLimitBytes"} or
                rule["basis"] not in ("observed-failure", "conservative-exclusion") or not rule["observations"]):
            raise ValueError("invalid memory exclusion rule")
        conditions = rule["conditions"]
        if any(conditions[key] is not None and (not isinstance(conditions[key], str) or not conditions[key])
               for key in ("runtimeImage", "executionPath")):
            raise ValueError("memory exclusion image/path must be known or null")
        limit = conditions["memoryLimitBytes"]
        if limit is not None and (type(limit) is not int or limit < 1):
            raise ValueError("memory exclusion limit must be positive bytes or null")
    if policy["sessions"] != 3 or policy["warmupIterations"] != 1:
        raise ValueError("coverage requires three independent sessions and one warmup iteration")
    for key in ("windowSeconds", "idleSeconds"):
        if type(policy[key]) is not int or policy[key] < 1:
            raise ValueError(key + " must be a positive integer")
    if policy["windowSeconds"] < 30:
        raise ValueError("active coverage requires at least thirty seconds")
    namespace = "/api/v1/namespaces/" + NS
    inputs = {}
    for name, path in (
            ("coverage", namespace + "/measurement-coverage"),
            ("plans", namespace + "/batch-energy-optimizations"),
            ("profiles", "/api/v1/scheduler/profiles"), ("inventory", "/api/v1/inventory"),
            ("models", "/api/v1/models"), ("preprocessors", "/api/v1/preprocessors"),
            ("datasets", "/api/v1/datasets"), ("workloads", "/api/v1/workloads")):
        inputs[name] = coverage_api(path)
        write_json(output / "inputs" / (name + ".json"), inputs[name])
    write_json(output / "coverage-plan.json", policy)
    runtime_values = (HERE.parents[1] / policy["runtimeValues"]).resolve()
    current_images = coverage_runtime_images(runtime_values)
    runtime_doc = yaml.safe_load(runtime_values.read_text())
    declarations = runtime_doc["profiling"]["executionDeclarations"]
    write_json(output / "inputs" / "current-runtime-images.json", {
        "source": identity(runtime_values), "imagesByDeviceClass": current_images,
        "executionDeclarations": declarations})
    results = []
    completed_conditions, profile_conditions = {}, {}
    for profile in inputs["profiles"]["profiles"]:
        name = profile["id"].split("/")[1]
        result = coverage_api(namespace + "/batch-energy-optimizations/" + name + "/result")
        results.append(result)
        campaign = next((p for p in inputs["plans"]["items"]
                         if p["metadata"].get("uid") == result["campaign"]["uid"]), {})
        if coverage_shared_result(result) and campaign.get("status", {}).get("phase") == "Complete":
            observed = {"runtimeImage": profile["condition"]["runtimeImage"],
                        "curveSchema": SHARED_CURVE_SCHEMA, "evidenceSchema": SHARED_EVIDENCE_SCHEMA}
            completed_conditions[result["campaign"]["uid"]] = observed
            profile_conditions[profile["id"]] = observed
        write_json(output / "inputs" / "results" / (name + ".json"), result)
    timings = coverage_timings(results)
    write_json(output / "timings.json", timings)
    classes = {c["name"]: c for c in inputs["inventory"]["classes"]}
    models = {m["metadata"]["name"]: m for m in inputs["models"]["items"]}
    lanes, rows, no_node, no_policy = defaultdict(list), [], [], []
    # The Profiler refuses a RunConfig without a (deviceClass, executionPath) policy; the values comments name the reason per omission.
    facts = json.loads(subprocess.check_output([ENV["kubectl"], "--kubeconfig", ENV["kubeconfig"], "--context", ENV["kubeContext"], "-n", NS,
                                                "get", "configmap", "chill-profiler-facts", "-o", "json"]))
    policies = yaml.safe_load(facts["data"]["execution-policies.yaml"])
    policies = policies if isinstance(policies, list) else policies["policies"]
    write_json(output / "inputs" / "execution-policies.json", facts)
    memory_inputs = {}
    kubectl = [ENV["kubectl"], "--kubeconfig", ENV["kubeconfig"], "--context", ENV["kubeContext"], "-n", NS]
    for key, args in (
            ("nodes", ["get", "nodes", "-o", "json"]),
            ("profilerDeployments", ["get", "deployments", "-l", "app.kubernetes.io/component=profiler", "-o", "json"])):
        try:
            memory_inputs[key] = json.loads(subprocess.check_output(kubectl + args))
        except subprocess.CalledProcessError as error:
            memory_inputs[key] = {"readError": str(error)}
    write_json(output / "inputs" / "memory-limit-inputs.json", memory_inputs)
    nodes = {n["metadata"]["name"]: n for n in memory_inputs["nodes"].get("items", [])}
    fractions = [
        arg.split("=", 1)[1]
        for deployment in memory_inputs["profilerDeployments"].get("items", [])
        for container in deployment["spec"]["template"]["spec"]["containers"]
        if "--fact-source-config-map=chill-profiler-facts" in container.get("args", [])
        for arg in container["args"] if arg.startswith("--measurement-memory-fraction=")]
    try:
        memory_fraction = float(fractions[0]) if len(fractions) == 1 else None
    except ValueError:
        memory_fraction = None
    has_policy = {(p["deviceClass"], p["executionPath"]) for p in policies}
    eligible_by_class = {
        device: sorted(n["name"] for n in c["nodes"] if n["name"] not in policy["excludedNodes"] and
                       n["ready"] == "True" and not n["schedulingDisabled"])
        for device, c in classes.items()}
    node_declared = {
        cell["id"]: eligible_by_class[cell["deviceClass"]["name"]][0]
        for cell in inputs["coverage"]["cells"]
        if cell["support"]["verdict"] == policy["unknownVerdict"] and
        eligible_by_class[cell["deviceClass"]["name"]] and coverage_declared_on_node(
            cell, eligible_by_class[cell["deviceClass"]["name"]][0], classes[cell["deviceClass"]["name"]],
            models[cell["model"]["name"]], declarations, has_policy)}
    required_conditions = {
        cell["id"]: coverage_required_condition(cell, eligible_by_class[cell["deviceClass"]["name"]][0],
            models[cell["model"]["name"]], declarations)
        for cell in inputs["coverage"]["cells"] if eligible_by_class[cell["deviceClass"]["name"]]}
    write_json(output / "inputs" / "required-execution-conditions.json", required_conditions)
    targets, unknown, stale, measured_current = coverage_targets(
        inputs["coverage"], policy, required_conditions, node_declared, profile_conditions)
    stale_ids = {cell["id"] for cell in stale}
    write_json(output / "node-declared-cells.json", node_declared)
    existing, excluded_batches, excluded_cells = [], [], []
    plan_hash = hashlib.sha256(json.dumps(
        [policy, inputs["coverage"]["snapshotId"], required_conditions], sort_keys=True).encode()).hexdigest()[:12]
    for cell in targets:
        model, device = cell["model"]["name"], cell["deviceClass"]["name"]
        eligible = eligible_by_class[device]
        if (device, cell["executionPath"]) not in has_policy and ("*", cell["executionPath"]) not in has_policy:
            no_policy.append(cell["id"])
            continue
        if not eligible:
            no_node.append({"cellId": cell["id"], "reason": "노드 없음",
                            "nodes": classes[device]["nodes"]})
            continue
        node = eligible[0]
        required = required_conditions.get(cell["id"])
        if required is None:
            no_policy.append(cell["id"])
            continue
        previous = coverage_existing(cell, inputs["plans"]["items"], policy, required, completed_conditions)
        if previous:
            existing.append({"cellId": cell["id"], "name": previous["metadata"]["name"],
                             "status": previous.get("status", {}).get("phase")})
            continue
        execution = next(p for p in models[model]["spec"]["executionPaths"] if p["name"] == cell["executionPath"])
        artifact = execution["artifact"]
        selected_artifact = next(a for a in models[model]["spec"]["artifacts"] if a["name"] == artifact)
        declared_batches = selected_artifact.get("derivation", {}).get("declaredBatchSizes", [])
        node_memory = nodes.get(node, {})
        inventory_node = next(n for n in classes[device]["nodes"] if n["name"] == node)
        if not inventory_node.get("uid") or node_memory.get("metadata", {}).get("uid") != inventory_node["uid"]:
            node_memory = {}
        memory_limit = coverage_memory_limit(cell, policies, node_memory, memory_fraction)
        execution_conditions = dict(required, memoryLimitBytes=memory_limit,
            memoryLimitStatus="confirmed" if memory_limit is not None else "unconfirmed")
        _, gate_estimates, reason = coverage_batch_plan(
            cell, declared_batches or [1], timings, policy, declared_batches, execution_conditions)
        if reason:
            excluded_batches.extend(dict(r, cellId=cell["id"], model=model, node=node) for r in gate_estimates)
            excluded_cells.append({"cellId": cell["id"], "model": model, "node": node,
                                   "reason": reason, "batchEstimates": gate_estimates})
            continue
        spec, inherited = coverage_defaults(cell, node, inputs["plans"]["items"], inputs["profiles"]["profiles"])
        spec["state"]["targetNode"] = node
        spec["state"]["candidate"].update(modelSpec=model, deviceClass=device,
                                         executionPath=cell["executionPath"], powerMode=cell["powerMode"])
        coverage_inputs(spec, model, artifact, inputs["preprocessors"]["items"], inputs["datasets"]["items"])
        minimum_samples = coverage_workload(spec, inputs["workloads"])
        seconds = policy["windowSeconds"]
        coverage_active_protocol(spec["activeProtocol"], policy)
        idle = spec["idleProtocol"]
        idle_padding = max(0, idle["metricObservation"]["durationSeconds"] - idle.get("durationSeconds", policy["idleSeconds"]))
        idle["durationSeconds"] = idle.get("durationSeconds") or policy["idleSeconds"]
        idle["metricObservation"]["durationSeconds"] = idle["durationSeconds"] + idle_padding
        spec["trialsPerBatch"] = policy["sessions"]
        name = policy["requestNamePrefix"] + plan_hash + "-" + hashlib.sha256(cell["id"].encode()).hexdigest()[:12]
        doc = {"apiVersion": "edge.dacs.io/v1alpha1", "kind": "BatchEnergyOptimization",
               "metadata": {"name": name, "namespace": NS, "annotations": {
                   policy["cellAnnotation"]: cell["id"],
                   EXECUTION_CONDITIONS_ANNOTATION: json.dumps(required, separators=(",", ":"))}}, "spec": spec}
        answer = coverage_api(namespace + "/batch-energy-optimizations/preflight", doc)
        if not answer["resolves"]:
            raise ValueError("automatic batch discovery plan unresolved for " + cell["id"] + ": " + answer.get("reason", ""))
        candidate_batches = answer["batchPlan"]["plannedBatches"]
        batches, estimates, _ = coverage_batch_plan(
            cell, candidate_batches, timings, policy, declared_batches, execution_conditions)
        priority = policy["models"][model]["priority"]
        row = {"cellId": cell["id"], "name": name, "model": model, "node": node, "priority": priority,
               "deviceClass": device,
               "powerMode": cell["powerMode"], "inheritedProtocol": inherited, "batches": batches,
               "activeSecondsPerSession": seconds, "candidateBatches": candidate_batches, "batchEstimates": estimates,
               "minimumServiceSamplesPerTrial": minimum_samples,
               "activeSeconds": policy["sessions"] * sum(max(seconds, minimum_samples * r["secondsPerCall"])
                   if r["secondsPerCall"] is not None else seconds for r in estimates if r["included"]),
               "idleSeconds": policy["sessions"] * (idle["durationSeconds"] + idle.get("settlingDurationSeconds", 0)),
               "runCount": (len(batches) + 1) * policy["sessions"]}
        row["runtimeMeasurementMissing"] = cell["id"] in stale_ids
        row["estimatedSeconds"] = row["activeSeconds"] + row["idleSeconds"] + row["runCount"] * policy["runOverheadSeconds"]
        row["remeasures"] = [
            {"name": p["metadata"]["name"], "phase": p.get("status", {}).get("phase")}
            for p in inputs["plans"]["items"]
            if p["metadata"]["name"].startswith(policy["requestNamePrefix"]) and
            p["metadata"].get("annotations", {}).get(policy["cellAnnotation"]) == cell["id"] and
            p.get("status", {}).get("phase") in ("Complete", "Measuring") and
            not coverage_condition_matches(completed_conditions.get(p["metadata"].get("uid")), required)]
        rows.append(row)
        lanes[node].append((row, doc))
    coverage_manifest(output, lanes, policy)
    summary = {"targetCellCount": len(targets), "manifestCellCount": len(rows),
               "cellsByModel": dict(Counter(r["model"] for r in rows)), "noNodeCellCount": len(no_node), "noPolicyCellCount": len(no_policy),
               "unknownSupportCount": len(unknown), "existingCellCount": len(existing),
               "nodeDeclaredCellCount": len(node_declared),
               "remeasurementCellCount": sum(bool(r["remeasures"]) for r in rows),
               "remeasurementsByDeviceClass": dict(Counter(r["deviceClass"] for r in rows if r["remeasures"])),
               "precisionMeaning": policy["precisionMeaning"],
               "staleRuntimeCellCount": len(stale), "currentRuntimeMeasuredCellCount": len(measured_current),
               "excludedBatchCount": len(excluded_batches), "excludedCellCount": len(excluded_cells),
               "memoryPolicyReviewCellCount": sum(c["reason"] == policy["memoryReviewReason"] for c in excluded_cells),
               "excludedBatchesByModel": dict(Counter(r["model"] for r in excluded_batches)),
               "excludedCellsByModel": dict(Counter(r["model"] for r in excluded_cells)),
               "unknownTimingBatchCount": sum(r["secondsPerCall"] is None for cell in rows for r in cell["batchEstimates"]),
               "estimateMeaning": f"full candidate-domain work estimate + shared idle + {policy['runOverheadSeconds']}s/run; automatic discovery may stop at an execution boundary; not a promised completed count or duration; excludes variable load/warmup and feeder polling",
               "lanes": {node: {"cellCount": len(lane),
                                  "activeSeconds": sum(r["activeSeconds"] for r, _ in lane),
                                  "idleSeconds": sum(r["idleSeconds"] for r, _ in lane),
                                  "runCount": sum(r["runCount"] for r, _ in lane),
                                  "estimatedSeconds": sum(r["estimatedSeconds"] for r, _ in lane),
                                  "models": {model: {
                                      "cellCount": sum(r["model"] == model for r, _ in lane),
                                      "estimatedSeconds": sum(r["estimatedSeconds"] for r, _ in lane
                                                              if r["model"] == model)}
                                      for model in policy["models"]}}
                         for node, lane in sorted(lanes.items())}}
    summary["totalNodeHours"] = sum(r["estimatedSeconds"] for r in rows) / 3600
    summary["longestLaneSeconds"] = max((lane["estimatedSeconds"] for lane in summary["lanes"].values()), default=0)
    write_json(output / "unknown-support.json", unknown)
    write_json(output / "no-node.json", no_node)
    write_json(output / "no-policy.json", no_policy)
    write_json(output / "cells.json", rows)
    write_json(output / "remeasurement-cells.json", [r for r in rows if r["remeasures"]])
    write_json(output / "existing-cells.json", existing)
    write_json(output / "excluded-batches.json", excluded_batches)
    write_json(output / "excluded-cells.json", excluded_cells)
    write_json(output / "stale-runtime-cells.json", [
        {"cellId": cell["id"], "previousStatus": cell["assessment"]["displayStatus"],
         "runtimeImages": sorted({v["condition"].get("runtimeImage", "") for v in cell["variants"]}),
         "reason": "현재 runtime 측정 없음"} for cell in stale])
    write_json(output / "current-runtime-measured-cells.json", [cell["id"] for cell in measured_current])
    if pilot:
        pilot_lanes = coverage_pilot(lanes, policy, classes)
        coverage_manifest(output / "pilot", pilot_lanes, policy)
        write_json(output / "pilot" / "coverage-plan.json", policy)
        pilot_rows = [r for lane in pilot_lanes.values() for r, _ in lane]
        summary["pilot"] = {"cells": pilot_rows, "estimatedSeconds": sum(r["estimatedSeconds"] for r in pilot_rows)}
        write_json(output / "pilot" / "summary.json", summary["pilot"])
    if preflight:
        # Audited read-only: readapi/handler.go -> kube preflight query (client.Reader)
        # -> cmd/chill-query/run.go projector.Project; projected transitions are discarded.
        # Query API assembly only lists requests and reads typed catalog/evidence.
        checks = []
        for model in sorted(summary["cellsByModel"]):
            doc = next(d for lane in lanes.values() for r, d in lane if r["model"] == model)
            try:
                answer = coverage_api(namespace + "/batch-energy-optimizations/preflight", doc)
                checks.append({"name": doc["metadata"]["name"], "model": model, "response": answer})
            except urllib.error.HTTPError as error:
                checks.append({"name": doc["metadata"]["name"], "model": model,
                               "httpStatus": error.code, "error": error.read().decode()})
        write_json(output / "preflight.json", checks)
        summary["preflight"] = [{"model": r["model"], "resolves": r.get("response", {}).get("resolves"),
                                 "reason": r.get("response", {}).get("reason") or r.get("error")}
                                for r in checks]
    write_json(output / "summary.json", summary)
    print(json.dumps(summary, indent=2, ensure_ascii=False))
    for cell in unknown:
        print("unknown-support: " + cell["id"])
    for cell in no_node:
        print("노드 없음: " + cell["cellId"])


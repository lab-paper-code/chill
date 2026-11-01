#!/usr/bin/env python3
"""Stage approved conditions or a live coverage-driven BEO plan; never apply CRs.

Coverage: CHILL_EXPERIMENT_CONFIG=/absolute/environment.json python3
hack/measure/gen.py --coverage --output-dir /tmp/coverage-plan-out --preflight
The frozen coverage-plan.json accompanies all outputs. --coverage --self-check
is offline. Feeder input is resume-plan.json (--stages 1,2); generation and the
optional read-only preflight do not submit measurements. --pilot covers each
planned runtime family plus the highest-priority (heaviest) model on its
smallest-memory planned node; repeated nodes remain ordered lanes.
Legacy approved derivations still require the already-installed scipy.
"""
import argparse
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
from decimal import Decimal
from pathlib import Path

import yaml
from scipy.stats import t

HERE = Path(__file__).resolve().parent
CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
DATA = Path(CONFIG["paths"]["campaign"])
PHASE = DATA / "phase-a"
EVIDENCE = PHASE / "measurement-generalization-20261023"
CURRENT = PHASE / "measurement-generalization-20261024"
DEFAULT_OUTPUT = CURRENT / "approved"
CAPTURED_VALUES = Path(ENV["capturedValues"])
NS = ENV["namespace"]
COEFFICIENTS = {
    "relativeXPlanningPrecision": 0.01, "planningConfidence": 0.95,
    "sdMargin": 1.25, "timeMargin": 1.25, "lifetimeMargin": 1.25,
    "pipelineMargin": 1.25, "tailSigma": 3, "maximumIdleBaseFraction": 0.05,
    "idleDErrorSecondsPerRequest": 0.001, "windowFloorSeconds": 360,
    "loadPreparationFloorSeconds": 60,
    "plannedElapsedCapSeconds": 3600, "idleReportRatio": 0.1,
    "loadedIdleFloorSeconds": 120, "unloadedIdleFloorSeconds": 300,
}
# Captured infrastructure envelopes, not inferred model execution times.
POLICY = {"observerReadyTimeoutSeconds": 1160,
          "observerStartOverheadSeconds": 1160,
          "attemptTimeoutOverheadSeconds": 1235,
          "activeDeadlineOverheadSeconds": 1265,
          "metricResultOverheadSeconds": 30,
          "workloadEndFenceTimeoutSeconds": 15,
          "workloadResultTimeoutSeconds": 30}
REPLACEMENTS = {
    "jetsono-012-smolvlm-fp32-q4-maxn": "jetsono-950-smolvlm-fp32-q4-maxn-replacement",
    "orangepi-017-plc-cpu-q4": "orangepi-950-plc-cpu-q4-replacement",
}
MISSING_PROFILES = ["rasp5-018-smolvlm-int8-q3", "orangepi-018-smolvlm-int8-q2",
                    "orangepi-019-smolvlm-int8-q6", "orangepi-020-mnv2-cpu-q2",
                    "orangepi-021-mnv2-cpu-q4"]
AUTHORITY = ENV["authority"]


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


def read_docs(path):
    return [json.loads(d) for d in path.read_text().split("---") if d.strip()]


def required_batches(mean, sd):
    """First integer M>=2 satisfying the approved two-sided t planning rule."""
    c = COEFFICIENTS
    def enough(m):
        return m >= (t.ppf((1 + c["planningConfidence"]) / 2, m - 1)
                     * c["sdMargin"] * sd / (c["relativeXPlanningPrecision"] * mean)) ** 2
    lo, hi = 2, 2
    while not enough(hi):
        hi *= 2
    while lo < hi:
        mid = (lo + hi) // 2
        if enough(mid):
            hi = mid
        else:
            lo = mid + 1
    return lo + 1




def derive(row, framed_bytes, pipeline):
    c, b = COEFFICIENTS, row["batch"]
    mu, sd = row["meanSeconds"], row["sdSeconds"]
    observed_max = row.get("maxObservedSeconds")
    if not isinstance(observed_max, (int, float)) or not math.isfinite(observed_max) or observed_max <= 0:
        raise ValueError(f"{row['caseID']}: positive finite profile maxObservedSeconds is required")
    fast = row["minObservedSeconds"] / c["timeMargin"]
    tail = c["timeMargin"] * max(row["maxObservedSeconds"], mu + c["tailSigma"] * sd)
    refill = c["pipelineMargin"] * pipeline["observedGlobalFinishToNextActorStartMaximumSeconds"]
    prefix = (c["pipelineMargin"] * pipeline["observedColdGatewayReceiptToActorStartMaximumSeconds"]
              + pipeline["completionMarginSeconds"])
    delivery = pipeline["marginInflatedDeliveryBudgetSeconds"]
    delivery_millis = math.ceil(delivery * 1000)
    delivery = delivery_millis / 1000
    q = max(1, math.ceil(refill / fast))
    slo = row["workload"]["slo"]["targetLatencyNanos"] / 1e9
    lifetime = math.ceil(c["lifetimeMargin"] * max(slo, (q + 2) * tail + prefix))
    bounds = {"queue-cap": 64, "bytes-cap": 67108864 // framed_bytes,
              "lifetime": math.floor((lifetime - delivery) / (b * observed_max))}
    limiting_bound = min(bounds, key=bounds.get)
    workers = bounds[limiting_bound]
    q = math.ceil(workers / b) - 1
    waiting_sum = workers * b * observed_max
    q_upper = min(row["queueRequestCap"] // b, (64 * 1024 * 1024) // (b * framed_bytes))
    capacity_feasible = workers >= b and waiting_sum + delivery <= lifetime
    n = required_batches(mu, sd)
    warmup = math.ceil(max(c["loadPreparationFloorSeconds"], (q + 1) * tail + refill))
    duration = math.ceil(max(c["windowFloorSeconds"], c["timeMargin"] * n * mu))
    drain = math.ceil(lifetime + tail + delivery)
    preparation = math.ceil(row["preparePlanningKnownSeconds"])
    supply_extension = lifetime + (delivery_millis + 999) // 1000
    known = preparation + warmup + duration + supply_extension + drain
    residual = c["plannedElapsedCapSeconds"] - known
    if residual < 2:
        raise ValueError(f"{row['caseID']}: approved elapsed cap cannot hold known components")
    # Explicit conservative reservation, not a fabricated setup/collection observation.
    setup, collection = residual // 2, residual - residual // 2
    idle = c["maximumIdleBaseFraction"]
    if not math.isfinite(idle) or not 0 <= idle <= 1:
        raise ValueError("maximumIdleBaseFraction must be finite and in [0,1]")
    return {"N": n, "waitingBatches": q, "workers": workers,
            "requestLifetimeSeconds": lifetime, "socketTimeoutSeconds": lifetime + (delivery_millis + 1) / 1000,
            "deliveryMarginMillis": delivery_millis, "supplyExtensionSeconds": supply_extension, "drainSeconds": drain,
            "preparationSeconds": preparation, "warmupSeconds": warmup, "durationSeconds": duration,
            "maximumIdleFraction": idle, "jobSetupSeconds": setup, "collectionSeconds": collection,
            "plannedElapsedSeconds": c["plannedElapsedCapSeconds"], "knownElapsedSeconds": known,
            "profileKnownPreparationSeconds": row["preparePlanningKnownSeconds"],
            "unknownBudgetMeaning": "remaining cap after supply extension split between Job setup and final collection; conservative allocation, not measured",
            "tailBatchSeconds": tail, "fastBatchSeconds": fast, "refillSeconds": refill,
            "prefixSeconds": prefix, "qUpper": q_upper, "framedDecodedRequestUpperBytes": framed_bytes,
            "supplySizing": {"batch": b, "profileMaxBatchSeconds": observed_max,
                "serviceTimeSource": "profile.maxObservedSeconds",
                "requestLifetimeSeconds": lifetime, "marginSeconds": delivery, "workerCap": 64,
                "queueBytesCap": 67108864, "framedDecodedRequestUpperBytes": framed_bytes,
                "limitingBound": limiting_bound},
            "waitingSeconds": waiting_sum,
            "capacityFeasible": capacity_feasible,
            "queueRequestCap": row["queueRequestCap"], "queueBytesCap": 67108864}


def frozen_inputs(row):
    # Raw arrays remain in the immutable source; keep their identities, not 500kB per CR.
    keys = ("caseID", "node", "model", "executionPath", "cpuRequest", "affinity", "powerMode", "batch",
            "BEO", "workload", "profileID", "runtimeImage", "sampleCount", "trialCount", "meanSeconds",
            "sdSeconds", "minObservedSeconds", "maxObservedSeconds", "inputIdentity", "trialSampleCounts",
            "trialMeansSeconds", "initializedBatches", "actualWarmupIterations", "preparePlanningKnownSeconds",
            "preparePlanningBoundary", "preparePlanningMissing", "prepareLifecycleEvidence", "sourceProfileResult",
            "profileContentInputSHA256", "iidPrecisionAssumption", "refillInputLimitation")
    result = {k: row[k] for k in keys if k in row}
    result["trials"] = [{k: tr[k] for k in ("run", "rawKey", "rawSHA256", "executionKey", "executionSHA256",
                                             "podUID", "sampleCount", "sampleMeanNanos") if k in tr}
                        for tr in row["trials"]]
    return result


def occupancy(row, calculated, provenance):
    d = calculated
    if not d["capacityFeasible"]:
        raise ValueError(f"{row['caseID']}: capped K={d['workers']} cannot supply one batch within the frozen lifetime")
    model = "plc" if row["model"] == "plc-lstm-ae" else "smolvlm" if row["model"].startswith("smolvlm") else "mnv2"
    path = "cuda" if "cuda" in row["executionPath"] else "cpu"
    name = f"{row['node']}-970-{row['caseID'].lower()}-occ-{model}-{path}"
    spec = {"node": row["node"], "batchEnergyOptimization": row["BEO"],
            "workload": row["workload"]["name"], "batch": row["batch"], "supplyMode": "closed-loop",
            "minimumBatches": d["N"], "maximumIdleFraction": format(Decimal(str(d["maximumIdleFraction"])), "f")}
    for k in ("workers", "warmupSeconds", "durationSeconds", "requestLifetimeSeconds", "deliveryMarginMillis",
              "drainSeconds", "preparationSeconds", "jobSetupSeconds", "collectionSeconds", "plannedElapsedSeconds"):
        spec[k] = d[k]
    spec["conditions"] = {"profileID": row["profileID"], "waitingBatches": d["waitingBatches"],
                          "minimumCycles": d["N"] - 1, "maximumIdleNanosPerRequest": 1000000,
                          "inputsJSON": json.dumps({"authority": AUTHORITY, "sources": provenance,
                                                      "coefficients": COEFFICIENTS, "profile": frozen_inputs(row),
                                                      "pipelineTransferVerified": False, "computed": d}, separators=(",", ":"))}
    return {"apiVersion": "edge.dacs.io/v1alpha1", "kind": "DispatchOccupancyMeasurement",
            "metadata": {"name": name, "namespace": NS}, "spec": spec}


def timing(node, arithmetic, planning):
    basis = ENV["plusBasisNode"] if node == ENV["plusBasisNode"] else ENV["gen3BasisNode"]
    age = arithmetic["age"][basis]
    provenance = {"basisNode": basis, "targetNode": node, "provenance": age["provenance"],
                  "label": age["label"], "family": age["family"],
                  "unmeasuredGen3Transfer": node not in (ENV["gen3BasisNode"], ENV["plusBasisNode"]),
                  "phaseAllowanceSeconds": str(age["phaseAllowanceSeconds"]),
                  "planningConditions": planning}
    return {"changeIntervalP95Seconds": str(age["p95ChangeIntervalSeconds"]),
            "maximumRequestSeconds": str(age["maximumRequestSeconds"]),
            "maximumReceiptGapSeconds": str(age["maximumReceiptGapSeconds"]),
            "source": json.dumps(provenance, separators=(",", ":"))}


def replacement_beo(original, name, tail, evidence):
    doc = copy.deepcopy(original)
    doc["metadata"]["name"] = name
    spec = doc["spec"]
    for mode in ("activeProtocol", "idleProtocol"):
        protocol = spec[mode]
        # Finite watchdog is distinct from the nominal measurement duration.
        protocol["metricObservation"]["durationSeconds"] = math.ceil(protocol["durationSeconds"] + tail + 2)
    watchdog = spec["activeProtocol"]["metricObservation"]["durationSeconds"]
    events = watchdog * 1000 // spec["activeProtocol"]["metricObservation"]["intervalMilliseconds"] + 3
    if events > 50000:
        raise ValueError("replacement exceeds captured observer event ceiling")
    doc["metadata"]["annotations"] = {
        "chill.dacs.io/replaces-batch-energy-optimization": original["metadata"]["name"],
        "chill.dacs.io/measurement-conditions": json.dumps({"authority": AUTHORITY, "tailSeconds": tail,
            "tailEvidence": evidence, "policy": POLICY, "maximumMetricEvents": events,
            "metricResultByteCeiling": 8388608, "eventFootprintBudgetBytes": 8388608 // events,
            "runtimeDigestChanged": False}, separators=(",", ":"))}
    return doc


def generate(output):
    output = output.resolve()
    if CURRENT not in output.parents:
        raise ValueError("output must be a dedicated new staging directory below measurement-generalization-20261024")
    inputs_path = EVIDENCE / "d-method/d-inputs-and-calculations.json"
    floor_path = EVIDENCE / "d-method/legacy-floor-scenario.json"
    arithmetic_path = CURRENT / "cost-shift-approved/arithmetic.json"
    inputs, floor, arithmetic = load(inputs_path), load(floor_path), load(arithmetic_path)
    provenance = [identity(p) for p in (inputs_path, floor_path, CAPTURED_VALUES)]
    floors = {r["caseID"]: r for r in floor["rows"]}
    source = [d for node in sorted({r["node"] for r in inputs["rows"]}) for d in read_docs(PHASE / f"{node}.yaml")]
    by_name = {d["metadata"]["name"]: d for d in source}
    rows = [r for r in inputs["rows"] if r.get("profileID")]
    if len(rows) != 34:
        raise ValueError("approved reference population must contain exactly 34 profiles")
    conditions, staged = [], {"representative": [], "d-pre-d2": [], "d-after-d2": []}
    for row in rows:
        d = derive(row, floors[row["caseID"]]["framedDecodedRequestUpperBytes"], inputs["pipelineScenario"])
        conditions.append({"caseID": row["caseID"], "inputs": frozen_inputs(row), "computed": d})
        cr = occupancy(row, d, provenance)
        case = row["caseID"]
        if case in ("D01", "D20"):
            staged["representative"].append(cr)
        elif row["model"] == "plc-lstm-ae":
            staged["d-after-d2"].append(cr)
        elif row["model"].startswith("smolvlm") or case in ("D27", "D31", "D36"):
            staged["d-pre-d2"].append(cr)
    for group, docs in staged.items():
        for d in docs:
            write_docs(output / group / (d["metadata"]["name"] + ".yaml"), [d])
    # 012 B4 is unmeasured: the frozen readiness envelope is an explicit conservative
    # tail fallback, never a claim that B4 was observed or has this physical bound.
    handling = load(EVIDENCE / "cost-methods/012-handling.json")
    original012 = load(handling["B2Trials"][0]["localRaw"])["runConfig"]
    reference017 = next(r for r in rows if r["caseID"] == "D21")
    replacement_docs = []
    for old, new in REPLACEMENTS.items():
        is012 = old.startswith("jetsono")
        evidence = {
            "source": identity(EVIDENCE / ("cost-methods/012-handling.json" if is012 else "beo-policy/evidence-trace.json")),
            "originalUID": handling["BEOUID"] if is012 else "a4f082ce-45a7-4656-a601-5f6a6626eaae",
            "basis": "captured frozen-runtime readiness envelope as conservative unmeasured-batch tail fallback",
            "capturedPolicy": identity(CAPTURED_VALUES),
            "observedB2Seconds": handling["captureCause"]["ninthBatchSeconds"] if is012 else None,
            "frozenInputIdentity": original012 if is012 else reference017["inputIdentity"],
            "identityScope": "original012 trial" if is012 else "same-node/model/runtime q1 reference; q4 CPU preserved from original spec"}
        replacement_docs.append(replacement_beo(by_name[old], new, POLICY["observerReadyTimeoutSeconds"], evidence))
    for d in replacement_docs:
        write_docs(output / "replacements" / (d["metadata"]["name"] + ".yaml"), [d])
    missing_names = {}
    for i, name in enumerate(MISSING_PROFILES):
        node, tail = name.split("-", 1)[0], name.split("-", 2)[2]
        new = f"{node}-{951 + i}-{tail}-approved"
        missing_names[name] = new
        reference = next(r for r in rows if r["node"] == node and r["model"] == by_name[name]["spec"]["state"]["candidate"]["modelSpec"]
                         and r["executionPath"] == by_name[name]["spec"]["state"]["candidate"]["executionPath"])
        d = replacement_beo(by_name[name], new, POLICY["observerReadyTimeoutSeconds"],
                            {"referenceProfileID": reference["profileID"], "frozenInputIdentity": reference["inputIdentity"],
                             "basis": "conservative captured readiness envelope; different CPU quota tail unmeasured"})
        d["metadata"]["annotations"].pop("chill.dacs.io/replaces-batch-energy-optimization")
        d["metadata"]["annotations"]["chill.dacs.io/source-planned-spec"] = name
        write_docs(output / "prerequisites" / (new + ".yaml"), [d])
    name_map = dict(REPLACEMENTS, **missing_names)
    costs = [copy.deepcopy(d) for d in source if d["kind"] == "OperationCostMeasurement"]
    if len(costs) != 14:
        raise ValueError("only the approved (a)14 cost sequences may be staged")
    idle_reference = next(r for r in arithmetic["rows"] if r["node"] == "rasp5" and r["action"] == "idle")
    idle_nominal = idle_reference["curveNominalEndpointContinuationJoules"]
    idle_ratio = idle_reference["uncertaintyJoules"] / abs(idle_nominal) if idle_nominal else None
    idle_plan = {"formula": "max(existing floor, ceil(B_reference/(reportRatio*abs(curveMeanWatts)))) when reference exists; no unsupported trace extension",
                 "reference": idle_reference, "referenceRatioToCurve": idle_ratio,
                 "referencePlanningOnly": True, "newAcceptedEvidence": False,
                 "reportRatio": 0.1, "isAcceptanceGate": False,
                 "loadedSeconds": COEFFICIENTS["loadedIdleFloorSeconds"],
                 "unloadedSeconds": COEFFICIENTS["unloadedIdleFloorSeconds"],
                 "unmeasuredStates": "retain declared floor; measure actual S+G, no historical upper field or precision veto"}
    if idle_nominal:
        implied = math.ceil(idle_reference["uncertaintyJoules"] /
                            (COEFFICIENTS["idleReportRatio"] * abs(idle_nominal / idle_reference["durationSeconds"])))
        idle_plan["referenceImpliedSecondsDiagnostic"] = implied
    per_node, cost_index = {}, []
    for doc in costs:
        node, old_name = doc["spec"]["node"], doc["metadata"]["name"]
        idx = len(per_node.setdefault(node, []))
        name = f"{node}-{960 + idx}-cost-approved"
        doc["metadata"]["name"] = name
        doc["metadata"]["annotations"] = {"chill.dacs.io/source-planned-spec": old_name,
            "chill.dacs.io/measurement-conditions": json.dumps({"authority": AUTHORITY,
                "timingEvidence": identity(arithmetic_path), "coefficients": COEFFICIENTS,
                "costUncertainty": "B=S+G; exact all-phase knot/stationary extrema; report-only10%",
                "operationBudgetSeconds": math.ceil(1.25 * 1800),
                "operationBudgetBasis": "1.25*captured1800s runner envelope; conservative planning, not operation duration observation",
                "idlePlan": {k:v for k,v in idle_plan.items() if k != "reference"}}, separators=(",", ":"))}
        doc["spec"]["observation"] = {"intervalMilliseconds": 1000, "requestTimeoutMilliseconds": 800,
            "maximumOperationSeconds": math.ceil(1.25 * 1800), "maximumGapMilliseconds": 3000,
            "timing": timing(node, arithmetic, json.loads(doc["metadata"]["annotations"]["chill.dacs.io/measurement-conditions"]))}
        for step in doc["spec"]["steps"]:
            if step.get("batchEnergyOptimization") in name_map:
                step["batchEnergyOptimization"] = name_map[step["batchEnergyOptimization"]]
            if step["kind"] == "idle":
                floor_key = "unloadedIdleFloorSeconds" if step["name"] == "unloaded" else "loadedIdleFloorSeconds"
                step["idleDurationSeconds"] = COEFFICIENTS[floor_key]
        per_node[node].append(doc)
        cost_index.append({"node": node, "name": name, "source": old_name, "scope": "(a)",
                           "path": str(output / "cost-a14" / (name + ".yaml"))})
        write_docs(output / "cost-a14" / (name + ".yaml"), [doc])
    representative_cost = next(d for d in costs if d["metadata"]["name"] == "rasp5-960-cost-approved")
    write_docs(output / "representative/rasp5-960-cost-approved.yaml", [representative_cost])
    # Same immutable name in the representative view: resume must reuse its outcome,
    # not schedule a fifteenth cost or repeat the sequence under a second identity.
    replacement_sharing = []
    for old, new in REPLACEMENTS.items():
        shared = next(s for s in inputs["allManifestBEOSharing"] if s["BEO"] == old)
        measured = [r for r in rows if r["caseID"] in shared["sharedDCaseIDs"]]
        replacement_sharing.append({
            "original": old, "replacement": new, "newReplacementSpecificD": False,
            "meaning": "legacy d-sharing relationship only; not a measurement derived from the replacement profile",
            "actualMeasuredDInputs": [{"caseID": r["caseID"], "BEO": r["BEO"], "profileID": r["profileID"]} for r in measured]})
    write_json(output / "conditions.json", {"authority": AUTHORITY, "coefficients": COEFFICIENTS,
        "sources": provenance + [identity(arithmetic_path)], "all34": conditions,
        "legacyMobilenetCPU8Preserved": True, "diagnosticRasp944Adopted": False,
        "idlePlan": idle_plan, "profilingPolicy": POLICY, "costSequences": cost_index,
        "replacementDSharing": replacement_sharing,
        "costProfileResolution": "After same-Node stage4 completion, existing cost admission resolves the named new BEO to namespace/name/newUID and verified catalog preparation; no old replacement UID or invented new profile statistics are frozen by this generator."})
    resume = {"authority": AUTHORITY, "executionOwner": "CampaignOps",
        "state": "staged only; hold remains effective; no cluster changes performed",
        "rollouts": 1, "firstfloorRuntimeDigestsFrozen": True,
        "preconditions": ["Main coordinated gates and real local smoke accepted",
            "CampaignOps numbered UTC instruction; fresh queued/active measurement, load Job, action, handback, serving and affected BEO/Run/Job execution counts all zero",
            "one coordinated rollout with CRDs/control/profiler/loadgen/observer and chart config; retain original evidence and root hold"],
        "sequence": [
            {"stage": 1, "action": "D01 representative", "paths": [str(output / "representative" / (d["metadata"]["name"] + ".yaml")) for d in staged["representative"] if "d01" in d["metadata"]["name"]]},
            {"stage": 2, "action": "full six-step Smol+PLC cost representative, not old945 first three operations", "paths": [str(output / "representative/rasp5-960-cost-approved.yaml")]},
            {"stage": 3, "action": "D20 representative", "paths": [str(output / "representative" / (d["metadata"]["name"] + ".yaml")) for d in staged["representative"] if "d20" in d["metadata"]["name"]]},
            {"stage": 4, "action": "after representative acceptance, coherent replacements and five missing profile prerequisites; preserve original roots/status/raw; no trial deletion or root status patch", "paths": [str(output / "replacements" / (d["metadata"]["name"] + ".yaml")) for d in replacement_docs] + [str(output / "prerequisites" / (name + ".yaml")) for name in missing_names.values()]},
            {"stage": 5, "action": "after prerequisites verified, residual (a) cost only, per-node file order; reuse representative same-name outcome", "paths": [c["path"] for c in cost_index if c["name"] != "rasp5-960-cost-approved"]},
            {"stage": 6, "action": "remaining approved Smol and missing GPU d; no legacy CPU8 rerun", "paths": [str(output / "d-pre-d2" / (d["metadata"]["name"] + ".yaml")) for d in staged["d-pre-d2"]]},
            {"stage": 7, "action": "D2 coordinated gates/deployment, then PLC d only", "paths": [str(output / "d-after-d2" / (d["metadata"]["name"] + ".yaml")) for d in staged["d-after-d2"]]}],
        "costScope": "exactly14 (a); no (b)9 versus approved8 scope resolution by regeneration",
        "originalsPreserved": list(REPLACEMENTS), "noOldPendingOrDrop3Regeneration": True,
        "lowestModeHandbackUnchanged": True, "holdRelease": "CampaignOps only after Main instruction and representative smoke acceptance"}
    resume["replacementDSharing"] = replacement_sharing
    resume["replacementCostDependency"] = "Stage4 replacement completion and Node handback precede each same-Node stage5 cost. Existing measurementProfile/operationCostRefusal resolves current BEO UID+catalog profile at execution; cost PrepareSpec is taken from that actual profile. No extra replacement-specific d or refresh step is added; D23=010 CUDA, D24=011 int8, D21=012 q1 remain independent retained profile inputs."
    resume["retainedOrangepi017Cleanup"] = {
        "beforeStage": 3, "exactlyOnce": True, "executionOwner": "CampaignOps", "namespace": NS,
        "rootName": "orangepi-017-plc-cpu-q4", "rootUID": "a4f082ce-45a7-4656-a601-5f6a6626eaae",
        "rootSnapshot": identity(EVIDENCE / "ops-state/orangepi017/root-current.json"),
        "preserveRootAdmissionAnnotation": "chill.dacs.io/admission=Granted 2026-10-23T06:48:46.160313542Z",
        "node": "orangepi", "capturedNodeUID": "3cbe367f-2511-47fc-9b34-67c98fac69fd",
        "staleTaint": {"key": "chill.dacs.io/measurement", "effect": "NoSchedule",
                       "value": "a4f082ce-45a7-4656-a601-5f6a6626eaae"},
        "preconditions": ["fresh all execution0 including servingPods0 and powerPending0",
                          "fresh Node UID matches captured UID; capture current resourceVersion",
                          "exact stale taint key/effect/value present; no foreign holder"],
        "operation": "JSON Patch Node orangepi with tests for fresh metadata.uid, metadata.resourceVersion and exact indexed taint, then remove only that taint; derive index from fresh snapshot, never remove all taints",
        "after": "verify exact stale taint absent and execution still0, then stage3 D20 receives normal admission; stage4 replacement follows later without repeated orphan cleanup",
        "oldRootDoesNotReadmit": "ObserverProfile24 confirmed admission.go160-163 excludes already-Granted old root from waiting; continueHeldNodes277-285 leaves Measuring root unchanged. Preserve Granted annotation, original UID/raw/status.",
        "forbidden": ["delete/patch original root or child Run/Job/raw/status", "manual power actuation",
                      "clear foreign holder", "new lease/fence/recovery framework"]}
    for step in resume["sequence"]:
        step["creationCommands"] = [
            [ENV["kubectl"], "--kubeconfig", ENV["kubeconfig"], "--context", ENV["kubeContext"], "-n", NS, "create", "-f", path]
            for path in step["paths"]]
        step["creationDiscipline"] = "CampaignOps only; one per Node, await successful verified prior evidence and handback before next; stop on Failed/Insufficient/Refused; never replay an existing name"
    resume["remainingAPreD2FeederCommand"] = [
        "python3", str(HERE / "feeder.py"), "--manifest", str(output / "resume-plan.json"), "--stages", "4,5,6"]
    resume["postD2FeederCommand"] = [
        "python3", str(HERE / "feeder.py"), "--manifest", str(output / "resume-plan.json"), "--stages", "7"]
    resume["feederDiscipline"] = "CampaignOps starts only after preceding acceptance/cleanup and explicit Main instruction. Existing holds/drop3 stay effective; held approved items block their lane, successful terminal plus Node measurement-taint handback is required to advance; Failed/Insufficient/Refused always stop including restarts. Stage7 cannot be combined with pre-D2 stages."
    resume["holdOwnership"] = {
        "path": str(DATA / "feeder-hold.json"), "executionOwner": "CampaignOps",
        "precondition": "after coordinated rollout and numbered Main instruction; no competing feeder",
        "representatives": "CampaignOps directly executes the exact single-CR stage1/2/3 creationCommands after Main instruction; existing all-hold remains unchanged. Stage3 additionally requires exactly-once old017 cleanup first.",
        "remainingAPreD2": "only after representative acceptance, CampaignOps clears existing pause/category holds for the approved staged scope and starts remainingAPreD2FeederCommand; no competing feeder",
        "postD2": "only after D2 acceptance start the separate stage7 command",
        "preserve": "existing drop rule always remains unchanged; no new hold bypass fields",
        "originalHoldEditedByGenerator": False}
    write_json(output / "resume-plan.json", resume)
    return conditions


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


def coverage_cpu(policy, device, mode):
    """Schedulable whole cores, capped by the power mode's online CPUs (ORT threads need that many)."""
    online = policy.get("onlineCPUsByMode", {}).get(device + "/" + mode)
    size = int(policy["cpuByDeviceClass"][device])
    return str(min(size, online) if online else size)


def coverage_online_cpus(values):
    counts = {}
    for device in values["discovery"]["catalog"]["classes"]:
        for mode in device["powerModes"]:
            if mode.get("onlineCPUs"):
                counts[device["name"] + "/" + mode["name"]] = sum(
                    int(b) - int(a) + 1 for a, _, b in (part.partition("-") if "-" in part else (part, "", part)
                                                       for part in mode["onlineCPUs"].split(",")))
    return counts


def coverage_cpu_matches(cell, cpu, policy):
    expected = Decimal(coverage_cpu(policy, cell["deviceClass"]["name"], cell["powerMode"]))
    def quantity(value):
        return Decimal(value[:-1]) / 1000 if value.endswith("m") else Decimal(value)
    return all(cpu.get(key) not in (None, "") and quantity(str(cpu[key])) == expected
               for key in ("request", "limit"))


def coverage_targets(coverage, policy, current_images, node_declared=()):
    targets, unknown, stale, measured_current = [], [], [], []
    if coverage["completeness"] != "complete":
        raise ValueError("incomplete coverage cannot define a complete plan")
    for cell in coverage["cells"]:
        verdict, assessment = cell["support"]["verdict"], cell["assessment"]
        if verdict == policy["unknownVerdict"] and cell["id"] not in node_declared:
            unknown.append(cell)
        elif verdict == policy["supportedVerdict"] or cell["id"] in node_declared:
            measured = [v for v in cell.get("variants", []) if
                        v.get("profileIds") or v.get("assessment", {}).get("evidence", {}).get("accepted")]
            current = [v for v in measured if
                       v["condition"].get("runtimeImage") in current_images.get(cell["deviceClass"]["name"], [])]
            if any(coverage_cpu_matches(cell, v["condition"].get("cpu", {}), policy) for v in current):
                measured_current.append(cell)
                continue
            if measured and not current:
                stale.append(cell)
            if cell["id"] in node_declared or measured or assessment["displayStatus"] in (policy["unmeasuredStatus"], "running") or (
                    assessment["displayStatus"] == policy["insufficientStatus"] and
                    any(r["code"] == policy["missingBatchReason"] for r in assessment["reasons"])):
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
            spec.pop("cpuAffinity", None)
            spec.pop("cpuQuota", None)
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
        if source is None:
            raise ValueError("no inherited retryLimit for " + cell["id"])
        spec["retryLimit"] = source["spec"]["retryLimit"]
        return spec, "profile:" + profile["id"]
    if source is None:
        raise ValueError("no inherited protocol for " + cell["id"])
    spec = copy.deepcopy(source["spec"])
    for field in ("cpuQuota", "cpuAffinity", "batchSizes"):
        spec.pop(field, None)
    spec["state"].pop("cpuRequest", None)
    if source["spec"]["state"]["candidate"]["modelSpec"] != model:
        spec["state"]["candidate"].pop("preprocessor", None)
        spec["activeProtocol"].pop("dataset", None)
    return spec, "request:" + source["metadata"]["name"]


def coverage_batches(model, path, policy):
    execution = next(p for p in model["spec"]["executionPaths"] if p["name"] == path)
    artifact = next(a for a in model["spec"]["artifacts"] if a["name"] == execution["artifact"])
    declared = artifact.get("derivation", {}).get("declaredBatchSizes", [])
    maximum = execution["runtime"]["settings"]["firstfloor"]["maxBatchSize"]
    batches = sorted(declared) if declared else sorted(b for b in policy["undeclaredBatchSizes"] if b <= maximum)
    if not batches or any(type(b) is not int or b < 1 for b in batches) or len(set(batches)) != len(batches):
        raise ValueError("invalid artifact batch set")
    return batches, bool(declared), artifact["name"]


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


def coverage_timings(results):
    timings = []
    for result in results:
        facts = result.get("measurementFacts")
        if not facts:
            continue
        condition = facts["condition"]
        for batch in facts["batches"]:
            active = [q for q in batch["quality"] if q["workloadMode"] == "saturated-serial-fixed-batch"]
            calls = batch.get("completedCalls", 0)
            seconds = sum(q["windowSeconds"] for q in active)
            if calls > 0 and active and math.isfinite(seconds) and seconds > 0:
                timings.append({"model": condition["logicalModel"], "deviceClass": condition["deviceClass"],
                                "batch": batch["batch"], "secondsPerCall": seconds / calls,
                                "executionPath": condition["artifact"]["executionPath"],
                                "powerMode": condition["powerMode"], "sourceUID": facts["request"]["uid"],
                                "source": facts["request"]["name"]})
    return timings


def coverage_existing(cell, plans, policy):
    expected = (cell["model"]["name"], cell["deviceClass"]["name"], cell["executionPath"], cell["powerMode"])
    for plan in plans:
        spec = plan.get("spec", {})
        state = spec.get("state", {})
        if not coverage_cpu_matches(cell, {"request": state.get("cpuRequest", ""),
                                          "limit": spec.get("cpuQuota", "")}, policy):
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


def coverage_batch_plan(cell, batches, timings, policy, declared=False):
    model, device = cell["model"]["name"], cell["deviceClass"]["name"]
    memory_max = policy["memoryMaxBatch"].get(model, {}).get(device)
    estimates = []
    excluded_from = None
    for batch in sorted(batches):
        measured = [r for r in timings if r["model"] == model and
                    r["deviceClass"] == device and r["batch"] == batch]
        source = max(measured, key=lambda r: r["secondsPerCall"]) if measured else None
        calls = math.floor(policy["windowSeconds"] / source["secondsPerCall"]) if source else None
        if calls is not None and calls < policy["minCallsInWindow"] and excluded_from is None:
            excluded_from = batch
        over_memory = memory_max is not None and batch > memory_max
        included = excluded_from is None and not over_memory
        estimates.append({"batch": batch, "secondsPerCall": source["secondsPerCall"] if source else None,
                          "estimatedCallsInWindow": calls, "timingSource": source, "included": included,
                          "reason": None if included else (policy["memoryExcludedBatchReason"] if over_memory
                                                           else policy["excludedBatchReason"]),
                          "excludedFromBatch": excluded_from})
    included = [r["batch"] for r in estimates if r["included"]]
    reason = policy["excludedBatchReason"] if not included else None
    if declared and included and included != sorted(batches):
        included, reason = [], policy["declaredBatchConflictReason"]
    return included, estimates, reason


def coverage_active_protocol(active, estimates, policy):
    seconds = policy["windowSeconds"]
    padding = max(0, active["metricObservation"]["durationSeconds"] - active["durationSeconds"])
    tail = max((r["secondsPerCall"] * policy["callDurationEstimateMultiplier"]
                if r["secondsPerCall"] is not None else policy["unknownCallDurationSeconds"])
               for r in estimates if r["included"])
    active.update(durationSeconds=seconds, warmupIterations=policy["warmupIterations"])
    active["metricObservation"]["durationSeconds"] = seconds + math.ceil(tail) + padding


def generate_coverage(output, policy_path, preflight=False, pilot=False):
    output = output.resolve()
    policy = load(policy_path)
    if policy["sessions"] != 3 or policy["warmupIterations"] != 1:
        raise ValueError("coverage requires three independent sessions and one warmup iteration")
    for key in ("windowSeconds", "minCallsInWindow", "idleSeconds"):
        if type(policy[key]) is not int or policy[key] < 1:
            raise ValueError(key + " must be a positive integer")
    for key in ("callDurationEstimateMultiplier", "unknownCallDurationSeconds"):
        if type(policy[key]) not in (int, float) or not math.isfinite(policy[key]) or policy[key] < 1:
            raise ValueError(key + " must be finite and at least one")
    for device, cpu in policy["cpuByDeviceClass"].items():
        value = Decimal(cpu)
        if not value.is_finite() or value <= 0 or value != value.to_integral_value():
            raise ValueError(device + ": allocatable CPU must be a positive whole core count")
    namespace = "/api/v1/namespaces/" + NS
    inputs = {}
    for name, path in (
            ("coverage", namespace + "/measurement-coverage"),
            ("plans", namespace + "/batch-energy-optimizations"),
            ("profiles", "/api/v1/scheduler/profiles"), ("inventory", "/api/v1/inventory"),
            ("models", "/api/v1/models"), ("preprocessors", "/api/v1/preprocessors"),
            ("datasets", "/api/v1/datasets")):
        inputs[name] = coverage_api(path)
        write_json(output / "inputs" / (name + ".json"), inputs[name])
    write_json(output / "coverage-plan.json", policy)
    runtime_values = (HERE.parents[1] / policy["runtimeValues"]).resolve()
    current_images = coverage_runtime_images(runtime_values)
    runtime_doc = yaml.safe_load(runtime_values.read_text())
    declarations = runtime_doc["profiling"]["executionDeclarations"]
    policy["onlineCPUsByMode"] = coverage_online_cpus(runtime_doc)
    write_json(output / "inputs" / "current-runtime-images.json", {
        "source": identity(runtime_values), "imagesByDeviceClass": current_images,
        "executionDeclarations": declarations})
    results = []
    for profile in inputs["profiles"]["profiles"]:
        name = profile["id"].split("/")[1]
        result = coverage_api(namespace + "/batch-energy-optimizations/" + name + "/result")
        results.append(result)
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
    targets, unknown, stale, measured_current = coverage_targets(
        inputs["coverage"], policy, current_images, node_declared)
    stale_ids = {cell["id"] for cell in stale}
    write_json(output / "node-declared-cells.json", node_declared)
    existing, excluded_batches, excluded_cells = [], [], []
    plan_hash = hashlib.sha256(json.dumps(
        [policy, inputs["coverage"]["snapshotId"], current_images], sort_keys=True).encode()).hexdigest()[:12]
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
        previous = coverage_existing(cell, inputs["plans"]["items"], policy)
        if previous:
            existing.append({"cellId": cell["id"], "name": previous["metadata"]["name"],
                             "status": previous.get("status", {}).get("phase")})
            continue
        candidate_batches, declared, artifact = coverage_batches(models[model], cell["executionPath"], policy)
        batches, estimates, reason = coverage_batch_plan(cell, candidate_batches, timings, policy, declared)
        excluded_batches.extend(dict(r, cellId=cell["id"], model=model, node=node)
                                for r in estimates if not r["included"])
        if reason:
            excluded_cells.append({"cellId": cell["id"], "model": model, "node": node,
                                   "reason": reason, "batchEstimates": estimates})
            continue
        spec, inherited = coverage_defaults(cell, node, inputs["plans"]["items"], inputs["profiles"]["profiles"])
        spec["state"]["targetNode"] = node
        spec["state"]["cpuRequest"] = coverage_cpu(policy, device, cell["powerMode"])
        spec["cpuQuota"] = spec["state"]["cpuRequest"]
        spec["state"]["candidate"].update(modelSpec=model, deviceClass=device,
                                         executionPath=cell["executionPath"], powerMode=cell["powerMode"])
        spec.pop("batchSizes", None)
        if not declared:
            spec["batchSizes"] = batches
        coverage_inputs(spec, model, artifact, inputs["preprocessors"]["items"], inputs["datasets"]["items"])
        seconds = policy["windowSeconds"]
        coverage_active_protocol(spec["activeProtocol"], estimates, policy)
        idle = spec["idleProtocol"]
        idle_padding = max(0, idle["metricObservation"]["durationSeconds"] - idle.get("durationSeconds", policy["idleSeconds"]))
        idle["durationSeconds"] = idle.get("durationSeconds") or policy["idleSeconds"]
        idle["metricObservation"]["durationSeconds"] = idle["durationSeconds"] + idle_padding
        spec["trialsPerBatch"] = policy["sessions"]
        name = policy["requestNamePrefix"] + plan_hash + "-" + hashlib.sha256(cell["id"].encode()).hexdigest()[:12]
        doc = {"apiVersion": "edge.dacs.io/v1alpha1", "kind": "BatchEnergyOptimization",
               "metadata": {"name": name, "namespace": NS, "annotations": {
                   policy["cellAnnotation"]: cell["id"]}}, "spec": spec}
        priority = policy["models"][model]["priority"]
        row = {"cellId": cell["id"], "name": name, "model": model, "node": node, "priority": priority,
               "deviceClass": device, "cpuRequest": spec["state"]["cpuRequest"], "cpuQuota": spec["cpuQuota"],
               "powerMode": cell["powerMode"], "inheritedProtocol": inherited, "batches": batches,
               "activeSecondsPerSession": seconds, "candidateBatches": candidate_batches, "batchEstimates": estimates,
               "activeSeconds": seconds * len(batches) * policy["sessions"],
               "idleSeconds": policy["sessions"] * (idle["durationSeconds"] + idle.get("settlingDurationSeconds", 0)),
               "runCount": (len(batches) + 1) * policy["sessions"]}
        row["runtimeMeasurementMissing"] = cell["id"] in stale_ids
        row["estimatedSeconds"] = row["activeSeconds"] + row["idleSeconds"] + row["runCount"] * policy["runOverheadSeconds"]
        row["remeasures"] = [
            {"name": p["metadata"]["name"], "cpuQuota": p["spec"].get("cpuQuota"),
             "phase": p.get("status", {}).get("phase")}
            for p in inputs["plans"]["items"]
            if p["metadata"]["name"].startswith(policy["requestNamePrefix"]) and
            p["metadata"].get("annotations", {}).get(policy["cellAnnotation"]) == cell["id"] and
            p.get("status", {}).get("phase") in ("Complete", "Measuring") and
            not coverage_cpu_matches(cell, {"request": p["spec"]["state"]["cpuRequest"],
                                           "limit": p["spec"].get("cpuQuota", "")}, policy)]
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
               "excludedBatchesByModel": dict(Counter(r["model"] for r in excluded_batches)),
               "excludedCellsByModel": dict(Counter(r["model"] for r in excluded_cells)),
               "unknownTimingBatchCount": sum(r["secondsPerCall"] is None for cell in rows for r in cell["batchEstimates"]),
               "estimateMeaning": f"active + shared idle (including settling) + {policy['runOverheadSeconds']}s/run; excludes variable load/warmup and feeder polling",
               "lanes": {node: {"cellCount": len(lane),
                                  "cpuQuotaCounts": dict(Counter(r["cpuQuota"] for r, _ in lane)),
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
        # -> cmd/console/run.go projector.Project; projected transitions are discarded.
        # chill-server enrichment only lists requests and reads catalog/evidence.
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


def coverage_self_check():
    policy = load(HERE / "coverage-plan.json")
    policy["cpuByDeviceClass"] = dict(policy["cpuByDeviceClass"], device="4")
    model = "mobilenet-v2-050"
    cell = {"id": "cell", "model": {"name": model}, "deviceClass": {"name": "device"},
            "executionPath": "path", "powerMode": "mode", "support": {"verdict": "Compatible"},
            "assessment": {"displayStatus": "unmeasured", "reasons": []}}
    partial = copy.deepcopy(cell)
    partial["assessment"] = {"displayStatus": "insufficient", "reasons": [{"code": "TargetBatchMissing"}]}
    unknown = copy.deepcopy(cell)
    unknown["support"]["verdict"] = "Unknown"
    unsupported = copy.deepcopy(cell)
    unsupported["support"]["verdict"] = "Incompatible"
    other = copy.deepcopy(partial)
    other["assessment"]["reasons"] = [{"code": "Unrelated"}]
    running = dict(cell, assessment={"displayStatus": "running", "reasons": []})
    assert coverage_targets({"completeness": "complete", "cells": [cell, partial, running, unknown, unsupported, other]},
                            policy, {"device": ["current-image"]}) == ([cell, partial, running], [unknown], [], [])
    missing = copy.deepcopy(unknown)
    missing["support"]["reasons"] = [{"code": "RuntimeDeclarationMissing"}]
    missing["assessment"]["displayStatus"] = "unknown"
    device = {"name": "device", "architecture": "amd64"}
    native_model = {"spec": {
        "executionPaths": [{"name": "path", "artifact": "a",
                            "runtime": {"family": "firstfloor", "backend": "ort-cuda"}}],
        "artifacts": [{"name": "a", "source": {"architecture": "amd64"}}]}}
    declarations = [{"deviceClass": "device", "node": "actual-node", "runtime": {"payload": json.dumps({
        "architecture": "amd64", "runtime": {"family": "firstfloor", "backends": ["ort-cuda"]}})}}]
    assert coverage_declared_on_node(missing, "actual-node", device, native_model, declarations, {("device", "path")})
    assert not coverage_declared_on_node(missing, "other-node", device, native_model, declarations, {("device", "path")})
    assert not coverage_declared_on_node(missing, "actual-node", device, native_model, declarations, set())
    assert not coverage_declared_on_node(unknown, "actual-node", device, native_model, declarations, {("device", "path")})
    assert not coverage_declared_on_node(missing, "actual-node", dict(device, architecture="arm64"),
                                        native_model, declarations, {("device", "path")})
    assert coverage_targets({"completeness": "complete", "cells": [missing]}, policy,
                            {"device": []}, {missing["id"]: "actual-node"}) == ([missing], [], [], [])
    old = copy.deepcopy(cell)
    old["assessment"]["displayStatus"] = "usable"
    old["variants"] = [{"condition": {"runtimeImage": "old-image"}, "profileIds": ["old-profile"]}]
    fresh = copy.deepcopy(old)
    fresh["id"] = "fresh"
    fresh["variants"][0]["condition"]["runtimeImage"] = "current-image"
    fresh["variants"][0]["condition"]["cpu"] = {"request": "4", "limit": "4"}
    selected, _, stale, measured = coverage_targets(
        {"completeness": "complete", "cells": [old, fresh]}, policy, {"device": ["current-image"]})
    assert selected == stale == [old] and measured == [fresh]
    small = copy.deepcopy(fresh)
    small["variants"][0]["condition"]["cpu"] = {"request": "1", "limit": "1"}
    assert coverage_targets({"completeness": "complete", "cells": [small]}, policy,
                            {"device": ["current-image"]}) == ([small], [], [], [])
    assert coverage_runtime_images(HERE.parents[1] / policy["runtimeValues"])
    plan = {"metadata": {"name": "old", "creationTimestamp": "2026-01-01"},
            "status": {"phase": "Complete"},
            "spec": {"state": {"targetNode": "node", "cpuRequest": "4", "candidate": {
                "modelSpec": model, "deviceClass": "device", "executionPath": "path",
                "powerMode": "mode", "preprocessor": "pre"}},
                "activeProtocol": {"durationSeconds": 10, "dataset": "dataset"},
                "idleProtocol": {"durationSeconds": 10}, "cpuQuota": "4", "cpuAffinity": [3],
                "batchSizes": [1], "retryLimit": 1}}
    newer = copy.deepcopy(plan)
    newer["metadata"] = {"name": "new", "creationTimestamp": "2026-02-01"}
    spec, source = coverage_defaults(cell, "node", [plan, newer], [])
    assert source == "request:new" and "cpuQuota" not in spec and "cpuRequest" not in spec["state"]
    assert "cpuAffinity" not in spec
    spec, source = coverage_defaults(cell, "different-node", [plan, newer], [])
    assert source == "request:new" and "cpuQuota" not in spec and "cpuRequest" not in spec["state"]
    assert "batchSizes" not in spec and spec["activeProtocol"]["dataset"] == "dataset"
    assert "cpuAffinity" in plan["spec"]  # inheritance never mutates retained requests
    profile = {"id": "profile", "nodeName": "node", "measuredAt": "2026-03-01", "condition": {
        "logicalModel": model, "deviceClass": "device", "artifact": {"executionPath": "path"},
        "powerMode": "mode", "preprocessor": {"name": "pre"}, "cpu": {"request": "2", "limit": "2"}}}
    for field in ("activeProtocol", "idleProtocol"):
        profile["condition"][field] = {"durationSeconds": 10, "warmupIterations": 2,
                                      "settlingDurationSeconds": 2, "dataset": {"name": "dataset"},
                                      "metricIntervalMilliseconds": 1000, "metricTimeoutMilliseconds": 500,
                                      "metricDurationSeconds": 12}
    different = copy.deepcopy(plan)
    different["spec"]["state"]["targetNode"] = "other"
    spec, source = coverage_defaults(cell, "node", [different], [profile])
    assert source == "profile:profile" and spec["retryLimit"] == 1
    assert "cpuQuota" not in spec and "cpuRequest" not in spec["state"]
    assert "settlingDurationSeconds" not in spec["activeProtocol"]
    catalog = {"spec": {"executionPaths": [{"name": "path", "artifact": "artifact", "runtime": {
        "settings": {"firstfloor": {"maxBatchSize": 4}}}}],
        "artifacts": [{"name": "artifact", "derivation": {"declaredBatchSizes": [4, 1, 2]}}]}}
    assert coverage_batches(catalog, "path", policy) == ([1, 2, 4], True, "artifact")
    catalog["spec"]["artifacts"][0].pop("derivation")
    assert coverage_batches(catalog, "path", policy) == ([1, 2, 4], False, "artifact")
    catalog["spec"]["executionPaths"][0]["runtime"]["settings"]["firstfloor"]["maxBatchSize"] = 3
    assert coverage_batches(catalog, "path", policy) == ([1, 2], False, "artifact")
    timings = [{"model": model, "deviceClass": device, "batch": batch, "secondsPerCall": seconds,
                "source": device} for device, batch, seconds in [("device", 1, .1), ("device", 4, .4),
                                                                ("slow", 16, 10)]]
    included, estimates, reason = coverage_batch_plan(cell, [1, 3, 16], timings, policy)
    assert included == [1, 3, 16] and reason is None
    assert estimates[-1]["secondsPerCall"] is None  # another class is not grounds for exclusion
    assert estimates[1]["estimatedCallsInWindow"] is None
    slow = dict(timings[0], secondsPerCall=60)
    assert coverage_batch_plan(cell, [1], [slow], policy)[0] == [1]  # exactly one call
    slow["secondsPerCall"] = 60.001
    included, estimates, reason = coverage_batch_plan(cell, [1], [slow], policy)
    assert included == [] and reason == policy["excludedBatchReason"] and estimates[0]["estimatedCallsInWindow"] == 0
    included, estimates, reason = coverage_batch_plan(cell, [1, 2], [slow], policy)
    assert included == [] and estimates[1]["secondsPerCall"] is None and estimates[1]["excludedFromBatch"] == 1
    fast = timings[0]
    mixed = [fast, dict(fast, batch=2, secondsPerCall=61), dict(fast, batch=4, secondsPerCall=.1)]
    included, estimates, reason = coverage_batch_plan(cell, [4, 1, 2], mixed, policy)
    assert included == [1] and estimates[-1]["excludedFromBatch"] == 2 and estimates[-1]["estimatedCallsInWindow"] == 600
    assert coverage_batch_plan(cell, [1, 2, 4], mixed, policy, declared=True)[2] == policy["declaredBatchConflictReason"]
    two_calls = dict(policy, minCallsInWindow=2)
    assert coverage_batch_plan(cell, [1], [dict(fast, secondsPerCall=40)], two_calls)[0] == []
    active = {"durationSeconds": 60, "durationToleranceSeconds": 0,
              "metricObservation": {"durationSeconds": 62}}
    coverage_active_protocol(active, [{"included": True, "secondsPerCall": 37.294},
                                     {"included": False, "secondsPerCall": 900}], policy)
    assert active["durationSeconds"] == 60 and active["durationToleranceSeconds"] == 0
    assert active["metricObservation"]["durationSeconds"] == 137
    active["metricObservation"]["durationSeconds"] = 62
    coverage_active_protocol(active, [{"included": True, "secondsPerCall": None}], policy)
    assert active["metricObservation"]["durationSeconds"] == 662
    facts = {"condition": {"logicalModel": model, "deviceClass": "device", "artifact": {"executionPath": "path"},
                          "powerMode": "mode"},
             "request": {"name": "source", "uid": "source-uid"},
             "batches": [{"batch": 1, "completedCalls": 100, "quality": [
                 {"workloadMode": "saturated-serial-fixed-batch", "windowSeconds": 10},
                 {"workloadMode": "idle-model-loaded", "windowSeconds": 500}]}]}
    assert coverage_timings([{"measurementFacts": facts}])[0]["secondsPerCall"] == .1
    assert coverage_timings([{"measurementFacts": None}]) == []
    assert coverage_batch_plan(cell, [1], [], policy)[0] == [1]
    previous = {"metadata": {"name": "pilot", "annotations": {
        policy["cellAnnotation"]: cell["id"]}},
        "spec": {"state": {"cpuRequest": "4000m"}, "cpuQuota": "4"}}
    assert coverage_existing(cell, [previous], policy) == previous
    assert coverage_existing(dict(cell, id="different"), [previous], policy) is None
    named = {"metadata": {"name": policy["requestNamePrefix"] + "already-created"}, "spec": {
        "cpuQuota": "4", "state": {"cpuRequest": "4", "candidate": {
            "modelSpec": model, "deviceClass": "device", "executionPath": "path", "powerMode": "mode"}}}}
    assert coverage_existing(cell, [named], policy) == named
    wrong = copy.deepcopy(named)
    wrong["spec"]["cpuQuota"] = "1"
    assert coverage_existing(cell, [wrong], policy) is None
    wrong["spec"]["cpuQuota"] = "4"
    wrong["spec"]["state"]["cpuRequest"] = "1"
    assert coverage_existing(cell, [wrong], policy) is None
    for path in ("firstfloor-ort-cuda-arm64", "firstfloor-rknnlite"):
        accelerated = dict(cell, executionPath=path)
        annotated = copy.deepcopy(wrong)
        annotated["metadata"]["annotations"] = {policy["cellAnnotation"]: cell["id"]}
        assert coverage_existing(accelerated, [annotated], policy) is None
        annotated["spec"]["state"]["cpuRequest"] = "4"
        assert coverage_existing(accelerated, [annotated], policy) == annotated
    heavy_model = max(policy["models"], key=lambda name: policy["models"][name]["priority"])
    classes = {
        "arm": {"architecture": "arm64", "memory": "64Gi"},
        "amd": {"architecture": "amd64", "memory": "8Gi"},
        "gpu": {"architecture": "amd64", "memory": "64Gi"},
        "nano": {"architecture": "arm64", "memory": "4096Mi", "accelerator": "nvidia-jetson-nano"},
        "orin": {"architecture": "arm64", "memory": "8Gi", "accelerator": "nvidia-jetson-agx-orin"},
        "rk": {"architecture": "arm64", "memory": "16Gi", "accelerator": "rk3588-npu"}}
    pilot_lanes = defaultdict(list)
    cases = [("arm", "ort-cpu", "CPU arm64"), ("amd", "ort-cpu", "CPU amd64"),
             ("gpu", "ort-cuda", "CUDA amd64"), ("nano", "ort-cuda", "L4T r32"),
             ("orin", "ort-cuda", "L4T r35"), ("rk", "rknnlite", "RKNN"),
             ("nano", "ort-cpu", "CPU arm64"), ("orin", "ort-cpu", "CPU arm64")]
    for index, (node, path, family) in enumerate(cases):
        m = heavy_model if index >= 6 else model
        row = {"node": node, "model": m, "cellId": str(index),
               "priority": policy["models"][m]["priority"], "estimatedSeconds": 100 if index >= 6 else 1}
        candidate = {"deviceClass": node, "executionPath": path}
        assert coverage_runtime_family(candidate, classes[node]) == family
        pilot_lanes[node].append((row, {"spec": {"state": {"candidate": candidate}}}))
    selected = coverage_pilot(pilot_lanes, policy, classes)
    assert {r["cellId"] for lane in selected.values() for r, _ in lane} == set(map(str, range(7)))
    assert len(selected["nano"]) == 2  # CUDA family and heavy CPU model share an ordered lane
    assert selected["nano"][0][1] is pilot_lanes["nano"][0][1]
    assert coverage_pilot({"arm": pilot_lanes["arm"]}, policy, classes) == {"arm": pilot_lanes["arm"]}
    assert coverage_pilot({}, policy, classes) == {}
    assert coverage_memory("4Gi") == coverage_memory("4096Mi") == coverage_memory("4294967296")
    lane = [
        ({"powerMode": "fixed", "model": model, "cellId": str(index)},
         {"spec": {"state": {"candidate": {"executionPath": path}}}})
        for index, path in enumerate(("firstfloor-ort-cuda-vlm-arm64", "firstfloor-ort-cpu-arm64",
                                     "firstfloor-ort-cuda-arm64", "firstfloor-ort-cuda-vlm-arm64",
                                     "firstfloor-ort-cpu-int8-arm64"))]
    for index, m in ((5, "smolvlm2-500m-video-instruct"), (6, model)):
        lane.append(({"powerMode": "fixed", "model": m, "cellId": str(index)},
                     {"spec": {"state": {"candidate": {"executionPath": "firstfloor-ort-cuda-amd64"}}}}))
    assert [r["cellId"] for r, _ in coverage_lane_order(lane, policy)] == ["1", "2", "4", "6", "0", "3", "5"]
    print("coverage selection, inheritance, batches, one-call boundary, monotone exclusion and runtime-family pilot checked")


def self_check():
    """Small runnable derivation regression, reserved for the coordinated gate."""
    inp = load(EVIDENCE / "d-method/d-inputs-and-calculations.json")
    floor = load(EVIDENCE / "d-method/legacy-floor-scenario.json")
    floors = {r["caseID"]: r for r in floor["rows"]}
    count = 0
    for row in inp["rows"]:
        if not row.get("profileID"):
            continue
        expected = floors[row["caseID"]]
        d = derive(row, expected["framedDecodedRequestUpperBytes"], inp["pipelineScenario"])
        assert d["maximumIdleFraction"] == COEFFICIENTS["maximumIdleBaseFraction"] == 0.05
        assert d["N"] == expected["N"], row["caseID"]
        assert d["requestLifetimeSeconds"] == expected["requestLifetimeSecondsScenario"], row["caseID"]
        assert d["durationSeconds"] == expected["measurementWindowSecondsScenario"], row["caseID"]
        assert d["supplyExtensionSeconds"] == math.ceil(d["requestLifetimeSeconds"] + d["deliveryMarginMillis"] / 1000)
        assert d["plannedElapsedSeconds"] == sum(d[k] for k in ("preparationSeconds", "warmupSeconds", "durationSeconds", "supplyExtensionSeconds", "drainSeconds", "jobSetupSeconds", "collectionSeconds")) <= 3600
        sizing = d["supplySizing"]
        bounds = {"queue-cap": sizing["workerCap"],
                  "bytes-cap": sizing["queueBytesCap"] // sizing["framedDecodedRequestUpperBytes"],
                  "lifetime": math.floor((sizing["requestLifetimeSeconds"] - sizing["marginSeconds"]) /
                                         (sizing["batch"] * sizing["profileMaxBatchSeconds"]))}
        assert d["workers"] == min(bounds.values())
        assert sizing["limitingBound"] == min(bounds, key=bounds.get)
        assert d["waitingBatches"] == math.ceil(d["workers"] / row["batch"]) - 1
        assert d["waitingSeconds"] + sizing["marginSeconds"] <= d["requestLifetimeSeconds"]
        assert d["socketTimeoutSeconds"] > d["requestLifetimeSeconds"] + d["deliveryMarginMillis"] / 1000
        count += 1
    assert count == 34
    assert required_batches(1, 0) == 3
    row = next(r for r in inp["rows"] if r.get("profileID") and r["caseID"] == "D05")
    d = derive(row, floors["D05"]["framedDecodedRequestUpperBytes"], inp["pipelineScenario"])
    assert d["workers"] == 64
    assert d["capacityFeasible"]
    d34 = next(r for r in inp["rows"] if r.get("profileID") and r["caseID"] == "D34")
    assert derive(d34, floors["D34"]["framedDecodedRequestUpperBytes"], inp["pipelineScenario"])["workers"] == 26
    for missing in (None, 0, -1, float("nan"), float("inf")):
        bad = dict(row, maxObservedSeconds=missing)
        try:
            derive(bad, floors["D05"]["framedDecodedRequestUpperBytes"], inp["pipelineScenario"])
        except ValueError:
            pass
        else:
            raise AssertionError("missing/invalid observed maximum must not fall back to mean")
    limited = derive(row, 67108864 // 3, inp["pipelineScenario"])
    assert limited["workers"] == 3
    assert limited["supplySizing"]["limitingBound"] == "bytes-cap"
    impossible = derive(row, 67108865, inp["pipelineScenario"])
    assert not impossible["capacityFeasible"]
    try:
        occupancy(row, impossible, [])
    except ValueError:
        pass
    else:
        raise AssertionError("K below one batch must not produce a CR")
    print("34 approved profile calculations checked")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, default=DEFAULT_OUTPUT)
    parser.add_argument("--self-check", action="store_true")
    parser.add_argument("--coverage", action="store_true", help="generate read-only live coverage plan")
    parser.add_argument("--plan-config", type=Path, default=HERE / "coverage-plan.json")
    parser.add_argument("--preflight", action="store_true", help="read-only preflight, one sample per model")
    parser.add_argument("--pilot", action="store_true", help="also emit runtime-family coverage and smallest-memory heavy-model pilot")
    args = parser.parse_args()
    if (args.preflight or args.pilot) and not args.coverage:
        parser.error("--preflight and --pilot require --coverage")
    if args.coverage:
        if args.self_check:
            coverage_self_check()
        else:
            generate_coverage(args.output_dir, args.plan_config, args.preflight, args.pilot)
    elif args.self_check:
        self_check()
    else:
        generate(args.output_dir)
        print(args.output_dir)

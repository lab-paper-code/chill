#!/usr/bin/env python3
"""Stage the approved 2026-10-24 conditions; never apply CRs or regenerate history.

Run: python3 gen.py [--output-dir PATH]
Requires the already-installed scipy for the approved Student-t planning rule.
Run --self-check only during the coordinated gate pass. Outputs are JSON/YAML,
including the explicit resume plan; feeder-hold.json is never changed.
"""
import argparse
import copy
import hashlib
import json
import math
import os
from decimal import Decimal
from pathlib import Path

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
    "pipelineMargin": 1.25, "tailSigma": 3, "maximumIdleBaseFraction": 0.01,
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
              "lifetime": math.floor((lifetime - delivery) / (b * mu))}
    limiting_bound = min(bounds, key=bounds.get)
    workers = bounds[limiting_bound]
    q = math.ceil(workers / b) - 1
    waiting_sum = workers * b * mu
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
    idle = min(c["maximumIdleBaseFraction"], c["idleDErrorSecondsPerRequest"] /
               (c["timeMargin"] * mu / b + c["idleDErrorSecondsPerRequest"]))
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
            "supplySizing": {"batch": b, "profileMeanBatchSeconds": mu,
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
        "legacyInputWarning": "Do not use feeder.py phase-a/lanes: those modes intentionally retain original files. Use --manifest and --stages below.",
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
        "precondition": "after coordinated rollout and numbered Main instruction; no competing legacy feeder",
        "representatives": "CampaignOps directly executes the exact single-CR stage1/2/3 creationCommands after Main instruction; existing all-hold remains unchanged. Stage3 additionally requires exactly-once old017 cleanup first.",
        "remainingAPreD2": "only after representative acceptance, CampaignOps clears existing pause/category holds for the approved staged scope and starts remainingAPreD2FeederCommand; no competing legacy feeder",
        "postD2": "only after D2 acceptance start the separate stage7 command",
        "preserve": "existing drop rule always remains unchanged; no new hold bypass fields",
        "originalHoldEditedByGenerator": False}
    write_json(output / "resume-plan.json", resume)
    return conditions


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
        assert d["N"] == expected["N"], row["caseID"]
        assert d["requestLifetimeSeconds"] == expected["requestLifetimeSecondsScenario"], row["caseID"]
        assert d["durationSeconds"] == expected["measurementWindowSecondsScenario"], row["caseID"]
        assert d["supplyExtensionSeconds"] == math.ceil(d["requestLifetimeSeconds"] + d["deliveryMarginMillis"] / 1000)
        assert d["plannedElapsedSeconds"] == sum(d[k] for k in ("preparationSeconds", "warmupSeconds", "durationSeconds", "supplyExtensionSeconds", "drainSeconds", "jobSetupSeconds", "collectionSeconds")) <= 3600
        sizing = d["supplySizing"]
        bounds = {"queue-cap": sizing["workerCap"],
                  "bytes-cap": sizing["queueBytesCap"] // sizing["framedDecodedRequestUpperBytes"],
                  "lifetime": math.floor((sizing["requestLifetimeSeconds"] - sizing["marginSeconds"]) /
                                         (sizing["batch"] * sizing["profileMeanBatchSeconds"]))}
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
    args = parser.parse_args()
    if args.self_check:
        self_check()
    else:
        generate(args.output_dir)
        print(args.output_dir)

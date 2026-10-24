#!/usr/bin/env python3
"""Offline D2 binding. Reads supplied JSON only; never contacts a cluster.

Usage: python3 bind_before.py --scheduler FULL_GET.json --capacity ROWS.json
       --nodes NODES.json --pods PODS.json --out /absolute/path/binding.json
Exit 2 and a written binding with errors means NOT executable. Capacity must be
computed separately from the exact scheduler snapshot; this tool never builds it.
"""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import sys
import tempfile

CONFIG = json.loads(Path(os.environ['CHILL_EXPERIMENT_CONFIG']).read_text())
UID = CONFIG['d2']['workloadUID']
NAME = CONFIG['d2']['workloadName']
SLO = {'percentile': 95, 'targetLatencyNanos': 1000000000}
NAMES = tuple(CONFIG['d2']['nodes'])
BEFORE_SHA = 'e18ac391d4353e5abba03558b163312f2a7b629b'
SETTINGS = ('completionMarginMS', 'capacityWindowNanos', 'capacityWindows',
            'capacityPassingWindows', 'capacityMinimumRequests')
SOURCES = ('internal/serving/capacity.go:31-111',
           'internal/serving/scheduler_replay.go:311-439',
           'internal/serving/kube/scheduler.go:563-579',
           'internal/serving/kube/measurement_sequence.go:254-297',
           'internal/serving/httpapi/scheduler_presentation.go:15-46')


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False)


def positive(value):
    return type(value) in (int, float) and math.isfinite(value) and value > 0


def require(obj, fields, label):
    if not isinstance(obj, dict):
        raise ValueError(label + ': expected object')
    missing = [f for f in fields if f not in obj or obj[f] is None]
    if missing:
        raise ValueError(label + ': missing ' + ', '.join(missing))


def measured_condition(instance, profile, batch):
    c = profile['condition']
    return canonical([instance['node'], c.get('artifact'), c.get('runtimeFamily'),
                      c.get('backend'), c.get('runtimeImage'), c.get('preprocessor'),
                      (c.get('activeProtocol') or {}).get('dataset'), c.get('powerMode'),
                      c.get('cpu'), c.get('firstfloor'), c.get('runtimeAccess'),
                      c.get('plannedBatchSizes'), instance['allocation'], batch, instance['maxWaitNanos']])


def measured_stamp(profile):
    stamp = profile['measuredAt']
    if not isinstance(stamp, str) or not stamp.endswith('Z') or len(stamp) < 20:
        raise ValueError('profile.measuredAt must retain observed UTC RFC3339 form')
    return stamp[:19], stamp[19:-1].lstrip('.').ljust(9, '0')


def allocations_fit(node, allocation, cpus):
    reasons = []
    used_cpu, used_gpu = set(), set()
    budget = quota = memory = 0
    for a in [node['reserved'], *(node.get('nonServing') or []), allocation]:
        require(a, ('memoryBytes', 'cpuCount', 'cpuReserved'), 'budget allocation')
        mask = a.get('cpus') or []  # Go nil slice is the observed empty CPU set.
        count = a['cpuCount'] or len(mask)
        memory += a['memoryBytes']
        if count < 0:
            reasons.append('UnknownCPUAllocationCount')
        if a['cpuReserved']:
            quota += count
        else:
            budget += count
            if count > len(mask):
                reasons.append('UnknownExclusiveCPUAffinity')
            for cpu in mask:
                if cpu not in cpus or cpu in used_cpu:
                    reasons.append('ExclusiveCPUInfeasible')
                used_cpu.add(cpu)
        gpu = a.get('gpuUUID')
        if gpu:
            if gpu not in (node.get('gpuUUIDs') or []) or gpu in used_gpu:
                reasons.append('ExclusiveGPUInfeasible')
            used_gpu.add(gpu)
    if budget > len(cpus) or quota > len(node['reserved'].get('cpus') or []):
        reasons.append('CPUBudgetInfeasible')
    if memory > node['memoryBytes']:
        reasons.append('MemoryBudgetInfeasible')
    return reasons


def eligibility(instance, profile, node, kube, settings, batch):
    """Native fixed-layout eligibility, not a claim of autonomous plan feasibility."""
    require(instance, ('id', 'node', 'allocation', 'maxBatch', 'capacity', 'weight', 'maxWaitNanos'), 'option.instance')
    require(profile, ('id', 'condition', 'batches', 'evidence', 'runtimeMetadata'), 'profile')
    condition, runtime, a = profile['condition'], profile['runtimeMetadata'], instance['allocation']
    require(condition, ('nodeUID', 'powerMode', 'runtimeFamily', 'runtimeImage', 'backend', 'cpu', 'firstfloor', 'logicalModel'), 'profile.condition')
    require(runtime, ('runtime', 'cpuAffinity', 'intraOpThreads', 'initializedBatches'), 'runtimeMetadata')
    require(a, ('cpus', 'memoryBytes', 'threads', 'cpuCount', 'cpuReserved'), 'allocation')
    require(node, ('node', 'ready', 'powerMode', 'cpus', 'memoryBytes', 'reserved'), 'scheduler node')
    require(condition['cpu'], ('affinityCPUs', 'ortIntraOpThreads', 'request', 'limit'), 'condition.cpu')
    require(condition['firstfloor'], ('maxBatchSize', 'maxQueueSize', 'deadlinePolicy'), 'condition.firstfloor')
    reasons = []
    if node['node'] != instance['node'] or condition['nodeUID'] != instance['node']['uid']:
        reasons.append('ProfileConditionNodeUIDMismatch')
    if kube['metadata']['uid'] != instance['node']['uid']:
        reasons.append('CurrentNodeUIDMismatch')
    if kube['metadata'].get('deletionTimestamp') or kube['spec'].get('unschedulable'):
        reasons.append('KubernetesNodeDeletingOrUnschedulable')
    ready = [c for c in kube.get('status', {}).get('conditions', []) if c.get('type') == 'Ready']
    if not node['ready'] or len(ready) != 1 or ready[0].get('status') != 'True':
        reasons.append('NodeUnavailable')
    if node.get('measurement'):
        reasons.append('MeasurementNode')
    for reason in node.get('reasons') or []:
        # ExcludedNode is the captured campaign autonomy hold, not a physical
        # rejection of the explicitly approved eight-node fixed-layout probe.
        # Preserve it in binding provenance; do not claim current autonomy works.
        if reason == 'ExcludedNode' and node['node']['name'] in NAMES:
            continue
        if reason != 'PowerModeUnavailable' or condition['powerMode'] != node['powerMode']:
            reasons.append('NodeReason:' + reason)
    cpus = node['cpus']
    if condition['powerMode'] != node['powerMode']:
        cpus = (node.get('modeCPUs') or {}).get(condition['powerMode'])
        if cpus is None:
            reasons.append('PlacementInventoryMissing:targetModeCPUs')
    if condition['runtimeFamily'] != 'firstfloor' or condition['backend'] != runtime['runtime']:
        reasons.append('NativeRuntimeMismatch')
    if not condition['runtimeImage'] or '@sha256:' not in condition['runtimeImage']:
        reasons.append('NativeRuntimeImageNotPinned')
    if runtime.get('synthetic') is not False:
        reasons.append('NativeRuntimeSyntheticOrUnknown')
    if sorted(a['cpus']) != sorted(runtime['cpuAffinity']) or sorted(a['cpus']) != sorted(condition['cpu']['affinityCPUs']):
        reasons.append('MeasuredAffinityMismatch')
    no_threads = a['threads'] == 0 and condition['backend'] == 'rknnlite'
    if not a['cpus'] or (not no_threads and not 1 <= a['threads'] <= len(a['cpus'])) or a['threads'] != runtime['intraOpThreads'] or a['threads'] != condition['cpu']['ortIntraOpThreads']:
        reasons.append('CPUThreadAllocationMismatch')
    if a.get('gpuUUID', '') != runtime.get('gpuUUID', ''):
        reasons.append('RuntimeGPUUUIDMismatch')
    if len(a['cpus']) > settings['maxCPUs']:
        reasons.append('MaxCPUsExceeded')
    if instance['capacity'] < 1 or instance['weight'] < 0 or instance['maxWaitNanos'] < 0:
        reasons.append('MissingQueueOrTransportEvidence')
    if instance['capacity'] != condition['firstfloor']['maxQueueSize']:
        reasons.append('NativeQueueConditionMismatch')
    batches = [b for b in profile['batches'] if b['batch'] <= batch]
    if not profile['evidence'] or not any(b['batch'] == 1 for b in batches):
        reasons.append('MissingProfileOrSingleBatchEvidence')
    if batch not in runtime['initializedBatches'] or batch > condition['firstfloor']['maxBatchSize']:
        reasons.append('BatchNotInitializedOrOutOfNativeBounds')
    for b in batches:
        require(b, ('durationNanos', 'memoryBytes', 'energyPerInputJoules'), 'profile batch')
        if not b['durationNanos'] or not all(positive(v) for v in b['durationNanos']):
            reasons.append('InvalidOrMissingServiceSample')
        energy = b['energyPerInputJoules']
        if type(energy) not in (int, float) or not math.isfinite(energy) or energy < 0:
            reasons.append('MissingServiceEnergyEvidence')
    memory = max((b['memoryBytes'] for b in batches), default=0)
    if not memory or memory + settings['servingMemoryBytes'] > a['memoryBytes']:
        reasons.append('MeasuredMemoryInfeasible')
    if cpus is not None:
        reasons.extend(allocations_fit(node, a, cpus))
    return sorted(set(reasons))


def bind(args):
    result = {'schema': 'd2-before-binding.v1', 'workload': {'name': NAME, 'uid': UID, 'slo': SLO},
              'beforeSha': BEFORE_SHA, 'settings': None, 'selected': [], 'sumKappa': None,
              'autonomousRate': None, 'rates': [], 'inputs': {}, 'prerequisites': [],
              'errors': [], 'exclusions': [], 'nativePolicyRealizations': [],
              'sourceProvenance': {'beforeShaAuthority': 'deployment2-before-plan.json sourcePair.before.sha',
                                   'eligibilitySource': list(SOURCES),
                                   'scope': 'evidence-bound native fixed layout; no autonomous transition/energy-plan eligibility claim',
                                   'shaAttestation': 'approved source identity; live image continuity is a conductor prerequisite'}}
    docs = {}
    for name in ('scheduler', 'capacity', 'nodes', 'pods'):
        path = Path(getattr(args, name)).resolve()
        try:
            raw = path.read_bytes()
            docs[name] = json.loads(raw, parse_constant=lambda x: (_ for _ in ()).throw(ValueError('non-finite JSON number: ' + x)))
            result['inputs'][name] = {'path': str(path), 'sha256': hashlib.sha256(raw).hexdigest(), 'bytes': len(raw)}
        except (OSError, ValueError) as exc:
            result['errors'].append(name + ': ' + str(exc))
    if result['errors']:
        return result
    try:
        scheduler = docs['scheduler']
        require(scheduler, ('settings',), 'full scheduler GET')
        settings = result['settings'] = scheduler['settings']
        require(settings, (*SETTINGS, 'maxCPUs', 'servingMemoryBytes'), 'scheduler.settings')
        if 'state' in scheduler:
            require(scheduler['state'], ('snapshot',), 'scheduler.state')
            snapshot = scheduler['state']['snapshot']
        else:
            require(scheduler, ('snapshot',), 'scheduler')
            snapshot = scheduler['snapshot']
        require(snapshot, ('at', 'workloads', 'options', 'nodes', 'dispatchOccupancy'), 'snapshot')
        result['inputs']['snapshotAt'] = snapshot['at']
        result['scenarioConditions'] = {
            'capturedExcludedNodes': settings.get('excludedNodes'),
            'capturedNodeReasons': {n['node']['name']: n.get('reasons') or [] for n in snapshot['nodes'] if n['node']['name'] in NAMES},
            'fixedLayoutNodes': list(NAMES),
            'approvedAutonomousExcludedNodes': list(CONFIG['d2']['excludedNodes']),
            'authority': 'User-approved P3A eight-node scope; campaign hold ExcludedNode is not physical ineligibility. Main/CPO must explicitly apply and restore the approved autonomy settings; no source snapshot is rewritten.'
        }
        workloads = [w for w in snapshot['workloads'] if w.get('uid') == UID]
        if len(workloads) != 1 or workloads[0].get('name') != NAME or workloads[0].get('slo') != SLO:
            raise ValueError('Canonical tight MNV UID/name/p95 SLO1s absent, ambiguous or mismatched')
        capacity = docs['capacity']
        rows = capacity if isinstance(capacity, list) else capacity['rows']
        if not isinstance(rows, list):
            raise ValueError('capacity must be a list or observed {rows:[...]} envelope')
        for index, row in enumerate(rows):
            require(row, ('input', 'status'), 'capacity.rows[' + str(index) + ']')
            if not isinstance(row['input'], dict) or not isinstance(row['status'], str):
                raise ValueError('capacity row input/status structure is malformed')
            if row.get('kappa') is not None and (type(row['kappa']) not in (int, float) or not math.isfinite(row['kappa'])):
                raise ValueError('capacity row kappa is malformed or nonfinite')
        for key in ('nodes', 'pods'):
            require(docs[key], ('items',), 'Kubernetes ' + key + ' List')
            if not isinstance(docs[key]['items'], list):
                raise ValueError(key + '.items must be array')
        knodes = docs['nodes']['items']
        for name in NAMES:
            sn = [n for n in snapshot['nodes'] if n.get('node', {}).get('name') == name]
            kn = [n for n in knodes if n.get('metadata', {}).get('name') == name]
            if len(sn) != 1 or len(kn) != 1:
                result['errors'].append(name + ': current scheduler/Kubernetes Node missing or ambiguous')
                continue
            node, kube = sn[0], kn[0]
            candidates = []
            relevant = 0
            for option_index, option in enumerate(snapshot['options']):
                i = option.get('instance', {})
                if i.get('node', {}).get('name') != name:
                    continue
                if UID not in (option.get('compatibleUIDs') or []):
                    result['exclusions'].append({'node': name, 'optionIndex': option_index, 'optionID': i.get('id'), 'reasons': ['CanonicalWorkloadNotCompatible']})
                    continue
                relevant += 1
                try:
                    p = i['profile']
                    if isinstance(p, dict) and '$ref' in p:
                        p = scheduler['profiles'][p['$ref']]
                    require(p, ('id', 'batches', 'condition'), 'option profile')
                    variants = [b['batch'] for b in p['batches'] if b['batch'] <= i['maxBatch']]
                    if not variants:
                        raise ValueError('NoMeasuredMaxBatchVariants')
                    for batch in variants:
                        # Latest measured profile wins for an equivalent native
                        # condition/resource/batch, as in schedulerMeasuredOptions.
                        condition_key = measured_condition(i, p, batch)
                        stamp = measured_stamp(p)
                        newer = []
                        for other in snapshot['options']:
                            oi = other['instance']
                            if oi['node'] != i['node']:
                                continue
                            op = oi['profile']
                            if isinstance(op, dict) and '$ref' in op:
                                op = scheduler['profiles'][op['$ref']]
                            if batch <= oi['maxBatch'] and any(b['batch'] == batch for b in op['batches']):
                                if measured_condition(oi, op, batch) == condition_key and measured_stamp(op) > stamp:
                                    newer.append(op['id'])
                        if newer:
                            result['exclusions'].append({'node': name, 'optionID': i['id'], 'profileID': p['id'],
                                                         'maxBatch': batch, 'reasons': ['SupersededMeasuredCondition'],
                                                         'supersededBy': sorted(set(newer))})
                            continue
                        reasons = eligibility(i, p, node, kube, settings, batch)
                        expected = {'workloadUID': UID, 'slo': SLO, 'node': i['node'],
                                    'powerMode': p['condition']['powerMode'], 'profileID': p['id'],
                                    'allocation': i['allocation'], 'maxBatch': batch,
                                    **{k: settings[k] for k in SETTINGS}}
                        matches = [r for r in rows if isinstance(r, dict) and isinstance(r.get('input'), dict)
                                   and all(r['input'].get(k) == v for k, v in expected.items())]
                        row = matches[0] if len(matches) == 1 else None
                        if not matches:
                            reasons.append('MissingExactCapacityInput')
                        elif len(matches) != 1:
                            reasons.append('AmbiguousExactCapacityInput')
                        else:
                            if row.get('status') != 'Built':
                                reasons.append('CapacityNotBuilt:' + str(row.get('status')) + ':' + str(row.get('reason', '')))
                            if not positive(row.get('kappa')):
                                reasons.append('CapacityKappaMissingNonfiniteOrNonpositive')
                            if row.get('status') not in ('Built', 'CapacityRejected'):
                                reasons.append('CapacityEligibilityPendingOrUnknown')
                            if row.get('status') == 'Built' and row.get('kappa') is None:
                                reasons.append('BuiltCapacityKappaUnknown')
                            if row.get('previous'):
                                reasons.append('PreviousCapacityRowNotCurrentBuild')
                            if not row.get('key') or not row.get('builtAt'):
                                reasons.append('MissingCapacityBuildProvenance')
                            occupancy = row['input'].get('dispatchOccupancy')
                            matching_occupancy = [o for o in snapshot['dispatchOccupancy'] if o.get('node', {}).get('uid') == i['node']['uid']
                                                  and o.get('powerMode') == p['condition']['powerMode'] and o.get('runtimeImage') == p['condition']['runtimeImage']
                                                  and o.get('model') == p['condition']['logicalModel']]
                            if row.get('status') == 'Built' and (len(matching_occupancy) != 1 or occupancy != matching_occupancy[0] or not occupancy or not occupancy.get('source')):
                                reasons.append('DispatchOccupancyMissingAmbiguousOrMismatched')
                        audit = {'node': name, 'optionIndex': option_index, 'optionID': i['id'], 'profileID': p['id'],
                                 'maxBatch': batch, 'capacityInput': expected, 'capacityStatus': row.get('status') if row else None,
                                 'capacityReason': row.get('reason') if row else None, 'reasons': sorted(set(reasons))}
                        if reasons:
                            result['exclusions'].append(audit)
                            continue
                        candidates.append({'node': i['node'], 'profileID': p['id'], 'optionID': i['id'],
                                           'maxBatch': batch, 'kappa': row['kappa'], 'allocation': i['allocation'],
                                           'powerMode': p['condition']['powerMode'], 'runtimeImage': p['condition']['runtimeImage'],
                                           'firstfloor': p['condition']['firstfloor'], 'profileCondition': p['condition'],
                                           'runtimeMetadata': p['runtimeMetadata'], 'capacity': i['capacity'], 'maxWaitNanos': i['maxWaitNanos'],
                                           'measuredBatches': [b['batch'] for b in p['batches']],
                                           'capacityRow': row, 'profileReference': p.get('reference'), 'profileEvidence': p['evidence'],
                                           'manualPrepareMaxBatch': i['maxBatch']})
                except (KeyError, TypeError, ValueError) as exc:
                    result['exclusions'].append({'node': name, 'optionIndex': option_index, 'optionID': i.get('id'),
                                                 'reasons': ['EligibilityUnknown:' + str(exc)]})
                    # Unknown candidates could be the maximum: never silently choose around them.
                    result['errors'].append(name + ': option ' + str(i.get('id')) + ' eligibility unknown: ' + str(exc))
            if not candidates:
                result['errors'].append(name + ': no eligible positive-kappa candidate (' + str(relevant) + ' compatible options inspected; see exclusions)')
                continue
            candidates.sort(key=lambda c: (-c['kappa'], c['profileID'], c['optionID'], c['maxBatch'], canonical(c['allocation'])))
            chosen = candidates[0]
            result['selected'].append(chosen)
            if chosen['powerMode'] != node['powerMode']:
                result['errors'].append(name + ': selected maxeligible kappa mode ' + chosen['powerMode']
                                        + ' differs from current ' + node['powerMode']
                                        + '; manual prepare cannot actuate power; explicit approved existing mode-transition binding required')
            if chosen['maxBatch'] != chosen['manualPrepareMaxBatch']:
                result['nativePolicyRealizations'].append({
                    'node': chosen['node'], 'profileID': chosen['profileID'],
                    'desiredMaxBatch': chosen['maxBatch'], 'defaultMaxBatch': chosen['manualPrepareMaxBatch'],
                    'maxWaitNanos': chosen['maxWaitNanos'],
                    'initializedBatches': chosen['runtimeMetadata']['initializedBatches'],
                    'status': 'PLANNED_NOT_OBSERVED',
                    'authority': 'Main: existing finite native policy protocol realizes approved strict maximum, not a new setter or changed measurement condition. CampaignOps requires a numbered exact-instance release.',
                    'required': 'prepare-defaults -> capture-policy -> exact native-policy release/ACK/next Tick -> normal bind-routing PUT/final native+snapshot+route ACK; never load at the temporary default policy.'
                })
        if len(result['selected']) == len(NAMES):
            total = math.fsum(c['kappa'] for c in result['selected'])
            result['sumKappa'] = total
            result['autonomousRate'] = min(50, 0.3 * total)
            result['rates'] = [0] + [50 * n for n in range(1, math.ceil(total / 50))] + [total]
        result['prerequisites'] = [
            'Main releases fresh computed rows from the exact repaired frozen scheduler input; stale archived rows are not current evidence.',
            'CampaignOps attests rev72/e18ac source and runtime7 image identities against captured Pods before execution.',
            'Conductor binds actual server/client/mesh resources, TLS/CA references, payload IDs/order and physical native identities; no token is read here.',
            'Main confirms cost13/empty10/catalog repair complete and records frozen evidence through both halves.',
            'Captured ExcludedNode campaign holds remain provenance, not a physical fixed-layout veto; approved P3A removes only its specified holds and cleanup restores original settings.',
            'Nondefault strict maxima require the Main-approved existing finite native policy protocol with actual physical/ConfigMap binding and native plus next-Tick plus normal-route ACK; no fixedBatchSize or weaker-candidate substitution.',
            'No live action is authorized by a binding file; parent owns explicit release, idle/ownership/mode/readback gates and exact cleanup.'
        ]
    except (KeyError, TypeError, ValueError) as exc:
        result['errors'].append('InputContract: ' + str(exc))
    result['status'] = 'BLOCKED' if result['errors'] else 'BOUND_PENDING_EXECUTION_PREREQUISITES'
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    for name in ('scheduler', 'capacity', 'nodes', 'pods', 'out'):
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args()
    out = Path(args.out)
    if not out.is_absolute():
        parser.error('--out must be absolute')
    if out.resolve() in {Path(getattr(args, n)).resolve() for n in ('scheduler', 'capacity', 'nodes', 'pods')}:
        parser.error('--out must not overwrite an input')
    result = bind(args)
    result['status'] = 'BLOCKED' if result['errors'] else 'BOUND_PENDING_EXECUTION_PREREQUISITES'
    out.parent.mkdir(parents=True, exist_ok=True)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode='w', encoding='utf-8', dir=out.parent, prefix='.binding-', delete=False) as handle:
            temporary = handle.name
            json.dump(result, handle, indent=2, allow_nan=False)
            handle.write('\n')
        os.replace(temporary, out)
    finally:
        if temporary and os.path.exists(temporary):
            os.unlink(temporary)
    print(json.dumps({'status': result['status'], 'out': str(out), 'errors': result['errors']}, allow_nan=False))
    return 2 if result['errors'] else 0


if __name__ == '__main__':
    sys.exit(main())

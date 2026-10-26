#!/usr/bin/env python3
"""Stage only the approved post-D2 d25; no cluster calls or shared-file writes."""
import argparse
import copy
import datetime as dt
import hashlib
import importlib.util
import json
import os
import math
from pathlib import Path

CONFIG = json.loads(Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
ENV = CONFIG["measure"]
CODE = Path(__file__).resolve().parents[3]
OUT = Path(ENV["prepareOutput"])
ROOT = Path(CONFIG["paths"]["ops"])
V = Path(CONFIG["paths"]["campaign"])
GENERAL = V / 'phase-a/measurement-generalization-20261024'
INPUT = V / 'phase-a/measurement-generalization-20261023/d-method/d-inputs-and-calculations.json'
FLOOR = INPUT.parent / 'legacy-floor-scenario.json'
CAPTURE = Path(ENV['prepareCapture'])
STATE = CAPTURE / 'state-pre.json'
RUNTIME = CAPTURE / 'runtime-seven-invariant-pre.json'
INVENTORY = Path(ENV['prepareInventory'])
TARGETS = ENV['prepareTargets']
REPRESENTATIVE = 'D06'


def load(path):
    return json.loads(Path(path).read_text())


def identity(path):
    return {'path': str(path), 'sha256': hashlib.sha256(Path(path).read_bytes()).hexdigest()}


def save(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + '\n')


def generate():
    started = dt.datetime.now(dt.timezone.utc).isoformat()
    module = importlib.util.spec_from_file_location('approved_d_generator', CODE / 'gen.py')
    gen = importlib.util.module_from_spec(module)
    module.loader.exec_module(gen)
    inputs, floors = load(INPUT), {r['caseID']: r for r in load(FLOOR)['rows']}
    snapshot, runtime, inventory = load(STATE)['snapshot'], load(RUNTIME), load(INVENTORY)
    options = {o['instance']['profile']['id']: o['instance'] for o in snapshot['options']}
    workloads = {w['name']: w for w in snapshot['workloads']}
    rows = {r['caseID']: r for r in inputs['rows']}
    partial_g2 = CAPTURE / 'jetsono-982-g2-ranked-empty-repair-20261024/raw-index.json'
    source_paths = [INPUT, FLOOR, CODE / 'gen.py', STATE, RUNTIME, INVENTORY,
                    Path(ENV['authority']),
                    ROOT / 'intent/north-star.md', ROOT / 'intent/user-intent.md', partial_g2]
    provenance = [identity(p) for p in source_paths]
    entries, lanes = [], []
    for node, cases in TARGETS.items():
        lane_entries = []
        for case in cases:
            row = copy.deepcopy(rows[case])
            actual = options[row['profileID']]
            condition = actual['profile']['condition']
            workload = {k: workloads[row['workload']['name']][k] for k in ('name', 'uid', 'generation', 'slo')}
            if workload != row['workload']:
                raise ValueError(f'{case}: Workload/SLO changed; fresh approved binding required')
            if (actual['node']['uid'] != row['inputIdentity']['targetNode']['uid'] or
                    condition['runtimeImage'] != row['runtimeImage'] or
                    condition['cpu'] != row['inputIdentity']['cpu'] or
                    condition['powerMode'] != row['powerMode']):
                raise ValueError(f'{case}: captured profile identity changed')
            d = gen.derive(row, floors[case]['framedDecodedRequestUpperBytes'], inputs['pipelineScenario'])
            cr = gen.occupancy(row, d, provenance)
            cr['metadata']['name'] = cr['metadata']['name'].replace('-970-', '-975-')
            cr['metadata']['annotations'] = {'chill.dacs.io/preparation-scope': 'post-d2-approved25',
                'chill.dacs.io/source-case': case}
            filename = OUT / 'cr' / (cr['metadata']['name'] + '.yaml')
            gen.write_docs(filename, [cr])
            related = [o['instance']['profile']['id'] for o in snapshot['options']
                       if (o['instance']['node']['uid'], o['instance']['profile']['condition']['powerMode'],
                           o['instance']['profile']['condition']['runtimeImage'], o['instance']['profile']['condition']['logicalModel'])
                       == (actual['node']['uid'], row['powerMode'], row['runtimeImage'], row['model'])]
            costs = [c for c in snapshot['costs'] if c['node']['uid'] == actual['node']['uid'] and
                     any(pid in json.dumps({k: c.get(k) for k in ('from', 'to', 'beforeState', 'targetState')}) for pid in related)]
            baselines = [b for b in snapshot['baselines'] if b['node']['uid'] == actual['node']['uid'] and
                         (not b.get('state') or any(pid in json.dumps(b['state']) for pid in related))]
            old_d = [v for v in snapshot['dispatchOccupancy'] if
                     (v['node']['uid'], v['powerMode'], v['runtimeImage'], v['model']) ==
                     (actual['node']['uid'], row['powerMode'], row['runtimeImage'], row['model'])]
            entry = {'caseID': case, 'group': 'PLC' if row['model'] == 'plc-lstm-ae' else
                     'MNVGPU' if 'cuda' in row['executionPath'] else 'MNVCPU',
                     'node': actual['node'], 'powerMode': row['powerMode'],
                     'profileID': row['profileID'], 'profileReference': actual['profile']['reference'],
                     'sourceProfileResult': row['sourceProfileResult'],
                     'profileContentInputSHA256': row['profileContentInputSHA256'],
                     'runtimeImage': row['runtimeImage'], 'allocation': actual['allocation'],
                     'condition': condition, 'workload': workload,
                     'frozenProfileInputs': gen.frozen_inputs(row), 'computed': d,
                     'NKL': {'N': d['N'], 'K': d['workers'], 'LSeconds': d['requestLifetimeSeconds']},
                     'crPath': str(filename), 'crName': cr['metadata']['name'],
                     'scientificStatus': 'prepared, not submitted or measured',
                     'existingD': old_d, 'affectedConsumerProfiles': related,
                     'dependentCosts': costs, 'dependentBaselines': baselines,
                     'dependencyAction': 'Replace d only after actual Accepted/raw/consumer/return closure. Retain cost/baseline identities under unchanged profile/runtime/allocation; no automatic cost or loaded-idle rerun.'}
            entries.append(entry)
            lane_entries.append(entry)
        lane = {'node': node, 'caseOrder': cases, 'powerModes': sorted({e['powerMode'] for e in lane_entries}),
                'paths': [e['crPath'] for e in lane_entries],
                'creationCommands': [[ENV['kubectl'], '--kubeconfig', ENV['kubeconfig'], '--context', ENV['kubeContext'],
                                      '-n', ENV['namespace'], 'create', '-f', e['crPath']] for e in lane_entries],
                'advanceGate': 'CampaignOps issues only next CR after prior actual raw/consumer/normal-return closure; no shell loop or parallel same-Node creates',
                'knownElapsedSeconds': sum(e['computed']['knownElapsedSeconds'] for e in lane_entries),
                'plannedBudgetSeconds': sum(e['computed']['plannedElapsedSeconds'] for e in lane_entries),
                'maximumOneRetryEachBudgetSeconds': 2 * sum(e['computed']['plannedElapsedSeconds'] for e in lane_entries),
                'startGate': 'D2 accepted/bound; paired300s and autonomous660s live AFTER both finished; this Node exact-owned cleanup/drain/remove and native8 normal Ready handback receipt verified. Start immediately per free Node after representative closure; other Node cleanup/offline AFTER work may continue. No native/isolated overlap.'}
        lanes.append(lane)
    representative = next(e for e in entries if e['caseID'] == REPRESENTATIVE)
    broad = [e for e in entries if e['caseID'] != REPRESENTATIVE]
    skips = [{'caseID': r['caseID'], 'node': r['node'], 'BEO': r['BEO'], 'model': r['model'],
              'executionPath': r['executionPath'], 'powerMode': r['powerMode'],
              'reason': 'Smol new-rule complete; no rerun' if r.get('profileID') else 'genesis Smol CUDA OOM excluded'}
             for r in inputs['rows'] if r['caseID'] not in {e['caseID'] for e in entries}]
    reference_modes = {e['node']['name']: e['powerMode'] for e in entries}
    skipped_modes = {}
    for node in TARGETS:
        for doc in gen.read_docs(V / 'lanes' / (node + '.yaml')):
            if doc['kind'] != 'BatchEnergyOptimization':
                continue
            spec = doc['spec']['state']
            mode = spec['candidate'].get('powerMode', 'fixed')
            if mode != reference_modes[node]:
                key = (node, spec['candidate']['modelSpec'], spec['candidate']['executionPath'], mode)
                skipped_modes[key] = {'node': node, 'model': key[1], 'executionPath': key[2], 'powerMode': mode,
                    'reason': 'E5-unused nonreference model/power expansion; not part of d25'}
    for key in sorted(skipped_modes):
        skips.append(skipped_modes[key])
    for node in ENV['excludedNodes']:
        skips.append({'node': node, 'models': ['plc-lstm-ae', 'mobilenet-v2-050', 'smolvlm2-500m-video-instruct'],
                      'powerModes': 'all', 'reason': 'excluded E5 candidate; no generated CR'})
    manifest = {'schemaVersion': 'post-d2-approved25-preparation.v1', 'startedAt': started,
        'preparedAt': dt.datetime.now(dt.timezone.utc).isoformat(), 'executionOwner': 'CampaignOps',
        'authority': ENV['authority'],
        'state': 'source-bound CRs; no live submission; final D2/AFTER fresh bindings required',
        'accounting': {'dNewRule': '9/34', 'exactCost': '69/69', 'evaluation': '0/7',
                       'planned25': {'PLC': 12, 'MNVCPU': 8, 'MNVGPU': 5},
                       'legacyOverlap': {'MNVCPU': 8, 'MNVGPU': 2}},
        'cachedSnapshotAt': snapshot['at'], 'snapshotIsAfterG2': False,
        'partialG2Evidence': {'index': load(partial_g2),
            'meaning': 'first Sono root four Accepted steps including two new empty records; final9/winner/consumer/normal-return/cleanup closure not inferred'},
        'sloBoundary': 'Measurement Workload identities/SLOs are current cached source: MNV p90/1.5s; canonical E5 tight p95/1s and phase6 numerical proposal are separate, not silently substituted.',
        'sourceAlignSchemaBinding': {'reportedCodeSHA': '01b82621102d2c9a7fbccd07f02f37e13b5b3852',
            'reportedDocHEAD': 'ceb95c', 'deploymentStatus': 'unverified and not deployed at preparation',
            'C1': 'existing requestLifetimeSeconds compared with actual HandlerConfig.RequestTimeout; no new CR field',
            'G1': 'conditions.inputsJSON.coefficients loadPreparationFloorSeconds/windowFloorSeconds/plannedElapsedCapSeconds consumed by edge.OccupancyPlanningFloors'},
        'sources': provenance, 'coefficients': gen.COEFFICIENTS, 'pipeline': {k: v for k, v in inputs['pipelineScenario'].items() if k != 'sources'},
        'runtime7Invariant': runtime, 'targets': entries, 'lanes': lanes, 'exactSkipList': skips,
        'representative': {'caseID': REPRESENTATIVE, 'node': representative['node'], 'path': representative['crPath'],
                           'closure': ['actual Accepted raw and full physical/unknown/idle conditions',
                                       'planner consumer exact new source and max(signed_d,0)', 'normal physical safe return']},
        'sequence': [{'stage': 6, 'action': 'first representative only; CampaignOps direct create after fresh gates', 'paths': [representative['crPath']]},
                     {'stage': 7, 'action': 'remaining24 serial perNode, parallel across freeNodes after representative closure', 'paths': [e['crPath'] for e in broad]}],
        'retry': {'maximumIdenticalConditionRetriesPerTarget': 1, 'automaticRetry': False,
                  'steps': ['preserve original CR UID/status/raw and classify failure',
                            'confirm failed load drained, no unresolved native execution and normal safe return',
                            'unchanged profile/NodeUID/runtime/allocation/SLO/coefficients and scope only',
                            'CampaignOps creates one new name by appending -retry1 to a byte-identical spec; preserve relation to original',
                            'second failure stops lane; new conditions/judgment/scope go to user']},
        'freshSubmitBindings': ['SourceAlign final source SHA/tree, CRD schema and control image/deployment identity accepted by Main',
            'C1 actual HandlerConfig.RequestTimeout >= each frozen requestLifetimeSeconds; G1 consumes exact inputsJSON.coefficients 60/360/3600 floors/cap',
            'CostCoverage24/CampaignOps fresh post-G2 snapshot proving exact9empty/current winners/minimum native transitions and cleanup closure; not this preflight snapshot',
            'fresh profileID/result hash, NodeUID, runtime7, CPU/allocation, Workload UID/generation/SLO unchanged; any scientific change stops rather than silently regenerates',
            'D2Before paired AFTER/native8 finish, load0, cleanup and normal Node handback receipts; same paired input freeze until paired completion',
            'fresh no queued/active measurement, load/outstanding, in-flight action/handback or conflicting serving on each target Node'],
        'nodeReleaseReceiptFacts': ['captureUTC and node name/UID', 'paired300s and autonomous660s AFTER terminal references',
            'remaining load/outstanding/nativeexecution/action/handback all0', 'exact owned Pod/Job removal references',
            'Ready, measurement taint absent, normal return mode, native8 use finished'],
        'scheduling': {'representativeFirst': True, 'serialWithinNode': True, 'parallelAcrossFreeNodes': True,
            'samePowerModeAdjacent': True, 'wholePOOLAFTEROverlap': False,
            'estimatedHoursAfterGates': 5, 'twiceEstimateReportAtHours': 10,
            'maximumOneRetryEachPlanningHours': 10,
            'estimateMeaning': 'conservative cap allocation: representative <=1h then longest residual lane <=4h; budgets not observed duration; report failure/stage and at2x estimate; gate waiting and unresolved unsafe return are blockers, never fabricated timings'},
        'executionHandoff': {'feederMode': 'approved manifest only; never old lanes/phase-a mode; no active feeder edit',
            'broadCommandAfterExternalGates': ['python3', str(CODE / 'feeder.py'), '--manifest', str(OUT / 'manifest.json'), '--stages', '7'],
            'immediateFreeNodeExecution': 'Use lane creationCommands via CampaignOps; independent freeNodes in parallel, next sameNode only after verified raw/consumer/normal-return closure. Do not wait for otherNode cleanup or offline AFTER processing.',
            'discipline': 'The feeder command is an alternative only once every included Node is released; it checks taint/terminal, not AFTER/native8 handback. Never run concurrent feeder processes sharing its PID/log/final paths; do not restart on a failed original. One manual identical retry maximum.'},
        'dependencyImpact': {'profileRuntimeAllocationChanges': 'none',
            'dAffected': [e['caseID'] for e in entries],
            'costAndBaselineAffected': 'exact dependentCosts/dependentBaselines per target; current node empty ranking and all current transitions retained below, refresh via final G2 snapshot before submit',
            'currentNodePowerTransitionCosts': [c for c in snapshot['costs'] if c['node']['name'] in TARGETS and c['kind'] == 'power'],
            'g2CurrentLowest': inventory['currentLowestByNode'],
            'g2ExactRankingReplacementsRequired': inventory['rankOnlyRemeasureRows'],
            'separateLoadedIdlePreserved': inventory['separateModelLoadedIdleBaselineCRsNotG2OperationTimingContract']},
        'pairedFreezePreservation': 'Preparation writes only its own output subtree; no BEFORE/AFTER source snapshot, profile, d, cost, baseline, policy or frozen reference is overwritten. Actual d25 creates new roots and new raw only.',
        'forbidden': ['Smol rerun', 'old legacy CR recreation', 'runtime rebuild', 'automatic policy/condition change',
                      'broad S3 scan', 'whole-cache or secret/token manifest publication', 'live create from preparation'],
        'coordinatedCheckArgv': ['python3', str(Path(__file__).resolve()), '--output-dir', str(OUT), '--check']}
    save(OUT / 'manifest.json', manifest)
    refresh_timing()
    print(json.dumps({'preparedAt': manifest['preparedAt'], 'count': len(entries), 'manifest': str(OUT / 'manifest.json')}))


def refresh_timing():
    """Derive ETA only from existing manifest observations; never rewrite CRs."""
    plan = load(OUT / 'manifest.json')
    plan['afterNodeHandoffReceipt'] = {
        'schemaVersion': 'd2-after-node-handoff.v1',
        'pathPattern': str(ROOT / 'audit-20261024/d2-before-execution/after-node-handoff/<node>.json'),
        'owner': 'CampaignOps; interface agreed with D2Before24', 'existsOrReleasedClaimed': False,
        'requiredFields': ['capturedUTC', 'node.name', 'node.uid', 'mainReleaseNumber',
            'pairedAfterTerminalRef', 'autonomousAfterTerminalRef', 'diagnosticDisposition',
            'remainingNative8Use', 'remainingLoad', 'outstanding', 'nativeExecution', 'actions',
            'pendingHandback', 'ownedPodRemovalRefs', 'ownedJobRemovalRefs', 'rawNodeRef',
            'ready', 'measurementTaintAbsent', 'normalReturnMode', 'nativeReturnRef', 'status'],
        'requiredValues': {'remainingNative8Use': False, 'remainingLoad': 0, 'outstanding': 0,
            'nativeExecution': 0, 'actions': 0, 'pendingHandback': 0, 'ready': True,
            'measurementTaintAbsent': True, 'status': 'RELEASED_FOR_D25'},
        'terminalMeaning': 'Both AFTER terminal refs require run-return and protected raw capture; diagnosticDisposition is0-or-terminal1. Fresh exact NodeUID and normal return reference required. Release immediately perNode after D06 closure; no wait for otherNode cleanup/offline work.',
        'purpose': 'AFTER Node lane release only; does not certify d25 measurement completion.'}
    lowest = {v['baseline']['node']['name']: v['baseline']['powerMode']
              for v in plan['dependencyImpact']['g2CurrentLowest']}
    powers = plan['dependencyImpact']['currentNodePowerTransitionCosts']
    entries = {e['caseID']: e for e in plan['targets']}
    for entry in entries.values():
        allocation = entry['allocation']
        state_id = (entry['profileID'] + ':[' + ' '.join(map(str, allocation['cpus'])) + ']:' +
                    allocation.get('gpuUUID', '') + ':' + str(allocation['memoryBytes']) + ':' +
                    str(allocation['threads']))
        removes = [c for c in entry['dependentCosts'] if c['kind'] == 'remove' and c['from'] == state_id]
        if not removes:
            raise ValueError(f"{entry['caseID']}: exact prior remove timing unavailable")
        remove = max(removes, key=lambda c: c['measuredAt'])
        remove_seconds = max(t['durationNanos'] for t in remove['trials']) / 1e9
        mode_costs = []
        if lowest[entry['node']['name']] != entry['powerMode']:
            for start, end in ((lowest[entry['node']['name']], entry['powerMode']),
                               (entry['powerMode'], lowest[entry['node']['name']])):
                matches = [c for c in powers if c['node']['name'] == entry['node']['name'] and
                           c['from'] == start and c['to'] == end]
                if not matches:
                    raise ValueError(f"{entry['caseID']}: prior power transition timing unavailable")
                mode_costs.append(max(matches, key=lambda c: c['measuredAt']))
        mode_seconds = sum(max(t['durationNanos'] for t in c['trials']) / 1e9 for c in mode_costs)
        entry['timingEstimate'] = {
            'derivedKnownSeconds': entry['computed']['knownElapsedSeconds'],
            'priorExactRemoveSeconds': remove_seconds, 'priorExactRemoveReference': remove['reference'],
            'priorModeRoundTripSeconds': mode_seconds,
            'priorModeRoundTripReferences': [c['reference'] for c in mode_costs],
            'referenceLowestMode': lowest[entry['node']['name']],
            'estimatedSeconds': entry['computed']['knownElapsedSeconds'] + remove_seconds + mode_seconds,
            'meaning': 'Approved derived prepare+warmup+window+supply extension+drain plus observed exact-state remove and observed per-root power round trip to prior lowest. Proxy for full handback, not observed end-to-end d duration. No mode-hold shortcut between roots.'}
        entry['sameConditionRetry'] = {'plannedName': entry['crName'] + '-retry1',
            'specSourcePath': entry['crPath'], 'maximumRetries': 1,
            'creation': 'CampaignOps clones identical spec after failure classification and safe return; only metadata.name changes. Preserve original UID/status/raw and record original->retry UID mapping.'}
    representative_seconds = entries[plan['representative']['caseID']]['timingEstimate']['estimatedSeconds']
    for lane in plan['lanes']:
        cursor = representative_seconds
        timeline = []
        for case in lane['caseOrder']:
            if case == plan['representative']['caseID']:
                continue
            seconds = entries[case]['timingEstimate']['estimatedSeconds']
            timeline.append({'caseID': case, 'startOffsetSeconds': cursor, 'endOffsetSeconds': cursor + seconds})
            cursor += seconds
        lane['timingEstimate'] = {'startOffsetSeconds': representative_seconds,
            'endOffsetSeconds': cursor, 'timeline': timeline,
            'releaseDelayMeaning': 'Offsets start at D06 release; a later perNode AFTER handback shifts that lane only.'}
    makespan = max(lane['timingEstimate']['endOffsetSeconds'] for lane in plan['lanes'])
    estimate = int((makespan + 299) // 300) * 300
    plan['scheduling'].update({'estimatedHoursAfterGates': estimate / 3600,
        'twiceEstimateReportAtHours': 2 * estimate / 3600,
        'capOnlyCeilingHours': 5, 'capOnlyCeilingWithOneRetryEachHours': 10,
        'derivedPlusObservedProxyMakespanSeconds': makespan,
        'representativeEstimateSeconds': representative_seconds,
        'estimatedSecondsAfterGates': estimate, 'twiceEstimateReportAtSeconds': 2 * estimate,
        'estimateMeaning': 'Representative then parallel8 lanes; derived known components plus prior exact-state remove and prior lowest-mode round trips. Rounded upward to5min for scheduling, not changed measurement conditions. Full handback ACK, Job setup/collection and consumer readback residuals unobserved; report if2x elapsed, never substitute3600s cap for expected duration. Final G2 actual lowest/transition identity is a fresh binding.'})
    plan['timingPreparedAt'] = dt.datetime.now(dt.timezone.utc).isoformat()
    save(OUT / 'manifest.json', plan)
    print(json.dumps({'timingPreparedAt': plan['timingPreparedAt'],
        'representativeSeconds': representative_seconds, 'makespanSeconds': makespan,
        'estimateSeconds': estimate, 'twiceEstimateSeconds': 2 * estimate,
        'lanes': {lane['node']: lane['timingEstimate']['endOffsetSeconds'] for lane in plan['lanes']}}))


def check():
    plan = load(OUT / 'manifest.json')
    entries = plan['targets']
    assert len(entries) == len({e['caseID'] for e in entries}) == 25
    assert {g: sum(e['group'] == g for e in entries) for g in ('PLC', 'MNVCPU', 'MNVGPU')} == {'PLC': 12, 'MNVCPU': 8, 'MNVGPU': 5}
    assert sum(len(s['paths']) for s in plan['sequence']) == 25
    assert plan['sequence'][0]['paths'] == [plan['representative']['path']]
    assert all(e['sameConditionRetry']['plannedName'] == e['crName'] + '-retry1' for e in entries)
    assert len([case for lane in plan['lanes'] for case in lane['timingEstimate']['timeline']]) == 24
    for entry in entries:
        docs = [json.loads(s) for s in Path(entry['crPath']).read_text().split('---') if s.strip()]
        assert len(docs) == 1
        cr, d = docs[0], entry['computed']
        spec = cr['spec']
        assert spec['supplyMode'] == 'closed-loop' and spec['workers'] == d['workers']
        assert spec['minimumBatches'] == d['N'] and spec['conditions']['minimumCycles'] == d['N'] - 1
        assert spec['conditions']['profileID'] == entry['profileID']
        frozen = json.loads(spec['conditions']['inputsJSON'])
        assert frozen['coefficients'] == plan['coefficients']
        assert (d['warmupSeconds'] >= 60 and d['durationSeconds'] >= 360 and d['plannedElapsedSeconds'] == 3600)
        sizing = d['supplySizing']
        assert d['workers'] == min(sizing['workerCap'],
            sizing['queueBytesCap'] // sizing['framedDecodedRequestUpperBytes'], math.floor(
            (sizing['requestLifetimeSeconds'] - sizing['marginSeconds']) /
            (sizing['batch'] * sizing['profileMeanBatchSeconds'])))
        assert d['waitingSeconds'] + sizing['marginSeconds'] <= d['requestLifetimeSeconds']
        assert d['socketTimeoutSeconds'] > d['requestLifetimeSeconds'] + d['deliveryMarginMillis'] / 1000
        assert d['requestLifetimeSeconds'] >= entry['workload']['slo']['targetLatencyNanos'] / 1e9
        assert d['waitingBatches'] == math.ceil(d['workers'] / entry['frozenProfileInputs']['batch']) - 1
        extension = math.ceil(spec['requestLifetimeSeconds'] + spec['deliveryMarginMillis'] / 1000)
        assert d['supplyExtensionSeconds'] == extension
        assert sum(d[k] for k in ('preparationSeconds', 'warmupSeconds', 'durationSeconds', 'supplyExtensionSeconds', 'drainSeconds', 'jobSetupSeconds', 'collectionSeconds')) == d['plannedElapsedSeconds'] <= 3600
    print('25 source-bound closed-loop CRs checked locally; no cluster calls, no measurement')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output-dir', type=Path, default=OUT, help='external preparation output subtree')
    parser.add_argument('--check', action='store_true', help='read-only bounded check for Main coordinated gap')
    parser.add_argument('--refresh-timing', action='store_true', help='update timing/retry manifest fields only from already captured manifest data')
    args = parser.parse_args()
    OUT = args.output_dir.resolve()
    repository = Path(CONFIG["paths"]["repository"]).resolve()
    if repository == OUT or repository in OUT.parents:
        parser.error("preparation output must remain outside the repository source tree")
    if args.check:
        check()
    elif args.refresh_timing:
        refresh_timing()
    else:
        generate()

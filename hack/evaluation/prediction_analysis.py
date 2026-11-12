"""Join v1alpha7 decision terms with archived, meter-only power windows."""
import argparse
import datetime as dt
import gzip
import json
import math
from pathlib import Path
import subprocess

import evidence_io

from evidence_io import timestamp_ns as ns


def stamp(value):
    seconds, nanos = divmod(value, 1_000_000_000)
    return dt.datetime.fromtimestamp(seconds, dt.timezone.utc).strftime('%Y-%m-%dT%H:%M:%S') + ('.%09d' % nanos if nanos else '') + 'Z'


def identity(node):
    return node.get('name'), node.get('uid')


def number(value):
    return type(value) in (int, float) and math.isfinite(value)


def decisions(observations):
    return sorted((event['decision'] for event in observations.get('events', []) if event.get('decision')),
                  key=lambda row: ns(row['at']))


def receipts(observations):
    result = {}
    for event in observations.get('events', []):
        action = event.get('action')
        if action:
            key = (action['id'], identity(action['node']))
            # A later start record must not erase a completion receipt.
            if key not in result or action.get('completedAt'):
                result[key] = action
    return result


def selected_plans(decision):
    result = {}
    for plan in (decision.get('capacity') or {}).get('plans', []):
        terms = plan.get('idleCosts') or {}
        if any('requestWatts' in term for term in terms.values()):
            key = identity(plan['node'])
            if key not in result or plan.get('key') != 'keep':
                result[key] = plan
    return result


def windows(start, end, observations):
    """Split validity windows at observed action boundaries; clip without rounding."""
    first, last = ns(start), ns(end)
    rows, ordered = [], decisions(observations)
    actual = receipts(observations)
    leading_end = min(last, ns(ordered[0]['at'])) if ordered else last
    if first < leading_end:
        rows.append({'decisionAt': None, 'from': stamp(first), 'to': stamp(leading_end)})
    for index, decision in enumerate(ordered):
        left = max(first, ns(decision['at']))
        right = min(last, ns(ordered[index + 1]['at']) if index + 1 < len(ordered) else last)
        if left >= right:
            continue
        cuts = {left, right}
        for action in (decision.get('target') or {}).get('actions', []):
            receipt = actual.get((action['id'], identity(action['node'])), {})
            for key in ('startedAt', 'completedAt'):
                if receipt.get(key) and left < ns(receipt[key]) < right:
                    cuts.add(ns(receipt[key]))
        cuts = sorted(cuts)
        rows.extend({'decisionAt': decision['at'], 'from': stamp(a), 'to': stamp(b)} for a, b in zip(cuts, cuts[1:]))
    return rows


def measured(powers, start, end, node):
    matches = [power for power in powers if ns(power['from']) == ns(start) and ns(power['to']) == ns(end)]
    if len(matches) != 1:
        return None, 'missing/ambiguous archived meter window'
    matches = [row for row in matches[0].get('nodes', []) if identity(row['node']) == identity(node)]
    if len(matches) != 1:
        return None, 'missing/ambiguous node name+UID meter'
    row = matches[0]
    if row.get('meterComplete') is not True or row.get('policyMet') is not True or not number(row.get('joules')):
        return None, 'incomplete meter or coverage policy not met'
    return row['joules'], None


def coverage_reasons(observations, start, end):
    """Only half-open scheduler gaps overlapping this evidence interval apply."""
    reasons = [] if observations.get('complete') is True else ['scheduler observation coverage incomplete']
    first, last = ns(start), ns(end)
    for gap in observations.get('gaps', []):
        if first < ns(gap['to']) and ns(gap['from']) < last:
            reasons.append('scheduler gap [' + gap['from'] + ', ' + gap['to'] + '): ' + gap['reason'])
    return reasons


def phase(decision, plan, at, actual):
    target = decision.get('target') or {}
    if target.get('keep') or not target:
        return 'before', None
    actions = target.get('actions', [])
    routes = [a for a in actions if a['kind'] == 'route' and a.get('from') != 'ready-subset']
    if not routes:
        return None, 'transition final route receipt unavailable'
    final_route = routes[-1]
    route = actual.get((final_route['id'], identity(final_route['node'])), {})
    if not route.get('completedAt') or route.get('error'):
        return None, 'transition final route completion unavailable'
    by_id = {action['id']: action for action in actions}
    pre_ids, frontier = set(), list(final_route.get('dependsOn', []))
    while frontier:
        key = frontier.pop()
        if key not in pre_ids:
            pre_ids.add(key)
            frontier.extend(by_id.get(key, {}).get('dependsOn', []))
    node_actions = [a for a in actions if identity(a['node']) == identity(plan['node'])
                    and a['kind'] in ('prepare', 'power', 'stop', 'drain', 'remove')]
    def completed(action):
        receipt = actual.get((action['id'], identity(action['node'])), {})
        return bool(receipt.get('completedAt') and not receipt.get('error') and ns(receipt['completedAt']) <= at)
    for action in node_actions:
        receipt = actual.get((action['id'], identity(action['node'])), {})
        if not receipt.get('startedAt'):
            return None, 'node action start unavailable: ' + action['id']
        if receipt.get('startedAt') and ns(receipt['startedAt']) <= at:
            if receipt.get('error') or not receipt.get('completedAt'):
                return None, 'node action completion unavailable: ' + action['id']
            if at < ns(receipt['completedAt']):
                if action['kind'] in ('prepare', 'power', 'remove'):
                    return 'action:' + action['id'], None
                return None, 'active stop/drain loaded state/routing has no predicted phase'
    pre = [action for action in node_actions if action['id'] in pre_ids]
    post = [action for action in node_actions if action['id'] not in pre_ids]
    if at >= ns(route['completedAt']):
        if all(completed(action) for action in post):
            return 'after', None
        return None, 'post-route loaded state/routing has no predicted phase until post actions complete'
    if all(completed(action) for action in pre):
        return 'mid', None
    if not any(completed(action) for action in pre):
        return 'before', None
    return None, 'intermediate pre-route loaded state/routing has no predicted phase'


def action_windows(observations):
    return [{'from': row['startedAt'], 'to': row['completedAt']} for row in receipts(observations).values()
            if row.get('completedAt') and ns(row['startedAt']) < ns(row['completedAt'])]


def analyze(stage, start, end, observations, powers):
    actual = receipts(observations)
    ordered = decisions(observations)
    by_at = {row['at']: row for row in ordered}
    nodes, actions, acceptance = [], [], []
    for window in windows(start, end, observations):
        if window['decisionAt'] is None:
            nodes.append({'stage': stage, **window, 'node': None, 'compared': None, 'phase': None,
                          'predictedWatts': None, 'measuredWatts': None, 'errorWatts': None,
                          'predictedJoules': None, 'measuredJoules': None, 'status': 'UNKNOWN',
                          'reasons': ['last decision before stage start unavailable']})
            continue
        decision = by_at[window['decisionAt']]
        compared = {identity(node) for node in (decision.get('search', {}).get('comparedNodes') or [])}
        plans = selected_plans(decision)
        wanted = {**{identity(node): node for node in (decision.get('search', {}).get('comparedNodes') or [])},
                  **{key: plan['node'] for key, plan in plans.items()}}
        for key, node in wanted.items():
            plan = plans.get(key, {})
            used, reason = phase(decision, plan, ns(window['from']), actual) if plan else (None, 'v1alpha7 node prediction unavailable')
            idle = (plan.get('idleCosts') or {}).get(used, {})
            predicted = idle.get('watts') + idle.get('requestWatts') if idle.get('source') == 'exact' and number(idle.get('watts')) and number(idle.get('requestWatts')) else None
            operation = None
            if used and used.startswith('action:'):
                operation = next((cost for cost in plan.get('actionCosts', []) if cost['action'] == used[7:]), {})
                predicted = (operation['joules'] / operation['seconds']
                             if (operation.get('provenance') or {}).get('source') == 'exact'
                             and number(operation.get('joules')) and number(operation.get('seconds')) and operation['seconds'] > 0 else None)
            if predicted is None and reason is None:
                reason = 'exact idle/request prediction unavailable'
            joules, meter_reason = measured(powers, window['from'], window['to'], node)
            seconds = (ns(window['to']) - ns(window['from'])) / 1e9
            watts = joules / seconds if joules is not None else None
            reasons = [value for value in (reason, meter_reason) if value]
            reasons.extend(coverage_reasons(observations, window['from'], window['to']))
            nodes.append({'stage': stage, **window, 'node': node, 'compared': key in compared, 'phase': used,
                          'idle': idle or None, 'requestWatts': idle.get('requestWatts'), 'requestReferences': idle.get('requestReferences'),
                          'operation': operation,
                          'predictedWatts': predicted, 'measuredWatts': watts,
                          'errorWatts': predicted - watts if predicted is not None and watts is not None else None,
                          'predictedJoules': predicted * seconds if predicted is not None else None, 'measuredJoules': joules,
                          'status': 'UNKNOWN' if reasons else 'MEASURED', 'reasons': reasons})
    seen = set()
    for decision in ordered:
        if not any(row['decisionAt'] == decision['at'] for row in nodes):
            continue
        target = decision.get('target') or {}
        plans = selected_plans(decision)
        costs = {(cost['action'], identity(plan['node'])): cost for plan in plans.values() for cost in plan.get('actionCosts', [])}
        for action in target.get('actions', []):
            key = (action['id'], identity(action['node']))
            if key in seen:
                continue
            seen.add(key)
            cost, receipt = costs.get(key, {}), actual.get(key, {})
            seconds = (ns(receipt['completedAt']) - ns(receipt['startedAt'])) / 1e9 if receipt.get('completedAt') else None
            joules, reason = measured(powers, receipt['startedAt'], receipt['completedAt'], action['node']) if seconds and seconds > 0 else (None, 'action measurement unavailable')
            predicted_seconds = cost.get('seconds') if (cost.get('provenance') or {}).get('source') == 'exact' else None
            predicted_joules = cost.get('joules') if (cost.get('provenance') or {}).get('source') == 'exact' else None
            coverage = (coverage_reasons(observations, receipt['startedAt'], receipt['completedAt'])
                        if receipt.get('startedAt') and receipt.get('completedAt') else ['action observation interval unavailable'])
            actions.append({'stage': stage, 'decisionAt': decision['at'], 'id': action['id'], 'kind': action['kind'], 'node': action['node'],
                            'predictedSeconds': predicted_seconds, 'predictedJoules': predicted_joules,
                            'source': cost.get('provenance') or action.get('expectedSource'), 'startedAt': receipt.get('startedAt'),
                            'completedAt': receipt.get('completedAt'), 'actualSeconds': seconds, 'actualJoules': joules,
                            'coverageStatus': 'UNKNOWN' if coverage else 'COMPLETE', 'coverageReasons': coverage,
                            'errorSeconds': predicted_seconds - seconds if number(predicted_seconds) and seconds is not None and not coverage else None,
                            'errorJoules': predicted_joules - joules if predicted_joules is not None and joules is not None and not coverage else None,
                            'energyScope': 'node-wall meter over action interval', 'reason': reason, 'actionError': receipt.get('error')})
        compared_rows = [row for row in nodes if row['decisionAt'] == decision['at'] and row['compared']]
        grouped = {}
        for row in compared_rows:
            grouped.setdefault(identity(row['node']), []).append(row)
        unknown = not grouped or any(row['status'] == 'UNKNOWN' for row in compared_rows)
        error = None if unknown else sum(abs(sum(row['predictedJoules'] - row['measuredJoules'] for row in group)) for group in grouped.values())
        keep_j, target_j = decision.get('keep', {}).get('expectedJoules'), target.get('expectedJoules')
        gain = keep_j - target_j if number(keep_j) and number(target_j) else None
        transition = bool(target) and not target.get('keep', False)
        feasible = decision.get('keep', {}).get('status') == 'Feasible'
        comparison = (decision.get('capacity') or {}).get('energyComparison') or {}
        applicable = transition and feasible and comparison.get('status') == 'decidable' and gain is not None and gain > 0
        kind = 'keep' if not transition else 'slo-recovery' if decision.get('keep', {}).get('status') == 'Overload' else 'transition'
        acceptance.append({'stage': stage, 'decisionAt': decision['at'], 'kind': kind,
                           'energyComparison': comparison,
                           'missingCells': (decision.get('capacity') or {}).get('missingCells', []),
                           'from': min((r['from'] for r in compared_rows), key=ns, default=None),
                           'to': max((r['to'] for r in compared_rows), key=ns, default=None),
                           'predictedDeltaJoules': gain, 'absoluteErrorJoules': error,
                           'verdict': ('UNKNOWN' if unknown else 'PASS' if error < gain else 'FAIL') if applicable else 'NOT_APPLICABLE',
                           'reason': 'incomplete node prediction/measurement or scheduler coverage' if unknown else None})
    return {'nodes': nodes, 'actions': actions, 'acceptance': acceptance,
            'status': 'AVAILABLE' if nodes else 'UNKNOWN', 'reasons': observations.get('errors', []) or ([] if nodes else ['v1alpha7 decisions unavailable'])}


def load_observations(folder):
    """Read already-hydrated campaign pages, retaining only V1 analysis inputs."""
    paths = sorted(Path(folder).glob('*.json.gz'))
    result = {'events': [], 'complete': bool(paths), 'errors': [], 'gaps': []}
    for path in paths:
        with gzip.open(path, 'rt') as source:
            page = json.load(source)
        coverage = page.get('coverage')
        if not isinstance(coverage, dict) or not isinstance(coverage.get('gaps'), list):
            result['errors'].append(str(path) + ': scheduler coverage unavailable')
        else:
            for gap in coverage['gaps']:
                try:
                    valid = ns(gap['from']) < ns(gap['to']) and isinstance(gap['reason'], str)
                except (KeyError, TypeError, ValueError):
                    valid = False
                if not valid:
                    result['errors'].append(str(path) + ': scheduler gap interval unavailable/invalid')
                elif gap not in result['gaps']:
                    result['gaps'].append(gap)
        for event in page['events']:
            row = {'at': event['at']}
            decision = event.get('decision')
            if decision is not None:
                capacity = decision.get('capacity') or {}
                target = decision.get('target')
                row['decision'] = {
                    'at': decision['at'], 'search': {'comparedNodes': decision.get('search', {}).get('comparedNodes') or []},
                    'keep': {key: decision.get('keep', {}).get(key) for key in ('expectedJoules', 'status')},
                    'target': None if not target else {
                        **{key: target[key] for key in ('id', 'keep', 'expectedJoules') if key in target},
                        'actions': [{key: action[key] for key in ('id', 'kind', 'node', 'from', 'dependsOn') if key in action}
                                    for action in target.get('actions', [])]},
                    'capacity': {
                        **{key: capacity[key] for key in ('energyComparison', 'missingCells') if key in capacity},
                        'plans': [{key: plan[key] for key in ('node', 'key', 'idleCosts', 'actionCosts') if key in plan}
                                  for plan in selected_plans(decision).values()]}}
            if event.get('action'):
                row['action'] = {key: event['action'][key] for key in (
                    'id', 'kind', 'node', 'phase', 'startedAt', 'completedAt', 'error') if key in event['action']}
            if 'decision' in row or 'action' in row:
                result['events'].append(row)
        last_cursor = page.get('nextCursor')
    if paths and last_cursor:
        result['errors'].append('last hydrated page still has nextCursor')
    if not paths:
        result['errors'].append('no hydrated scheduler pages')
    result['complete'] = bool(paths) and not result['errors']
    return result


def load_stages(path):
    stages = [{key: row[key] for key in ('id', 'from', 'to')} for row in evidence_io.load(path)]
    if not stages or len({row['id'] for row in stages}) != len(stages):
        raise ValueError('nonempty stages with unique IDs required')
    if any(not isinstance(row['id'], str) or not row['id'] or ns(row['from']) >= ns(row['to']) for row in stages):
        raise ValueError('stage ID and positive RFC3339 interval required')
    return stages


def requested_windows(stages, observations):
    intervals, relevant = {}, set()
    by_at = {row['at']: row for row in decisions(observations)}
    for stage in stages:
        for window in windows(stage['from'], stage['to'], observations):
            if window['decisionAt'] is None:
                continue
            intervals[(ns(window['from']), ns(window['to']))] = (window['from'], window['to'])
            for action in (by_at[window['decisionAt']].get('target') or {}).get('actions', []):
                relevant.add((action['id'], identity(action['node'])))
    for key, receipt in receipts(observations).items():
        if key in relevant and key[1][1] and receipt.get('completedAt') and ns(receipt['startedAt']) < ns(receipt['completedAt']):
            intervals[(ns(receipt['startedAt']), ns(receipt['completedAt']))] = (receipt['startedAt'], receipt['completedAt'])
    return [{'id': 'w%06d' % index, 'from': interval[0], 'to': interval[1]}
            for index, (_, interval) in enumerate(sorted(intervals.items()))]


def meter_windows(response, collection):
    from archive import meter_energy, energy_policy
    return [meter_energy(row['power'], collection['powerMeasurement'], energy_policy(collection),
                         row['power']['from'], row['power']['to']) for row in response['windows']]


def offline_report(stages, observations, powers):
    return {'stages': [{**stage, 'predictionAnalysis': analyze(stage['id'], stage['from'], stage['to'], observations, powers)}
                       for stage in stages]}


def run_offline(stages, observations, collection, output, executable, raw, settings, state):
    """One local CLI invocation; never access the scheduler or power HTTP APIs."""
    output = Path(output)
    output.mkdir(parents=True, exist_ok=True)
    wanted = requested_windows(stages, observations)
    windows_path, power_path = output / 'prediction-windows.json', output / 'prediction-power.json'
    evidence_io.save(windows_path, wanted)
    if wanted:
        with power_path.open('w') as destination:
            subprocess.run([str(executable), '--dir', str(raw), '--settings', str(settings), '--state', str(state),
                            '--windows', str(windows_path)], stdout=destination, check=True)
        powers = meter_windows(evidence_io.load(power_path), collection)
    else:
        evidence_io.save(power_path, {'windows': []})
        powers = []
    return offline_report(stages, observations, powers)


def render_report(report):
    lines = ['Prediction consistency (offline v1alpha7): stage | row kind | prediction / measured / error / coverage']
    for stage in report['stages']:
        for kind in ('nodes', 'actions', 'acceptance'):
            lines.extend(stage['id'] + ' | ' + kind + ' | ' + json.dumps(row, sort_keys=True)
                         for row in stage['predictionAnalysis'][kind])
    return '\n'.join(lines) + '\n'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=('windows', 'report', 'run'))
    parser.add_argument('--scheduler', required=True, help='already-hydrated scheduler-observations directory')
    parser.add_argument('--stages', required=True, help='JSON list of {id,from,to}; UTC RFC3339Nano')
    parser.add_argument('--out', required=True, help='windows/report JSON, or output directory for run')
    parser.add_argument('--power', help='power CLI --windows output (report)')
    parser.add_argument('--binding', help='frozen evaluation binding with powerMeasurement and energyCoverage')
    parser.add_argument('--table', help='optional offline text report path')
    parser.add_argument('--power-cli', help='compiled hack/observations/power executable (run)')
    parser.add_argument('--raw', help='fetched raw obs directory (run)')
    parser.add_argument('--settings', help='accepted meter settings JSON (run)')
    parser.add_argument('--state', help='actual scheduler state JSON (run)')
    args = parser.parse_args()
    stages, observations = load_stages(args.stages), load_observations(args.scheduler)
    if args.command == 'windows':
        evidence_io.save(args.out, requested_windows(stages, observations))
        return
    if not args.binding:
        parser.error('--binding is required for report/run')
    collection = evidence_io.load_binding(args.binding)['config']['collection']
    if args.command == 'run':
        if not all((args.power_cli, args.raw, args.settings, args.state)):
            parser.error('run requires --power-cli, --raw, --settings and --state')
        report = run_offline(stages, observations, collection, args.out, args.power_cli, args.raw, args.settings, args.state)
        evidence_io.save(Path(args.out) / 'prediction-analysis.json', report)
    else:
        if not args.power:
            parser.error('report requires --power')
        report = offline_report(stages, observations, meter_windows(evidence_io.load(args.power), collection))
        evidence_io.save(args.out, report)
    if args.table:
        Path(args.table).write_text(render_report(report))


if __name__ == '__main__':
    main()

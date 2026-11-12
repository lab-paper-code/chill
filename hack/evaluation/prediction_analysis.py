"""Join v1alpha7 decision terms with archived, meter-only power windows."""
import datetime as dt
import math

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


def phase(decision, plan, at, actual):
    target = decision.get('target') or {}
    if target.get('keep') or not target:
        return 'before', None
    actions = target.get('actions', [])
    routes = [a for a in actions if a['kind'] == 'route' and a.get('from') != 'ready-subset']
    if not routes:
        return None, 'transition final route receipt unavailable'
    route = actual.get((routes[-1]['id'], identity(routes[-1]['node'])), {})
    if not route.get('completedAt') or route.get('error'):
        return None, 'transition final route completion unavailable'
    node_actions = [a for a in actions if identity(a['node']) == identity(plan['node']) and a['kind'] in ('prepare', 'power', 'remove')]
    for action in node_actions:
        receipt = actual.get((action['id'], identity(action['node'])), {})
        if not receipt.get('startedAt') or not receipt.get('completedAt') or receipt.get('error'):
            return None, 'node transition completion unavailable: ' + action['id']
        if ns(receipt['startedAt']) <= at < ns(receipt['completedAt']):
            return 'action:' + action['id'], None
    if at >= ns(route['completedAt']):
        return 'after', None
    if node_actions and at < min(ns(actual[(a['id'], identity(a['node']))]['startedAt']) for a in node_actions):
        return 'before', None
    return 'mid', None


def action_windows(observations):
    return [{'from': row['startedAt'], 'to': row['completedAt']} for row in receipts(observations).values()
            if row.get('completedAt') and ns(row['startedAt']) < ns(row['completedAt'])]


def analyze(stage, start, end, observations, powers):
    actual = receipts(observations)
    ordered = decisions(observations)
    by_at = {row['at']: row for row in ordered}
    nodes, actions, acceptance = [], [], []
    for window in windows(start, end, observations):
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
            if observations.get('complete') is not True:
                reasons.append('scheduler observation coverage incomplete')
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
            predicted_seconds = (cost.get('seconds') if (cost.get('provenance') or {}).get('source') == 'exact'
                                 else action.get('expectedSeconds') if not cost else None)
            predicted_joules = cost.get('joules') if (cost.get('provenance') or {}).get('source') == 'exact' else None
            actions.append({'stage': stage, 'decisionAt': decision['at'], 'id': action['id'], 'kind': action['kind'], 'node': action['node'],
                            'predictedSeconds': predicted_seconds, 'predictedJoules': predicted_joules,
                            'source': cost.get('provenance') or action.get('expectedSource'), 'startedAt': receipt.get('startedAt'),
                            'completedAt': receipt.get('completedAt'), 'actualSeconds': seconds, 'actualJoules': joules,
                            'errorSeconds': predicted_seconds - seconds if number(predicted_seconds) and seconds is not None else None,
                            'errorJoules': predicted_joules - joules if predicted_joules is not None and joules is not None else None,
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
        applicable = transition and gain is not None and gain > 0
        acceptance.append({'stage': stage, 'decisionAt': decision['at'], 'kind': 'transition' if transition else 'keep',
                           'from': min((r['from'] for r in compared_rows), key=ns, default=None),
                           'to': max((r['to'] for r in compared_rows), key=ns, default=None),
                           'predictedDeltaJoules': gain, 'absoluteErrorJoules': error,
                           'verdict': ('UNKNOWN' if unknown else 'PASS' if error < gain else 'FAIL') if applicable else 'NOT_APPLICABLE',
                           'reason': 'incomplete node prediction/measurement or scheduler coverage' if unknown else None})
    return {'nodes': nodes, 'actions': actions, 'acceptance': acceptance,
            'status': 'AVAILABLE' if nodes else 'UNKNOWN', 'reasons': observations.get('errors', []) or ([] if nodes else ['v1alpha7 decisions unavailable'])}

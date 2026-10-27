#!/usr/bin/env python3
"""Read-only E5 numeric tables; no build, replay, writes, or cluster mutation.

python3 hack/e5/numbers.py --snapshot scheduler-state.json
python3 hack/e5/numbers.py --url https://server --ca-file ca.pem --token-file token
python3 hack/e5/numbers.py --self-check

A snapshot may be a raw scheduler/state response, a full /scheduler response,
or a bundle with state, profiles, settings, capacity (JSON objects or relative
file paths). Optional bundle fields:
  observations: list of saved Workload ObservationResult objects/paths (cohorts
    retain completionResidualNanos, definition, from/to and UID; never pool
    definitions or overlapping observation intervals).
  selections: {canonical-name: {percentile: 95|99, targetLatencyNanos: number}}
    freezes an approved choice; absent PLC/Smol choices remain preliminary.
  poolShares: {MNV: 0.45, PLC: 0.1, Smol: 0.45}; finite [0,1], sum 1.
  witnesses: [{kind: initial|expand|shrink|swap, source: actual-plan-reference,
    before: [capacity-row-key], after: [capacity-row-key],
    chains: [{costIDs: [cost-id], removedBeforeRoute: [node-name]}],
    workloads: {canonical-name: {lambda: req/s, q: requests}},
    keepJ: number, bestJ: number, hurdleJ: number, keepFeasible: bool}]
Only actual, matching Built rows support witness calculations. chains contain
route prerequisites only, in execution order, one chain per Node; parallel
chains use their maximum duration. Energy/hurdle are the planner's signed-
evidence-cancelled values, not a sum of raw cost uncertainties.

Capacity is consumed, NOT reimplemented: Python service-time division is mu,
not capacity.go's Poisson-replay kappa. Missing rows/d/residuals stay unknown.
Final candidate replay must run separately in a measurement-free window.
"""
import sys
# Do not shadow stdlib numbers (e.g. decimal/error hooks) when run as a script.
if __name__ == '__main__':
    sys.path = [p for p in sys.path if p != __import__('os').path.dirname(__file__)]
import argparse
from collections import defaultdict
from datetime import datetime, timezone
import importlib.util
import json
import math
from pathlib import Path
import ssl
import time
import urllib.request

# Reuse the existing evidence-file reader and empirical nearest-rank summary.
_spec = importlib.util.spec_from_file_location('e5_analysis', Path(__file__).parent / 'd2/analyze_before.py')
analysis = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analysis)
NODES = ('genesis2', 'genesis3', 'lattepanda', 'rasp5', 'orangepi', 'jetsono', 'jetsonx', 'jetson-nano')
MODELS = {'image-classification-tight-slo': 'mobilenet-v2-050',
          'plc-anomaly': 'plc-lstm-ae', 'smolvlm2-vqa': 'smolvlm2-500m-video-instruct'}
SETTINGS = ('completionMarginMS', 'capacityWindowNanos', 'capacityWindows',
            'capacityPassingWindows', 'capacityMinimumRequests')
UNKNOWN = '미측정'


def numeric(value, minimum=None):
    if type(value) not in (int, float) or not math.isfinite(value) or (minimum is not None and value < minimum):
        raise ValueError('Invalid finite number: ' + repr(value))
    return value


def fmt(value):
    return UNKNOWN if value is None else ('%.6g' % value if isinstance(value, (int, float)) else str(value).replace('|', '\\|').replace('\n', ' '))


def table(title, headings, rows):
    print('\n## ' + title + '\n')
    print('| ' + ' | '.join(headings) + ' |')
    print('|' + '|'.join('---' for _ in headings) + '|')
    for row in rows:
        print('| ' + ' | '.join(fmt(v) for v in row) + ' |')


def quantile(values, percentile):
    # stats is the shared empirical nearest-rank implementation (no interpolation).
    return analysis.stats(values)['p' + str(percentile)]


def widest(values):
    """Largest absolute adjacent gap; ties choose the lower target deterministically."""
    ordered = sorted(values.items(), key=lambda x: (x[1], x[0]))
    if len(ordered) != len(NODES):
        return None
    # At least five positive Nodes (strict majority); leave slowest two out.
    candidates = [(ordered[i + 1][1] - ordered[i][1], i) for i in range(4, len(ordered) - 2)]
    gap, i = max(candidates, key=lambda x: (x[0], -x[1]))
    return {'low': ordered[i][1], 'high': ordered[i + 1][1],
            'target': (ordered[i][1] + ordered[i + 1][1]) / 2,
            'fast': [n for n, _ in ordered[:i + 1]],
            'slow': [n for n, _ in ordered[-2:]], 'gap': gap}


def waiting(q, rate, retained, new, route, slo, margin):
    for value in (q, rate, retained, new, route, slo, margin):
        numeric(value, 0)
    backlog = q + max(0, rate - retained) * route
    wait = backlog / new if new else (math.inf if backlog else 0)
    return backlog, wait, new >= rate and wait <= slo - margin


def read_inputs(args):
    reader = analysis.Inputs(Path('.'))
    if args.snapshot:
        path = Path(args.snapshot)
        data = reader.json(path)
        if data is None:
            raise ValueError(str(reader.errors))
        base = path.parent
        def resolve(value):
            if isinstance(value, str):
                result = reader.json(base / value)
                if result is None:
                    raise ValueError(str(reader.errors))
                return result
            return value
        if not isinstance(data, dict):
            raise ValueError('Snapshot must be a JSON object')
        for key in ('state', 'profiles', 'settings', 'capacity'):
            if key in data:
                data[key] = resolve(data[key])
        data['observations'] = [resolve(x) for x in data.get('observations', [])]
        sources = [str(path.resolve())] + list(reader.sources)[1:]
    else:
        if not args.url or not args.ca_file or not args.token_file:
            raise ValueError('Use --snapshot or --url with --ca-file and --token-file')
        if not args.url.startswith('https://'):
            raise ValueError('Authenticated GET requires https:// with a verified CA')
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *unused):
                return None
        opener = urllib.request.build_opener(NoRedirect(), urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=args.ca_file)))
        token = Path(args.token_file).read_text().strip()
        data, sources = {}, []
        for key in ('profiles', 'state', 'settings', 'capacity'):
            url = args.url.rstrip('/') + '/api/v1/scheduler/' + key
            request = urllib.request.Request(url, headers={'Authorization': 'Bearer ' + token}, method='GET')
            with opener.open(request, timeout=30) as response:
                data[key] = json.load(response)
            sources.append(url + ' @ ' + datetime.now(timezone.utc).isoformat())
    state = data.get('state', data)
    snapshot = state.get('snapshot', state)
    if not isinstance(snapshot.get('options'), list):
        raise ValueError('snapshot.options must be present (empty list is allowed)')
    settings = data.get('settings') or state.get('decision', {}).get('settings') or {}
    profiles = data.get('profiles') or state.get('profiles') or {}
    if 'profiles' in profiles:
        profiles = profiles['profiles']
    capacity = data.get('capacity') or []
    if isinstance(capacity, dict):
        capacity = capacity.get('rows', [])
    if not isinstance(capacity, list):
        raise ValueError('capacity must be rows or {rows: [...]}')
    return data, state, snapshot, settings, profiles, capacity, sources


def residuals(data, workloads, selected):
    results, evidence = {}, []
    for name in MODELS:
        uid = workloads.get(name, {}).get('uid')
        cohorts, intervals = defaultdict(list), []
        for obs in data.get('observations', []):
            if obs.get('uid') != uid or not uid:
                continue
            if not obs.get('from') or not obs.get('to'):
                raise ValueError('Observation requires captured from/to')
            start, end = analysis.epoch(obs['from']), analysis.epoch(obs['to'])
            if start is None or end is None or start >= end:
                raise ValueError('Invalid observation interval')
            if any(start < b and a < end for a, b in intervals):
                raise ValueError('Overlapping residual windows for ' + name)
            intervals.append((start, end))
            for cohort in obs.get('cohorts', []):
                definition = cohort['definition']
                expected = '/'.join(str(x) for x in (uid, workloads[name]['generation'], workloads[name]['model']['uid'], workloads[name]['model']['generation'], workloads[name]['preprocessor']['uid'], workloads[name]['preprocessor']['generation']))
                if definition != expected:
                    continue
                values = cohort.get('completionResidualNanos') or []
                cohorts[definition].extend(numeric(v) for v in values)
                evidence.append((name, definition, obs['from'] + '..' + obs['to'], len(values)))
        values = next(iter(cohorts.values()), [])
        estimate = quantile(values, selected[name]['percentile']) / 1e6 if values else None
        results[name] = (max(0, estimate) if estimate is not None else None, len(values))
    return results, evidence


def report(args):
    began = time.monotonic()
    data, state, snap, settings, profiles, rows, sources = read_inputs(args)
    shares = data.get('poolShares', {'MNV': .45, 'PLC': .1, 'Smol': .45})
    if not isinstance(shares, dict) or set(shares) != {'MNV', 'PLC', 'Smol'} or any(type(v) not in (int, float) or not math.isfinite(v) or not 0 <= v <= 1 for v in shares.values()) or not math.isclose(sum(shares.values()), 1, rel_tol=0, abs_tol=1e-9):
        raise ValueError('poolShares requires finite MNV/PLC/Smol shares in [0,1] summing to 1')
    share_by_name = {'image-classification-tight-slo': shares['MNV'], 'plc-anomaly': shares['PLC'], 'smolvlm2-vqa': shares['Smol']}
    workloads = {w['name']: w for w in snap.get('workloads', [])}
    options = defaultdict(list)
    profile_rows, d_rows, cost_rows = [], [], []
    for option in snap['options']:
        instance = option['instance']
        profile = instance['profile']
        if '$ref' in profile:
            profile = profiles[profile['$ref']]
        condition = profile['condition']
        node = instance['node']['name']
        if node not in NODES or condition['logicalModel'] not in MODELS.values():
            continue
        batch = next((b for b in profile['batches'] if b['batch'] == 1), None)
        if not batch or not batch['durationNanos']:
            continue
        samples = [numeric(x, 0) / 1e6 for x in batch['durationNanos']]
        ds = [d for d in snap.get('dispatchOccupancy', []) if d['node'] == instance['node'] and all(d.get(k) == condition.get(v) for k, v in [('powerMode', 'powerMode'), ('runtimeImage', 'runtimeImage'), ('model', 'logicalModel')])]
        if len(ds) > 1:
            raise ValueError('Ambiguous consumed d identity: ' + profile['id'])
        occupancy = ds[0] if ds else None
        record = {'option': option, 'instance': instance, 'profile': profile, 'd': occupancy,
                  'p95': quantile(samples, 95), 'p99': quantile(samples, 99)}
        options[condition['logicalModel']].append(record)
        profile_rows.append((node, condition['logicalModel'], condition['powerMode'], condition['backend'], record['p95'], record['p99'], len(samples), profile['id'], profile.get('measuredAt')))
        d_rows.append((node, condition['logicalModel'], condition['backend'], condition['powerMode'], occupancy['perRequestNanos'] / 1e6 if occupancy else None, occupancy.get('source') if occupancy else UNKNOWN))
        # Descriptive cost availability only; exact resource-state matching remains
        # the planner's job, not a weaker profile-prefix eligibility gate here.
        costs = [c for c in snap.get('costs', []) if c['node'] == instance['node'] and c.get('powerMode') == condition['powerMode'] and any(str(c.get(k, '')).startswith(profile['id'] + ':') for k in ('from', 'to'))]
        maximum = lambda kind: max((numeric(t['durationNanos'], 0) / 1e9 for c in costs if c['kind'] == kind for t in c['trials']), default=None)
        cost_rows.append((node, profile['id'], maximum('prepare'), maximum('remove')))
    print('# E5 숫자 — 읽기 전용 부분 결과\n')
    print('입력: ' + '; '.join(sources))
    print('snapshot.at: ' + str(snap.get('at', UNKNOWN)))
    print('μ/프로파일 분위수를 κ로 대신하지 않음. 소비 d는 Accepted projection이며 새 규칙 인증 수와 다름. 누락은 0이 아님.')
    table('프로파일 b1 p95/p99 (ms)', ('Node', '모델', '모드', 'backend', 'p95', 'p99', 'n', 'profile ID', 'measuredAt'), profile_rows)
    table('소비 d (ms/request; legacy/새 규칙 구분은 원시 record 확인 필요)', ('Node', '모델', 'backend', '모드', 'max(d,0)', 'evidence-store key'), d_rows)
    gaps, selected, candidate_rows, fastest_rows = {}, {}, [], []
    current_m = settings.get('completionMarginMS')
    for name, model in MODELS.items():
        for percentile in (95, 99):
            fastest = {}
            for node in NODES:
                records = [r for r in options[model] if r['instance']['node']['name'] == node]
                if not records:
                    continue
                best = min(records, key=lambda r: (r['p' + str(percentile)], r['profile']['id']))
                fastest[node] = best['p' + str(percentile)]
                fastest_rows.append((name, node, percentile, fastest[node], best['d']['perRequestNanos'] / 1e6 if best['d'] else None, best['profile']['id']))
            gap = widest(fastest)
            gaps[name, percentile] = gap
            if name == 'image-classification-tight-slo':
                continue  # Canonical MNV stays p95 1s, never a new SLO proposal.
            target = gap['target'] + current_m if gap and current_m is not None else None
            candidate_rows.append((name, percentile, gap['low'] if gap else None, gap['high'] if gap else None, target, ', '.join(gap['slow']) if gap else UNKNOWN, '[추정] profile-only 중점+현재 m; d/최종 m/κ 검증 전'))
            # Also show the d-adjusted screen when every Node has at least one
            # exactly matched consumed d. Never turn absent d into zero.
            adjusted = {node: min((r['p' + str(percentile)] + r['d']['perRequestNanos'] / 1e6 for r in options[model] if r['instance']['node']['name'] == node and r['d']), default=None) for node in NODES}
            adjusted_gap = widest({n: v for n, v in adjusted.items() if v is not None})
            if adjusted_gap:
                candidate_rows.append((name, percentile, adjusted_gap['low'], adjusted_gap['high'], adjusted_gap['target'] + current_m if current_m is not None else None, ', '.join(adjusted_gap['slow']), '[추정] b1+d 중점+현재 m; legacy 구분/최종 m/κ 검증 전'))
        if name == 'image-classification-tight-slo':
            choice = {'percentile': 95, 'targetLatencyNanos': 1000000000}
        else:
            # No automatic percentile approval: keep both alternatives visible.
            choice = data.get('selections', {}).get(name)
            if choice is None:
                choice = {'percentile': 95, 'targetLatencyNanos': None}
        if choice['percentile'] not in (95, 99):
            raise ValueError('E5 percentile must be 95 or 99')
        if choice['targetLatencyNanos'] is not None:
            numeric(choice['targetLatencyNanos'], 1)
        selected[name] = choice
    table('Node별 가장 빠른 b1 option (ms; percentile별 선택)', ('Workload', 'Node', 'percentile', 'b1', '소비 d', 'profile ID'), fastest_rows)
    table('widest interval SLO 후보 (ms)', ('Workload', 'percentile', '간격 아래', '간격 위', '목표 후보', '느린 두 Node', '상태'), candidate_rows)
    margins, margin_evidence = residuals(data, workloads, selected)
    final_m = max(v[0] for v in margins.values()) if all(v[0] is not None for v in margins.values()) else None
    table('완료 여유 m (ms)', ('Workload', '선택 percentile', 'residual n', '추정값', '비고'), [(n, selected[n]['percentile'] if selected[n]['targetLatencyNanos'] else '미선택(p95/p99 비교 필요)', margins[n][1], margins[n][0] if selected[n]['targetLatencyNanos'] else None, '현재 설정=' + fmt(current_m)) for n in MODELS])
    if any(selected[n]['targetLatencyNanos'] is None for n in MODELS):
        final_m = None
    print('\n단일 m=max(Workload별 선택 분위수): ' + fmt(final_m) + ' ms. 현재 설정값은 최종 m을 대신하지 않음.')
    if margin_evidence:
        table('residual 출처', ('Workload', '불변 정의', '관측 구간', 'n'), margin_evidence)
    accepted, capacities, capacity_rows = {}, {}, []
    for name, model in MODELS.items():
        uid, choice = workloads.get(name, {}).get('uid'), selected[name]
        candidates = []
        for row in rows:
            inp = row.get('input', {})
            if not uid or inp.get('workloadUID') != uid or inp.get('slo') != choice or row.get('status') != 'Built' or row.get('previous'):
                continue
            if any(inp.get(k) != settings.get(k) for k in SETTINGS) or inp.get('completionMarginMS') is None:
                continue
            for record in options[model]:
                i, p = record['instance'], record['profile']
                if uid not in (record['option'].get('compatibleUIDs') or []):
                    continue
                if all(inp.get(k) == v for k, v in {'node': i['node'], 'profileID': p['id'], 'powerMode': p['condition']['powerMode'], 'allocation': i['allocation'], 'dispatchOccupancy': record['d']}.items()) and inp.get('maxBatch', 0) <= i['maxBatch'] and any(b['batch'] == inp.get('maxBatch') for b in p['batches']) and record['d']:
                    numeric(row['kappa'], 0)
                    candidates.append(row)
                    accepted[row['key']] = row
                    break
        per_node = {}
        for node in NODES:
            node_rows = [r for r in candidates if r['input']['node']['name'] == node]
            per_node[node] = max((r['kappa'] for r in node_rows), default=None)
            capacity_rows.append((name, node, per_node[node], ' / '.join(r['key'] for r in node_rows) or UNKNOWN, sum(r.get('elapsedNanos', 0) for r in node_rows) / 1e9 if node_rows else None))
        capacities[name] = per_node
    table('κ_w / Node (req/s; 동일 입력 Built 행만, 물리/자원 적격성 별도)', ('Workload', 'Node', '최대 κ', 'row keys', '행 계산시간 합 s'), capacity_rows)
    rate_rows = []
    for name, values in capacities.items():
        total = sum(values.values()) if all(v is not None for v in values.values()) else None
        gap = gaps[name, selected[name]['percentile']]
        positives = sum(v > 0 for v in values.values() if v is not None)
        zeros = all(values[n] == 0 for n in gap['slow']) if gap else False
        rule = (str(positives) + '/8 positive; slowest2 κ0=' + str(zeros) + '; pass=' + str(positives >= 5 and zeros)) if total is not None else UNKNOWN
        if name == 'image-classification-tight-slo':
            rule = 'canonical p95 1s 유지 (PLC/Smol 선별 규칙 대상 아님)'
        share = share_by_name[name]
        rate_rows.append((name, selected[name]['percentile'] if selected[name]['targetLatencyNanos'] else '미선택', selected[name]['targetLatencyNanos'] / 1e6 if selected[name]['targetLatencyNanos'] else None, total, share, *(alpha * share * total if total is not None else None for alpha in (.3, .5, .7, .9, 1.2)), rule))
    table('R 및 POOL 단계률 (req/s; rate_w=α×s_w×R_w; 고립 R 동시 사용 불가)', ('Workload', 'percentile', 'SLO ms (L은 이 이상)', 'R 후보', 's_w', 'α=.3', 'α=.5', 'α=.7', 'α=.9', 'α=1.2', '과반/느린2 확인'), rate_rows)
    known_rows = []
    for name, values in capacities.items():
        known = [v for v in values.values() if v is not None]
        partial = sum(known) if known else None
        known_rows.append((name, len(known), partial, share_by_name[name], *(alpha * share_by_name[name] * partial if partial is not None else None for alpha in (.3, .5, .7, .9, 1.2))))
    table('K 진단 부분합 단계률 (미확정 R 대체 불가; SLO 변경 후 하한 보장 없음)', ('Workload', 'known Nodes/8', 'K', 's_w', 'α=.3', 'α=.5', 'α=.7', 'α=.9', 'α=1.2'), known_rows)
    print('\nR 후보도 자원/모드 적격성 및 최종 m 재생을 통과해야 확정. CapacityRejected/NonMonotone/Pending/누락은 κ0으로 채우지 않음.')
    table('비용 연결 현황 (s; profile 연결만, exact-state 인증 아님)', ('Node', 'profile ID', 'prepare 최대', 'remove 최대'), cost_rows)
    cold_rows = []
    for name, model in MODELS.items():
        ids = {r['profile']['id'] for r in options[model]}
        for node, profile_id, prepare, _ in cost_rows:
            if profile_id in ids and prepare and current_m is not None:
                target = selected[name]['targetLatencyNanos']
                if target is None:
                    gap = gaps[name, 95]
                    target = (gap['target'] + current_m) * 1e6 if gap else None
                if target is not None:
                    budget = target / 1e9 - current_m / 1000
                    cold_rows.append((name, node, profile_id, prepare, budget, budget / prepare, .3 * prepare))
    table('[추정] 빈 Node 준비만의 시나리오 (q=0, κ_ret=0, 현재 m; 실제 witness 아님)', ('Workload', 'Node', 'profile ID', 'prepare 최대 s', 'SLO−m s', 'λ/κ_new 허용 상한', 'λ=0.3κ_new의 W s'), cold_rows)
    witness_rows, energy_rows = [], []
    costs = {c['id']: c for c in snap.get('costs', [])}
    for witness in data.get('witnesses', []):
        if witness['kind'] not in ('initial', 'expand', 'shrink', 'swap') or not witness.get('source'):
            raise ValueError('Witness needs an actual source and known kind')
        energy_rows.append((witness['kind'], numeric(witness['keepJ']), numeric(witness['bestJ']), numeric(witness['hurdleJ'], 0), numeric(witness['keepJ']) - numeric(witness['bestJ']), witness['keepFeasible'], witness['source']))
        before, after = ([accepted[k] for k in witness[field]] for field in ('before', 'after'))
        for config in (before, after):
            nodes = [r['input']['node']['name'] for r in config]
            if len(nodes) != len(set(nodes)):
                raise ValueError('Witness violates one physical instance per Node')
        removed, durations = set(), []
        chain_nodes = set()
        for chain in witness['chains']:
            chain_costs = [costs[k] for k in chain['costIDs']]
            nodes = {c['node']['name'] for c in chain_costs}
            if len(nodes) > 1 or nodes & chain_nodes:
                raise ValueError('Witness chains must partition Nodes')
            chain_nodes.update(nodes)
            removed.update(chain['removedBeforeRoute'])
            durations.append(sum(max(numeric(t['durationNanos'], 0) for t in c['trials']) / 1e9 for c in chain_costs))
        route = max(durations, default=0)
        before_modes = {r['input']['node']['name']: r['input']['powerMode'] for r in before}
        changed = {r['input']['node']['name'] for r in after if r['input']['node']['name'] in before_modes and r['input']['powerMode'] != before_modes[r['input']['node']['name']]}
        if not changed <= removed or not removed <= set(before_modes):
            raise ValueError('Power changes must remove existing instance before route')
        for name in MODELS:
            uid = workloads[name]['uid']
            new_rows = [r for r in after if r['input']['workloadUID'] == uid]
            new = sum(r['kappa'] for r in new_rows)
            retained = sum(r['kappa'] for r in before if r['input']['workloadUID'] == uid and r['input']['node']['name'] not in removed)
            demand = witness['workloads'][name]
            slo = selected[name]['targetLatencyNanos']
            if final_m is None or slo is None:
                witness_rows.append((witness['kind'], name, demand['lambda'], demand['q'], retained, new, route, None, None, None, '미측정: 최종 SLO/m', witness['source']))
                continue
            backlog, wait, feasible = waiting(demand['q'], demand['lambda'], retained, new, route, slo / 1e9, final_m / 1e3)
            feasible = feasible and bool(new_rows)
            energy = ('회복 경로' if not witness['keepFeasible'] else ('gain>hurdle' if numeric(witness['keepJ']) - numeric(witness['bestJ']) > numeric(witness['hurdleJ'], 0) else 'KEEP (gain<=hurdle)'))
            witness_rows.append((witness['kind'], name, demand['lambda'], demand['q'], retained, new, route, backlog, wait, slo / 1e9 - final_m / 1e3, str(feasible) + '; ' + energy, witness['source']))
    for kind in ('initial', 'expand', 'shrink', 'swap'):
        if not any(w['kind'] == kind for w in data.get('witnesses', [])):
            witness_rows.append((kind, '전체 3 Workload', None, None, None, None, None, None, None, None, '미측정: 실제 후보/action chain/부하/큐 필요', UNKNOWN))
    table('전환 B=q+max(0,λ−κ_ret)r; W=B/κ_new≤SLO−m', ('전환', 'Workload', 'λ', 'q', 'κ_ret', 'κ_new', 'r s', 'B', 'W s', 'SLO−m s', '판정 (물리/에너지 검증 별도)', '출처'), witness_rows)
    if energy_rows:
        table('실제 planner 에너지/문턱 (J; signed evidence 상쇄 후)', ('전환', 'keep', 'best', '문턱 B', 'gain', 'keep feasible', '출처'), energy_rows)
    print('\n초기 λ=0/q=0의 W=0은 유입 이후 성공 증명이 아님. 증설/축소/교체의 자연 선택은 모든 Workload의 feasibility와 keep/best J, signed B 문턱까지 필요.')
    print('G: 고정 배치 혼합 계단 α=.3/.5/.7/.9/1.2 각120s 별도 실측; rate_w=α×s_w×R_w. 초과 시 자동 clip하지 않음.')
    print('실패 분모: 전환 중 수신 + 전환 밖 수신 + 구간 불명 = 전체 유효 offered 요청. 미완료/만료/실패를 제외하지 않음.')
    print('\n표 계산시간(파일/API 읽기 포함): %.3f s; κ 재생 실행 0회.' % (time.monotonic() - began))


def self_check():
    assert quantile([1, 2, 3, 4, 5], 95) == 5
    gap = widest(dict(zip(NODES, [1, 2, 3, 4, 5, 20, 100, 101])))
    assert gap['target'] == 60 and gap['slow'] == list(NODES[-2:])
    assert widest(dict(zip(NODES[:7], range(7)))) is None
    assert waiting(2, 10, 8, 20, 3, 1, .1) == (8, .4, True)
    assert waiting(0, 10, 0, 20, 3, 1, .1)[2] is False
    assert waiting(0, 0, 0, 0, 0, 1, .1) == (0, 0, True)
    assert math.isinf(waiting(1, 0, 0, 0, 0, 1, .1)[1])
    print('self-check: PASS')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--snapshot')
    parser.add_argument('--url')
    parser.add_argument('--ca-file')
    parser.add_argument('--token-file')
    parser.add_argument('--self-check', action='store_true')
    args = parser.parse_args()
    try:
        self_check() if args.self_check else report(args)
    except (OSError, ValueError, KeyError, TypeError) as exc:
        print('numbers: ' + str(exc), file=sys.stderr)
        sys.exit(2)

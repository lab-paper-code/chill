"""Render existing E5 summaries without changing judgments or canonical documents."""
import json
from pathlib import Path


def load(path):
    with path.open() as stream:
        return json.load(stream)


def table_lines(path, header):
    lines = path.read_text().splitlines()
    start = next(i for i, line in enumerate(lines) if line.startswith(header))
    stop = start
    while stop < len(lines) and lines[stop].startswith('|'):
        stop += 1
    return lines[start:stop]


def cell(value):
    return str(value).replace('|', '&#124;').replace('\n', ' ')


def number(value):
    return 'UNKNOWN' if value is None else f'{value:,.3f}'


def result_counts(metric):
    """Disjoint planned population; completed-only quantiles are not this denominator."""
    offered, outcomes = metric.get('offered'), metric.get('outcomes')
    if offered is None or outcomes is None:
        return 'UNKNOWN'
    latency = metric.get('gatewayCompletedLatencyNS') or metric.get('completedLatencyNS') or {}
    completed = latency.get('samples', outcomes.get('completed', 0))
    failed = sum(outcomes.get(key, 0) for key in ('error', 'timeout', 'preprocess_failed', 'delivery_failed'))
    rejected = outcomes.get('rejected', 0)
    canceled = outcomes.get('canceled', 0)
    unfinished = max(0, offered - completed - failed - rejected - canceled)
    errors = (metric.get('offeredResponsePopulation') or {}).get('errors', {})
    top = sorted(errors.items(), key=lambda item:(-item[1], item[0]))[:3]
    detail = '; '.join(f'{key}={count:,}' for key, count in top)
    if not detail:
        detail = '; '.join(f'{key}={outcomes[key]:,}' for key in ('error', 'timeout', 'preprocess_failed', 'delivery_failed') if outcomes.get(key))
    return (f'계획 {offered:,}; 완료 {completed:,} / 실패 {failed:,}'
            + (f' ({detail})' if detail else '') +
            f' / 거절 {rejected:,} / 취소 {canceled:,} / 미완료·불명 {unfinished:,}')


def energy_text(power):
    power = power or {}
    known = power.get('complete') is True and power.get('energyStatus', 'MEASURED') == 'MEASURED'
    whole, active = (power.get('wholePoolJoules'), power.get('activePoolJoules')) if known else (None, None)
    bound = power.get('gapErrorBoundJoules') if known else None
    coverage = power.get('energyCoverage') or {}
    fraction = coverage.get('maximumNodeGapFraction')
    return (f'{number(whole)} / {number(active)} J; ±{number(bound)} J; '
            f'gapΣNode={number(coverage.get("nodeGapSecondsSum"))}s, '
            f'max={number(coverage.get("maximumGapSeconds"))}s, '
            f'maxNode={number(fraction * 100 if fraction is not None else None)}%')


def paired_power(pair, mode):
    prefix = 'full' if mode == 'full' else 'static'
    error = pair.get('energyGapError') or {}
    whole, active = pair.get(prefix+'WholePOOLJ'), pair.get(prefix+'ActivePOOLJ')
    return {'complete':whole is not None and active is not None,
            'wholePoolJoules':whole, 'activePoolJoules':active,
            'gapErrorBoundJoules':error.get(prefix+'BoundJoules'),
            'energyCoverage':error.get(prefix+'Coverage')}


def render(root, repo):
    root = root.resolve()
    if not (root / 'binding.json').exists() and (root / 'run/binding.json').exists():
        root = root / 'run'
    binding = load(root / 'binding.json')
    operational = binding.get('schema') == 'demo-e5-binding.v1'
    summary = {} if operational else load(root / 'e5-summary.json')
    spec = table_lines(repo / 'docs/spec/README.md', '| 시나리오 | 관찰할 내용 |')
    # workload-pools is a design document in this repository, not a spec file.
    pools = table_lines(repo / 'docs/design/serving/workload-pools.md', '| 단계 | 시간 | 요청률')
    scenarios = ['steady', 'gradual-up-and-down', 'burst', 'post-transition-drop-resurge',
                 'mixed-heterogeneous', 'pool-overload', 'node-serving-control-failure']
    title = f'# E5 table rows: {cell(root.name)}'
    lines = [title, '', 'PRIVATE_OPERATIONAL_ONLY; spec/design files are not modified.',
             'Existing summary judgments only; MEASURED is a result link, not criterion PASS or complete scenario coverage. Missing values are not zero.',
             'Operational G0/demo evidence does not complete an E5 evaluation row.' if operational else 'Source: e5-summary.json (existing summarize output).', '', '## docs/spec/README.md', '', *spec[:2]]
    for row, key in zip(spec[2:], scenarios):
        columns = row.split('|')[1:-1]
        status = summary.get('specScenarios', {}).get(key, 'UNMEASURED')
        columns[-1] = f' {status}; Run ID `{cell(root.parent.name if root.name == "run" else root.name)}`; raw `{cell(root)}`; summary `{cell(root / ("summary.json" if operational else "e5-summary.json"))}` '
        lines.append('|' + '|'.join(columns) + '|')
    lines += ['', 'Scope: E5-3 covers increases, not gradual decreases; E5-5 is an abrupt drop/resurge. '
              'The burst is Smol-only. One paired seed set is not independent repetition. '
              'membership-off/replicas-off do not isolate every control contribution.', '']
    lines += ['', '## workload-pools E5', '', *pools[:2]]
    for row in pools[2:]:
        columns = row.split('|')[1:-1]
        stage = columns[0].strip().split()[0]
        status = summary.get('planRows', {}).get(stage, 'UNMEASURED')
        columns[-1] = f' {status}; Run ID `{cell(root.parent.name if root.name == "run" else root.name)}`; raw `{cell(root)}` '
        lines.append('|' + '|'.join(columns) + '|')
    results_at = len(lines)
    result_rows = []
    slos = {workload['name']:workload.get('slo', {}) for workload in binding.get('config', {}).get('workloads', [])}
    pairs = {pair['row']:pair for pair in summary.get('pairedComparisons', [])}
    lines += ['', '## Frozen evidence scope and conditions', '',
              '```json', json.dumps({
                  'excludedNodes': (binding.get('schedulerSettings') or {}).get('excludedNodes'),
                  'conditions': summary.get('conditions'),
                  'scenarioLimitations': summary.get('scenarioLimitations'),
                  'limitations': summary.get('limitations', binding.get('limitations')),
                  'energyBelowResolution': summary.get('energyBelowResolution'),
                  'measurementMeaning': summary.get('measurementMeaning'),
              }, ensure_ascii=False, indent=2), '```',
              'energyBelowResolution marks an explicit below-resolution 0 J/item, not exact zero energy '
              'or measured whole-POOL savings. Missing report metadata remains null.', '']
    if 'genesis2' in (binding.get('schedulerSettings') or {}).get('excludedNodes', []):
        lines += ['genesis2 하드웨어 결함으로 제외 (excluded for hardware failure); seven serving Nodes only.', '']
    if not operational:
        lines += ['', '## E5-7 manual failure evidence', '',
                  'The recommended 180/360/600s operator runbook is separate from automatic workload replay. Failed commands and absent recovery are observations. Missing operator evidence leaves the failure scenario UNMEASURED; it is not an extra SLO/energy validity gate.',
                  '```json', json.dumps(summary.get('failureScenarioEvidence'), ensure_ascii=False, indent=2), '```', '']
    lines += ['', '## Three timing populations (existing summary values)', '',
              'Generator: scheduled arrival → actual send (ns); entrance: actual send → gateway receivedAt (ns, cross-host clock uncertainty, negative values retained); SLO: CHILL receipt → response via gateway elapsedNanos. Offered includes unsent requests. Completed latency quantiles do not replace the offered denominator.', '']
    paths = sorted(root.glob('smoke-*/summary.json')) if operational else sorted(root.glob('*/judgment.json'))
    for path in paths:
        stage = load(path)
        if not operational:
            mode, marker, suffix = path.parent.name.partition('-E5-')
            row_id = 'E5-'+suffix if marker else path.parent.name
            pair = pairs.get(row_id, {})
            chill = paired_power(pair, 'full') if mode == 'static' else stage.get('power')
            static = stage.get('power') if mode == 'static' else paired_power(pair, 'static')
            paired = row_id in {'E5-'+str(index) for index in range(1,7)}
            static_text = energy_text(static) if paired else 'N/A (E5-S 대응 단계 없음)'
            difference = pair.get('staticMinusFullWholePOOLJ')
            difference_bound = (pair.get('energyGapError') or {}).get('differenceBoundJoules')
            if mode not in ('full', 'static') and paired:
                own_j, static_j = (chill or {}).get('wholePoolJoules'), static.get('wholePoolJoules')
                own_b, static_b = (chill or {}).get('gapErrorBoundJoules'), static.get('gapErrorBoundJoules')
                difference = static_j-own_j if own_j is not None and static_j is not None else None
                difference_bound = own_b+static_b if own_b is not None and static_b is not None else None
            difference_text = f'{number(difference)} ±{number(difference_bound)} J' if paired else 'N/A'
        lines += [f'### {cell(path.parent.name)}', '', f'Source: `{cell(path)}`; status: {cell(stage.get("status", "UNKNOWN"))}', '']
        if 'judgmentCriteria' in stage:
            lines += ['Judgment criteria: ' + ', '.join(stage['judgmentCriteria']) + '.', '']
        if not operational:
            energy={}
            for window in ('power','powerDrainInclusive'):
                power=stage.get(window) or {}
                known=power.get('energyStatus','MEASURED' if power.get('complete') is True else 'UNKNOWN')=='MEASURED'
                energy[window]={'energyStatus':'MEASURED' if known else 'UNKNOWN','complete':power.get('complete'),
                    'wholePoolJoules':power.get('wholePoolJoules') if known else None,
                    'activePoolJoules':power.get('activePoolJoules') if known else None,
                    'reasons':power.get('reasons'),'nodes':power.get('nodes'),
                    'energyCoverage':power.get('energyCoverage'),'gapErrorBoundJoules':power.get('gapErrorBoundJoules'),
                    'strictReasons':power.get('strictReasons')}
            lines += ['Energy and coverage (incomplete-window energy is UNKNOWN, not zero):', '',
                      '```json', json.dumps(energy,ensure_ascii=False,indent=2), '```', '']
        for name, metric in stage.get('workloads', stage.get('requests', {})).items():
            fidelity = metric.get('issuanceFidelity') or {}
            selected = {'offered': metric.get('offered'), 'generatorQuality': fidelity.get('generatorQuality'),
                        'sendLatenessNS': fidelity.get('sendLatenessNS'), 'entranceDelayNS': metric.get('entranceDelayNS'),
                        'receiptToResponse': metric.get('gatewayCompletedLatencyNS', metric.get('latencyMS')),
                        'receiptToResponseUnit': 'ms' if operational else 'ns',
                        'SLOVerdict': metric.get('SLOVerdict', metric.get('D1')),
                        'withinSLOFraction': metric.get('withinSLOFraction', metric.get('deadlineSuccessFraction'))}
            if not operational:
                slo = slos.get(name, {})
                target = slo.get('targetLatencyNanos')
                latency = metric.get('gatewayCompletedLatencyNS') or metric.get('completedLatencyNS') or {}
                p95 = latency.get('p95')
                verdict = f'{metric.get("D1", "UNKNOWN")} ({metric.get("deadlineSuccess", "UNKNOWN")}/{metric.get("requiredSuccessfulRank", "UNKNOWN")})'
                columns = [row_id, mode, name+'; p'+str(slo.get('percentile', '?'))+'≤'+number(target/1e6 if target is not None else None)+'ms',
                           number(p95/1e6 if p95 is not None else None), result_counts(metric), verdict,
                           energy_text(chill), static_text, difference_text]
                result_rows.append('| '+' | '.join(cell(column) for column in columns)+' |')
            lines += [f'**{cell(name)}**', '', '```json', json.dumps(selected, ensure_ascii=False, indent=2), '```', '']
        del stage
    if not paths:
        lines += ['No stage summaries present; timing values unavailable.', '']
    if not operational:
        lines[results_at:results_at] = [
            '', '## 워크로드별 처리 결과와 동일 단계 POOL 에너지', '',
            '수신 p95는 성공·executionComplete HTTP final의 완료-only 지연 분포다. D1은 전체 planned offered를 분모로 하며 deadline 성공 수/필요 rank를 표시한다. 오류·거절·취소·무발행·미완료는 p95 표본에 섞지 않지만 D1 분모에는 남는다.',
            '완료 수는 이 완료-only 지연 표본 수, 실패는 error/timeout/preprocess_failed/delivery_failed, 미완료·불명은 나머지 planned다. 괄호는 offered 요청에 결합한 상위 오류 코드(없으면 원래 실패 outcome)다.',
            '에너지는 workload 귀속이 아닌 동일 단계 공유 POOL 값(전체/활성)이며 반복 행을 합산하지 않는다. ±는 198150d6 E5 공백 보간 오차 상한, coverage는 Node 공백초 합/최대 공백/최대 Node 공백 비율이다. UNKNOWN을 0으로 바꾸지 않는다.',
            '고정 E5-S는 같은 E5-1~6 trace의 기준선이다. static 행의 에너지 열도 full CHILL과 해당 고정을 나란히 보여주며, Δ=고정−CHILL(양수는 CHILL 절감)이다. factor 행에서는 CHILL 열이 표시된 제어요소 변형이다.',
            '', '| 단계 | 방식 | Workload·SLO | 수신 p95 (ms) | 결과 수 (요청) | D1 (deadline/필요 rank) | CHILL 전체/활성 POOL·오차·coverage | 대응 고정 E5-S 전체/활성 POOL·오차·coverage | Δ 고정−CHILL 전체 POOL |',
            '|---|---|---|---:|---|---|---|---|---|',
            *result_rows, '']
    return '\n'.join(lines)


def self_check(repo):
    import tempfile
    with tempfile.TemporaryDirectory() as folder:
        root = Path(folder)
        (root / 'binding.json').write_text(json.dumps({'schema':'demo-e5-binding.v1',
            'schedulerSettings':{'excludedNodes':['genesis1','orin-nano','genesis2']}}))
        stage = root / 'smoke-check'
        stage.mkdir()
        metric = {'offered': 2, 'latencyMS': {'p99': 3}, 'entranceDelayNS': {'min': -7},
                  'SLOVerdict': 'INVALID_GENERATOR', 'issuanceFidelity': {'sendLatenessNS': {'max': 11}, 'generatorQuality': {'status': 'INVALID_GENERATOR'}}}
        (stage / 'summary.json').write_text(json.dumps({'status': 'INVALID_GENERATOR', 'workloads': {'test': metric}}))
        text = render(root, repo)
        assert text.count('UNMEASURED; Run ID') == 16
        assert '"min": -7' in text and '"max": 11' in text and '"p99": 3' in text
        assert '"SLOVerdict": "INVALID_GENERATOR"' in text
        assert 'genesis2 하드웨어 결함으로 제외' in text and '"energyBelowResolution": null' in text
        (root / 'binding.json').write_text('{"schema":"e5-binding.v1"}')
        (root / 'e5-summary.json').write_text(json.dumps({
            'planRows': {'E5-7': 'MEASURED'},
            'specScenarios': {'node-serving-control-failure': 'MEASURED'},
            'failureScenarioEvidence': {'status': 'CAPTURED', 'source': 'operator-receipt'},
            'energyBelowResolution': {'full-E5-7': {'profiles': [{'profileID':'below-resolution-profile',
                'batch':1,'energyPerInputJoules':0,'energyBelowResolution':True}]}},
        }))
        text = render(root, repo)
        assert 'E5-7 실패' in text and text.count('MEASURED; Run ID') == 16
        failure_row = next(line for line in text.splitlines() if line.startswith('| E5-7 실패'))
        assert ' MEASURED;' in failure_row and 'UNMEASURED' not in failure_row
        assert '"status": "CAPTURED"' in text and 'operator-receipt' in text
        assert 'below-resolution-profile' in text and '"energyBelowResolution": true' in text
        # Synthetic presentation fixture: completed-only p95 passes, planned D1 fails.
        workload = 'mnv-fixture'
        (root/'binding.json').write_text(json.dumps({'schema':'e5-binding.v1','config':{'workloads':[
            {'name':workload,'slo':{'percentile':95,'targetLatencyNanos':1_000_000_000}}]}}))
        metric = {'offered':100,'outcomes':{'completed':90,'error':6,'timeout':1,'rejected':1,'canceled':1,'unfinished-or-no-final':1},
                  'gatewayCompletedLatencyNS':{'samples':90,'p95':400_000_000},'D1':'FAIL',
                  'deadlineSuccess':89,'requiredSuccessfulRank':95,
                  'offeredResponsePopulation':{'errors':{'BackendUnavailable':6,'RequestTimeout':1}}}
        coverage = {'nodeGapSecondsSum':2,'maximumGapSeconds':1.5,'maximumNodeGapFraction':.005}
        full_power = {'complete':True,'wholePoolJoules':1000,'activePoolJoules':800,
                      'gapErrorBoundJoules':5,'energyCoverage':coverage}
        static_power = dict(full_power,wholePoolJoules=1200,activePoolJoules=900,gapErrorBoundJoules=7)
        summary = load(root/'e5-summary.json')
        summary['planRows']['E5-1'] = 'FAIL'
        summary['pairedComparisons'] = [{'row':'E5-1','fullWholePOOLJ':1000,'staticWholePOOLJ':1200,
            'fullActivePOOLJ':800,'staticActivePOOLJ':900,'staticMinusFullWholePOOLJ':200,
            'energyGapError':{'fullBoundJoules':5,'staticBoundJoules':7,'differenceBoundJoules':12,
                             'fullCoverage':coverage,'staticCoverage':coverage}}]
        (root/'e5-summary.json').write_text(json.dumps(summary))
        for mode, power in (('full',full_power),('static',static_power)):
            folder = root/(mode+'-E5-1')
            folder.mkdir()
            (folder/'judgment.json').write_text(json.dumps({'status':'FAIL','power':power,'requests':{workload:metric}}))
        text = render(root,repo)
        row = next(line for line in text.splitlines() if line.startswith('| E5-1 | full |'))
        assert 'p95≤1,000.000ms' in row and '| 400.000 |' in row and 'FAIL (89/95)' in row
        assert '완료 90 / 실패 7' in row and '거절 1 / 취소 1 / 미완료·불명 1' in row
        assert 'BackendUnavailable=6' in row and 'RequestTimeout=1' in row
        assert '1,000.000 / 800.000 J; ±5.000 J' in row and '1,200.000 / 900.000 J; ±7.000 J' in row
        assert 'gapΣNode=2.000s, max=1.500s, maxNode=0.500%' in row and '200.000 ±12.000 J' in row
        static_row = next(line for line in text.splitlines() if line.startswith('| E5-1 | static |'))
        assert '1,000.000 / 800.000 J; ±5.000 J' in static_row and '1,200.000 / 900.000 J; ±7.000 J' in static_row
        assert '99,999' not in energy_text(dict(full_power,complete=False,wholePoolJoules=99999))
    print('tables self-check: PASS')

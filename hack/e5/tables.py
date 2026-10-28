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
            lines += [f'**{cell(name)}**', '', '```json', json.dumps(selected, ensure_ascii=False, indent=2), '```', '']
        del stage
    if not paths:
        lines += ['No stage summaries present; timing values unavailable.', '']
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
    print('tables self-check: PASS')

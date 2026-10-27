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
             'Existing summary judgments only; MEASURED is not criterion PASS. Missing values are not zero.',
             'Operational G0/demo evidence does not complete an E5 evaluation row.' if operational else 'Source: e5-summary.json (existing summarize output).', '', '## docs/spec/README.md', '', *spec[:2]]
    for row, key in zip(spec[2:], scenarios):
        columns = row.split('|')[1:-1]
        status = summary.get('specScenarios', {}).get(key, 'UNMEASURED')
        columns[-1] = f' {status}; Run ID `{cell(root.parent.name if root.name == "run" else root.name)}`; raw `{cell(root)}`; summary `{cell(root / ("summary.json" if operational else "e5-summary.json"))}` '
        lines.append('|' + '|'.join(columns) + '|')
    lines += ['', '## workload-pools E5', '', *pools[:2]]
    for row in pools[2:]:
        columns = row.split('|')[1:-1]
        stage = columns[0].strip().split()[0]
        status = summary.get('planRows', {}).get(stage, 'UNMEASURED')
        columns[-1] = f' {status}; Run ID `{cell(root.parent.name if root.name == "run" else root.name)}`; raw `{cell(root)}` '
        lines.append('|' + '|'.join(columns) + '|')
    lines += ['', '## Three timing populations (existing summary values)', '',
              'Generator: scheduled arrival → actual send (ns); entrance: actual send → gateway receivedAt (ns, cross-host clock uncertainty, negative values retained); SLO: CHILL receipt → response via gateway elapsedNanos. Offered includes unsent requests. Completed latency quantiles do not replace the offered denominator.', '']
    paths = sorted(root.glob('smoke-*/summary.json')) if operational else sorted(root.glob('*/judgment.json'))
    for path in paths:
        stage = load(path)
        lines += [f'### {cell(path.parent.name)}', '', f'Source: `{cell(path)}`; status: {cell(stage.get("status", "UNKNOWN"))}', '']
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
        (root / 'binding.json').write_text('{"schema":"demo-e5-binding.v1"}')
        stage = root / 'smoke-check'
        stage.mkdir()
        metric = {'offered': 2, 'latencyMS': {'p99': 3}, 'entranceDelayNS': {'min': -7},
                  'SLOVerdict': 'INVALID_GENERATOR', 'issuanceFidelity': {'sendLatenessNS': {'max': 11}, 'generatorQuality': {'status': 'INVALID_GENERATOR'}}}
        (stage / 'summary.json').write_text(json.dumps({'status': 'INVALID_GENERATOR', 'workloads': {'test': metric}}))
        text = render(root, repo)
        assert text.count('UNMEASURED; Run ID') == 16
        assert '"min": -7' in text and '"max": 11' in text and '"p99": 3' in text
        assert '"SLOVerdict": "INVALID_GENERATOR"' in text
    print('tables self-check: PASS')

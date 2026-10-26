#!/usr/bin/env python3
"""Offline demo reports, ported from the latest retained timeline.py (multipart).

Use through demo.py report DIRECTORY [--compare DIRECTORY ...] [--out DIRECTORY].
Only phase.json, summary.json, *-client.jsonl, power.json and scheduler.jsonl.gz
are inputs. No environment, network, cluster, config, report or state-file reads.
The existing indented per-stage summary supplies captured boundary key changes;
raw scheduler observations supply occupancy, actions and scaleDown observations.
Multipart energy is unknown unless a power.json covers that exact part window:
compact scheduler records alone do not retain their catalog coverage policy.
"""
from pathlib import Path
from collections import defaultdict
from datetime import datetime, timezone
import gzip
import json
import math
import re


def epoch(value):
    value = re.sub(r'\.(\d+)', lambda m: '.' + m[1][:6].ljust(6, '0'), value)
    return datetime.fromisoformat(value.replace('Z', '+00:00')).timestamp()


def utc(value):
    return datetime.fromtimestamp(value, timezone.utc).isoformat()


def percentile(values, p):
    values = sorted(values)
    return values[max(0, math.ceil(len(values) * p) - 1)] if values else None


def success(row):
    return row.get('status') == 200 and not row.get('error')


def summary_changes(path):
    """Read only the small boundary-change field, not hundreds of MB of actions.

    summary.json is written with indent=2 by demo.summarize. A malformed or
    differently formatted summary fails explicitly, rather than dropping counts.
    """
    with path.open() as stream:
        for line in stream:
            if line.startswith('  "placementChanges": '):
                value = line.split(': ', 1)[1]
                if value.strip() == '[],':
                    return []
                if value.strip() != '[':
                    raise ValueError(f'{path}: expected indented placementChanges array')
                lines = [value]
                for line in stream:
                    lines.append(line)
                    if line.startswith('  ]'):
                        return json.loads(''.join(lines).rstrip().rstrip(','))
                break
    raise ValueError(f'{path}: missing/incomplete placementChanges; regenerate summary first')


def selected_clients(path, trace):
    rows = {}
    with path.open() as stream:
        for line in stream:
            row = json.loads(line)
            if 'requestIndex' not in row:
                continue
            i = row['requestIndex']
            if type(i) is not int or not 0 <= i < len(trace) or row.get('scheduledOffsetNs') != trace[i]:
                raise ValueError(f'{path}: client/trace identity mismatch: {i}')
            if i in rows:
                raise ValueError(f'{path}: duplicate requestIndex {i}')
            rows[i] = row
    return rows


def collect(root):
    root = Path(root).resolve()
    phases = {}
    for path in root.glob('smoke-*/phase.json'):
        phase = json.loads(path.read_text())
        if not phase.get('startUTC') or not phase.get('endUTC'):
            raise ValueError(f'{path}: finalized phase required')
        phases[path.parent.name] = phase
    if not phases:
        raise ValueError(f'{root}: no smoke-*/phase.json')
    phases = dict(sorted(phases.items(), key=lambda pair: epoch(pair[1]['startUTC'])))
    names = tuple(next(iter(phases.values()))['stage']['traces'])
    # Match the retained report's M/P/S order, without tying stages to a run.
    known = ('image-classification-tight-slo', 'plc-anomaly', 'smolvlm2-vqa')
    names = tuple(w for w in known if w in names) + tuple(w for w in names if w not in known)
    labels = tuple(dict(zip(known, ('MobileNetV2', 'PLC anomaly', 'SmolVLM2'))).get(w, w) for w in names)
    segments, windows, metrics, bins, uids, counts, changes, power = {}, {}, {}, {}, {}, {}, [], {}
    client_notes, energy_notes = [], []
    for folder, phase in phases.items():
        stage = phase['stage']
        parts = stage.get('parts') or [stage]
        pool_bounds = [epoch(phase['startUTC'])]
        offset = 0
        for pi, part in enumerate(parts):
            if pi:
                anchor = next((w for w in names if part['rates'][w] != parts[pi-1]['rates'][w]), names[0])
                start = phase['starts'].get(anchor)
                pool_bounds.append(epoch(start['at'] if start else phase['startUTC']) + offset/1e9)
            offset += round(part['seconds'] * 1e9)
        pool_bounds.append(epoch(phase['endUTC']))
        base = 0
        stage_changes = summary_changes(root / folder / 'summary.json')
        changes.extend(stage_changes)
        ppath = root / folder / 'power.json'
        p = json.loads(ppath.read_text()) if ppath.exists() else None
        for pi, part in enumerate(parts):
            s = part['id']
            if s in segments:
                raise ValueError(f'Duplicate part ID {s}')
            stop = base + round(part['seconds'] * 1e9)
            segments[s] = dict(folder=folder, rates=part['rates'], offsets=[base, stop])
            for w in names:
                start = phase['starts'].get(w)
                windows[s, w] = ((epoch(start['at']) + base/1e9, epoch(start['at']) + stop/1e9)
                                 if start else (epoch(phase['startUTC']), epoch(phase['endUTC'])))
            # One shared physical boundary; requests still use individual clocks.
            a, b = pool_bounds[pi:pi+2]
            segments[s]['poolWindow'] = [a, b]
            counts[s] = 0
            power[s] = p if len(parts) == 1 else None
            if len(parts) > 1:
                energy_notes.append(f'{s}: no exact-part power.json; combined {stage["id"]} integral is not split proportionally. Raw compact scheduler samples lack catalog coverage policy.')
            for change in stage_changes:
                t = epoch(change['observedAtUTC'])
                # Preserve pre-stage gap inclusion; shared multipart boundary goes to next part.
                if (base == 0 or t >= a) and (part is parts[-1] or t < b):
                    counts[s] += 1
            base = stop
        for w in names:
            trace = stage['traces'][w]['offsetsNS']
            rows = selected_clients(root / folder / (w + '-client.jsonl'), trace)
            for r in rows.values():
                if r.get('workloadUID'):
                    if w in uids and uids[w] != r['workloadUID']:
                        raise ValueError(f'{w}: workload UID changed within run')
                    uids[w] = r['workloadUID']
            for s, segment in segments.items():
                if segment['folder'] != folder:
                    continue
                a, b = segment['offsets']
                indexes = [i for i, offset in enumerate(trace) if a <= offset < b]
                selected = [rows[i] for i in indexes if i in rows]
                good = [r for r in selected if success(r)]
                metrics[s, w] = dict(offered=len(indexes), success=len(good), p95MS=percentile([r['latencyMs'] for r in good], .95), missingClientRows=len(indexes)-len(selected))
                grouped = defaultdict(lambda: [0, 0])
                start = phase['starts'].get(w)
                for row in selected:
                    if not start or row.get('sentMonotonicNS') is None:
                        continue
                    sent = epoch(start['at']) + (row['sentMonotonicNS']-start['monotonicNS'])/1e9
                    k = math.floor(sent/30)
                    grouped[k][0] += 1
                    grouped[k][1] += int(success(row))
                unknown = len(indexes)-sum(c[0] for c in grouped.values())
                if unknown:
                    client_notes.append(f'{s}/{w}: {unknown} offered requests lack actual send UTC; kept in denominator, excluded only from time bins.')
                bins[s, w] = grouped
    lo = min(epoch(p['startUTC']) for p in phases.values())
    end = max(epoch(p.get('drainedUTC') or p['endUTC']) for p in phases.values())
    snapshots, actions, failures = {}, {}, {}
    seen_paths = set()
    for folder in phases:
        for path in sorted((root / folder / 'obs').glob('*/scheduler.jsonl.gz')):
            # Hardlinked captures still deduplicate observations by timestamp/identity.
            stat = path.stat()
            identity = stat.st_dev, stat.st_ino
            if identity in seen_paths:
                continue
            seen_paths.add(identity)
            with gzip.open(path, 'rt') as stream:
                for line in stream:
                    row = json.loads(line)
                    t = epoch(row['at'])
                    if not lo <= t <= end:
                        continue
                    if row.get('snapshot'):
                        snap = row['snapshot']
                        snapshots[t] = {k: snap.get(k) or ({} if k in ('routing', 'scaleDown') else []) for k in ('instances', 'routing', 'nodes', 'scaleDown')}
                        snapshots[t]['at'] = row['at']
                    if row.get('action'):
                        action = row['action']
                        actions[action['id'], action['phase']] = action
                    if row.get('samples') or row.get('failures'):
                        bad = row.get('failures', []) + [sample for sample in row.get('samples', []) if sample.get('error') or sample.get('failure')]
                        if bad:
                            key = row['node']['name'], row.get('startedAt'), row['at']
                            failures[key] = dict(node=row['node']['name'], start=row.get('startedAt'), end=row['at'], samples=bad)
    if not snapshots:
        raise ValueError(f'{root}: no scheduler snapshots within phase/drain windows')
    # UID is not inferred from profile names; missing client identity stays unknown.
    return dict(root=root, phases=phases, names=names, labels=labels, segments=segments,
                windows=windows, metrics=metrics, bins=bins, uids=uids, counts=counts,
                changes=changes, power=power, snapshots=snapshots, actions=actions,
                failures=list(failures.values()), lo=lo, hi=max(snapshots),
                client_notes=client_notes, energy_notes=energy_notes)


def fmt(value, digits=2):
    return '—' if value is None else f'{value:.{digits}f}'


def table(headers, rows):
    def clean(value):
        return str(value).replace('|', '/').replace('\n', ' ')
    return ['| ' + ' | '.join(headers) + ' |', '|' + '|'.join('---' for _ in headers) + '|'] + ['| ' + ' | '.join(clean(v) for v in row) + ' |' for row in rows]


def energy(p):
    return p['wholePoolJoules']/1000 if p and p.get('complete') and p.get('wholePoolJoules') is not None else None


def timing_rows(data):
    """First permission per downSince episode overlapping a decreasing-load part.

    Permission is an observed gate, not an action. Prepare/route is a selection
    proxy; stop blocks requests; completed remove is not exact physical deletion.
    """
    rows = []
    reverse = {v:k for k,v in data['uids'].items()}
    intervals = {}
    for action in data['actions'].values():
        item = intervals.setdefault(action['id'], dict(action))
        if action.get('phase') == 'starting':
            item['startedAt'] = action.get('startedAt')
        if action.get('completedAt'):
            item['completedAt'] = action['completedAt']
    stages = list(data['segments'])
    for i, s in enumerate(stages):
        seg = data['segments'][s]
        if not i or not any(seg['rates'][w] < data['segments'][stages[i-1]]['rates'][w] for w in data['names']):
            continue
        a, b = seg['poolWindow']
        episodes = {}
        for t, snap in sorted(data['snapshots'].items()):
            for uid, gate in snap['scaleDown'].items():
                if uid not in reverse or not gate.get('downSince'):
                    continue
                key = uid, gate['downSince']
                if gate.get('allowed') and key not in episodes:
                    episodes[key] = (t, snap['at'], gate)
        # Retain only episodes still observed during this lower-load window.
        active = {(uid, g['downSince']) for t, snap in data['snapshots'].items() if a <= t <= b for uid,g in snap['scaleDown'].items() if g.get('downSince')}
        permitted = set()
        for (uid, since), (t, observed, gate) in sorted(episodes.items(), key=lambda pair: pair[1][0]):
            if (uid,since) in active and t <= b:
                rows.append([s, reverse[uid], 'scaleDown allowed 최초 관측', since, observed, fmt(gate.get('elapsedNanos', 0)/1e9, 6) + ' s'])
                permitted.add(uid)
        for w in data['names']:
            if data['uids'].get(w) not in permitted:
                rows.append([s, w, 'scaleDown allowed 최초 관측', '—', '—', '보존 snapshot에 허가 관측 없음'])
        candidates = [x for x in intervals.values() if x.get('startedAt') and a <= epoch(x['startedAt']) <= b]
        for kinds, label in [(('prepare', 'route'), '첫 축소 선택 관련 prepare/route (동작 proxy)'), (('stop',), '첫 stop'), (('remove',), '첫 제거 완료')]:
            valid = [x for x in candidates if x.get('kind') in kinds and (x.get('completedAt') if kinds == ('remove',) else True)]
            if valid:
                action = min(valid, key=lambda x: epoch(x.get('completedAt') if kinds == ('remove',) else x['startedAt']))
                rows.append([s, action.get('node', {}).get('name') or 'POOL', label, action['startedAt'], action.get('completedAt') or '—', action['id']])
            else:
                rows.append([s, 'POOL', label, '—', '—', '보존 창 안 관측 없음'])
    return rows


def write_tables(data, comparisons, out):
    rows = []
    for s, segment in data['segments'].items():
        a,b = segment['poolWindow']
        for w in data['names']:
            m = data['metrics'][s,w]
            rows.append([f'{s} / {utc(a)}–{utc(b)}', w, f'{m["offered"]:,} / {m["success"]:,}', fmt(100*m['success']/m['offered'] if m['offered'] else None) + ('%' if m['offered'] else ''), fmt(m['p95MS']), data['counts'][s], fmt(energy(data['power'][s]), 3)])
    lines = ['# Offline demo report tables', '', '![Timeline](timeline.svg)', '', '## 단계별 결과', '', '성공 = 오류 없는 HTTP 200 / 동결 예정 추적 전체; 누락 행도 분모에 유지. p95 = 성공 응답 HTTP 왕복 nearest-rank. 배치 변경은 batch 크기가 아닌 placement key 차이이며 준비·수집 공백을 다음 단계에 포함한다. 공유 POOL 에너지를 작업마다 반복하므로 합산하지 않는다. 결측은 0이 아니다.', '']
    lines += table(['단계 / UTC 구간', '작업', '예정 / 성공', '성공률', 'p95 ms', '배치 변경', 'POOL kJ'], rows)
    lines += ['', '## 전력 결측 노드·UTC 구간', '']
    pool_nodes = set()
    for folder in data['phases']:
        path = data['root']/folder/'power.json'
        if path.exists():
            pool_nodes.update(n['node']['name'] for n in json.loads(path.read_text()).get('nodes', []))
    missing = []
    for failure in sorted(data['failures'], key=lambda f: (f['start'], f['node'])):
        node = failure['node'] + (' (POOL 외)' if failure['node'] not in pool_nodes else '')
        missing.append([node, failure['start'], failure['end'], '; '.join(x.get('observedAt', '?') for x in failure['samples']), json.dumps(failure['samples'], ensure_ascii=False)])
    for folder in data['phases']:
        path = data['root']/folder/'power.json'
        if not path.exists():
            missing.append([folder, '—', '—', '—', 'power.json absent'])
            continue
        p = json.loads(path.read_text())
        for n in p.get('nodes', []):
            if n.get('joules') is None or n.get('reasons'):
                missing.append([n['node']['name'], p['from'], p['to'], 'reducer whole-window coverage', '; '.join(n.get('reasons', [])) or 'joules missing'])
    lines += table(['노드', 'UTC 시작', 'UTC 종료', '실패 sample UTC', '근거 / 사유'], missing or [['—']*4+['결측 관측 없음 (자료 밖 완전성 주장은 아님)']])
    lines += ['', '## 회차 비교', '', '입력 폴더 이름으로 식별하며 회차/제어 버전을 추측하지 않는다. 작업 순서: ' + ' / '.join(data['names']) + '. 동일 조건 반복이나 인과 실험이 아니다.', '']
    comparison_rows = []
    for run in comparisons + [data]:
        for s in run['segments']:
            ms = [run['metrics'].get((s,w)) for w in data['names']]
            comparison_rows.append([run['root'].name, s, ' / '.join(fmt(100*m['success']/m['offered'] if m and m['offered'] else None) for m in ms), ' / '.join(fmt(m['p95MS'] if m else None) for m in ms), run['counts'][s], fmt(energy(run['power'][s]), 3)])
    lines += table(['회차 / 제어 버전 (입력 폴더)', '단계', '성공률 % (M/P/S)', 'p95 ms (M/P/S)', '배치 변경', 'POOL kJ'], comparison_rows)
    lines += ['', '## 하강 허가·첫 축소·제거 완료', '', 'snapshot allowed 최초 관측, action 시작/완료, 물리 삭제를 구분한다. 첫 prepare/route는 하강 구간의 선택 동작 proxy이지 용량 순감소 증명이 아니다. C 시작에 타이머를 새로 시작했다고 가정하지 않는다.', '']
    lines += table(['단계', '작업 / 노드', '관측 / 동작', 'downSince / 시작 UTC', '최초 관측 / 완료 UTC', 'elapsed / action ID'], timing_rows(data))
    lines += ['', '## 재생산과 관측 한계', '', '입력: smoke-*/phase.json, summary.json, *-client.jsonl, power.json, obs/*/scheduler.jsonl.gz만. 원 폴더 변경 없음. summary 경계 변경은 명시적 상태를 포함하는 기존 집계이며 raw 스냅샷만의 그림과 관측 밀도가 다를 수 있다. 관측 공백은 0이 아니며 cleanup 후 상태를 추측하지 않는다.', '']
    lines.append('배치 수는 demo.summarize의 정규화된 placementChanges를 재사용한다. compact raw에 빠진 profileID를 명시적 상태의 실제 profileID와 직접 비교해 생기는 표현 차이를 새 배치 변경으로 추가하지 않는다.')
    lines += ['- '+note for note in data['energy_notes'] + data['client_notes']]
    (out/'report-tables.md').write_text('\n'.join(lines)+'\n')


def report(root, compare=(), out=None):
    root = Path(root).resolve()
    out = Path(out).resolve() if out else root
    # External output must not overlap a comparison input either.
    inputs = [root] + [Path(p).resolve() for p in compare]
    if out != root and any(out == p or out in p.parents or p in out.parents for p in inputs):
        raise ValueError('--out must not overlap input evidence directories')
    data = collect(root)
    comparisons = [collect(p) for p in compare]
    out.mkdir(parents=True, exist_ok=True)
    render(data, out)
    write_tables(data, comparisons, out)
    print(str(out/'timeline.svg'))
    print(str(out/'report-tables.md'))


def render(data, out):
    # Latest retained four-panel timeline, generalized to discovered parts/clocks.
    # The sibling analysis CLI numbers.py must not shadow stdlib numbers in NumPy.
    import sys
    previous_path = sys.path[:]
    try:
        sys.path[:] = [p for p in sys.path if Path(p).resolve() != Path(__file__).parent]
        import matplotlib
        matplotlib.use('Agg')
        import matplotlib.pyplot as plt
        import matplotlib.dates as mdates
        from matplotlib.lines import Line2D
    finally:
        sys.path[:] = previous_path
    def date(t):
        return mdates.date2num(datetime.fromtimestamp(t, timezone.utc))
    names, labels = data['names'], data['labels']
    colors = [plt.get_cmap('tab10')(i) for i in range(len(names))]
    stages, windows = data['segments'], data['windows']
    snapshots, uids = data['snapshots'], data['uids']
    times = sorted(snapshots)
    lo, hi = data['lo'], data['hi']
    plt.rcParams.update({'font.family': 'DejaVu Sans', 'font.size': 10, 'svg.fonttype': 'none',
                         'axes.spines.top': False, 'axes.spines.right': False})
    fig, axes = plt.subplots(4, 1, figsize=(24, 20), sharex=True,
                             gridspec_kw={'height_ratios': [2, 2, 7, 2]})
    fig.subplots_adjust(left=.18, right=.98, bottom=.12, top=.92, hspace=.27)
    fig.suptitle('CHILL autonomous workload pools | ' + data['root'].name,
                 x=.18, y=.98, ha='left', fontsize=22, weight='bold')
    fig.text(.18, .95, 'UTC | Shared POOL energy, not per-workload energy. Operational evidence, not research results.',
             color='#475569')
    gaps, end = [], lo
    for phase in data['phases'].values():
        a, b = epoch(phase['startUTC']), epoch(phase['endUTC'])
        if a > end:
            gaps.append((end, a))
        end = max(end, b)
    if end < hi:
        gaps.append((end, hi))
    for ax in axes:
        ax.set_xlim(date(lo), date(hi))
        ax.set_facecolor('#fbfcfd')
        ax.grid(axis='x', alpha=.2)
        for a,b in gaps:
            ax.axvspan(date(a), date(b), facecolor='#e8edf1', edgecolor='#c3cdd6',
                       hatch='///', linewidth=0, alpha=.65)
        for segment in stages.values():
            ax.axvline(date(segment['poolWindow'][0]), color='#64748b', lw=.6, alpha=.6)
    axes[0].set_title('1 / Configured load (requests/s; actual producer clocks)', loc='left', weight='bold')
    for wi,w in enumerate(names):
        for s,segment in stages.items():
            a,b = windows[s,w]
            axes[0].broken_barh([(date(a), date(b)-date(a))], (wi-.28,.56), facecolors=colors[wi])
            axes[0].text(date((a+b)/2), wi, f"{s}\n{segment['rates'][w]:g}",
                         ha='center', va='center', color='white', fontsize=9)
    axes[0].set_yticks(range(len(names)), labels)
    axes[1].set_title('2 / HTTP success by actual send time (30-second UTC bins; unknown sends blank)',
                      loc='left', weight='bold')
    for wi,w in enumerate(names):
        merged = defaultdict(lambda: [0,0])
        for s in stages:
            for k,(n,ok) in data['bins'][s,w].items():
                merged[k][0] += n
                merged[k][1] += ok
        for k,(n,ok) in merged.items():
            a,b = max(lo,k*30), min(hi,(k+1)*30)
            if a < b:
                axes[1].plot([date(a),date(b)], [100*ok/n]*2, color=colors[wi], lw=2)
    axes[1].set_ylim(-5,105)
    axes[1].set_yticks([0,50,100], ['0%','50%','100%'])
    axes[1].legend([Line2D([],[],color=c,lw=2) for c in colors], labels, loc='lower left', ncol=len(names))
    ax = axes[2]
    ax.set_title('3 / Observed instances/node (+ added, x removed, diamond routing; red power)',
                 loc='left', weight='bold')
    nodes = sorted({i['node']['name'] for snap in snapshots.values() for i in snap['instances']
                    if i.get('ownerWorkloadUID') in uids.values()})
    rowspec = [(w,n) for w in names for n in nodes]
    ys = {key: len(rowspec)-1-i for i,key in enumerate(rowspec)}
    ax.set_yticks([ys[key] for key in rowspec],
                  [f'{labels[names.index(w)]} / {n}' for w,n in rowspec], fontsize=8)
    ax.set_ylim(-1,len(rowspec)+1)
    for a,b in zip(times,times[1:]):
        if b-a > 30:
            ax.axvspan(date(a),date(b),facecolor='none',edgecolor='#94a3b8',
                       hatch='xxx',linewidth=0,alpha=.45)
    for wi,w in enumerate(names):
        uid = uids.get(w)
        if uid is None:
            continue
        for node in nodes:
            y = ys[w,node]
            values = [sum(i.get('ownerWorkloadUID') == uid and i['node']['name'] == node
                          for i in snapshots[t]['instances']) for t in times]
            begin = 0
            for j in range(1,len(times)+1):
                gap = j < len(times) and times[j]-times[j-1] > 30
                if j < len(times) and values[j] == values[begin] and not gap:
                    continue
                a = times[begin]
                b = times[j-1] if gap else times[j] if j < len(times) else times[-1]
                count = values[begin]
                if count and b > a:
                    ax.broken_barh([(date(a),date(b)-date(a))],(y-.35,.7),
                                   facecolors=colors[wi],alpha=.7)
                    if b-a > 60:
                        ax.text(date((a+b)/2),y,str(count),ha='center',va='center',
                                fontsize=8,color='white')
                begin = j
        previous = None
        for t in times:
            snap = snapshots[t]
            inst = {i['id']:i for i in snap['instances'] if i.get('ownerWorkloadUID') == uid}
            route = snap['routing'].get(uid,{})
            if previous is not None:
                old,oldroute = previous
                for iid in inst.keys()-old.keys():
                    ax.plot(date(t),ys[w,inst[iid]['node']['name']],'+',color='#133c29',ms=7)
                for iid in old.keys()-inst.keys():
                    ax.plot(date(t),ys[w,old[iid]['node']['name']],'x',color='#9f1239',ms=6)
                if route != oldroute:
                    touched = {inst[i]['node']['name'] for i,weight in route.items() if weight and i in inst}
                    touched |= {old[i]['node']['name'] for i,weight in oldroute.items() if weight and i in old}
                    for node in touched:
                        ax.plot(date(t),ys[w,node],'D',color='#172554',ms=4)
            previous = inst,route
    for action in data['actions'].values():
        if action.get('kind') == 'power' and action.get('completedAt'):
            ax.axvspan(date(epoch(action['startedAt'])),date(epoch(action['completedAt'])),
                       color='#e63946',alpha=.14)
    ax = axes[3]
    ax.set_title('4 / Shared POOL energy (exact power.json windows; unavailable is not zero)',
                 loc='left', weight='bold')
    ceiling = max((energy(p) or 0 for p in data['power'].values()),default=1)*1.3 or 1
    for s,segment in stages.items():
        p = data['power'][s]
        a,b = (epoch(p['from']),epoch(p['to'])) if p else segment['poolWindow']
        kj = energy(p)
        if kj is not None:
            ax.fill_between([date(a),date(b)],[kj,kj],color='#34495e',alpha=.75)
            ax.text(date((a+b)/2),kj+ceiling*.035,f'{s}: {kj:.3f} kJ',ha='center',fontsize=9)
        else:
            ax.axvspan(date(a),date(b),facecolor='none',edgecolor='#b91c1c',hatch='xxx',linewidth=0)
            ax.text(date((a+b)/2),ceiling*.5,f'{s}: unavailable\nnot zero',ha='center',fontsize=9)
    ax.set_ylim(0,ceiling)
    ax.set_ylabel('kJ / stage')
    ax.xaxis.set_major_locator(mdates.AutoDateLocator(minticks=8,maxticks=18))
    ax.xaxis.set_major_formatter(mdates.DateFormatter('%H:%M:%S',tz=timezone.utc))
    ax.set_xlabel('UTC | /// outside offered/gate windows; xxx unobserved scheduler/power; neither means zero')
    fig.text(.18,.025,
             'Events are first observed snapshot differences, not exact physical creation/removal times.\n'
             'Blank occupancy across >30 s gaps is unknown, not zero. No post-drain cleanup state is invented.\n'
             'Requests follow frozen scheduled part membership on each actual producer clock; missing sends are not fabricated.\n'
             'Boundary placement counts include existing summary captures. Exact gaps, gate/action times and energy limitations: report-tables.md.',
             fontsize=9,color='#475569')
    fig.savefig(out/'timeline.svg',metadata={'Date':None})
    plt.close(fig)


def self_check():
    """One runnable local check: real parser, multipart clocks, gates and output."""
    import tempfile
    with tempfile.TemporaryDirectory(prefix='demo-report-check-') as temp:
        root = Path(temp)/'evidence'
        folder = root/'smoke-highlow'
        obs = folder/'obs'/'hour'
        obs.mkdir(parents=True)
        start = '2026-01-01T00:00:00Z'
        at = lambda seconds: utc(epoch(start)+seconds)
        name = 'test-workload'
        parts = [dict(id='high',seconds=10,rates={name:2}),
                 dict(id='low',seconds=10,rates={name:1})]
        trace = [0,9_999_999_999,10_000_000_000,19_999_999_999]
        phase = dict(stage=dict(id='highlow',parts=parts,traces={name:dict(offsetsNS=trace)}),
                     startUTC=start,endUTC=at(21),drainedUTC=at(22),
                     starts={name:dict(at=at(1),monotonicNS=100)})
        (folder/'phase.json').write_text(json.dumps(phase))
        changes = [dict(observedAtUTC=at(t)) for t in (0,10,11,20)]
        (folder/'summary.json').write_text(json.dumps(dict(placementChanges=changes,actions=['unused']),indent=2))
        clients = [dict(requestIndex=i,scheduledOffsetNs=trace[i],status=200,
                        workloadUID='uid',latencyMs=i+1,sentMonotonicNS=100+i*1_000_000_000)
                   for i in (0,1,2)]  # Last offered response intentionally missing.
        clients[1]['error'] = 'HTTP 200 with client error is not success'
        cp = folder/(name+'-client.jsonl')
        cp.write_text(''.join(json.dumps(r)+'\n' for r in clients))
        power = dict(complete=True,wholePoolJoules=2000,nodes=[],**{'from':start,'to':at(21)})
        (folder/'power.json').write_text(json.dumps(power))
        def snap(t, allowed):
            return dict(at=at(t),snapshot=dict(instances=[],nodes=[],routing={},
                        scaleDown={'uid':dict(downSince=at(9),allowed=allowed,elapsedNanos=(t-9)*1_000_000_000)}))
        events = [snap(1,False),snap(11,True),snap(20,True),
                  dict(at=at(15),node=dict(name='node'),startedAt=at(14),samples=[],
                       failures=[dict(observedAt=at(14.5),reason='Timeout')]),
                  dict(at=at(13),action=dict(id='remove',phase='complete',kind='remove',
                       startedAt=at(12),completedAt=at(13),node=dict(name='node')))]
        with gzip.open(obs/'scheduler.jsonl.gz','wt') as stream:
            stream.write(''.join(json.dumps(r)+'\n' for r in events+events))
        before = {p.relative_to(root):p.read_bytes() for p in root.rglob('*') if p.is_file()}
        data = collect(root)
        assert data['counts'] == {'high':2,'low':2}, data['counts']
        assert data['metrics']['high',name]['success'] == 1
        assert data['metrics']['low',name]['offered'] == 2
        assert data['metrics']['low',name]['missingClientRows'] == 1
        assert data['uids'] == {name:'uid'}
        assert len(data['failures']) == 1 and len(data['actions']) == 1
        assert all(energy(p) is None for p in data['power'].values())
        timings = timing_rows(data)
        assert any(row[2] == 'scaleDown allowed 최초 관측' and row[3] == at(9) and row[4] == at(11) for row in timings)
        assert any(row[2] == '첫 제거 완료' and row[4] == at(13) for row in timings)
        assert percentile([1,2,3,4],.95) == 4 and percentile([],.95) is None
        assert epoch(start) == epoch('2026-01-01T00:00:00+00:00')
        out = Path(temp)/'output'
        report(root,[root],out)
        assert (out/'timeline.svg').stat().st_size > 0
        text = (out/'report-tables.md').read_text()
        assert 'Timeout' in text and '50.00%' in text and 'no exact-part power.json' in text
        assert before == {p.relative_to(root):p.read_bytes() for p in root.rglob('*') if p.is_file()}
        cp.write_text(cp.read_text()+json.dumps(clients[0])+'\n')
        try:
            selected_clients(cp,trace)
        except ValueError as exc:
            assert 'duplicate requestIndex' in str(exc)
        else:
            raise AssertionError('Duplicate client rows accepted')
    print('report self-check: PASS (offline multipart, boundaries, missing/error rows, dedup, gates, power, immutable inputs)')

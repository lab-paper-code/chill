#!/usr/bin/env python3
"""Released, one-shot E5 round orchestration. No implicit measurement retries.

Configuration and resume semantics are documented in hack/README.md.
--self-check is entirely offline and does not require experiment configuration.
"""
import argparse
import base64
import copy
import datetime as dt
import fcntl
import gzip
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace

HERE = Path(__file__).resolve().parent
ROWS = ('E5-3', 'E5-5', 'E5-6', 'E5-7')
ACCOUNTS = {'.token': 'chill-dashboard-user', '.token-collector': 'chill-e5-evidence-collector'}
KAPPA = ['-mode', 'table', '-workloads', 'image-classification-tight-slo,smolvlm2-vqa,plc-anomaly',
         '-margin', '29', '-window', '60', '-windows', '20', '-passing', '19', '-workers', '3']


def load(path):
    return json.loads(Path(path).read_text(), parse_constant=lambda value: fail('Nonfinite JSON: '+value))


def fail(reason):
    raise ValueError(reason)


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, str(path))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def tag(value):
    return isinstance(value, str) and re.fullmatch('[A-Za-z0-9]+', value) is not None


def base_row(row):
    return row[:-1] if row.endswith('m') else row


def validate(c):
    if not isinstance(c, dict):
        fail('Round JSON object required')
    required = ('sourceSHA', 'rows', 'rowTag', 'restoreOriginTag', 'h', 'rateBasis',
                'compareTags', 'environment', 'binding', 'release', 'run', 'evidence',
                'control', 'publication', 'minimumFreeBytes', 'kappa')
    for key in required:
        if key not in c:
            fail('Round requires '+key)
    if not isinstance(c['sourceSHA'], str) or not re.fullmatch('[0-9a-f]{40}', c['sourceSHA']):
        fail('sourceSHA requires a full commit SHA')
    rows = c['rows']
    if not isinstance(rows, list) or not rows or any(not isinstance(r, str) or base_row(r) not in ROWS for r in rows) or len(rows) != len(set(rows)):
        fail('rows requires ordered, unique E5-{3,5,6,7} or micro m rows')
    if not tag(c['rowTag']) or not tag(c['restoreOriginTag']):
        fail('rowTag/restoreOriginTag require ASCII letters and digits')
    if type(c['h']) not in (float, int) or not math.isfinite(c['h']) or not 0 <= c['h'] <= 1:
        fail('h requires a finite fraction in [0,1]')
    if type(c['minimumFreeBytes']) is not int or c['minimumFreeBytes'] < 310_000_000_000:
        fail('minimumFreeBytes must be at least the existing E5 310 GB boundary')
    if not isinstance(c['control'], str) or not c['control'].isdigit():
        fail('control requires decimal digits')
    basis = c['rateBasis']
    if not isinstance(basis, dict) or basis.get('tag') != '3' or 'binding' not in basis:
        fail('rateBasis requires tag "3" and its frozen binding path')
    if not isinstance(c['compareTags'], list) or any(not tag(t) for t in c['compareTags']) or len(set(c['compareTags'])) != len(c['compareTags']):
        fail('compareTags requires unique ASCII tags')
    paths = [c[k] for k in ('environment', 'binding', 'release', 'run', 'evidence', 'publication')] + [basis['binding']]
    kappa = c['kappa']
    if not isinstance(kappa, dict) or set(kappa) not in ({'executable'}, {'reuseRows'}):
        fail('kappa requires exactly executable (prebuilt) or reuseRows')
    paths += list(kappa.values())
    if any(not isinstance(p, str) or not Path(p).is_absolute() for p in paths):
        fail('All configured paths must be absolute')
    if 'harnessSHA' in c and (not isinstance(c['harnessSHA'], str) or not re.fullmatch('[0-9a-f]{40}', c['harnessSHA'])):
        fail('harnessSHA requires a full commit SHA')
    for key in ('takeoverAfter600Seconds', 'failureInjection'):
        if key in c and type(c[key]) is not bool:
            fail(key+' must be boolean')
    if any(base_row(r) == 'E5-7' for r in rows) and c.get('failureInjection') is not True:
        fail('E5-7 requires explicit failureInjection:true operator authorization')
    for key in ('restoreOrigins', 'compareOrigins'):
        mapping = c.get(key, {})
        if not isinstance(mapping, dict):
            fail(key+' must be an object')
        groups = [mapping] if key == 'restoreOrigins' else list(mapping.values())
        if any(not isinstance(g, dict) or any(r not in rows or not isinstance(t, str) or t and not tag(t) for r, t in g.items()) for g in groups):
            fail(key+' requires row-to-origin-tag mappings (empty tag means original unsuffixed row)')
    return c


def steps(rows):
    return ['0', '1', '2', '3', '4'] + [f'5:{row}:{part}' for row in rows for part in ('factor', 'headroom', 'stage', 'judge', 'guard', 'cleanup')] + ['6', '7']


def resume_step(value, rows):
    value = {'preflight': '0', 'deploy': '1', 'reset': '2', 'headroom': '3', 'kappa': '4', 'rows': '5', 'tables': '6', 'finish': '7'}.get(value, value)
    if value == '5':
        value = '5:'+rows[0]+':factor'
    if value.startswith('5:') and value.count(':') == 1:
        value += ':factor'
    if value not in steps(rows):
        fail('Unknown resume step '+value)
    return value


def next_tag(value):
    match = re.fullmatch(r'(.*?)([a-z]*)', value)
    stem, suffix = match.groups()
    if not stem:
        return value+'a'
    chars = list(suffix or '`')
    while chars and chars[-1] == 'z':
        chars.pop()
    return stem+('a'*(len(suffix)+1) if not chars else ''.join(chars[:-1])+chr(ord(chars[-1])+1)+'a'*(len(suffix)-len(chars)))


def sections(path):
    """Read only small top-level judgment fields, never duplicated GB-sized ticks.

    This is a projection of the existing judge, not another latency/D1 reducer.
    Strict e5.save indentation is required; gzip retains historical compressed rows.
    """
    if not path.exists():
        path = Path(str(path)+'.gz')
    if not path.exists():
        return None
    wanted = {'requests', 'power', 'failureScenarioEvidence', 'status'}
    result = {}
    with (gzip.open(path, 'rt') if path.suffix == '.gz' else path.open()) as stream:
        for line in stream:
            match = re.match(r'^  "([^"]+)": (.*)', line)
            if not match or match[1] not in wanted:
                continue
            key, text = match.groups()
            if text == '{':
                chunk = [text+'\n']
                for part in stream:
                    chunk.append(part.rstrip().rstrip(',')+'\n' if part.startswith('  }') else part)
                    if part.startswith('  }'):
                        break
                result[key] = json.loads(''.join(chunk))
            else:
                result[key] = json.loads(text.rstrip().rstrip(','))
            if wanted <= result.keys():
                break
    if 'requests' not in result or 'status' not in result:
        fail('Incomplete judgment sections: '+str(path))
    result['judgmentRef'] = str(path)
    return result


class Round:
    def __init__(self, c, resume=None, advance_tag=False):
        self.c = validate(c)
        self.env = load(c['environment'])
        self.repo = Path(self.env['paths']['repository'])
        self.ops = Path(self.env['paths']['ops'])
        self.run = Path(c['run'])
        self.root = Path(c['evidence'])
        for path in (self.root, self.run):
            if self.ops.resolve() not in path.resolve().parents:
                fail('Evidence and run must remain below external ops')
        if not self.run.is_dir():
            fail('Existing campaign run directory required')
        self.plan = steps(c['rows'])
        self.start = resume_step(resume or '0', c['rows'])
        self.state_path = self.root/'round-state.json'
        if resume:
            self.state = load(self.state_path)
            if self.state['config'] != c:
                fail('Resume must use the identical round JSON; correct inputs in a new round, never rewrite history')
            prior = self.plan[:self.plan.index(self.start)]
            if any(step not in self.state['completed'] for step in prior):
                fail('Resume would skip an incomplete prerequisite')
        else:
            if advance_tag:
                fail('--next-row-tag requires --resume-from')
            self.root.mkdir(parents=True, exist_ok=False)
            self.state = {'schema': 'e5-round-state.v1', 'config': c, 'completed': [], 'rowTags': dict.fromkeys(c['rows'], c['rowTag']), 'measurementRetries': 0}
        self.attempt = self.root/('attempt-'+dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%S%fZ'))
        self.attempt.mkdir()
        self.harness = self.ops/'harness'/('e5-'+c.get('harnessSHA', c['sourceSHA']))
        self.binding = self.run/('binding-round-'+c['rowTag']+'.json')
        self.k = self.env['d2']['kubectl']+['-n', self.env['d2'].get('namespace', 'chill-system'), '--request-timeout=15s']
        self.e5 = None
        self.cpo = None
        self.active_step = self.start
        if advance_tag:
            if not self.start.startswith('5:') or not self.start.endswith(':factor'):
                fail('--next-row-tag requires resume at 5:ROW:factor')
            row = self.start.split(':')[1]
            for r in c['rows'][c['rows'].index(row):]:
                phase = self.folder(r)/'phase.json'
                if phase.exists() and load(phase).get('startUTC'):
                    fail('Measurement already started for '+r+'; a new tag is not retry authorization')
                candidate = next_tag(self.state['rowTags'][r])
                while self.used_tag(r, candidate):
                    candidate = next_tag(candidate)
                self.state['rowTags'][r] = candidate
            self.state['completed'] = [s for s in self.state['completed'] if self.plan.index(s) < self.plan.index(self.start)]
        self.save_state()

    def save(self, name, value):
        path = self.attempt/name
        with path.open('x') as stream:
            json.dump(value, stream, ensure_ascii=False, indent=2, allow_nan=False)
            stream.write('\n')
        return path

    def save_state(self):
        temp = self.state_path.with_suffix('.tmp')
        temp.write_text(json.dumps(self.state, indent=2, allow_nan=False)+'\n')
        temp.replace(self.state_path)

    def folder(self, row):
        return self.run/('full-'+row+'-'+self.state['rowTags'][row])

    def used_tag(self, row, value):
        return any((self.run/name).exists() for name in ('full-'+row+'-'+value, 'full-'+row+'-'+value+'-restore.json', 'factor-full-'+row+'-'+value+'-reset-request.json'))

    def command(self, argv, name, timeout=120, allowed=(0,), watch=None):
        prefix = self.attempt/name
        argv = list(map(str, argv))
        started = now()
        with prefix.with_suffix('.stdout').open('x') as out, prefix.with_suffix('.stderr').open('x') as err:
            p = subprocess.Popen(argv, env=dict(os.environ, CHILL_EXPERIMENT_CONFIG=self.c['environment'], GOMAXPROCS='3', PYTHONDONTWRITEBYTECODE='1'), cwd=self.repo, stdout=out, stderr=err)
            deadline = time.monotonic()+timeout
            try:
                while p.poll() is None:
                    if time.monotonic() >= deadline:
                        raise RuntimeError(name+' exceeded its timebox')
                    if watch:
                        watch()
                    time.sleep(.5)
                if watch:
                    watch()
                if p.returncode not in allowed:
                    raise RuntimeError(name+' exit '+str(p.returncode)+'; see '+str(prefix))
            finally:
                if p.poll() is None:
                    p.terminate()
                    try:
                        p.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        p.kill()
                        p.wait()
                self.save(name+'.command.json', {'argv': argv, 'startedUTC': started, 'endedUTC': now(), 'exitCode': p.returncode})
        return prefix.with_suffix('.stdout')

    def import_harness(self):
        os.environ['CHILL_EXPERIMENT_CONFIG'] = self.c['environment']
        self.e5 = module('round_e5', self.harness/'hack/e5/e5.py')

    def controller(self, row=None, command='factor', row_tag=None):
        if self.e5 is None:
            self.import_harness()
        a = SimpleNamespace(binding=self.binding, release=Path(self.c['release']), out=self.run, command=command, mode='full', stage=row or self.c['rows'][0], row_tag=row_tag or self.state['rowTags'][row or self.c['rows'][0]])
        return self.e5.CPO(a)

    def call(self, command, row, extra=(), **kwargs):
        argv = [sys.executable, '-B', self.harness/'hack/e5/e5.py', command, '--binding', self.binding, '--release', self.c['release'], '--out', self.run, '--mode', 'full', '--stage', row, '--row-tag', self.state['rowTags'][row], *extra]
        return self.command(argv, row+'-'+command, timeout=7200, **kwargs)

    def disk(self, label):
        free = shutil.disk_usage(self.run).free
        self.save(label+'.json', {'at': now(), 'path': str(self.run), 'freeBytes': free, 'minimumFreeBytes': self.c['minimumFreeBytes']})
        if free < self.c['minimumFreeBytes']:
            raise RuntimeError('DISK_GUARD '+label)

    def cluster(self, resource, name):
        return load(self.command(self.k+['get', resource, '-o', 'json'], name, timeout=30))

    def templates(self, name):
        return {d['metadata']['name']: d['spec']['template'] for d in self.cluster('deploy', name)['items']}

    def server_status(self, name):
        pods = self.cluster('pods', name)
        expected = self.publication['roles']['server']
        digests = [expected['digest'], *expected.get('platformDigests', [])]
        statuses = {}
        for pod in pods['items']:
            if pod['metadata'].get('deletionTimestamp'):
                continue
            for container in pod['spec'].get('containers', []):
                if 'chill-serving-entrypoint' not in container['image']:
                    continue
                status = next((s for s in pod.get('status', {}).get('containerStatuses', []) if s['name'] == container['name']), {})
                if not status.get('ready') or not any(x['type'] == 'Ready' and x['status'] == 'True' for x in pod.get('status', {}).get('conditions', [])) or not status.get('imageID', '').endswith(tuple(digests)):
                    raise RuntimeError('Server Ready/digest gate failed')
                statuses[pod['metadata']['uid']+'/'+container['name']] = status.get('restartCount', 0)
        if not statuses or any(statuses.values()):
            raise RuntimeError('Server missing or restartCount not zero')
        return statuses

    def tokens(self, budget):
        auth = module('round_auth', HERE/'auth.py')
        records = {}
        configured = self.env['d2']['tokenFile']
        if Path(configured) != self.ops/'.token':
            fail('Operator token must use the fixed ops/.token service account file')
        for filename, account in ACCOUNTS.items():
            path = self.ops/filename
            try:
                records[filename] = auth.check(path, budget, endpoint=self.env['d2']['apiBase'], ca_file=self.env['d2']['caFile'])
                continue
            except (ValueError, FileNotFoundError):
                pass
            # Credentials never enter command evidence/stdout artifacts. The account is
            # fixed, not inferred from an expired or attacker-controlled JWT subject.
            result = subprocess.run(self.k+['create', 'token', account, '--duration='+str(math.ceil(budget+3600))+'s'], capture_output=True, text=True, timeout=30, check=True)
            credential = result.stdout.strip()
            claims = json.loads(base64.urlsafe_b64decode(credential.split('.')[1]+'==='))
            namespace = self.env['d2'].get('namespace', 'chill-system')
            if claims.get('sub') != 'system:serviceaccount:'+namespace+':'+account:
                fail('Minted token account mismatch')
            backup = Path(str(path)+'.bak')
            if path.exists():
                if backup.exists():
                    backup = Path(str(backup)+'.'+dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%S%fZ'))
                shutil.copy2(path, backup)
                backup.chmod(0o600)
            temp = Path(str(path)+'.new')
            fd = os.open(temp, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            with os.fdopen(fd, 'w') as stream:
                stream.write(credential+'\n')
            temp.replace(path)
            records[filename] = auth.check(path, budget, endpoint=self.env['d2']['apiBase'], ca_file=self.env['d2']['caFile'])
            records[filename].update(renewed=True, serviceAccount=account, backup=str(backup) if backup.exists() else None)
        self.save('token-preflight.json', records)

    def preflight(self):
        self.disk('preflight-disk')
        sha = self.c.get('harnessSHA', self.c['sourceSHA'])
        if not re.fullmatch('[0-9a-f]{40}', sha):
            fail('harnessSHA requires a full commit SHA')
        resolved = self.command(['git', 'rev-parse', '--verify', sha+'^{commit}'], 'harness-commit').read_text().strip()
        if resolved != sha:
            fail('Harness commit mismatch')
        if not self.harness.exists():
            # git archive is extracted in a sibling temporary tree; incomplete copies
            # can never masquerade as the immutable final harness after interruption.
            self.harness.parent.mkdir(parents=True, exist_ok=True)
            with tempfile.TemporaryDirectory(dir=self.harness.parent, prefix='.e5-') as directory:
                archive = Path(directory)/'source.tar'
                self.command(['git', 'archive', '--format=tar', '--output='+str(archive), sha], 'harness-archive')
                source = Path(directory)/'source'
                source.mkdir()
                self.command(['tar', '-xf', archive, '-C', source], 'harness-extract')
                (source/'round-source.json').write_text(json.dumps({'sourceSHA': sha})+'\n')
                source.rename(self.harness)
        tracked = self.command(['git', 'ls-tree', '-r', '--name-only', sha, 'hack/e5'], 'harness-files').read_text().splitlines()
        for index, relative in enumerate(tracked):
            if not relative.endswith('.py'):
                continue
            frozen = self.command(['git', 'show', sha+':'+relative], 'harness-file-'+str(index))
            if frozen.read_bytes() != (self.harness/relative).read_bytes():
                fail('Frozen harness content differs from commit: '+relative)
        self.import_harness()
        b = self.e5.load_binding(self.c['binding'])
        basis = self.e5.load_binding(self.c['rateBasis']['binding'])
        for key in ('payload', 'kappa'):
            if b[key] != basis[key]:
                fail('Frozen R3 '+key+' differs')
        for key in ('seeds', 'arrivals', 'workloads'):
            if b['config'][key] != basis['config'][key]:
                fail('Frozen R3 '+key+' differs')
        schedule = {s['id']: s for s in b['schedule']}
        original = {s['id']: s for s in basis['schedule']}
        for row in self.c['rows']:
            if row not in schedule:
                fail('Binding lacks configured row '+row+' (micro schedules must be bound beforehand)')
            if not row.endswith('m') and schedule[row] != original[row]:
                fail('Frozen R3 schedule differs: '+row)
            if row.endswith('m') and (schedule[row].get('micro') is not True or schedule[row].get('restoreStage') != base_row(row)):
                fail('Micro row requires explicit micro:true and restoreStage')
            prior = self.origin(row)
            if not prior.is_file() or not prior.with_name('pre-settings.json').is_file():
                fail('Original restoration pre-state/pre-settings missing: '+str(prior))
            if self.used_tag(row, self.state['rowTags'][row]):
                fail('Row tag already attempted: '+row+'; preserve it, use explicit resume/new tag before load only')
        self.publication = load(self.c['publication'])
        if self.publication.get('sourceSHA') != self.c['sourceSHA'] or set(self.publication.get('roles', {})) != {'server'} or self.publication.get('runtimeBuilds') != 0:
            fail('Prebuilt server-only publication with matching sourceSHA/runtimeBuilds:0 required')
        entry = self.publication['roles']['server']
        if not re.fullmatch('sha256:[0-9a-f]{64}', entry.get('digest', '')):
            fail('Publication digest missing')
        b['config']['approval']['sources']['server'] = self.c['sourceSHA']
        b['config']['approval']['controlImageDigest'] = entry['image'].split('@')[0].split(':pools-control-')[0]+'@'+entry['digest']
        b['schedulerSettings']['scaleDownHeadroomFraction'] = self.c['h']
        b['roundProvenance'] = {'sourceSHA': self.c['sourceSHA'], 'harnessSHA': sha, 'harness': str(self.harness), 'rateBasis': self.c['rateBasis'], 'measurementRetries': 0}
        for key in b['sources']:
            if key.startswith('package-'):
                b['sources'][key] = self.e5.ref(self.harness/'hack/e5'/key[len('package-'):])
            elif key == 'failureRunbook':
                b['sources'][key] = self.e5.ref(self.harness/'hack/e5/demo-config.e5.example.json')
            elif key.startswith('D2-helper-'):
                b['sources'][key] = self.e5.ref(self.harness/'hack/e5/d2'/key[len('D2-helper-'):])
        release = load(self.c['release'])
        if release.get('bindingPath') != str(self.binding) or release.get('executor') != 'CampaignOps' or release.get('command') != 'e5-execute' or not release.get('mainInstructionUTC') or self.e5.approval_sources(release) != self.e5.approval_sources(b['config']['approval']):
            fail('Main-approved e5-execute release must name generated bindingPath and exact component sources')
        auth = module('round_budget', HERE/'auth.py')
        budget = max(1200, auth.lifetime_budget(b['schedule'], b['config']['timeboxes'], b['config'].get('tokenSafetyMarginSeconds', 60)))
        budget += sum(schedule[r]['seconds']+sum(b['config']['timeboxes'].values()) for r in self.c['rows'])+7200
        self.tokens(budget)
        if self.binding.exists():
            if load(self.binding) != b:
                fail('Generated binding exists with different frozen content')
        else:
            self.binding.write_text(json.dumps(b, indent=2, allow_nan=False)+'\n')
        self.save('preflight.json', {'harness': str(self.harness), 'binding': str(self.binding), 'release': self.c['release'], 'budgetSeconds': budget, 'stageDirectoriesPrecreated': False})

    def origin(self, row):
        default = '' if row == 'E5-3' else self.c['restoreOriginTag']
        suffix = self.c.get('restoreOrigins', {}).get(row, default)
        return self.run/('full-'+base_row(row)+('-'+suffix if suffix else ''))/'pre-state.json'

    def deploy(self):
        self.publication = load(self.c['publication'])
        before = self.templates('deployment-before')
        pods = self.cluster('pods', 'pods-before')
        expected = self.publication['roles']['server']
        servers = [s for p in pods['items'] if not p['metadata'].get('deletionTimestamp') for s in p.get('status', {}).get('containerStatuses', []) if any(c['name'] == s['name'] and 'chill-serving-entrypoint' in c['image'] for c in p['spec'].get('containers', []))]
        same = bool(servers) and all(s.get('imageID', '').endswith(tuple([expected['digest'], *expected.get('platformDigests', [])])) for s in servers)
        if not same:
            path = self.command([self.repo/'hack/fast-deploy.sh', 'upgrade', '--source', self.c['sourceSHA'], '--control', self.c['control'], '--images', 'server', '--prebuilt-images', self.c['publication']], 'fast-deploy', timeout=1500)
            match = re.search(r'^READY: (.+)$', path.read_text(), re.M)
            if not match:
                fail('fast-deploy did not retain a READY evidence path')
            self.state['deploymentEvidence'] = match[1]
        self.restarts = self.server_status('pods-ready')
        after = self.templates('deployment-after')
        normalized = copy.deepcopy(after)
        for name, template in normalized.items():
            if name not in before:
                fail('Unexpected deployment created')
            for container in template['spec'].get('containers', []):
                previous = next(c for c in before[name]['spec'].get('containers', []) if c['name'] == container['name'])
                if 'chill-serving-entrypoint' in previous['image']:
                    container['image'] = previous['image']
        if normalized != before:
            fail('Non-server deployment template changed; chart must remain unchanged')
        self.cpo = self.controller()
        self.save('deployment-api.json', self.cpo.state())
        self.state['deploymentBaseline'] = after
        self.state['serverRestarts'] = self.restarts
        self.save('deployment-baseline.json', after)
        self.save('readiness.json', {'ready': True, 'api': 200, 'restartCount': self.restarts, 'sameDigestSkipped': same, 'sourceSHA': self.c['sourceSHA']})

    def empty(self, label):
        state, settings = self.cpo.state(), self.cpo.settings()
        self.save(label+'-state.json', state)
        self.save(label+'-settings.json', settings)
        snapshot = state['snapshot']
        if snapshot.get('instances') or snapshot.get('reservations') or settings.get('autonomousWorkloads'):
            raise RuntimeError('Final state requires instances0/reservations0/autonomyoff')
        return {'instances': 0, 'reservations': 0, 'autonomous': False, 'h': settings.get('scaleDownHeadroomFraction')}

    def reset(self):
        self.cpo = self.controller(row_tag='round'+self.c['rowTag']+'pre')
        self.cpo.tb = dict(self.cpo.tb, finishResetWaitSeconds=600)
        prefix = 'round-'+self.c['rowTag']+'-initial'
        if (self.run/(prefix+'-reset-request.json')).exists():
            fail('Initial reset already attempted; no implicit repeat')
        try:
            self.cpo.clear_owned(prefix)
        except RuntimeError as error:
            wait = self.run/(prefix+'-reset-wait.json')
            if not self.c.get('takeoverAfter600Seconds') or not wait.exists() or not load(wait).get('timedOut') or self.state.get('takeoverAttempted'):
                raise
            if not any(s['elapsedSeconds'] >= 600 for s in load(wait)['observations']):
                raise
            self.save('pre-takeover-stop.json', {'at': now(), 'error': str(error), 'originalWait': str(wait)})
            self.state['takeoverAttempted'] = True
            self.save_state()
            self.cpo.operation({'kind': 'reset', 'workloadUIDs': self.cpo.uids, 'reason': 'Explicit round JSON authorization: one takeover after retained >600s wait; no measured retry'}, prefix+'-takeover')
        settings = self.cpo.settings()
        settings['autonomousWorkloads'] = []
        self.save('reset-settings-ack.json', self.cpo.put(settings))
        self.empty('initial-empty')

    def headroom(self, row=None):
        self.cpo = self.controller(row)
        settings = self.cpo.settings()
        if row is None:
            settings['autonomousWorkloads'] = []
            settings['scaleDownHeadroomFraction'] = self.c['h']
            self.save('headroom-put-ack.json', self.cpo.put(settings))
            settings = self.cpo.settings()
        self.save((row+'-' if row else '')+'headroom-get.json', settings)
        if settings.get('scaleDownHeadroomFraction') != self.c['h']:
            raise RuntimeError('INVALID_CONFIGURATION headroom '+str(row))

    def kappa(self):
        if 'reuseRows' in self.c['kappa']:
            source = Path(self.c['kappa']['reuseRows'])
            rows = load(source)
            self.save('kappa-reuse.json', {'source': str(source), 'replays': 0, 'sha256': self.e5.ref(source)['sha256']})
        else:
            self.cpo = self.controller()
            scheduler = self.save('scheduler.json', self.cpo.api('GET', '/api/v1/scheduler'))
            output = self.attempt/'kappa-rows.json'
            self.command([self.c['kappa']['executable'], *KAPPA, '-scheduler', scheduler, '-rows', output], 'kappa', timeout=3600)
            rows = load(output)
            self.state['kappaRows'] = str(output)
        if not isinstance(rows, list) or not rows:
            fail('Kappa rows must be a nonempty array')
        per = {}
        for r in rows:
            if r.get('status') == 'Built' and type(r.get('kappa')) in (float, int) and math.isfinite(r['kappa']) and r['kappa'] > 0:
                uid, node = r['input']['workloadUID'], r['input']['node']['name']
                per.setdefault(uid, {})[node] = max(per.get(uid, {}).get(node, 0), r['kappa'])
        if set(per) != {w['uid'] for w in load(self.binding)['config']['workloads']}:
            fail('Kappa lacks positive Built rows for every canonical workload')
        self.save('calibration-result.json', {'replays': int('executable' in self.c['kappa']), 'R': {u: sum(v.values()) for u, v in per.items()}, 'perNode': per, 'status': 'RECORDED_NOT_APPLIED_TO_R3_RATES'})

    def guard(self, label):
        self.disk(label+'-disk')
        restarts = self.server_status(label+'-pods')
        self.save(label+'-restarts.json', restarts)
        if restarts != self.state['serverRestarts']:
            raise RuntimeError('RESTART_COUNT_CHANGED')
        if self.templates(label+'-deployments') != self.state['deploymentBaseline']:
            raise RuntimeError('EXTERNAL_DEPLOYMENT')

    def stage(self, row):
        phase_path = self.folder(row)/'phase.json'
        if self.folder(row).exists():
            fail('Stage evidence already exists; no measurement retry: '+str(self.folder(row)))
        self.cpo = self.controller(row)
        self.guard(row+'-before')
        first = False
        last_poll = 0
        serial = 0
        injected = 0
        selected = None
        stage = next(s for s in load(self.binding)['schedule'] if s['id'] == row)
        runbook = stage.get('failureInjectionRunbook', {})
        injections = runbook.get('steps', [{'offsetSeconds': 180, 'kind': 'delete-serving-pod'}, {'offsetSeconds': 360, 'kind': 'cordon-delete'}, {'offsetSeconds': 600, 'kind': 'uncordon'}]) if base_row(row) == 'E5-7' else []
        if row.endswith('m') and base_row(row) == 'E5-7' and not runbook.get('steps'):
            fail('Micro failure row requires its bound injection steps')
        events_path = self.folder(row)/'failure-events.json'

        def watch():
            nonlocal first, last_poll, serial, injected, selected
            if time.monotonic()-last_poll < 1:
                return
            last_poll = time.monotonic()
            serial += 1
            if serial == 1 or serial % 30 == 0:
                self.guard(row+'-watch-'+str(serial))
            if not phase_path.exists():
                return
            phase = load(phase_path)
            if not phase.get('startUTC') or phase.get('status') != 'RUNNING':
                return
            if not first:
                tick = self.cpo.api('GET', '/api/v1/scheduler')
                decision = tick['state']['decision']
                if self.cpo.d.epoch(decision.get('at')) is not None and self.cpo.d.epoch(decision['at']) >= self.cpo.d.epoch(phase['startUTC']):
                    self.save(row+'-first-load-tick.json', tick)
                    first = True
                    if decision.get('settings', {}).get('scaleDownHeadroomFraction') != self.c['h']:
                        raise RuntimeError('INVALID_CONFIGURATION first load tick h '+row)
            if not injections or injected >= len(injections):
                return
            planned = injections[injected]
            elapsed = time.time()-self.cpo.d.epoch(phase['startUTC'])
            if elapsed < planned['offsetSeconds']:
                return
            kind = planned['kind']
            if kind != 'uncordon':
                self.cpo.a.failure_kind = kind
                self.cpo.failure_select()
                record = load(events_path)
                selected = record['selections'][-1]
                at = now()
                commands = selected['operatorArgv']
            else:
                record = load(events_path)
                at = now()
                commands = [self.k+['uncordon', selected['nodeName']]]
            results = []
            for index, argv in enumerate(commands):
                if kind != 'uncordon' and time.time() >= self.cpo.d.epoch(selected['expiresAtUTC']):
                    raise RuntimeError('Failure selection expired before operator command')
                ref = self.command(argv, row+'-injection-'+str(injected)+'-'+str(index), timeout=30)
                results.append({'argv': argv, 'exitCode': 0, 'stdoutPath': str(ref), 'stderrPath': str(ref.with_suffix('.stderr'))})
            command_ref = self.save(row+'-injection-'+str(injected)+'.json', {'argv': commands[-1], 'commands': commands, 'results': results})
            record['events'].append({'offsetSeconds': planned['offsetSeconds'], 'actualOffsetSeconds': time.time()-self.cpo.d.epoch(phase['startUTC']), 'kind': kind, 'atUTC': at, 'exitCode': 0, 'nodeUID': selected['nodeUID'], 'podUID': selected['podUID'], 'commandRef': str(command_ref), 'responseRef': results[-1]['stdoutPath']})
            record['status'] = 'OPERATOR_RECORDED'
            self.e5.save(events_path, record)
            injected += 1
        try:
            self.call('stage', row, watch=watch)
        finally:
            # Never run cleanup/reset/uncordon after a STOP: preserve physical state
            # and original injection evidence for the explicitly released recovery.
            if injections and injected != len(injections) and events_path.exists():
                self.save(row+'-injection-incomplete.json', {'completedEvents': injected, 'originalEvidence': str(events_path), 'recoveryAttempted': False})
        if not first or injections and injected != len(injections):
            raise RuntimeError('Missing first load tick or failure injection evidence')

    def reports(self):
        tables = module('round_tables', self.harness/'hack/e5/tables.py')
        current = {}
        lines = ['E5 round '+self.c['rowTag']+' — frozen R3 rates; existing raw judgments only.']
        for row in self.c['rows']:
            value = sections(self.folder(row)/'judgment.json')
            current[row] = value
            if not value:
                lines.append(row+': UNMEASURED (no fabricated values)')
                continue
            lines.append(row+': '+value['status'])
            for name, metric in value['requests'].items():
                latency = metric.get('gatewayCompletedLatencyNS') or metric.get('completedLatencyNS') or {}
                p95 = latency.get('p95')
                lines.append(f"{row} {name}: p95MS={p95/1e6 if p95 is not None else 'UNKNOWN'}; {tables.result_counts(metric)}; D1={metric.get('D1', 'UNKNOWN')}")
            lines.append(row+' power: '+tables.energy_text(value.get('power')))
        (self.attempt/('tables-'+self.c['rowTag']+'.txt')).write_text('\n'.join(lines)+'\n')
        for version in self.c['compareTags']:
            rows = []
            comparison = []
            for row in self.c['rows']:
                default = '' if version == '3' and base_row(row) == 'E5-3' else '3b' if version == '3' and base_row(row) == 'E5-7' else version
                origin = self.c.get('compareOrigins', {}).get(version, {}).get(row, default)
                folder = self.run/('full-'+base_row(row)+('-'+origin if origin else ''))
                old = sections(folder/'judgment.json')
                rows.append({'stage': row, 'baseline': old, 'current': current[row], 'status': 'MEASURED_DESCRIPTIVE' if current[row] else 'UNMEASURED', 'micro': row.endswith('m')})
                comparison.append(row+': baseline '+(old['status'] if old else 'UNMEASURED')+' → current '+(current[row]['status'] if current[row] else 'UNMEASURED'))
                if old and current[row]:
                    for name, metric in current[row]['requests'].items():
                        before = old['requests'].get(name)
                        if before is None:
                            continue
                        def latency(value):
                            ns = (value.get('gatewayCompletedLatencyNS') or value.get('completedLatencyNS') or {}).get('p95')
                            return ns/1e6 if ns is not None else None
                        comparison.append(f"{row} {name}: p95MS {latency(before)}→{latency(metric)}; D1 {before.get('D1')}→{metric.get('D1')}; before [{tables.result_counts(before)}]; after [{tables.result_counts(metric)}]")
                    comparison.append(row+' power: '+tables.energy_text(old.get('power'))+' → '+tables.energy_text(current[row].get('power')))
            self.save('comparison-'+self.c['rowTag']+'-vs-'+version+'.json', {'rows': rows, 'measurementRetries': 0, 'judgmentFormulasChanged': False})
            (self.attempt/('comparison-'+self.c['rowTag']+'-vs-'+version+'.txt')).write_text('\n'.join(comparison)+'\n')
        self.state['reports'] = str(self.attempt)

    def finish(self):
        self.cpo = self.controller(row_tag='round'+self.c['rowTag']+'final', command='finish')
        marker = self.run/('finish-round'+self.c['rowTag']+'final-complete.json')
        if not marker.exists():
            self.cpo.finish()
        self.state['finalState'] = self.empty('final')
        self.save('round-summary.json', {'status': 'COMPLETE', 'at': now(), 'sourceSHA': self.c['sourceSHA'], 'rowTags': self.state['rowTags'], 'reports': self.state['reports'], 'finalState': self.state['finalState'], 'measurementRetries': 0, 'meaning': 'Completion is workflow completion, not scientific PASS; finish restores initial settings and may reset h to zero'})
        print(json.dumps({'status': 'COMPLETE', 'summary': str(self.attempt/'round-summary.json'), 'measurementRetries': 0}))

    def execute(self):
        lock = None
        try:
            # fast-deploy owns the same heavy lock during deployment. Measurement
            # acquires it afterward and retains it through final state verification.
            for step in self.plan[self.plan.index(self.start):]:
                self.active_step = step
                if step in self.state['completed']:
                    continue
                self.save('step-'+step.replace(':', '-')+'-start.json', {'at': now(), 'step': step})
                if step == '0':
                    self.preflight()
                elif step == '1':
                    self.deploy()
                else:
                    if lock is None:
                        lock = open('/tmp/chill-heavy.lock', 'a')
                        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                        self.publication = load(self.c['publication'])
                        if self.e5 is None:
                            self.import_harness()
                        self.cpo = self.controller()
                        self.guard('resume-gate')
                    if step == '2': self.reset()
                    elif step == '3': self.headroom()
                    elif step == '4': self.kappa()
                    elif step == '6': self.reports()
                    elif step == '7': self.finish()
                    else:
                        _, row, part = step.split(':')
                        if part == 'factor':
                            if self.used_tag(row, self.state['rowTags'][row]):
                                fail('Full row restoration already attempted; no tag reuse')
                            self.call('factor', row, ['--pre-state', self.origin(row)])
                        elif part == 'headroom': self.headroom(row)
                        elif part == 'stage': self.stage(row)
                        elif part == 'judge': self.call('judge', row, ['--out', self.folder(row)])
                        elif part == 'guard': self.guard(row+'-after')
                        elif part == 'cleanup': self.call('cleanup-jobs', row)
                self.state['completed'].append(step)
                self.save_state()
            return 0
        except BaseException as error:
            self.save('STOP.json', {'status': 'STOP', 'at': now(), 'step': self.active_step, 'reason': str(error), 'measurementRetries': 0, 'physicalRecoveryAttempted': False, 'rowTags': self.state['rowTags']})
            self.state['lastStop'] = {'step': self.active_step, 'reason': str(error), 'evidence': str(self.attempt)}
            self.save_state()
            print('STOP '+self.active_step+': '+str(error)+'; evidence '+str(self.attempt), file=sys.stderr)
            # Offline reports are useful even when a later row was never measured.
            # They live in this attempt, never replace previous raw or reports.
            if self.e5 is not None and self.active_step != '6' and self.state.get('reports') != str(self.attempt):
                try:
                    self.reports()
                except Exception as report_error:
                    self.save('report-error.json', {'error': str(report_error)})
            return 2
        finally:
            if lock:
                lock.close()


def self_check():
    from unittest.mock import patch
    original_load = load
    fixture = dict(sourceSHA='a'*40, rows=['E5-5', 'E5-7', 'E5-6', 'E5-3'], rowTag='8d', restoreOriginTag='223', h=.25, rateBasis={'tag': '3', 'binding': '/ops/r3.json'}, compareTags=['3', '7'], environment='/ops/environment.json', binding='/ops/input.json', release='/ops/release.json', run='/ops/run', evidence='/ops/evidence', control='265', publication='/ops/publication.json', minimumFreeBytes=310_000_000_000, kappa={'reuseRows': '/ops/kappa.json'}, failureInjection=True)
    validate(fixture)
    assert steps(fixture['rows'])[:8] == ['0', '1', '2', '3', '4', '5:E5-5:factor', '5:E5-5:headroom', '5:E5-5:stage']
    assert steps(fixture['rows'])[-2:] == ['6', '7']
    assert resume_step('5:E5-6', fixture['rows']) == '5:E5-6:factor'
    assert [next_tag(t) for t in ('8', '8d', '8z', '8az', 'abc')] == ['8a', '8e', '8aa', '8ba', 'abca']
    for key, value in [('h', True), ('h', float('nan')), ('rows', ['E5-5', 'E5-5']), ('rows', ['E5-4']), ('rowTag', '../8'), ('sourceSHA', 'HEAD'), ('minimumFreeBytes', 1), ('kappa', {'argv': ['go', 'run']}), ('rateBasis', {'tag': '7', 'binding': '/ops/x'}), ('failureInjection', False), ('environment', 'relative')]:
        try:
            validate(dict(fixture, **{key: value}))
        except ValueError:
            pass
        else:
            raise AssertionError('Invalid input accepted: '+key)
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        judgment = {'status': 'INVALID_GENERATOR', 'requests': {'w': {'D1': 'INVALID_GENERATOR'}}, 'power': {'complete': False, 'wholePoolJoules': None}, 'failureScenarioEvidence': None, 'Tick': {'events': [{'huge': 'not read'}]}}
        p = root/'judgment.json'
        p.write_text(json.dumps(judgment, indent=2)+'\n')
        projected = sections(p)
        assert projected['status'] == 'INVALID_GENERATOR' and projected['requests'] == judgment['requests'] and 'Tick' not in projected
        with gzip.open(str(p)+'.gz', 'wt') as stream:
            stream.write(p.read_text())
        p.unlink()
        assert sections(p)['power']['wholePoolJoules'] is None
        c = dict(fixture, rows=['E5-5'], failureInjection=False, environment=str(root/'env.json'), run=str(root/'run'), evidence=str(root/'evidence'))
        Path(c['run']).mkdir()
        Path(c['environment']).write_text(json.dumps({'paths': {'repository': temp, 'ops': temp}, 'd2': {'kubectl': ['offline-kubectl']}}))
        runner = Round(c)
        order = []
        runner.preflight = lambda: order.append('0')
        def deploy():
            order.append('1')
            runner.state.update(deploymentBaseline={}, serverRestarts={})
        runner.deploy = deploy
        runner.import_harness = lambda: None
        runner.controller = lambda *a, **k: None
        runner.guard = lambda label: None
        runner.reset = lambda: order.append('2')
        runner.headroom = lambda row=None: order.append('headroom' if row else '3')
        runner.kappa = lambda: order.append('4')
        runner.used_tag = lambda *a: False
        runner.call = lambda command, row, *a, **k: order.append(command)
        runner.stage = lambda row: order.append('stage')
        runner.reports = lambda: order.append('6')
        runner.finish = lambda: order.append('7')
        with patch.object(fcntl, 'flock'), patch(__name__+'.load', side_effect=lambda path: {} if str(path) == c['publication'] else original_load(path)):
            assert runner.execute() == 0
        assert order == ['0', '1', '2', '3', '4', 'factor', 'headroom', 'stage', 'judge', 'cleanup-jobs', '6', '7']
        c['evidence'] = str(root/'stop-evidence')
        stopped = Round(c)
        stopped.preflight = lambda: (_ for _ in ()).throw(RuntimeError('fixture stop'))
        stopped.reports = lambda: None
        assert stopped.execute() == 2
        assert load(stopped.attempt/'STOP.json')['step'] == '0'
        assert stopped.state['completed'] == []
        try:
            Round(c, resume='7')
        except ValueError:
            pass
        else:
            raise AssertionError('Resume skipped incomplete prerequisites')
        measured = stopped.folder('E5-5')
        measured.mkdir()
        (measured/'phase.json').write_text(json.dumps({'startUTC': '2026-01-01T00:00:00Z'}))
        stopped.state['completed'] = ['0', '1', '2', '3', '4']
        stopped.save_state()
        try:
            Round(c, resume='5:E5-5', advance_tag=True)
        except ValueError:
            pass
        else:
            raise AssertionError('New row tag permitted a measurement retry')
    print('round self-check: PASS (offline ordering/input/STOP/gzip checks; no cluster/build/deploy)')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--round', type=Path)
    parser.add_argument('--resume-from', help='0..7, name, or 5:E5-N[:factor|headroom|stage|judge|guard|cleanup]')
    parser.add_argument('--next-row-tag', action='store_true', help='Explicit fresh tag for a pre-load input correction only; never retry measured rows')
    parser.add_argument('--self-check', action='store_true')
    args = parser.parse_args()
    if args.self_check:
        self_check()
        return 0
    if args.round is None:
        parser.error('--round <JSON> required')
    os.umask(0o077)
    try:
        return Round(load(args.round), args.resume_from, args.next_row_tag).execute()
    except Exception as error:
        print('STOP input: '+str(error), file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())

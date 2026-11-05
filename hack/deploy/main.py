#!/usr/bin/env python3
"""Explicit roll-forward deployment; no reset, rollback, resume, or shell evaluator."""
import argparse
import datetime as dt
import difflib
import fcntl
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import time
import tempfile

import yaml
import acceptance
import api_checks
import images
import image_reuse
import configuration
import inspection
import watch
import inflight
import inventory

ROOT = Path(__file__).resolve().parents[2]
PHASES = ('preflight', 'stage', 'quiesce', 'migrate', 'configure', 'accept')
FIELDS = {'source', 'context', 'kubeconfig', 'namespace', 'release', 'registry', 'registryScheme',
          'builder', 'tag', 'images', 'values', 'replaceValues', 'apiEnvironment', 'expectedRevision',
          'minFreeBytes', 'commands', 'toolSources', 'allowedLostProfileIDs', 'expectedCoverageUsable',
          'apiChecks', 'chartDiffApproved', 'readinessSeconds', 'upgradeSeconds', 'buildSeconds',
          'helm', 'kubectl', 'lock', 'generatedValues', 'maxDiskUsedPercent',
          'queryService', 'dashboardCheck', 'runtimeDeclarations', 'runtimeInspectionSeconds', 'publicationOnly',
          'baselineDirectory', 'expectedProfileCount', 'stagedDiffApproved', 'checkConfiguration', 'schedulerConfigMapFile', 'waitFor',
          'inspectionCells', 'inspectionRequests', 'reusePublication', 'reusePendingBuilds', 'pruneCompletedBuildCache'}


def dump(path, value):
    path.write_text(json.dumps(value, indent=2, default=str) + '\n')


def positive(value, name):
    if type(value) is not int or value <= 0:
        raise ValueError(name + ' must be a positive integer')
    return value


def load(path, source):
    recipe = json.loads(path.read_text())
    if not isinstance(recipe, dict) or set(recipe) - FIELDS:
        raise ValueError('unknown deployment fields: ' + str(set(recipe) - FIELDS))
    for field in ('context', 'kubeconfig', 'namespace', 'release', 'registry', 'apiEnvironment'):
        if not isinstance(recipe.get(field), str) or not recipe[field]:
            raise ValueError(field + ' is required')
    for field in ('namespace', 'release'):
        if not re.fullmatch(r'[a-z0-9][a-z0-9.-]*', recipe[field]):
            raise ValueError('invalid ' + field)
    for field in ('replaceValues', 'dashboardCheck', 'runtimeDeclarations', 'checkConfiguration', 'pruneCompletedBuildCache'):
        if field in recipe and type(recipe[field]) is not bool:
            raise ValueError(field + ' must be boolean')
    positive(recipe.get('expectedRevision'), 'expectedRevision')
    positive(recipe.get('minFreeBytes'), 'minFreeBytes')
    for field, default in (('readinessSeconds', 480), ('upgradeSeconds', 900), ('buildSeconds', 1800)):
        recipe[field] = positive(recipe.get(field, default), field)
    positive(recipe.get('runtimeInspectionSeconds', 3600), 'runtimeInspectionSeconds')
    percent = recipe.get('maxDiskUsedPercent', 85)
    if type(percent) is not int or not 1 <= percent <= 100:
        raise ValueError('maxDiskUsedPercent must be an integer in 1..100')
    if 'queryService' in recipe and (not isinstance(recipe['queryService'], str) or not re.fullmatch(r'[a-z0-9][a-z0-9.-]*', recipe['queryService'])):
        raise ValueError('queryService must name the direct Query Service')
    if not isinstance(recipe.get('images'), list) or not recipe['images']:
        raise ValueError('explicit nonempty images required')
    values = recipe.setdefault('values', [])
    if not isinstance(values, list) or any(not isinstance(x, str) or not x for x in values):
        raise ValueError('values must be a list of file paths')
    if recipe.get('replaceValues') and not values:
        raise ValueError('replaceValues requires complete explicit values')
    generated = recipe.setdefault('generatedValues', [])
    if not isinstance(generated, list) or any(not isinstance(x, str) or not x or Path(x).is_absolute() or '..' in Path(x).parts for x in generated):
        raise ValueError('generatedValues must name evidence-relative files')
    if 'reusePublication' in recipe and (not isinstance(recipe['reusePublication'], str) or not recipe['reusePublication']):
        raise ValueError('reusePublication must name an existing publication receipt')
    for field in ('kubeconfig', 'apiEnvironment', 'chartDiffApproved', 'stagedDiffApproved', 'baselineDirectory', 'inspectionCells', 'inspectionRequests', 'reusePublication'):
        if field in recipe:
            recipe[field] = str(Path(recipe[field]).expanduser().resolve())
    recipe['values'] = [str(Path(x).expanduser().resolve()) if Path(x).is_absolute() else x for x in values]
    commands = recipe.setdefault('commands', {})
    tools = recipe.setdefault('toolSources', {})
    if not isinstance(tools, dict) or any(not re.fullmatch(r'[a-z][a-z0-9-]*', k) or not isinstance(v, str) for k, v in tools.items()):
        raise ValueError('toolSources requires named git revisions')
    if not isinstance(commands, dict) or set(commands) - set(PHASES):
        raise ValueError('commands supports only ' + ','.join(PHASES))
    for phase, entries in commands.items():
        if not isinstance(entries, list):
            raise ValueError('phase commands must be lists')
        for step in entries:
            if not isinstance(step, dict) or set(step) - {'argv', 'seconds', 'source'}:
                raise ValueError('command requires argv/seconds and optional tool source')
            argv = step.get('argv')
            if not isinstance(argv, list) or not argv or any(not isinstance(x, str) or not x or '\x00' in x for x in argv):
                raise ValueError('command argv must be nonempty strings')
            positive(step.get('seconds'), phase + ' seconds')
            if step.get('source', 'deployment') not in {'deployment', *tools}:
                raise ValueError('unknown command source')
    if any(commands.get(p) for p in ('quiesce', 'migrate', 'configure')) and not commands.get('accept'):
        raise ValueError('state-changing deployment commands require explicit accept commands')
    roles = [x if isinstance(x, str) else x.get('role', '') if isinstance(x, dict) else '' for x in recipe['images']]
    if 'reusePendingBuilds' in recipe:
        pending = recipe['reusePendingBuilds']
        if (not isinstance(pending, dict) or not set(pending) <= set(roles)
                or any(not isinstance(value, str) or not value for value in pending.values())):
            raise ValueError('reusePendingBuilds must map selected image roles to metadata paths')
        if not recipe.get('reusePublication'):
            raise ValueError('reusePendingBuilds requires reusePublication')
        recipe['reusePendingBuilds'] = {role: str(Path(value).expanduser().resolve()) for role, value in pending.items()}
    published_only = recipe.setdefault('publicationOnly', [])
    if not isinstance(published_only, list) or not set(published_only) <= set(roles):
        raise ValueError('publicationOnly must explicitly name selected roles')
    unmapped = [x for x in roles if x.startswith(('firstfloor', 'evaluation-harness')) and x not in published_only]
    if unmapped and not recipe.get('runtimeDeclarations') and (not commands.get('stage') or not commands.get('accept') or not generated):
        raise ValueError('runtime/harness activation requires stage, generatedValues and accept; use explicit publicationOnly for unactivated builds')
    if recipe.get('runtimeDeclarations'):
        required = {'firstfloor', 'firstfloor-cuda', 'firstfloor-l4t-r32', 'firstfloor-l4t-r35', 'firstfloor-l4t-r36', 'firstfloor-rknn'}
        if not required <= set(roles) or required & set(published_only):
            raise ValueError('runtimeDeclarations requires all six runtime roles activated together')
    lost = recipe.setdefault('allowedLostProfileIDs', [])
    if not isinstance(lost, list) or any(not isinstance(x, str) or not x for x in lost) or len(set(lost)) != len(lost):
        raise ValueError('allowedLostProfileIDs requires unique IDs')
    expected = recipe.get('expectedCoverageUsable')
    if expected is not None and (type(expected) is not int or expected < 0):
        raise ValueError('expectedCoverageUsable must be nonnegative')
    if 'expectedProfileCount' in recipe:
        positive(recipe['expectedProfileCount'], 'expectedProfileCount')
    checks = recipe.setdefault('apiChecks', [])
    if not isinstance(checks, list):
        raise ValueError('apiChecks must be a list')
    for check in checks:
        if not isinstance(check, dict) or set(check) != {'path', 'maxBytes'} or not isinstance(check['path'], str) or not check['path'].startswith('/api/v1/') or '\r' in check['path'] or '\n' in check['path']:
            raise ValueError('apiChecks requires relative /api/v1/ path and maxBytes')
        positive(check['maxBytes'], 'maxBytes')
    waiting = recipe.setdefault('waitFor', [])
    if not isinstance(waiting, list) or any(not isinstance(x, str) or not re.fullmatch(r'(deployment|daemonset)/[a-z0-9][a-z0-9.-]*', x) for x in waiting):
        raise ValueError('waitFor must list named deployment/daemonset resources')
    if 'schedulerConfigMapFile' in recipe and (not isinstance(recipe['schedulerConfigMapFile'], str) or not recipe['schedulerConfigMapFile']):
        raise ValueError('schedulerConfigMapFile must name the explicitly staged ConfigMap')
    sha = subprocess.check_output(['git', 'rev-parse', '--verify', (source or recipe.get('source', '')) + '^{commit}'], cwd=ROOT, text=True).strip()
    tools = {k: subprocess.check_output(['git', 'rev-parse', '--verify', v + '^{commit}'], cwd=ROOT, text=True).strip() for k, v in tools.items()}
    recipe['source'], recipe['toolSources'] = sha, tools
    recipe['sourceRepository'] = str(ROOT)
    return recipe


def plan_images(recipe, source, out):
    preflight = bool(recipe['commands'].get('preflight'))
    if preflight and recipe.get('reusePendingBuilds'):
        raise ValueError('reusePendingBuilds cannot be used with custom preflight commands')
    planned = image_reuse.apply(images.plan(recipe, recipe['source'], source, out),
                                recipe['source'], ROOT, source,
                                None if preflight else recipe.get('reusePublication'),
                                pending_builds=recipe.get('reusePendingBuilds'))
    if preflight:
        for item in planned:
            if item['action'] == 'build':
                item['reason'] = 'custom preflight commands may modify archived build inputs'
    return planned


class Runner:
    def __init__(self, out, cwd, environment=None):
        self.out, self.cwd, self.environment = out, cwd, environment
        self.log = (out / 'commands.log').open('a', buffering=1)
        self.number = 0
        self.stage = 'archive'
        self.timings = []

    def run(self, argv, name, timeout=120, cwd=None, env=None, read_output=True):
        self.number += 1
        argv = list(map(str, argv))
        stdout = self.out / ('%03d-' % self.number + name)
        stderr = stdout.with_name(stdout.name + '.stderr')
        self.log.write('+ ' + shlex.join(argv) + '\n')
        started = time.monotonic()
        record = {'number': self.number, 'stage': self.stage, 'name': name, 'startedUTC': dt.datetime.now(dt.timezone.utc).isoformat(), 'timeoutSeconds': timeout, 'exitCode': None}
        try:
            record['exitCode'] = watch.command(
                argv, stdout, stderr, cwd=cwd or self.cwd, env=env or self.environment,
                timeout=timeout, label=stdout.name, stream=stdout.suffix in {'.log', '.txt'})
            self.log.write('exit=%d evidence=%s\n' % (record['exitCode'], stdout.name))
            if record['exitCode']:
                raise RuntimeError('command failed (%d): %s' % (record['exitCode'], stdout))
            return stdout.read_bytes() if read_output else stdout
        finally:
            record['elapsedSeconds'] = time.monotonic() - started
            record['stdoutBytes'] = stdout.stat().st_size if stdout.exists() else 0
            record['stderrBytes'] = stderr.stat().st_size if stderr.exists() else 0
            self.timings.append(record)
            with (self.out / 'command-timings.jsonl').open('a') as stream:
                stream.write(json.dumps(record) + '\n')


def objects(raw):
    result = []
    for item in yaml.safe_load_all(raw):
        if not isinstance(item, dict):
            continue
        result.append(item)
    return sorted(result, key=lambda x: (x['apiVersion'], x['kind'], x['metadata'].get('namespace', ''), x['metadata']['name']))


def difference(live, desired):
    return ''.join(difflib.unified_diff((json.dumps(objects(live), sort_keys=True, indent=2) + '\n').splitlines(True),
                                     (json.dumps(objects(desired), sort_keys=True, indent=2) + '\n').splitlines(True), fromfile='live', tofile='source-chart'))


def crd_retention(original, desired, kube, run, out):
    old = {x['metadata']['name'] for x in objects(original) if x['kind'] == 'CustomResourceDefinition'}
    new = {x['metadata']['name'] for x in objects(desired) if x['kind'] == 'CustomResourceDefinition'}
    receipt = {'owner': 'Helm chart only', 'removedFromChart': sorted(old - new), 'kept': []}
    for name in receipt['removedFromChart']:
        current = json.loads(run([*kube, 'get', 'crd', name, '-o', 'json'], 'crd-retention-' + name + '.json'))
        # Helm 3 upgrade fetches the live object's annotations before deletion.
        if current['metadata'].get('annotations', {}).get('helm.sh/resource-policy') != 'keep':
            dump(out / 'crd-retention.json', receipt)
            raise ValueError('refusing CRD data loss: ' + name + ' leaves the chart without live helm.sh/resource-policy=keep')
        receipt['kept'].append(name)
    dump(out / 'crd-retention.json', receipt)
    return receipt


def archive(run, sha, destination):
    destination.mkdir()
    tar = destination.with_suffix('.tar')
    run.run(['git', '-c', 'core.attributesFile=/dev/null', '-C', ROOT, 'archive', '--format=tar', '--output=' + str(tar), sha], 'archive.log', read_output=False)
    # -p: the private evidence umask must not strip image file modes for non-root UIDs.
    run.run(['tar', '-xpf', tar, '-C', destination], 'extract.log', read_output=False)


def image_ready(pods, expected):
    matches = []
    for pod in pods['items']:
        if pod['metadata'].get('deletionTimestamp'):
            continue
        statuses = {x['name']: x for x in pod.get('status', {}).get('containerStatuses', [])}
        for container in pod['spec'].get('containers', []):
            if container['image'] != expected['image']:
                continue
            status = statuses.get(container['name'], {})
            digests = [expected['digest'], *expected['platformDigests']]
            matches.append(status.get('ready') and status.get('restartCount') == 0 and
                           any(status.get('imageID', '').endswith('@' + d) or status.get('imageID') == d for d in digests) and
                           any(x['type'] == 'Ready' and x['status'] == 'True' for x in pod.get('status', {}).get('conditions', [])))
    return bool(matches) and all(matches)


def deploy(recipe, out, inspect=False, approval=None, staged_approval=None):
    os.umask(0o077)
    out.mkdir(parents=True, exist_ok=False)
    dump(out / 'recipe.json', recipe)
    src = out / 'source'
    env = dict(os.environ, KUBECONFIG=recipe['kubeconfig'], CHILL_DEPLOY_CONTEXT=recipe['context'],
               CHILL_DEPLOY_NAMESPACE=recipe['namespace'], CHILL_DEPLOY_RELEASE=recipe['release'],
               CHILL_DEPLOY_EVIDENCE=str(out), CHILL_EXPERIMENT_CONFIG=recipe['apiEnvironment'], GIT_ATTR_NOSYSTEM='1')
    run = Runner(out, ROOT, env)
    kube = [recipe.get('kubectl', 'kubectl'), '--kubeconfig', recipe['kubeconfig'], '--context', recipe['context'], '-n', recipe['namespace']]
    helm = [recipe.get('helm', 'helm'), '--kubeconfig', recipe['kubeconfig'], '--kube-context', recipe['context'], '-n', recipe['namespace']]
    state = {'sourceSHA': recipe['source'], 'stage': 'archive', 'ready': False, 'mode': 'inspect' if inspect else 'execute'}
    dump(out / 'state.json', state)
    lock = None
    observed_cluster = False
    started = phase_started = time.monotonic()
    phases = []
    print('DEPLOY mode=%s source=%s evidence=%s' % (state['mode'], recipe['source'], out), flush=True)
    print('PHASE archive BEGIN', flush=True)

    def stage(name):
        nonlocal phase_started
        instant = time.monotonic()
        print('PHASE %s DONE elapsed=%.1fs total=%.1fs' %
              (state['stage'], instant - phase_started, instant - started), flush=True)
        phases.append({'stage': state['stage'], 'elapsedSeconds': instant - phase_started})
        phase_started = instant
        run.stage = name
        state['stage'] = name
        state['updatedUTC'] = dt.datetime.now(dt.timezone.utc).isoformat()
        dump(out / 'state.json', state)
        print('PHASE %s %s total=%.1fs' %
              (name, 'DONE' if name == 'complete' else 'BEGIN', instant - started), flush=True)

    def disk():
        usage = shutil.disk_usage(out)
        dump(out / 'disk.json', {'total': usage.total, 'used': usage.used, 'free': usage.free, 'minimumFreeBytes': recipe['minFreeBytes'], 'maxUsedPercent': recipe.get('maxDiskUsedPercent', 85)})
        if usage.free < recipe['minFreeBytes'] or usage.used * 100 >= usage.total * recipe.get('maxDiskUsedPercent', 85):
            raise RuntimeError('insufficient local disk headroom; no unrelated cache/volume prune')

    sources = {'deployment': src}

    def commands(phase):
        if recipe['commands'].get(phase) or (phase == 'preflight' and any(recipe['commands'].values())):
            selected = run.run([recipe.get('kubectl', 'kubectl'), '--kubeconfig', recipe['kubeconfig'], 'config', 'current-context'], 'phase-context.txt').decode().strip()
            if selected != recipe['context']:
                raise ValueError('custom phase KUBECONFIG current-context differs from the pinned deployment context')
        for index, step in enumerate(recipe['commands'].get(phase, [])):
            replacements = {'{evidence}': str(out), '{source}': str(src), '{namespace}': recipe['namespace'], '{release}': recipe['release']}
            for name, directory in sources.items():
                replacements['{tool:' + name + '}'] = str(directory)
            argv = step['argv'][:]
            for key, value in replacements.items():
                argv = [x.replace(key, value) for x in argv]
            if any(re.search(r'\{(?:evidence|source|namespace|release|tool:)[^}]*\}', x) for x in argv):
                raise ValueError('unresolved deployment command placeholder')
            run.run(argv, phase + '-%d.log' % index, timeout=step['seconds'], cwd=sources[step.get('source', 'deployment')], read_output=False)

    def check_configuration(manifest):
        if not recipe.get('checkConfiguration'):
            return
        selected = recipe.get('schedulerConfigMapFile')
        if selected:
            selected = selected.replace('{source}', str(src)).replace('{evidence}', str(out))
        state['configuration'] = configuration.validate(src, out, manifest, kube, run.run, scheduler_file=selected)

    def revision():
        nonlocal observed_cluster
        observed_cluster = True
        entries = json.loads(run.run(helm + ['list', '-o', 'json'], 'helm-list.json'))
        release = [x for x in entries if x['name'] == recipe['release']]
        if len(release) != 1 or int(release[0]['revision']) != recipe['expectedRevision'] or release[0]['status'] != 'deployed':
            raise RuntimeError('live release/revision/status changed; review before roll-forward')

    try:
        if not inspect:
            lock = open(recipe.get('lock', '/tmp/chill-heavy.lock'), 'a')
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        disk()
        archive(run, recipe['source'], src)
        for name, sha in recipe['toolSources'].items():
            directory = out / ('tool-' + name)
            archive(run, sha, directory)
            sources[name] = directory
        run.cwd = src
        stage('plan-images')
        planned = plan_images(recipe, src, out)
        dump(out / 'image-plan.json', planned)
        state['images'] = [{'role': item['role'], 'action': item['action'], 'reason': item['reason']}
                           for item in planned]
        for item in state['images']:
            print('IMAGE {role}: {action} — {reason}'.format(**item), flush=True)
        stage('preflight')
        revision()
        state['inflightPreflight'] = inflight.check(kube, run.run, out, 'inflight-preflight')
        if recipe.get('runtimeDeclarations'):
            import runtime
            state['probeNodesPreflight'] = runtime.check_nodes(src, out, kube, run.run, 'preflight')
        config = api_checks.load_config(recipe['apiEnvironment'])
        upgrade_process_seconds = recipe['upgradeSeconds'] + 30
        budget = 120 + recipe['readinessSeconds'] + upgrade_process_seconds + len(recipe['images']) * recipe['buildSeconds'] + sum(x['seconds'] for steps in recipe['commands'].values() for x in steps)
        budget += (recipe.get('runtimeInspectionSeconds', 3600) + 2 * recipe['readinessSeconds'] if recipe.get('runtimeDeclarations') else 0) + 60 * (len(recipe['apiChecks']) + 2)
        budget += 360 if recipe.get('checkConfiguration') else 0
        if recipe.get('pruneCompletedBuildCache', False):
            budget += 120 * sum(item['action'] == 'build' for item in planned)
        if not inspect:
            api_checks.preflight(config, budget, out)
            commands('preflight')
        stage('backup')
        run.run(helm + ['history', recipe['release'], '-o', 'json'], 'helm-history.json', read_output=False)
        live_values = run.run(helm + ['get', 'values', recipe['release'], '-o', 'json'], 'live-values.json')
        (out / 'live-values.json').write_bytes(live_values)
        live = run.run(helm + ['get', 'manifest', recipe['release']], 'live-manifest.yaml')
        hooks = run.run(helm + ['get', 'hooks', recipe['release']], 'live-hooks.yaml')
        if not inspect:
            state['crdBackup'] = inventory.capture_crds(live, hooks, recipe['namespace'], kube, run.run)
            for resource, name in [('configmaps', 'configmaps.json'), ('deployments,daemonsets,services,serviceaccounts,roles,rolebindings', 'resources.json')]:
                run.run(kube + ['get', resource, '-o', 'json'], name, read_output=False)
        flags = [] if recipe.get('replaceValues') else ['-f', str(out / 'live-values.json')]
        for index, value in enumerate(recipe['values']):
            path = Path(value) if Path(value).is_absolute() else src / value
            target = out / ('values-%d.yaml' % index)
            target.write_bytes(path.read_bytes())
            flags += ['-f', str(target)]
        chart = src / 'charts/chill'
        render = helm + ['template', recipe['release'], str(chart), *flags, '--is-upgrade']
        stage('render-review')
        before = run.run(render, 'render-before.yaml')
        (out / 'render-before.yaml').write_bytes(before)
        chart_diff = difference(live + b'\n---\n' + hooks, before)
        (out / 'chart-diff.txt').write_text(chart_diff)
        approved = approval or recipe.get('chartDiffApproved')
        if chart_diff and (not approved or Path(approved).read_text() != chart_diff):
            if inspect:
                state['chartApproved'] = False
                print('REVIEW: exact chart-diff.txt approval required; ' + str(out))
            else:
                raise RuntimeError('chart drift requires exact --chart-diff-approved file; inspect first')
        if approved:
            (out / 'chart-diff-approved.txt').write_text(Path(approved).read_text())
        frozen = recipe.get('baselineDirectory')
        if frozen:
            baseline = acceptance.load_baseline(frozen)
            current = acceptance.capture(config, recipe['namespace'], out / 'before')
            report = acceptance.compare(
                baseline, current, recipe['allowedLostProfileIDs'], out / 'pre-deploy-acceptance',
                recipe.get('expectedCoverageUsable'), recipe.get('expectedProfileCount'))
            dump(out / 'pre-deploy-acceptance/acceptance.json', report)
            state['preDeployDiagnostics'] = report
            frozen_out = out / 'frozen-baseline'
            frozen_out.mkdir()
            for name in ('profiles.json', 'coverage.json'):
                (frozen_out / name).write_bytes((Path(frozen) / name).read_bytes())
        if inspect:
            for item in planned:
                if item['action'] == 'reuse':
                    images.verify(item, item['reuse']['sourceSHA'], item['reuse']['receipt']['digest'], run.run)
                elif item['action'] == 'verify-pending':
                    pending = item['pendingPublication']
                    images.verify(item, pending['sourceSHA'], pending['digest'], run.run)
            check_configuration(out / 'render-before.yaml')
            if recipe.get('inspectionCells'):
                if not frozen:
                    acceptance.capture(config, recipe['namespace'], out / 'before')
                requests = out / 'inspection-requests.json'
                requests.write_bytes(run.run(kube + ['get', 'batchenergyoptimizations', '-o', 'json'], 'inspection-requests.json'))
                state['coverageProjection'] = inspection.project(recipe, src, out, run.run, out / 'before/profiles.json', out / 'before/coverage.json', requests)
                print('COVERAGE PROJECTION: ' + json.dumps({key: state['coverageProjection'][key] for key in ('profileRecordsInput', 'countsByDisplayStatus', 'selectedCells', 'currentFailedMeasurements')}))
            state['crdRetention'] = crd_retention(live, before, kube, run.run, out)
            state.setdefault('chartApproved', True)
            stage('complete')
            dump(out / 'state.json', state)
            print('INSPECTED (no image publication/cluster writes; local Go helpers may compile): ' + str(out))
            return
        if not frozen:
            baseline = acceptance.capture(config, recipe['namespace'], out / 'before')
        current_before = current if frozen else baseline
        stage('publish-images')
        builders = sorted({item['builder'] for item in planned if item['action'] == 'build'})
        for builder in builders:
            run.run(['docker', 'buildx', 'du', '--builder', builder], 'build-cache-' + builder + '-before.txt', read_output=False)
        def build_run(argv, name, timeout=120):
            disk()
            argv = list(map(str, argv))
            is_build = argv[:3] == ['docker', 'buildx', 'build']
            result = run.run(argv, name, timeout=recipe['buildSeconds'] if is_build else timeout,
                             read_output=not is_build)
            if is_build:
                if recipe.get('pruneCompletedBuildCache', False):
                    builder = argv[argv.index('--builder') + 1]
                    run.run(['docker', 'buildx', 'prune', '--builder', builder, '--force', '--filter', 'inuse=false'],
                            name + '-cache-prune.txt', read_output=False)
                disk()
            return result
        publication = images.build(planned, recipe['source'], src, out, build_run)
        for builder in builders:
            run.run(['docker', 'buildx', 'du', '--builder', builder], 'build-cache-' + builder + '-after.txt', read_output=False)
        dump(out / 'publication.json', publication)
        overlay = images.overlay(publication)
        stage('stage')
        commands('stage')
        trusted_flags = [*flags, '-f', str(out / 'image-overlay.json')]
        if recipe.get('runtimeDeclarations'):
            import runtime
            import quiescence
            with quiescence.gpu_probes(config, state['probeNodesPreflight']['nodes'],
                                       out, kube, run.run, recipe['readinessSeconds']):
                declaration_values = runtime.stage(publication, src, out, kube, run.run, timeout=recipe.get('runtimeInspectionSeconds', 3600))
            trusted_flags += ['-f', str(declaration_values)]
        dump(out / 'image-overlay.json', overlay)
        final_flags = list(trusted_flags)
        for index, value in enumerate(recipe['generatedValues']):
            staged = out / value
            frozen = out / ('staged-values-%d.yaml' % index)
            frozen.write_bytes(staged.read_bytes())
            final_flags += ['-f', str(frozen)]
        after = run.run(helm + ['template', recipe['release'], str(chart), *final_flags, '--is-upgrade'], 'render-final.yaml')
        (out / 'render-final.yaml').write_bytes(after)
        if recipe['generatedValues']:
            trusted = run.run(helm + ['template', recipe['release'], str(chart), *trusted_flags, '--is-upgrade'], 'render-trusted.yaml')
            staged_diff = difference(trusted, after)
            (out / 'staged-diff.txt').write_text(staged_diff)
            staged_approved = staged_approval or recipe.get('stagedDiffApproved')
            if staged_diff and (not staged_approved or Path(staged_approved).read_text() != staged_diff):
                raise ValueError('unapproved staged configuration change; exact --staged-diff-approved required before quiesce')
            if staged_approved:
                (out / 'staged-diff-approved.txt').write_text(Path(staged_approved).read_text())
        # Explicit stage outputs are frozen before writers stop or data contracts change.
        (out / 'render-diff.txt').write_text(difference(live + b'\n---\n' + hooks, after))
        check_configuration(out / 'render-final.yaml')
        stage('crd-retention')
        state['crdRetention'] = crd_retention(live, after, kube, run.run, out)
        run.run(kube + ['apply', '--dry-run=server', '-f', out / 'render-final.yaml'], 'server-dry-run.log', read_output=False)
        revision()
        api_checks.preflight(config, 120 + recipe['readinessSeconds'] + upgrade_process_seconds + 60 * (len(recipe['apiChecks']) + 2) + sum(x['seconds'] for p in ('quiesce', 'migrate', 'configure', 'accept') for x in recipe['commands'].get(p, [])), out)
        for phase in ('quiesce', 'migrate'):
            stage(phase)
            commands(phase)
        stage('configure')
        commands('configure')
        revision()
        state['inflightPreUpgrade'] = inflight.check(kube, run.run, out, 'inflight-pre-upgrade')
        stage('upgrade')
        run.run(helm + ['upgrade', recipe['release'], chart, '--reset-values', *final_flags, '--wait', '--timeout', str(recipe['upgradeSeconds']) + 's'], 'upgrade.log', timeout=upgrade_process_seconds, read_output=False)
        stage('readiness')
        deadline = time.monotonic() + recipe['readinessSeconds']
        for resource in recipe['waitFor']:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RuntimeError('controller rollout readiness deadline exceeded')
            run.run(kube + ['rollout', 'status', resource, '--timeout=' + str(remaining) + 's'], 'rollout-' + resource.replace('/', '-') + '.log', timeout=remaining, read_output=False)
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RuntimeError('Pod Ready/restarts/digest readiness deadline exceeded')
            pods = json.loads(run.run(kube + ['get', 'pods', '-o', 'json'], 'pods.json', timeout=min(120, remaining)))
            deployed = {r: e for r, e in publication['roles'].items() if r in ('server', 'operator', 'profiler', 'dashboard', 'query', 'node-agent')}
            checks = {role: image_ready(pods, expected) for role, expected in deployed.items()}
            dump(out / 'readiness.json', checks)
            if all(checks.values()):
                break
            time.sleep(min(5, max(0, deadline - time.monotonic())))
        stage('accept')
        accept_out = out / 'after'
        import evaluation_rbac
        state['evaluationRbac'] = evaluation_rbac.check(
            after.decode(), recipe['namespace'], kube, run.run, out / 'evaluation-rbac.json')
        snapshot = acceptance.wait_capture(config, recipe['namespace'], accept_out, deadline)
        report = acceptance.compare(
            baseline, snapshot, recipe['allowedLostProfileIDs'], accept_out,
            recipe.get('expectedCoverageUsable'), recipe.get('expectedProfileCount'))
        dump(accept_out / 'acceptance.json', report)
        state['diagnostics'] = report
        if not recipe['commands'].get('migrate'):
            state['retainedRecords'] = acceptance.compare_records(current_before, snapshot, out / 'retained-records')
        else:
            state['retainedRecords'] = {'checked': False, 'reason': 'explicit migration accept commands own the approved stored-contract transition'}
        api_checks.check_routes(config, recipe['apiChecks'], out / 'api-checks')
        if recipe.get('queryService'):
            api_checks.check_query(config, kube, recipe['namespace'], out / 'query-auth', query_service=recipe['queryService'])
        if recipe.get('dashboardCheck'):
            api_checks.check_dashboard(config, out / 'dashboard')
        commands('accept')
        run.run(helm + ['get', 'values', recipe['release'], '-o', 'json'], 'post-values.json', read_output=False)
        stage('complete')
        state['ready'] = True
        dump(out / 'state.json', state)
        print('DEPLOY COMPLETE elapsed=%.1fs' % (time.monotonic() - started), flush=True)
        print('READY: ' + str(out), flush=True)
    except BaseException as error:
        state.update(error=str(error), ready=False, disposition='STOP; retain evidence, fix and deploy a reviewed roll-forward; no automatic deployment retry/rollback; temporary probe settings restoration recorded separately')
        dump(out / 'failure.json', state)
        print('DEPLOY STOP phase=%s elapsed=%.1fs type=%s evidence=%s' %
              (state['stage'], time.monotonic() - started, type(error).__name__, out), flush=True)
        if observed_cluster:
            collectors = [(helm + ['history', recipe['release'], '-o', 'json'], 'failure-history.json'),
                          (kube + ['get', 'pods,deployments,daemonsets', '-o', 'json'], 'failure-resources.json')]
            for component in ('operator', 'profiler', 'query', 'serving-entrypoint', 'dashboard'):
                for previous in (False, True):
                    collectors.append((kube + ['logs', 'deployment/' + recipe['release'] + '-' + component, '--all-containers', '--tail=100', '--timestamps', *(['--previous'] if previous else [])], 'failure-' + component + ('-previous' if previous else '') + '.log'))
            for argv, name in collectors:
                try:
                    run.run(argv, name, timeout=20, read_output=False)
                except BaseException:
                    pass
        raise
    finally:
        phases.append({'stage': state['stage'], 'elapsedSeconds': time.monotonic() - phase_started})
        dump(out / 'performance.json', {'elapsedSeconds': time.monotonic() - started, 'phases': phases, 'commands': run.timings, 'slowestCommands': sorted(run.timings, key=lambda x: x['elapsedSeconds'], reverse=True)[:10]})
        run.log.close()
        if lock:
            lock.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('recipe', type=Path)
    parser.add_argument('--source', help='immutable deployment commit; overrides recipe source')
    parser.add_argument('--evidence', type=Path, help='new evidence directory; existing directories are never resumed')
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument('--dry-run', action='store_true', help='default: local plan/projection, no network/image builds/lock/persistent evidence writes')
    mode.add_argument('--inspect', action='store_true', help='read-only cluster/chart inspection, no custom phase commands or publication')
    mode.add_argument('--execute', action='store_true', help='main-only explicit cluster mutation/publication authorization')
    parser.add_argument('--chart-diff-approved', type=Path)
    parser.add_argument('--staged-diff-approved', type=Path)
    args = parser.parse_args()
    try:
        recipe = load(args.recipe, args.source)
        if not args.execute and not args.inspect:
            print('LOCAL PLAN ONLY; no cluster/network/image builds/lock/persistent evidence writes; optional Go projection compiles locally')
            with tempfile.TemporaryDirectory(prefix='chill-deploy-plan-') as temporary:
                directory = Path(temporary)
                tar = directory / 'source.tar'
                subprocess.run(['git', '-c', 'core.attributesFile=/dev/null', 'archive', '--format=tar', '--output=' + str(tar), recipe['source']], cwd=ROOT, env=dict(os.environ, GIT_ATTR_NOSYSTEM='1'), check=True)
                src = directory / 'source'
                src.mkdir()
                subprocess.run(['tar', '-xf', tar, '-C', src], check=True)
                planned = plan_images(recipe, src, Path('/PLAN'))
                projection = None
                if recipe.get('inspectionCells'):
                    if not recipe.get('inspectionRequests') or not recipe.get('baselineDirectory'):
                        raise ValueError('local projection requires inspectionRequests and baselineDirectory captured inputs')
                    def local_run(argv, name, timeout=120, cwd=None, env=None):
                        return subprocess.check_output(argv, timeout=timeout, cwd=cwd, env=env)
                    frozen = Path(recipe['baselineDirectory'])
                    projection = inspection.project(recipe, src, directory, local_run, frozen / 'profiles.json', frozen / 'coverage.json', recipe['inspectionRequests'])
            print(json.dumps({'sourceSHA': recipe['source'], 'phases': ['preflight', 'backup', 'render-review', 'publish-images', 'stage', 'configuration-check', 'crd-retention', 'quiesce', 'migrate', 'configure', 'upgrade', 'readiness', 'accept'], 'recipe': recipe, 'images': planned, 'coverageProjection': projection}, indent=2, default=str))
            return 0
        if not args.evidence:
            parser.error('--evidence new-directory required for inspect/execute')
        deploy(recipe, args.evidence.expanduser().resolve(), args.inspect, args.chart_diff_approved, args.staged_diff_approved)
        return 0
    except (Exception, KeyboardInterrupt) as error:
        message = type(error).__name__ if isinstance(error, (subprocess.TimeoutExpired, subprocess.CalledProcessError)) else str(error)
        print('STOP: ' + message, file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())

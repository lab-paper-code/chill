#!/usr/bin/env python3
"""Offline real-CLI smoke: local TLS API/registry and process-backed cluster/build doubles."""
import base64
import copy
import fcntl
import http.server
import json
import os
from pathlib import Path
import shutil
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]

# One executable double implements process boundaries; the actual runner, curl,
# source archives, registry verification and authenticated TLS client still run.
FAKE = r'''#!/usr/bin/env python3
import hashlib,json,os,pathlib,sys
root=pathlib.Path(os.environ['FAKE_DEPLOY_DIR']); args=sys.argv[1:]; name=pathlib.Path(sys.argv[0]).name
with (root/'calls.jsonl').open('a') as f:f.write(json.dumps([name,*args])+'\n')
state=json.loads((root/'state.json').read_text())
def save(): (root/'state.json').write_text(json.dumps(state))
def argument(flag):return args[args.index(flag)+1]
def document(image):
 return {'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'chill-serving-entrypoint','namespace':'review'},'spec':{'template':{'spec':{'containers':[{'name':'server','image':image}]}}}}
if name=='docker':
 if args[:2]==['buildx','du']:print('Total: 20GB');sys.exit(0)
 if state.get('failBuild'):sys.exit(17)
 image=argument('--output').split('name=',1)[1].split(',',1)[0]; host,path=image.split('/',1); repo,tag=path.rsplit(':',1)
 revision=argument('--label').split('=',1)[1]; manifests=[]
 def store(kind,obj):
  data=json.dumps(obj,sort_keys=True).encode(); digest='sha256:'+hashlib.sha256(data).hexdigest(); target=root/'registry'/'v2'/repo/kind/digest;target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes(data);return digest
 for platform in argument('--platform').split(','):
  system,arch=platform.split('/');config={'os':system,'architecture':arch,'config':{'Labels':{'org.opencontainers.image.revision':revision}}};cd=store('blobs',config)
  child={'schemaVersion':2,'config':{'digest':cd}};d=store('manifests',child);manifests.append({'digest':d,'platform':{'os':system,'architecture':arch}})
 index={'schemaVersion':2,'manifests':manifests};digest=store('manifests',index);(root/'registry'/'v2'/repo/'manifests'/tag).write_bytes((root/'registry'/'v2'/repo/'manifests'/digest).read_bytes())
 pathlib.Path(argument('--metadata-file')).write_text(json.dumps({'containerimage.digest':digest}));sys.exit(0)
if name=='helm':
 for verb in ('list','history','get','template','upgrade'):
  if verb in args:break
 if verb=='list':print(json.dumps([{'name':'chill','revision':state.get('revision',7),'status':'deployed'}]))
 elif verb=='history':print('[]')
 elif verb=='get':
  what=args[args.index('get')+1]
  if what=='values':print('{}')
  elif what=='manifest':print(json.dumps(document('old:source')))
  else:print('')
 elif verb=='template':
  image='old:source'; replicas=None
  for i,arg in enumerate(args):
   if arg=='-f':
    path=pathlib.Path(args[i+1])
    if path.name=='image-overlay.json':
     value=json.loads(path.read_text());item=value.get('servingEntrypoint',{}).get('image');image=item['repository']+':'+item['tag'] if item else image
    elif path.name.startswith('staged-values-'):
     replicas=json.loads(path.read_text()).get('operator',{}).get('replicaCount')
  if state.get('chartDrift'):print(json.dumps({'apiVersion':'v1','kind':'ConfigMap','metadata':{'name':'changed','namespace':'review'}}))
  else:
   result=document(image)
   if replicas is not None:result['spec']['replicas']=replicas
   print(json.dumps(result))
 elif verb=='upgrade':
  if state.get('failUpgrade'):sys.exit(19)
  overlay=pathlib.Path(args[args.index('-f')+1])
  for i,arg in enumerate(args):
   if arg=='-f' and pathlib.Path(args[i+1]).name=='image-overlay.json':overlay=pathlib.Path(args[i+1])
  state['publication']=str(overlay.parent/'publication.json');state['upgraded']=True;save();print('upgraded')
 sys.exit(0)
if name=='kubectl':
 if 'current-context' in args:print(state.get('currentContext','isolated-review'));sys.exit(0)
 if 'get' in args and 'profilingruns,batchenergyoptimizations,modelloadedidlebaselines,dispatchoccupancymeasurements,evaluationruns' in args and '--all-namespaces' in args:
  state['inflightChecks']=state.get('inflightChecks',0)+1;save()
  items=[{'kind':'DispatchOccupancyMeasurement','metadata':{'name':'active','namespace':'another-chill-ns'},'status':{'phase':'Running'}}] if state.get('inflightAt')==state['inflightChecks'] else []
  print(json.dumps({'items':items}));sys.exit(0)
 if 'port-forward' in args:
  import http.server
  class Query(http.server.BaseHTTPRequestHandler):
   def log_message(self,*args):pass
   def do_GET(self):
    status=200 if self.headers.get('Authorization')=='Bearer '+(root/'token').read_text().strip() else 401
    self.send_response(status);self.end_headers();self.wfile.write(b'{}')
  server=http.server.HTTPServer(('127.0.0.1',0),Query)
  print('Forwarding from 127.0.0.1:%d -> 8080'%server.server_port,flush=True);server.serve_forever()
 if 'get' in args and args[args.index('get')+1]=='pods' and state.get('publication'):
  publication=json.loads(pathlib.Path(state['publication']).read_text());items=[]
  for role,entry in publication['roles'].items():
   if role not in ('server','operator','query','dashboard','node-agent','profiler'):continue
   items.append({'metadata':{'name':role},'spec':{'containers':[{'name':role,'image':entry['image']}]},'status':{'conditions':[{'type':'Ready','status':'True'}],'containerStatuses':[{'name':role,'ready':True,'restartCount':0,'imageID':'registry@'+entry['platformDigests'][0]}]}})
  print(json.dumps({'items':items}))
 else:print('{"items":[]}')
 sys.exit(0)
sys.exit(89)
'''


class DeploymentCLI(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory(prefix='chill-deploy-smoke-')
        cls.directory = Path(cls.tmp.name)
        cls.registry_root = cls.directory / 'registry'
        cls.registry_root.mkdir()
        cls.sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
        cls.token = 'e30.' + base64.urlsafe_b64encode(json.dumps({'exp': time.time() + 172800}).encode()).decode().rstrip('=') + '.signature'
        cls.cert, cls.key = cls.directory / 'cert.pem', cls.directory / 'key.pem'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(cls.key), '-out', str(cls.cert), '-days', '1', '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost'], check=True, capture_output=True)
        class API(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass
            def do_GET(self):
                cls.requests.append((self.path, self.headers.get('Authorization')))
                if self.headers.get('Authorization') != 'Bearer ' + cls.token:
                    self.send_response(401); self.end_headers(); return
                if self.path == '/':
                    self.send_response(200); self.send_header('Content-Type', 'text/html'); self.end_headers(); self.wfile.write(b'<!doctype html><html><body>CHILL</body></html>'); return
                if self.path.endswith('measurement-coverage'):
                    unknown = json.loads((cls.directory / 'state.json').read_text()).get('unknownCoverage', False)
                    body = {'summary': {'countsByDisplayStatus': {'usable': None if unknown else 2}}, 'sources': [{'complete': not unknown}]}
                elif self.path == '/api/v1/scheduler/profiles':
                    state = json.loads((cls.directory / 'state.json').read_text())
                    metric = 4 if state.get('upgraded') and state.get('changeMetrics') else 3
                    body = {'profiles': [{'id': 'profile-1', 'assessment': {'displayStatus': 'usable'}, 'batches': [{'batch': 1, 'energyPerInputJoules': metric, 'meanWallWatts': 5}]}]}
                    body['profiles'][0].update(reference={'store': 'fixture', 'key': 'result', 'size': 1}, evidence=[], nodeName='fixture-node', condition={'runtimeImage': 'recorded-runtime', 'backend': 'ort-cpu'}, confidenceLevel=0.95, idleBaselineWatts=1, measuredAt='2026-11-03T00:00:00Z', metricSource={})
                else:
                    body = {'ok': True}
                data = json.dumps(body).encode()
                self.send_response(200); self.send_header('Content-Type', 'application/json'); self.end_headers(); self.wfile.write(data)
        cls.requests = []
        cls.api = http.server.ThreadingHTTPServer(('127.0.0.1', 0), API)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(str(cls.cert), str(cls.key))
        cls.api.socket = context.wrap_socket(cls.api.socket, server_side=True)
        class Registry(http.server.SimpleHTTPRequestHandler):
            def __init__(self, *args, **kwargs):
                super().__init__(*args, directory=str(cls.registry_root), **kwargs)
            def log_message(self, *args):
                pass
        cls.registry = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Registry)
        cls.threads = []
        for server in (cls.api, cls.registry):
            thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start(); cls.threads.append(thread)
        cls.bin = cls.directory / 'bin'; cls.bin.mkdir()
        script = cls.bin / 'fake'; script.write_text(FAKE); script.chmod(0o700)
        for name in ('helm', 'kubectl', 'docker'):
            (cls.bin / name).symlink_to(script)
        (cls.directory / 'token').write_text(cls.token)
        (cls.directory / 'environment.json').write_text(json.dumps({'d2': {'apiBase': 'https://localhost:' + str(cls.api.server_port), 'caFile': str(cls.cert), 'tokenFile': str(cls.directory / 'token')}}))
        (cls.directory / 'kubeconfig').write_text('synthetic cluster; never read by real kubectl\n')

    @classmethod
    def tearDownClass(cls):
        for server in (cls.api, cls.registry):
            server.shutdown(); server.server_close()
        for thread in cls.threads:
            thread.join()
        cls.tmp.cleanup()

    def setUp(self):
        (self.directory / 'state.json').write_text('{}')
        (self.directory / 'calls.jsonl').write_text('')
        self.requests.clear()
        self.recipe = {'source': self.sha, 'context': 'isolated-review', 'kubeconfig': str(self.directory / 'kubeconfig'), 'namespace': 'review', 'release': 'chill', 'registry': '127.0.0.1:%d/chill' % self.registry.server_port, 'registryScheme': 'http', 'apiEnvironment': str(self.directory / 'environment.json'), 'expectedRevision': 7, 'minFreeBytes': 1, 'images': ['server'], 'helm': str(self.bin / 'helm'), 'kubectl': str(self.bin / 'kubectl'), 'lock': str(self.directory / 'heavy.lock'), 'readinessSeconds': 3, 'buildSeconds': 30, 'apiChecks': [{'path': '/api/v1/workloads?view=summary', 'maxBytes': 1024}]}
        self.out = self.directory / self.id().rsplit('.', 1)[-1]

    def cli(self, *args, expected=0):
        recipe_path = self.directory / 'recipe.json'; recipe_path.write_text(json.dumps(self.recipe))
        environment = dict(os.environ, FAKE_DEPLOY_DIR=str(self.directory), PATH=str(self.bin) + os.pathsep + os.environ['PATH'])
        environment.pop('CHILL_EXPERIMENT_CONFIG', None)
        result = subprocess.run([str(ROOT / 'hack/fast-deploy.sh'), 'run', str(recipe_path), *args], cwd=ROOT, env=environment, capture_output=True, text=True, timeout=90)
        self.assertEqual(result.returncode, expected, result.stdout + '\n' + result.stderr)
        return result

    def calls(self):
        return [json.loads(x) for x in (self.directory / 'calls.jsonl').read_text().splitlines()]

    def state(self, **kwargs):
        (self.directory / 'state.json').write_text(json.dumps(kwargs))

    def test_local_plan_never_contacts_cluster_or_api(self):
        result = self.cli('--dry-run', '--evidence', str(self.out))
        self.assertIn('LOCAL PLAN ONLY', result.stdout)
        self.assertFalse(self.out.exists())
        self.assertEqual(self.calls(), [])
        self.assertEqual(self.requests, [])

    def test_complete_cli_build_registry_tls_and_acceptance(self):
        self.cli('--execute', '--evidence', str(self.out))
        self.assertTrue(json.loads((self.out / 'state.json').read_text())['ready'])
        publication = json.loads((self.out / 'publication.json').read_text())
        self.assertIn('@sha256:', publication['roles']['server']['image'])
        self.assertTrue(json.loads((self.out / 'after/acceptance.json').read_text())['passed'])
        self.assertTrue(any(x[0] == 'docker' for x in self.calls()))
        self.assertTrue(any(x[0] == 'helm' and 'upgrade' in x for x in self.calls()))
        self.assertTrue(all(token == 'Bearer ' + self.token for _, token in self.requests))
        self.assertNotIn(self.token, (self.out / 'commands.log').read_text())
        self.assertEqual(self.out.stat().st_mode & 0o077, 0)
        # Reusing an evidence directory cannot accidentally replay side effects.
        previous = len(self.calls())
        self.cli('--execute', '--evidence', str(self.out), expected=1)
        self.assertEqual(len(self.calls()), previous)

    def test_inspect_drift_is_read_only_and_requires_exact_approval(self):
        self.state(chartDrift=True)
        self.cli('--inspect', '--evidence', str(self.out))
        self.assertFalse(json.loads((self.out / 'state.json').read_text())['chartApproved'])
        self.assertFalse(any(x[0] == 'docker' or 'upgrade' in x or 'apply' in x for x in self.calls()))
        self.assertEqual(self.requests, [])
        self.cli('--execute', '--evidence', str(self.out) + '-blocked', expected=1)
        self.assertFalse(any(x[0] == 'docker' or 'upgrade' in x for x in self.calls()))

    def test_live_revision_change_stops_before_build_or_write(self):
        self.state(revision=8)
        self.cli('--execute', '--evidence', str(self.out), expected=1)
        self.assertEqual(json.loads((self.out / 'failure.json').read_text())['stage'], 'preflight')
        self.assertFalse(any(x[0] == 'docker' or 'upgrade' in x or 'apply' in x for x in self.calls()))

    def test_failure_retains_stage_and_never_rolls_back(self):
        self.state(failUpgrade=True)
        self.cli('--execute', '--evidence', str(self.out), expected=1)
        failure = json.loads((self.out / 'failure.json').read_text())
        self.assertEqual(failure['stage'], 'upgrade')
        self.assertFalse(failure['ready'])
        self.assertFalse(any(any(word in x for word in ('rollback', 'uninstall', 'delete')) for x in self.calls()))

    def test_numeric_regression_is_reported_without_rejecting_execution(self):
        self.state(changeMetrics=True)
        self.cli('--execute', '--evidence', str(self.out))
        report = json.loads((self.out / 'after/acceptance.json').read_text())
        self.assertFalse(report['passed'])
        self.assertEqual(report['numericChanges'][0]['field'], 'energyPerInputJoules')
        status = json.loads((self.out / 'state.json').read_text())
        self.assertTrue(status['ready'])
        self.assertFalse(status['diagnostics']['passed'])
        self.assertFalse(status['retainedRecords']['passed'])

    def test_unknown_coverage_remains_diagnostic_before_and_after_upgrade(self):
        self.state(unknownCoverage=True)
        frozen = self.directory / 'incomplete-baseline'
        frozen.mkdir()
        (frozen / 'profiles.json').write_text(json.dumps({'profiles': [{'id': 'profile-1', 'assessment': {'displayStatus': 'usable'}, 'batches': [{'batch': 1, 'energyPerInputJoules': 3, 'meanWallWatts': 5}]}]}))
        (frozen / 'coverage.json').write_text(json.dumps({'summary': {'countsByDisplayStatus': {'usable': None}}, 'sources': [{'complete': False}]}))
        self.recipe.update(baselineDirectory=str(frozen), expectedProfileCount=1)
        self.cli('--execute', '--evidence', str(self.out))
        state = json.loads((self.out / 'state.json').read_text())
        self.assertTrue(state['ready'])
        for report in (state['preDeployDiagnostics'], state['diagnostics']):
            self.assertFalse(report['passed'])
            self.assertIsNone(report['coverageUsableAfter'])
            self.assertEqual(report['after'], 1)
            self.assertTrue(report['checks']['expectedProfileCount'])

    def test_shared_heavy_lock_stops_before_cluster(self):
        with (self.directory / 'heavy.lock').open('a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.cli('--execute', '--evidence', str(self.out), expected=1)
        self.assertEqual(self.calls(), [])

    def test_direct_query_auth_and_dashboard_process_checks(self):
        self.recipe.update(queryService='chill-query', dashboardCheck=True)
        self.cli('--execute', '--evidence', str(self.out))
        self.assertTrue(json.loads((self.out / 'query-auth/query-check.json').read_text())['passed'])
        self.assertTrue(json.loads((self.out / 'dashboard/dashboard-check.json').read_text())['passed'])

    def test_staging_is_frozen_and_migration_failure_stops_upgrade(self):
        program = "from pathlib import Path; import sys; p=Path(sys.argv[1]); p.mkdir(exist_ok=True); (p/'phase-log').open('a').write(sys.argv[2]+'\\n'); (p/'generated.json').write_text('{}') if sys.argv[2]=='stage' else None; sys.exit(23 if sys.argv[2]=='migrate' else 0)"
        self.recipe['generatedValues'] = ['generated.json']
        self.recipe['commands'] = {phase: [{'argv': [sys.executable, '-c', program, '{evidence}', phase], 'seconds': 10}] for phase in ('stage', 'quiesce', 'migrate', 'configure', 'accept')}
        self.cli('--execute', '--evidence', str(self.out), expected=1)
        self.assertEqual((self.out / 'phase-log').read_text().splitlines(), ['stage', 'quiesce', 'migrate'])
        self.assertEqual(json.loads((self.out / 'failure.json').read_text())['stage'], 'migrate')
        self.assertTrue((self.out / 'staged-values-0.yaml').exists())
        self.assertFalse(any('upgrade' in x for x in self.calls()))

    def test_disk_guard_prevents_cluster_and_build(self):
        self.recipe['minFreeBytes'] = 2 ** 63
        self.cli('--execute', '--evidence', str(self.out), expected=1)
        self.assertEqual(self.calls(), [])

    def test_inflight_work_blocks_before_publication_or_upgrade(self):
        for check, expected_build in ((1, False), (2, True)):
            self.state(inflightAt=check)
            self.cli('--execute', '--evidence', str(self.out) + '-' + str(check), expected=1)
            self.assertEqual(any(call[0] == 'docker' and 'build' in call for call in self.calls()), expected_build)
            self.assertFalse(any(call[0] == 'helm' and 'upgrade' in call for call in self.calls()))
            (self.directory / 'calls.jsonl').write_text('')

    def test_context_mismatch_rejects_custom_phase_before_publication(self):
        self.state(currentContext='other-cluster')
        self.recipe['commands'] = {'preflight': [{'argv': [sys.executable, '-c', 'raise SystemExit(90)'], 'seconds': 10}]}
        self.cli('--execute', '--evidence', str(self.out), expected=1)
        self.assertFalse(any(x[0] == 'docker' or 'upgrade' in x for x in self.calls()))
        self.assertIn('current-context differs', json.loads((self.out / 'failure.json').read_text())['error'])

    def test_staged_nonimage_change_needs_exact_approval(self):
        program = "from pathlib import Path; import json, sys; Path(sys.argv[1]).write_text(json.dumps({'operator':{'replicaCount':0}}))"
        self.recipe['commands'] = {'stage': [{'argv': [sys.executable, '-c', program, '{evidence}/generated.json'], 'seconds': 10}]}
        self.recipe['generatedValues'] = ['generated.json']
        self.cli('--execute', '--evidence', str(self.out), expected=1)
        self.assertTrue((self.out / 'staged-diff.txt').read_text())
        self.assertFalse(any('upgrade' in x for x in self.calls()))

    def test_timeout_stops_worker_after_wrapper_exits(self):
        import main as runner_module
        self.out.mkdir()
        marker = self.out / 'worker-writes'
        worker = "import signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN)\nwhile True:\n with open(" + repr(str(marker)) + ",'a') as f: f.write('write\\n')\n time.sleep(.02)"
        wrapper = "import subprocess,sys,time; subprocess.Popen([sys.executable,'-c'," + repr(worker) + "]); time.sleep(60)"
        runner = runner_module.Runner(self.out, ROOT)
        try:
            with self.assertRaises(subprocess.TimeoutExpired):
                runner.run([sys.executable, '-c', wrapper], 'worker.log', timeout=1)
        finally:
            runner.log.close()
        before = marker.read_bytes()
        time.sleep(.2)
        self.assertEqual(marker.read_bytes(), before)
        self.assertEqual(runner.timings[0]['exitCode'], None)

    def test_removed_crd_requires_actual_live_keep_policy(self):
        import main as runner_module
        self.out.mkdir()
        original = json.dumps({'apiVersion': 'apiextensions.k8s.io/v1', 'kind': 'CustomResourceDefinition', 'metadata': {'name': 'measurements.example.test'}})
        for annotations in ({}, {'helm.sh/resource-policy': 'keep'}):
            def live(*args, **kwargs):
                return json.dumps({'metadata': {'name': 'measurements.example.test', 'annotations': annotations}}).encode()
            if not annotations:
                with self.assertRaisesRegex(ValueError, 'CRD data loss'):
                    runner_module.crd_retention(original, b'', ['NO-LIVE-KUBECTL'], live, self.out)
            else:
                receipt = runner_module.crd_retention(original, b'', ['NO-LIVE-KUBECTL'], live, self.out)
                self.assertEqual(receipt['kept'], ['measurements.example.test'])


if __name__ == '__main__':
    unittest.main(verbosity=2)

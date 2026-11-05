#!/usr/bin/env python3
"""Offline real-CLI smoke: local TLS API/registry and process-backed cluster/build doubles."""
import base64
import copy
import fcntl
import http.server
import json
import os
from pathlib import Path
import selectors
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
import hashlib,json,os,pathlib,sys,time
root=pathlib.Path(os.environ['FAKE_DEPLOY_DIR']); args=sys.argv[1:]; name=pathlib.Path(sys.argv[0]).name
with (root/'calls.jsonl').open('a') as f:f.write(json.dumps([name,*args])+'\n')
state=json.loads((root/'state.json').read_text())
def save(): (root/'state.json').write_text(json.dumps(state))
def argument(flag):return args[args.index(flag)+1]
def document(image):
 return {'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'chill-serving-entrypoint','namespace':'review'},'spec':{'template':{'spec':{'containers':[{'name':'server','image':image}]}}}}
if name=='docker':
 if args[:2]==['buildx','du']:print('Total: 20GB');sys.exit(0)
 if args[:2]==['buildx','prune']:
  state['pruneCount']=state.get('pruneCount',0)+1;save()
  if state.get('failPrune') or state['pruneCount']==state.get('failPruneAt'):sys.exit(18)
  builder=argument('--builder')
  state['buildCache']=[entry for entry in state.get('buildCache',[]) if entry['builder']!=builder or entry['inUse']]
  save();print('Reclaimed completed unused build cache');sys.exit(0)
 if args[:2]!=['buildx','build']:sys.exit(89)
 if state.get('failBuild'):sys.exit(17)
 if state.get('buildBarrier'):
  print('BUILD_PROGRESS_BEFORE_COMPLETION',flush=True)
  while not pathlib.Path(state['buildBarrier']).exists():time.sleep(.02)
 image=argument('--output').split('name=',1)[1].split(',',1)[0]; host,path=image.split('/',1); repo,tag=path.rsplit(':',1)
 revision=state.get('registryRevision',argument('--label').split('=',1)[1]); manifests=[]
 def store(kind,obj):
  data=json.dumps(obj,sort_keys=True).encode(); digest='sha256:'+hashlib.sha256(data).hexdigest(); target=root/'registry'/'v2'/repo/kind/digest;target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes(data);return digest
 for platform in argument('--platform').split(','):
  system,arch=platform.split('/');config={'os':system,'architecture':arch,'config':{'Labels':{'org.opencontainers.image.revision':revision}}};cd=store('blobs',config)
  child={'schemaVersion':2,'config':{'digest':cd}};d=store('manifests',child);manifests.append({'digest':d,'platform':{'os':system,'architecture':arch}})
 index={'schemaVersion':2,'manifests':manifests};digest=store('manifests',index);(root/'registry'/'v2'/repo/'manifests'/tag).write_bytes((root/'registry'/'v2'/repo/'manifests'/digest).read_bytes())
 pathlib.Path(argument('--metadata-file')).write_text(json.dumps({'containerimage.digest':digest,'image.name':image}))
 if state.get('diskFixture'):
  receipt=pathlib.Path(os.environ['CHILL_DEPLOY_EVIDENCE'])/'publication.json'
  state.setdefault('receiptsBeforeBuild',[]).append(sorted(json.loads(receipt.read_text())['roles']) if receipt.exists() else [])
  state['buildCache'].append({'builder':argument('--builder'),'id':image,'inUse':False,'bytes':100})
  if state.get('diskRemainsLow'):state['unreclaimableBytes']=100
  save()
 sys.exit(0)
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
  duration=argument('--timeout');wait_seconds=int(duration[:-1])*{'s':1,'m':60}[duration[-1]]
  if state.get('serverReadySeconds',0)>wait_seconds:
   print('timed out waiting for server listener',file=sys.stderr);sys.exit(20)
  overlay=pathlib.Path(args[args.index('-f')+1])
  for i,arg in enumerate(args):
   if arg=='-f' and pathlib.Path(args[i+1]).name=='image-overlay.json':overlay=pathlib.Path(args[i+1])
  state['publication']=str(overlay.parent/'publication.json');state['upgraded']=True;save();print('upgraded')
 if verb=='template' or verb=='get' and args[args.index('get')+1]=='manifest':
  for item in state.get('evaluationRbac',[]):
   print('---\n# Source: chill/templates/evaluation_rbac.yaml\n'+json.dumps(item))
 sys.exit(0)
if name=='kubectl':
 if state.get('runtimeFixture'):
  if 'get' in args and args[args.index('get')+1]=='nodes':
   print(json.dumps(state['nodes']));sys.exit(0)
  if 'get' in args and args[args.index('get')+1]=='pods':
   items=[]
   if not state.get('released'):
    pod=state['servingPod']
    if state.get('removed'):
     state['releaseLists']=state.get('releaseLists',0)+1
     if state['releaseLists']>=2:
      state['released']=True
     else:pod['metadata']['deletionTimestamp']='2026-11-05T00:00:00Z'
    if not state.get('released'):items.append(pod)
    save()
   if not state.get('publication'):
    print(json.dumps({'items':items}));sys.exit(0)
  if 'create' in args:
   pod=json.load(sys.stdin);container=pod['spec']['containers'][0]
   gpu=container.get('resources',{}).get('limits',{}).get('nvidia.com/gpu')
   if gpu:
    state['gpuAttempts']=state.get('gpuAttempts',0)+1
    if not state.get('removed') or not state.get('released'):
     state['prematureProbe']=True;save()
     print('requested 1 nvidia.com/gpu, available 0: serving allocation not released',file=sys.stderr);sys.exit(23)
    settings=state['settings']
    if not set(state['gpuNodes']).issubset(settings['excludedNodes']) or state['owner'] in settings['autonomousWorkloads']:
     print('GPU probe admitted without scheduler exclusion',file=sys.stderr);sys.exit(24)
   state['probeCount']=state.get('probeCount',0)+1;name=pod['metadata']['name']
   state.setdefault('probes',{})[name]=pod
   if state.get('concurrentEdit') and container['command'][-1]=='rknnlite':
    state['settings']['minimumDemand']['external']=99
    if state['concurrentEdit']=='tracked':state['settings']['excludedNodes']=['external-choice']
    state['settingsVersion']+=1
    state.pop('concurrentEdit')
   save();print('pod/'+name);sys.exit(0)
  if 'get' in args and args[args.index('get')+1].startswith('pod/'):
   pod=state['probes'][args[args.index('get')+1].split('/',1)[1]]
   container=pod['spec']['containers'][0];backend=container['command'][-1]
   facts={'machine':state['machines'][pod['spec']['nodeName']],'ortVersion':'1.24.0','providers':['CPUExecutionProvider','CUDAExecutionProvider'],'packages':{'onnxruntime':'1.24.0','onnxruntime-gpu':'1.24.0','numpy':'1.26.4','rknn-toolkit-lite2':'2.3.0'},'gpuUUIDs':['GPU-observed-'+pod['spec']['nodeName']],'driverVersion':'fixture-driver','compatible':['rockchip,rk3588']}
   failed=state.get('failProbe') and backend=='ort-cuda'
   print(json.dumps({'status':{'phase':'Failed' if failed else 'Succeeded','containerStatuses':[{'state':{'terminated':{'exitCode':1 if failed else 0,'message':json.dumps(facts)}}}]}}));sys.exit(0)
  if 'delete' in args:
   names=args[args.index('delete')+1:]
   if any('serving' in item for item in names):
    print('serving Pod deletion forbidden',file=sys.stderr);sys.exit(25)
   sys.exit(0)
 if 'auth' in args and 'can-i' in args:
  denied=state.get('denyEvaluationPermission') and '/chill/api/v1/workloads' in args and '--as=system:serviceaccount:review:custom-action' in args
  print('no' if denied else 'yes');sys.exit(1 if denied else 0)
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
    body=b'{}' if status==401 or self.path=='/api/v1/scheduler/profiles?view=summary&limit=1' else b' '*(9<<20)
    self.send_response(status);self.send_header('Content-Length',str(len(body)));self.end_headers()
    try:self.wfile.write(body)
    except (BrokenPipeError,ConnectionResetError):pass
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
            def reply(self, body, status=200, etag=None):
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                if etag:
                    self.send_header('ETag', etag)
                self.end_headers()
                self.wfile.write(json.dumps(body).encode())

            def mutation(self, method):
                cls.requests.append((self.path, self.headers.get('Authorization')))
                if self.headers.get('Authorization') != 'Bearer ' + cls.token:
                    self.reply({}, 401); return
                state_path = cls.directory / 'state.json'
                state = json.loads(state_path.read_text())
                body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))))
                state.setdefault('apiMutations', []).append({'method': method, 'path': self.path, 'body': body, 'ifMatch': self.headers.get('If-Match')})
                if method == 'PUT' and self.path == '/api/v1/scheduler/settings':
                    if self.headers.get('If-Match') != '"%d"' % state['settingsVersion']:
                        self.reply({'error': 'conflict'}, 409); return
                    state['settings'] = body
                    state['settingsVersion'] += 1
                    if state.get('eligibilityRace') and state['settingsVersion'] == 2:
                        state['routing'] = {state['owner']: {state['instance']['id']: 1}}
                    state_path.write_text(json.dumps(state))
                    self.reply({'after': body, 'appliedAt': '2026-11-05T00:00:00Z'}, etag='"%d"' % state['settingsVersion'])
                elif method == 'POST' and self.path == '/api/v1/scheduler/operations':
                    expected = {'kind': 'remove', 'workloadUID': state['owner'], 'node': state['instance']['node'], 'instance': state['instance']['id'], 'reason': body.get('reason')}
                    admitted = body == expected and bool(body.get('reason')) and state['owner'] not in state['settings']['autonomousWorkloads'] and set(state['gpuNodes']).issubset(state['settings']['excludedNodes'])
                    routed = any(weights.get(state['instance']['id'], 0) > 0 for weights in state.get('routing', {}).values())
                    accepted = bool(state.get('outstanding'))
                    failed = routed or accepted or not admitted
                    if state.get('removeFailed'):
                        state_path.write_text(json.dumps(state))
                        self.reply({'status': 'failed', 'error': 'native drain did not complete'}); return
                    if not failed:
                        state['removed'] = True
                    state_path.write_text(json.dumps(state))
                    message = 'the instance still owns accepted requests' if accepted else 'the instance still receives routing; unroute it first'
                    self.reply({'code': 'OperationConflict', 'message': message} if failed else {'status': 'complete'}, 409 if failed else 200)
                else:
                    self.reply({}, 404)

            def do_PUT(self):
                self.mutation('PUT')

            def do_POST(self):
                self.mutation('POST')

            def log_message(self, *args):
                pass
            def do_GET(self):
                cls.requests.append((self.path, self.headers.get('Authorization')))
                if self.headers.get('Authorization') != 'Bearer ' + cls.token:
                    self.send_response(401); self.end_headers(); return
                if self.path in ('/api/v1/scheduler/settings', '/api/v1/scheduler/state'):
                    state = json.loads((cls.directory / 'state.json').read_text())
                    if state.get('runtimeFixture'):
                        if self.path.endswith('/settings'):
                            self.reply(state['settings'], etag='"%d"' % state['settingsVersion'])
                        else:
                            self.reply({'snapshot': {'instances': [] if state.get('removed') else [state['instance']]}})
                        return
                if self.path == '/':
                    self.send_response(200); self.send_header('Content-Type', 'text/html'); self.end_headers(); self.wfile.write(b'<!doctype html><html><body>CHILL</body></html>'); return
                if self.path.endswith('measurement-coverage'):
                    state = json.loads((cls.directory / 'state.json').read_text())
                    if state.get('upgraded') and state.get('warmingOnce') and not state.get('warmed'):
                        state['warmed'] = True
                        (cls.directory / 'state.json').write_text(json.dumps(state))
                        self.send_response(503); self.end_headers()
                        self.wfile.write(b'{"code":"CoverageWarming"}')
                        return
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
        # Only local disk accounting is synthetic; the official CLI and registry proof still run.
        (cls.bin / 'sitecustomize.py').write_text('''
import collections, json, os, pathlib, shutil
original_disk_usage = shutil.disk_usage
usage = collections.namedtuple('usage', 'total used free')
def disk_usage(path):
    state = json.loads((pathlib.Path(os.environ['FAKE_DEPLOY_DIR']) / 'state.json').read_text())
    if not state.get('diskFixture'):
        return original_disk_usage(path)
    free = 450 - sum(entry['bytes'] for entry in state['buildCache']) - state.get('unreclaimableBytes', 0)
    return usage(1000, 1000 - free, free)
shutil.disk_usage = disk_usage
''')
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
        environment = dict(os.environ, FAKE_DEPLOY_DIR=str(self.directory), PYTHONPATH=str(self.bin),
                           PATH=str(self.bin) + os.pathsep + os.environ['PATH'])
        environment.pop('CHILL_EXPERIMENT_CONFIG', None)
        root = getattr(self, 'repository', ROOT)
        result = subprocess.run([str(root / 'hack/fast-deploy.sh'), 'run', str(recipe_path), *args], cwd=root, env=environment, capture_output=True, text=True, timeout=90)
        self.assertEqual(result.returncode, expected, result.stdout + '\n' + result.stderr)
        return result

    def calls(self):
        return [json.loads(x) for x in (self.directory / 'calls.jsonl').read_text().splitlines()]

    def state(self, **kwargs):
        (self.directory / 'state.json').write_text(json.dumps(kwargs))

    def test_runtime_gpu_quiescence_restores_settings(self):
        import runtime
        from test_runtime import nodes_fixture
        _, inspector = runtime._inspector(ROOT)
        gpu_nodes = sorted({node for _, _, variable, nodes, _ in inspector.CLASSES if variable for node in nodes})
        machines = {node: 'x86_64' if architecture == 'amd64' else 'aarch64'
                    for _, architecture, _, nodes, _ in inspector.CLASSES for node in nodes}
        machines.update({node: 'x86_64' if architecture == 'amd64' else 'aarch64'
                         for architecture, (_, node) in inspector.CPU.items()})
        machines[inspector.RKNN_NODE] = 'aarch64'
        owner = 'observed-owner-uid'
        instance = {'id': 'serving-fixture/server', 'node': {'name': gpu_nodes[0], 'uid': 'observed-node-uid'},
                    'ownerWorkloadUID': owner, 'workloadUID': '', 'podName': 'serving-fixture', 'podUID': 'observed-pod-uid'}
        settings = {'excludedNodes': ['preexisting-exclusion'], 'autonomousWorkloads': [owner, 'unrelated-workload'],
                    'minimumDemand': {'unchanged': 7}}
        pod = {'metadata': {'name': instance['podName'], 'namespace': 'review', 'uid': instance['podUID']},
               'spec': {'nodeName': gpu_nodes[0], 'containers': [{'name': 'server', 'resources': {'requests': {'nvidia.com/gpu': '1'}}}]},
               'status': {'phase': 'Running'}}
        self.recipe.update(images=['server', *sorted({role for role, _ in runtime.RUNTIME_IMAGES.values()})],
                           runtimeDeclarations=True, readinessSeconds=10)
        for scenario in ('success', 'probe-fail', 'remove-failed', 'remove-conflict', 'accepted-request', 'eligibility-race',
                         'foreign-consumer', 'concurrent-unrelated', 'concurrent-tracked'):
            with self.subTest(scenario=scenario):
                fixture = {'runtimeFixture': True, 'nodes': nodes_fixture(ROOT), 'gpuNodes': gpu_nodes, 'machines': machines,
                           'owner': owner, 'instance': copy.deepcopy(instance), 'servingPod': copy.deepcopy(pod),
                           'settings': copy.deepcopy(settings), 'settingsVersion': 1}
                if scenario == 'probe-fail':
                    fixture['failProbe'] = True
                elif scenario == 'remove-failed':
                    fixture['removeFailed'] = True
                elif scenario == 'remove-conflict':
                    fixture['routing'] = {owner: {instance['id']: 1}}
                elif scenario == 'accepted-request':
                    fixture['outstanding'] = [{'id': 'accepted-fixture', 'instance': instance['id']}]
                elif scenario == 'eligibility-race':
                    fixture['eligibilityRace'] = True
                elif scenario == 'foreign-consumer':
                    fixture['servingPod']['metadata']['uid'] = 'foreign-pod-uid'
                elif scenario.startswith('concurrent-'):
                    fixture['concurrentEdit'] = scenario[len('concurrent-'):]
                self.state(**fixture)
                (self.directory / 'calls.jsonl').write_text('')
                evidence = self.out / scenario
                failed = scenario in ('probe-fail', 'remove-failed', 'remove-conflict', 'accepted-request', 'eligibility-race', 'foreign-consumer')
                self.cli('--execute', '--evidence', str(evidence), expected=int(failed))
                observed = json.loads((self.directory / 'state.json').read_text())
                restored = copy.deepcopy(settings)
                if scenario.startswith('concurrent-'):
                    restored['minimumDemand']['external'] = 99
                if scenario == 'concurrent-tracked':
                    restored['excludedNodes'] = ['external-choice']
                self.assertEqual(observed['settings'], restored)
                self.assertFalse(observed.get('prematureProbe'), observed)
                operations = [item for item in observed.get('apiMutations', []) if item['method'] == 'POST']
                if scenario == 'foreign-consumer':
                    self.assertEqual(operations, [])
                else:
                    self.assertEqual(len(operations), 1, operations)
                    self.assertEqual(operations[0]['body']['kind'], 'remove')
                if scenario in ('remove-failed', 'remove-conflict', 'accepted-request', 'eligibility-race', 'foreign-consumer'):
                    self.assertEqual(observed.get('probeCount', 0), 0)
                    self.assertFalse(observed.get('removed'))
                else:
                    self.assertTrue(observed['removed'])
                    self.assertTrue(observed['released'])
                    self.assertGreater(observed['gpuAttempts'], 0)
                if failed:
                    self.assertTrue((evidence / 'failure.json').exists())
                    self.assertFalse(any('upgrade' in call for call in self.calls()))
                    if scenario in ('remove-conflict', 'accepted-request', 'eligibility-race'):
                        raw_evidence = '\n'.join(path.read_text() for path in (evidence / 'runtime-quiescence').glob('*.json'))
                        self.assertIn('OperationConflict', raw_evidence)
                        self.assertIn(instance['id'], raw_evidence)
                else:
                    self.assertTrue(json.loads((evidence / 'state.json').read_text())['ready'])
                    self.assertTrue((evidence / 'runtime-values.yaml').exists())
                    publication = json.loads((evidence / 'publication.json').read_text())
                    self.assertTrue(set(role for role, _ in runtime.RUNTIME_IMAGES.values()).issubset(publication['roles']))
                receipts = [json.loads(path.read_text()) for path in (evidence / 'runtime-quiescence').glob('*.json')]
                if scenario != 'foreign-consumer':
                    self.assertTrue(any(receipt.get('revertedAt') for receipt in receipts), receipts)
                if scenario == 'concurrent-tracked':
                    self.assertTrue(any(any(item.get('path') == ['excludedNodes'] and item.get('reason') == 'external-change-revert-skipped'
                                                for item in receipt.get('revertSkipped', [])) for receipt in receipts), receipts)
                for call in self.calls():
                    self.assertNotIn('uninstall', call)
                    self.assertNotIn('rollback', call)
                    if 'delete' in call:
                        self.assertFalse(any('serving-fixture' in item or 'workload' in item for item in call))

    def test_local_plan_never_contacts_cluster_or_api(self):
        result = self.cli('--dry-run', '--evidence', str(self.out))
        self.assertIn('LOCAL PLAN ONLY', result.stdout)
        self.assertFalse(self.out.exists())
        self.assertEqual(self.calls(), [])
        self.assertEqual(self.requests, [])
        original = self.recipe.copy()
        for invalid in ({'pruneCompletedBuildCache': 1},
                        {'reusePendingBuilds': []},
                        {'reusePendingBuilds': {'foreign-role': 'metadata.json'}},
                        {'reusePendingBuilds': {'server': ''}},
                        {'reusePendingBuilds': {'server': 'metadata.json'}},
                        {'reusePublication': str(self.directory / 'publication.json'),
                         'reusePendingBuilds': {'server': 'metadata.json'},
                         'commands': {'preflight': [{'argv': ['true'], 'seconds': 1}]}}):
            with self.subTest(invalid=invalid):
                self.recipe = dict(original, **invalid)
                self.cli('--dry-run', expected=1)
                self.assertEqual(self.calls(), [])
                self.assertEqual(self.requests, [])

    def test_cli_reuses_unchanged_images_and_builds_only_changed_copy_inputs(self):
        self.repository = self.directory / 'reuse-repository'
        self.repository.mkdir()
        shutil.copytree(ROOT / 'hack/deploy', self.repository / 'hack/deploy',
                        ignore=shutil.ignore_patterns('__pycache__'))
        (self.repository / 'hack/evaluation').mkdir()
        shutil.copy2(ROOT / 'hack/evaluation/auth.py', self.repository / 'hack/evaluation/auth.py')
        shutil.copy2(ROOT / 'hack/fast-deploy.sh', self.repository / 'hack/fast-deploy.sh')
        shutil.copytree(ROOT / 'build/docker', self.repository / 'build/docker')
        shutil.copy2(ROOT / 'Makefile', self.repository / 'Makefile')
        for path, body in {'go.mod': 'module fixture\n', 'go.sum': '',
                           'cmd/chill-server/main.go': 'package main\n',
                           'chill-dashboard/package.json': '{}\n',
                           'charts/chill/values.yaml': 'fixture: true\n'}.items():
            target = self.repository / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(body)
        def git(*args):
            return subprocess.check_output(['git', '-C', str(self.repository), *args], stderr=subprocess.STDOUT).decode().strip()
        git('init')
        git('config', 'user.name', 'Deployment fixture')
        git('config', 'user.email', 'fixture@example.invalid')
        def commit(message):
            git('add', '.')
            git('commit', '-m', message)
            return git('rev-parse', 'HEAD')
        self.recipe.update(source=commit('initial build inputs'), images=['server', 'dashboard'])
        original = self.recipe['source']
        initial = self.out / 'initial'
        self.cli('--execute', '--evidence', str(initial))
        before = json.loads((initial / 'publication.json').read_text())
        self.recipe['reusePublication'] = str(initial / 'publication.json')
        (self.repository / 'charts/chill/values.yaml').write_text('fixture: changed\n')
        self.recipe['source'] = commit('chart-only change')
        call_count = len(self.calls())
        local = self.cli('--dry-run')
        planned = json.loads(local.stdout.split('\n', 1)[1])
        self.assertEqual({x['role']: x['action'] for x in planned['images']},
                         {'server': 'reuse', 'dashboard': 'reuse'})
        self.assertEqual(len(self.calls()), call_count)
        inspected = self.out / 'inspect'
        self.cli('--inspect', '--evidence', str(inspected))
        self.assertFalse(any(call[0] == 'docker' for call in self.calls()[call_count:]))
        reused = self.out / 'reused'
        self.cli('--execute', '--evidence', str(reused))
        publication = json.loads((reused / 'publication.json').read_text())
        for role in ('server', 'dashboard'):
            self.assertEqual(publication['roles'][role]['digest'], before['roles'][role]['digest'])
            self.assertEqual(publication['roles'][role]['sourceSHA'], original)
            self.assertEqual(publication['roles'][role]['action'], 'reuse')
        self.assertFalse(any(call[0] == 'docker' for call in self.calls()[call_count:]))
        self.recipe['reusePublication'] = str(reused / 'publication.json')
        (self.repository / 'cmd/chill-server/main.go').write_text('package main\n// changed code input\n')
        self.recipe['source'] = commit('server COPY input change')
        call_count = len(self.calls())
        changed = self.out / 'changed'
        self.cli('--execute', '--evidence', str(changed))
        publication = json.loads((changed / 'publication.json').read_text())
        self.assertEqual(publication['roles']['server']['action'], 'build')
        self.assertEqual(publication['roles']['server']['sourceSHA'], self.recipe['source'])
        self.assertNotEqual(publication['roles']['server']['digest'], before['roles']['server']['digest'])
        self.assertEqual(publication['roles']['dashboard']['action'], 'reuse')
        builds = [call for call in self.calls()[call_count:] if call[0] == 'docker' and 'build' in call]
        self.assertEqual(len(builds), 1)
        self.recipe['reusePublication'] = str(changed / 'publication.json')
        self.recipe['commands'] = {'preflight': [{
            'argv': [sys.executable, '-c',
                     "from pathlib import Path; Path('chill-dashboard/package.json').write_text('{\"name\":\"generated\"}')"],
            'seconds': 10}]}
        local = self.cli('--dry-run')
        planned = json.loads(local.stdout.split('\n', 1)[1])
        self.assertTrue(all(x['action'] == 'build' for x in planned['images']))
        generated = self.out / 'generated'
        self.cli('--execute', '--evidence', str(generated))
        publication = json.loads((generated / 'publication.json').read_text())
        self.assertTrue(all(x['action'] == 'build' for x in publication['roles'].values()))
        self.assertEqual(json.loads((generated / 'source/chill-dashboard/package.json').read_text()),
                         {'name': 'generated'})

    def test_complete_cli_build_registry_tls_and_acceptance(self):
        self.cli('--execute', '--evidence', str(self.out))
        self.assertTrue(json.loads((self.out / 'state.json').read_text())['ready'])
        publication = json.loads((self.out / 'publication.json').read_text())
        self.assertIn('@sha256:', publication['roles']['server']['image'])
        self.assertTrue(json.loads((self.out / 'after/acceptance.json').read_text())['passed'])
        self.assertTrue(any(x[0] == 'docker' for x in self.calls()))
        self.assertFalse(any('prune' in call for call in self.calls()))
        self.assertTrue(any(x[0] == 'helm' and 'upgrade' in x for x in self.calls()))
        self.assertTrue(all(token == 'Bearer ' + self.token for _, token in self.requests))
        self.assertNotIn(self.token, (self.out / 'commands.log').read_text())
        self.assertEqual(self.out.stat().st_mode & 0o077, 0)
        # Reusing an evidence directory cannot accidentally replay side effects.
        previous = len(self.calls())
        self.cli('--execute', '--evidence', str(self.out), expected=1)
        self.assertEqual(len(self.calls()), previous)
        self.recipe.update(images=['server', 'dashboard'], builder='publication-builder',
                           minFreeBytes=350, pruneCompletedBuildCache=True)
        retained = [{'builder': 'publication-builder', 'id': 'active', 'inUse': True, 'bytes': 20},
                    {'builder': 'unrelated-builder', 'id': 'foreign', 'inUse': False, 'bytes': 30}]
        for scenario in ('reclaimed', 'diskRemainsLow', 'failPrune', 'failBuild', 'registryRevision', 'no-approval'):
            with self.subTest(scenario=scenario):
                self.recipe['pruneCompletedBuildCache'] = scenario != 'no-approval'
                fixture = {'diskFixture': True, 'buildCache': copy.deepcopy(retained)}
                if scenario in ('diskRemainsLow', 'failPrune', 'failBuild'):
                    fixture[scenario] = True
                elif scenario == 'registryRevision':
                    fixture[scenario] = 'foreign-source'
                self.state(**fixture)
                call_count = len(self.calls())
                evidence = self.out / scenario
                self.cli('--execute', '--evidence', str(evidence), expected=0 if scenario == 'reclaimed' else 1)
                observed = json.loads((self.directory / 'state.json').read_text())
                calls = self.calls()[call_count:]
                docker = [call for call in calls if call[0] == 'docker']
                self.assertTrue(all(call[call.index('--builder') + 1] == 'publication-builder' for call in docker))
                prunes = [call for call in docker if call[1:3] == ['buildx', 'prune']]
                self.assertEqual(len(prunes), 2 if scenario == 'reclaimed' else 0 if scenario in ('failBuild', 'no-approval') else 1)
                self.assertTrue(all(call == ['docker', 'buildx', 'prune', '--builder', 'publication-builder',
                                             '--force', '--filter', 'inuse=false'] for call in prunes))
                self.assertEqual([entry for entry in observed['buildCache'] if entry['id'] in ('active', 'foreign')], retained)
                if scenario == 'reclaimed':
                    self.assertEqual(observed['buildCache'], retained)
                    self.assertEqual(observed['receiptsBeforeBuild'], [[], ['server']])
                    self.assertTrue(json.loads((evidence / 'state.json').read_text())['ready'])
                    self.assertTrue(json.loads((evidence / 'after/acceptance.json').read_text())['passed'])
                    proof = json.loads((evidence / 'publication.json').read_text())
                    self.assertEqual(set(proof['roles']), {'server', 'dashboard'})
                    self.assertTrue(all(entry['platformReceipts'] for entry in proof['roles'].values()))
                else:
                    self.assertEqual(json.loads((evidence / 'failure.json').read_text())['stage'], 'publish-images')
                    self.assertFalse((evidence / 'publication.json').exists())
                    self.assertFalse(any('upgrade' in call or 'apply' in call for call in calls))
                if scenario in ('diskRemainsLow', 'no-approval'):
                    self.assertLess(json.loads((evidence / 'disk.json').read_text())['free'], 350)
        roles = ['server', 'query', 'dashboard', 'profiler', 'evaluation-harness', 'firstfloor']
        self.recipe.update(images=roles, publicationOnly=['evaluation-harness', 'firstfloor'],
                           minFreeBytes=1, pruneCompletedBuildCache=True)
        self.state(failPruneAt=6)
        stopped = self.out / 'five-receipts-pending-firstfloor'
        call_count = len(self.calls())
        self.cli('--execute', '--evidence', str(stopped), expected=1)
        partial = json.loads((stopped / 'publication.json').read_text())
        self.assertEqual(set(partial['roles']), set(roles[:-1]))
        self.assertTrue((stopped / 'firstfloor-build.json').exists())
        self.assertFalse(any('upgrade' in call or 'apply' in call for call in self.calls()[call_count:]))
        frozen = {path: path.read_bytes() for path in stopped.iterdir() if path.is_file()}
        self.recipe.update(reusePublication=str(stopped / 'publication.json'),
                           reusePendingBuilds={'firstfloor': str(stopped / 'firstfloor-build.json')})
        self.state()
        call_count = len(self.calls())
        self.cli('--inspect', '--evidence', str(self.out / 'pending-inspection'))
        recovered = self.out / 'reverified-six-roles'
        self.cli('--execute', '--evidence', str(recovered))
        proof = json.loads((recovered / 'publication.json').read_text())
        self.assertEqual(set(proof['roles']), set(roles))
        self.assertTrue(all(proof['roles'][role]['action'] == 'reuse' for role in roles[:-1]))
        self.assertEqual(proof['roles']['firstfloor']['action'], 'reuse-pending')
        self.assertTrue(all(entry['platformReceipts'] for entry in proof['roles'].values()))
        self.assertFalse(any(call[0] == 'docker' for call in self.calls()[call_count:]))
        self.assertTrue(json.loads((recovered / 'state.json').read_text())['ready'])
        self.assertTrue(json.loads((recovered / 'after/acceptance.json').read_text())['passed'])
        self.assertTrue(all(path.read_bytes() == data for path, data in frozen.items()))

    def test_cli_watches_build_before_exit_and_then_completes(self):
        barrier = self.directory / 'watch-build-release'
        self.state(buildBarrier=str(barrier))
        recipe = self.directory / 'watch-recipe.json'
        recipe.write_text(json.dumps(self.recipe))
        environment = dict(os.environ, FAKE_DEPLOY_DIR=str(self.directory),
                           PATH=str(self.bin) + os.pathsep + os.environ['PATH'])
        environment.pop('CHILL_EXPERIMENT_CONFIG', None)
        process = subprocess.Popen([str(ROOT / 'hack/fast-deploy.sh'), 'run', str(recipe),
                                    '--execute', '--evidence', str(self.out)],
                                   cwd=ROOT, env=environment, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE)
        visible = b''
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(process.stdout, selectors.EVENT_READ)
                deadline = time.monotonic() + 20
                while b'BUILD_PROGRESS_BEFORE_COMPLETION' not in visible:
                    remaining = deadline - time.monotonic()
                    self.assertGreater(remaining, 0, visible.decode(errors='replace'))
                    self.assertTrue(selector.select(remaining), visible.decode(errors='replace'))
                    data = os.read(process.stdout.fileno(), 65536)
                    self.assertTrue(data, visible.decode(errors='replace'))
                    visible += data
            self.assertIsNone(process.poll())
            self.assertFalse((self.out / 'publication.json').exists())
        finally:
            barrier.touch()
            try:
                output, errors = process.communicate(timeout=90)
            except BaseException:
                process.kill()
                process.communicate()
                raise
        self.assertEqual(process.returncode, 0, errors.decode(errors='replace'))
        self.assertTrue(json.loads((self.out / 'state.json').read_text())['ready'])
        self.assertNotIn(self.token.encode(), visible + output)

    def test_custom_command_timeout_does_not_print_secret_arguments(self):
        secret = 'PRIVATE_TIMEOUT_ARGUMENT_SENTINEL'
        self.recipe['commands'] = {'preflight': [{
            'argv': [sys.executable, '-c', 'import time; time.sleep(60)', secret],
            'seconds': 1}]}
        result = self.cli('--execute', '--evidence', str(self.out), expected=1)
        self.assertNotIn(secret, result.stdout + result.stderr)
        self.assertEqual(json.loads((self.out / 'failure.json').read_text())['stage'], 'preflight')

    def test_cli_waits_for_warming_and_records_ready(self):
        self.state(warmingOnce=True)
        self.recipe['readinessSeconds'] = 15
        self.cli('--execute', '--evidence', str(self.out))
        self.assertTrue(json.loads((self.out / 'state.json').read_text())['ready'])
        self.assertEqual((self.out / 'after/attempt-000/coverage.http-status').read_text().strip(), '503')
        self.assertFalse((self.out / 'after/attempt-000/profiles.json').exists())
        self.assertEqual((self.out / 'after/attempt-001/profiles.http-status').read_text().strip(), '200')
        self.assertEqual(sum(path.endswith('/profiles') for path, _ in self.requests), 2)

    def test_cold_listener_wait_budget_and_operator_override(self):
        for label, cold_start, configured, success in (
                ('default', 700, None, True),
                ('larger-catalog', 1100, 1200, True),
                ('explicit-short-budget', 700, 600, False)):
            with self.subTest(label=label):
                self.state(serverReadySeconds=cold_start)
                if configured is None:
                    self.recipe.pop('upgradeSeconds', None)
                else:
                    self.recipe['upgradeSeconds'] = configured
                evidence = self.out / label
                self.cli('--execute', '--evidence', str(evidence), expected=0 if success else 1)
                if success:
                    self.assertTrue(json.loads((evidence / 'state.json').read_text())['ready'])
                    self.assertTrue(json.loads((evidence / 'after/acceptance.json').read_text())['passed'])
                else:
                    self.assertEqual(json.loads((evidence / 'failure.json').read_text())['stage'], 'upgrade')
                    self.assertFalse((evidence / 'after/acceptance.json').exists())

    def test_query_auth_does_not_download_unbounded_catalog_and_continues_acceptance(self):
        self.recipe.update(queryService='chill-query', dashboardCheck=True)
        self.cli('--execute', '--evidence', str(self.out))
        auth = json.loads((self.out / 'query-auth/query-check.json').read_text())
        self.assertTrue(auth['passed'])
        self.assertEqual(auth['query-unauthenticated']['status'], 401)
        self.assertEqual(auth['query-authenticated']['status'], 200)
        self.assertTrue(json.loads((self.out / 'state.json').read_text())['ready'])

    def test_evaluation_sa_permission_denial_stops_accept_and_retains_evidence(self):
        from test_evaluation_rbac import documents
        for denied in (False, True):
            with self.subTest(denied=denied):
                self.state(evaluationRbac=documents(), denyEvaluationPermission=denied)
                evidence = self.out / ('denied' if denied else 'allowed')
                self.cli('--execute', '--evidence', str(evidence), expected=1 if denied else 0)
                receipt = json.loads((evidence / 'evaluation-rbac.json').read_text())
                self.assertEqual(receipt['passed'], not denied)
                if denied:
                    failures = [x for x in receipt['checks'] if x['status'] == 'failed']
                    self.assertEqual([(x['serviceAccount'], x['verb'], x['target']) for x in failures],
                                     [('custom-action', 'get', '/chill/api/v1/workloads')])
                    self.assertEqual(json.loads((evidence / 'failure.json').read_text())['stage'], 'accept')
                    self.assertFalse((evidence / 'post-values.json').exists())
                else:
                    self.assertTrue(json.loads((evidence / 'state.json').read_text())['ready'])
                    self.assertTrue(all(x['status'] == 'allowed' for x in receipt['checks']))

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
        self.recipe['expectedProfileCount'] = 2
        self.cli('--execute', '--evidence', str(self.out))
        report = json.loads((self.out / 'after/acceptance.json').read_text())
        self.assertFalse(report['passed'])
        self.assertEqual(report['numericChanges'][0]['field'], 'energyPerInputJoules')
        self.assertFalse(report['checks']['expectedProfileCount'])
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

    def test_source_archive_keeps_modes_under_private_umask(self):
        import main as runner_module
        self.out.mkdir()
        runner = runner_module.Runner(self.out, ROOT)
        previous = os.umask(0o077)
        try:
            runner_module.archive(runner, 'HEAD', self.out / 'source')
        finally:
            os.umask(previous)
            runner.log.close()
        # Images run as UID 65532 and must read the copied Python harness.
        self.assertTrue((self.out / 'source/hack/evaluation/evaluation.py').stat().st_mode & 0o004)


if __name__ == '__main__':
    unittest.main(verbosity=2)

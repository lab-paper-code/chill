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
if name=='go' and args[:1]==['build'] and args[-1]=='./hack/prejob-retirement-check':
 target=pathlib.Path(argument('-o'));target.write_text(pathlib.Path(__file__).read_text());target.chmod(0o700);sys.exit(0)
if name=='go':
 import shutil
 search=os.pathsep.join(path for path in os.environ['PATH'].split(os.pathsep) if pathlib.Path(path).resolve()!=pathlib.Path(__file__).parent.resolve())
 real=shutil.which('go',path=search)
 if real:os.execv(real,[real,*args])
 sys.exit(127)
if name=='prejob-retirement-check':
 parents=[obj for obj in state['inflightObjects'] if obj['kind']=='BatchEnergyOptimization']
 state['repairProofChecks']=state.get('repairProofChecks',0)+1
 final=state['repairProofChecks']>len(parents)
 failure=state.get('repairProofFailureAtFinal' if final else 'repairProofFailure')
 parent=next(obj for obj in parents if obj['metadata']['name']==argument('--name'))
 meta=parent['metadata']
 child=next(obj for obj in state['inflightObjects'] if obj['kind']=='ProfilingRun' and any(ref.get('uid')==meta['uid'] for ref in obj['metadata'].get('ownerReferences',[])))
 def identity(obj):return {key:obj['metadata'][key] for key in ('namespace','name','uid','resourceVersion','generation')}
 node=next(node for node in state['repairNodes'] if node['spec']['taints'][0]['value']==meta['uid'])
 proof={'version':1,'eligible':failure is None,'reason':'ExecutionPolicyInvalid','request':identity(parent),'child':identity(child),'plan':{'store':'retained','key':'frozen-plan','schemaVersion':'1'},'ownedRunCount':4,'ownedJobCount':0,'idleRuns':[],'planFound':True,'publishedResultAbsent':True,'admission':{'granted':True,'at':'2026-11-05T00:00:00Z'},'node':{'name':node['metadata']['name'],'uid':node['metadata']['uid'],'resourceVersion':node['metadata']['resourceVersion'],'holder':meta['uid']},'checkedAt':'2026-11-06T00:00:00Z'}
 if final:
  proof['request']['resourceVersion']+='-fresh';proof['child']['resourceVersion']+='-fresh';proof['node']['resourceVersion']+='-heartbeat'
 if failure=='changed-identity':proof['request']['uid']='different-uid';proof['eligible']=True
 elif failure=='changed-child':proof['child']['uid']='different-child';proof['eligible']=True
 elif failure=='changed-generation':proof['child']['generation']+=1;proof['eligible']=True
 elif failure=='wrong-reason':proof['reason']='Unknown';proof['eligible']=True
 elif failure:proof['reason']=failure
 save();print(json.dumps(proof))
 sys.exit(0 if proof['eligible'] else 1)
def document(image):
 return {'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'chill-serving-entrypoint','namespace':'review'},'spec':{'template':{'spec':{'containers':[{'name':'server','image':image}]}}}}
def dashboard_hook(changed=False):
 return {'apiVersion':'batch/v1','kind':'Job','metadata':{'name':'chill-evaluation-store-ensure-bucket','namespace':'review','annotations':{'helm.sh/hook':'post-upgrade','helm.sh/hook-delete-policy':'before-hook-creation,hook-succeeded'}},'spec':{'template':{'spec':{'restartPolicy':'Never','containers':[{'name':'ensure-bucket','image':'store-client:retained','command':['/ensure-bucket','changed-bucket' if changed else 'measurements']}]}}}}
def dashboard_documents(image,desired=False):
 backend=document('backend:retained')
 dashboard={'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'chill-dashboard','namespace':'review','labels':{'app.kubernetes.io/instance':'chill'}},'spec':{'replicas':1,'selector':{'matchLabels':{'app':'dashboard'}},'template':{'metadata':{'labels':{'app':'dashboard'}},'spec':{'containers':[{'name':'dashboard','image':image,'ports':[{'containerPort':3000}],'env':[{'name':'CHILL_API_BASE','value':'http://chill-serving-entrypoint:8080'}]}]}}}}
 crd={'apiVersion':'apiextensions.k8s.io/v1','kind':'CustomResourceDefinition','metadata':{'name':'batchenergyoptimizations.chill.example.test'},'spec':{'group':'chill.example.test','scope':'Namespaced','names':{'plural':'batchenergyoptimizations','singular':'batchenergyoptimization','kind':'BatchEnergyOptimization'},'versions':[{'name':'v1','served':True,'storage':True,'schema':{'openAPIV3Schema':{'type':'object'}}}]}}
 config={'apiVersion':'v1','kind':'ConfigMap','metadata':{'name':'chill-measurement-config','namespace':'review'},'data':{'measurementWindowSeconds':'60'}}
 if desired:
  change=state.get('dashboardChange')
  if image!='dashboard:retained':change=state.get('dashboardChangeAtFinal',change)
  if change=='server':backend['spec']['template']['spec']['containers'][0]['image']='backend:changed'
  elif change=='crd':crd['spec']['versions'][0]['schema']['openAPIV3Schema']['description']='changed contract'
  elif change=='measurement-config':config['data']['measurementWindowSeconds']='120'
 result=[backend,dashboard,crd,config]
 if desired and state.get('dashboardChange')=='duplicate':result.append(dashboard)
 if desired and state.get('dashboardChange')=='missing-dashboard':result.remove(dashboard)
 if desired:result.append(dashboard_hook(change=='hook'))
 return result
def prejob_documents(server='old:source',query='query:retained',profiler='profiler:retained',desired=False):
 result=dashboard_documents('dashboard:retained')
 backend=result[0];backend['spec']['template']['spec']['containers'][0].update(name='serving-entrypoint',image=server)
 backend['spec']['template']['spec']['containers'].append({'name':'sidecar','image':'sidecar:retained'})
 query_deployment=document(query);query_deployment['metadata']['name']='chill-query';query_deployment['spec']['template']['spec']['containers'][0]['name']='query'
 result.append(query_deployment)
 config={'apiVersion':'v1','kind':'ConfigMap','metadata':{'name':'chill-profiler-config','namespace':'review'},'data':{'config.yaml':json.dumps({'image':profiler,'measurement':{'policy':'retained','windowSeconds':60},'healthProbePort':8081,'measurementMemoryFraction':0.8}),'other':'retained'}}
 result.append(config)
 result.append({'apiVersion':'chill.dacs.io/v1alpha1','kind':'ChillSystem','metadata':{'name':'chill'},'spec':{'managementNamespace':'review','evidenceStore':{'endpoint':'http://retained-store:9000','bucket':'measurements'}}})
 change=state.get('repairChange')
 if desired and server!='old:source':change=state.get('repairChangeAtFinal',change)
 if desired:
  if change=='other-image':backend['spec']['template']['spec']['containers'][1]['image']='sidecar:changed'
  elif change=='crd':result[2]['spec']['versions'][0]['schema']['openAPIV3Schema']['description']='changed'
  elif change=='backend-config':result[3]['data']['measurementWindowSeconds']='120'
  elif change in ('profiler-config','policy','runtime','observer'):
   inner=json.loads(config['data']['config.yaml']);inner[change]='changed';config['data']['config.yaml']=json.dumps(inner)
  elif change=='duplicate-inner':config['data']['config.yaml']='image: profiler:retained\nimage: profiler:changed\n'
  elif change=='non-string-inner':config['data']['config.yaml']='image: profiler:retained\ntrue: hidden\n1: collision\n'
  elif change=='typed-inner':config['data']['config.yaml']=config['data']['config.yaml'].replace('"windowSeconds": 60','"windowSeconds": 60.0')
  elif change=='sexagesimal-int':config['data']['config.yaml']=config['data']['config.yaml'].replace('"healthProbePort": 8081','"healthProbePort": 2:14:41')
  elif change=='sexagesimal-float':config['data']['config.yaml']=config['data']['config.yaml'].replace('"measurementMemoryFraction": 0.8','"measurementMemoryFraction": 0:0.8')
  elif change=='duplicate':result.append(query_deployment)
  elif change=='missing-query':result.remove(query_deployment)
 if desired:result.append(dashboard_hook(change=='hook'))
 return result
def emit(documents,desired=False):
 raw_documents=[]
 for item in documents:
  raw=json.dumps(item)
  if state.get('nonStringBackendKeys') and item['metadata']['name']=='chill-serving-entrypoint':
   annotations='{true: "'+('changed' if desired else 'old')+'", 1: "stable"}'
   raw=raw.replace('"template": {','"template": {"metadata": {"annotations": '+annotations+'}, ',1)
  raw_documents.append(raw)
 print('\n---\n'.join(raw_documents))
if name=='docker':
 if args[:2]==['buildx','du']:print('Total: 20GB');sys.exit(0)
 if args[:2]==['buildx','prune']:
  state['pruneCount']=state.get('pruneCount',0)+1;save()
  if state.get('failPrune') or state['pruneCount']==state.get('failPruneAt'):sys.exit(18)
  builder=argument('--builder')
  # BuildKit exposes inuse as presence, not a string-valued boolean.
  if '--filter' in args and argument('--filter')=='inuse=false':print('Total: 0B');sys.exit(0)
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
 for platform in state.get('registryPlatforms',argument('--platform').split(',')):
  system,arch=platform.split('/');config={'os':system,'architecture':arch,'config':{'Labels':{} if revision is None else {'org.opencontainers.image.revision':revision}}};cd=store('blobs',config)
  child={'schemaVersion':2,'config':{'digest':cd}};d=store('manifests',child);manifests.append({'digest':d,'platform':{'os':system,'architecture':arch}})
 index={'schemaVersion':2,'manifests':manifests};digest=store('manifests',index);(root/'registry'/'v2'/repo/'manifests'/tag).write_bytes((root/'registry'/'v2'/repo/'manifests'/digest).read_bytes())
 pathlib.Path(argument('--metadata-file')).write_text(json.dumps({'containerimage.digest':digest,'image.name':image}))
 if state.get('movedTag'):(root/'registry'/'v2'/repo/'manifests'/tag).write_text('{"schemaVersion":2,"manifests":[]}')
 if state.get('missingArtifact'):(root/'registry'/'v2'/repo/'manifests'/digest).unlink()
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
  if what=='values':
   client={'apiServer':'https://kubernetes.review:6443','discoverFromDefaultEndpoint':False}
   values={'nodeAgent':{'kubernetesClient':client},'profiler':{'workloadObserver':{'kubernetesClient':client}}}
   print(json.dumps(state.get('dashboardValues',values) if state.get('dashboardFixture') or state.get('repairFixture') else {}))
  elif what=='manifest':
   if state.get('repairFixture'):emit(prejob_documents())
   elif state.get('dashboardFixture'):emit(dashboard_documents('dashboard:retained'))
   else:print(json.dumps(document('old:source')))
  elif what=='hooks' and (state.get('dashboardFixture') or state.get('repairFixture')):emit([dashboard_hook()])
  else:print('')
 elif verb=='template':
  image='old:source';dashboard_image='dashboard:retained';profiler_image='profiler:retained';query_image='query:retained';replicas=None;published=False
  for i,arg in enumerate(args):
   if arg=='-f':
    path=pathlib.Path(args[i+1])
    if path.name=='image-overlay.json':
     value=json.loads(path.read_text());item=value.get('servingEntrypoint',{}).get('image');image=item['repository']+':'+item['tag'] if item else image
     item=value.get('dashboard',{}).get('image');dashboard_image=item['repository']+':'+item['tag'] if item else dashboard_image;published=True
     item=value.get('profiler',{}).get('image');profiler_image=item['repository']+':'+item['tag'] if item else profiler_image
     item=value.get('query',{}).get('image');query_image=item['repository']+':'+item['tag'] if item else query_image
    elif path.name.startswith('staged-values-'):
     replicas=json.loads(path.read_text()).get('operator',{}).get('replicaCount')
  if state.get('repairFixture'):
   malformed=state.get('malformedRepairRender')
   if malformed=='before' or malformed=='final' and published:print('{invalid render')
   else:emit(prejob_documents(image,query_image,profiler_image,True),True)
  elif state.get('dashboardFixture'):
   malformed=state.get('malformedDashboardRender')
   if malformed=='before' or malformed=='final' and published:print('{invalid render')
   else:emit(dashboard_documents(dashboard_image,True),True)
  elif state.get('chartDrift'):print(json.dumps({'apiVersion':'v1','kind':'ConfigMap','metadata':{'name':'changed','namespace':'review'}}))
  else:
   result=document(image)
   if replicas is not None:result['spec']['replicas']=replicas
   print(json.dumps(result))
 elif verb=='upgrade':
  if state.get('failUpgrade'):sys.exit(19)
  duration=argument('--timeout');wait_seconds=int(duration[:-1])*{'s':1,'m':60}[duration[-1]]
  if state.get('serverReadySeconds',0)>wait_seconds:
   print('timed out waiting for server listener',file=sys.stderr);sys.exit(20)
  if (state.get('dashboardFixture') or state.get('repairFixture')) and '--no-hooks' not in args:
   state['hookExecutions']=state.get('hookExecutions',0)+1
   state.setdefault('ensuredBuckets',[]).append('measurements')
  overlay=pathlib.Path(args[args.index('-f')+1])
  for i,arg in enumerate(args):
   if arg=='-f' and pathlib.Path(args[i+1]).name=='image-overlay.json':overlay=pathlib.Path(args[i+1])
  state['publication']=str(overlay.parent/'publication.json');state['upgraded']=True;save();print('upgraded')
 if verb=='template' or verb=='get' and args[args.index('get')+1]=='manifest':
  for item in state.get('evaluationRbac',[]):
   print('---\n# Source: chill/templates/evaluation_rbac.yaml\n'+json.dumps(item))
 sys.exit(0)
if name=='kubectl':
 if (state.get('dashboardFixture') or state.get('repairFixture')) and 'get' in args and args[args.index('get')+1]=='crd':
  emit([dashboard_documents('dashboard:retained')[2]]);sys.exit(0)
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
   facts={'machine':state['machines'][pod['spec']['nodeName']],'ortVersion':state.get('importedOrtVersion','1.24.0'),'providers':['CPUExecutionProvider','CUDAExecutionProvider'],'packages':{'onnxruntime':'1.24.0','onnxruntime-gpu':'1.24.0','numpy':'1.26.4','rknn-toolkit-lite2':'2.3.0'},'gpuUUIDs':['GPU-observed-'+pod['spec']['nodeName']],'driverVersion':'fixture-driver','compatible':['rockchip,rk3588']}
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
  items=state.get('inflightObjects',[])
  if state.get('inflightAt')==state['inflightChecks']:items=[{'kind':'DispatchOccupancyMeasurement','metadata':{'name':'active','namespace':'another-chill-ns'},'status':{'phase':'Running'}}]
  if state.get('repairFixture') and state['inflightChecks']==2:
   items=json.loads(json.dumps(items))
   change=state.get('cohortChange')
   for obj in items:
    if 'resourceVersion' not in obj['metadata']:continue
    obj['metadata']['resourceVersion']+='-fresh'
    if change=='generation' and obj['kind']=='ProfilingRun':obj['metadata']['generation']+=1
    elif change=='uid' and obj['kind']=='ProfilingRun':obj['metadata']['uid']+='-replacement'
   if change=='new-pair':
    extra=json.loads(json.dumps(items[:2]))
    for obj in extra:
     obj['metadata']['name']+='-new';obj['metadata']['uid']+='-new'
     for owner in obj['metadata'].get('ownerReferences',[]):owner['name']+='-new';owner['uid']+='-new'
    items+=extra
    node=json.loads(json.dumps(state['repairNodes'][0]));node['metadata']['name']+='-new';node['metadata']['uid']+='-new';node['spec']['taints'][0]['value']=extra[0]['metadata']['uid'];state['repairNodes'].append(node)
   if change:
    state['inflightObjects']=items;save()
  print(json.dumps({'items':items}));sys.exit(0)
 if 'get' in args and args[args.index('get')+1]=='jobs' and '--all-namespaces' in args:
  print(json.dumps({'items':state.get('inflightJobs',[])}));sys.exit(0)
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
   items.append({'metadata':{'name':role},'spec':{'containers':[{'name':role,'image':'foreign:old' if state.get('wrongPodImage') else entry['image']}]},'status':{'conditions':[{'type':'Ready','status':'False' if state.get('podNotReady') else 'True'}],'containerStatuses':[{'name':role,'ready':not state.get('containerNotReady',False),'restartCount':state.get('podRestartCount',0),'imageID':state.get('podImageID','registry@'+entry['platformDigests'][0])}]}})
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
                if self.path in ('/api/v1/scheduler/settings', '/api/v1/scheduler/state',
                                 '/api/v1/scheduler/state?view=compact'):
                    state = json.loads((cls.directory / 'state.json').read_text())
                    if state.get('runtimeFixture'):
                        if self.path.endswith('/settings'):
                            self.reply(state['settings'], etag='"%d"' % state['settingsVersion'])
                        else:
                            snapshot = {'instances': [] if state.get('removed') else [state['instance']]}
                            if state.get('oversizedState') and self.path == '/api/v1/scheduler/state':
                                snapshot['options'] = [{'profile': {'batches': [{'serviceSamplesSeconds': [0.001] * 1400000}]}}]
                            self.reply({'snapshot': snapshot})
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
        for name in ('helm', 'kubectl', 'docker', 'go'):
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
        for scenario in ('success', 'oversized-state', 'probe-fail', 'remove-failed', 'remove-conflict', 'accepted-request', 'eligibility-race',
                         'foreign-consumer', 'concurrent-unrelated', 'concurrent-tracked'):
            with self.subTest(scenario=scenario):
                fixture = {'runtimeFixture': True, 'nodes': nodes_fixture(ROOT), 'gpuNodes': gpu_nodes, 'machines': machines,
                           'owner': owner, 'instance': copy.deepcopy(instance), 'servingPod': copy.deepcopy(pod),
                           'settings': copy.deepcopy(settings), 'settingsVersion': 1,
                           'importedOrtVersion': 'observed-import-version'}
                if scenario == 'oversized-state':
                    fixture['oversizedState'] = True
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
        self.state(movedTag=True, registryRevision=None, podRestartCount=4, podImageID='containerd://opaque')
        initial = self.out / 'initial'
        self.cli('--execute', '--evidence', str(initial))
        before = json.loads((initial / 'publication.json').read_text())
        stale = copy.deepcopy(before)
        stale['sourceSHA'] = 'old-deployment-metadata'
        for entry in stale['roles'].values():
            entry.update(platformDigests=None, platformReceipts=None)
            entry['image'] = entry['image'].split('@', 1)[0] + '@old-receipt-digest'
        (initial / 'publication.json').write_text(json.dumps(stale))
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
            self.assertIsNone(publication['roles'][role]['platformReceipts'][0]['revision'])
        self.assertFalse(any(call[0] == 'docker' for call in self.calls()[call_count:]))
        self.state(movedTag=True, podRestartCount=4, podImageID='containerd://opaque')
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
        missing_origin = copy.deepcopy(publication)
        missing_origin['roles']['dashboard'].pop('sourceSHA')
        (changed / 'publication.json').write_text(json.dumps(missing_origin))
        self.recipe['reusePublication'] = str(changed / 'publication.json')
        without_origin = self.out / 'without-origin'
        self.cli('--execute', '--evidence', str(without_origin))
        rebuilt = json.loads((without_origin / 'publication.json').read_text())
        self.assertEqual(rebuilt['roles']['dashboard']['action'], 'build')
        self.assertEqual(rebuilt['roles']['dashboard']['sourceSHA'], self.recipe['source'])
        self.recipe['reusePublication'] = str(without_origin / 'publication.json')
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

    def test_cli_keeps_prebuilt_origin_and_raw_revision_despite_stale_metadata(self):
        self.state(registryRevision='observed-old-revision', movedTag=True,
                   registryPlatforms=['linux/amd64', 'linux/arm64'], podRestartCount=3,
                   podImageID='cri-o://opaque-status')
        initial = self.out / 'initial'
        self.cli('--execute', '--evidence', str(initial))
        receipt = json.loads((initial / 'publication.json').read_text())
        entry = receipt['roles']['server']
        entry.update(sourceSHA='recorded-build-origin', platformDigests=None,
                     platformReceipts=None, dockerfile='historic/Dockerfile',
                     buildArgs={'historic': 'value'})
        receipt['sourceSHA'] = 'old-deployment'
        path = self.out / 'archived-receipt.json'
        path.write_text(json.dumps(receipt))
        self.recipe['images'] = [{'role': 'server', 'prebuilt': str(path)}]
        selected = self.out / 'selected'
        self.cli('--execute', '--evidence', str(selected))
        publication = json.loads((selected / 'publication.json').read_text())
        selected_entry = publication['roles']['server']
        self.assertTrue(json.loads((selected / 'state.json').read_text())['ready'])
        self.assertEqual(selected_entry['sourceSHA'], 'recorded-build-origin')
        self.assertEqual(selected_entry['buildArgs'], {'historic': 'value'})
        self.assertEqual(selected_entry['platformReceipts'][0]['revision'], 'observed-old-revision')
        self.assertEqual([r['platform'] for r in selected_entry['platformReceipts']], ['linux/amd64'])
        self.assertEqual(path.read_text(), json.dumps(receipt))

    def test_cli_unavailable_artifact_platform_and_current_readiness_stop(self):
        for failure in ('missingArtifact', 'unavailable-platform', 'wrongPodImage',
                        'podNotReady', 'containerNotReady'):
            with self.subTest(failure=failure):
                self.state(**({'registryPlatforms': ['linux/arm64']} if failure == 'unavailable-platform'
                              else {failure: True}))
                evidence = self.out / failure
                self.cli('--execute', '--evidence', str(evidence), expected=1)
                self.assertFalse(json.loads((evidence / 'state.json').read_text())['ready'])
                if failure in ('missingArtifact', 'unavailable-platform'):
                    cluster = json.loads((self.directory / 'state.json').read_text())
                    self.assertFalse(cluster.get('upgraded'))

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
        for scenario in ('reclaimed', 'diskRemainsLow', 'failPrune', 'failBuild', 'no-approval'):
            with self.subTest(scenario=scenario):
                self.recipe['pruneCompletedBuildCache'] = scenario != 'no-approval'
                fixture = {'diskFixture': True, 'buildCache': copy.deepcopy(retained)}
                if scenario in ('diskRemainsLow', 'failPrune', 'failBuild'):
                    fixture[scenario] = True
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
        metadata = self.out / 'archived-firstfloor-build.json'
        shutil.copy2(stopped / 'firstfloor-build.json', metadata)
        frozen_plan_path = stopped / 'image-plan.json'
        frozen_plan = json.loads(frozen_plan_path.read_text())
        frozen_plan[-1].update(action='reuse', metadataFile='old-bookkeeping-path')
        frozen_plan_path.write_text(json.dumps(frozen_plan))
        frozen = {path: path.read_bytes() for path in stopped.iterdir() if path.is_file()}
        self.recipe.update(reusePublication=str(stopped / 'publication.json'),
                           reusePendingBuilds={'firstfloor': str(metadata)})
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

    def prejob_rollout(self, **changes):
        self.recipe.update(images=['server', 'query', 'profiler'], prejobRepairOnly=True)
        retained, nodes = [], []
        for index, duration in enumerate((2027, 1792, 1970)):
            name = 'frozen-request-%d' % index
            uid = 'parent-uid-%d' % index
            parent = {'apiVersion': 'edge.dacs.io/v1alpha1', 'kind': 'BatchEnergyOptimization',
                      'metadata': {'name': name, 'namespace': 'review', 'uid': uid,
                                   'resourceVersion': str(400 + index), 'generation': 1},
                      'status': {'phase': 'Running', 'completedTrials': 3, 'plan': {'key': name + '/plan.json'}}}
            child = {'apiVersion': 'edge.dacs.io/v1alpha1', 'kind': 'ProfilingRun',
                     'metadata': {'name': name + '-active-b1-a1', 'namespace': 'review',
                                  'uid': 'child-uid-%d' % index, 'resourceVersion': str(500 + index),
                                  'generation': 1, 'ownerReferences': [{
                                      'apiVersion': parent['apiVersion'], 'kind': parent['kind'],
                                      'name': name, 'uid': uid, 'controller': True}]},
                     'spec': {'metricObservation': {'durationSeconds': duration}}}
            retained.extend((parent, child))
            for idle in range(3):
                run = copy.deepcopy(child)
                run['metadata'].update(name=name + '-idle-%d' % idle,
                                       uid='idle-uid-%d-%d' % (index, idle))
                run['status'] = {'conditions': [{'type': 'EvidenceVerified', 'status': 'True'}]}
                retained.append(run)
            nodes.append({'apiVersion': 'v1', 'kind': 'Node',
                          'metadata': {'name': 'held-node-%d' % index, 'uid': 'node-uid-%d' % index,
                                       'resourceVersion': str(600 + index)},
                          'spec': {'taints': [{'key': 'chill.dacs.io/measurement',
                                               'value': uid, 'effect': 'NoSchedule'}]}})
        self.state(repairFixture=True, inflightObjects=retained, repairNodes=nodes, **changes)
        return retained, nodes

    def assert_prejob_preserved(self, retained, nodes, evidence, *, published=False, upgraded=False):
        cluster = json.loads((self.directory / 'state.json').read_text())
        self.assertEqual(cluster['inflightObjects'], retained)
        self.assertEqual(cluster['repairNodes'], nodes)
        self.assertEqual(bool(cluster.get('upgraded')), upgraded)
        self.assertEqual((evidence / 'publication.json').exists(), published)
        for field in ('apiMutations', 'probeCount', 'hookExecutions', 'ensuredBuckets'):
            self.assertFalse(cluster.get(field), field)
        for call in self.calls():
            if call[0] == 'kubectl':
                self.assertFalse(any(verb in call for verb in ('create', 'delete', 'patch', 'replace')))
                if 'apply' in call:
                    self.assertIn('--dry-run=server', call)

    def test_prejob_six_blockers_three_images_preserve_frozen_cohort(self):
        import yaml
        import inflight
        retained, nodes = self.prejob_rollout()
        self.cli('--execute', '--evidence', str(self.out))
        self.assert_prejob_preserved(retained, nodes, self.out, published=True, upgraded=True)
        state = json.loads((self.out / 'state.json').read_text())
        self.assertTrue(state['ready'])
        expected_blockers = inflight.blockers(retained, [])
        self.assertEqual(len(expected_blockers), 6)
        for stage in ('Preflight', 'PreUpgrade'):
            report = state['inflight' + stage]
            self.assertEqual(report['blockers'], expected_blockers)
            captured = Path(report['evidenceDirectory'])
            self.assertEqual(json.loads((captured / 'blockers.json').read_text()), expected_blockers)
            self.assertEqual(json.loads((captured / 'jobs.json').read_text()), {'items': []})
        publication = json.loads((self.out / 'publication.json').read_text())
        self.assertEqual(set(publication['roles']), {'server', 'query', 'profiler'})
        live = list(yaml.safe_load_all(next(self.out.glob('*-live-manifest.yaml')).read_text()))
        live += list(yaml.safe_load_all(next(self.out.glob('*-live-hooks.yaml')).read_text()))
        final = list(yaml.safe_load_all((self.out / 'render-final.yaml').read_text()))
        for item in final:
            name = item['metadata']['name']
            if name in ('chill-serving-entrypoint', 'chill-query'):
                role = 'server' if name.endswith('serving-entrypoint') else 'query'
                container = item['spec']['template']['spec']['containers'][0]
                self.assertEqual(container['image'], publication['roles'][role]['image'])
                container['image'] = 'old:source' if role == 'server' else 'query:retained'
            elif name == 'chill-profiler-config':
                config = yaml.safe_load(item['data']['config.yaml'])
                self.assertEqual(config['image'], publication['roles']['profiler']['image'])
                config['image'] = 'profiler:retained'
                item['data']['config.yaml'] = json.dumps(config)
        self.assertEqual(final, live)
        selected = list(yaml.safe_load_all((self.out / 'prejob-image-objects.yaml').read_text()))
        self.assertEqual({(obj['kind'], obj['metadata']['name']) for obj in selected},
                         {('Deployment', 'chill-serving-entrypoint'), ('Deployment', 'chill-query'),
                          ('ConfigMap', 'chill-profiler-config')})
        upgrades = [call for call in self.calls() if call[0] == 'helm' and 'upgrade' in call]
        self.assertEqual(len(upgrades), 1)
        self.assertTrue(json.loads((self.out / 'after/acceptance.json').read_text())['passed'])

    def test_prejob_failed_or_mismatched_authority_stops_before_effects(self):
        for failure in ('existing-job', 'foreign-owner', 'api-error', 'store-error', 'Unknown',
                        'NoSuchBucket', 'changed-identity', 'changed-child', 'changed-generation', 'wrong-reason'):
            for final in (False, True):
                with self.subTest(failure=failure, final=final):
                    key = 'repairProofFailureAtFinal' if final else 'repairProofFailure'
                    retained, nodes = self.prejob_rollout(**{key: failure})
                    evidence = self.out / (failure + ('-final' if final else '-preflight'))
                    self.cli('--execute', '--evidence', str(evidence), expected=1)
                    self.assert_prejob_preserved(retained, nodes, evidence, published=final)
                    self.assertFalse(any('upgrade' in call for call in self.calls()))
                    (self.directory / 'calls.jsonl').write_text('')

    def test_prejob_mixed_or_unknown_render_stops(self):
        for change in ('other-image', 'crd', 'backend-config', 'profiler-config', 'policy', 'runtime',
                       'observer', 'hook', 'duplicate-inner', 'non-string-inner', 'typed-inner',
                       'sexagesimal-int', 'sexagesimal-float', 'duplicate', 'missing-query'):
            for final in (False, True):
                with self.subTest(change=change, final=final):
                    key = 'repairChangeAtFinal' if final else 'repairChange'
                    retained, nodes = self.prejob_rollout(**{key: change})
                    evidence = self.out / (change + ('-final' if final else '-preflight'))
                    self.cli('--execute', '--evidence', str(evidence), expected=1)
                    self.assert_prejob_preserved(retained, nodes, evidence, published=final)
                    (self.directory / 'calls.jsonl').write_text('')
        for phase in ('before', 'final'):
            retained, nodes = self.prejob_rollout(malformedRepairRender=phase)
            evidence = self.out / ('malformed-' + phase)
            self.cli('--execute', '--evidence', str(evidence), expected=1)
            self.assert_prejob_preserved(retained, nodes, evidence, published=phase == 'final')
            (self.directory / 'calls.jsonl').write_text('')

    def test_prejob_unproved_or_other_blocker_stops(self):
        for change in ('unrelated-final', 'unrelated-preflight', 'native-job', 'foreign-owner', 'duplicate-object'):
            with self.subTest(change=change):
                kwargs = {}
                if change == 'unrelated-final':
                    kwargs['inflightAt'] = 2
                retained, nodes = self.prejob_rollout(**kwargs)
                fixture = json.loads((self.directory / 'state.json').read_text())
                if change == 'unrelated-preflight':
                    fixture['inflightObjects'].append({'kind': 'EvaluationRun', 'metadata': {
                        'name': 'unrelated', 'namespace': 'other', 'uid': 'unrelated-uid',
                        'resourceVersion': '999', 'generation': 1}})
                elif change == 'native-job':
                    fixture['inflightJobs'] = [{'kind': 'Job', 'metadata': {'name': 'existing-job',
                        'namespace': 'review', 'ownerReferences': [{'kind': 'ProfilingRun'}]}}]
                elif change == 'foreign-owner':
                    fixture['inflightObjects'][1]['metadata']['ownerReferences'][0]['uid'] = 'foreign'
                elif change == 'duplicate-object':
                    fixture['inflightObjects'].append(copy.deepcopy(fixture['inflightObjects'][0]))
                self.state(**fixture)
                retained = fixture['inflightObjects']
                evidence = self.out / change
                self.cli('--execute', '--evidence', str(evidence), expected=1)
                self.assert_prejob_preserved(retained, nodes, evidence,
                                            published=change == 'unrelated-final')
                self.assertFalse(any('upgrade' in call for call in self.calls()))
                (self.directory / 'calls.jsonl').write_text('')

    def test_prejob_fresh_authority_can_approve_changed_cohort(self):
        for change in ('uid', 'generation', 'new-pair'):
            with self.subTest(change=change):
                self.prejob_rollout(cohortChange=change)
                evidence = self.out / change
                self.cli('--execute', '--evidence', str(evidence))
                state = json.loads((evidence / 'state.json').read_text())
                self.assertTrue(state['ready'])
                first = state['prejobProofPreflight']['cohort']
                final = state['prejobProofPreUpgrade']['cohort']
                self.assertNotEqual(first, final)
                cluster = json.loads((self.directory / 'state.json').read_text())
                self.assert_prejob_preserved(cluster['inflightObjects'], cluster['repairNodes'], evidence,
                                             published=True, upgraded=True)
                (self.directory / 'calls.jsonl').write_text('')

    def test_prejob_scope_rejects_overrides_before_cluster_access(self):
        self.prejob_rollout()
        original = copy.deepcopy(self.recipe)
        for index, override in enumerate((
                {'prejobRepairOnly': 1}, {'images': ['server', 'profiler']},
                {'images': ['server', 'query', 'profiler', 'operator']},
                {'values': ['unread-values.yaml']}, {'replaceValues': True},
                {'generatedValues': ['uncreated.yaml']}, {'runtimeDeclarations': True},
                {'publicationOnly': ['server']}, {'toolSources': {'other': self.sha}},
                {'checkConfiguration': True}, {'allowedLostProfileIDs': ['retained']},
                {'commands': {'stage': [{'argv': ['true'], 'seconds': 1}]}})):
            with self.subTest(override=override):
                self.recipe = dict(original, **override)
                self.cli('--execute', '--evidence', str(self.out / str(index)), expected=1)
                self.assertEqual(self.calls(), [])
                self.assertEqual(self.requests, [])

    def test_prejob_dynamic_lookup_inputs_stop_and_normal_gate_stays_closed(self):
        for scenario in ('dynamic-node', 'dynamic-observer', 'missing-inputs', 'normal'):
            with self.subTest(scenario=scenario):
                retained, nodes = self.prejob_rollout()
                if scenario == 'normal':
                    self.recipe['prejobRepairOnly'] = False
                else:
                    values = {'nodeAgent': {'kubernetesClient': {'discoverFromDefaultEndpoint': False}},
                              'profiler': {'workloadObserver': {'kubernetesClient': {'discoverFromDefaultEndpoint': False}}}}
                    if scenario == 'missing-inputs':
                        values = {}
                    elif scenario == 'dynamic-node':
                        values['nodeAgent']['kubernetesClient']['discoverFromDefaultEndpoint'] = True
                    else:
                        values['profiler']['workloadObserver']['kubernetesClient']['discoverFromDefaultEndpoint'] = True
                    fixture = json.loads((self.directory / 'state.json').read_text())
                    fixture['dashboardValues'] = values
                    self.state(**fixture)
                evidence = self.out / scenario
                self.cli('--execute', '--evidence', str(evidence), expected=1)
                self.assert_prejob_preserved(retained, nodes, evidence)
                (self.directory / 'calls.jsonl').write_text('')

    def test_prejob_embedded_yaml_preserves_scalar_and_alias_boundaries(self):
        import main as runner_module
        import yaml
        server = {'apiVersion': 'apps/v1', 'kind': 'Deployment',
                  'metadata': {'name': 'chill-serving-entrypoint', 'namespace': 'review'},
                  'spec': {'template': {'spec': {'containers': [
                      {'name': 'serving-entrypoint', 'image': 'server:retained'}]}}}}
        query = copy.deepcopy(server)
        query['metadata']['name'] = 'chill-query'
        query['spec']['template']['spec']['containers'][0] = {'name': 'query', 'image': 'query:retained'}
        config = {'apiVersion': 'v1', 'kind': 'ConfigMap',
                  'metadata': {'name': 'chill-profiler-config', 'namespace': 'review'}, 'data': {}}
        def manifest(raw):
            config['data']['config.yaml'] = raw
            return yaml.safe_dump_all([server, query, config]).encode()
        original = 'image: profiler:retained\nport: 8081\nfraction: 0.8\nwindow: 60\n'
        live = manifest(original)
        self.assertTrue(runner_module.prejob_only(live, manifest(original.replace('profiler:retained', 'profiler:changed')), 'review', 'chill'))
        for raw in (
                original.replace('8081', '2:14:41'),
                original.replace('0.8', '0:0.8'),
                original.replace('60', '60.0'),
                original.replace('60', '060'),
                original + 'window: 60\n',
                original + 'true: hidden\n1: collision\n',
                original + '<<: {window: 60}\n',
                original.replace('60', 'false')):
            with self.subTest(raw=raw):
                self.assertFalse(runner_module.prejob_only(live, manifest(raw), 'review', 'chill'))
        aliased = 'image: &image profiler:retained\nobserverImage: *image\n'
        self.assertFalse(runner_module.prejob_only(
            manifest(aliased), manifest(aliased.replace('profiler:retained', 'profiler:changed')), 'review', 'chill'))

    def dashboard_rollout(self, **changes):
        self.recipe.update(images=['dashboard'], dashboardCheck=True)
        retained = {'apiVersion': 'chill.example.test/v1', 'kind': 'BatchEnergyOptimization',
                    'metadata': {'name': 'retained-campaign', 'namespace': 'another-chill-ns',
                                 'uid': 'retained-campaign-uid', 'resourceVersion': '452'},
                    'spec': {'workloadRef': {'name': 'retained-workload'}},
                    'status': {'phase': 'Running', 'completedTrials': 3}}
        self.state(dashboardFixture=True, inflightObjects=[retained], **changes)
        return retained

    def test_dashboard_only_cli_preserves_nonterminal_campaign_and_backend(self):
        import yaml
        retained = self.dashboard_rollout()
        self.cli('--execute', '--evidence', str(self.out))
        state = json.loads((self.out / 'state.json').read_text())
        self.assertTrue(state['ready'])
        self.assertTrue(state['hooksSkipped'])
        publication = json.loads((self.out / 'publication.json').read_text())
        self.assertEqual(set(publication['roles']), {'dashboard'})
        live = list(yaml.safe_load_all(next(self.out.glob('*-live-manifest.yaml')).read_text()))
        live += list(yaml.safe_load_all(next(self.out.glob('*-live-hooks.yaml')).read_text()))
        final = list(yaml.safe_load_all((self.out / 'render-final.yaml').read_text()))
        dashboard = next(item for item in final if item['metadata']['name'] == 'chill-dashboard')
        image = dashboard['spec']['template']['spec']['containers'][0]['image']
        self.assertEqual(image, publication['roles']['dashboard']['image'])
        dashboard['spec']['template']['spec']['containers'][0]['image'] = 'dashboard:retained'
        self.assertEqual(final, live)
        dry_run = list(yaml.safe_load_all((self.out / 'dashboard-deployment.yaml').read_text()))
        self.assertEqual(len(dry_run), 1)
        self.assertEqual(dry_run[0]['metadata']['name'], 'chill-dashboard')
        self.assertEqual(dry_run[0]['spec']['template']['spec']['containers'][0]['image'], image)
        for label in ('inflightPreflight', 'inflightPreUpgrade'):
            report = state[label]
            self.assertEqual(report['blockers'], [{
                'kind': 'BatchEnergyOptimization', 'namespace': 'another-chill-ns',
                'name': retained['metadata']['name'], 'reasons': ['campaign nonterminal/unfrozen']}])
            evidence = Path(report['evidenceDirectory'])
            self.assertEqual(json.loads((evidence / 'objects.json').read_text())['items'], [retained])
            self.assertEqual(json.loads((evidence / 'blockers.json').read_text()), report['blockers'])
        cluster = json.loads((self.directory / 'state.json').read_text())
        self.assertEqual(cluster['inflightObjects'], [retained])
        self.assertFalse(cluster.get('apiMutations'))
        self.assertFalse(cluster.get('probeCount'))
        self.assertFalse(cluster.get('hookExecutions'))
        self.assertFalse(cluster.get('ensuredBuckets'))
        self.assertTrue(json.loads((self.out / 'dashboard/dashboard-check.json').read_text())['passed'])

    def test_full_deployment_retains_post_upgrade_hook_execution(self):
        self.dashboard_rollout()
        self.recipe['images'] = ['server', 'dashboard']
        self.state(dashboardFixture=True, inflightObjects=[])
        self.cli('--execute', '--evidence', str(self.out))
        cluster = json.loads((self.directory / 'state.json').read_text())
        self.assertEqual(cluster['hookExecutions'], 1)
        self.assertEqual(cluster['ensuredBuckets'], ['measurements'])
        state = json.loads((self.out / 'state.json').read_text())
        self.assertTrue(state['ready'])
        self.assertFalse(state['hooksSkipped'])

    def test_dashboard_mixed_changes_keep_nonterminal_campaign_stop(self):
        for change in ('server', 'crd', 'measurement-config', 'hook'):
            with self.subTest(change=change):
                retained = self.dashboard_rollout(dashboardChange=change)
                evidence = self.out / change
                self.cli('--execute', '--evidence', str(evidence), expected=1)
                reports = list(evidence.glob('inflight-preflight-*/blockers.json'))
                self.assertEqual(len(reports), 1)
                self.assertEqual(json.loads(reports[0].read_text())[0]['name'], retained['metadata']['name'])
                self.assertFalse(any(call[0] == 'docker' or 'upgrade' in call for call in self.calls()))
                self.assertEqual(json.loads((self.directory / 'state.json').read_text())['inflightObjects'], [retained])
                (self.directory / 'calls.jsonl').write_text('')

    def test_dashboard_undecidable_or_final_mixed_render_stops(self):
        cases = (
            ('malformed-before', {'malformedDashboardRender': 'before'}, False),
            ('non-string-keys', {'nonStringBackendKeys': True}, False),
            ('duplicate', {'dashboardChange': 'duplicate'}, False),
            ('missing-dashboard', {'dashboardChange': 'missing-dashboard'}, False),
            ('malformed-final', {'malformedDashboardRender': 'final'}, True),
            ('mixed-final', {'dashboardChangeAtFinal': 'measurement-config'}, True),
            ('hook-final', {'dashboardChangeAtFinal': 'hook'}, True),
        )
        for label, changes, published in cases:
            with self.subTest(label=label):
                retained = self.dashboard_rollout(**changes)
                evidence = self.out / label
                self.cli('--execute', '--evidence', str(evidence), expected=1)
                self.assertTrue((evidence / 'failure.json').exists())
                self.assertEqual((evidence / 'publication.json').exists(), published)
                self.assertFalse(any('upgrade' in call for call in self.calls()))
                cluster = json.loads((self.directory / 'state.json').read_text())
                self.assertEqual(cluster['inflightObjects'], [retained])
                self.assertFalse(cluster.get('apiMutations'))
                self.assertFalse(cluster.get('probeCount'))
                self.assertTrue(list(evidence.glob('inflight-preflight-*/blockers.json')))
                (self.directory / 'calls.jsonl').write_text('')

    def test_dashboard_boundary_rejects_alias_and_typed_scalar_changes(self):
        import main as runner_module
        live = b"""apiVersion: apps/v1
kind: Deployment
metadata: {name: chill-dashboard, namespace: review}
spec:
  template:
    spec:
      containers:
        - &dashboard {name: dashboard, image: dashboard:retained}
      initContainers:
        - *dashboard
"""
        desired = live.replace(b'dashboard:retained', b'dashboard:changed')
        self.assertTrue(runner_module.dashboard_only(live, live, 'review', 'chill'))
        self.assertFalse(runner_module.dashboard_only(live, desired, 'review', 'chill'))
        typed_live = live.replace(b'      containers:', b'      hostNetwork: false\n      containers:')
        typed_desired = typed_live.replace(b'hostNetwork: false', b'hostNetwork: 0')
        self.assertTrue(runner_module.dashboard_only(typed_live, typed_live, 'review', 'chill'))
        self.assertFalse(runner_module.dashboard_only(typed_live, typed_desired, 'review', 'chill'))

    def test_dashboard_dynamic_endpoint_inputs_keep_nonterminal_stop(self):
        for path in (('nodeAgent', 'kubernetesClient'),
                     ('profiler', 'workloadObserver', 'kubernetesClient'), ()):
            with self.subTest(path=path):
                values = {}
                if path:
                    for client_path in (('nodeAgent', 'kubernetesClient'),
                                        ('profiler', 'workloadObserver', 'kubernetesClient')):
                        parent = values
                        for key in client_path[:-1]:
                            parent = parent.setdefault(key, {})
                        parent[client_path[-1]] = {'discoverFromDefaultEndpoint': client_path == path}
                retained = self.dashboard_rollout(dashboardValues=values)
                evidence = self.out / ('-'.join(path) or 'defaults')
                self.cli('--execute', '--evidence', str(evidence), expected=1)
                reports = evidence.glob('inflight-preflight-*/blockers.json')
                self.assertEqual([json.loads(report.read_text())[0]['name'] for report in reports],
                                 [retained['metadata']['name']])
                self.assertFalse((evidence / 'publication.json').exists())
                self.assertFalse(any('upgrade' in call for call in self.calls()))
                self.assertEqual(json.loads((self.directory / 'state.json').read_text())['inflightObjects'], [retained])
                (self.directory / 'calls.jsonl').write_text('')

    def test_dashboard_values_changes_keep_nonterminal_stop(self):
        retained = self.dashboard_rollout()
        values = self.directory / 'lookup-values.json'
        values.write_text(json.dumps({'nodeAgent': {'kubernetesClient': {
            'apiServer': '', 'discoverFromDefaultEndpoint': True}}}))
        for label, overrides in (('override', {'values': [str(values)]}),
                                 ('replace', {'values': [str(values)], 'replaceValues': True})):
            with self.subTest(label=label):
                self.recipe.update(overrides)
                evidence = self.out / label
                self.cli('--execute', '--evidence', str(evidence), expected=1)
                reports = evidence.glob('inflight-preflight-*/blockers.json')
                self.assertEqual([json.loads(report.read_text())[0]['name'] for report in reports],
                                 [retained['metadata']['name']])
                self.assertFalse((evidence / 'publication.json').exists())
                self.assertFalse(any('upgrade' in call for call in self.calls()))
                (self.directory / 'calls.jsonl').write_text('')

    def test_dashboard_custom_phase_cannot_bypass_nonterminal_stop(self):
        self.dashboard_rollout()
        marker = self.directory / 'forbidden-dashboard-stage'
        self.recipe['commands'] = {'stage': [{
            'argv': [sys.executable, '-c',
                     'from pathlib import Path; import sys; Path(sys.argv[1]).touch()', str(marker)],
            'seconds': 10}]}
        self.cli('--execute', '--evidence', str(self.out), expected=1)
        self.assertFalse(marker.exists())
        self.assertFalse((self.out / 'publication.json').exists())

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

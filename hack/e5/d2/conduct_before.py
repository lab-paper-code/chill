#!/usr/bin/env python3
"""CPO-only D2 helpers used by the E5 conductors.
No build, image publication, power patch or trace activation. Native policy uses
only the actual frozen eight's Main-bound finite nondefault subset.
"""
import copy
from concurrent.futures import ThreadPoolExecutor, as_completed
import datetime as dt
import json
import ipaddress
import math
import os
from pathlib import Path
import runpy
import selectors
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

CONFIG = json.loads(Path(os.environ['CHILL_EXPERIMENT_CONFIG']).read_text())
D2 = CONFIG['d2']
ROOT = Path(__file__).resolve().parent
OPS = Path(CONFIG['paths']['ops'])
REPOBIN = D2['repoBin']
BASE = D2['apiBase']
CA = Path(D2['caFile'])
TOKEN = Path(D2['tokenFile'])
K = D2['kubectl'] + ['-n', D2.get('namespace','chill-system')]
ENV = dict(os.environ, LOCALBIN=REPOBIN, PATH=REPOBIN+':'+os.environ.get('PATH',''))
TEMPLATE = Path(D2['jobTemplate'])
UID = D2['workloadUID']
NAME = D2['workloadName']
POLICY_ANNOTATION = 'edge.dacs.io/serving-policy'
epoch = runpy.run_path(str(ROOT/'analyze_before.py'), run_name='d2_time_rules')['epoch']
NATIVE_RULES = runpy.run_path(str(ROOT/'native_mode_request.py'),run_name='d2_native_mode_rules')

# Delegate to the exact installed CLI. One diagnostic-free metadata envelope is
# emitted before replay; neither payload bytes nor credentials are printed.
LOAD_WRAPPER = r'''
import datetime as dt,json,sys,time
from pathlib import Path
from chill_loadgen import cli
original=cli.replay

def observed(records,**kw):
    manifest=[{'recordID':rid,'fields':{key:len(value) for key,value in inputs.items()}} for rid,inputs in records]
    offered=len(cli.arrival_offsets_ns(kw['arrivals'],kw['rate'],kw['duration'],kw['seed']))
    print(json.dumps({'d2Event':'payload','records':manifest,'offeredRequests':offered,'arrivals':kw['arrivals'],'seed':kw['seed'],'rate':kw['rate'],'durationSeconds':kw['duration']}),flush=True)
    print(json.dumps({'d2Event':'ready-for-release','at':dt.datetime.now(dt.timezone.utc).isoformat()}),flush=True)
    deadline=time.monotonic()+300
    while not Path('/tmp/d2-start').exists():
        if time.monotonic()>=deadline: raise RuntimeError('CPO start barrier expired; no inference sent')
        time.sleep(.1)
    print(json.dumps({'d2Event':'replay-start','at':dt.datetime.now(dt.timezone.utc).isoformat(),'clockNote':'Immediately before existing replay call; logging/call scheduling is not an exact native timestamp.'}),flush=True)
    result=original(records,**kw)
    print(json.dumps({'d2Event':'replay-drained','at':dt.datetime.now(dt.timezone.utc).isoformat()}),flush=True)
    # Preserve the same client's cgroup until its original-window end counter
    # has been saved. The existing Job/host watchdogs still bound this wait.
    while Path('/tmp/d2-start').exists():time.sleep(.1)
    return result
cli.replay=observed
sys.exit(cli.main(sys.argv[1:]))
'''


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat()

def read_stream_lines(stream, pending):
    """Drain raw FD bytes, including every complete prefetched line before EOF."""
    try:chunk=os.read(stream.fileno(),65536)
    except BlockingIOError:return [],pending,False
    parts=(pending+chunk).split(b'\n');pending=parts.pop()
    if not chunk and pending:parts.append(pending);pending=b''
    return [line.decode('utf-8') for line in parts],pending,not chunk

def producer_barrier_deadline(event, seconds=300):
    ready=epoch(event['at'])
    if ready is None:raise RuntimeError('Producer ready timestamp is not valid timezone-qualified RFC3339')
    return ready+seconds

def assert_producer_running(pod, expected_pod):
    current=[c for c in pod.get('status',{}).get('containerStatuses',[]) if c.get('name')=='load']
    expected=[c for c in expected_pod.get('status',{}).get('containerStatuses',[]) if c.get('name')=='load']
    if pod['metadata']['uid']!=expected_pod['metadata']['uid'] or pod.get('status',{}).get('phase')!='Running' or len(current)!=1 or len(expected)!=1 or not current[0].get('state',{}).get('running') or not expected[0].get('containerID') or current[0].get('containerID')!=expected[0]['containerID']:
        raise RuntimeError('Actual bound producer Pod/container is not running')

def client_end_counter(path, pod_uid, container_id, end_utc):
    """Read a real, complete exact-client counter row; never synthesize an endpoint."""
    end=epoch(end_utc)
    if not pod_uid or not container_id or end is None:
        raise RuntimeError('Original client identity/window end required for counter closure')
    expected=container_id[len('containerd://'):] if container_id.startswith('containerd://') else container_id
    if not Path(path).exists():return None
    with Path(path).open() as rows:
        for line in rows:
            if not line.endswith('\n'):continue  # Concurrent writer's unfinished last record.
            row=json.loads(line)
            if row.get('kind')!='cgroup' or row.get('role')!='client' or row.get('podUID')!=pod_uid or row.get('containerID') not in (expected,'containerd://'+expected):continue
            at=epoch(row.get('at'))
            if at is None or at<end:continue
            counters=[row.get(key) for key in ('cpuUsageNanos','nrThrottled','throttledNanos')]
            if all(isinstance(value,(int,float)) and not isinstance(value,bool) and math.isfinite(value) and value>=0 for value in counters):
                return row
    return None


def load(path):
    return json.loads(Path(path).read_text())


def save(path, value):
    path=Path(path);path.parent.mkdir(parents=True,exist_ok=True)
    path.write_text(json.dumps(value,indent=2,allow_nan=False)+'\n')


def control_alert_decision(row, actuation_evidence=(), bound_nodes=(), *, timeboxes=None):
    """Read the existing monitor wire format; never blanket-ignore Node boots."""
    timeboxes=timeboxes or {}
    allowance=timeboxes.get('nativeFutureAllowanceSeconds',15)
    maximum=timeboxes.get('maxActuationSeconds',900)
    signals=[str(row.get('event',''))]
    reasons=row.get('reasons',[])
    signals.extend(str(x) for x in (reasons if isinstance(reasons,list) else [reasons]))
    exit137=False
    for field in ('state','lastState'):
        state=row.get(field) or {}
        if isinstance(state,str):
            signals.append(state);continue
        for kind in ('waiting','terminated'):
            detail=state.get(kind) or {}
            signals.extend(str(detail.get(k,'')) for k in ('reason','message'))
            exit137=exit137 or detail.get('exitCode')==137
    text=' '.join(signals).lower()
    compact=''.join(c for c in text if c.isalnum())
    if exit137 or 'oom' in text or 'outofmemory' in compact:
        return {'classification':'fatal-oom','stop':True}
    if 'crashloop' in compact:
        return {'classification':'fatal-crashloop','stop':True}
    if any(word in compact for word in ('queryfailure','queryfailed','queryerror')):
        return {'classification':'fatal-query-failure','stop':True}
    if any(word in compact for word in ('poddisappeared','poduidreplacement','uidreplacement')):
        return {'classification':'fatal-control-identity-change','stop':True,'meaning':'Actual existing watcher disappearance/UID-replacement signal; never ordinary state or a blanket mode exception'}
    if 'restart' not in text:
        return {'classification':'ordinary-state','stop':False}
    if row.get('component') not in ('node-agent','node-agent-actuation') or row.get('container') not in ('node-agent','node-agent-actuation','ensure-boot-ordering'):
        return {'classification':'fatal-unexpected-restart','stop':True}
    bound={n['name']:n['uid'] for n in bound_nodes}
    def stamp(value):
        return epoch(value)
    at=stamp(row.get('at'))
    for proof in actuation_evidence:
        operation=proof.get('operation',{})
        if operation.get('kind')=='power':
            node=operation.get('node',{});start=stamp(proof.get('startedAt'));end=stamp(proof.get('endedAt'))
            target=operation.get('powerMode');generation=proof.get('actual',{}).get('generation')
            valid=proof.get('status')=='complete' and not proof.get('error') and proof.get('actual',{}).get('node')==node and proof.get('actual',{}).get('powerMode')==target
        elif proof.get('kind')=='node-power-safety-observation':
            rules=NATIVE_RULES
            raw=proof['node'];meta=raw['metadata'];labels=meta.get('labels',{});annotations=meta.get('annotations',{})
            node={'name':meta['name'],'uid':meta['uid']}
            observed=stamp(proof.get('observedAtUTC'));authorized=stamp(proof.get('authorizedSinceUTC'))
            start=stamp(annotations.get(rules['REQUESTED']));target=labels.get(rules['LABEL'])
            try:
                transition=json.loads(annotations.get(rules['TRANSITION'],'{}'))
                generation=int(annotations[rules['GENERATION']])
            except (KeyError,ValueError,TypeError):
                return {'classification':'fatal-mode-transition-mismatch','stop':True,'meaning':'Native transition/generation is unreadable'}
            fresh=at is not None and observed is not None and authorized is not None and start is not None and authorized<=start<=at<=observed+allowance and 0<=observed-start<=maximum+allowance
            same=node.get('name')==row.get('node') and bound.get(node.get('name'))==node.get('uid') and target and transition.get('powerMode')==target and transition.get('requestedAt')==annotations.get(rules['REQUESTED'])
            if not fresh or not same or generation<0 or transition.get('failure') or labels.get(rules['STATE'])=='failed':
                return {'classification':'fatal-mode-transition-mismatch','stop':True,'meaning':'No fresh authorized same-Node native request/transition; an old request cannot excuse a new restart'}
            detail={'node':node,'targetMode':target,'generation':generation,'transitionGeneration':transition.get('generation'),'requestedAt':annotations[rules['REQUESTED']],'observedAtUTC':proof['observedAtUTC']}
            if labels.get(rules['STATE'])=='pending' and transition.get('applyStartedAt'):
                return dict(detail,classification='approved-node-actuation-in-progress',stop=False,requiresPostDrainCompletion=True,meaning='Actual pending same request/apply fence immediately observed. Fence generation may change at verified readback; complete native outcome remains mandatory.')
            try:ready,_=rules['settled'](raw,target,annotations[rules['REQUESTED']])
            except (RuntimeError,ValueError,KeyError,TypeError) as error:
                return dict(detail,classification='fatal-mode-transition-mismatch',stop=True,error=str(error))
            ready_at=stamp(transition.get('readyAt'))
            if labels.get(rules['STATE'])=='success' and ready_at is not None and start<=ready_at and abs(at-ready_at)<=allowance and ready_at<=observed+allowance:
                return dict(detail,classification='approved-node-actuation-boot' if ready else 'approved-node-actuation-in-progress',stop=False,requiresPostDrainCompletion=not ready,meaning='Actual generation-matched native success plus fresh restart-time correlation; Node Ready is separately recorded. Native ReadyAt is a coarse safety freshness fence, never a request-latency measurement; unusable clocks fail closed.')
            return dict(detail,classification='fatal-unconfirmed-node-restart',stop=True,meaning='Explicit fresh native apply/Ready correlation unavailable; stop now, not at end of soak')
        elif proof.get('status')=='native-ready' and proof.get('mutations')==1:
            node=proof.get('node',{});start=stamp(proof.get('requestedAt'));end=stamp(proof.get('at'))
            target=proof.get('targetMode');transition=proof.get('transition',{});generation=transition.get('generation')
            valid=transition.get('powerMode')==target and transition.get('requestedAt')==proof.get('requestedAt') and bool(transition.get('readyAt')) and isinstance(generation,int) and generation>0
        else:continue
        if node.get('name')!=row.get('node') or at is None or start is None or end is None or not start<=at<=end+allowance:
            continue
        if not valid or not target or bound.get(node.get('name'))!=node.get('uid'):
            return {'classification':'fatal-mode-transition-mismatch','stop':True}
        return {'classification':'approved-node-actuation-boot','stop':False,'node':node,'generation':generation,'targetMode':target,'startedAt':start,'endedAt':end,'meaning':'Actual completed same-Node mode proof; formal power results do not export a numeric generation. OOM/CrashLoop/query failure were checked first. Native lastState clocks are not compared.'}
    if bound.get(row.get('node')):
        return {'classification':'unconfirmed-node-actuation-candidate','stop':True,'requiresImmediateNativeCorrelation':True,'meaning':'Immediately notify CPO/Main and correlate existing Kubernetes native request/transition/Ready. Autonomous+bound Node alone never allows continuation.'}
    return {'classification':'fatal-unexpected-restart','stop':True,'meaning':'No actual matching same-Node mode proof; no global Jetson exemption.'}

def self_check():
    """Check nondefault native freshness bounds using a pending native request."""
    rules=NATIVE_RULES
    start='2026-10-30T00:00:00+00:00'
    node={'metadata':{'name':'node','uid':'uid','labels':{rules['LABEL']:'mode',rules['STATE']:'pending'},
                     'annotations':{rules['REQUESTED']:start,rules['GENERATION']:'1',
                                    rules['TRANSITION']:json.dumps(dict(powerMode='mode',requestedAt=start,applyStartedAt=start))}}}
    proof=dict(kind='node-power-safety-observation',node=node,authorizedSinceUTC=start,
               observedAtUTC='2026-10-30T00:00:40+00:00')
    row=dict(event='restart',component='node-agent',container='node-agent',node='node',at='2026-10-30T00:00:42+00:00')
    bound=[dict(name='node',uid='uid')]
    assert not control_alert_decision(row,[proof],bound,timeboxes=dict(maxActuationSeconds=38,nativeFutureAllowanceSeconds=2))['stop']
    assert control_alert_decision(row,[proof],bound,timeboxes=dict(maxActuationSeconds=37,nativeFutureAllowanceSeconds=2))['stop']
    assert control_alert_decision(dict(row,at='2026-10-30T00:00:43+00:00'),[proof],bound,timeboxes=dict(maxActuationSeconds=38,nativeFutureAllowanceSeconds=2))['stop']


def resolve_control_alert(row, actuation_evidence, bound_nodes, *, authorized_since=None, node_reader=None, report_candidate=None, timeboxes=None):
    decision=control_alert_decision(row,actuation_evidence,bound_nodes,timeboxes=timeboxes)
    if decision['classification']!='unconfirmed-node-actuation-candidate':return decision
    if report_candidate is not None:report_candidate(row,decision)
    else:print('CONTROL_RESTART_IMMEDIATE_CPO_MAIN '+json.dumps(dict(row=row,decision=decision)),file=sys.stderr,flush=True)
    if not authorized_since or node_reader is None:return decision
    try:node=node_reader(row['node'])
    except Exception as error:return dict(classification='fatal-native-safety-query-failure',stop=True,error=str(error))
    observation=dict(kind='node-power-safety-observation',node=node,authorizedSinceUTC=authorized_since,observedAtUTC=now())
    result=control_alert_decision(row,[*actuation_evidence,observation],bound_nodes,timeboxes=timeboxes)
    result['nodeSafetyObservation']=observation
    return result


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, newurl):
        return None


class Conductor:
    def record(self, value):
        with self.journal_lock:
            with self.journal.open('a') as f: f.write(json.dumps(dict(at=now(),phase=self.a.phase,sourceSha=self.source,**value))+'\n')

    def api(self, method, path, body=None, timeout=129):
        token = getattr(self, 'collector_token_file', TOKEN) if method == 'GET' else TOKEN
        headers={'Authorization':'Bearer '+Path(token).read_text().strip()}
        if body is not None: headers['Content-Type']='application/json'
        request=urllib.request.Request(BASE+path,data=json.dumps(body).encode() if body is not None else None,headers=headers,method=method)
        begun=now()
        try:
            with self.http.open(request,timeout=timeout) as response:
                raw=response.read();status=response.status
        except urllib.error.HTTPError as e:
            raw=e.read();status=e.code
        self.record(dict(kind='api',method=method,path=path,startUTC=begun,status=status,bytes=len(raw)))
        value=json.loads(raw)
        if status!=200:
            save(self.phase_out/('api-failure-'+self.a.command+'.json'),dict(method=method,path=path,status=status,response=value))
            raise RuntimeError('API '+method+' '+path+' status '+str(status))
        return value

    def k(self, argv, stdin=None, timeout=129):
        command=D2['kubectl']+['-n',getattr(self,'config',{}).get('namespace',D2.get('namespace','chill-system'))]+argv
        begun=now();r=subprocess.run(command,input=stdin,text=True,capture_output=True,timeout=timeout,env=ENV)
        self.record(dict(kind='kubectl',argv=command,startUTC=begun,exitCode=r.returncode))
        if r.returncode:
            raise RuntimeError('kubectl failed: '+r.stderr[:600])
        return json.loads(r.stdout) if r.stdout.strip() else None

    def state(self):
        return self.api('GET','/api/v1/scheduler/state')

    def settings(self):
        return self.api('GET','/api/v1/scheduler/settings')

    def put(self, settings):
        body=copy.deepcopy(settings);body.pop('staticRouting',None);body.pop('staticRoutingValidatedAt',None)
        save(self.out/(self.a.command+'-settings-request.json'),body)
        response=self.api('PUT','/api/v1/scheduler/settings',body)
        with (self.out/'settings-write-receipts.jsonl').open('a') as receipts:
            receipts.write(json.dumps(dict(at=now(),phase=self.a.phase,command=self.a.command,sourceSha=self.source,request=body,response=response))+'\n')
        return response

    def operation(self, body, prefix):
        save(self.out/(prefix+'-request.json'),body)
        result=self.api('POST','/api/v1/scheduler/operations',body,timeout=1800)
        save(self.out/(prefix+'-result.json'),result)
        if result.get('status')!='complete' or result.get('error'):
            raise RuntimeError('Scheduler '+body['kind']+' failed; retain step/ACK evidence and stop')
        return result

    def routing_publication_ready(self, state, wanted, previous, *, workload_uid=None, allowed_owner_uids=None):
        uid=UID if workload_uid is None else workload_uid
        allowed={uid} if allowed_owner_uids is None else set(allowed_owner_uids)
        actual=state['snapshot'].get('routing',{}).get(uid,{})
        if actual!=wanted and actual!=previous:
            raise RuntimeError('Actual routing conflicts with both the exact requested and original cached routes')
        instances=state['snapshot'].get('instances')
        if instances is None:instances=[]
        owned=[i for i in instances if i.get('ownerWorkloadUID')==uid]
        if any(i.get('workloadUID') not in ('',None,uid) for i in owned):
            raise RuntimeError('Actual served UID conflicts with owned workload')
        if actual!=wanted:return False
        if not wanted:
            if any(i.get('ownerWorkloadUID') not in allowed for i in instances):
                raise RuntimeError('Foreign physical ownership during empty-route publication')
            return all(i.get('workloadUID') in ('',None) for i in owned)
        for instance_id in wanted:
            found=[i for i in instances if i['id']==instance_id]
            if len(found)!=1 or found[0].get('ownerWorkloadUID')!=uid:
                raise RuntimeError('Exact routed physical ownership is missing/conflicting')
            if found[0].get('workloadUID')!=uid:return False
        return True

    def publication_end(self, completion, seconds=194):
        stamp=completion.get('appliedAt') or completion.get('endedAt')
        if not stamp:raise RuntimeError('Original operation completion timestamp missing; never restart publication timeout')
        completed=epoch(stamp)
        if completed is None:raise RuntimeError('Original operation completion timestamp is not valid timezone-qualified RFC3339')
        deadline=completed+seconds
        return time.monotonic()+max(0,deadline-time.time()),deadline

    def wait_routing_publication(self, wanted, previous, completion, prefix, empty_instances=False):
        end,deadline=self.publication_end(completion)
        while True:
            state=self.state();save(self.out/(prefix+'-last-state.json'),state)
            ready=self.routing_publication_ready(state,wanted,previous)
            if ready and (not empty_instances or not state['snapshot'].get('instances')) and self.idle(state):
                save(self.out/(prefix+'-publication-proof.json'),dict(at=now(),originalCompletion=completion,deadlineUTC=dt.datetime.fromtimestamp(deadline,dt.timezone.utc).isoformat(),currentAlreadyReadyAfterDeadline=time.time()>deadline,snapshotAt=state['snapshot']['at'],routing=state['snapshot'].get('routing',{}).get(UID,{})))
                return state
            remaining=end-time.monotonic()
            if remaining<=0:raise RuntimeError('Original194s route/physical publication budget exhausted; no timeout reset or repeated PUT')
            time.sleep(min(5,remaining))

    def boundary(self, folder, prefix):
        save(folder/(prefix+'-state.json'),self.state())
        save(folder/(prefix+'-pools.json'),self.api('GET','/api/v1/pools'))

    def idle(self, state):
        if state['snapshot'].get('outstanding'): return False
        if state['snapshot'].get('reservations'): return False
        if any(a.get('status') in ('running','cancel') for a in state.get('actions',[])): return False
        view=self.api('GET','/api/v1/workloads/'+NAME)
        if view.get('pending')!=0 or view.get('assigned')!=0: return False
        return True

    def wait_idle(self, seconds=194):
        end=time.monotonic()+seconds
        while time.monotonic()<end:
            s=self.state()
            if self.idle(s): return s
            time.sleep(5)
        raise RuntimeError('Physical/workload drain or action terminal bound exceeded; stop lane')

    def assert_initial_empty(self, state):
        if state['snapshot'].get('instances'):
            raise RuntimeError('Expected original empty serving state before independent preparation')
        if not self.idle(state): raise RuntimeError('Pending/outstanding/actions are not zero')
        settings=self.settings()
        if settings.get('autonomousWorkloads'): raise RuntimeError('Another autonomous Workload remains enabled')

    def capture(self):
        folder=self.phase_out/self.a.command
        if folder.exists(): raise RuntimeError('Capture already exists for this phase/command; never replace BEFORE evidence')
        folder.mkdir(parents=True)
        for suffix,path in [('scheduler','/api/v1/scheduler'),('settings','/api/v1/scheduler/settings'),('profiles','/api/v1/scheduler/profiles'),('capacity','/api/v1/scheduler/capacity'),('pools','/api/v1/pools')]:
            save(folder/(suffix+'.json'),self.api('GET',path))
        save(folder/'nodes.json',self.k(['get','nodes','-o','json']))
        save(folder/'pods.json',self.k(['get','pods','-A','-o','json']))
        save(folder/'services.json',self.k(['get','services','-o','json']))
        # Public certificate only; never persist the token or Secret contents.
        (folder/'gateway-ca.crt').write_bytes(CA.read_bytes())
        save(folder/'capture.json',dict(at=now(),sourceSha=self.source,binding=str(self.a.binding),release=str(self.a.release),phase=self.a.phase,phaseRoot=str(self.phase_out),physicalLifecycle=str(self.out/'lifecycle.json'),physicalBinding=str(self.out/'physical-binding.json'),files={name:str(folder/(name+'.json')) for name in ('scheduler','settings','profiles','capacity','pools','nodes','pods','services')}))

    def defaults_match(self, state, life):
        instances=state['snapshot'].get('instances',[])
        if len(life['instances'])!=8 or len({i['id'] for i in life['instances']})!=8 or len(instances)!=8 or state['snapshot'].get('routing',{}).get(UID):
            return False
        for choice in self.b['selected']:
            owned=[i for i in life['instances'] if i['node']==choice['node']]
            if len(owned)!=1:return False
            found=[i for i in instances if i['id']==owned[0]['id'] and i['node']==choice['node'] and i.get('profile',{}).get('id')==choice['profileID'] and i.get('ownerWorkloadUID')==UID and i.get('workloadUID') in ('',None)]
            if len(found)!=1 or not found[0].get('ready') or found[0].get('maxBatch')!=choice['manualPrepareMaxBatch'] or found[0].get('maxWaitNanos')!=choice['maxWaitNanos'] or found[0].get('allocation')!=choice['allocation']:
                return False
        return True

    def prepare(self):
        if (self.out/'lifecycle.json').exists(): raise RuntimeError('Preparation already recorded; do not repeat or resume implicitly')
        state=self.state();self.assert_initial_empty(state)
        life=dict(startUTC=now(),originalSettings=self.settings(),originalNodes=state['snapshot']['nodes'],instances=[],stage='PREPARING')
        life['preparationResults']=[]
        save(self.out/'lifecycle.json',life)
        prepare_ack=self.operation(dict(kind='unroute',workloadUID=UID,reason='Approved unloaded native setup before exact-cap binding'),'prepare-empty-route')
        self.wait_routing_publication({},state['snapshot'].get('routing',{}).get(UID,{}),prepare_ack,'prepare-empty-route')
        self.prepare_public_batch(state,life)

    def continue_unstarted_prepare(self):
        """Adopt this completed empty declaration, then execute only its never-started batch."""
        life=load(self.out/'lifecycle.json')
        if life.get('stage')!='PREPARING' or life.get('instances') or life.get('preparationResults') or life.get('batchStartedUTC'):
            raise RuntimeError('Only exact PREPARING with native batch never started can continue')
        if any((self.out/('prepare-'+choice['node']['name']+'-'+suffix+'.json')).exists() for choice in self.b['selected'] for suffix in ('request','result','error')):
            raise RuntimeError('Native RPC/request evidence already exists; do not repeat any preparation')
        ack=load(self.out/'prepare-empty-route-result.json')
        if ack.get('status')!='complete' or ack.get('error') or ack.get('operation',{}).get('kind')!='unroute':
            raise RuntimeError('Original completed unroute ACK required before first preparation batch')
        if (self.settings().get('staticRouting') or {}).get(UID)!= {}:
            raise RuntimeError('Original empty route declaration changed')
        state=self.wait_routing_publication({},(ack.get('before',{}).get('routing') or {}).get(UID,{}),ack,'continue-original-empty-route')
        self.assert_initial_empty(state)
        save(self.out/'original-empty-declaration-adopted-before-first-batch.json',dict(at=now(),originalACK=ack,snapshotAt=state['snapshot']['at'],nativeRPCPreviouslyStarted=False,newSettingsPUTs=0))
        self.prepare_public_batch(state,life)

    def prepare_public_batch(self, state, life):
        if len({c['node']['uid'] for c in self.b['selected']})!=8:
            raise RuntimeError('Parallel public prepare requires one exact distinct frozen Node per request')
        for choice in self.b['selected']:
            node=next(n for n in state['snapshot']['nodes'] if n['node']==choice['node'])
            if node['powerMode']!=choice['powerMode']:
                raise RuntimeError('Native mode preparation path not bound; no manual power fallback')
        life['batchStartedUTC']=now();save(self.out/'lifecycle.json',life)
        def prepare_one(choice):
            body=dict(kind='prepare',workloadUID=UID,profileID=choice['profileID'],node=choice['node'],reason='Approved D2 frozen native preparation')
            save(self.out/('prepare-'+choice['node']['name']+'-request.json'),body)
            result=self.api('POST','/api/v1/scheduler/operations',body,timeout=1800)
            save(self.out/('prepare-'+choice['node']['name']+'-result.json'),result)
            return result
        failures=[]
        # ExecuteManual releases tickMu before waiting on its existing per-Node
        # asynchronous run. Reuse only that public path, no batch API/bypass.
        with ThreadPoolExecutor(max_workers=8) as workers:
            pending={workers.submit(prepare_one,c):c for c in self.b['selected']}
            for completed in as_completed(pending):
                choice=pending[completed];name=choice['node']['name']
                try:
                    result=completed.result()
                    life['preparationResults'].append(dict(node=choice['node'],path=str(self.out/('prepare-'+name+'-result.json')),status=result.get('status'),error=result.get('error')))
                    native=result.get('actual',{}).get('native',{})
                    if len(native)==1:
                        actual_id,actual_state=next(iter(native.items()))
                        metadata=actual_state.get('metadata',{});policy=metadata.get('policy',{})
                        allocation=result.get('operation',{}).get('prepare',{}).get('instance',{}).get('allocation')
                        life['instances'].append(dict(id=actual_id,node=choice['node'],profileID=metadata.get('profileID'),maxBatch=policy.get('maxBatchSize'),desiredMaxBatch=choice['maxBatch'],maxWaitNanos=choice['maxWaitNanos'],allocation=allocation,weight=max(1,round(choice['kappa']*1000000))))
                    if result.get('status')!='complete' or result.get('error') or len(native)!=1 or not actual_state.get('ready') or metadata.get('profileID')!=choice['profileID'] or policy.get('maxBatchSize')!=choice['manualPrepareMaxBatch'] or policy.get('maxBatchWaitMS',0)!=choice['maxWaitNanos']/1000000 or allocation!=choice['allocation']:
                        failures.append(name+': public prepare/actual native proof incomplete')
                except Exception as e:
                    failures.append(name+': '+str(e))
                    save(self.out/('prepare-'+name+'-error.json'),dict(at=now(),node=choice['node'],profileID=choice['profileID'],error=str(e)))
                save(self.out/'lifecycle.json',life)
        if failures:
            save(self.out/'prepare-failures.json',failures)
            raise RuntimeError('Parallel public preparation incomplete; preserve exact per-Node results for owned cleanup, producer0')
        end=time.monotonic()+194
        while time.monotonic()<end:
            fresh=self.state();save(self.out/'prepared-defaults-last-state.json',fresh)
            if self.defaults_match(fresh,life) and self.idle(fresh): break
            time.sleep(2)
        else: raise RuntimeError('Actual public prepare8 did not converge in a successful cached snapshot within194s')
        self.accept_prepared_defaults()

    def accept_prepared_defaults(self):
        """Explicit read-only continuation of the same saved8; never prepares/routes/patches."""
        life=load(self.out/'lifecycle.json')
        if life.get('stage')!='PREPARING' or len(life.get('preparationResults',[]))!=8:
            raise RuntimeError('Existing same-run8 preparation ledger required; no recreate/retry')
        for choice in self.b['selected']:
            records=[r for r in life['preparationResults'] if r['node']==choice['node']]
            owned=[i for i in life['instances'] if i['node']==choice['node']]
            if len(records)!=1 or len(owned)!=1:raise RuntimeError('Existing preparation identities incomplete/ambiguous')
            result=load(records[0]['path']);native=result.get('actual',{}).get('native',{})
            if result.get('status')!='complete' or result.get('error') or list(native)!=[owned[0]['id']]:
                raise RuntimeError('Original flat public OperationResult not complete/exact-owned')
            actual=native[owned[0]['id']];metadata=actual.get('metadata',{});policy=metadata.get('policy',{})
            allocation=result.get('operation',{}).get('prepare',{}).get('instance',{}).get('allocation')
            if not actual.get('ready') or metadata.get('profileID')!=choice['profileID'] or policy.get('maxBatchSize')!=choice['manualPrepareMaxBatch'] or policy.get('maxBatchWaitMS',0)!=choice['maxWaitNanos']/1000000 or allocation!=choice['allocation']:
                raise RuntimeError('Original temporary-default ACK/profile/cap/wait/allocation mismatch')
        fresh=self.assert_policy_idle();save(self.out/'prepared-defaults-accepted-existing-state.json',fresh)
        if not self.defaults_match(fresh,life):
            raise RuntimeError('Existing8 no longer match exact owned/unrouted temporary default state')
        order={c['node']['uid']:n for n,c in enumerate(self.b['selected'])}
        life['instances'].sort(key=lambda i:order[i['node']['uid']])
        life['stage']='DEFAULTS_ONLY_NOT_LOAD_READY';save(self.out/'lifecycle.json',life)
        save(self.out/'prepared-defaults-binding.json',dict(at=now(),instances=life['instances'],loadReady=False,continuation='Existing same-run8 read-only accepted; no prepare/route/policy mutation'))
        save(self.out/'prepared-defaults-pods.json',self.k(['get','pods','-o','json']))
        save(self.out/'prepared-defaults-services.json',self.k(['get','services','-o','json']))

    def assert_policy_idle(self):
        state=self.wait_idle()
        if self.settings().get('autonomousWorkloads'): raise RuntimeError('Native policy setup requires autonomy off')
        jobs=self.k(['get','jobs','-o','json'])['items']
        if any(not j.get('spec',{}).get('suspend') and not any(c.get('type') in ('Complete','Failed') and c.get('status')=='True' for c in j.get('status',{}).get('conditions',[])) for j in jobs):
            raise RuntimeError('Nonterminal producer/measurement Job exists; no policy mutation')
        return state

    def policy_choices(self):
        choices=[c for c in self.b['selected'] if c['maxBatch']!=c['manualPrepareMaxBatch']]
        if not 0<len(choices)<=8 or len({c['node']['uid'] for c in choices})!=len(choices):
            raise RuntimeError('Policy changes must be the finite distinct-Node nondefault subset of the actual frozen eight binding')
        if any(c['maxWaitNanos']!=0 for c in choices):
            raise RuntimeError('Actual binding changed the frozen wait0 condition; no policy substitution')
        return choices

    def same_policy_pod(self, pod, frame):
        statuses=[s for s in pod.get('status',{}).get('containerStatuses',[]) if s.get('name')=='firstfloor']
        if pod['metadata']['uid']!=frame['podUID'] or pod['metadata'].get('deletionTimestamp') or len(statuses)!=1:
            raise RuntimeError('Policy target Pod identity changed')
        status=statuses[0]
        if status.get('containerID')!=frame['containerID'] or not status.get('ready') or not status.get('state',{}).get('running') or not any(c.get('type')=='Ready' and c.get('status')=='True' for c in pod.get('status',{}).get('conditions',[])):
            raise RuntimeError('Policy target running physical executor changed/unready')
        spec=pod['spec'];containers=spec.get('containers',[])
        if pod['metadata'].get('labels',{}).get('edge.dacs.io/serving-pod-uid')!=frame['podUID'] or spec.get('restartPolicy')!='Never' or spec.get('hostNetwork',False) or len(containers)!=1 or containers[0].get('name')!='firstfloor' or spec.get('ephemeralContainers') or spec.get('nodeName')!=frame['node']['name']:
            raise RuntimeError('Native transport Pod differs from original isolated physical binding')
        if not any(p.get('name')=='http' and p.get('containerPort')==8080 and p.get('hostPort',0)==0 and p.get('protocol','') in ('','TCP') for p in containers[0].get('ports',[])):
            raise RuntimeError('Native transport Pod lacks original named http8080 port')

    def policy_transport(self, pod, frame):
        self.same_policy_pod(pod,frame)
        service=self.k(['get','service','chill-executor-'+frame['podUID'],'-o','json'])
        meta=service['metadata'];owners=meta.get('ownerReferences',[]);spec=service['spec'];ports=spec.get('ports',[]);labels=meta.get('labels',{})
        if meta.get('deletionTimestamp') or meta.get('namespace')!=pod['metadata'].get('namespace') or meta.get('name')!='chill-executor-'+frame['podUID'] or len(owners)!=1 or owners[0].get('apiVersion')!='v1' or owners[0].get('kind')!='Pod' or owners[0].get('uid')!=frame['podUID'] or owners[0].get('name')!=frame['podName'] or owners[0].get('controller') is not True:
            raise RuntimeError('Native read Service is not owned by the exact physical Pod')
        if labels.get('edge.dacs.io/serving-pod-uid')!=frame['podUID'] or 'service.edgemesh.kubeedge.io/service-proxy-name' not in labels or labels['service.edgemesh.kubeedge.io/service-proxy-name']!='' or spec.get('selector')!={'edge.dacs.io/serving-pod-uid':frame['podUID']} or spec.get('type')!='ClusterIP' or len(ports)!=1 or spec.get('publishNotReadyAddresses',False) or spec.get('externalIPs') or spec.get('externalName','')!='' or spec.get('sessionAffinity','') not in ('','None') or spec.get('internalTrafficPolicy') not in (None,'Cluster'):
            raise RuntimeError('Native Service differs from original single-Pod/edgemesh transport')
        port=ports[0]
        if port.get('name')!='http' or port.get('protocol')!='TCP' or port.get('port')!=8080 or port.get('targetPort')!='http' or port.get('nodePort',0)!=0 or port.get('appProtocol') is not None:
            raise RuntimeError('Native Service named http port differs from original transport')
        selected=self.k(['get','pods','-l','edge.dacs.io/serving-pod-uid='+frame['podUID'],'-o','json'])['items']
        if len(selected)!=1 or selected[0]['metadata']['name']!=frame['podName'] or selected[0]['spec'].get('nodeName')!=pod['spec'].get('nodeName'):
            raise RuntimeError('Native Service selector does not select only the current executor')
        self.same_policy_pod(selected[0],frame)
        ip=str(ipaddress.ip_address(spec['clusterIP']));host='['+ip+']' if ':' in ip else ip
        return service,'http://'+host+':8080'

    def native_read(self, endpoint):
        # Exact source transport is HTTP ClusterIP:8080. External host access is
        # NOT presumed: capture-policy must actually succeed before any write.
        opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
        with opener.open(endpoint+'/v2/models/chill_model/state',timeout=10) as response:
            raw=response.read((64<<20)+1)
        if len(raw)>(64<<20): raise RuntimeError('Native state exceeds bounded response; no mutation')
        return json.loads(raw)

    def policy_frame(self, choice, life):
        owned=[i for i in life['instances'] if i['node']==choice['node'] and i['profileID']==choice['profileID']]
        if len(owned)!=1: raise RuntimeError('Exact prepared policy instance is absent/ambiguous')
        pods=self.k(['get','pods','-o','json'])['items']
        matches=[p for p in pods if any(s.get('name')=='firstfloor' and p['metadata']['uid']+'/'+s.get('containerID','')==owned[0]['id'] for s in p.get('status',{}).get('containerStatuses',[]))]
        if len(matches)!=1: raise RuntimeError('Exact owned physical policy Pod not found')
        pod=matches[0];status=next(s for s in pod['status']['containerStatuses'] if s['name']=='firstfloor')
        frame=dict(node=choice['node'],profileID=choice['profileID'],instanceID=owned[0]['id'],podName=pod['metadata']['name'],podUID=pod['metadata']['uid'],containerID=status['containerID'])
        self.same_policy_pod(pod,frame)
        if pod['spec'].get('nodeName')!=choice['node']['name']: raise RuntimeError('Policy Pod left bound Node')
        service,frame['nativeEndpoint']=self.policy_transport(pod,frame)
        frame['serviceUID']=service['metadata']['uid']
        cm=self.k(['get','configmap',frame['podName'],'-o','json'])
        config=json.loads(cm['data']['config.json']);native=self.native_read(frame['nativeEndpoint'])
        metadata=native['metadata'];initialized=choice['runtimeMetadata']['initializedBatches']
        if not native.get('ready') or metadata.get('model')!='chill_model' or metadata.get('profileID')!=choice['profileID'] or config.get('model')!='chill_model' or choice['profileCondition'].get('logicalModel')!='mobilenet-v2-050' or config['policy'].get('profileID')!=choice['profileID'] or metadata['runtime'].get('initializedBatches')!=initialized or metadata.get('maxBatchSize')!=max(initialized) or config.get('maxBatchSize')!=max(initialized) or choice['maxBatch'] not in initialized:
            raise RuntimeError('Native initialized/profile identity differs from measured fixed condition')
        if choice['maxBatch'] not in choice['measuredBatches'] or choice['capacityRow']['input']['maxBatch']!=choice['maxBatch'] or choice['capacityRow']['status']!='Built':
            raise RuntimeError('Selected cap has no exact measured/Built capacity provenance')
        if pod['metadata'].get('annotations',{}).get(POLICY_ANNOTATION)!=metadata.get('decisionID'):
            raise RuntimeError('Native current DecisionID and desired Pod annotation differ')
        frame.update(configMapName=cm['metadata']['name'],configMapUID=cm['metadata']['uid'],configMapResourceVersion=cm['metadata']['resourceVersion'],pod=pod,service=service,configMap=cm,config=config,native=native,desiredMaxBatch=choice['maxBatch'],maxWaitNanos=choice['maxWaitNanos'])
        return frame

    def policy_matches(self, state, policy):
        metadata=state['metadata']
        def normalized(p):
            return {k:p.get(k,d) or d for k,d in [('decisionID',''),('profileID',''),('maxBatchWaitMS',0),('maxBatchSize',0),('deadlinePolicy','keep'),('fixedBatchSize',0)]}
        return state.get('ready') and metadata.get('profileID')==policy['profileID'] and metadata.get('decisionID')==policy['decisionID'] and normalized(metadata['policy'])==normalized(policy)

    def capture_policy(self):
        self.assert_policy_idle();life=load(self.out/'lifecycle.json')
        frames=[self.policy_frame(c,life) for c in self.b['selected']]
        for f in frames:
            if not self.policy_matches(f['native'],f['config']['policy']): raise RuntimeError('Preflight native/config full policy mismatch')
        save(self.out/'policy-preflight.json',dict(at=now(),readOnly=True,frames=frames,transport='Exact existing Service ClusterIP HTTP; success here is the actual external read witness, not inferred from source'))

    def native_policy(self):
        if (self.out/'native-policy-attempt.json').exists(): raise RuntimeError('Policy attempt already recorded; no implicit retry/resume')
        self.assert_policy_idle();life=load(self.out/'lifecycle.json')
        if life.get('stage')!='DEFAULTS_ONLY_NOT_LOAD_READY': raise RuntimeError('Policy realization requires exact prepared-defaults stage')
        if not (self.out/'policy-preflight.json').exists(): raise RuntimeError('Actual read-only native transport/preimage capture required before Main policy release')
        choices=self.policy_choices();targets=self.release.get('targets',[])
        if len(targets)!=len(choices): raise RuntimeError('Main exact-instance release must equal the actual bound nondefault subset')
        frames=[self.policy_frame(c,life) for c in choices]
        for choice,frame in zip(choices,frames):
            matches=[t for t in targets if t.get('instanceID')==frame['instanceID']]
            if len(matches)!=1: raise RuntimeError('Main policy target missing/ambiguous')
            target=matches[0]
            for key in ('node','profileID','podUID','containerID','configMapUID','configMapResourceVersion','serviceUID','nativeEndpoint','desiredMaxBatch','maxWaitNanos'):
                if target.get(key)!=frame[key]: raise RuntimeError('Main exact policy target changed: '+key)
            policy=frame['config']['policy'];did=target.get('decisionID')
            if not isinstance(did,str) or not 0<len(did.encode())<=256 or not did.isascii() or any(not (x.isalnum() or x in '-_.') for x in did) or did==policy.get('decisionID'):
                raise RuntimeError('Main must bind a fresh bounded policy DecisionID')
            if target.get('beforeDecisionID')!=policy.get('decisionID') or not self.policy_matches(frame['native'],policy) or policy.get('maxBatchSize')!=choice['manualPrepareMaxBatch'] or policy.get('maxBatchWaitMS',0)!=0 or policy.get('fixedBatchSize',0)!=0:
                raise RuntimeError('Prepared original dynamic policy differs from exact Main preimage')
            frame['desiredPolicy']=copy.deepcopy(policy);frame['desiredPolicy'].update(maxBatchSize=choice['maxBatch'],decisionID=did)
        save(self.out/'native-policy-attempt.json',dict(at=now(),frames=frames,instruction='No producer. On partial failure retain exact preimages/responses; Main releases existing exact-owned cleanup/restoration, no automatic retry/rollback.'))
        for choice,frame in zip(choices,frames):
            self.assert_policy_idle();name=choice['node']['name'];folder=self.out/('native-policy-'+name);folder.mkdir(exist_ok=True)
            current=self.k(['get','configmap',frame['configMapName'],'-o','json'])
            if current['metadata']['uid']!=frame['configMapUID'] or current['metadata']['resourceVersion']!=frame['configMapResourceVersion'] or current['data']['config.json']!=frame['configMap']['data']['config.json']:
                raise RuntimeError('ConfigMap changed after Main/preflight binding; no write/retry')
            config=copy.deepcopy(frame['config']);config['policy']=frame['desiredPolicy']
            patch=[dict(op='test',path='/metadata/uid',value=frame['configMapUID']),dict(op='test',path='/metadata/resourceVersion',value=frame['configMapResourceVersion']),dict(op='replace',path='/data/config.json',value=json.dumps(config,separators=(',',':')))]
            save(folder/'configmap-patch.json',patch)
            save(folder/'configmap-response.json',self.k(['patch','configmap',frame['configMapName'],'--type=json','--patch-file',str(folder/'configmap-patch.json'),'-o','json']))
            pod=self.k(['get','pod',frame['podName'],'-o','json']);self.same_policy_pod(pod,frame)
            if pod['metadata'].get('annotations',{}).get(POLICY_ANNOTATION)!=frame['config']['policy']['decisionID']:
                raise RuntimeError('Pod policy annotation changed; retain partial ConfigMap write and stop')
            patch=[dict(op='test',path='/metadata/uid',value=frame['podUID']),dict(op='test',path='/metadata/resourceVersion',value=pod['metadata']['resourceVersion']),dict(op='add',path='/metadata/annotations/edge.dacs.io~1serving-policy',value=frame['desiredPolicy']['decisionID'])]
            save(folder/'pod-patch.json',patch)
            save(folder/'pod-response.json',self.k(['patch','pod',frame['podName'],'--type=json','--patch-file',str(folder/'pod-patch.json'),'-o','json']))
            end=time.monotonic()+194;ack=False;ack_snapshot_at=None
            while time.monotonic()<end:
                pod=self.k(['get','pod',frame['podName'],'-o','json']);self.same_policy_pod(pod,frame)
                service,endpoint=self.policy_transport(pod,frame)
                if service['metadata']['uid']!=frame['serviceUID'] or endpoint!=frame['nativeEndpoint']:
                    raise RuntimeError('Bound native Service identity/endpoint changed during ACK')
                native=self.native_read(endpoint);save(folder/'last-native-state.json',native)
                if self.policy_matches(native,frame['desiredPolicy']):
                    ack=True;state=self.state();save(folder/'last-scheduler-state.json',state)
                    if ack_snapshot_at is None:
                        ack_snapshot_at=state['snapshot']['at']
                        save(folder/'native-ack-snapshot-baseline.json',dict(at=now(),native=native,snapshotAt=ack_snapshot_at))
                        time.sleep(2);continue
                    instances=[i for i in state['snapshot'].get('instances',[]) if i.get('id')==frame['instanceID']]
                    if state['snapshot']['at']!=ack_snapshot_at and len(instances)==1 and instances[0].get('maxBatch')==choice['maxBatch'] and instances[0].get('maxWaitNanos')==choice['maxWaitNanos'] and instances[0].get('profile',{}).get('id')==choice['profileID'] and instances[0].get('allocation')==choice['allocation'] and instances[0].get('ready') and self.idle(state):
                        save(folder/'ack-and-observed-cap.json',dict(at=now(),native=native,schedulerState=state,pod=pod,routeReady=False));break
                else:
                    ack_snapshot_at=None
                time.sleep(2)
            else: raise RuntimeError('Native ACK/next successful Tick cap convergence exceeded194s; nativeACK='+str(ack))
            owned=next(i for i in life['instances'] if i['id']==frame['instanceID']);owned['maxBatch']=choice['maxBatch']
            save(self.out/'lifecycle.json',life)
        life['stage']='POLICIES_ACKED_ROUTE_PENDING';save(self.out/'lifecycle.json',life)
        save(self.out/'native-policy-complete.json',dict(at=now(),loadReady=False,instruction='Next Main-released bind-routing uses explicit route operation and native ACK; patch DecisionID need not survive.'))

    def bind_routing(self):
        life=load(self.out/'lifecycle.json')
        if life.get('stage')!='POLICIES_ACKED_ROUTE_PENDING': raise RuntimeError('Exact policy stage is not ready for normal routing')
        if (self.out/'routing-result.json').exists() or (self.out/'routing-attempt.json').exists():
            raise RuntimeError('Route attempt already recorded; use explicit read-only accept-routing, never repeat the operation')
        self.assert_policy_idle()
        for owned in life['instances']:
            if owned['maxBatch']!=owned['desiredMaxBatch']: raise RuntimeError('Temporary default policy is not load-ready')
        before=self.state()['snapshot']['at'];routes={i['id']:i['weight'] for i in life['instances']}
        save(self.out/'routing-attempt.json',dict(at=now(),beforeSnapshotAt=before,weights=routes,bindingPath=str(self.a.binding)))
        self.operation(dict(kind='route',workloadUID=UID,weights=routes,reason='Approved D2 strict-max initial8 exact physical binding'),'routing')
        self.accept_routing()

    def accept_routing(self):
        """Finish the same original route using actual ACK and fresh reads, no mutation."""
        life=load(self.out/'lifecycle.json')
        if life.get('stage')!='POLICIES_ACKED_ROUTE_PENDING':
            raise RuntimeError('Read-only routing continuation requires same strict-policy stage')
        self.assert_policy_idle()
        routes={i['id']:i['weight'] for i in life['instances']}
        route_result=load(self.out/'routing-result.json')
        if route_result.get('status')!='complete' or route_result.get('error') or route_result.get('operation',{}).get('kind')!='route':
            raise RuntimeError('Original complete route ACK required')
        settings_ack=route_result
        route_id=route_result['operation']['target']['id'];actual=route_result['actual']
        if actual.get('routing')!=route_result['operation']['target']['routing'] or actual['routing'].get(UID)!=routes or set(actual.get('native',{}))!={i['id'] for i in life['instances']}:
            raise RuntimeError('Normal routing result lacks exact eight-native/full-route ACK')
        for choice in self.b['selected']:
            owned=next(i for i in life['instances'] if i['node']==choice['node'] and i['profileID']==choice['profileID'])
            policy=dict(decisionID=route_id,profileID=choice['profileID'],maxBatchSize=choice['maxBatch'],maxBatchWaitMS=choice['maxWaitNanos']/1000000,deadlinePolicy=choice['firstfloor'].get('deadlinePolicy') or 'keep',fixedBatchSize=0)
            if not self.policy_matches(actual['native'][owned['id']],policy): raise RuntimeError('Normal route result native policy/profile/DecisionID ACK differs')
        save(self.out/'normal-route-complete-eight-ack.json',route_result)
        attempt_path=self.out/'routing-attempt.json'
        before_snapshot=load(attempt_path).get('beforeSnapshotAt') if attempt_path.exists() else None
        route_state=self.verify(life,staging=True,after_snapshot=before_snapshot,completion=settings_ack)
        save(self.out/'routing-state-ack.json',route_state)
        for choice in self.b['selected']:
            frame=self.policy_frame(choice,life)
            if not self.policy_matches(frame['native'],frame['config']['policy']) or frame['native']['metadata'].get('decisionID')!=route_id or frame['native']['metadata']['policy'].get('maxBatchSize')!=choice['maxBatch'] or frame['native']['metadata']['policy'].get('maxBatchWaitMS',0)!=0:
                raise RuntimeError('Final route DecisionID/native actual policy is incoherent')
            save(self.out/('final-route-policy-'+choice['node']['name']+'.json'),frame)
        save(self.out/'final-routing-state.json',self.verify(life,staging=True,completion=settings_ack))
        final_settings=self.settings();save(self.out/'final-routing-settings.json',final_settings)
        if UID in (final_settings.get('autonomousWorkloads') or []) or (final_settings.get('staticRouting') or {}).get(UID)!=routes:
            raise RuntimeError('Final declared routing/autonomy differs from approved exact settings')
        save(self.out/'final-routing-pools.json',self.api('GET','/api/v1/pools'))
        life['stage']='READY_BOUND';save(self.out/'lifecycle.json',life)
        save(self.out/'physical-binding.json',dict(at=now(),instances=life['instances'],routing=routes,weightEncoding='round(kappa*1e6),minimum1; exact resulting integers retained',loadReady=True,decisionIDNote='Final route ID/native ACK, not preservation of external patch DecisionID'))

    def verify(self, life=None, staging=False, after_snapshot=None, completion=None):
        life=life or load(self.out/'lifecycle.json')
        if not staging and life.get('stage')!='READY_BOUND': raise RuntimeError('Setup is not final native+snapshot+route bound; producer must not start')
        end=self.publication_end(completion)[0] if completion is not None else time.monotonic()+194
        wanted={i['id']:i['weight'] for i in life['instances']}
        while True:
            state=self.state();is_idle=self.idle(state);instances=state['snapshot']['instances']
            served=True
            for expected in life['instances']:
                matches=[i for i in instances if i['id']==expected['id']]
                if len(matches)!=1: raise RuntimeError('Physical continuity unavailable: '+expected['id'])
                i=matches[0]
                if i.get('node')!=expected['node'] or i.get('ownerWorkloadUID')!=UID:
                    raise RuntimeError('Owned Node identity mismatch')
                if i.get('workloadUID') not in ('',None,UID):
                    raise RuntimeError('Actual served workload conflicts with exclusive bound owner')
                served=served and i.get('workloadUID')==UID
                if i.get('profile',{}).get('id')!=expected['profileID'] or i.get('allocation')!=expected['allocation'] or i.get('maxBatch')!=expected['maxBatch'] or i.get('maxWaitNanos')!=expected['maxWaitNanos'] or not i.get('ready'):
                    raise RuntimeError('Physical policy/allocation identity mismatch')
            published=self.routing_publication_ready(state,wanted,{})
            if served and published and is_idle and (after_snapshot is None or state['snapshot']['at']!=after_snapshot):return state
            remaining=end-time.monotonic()
            if remaining<=0:raise RuntimeError('Static routing ACK did not reach bound weights within194s')
            time.sleep(min(5,remaining))

    def autonomous_ready(self, state):
        instances=state['snapshot'].get('instances') or []
        routes=state['snapshot'].get('routing',{}).get(UID,{})
        if any(i.get('ownerWorkloadUID')!=UID or i.get('workloadUID') not in ('',None,UID) for i in instances):
            raise RuntimeError('Autonomous physical ownership/served Workload conflicts')
        positive={key:value for key,value in routes.items() if value>0}
        if not instances or not positive or not self.idle(state):return False
        by_id={i['id']:i for i in instances}
        return (len(by_id)==len(instances) and set(positive)<=set(by_id)
                and all(i.get('ready') for i in instances)
                and all(by_id[key].get('workloadUID')==UID for key in positive))

    def wait_autonomous_ready(self, folder):
        end=time.monotonic()+194
        while True:
            state=self.state();save(folder/'unloaded-autonomous-last-state.json',state)
            if self.autonomous_ready(state):
                life=load(self.out/'lifecycle.json')
                life.update(instances=state['snapshot']['instances'],stage='READY_BOUND')
                save(self.out/'lifecycle.json',life)
                save(folder/'unloaded-autonomous-ready.json',dict(at=now(),state=state))
                return state
            remaining=end-time.monotonic()
            if remaining<=0:raise RuntimeError('Unloaded autonomous Ready/positive routing/idle unavailable within194s; producer remains blocked')
            time.sleep(min(5,remaining))

    def autonomy(self, enabled, *, empty_start=False):
        self.wait_idle();settings=self.settings()
        if enabled:
            if empty_start:self.assert_initial_empty(self.assert_policy_idle())
            else:self.verify()
            settings['autonomousWorkloads']=[UID]
            settings['excludedNodes']=list(D2['excludedNodes'])
            self.operation(dict(kind='release',workloadUID=UID,reason='Release D2 manual route before autonomous control'),'autonomy-release')
        else:
            if any(u!=UID for u in settings.get('autonomousWorkloads',[])): raise RuntimeError('Unexpected other autonomous owner')
            settings['autonomousWorkloads']=[]
        self.put(settings)

    def pages(self, folder, path, start, end):
        folder.mkdir(parents=True,exist_ok=True);cursor=None
        for index in range(10000):
            q=dict(**{'from':start,'to':end,'limit':1000})
            if cursor is not None:q['cursor']=cursor
            page=self.api('GET',path+'?'+urllib.parse.urlencode(q))
            save(folder/(str(index).zfill(5)+'.json'),page)
            nxt=page.get('nextCursor')
            if not nxt:return
            if nxt==cursor:raise RuntimeError('Observation cursor did not advance')
            cursor=nxt
        raise RuntimeError('Finite10000-page capture cap reached; no silent partial completion')

    def job(self, label, rate, duration):
        job=load(TEMPLATE);job['metadata']={'name':'d2-before-'+label,'namespace':getattr(self,'config',{}).get('namespace',D2.get('namespace','chill-system')),'labels':{'chill.dacs.io/d2-before':self.a.phase}}
        spec=job['spec'];spec.pop('selector',None);spec.pop('manualSelector',None)
        spec['template']['metadata']={'labels':{'chill.dacs.io/d2-before':self.a.phase}}
        pod=spec['template']['spec'];container=pod['containers'][0]
        container['command']=['python3','-u','-c',LOAD_WRAPPER]
        container['args']=['replay','--endpoint',D2['producerEndpoint'],'--token-file','/var/run/secrets/kubernetes.io/serviceaccount/token','--ca-file','/ca/tls.crt','--workload',NAME,'--dataset-dir','/data','--fields','image','--arrivals','poisson','--seed','1','--workers','320','--duration',str(duration),'--rate',str(rate),'--delivery-margin','0.010','--timeout','30']
        lifetime=D2.get('requestLifetimeSeconds')
        if lifetime is not None:
            if type(lifetime) not in (int,float) or not math.isfinite(lifetime) or lifetime<=0:raise ValueError('Explicit requestLifetimeSeconds must be positive')
            container['args']+=['--request-lifetime',str(lifetime)]
            container['args'][container['args'].index('--timeout')+1]=str(max(30,lifetime+.02))
        if container['resources']!={} or pod['nodeName']!=D2['producerNode']:raise RuntimeError('Historical client resource/node contract differs')
        if container['image']!=D2['producerImage']:
            raise RuntimeError('Load image differs from frozen deployed e18ac client')
        if pod['initContainers'][0]['image']!=D2['datasetImage']:
            raise RuntimeError('Dataset image differs from approved physical payload')
        return job

    def remove_job(self, name, uid):
        actual=self.k(['get','job',name,'-o','json'])
        if actual['metadata']['uid']!=uid:raise RuntimeError('Owned Job UID changed; refuse deletion')
        options=dict(apiVersion='v1',kind='DeleteOptions',propagationPolicy='Foreground',preconditions=dict(uid=uid,resourceVersion=actual['metadata']['resourceVersion']))
        save(self.phase_out/('delete-'+name+'-options.json'),options)
        namespace=getattr(self,'config',{}).get('namespace',D2.get('namespace','chill-system'))
        uri='/apis/batch/v1/namespaces/'+urllib.parse.quote(namespace,safe='')+'/jobs/'+urllib.parse.quote(name,safe='')
        # Raw DELETE returns the real API JSON; ordinary kubectl delete supports
        # only text/name output and does not itself check resourceVersion.
        result=self.k(['delete','--raw',uri,'-f','-'],stdin=json.dumps(options),timeout=75)
        save(self.phase_out/('delete-'+name+'-response.json'),result)
        deadline=time.monotonic()+60
        while time.monotonic()<deadline:
            jobs=self.k(['get','jobs','--field-selector','metadata.name='+name,'-o','json'])['items']
            if any(j['metadata']['uid']!=uid for j in jobs):raise RuntimeError('A different same-name Job exists; never delete it')
            pods=self.k(['get','pods','-l','job-name='+name,'-o','json'])['items']
            owned=[p for p in pods if any(o.get('uid')==uid for o in p['metadata'].get('ownerReferences',[]))]
            if not jobs and not owned:
                save(self.phase_out/('delete-'+name+'-absence.json'),dict(at=now(),jobUID=uid,ownedPodUIDs=[],actualJobs=jobs))
                return
            time.sleep(1)
        raise RuntimeError('Exact owned Job/Pods deletion did not complete within existing60s bound')

    def wait_load_container(self, jobref, folder, deadline):
        while time.monotonic()<deadline:
            pods=self.k(['get','pods','-l','job-name='+jobref[0],'-o','json'])['items']
            if not pods:
                time.sleep(1);continue
            if len(pods)!=1:raise RuntimeError('Owned producer Pod missing/ambiguous during startup')
            pod=pods[0];save(folder/'client-pod-startup.json',pod)
            if not any(o.get('kind')=='Job' and o.get('uid')==jobref[1] for o in pod['metadata'].get('ownerReferences',[])):
                raise RuntimeError('Producer Pod belongs to a different Job UID')
            statuses=pod.get('status',{}).get('containerStatuses',[])
            load=[c for c in statuses if c.get('name')=='load']
            if pod.get('status',{}).get('phase') in ('Failed','Succeeded') or any(c.get('state',{}).get('terminated') for c in load):
                raise RuntimeError('Producer terminated before log attachment/start barrier')
            if any(c.get('state',{}).get('waiting',{}).get('reason') in ('CrashLoopBackOff','ImagePullBackOff','ErrImagePull','CreateContainerConfigError') for c in statuses):
                raise RuntimeError('Producer container startup failed')
            if len(load)==1 and load[0].get('state',{}).get('running'):
                save(folder/'client-pod-before-log-attach.json',pod)
                return pod
            time.sleep(1)
        raise RuntimeError('Actual load container did not run within original3592s startup bound')

    def wait_job_complete(self, jobref, folder, deadline):
        while time.monotonic()<deadline:
            job=self.k(['get','job',jobref[0],'-o','json'])
            if job['metadata']['uid']!=jobref[1]:raise RuntimeError('Producer Job UID changed before terminal publication')
            conditions=job.get('status',{}).get('conditions',[])
            save(folder/'load-job-last-state.json',job)
            if any(c.get('type')=='Failed' and c.get('status')=='True' for c in conditions):
                save(folder/'load-job-terminal.json',job)
                raise RuntimeError('Owned producer Job Failed; no automatic rerun')
            if any(c.get('type')=='Complete' and c.get('status')=='True' for c in conditions):
                save(folder/'load-job-terminal.json',job)
                return job
            time.sleep(1)
        raise RuntimeError('Owned producer terminal condition unavailable within original3650s watchdog')

    def telemetry_ready(self, folder, client_pod=None):
        expected={i['id'].split('/')[0] for i in load(self.out/'lifecycle.json')['instances']}
        deadline=time.monotonic()+120
        while time.monotonic()<deadline:
            seen={};errors=[]
            for path in folder.glob('*-cgroup.jsonl'):
                for line in path.read_text().splitlines():
                    try:r=json.loads(line)
                    except ValueError:continue
                    if r.get('kind')=='collector-error':errors.append(r)
                    if r.get('kind')!='cgroup':continue
                    if any(r.get(k) is None for k in ('cpuUsageNanos','nrThrottled','throttledNanos')):continue
                    key=(r.get('role'),r.get('podUID'),r.get('pod'))
                    seen[key]=seen.get(key,0)+1
            native={uid for (role,uid,pod),count in seen.items() if role=='native' and count>=2}
            server=any(role=='server' and count>=2 for (role,uid,pod),count in seen.items())
            mesh=any(role=='edgemesh' and count>=2 for (role,uid,pod),count in seen.items())
            client=client_pod is None or any(role=='client' and pod==client_pod and count>=2 for (role,uid,pod),count in seen.items())
            if native==expected and server and mesh and client:
                save(folder/'telemetry-start-readiness.json',dict(at=now(),nativePodUIDs=sorted(native),clientPod=client_pod,priorErrors=errors))
                return
            time.sleep(1)
        save(folder/'telemetry-start-failure.json',dict(at=now(),expectedNative=sorted(expected),observed=[dict(role=k[0],podUID=k[1],pod=k[2],samples=n) for k,n in seen.items()],errors=errors))
        raise RuntimeError('Exact prestart cgroup coverage unavailable; no inference release')

    def assert_producer_startable(self, folder, pod, expected_pod, *, barrier_seconds=300):
        event=load(folder/'producer-ready-event.json')
        remaining=producer_barrier_deadline(event,barrier_seconds)-time.time()
        if remaining<=0:raise RuntimeError('Original producer'+str(barrier_seconds)+'s barrier expired; autonomy/GO forbidden')
        assert_producer_running(pod,expected_pod)
        return remaining

    def release_client_end_counter(self, folder, pod, phase):
        expected=next(c for c in pod['status']['containerStatuses'] if c['name']=='load')
        path=folder/(D2['producerNode']+'-cgroup.jsonl')
        row=client_end_counter(path,pod['metadata']['uid'],expected['containerID'],phase['endUTC'])
        if row is None:return False
        current=self.k(['get','pod',pod['metadata']['name'],'-o','json'])
        assert_producer_running(current,pod)
        save(folder/'client-end-counter-proof.json',dict(at=now(),originalEndUTC=phase['endUTC'],drainedUTC=phase['drainedUTC'],collectorPath=str(path),row=row))
        self.k(['exec',pod['metadata']['name'],'-c','load','--','python3','-c',"from pathlib import Path; Path('/tmp/d2-start').unlink()"],timeout=30)
        phase['endCounterReleasedUTC']=now();save(folder/'phase.json',phase)
        return True

    def payload_release(self, folder, pod):
        actual=load(folder/'payload.json')
        event=load(folder/'producer-ready-event.json');remaining=producer_barrier_deadline(event)-time.time()
        if remaining<=0:raise RuntimeError('Original producer300s barrier expired before metadata binding')
        approved=self.release.get('payloadManifestPath')
        if approved:
            if load(approved).get('records')!=actual.get('records'):
                raise RuntimeError('Payload IDs/order/byte sizes differ from Main-bound manifest')
            return
        ready=dict(at=now(),producerReadyUTC=event['at'],setupBarrierDeadlineUTC=dt.datetime.fromtimestamp(producer_barrier_deadline(event),dt.timezone.utc).isoformat(),producerReadyEventPath=str(folder/'producer-ready-event.json'),phase=self.a.phase,sourceSha=self.source,bindingPath=str(self.a.binding),payloadPath=str(folder/'payload.json'),producerPodUID=pod['metadata']['uid'],clientPodPath=str(folder/'client-pod-before-release.json'),resourcePolicy={},CASecret='chill-dashboard-tls/tls.crt',hostPublicCA=str(CA))
        save(folder/'producer-ready-for-main.json',ready)
        print(json.dumps(dict(event='producer-ready-for-Main-technical-binding',path=str(folder/'producer-ready-for-main.json'))),flush=True)
        path=folder/'producer-start-release.json';deadline=time.monotonic()+min(150,remaining)
        while time.monotonic()<deadline:
            if path.exists():
                release=load(path)
                if release.get('executor')!='CampaignOps' or release.get('command')!='start-producer' or release.get('producerPodUID')!=pod['metadata']['uid'] or release.get('payloadPath')!=str(folder/'payload.json') or release.get('bindingPath')!=str(self.a.binding) or release.get('sourceSha')!=self.source or not release.get('mainInstructionUTC'):
                    raise RuntimeError('Producer technical binding is not for this exact prepared Pod/payload/source')
                if time.time()>=producer_barrier_deadline(event):raise RuntimeError('Original producer300s barrier expired before release')
                return
            time.sleep(1)
        raise RuntimeError('Main producer metadata binding not received within finite setup window; no inference released')

    def control_alerts(self, offset):
        path=OPS/'campaign/v2/control-alerts.jsonl'
        if not path.exists():raise RuntimeError('Existing control-alert monitor unavailable')
        with path.open() as f:
            f.seek(offset);data=f.read();new_offset=f.tell()
        for line in data.splitlines():
            try:row=json.loads(line)
            except ValueError:raise RuntimeError('Unreadable control alert')
            decision=control_alert_decision(row)
            if decision['classification']=='fatal-unexpected-restart' and row.get('component') in ('node-agent','node-agent-actuation'):
                evidence=[]
                for reference in self.release.get('nodeActuationEvidenceRefs',[]):
                    if not Path(reference).is_absolute():raise RuntimeError('Node-actuation proof path must be absolute')
                    document=load(reference)
                    if isinstance(document,list):evidence.extend(document)
                    elif isinstance(document.get('results'),list):evidence.extend(document['results'])
                    elif isinstance(document.get('state',{}).get('results'),list):evidence.extend(document['state']['results'])
                    else:evidence.append(document)
                decision=resolve_control_alert(row,evidence,[c['node'] for c in self.b['selected']],authorized_since=self.native_mode_authorized_since,node_reader=lambda name:self.k(['get','node',name,'-o','json'],timeout=20))
            if decision['stop']:
                save(self.phase_out/'control-stop-alert.json',dict(row=row,decision=decision))
                raise RuntimeError('Control safety alert: '+decision['classification'])
            if decision['classification'] in ('approved-node-actuation-boot','approved-node-actuation-in-progress'):
                self.phase_out.mkdir(parents=True,exist_ok=True)
                with (self.phase_out/'node-actuation-boot-alerts.jsonl').open('a') as f:
                    f.write(json.dumps(dict(row=row,decision=decision,evidenceRefs=self.release.get('nodeActuationEvidenceRefs',[])))+'\n')
                if decision['classification']=='approved-node-actuation-in-progress':
                    self.pending_node_boots.append(row)
        return new_offset

    def collect_phase(self, folder, start):
        for suffix in ('cgmon.log','control-alerts.jsonl'):
            path=OPS/'campaign/v2'/suffix
            if path.exists():(folder/suffix).write_bytes(path.read_bytes())
        errors=[]
        for suffix,path in [('request-pages','/api/v1/observations/workloads/'+UID),('scheduler-pages','/api/v1/observations/scheduler')]:
            try:self.pages(folder/suffix,path,start,now())
            except Exception as error:errors.append(suffix+': '+str(error))
        try:
            pods=self.k(['get','pods','-o','json']);save(folder/'pods-post.json',pods)
            serving=[p for p in pods['items'] if p['metadata'].get('labels',{}).get('app.kubernetes.io/component')=='serving-entrypoint']
            if len(serving)==1:
                name=serving[0]['metadata']['name']
                result=subprocess.run(K+['logs',name,'-c','serving-entrypoint','--since-time='+start,'--timestamps=true'],text=True,capture_output=True,env=ENV,timeout=129)
                (folder/'server.log').write_text(result.stdout);(folder/'server.stderr').write_text(result.stderr)
                if result.returncode:errors.append('server logs exit '+str(result.returncode))
        except Exception as error:errors.append('server logs: '+str(error))
        if errors:raise RuntimeError('Raw collection incomplete: '+str(errors))

    def run(self, label, rate, duration, autonomous, *, empty_start=False):
        phase_dir=self.phase_out/label
        if phase_dir.exists(): raise RuntimeError('Phase directory exists; no implicit rerun')
        self.pending_node_boots=[]
        self.native_mode_authorized_since=None
        phase_dir.mkdir(parents=True);self.boundary(phase_dir,'pre')
        settings=self.settings()
        if settings.get('autonomousWorkloads')!=[]:raise RuntimeError('Initial autonomy must be off until producer start barrier')
        if empty_start:
            if not autonomous:raise RuntimeError('Empty start requires autonomous diagnostic')
            if (self.out/'lifecycle.json').exists():raise RuntimeError('Empty start requires fresh lifecycle evidence')
            state=self.assert_policy_idle();self.assert_initial_empty(state)
            save(self.out/'lifecycle.json',dict(startUTC=now(),originalSettings=settings,originalNodes=state['snapshot']['nodes'],instances=[],stage='EMPTY_START',emptyStart=True))
        else:self.verify()
        hosts=self.a.hosts
        if hosts is None:raise RuntimeError('Exact bound telemetry host inventory required before load')
        alerts=OPS/'campaign/v2/control-alerts.jsonl'
        if not alerts.exists():raise RuntimeError('CPO existing control-alert monitor is not bound')
        alert_offset=alerts.stat().st_size
        sampler=subprocess.Popen([sys.executable,str(ROOT/'sample_cgroups.py'),'--hosts',str(hosts),'--duration','3780','--phase',label,'--out',str(phase_dir)],env=ENV)
        phase=dict(phase=label,kind='autonomous' if autonomous else 'static-staircase',sourceSha=self.source,rate=rate,warmupSeconds=60,durationSeconds=duration,startUTC=None,endUTC=None,offeredRequests=0 if rate==0 else None,autonomous=autonomous,static=not autonomous,emptyStart=empty_start,setupStartUTC=now())
        save(phase_dir/'phase.json',phase)
        jobref=None;logs=None
        autonomy_attempted=False
        try:
            if empty_start:
                autonomy_attempted=True
                self.native_mode_authorized_since=now();phase['nativeModeAuthorizedSinceUTC']=self.native_mode_authorized_since
                self.autonomy(True,empty_start=True)
                self.wait_autonomous_ready(phase_dir)
                save(phase_dir/'phase.json',phase)
            if rate==0:
                (phase_dir/'client.jsonl').write_text('')
                self.telemetry_ready(phase_dir)
                phase['startUTC']=now();save(phase_dir/'phase.json',phase)
                deadline=time.monotonic()+duration
                while time.monotonic()<deadline:
                    alert_offset=self.control_alerts(alert_offset)
                    time.sleep(min(1,max(0,deadline-time.monotonic())))
                phase['endUTC']=now()
            else:
                manifest=self.job(label,rate,duration);save(phase_dir/'load-job.json',manifest)
                created=self.k(['create','-f','-','-o','json'],stdin=json.dumps(manifest))
                jobref=(created['metadata']['name'],created['metadata']['uid']);save(phase_dir/'load-job-created.json',created)
                producer_setup_start=time.monotonic()
                deadline=producer_setup_start+3650
                startup_deadline=producer_setup_start+3592
                log_pod=self.wait_load_container(jobref,phase_dir,startup_deadline)
                argv=K+['logs','-f','pod/'+log_pod['metadata']['name'],'-c','load','--pod-running-timeout=3592s']
                logs=subprocess.Popen(argv,stdout=subprocess.PIPE,stderr=(phase_dir/'load-follow.stderr').open('w'),env=ENV)
                os.set_blocking(logs.stdout.fileno(),False);pending_lines=b''
                with (phase_dir/'client.jsonl').open('w') as client,(phase_dir/'producer-other.log').open('w') as other:
                    sel=selectors.DefaultSelector();sel.register(logs.stdout,selectors.EVENT_READ)
                    while time.monotonic()<deadline:
                        alert_offset=self.control_alerts(alert_offset)
                        for key,_ in sel.select(timeout=1):
                            lines,pending_lines,eof=read_stream_lines(key.fileobj,pending_lines)
                            if eof:sel.unregister(key.fileobj)
                            for line in lines:
                                try:r=json.loads(line)
                                except ValueError:other.write(line+'\n');other.flush();continue
                                if r.get('d2Event'):
                                    with (phase_dir/'producer-control-events.jsonl').open('a') as events:events.write(json.dumps(r)+'\n')
                                if r.get('d2Event')=='payload':save(phase_dir/'payload.json',r);phase['offeredRequests']=r['offeredRequests']
                                elif r.get('d2Event')=='ready-for-release':
                                    save(phase_dir/'producer-ready-event.json',r)
                                    phase['producerReadyUTC']=r['at'];phase['setupBarrierDeadlineUTC']=dt.datetime.fromtimestamp(producer_barrier_deadline(r),dt.timezone.utc).isoformat();save(phase_dir/'phase.json',phase)
                                    pods=self.k(['get','pods','-l','job-name='+jobref[0],'-o','json'])['items']
                                    if len(pods)!=1:raise RuntimeError('Owned producer Pod missing/ambiguous')
                                    pod=pods[0];self.assert_producer_startable(phase_dir,pod,log_pod);save(phase_dir/'client-pod-before-release.json',pod)
                                    client_container=next(c for c in pod['spec']['containers'] if c['name']=='load')
                                    if client_container.get('resources',{})!={} or pod['spec'].get('nodeName')!=D2['producerNode']:
                                        raise RuntimeError('Admitted client resources/node differ from frozen binding')
                                    self.telemetry_ready(phase_dir,pod['metadata']['name'])
                                    self.payload_release(phase_dir,pod)
                                    self.assert_producer_startable(phase_dir,self.k(['get','pod',pod['metadata']['name'],'-o','json']),log_pod)
                                    if autonomous and not empty_start:
                                        autonomy_attempted=True
                                        self.native_mode_authorized_since=now();phase['nativeModeAuthorizedSinceUTC']=self.native_mode_authorized_since
                                        self.autonomy(True)
                                    if empty_start:
                                        state=self.state()
                                        if not self.autonomous_ready(state):raise RuntimeError('Unloaded autonomous readiness changed before producer release')
                                        expected={i['id'] for i in load(self.out/'lifecycle.json')['instances']}
                                        if {i['id'] for i in state['snapshot']['instances']}!=expected:raise RuntimeError('Autonomous physical set changed after telemetry binding')
                                    self.assert_producer_startable(phase_dir,self.k(['get','pod',pod['metadata']['name'],'-o','json']),log_pod)
                                    self.k(['exec',pod['metadata']['name'],'-c','load','--','python3','-c',"from pathlib import Path; Path('/tmp/d2-start').touch()"],timeout=30)
                                elif r.get('d2Event')=='replay-start':
                                    phase['startUTC']=r['at']
                                    phase['endUTC']=(dt.datetime.fromisoformat(r['at'])+dt.timedelta(seconds=duration)).isoformat()
                                    save(phase_dir/'phase.json',phase)
                                elif r.get('d2Event')=='replay-drained':phase['drainedUTC']=r['at']
                                elif 'scheduledOffsetNs' in r:client.write(json.dumps(r)+'\n');client.flush()
                                else:other.write(line+'\n');other.flush()
                        if phase.get('drainedUTC') and not phase.get('endCounterReleasedUTC'):
                            self.release_client_end_counter(phase_dir,log_pod,phase)
                        if logs.poll() is not None and not sel.get_map():break
                    else:raise RuntimeError('Producer finite watchdog exceeded; retain partial evidence')
                if logs.wait(timeout=5)!=0:raise RuntimeError('Load log stream failed; actual producer status unknown')
                if not phase.get('endCounterReleasedUTC'):
                    raise RuntimeError('Producer exited without exact postboundary client counter proof')
                self.wait_job_complete(jobref,phase_dir,deadline)
                if phase['startUTC'] is None:raise RuntimeError('Actual producer start witness absent')
            save(phase_dir/'phase.json',phase)
            self.wait_idle();self.boundary(phase_dir,'post')
            alert_offset=self.control_alerts(alert_offset)
            time.sleep(2)
            # End sampling only after drain. SIGTERM stops read-only children; no native process is signalled.
            sampler.terminate();sampler.wait(timeout=15)
            self.collect_phase(phase_dir,phase['startUTC'])
            if jobref:self.remove_job(*jobref);jobref=None
            if self.pending_node_boots:
                post_state_path=phase_dir/'post-state.json'
                proof=load(post_state_path).get('results',[])
                action_pages=sorted((phase_dir/'scheduler-pages').glob('*.json'))
                for page_path in action_pages:
                    for event in load(page_path).get('events',[]):
                        actual=(event.get('action') or {}).get('actual')
                        if isinstance(actual,dict):proof.append(actual)
                reconciled=[dict(row=row,decision=control_alert_decision(row,proof,[c['node'] for c in self.b['selected']])) for row in self.pending_node_boots]
                save(phase_dir/'node-actuation-boot-reconciliation.json',dict(at=now(),sources=[str(post_state_path)]+[str(p) for p in action_pages],records=reconciled,instruction='Only immediately actual-Node-correlated in-progress boots may reach here; each needs actual completed same-Node power outcome. Durable event.action.actual is FULL OperationResult, not wrapper completedAt. Formal power results do not export numeric generation.'))
                if any(record['decision']['stop'] for record in reconciled):
                    raise RuntimeError('Immediately correlated native transition lacks completed same-Node outcome after drain')
            argv=[sys.executable,str(ROOT/'analyze_before.py'),'--phase-dir',str(phase_dir),'--out',str(phase_dir/'summary.json')]
            if rate and not autonomous:
                idle=self.phase_out/'static-idle'/'summary.json'
                if idle.exists():argv+=['--idle-summary',str(idle)]
            r=subprocess.run(argv,env=ENV)
            if r.returncode:raise RuntimeError('Offline analysis failed; no next rate')
            return load(phase_dir/'summary.json')
        except Exception as e:
            phase['error']=str(e);phase['failureUTC']=now();save(phase_dir/'phase.json',phase)
            raise
        finally:
            preserving_primary=sys.exc_info()[0] is not None
            cleanup_errors=[]
            if preserving_primary:
                try:
                    self.collect_phase(phase_dir,phase.get('startUTC') or phase['setupStartUTC'])
                except Exception as cleanup_error:cleanup_errors.append('raw-preservation: '+str(cleanup_error))
                try:
                    if jobref:
                        pods=self.k(['get','pods','-l','job-name='+jobref[0],'-o','json'])
                        save(phase_dir/'failed-producer-pods.json',pods)
                        for pod in pods['items']:
                            if not any(owner.get('uid')==jobref[1] for owner in pod['metadata'].get('ownerReferences',[])):continue
                            result=subprocess.run(K+['logs',pod['metadata']['name'],'-c','load','--timestamps=true'],text=True,capture_output=True,env=ENV,timeout=129)
                            (phase_dir/('failed-producer-'+pod['metadata']['uid']+'.log')).write_text(result.stdout)
                            (phase_dir/('failed-producer-'+pod['metadata']['uid']+'.stderr')).write_text(result.stderr)
                            if result.returncode:raise RuntimeError('Failed producer log collection exit '+str(result.returncode))
                except Exception as cleanup_error:cleanup_errors.append('producer-log-preservation: '+str(cleanup_error))
            if preserving_primary and autonomy_attempted:
                try:
                    current=self.settings();restore=copy.deepcopy(current)
                    members=[u for u in current.get('autonomousWorkloads',[]) if u!=UID]
                    if UID in settings.get('autonomousWorkloads',[]):members.append(UID)
                    restore['autonomousWorkloads']=members;restore['excludedNodes']=settings.get('excludedNodes',[])
                    response=self.put(restore)
                    actual_restore=self.settings()
                    save(phase_dir/'failed-start-autonomy-restore.json',dict(at=now(),prior=settings,requested=restore,response=response,actual=actual_restore,topologyRestored=False,instruction='Preserve actual topology; logical8 reset requires separately approved existing path, never assume old8 survived'))
                    if actual_restore.get('autonomousWorkloads')!=members or actual_restore.get('excludedNodes',[])!=settings.get('excludedNodes',[]):
                        raise RuntimeError('Prior owned autonomy/exclusions did not restore')
                except Exception as cleanup_error:cleanup_errors.append('autonomy-restore: '+str(cleanup_error))
            if preserving_primary:
                try:
                    if logs and logs.poll() is None:logs.terminate()
                except Exception as cleanup_error:cleanup_errors.append('log-follower: '+str(cleanup_error))
                try:
                    if jobref:self.remove_job(*jobref)
                except Exception as cleanup_error:cleanup_errors.append('owned-job: '+str(cleanup_error))
            if cleanup_errors:
                phase['cleanupErrors']=cleanup_errors;save(phase_dir/'phase.json',phase)
                save(phase_dir/'secondary-cleanup-errors.json',dict(at=now(),primaryError=phase.get('error'),cleanupErrors=cleanup_errors))
                print('SECONDARY_CLEANUP_FAILURE '+json.dumps(cleanup_errors),file=sys.stderr,flush=True)
                if not preserving_primary:raise RuntimeError('Cleanup failed: '+str(cleanup_errors))
            try:
                if sampler.poll() is None:
                    sampler.terminate()
                    try:sampler.wait(timeout=15)
                    except subprocess.TimeoutExpired:sampler.kill();sampler.wait()
            except Exception as cleanup_error:
                save(phase_dir/'secondary-sampler-cleanup-error.json',dict(at=now(),error=str(cleanup_error),preservingPrimary=preserving_primary))
                if not preserving_primary:raise

    def cleanup(self):
        life=load(self.out/'lifecycle.json')
        result=self.operation(dict(kind='reset',workloadUIDs=[UID],reason='Approved D2 owned campaign cleanup'),'cleanup-reset')
        original=copy.deepcopy(life['originalSettings'])
        original['autonomousWorkloads']=[uid for uid in original.get('autonomousWorkloads',[]) if uid!=UID]
        save(self.out/'cleanup-original-settings-ack.json',self.put(original))
        save(self.out/'cleanup-state.json',self.state())
        save(self.out/'cleanup-complete.json',dict(at=now(),physicalInstances=0,resetStatus=result['status'],powerReturn='Server reset minimum supported idle mode'))

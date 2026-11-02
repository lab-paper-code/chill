#!/usr/bin/env python3
"""CPO-only numeric phase6 -> actual canonical registration, evidence binding and frozen evaluation execution.
Reads the existing plan's sealed actual references; never selects a latest-looking old run.
Preparation code only: no command in this module runs on import.
"""
import argparse
import copy
import datetime as dt
import importlib.util
import gzip
import json
import math
import os
from pathlib import Path
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request

HERE=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('evaluationops',HERE/'conductor.py'); harness_module=importlib.util.module_from_spec(spec); spec.loader.exec_module(harness_module)
PLAN=Path(harness_module.EVALUATION['plan'])
DATA_PROBE=r'''
import hashlib,json
from pathlib import Path
from chill_loadgen import cli
fields=json.loads(__import__('sys').argv[1])
def identity(records):
    return [{'recordID':rid,'fields':{k:{'bytes':len(v),'sha256':hashlib.sha256(v).hexdigest()} for k,v in inputs.items()}} for rid,inputs in records]
profile=identity(cli.load_records(Path('/data/profiling'),fields)); evaluation=identity(cli.load_records(Path('/data/evaluation'),fields))
if {x['recordID'] for x in profile}&{x['recordID'] for x in evaluation}: raise ValueError('Pinned carrier profiling/evaluation record IDs overlap')
parser=cli.build_parser(); replay=next(a for a in parser._actions if getattr(a,'choices',None) and 'replay' in a.choices).choices['replay']
flags=sorted({flag for action in replay._actions for flag in action.option_strings})
profile_images={x['fields']['image']['sha256'] for x in profile if 'image' in x['fields']}; eval_images={x['fields']['image']['sha256'] for x in evaluation if 'image' in x['fields']}
print(json.dumps({'profiling':profile,'evaluation':evaluation,'cliFlags':flags,'recordIDDisjoint':True,'sharedImageHashes':sorted(profile_images&eval_images),'heldoutMeaning':'Record/question-ID split, not a claim of disjoint image bytes'}))
'''


def template(op, workload, image, probe=False):
    """Same dataset-specific source loadJob recipe as serving/kube/occupancy.go:698-729."""
    w=op['workloads'][workload]; data={'name':'data','mountPath':'/data'}
    return {'apiVersion':'batch/v1','kind':'Job','metadata':{'namespace':op.get('namespace',harness_module.ENVIRONMENT['d2'].get('namespace','chill-system'))},'spec':{'backoffLimit':0,'template':{'metadata':{},'spec':{'restartPolicy':'Never','nodeName':harness_module.EVALUATION['producerNode'],'serviceAccountName':'chill-load-generator','initContainers':[{'name':'dataset','image':w['datasetImage'],'command':['sh','-ec','cp -R "$1"/. /data/','--',w['datasetImagePath']+('' if probe else '/evaluation')],'volumeMounts':[data],'resources':{}}],'containers':[{'name':'load','image':image,'volumeMounts':[data,{'name':'ca','mountPath':'/ca','readOnly':True}],'resources':{}}],'volumes':[{'name':'data','emptyDir':{}},{'name':'ca','projected':{'sources':[{'secret':{'name':'chill-dashboard-tls','items':[{'key':'tls.crt','path':'tls.crt'}]}}]}}]}}}}


class Prepare:
    def __init__(self,a):
        self.a=a; self.n=harness_module.load(a.numbers); self.release=harness_module.load(a.release); self.op=harness_module.load(harness_module.OPERATIONAL_RECIPE); self.out=a.out
        self.op['collectorTokenFile']=str(harness_module.OPS/'.token-collector')
        if harness_module.CAMPAIGN.resolve() not in self.out.resolve().parents:raise ValueError('Preparation output must remain below the external campaign directory')
        if self.release.get('executor')!='CampaignOps' or self.release.get('command')!='evaluation-execute' or not self.release.get('mainInstructionUTC'): raise ValueError('One actual Main/CPO frozen evaluation-execute release required')
        self.sources=harness_module.approval_sources(self.release)
        harness_module.need(self.n,['approval','g0Evidence','mSeconds','N','burstLowSeconds','timeboxes','smokeSeconds','workloads','kappaSeconds'],'Approved phase6 numbers')
        if harness_module.approval_sources(self.n['approval']) != self.sources:raise ValueError('Approved numeric component sources differ from release')
        harness_module.need(self.n['timeboxes'],harness_module.REQUIRED['timeboxes'],'Actual finite timeboxes')
        harness_module.freeze_harness_config(self.n)
        self.op['namespace']=self.n['namespace']
        stages=harness_module.configured_stage_specs(self.n)
        if not all(harness_module.positive(self.n[k]) for k in ('smokeSeconds','kappaSeconds')) or not 0<self.n['burstLowSeconds']<self.n['burstLowMaximumSeconds'] or type(self.n['mSeconds']) not in (int,float) or not math.isfinite(self.n['mSeconds']) or self.n['mSeconds']<0:raise ValueError('Invalid approved numerical scope')
        if any(self.n['timeboxes']['setupSeconds']+sum(seconds for label,seconds,_ in stages if label==row['id'] or label.startswith(row['id']+'-'))+self.n['timeboxes']['drainSeconds']+self.n['timeboxes']['flushSeconds']>self.n['collection']['maximumDurationSeconds'] for row in self.n['stages']):raise ValueError('Timeboxes do not fit selected finite collector')
        if self.release.get('numbersPath')!=str(a.numbers):raise ValueError('Frozen release must name the actual approved numeric input file')
        if self.out.exists() and any(self.out.iterdir()):raise ValueError('Preparation output is not fresh; retain prior attempt, no overwrite/retry')
        if Path(self.release['bindingPath']).exists():raise ValueError('Frozen binding already exists')
        if type(self.n['N']) is not int or not 1<=self.n['N']<=len(self.op['factorOrder']):raise ValueError('N must select the frozen source-witnessed factor menu prefix; no guessed count or phantom factor')
        self.factor_names=self.op['factorOrder'][:self.n['N']]
        for name in harness_module.WORKLOADS:
            values=self.n['workloads'][name];harness_module.need(values,['slo','R','deliverySeconds','socketSeconds','workers'],name)
            if not 0<values['slo']['percentile']<=100 or not harness_module.positive(values['slo']['targetLatencyNanos']) or not all(harness_module.positive(values[k]) for k in ('R','deliverySeconds','socketSeconds','workers')) or type(values['workers']) is not int:raise ValueError('Invalid approved Workload numerical bindings')
            lifetime=values.get('lifetimeSeconds')
            if lifetime is not None and (not harness_module.positive(lifetime) or lifetime<values['slo']['targetLatencyNanos']/1e9 or values['socketSeconds']<=lifetime+values['deliverySeconds']):raise ValueError('Invalid explicit lifetime/SLO/socket tuple')
        shares=harness_module.pool_shares(self.n)
        weights=dict(zip(harness_module.WORKLOADS,(shares['MNV'],shares['PLC'],shares['Smol'])))
        gate_config=dict(self.n,workloads=[dict(values,name=name) for name,values in self.n['workloads'].items()])
        harness_module.g0_evidence_gate(gate_config,sum(self.n['workloads'][name]['R']*weights[name] for name in harness_module.WORKLOADS))
        self.sha=self.sources['server']; self.out.mkdir(parents=True,exist_ok=True)
        plan=harness_module.load(PLAN)
        if plan['sourcePair']['after'].get('sha')!=self.sha: raise ValueError('Actual final D2 SHA not sealed in existing plan; no old-source fallback')
        self.pub=plan['evaluationInputPublication']
        harness_module.need(self.pub,['imagePublication','after','mMeasurement','d25','ranking','costs','bias','kappaExecutable'],'sealed actual evaluation publication')
        for key in ('imagePublication','after','mMeasurement','d25','ranking','costs','bias','kappaExecutable'): harness_module.ref(self.pub[key])
        power=Path(harness_module.EVALUATION['powerExecutable'])
        receipt_path=Path(harness_module.EVALUATION['powerReceipt']);receipt=harness_module.load(receipt_path);actual_power=harness_module.ref(power)
        harness_module.require_component_source(receipt,self.sources['power'],'power',str(power))
        if receipt.get('executable')!=str(power) or receipt.get('sha256')!=actual_power['sha256'] or receipt.get('buildExitCode')!=0 or not receipt.get('atUTC') or not receipt.get('buildArgv') or not receipt.get('buildCwd'):raise ValueError('Existing offline power executable lacks actual component-source once-window build receipt')
        self.op['collection'].update(powerExecutable=str(power),powerReceipt=str(receipt_path))
        after=harness_module.load(self.pub['after'])
        harness_module.need(after,['sourceSha','files','phaseRoot','physicalLifecycle','physicalBinding'],'Actual AFTER capture')
        if after['sourceSha']!=self.sha:raise ValueError('AFTER capture is not the sealed final source')
        for file in after['files'].values():harness_module.ref(file)
        harness_module.ref(after['physicalLifecycle']);harness_module.ref(after['physicalBinding'])
        expected=harness_module.load(harness_module.EVALUATION['d25Manifest'])
        handoff=expected['afterNodeHandoffReceipt'];nodes={x['node']['name']:x['node'] for x in expected['targets']}
        for name,node in nodes.items():
            receipt=harness_module.load(harness_module.D2_STATE/'after-node-handoff'/(name+'.json'))
            if receipt.get('schemaVersion')!=handoff['schemaVersion'] or any(receipt.get('node',{}).get(k)!=node[k] for k in ('name','uid')) or any(receipt.get(k)!=v for k,v in handoff['requiredValues'].items()):raise ValueError('Actual AFTER paired/autonomous/native-return closure incomplete: '+name)
            for field in ('pairedAfterTerminalRef','autonomousAfterTerminalRef','nativeReturnRef'):harness_module.ref(receipt[field])
        for name in self.factor_names:
            witness=self.op['factorMenu'][name]['witness']
            if not witness.get('sourceRefs') or not witness.get('effect') or not witness.get('resetConditions'):raise ValueError('Existing operational source/reset witness incomplete; do not ask phase6 to invent a protocol')
        images=harness_module.load(self.pub['imagePublication'])
        if images['commit']!=self.sha: raise ValueError('Image publication is not actual final D2 source')
        self.images={x['component']:x for x in images['images']}
        for key in ('server','loadgen'):
            x=self.images[key]
            harness_module.require_component_source({'sourceSHA':x['sourceCommit']},self.sources[key],key)
            if x['publish']['exitCode']!=0 or '@sha256:' not in x['immutableImage']: raise ValueError('Actual control/producer component publication incomplete')
        self.tb=self.n['timeboxes']; self.k=harness_module.ENVIRONMENT['d2']['kubectl']+['-n',self.n['namespace']]
        old=harness_module.module('evaluationd2connect',harness_module.D2/'conduct_before.py'); self.http=urllib.request.build_opener(old.NoRedirect(),urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=self.op['caFile'])))
        old.K=self.k;self.io=object.__new__(old.Conductor);self.io.out=self.out;self.io.source=self.sha
        self.io.config=self.n
        self.io.journal=self.out/'dataset-commands.jsonl';self.io.journal_lock=harness_module.threading.Lock()
        self.io.a=argparse.Namespace(phase='evaluation-dataset-preparation',command=self.a.command);self.io.phase_out=self.out
    def api(self,method,path,body=None,headers=None,accepted=(200,)):
        token = self.op['collectorTokenFile'] if method == 'GET' else self.op['tokenFile']
        h={'Authorization':'Bearer '+Path(token).read_text().strip(),**(headers or {})}
        if body is not None:h['Content-Type']='application/json'
        request=urllib.request.Request(self.op['endpoint']+path,data=json.dumps(body).encode() if body is not None else None,headers=h,method=method)
        try:
            with self.http.open(request,timeout=self.tb['apiSeconds']) as r: status=r.status; raw=r.read()
        except urllib.error.HTTPError as exc: status=exc.code; raw=exc.read()
        result=json.loads(raw)
        if status not in accepted: raise RuntimeError('Actual management API '+method+' '+path+' status '+str(status)+': '+json.dumps(result))
        return result,status
    def kubectl(self,args,body=None):
        r=subprocess.run(self.k+args,input=json.dumps(body) if body is not None else None,text=True,capture_output=True,timeout=self.tb['apiSeconds'])
        if r.returncode:raise RuntimeError('Actual kubectl failed: '+r.stderr[:500])
        return json.loads(r.stdout) if r.stdout.strip() else None
    def controller(self,declared=None):
        """Resolve the newest closed cluster Pod facts; never inspect CHILL via kubectl."""
        deadline=time.monotonic()+self.tb['setupSeconds']
        wait=60-time.time()%60
        if wait>=self.tb['setupSeconds']:raise RuntimeError('Setup deadline cannot include the next closed observation minute')
        time.sleep(wait)
        archive=harness_module.module('evaluation_wire_archive',HERE/'archive.py');self.io.http=self.http
        hour=dt.datetime.now(dt.timezone.utc).replace(minute=0,second=0,microsecond=0);seen=set()
        while time.monotonic()<deadline:
            key=hour.strftime('%Y%m%dT%H')
            index,status=self.api('GET','/api/v1/observations/raw?hour='+key,accepted=(200,404))
            if status==200 and any(kind['kind']=='cluster' and kind['available'] for kind in index['kinds']):
                target=self.out/('cluster-'+key+'.jsonl.gz')
                archive._download(self.out,self.io,'/api/v1/observations/raw/'+key+'/cluster',target,deadline)
                with gzip.open(target,'rt') as source:rows=[json.loads(line) for line in source if line.strip()]
                candidates=[]
                for row in sorted(rows,key=lambda row:row['at'],reverse=True):
                    pod=row.get('pod')
                    if not pod or pod['uid'] in seen:continue
                    seen.add(pod['uid'])
                    if row['event']=='deleted' or pod.get('deletionTimestamp'):continue
                    if any(container['name']=='serving-entrypoint' for container in pod.get('containers',[])):candidates.append(pod)
                if candidates:
                    if len(candidates)!=1:raise RuntimeError('API cluster controller identity ambiguous')
                    pod=candidates[0];container=next(c for c in pod['containers'] if c['name']=='serving-entrypoint')
                    image_id=container.get('imageID','')
                    allowed=({declared['indexDigest']}|{p['manifestDigest'] for p in declared.get('platforms',{}).values()}) if declared is not None else None
                    if pod.get('ready')!='True' or container.get('terminated') or 'sha256:' not in image_id or (allowed is not None and 'sha256:'+image_id.rsplit('sha256:',1)[-1] not in allowed):raise RuntimeError('API cluster controller is not Ready on the required image')
                    harness_module.save(self.out/'controller.json',{'pod':pod,'source':harness_module.ref(target),'capturedUTC':harness_module.now()})
                    return pod,container
            hour-=dt.timedelta(hours=1)
        raise RuntimeError('API cluster controller identity absent within setup deadline')
    def safety(self):
        s,_=self.api('GET','/api/v1/scheduler/state'); snap=s['snapshot']
        if snap.get('instances') or snap.get('outstanding') or snap.get('reservations') or any(x.get('status') in ('running','cancel') for x in s.get('actions',[])):raise RuntimeError('Fresh global serving/action drain required before canonical registration/binding')
        jobs=self.kubectl(['get','jobs','-o','json'])['items']
        if any(not j.get('spec',{}).get('suspend') and not any(c.get('type') in ('Complete','Failed') and c.get('status')=='True' for c in j.get('status',{}).get('conditions',[])) for j in jobs):raise RuntimeError('Measurement/producer still active; no binding calculation or smoke')
        self.control_pod,self.control_container=self.controller(self.images['server'])
        settings,_=self.api('GET','/api/v1/scheduler/settings')
        self.cap=settings['requestTimeout']/1e9
        if not harness_module.positive(self.cap) or any(self.n['workloads'][name].get('lifetimeSeconds') is not None and self.n['workloads'][name]['lifetimeSeconds']>self.cap for name in harness_module.WORKLOADS):raise ValueError('Approved explicit lifetime exceeds actual API HandlerConfig cap')
        harness_module.d25_gate(self.pub['d25'],snap)
        return s
    def canonical(self):
        for name in harness_module.WORKLOADS:
            frozen=self.op['workloads'][name]; numerical=self.n['workloads'][name]; slo=numerical['slo']
            body={'modelRef':{'name':frozen['modelRef']},'preprocessorRef':{'name':frozen['preprocessorRef']},'datasetRef':{'name':frozen['datasetRef']},'slo':{'percentile':slo['percentile'],'targetLatency':str(slo['targetLatencyNanos'])+'ns'}}
            actual,status=self.api('GET','/api/v1/workloads/'+name,accepted=(200,404))
            if status==404:
                result,_=self.api('POST','/api/v1/workloads',{'name':name,'spec':body},accepted=(201,));harness_module.save(self.out/('register-'+name+'.json'),result)
            else:
                desired=actual['desired']; existing=desired['spec']
                if any(existing[k]!=body[k] for k in ('modelRef','preprocessorRef','datasetRef')):raise RuntimeError('Canonical reference differs; no replacement or fake UID: '+name)
                applied=actual.get('appliedDefinition') or {}
                if applied.get('slo')!=slo:
                    result,_=self.api('PUT','/api/v1/workloads/'+name,{'spec':body},headers={'If-Match':desired['metadata']['resourceVersion']},accepted=(200,202)); harness_module.save(self.out/('slo-apply-'+name+'.json'),result)
        deadline=time.monotonic()+self.tb['settleSeconds']
        while time.monotonic()<deadline:
            s,_=self.api('GET','/api/v1/scheduler'); snap=s['state']['snapshot']
            if all(any(w['name']==name and w['slo']==self.n['workloads'][name]['slo'] for w in snap['workloads']) for name in harness_module.WORKLOADS):return s
            time.sleep(1)
        raise RuntimeError('Actual canonical UID/generation/SLO did not apply in finite time; no invented identity')
    def dataset(self,name,image):
        job=template(self.op,name,image,probe=True); label='load-data-'+self.op['workloads'][name]['datasetRef'];job['metadata']['name']=label;job['spec']['activeDeadlineSeconds']=int(self.tb['setupSeconds']); c=job['spec']['template']['spec']['containers'][0]
        fields=['image','question'] if name!=harness_module.WORKLOADS[1] else ['records'];c['command']=['python3','-u','-c',DATA_PROBE];c['args']=[json.dumps(fields)]
        created=self.kubectl(['create','-f','-','-o','json'],job);harness_module.save(self.out/(label+'-created.json'),created)
        deadline=time.monotonic()+self.tb['setupSeconds'];folder=self.out/(label+'-lifecycle');folder.mkdir()
        self.io.a.phase=label;self.io.phase_out=folder;failure=None
        try:
            self.io.wait_job_complete((label,created['metadata']['uid']),folder,deadline)
            pods=self.kubectl(['get','pods','-l','job-name='+label,'-o','json']);harness_module.save(self.out/(label+'-pods.json'),pods)
            if len(pods['items'])!=1 or not any(owner.get('kind')=='Job' and owner.get('uid')==created['metadata']['uid'] for owner in pods['items'][0]['metadata'].get('ownerReferences',[])):raise RuntimeError('Completed metadata Pod belongs to a different/ambiguous Job UID')
            r=subprocess.run(self.k+['logs','pod/'+pods['items'][0]['metadata']['name'],'-c','load'],capture_output=True,text=True,timeout=self.tb['apiSeconds'])
            (folder/'identity.stdout').write_text(r.stdout);(folder/'identity.stderr').write_text(r.stderr)
            if r.returncode:raise RuntimeError('Dataset identity result log unavailable')
            result=json.loads(r.stdout);harness_module.save(self.out/(label+'-identity.json'),result);return result
        except Exception as error:
            failure=error;raise
        finally:
            try:self.io.remove_job(label,created['metadata']['uid'])
            except Exception as cleanup_error:
                harness_module.save(folder/'cleanup-error.json',{'at':harness_module.now(),'primaryError':str(failure) if failure else None,'cleanupError':str(cleanup_error),'instruction':'Preserve primary failure and exact owned artifacts; no implicit retry'})
                if failure is None:raise
    def run(self):
        self.safety(); snapshot=self.canonical(); settings=snapshot['settings']
        if settings.get('autonomousWorkloads'):raise RuntimeError('Canonical registration must not enable autonomous control')
        for key in ('staticRouting','staticRoutingValidatedAt','meters','requestTimeout'):settings.pop(key,None)
        settings['completionMarginMS']=self.n['mSeconds']*1000;settings['excludedNodes']=harness_module.EVALUATION['excludedNodes']
        self.api('PUT','/api/v1/scheduler/settings',settings)
        deadline=time.monotonic()+self.tb['settleSeconds']
        while time.monotonic()<deadline:
            snapshot,_=self.api('GET','/api/v1/scheduler')
            if snapshot['settings']['completionMarginMS']==self.n['mSeconds']*1000:break
            time.sleep(1)
        else:raise RuntimeError('Actual approved margin settings did not apply')
        harness_module.save(self.out/'scheduler.json',snapshot);settings=snapshot['settings']
        server_image=self.images['server']['immutableImage'];image=self.images['loadgen']['immutableImage']
        cap=self.cap;actual_control=self.control_container['imageID']
        harness_module.save(self.out/'gateway.json',{'requestTimeoutSeconds':cap,'sourceSHA':self.sha,'controlImageDigest':actual_control,'publicationIndexImage':server_image,'capturedUTC':harness_module.now(),'actualPod':self.control_pod,'controllerEvidence':harness_module.ref(self.out/'controller.json')})
        dataset={}
        for name in (harness_module.WORKLOADS[2],harness_module.WORKLOADS[1]):dataset[self.op['workloads'][name]['datasetRef']]=self.dataset(name,image)
        actual_flags=dataset['vqav2-val-subset']['cliFlags']
        harness_module.save(self.out/'producer.json',{'sourceSHA':self.images['loadgen']['sourceCommit'],'imageDigest':image,'cliFlags':actual_flags,'deploymentReceipt':self.pub['imagePublication']})
        binary=self.pub['kappaExecutable'];argv=[binary,'-mode','table','-scheduler',str(self.out/'scheduler.json'),'-workloads',','.join(harness_module.WORKLOADS),'-margin',str(self.n['mSeconds']*1000),'-window',str(settings['capacityWindowNanos']/1e9),'-windows',str(settings['capacityWindows']),'-passing',str(settings['capacityPassingWindows']),'-workers',str(settings['planSearchWorkers']),'-rows',str(self.out/'capacity.json')]
        with (self.out/'kappa.stdout').open('w') as stdout,(self.out/'kappa.stderr').open('w') as stderr:r=subprocess.run(argv,stdout=stdout,stderr=stderr,timeout=self.n['kappaSeconds'],env=dict(os.environ,GOMAXPROCS='6'))
        harness_module.save(self.out/'kappa-execution.json',{'at':harness_module.now(),'sourceSHA':self.sha,'argv':argv,'exitCode':r.returncode,'meaning':'Actual registered canonical UID/generation/SLO inputs; no hypothetical live UID'})
        if r.returncode:raise RuntimeError('Final actual-UID kappa calculation failed')
        c={**{k:self.op[k] for k in ('endpoint','caFile','tokenFile','kubeContext')},**{k:self.n[k] for k in ('g0Evidence','mSeconds','N','burstLowSeconds','timeboxes','smokeSeconds')}}
        c['collectorTokenFile']=self.op['collectorTokenFile']
        c['failureSelectSnapshotTTLSeconds']=self.n['failureSelectSnapshotTTLSeconds']
        c['poolShares'] = harness_module.pool_shares(self.n)
        c['approval']={**self.n['approval'],'sources':self.sources,'controlImageDigest':actual_control,'runtimeDigests':harness_module.load(PLAN)['frozenRuntimeSha256']}
        c['inputs']={k:self.pub[k] for k in ('d25','ranking','costs','bias')}
        c['inputs'].update({k:str(self.out/(k+'.json')) for k in ('scheduler','capacity','gateway','producer')})
        c['inputs']['capacityScheduler']=str(self.out/'scheduler.json')
        c['inputs']['powerObservation']=str(self.out/'power-observation.json')
        c['inputs']['evaluationPayload']=str(self.out/'evaluation-payload.json')
        c['collection']={k:self.op['collection'][k] for k in ('powerExecutable','powerReceipt')}
        for key in ('namespace','stages','seeds','arrivals','producerResources','producerShards','failureInjectionRunbook','tokenSafetyMarginSeconds','burstLowMaximumSeconds'):
            if key in self.n:c[key]=self.n[key]
        c['collection'].update(self.n['collection'])
        c['inputs'].update(self.n['inputs'])
        harness_module.save(c['inputs']['powerObservation'],{'schedulerSettings':{'powerObservation':harness_module.power_observation(c)}})
        c['workloads']=[]
        sys.path.insert(0,str(harness_module.REPO/'load-generator/src'));from chill_loadgen import cli
        producer=harness_module.module('evaluationprod_identity',HERE/'producer.py')
        payload={}
        for name in harness_module.WORKLOADS:
            w=self.op['workloads'][name];n=self.n['workloads'][name];actual=next(x for x in snapshot['state']['snapshot']['workloads'] if x['name']==name);observed=dataset[w['datasetRef']]
            filtered=[{'recordID':x['recordID'],'fields':{k:x['fields'][k] for k in w['fields']}} for x in observed['evaluation']]
            payload[name]=filtered
            if producer.identity(cli.load_records(Path(w['datasetDir']),w['fields']))!=filtered:raise RuntimeError('Recorded host evaluation differs from actual pinned carrier')
            job=template(self.op,name,image);path=self.out/('producer-template-'+name+'.json');harness_module.save(path,job)
            c['workloads'].append({**n,'name':name,'uid':actual['uid'],'generation':actual['generation'],'slo':actual['slo'],'datasetDir':w['datasetDir'],'fields':w['fields'],'profilingRecordIDs':[x['recordID'] for x in observed['profiling']],'jobTemplate':str(path),'gatewayCapSeconds':cap,'producerEndpoint':self.op['producer']['endpoint'],'producerTokenFile':self.op['producer']['tokenFile'],'producerCAFile':self.op['producer']['caFile'],'producerDatasetDir':self.op['producer']['datasetDir']})
        harness_module.save(c['inputs']['evaluationPayload'],{'payload':payload})
        baseline=snapshot['settings']['controls'];c['factors']=[]
        for name in self.factor_names:
            item=self.op['factorMenu'][name];controls=dict(baseline);controls[item['changedControl']]=item['value'];witness_path=self.out/('factor-witness-'+name+'.json');harness_module.save(witness_path,{**item['witness'],'sourceSHA':self.sha,'evidenceLevel':'Source-witnessed constraint and executable reset; observed factor effect remains unmeasured'});c['factors'].append({'name':name,'controls':controls,'witness':str(witness_path)})
        path=self.out/'assembled-config.json';harness_module.save(path,c);binding=harness_module.bind(path);destination=Path(self.release['bindingPath'])
        binding['sources']['phase6Numbers']=harness_module.ref(self.a.numbers)
        binding['sources']['operationalRecipe']=harness_module.ref(harness_module.OPERATIONAL_RECIPE)
        binding['sources']['imagePublication']=harness_module.ref(self.pub['imagePublication'])
        if destination.exists():raise RuntimeError('Frozen binding already exists; never overwrite')
        harness_module.save(destination,binding);return destination


def self_check_controller():
    """Offline cluster projection regression; no cluster reads or sleeps."""
    import tempfile
    from types import SimpleNamespace
    from unittest.mock import patch
    with tempfile.TemporaryDirectory() as temp:
        prepared=object.__new__(Prepare);prepared.out=Path(temp);prepared.tb={'setupSeconds':120}
        prepared.io=SimpleNamespace();prepared.http=object()
        pod={'uid':'server','ready':'True','containers':[{'name':'serving-entrypoint','imageID':'sha256:actual'}]}
        old={'uid':'deleted-server','ready':'True','containers':[{'name':'serving-entrypoint','imageID':'sha256:old'}]}
        pages=[[{'at':'2026-10-30T03:00:00Z','event':'deleted','pod':old}],
               [{'at':'2026-10-30T02:00:00Z','event':'observed','pod':old},
                {'at':'2026-10-30T02:01:00Z','event':'observed','pod':pod}]]
        def download(folder,io,path,target,deadline):
            with gzip.open(target,'wt') as output:
                for row in pages.pop(0):output.write(json.dumps(row)+'\n')
        prepared.api=lambda *args,**kwargs:({'kinds':[{'kind':'cluster','available':True}]},200)
        with patch.object(time,'sleep'),patch.object(harness_module,'module',return_value=SimpleNamespace(_download=download)):
            actual,container=prepared.controller({'indexDigest':'sha256:actual'})
        assert actual==pod and container['imageID']=='sha256:actual' and not pages


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('command',choices=['schema','self-check','prepare','execute']);p.add_argument('--numbers',type=Path);p.add_argument('--release',type=Path);p.add_argument('--out',type=Path);a=p.parse_args()
    try:
        if a.command=='self-check':
            self_check_controller();return 0
        if a.command=='schema':
            print(json.dumps({'numbers':['g0Evidence','mSeconds','N','burstLowSeconds','smokeSeconds','kappaSeconds','timeboxes','workloads','approval'],'g0Evidence':{'runID':'actual G0 verdict directory name','verdict':'absolute recorded g0-verdict.json path; gateway criteria are judged directly, aggregate and Smol SLO verdicts are reference only','binding':'absolute same-run run/binding.json path (120s alpha=1.2 G-check)','csCPULimitCores':'explicit frozen positive ChillServer CPU limit; recorded peak/limit must be <1'},'workloadNumbers':['slo.percentile','slo.targetLatencyNanos','R','deliverySeconds','socketSeconds','workers'],'optionalWorkloadNumbers':['lifetimeSeconds'],'timeboxes':harness_module.REQUIRED['timeboxes'],'approval':['sources'],'factorSelection':'N selects the frozen operational recipe factorOrder prefix; source/reset witnesses are already bound there, observed effect remains unmeasured','release':['executor=CampaignOps','command=evaluation-execute','mainInstructionUTC','sources','numbersPath','bindingPath'],'sources':{'server':'full Git SHA matching AFTER and server publication','loadgen':'full Git SHA matching loadgen publication sourceCommit','power':'full Git SHA or pinned repository-relative binary path matching actual power build receipt'},'autoInputs':str(PLAN)+'.evaluationInputPublication','operationalRecipe':str(harness_module.OPERATIONAL_RECIPE),'scientificResults':'UNMEASURED'},indent=2));return 0
        if any(x is None for x in (a.numbers,a.release,a.out)):raise ValueError('--numbers, --release and --out required for CPO preparation/execution')
        if any(not x.is_absolute() for x in (a.numbers,a.release,a.out)):raise ValueError('Absolute paths required')
        prepared=Prepare(a);binding=prepared.run()
        if a.command=='execute':
            common={'binding':binding,'release':a.release,'stage':'','mode':'full','row_tag':'','pre_state':None}
            smoke_args=argparse.Namespace(**common,command='start',out=a.out/'smoke-run');smoke=harness_module.CPO(smoke_args);smoke.start();smoke_args.command='smoke';smoke_args.mode='smoke';smoke_args.stage='SMOKE';smoke.run()
            result=harness_module.judge(binding,smoke_args.out/'smoke-SMOKE')
            if result['status']!='OPERATIONAL_SMOKE_COMPLETE':raise RuntimeError('Actual operational smoke incomplete; no evaluation result or campaign advance')
            smoke.finish();smoke.cleanup_jobs();harness_module.collection_manifest(smoke_args.out,binding)
            full=harness_module.CPO(argparse.Namespace(**common,command='campaign',out=a.out/'campaign-run'));full.campaign();harness_module.collection_manifest(a.out,binding)
        return 0
    except Exception as exc:
        if a.out:harness_module.save(a.out/'STOP.json',{'at':harness_module.now(),'status':'NOT_READY','error':str(exc),'scientificResults':'UNMEASURED_OR_PARTIAL; no old-source or invented-UID fallback'})
        print('NOT_READY: '+str(exc),file=sys.stderr);return 2


if __name__=='__main__':sys.exit(main())

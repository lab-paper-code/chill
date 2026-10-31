#!/usr/bin/env python3
"""EvaluationRun Job adapter. Only prepare resolves live scientific definitions.

Inputs are an evaluation-inputs.v1alpha1 payload manifest (evaluation_inputs.py),
LoadScenario pins, and frozen operational ConfigMap values. All mutable state is
inside the execution PVC. Result objects are immutable and always uploaded last.
"""
import argparse
import copy
import datetime as dt
import importlib.util
import json
import os
from pathlib import Path
import ssl
import subprocess
import re
import tempfile
import time
from types import SimpleNamespace, MethodType
import urllib.request

HERE=Path(__file__).resolve().parent
REPO=HERE.parent.parent
TOKEN='/var/run/secrets/kubernetes.io/serviceaccount/token'
CA='/etc/chill/ca/tls.crt'
STEPS=('prepare','finish','publish','factor','stage','judge','cleanup-jobs')


def now(): return dt.datetime.now(dt.timezone.utc).isoformat()
def load(path): return json.loads(Path(path).read_text())
def save(path,value):
    path=Path(path);path.parent.mkdir(parents=True,exist_ok=True)
    temporary=path.with_name(path.name+'.tmp');temporary.write_text(json.dumps(value,indent=2,allow_nan=False)+'\n');temporary.replace(path)
def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path);value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value);return value

AUTH=module('evaluation_auth',HERE/'auth.py')


class Store:
    def __init__(self): self.name=os.environ['CHILL_EVIDENCE_STORE']
    def transfer(self,operation,key,path,size=0):
        result=subprocess.run([str(REPO/'bin/evaluation-evidence'),'--operation',operation,'--key',key,'--file',str(path),'--size',str(size)],capture_output=True,text=True)
        if result.returncode == 3: raise FileNotFoundError(key)
        if result.returncode: raise RuntimeError('EvidenceStoreFailed: '+result.stderr.strip())
        return json.loads(result.stdout)
    def put(self,key,path): return self.transfer('put',key,path)
    def get(self,ref,path):
        if ref['store']!=self.name: raise ValueError('FrozenInputInvalid: foreign evidence store')
        return self.transfer('get',ref['key'],path,ref.get('size',0))


def api(config,path):
    request=urllib.request.Request(config['chillAPIURL'].rstrip('/')+path,headers={'Authorization':'Bearer '+Path(TOKEN).read_text().strip()})
    opener=urllib.request.build_opener(AUTH.NoRedirect(),urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=CA)))
    with opener.open(request,timeout=30) as response: return json.load(response)


def kubectl(namespace,*args):
    result=subprocess.run(['/usr/bin/kubectl','-n',namespace,*args,'-o','json'],capture_output=True,text=True,timeout=60)
    if result.returncode: raise RuntimeError('TargetUnavailable: '+result.stderr.strip())
    return json.loads(result.stdout)


def expand(value,inputs):
    if isinstance(value,dict): return {key:expand(child,inputs) for key,child in value.items()}
    if isinstance(value,list): return [expand(child,inputs) for child in value]
    if isinstance(value,str) and value.startswith('$files/'):
        relative=Path(value[7:])
        if relative.is_absolute() or '..' in relative.parts: raise ValueError('FrozenInputInvalid: input path traversal')
        return str(inputs/'files'/relative)
    return value


def environment(run,config,scientific,workloads):
    work=run.parent.parent
    env={'paths':{'repository':str(REPO),'ops':str(work),'campaign':str(run.parent),'d2State':str(work/'d2'),'inputs':str(work/'inputs')},
         'e5':{**scientific,'publishExecutable':str(REPO/'bin/e5-publish')},
         'd2':{'kubectl':['/usr/bin/kubectl'],'namespace':config['namespace'],'apiBase':config['chillAPIURL'],'caFile':CA,'tokenFile':TOKEN,'collectorTokenFile':TOKEN,'repoBin':str(REPO/'bin'),'jobTemplate':workloads[0]['jobTemplate'],'workloadUID':workloads[0]['uid'],'workloadName':workloads[0]['name']}}
    env['d2']['nodes']=scientific['nodes']
    path=work/'environment.json';save(path,env);os.environ['CHILL_EXPERIMENT_CONFIG']=str(path)
    return env


def stage_config(base,spec,scenario,execution,config,root):
    value=copy.deepcopy(base);operational=json.loads(config['e5Operational'])
    rules=module('evaluation_input_rules',HERE/'evaluation_inputs.py')
    timebox_keys={'setupSeconds','barrierSeconds','drainSeconds','flushSeconds','collectSeconds','apiSeconds','settleSeconds','finishResetWaitSeconds','powerDownSeconds','factorAckWaitSeconds','maxActuationSeconds','nativeCallerSeconds'}
    if set(operational.get('timeboxes',{}))-timebox_keys:
        raise ValueError('FrozenInputInvalid: unknown operational timebox fields')
    if set(operational)-(rules.OPERATIONAL|{'workloads','collection'}):
        raise ValueError('FrozenInputInvalid: unknown e5Operational fields')
    if set(operational.get('collection',{}))-{'maximumDurationSeconds'}:
        raise ValueError('FrozenInputInvalid: unknown operational collection fields')
    for workload in operational.get('workloads',{}).values():
        if set(workload)-rules.WORKLOAD_OPERATIONAL:
            raise ValueError('FrozenInputInvalid: unknown operational workload fields')
    value.update({key:child for key,child in operational.items() if key not in ('workloads','collection')})
    value.update(endpoint=config['chillAPIURL'],caFile=CA,tokenFile=TOKEN,collectorTokenFile=TOKEN,kubeContext='in-cluster',namespace=execution['namespace'])
    value['collection'].update(operational.get('collection',{}))
    value['collection'].update(hosts=str(root/'producer-hosts.json'),powerExecutable=str(REPO/'bin/observation-power-source-d39c9576'),powerReceipt=str(REPO/'bin/observation-power-source-d39c9576.receipt.json'))
    value['timeboxes'].update({key:float(config[key]) for key in ('finishResetWaitSeconds','factorAckWaitSeconds') if key in config})
    if 'drainTimeoutSeconds' in config: value['timeboxes']['drainSeconds']=float(config['drainTimeoutSeconds'])
    if value['approval']['sources']['power'] not in ('bin/observation-power-source-d39c9576','d39c9576f5b2f4ee6a48034e6b95eba28e43cd4e'):
        raise ValueError('IdentityMismatch: bundled power reducer source differs from image')
    value['approval']['sources']['power']='bin/observation-power-source-d39c9576'
    value['seeds']=scenario['seeds'];value['arrivals']=scenario['spec']['arrivalDistribution']
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9-]{0,39}',spec['row']):
        raise ValueError('FrozenInputInvalid: row requires a safe E5 row label')
    value['stages']=[{'id':spec['row'],'parts':[{'id':part['id'],'seconds':part['durationSeconds'],**{key:part[key] for key in ('rates','alpha') if key in part}} for part in scenario['spec']['parts']]}]
    value['generatorQuality']={'latenessToleranceNS':spec['judgment']['generatorQuality']['latenessToleranceNanos'],'maxExceedingFraction':spec['judgment']['generatorQuality']['maxExceedingFraction']}
    value['collection']['energyCoverage']=spec['judgment']['energyCoverage']
    aliases={'mobilenet':'image-classification-tight-slo','plc':'plc-anomaly','smolvlm2':'smolvlm2-vqa'}
    value['producerShards']={alias:spec['loadGenerator']['shards'][name] for alias,name in aliases.items()}
    resources=spec['loadGenerator']['resources'];value['producerResources']=copy.deepcopy(resources)
    for workload in value['workloads']:
        operation=operational['workloads'][workload['name']]
        workload.update({key:child for key,child in operation.items() if key!='podSpec'})
        template=load(workload['jobTemplate']);pod=copy.deepcopy(operation['podSpec'])
        for group in ('containers','initContainers'):
            images={container['name']:container['image'] for container in template['spec']['template']['spec'].get(group,[])}
            if set(images)!={container['name'] for container in pod.get(group,[])}:
                raise ValueError('FrozenInputInvalid: operational producer containers differ from frozen template')
            for container in pod.get(group,[]):
                if 'image' in container: raise ValueError('FrozenInputInvalid: operational template cannot override images')
                container['image']=images[container['name']]
        template['spec']['template']['spec']=pod
        destination=root/('producer-template-'+workload['name']+'.json');save(destination,template)
        workload['jobTemplate']=str(destination)
        pinned=next(w for w in scenario['workloads'] if w['name']==workload['name'])
        for key in ('uid','generation','slo'): workload[key]=pinned[key]
        workload['seed']=value['seeds'][workload['name']];workload['arrivals']=value['arrivals'][workload['name']]
    value['evaluation']={'execution':execution,'sourceSHA':spec['harness']['sourceSHA'],'jobActiveDeadlineSeconds':int(config['jobActiveDeadlineSeconds']),'minimumFreeBytes':int(config['minimumFreeBytes']),'stageFolder':str(root/'measured'),'mode':spec['mode'],'control':spec['control'],'resources':resources}
    value['evaluation']['traceOffsets']=scenario.get('traceOffsets',{})
    value['evaluation']['failureInjection']=spec.get('failureInjection',[])
    value['evaluation']['barrier']=spec['loadGenerator']['barrier']
    if value['approval']['sources']['loadgen']!=spec['loadGenerator']['sourceSHA']:
        raise ValueError('IdentityMismatch: load generator source differs from frozen producer receipt')
    for workload in value['workloads']:
        template=load(workload['jobTemplate'])
        if template['spec']['template']['spec']['containers'][0]['image']!=spec['loadGenerator']['image']:
            raise ValueError('IdentityMismatch: load generator image differs from frozen producer template')
    return value


def result_record(execution,step,started,status='Complete',reason='ProcedureComplete',measurement=None,artifacts=None,outputs=None):
    record={'schemaVersion':'evaluation-step.v1alpha1','runUID':execution['runUID'],'step':step,'execution':{'status':status,'reason':reason},'startedAt':started,'finishedAt':now(),'artifacts':artifacts or []}
    for key in ('stageUID','stageID'):
        if execution.get(key): record[key]=execution[key]
    if measurement: record['measurement']=measurement
    if outputs: record['outputs']=outputs
    if step in ('finish','cleanup-jobs'): record['cleanup']={'status':status,'reason':reason}
    return record


def verdict(judgment):
    status=judgment['status'];failure=judgment.get('failureScenarioEvidence') or {}
    if status.startswith('INVALID') or failure.get('status')=='INVALID': return {'verdict':'INVALID','reason':'GeneratorInvalid' if status=='INVALID_GENERATOR' else 'InjectionInvalid'}
    if status in ('NOT_READY','INCOMPLETE','UNKNOWN') or failure.get('status')=='UNMEASURED': return {'verdict':'NOT_EVALUATED','reason':'ObservationUnavailable'}
    return {'verdict':'FAIL' if status=='FAIL' else 'PASS','reason':'SLOFailed' if status=='FAIL' else 'MeasurementComplete'}


def exit_code(record):
    if record['execution']['status']!='Complete': return 1
    return 2 if record.get('measurement',{}).get('verdict') in ('INVALID','NOT_EVALUATED') else 0


class Harness:
    def __init__(self,run,specdir,store=None):
        self.run=run.resolve();self.spec=load(specdir/'spec.json');self.execution=load(specdir/'execution.json');self.config=load(specdir/'config.json')
        self.config['namespace']=self.execution['namespace'];self.store=store or Store();self.inputs=self.run.parent.parent/'inputs'
        self.scope='stages/'+self.execution['stageID'] if self.execution.get('stageID') else 'run'
        self.prefix='experiments/'+self.execution['runUID']+'/'+self.scope
        self.root=self.run/self.scope if self.scope!='run' else self.run
        self.e5=None
    def upload(self,path,key): return self.store.put(key,path)
    def emit(self,value,name,key=None):
        path=self.root/name;save(path,value);return self.upload(path,key or self.prefix+'/'+self.step+'/'+name)
    def prepare(self):
        usage=os.statvfs(self.run)
        if usage.f_bavail*usage.f_frsize<int(self.config['minimumFreeBytes']):
            raise ValueError('EvidenceCapacityInsufficient: workspace free bytes')
        bundle_module=module('evaluation_inputs',HERE/'evaluation_inputs.py');scenarios={};bases={}
        for template in self.spec['stages']:
            pin=template['loadScenarioRef'];identity=pin['uid']
            if identity in scenarios: continue
            obj=kubectl(self.execution['namespace'],'get','loadscenario',pin['name'])
            if obj['metadata']['uid']!=identity: raise ValueError('IdentityMismatch: LoadScenario UID')
            scenario=obj['spec'];folder=self.inputs/identity;folder.mkdir(parents=True,exist_ok=True)
            bundle_path=folder/'bundle.json';self.store.get(scenario['payloadManifestRef'],bundle_path)
            bundle=bundle_module.validate(load(bundle_path))
            for name,ref in bundle['files'].items():
                path=folder/'files'/name;path.parent.mkdir(parents=True,exist_ok=True);self.store.get(ref,path)
            for path in (folder/'files').rglob('*.json'): save(path,expand(load(path),folder))
            scientific=expand(bundle['environment'],folder);base=expand(bundle['config'],folder)
            workloads=[]
            for pin_workload in scenario['workloads']:
                view=api(self.config,'/api/v1/workloads/'+pin_workload['name']);applied=view.get('appliedDefinition') or {}
                actual={key:applied.get(key) for key in ('name','uid','generation','slo','model','preprocessor','dataset')}
                if any(actual.get(key)!=pin_workload[key] for key in ('name','uid','generation')): raise ValueError('IdentityMismatch: applied Workload '+pin_workload['name'])
                workloads.append(actual)
            seeds_path=folder/'seeds.json';self.store.get(scenario['seedRef'],seeds_path);seeds=load(seeds_path)
            if set(seeds)!=set(scenario['arrivalDistribution']): raise ValueError('FrozenInputInvalid: seed workload keys')
            traces={}
            for name,ref in scenario.get('traceRefs',{}).items():
                path=folder/('trace-'+name+'.json');self.store.get(ref,path);traces[name]=load(path)
            for field in ('rateBasisRef','kappaRef'):
                if scenario.get(field):
                    path=folder/(field+'.json');self.store.get(scenario[field],path)
                    actual=load(path)
                    if field=='kappaRef':
                        if actual!=load(base['inputs']['capacity']): raise ValueError('FrozenInputInvalid: kappaRef differs from supplied capacity rows')
                    else:
                        shares=base.get('poolShares',{'MNV':.45,'PLC':.1,'Smol':.45})
                        expected={w['name']:w['R']*share for w,share in zip(base['workloads'],(shares['MNV'],shares['PLC'],shares['Smol']))}
                        if actual!=expected: raise ValueError('FrozenInputInvalid: rateBasisRef differs from capacity-derived pool rates')
            scenarios[identity]={'ref':{'name':obj['metadata']['name'],'uid':identity},'spec':scenario,'workloads':workloads,'seeds':seeds,'bundle':bundle,'traceOffsets':traces}
            for workload in base['workloads']:
                actual=next(value for value in workloads if value['name']==workload['name'])
                workload.update({key:actual[key] for key in ('uid','generation','slo')})
            bases[identity]=(base,scientific)
        first=next(iter(bases.values()));environment(self.run,self.config,first[1],first[0]['workloads']);self.e5=module('evaluation_e5',HERE/'e5.py')
        self.run.mkdir(parents=True,exist_ok=True)
        initial={'state':api(self.config,'/api/v1/scheduler/state'),'settings':api(self.config,'/api/v1/scheduler/settings'),'at':now()};save(self.run/'initial.json',initial)
        if self.spec['harness']['sourceSHA']!=os.environ.get('CHILL_HARNESS_SOURCE_SHA'):
            raise ValueError('IdentityMismatch: harness source differs from image revision')
        settings=initial['settings'];snapshot=initial['state']['snapshot']
        current=api(self.config,'/api/v1/scheduler');current['settings']=copy.deepcopy(settings)
        scheduler_path=self.inputs/'current-scheduler.json';save(scheduler_path,current)
        power_path=self.inputs/'current-power-policy.json';save(power_path,{'schedulerSettings':{'powerObservation':settings['powerObservation']}})
        bindings={}
        for template in self.spec['stages']:
            stage={**{key:self.spec[key] for key in ('target','harness','loadGenerator','powerMeasurement','control','judgment','evidenceStore')},**template}
            base,scientific=bases[template['loadScenarioRef']['uid']]
            for key in scientific:
                left,right=scientific[key],first[1][key]
                if key in ('d25Manifest','operationalRecipe'): left,right=load(left),load(right)
                if left!=right: raise ValueError('FrozenInputInvalid: inconsistent equipment environments')
            root=self.run/'stages'/template['id'];root.mkdir(parents=True,exist_ok=True)
            self.hosts(root)
            config=stage_config(base,stage,scenarios[template['loadScenarioRef']['uid']],self.execution,self.config,root)
            targets=self.check_target(stage['target'])
            serving=[(component,container) for component in stage['target']['components'] for target in targets if target['name']==component['name'] for pod in target['pods'] for container in pod['containers'] if container['name']=='serving-entrypoint']
            if len(serving)!=1: raise ValueError('IdentityMismatch: exactly one serving-entrypoint identity required')
            component,container=serving[0]
            config['approval']['sources']['server']=component['sourceSHA'];config['approval']['controlImageDigest']=container['imageID']
            gateway=root/'current-gateway.json';save(gateway,{'requestTimeoutSeconds':settings['requestTimeout']/1e9,'sourceSHA':component['sourceSHA'],'controlImageDigest':container['imageID'],'capturedUTC':now(),'target':targets})
            config['inputs'].update(scheduler=str(scheduler_path),powerObservation=str(power_path),gateway=str(gateway))
            path=root/'prepared-config.json';save(path,config)
            binding=self.e5.bind(path)
            if binding['payload']!=scenarios[template['loadScenarioRef']['uid']]['bundle']['payload']:
                raise ValueError('FrozenInputInvalid: payload manifest differs from materialized dataset')
            if scientific['producerNode']!=self.config['sshNode']: raise ValueError('FrozenInputInvalid: SSH Node differs from producer binding')
            expected=stage['powerMeasurement']
            if {node['uid'] for node in expected['nodes']}!={row['node']['uid'] for row in snapshot['nodes'] if row['node']['name'] in scientific['nodes']}: raise ValueError('IdentityMismatch: power Nodes')
            meters={meter['id']:meter for meter in settings['meters']}
            for meter in expected['meters']:
                actual=meters.get(meter['id'])
                if not actual or json.loads(meter['sourceIdentity'])!=actual['source'] or actual['source']['metric']!=expected['series']:
                    raise ValueError('IdentityMismatch: configured power meter source or series')
            if settings['powerObservation']['sampling']['Interval']!=expected['sampling']['intervalNanos']:
                raise ValueError('IdentityMismatch: operating power sampling interval')
            bindings[template['id']]=binding
        usage=os.statvfs(self.run)
        if usage.f_bavail*usage.f_frsize<int(self.config['minimumFreeBytes']): raise ValueError('EvidenceCapacityInsufficient: workspace free bytes')
        snapshot={'schemaVersion':'evaluation-snapshot.v1alpha1','runUID':self.execution['runUID'],'runSpec':self.spec,'scenarios':scenarios,'environment':first[1],'bootstrapWorkload':first[0]['workloads'][0],'bindings':bindings,'initial':initial,'config':self.config}
        path=self.inputs/'snapshot.json';save(path,snapshot)
        ref=self.upload(path,'experiments/'+self.execution['runUID']+'/run/prepare/input-snapshot.json')
        self.outputs={'inputSnapshot':ref}
    def hosts(self,root):
        config=self.config
        argv=['ssh','-T','-i','/secrets/producer/ssh-privatekey','-o','IdentitiesOnly=yes','-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o','UserKnownHostsFile=/secrets/producer/known_hosts','-o','ConnectTimeout=5','-p',config['sshPort']]
        if config.get('sshHostKeyAlias'): argv+=['-o','HostKeyAlias='+config['sshHostKeyAlias']]
        argv+=[config['sshUser']+'@'+config['sshHost'],'python3','-u','-']
        save(root/'producer-hosts.json',[{'node':config['sshNode'],'ssh':argv}])
    def check_target(self,target):
        identities=[]
        for component in target['components']:
            resource=component['resource'];obj=kubectl(resource['namespace'],'get',resource['kind'],resource['name'])
            podspec=obj['spec'].get('template',{}).get('spec',obj['spec'])
            expected_digest=component['image'].split('@')[-1]
            repository=component['image'].split('@')[0]
            container_names={c['name'] for c in podspec['containers'] if re.sub(r':[^/:]+$','',c['image'].split('@')[0])==repository}
            labels=obj['metadata'].get('labels',{});annotations=obj['metadata'].get('annotations',{})
            source=labels.get('org.opencontainers.image.revision',annotations.get('org.opencontainers.image.revision'))
            if source is not None and source!=component['sourceSHA']: raise ValueError('IdentityMismatch: target source '+component['name'])
            pods=kubectl(resource['namespace'],'get','pods','-l',','.join(k+'='+v for k,v in obj['spec'].get('selector',{}).get('matchLabels',{}).items())) if obj['kind']!='Pod' else {'items':[obj]}
            current=[pod for pod in pods['items'] if not pod['metadata'].get('deletionTimestamp')]
            if not current: raise ValueError('IdentityMismatch: target image '+component['name']+' observed=[]')
            for pod in current:
                statuses=pod.get('status',{}).get('containerStatuses',[])
                observed={status['name']:status.get('imageID') for status in statuses if status['name'] in container_names}
                if not observed or any(not image or image.rsplit('://',1)[-1].split('@')[-1]!=expected_digest for image in observed.values()):
                    raise ValueError('IdentityMismatch: target image '+component['name']+' observed='+json.dumps(observed,sort_keys=True))
                if not statuses or any(not status.get('ready') or status.get('restartCount') or not status.get('imageID') for status in statuses) or not any(condition['type']=='Ready' and condition['status']=='True' for condition in pod.get('status',{}).get('conditions',[])):
                    raise RuntimeError('TargetUnavailable: target Pod not Ready or restarted')
            pods['items']=sorted(current,key=lambda pod:pod['metadata']['uid'])
            identities.append({'name':component['name'],'uid':obj['metadata']['uid'],'spec':obj['spec'],'pods':[{'uid':pod['metadata']['uid'],'containers':[{key:status.get(key) for key in ('name','imageID','restartCount')} for status in pod.get('status',{}).get('containerStatuses',[])]} for pod in pods['items']]})
        return identities
    def open_snapshot(self):
        path=self.inputs/'snapshot.json'
        if not path.exists(): self.store.get(self.spec['inputSnapshotRef'],path)
        self.snapshot=load(path)
        if self.snapshot.get('schemaVersion')!='evaluation-snapshot.v1alpha1' or self.snapshot.get('runUID')!=self.execution['runUID']:
            raise ValueError('FrozenInputInvalid: input snapshot execution identity')
        if self.execution.get('stageID'):
            template=next(stage for stage in self.snapshot['runSpec']['stages'] if stage['id']==self.execution['stageID'])
            expected={**{key:self.snapshot['runSpec'][key] for key in ('target','harness','loadGenerator','powerMeasurement','control','judgment','evidenceStore')},**template}
            for key,value in expected.items():
                if key not in ('id','startState') and self.spec.get(key)!=value:
                    raise ValueError('FrozenInputInvalid: child differs from frozen '+key)
            if self.spec['stageID']!=self.execution['stageID']:
                raise ValueError('FrozenInputInvalid: child stage ID differs from execution')
        environment(self.run,self.config,self.snapshot['environment'],[self.snapshot['bootstrapWorkload']]);self.e5=module('evaluation_e5',HERE/'e5.py')
    def controller(self):
        stage_id=self.execution.get('stageID',self.snapshot['runSpec']['stages'][0]['id'])
        binding=copy.deepcopy(self.snapshot['bindings'][stage_id]);config=binding['config']
        config['evaluation']['execution']=self.execution
        if 'stageID' not in self.execution: config['evaluation'].pop('stageFolder',None)
        path=self.root/'binding.json';save(path,binding)
        release=self.root/'release.json';save(release,{'executor':'EvaluationRun','execution':self.execution,'bindingPath':str(path),'mainInstructionUTC':self.snapshot['initial']['at']})
        args=SimpleNamespace(binding=path,release=release,out=self.root,mode='full',stage=self.spec.get('row',binding['schedule'][0]['id']),row_tag=self.execution.get('stageUID','').replace('-',''),pre_state=None,command=self.step)
        return self.e5.CPO(args,restore_control_on_error=False,go_callback=self.go)
    def go(self,value):
        value.update({key:self.execution[key] for key in ('runUID','stageUID','stageID')})
        path=self.root/'measured/go.json';save(path,value)
        ref=self.upload(path,self.prefix+'/go.json');print('EVALUATION_GO '+ref['store']+'/'+ref['key']+' '+str(ref['size']),flush=True)
    def factor(self):
        path=self.root/'start-state.json';self.store.get(self.spec['startState']['evidenceRef'],path)
        state=load(path)
        if not isinstance(state,dict) or 'settings' not in state or not ('state' in state or 'snapshot' in state):
            raise ValueError('FrozenInputInvalid: startState requires state and settings')
        controller=self.controller();controller.a.pre_state=path;controller.factor()
        self.emit(controller.state(),'restored-state.json')
    def stage(self):
        controller=self.controller();baseline=self.check_target(self.spec['target']);last=[0];first=[False]
        def monitor(instance,offset,folder):
            if time.monotonic()-last[0]>=30:
                instance.disk_boundary(self.execution['stageID'],'watch')
                if self.check_target(self.spec['target'])!=baseline: raise RuntimeError('IdentityMismatch: target restarted or deployment changed')
                last[0]=time.monotonic()
            phase_path=folder/'phase.json'
            if not first[0] and phase_path.exists():
                phase=load(phase_path)
                if phase.get('goUTC'):
                    scheduler=instance.api('GET','/api/v1/scheduler');decision=scheduler['state']['decision']
                    if instance.d.epoch(decision.get('at')) is not None and instance.d.epoch(decision['at'])>=instance.d.epoch(phase['goUTC']):
                        settings=decision.get('settings',{})
                        for key in ('scaleDownHeadroomFraction','minimumDemand'):
                            if settings.get(key)!=self.spec['control'][key]: raise RuntimeError('FrozenInputInvalid: first Decision '+key)
                        save(folder/'first-load-tick.json',scheduler);first[0]=True
            return offset
        controller.monitor=MethodType(monitor,controller)
        controller.run()
        phase=load(self.root/'measured/phase.json')
        parser=self.e5.module('evaluation_start_times',HERE/'e5-raw.py')._time
        starts=[parser(value['at']) for value in phase['starts'].values()]
        skew=max(starts)-min(starts) if starts else 0
        save(self.root/'measured/generator-barrier.json',{'startSkewNanos':skew,'maximumStartSkewNanos':self.spec['loadGenerator']['barrier']['maxStartSkewNanos'],'accepted':skew<=self.spec['loadGenerator']['barrier']['maxStartSkewNanos']})
        if not first[0] and self.spec['mode']!='fixed': raise RuntimeError('ObservationUnavailable: missing first load Decision')
        state={'state':controller.state(),'settings':controller.settings()};self.outputs={'startState':self.emit(state,'output-start-state.json')}
    def judge(self):
        controller_binding=self.root/'binding.json';folder=self.root/'measured'
        if self.spec.get('failureInjection'): self.injections(folder)
        self.measurement=verdict(self.e5.judge(controller_binding,folder))
        if not load(folder/'generator-barrier.json')['accepted']:
            self.measurement={'verdict':'INVALID','reason':'GeneratorInvalid'}
    def injections(self,folder):
        records=[]
        for plan in self.spec['failureInjection']:
            path=self.root/('injection-'+plan['id']+'.json')
            self.store.get({'store':self.store.name,'key':self.prefix+'/injections/'+plan['id']+'.json'},path)
            record=load(path)
            if any(record.get(key)!=self.execution[key] for key in ('runUID','stageUID','stageID')):
                raise ValueError('FrozenInputInvalid: injection evidence execution identity')
            records.append(record)
        self.emit({'plans':self.spec['failureInjection'],'records':records},'injection-records.json')
        events=[]
        for plan,original in zip(self.spec['failureInjection'],records):
            record=original['record'];path=self.root/('injection-'+plan['id']+'.json')
            events.append({'kind':plan['action'],'atUTC':record.get('requestedAt',record['scheduledAt']),'exitCode':0 if record.get('outcome')=='Complete' else 1,'podUID':record.get('podRef',{}).get('uid'),'nodeUID':record.get('nodeRef',{}).get('uid'),'commandRef':str(path),'responseRef':str(path)})
        save(folder/'failure-events.json',{'stage':self.spec['row'],'selectionPolicy':'evaluation-controller-v1','plans':self.spec['failureInjection'],'records':records,'events':events})
    def cleanup_jobs(self): self.controller().cleanup_jobs()
    def finish(self): self.controller().finish()
    def publish(self):
        tables=module('evaluation_tables',HERE/'tables.py');stages=[];results={};artifacts=[];procedures={};actual_specs={};stage_outcomes=[]
        not_started={'status':'Failed','reason':'NotStarted'}
        def execution_outcome(values):
            executions=[value.get('execution',not_started) for value in values]
            failures=[value for value in executions if value['status']!='Complete']
            return next((value for value in failures if value.get('reason')!='NotStarted'),failures[0] if failures else {'status':'Complete','reason':'ProcedureComplete'})
        publication_prefix='serving/experiments.v1alpha1/'+self.execution['runUID']+'/'
        for template in self.snapshot['runSpec']['stages']:
            sid=template['id'];scope=self.run/'stages'/sid;result_path=self.run/('publish-result-'+sid+'.json')
            try:
                self.store.get({'store':self.store.name,'key':'experiments/'+self.execution['runUID']+'/stages/'+sid+'/judge/result.json'},result_path)
                result=load(result_path)
            except FileNotFoundError:
                result={'execution':{'status':'Failed','reason':'NotStarted'},'measurement':{'verdict':'NOT_EVALUATED','reason':'NotStarted'},'artifacts':[]}
            results[sid]=result;artifacts.extend(result['artifacts'])
            procedures[sid]={}
            for step in ('factor','stage','judge','cleanup-jobs'):
                if step=='judge': procedures[sid][step]=result;continue
                path=self.run/('publish-'+sid+'-'+step+'.json')
                try:
                    self.store.get({'store':self.store.name,'key':'experiments/'+self.execution['runUID']+'/stages/'+sid+'/'+step+'/result.json'},path)
                    value=load(path);procedures[sid][step]=value;artifacts.extend(value['artifacts'])
                except FileNotFoundError:
                    procedures[sid][step]={'execution':{'status':'Failed','reason':'NotStarted'}}
            if (scope/'execution-spec.json').exists(): actual_specs[sid]=load(scope/'execution-spec.json')
            child=procedures[sid]
            injection_path=self.run/('publish-injections-'+sid+'.json')
            injection_ref=next((ref for ref in result['artifacts'] if ref['key'].endswith('/injection-records.json')),None)
            injection_evidence={}
            if injection_ref:
                self.store.get(injection_ref,injection_path);injection_evidence=load(injection_path)
            plans=injection_evidence.get('plans',template.get('failureInjection',[]))
            records={value['record']['id']:value['record'] for value in injection_evidence.get('records',[])}
            injections=[]
            for plan in plans:
                record=records.get(plan['id'],{})
                original=record.get('outcome','NotStarted')
                injection_outcome={'Complete':'Executed','Executed':'Executed','Cancelled':'Cancelled','NotStarted':'Cancelled'}.get(original,'Invalid')
                reason=record.get('reason') or original
                if original!=injection_outcome and original not in reason: reason=original+': '+reason
                injections.append({'id':plan['id'],'action':plan['action'],'outcome':injection_outcome,'reason':reason,'scheduledOffsetSeconds':plan['offsetSeconds'],'actualOffsetSeconds':record.get('actualOffsetSeconds')})
            cleanup=child['cleanup-jobs']
            stage_outcomes.append({'id':sid,'execution':execution_outcome(child.values()),'measurement':result.get('measurement',{'verdict':'NOT_EVALUATED','reason':'NotStarted'}),'cleanup':cleanup.get('cleanup',cleanup.get('execution',not_started)),'injections':injections})
            phase_path=scope/'measured/phase.json'
            if not phase_path.exists(): continue
            phase=load(phase_path);judgment=tables.sections(scope/'measured/judgment.json')
            if phase.get('startUTC') and phase.get('endUTC') and judgment:
                bound=self.snapshot['bindings'][sid];requests=tables.publication_requests(judgment)
                definitions={'workloads':self.snapshot['scenarios'][template['loadScenarioRef']['uid']]['workloads']}
                phase_source=next(({key:ref[key] for key in ('store','key')} for ref in artifacts if ref['key'].endswith('/stage/artifacts/measured/phase.json') and '/stages/'+sid+'/' in ref['key']),None)
                for name,row in requests.items():
                    metadata,reasons=self.e5.publication_metadata(bound,phase,name,definitions,{'store':self.store.name,'key':publication_prefix+'execution.json'},phase_source,row.get('actualGatewayIdentities',[]))
                    if row.get('offered') and not row.get('actualGatewayIdentities'):
                        metadata.update(workloadUID=None,definitionID=None,identitySource=None);reasons['identity']='Observed request identity absent'
                    row.update(metadata);row['missingReasons'].update(reasons)
                power=judgment.get('power') or {}
                kpis={'status':judgment['status'],'requests':requests,'wholePoolJoules':power.get('wholePoolJoules') if power.get('complete') else None,'activePoolJoules':power.get('activePoolJoules') if power.get('complete') else None,'power':{key:power.get(key) for key in ('complete','wholePoolJoules','activePoolJoules','energyCoverage','gapErrorBoundJoules')},'schedulerSettings':bound['schedulerSettings'],'limitations':bound['limitations']}
                kpis.update(self.e5.publication_measurements(bound,power))
                stages.append({'id':sid,'from':phase['startUTC'],'to':phase['endUTC'],'kpis':kpis})
        generated=now();start=min((stage['from'] for stage in stages),default=self.snapshot['initial']['at']);end=max((stage['to'] for stage in stages),default=generated)
        prefix='serving/experiments.v1alpha1/'+self.execution['runUID']+'/'
        for step in ('prepare','finish'):
            path=self.run/('publish-run-'+step+'.json')
            try:
                self.store.get({'store':self.store.name,'key':'experiments/'+self.execution['runUID']+'/run/'+step+'/result.json'},path)
                value=load(path);procedures[step]=value;artifacts.extend(value['artifacts'])
            except FileNotFoundError: procedures[step]={'execution':{'status':'Failed','reason':'NotStarted'}}
        execution_path=self.run/'publication-execution.json';save(execution_path,{'runUID':self.execution['runUID'],'results':results,'procedures':procedures,'stages':actual_specs,'frozenConfig':self.config})
        index_path=self.run/'publication-evidence-index.json';save(index_path,{'artifacts':artifacts})
        objects=[{'store':self.store.name,'key':prefix+name,'localPath':str(path)} for name,path in (('execution.json',execution_path),('evidence-index.json',index_path))]
        first_binding=next(iter(self.snapshot['bindings'].values()));save(self.run/'binding.json',first_binding)
        aggregated=self.e5.summarize(None,self.run,evaluation={'stages':stages,'results':results,'physicalRestoration':procedures.get('finish',{}).get('cleanup')})
        measurement=aggregated['status']
        scenarios=list(self.snapshot['scenarios'].values())
        definitions={workload['uid']:workload for scenario in scenarios for workload in scenario['workloads']}
        definition={'schema':'evaluation-definition.v1alpha1','run':{'name':self.execution['runName'],'uid':self.execution['runUID']},'spec':self.snapshot['runSpec'],'loadScenarios':[{'name':s['ref']['name'],'uid':s['ref']['uid'],'spec':s['spec']} for s in scenarios],'workloads':list(definitions.values())}
        finish=procedures['finish']
        outcome={'execution':execution_outcome([procedures['prepare'],finish]+[{'execution':stage['execution']} for stage in stage_outcomes]),'measurement':verdict(aggregated),'cleanup':finish.get('cleanup',finish.get('execution',not_started)),'stages':stage_outcomes}
        summary={'schemaVersion':'serving-experiment.v1alpha1','id':self.execution['runUID'],'from':start,'to':end,'status':measurement,'stages':stages,'sources':[{key:obj[key] for key in ('store','key')} for obj in objects],'definition':definition,'outcome':outcome}
        summary_path=self.run/'publication-summary.json';save(summary_path,summary)
        manifest=self.run/'publication-manifest.json';save(manifest,{'objects':objects,'summary':{'store':self.store.name,'key':prefix+'summary.json','localPath':str(summary_path)}})
        (self.run/'tables.txt').write_text(tables.render(self.run,REPO,evaluation=aggregated))
        private={'logicalStore':self.store.name,'bucket':os.environ['CHILL_EVIDENCE_BUCKET'],'endpoint':os.environ['CHILL_EVIDENCE_ENDPOINT'],'region':os.environ['CHILL_EVIDENCE_REGION'],'accessKeyID':os.environ['AWS_ACCESS_KEY_ID'],'secretAccessKey':os.environ['AWS_SECRET_ACCESS_KEY'],'tempDir':str(self.run.parent.parent/'.evidence-tmp')}
        Path(private['tempDir']).mkdir(mode=0o700,parents=True,exist_ok=True)
        with tempfile.TemporaryDirectory(prefix='evaluation-publish-',dir='/private') as directory:
            credentials=Path(directory)/'config.json';credentials.write_text(json.dumps(private));credentials.chmod(0o600)
            result=subprocess.run([str(REPO/'bin/e5-publish'),'--config',str(credentials),'--manifest',str(manifest)],capture_output=True,text=True)
            if result.returncode: raise RuntimeError('PublicationFailed: '+result.stderr.strip())
        self.outputs={'summary':{'store':self.store.name,'key':prefix+'summary.json','size':summary_path.stat().st_size}}
    def execute(self,step):
        self.step=step;self.outputs={};self.measurement=None;started=now();status='Complete';reason='ProcedureComplete';artifacts=[]
        before={path:(path.stat().st_mtime_ns,path.stat().st_size) for path in self.root.rglob('*') if path.is_file()}
        try:
            save(self.root/'execution-spec.json',self.spec)
            save(self.root/'execution.json',self.execution)
            if step!='prepare': self.open_snapshot()
            getattr(self,step.replace('-','_'))()
        except Exception as error:
            status='Failed';reason=str(error).split(':',1)[0] if ':' in str(error) else 'ProcedureFailed'
            if isinstance(error,(FileNotFoundError,KeyError)): reason='MissingInput'
            elif isinstance(error,json.JSONDecodeError): reason='FrozenInputInvalid'
            save(self.root/(step+'-error.json'),{'at':now(),'reason':reason,'error':str(error)})
        try:
            for path in sorted(self.root.rglob('*')):
                if not path.is_file() or path.is_symlink() or not path.stat().st_size or 'results' in path.relative_to(self.root).parts: continue
                if before.get(path)==(path.stat().st_mtime_ns,path.stat().st_size): continue
                relative=path.relative_to(self.root).as_posix()
                if self.scope=='run' and relative.startswith('stages/'): continue
                artifacts.append(self.upload(path,self.prefix+'/'+step+'/artifacts/'+relative))
            record=result_record(self.execution,step,started,status,reason,self.measurement,artifacts,self.outputs)
            path=self.run/'results'/self.scope/(step+'.json');save(path,record)
            self.upload(path,self.prefix+'/'+step+'/result.json')
            return exit_code(record)
        except Exception as error:
            print('EvidenceStoreFailed: '+str(error),flush=True);return 1


def main():
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('step',choices=STEPS);parser.add_argument('--run',type=Path,required=True);parser.add_argument('--spec',type=Path,required=True)
    args=parser.parse_args()
    try: return Harness(args.run,args.spec).execute(args.step)
    except Exception as error: print(str(error),flush=True);return 1

if __name__=='__main__': raise SystemExit(main())

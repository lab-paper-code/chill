#!/usr/bin/env python3
"""EvaluationRun Job adapter for frozen black-box declarations and Dataset carriers.

Operational ConfigMap values contain client budgets and credential projections,
never scientific host files. Immutable result objects are uploaded last.
"""
import argparse
import copy
import json
import os
from pathlib import Path
import subprocess
import re
import time
from types import SimpleNamespace, MethodType

import input_snapshot
import evidence_io
import judgment
import publication
import live

HERE=Path(__file__).resolve().parent
REPO=HERE.parent.parent
STEPS=('prepare','finish','publish','factor','stage','judge','cleanup-jobs')




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




def result_record(execution,step,started,status='Complete',reason='ProcedureComplete',measurement=None,artifacts=None,outputs=None):
    record={'schemaVersion':'evaluation-step.v1alpha1','failurePolicy':'v2','runUID':execution['runUID'],'step':step,'execution':{'status':status,'reason':reason},'startedAt':started,'finishedAt':input_snapshot.now(),'artifacts':artifacts or []}
    for key in ('stageUID','stageID'):
        if execution.get(key): record[key]=execution[key]
    if measurement: record['measurement']=measurement
    if outputs: record['outputs']=outputs
    if step in ('finish','cleanup-jobs'): record['cleanup']={'status':status,'reason':reason}
    return record




def exit_code(record):
    if record['execution']['status']!='Complete': return 1
    return 2 if record.get('measurement',{}).get('verdict') in ('INVALID','NOT_EVALUATED') else 0


class Harness:
    def __init__(self,run,specdir,store=None):
        self.run=run.resolve();self.spec=input_snapshot.load(specdir/'spec.json');self.execution=input_snapshot.load(specdir/'execution.json');self.config=input_snapshot.load(specdir/'config.json')
        self.config['namespace']=self.execution['namespace'];self.store=store or Store();self.inputs=self.run.parent.parent/'inputs'
        self.scope='stages/'+self.execution['stageID'] if self.execution.get('stageID') else 'run'
        self.prefix='experiments/'+self.execution['runUID']+'/'+self.scope
        self.root=self.run/self.scope if self.scope!='run' else self.run
        self.harness_module=None
        progress_path = self.root / 'stage-progress.json'
        self.progress_records = input_snapshot.load(progress_path) if progress_path.exists() else []
    def upload(self,path,key): return self.store.put(key,path)
    def emit(self,value,name,key=None):
        path=self.root/name;input_snapshot.save(path,value);return self.upload(path,key or self.prefix+'/'+self.step+'/'+name)
    def prepare(self):
        input_snapshot.prepare(self)
    def controller(self):
        stage_id=self.execution.get('stageID',self.snapshot['runSpec']['stages'][0]['id'])
        binding=copy.deepcopy(self.snapshot['bindings'][stage_id]);config=binding['config']
        config['evaluation']['execution']=self.execution
        if 'stageID' not in self.execution: config['evaluation'].pop('stageFolder',None)
        path=self.root/'binding.json';input_snapshot.save(path,binding)
        args=SimpleNamespace(binding=path,out=self.root,stage=self.spec.get('row',binding['schedule'][0]['id']))
        return self.harness_module.CPO(args,go_callback=self.go,provisional_callback=self.provisional,
                                       progress_callback=self.progress,progress_publish_callback=self.publish_progress)
    def go(self,value):
        value.update({key:self.execution[key] for key in ('runUID','stageUID','stageID')})
        path=self.root/'measured/go.json';input_snapshot.save(path,value)
        ref=self.upload(path,self.prefix+'/go.json');print('EVALUATION_GO '+ref['store']+'/'+ref['key']+' '+str(ref['size']),flush=True)
    def provisional(self, value):
        if not self.execution.get('stageName'):
            return
        self.status([{'op': 'add', 'path': '/status/provisionalD1', 'value': value}])
    def status(self, operations):
        patch = [{'op': 'test', 'path': '/metadata/uid', 'value': self.execution['stageUID']}, *operations]
        input_snapshot.kubectl(self.execution['namespace'], 'patch', 'evaluationstage', self.execution['stageName'],
                               '--subresource=status', '--type=json', '-p', json.dumps(patch))
    def progress(self, phase, at=None, **facts):
        record = {'phase': phase, 'at': at or input_snapshot.now(), **facts}
        self.progress_records.append(record)
        print('EVALUATION_PROGRESS ' + json.dumps(record), flush=True)
    def publish_progress(self):
        now = input_snapshot.now()
        try:
            input_snapshot.save(self.root / 'stage-progress.json', self.progress_records)
            # RFC 6902 add replaces object members and also works on the first publication.
            self.status([{'op': 'add', 'path': '/status/progress', 'value': self.progress_records},
                         {'op': 'add', 'path': '/status/lastProgressAt', 'value': now}])
        except Exception as error:
            print('EVALUATION_PROGRESS_ERROR ' + json.dumps({'at': now, 'error': str(error)}), flush=True)
    def factor(self):
        input_snapshot.check_fleet(self, self.spec)
        self.controller().factor()
    def stage(self):
        live.start_gate(self)
        controller = self.controller()
        input_snapshot.check_target(self, self.spec['target'])
        input_snapshot.check_fleet(self, self.spec)
        last = [0]
        def monitor(instance, offset, folder):
            instance.disk_boundary()
            if time.monotonic() - last[0] >= 30:
                targets = input_snapshot.check_target(self, self.spec['target'])
                path = folder / 'target-observations.json'
                history = input_snapshot.load(path) if path.exists() else []
                history.append({'at': input_snapshot.now(), 'targets': targets})
                input_snapshot.save(path, history)
                input_snapshot.check_fleet(self, self.spec)
                instance.observe_generators(folder)
                last[0] = time.monotonic()
            return offset
        controller.monitor = MethodType(monitor, controller)
        controller.run()
        phase = input_snapshot.load(self.root / 'measured/phase.json')
        starts = [evidence_io.timestamp_ns(value['at']) for value in phase['starts'].values()]
        skew = max(starts) - min(starts) if starts else 0
        input_snapshot.save(self.root / 'measured/generator-barrier.json',
             {'startSkewNanos': skew, 'maximumStartSkewNanos': self.spec['loadGenerator']['barrier']['maxStartSkewNanos'],
              'accepted': skew <= self.spec['loadGenerator']['barrier']['maxStartSkewNanos']})
    def judge(self):
        self.progress('judgeStarted')
        self.publish_progress()
        controller_binding=self.root/'binding.json';folder=self.root/'measured'
        if self.spec.get('failureInjection'): self.injections(folder)
        self.measurement=publication.verdict(judgment.judge(controller_binding,folder))
        if not input_snapshot.load(folder/'generator-barrier.json')['accepted']:
            self.measurement={'verdict':'INVALID','reason':'GeneratorInvalid'}
        self.progress('judgeEnded')
        self.publish_progress()
    def injections(self,folder):
        records=[]
        for plan in self.spec['failureInjection']:
            path=self.root/('injection-'+plan['id']+'.json')
            self.store.get({'store':self.store.name,'key':self.prefix+'/injections/'+plan['id']+'.json'},path)
            record=input_snapshot.load(path)
            if any(record.get(key)!=self.execution[key] for key in ('runUID','stageUID','stageID')):
                raise ValueError('FrozenInputInvalid: injection evidence execution identity')
            records.append(record)
        self.emit({'plans':self.spec['failureInjection'],'records':records},'injection-records.json')
        events=[]
        for plan,original in zip(self.spec['failureInjection'],records):
            record=original['record'];path=self.root/('injection-'+plan['id']+'.json')
            events.append({'kind':plan['action'],'atUTC':record.get('requestedAt',record['scheduledAt']),'exitCode':0 if record.get('outcome')=='Complete' else 1,'podUID':record.get('podRef',{}).get('uid'),'nodeUID':record.get('nodeRef',{}).get('uid'),'commandRef':str(path),'responseRef':str(path)})
        input_snapshot.save(folder/'failure-events.json',{'stage':self.spec['row'],'selectionPolicy':'evaluation-controller-v1','plans':self.spec['failureInjection'],'records':records,'events':events})
    def cleanup_jobs(self):
        controller=self.controller()
        try: controller.cleanup_jobs()
        finally: self.residuals=controller.residuals if hasattr(controller,'residuals') else []
    def finish(self):
        if self.execution.get('prepareCompleted') is False:
            self.reason='NoControlEffects'
            return
        controller=self.controller()
        try: controller.finish()
        finally: self.residuals=controller.residuals if hasattr(controller,'residuals') else []
    def publish(self):
        publication.publish(self)
    def execute(self,step):
        self.step=step;self.outputs={};self.measurement=None;self.residuals=[];self.reason='ProcedureComplete';started=input_snapshot.now();status='Complete';reason='ProcedureComplete';artifacts=[]
        before={path:(path.stat().st_mtime_ns,path.stat().st_size) for path in self.root.rglob('*') if path.is_file()}
        message=''
        try:
            input_snapshot.save(self.root/'execution-spec.json',self.spec)
            input_snapshot.save(self.root/'execution.json',self.execution)
            if step!='prepare' and not (step in ('finish','publish') and self.execution.get('prepareCompleted') is False): input_snapshot.open_snapshot(self)
            watcher = live.start_watcher(self, step) if step in ('stage', 'finish') else None
            try:
                getattr(self,step.replace('-','_'))()
            finally:
                if watcher is not None: live.stop_watcher(self, watcher)
            reason=self.reason
        except Exception as error:
            status='Failed';candidate=str(error).split(':',1)[0]
            reason=candidate if len(candidate)<=1024 and re.fullmatch(r'[A-Z][a-zA-Z0-9]*',candidate) else 'ProcedureFailed'
            if isinstance(error,(FileNotFoundError,KeyError)): reason='MissingInput'
            elif isinstance(error,json.JSONDecodeError): reason='FrozenInputInvalid'
            message=str(error)[:32768]
            input_snapshot.save(self.root/(step+'-error.json'),{'at':input_snapshot.now(),'reason':reason,'error':str(error)})
            print(step+' failed: '+reason+': '+str(error),flush=True)
        try:
            for path in sorted(self.root.rglob('*')):
                if not path.is_file() or path.is_symlink() or not path.stat().st_size or 'results' in path.relative_to(self.root).parts: continue
                if before.get(path)==(path.stat().st_mtime_ns,path.stat().st_size): continue
                relative=path.relative_to(self.root).as_posix()
                if self.scope=='run' and relative.startswith('stages/'): continue
                artifacts.append(self.upload(path,self.prefix+'/'+step+'/artifacts/'+relative))
            record=result_record(self.execution,step,started,status,reason,self.measurement,artifacts,self.outputs)
            if message: record['execution']['message']=message
            if self.residuals: record['residuals']=self.residuals
            path=self.run/'results'/self.scope/(step+'.json');input_snapshot.save(path,record)
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

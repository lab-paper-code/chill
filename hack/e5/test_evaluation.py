#!/usr/bin/env python3
"""Network-free adapter checks; optionally replay a copied historical raw stage.

python3 -B hack/e5/test_evaluation.py
python3 -B hack/e5/test_evaluation.py --offline-binding B --offline-stage S --environment E
"""
import ast
import copy
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import evaluation
import evaluation_inputs

NAMES=['image-classification-tight-slo','plc-anomaly','smolvlm2-vqa']


class AdapterTests(unittest.TestCase):
    @patch.object(evaluation,'save')
    @patch.object(evaluation,'load',return_value={'spec':{'template':{'spec':{'containers':[{'name':'load','image':'load@sha256:test'}]}}}})
    def test_envelope_and_operational_boundary(self,unused_load,unused_save):
        base={'collection':{},'approval':{'sources':{'loadgen':'a'*40,'power':'bin/observation-power-source-d39c9576'}},'workloads':[{'name':name,'R':1,'jobTemplate':'/work/template.json'} for name in NAMES]}
        spec={'row':'E5-5m','mode':'controls-disabled','harness':{'sourceSHA':'a'*40},'control':{'controls':{'membership':False}},'judgment':{'generatorQuality':{'latenessToleranceNanos':1,'maxExceedingFraction':0},'energyCoverage':{'maxGapFraction':.01,'maxSingleGapSeconds':30}},'loadGenerator':{'shards':dict.fromkeys(NAMES,2),'resources':{'requests':{'cpu':'1','memory':'1Gi'},'limits':{'cpu':'2','memory':'2Gi'}}}}
        spec['loadGenerator'].update(image='load@sha256:test',sourceSHA='a'*40,barrier={'minimumReadySamples':2,'maxStartSkewNanos':1000000000})
        scenario={'spec':{'arrivalDistribution':dict.fromkeys(NAMES,'fixed'),'parts':[{'id':'steady','durationSeconds':10,'rates':dict.fromkeys(NAMES,3)}]},'seeds':dict.fromkeys(NAMES,1),'workloads':[{'name':name,'uid':str(index),'generation':4,'slo':{'percentile':95,'targetLatencyNanos':100}} for index,name in enumerate(NAMES)]}
        execution={'runUID':'run','stageUID':'stage','stageID':'different-id','namespace':'test'}
        operational={'timeboxes':{'drainSeconds':20},'workloads':{name:{'podSpec':{'restartPolicy':'Never','containers':[{'name':'load'}]}} for name in NAMES},'collection':{'maximumDurationSeconds':100}}
        config={'e5Operational':json.dumps(operational),'chillAPIURL':'https://chill','jobActiveDeadlineSeconds':'120','minimumFreeBytes':'123'}
        value=evaluation.stage_config(base,spec,scenario,execution,config,Path('/work/campaign/run/stages/different-id'))
        self.assertEqual(value['stages'][0]['id'],'E5-5m')
        self.assertEqual(value['evaluation']['execution']['stageID'],'different-id')
        self.assertEqual(value['evaluation']['control'],spec['control'])
        self.assertEqual(value['producerShards'],{'mobilenet':2,'plc':2,'smolvlm2':2})
        self.assertEqual(value['collectorTokenFile'],evaluation.TOKEN)
        self.assertEqual(value['workloads'][0]['generation'],4)
        self.assertNotIn('evaluation',base)
        for forbidden in ('inputs','approval','stages','evaluation'):
            bad=copy.deepcopy(config);bad['e5Operational']=json.dumps(dict(operational,**{forbidden:{}}))
            with self.assertRaisesRegex(ValueError,'FrozenInputInvalid'): evaluation.stage_config(base,spec,scenario,execution,bad,Path('/work'))
    def test_result_and_exit_mapping(self):
        execution={'runUID':'run','stageUID':'stage','stageID':'id'}
        for status,expected in [('MEASURED_DESCRIPTIVE',0),('FAIL',0),('INVALID_GENERATOR',2),('NOT_READY',2)]:
            record=evaluation.result_record(execution,'judge','start',measurement=evaluation.verdict({'status':status}))
            self.assertEqual(evaluation.exit_code(record),expected)
            self.assertEqual(record['schemaVersion'],'evaluation-step.v1alpha1')
            self.assertEqual(record['stageUID'],'stage')
        self.assertEqual(evaluation.exit_code(evaluation.result_record(execution,'stage','start','Failed','RemoteEffectUnknown')),1)
    def test_error_does_not_restore_control(self):
        tree=ast.parse((Path(__file__).parent/'e5.py').read_text())
        cpo=next(node for node in tree.body if isinstance(node,ast.ClassDef) and node.name=='CPO')
        run=next(node for node in cpo.body if isinstance(node,ast.FunctionDef) and node.name=='run')
        guard=next(node for node in ast.walk(run) if isinstance(node,ast.If) and any(isinstance(part,ast.Constant) and part.value=='restore_control_on_error' for part in ast.walk(node.test)))
        calls=[];runner=SimpleNamespace(restore_control_on_error=False,settings=lambda:calls.append('GET'),put=lambda value:calls.append('PUT'))
        exec(compile(ast.fix_missing_locations(ast.Module(body=[guard],type_ignores=[])),'restore-guard','exec'),{'self':runner,'static':False})
        self.assertEqual(calls,[])
    def test_bundle_rejection(self):
        with self.assertRaisesRegex(ValueError,'MissingInput'): evaluation_inputs.validate({})
    def test_result_uploaded_last_on_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);specdir=root/'spec';specdir.mkdir()
            for name,value in [('spec',{}),('execution',{'runUID':'run','namespace':'test'}),('config',{})]: evaluation.save(specdir/(name+'.json'),value)
            keys=[]
            store=SimpleNamespace(name='store',put=lambda key,path:keys.append(key) or {'store':'store','key':key,'size':path.stat().st_size})
            harness=evaluation.Harness(root/'campaign/run',specdir,store)
            with patch.object(harness,'prepare',side_effect=ValueError('MissingInput: absent')):
                self.assertEqual(harness.execute('prepare'),1)
            self.assertEqual(keys[-1],'experiments/run/run/prepare/result.json')
            result=evaluation.load(root/'campaign/run/results/run/prepare.json')
            self.assertEqual(result['execution'],{'status':'Failed','reason':'MissingInput'})
    def test_rotating_token_budget_and_request_reads(self):
        from contextlib import nullcontext
        tree=ast.parse((Path(__file__).parent/'e5.py').read_text())
        function=next(node for node in tree.body if isinstance(node,ast.FunctionDef) and node.name=='token_preflight')
        auth=SimpleNamespace(check=lambda path,budget,**kw:{'requiredSeconds':budget},lifetime_budget=lambda *args:333)
        namespace={'module':lambda *args:auth,'HERE':Path('/fixture')}
        exec(compile(ast.fix_missing_locations(ast.Module(body=[function],type_ignores=[])),'token-budget','exec'),namespace)
        config={'evaluation':{'jobActiveDeadlineSeconds':7200},'timeboxes':{'apiSeconds':30},'tokenFile':'projected'}
        record=namespace['token_preflight'](config,[{'seconds':10000}])
        self.assertEqual(record['requiredSeconds'],90)
        self.assertEqual(record['jobActiveDeadlineSeconds'],7200)
        del config['evaluation']
        self.assertEqual(namespace['token_preflight'](config,[])['requiredSeconds'],333)
        tree=ast.parse((Path(__file__).parent/'d2/conduct_before.py').read_text())
        conductor=next(node for node in tree.body if isinstance(node,ast.ClassDef) and node.name=='Conductor')
        method=next(node for node in conductor.body if isinstance(node,ast.FunctionDef) and node.name=='api')
        with tempfile.TemporaryDirectory() as directory:
            token=Path(directory)/'token';observed=[]
            client=SimpleNamespace(record=lambda value:None,http=SimpleNamespace(open=lambda request,**kw:observed.append(request.get_header('Authorization')) or nullcontext(SimpleNamespace(status=200,read=lambda:b'{}'))))
            namespace={'Path':Path,'TOKEN':token,'BASE':'https://offline.invalid','urllib':evaluation.urllib,'json':json,'now':lambda:'now'}
            exec(compile(ast.fix_missing_locations(ast.Module(body=[method],type_ignores=[])),'request-token','exec'),namespace)
            for value in ('first','rotated'):
                token.write_text(value);namespace['api'](client,'GET','/state')
            self.assertEqual(observed,['Bearer first','Bearer rotated'])
    def test_capacity_defaults_only_when_absent(self):
        tree=ast.parse((Path(__file__).parent/'d2/bind_before.py').read_text())
        nodes=[node for node in tree.body if isinstance(node,ast.FunctionDef) and node.name=='capacity_settings' or isinstance(node,ast.Assign) and any(isinstance(target,ast.Name) and target.id=='CAPACITY_DEFAULTS' for target in node.targets)]
        namespace={};exec(compile(ast.fix_missing_locations(ast.Module(body=nodes,type_ignores=[])),'capacity-defaults','exec'),namespace)
        self.assertEqual(namespace['capacity_settings']({}),{'capacityGridSteps':20,'capacityBisectionWidth':.005,'capacityTailSamples':100})
        original={'capacityGridSteps':4,'capacityBisectionWidth':None}
        values=namespace['capacity_settings'](original)
        self.assertEqual(values['capacityGridSteps'],4)
        self.assertIsNone(values['capacityBisectionWidth'])
        self.assertEqual(original,{'capacityGridSteps':4,'capacityBisectionWidth':None})
    def test_native_guard_excludes_only_exact_factor_job(self):
        tree=ast.parse((Path(__file__).parent/'d2/native_mode_request.py').read_text())
        function=next(node for node in tree.body if isinstance(node,ast.FunctionDef) and node.name=='active_jobs')
        namespace={};exec(compile(ast.fix_missing_locations(ast.Module(body=[function],type_ignores=[])),'native-jobs','exec'),namespace)
        current={'name':'factor','uid':'job','runUID':'run','stageUID':'stage','stageID':'id'}
        job={'metadata':{'name':'factor','uid':'job','labels':{'edge.dacs.io/evaluation-run-uid':'run','edge.dacs.io/evaluation-stage':'id','edge.dacs.io/evaluation-step':'factor'},'ownerReferences':[{'kind':'EvaluationStage','uid':'stage','controller':True}]},'status':{'active':1}}
        other={'metadata':{'name':'other','uid':'other'},'status':{'active':1}}
        self.assertEqual(namespace['active_jobs']([job,other],current),[other])
        self.assertEqual(namespace['active_jobs']([job]),[job])
        self.assertEqual(namespace['active_jobs']([job],current),[])
        changed=copy.deepcopy(job);changed['metadata']['uid']='replacement'
        with self.assertRaises(RuntimeError): namespace['active_jobs']([changed],current)
        changed=copy.deepcopy(job);changed['metadata']['labels']['edge.dacs.io/evaluation-step']='stage'
        with self.assertRaises(RuntimeError): namespace['active_jobs']([changed],current)
    def test_zero_load_go_precedes_decision_confirmation(self):
        import contextlib
        import io
        from types import MethodType
        tree=ast.parse((Path(__file__).parent/'e5.py').read_text())
        cpo=next(node for node in tree.body if isinstance(node,ast.ClassDef) and node.name=='CPO')
        run=next(node for node in cpo.body if isinstance(node,ast.FunctionDef) and node.name=='run')
        idle=next(node for node in ast.walk(run) if isinstance(node,ast.If) and isinstance(node.test,ast.Name) and node.test.id=='released')
        record=next(node for node in cpo.body if isinstance(node,ast.FunctionDef) and node.name=='record_go')
        namespace={'save':evaluation.save};exec(compile(ast.fix_missing_locations(ast.Module(body=[record],type_ignores=[])),'record-go','exec'),namespace)
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);folder=root/'measured';folder.mkdir();control={'scaleDownHeadroomFraction':.25,'minimumDemand':{}}
            harness=object.__new__(evaluation.Harness);harness.root=root;harness.execution={'runUID':'run','stageUID':'stage','stageID':'idle'};harness.prefix='experiments/run/stages/idle';harness.step='stage'
            harness.store=SimpleNamespace(put=lambda key,path:{'store':'store','key':key,'size':path.stat().st_size})
            harness.spec={'target':{},'control':control,'mode':'autonomous','loadGenerator':{'barrier':{'maxStartSkewNanos':0}}}
            harness.e5=SimpleNamespace(module=lambda *args:SimpleNamespace(_time=lambda value:0))
            controller=SimpleNamespace(c={'evaluation':{'enabled':True}},ws={},go_callback=harness.go,authorize_native_modes=lambda:None,put=lambda settings:settings,disk_boundary=lambda *args:None,d=SimpleNamespace(epoch=lambda value:0),api=lambda *args:{'state':{'decision':{'at':'after-GO','settings':control}}},state=lambda:{'snapshot':{}},settings=lambda:control)
            controller.record_go=MethodType(namespace['record_go'],controller)
            def run_idle():
                phase={'starts':{},'status':'RUNNING'}
                scope={'self':controller,'released':True,'targeted':True,'restored':{'fullSettings':{'autonomousWorkloads':['w']}},'phase':phase,'folder':folder,'jobs':[],'pods':{},'st':{'seconds':1},'now':lambda:'2026-11-01T00:00:00Z','time':SimpleNamespace(monotonic=lambda:0),'save':evaluation.save}
                exec(compile(ast.fix_missing_locations(ast.Module(body=[idle],type_ignores=[])),'idle-go','exec'),scope)
                controller.monitor(0,folder)
            controller.run=run_idle
            with patch.object(harness,'controller',return_value=controller),patch.object(harness,'check_target',return_value=[]),contextlib.redirect_stdout(io.StringIO()) as output:
                harness.stage()
            self.assertIn('EVALUATION_GO store/experiments/run/stages/idle/go.json ',output.getvalue())
            self.assertEqual(evaluation.load(folder/'go.json')['producerRefs'],[])
            self.assertTrue((folder/'first-load-tick.json').exists())
    def test_summary_aggregates_memory_without_input_reads(self):
        from collections import Counter
        tree=ast.parse((Path(__file__).parent/'e5.py').read_text())
        function=next(node for node in tree.body if isinstance(node,ast.FunctionDef) and node.name=='summarize')
        def unexpected(*args): raise AssertionError('Summary reread its inputs')
        namespace={'load_binding':unexpected,'load':unexpected,'Counter':Counter,'now':lambda:'now','save':evaluation.save}
        exec(compile(ast.fix_missing_locations(ast.Module(body=[function],type_ignores=[])),'memory-summary','exec'),namespace)
        values={'stages':[{'id':'id','kpis':{'status':'FAIL'}}],'results':{'id':{'measurement':{'verdict':'FAIL'}}},'physicalRestoration':{'status':'Complete'}}
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);summary=namespace['summarize'](None,root,evaluation=values)
            self.assertEqual(summary['status'],'FAIL')
            self.assertEqual(summary['rowJudgmentCounts'],{'FAIL':1})
            self.assertEqual(summary['physicalRestoration'],{'status':'Complete'})
            self.assertFalse((root/'evaluation-results.json').exists())
            self.assertNotIn('status',values)
    def test_evaluation_rejects_unused_legacy_allowances(self):
        for operation in ({'failureSelectSnapshotTTLSeconds':15},{'timeboxes':{'nativeFutureAllowanceSeconds':15}}):
            with self.assertRaisesRegex(ValueError,'FrozenInputInvalid'):
                evaluation.stage_config({}, {}, {}, {}, {'e5Operational':json.dumps(operation)},Path('/work'))
        self.assertNotIn('failureSelectSnapshotTTLSeconds',evaluation_inputs.OPERATIONAL)
    def test_publish_outcome_failed_stage_and_unperformed_injection(self):
        with tempfile.TemporaryDirectory() as directory:
            run=Path(directory)/'campaign/run';run.mkdir(parents=True)
            harness=object.__new__(evaluation.Harness);harness.run=run;harness.config={}
            harness.execution={'runUID':'run','runName':'name'}
            plans=[{'id':'done','action':'delete-serving-pod','offsetSeconds':10},{'id':'missed','action':'uncordon','offsetSeconds':20}]
            harness.snapshot={'runSpec':{'stages':[{'id':'failed','failureInjection':plans},{'id':'never','failureInjection':plans[:1]}]},'initial':{'at':'start'},'bindings':{'failed':{}},'scenarios':{}}
            complete={'execution':{'status':'Complete','reason':'ProcedureComplete'},'artifacts':[]}
            evidence={'experiments/run/run/prepare/result.json':complete,'experiments/run/run/finish/result.json':dict(complete,cleanup={'status':'Failed','reason':'RestorationFailed'})}
            for step in ('factor','stage','judge','cleanup-jobs'):
                evidence['experiments/run/stages/failed/'+step+'/result.json']=copy.deepcopy(complete)
            evidence['experiments/run/stages/failed/stage/result.json']['execution']={'status':'Failed','reason':'TargetUnavailable'}
            judge=evidence['experiments/run/stages/failed/judge/result.json']
            judge['measurement']={'verdict':'INVALID','reason':'InjectionInvalid'}
            judge['artifacts']=[{'store':'store','key':'judge/artifacts/injection-records.json'}]
            evidence['judge/artifacts/injection-records.json']={'plans':plans,'records':[{'record':{'id':'done','outcome':'Complete','actualOffsetSeconds':10.5}},{'record':{'id':'missed','outcome':'INVALID','reason':'InjectionInvalid'}}]}
            def get(ref,path):
                if ref['key'] not in evidence: raise FileNotFoundError(ref['key'])
                evaluation.save(path,evidence[ref['key']])
            harness.store=SimpleNamespace(name='store',get=get)
            harness.e5=SimpleNamespace(summarize=lambda *args,**kw:{'status':'INVALID'})
            tables=SimpleNamespace(render=lambda *args,**kw:'table')
            env=dict.fromkeys(('CHILL_EVIDENCE_BUCKET','CHILL_EVIDENCE_ENDPOINT','CHILL_EVIDENCE_REGION','AWS_ACCESS_KEY_ID','AWS_SECRET_ACCESS_KEY'),'test')
            temporary=evaluation.tempfile.TemporaryDirectory
            with patch.object(evaluation,'module',return_value=tables),patch.dict(os.environ,env),patch.object(evaluation.tempfile,'TemporaryDirectory',side_effect=lambda **kw:temporary(dir=run)),patch.object(evaluation.subprocess,'run',return_value=SimpleNamespace(returncode=0)):
                harness.publish()
            summary=evaluation.load(run/'publication-summary.json');outcome=summary['outcome']
            self.assertEqual(summary['definition']['spec'],harness.snapshot['runSpec'])
            self.assertEqual(summary['stages'],[])
            self.assertEqual(outcome['execution'],{'status':'Failed','reason':'TargetUnavailable'})
            self.assertEqual(outcome['measurement']['verdict'],'INVALID')
            self.assertEqual(outcome['cleanup'],{'status':'Failed','reason':'RestorationFailed'})
            failed,never=outcome['stages']
            self.assertEqual(failed['id'],'failed')
            self.assertEqual(failed['measurement'],judge['measurement'])
            self.assertEqual(failed['injections'][0]['actualOffsetSeconds'],10.5)
            self.assertEqual(failed['injections'][1],{'id':'missed','action':'uncordon','outcome':'Invalid','reason':'INVALID: InjectionInvalid','scheduledOffsetSeconds':20,'actualOffsetSeconds':None})
            self.assertEqual(never['execution']['reason'],'NotStarted')
            self.assertEqual(never['measurement'],{'verdict':'NOT_EVALUATED','reason':'NotStarted'})
            self.assertEqual(never['cleanup']['reason'],'NotStarted')
            self.assertEqual(never['injections'][0]['outcome'],'Cancelled')
            self.assertEqual(never['injections'][0]['reason'],'NotStarted')
            self.assertIsNone(never['injections'][0]['actualOffsetSeconds'])
            self.assertEqual(failed['injections'][0]['outcome'],'Executed')
            self.assertEqual(failed['injections'][0]['reason'],'Complete')
    def test_target_identity_uses_every_running_image_digest(self):
        digest='sha256:'+'a'*64
        target={'components':[{'name':'server','image':'registry:5000/server@'+digest,'sourceSHA':'source','resource':{'namespace':'test','kind':'Deployment','name':'server'}}]}
        deployment={'kind':'Deployment','metadata':{'uid':'deployment'},'spec':{'selector':{'matchLabels':{'app':'server'}},'template':{'spec':{'containers':[{'name':'server','image':'registry:5000/server:live'}]}}}}
        pod={'metadata':{'uid':'pod'},'status':{'conditions':[{'type':'Ready','status':'True'}],'containerStatuses':[{'name':'server','ready':True,'restartCount':0,'imageID':'docker-pullable://registry:5000/server@'+digest}]}}
        harness=object.__new__(evaluation.Harness)
        for images in ([digest,digest],[],['sha256:'+'b'*64],[digest,'sha256:'+'b'*64]):
            pods=[]
            for index,image in enumerate(images):
                value=copy.deepcopy(pod);value['metadata']['uid']=str(index);value['status']['containerStatuses'][0]['imageID']='containerd://'+image;pods.append(value)
            with patch.object(evaluation,'kubectl',side_effect=[deployment,{'items':pods}]) as request:
                if images==[digest,digest]:
                    self.assertEqual(len(harness.check_target(target)[0]['pods']),2)
                else:
                    with self.assertRaisesRegex(ValueError,'IdentityMismatch:.*observed='):
                        harness.check_target(target)
                self.assertEqual(request.call_args.args,('test','get','pods','-l','app=server'))


def offline(binding,stage,environment):
    original=evaluation.load(environment);campaign=Path(original['paths']['campaign'])
    tables=evaluation.module('offline_tables',Path(__file__).parent/'tables.py')
    expected=tables.sections(stage/'judgment.json')
    with tempfile.TemporaryDirectory(prefix='evaluation-offline-',dir=campaign) as directory:
        target=Path(directory)/stage.name;shutil.copytree(stage,target)
        def rebase(value):
            if isinstance(value,dict): return {key:rebase(child) for key,child in value.items()}
            if isinstance(value,list): return [rebase(child) for child in value]
            if isinstance(value,str) and value.startswith(str(stage)+'/'): return str(target)+value[len(str(stage)):]
            return value
        index=target/'raw-source-index.json';evaluation.save(index,rebase(evaluation.load(index)))
        os.environ['CHILL_EXPERIMENT_CONFIG']=str(environment)
        harness=evaluation.module('offline_e5',Path(__file__).parent/'e5.py')
        result=harness.judge(binding,target)
        selfields=('offered','outcomes','D1','gatewayCompletedLatencyNS','completedLatencyNS','deadlineSuccessFraction','issuanceFidelity')
        assert result['status']==expected['status'],(result['status'],expected['status'],result.get('notReadyReasons'))
        for name,metrics in expected['requests'].items():
            for key in selfields:
                assert result['requests'][name].get(key)==metrics.get(key),(name,key)
        for key in ('complete','wholePoolJoules','activePoolJoules','energyCoverage'):
            assert result['power'].get(key)==expected['power'].get(key),key
    print('PASS copied historical raw judge: status, offered/outcomes, D1, latency, generator quality and power coverage unchanged')


def offline_bundle(directory):
    bundle=evaluation_inputs.validate(evaluation.load(directory/'bundle.json'))
    with tempfile.TemporaryDirectory(prefix='evaluation-envelope-') as temporary:
        work=Path(temporary);inputs=work/'inputs';shutil.copytree(directory/'files',inputs/'files')
        for path in (inputs/'files').rglob('*.json'): evaluation.save(path,evaluation.expand(evaluation.load(path),inputs))
        base=evaluation.expand(bundle['config'],inputs);scientific=evaluation.expand(bundle['environment'],inputs)
        operational=evaluation.load(directory/'config-values.json')['e5Operational']
        resource={'cpu':'2','memory':'1Gi'}
        scheduler=evaluation.load(base['inputs']['scheduler']);control={key:scheduler['settings'][key] for key in ('controls','minimumDemand','scaleDownHeadroomFraction')}
        spec={'row':'E5-5m','mode':'autonomous','control':control,'harness':{'sourceSHA':'a'*40},'loadGenerator':{'image':evaluation.load(base['workloads'][0]['jobTemplate'])['spec']['template']['spec']['containers'][0]['image'],'sourceSHA':base['approval']['sources']['loadgen'],'resources':{'requests':resource,'limits':resource},'shards':{name:1 for name in NAMES},'barrier':{'minimumReadySamples':2,'maxStartSkewNanos':1000000000}},'judgment':{'generatorQuality':{'latenessToleranceNanos':50000000,'maxExceedingFraction':.01},'energyCoverage':{'maxGapFraction':.01,'maxSingleGapSeconds':30}}}
        scenario={'spec':{'arrivalDistribution':dict.fromkeys(NAMES,'poisson'),'parts':[{'id':'one','durationSeconds':60,'rates':dict.fromkeys(NAMES,1)}]},'seeds':evaluation.load(directory/'seeds.json'),'workloads':base['workloads']}
        execution={'runUID':'offline','namespace':'test'};config={'namespace':'test','e5Operational':operational,'chillAPIURL':'https://offline.invalid','minimumFreeBytes':'1','jobActiveDeadlineSeconds':'7200'}
        run=work/'campaign/run';root=run/'stages/offline';root.mkdir(parents=True)
        evaluation.save(root/'producer-hosts.json',[{'node':scientific['producerNode'],'ssh':['ssh','python3','-u','-']}])
        evaluation.environment(run,config,scientific,base['workloads'])
        harness=evaluation.module('offline_bundle_e5',Path(__file__).parent/'e5.py')
        assembled=evaluation.stage_config(base,spec,scenario,execution,config,root);path=root/'config.json';evaluation.save(path,assembled)
        gateway=evaluation.load(assembled['inputs']['gateway'])
        assembled['approval']['sources']['server']=gateway['sourceSHA']
        assembled['approval']['controlImageDigest']=gateway['controlImageDigest']
        evaluation.save(path,assembled)
        with patch.object(harness,'token_preflight',return_value={'authentication':'not exercised; offline transformation only'}):
            bound=harness.bind(path)
        assert bound['status']=='BOUND_NOT_MEASURED' and bound['schedule'][0]['id']=='E5-5m'
        assert bound['schedule'][0]['rates']==dict.fromkeys(NAMES,1)
    print('PASS recorded bundle materialization and existing e5.bind scientific gates; authentication deliberately not exercised')


if __name__=='__main__':
    if '--offline-bundle' in sys.argv:
        offline_bundle(Path(sys.argv[sys.argv.index('--offline-bundle')+1]))
    elif '--offline-binding' in sys.argv:
        import argparse
        parser=argparse.ArgumentParser()
        for flag in ('offline-binding','offline-stage','environment'): parser.add_argument('--'+flag,type=Path,required=True)
        args=parser.parse_args();offline(args.offline_binding,args.offline_stage,args.environment)
    else: unittest.main()

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
    def test_sections_and_bundle_rejection(self):
        tables=evaluation.module('evaluation_tables_test',Path(__file__).parent/'tables.py')
        with tempfile.TemporaryDirectory() as directory:
            path=Path(directory)/'judgment.json';evaluation.save(path,{'requests':{'w':{'offered':1}},'Tick':[{'large':'ignored'}],'power':{},'status':'FAIL'})
            self.assertEqual(tables.sections(path)['status'],'FAIL')
            self.assertNotIn('Tick',tables.sections(path))
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

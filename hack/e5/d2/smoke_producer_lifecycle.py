#!/usr/bin/env python3
"""Bounded local lifecycle fixtures; no Kubernetes or producer execution."""
import copy
import ast
import importlib.util
import json
from pathlib import Path
import tempfile
import os
from types import SimpleNamespace
ROOT=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('d2_producer_lifecycle_check',ROOT/'conduct_before.py')
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
clock=[0.0];original_monotonic=m.time.monotonic;original_sleep=m.time.sleep
m.time.monotonic=lambda:clock[0]
m.time.sleep=lambda seconds:clock.__setitem__(0,clock[0]+seconds)
try:
    with tempfile.TemporaryDirectory(prefix='d2-producer-lifecycle-') as temporary:
        folder=Path(temporary);driver=object.__new__(m.Conductor);driver.phase_out=folder
        pod={'metadata':{'name':'fixture-pod','uid':'fixture-pod-uid','ownerReferences':[{'kind':'Job','uid':'fixture-job-uid'}]},'status':{'phase':'Pending','containerStatuses':[{'name':'load','state':{'waiting':{'reason':'PodInitializing'}}}]}}
        running=copy.deepcopy(pod);running['status']={'phase':'Running','containerStatuses':[{'name':'load','state':{'running':{'startedAt':'2026-10-24T18:00:00Z'}}}]}
        responses=iter([{'items':[pod]},{'items':[running]}]);calls=[]
        driver.k=lambda argv,**kwargs:calls.append(argv) or next(responses)
        assert driver.wait_load_container(('fixture-job','fixture-job-uid'),folder,10)['metadata']['uid']=='fixture-pod-uid'
        assert len(calls)==2 and clock[0]==1
        pending={'metadata':{'uid':'fixture-job-uid'},'status':{}}
        complete=copy.deepcopy(pending);complete['status']['conditions']=[{'type':'Complete','status':'True'}]
        responses=iter([pending,complete]);calls.clear()
        assert driver.wait_job_complete(('fixture-job','fixture-job-uid'),folder,10)['status']['conditions'][0]['type']=='Complete'
        assert len(calls)==2
        calls.clear()
        def kubernetes_fixture(argv,stdin=None,timeout=None):
            calls.append((argv,stdin))
            if argv[:2]==['get','job']:return {'metadata':{'uid':'fixture-job-uid','resourceVersion':'7'}}
            if argv[0]=='delete':
                assert argv==['delete','--raw','/apis/batch/v1/namespaces/chill-system/jobs/fixture-job','-f','-']
                body=json.loads(stdin)
                assert body['preconditions']=={'uid':'fixture-job-uid','resourceVersion':'7'} and body['propagationPolicy']=='Foreground'
                return {'kind':'Status','status':'Success'}
            return {'items':[]}
        driver.k=kubernetes_fixture;driver.remove_job('fixture-job','fixture-job-uid')
        assert sum(argv[0]=='delete' for argv,_ in calls)==1
        calls.clear();driver.k=lambda argv,**kwargs:calls.append(argv) or {'metadata':{'uid':'foreign','resourceVersion':'8'}}
        try:driver.remove_job('fixture-job','fixture-job-uid')
        except RuntimeError as error:assert 'UID changed' in str(error)
        else:raise AssertionError('Foreign Job UID deletion allowed')
        assert len(calls)==1 and calls[0][0]=='get'
        # Exercise the real empty-start wait, producer release and failure cleanup.
        ready={'snapshot':{'at':'2026-10-25T12:00:00Z','nodes':[],'instances':[{'id':'native/0','ownerWorkloadUID':m.UID,'workloadUID':m.UID,'ready':True}],'routing':{m.UID:{'native/0':1}},'outstanding':0,'inProgressTarget':None},'actions':[]}
        empty=copy.deepcopy(ready);empty['snapshot'].update(instances=[],routing={})
        unready=copy.deepcopy(ready);unready['snapshot']['instances'][0]['ready']=False
        busy=copy.deepcopy(ready);busy['actions']=[{'status':'running'}]
        foreign=copy.deepcopy(ready);foreign['snapshot']['instances'][0]['ownerWorkloadUID']='other'
        original_ops=m.OPS;original_popen=m.subprocess.Popen;original_run=m.subprocess.run
        m.OPS=folder/'ops';(m.OPS/'campaign/v2').mkdir(parents=True)
        (m.OPS/'campaign/v2/control-alerts.jsonl').write_text('')
        class Process:
            def __init__(self,stream=None):self.stdout=stream;self.done=False
            def poll(self):return 0 if self.done else None
            def terminate(self):self.done=True
            def wait(self,timeout=None):self.done=True;return 0
        try:
            for name,states,expect_release in [
                    ('empty',[empty],False),('foreign',[foreign],False),
                    ('unready',[unready],False),('ready',[empty,unready,busy,ready],True)]:
                events=[];clock[0]=0
                lane=object.__new__(m.Conductor);lane.out=folder/name;lane.out.mkdir()
                lane.phase_out=lane.out/'phase';lane.a=SimpleNamespace(hosts=folder/'hosts')
                lane.source='fixture-source';lane.pending_node_boots=[]
                current_settings={'autonomousWorkloads':[],'staticRouting':{},'excludedNodes':[]}
                lane.settings=lambda:copy.deepcopy(current_settings)
                def put(settings,reason):
                    events.append('settings');current_settings.update(copy.deepcopy(settings));return {}
                lane.put=put;lane.boundary=lambda *args:None
                lane.assert_policy_idle=lambda:copy.deepcopy(empty)
                snapshots=iter(states);last=[states[-1]]
                def state():
                    value=next(snapshots,last[0]);events.append('state');return copy.deepcopy(value)
                lane.state=state;lane.wait_idle=lambda:copy.deepcopy(empty)
                lane.api=lambda method,path,body=None,**kwargs:({'operation':body,'status':'complete','endedAt':m.now()} if method=='POST' else {'pending':0,'assigned':0})
                lane.control_alerts=lambda offset:offset
                lane.telemetry_ready=lambda *args:events.append('telemetry')
                lane.payload_release=lambda *args:None
                lane.assert_producer_startable=lambda *args:None
                def job(*args):
                    assert m.load(lane.out/'lifecycle.json')['instances']
                    events.append('job');return {}
                lane.job=job
                producer={'metadata':{'name':'load','uid':'load-uid','ownerReferences':[{'uid':'job-uid'}]},'spec':{'nodeName':m.D2['producerNode'],'containers':[{'name':'load','resources':{}}]}}
                lane.wait_load_container=lambda *args:producer
                def k(argv,**kwargs):
                    if argv[0]=='create':return {'metadata':{'name':'job','uid':'job-uid'}}
                    if argv[0]=='exec':events.append('release');raise RuntimeError('fixture release stop')
                    if argv[:2]==['get','pods']:return {'items':[producer]}
                    if argv[:2]==['get','pod']:return producer
                    raise AssertionError(argv)
                lane.k=k
                lane.remove_job=lambda *args:events.append('delete')
                def pages(target,path,start,end):
                    events.append('raw');m.save(target/'00000.json',{'items':[]})
                lane.pages=pages
                def popen(argv,**kwargs):
                    if 'sample_cgroups.py' in str(argv):return Process()
                    read_fd,write_fd=os.pipe()
                    os.write(write_fd,(json.dumps({'d2Event':'ready-for-release','at':m.now()})+'\n').encode());os.close(write_fd)
                    return Process(os.fdopen(read_fd,'rb'))
                m.subprocess.Popen=popen
                def capture_logs(argv,**kwargs):
                    events.append('producer-log')
                    return SimpleNamespace(stdout='retained producer log\n',stderr='',returncode=0)
                m.subprocess.run=capture_logs
                try:lane.run('diagnostic',50,660,True,empty_start=True)
                except RuntimeError:pass
                else:raise AssertionError('Fixture failure was swallowed')
                assert ('release' in events)==expect_release,(name,events)
                if expect_release:
                    assert events.index('job')<events.index('telemetry')<events.index('release')
                    assert events.index('raw')<events.index('producer-log')<events.index('delete')
                    assert (lane.phase_out/'diagnostic/failed-producer-load-uid.log').read_text()=='retained producer log\n'
                else:assert 'job' not in events and 'delete' not in events
                assert (lane.phase_out/'diagnostic/request-pages/00000.json').exists()
        finally:
            m.OPS=original_ops;m.subprocess.Popen=original_popen;m.subprocess.run=original_run
        e5spec=importlib.util.spec_from_file_location('e5_cleanup_check',ROOT.parent/'e5.py')
        e5=importlib.util.module_from_spec(e5spec);e5spec.loader.exec_module(e5);e5.OPS=folder
        for harness,uids in [('d2',[m.UID]),('e5',['a','b','c'])]:
            for name,foreign,failed in [
                    ('empty',{},False),('foreign-route',{'other':{'other-native':1}},False),
                    ('foreign-stop',{'other':{}},False),('failed-drain',{'other':{}},True)]:
                lane=object.__new__(m.Conductor if harness=='d2' else e5.CPO)
                lane.out=folder/(harness+'-reset-'+name);lane.out.mkdir()
                lane.a=SimpleNamespace(command='cleanup' if harness=='d2' else 'finish',phase='fixture-reset');lane.source='fixture-source'
                initial={'autonomousWorkloads':uids+['other-auto'],'excludedNodes':['held'],
                         'staticRouting':{**{uid:{} for uid in uids},**copy.deepcopy(foreign)},
                         'staticRoutingValidatedAt':'2026-10-25T12:00:00Z'}
                current=copy.deepcopy(initial);events=[];writes=[]
                if harness=='d2':m.save(lane.out/'lifecycle.json',{'originalSettings':initial})
                else:
                    lane.uids=uids;lane.tb={'settleSeconds':194}
                    e5.save(lane.out/'initial.json',{'settings':initial})
                def api(method,path,body=None,**kwargs):
                    if method=='POST':
                        assert path=='/api/v1/scheduler/operations'
                        assert body['kind']=='reset' and body['workloadUIDs']==uids and body['reason']
                        events.append('reset')
                        current['autonomousWorkloads']=[uid for uid in current['autonomousWorkloads'] if uid not in uids]
                        for uid in uids:
                            if failed:current['staticRouting'][uid]={}
                            else:current['staticRouting'].pop(uid,None)
                        return {'operation':copy.deepcopy(body),'status':'failed' if failed else 'complete',
                                'error':'native drain failed' if failed else '',
                                'steps':[{'name':'disable-autonomy','status':'complete'},
                                         {'name':'drain','status':'failed' if failed else 'complete'}],
                                'endedAt':m.now(),'actual':{'routing':copy.deepcopy(current['staticRouting'])}}
                    assert method=='PUT' and path=='/api/v1/scheduler/settings'
                    assert events==['reset'] and not failed
                    assert 'staticRouting' not in body and 'staticRoutingValidatedAt' not in body
                    events.append('settings');writes.append(copy.deepcopy(body))
                    current.update({key:value for key,value in body.items() if key!='reason'})
                    return {'validatedAt':m.now()}
                lane.api=api
                lane.state=lambda:{'snapshot':{'instances':[],'nodes':[],'routing':copy.deepcopy(current['staticRouting'])}}
                if harness=='e5':lane.io=SimpleNamespace(api=api)
                complete=lane.out/('cleanup-complete.json' if harness=='d2' else 'finish-complete.json')
                try:
                    if harness=='d2':lane.cleanup()
                    else:lane.finish()
                except RuntimeError as error:
                    assert failed and 'reset failed' in str(error)
                    assert events==['reset'] and not writes and not complete.exists()
                else:
                    assert not failed and events==['reset','settings'] and complete.exists()
                    assert current['staticRouting']==foreign
                    assert current['autonomousWorkloads']==['other-auto']
                result_path=lane.out/('cleanup-reset-result.json' if harness=='d2' else 'finish-reset-result.json')
                result=m.load(result_path)
                assert result['steps'][1]['status']==('failed' if failed else 'complete')
        # Exercise the measurement smoke's real operation boundary without its CLI/live setup.
        smoke_source=ROOT.parents[1]/'measure/smoke/smoke.py'
        operation=next(node for node in ast.parse(smoke_source.read_text()).body if isinstance(node,ast.FunctionDef) and node.name=='operation')
        output=folder/'measurement-operation';output.mkdir()
        response=[{'status':'complete','steps':[]}]
        requests=[]
        def smoke_api(method,path,body):
            assert method=='POST' and path=='/api/v1/scheduler/operations'
            requests.append(copy.deepcopy(body));return 200,json.dumps(response[0])
        scope={'Path':Path,'json':json,'api':smoke_api,'L':lambda *args:None,'out':str(output),'ok':True}
        exec(compile(ast.Module(body=[operation],type_ignores=[]),str(smoke_source),'exec'),scope)
        route={'kind':'route','workloadUID':'smoke','weights':{'native':1000},'reason':'Local route fixture'}
        reset={'kind':'reset','workloadUIDs':['smoke'],'reason':'Local reset fixture'}
        scope['operation'](route,'route')
        response[0]={'status':'failed','error':'drain failed','steps':[{'name':'drain','status':'failed'}]}
        try:scope['operation'](reset,'reset')
        except RuntimeError as error:assert 'reset failed' in str(error)
        else:raise AssertionError('Measurement smoke continued after failed reset')
        assert requests==[route,reset] and scope['ok'] is False
        assert m.load(output/'reset-result.json')==response[0]
finally:
    m.time.monotonic=original_monotonic;m.time.sleep=original_sleep
print('producer lifecycle check PASS: init/terminal publication; UID+RV deletion; empty/unready/foreign blocked; Ready+idle release; failure raw/log before deletion; one server reset with retained steps; reset failure stops settings; readonly routing omitted and foreign routes/stops preserved; API/load/build=0')

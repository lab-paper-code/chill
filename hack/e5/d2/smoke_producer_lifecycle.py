#!/usr/bin/env python3
"""Bounded local lifecycle fixtures; no Kubernetes or producer execution."""
import copy
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
                lane.api=lambda method,path,**kwargs:{'pending':0,'assigned':0}
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
        for name,initial_routes,expected_routes,lingering in [
                ('absent',None,None,False),('owned-empty',{m.UID:{}},None,False),
                ('foreign-route',{m.UID:{},'other':{'other-native':1}},{'other':{'other-native':1}},False),
                ('foreign-stop',{m.UID:{},'other':{}},{'other':{}},False),
                ('pod-remains',{m.UID:{}},None,True)]:
            lane=object.__new__(m.Conductor);lane.out=folder/('cleanup-'+name);lane.out.mkdir()
            current={'autonomousWorkloads':[]}
            if initial_routes is not None:current['staticRouting']=copy.deepcopy(initial_routes)
            instance={'id':'owned-pod/0','node':{'name':'fixture','uid':'node-uid'},'ownerWorkloadUID':m.UID,'workloadUID':''}
            m.save(lane.out/'lifecycle.json',{'stage':'READY_BOUND','instances':[instance],'originalSettings':copy.deepcopy(current),'originalNodes':[]})
            current.setdefault('staticRouting',{})[m.UID]={}
            removed=[False];events=[];writes=[]
            lane.settings=lambda:copy.deepcopy(current)
            def state():
                return {'snapshot':{'at':m.now(),'instances':[] if removed[0] else [instance],'nodes':[],'routing':{m.UID:{}},'outstanding':0,'inProgressTarget':None},'actions':[]}
            lane.state=state
            def put(settings,reason):
                if m.UID not in (settings.get('staticRouting') or {}):
                    assert removed[0] and 'pods-absent' in events
                    events.append('release')
                writes.append(copy.deepcopy(settings));current.clear();current.update(copy.deepcopy(settings))
                return {'status':'complete','endedAt':m.now()}
            lane.put=put
            def api(method,path,body=None,**kwargs):
                if method=='GET':return {'pending':0,'assigned':0}
                assert method=='POST' and body['kind']=='remove'
                assert current['staticRouting'][m.UID]=={}
                removed[0]=True;events.append('remove')
                return {'status':'complete','endedAt':m.now()}
            lane.api=api
            def k(argv,**kwargs):
                assert argv==['get','pods','-o','json'] and removed[0]
                if lingering:return {'items':[{'metadata':{'uid':'owned-pod'}}]}
                events.append('pods-absent');return {'items':[]}
            lane.k=k
            try:lane.cleanup()
            except RuntimeError as error:
                assert lingering and 'physical Pod remains' in str(error)
                assert 'release' not in events and current['staticRouting'][m.UID]=={}
            else:
                assert not lingering
                assert events.index('remove')<events.index('pods-absent')<events.index('release')
                if expected_routes is None:assert writes[-1]['staticRouting']=={}
                else:assert writes[-1]['staticRouting']==expected_routes
                assert m.UID not in writes[-1].get('staticRouting',{})
        e5spec=importlib.util.spec_from_file_location('e5_cleanup_check',ROOT.parent/'e5.py')
        e5=importlib.util.module_from_spec(e5spec);e5spec.loader.exec_module(e5);e5.OPS=folder
        for name,routes,expected,lingering in [
                ('absent',None,{},False),('owned',{'a':{},'b':{},'c':{}},{},False),
                ('other-stop',{'a':{},'other':{}},{'other':{}},False),
                ('pod-remains',None,{},True)]:
            lane=object.__new__(e5.CPO);lane.out=folder/('e5-'+name);lane.out.mkdir();lane.uids=['a','b','c']
            initial={'autonomousWorkloads':[]}
            if routes is not None:initial['staticRouting']=routes
            e5.save(lane.out/'initial.json',{'settings':initial,'state':{'snapshot':{'nodes':[]}}})
            events=[];writes=[]
            def clear_owned(prefix):
                events.append('removed');return {'snapshot':{'instances':[{'id':'owned-pod/0'}]}}
            lane.clear_owned=clear_owned
            def pods(argv):
                assert events==['removed']
                if lingering:return {'items':[{'metadata':{'uid':'owned-pod'}}]}
                events.append('pods-absent');return {'items':[]}
            lane.io=SimpleNamespace(k=pods)
            def put(settings,reason):
                assert events==['removed','pods-absent'];writes.append(settings);return {}
            lane.put=put
            final={'snapshot':{'instances':[],'nodes':[]}}
            lane.settings=lambda:copy.deepcopy(writes[-1])
            lane.wait_empty_publication=lambda *args:final;lane.idle=lambda:final
            try:lane.finish()
            except RuntimeError as error:assert lingering and 'Pod remains' in str(error) and not writes
            else:
                assert not lingering and writes[-1]['staticRouting']==expected
finally:
    m.time.monotonic=original_monotonic;m.time.sleep=original_sleep
print('producer lifecycle check PASS: init/terminal publication; UID+RV deletion; empty/unready/foreign blocked; Ready+idle release; failure raw/log before deletion; cleanup releases owned routing after Pod removal; foreign route/stop preserved; API/load/build=0')

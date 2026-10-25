#!/usr/bin/env python3
"""Main072 actual local pipe and no-live-action failed-start fixtures."""
import copy
import datetime as dt
import importlib.util
import json
import os
from pathlib import Path
import selectors
import tempfile
import subprocess
import sys
import time
from types import SimpleNamespace
ROOT=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('d2_producer_pipe_check',ROOT/'conduct_before.py')
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
read_fd,write_fd=os.pipe();stream=os.fdopen(read_fd,'rb',buffering=0);os.set_blocking(read_fd,False)
ready={'d2Event':'ready-for-release','at':dt.datetime.now(dt.timezone.utc).isoformat()}
payload={'d2Event':'payload','records':[],'offeredRequests':1}
try:
    os.write(write_fd,(json.dumps(payload)+'\n'+json.dumps(ready)+'\n').encode())
    selector=selectors.DefaultSelector();selector.register(stream,selectors.EVENT_READ)
    assert selector.select(.5),'Actual local pipe never became readable'
    lines,pending,eof=m.read_stream_lines(stream,b'')
    assert [json.loads(line)['d2Event'] for line in lines]==['payload','ready-for-release'] and pending==b'' and not eof
    # Writer is deliberately still open: both lines were delivered BEFORE EOF.
    assert m.read_stream_lines(stream,pending)==([],b'',False)
    selector.close()
finally:stream.close();os.close(write_fd)
with tempfile.TemporaryDirectory(prefix='d2-producer-pipe-') as temporary:
    root=Path(temporary);folder=root/'expiry';folder.mkdir()
    pod={'metadata':{'name':'fixture-pod','uid':'fixture-pod-uid'},'spec':{'nodeName':m.D2['producerNode'],'containers':[{'name':'load','resources':{}}]},'status':{'phase':'Running','containerStatuses':[{'name':'load','containerID':'fixture-container','state':{'running':{'startedAt':ready['at']}}}]}}
    driver=object.__new__(m.Conductor);actions=[]
    driver.k=lambda *args,**kwargs:actions.append('FORBIDDEN-live-action')
    expired=dict(ready,at=(dt.datetime.now(dt.timezone.utc)-dt.timedelta(seconds=301)).isoformat())
    (folder/'producer-ready-event.json').write_text(json.dumps(expired))
    try:driver.assert_producer_startable(folder,pod,pod)
    except RuntimeError as error:assert 'barrier expired' in str(error)
    else:raise AssertionError('Expired ready event permitted a start')
    assert actions==[]
    (folder/'producer-ready-event.json').write_text(json.dumps(ready))
    dead=copy.deepcopy(pod);dead['status']['phase']='Failed'
    try:driver.assert_producer_startable(folder,dead,pod)
    except RuntimeError as error:assert 'not running' in str(error)
    else:raise AssertionError('Dead Pod permitted autonomy/GO')
    # Drive the real run() exception/finally path with local fixtures only.
    fd,w=os.pipe();pipe=os.fdopen(fd,'rb',buffering=0)
    os.write(w,(json.dumps(payload)+'\n'+json.dumps(ready)+'\n').encode());os.close(w)
    class Process:
        def __init__(self,stdout=None):self.stdout=stdout;self.done=False
        def poll(self):return 0 if self.done else None
        def terminate(self):self.done=True;actions.append('process-terminate')
        def wait(self,timeout=None):self.done=True;return 0
        def kill(self):self.done=True
    sampler=Process();logs=Process(pipe);processes=iter([sampler,logs]);original_popen=m.subprocess.Popen;original_ops=m.OPS
    m.subprocess.Popen=lambda *args,**kwargs:next(processes)
    m.OPS=root/'ops';alert=m.OPS/'campaign/v2/control-alerts.jsonl';alert.parent.mkdir(parents=True);alert.write_text('')
    driver.phase_out=root/'phase';driver.phase_out.mkdir();driver.out=root/'physical';driver.out.mkdir()
    driver.a=SimpleNamespace(hosts=root/'hosts.json',phase='fixture',command='autonomous');driver.source='fixture-source'
    prior={'autonomousWorkloads':[],'excludedNodes':['original-held'],'staticRouting':{m.UID:{},'other':{'old-native-id':1}},'staticRoutingValidatedAt':ready['at']}
    current=copy.deepcopy(prior)
    driver.boundary=lambda *args:None;driver.verify=lambda *args,**kwargs:None
    driver.settings=lambda:copy.deepcopy(current)
    driver.control_alerts=lambda offset:offset
    driver.job=lambda *args:{'metadata':{'name':'fixture-job'}}
    driver.wait_load_container=lambda *args:pod
    driver.telemetry_ready=lambda *args:None;driver.payload_release=lambda *args:None
    def kube(argv,**kwargs):
        if argv[0]=='create':return {'metadata':{'name':'fixture-job','uid':'fixture-job-uid'}}
        if argv[:2]==['get','pods']:return {'items':[pod]}
        if argv[:2]==['get','pod']:return pod
        if argv[0]=='exec':actions.append('go-failed');raise RuntimeError('original-go-error')
        raise AssertionError('Unexpected fixture command '+str(argv))
    driver.k=kube
    def autonomy(enabled):
        actions.append('autonomy-on');current['autonomousWorkloads']=[m.UID];current['excludedNodes']=['probe'];current['staticRouting'].pop(m.UID,None)
    driver.autonomy=autonomy
    def api(method,path,body=None,**kwargs):
        if method=='GET':
            assert path.startswith('/api/v1/observations/')
            actions.append('raw-page')
            return {'events':[],'nextCursor':None}
        assert method=='PUT' and path=='/api/v1/scheduler/settings'
        assert 'staticRouting' not in body and 'staticRoutingValidatedAt' not in body and 'reason' not in body
        actions.append('restore-prior-flags')
        current.update(copy.deepcopy(body))
        return {'actualFixtureWrite':True}
    driver.api=api
    def cleanup(*args):actions.append('cleanup-job');raise RuntimeError('secondary-cleanup-error')
    driver.remove_job=cleanup
    try:
        try:driver.run('fixture-run',50,660,True)
        except RuntimeError as error:assert str(error)=='original-go-error',str(error)
        else:raise AssertionError('Original failure was suppressed')
        assert actions.index('restore-prior-flags')<actions.index('cleanup-job')
        assert actions.count('raw-page')==2 and actions.index('raw-page')<actions.index('cleanup-job')
        assert current['autonomousWorkloads']==[] and current['excludedNodes']==['original-held'] and current['staticRouting']=={'other':{'old-native-id':1}}
        phase=json.loads((driver.phase_out/'fixture-run/phase.json').read_text())
        assert phase['error']=='original-go-error' and 'secondary-cleanup-error' in phase['cleanupErrors'][0]
    finally:m.subprocess.Popen=original_popen;m.OPS=original_ops;pipe.close()
    # The same real run()/finally path must also preserve a post-drain failure.
    fd,w=os.pipe();pipe=os.fdopen(fd,'rb',buffering=0)
    events=[payload,ready,{'d2Event':'replay-start','at':ready['at']},{'d2Event':'replay-drained','at':ready['at']}]
    os.write(w,(''.join(json.dumps(event)+'\n' for event in events)).encode());os.close(w)
    sampler=Process();logs=Process(pipe);processes=iter([sampler,logs])
    def end_kube(argv,**kwargs):
        if argv[0]=='exec':actions.append('go-released');return {}
        return kube(argv,**kwargs)
    def fail_end_counter(*args):raise RuntimeError('original-end-counter-error')
    driver.k=end_kube;driver.release_client_end_counter=fail_end_counter
    m.subprocess.Popen=lambda *args,**kwargs:next(processes);m.OPS=root/'ops'
    try:
        try:driver.run('fixture-end-run',50,660,True)
        except RuntimeError as error:assert str(error)=='original-end-counter-error',str(error)
        else:raise AssertionError('Post-drain failure was suppressed')
        end_phase=json.loads((driver.phase_out/'fixture-end-run/phase.json').read_text())
        assert end_phase['error']=='original-end-counter-error' and 'secondary-cleanup-error' in end_phase['cleanupErrors'][0]
        assert current['autonomousWorkloads']==[] and current['excludedNodes']==['original-held']
    finally:m.subprocess.Popen=original_popen;m.OPS=original_ops;pipe.close()

# Run the actual wrapper in a real subprocess, replacing only the scientific CLI
# with a no-inference fixture and relocating its existing marker into this tempdir.
actual_phase=Path(m.CONFIG['paths']['d2State'])/'autonomous-before-reset-073/before-autonomous-073/autonomous-before-autonomous-073'
actual_end=json.loads((actual_phase/'phase.json').read_text())['endUTC']
with (actual_phase/(m.D2['producerNode']+'-cgroup.jsonl')).open() as rows:
    valid=next(json.loads(line) for line in rows if line.strip() and json.loads(line).get('role')=='client' and m.epoch(json.loads(line).get('at'))>=m.epoch(actual_end) and all(json.loads(line).get(key) is not None for key in ('cpuUsageNanos','nrThrottled','throttledNanos')))
with tempfile.TemporaryDirectory(prefix='d2-client-end-') as temporary:
    folder=Path(temporary);marker=folder/'d2-start';marker.touch()
    cli_fixture="from types import SimpleNamespace\ncli=SimpleNamespace(replay=lambda *args,**kw: None,arrival_offsets_ns=lambda *args: [])\ndef fixture_main(argv):\n    cli.replay([],arrivals='poisson',rate=50,duration=0,seed=1)\n    return 0\ncli.main=fixture_main"
    script=m.LOAD_WRAPPER.replace('from chill_loadgen import cli',cli_fixture).replace('/tmp/d2-start',str(marker))
    process=subprocess.Popen([sys.executable,'-u','-c',script],stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    selector=selectors.DefaultSelector();selector.register(process.stdout,selectors.EVENT_READ);os.set_blocking(process.stdout.fileno(),False)
    pending=b'';drained=None;deadline=time.monotonic()+5
    try:
        while drained is None and time.monotonic()<deadline:
            for key,_ in selector.select(.1):
                lines,pending,eof=m.read_stream_lines(key.fileobj,pending)
                for line in lines:
                    event=json.loads(line)
                    if event.get('d2Event')=='replay-drained':drained=event
        assert drained is not None and process.poll() is None and marker.exists()
        folder_counter=folder/(m.D2['producerNode']+'-cgroup.jsonl')
        pod=copy.deepcopy(pod);pod['metadata']['uid']=valid['podUID'];pod['status']['containerStatuses'][0]['containerID']='containerd://'+valid['containerID']
        driver=object.__new__(m.Conductor);actions=[]
        phase={'startUTC':'fixture-start','endUTC':actual_end,'drainedUTC':drained['at']}
        def local_kube(argv,**kwargs):
            if argv[:2]==['get','pod']:return pod
            assert argv[0]=='exec' and "Path('/tmp/d2-start').unlink()" in argv[-1]
            proof=json.loads((folder/'client-end-counter-proof.json').read_text())
            assert proof['row']==valid
            actions.append('release-after-real-row');marker.unlink();return {}
        driver.k=local_kube
        assert not driver.release_client_end_counter(folder,pod,phase)
        invalid=[]
        for key,value in [('podUID','foreign-pod'),('containerID','foreign-container'),('at','2026-10-24T20:22:16Z'),('nrThrottled',None),('cpuUsageNanos',float('nan'))]:
            row=copy.deepcopy(valid);row[key]=value;invalid.append(row)
        for row in invalid:
            folder_counter.write_text(json.dumps(row)+'\n')
            assert not driver.release_client_end_counter(folder,pod,phase)
            assert process.poll() is None and marker.exists() and actions==[]
        folder_counter.write_text(json.dumps(valid)+'\n')
        replaced=copy.deepcopy(pod);replaced['metadata']['uid']='replacement-pod'
        driver.k=lambda *args,**kwargs:replaced
        try:driver.release_client_end_counter(folder,pod,phase)
        except RuntimeError as error:assert 'not running' in str(error)
        else:raise AssertionError('Replaced producer identity released its marker')
        assert marker.exists() and process.poll() is None and actions==[]
        driver.k=local_kube
        assert driver.release_client_end_counter(folder,pod,phase)
        assert process.wait(timeout=5)==0 and actions==['release-after-real-row']
        assert phase['endCounterReleasedUTC'] and not marker.exists()
        assert process.stderr.read()==b''
    finally:
        selector.close()
        if process.poll() is None:process.kill();process.wait()
        process.stdout.close();process.stderr.close()
print('Main078 client-end PASS: actual subprocess alive after drained; no release for missing/wrongUID/wrongCID/pre-end/null/NaN counter; saved real same-UID/CID post-end row precedes marker release/normal exit; real run post-drain primary preserved through cleanup failure; API/Job/load=0')
print('producer pipe PASS: two lines/one actual write before EOF; expired/dead rejection with no live action; failed-start prior autonomy restore before cleanup; primary exception retained; API/Job/load=0')

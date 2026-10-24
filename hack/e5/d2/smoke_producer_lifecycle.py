#!/usr/bin/env python3
"""Main070 bounded local fixtures/structure only; no Kubernetes or producer execution."""
import ast
import copy
import importlib.util
import inspect
import json
from pathlib import Path
import tempfile
import textwrap
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
finally:
    m.time.monotonic=original_monotonic;m.time.sleep=original_sleep
source=textwrap.dedent(inspect.getsource(m.Conductor.run))
assert source.index('self.wait_load_container')<source.index('logs=subprocess.Popen')
handler=next(h for node in ast.walk(ast.parse(source)) if isinstance(node,ast.Try) for h in node.handlers if h.name=='e')
assert isinstance(handler.body[-1],ast.Raise) and handler.body[-1].exc is None
assert 'secondary-cleanup-errors.json' in source
print('producer lifecycle check PASS: actual-running-before-logs structure; init/terminal publication fixtures; raw DELETE UID+RV; foreign UID refusal; original exception rethrow; API/load/build=0')

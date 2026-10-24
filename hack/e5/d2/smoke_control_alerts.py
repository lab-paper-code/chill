#!/usr/bin/env python3
"""One local regression: actual monitor wire shape, no cluster/token/API/load.
Fixture actuation proof is test data, never a live receipt or readiness claim.
Run only in Main's authorized short no-measurement gap.
"""
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace

HERE=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('d2_control_alert_smoke',HERE/'conduct_before.py')
m=importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
node={'name':'fixture-node','uid':'52b04c16-4f4a-4b4c-938c-eac35e679f09'}
boot={'at':'2026-10-24T15:39:02Z','event':'state','uid':'215468e5-98f5-45f4-a5e4-6919e1f1ae56','pod':'chill-node-agent-q4rj9','node':node['name'],'component':'node-agent','container':'node-agent','phase':'Running','ready':True,'restarts':34,'state':{'running':{'startedAt':'2026-10-24T15:38:23Z'}},'lastState':{'terminated':{'exitCode':255,'reason':'Unknown','finishedAt':'2024-06-17T20:29:45Z'}},'reasons':['restart increment']}
proof={'status':'native-ready','mutations':1,'node':node,'targetMode':'MAXN','requestedAt':'2026-10-24T15:36:00Z','at':'2026-10-24T15:39:01Z','transition':{'powerMode':'MAXN','requestedAt':'2026-10-24T15:36:00Z','generation':37,'readyAt':'2026-10-24T15:38:59Z'}}

def altered(**fields):
    row=copy.deepcopy(boot);row.update(fields);return row

bad_mode=copy.deepcopy(proof);bad_mode['transition']['powerMode']='MODE_15W'
bad_uid=copy.deepcopy(proof);bad_uid['node']=dict(node,uid='different-node-uid')
cases=[
    ('legacy oom',{'event':'OOMKilled'},(),True,'fatal-oom'),
    ('state reasons oom',altered(reasons=['OOMKilled/137']),(proof,),True,'fatal-oom'),
    ('state reasons crashloop',altered(reasons=['CrashLoopBackOff']),(proof,),True,'fatal-crashloop'),
    ('lastState137',altered(reasons=[],lastState={'terminated':{'exitCode':137,'reason':'Error'}}),(proof,),True,'fatal-oom'),
    ('legacy query failure',{'event':'query failure'},(),True,'fatal-query-failure'),
    ('unexpected control restart',altered(component='serving-entrypoint',container='serving-entrypoint'),(proof,),True,'fatal-unexpected-restart'),
    ('unproved node restart',boot,(),True,'unconfirmed-node-actuation-candidate'),
    ('approved actual-shape mode boot',boot,(proof,),False,'approved-node-actuation-boot'),
    ('wrong bound uid',boot,(bad_uid,),True,'fatal-mode-transition-mismatch'),
    ('wrong returned mode',boot,(bad_mode,),True,'fatal-mode-transition-mismatch'),
    ('ordinary state',altered(reasons=[],lastState={}),(),False,'ordinary-state'),
]
native={'metadata':dict(node,labels={'chill.dacs.io/power-mode':'MAXN','chill.dacs.io/power-mode.state':'success'},annotations={'chill.dacs.io/power-mode-requested-at':proof['requestedAt'],'chill.dacs.io/power-mode-generation':'37','chill.dacs.io/power-mode-transition':json.dumps(dict(proof['transition'],applyStartedAt='2026-10-24T15:36:01Z'))}),'status':{'conditions':[{'type':'Ready','status':'True'}]}}
def observation(raw):
    return {'kind':'node-power-safety-observation','node':raw,'authorizedSinceUTC':'2026-10-24T15:35:00Z','observedAtUTC':'2026-10-24T15:39:03Z'}
pending=copy.deepcopy(native);pending['metadata']['labels']['chill.dacs.io/power-mode.state']='pending';pending['metadata']['annotations']['chill.dacs.io/power-mode-generation']='38'
stale=copy.deepcopy(native);stale_transition=dict(proof['transition'],readyAt='2026-10-24T15:36:30Z');stale['metadata']['annotations']['chill.dacs.io/power-mode-transition']=json.dumps(stale_transition)
wrong_gen=copy.deepcopy(native);wrong_gen['metadata']['annotations']['chill.dacs.io/power-mode-generation']='38'
old_request=observation(native);old_request['authorizedSinceUTC']='2026-10-24T15:37:00Z'
failed=copy.deepcopy(native);failed['metadata']['labels']['chill.dacs.io/power-mode.state']='failed'
cases.extend([
    ('immediate native Ready',(boot),(observation(native),),False,'approved-node-actuation-boot'),
    ('pending fence generation may advance',boot,(observation(pending),),False,'approved-node-actuation-in-progress'),
    ('stale ready is not restart excuse',boot,(observation(stale),),True,'fatal-unconfirmed-node-restart'),
    ('success generation mismatch',boot,(observation(wrong_gen),),True,'fatal-mode-transition-mismatch'),
    ('old request before authorized phase',boot,(old_request,),True,'fatal-mode-transition-mismatch'),
    ('failed native transition',boot,(observation(failed),),True,'fatal-mode-transition-mismatch'),
])
for label,row,proofs,stop,kind in cases:
    result=m.control_alert_decision(row,proofs,[node])
    assert result['stop'] is stop and result['classification']==kind,(label,result)
calls=[];original_now=m.now;m.now=lambda:'2026-10-24T15:39:03Z'
result=m.resolve_control_alert(boot,[],[node],authorized_since='2026-10-24T15:35:00Z',report_candidate=lambda *_:calls.append('report'),node_reader=lambda name:calls.append('node-get') or native)
assert calls==['report','node-get'] and result['classification']=='approved-node-actuation-boot'
calls.clear()
result=m.resolve_control_alert(boot,[],[node],report_candidate=lambda *_:calls.append('report'),node_reader=lambda _:calls.append('forbidden'))
assert calls==['report'] and result['stop'] and result['requiresImmediateNativeCorrelation']
calls.clear()
result=m.resolve_control_alert(altered(reasons=['OOMKilled/137']),[],[node],authorized_since='2026-10-24T15:35:00Z',report_candidate=lambda *_:calls.append('wrong-report'),node_reader=lambda _:calls.append('forbidden'))
assert calls==[] and result['classification']=='fatal-oom' and result['stop']
def unavailable(_):raise RuntimeError('test-only Kubernetes safety read failure')
result=m.resolve_control_alert(boot,[],[node],authorized_since='2026-10-24T15:35:00Z',report_candidate=lambda *_:None,node_reader=unavailable)
assert result['classification']=='fatal-native-safety-query-failure' and result['stop']
m.now=original_now

with tempfile.TemporaryDirectory(prefix='d2-control-alert-smoke-') as temporary:
    root=Path(temporary);m.OPS=root
    log=root/'campaign/v2/control-alerts.jsonl';log.parent.mkdir(parents=True)
    phase=root/'phase';phase.mkdir()
    reader=SimpleNamespace(phase_out=phase,release={},b={'selected':[{'node':node}]},native_mode_authorized_since=None,pending_node_boots=[])
    log.write_text(json.dumps(altered(reasons=['OOMKilled/137']))+'\n')
    try:m.Conductor.control_alerts(reader,0)
    except RuntimeError as error:assert 'fatal-oom' in str(error)
    else:raise AssertionError('actual state/reasons OOM was not stopped')
    captured=json.loads((phase/'control-stop-alert.json').read_text())
    assert captured['row']['reasons']==['OOMKilled/137']
    proof_path=root/'actual-shape-test-proof.json';proof_path.write_text(json.dumps(proof))
    reader.release={'nodeActuationEvidenceRefs':[str(proof_path)]}
    log.write_text(json.dumps(boot)+'\n')
    offset=m.Conductor.control_alerts(reader,0)
    assert offset==log.stat().st_size
    captured=json.loads((phase/'node-actuation-boot-alerts.jsonl').read_text())
    assert captured['row']['uid']==boot['uid'] and captured['decision']['generation']==37
print('control-alert smoke PASS: 17 classifier cases + 4 immediate-resolver paths + 2 shared-reader paths; cluster/API/load=0')

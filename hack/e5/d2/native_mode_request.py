#!/usr/bin/env python3
"""CPO-only existing desired-label/native-actuator request, never host nvpmodel.
Requires Main's exact node/mode technical release after actual kappa selection.
One fresh UID/RV-tested request, no mutation retry or scientific measurement.
"""
import argparse
import datetime as dt
import json
import os
from pathlib import Path
import subprocess
import sys
import time

CONFIG=json.loads(Path(os.environ['CHILL_EXPERIMENT_CONFIG']).read_text())
K=CONFIG['d2']['kubectl']
LABEL='chill.dacs.io/power-mode'
STATE='chill.dacs.io/power-mode.state'
REQUESTED='chill.dacs.io/power-mode-requested-at'
TRANSITION='chill.dacs.io/power-mode-transition'
GENERATION='chill.dacs.io/power-mode-generation'


def now():return dt.datetime.now(dt.timezone.utc).isoformat()

def save(path,value):Path(path).write_text(json.dumps(value,indent=2)+'\n')

def k(argv):
    p=subprocess.run(K+argv,text=True,capture_output=True,timeout=60)
    if p.returncode:raise RuntimeError('kubectl failed without retry: '+p.stderr[:500])
    return json.loads(p.stdout)

def settled(node,mode,requested=None):
    meta=node['metadata'];labels=meta.get('labels',{});annotations=meta.get('annotations',{})
    transition=json.loads(annotations.get(TRANSITION,'{}'))
    serves=labels.get(LABEL)==mode and transition.get('powerMode')==mode and transition.get('requestedAt')==annotations.get(REQUESTED)
    if requested is not None:serves=serves and annotations.get(REQUESTED)==requested
    state=labels.get(STATE)
    if serves and state=='failed':raise RuntimeError('Native actuation failed: '+str(transition.get('failure')))
    if serves and state=='success':
        if transition.get('generation')!=int(annotations[GENERATION]) or not transition.get('readyAt'):
            raise RuntimeError('Node left target generation or has no ReadyAt')
        ready=any(c.get('type')=='Ready' and c.get('status')=='True' for c in node.get('status',{}).get('conditions',[]))
        return ready,transition
    return False,transition


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--release',required=True,type=Path)
    p.add_argument('--out',required=True,type=Path)
    a=p.parse_args()
    if not a.release.is_absolute() or not a.out.is_absolute():p.error('Absolute paths required')
    a.out.mkdir(parents=True,exist_ok=True)
    release=json.loads(a.release.read_text())
    if release.get('executor')!='CampaignOps' or release.get('command')!='node-native-mode' or not release.get('mainInstructionUTC'):
        p.error('Main exact native-mode technical release required')
    name=release['node']['name'];uid=release['node']['uid'];mode=release['targetMode']
    maximum=release.get('maxActuationSeconds')
    if maximum!=900:p.error('Bind actual existing900s native actuation policy; do not invent another watchdog')
    try:
        # These read-only guards precede a native request and never overlap load.
        jobs=k(['-n','chill-system','get','jobs','-o','json'])
        if any(x.get('status',{}).get('active',0) for x in jobs['items']):raise RuntimeError('Active producer/measurement Job exists')
        pods=k(['-n','chill-system','get','pods','--field-selector','spec.nodeName='+name,'-o','json'])
        active=[x for x in pods['items'] if not x['metadata'].get('deletionTimestamp') and x.get('status',{}).get('phase') not in ('Succeeded','Failed') and any(c.get('name')=='firstfloor' for c in x['spec'].get('containers',[]))]
        if active:raise RuntimeError('Target native serving instances remain; mode realization requires drained empty node')
        node=k(['get','node',name,'-o','json']);save(a.out/'node-fresh-before.json',node)
        if node['metadata']['uid']!=uid:raise RuntimeError('Actual NodeUID differs from Main binding')
        done,transition=settled(node,mode)
        if done:
            save(a.out/'already-native-ready.json',dict(at=now(),node=node,transition=transition,nativeRequests=0));return 0
        annotations=node['metadata'].get('annotations',{})
        old_mode=node['metadata'].get('labels',{}).get(LABEL)
        old_done,_=settled(node,old_mode)
        if not old_done:raise RuntimeError('Current native request is not normally settled; do not overwrite it')
        if REQUESTED not in annotations:raise RuntimeError('Native request annotation map/protocol unavailable')
        stamp=now()
        patch=[{'op':'test','path':'/metadata/uid','value':uid},{'op':'test','path':'/metadata/resourceVersion','value':node['metadata']['resourceVersion']},{'op':'add','path':'/metadata/labels/chill.dacs.io~1power-mode','value':mode},{'op':'add','path':'/metadata/annotations/chill.dacs.io~1power-mode-requested-at','value':stamp}]
        save(a.out/'fresh-native-request.json',patch)
        response=k(['patch','node',name,'--type=json','--patch-file',str(a.out/'fresh-native-request.json'),'-o','json'])
        save(a.out/'native-request-response.json',response)
        deadline=time.monotonic()+maximum
        with (a.out/'native-readback.jsonl').open('w') as log:
            while time.monotonic()<deadline:
                current=k(['get','node',name,'-o','json'])
                if current['metadata']['uid']!=uid:raise RuntimeError('NodeUID changed during native actuation')
                done,transition=settled(current,mode,stamp)
                log.write(json.dumps(dict(at=now(),node=current))+'\n');log.flush()
                if done:
                    save(a.out/'node-native-ready.json',current)
                    save(a.out/'result.json',dict(at=now(),status='native-ready',node={'name':name,'uid':uid},targetMode=mode,requestedAt=stamp,transition=transition,mutations=1,hostNvpmodelCalls=0,automaticRetries=0))
                    return 0
                time.sleep(5)
        raise RuntimeError('Native request did not reach matching generation/ReadyAt/NodeReady within900s')
    except Exception as e:
        save(a.out/'STOP.json',dict(at=now(),node={'name':name,'uid':uid},targetMode=mode,error=str(e),instruction='Stop node lane; preserve actual native outcome/handback and ask Main. No retry, host power command or speculative rollback.'))
        print(str(e),file=sys.stderr);return 2

if __name__=='__main__':sys.exit(main())

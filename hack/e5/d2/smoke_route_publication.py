#!/usr/bin/env python3
"""Main073 one bounded saved-unroute/cache regression; no API/PUT/native actions."""
import copy
import datetime as dt
import importlib.util
import json
from pathlib import Path
import tempfile
ROOT=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('d2_route_publication_check',ROOT/'conduct_before.py')
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
DATA=Path(m.CONFIG['paths']['d2State'])
EVIDENCE=DATA/'logical-eight-reference-reset-073'
def state(path):
    document=json.loads(path.read_text())
    return document.get('state',document)
before=state(EVIDENCE/'fresh-before-cleanup-state.json')
after=state(EVIDENCE/'unroute-publication-current-state.json')
ack=json.loads((EVIDENCE/'same-original-unroute-full-complete-operation-ack.json').read_text())['fullOperationResult']
assert ack['status']=='complete' and not ack.get('error')
previous=before['snapshot']['routing'][m.UID]
assert previous and after['snapshot']['routing'][m.UID]=={}
origin=m.epoch(ack['endedAt'])
original_time=m.time.time;original_monotonic=m.time.monotonic;original_sleep=m.time.sleep
clock=[0.0];epoch=[origin+.166]
m.time.time=lambda:epoch[0]+clock[0]
m.time.monotonic=lambda:clock[0]
m.time.sleep=lambda seconds:clock.__setitem__(0,clock[0]+seconds)
try:
    with tempfile.TemporaryDirectory(prefix='d2-route-publication-') as temporary:
        driver=object.__new__(m.Conductor);driver.out=Path(temporary);writes=[]
        driver.put=lambda *args,**kwargs:writes.append('FORBIDDEN PUT')
        driver.idle=lambda value:not value['snapshot'].get('outstanding') and not value['snapshot'].get('inProgressTarget') and not any(a.get('status') in ('running','cancel') for a in value.get('actions',[]))
        reads=iter([before,after]);driver.state=lambda:next(reads)
        result=driver.wait_routing_publication({},previous,ack,'same-original')
        assert result is after and clock[0]==5 and writes==[]
        # Only one second remains in this SAME original194s budget, not new194.
        epoch[0]=origin+193;clock[0]=0;driver.state=lambda:before
        try:driver.wait_routing_publication({},previous,ack,'deadline')
        except RuntimeError as error:assert 'no timeout reset' in str(error)
        else:raise AssertionError('Original publication deadline was reset')
        assert abs(clock[0]-1)<.001 and writes==[]
        # Already-published current evidence is adoptable read-only, even later.
        epoch[0]=origin+1000;clock[0]=0;driver.state=lambda:after
        assert driver.wait_routing_publication({},previous,ack,'already-ready') is after
        assert clock[0]==0 and writes==[]
        conflict=copy.deepcopy(before);conflict['snapshot']['routing'][m.UID]={'not-original-instance':1}
        try:driver.routing_publication_ready(conflict,{},previous)
        except RuntimeError as error:assert 'conflicts' in str(error)
        else:raise AssertionError('Actual third routing was accepted as cache lag')
        foreign=copy.deepcopy(after);foreign['snapshot']['instances'][0]['workloadUID']='foreign'
        try:driver.routing_publication_ready(foreign,{},previous)
        except RuntimeError as error:assert 'conflicts' in str(error)
        else:raise AssertionError('Foreign served UID was accepted')
        scoped=copy.deepcopy(after);scoped['snapshot']['instances'].append({'id':'other-owner-instance','ownerWorkloadUID':'fixture-owner2','workloadUID':'fixture-owner2'})
        assert driver.routing_publication_ready(scoped,{},previous,workload_uid=m.UID,allowed_owner_uids=[m.UID,'fixture-owner2','fixture-owner3'])
        assert abs(driver.publication_end(ack,seconds=360)[1]-(origin+360))<.001
finally:
    m.time.time=original_time;m.time.monotonic=original_monotonic;m.time.sleep=original_sleep
actual_root=DATA/'autonomous-before-reset-073'
actual_ack=json.loads((actual_root/'prepare-empty-route-settings-ack.json').read_text())
actual_settings=json.loads((actual_root/'post-nanoparser-failure-original-empty-mutation-current-settings.json').read_text())
actual_empty=state(actual_root/'post-nanoparser-failure-original-empty-mutation-current-state.json')
expected=m.epoch('2026-10-24T20:00:24Z')
for timestamp,fraction in [('2026-10-24T20:00:24.455696287Z',.455696287),('2026-10-24T20:00:24.455696Z',.455696),('2026-10-24T20:00:24Z',0),('2026-10-24T21:00:24.455696287+01:00',.455696287)]:
    assert abs(m.epoch(timestamp)-(expected+fraction))<.000001
assert m.epoch('not-a-timestamp') is None and m.epoch(None) is None
with tempfile.TemporaryDirectory(prefix='d2-unstarted-prepare-') as temporary:
    driver=object.__new__(m.Conductor);driver.out=Path(temporary)
    driver.b=json.loads((DATA/'binding-final-g2-eligible.json').read_text())
    m.save(driver.out/'lifecycle.json',dict(stage='PREPARING',instances=[],preparationResults=[]))
    m.save(driver.out/'prepare-empty-route-settings-ack.json',actual_ack)
    receipts=[json.loads(line) for line in (actual_root/'settings-write-receipts.jsonl').read_text().splitlines() if line.strip()]
    original=[row for row in receipts if row['command']=='prepare-defaults' and row['response']==actual_ack]
    assert len(original)==1
    receipt_path=driver.out/'settings-write-receipts.jsonl'
    receipt_line=json.dumps(original[0])+'\n'
    receipt_path.write_text(receipt_line)
    writes=[];batches=[];driver.put=lambda *args,**kwargs:writes.append('FORBIDDEN PUT')
    driver.settings=lambda:actual_settings
    driver.state=lambda:actual_empty
    driver.idle=lambda _:True
    driver.prepare_public_batch=lambda s,l:batches.append((s,l))
    driver.continue_unstarted_prepare()
    assert len(batches)==1 and batches[0][0] is actual_empty and writes==[]
    proof=json.loads((driver.out/'continue-original-empty-route-publication-proof.json').read_text())
    assert proof['originalCompletion']==actual_ack and proof['currentAlreadyReadyAfterDeadline'] is True
    receipt_path.write_text(receipt_line*2)
    try:driver.continue_unstarted_prepare()
    except RuntimeError as error:assert 'single preparation declaration' in str(error)
    else:raise AssertionError('Duplicate declaration receipts were accepted')
    receipt_path.write_text(receipt_line)
    unstarted=json.loads((driver.out/'lifecycle.json').read_text())
    started=copy.deepcopy(unstarted);started['batchStartedUTC']='fixture-already-started'
    (driver.out/'lifecycle.json').write_text(json.dumps(started))
    try:driver.continue_unstarted_prepare()
    except RuntimeError as error:assert 'never started' in str(error)
    else:raise AssertionError('Already-started batch was repeated')
    (driver.out/'lifecycle.json').write_text(json.dumps(unstarted))
    (driver.out/('prepare-'+driver.b['selected'][0]['node']['name']+'-request.json')).write_text('{}')
    try:driver.continue_unstarted_prepare()
    except RuntimeError as error:assert 'evidence already exists' in str(error)
    else:raise AssertionError('Existing native RPC request was repeated')
    assert len(batches)==1 and writes==[]
print('continuation PASS: independent unstarted fixture with actual original ACK/currentempty adopted after deadline; batch once and PUT0; already-started/request-present refused; RFC3339 precision/offset preserved; live API/batch=0')
print('route publication PASS: actual old-positive->empty; same194 residual/no-reset; conflict/foreign refusal; already-ready read-only PUT0; explicit E5 scope/duration fixture; API/load/build=0')

#!/usr/bin/env python3
"""Main068 local saved-evidence stage regression; no API/prepare/policy/load."""
import copy
import importlib.util
import json
from pathlib import Path
ROOT=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('d2_default_stage_regression',ROOT/'conduct_before.py')
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
DATA=Path(m.CONFIG['paths']['d2State'])
OUT=DATA/'autonomous-before-final-g2'
driver=object.__new__(m.Conductor)
driver.b=json.loads((DATA/'binding-final-g2-eligible.json').read_text())
life=json.loads((OUT/'original-prepare-failure/lifecycle.json').read_text())
state=json.loads((OUT/'original-prepare-failure/prepared-defaults-last-state.json').read_text())
assert driver.defaults_match(state,life),'Actual exact8 owned/unrouted defaults must satisfy preparation only'
temporary=copy.deepcopy(life);temporary['stage']='DEFAULTS_ONLY_NOT_LOAD_READY'
try:driver.verify(temporary)
except RuntimeError as error:assert 'producer must not start' in str(error)
else:raise AssertionError('Temporary default stage incorrectly permits load')
# Even a forged lifecycle stage cannot replace served-workload/route proof.
temporary['stage']='READY_BOUND';driver.state=lambda:state;driver.idle=lambda _:True
clock=[0.0];original_monotonic=m.time.monotonic;original_sleep=m.time.sleep
m.time.monotonic=lambda:clock[0]
m.time.sleep=lambda seconds:clock.__setitem__(0,clock[0]+seconds)
try:
    try:driver.verify(temporary)
    except RuntimeError as error:assert 'Static routing ACK did not reach bound weights within194s' in str(error)
    else:raise AssertionError('Unrouted instance incorrectly passes post-route load guard')
finally:
    m.time.monotonic=original_monotonic;m.time.sleep=original_sleep
wrong=copy.deepcopy(state);wrong['snapshot']['instances'][0]['ownerWorkloadUID']='not-the-target'
assert not driver.defaults_match(wrong,life),'Foreign owner must remain blocked'
wrong=copy.deepcopy(state);wrong['snapshot'].setdefault('routing',{})[m.UID]={life['instances'][0]['id']:1}
assert not driver.defaults_match(wrong,life),'Early route must not satisfy temporary-default stage'
print('stage regression PASS: actual owned/unrouted8 accepted only for defaults; default and forged-ready load rejected; foreign-owner/early-route blocked; API/prepare/policy/load=0')

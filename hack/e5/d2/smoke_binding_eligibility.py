#!/usr/bin/env python3
"""Main-authorized small offline regression; temporary fixture variants, no kappa/API/load."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace

ROOT=Path(__file__).resolve().parent
CONFIG=json.loads(Path(os.environ['CHILL_EXPERIMENT_CONFIG']).read_text())
DATA=Path(CONFIG['paths']['d2State'])
INPUT=DATA/'native-reference-modes-20261024T173624Z/actual-reference-mode-inputs'
spec=importlib.util.spec_from_file_location('d2_binding_regression',ROOT/'bind_before.py')
binder=importlib.util.module_from_spec(spec);spec.loader.exec_module(binder)
args=SimpleNamespace(scheduler=str(INPUT/'scheduler.json'),capacity=str(DATA/'rows-before-e18ac-final-g2.json'),nodes=str(INPUT/'nodes.json'),pods=str(INPUT/'control-pods.json'))
base=binder.bind(args)
assert not base['errors'] and len(base['selected'])==8,base['errors']
identity=lambda result:[(x['node']['uid'],x['profileID'],x['maxBatch'],x['kappa']) for x in result['selected']]
original=json.loads(Path(args.capacity).read_text())

def rows(document):return document if isinstance(document,list) else document['rows']

with tempfile.TemporaryDirectory(prefix='d2-binding-regression-') as temporary:
    fixture=Path(temporary)/'capacity.json'
    args.capacity=str(fixture)
    unknown=copy.deepcopy(original)
    unselected=next(x for x in rows(unknown) if x['status']=='CapacityRejected')
    unselected.update(status='Pending',reason='test-only unselected unknown fixture')
    fixture.write_text(json.dumps(unknown))
    result=binder.bind(args)
    assert not result['errors'] and len(result['selected'])==8 and identity(result)==identity(base),result['errors']
    assert any(x.get('capacityStatus')=='Pending' and 'CapacityEligibilityPendingOrUnknown' in x['reasons'] for x in result['exclusions'])
    missing=copy.deepcopy(original)
    kept=[x for x in rows(missing) if x['input']['node']['name']!=CONFIG['d2']['bindingMissingNode']]
    if isinstance(missing,list):missing=kept
    else:missing['rows']=kept
    fixture.write_text(json.dumps(missing))
    result=binder.bind(args)
    assert len(result['selected'])<8 and any(CONFIG['d2']['bindingMissingNode']+': no eligible positive-kappa candidate' in x for x in result['errors']),result['errors']
    malformed=copy.deepcopy(original);rows(malformed).append(None)
    fixture.write_text(json.dumps(malformed))
    result=binder.bind(args)
    assert any('expected object' in x for x in result['errors']),result['errors']
    nonfinite=copy.deepcopy(original);next(x for x in rows(nonfinite) if x['status']=='Built')['kappa']=float('nan')
    fixture.write_text(json.dumps(nonfinite,allow_nan=True))
    result=binder.bind(args)
    assert any('non-finite JSON number' in x for x in result['errors']),result['errors']

# Existing watch actually emits these two signals; do not fork E5's parser.
spec=importlib.util.spec_from_file_location('d2_identity_regression',ROOT/'conduct_before.py')
control=importlib.util.module_from_spec(spec);spec.loader.exec_module(control)
for row in ({'event':'Pod disappeared'},{'event':'state','reasons':['Pod UID replacement']}):
    result=control.control_alert_decision(row)
    assert result['stop'] and result['classification']=='fatal-control-identity-change',result
print('binding regression PASS: valid8 with excluded unknown; missing required Node blocked; malformed/NaN blocked; 2 actual watcher identity signals STOP; kappa/API/load=0')

#!/usr/bin/env python3
"""Main071 recorded-data unit regression only; no host/process/API collection."""
import ast
import importlib.util
import json
import os
from pathlib import Path
ROOT=Path(__file__).resolve().parent
CONFIG=json.loads(Path(os.environ['CHILL_EXPERIMENT_CONFIG']).read_text())
DATA=Path(CONFIG['paths']['d2State'])
spec=importlib.util.spec_from_file_location('d2_cgroup_counter_source',ROOT/'sample_cgroups.py')
sampler=importlib.util.module_from_spec(spec);spec.loader.exec_module(sampler)
functions=[node for node in ast.parse(sampler.REMOTE).body if isinstance(node,ast.FunctionDef) and node.name in ('text','integer','read_cgroup_counters')]
environment={}
exec(compile(ast.Module(body=functions,type_ignores=[]),'recorded-cgroup-counter-functions','exec'),environment)
results=[]
for node in CONFIG['d2']['telemetryFixtureNodes']:
    path=DATA/'telemetry-reader-capability-070'/(node+'-exact-native-cgroup-mount-and-counters.json')
    document=json.loads(path.read_text());paths={}
    for line in document['procPIDCgroup'].splitlines():
        _,controllers,relative=line.split(':',2)
        for controller in controllers.split(','):paths[controller]=relative
    captured=next(x for x in document['derivedCounterReads'] if x['filesystem']=='cgroup2')
    assert captured['mountRoot']=='/' and captured['mountPoint']=='/sys/fs/cgroup'
    values={value['path']:value.get('text') for value in captured['counters'].values()}
    environment['text']=lambda name,values=values:values.get(name)
    actual=environment['read_cgroup_counters'](paths)
    raw=dict(line.split() for line in captured['counters']['cpu.stat']['text'].splitlines())
    assert actual['cgroup']==captured['processRelative'] and actual['cgroupVersion']==2
    assert actual['cpuUsageNanos']==int(raw['usage_usec'])*1000
    assert actual['nrThrottled']==int(raw['nr_throttled'])
    assert actual['throttledNanos']==int(raw['throttled_usec'])*1000
    assert actual['memoryBytes']==int(captured['counters']['memory.current']['text'])
    results.append({'node':node,'cpuUsageNanos':actual['cpuUsageNanos'],'throttledNanos':actual['throttledNanos'],'memoryBytes':actual['memoryBytes'],'source':str(path)})
# Explicit v1/hybrid fixture: v1 ns remain ns and v1 wins over a hybrid entry.
values={'/sys/fs/cgroup/cpu,cpuacct/fixture/cpu.stat':'nr_throttled 7\nthrottled_time 456\n','/sys/fs/cgroup/cpu,cpuacct/fixture/cpuacct.usage':'123','/sys/fs/cgroup/memory/fixture/memory.usage_in_bytes':'789'}
environment['text']=lambda name:values.get(name)
actual=environment['read_cgroup_counters']({'cpuacct':'/fixture','memory':'/fixture','':'/hybrid'})
assert (actual['cgroupVersion'],actual['cpuUsageNanos'],actual['nrThrottled'],actual['throttledNanos'],actual['memoryBytes'])==(1,123,7,456,789)
environment['text']=lambda _:None
unknown=environment['read_cgroup_counters']({'':'/missing'})
assert all(unknown[key] is None for key in ('cpuUsageNanos','nrThrottled','throttledNanos','memoryBytes'))
print(json.dumps({'result':'PASS','recordedUnified':results,'v1HybridNanosecondsPreserved':True,'missingRequiredCountersRemainUnknown':True,'newCollectionOrAPIOrLoad':0}))

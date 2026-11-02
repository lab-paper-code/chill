#!/usr/bin/env python3
"""Build immutable evaluation inputs from an existing frozen evaluation binding (offline).

build --binding FILE --environment FILE --out DIR --store STORE --prefix KEY
writes bundle.json, objects.json and config-values.json. Provision each object
with conditional create before bundle.json; no network access or upload occurs.
$files/<name> is a bundle-local reference, never a runtime host path.
"""
import argparse
import copy
import json
from pathlib import Path
import shutil

OPERATIONAL = {'timeboxes','tokenSafetyMarginSeconds'}
LEGACY_OPERATIONAL = OPERATIONAL | {'endpoint','caFile','tokenFile','collectorTokenFile','kubeContext','producerResources','producerShards','namespace','failureSelectSnapshotTTLSeconds'}
WORKLOAD_OPERATIONAL = {'producerEndpoint', 'producerTokenFile', 'producerCAFile', 'producerDatasetDir',
                        'deliverySeconds', 'socketSeconds', 'gatewayCapSeconds', 'workers', 'lifetimeSeconds', 'podSpec'}
CONFIG_KEYS = {'approval','inputs','g0Evidence','mSeconds','N','burstLowSeconds','smokeSeconds','workloads',
               'collection','factors','poolShares','burstLowMaximumSeconds','failureInjectionRunbook',
               }
BUNDLE_KEYS = {'schemaVersion','config','environment','files','payload'}


def load(path):
    return json.loads(Path(path).read_text())


def save(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, allow_nan=False)+'\n')


def validate(bundle):
    missing = BUNDLE_KEYS - bundle.keys()
    if missing: raise ValueError('MissingInput: bundle '+','.join(sorted(missing)))
    if set(bundle) != BUNDLE_KEYS or bundle['schemaVersion'] != 'evaluation-inputs.v1alpha1':
        raise ValueError('FrozenInputInvalid: unknown bundle fields or schema')
    if set(bundle['config']) - CONFIG_KEYS: raise ValueError('FrozenInputInvalid: unknown scientific config fields')
    required = {'approval','inputs','g0Evidence','mSeconds','N','burstLowSeconds','smokeSeconds','workloads','collection','factors'}
    if required - bundle['config'].keys(): raise ValueError('MissingInput: required scientific config')
    if set(bundle['config']['approval'])-{'sources','controlImageDigest','runtimeDigests'}:
        raise ValueError('FrozenInputInvalid: unknown approval fields')
    if set(bundle['config']['collection'])-{'energyCoverage'}:
        raise ValueError('FrozenInputInvalid: unknown scientific collection fields')
    input_keys={'nodes','scheduler','capacity','capacityScheduler','ranking','d25','costs','bias','pods','gateway','producer','powerObservation','evaluationPayload','decisions','maximumIdleNanosPerRequest'}
    if set(bundle['config']['inputs'])-input_keys:
        raise ValueError('FrozenInputInvalid: unknown scientific input fields')
    workload_keys={'name','uid','generation','slo','R','datasetDir','fields','jobTemplate','profilingRecordIDs','profilingRecordIDSource','profilingEvaluationSeparation','capacityDefinitionProvenance','definitionID','seed','arrivals'}
    for workload in bundle['config']['workloads']:
        if set(workload)-workload_keys: raise ValueError('FrozenInputInvalid: unknown scientific workload fields')
    if set(bundle['environment']) != {'nodes','excludedNodes','producerNode','d25Manifest','operationalRecipe','thermalSlackNodes','runtimeExclusionNote'}:
        raise ValueError('FrozenInputInvalid: environment fields')
    def paths(value):
        if isinstance(value, dict):
            for key, child in value.items():
                if key in LEGACY_OPERATIONAL | WORKLOAD_OPERATIONAL: raise ValueError('FrozenInputInvalid: operational field '+key)
                paths(child)
        elif isinstance(value, list):
            for child in value: paths(child)
        elif isinstance(value, str) and value.startswith('/'):
            raise ValueError('FrozenInputInvalid: absolute host path in scientific config')
    paths(bundle['config'])
    paths(bundle['environment'])
    for name, ref in bundle['files'].items():
        if Path(name).is_absolute() or '..' in Path(name).parts or not isinstance(ref, dict) or set(ref) != {'store','key','size'} or ref['size'] < 1:
            raise ValueError('FrozenInputInvalid: named file reference')
    return bundle


def build(binding_path, environment_path, out, store, prefix):
    if out.exists(): raise ValueError('Output exists; no overwrite')
    binding, environment = load(binding_path), load(environment_path)
    config = copy.deepcopy(binding['config'])
    operational = {key: config.pop(key) for key in list(config) if key in OPERATIONAL}
    for key in LEGACY_OPERATIONAL-OPERATIONAL: config.pop(key,None)
    operational.get('timeboxes',{}).pop('nativeFutureAllowanceSeconds',None)
    operational['workloads'] = {}
    templates={}
    for workload in config['workloads']:
        operational['workloads'][workload['name']] = {key:workload.pop(key) for key in list(workload) if key in WORKLOAD_OPERATIONAL}
        template=load(workload['jobTemplate']);pod=copy.deepcopy(template['spec']['template']['spec'])
        identities={group:[{'name':container['name'],'image':container['image']} for container in pod.get(group,[])] for group in ('containers','initContainers')}
        for group in identities:
            for container in pod.get(group,[]): container.pop('image')
        operational['workloads'][workload['name']]['podSpec']=pod
        templates[Path(workload['jobTemplate'])]={'apiVersion':'batch/v1','kind':'Job','metadata':{},'spec':{'template':{'spec':identities}}}
    for key in ('stages','seeds','arrivals','d25Acceptance','g0Gate','generatorQuality','smoke','harnessRepositoryCommit','tokenLifetime','harnessSHA','condition','canonicalDefinitionsReused','demoProvenance','demoSkipped'): config.pop(key, None)
    collection = config['collection']
    operational['collection'] = {key:collection[key] for key in ('maximumDurationSeconds',) if key in collection}
    config['collection'] = {key:collection[key] for key in ('energyCoverage',) if key in collection}
    # Sources are scientific; runtime executable paths are supplied by the image.
    out.mkdir(parents=True)
    roots = [(Path(environment['paths']['ops']), 'evidence'), (Path(environment['paths']['repository']), 'source')]
    files, objects, visiting = {}, [], set()
    def freeze(value, metadata=False):
        if isinstance(value, dict):
            if value.get('schema') in ('e5-binding.v1','demo-e5-binding.v1'):
                value={**{key:value[key] for key in ('schema','schedule','payload','schedulerSettings') if key in value},'demoConfig':{'measurementSLO':value.get('demoConfig',{}).get('measurementSLO')},'config':{'workloads':[{key:workload[key] for key in ('name','uid')} for workload in value['config']['workloads']]}}
            return {key:freeze(child,metadata) for key,child in value.items() if key not in ('tokenFile','collectorTokenFile','producerTokenFile','caFile','producerCAFile','kubeContext')}
        if isinstance(value, list): return [freeze(child,metadata) for child in value]
        if not isinstance(value,str) or not value.startswith('/'): return value
        path = Path(value)
        if any(part in ('secret','secrets','.token','.token-collector') for part in path.parts):
            if metadata: return value  # Reference only; never copy credential bytes.
            raise ValueError('Secret path in scientific input: '+value)
        root, category = next(((root,category) for root,category in roots if root == path or root in path.parents), (None,None))
        # Captured cgroup/API paths are observations, not local file dependencies.
        if metadata and (root is None or not path.exists() or path.is_dir() or path.stat().st_size==0): return value
        if root is None: raise ValueError('Scientific input outside recorded roots: '+value)
        relative = category+'/'+path.relative_to(root).as_posix()
        marker = '$files/'+relative
        if relative in visiting: return marker
        visiting.add(relative)
        if path.is_dir():
            for child in sorted(path.rglob('*')):
                if child.is_file(): freeze(str(child))
            return marker
        if not path.is_file() or path.is_symlink(): raise ValueError('Missing or symlink scientific file: '+value)
        target = out/'files'/relative
        if path.suffix == '.json':
            document=copy.deepcopy(templates[path]) if path in templates else load(path)
            if path==Path(binding['config']['inputs']['scheduler']):
                document['settings']=copy.deepcopy(binding['schedulerSettings'])
            save(target, freeze(document,metadata=True))
        else:
            target.parent.mkdir(parents=True,exist_ok=True); shutil.copyfile(path,target)
        size = target.stat().st_size
        if not size: raise ValueError('Empty scientific file: '+value)
        ref = {'store':store,'key':prefix.rstrip('/')+'/files/'+relative,'size':size}
        files[relative]=ref;objects.append({**ref,'localPath':str(target.resolve())})
        return marker
    g0=Path(binding['config']['g0Evidence']['binding']).parent/'smoke-G-check'
    for path in (g0/'phase.json',g0/'plc-anomaly-client.jsonl',g0/'raw-source-index.json'):
        freeze(str(path))
    env = {key:environment['evaluation'][key] for key in ('nodes','excludedNodes','producerNode','d25Manifest','thermalSlackNodes','runtimeExclusionNote')}
    recipe=out/'files/adapter-recipe.json';save(recipe,{'schemaVersion':'evaluation-recipe.v1alpha1','sources':config['approval']['sources']})
    files['adapter-recipe.json']={'store':store,'key':prefix.rstrip('/')+'/files/adapter-recipe.json','size':recipe.stat().st_size}
    objects.append({**files['adapter-recipe.json'],'localPath':str(recipe.resolve())})
    env['operationalRecipe']='$files/adapter-recipe.json'
    bundle = {'schemaVersion':'evaluation-inputs.v1alpha1','config':freeze(config),'environment':freeze(env),'files':files,'payload':copy.deepcopy(binding['payload'])}
    validate(bundle)
    save(out/'bundle.json',bundle)
    objects.append({'store':store,'key':prefix.rstrip('/')+'/bundle.json','size':(out/'bundle.json').stat().st_size,'localPath':str((out/'bundle.json').resolve())})
    refs={'payloadManifestRef':{key:objects[-1][key] for key in ('store','key','size')}}
    seed_path=out/'seeds.json';save(seed_path,binding['config'].get('seeds',dict(zip((w['name'] for w in binding['config']['workloads']),(101,202,303)))))
    basis_path=out/'rate-basis.json';shares=binding['config'].get('poolShares',{'MNV':.45,'PLC':.1,'Smol':.45})
    save(basis_path,{w['name']:w['R']*share for w,share in zip(binding['config']['workloads'],(shares['MNV'],shares['PLC'],shares['Smol']))})
    kappa_path=out/'kappa.json';shutil.copyfile(binding['config']['inputs']['capacity'],kappa_path)
    for field,path in (('seedRef',seed_path),('rateBasisRef',basis_path),('kappaRef',kappa_path)):
        reference={'store':store,'key':prefix.rstrip('/')+'/'+path.name,'size':path.stat().st_size}
        objects.append({**reference,'localPath':str(path.resolve())});refs[field]=reference
    save(out/'load-scenario-inputs.json',refs)
    save(out/'objects.json',objects)
    save(out/'config-values.json',{'e5Operational':json.dumps(operational, separators=(',',':'))})


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command',choices=['build'])
    parser.add_argument('--binding',type=Path)
    parser.add_argument('--environment',type=Path)
    parser.add_argument('--round',type=Path)
    parser.add_argument('--attempt',type=Path,help='recorded attempt directory; resolves the binding used by command.json')
    parser.add_argument('--out',required=True,type=Path)
    for flag in ('store','prefix'): parser.add_argument('--'+flag,required=True)
    args=parser.parse_args()
    if args.round:
        round_config=load(args.round)
        args.binding=args.binding or Path(round_config['binding'])
        args.environment=args.environment or Path(round_config['environment'])
    if args.attempt:
        bindings=set()
        for command in sorted(args.attempt.glob('*.command.json')):
            record=load(command);argv=record.get('argv',[]) if isinstance(record,dict) else record
            if '--binding' in argv: bindings.add(argv[argv.index('--binding')+1])
        if len(bindings)!=1: parser.error('attempt must name exactly one recorded binding')
        args.binding=Path(next(iter(bindings)))
    if not args.binding or not args.environment: parser.error('--binding and --environment or --round required')
    build(args.binding,args.environment,args.out,args.store,args.prefix)

if __name__=='__main__': main()

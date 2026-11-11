"""Assemble and publish frozen EvaluationRun evidence."""
import datetime as dt
import json
import os
from pathlib import Path
import subprocess
import tempfile

import input_snapshot
import judgment as judgment_rules

HERE=Path(__file__).resolve().parent
REPO=HERE.parent.parent


def verdict(judgment):
    status=judgment.get('components',{}).get('slo',{}).get('status',judgment['status'])
    if status == 'INVALID': return {'verdict':'INVALID','reason':judgment.get('reason','MeasurementInvalid')}
    if status in ('NOT_EVALUATED','NOT_READY','INCOMPLETE','UNKNOWN','INVALID_NO_LOAD'): return {'verdict':'NOT_EVALUATED','reason':judgment.get('reason','NoLoad' if status=='INVALID_NO_LOAD' else 'ObservationUnavailable')}
    return {'verdict':'FAIL' if status=='FAIL' else 'PASS','reason':'SLOFailed' if status=='FAIL' else 'MeasurementComplete'}


def publish(context):
    tables=input_snapshot.module('evaluation_tables',HERE/'tables.py');stages=[];results={};artifacts=[];procedures={};actual_specs={};stage_outcomes=[]
    prepare_failed=context.execution.get('prepareCompleted') is False
    run_spec=context.spec if prepare_failed else context.snapshot['runSpec']
    templates=[] if prepare_failed else run_spec['stages']
    snapshot_source = None if prepare_failed else context.execution.get('inputSnapshotRef') or context.spec.get('inputSnapshotRef')
    existing_sources = [snapshot_source] if snapshot_source else []
    not_started={'status':'Failed','reason':'NotStarted'}
    def execution_outcome(values):
        executions=[value.get('execution',not_started) for value in values]
        failures=[value for value in executions if value['status']!='Complete']
        return next((value for value in failures if value.get('reason')!='NotStarted'),failures[0] if failures else {'status':'Complete','reason':'ProcedureComplete'})
    publication_prefix='serving/experiments.v1alpha1/'+context.execution['runUID']+'/'
    for template in templates:
        sid=template['id'];scope=context.run/'stages'/sid;result_path=context.run/('publish-result-'+sid+'.json')
        try:
            context.store.get({'store':context.store.name,'key':'experiments/'+context.execution['runUID']+'/stages/'+sid+'/judge/result.json'},result_path)
            result=input_snapshot.load(result_path)
        except FileNotFoundError:
            result={'execution':{'status':'Failed','reason':'NotStarted'},'measurement':{'verdict':'NOT_EVALUATED','reason':'NotStarted'},'artifacts':[]}
        results[sid]=result;artifacts.extend(result['artifacts'])
        procedures[sid]={}
        for step in ('factor','stage','judge','cleanup-jobs'):
            if step=='judge': procedures[sid][step]=result;continue
            path=context.run/('publish-'+sid+'-'+step+'.json')
            try:
                context.store.get({'store':context.store.name,'key':'experiments/'+context.execution['runUID']+'/stages/'+sid+'/'+step+'/result.json'},path)
                value=input_snapshot.load(path);procedures[sid][step]=value;artifacts.extend(value['artifacts'])
            except FileNotFoundError:
                procedures[sid][step]={'execution':{'status':'Failed','reason':'NotStarted'}}
        if (scope/'execution-spec.json').exists(): actual_specs[sid]=input_snapshot.load(scope/'execution-spec.json')
        child=procedures[sid]
        injections=[]
        for plan in template.get('failureInjection',[]):
            injection_path=context.run/('publish-injection-'+sid+'-'+plan['id']+'.json')
            try:
                context.store.get({'store':context.store.name,'key':'experiments/'+context.execution['runUID']+'/stages/'+sid+'/injections/'+plan['id']+'.json'},injection_path)
                evidence=input_snapshot.load(injection_path)
                if evidence['runUID']!=context.execution['runUID'] or evidence['stageID']!=sid: raise ValueError('FrozenInputInvalid: injection publication identity')
                record=evidence['record']
            except FileNotFoundError: record={}
            original=record.get('outcome','NotStarted')
            injection_outcome={'Complete':'Executed','Executed':'Executed','Cancelled':'Cancelled','NotStarted':'Cancelled'}.get(original,'Invalid')
            reason=record.get('reason') or original
            if original!=injection_outcome and reason!=original: reason=original+': '+reason
            injections.append({'id':plan['id'],'action':plan['action'],'outcome':injection_outcome,'reason':reason,'scheduledOffsetSeconds':plan['offsetSeconds'],'actualOffsetSeconds':record.get('actualOffsetSeconds')})
        cleanup=child['cleanup-jobs']
        stage_outcomes.append({'id':sid,'execution':execution_outcome(child.values()),'measurement':result.get('measurement',{'verdict':'NOT_EVALUATED','reason':'NotStarted'}),'cleanup':cleanup.get('cleanup',not_started),'injections':injections})
        phase_path=scope/'measured/phase.json'
        if not phase_path.exists(): continue
        phase=input_snapshot.load(phase_path);judgment=tables.sections(scope/'measured/judgment.json')
        if phase.get('startUTC') and phase.get('endUTC') and judgment:
            bound=context.snapshot['bindings'][sid];requests=tables.publication_requests(judgment)
            definitions={'workloads':context.snapshot['scenarios'][template['loadScenarioRef']['uid']]['workloads']}
            phase_source=next(({key:ref[key] for key in ('store','key')} for ref in artifacts if ref['key'].endswith('/stage/artifacts/measured/phase.json') and '/stages/'+sid+'/' in ref['key']),None)
            judgment_rules.publication_stage_metadata(bound,phase,definitions,
                snapshot_source,
                phase_source,scope/'measured',context.run,requests)
            power=judgment.get('power') or {}
            kpis={'failurePolicy':'v2','status':judgment['status'],'components':judgment['components'],'requests':requests,
                  'wholePoolJoules':power.get('wholePoolJoules') if power.get('complete') else None,
                  'power':power,'powerDrainInclusive':judgment.get('powerDrainInclusive'),'limitations':bound['limitations']}
            kpis.update(judgment_rules.publication_measurements(bound,power))
            stages.append({'id':sid,'from':phase['startUTC'],'to':phase['endUTC'],'kpis':kpis})
    generated=input_snapshot.now();start=min((stage['from'] for stage in stages),default=generated if prepare_failed else context.snapshot['preparedAt']);end=max((stage['to'] for stage in stages),default=generated)
    prefix='serving/experiments.v1alpha1/'+context.execution['runUID']+'/'
    for step in ('prepare','finish'):
        path=context.run/('publish-run-'+step+'.json')
        try:
            context.store.get({'store':context.store.name,'key':'experiments/'+context.execution['runUID']+'/run/'+step+'/result.json'},path)
            value=input_snapshot.load(path);procedures[step]=value;artifacts.extend(value['artifacts'])
        except FileNotFoundError: procedures[step]={'execution':{'status':'Failed','reason':'NotStarted'}}
    if prepare_failed:
        start=procedures['prepare'].get('startedAt',generated)
        end=procedures['finish'].get('finishedAt',generated)
        start_time=dt.datetime.fromisoformat(start);end_time=dt.datetime.fromisoformat(end)
        if end_time<=start_time: end=(start_time+dt.timedelta(microseconds=1)).isoformat()
    execution_path=context.run/'publication-execution.json';input_snapshot.save(execution_path,{'runUID':context.execution['runUID'],'results':results,'procedures':procedures,'stages':actual_specs,'frozenConfig':context.config})
    index_path=context.run/'publication-evidence-index.json';input_snapshot.save(index_path,{'artifacts':artifacts})
    objects=[{'store':context.store.name,'key':prefix+name,'localPath':str(path)} for name,path in (('execution.json',execution_path),('evidence-index.json',index_path))]
    if prepare_failed:
        aggregated={'schema':'e5-summary.v5','failurePolicy':'v2','stages':[],'results':{},'status':'NOT_EVALUATED','reason':procedures['prepare']['execution']['reason']}
    else:
        first_binding=next(iter(context.snapshot['bindings'].values()));input_snapshot.save(context.run/'binding.json',first_binding)
        aggregated=judgment_rules.summarize(None,context.run,evaluation={'stages':stages,'results':results})
    measurement=aggregated['status']
    scenarios=[] if prepare_failed else list(context.snapshot['scenarios'].values())
    definitions={workload['uid']:workload for scenario in scenarios for workload in scenario['workloads']}
    definition={'schema':'evaluation-definition.v1alpha1','run':{'name':context.execution['runName'],'uid':context.execution['runUID']},'spec':run_spec,'loadScenarios':[{'name':s['ref']['name'],'uid':s['ref']['uid'],'spec':s['spec']} for s in scenarios],'workloads':list(definitions.values())}
    finish=procedures['finish']
    children=context.execution['childRefs']
    # Finish re-observes all run-owned Jobs/Pods and restores every settings receipt.
    # Earlier execution loss or cleanup attempts cannot override that final boundary.
    cleanup=finish.get('cleanup',not_started)
    measured=verdict(aggregated)
    outcome={'execution':execution_outcome([procedures['prepare'],finish]+[{'execution':stage['execution']} for stage in stage_outcomes]+[{'execution':child['execution']} for child in children]),'measurement':measured,'cleanup':cleanup,'stages':stage_outcomes}
    residuals=[];seen_refs=set()
    def collect_residuals(value,reason='ResidualState'):
        if not isinstance(value,dict): return
        reason=value.get('reason') or reason
        failed=any(value.get(axis,{}).get('status')=='Failed' for axis in ('execution','cleanup')) or value.get('status')=='Failed'
        for residual in value.get('residuals',[]) if failed else []:
            if residual['kind'] in ('Node','Pod','Job','SchedulerSettings'):
                item={key:residual[key] for key in ('kind','name')};item['reason']=residual.get('reason') or reason
                if item not in residuals: residuals.append(item)
        reference=value.get('residualStateRef')
        if reference and reference['key'] not in seen_refs:
            seen_refs.add(reference['key']);path=context.run/('publication-residual-'+str(len(seen_refs))+'.json')
            context.store.get(reference,path);collect_residuals(input_snapshot.load(path),reason)
        record=value.get('record',{})
        if record.get('outcome') in ('Unknown','Requested'):
            for key,kind in (('nodeRef','Node'),('podRef','Pod')):
                if record.get(key,{}).get('name'):
                    item={'kind':kind,'name':record[key]['name'],'reason':record.get('reason') or 'RemoteEffectUnknown'}
                    if item not in residuals: residuals.append(item)
        for key in ('details','cleanup'):
            collect_residuals(value.get(key),reason)
    collect_residuals(finish)
    outcome['cleanup']={**outcome['cleanup'],'residuals':residuals}
    sources = [{key:obj[key] for key in ('store','key')} for obj in objects + existing_sources]
    summary={'schemaVersion':'serving-experiment.v1alpha1','id':context.execution['runUID'],'from':start,'to':end,'status':measurement,'stages':stages,'sources':sources,'definition':definition,'outcome':outcome}
    summary_path=context.run/'publication-summary.json';input_snapshot.save(summary_path,summary)
    manifest=context.run/'publication-manifest.json';input_snapshot.save(manifest,{'objects':objects,'existingSources':existing_sources,'summary':{'store':context.store.name,'key':prefix+'summary.json','localPath':str(summary_path)}})
    (context.run/'tables.txt').write_text(tables.render(context.run,REPO,evaluation=aggregated))
    private={'logicalStore':context.store.name,'bucket':os.environ['CHILL_EVIDENCE_BUCKET'],'endpoint':os.environ['CHILL_EVIDENCE_ENDPOINT'],'region':os.environ['CHILL_EVIDENCE_REGION'],'accessKeyID':os.environ['AWS_ACCESS_KEY_ID'],'secretAccessKey':os.environ['AWS_SECRET_ACCESS_KEY'],'tempDir':str(context.run.parent.parent/'.evidence-tmp')}
    Path(private['tempDir']).mkdir(mode=0o700,parents=True,exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='evaluation-publish-',dir='/private') as directory:
        credentials=Path(directory)/'config.json';credentials.write_text(json.dumps(private));credentials.chmod(0o600)
        result=subprocess.run([str(REPO/'bin/evaluation-publish'),'--config',str(credentials),'--manifest',str(manifest)],capture_output=True,text=True)
        if result.returncode: raise RuntimeError('PublicationFailed: '+result.stderr.strip())
    context.outputs={'summary':{'store':context.store.name,'key':prefix+'summary.json','size':summary_path.stat().st_size}}

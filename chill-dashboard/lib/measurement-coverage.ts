import type { ExecutionPath, ModelSpec } from "./domain.ts"
import type { BatchStatistics, CampaignResult, InventoryClass, InventoryView, MeasurementPlan, MeasurementPlanSpec, ModelCompatibility, ProfilingRun, ProfilesView, SchedulerProfile } from "./read-types.ts"
import { measurementBatchCompletion, safeMeasurementReturn, significant } from "./measurements.ts"

export type CoverageModel = InventoryView["models"][number] & {spec:ModelSpec}
export const coverageStates = {complete:"측정 완료",partial:"부분 완료",warning:"품질 경고",failed:"실패",running:"진행 중",unmeasured:"미측정",unsupported:"지원 안 함",unknown:"확인 불가"} as const
export type CoverageState = keyof typeof coverageStates
export const stateSymbols:Record<CoverageState,string> = {complete:"✓",partial:"◐",warning:"△",failed:"×",running:"◷",unmeasured:"○",unsupported:"∕",unknown:"?"}
export type Compatibility = {state:"candidate"|"unsupported"|"unknown";reason:string}
export function staticCompatibility(model:Pick<CoverageModel,"spec">, path:ExecutionPath, cls:InventoryClass):Compatibility {
  const artifact = model.spec.artifacts.find(item => item.name === path.artifact)
  if (!artifact?.source.architecture || !cls.architecture) return {state:"unknown",reason:"모델 파일 또는 architecture 선언 미확인"}
  if (artifact.source.architecture !== cls.architecture) return {state:"unsupported",reason:`architecture 불일치: 파일 ${artifact.source.architecture} / 장치 ${cls.architecture}`}
  return {state:"candidate",reason:"선언상 후보 · 실제 runtime 지원 미확인"}
}
export type CompatibilityRead = {node:string;selection?:ModelCompatibility["selection"];data?:ModelCompatibility;error?:string}
export function serverCompatibility(fallback:Compatibility, reads:CompatibilityRead[]):Compatibility {
  if (!reads.length) return fallback
  const reason=reads.map(read=>`${read.node}: ${read.error??read.data?.reasons.map(reason=>`${reason.message} (${reason.code})`).join(" / ")??"미조회"}`).join(" · ")
  if (reads.some(read=>read.error||!read.data)) return {state:"unknown",reason:`지원 확인 불가 — ${reason}`}
  if (reads.every(read=>read.data?.verdict==="Compatible")) return {state:"candidate",reason:"정적 실행 지원 확인 · 측정 완료·Ready·admission 보장 아님"}
  if (reads.every(read=>read.data?.verdict==="Incompatible")) return {state:"unsupported",reason:`지원 안 함 — ${reason}`}
  return {state:"unknown",reason:`지원 확인 불가 — ${reason}`}
}
export type CompatibilityMatrix = {readAt:string;scope:string;rows:ModelCompatibility[]}
export function matrixCompatibility(model:CoverageModel,path:ExecutionPath,cls:InventoryClass,rows:ModelCompatibility[],node?:string):Compatibility {
  const declaration=staticCompatibility(model,path,cls)
  if(declaration.state==="unsupported") return declaration
  const row=rows.find(row=>row.selection.modelSpec===model.name&&row.selection.executionPath===path.name&&row.selection.deviceClass===cls.name)
  if(!row||row.inputs.modelSpec.uid!==model.uid||row.inputs.modelSpec.resourceVersion!==model.resourceVersion||row.inputs.deviceClass.uid!==cls.uid||row.inputs.deviceClass.resourceVersion!==cls.resourceVersion||!cls.nodes.some(n=>n.uid===row.inputs.node.uid)||node&&row.selection.node!==node) return {state:"unknown",reason:"현재 선언 또는 선택 Node의 실행 지원 미확인"}
  return serverCompatibility({state:"unknown",reason:"실행 지원 미확인"},[{node:row.selection.node,data:row}])
}
export function ownedRuns(runs:ProfilingRun[],parent:{namespace:string;uid:string}) {
  return runs.filter(run=>run.namespace===parent.namespace&&run.ownerReferences?.some(owner=>owner.controller===true&&owner.apiVersion==="edge.dacs.io/v1alpha1"&&owner.kind==="BatchEnergyOptimization"&&owner.uid===parent.uid))
}
export function runAttempt(run:ProfilingRun) {
  const raw=run.labels?.["edge.dacs.io/attempt"]
  return raw&&/^[1-9]\d*$/.test(raw)&&Number.isSafeInteger(Number(raw))?`${raw}회차`:"회차 기록 없음"
}
export function statisticsMissing(reason:CampaignResult["statisticsReason"]) {
  return reason==="RetainedEvidenceMissing"?"통계 원근거 기록 없음":reason==="StatisticsNotRecorded"?"CI·통합 p95 기록 없음":reason==="NoPublishedResult"?"게시된 결과 없음":"CI 미제공 · p95 미제공"
}
export function batchStatisticsText(row:BatchStatistics) {
  const mean=row.incrementalWorkJoulesPerItem,c=row.confidence
  const valid=Number.isFinite(mean)&&Number.isFinite(c.halfWidth)&&c.halfWidth>=0&&Number.isFinite(c.level)&&c.level>0&&c.level<1&&(c.varianceOfMean===undefined||(Number.isFinite(c.varianceOfMean)&&c.varianceOfMean>=0))
  return {
    ci:valid?`${significant(c.level*100)}% CI [${significant(mean-c.halfWidth)}, ${significant(mean+c.halfWidth)}] J/건`:"CI 품질 경고",
    p95:Number.isFinite(row.latencyP95Milliseconds)&&row.latencyP95Milliseconds>=0&&Number.isSafeInteger(row.latencySampleCount)&&row.latencySampleCount>0?`통합 p95 ${significant(row.latencyP95Milliseconds)} ms/호출 · 호출 표본 N ${row.latencySampleCount}`:"p95 품질 경고",
    trials:`독립 active ${row.activeTrialCount} / 공유 idle ${row.idleTrialCount} Run`,
  }
}
export function stableCondition(value:unknown):string {
  return JSON.stringify(value, (_key,item) => typeof item === "bigint" ? item.toString() : item && typeof item === "object" && !Array.isArray(item) ? Object.fromEntries(Object.entries(item).sort(([a],[b]) => a.localeCompare(b))) : item)
}
export function profileVariant(profile:SchedulerProfile) {
  const {name:_name,plannedBatchSizes:_batches,...condition} = profile.condition
  void _name; void _batches
  return stableCondition({condition,metricSource:profile.metricSource})
}
export function planVariant(spec:MeasurementPlanSpec) {
  return stableCondition({state:spec.state,cpuQuota:spec.cpuQuota,cpuAffinity:spec.cpuAffinity,activeProtocol:spec.activeProtocol,idleProtocol:spec.idleProtocol})
}
export type RunSpec = {candidate?:MeasurementPlanSpec["state"]["candidate"];targetNode?:string;cpuRequest?:string;measurement?:{batch?:number;cpuQuota?:string;cpuAffinity?:number[];workloadMode?:string;dataset?:string;[key:string]:unknown};metricObservation?:unknown}
export const runSpec = (run:ProfilingRun) => run.spec as RunSpec
export function runGroupKey(run:ProfilingRun) {
  const spec = runSpec(run), {batch:_batch,...measurement} = spec.measurement ?? {}
  void _batch
  return stableCondition({namespace:run.namespace,candidate:spec.candidate,targetNode:spec.targetNode,cpuRequest:spec.cpuRequest,measurement,metricObservation:spec.metricObservation})
}
export function currentRunCondition(run:ProfilingRun,type:string) {
  return run.status.conditions?.find(condition => condition.type === type && condition.observedGeneration === run.generation)
}
export function runEvidenceState(run:ProfilingRun):CoverageState {
  const execution = currentRunCondition(run,"ExecutionComplete"), evidence = currentRunCondition(run,"EvidenceVerified")
  if (execution?.status === "False") return "failed"
  if (execution?.status === "Unknown") return "running"
  if (execution?.status === "True") return evidence?.status === "True" && !!run.status.evidence ? "complete" : "warning"
  return "unknown"
}
export type TrialReference = {run:{namespace:string;name:string;uid:string};measurement?:{batch?:number;workloadMode?:string};batch?:number;attempt?:number;[key:string]:unknown}
export function exactRunTrial(run:ProfilingRun,result:unknown):{trial:TrialReference;excluded:boolean}|undefined {
  if (!result || typeof result !== "object") return
  const body = result as {completedTrials?:TrialReference[];excludedTrials?:TrialReference[]}
  for (const [trials,excluded] of [[body.completedTrials,false],[body.excludedTrials,true]] as const) {
    const trial = trials?.find(item => item.run.namespace === run.namespace && item.run.name === run.name && item.run.uid === run.uid)
    if (trial) return {trial,excluded}
  }
}
function location(value:unknown):string|undefined {
  if (!value || typeof value !== "object") return
  const {store,key} = value as {store?:string;key?:string}
  return store && key ? stableCondition({store,key}) : undefined
}
export const profileForPlan = (plan:MeasurementPlan,profiles:SchedulerProfile[]) => profiles.filter(profile => !!location(plan.status?.result) && location(plan.status?.result) === location(profile.reference))
export function relatedPlans(plan:MeasurementPlan,plans:MeasurementPlan[]) {
  const candidate = plan.spec.state.candidate
  return plans.filter(other => other.metadata.uid !== plan.metadata.uid && other.spec.state.candidate.modelSpec === candidate.modelSpec && other.spec.state.candidate.deviceClass === candidate.deviceClass && other.spec.state.candidate.executionPath === candidate.executionPath && other.spec.state.candidate.powerMode === candidate.powerMode && other.spec.state.targetNode === plan.spec.state.targetNode).map(other => ({plan:other,sameRequestedCondition:planVariant(other.spec) === planVariant(plan.spec),frozenIdentityConfirmed:!!location(plan.status?.result) && location(plan.status?.result) === location(other.status?.result)}))
}
export type CoverageCell = {key:string;model:CoverageModel;path:ExecutionPath;cls:InventoryClass;mode:string;plans:MeasurementPlan[];profiles:SchedulerProfile[];runs:ProfilingRun[];state:CoverageState;reason:string;compatibility:Compatibility;variants:number;completeNodes:number;nodes:number;batches:number[];notes:string[]}
type Sources = {inventory?:InventoryView;plans?:MeasurementPlan[];catalog?:ProfilesView;runs?:ProfilingRun[];matrix?:ModelCompatibility[];complete:boolean;runsComplete:boolean;node?:string;evidenceIssues?:Record<string,string>}
export function coverageReady(queries:{data:unknown;error:unknown;isFetching:boolean}[]) {
  return queries.every(query=>query.data!==undefined&&!query.error&&!query.isFetching)
}
export function coverageOverview(cells:CoverageCell[]) {
  const groups=new Map<string,{key:string;model:CoverageModel;cls:InventoryClass;cells:CoverageCell[];complete:number;eligible:number;failed:number}>()
  for(const cell of cells) {
    const key=stableCondition([cell.model.uid,cell.cls.uid])
    let group=groups.get(key)
    if(!group) {group={key,model:cell.model,cls:cell.cls,cells:[],complete:0,eligible:0,failed:0};groups.set(key,group)}
    group.cells.push(cell)
    if(cell.compatibility.state==="candidate") group.eligible++
    if(cell.state==="complete"&&cell.compatibility.state==="candidate") group.complete++
    if(cell.state==="failed") group.failed++
  }
  return [...groups.values()]
}
export function coverageFailureCounts(cells:CoverageCell[],plans:MeasurementPlan[]) {
  return {cells:cells.filter(cell=>cell.state==="failed").length,requests:new Set(cells.flatMap(cell=>cell.plans.filter(plan=>plan.status?.phase==="Failed").map(plan=>plan.metadata.uid))).size,totalRequests:plans.filter(plan=>plan.status?.phase==="Failed").length}
}
export function coverageCellSummary(cell:CoverageCell) {
  const batches=cell.profiles.flatMap(profile=>(profile.batches??[]).map(batch=>({...batch,cpu:profile.condition.cpu.limit}))).filter(batch=>Number.isFinite(batch.energyPerInputJoules)&&batch.energyPerInputJoules>=0)
  const best=batches.reduce<typeof batches[number]|undefined>((best,batch)=>!best||batch.energyPerInputJoules<best.energyPerInputJoules?batch:best,undefined)
  const requested=[...new Set(cell.plans.flatMap(plan=>plan.spec.batchSizes??[]))]
  const repetitions=[...new Set(cell.plans.map(plan=>plan.spec.trialsPerBatch).filter(value=>value!==undefined))]
  const cpus=[...new Set(cell.profiles.map(profile=>profile.condition.cpu.limit).concat(cell.plans.flatMap(plan=>plan.spec.cpuQuota?[plan.spec.cpuQuota]:[])))]
  return [best?`최저 @배치 ${best.batch}: ${significant(best.energyPerInputJoules)} J/건`:null,cell.batches.length?`측정 배치 ${cell.batches.join(", ")}${requested.length?` (${cell.batches.filter(batch=>requested.includes(batch)).length}/${requested.length})`:""}`:null,repetitions.length?`반복 ${repetitions.join(", ")}회`:null,cpus.length?`CPU ${cpus.join(", ")}`:null].filter(Boolean).join(" / ")
}
export function coverageCells(source:Sources):CoverageCell[] {
  if (!source.inventory) return []
  const plansByKey = new Map<string,MeasurementPlan[]>(), profilesByKey = new Map<string,SchedulerProfile[]>(), runsByKey = new Map<string,ProfilingRun[]>()
  const key = (m:string,p:string,c:string,mode:string) => stableCondition([m,p,c,mode])
  function add<T>(map:Map<string,T[]>,k:string,value:T) { const rows = map.get(k); if (rows) rows.push(value); else map.set(k,[value]) }
  for (const plan of source.plans ?? []) {const c=plan.spec.state.candidate;if (!source.node || source.node === plan.spec.state.targetNode) add(plansByKey,key(c.modelSpec,c.executionPath,c.deviceClass,c.powerMode),plan)}
  for (const profile of source.catalog?.profiles ?? []) {const c=profile.condition;if (!source.node || profile.nodeName === source.node) add(profilesByKey,key(c.logicalModel,c.artifact.executionPath,c.deviceClass,c.powerMode),profile)}
  for (const run of source.runs ?? []) {const s=runSpec(run),c=s.candidate;if (c && (!source.node || s.targetNode === source.node)) add(runsByKey,key(c.modelSpec,c.executionPath,c.deviceClass,c.powerMode),run)}
  const result:CoverageCell[]=[]
  for (const raw of source.inventory.models) {
    const model = raw as CoverageModel
    for (const path of model.spec.executionPaths ?? []) for (const cls of source.inventory.classes) {
      if (source.node && !cls.nodes.some(node => node.name === source.node)) continue
      for (const mode of cls.powerModes) {
        const k=key(model.name,path.name,cls.name,mode.name), plans=plansByKey.get(k)??[], profiles=profilesByKey.get(k)??[], runs=runsByKey.get(k)??[]
        const nodes=cls.nodes.filter(node => !source.node || node.name === source.node)
        const compatibility=source.matrix?matrixCompatibility(model,path,cls,source.matrix,source.node):staticCompatibility(model,path,cls), notes:string[]=[]
        const completeNodes=nodes.filter(node => profiles.some(profile => profile.condition.nodeUID === node.uid)).length
        const issues=(source.catalog?.insufficiencies??[]).filter(issue => issue.code !== "ReplacementSelection" && plans.some(plan => issue.source.Namespace === plan.metadata.namespace && issue.source.Name === plan.metadata.name))
        const replaced=new Set((source.catalog?.insufficiencies??[]).filter(issue => issue.code === "ReplacementSelection").map(issue => `${issue.source.Namespace}/${issue.source.Name}`))
        const latestProfile = profiles.reduce((at,p) => p.measuredAt > at ? p.measuredAt : at,"")
        const unresolvedPlans=plans.filter(plan => !replaced.has(`${plan.metadata.namespace}/${plan.metadata.name}`) && !profileForPlan(plan,profiles).length && !(plan.status?.phase === "Failed" && plan.metadata.creationTimestamp < latestProfile && plans.some(other => profileForPlan(other,profiles).length && planVariant(other.spec) === planVariant(plan.spec))))
        const failures=plans.filter(plan => plan.status?.phase === "Failed")
        const running=plans.some(plan => plan.status?.phase === "Measuring") || runs.some(run => runEvidenceState(run) === "running")
        const numericWarning=profiles.some(profile => (profile.batches??[]).some(batch => !Number.isFinite(batch.energyPerInputJoules) || batch.energyPerInputJoules < 0 || batch.durationNanos?.some(value => !Number.isFinite(value) || value < 0)))
        const runWarning=runs.some(run => ["warning","unknown"].includes(runEvidenceState(run)) && run.status.conditions?.some(condition=>condition.type==="ExecutionComplete"&&condition.status==="True"))
        const partialBatches=plans.some(plan=>{const linked=profileForPlan(plan,profiles);return linked.length>0&&measurementBatchCompletion(plan.spec.batchSizes,linked.flatMap(profile=>profile.batches?.map(batch=>batch.batch)??[])).partial})
        let state:CoverageState="unknown",reason="상태 조합을 확인할 수 없어요"
        if (!source.complete) reason="필수 목록의 조회 실패 또는 페이지 확인 중 · 알려진 결과만 표시해요"
        else if (!model.uid || !cls.uid || profiles.some(profile => !nodes.some(node => node.uid === profile.condition.nodeUID))) reason="현재 선언과 증거의 Node identity를 확인할 수 없어요"
        else if (compatibility.state === "unsupported" && !profiles.length && !plans.length && !runs.length) {state="unsupported";reason=compatibility.reason}
        else if (compatibility.state === "unsupported" && profiles.length) {state="warning";reason="현재 정적 선언과 저장된 측정 근거가 달라요 · 각각 확인해 주세요"}
        else if (issues.length || numericWarning || runWarning || unresolvedPlans.some(plan => plan.status?.phase === "Complete") || (profiles.length && unresolvedPlans.some(plan => plan.status?.phase !== "Measuring"))) {state="warning";reason=issues[0]?.reason ?? (numericWarning?"연결된 수치가 유효하지 않아요":runWarning?"완료 회차의 검증/근거를 확인해 주세요":"실행 완료와 사용 가능한 근거가 일치하지 않아요")}
        else if (profiles.length&&(partialBatches||completeNodes<nodes.length)) {state="partial";reason=partialBatches?"요청한 배치 중 일부만 측정됐어요":`측정 Node ${completeNodes}/${nodes.length} — 다른 Node는 미측정`}
        else if (profiles.length) {state="complete";reason="현재 사용 가능한 에너지 측정 결과가 있어요"}
        else if (running) {state="running";reason="명시된 진행 상태가 있어요"}
        else if (failures.length || runs.some(run => runEvidenceState(run) === "failed")) {state="failed";reason="실행 실패 이력 · 상세 원문을 확인해 주세요"}
        else if (!plans.length && !runs.length) {if (source.runsComplete) {state="unmeasured";reason="측정 이력 없음 — 실행 지원 판정과 별도"} else reason="이력 확인 중 · Run 전 범위 조회 전"}
        if (failures.length && profiles.length) notes.push(unresolvedPlans.some(plan => plan.status?.phase === "Failed") ? "사용 가능 결과 있음 · 최근/다른 조건 시도 실패" : "실패 이력 · 관련 재측정 결과 있음(동결 identity 일부 미확인)")
        if (running && profiles.length) notes.push("완료 · 재측정 진행")
        if (!source.runsComplete && profiles.length) notes.push("catalog 근거 기준 · 회차 상세 미조회")
        const variants=new Set(profiles.map(profileVariant));for(const plan of unresolvedPlans) variants.add(planVariant(plan.spec))
        const evidenceIssue=source.evidenceIssues?.[stableCondition([model.uid,path.name,cls.uid,mode.name])]
        if(evidenceIssue&&profiles.length) {state="warning";reason=evidenceIssue}
        result.push({key:stableCondition([model.uid,path.name,cls.uid,mode.name]),model,path,cls,mode:mode.name,plans,profiles,runs,state,reason,compatibility,variants:variants.size,completeNodes,nodes:nodes.length,batches:[...new Set(profiles.flatMap(p=>(p.batches??[]).map(b=>b.batch)))].sort((a,b)=>a-b),notes})
      }
    }
  }
  return result
}
export function coverageRequestHref(cell:CoverageCell,namespace:string,node:string,returnTo:string) {
  return `/measurements/new?${new URLSearchParams({namespace,model:cell.model.name,deviceClass:cell.cls.name,...(node?{node}:{}),path:cell.path.name,powerMode:cell.mode,returnTo:safeMeasurementReturn(returnTo)})}`
}
export type MeasurementPrefill = {active:boolean;values:Record<string,string>;errors:string[];returnTo:string;node:string}
export function resolveMeasurementPrefill(params:URLSearchParams,namespace:string,inventory:InventoryView):MeasurementPrefill {
  const keys=["namespace","model","deviceClass","node","path","powerMode","from","uid","fromNamespace","fromUID","returnTo"],errors:string[]=[]
  for(const key of keys) if(params.getAll(key).length>1) errors.push(`${key}: 같은 키가 중복됐어요`)
  const values=Object.fromEntries(keys.map(key=>[key,params.get(key)??""]))
  const active=["model","deviceClass","node","path","powerMode"].some(key=>params.has(key))
  if(values.namespace && values.namespace !== namespace) errors.push(`namespace 접근 범위 불일치: ${values.namespace}`)
  if(values.fromNamespace && values.fromNamespace !== namespace) errors.push(`복사 원본 namespace 접근 범위 불일치: ${values.fromNamespace}`)
  if(active && values.from) errors.push("복사 원본과 조합 초깃값을 함께 사용할 수 없어요")
  const model=inventory.models.find(model=>model.name===values.model) as CoverageModel|undefined,cls=inventory.classes.find(cls=>cls.name===values.deviceClass)
  if(active) {
    if(!model) errors.push(`모델을 찾지 못했어요: ${values.model}`)
    if(!cls) errors.push(`장치 종류를 찾지 못했어요: ${values.deviceClass}`)
    const path=model?.spec.executionPaths.find(path=>path.name===values.path)
    if(!path) errors.push(`경로를 찾지 못했어요: ${values.path}`)
    if(cls && !cls.powerModes.some(mode=>mode.name===values.powerMode)) errors.push(`전력모드를 찾지 못했어요: ${values.powerMode}`)
    if(values.node && !cls?.nodes.some(node=>node.name===values.node)) errors.push(`해당 장치 종류의 Node가 아니에요: ${values.node}`)
    if(model && path && cls) {const verdict=staticCompatibility(model,path,cls);if(verdict.state==="unsupported") errors.push(verdict.reason)}
  }
  return {active,values,errors,returnTo:safeMeasurementReturn(values.returnTo),node:values.node || (cls?.nodes.length===1?cls.nodes[0].name:"")}
}

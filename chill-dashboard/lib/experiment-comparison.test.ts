import * as comparisonHelpers from "./experiment-comparison.ts"
import * as routeHelpers from "./dashboard-routes.ts"
import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { execFileSync } from "node:child_process"
import { object, publishedText, type Experiment, type ExperimentStage } from "./experiments.ts"
import { comparisonDirection, frozenP95SLO, serviceDelta, comparisonRows, difference, formatResult, ratio, relativeEnergy, resultMetric, stageRate, stageSeconds, experimentRows, energyText, differentMeasurementRules } from "./experiment-comparison.ts"
import { definitionStage, decodeExperiment, decodeExperiments, historicalPublicationIDs, experimentOutcome, injectionRows } from "./experiments.ts"
import { campaignScoreboard, stageLabel, definitionPolicy, definitionComparisons, definitionComparisonReason, type ExperimentHeatmapRow } from "./experiment-comparison.ts"
import { selectedExperimentStage } from "./experiments.ts"
import { publishedVerdict, experimentHeatmap, experimentChartData, durationMismatch } from "./experiment-comparison.ts"
import { comparisonValidity } from "./experiment-comparison.ts"
import { createRequire } from "node:module"
import { createElement, type ComponentType, type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as experimentHelpers from "./experiments.ts"
import type { EvaluationDefinition } from "./evaluation-definition.ts"
import * as measurementHelpers from "./measurements.ts"
import * as icons from "lucide-react"
import * as measurementSummary from "./measurement-summary.ts"

const require=createRequire(import.meta.url),transport:Record<string,unknown>={}
const transportCode=transpileModule(readFileSync(new URL("./client-transport.ts",import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS}}).outputText
new Function("require","exports",transportCode)((name:string)=>name==="@/lib/measurement-summary"?measurementSummary:require(name),transport)

test("R18: list facts render without per-item detail requests and absent facts stay unrecorded",()=>{
  const facts={name:"기록된 실험",question:"기록된 질문",stages:[{id:stage.id,row:"기록 행",settingsPatchKeys:["maxReplicas","controls.power","minimumDemand.camera","future.setting"],loadScenarioRef:{name:"시나리오 이름",uid:"load-uid"}},{id:"unchanged",settingsPatchKeys:[]},{id:"unrecorded",settingsPatchKeys:null}],sloSources:[{name:"camera",uid:"camera",generation:9007199254740993n,slo:{percentile:95,targetLatencyNanos:50000000}},{name:"큰 지연",uid:"large-latency",slo:{percentile:95,targetLatencyNanos:9007199254740993n}}],loadSources:[{name:"기록 부하",uid:"load-uid"}]}
  const run=decodeExperiments(publishedText({items:[{...recorded.publication,facts}]})).items[0]
  assert.deepEqual(run.facts,facts)
  assert.equal(run.definition,undefined)
  const source=readFileSync(new URL("../features/experiments-conclusion.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const require=createRequire(import.meta.url),exports={} as {useHistoricalExperiments:(runs:Experiment[])=>unknown;ExperimentFacts:ComponentType<{run:Experiment}>;ExperimentResultRow:ComponentType<{run:Experiment;runs:Experiment[];pending:boolean;error:boolean}>},paths:string[][]=[]
  const modules:Record<string,unknown>={"@/lib/experiment-comparison":comparisonHelpers,"@/lib/dashboard-routes":routeHelpers,"@/lib/experiments":experimentHelpers,"@/lib/measurements":measurementHelpers,"@/lib/client-transport":transport,"@/lib/client":{useChillQueries:(ids:string[])=>{paths.push(ids);return []}},"@/components/ui/button":{Button:({children}:{children?:ReactNode})=>createElement("button",{},children)},"next/link":{__esModule:true,default:({children}:{children?:ReactNode})=>createElement("a",{},children)}}
  new Function("require","exports",compiled)((name:string)=>modules[name]??require(name),exports)
  exports.useHistoricalExperiments([run])
  assert.deepEqual(paths,[[]])
  exports.useHistoricalExperiments([{...run,facts:undefined}])
  assert.deepEqual(paths,[[],[]])
  exports.useHistoricalExperiments([{...run,comparisons:[{left:{historical:{publicationID:"explicit-history",stageID:"old"}},right:{stageID:stage.id},reason:"기록 비교"}]}])
  assert.deepEqual(paths[2],["v1/experiments/explicit-history"])
  const html=renderToStaticMarkup(createElement(exports.ExperimentFacts,{run}))
  for(const value of ["기록된 실험","기록된 질문","복제 수 상한","전력모드","최소 수요 · camera","future.setting","변경 없음","기록 부하","load-uid","시나리오 이름","부하 시나리오 이름","부하 시나리오 UID","SLO 출처 이름","SLO 출처 UID",measurementHelpers.formatMeasurementDuration(50000000),measurementHelpers.formatMeasurementDuration(9007199254740993n)])assert.ok(html.includes(value),value)
  for(const value of ["제어 정책","제어 출처 ID",">전력</th>",">배치</th>","제어요소 끔"])assert.ok(!html.includes(value),value)
  const listHTML=renderToStaticMarkup(createElement(exports.ExperimentResultRow,{run,runs:[run],pending:false,error:false}))
  for(const value of ["기록된 실험","복제 수 상한, 전력모드, 최소 수요 · camera, future.setting","변경 없음","미기록"])assert.ok(listHTML.includes(value),value)
  assert.equal(definitionPolicy(run.stages[0],run),"복제 수 상한, 전력모드, 최소 수요 · camera, future.setting")
  const detailed=withDefinition(definedRun([run.stages[0]]),definition=>{definition.spec.settingsPatch={maxReplicas:2,planSearchStrategy:"v1"};definition.spec.stages[0].settingsPatch={controls:{power:false},planSearchStrategy:"v2"}})
  detailed.facts=run.facts
  const detailHTML=renderToStaticMarkup(createElement(exports.ExperimentFacts,{run:detailed}))
  assert.match(detailHTML,/Run 운영 설정 패치/)
  assert.match(detailHTML,/단계 운영 설정 패치/)
  assert.ok(detailHTML.includes("maxReplicas"))
  assert.ok(detailHTML.includes("controls.power"))
  assert.ok(detailHTML.includes("false"))
  assert.match(detailHTML,/스케줄러 \(planSearchStrategy\)/)
  assert.match(detailHTML,/CHILL v1/)
  assert.match(detailHTML,/CHILL v2/)
  const sloRow=experimentRows({...run.stages[0],kpis:{requests:{camera:{workloadUID:"camera"}}}})[0]
  assert.equal(frozenP95SLO(sloRow,run),null)
  const absent=renderToStaticMarkup(createElement(exports.ExperimentFacts,{run:{...run,facts:undefined}}))
  assert.match(absent,/미기록/)
  const absentValues=Array.from(absent.matchAll(/<td\b[^>]*>(.*?)<\/td>/gs),match=>match[1])
  assert.ok(absentValues.length>0)
  assert.ok(absentValues.every(value=>value==="미기록"))
})
test("R18: settingsPatchKeys distinguish Korean labels, unknown paths, empty, null and missing projections",()=>{
  const policy=(settingsPatchKeys:unknown,include=true)=>definitionPolicy(stage,decodeExperiments(publishedText({items:[{...recorded.publication,facts:{stages:[{id:stage.id,...(include?{settingsPatchKeys}:{})}]}}]})).items[0])
  assert.equal(policy(["maxReplicas","controls.routing","autonomousWorkloads","minimumDemand.camera","excludedNodes","future.setting"]),"복제 수 상한, 요청 분배, CHILL 자동 운영 서비스, 최소 수요 · camera, 계획 제외 장비, future.setting")
  assert.equal(policy([]),"변경 없음")
  assert.equal(policy(null),"미기록")
  assert.equal(policy(undefined,false),"미기록")
  for(const invalid of [["maxReplicas",1],{},false,"maxReplicas"])assert.equal(policy(invalid),"미기록")
  assert.equal(policy(["future.controls.power"]),"future.controls.power")
})

import type { SchedulerObservation } from "./read-types.ts"
import { relayPlan } from "./relay.ts"
import { serverConfig } from "./server-config.ts"
import { parseSchedulerJSON } from "./scheduler.ts"

const recorded=JSON.parse(readFileSync(new URL("./fixtures/experiment-213.json",import.meta.url),"utf8")) as {publication:Experiment;decision:SchedulerObservation}
const demo=JSON.parse(readFileSync(new URL("./fixtures/experiment-demo.json",import.meta.url),"utf8")) as Experiment
const stage=recorded.publication.stages[0]

const evidenceRef={store:"evaluation-archive",key:"frozen/start-state.json",size:128}
const image={image:`registry.invalid/evaluation@sha256:${"a".repeat(64)}`,sourceSHA:"0123456789abcdef"}
type DeclaredPair = [left:string,right:string,row?:string]

// Synthetic EvaluationRun publication; the retained JSON fixtures remain past publications.
function definitionFixture(stages:ExperimentStage[],pairs:DeclaredPair[]=[]) {
  const workload={name:"camera",uid:"camera",generation:1}
  return {
    schema:"evaluation-definition.v1alpha1",
    run:{name:"evaluation-fixture",uid:"evaluation-fixture-uid"},
    spec:{
      question:"같은 부하에서 선언한 제어 구성의 성공률·지연·에너지는 어떻게 다른가?",
      target:{components:[{name:"scheduler",resource:{apiVersion:"apps/v1",kind:"Deployment",namespace:"research",name:"chill-scheduler"}}]},
      harness:{...image},
      loadGenerator:{...image,nodeName:"generator-1",resources:{requests:{memory:"256Mi"},limits:{memory:"256Mi"}},shards:{camera:1},barrier:{maxStartSkewNanos:1000000,minimumReadySamples:1}},
      powerMeasurement:{meters:[{id:"meter-1",sourceIdentity:"lab-meter-1"}],nodes:[{name:"worker-1",uid:"worker-1-uid"}],series:"power_watts",sampling:{intervalNanos:1000000000}},
      judgment:{generatorQuality:{latenessToleranceNanos:1000000,maxExceedingFraction:.01},energyCoverage:{maxGapFraction:.01,maxSingleGapSeconds:30}},
      evidenceStore:"evaluation-archive",
      stages:[...new Map(stages.map(stage=>[stage.id,stage])).values()].map(stage=>{
        const pair=pairs.find(pair=>pair[0]===stage.id||pair[1]===stage.id)
        return {id:stage.id,row:pair?.[2]??pair?.[0]??stage.id,
          loadScenarioRef:{name:"camera-load",uid:"camera-load-uid"}}
      }),
      comparisons:pairs.map(([left,right])=>({left:{stageID:left},right:{stageID:right},reason:`${left}와 ${right}의 선언된 비교`})),
    },
    loadScenarios:[{name:"camera-load",uid:"camera-load-uid",spec:{workloads:[workload],parts:[{id:"steady",durationSeconds:60,rates:{camera:10}}],arrivalDistribution:{camera:"poisson"},seeds:{camera:"0"},datasetRefs:{camera:{name:"evaluation-images",uid:"dataset-uid"}}}}],
    workloads:[{...workload,slo:{percentile:95,targetLatencyNanos:50000000},model:{uid:"model",generation:2},preprocessor:{uid:"preprocessor",generation:3},dataset:"evaluation-images"}],
  }
}

function definedRun(stages:ExperimentStage[],pairs:DeclaredPair[]=[]):Experiment {
  return decodeExperiment(JSON.stringify({
    schemaVersion:"serving-experiment.v1alpha1",id:"evaluation-fixture-uid",
    from:"2026-10-30T00:00:00Z",to:"2026-10-30T01:00:00Z",status:"FAIL",stages,
    sources:[{store:"evaluation-archive",key:"runs/evaluation-fixture-uid/summary.json"},evidenceRef],
    definition:definitionFixture(stages,pairs),
  }))
}

function withDefinition(run:Experiment,change:(definition:EvaluationDefinition)=>void):Experiment {
  const definition=JSON.parse(JSON.stringify(run.definition)) as EvaluationDefinition
  change(definition)
  return decodeExperiment(JSON.stringify({...run,definition}))
}
test("R19: published observed identity joins only its frozen p95 definition and evidence source",()=>{
  const source={store:"store",key:"experiments/run/run/prepare/input-snapshot.json"}
  const definition=definitionFixture([stage])
  definition.workloads[0]={...definition.workloads[0],uid:"historical",generation:1,model:{uid:"model",generation:2},preprocessor:{uid:"preprocessor",generation:3},slo:{percentile:95,targetLatencyNanos:1000000000}}
  const run=decodeExperiment(JSON.stringify({...recorded.publication,definition,sources:[source]}))
  const published={workloadUID:"historical",definitionID:"historical/1/model/2/preprocessor/3",identitySource:source,offered:1,deadlineSuccess:0,actualGatewayIdentities:1,missingReasons:{identity:null}}
  const applied=(value:Record<string,unknown>,owner=run)=>frozenP95SLO(experimentRows({...stage,kpis:{requests:{service:value}}})[0],owner)
  assert.equal(applied(published),1000)
  assert.equal(applied({...published,workloadUID:"wrong"}),null)
  assert.equal(applied({...published,definitionID:"historical/2/model/2/preprocessor/3"}),null)
  assert.equal(applied({...published,identitySource:{...source,key:"different.json"}}),null)
  assert.equal(applied({...published,identitySource:{...source,store:"other"}}),null)
  assert.equal(applied({...published,workloadUID:null,definitionID:null,identitySource:null}),null)
  assert.equal(applied(published,withDefinition(run,d=>d.workloads[0].slo.percentile=90)),null)
  for(const field of ["workloadUID","definitionID","identitySource"]) {
    assert.equal(applied({...published,[field]:null}),null)
    const missing:Record<string,unknown>={...published};delete missing[field]
    assert.equal(applied(missing),null)
  }
})
test("interval selection distinguishes historical left publications with identical stage IDs",()=>{
  const local=definedRun([{...stage,id:"x"},{...stage,id:"y"}],[["x","y"]])
  const historical={...definedRun([{...stage,id:"x"}]),id:"historical"}
  const run=withDefinition(local,d=>d.spec.comparisons!.push({left:{historical:{publicationID:historical.id,stageID:"x"}},right:{stageID:"y"},reason:"historical left"}))
  const rows=experimentHeatmap(run,[run,historical]).filter(row=>row.other)
  assert.ok("experimentIntervalSelection" in comparisonHelpers)
  const select=comparisonHelpers.experimentIntervalSelection
  assert.equal(typeof select,"function")
  const first=select(run,rows[0]),second=select(run,rows[1])
  assert.equal(first.a,run.id)
  assert.equal(second.a,run.id)
  assert.notDeepEqual(first,second)
  const params=new URLSearchParams(second)
  assert.equal(rows.find(row=>Object.entries(select(run,row)).every(([key,value])=>params.get(key)===value)),rows[1])
})

test("definition decoders retain complete Go-shaped definitions and preserve published status and KPI precision",()=>{
  const raw={schemaVersion:"serving-experiment.v1alpha1",id:"evaluation-fixture-uid",from:stage.from,to:stage.to,status:"FAIL",
    stages:[{...stage,id:"candidate",kpis:{status:"INVALID",executionComplete:true,cleanupComplete:false,requests:{camera:{workloadUID:"camera",offered:9007199254740993n,completedLatencyNS:{p95:123456789}}}}}],
    sources:[{store:"evaluation-archive",key:"summary.json"}],definition:definitionFixture([{...stage,id:"candidate"}])}
  const decoded=decodeExperiment(publishedText(raw))
  assert.equal(decoded.definition?.schema,"evaluation-definition.v1alpha1")
  assert.deepEqual(decoded.definition,raw.definition)
  assert.equal(decoded.status,"FAIL")
  assert.deepEqual(decoded.stages,raw.stages)
  assert.equal(experimentRows(decoded.stages[0])[0].original.offered,9007199254740993n)
  const list=decodeExperiments(publishedText({items:[raw,recorded.publication,demo]}))
  assert.deepEqual(list.items[0],decoded)
  assert.equal(list.items[1].definition,undefined)
  assert.equal(list.items[2].definition,undefined)
  assert.deepEqual(list.items[1].stages,recorded.publication.stages)
})

test("past publications and malformed definition envelopes remain 미기록 instead of using stage names or KPIs",()=>{
  for(const original of [recorded.publication,demo]) {
    assert.equal(original.definition,undefined)
    const run=decodeExperiment(publishedText(original))
    assert.equal(run.definition,undefined)
    assert.equal(definitionPolicy(run.stages[0],run),"미기록")
    assert.equal(stageLabel(run.stages[0],run),`${run.stages[0].id} / 미기록`)
    assert.equal(definitionComparisonReason(run),"미기록")
    assert.deepEqual(definitionComparisons(run),[])
    assert.ok(experimentRows(run.stages[0]).every(row=>frozenP95SLO(row,run)===null))
    assert.equal(comparisonValidity(run.stages[0],run.stages[0],run,run).load,"미기록")
    assert.ok(experimentHeatmap(run).every(row=>row.reason==="미기록"&&row.other===null))
  }
  const run=definedRun([{...stage,id:"candidate"},{...stage,id:"baseline"}],[["candidate","baseline"]])
  for(const definition of [null,[],{},false,"evaluation-definition.v1alpha1",{schema:"evaluation-definition.v2"},{schema:"evaluation-definition.v1alpha1"}]) {
    const decoded=decodeExperiment(publishedText({...run,definition}))
    assert.equal(decoded.definition,undefined)
    assert.equal(definitionPolicy(decoded.stages[0],decoded),"미기록")
    assert.equal(definitionComparisonReason(decoded),"미기록")
    assert.deepEqual(definitionComparisons(decoded),[])
    assert.deepEqual(decoded.stages,run.stages)
  }
})

test("definition boundary rejects malformed required downstream fields before any reader uses them",()=>{
  const run=definedRun([{...stage,id:"candidate"},{...stage,id:"baseline"}],[["candidate","baseline"]])
  const invalid:Array<[string,(definition:EvaluationDefinition)=>void]>=[
    ["schema",d=>{object(d).schema="evaluation-definition.v9"}],
    ["run UID",d=>{d.run.uid=""}],
    ["run name",d=>{delete object(d.run).name}],
    ["spec",d=>{object(d).spec=null}],
    ["question",d=>{d.spec.question=""}],
    ["global settings patch",d=>{object(d.spec).settingsPatch=[]}],
    ["load generator",d=>{d.spec.loadGenerator={}}],
    ["generator image",d=>{delete d.spec.loadGenerator.image}],
    ["generator source type",d=>{d.spec.loadGenerator.sourceSHA=5}],
    ["generator resources",d=>{d.spec.loadGenerator.resources=[]}],
    ["generator shards",d=>{d.spec.loadGenerator.shards={camera:0}}],
    ["generator barrier",d=>{object(d.spec.loadGenerator.barrier).minimumReadySamples=0}],
    ["stages",d=>{d.spec.stages=[]}],
    ["stage ID",d=>{d.spec.stages[0].id=""}],
    ["duplicate stage IDs",d=>{d.spec.stages.push({...d.spec.stages[0]})}],
    ["row",d=>{delete object(d.spec.stages[0]).row}],
    ["scenario UID",d=>{d.spec.stages[0].loadScenarioRef.uid=""}],
    ["scenario name",d=>{d.spec.stages[0].loadScenarioRef.name=""}],
    ["unresolved scenario UID",d=>{d.spec.stages[0].loadScenarioRef.uid="missing-load"}],
    ["stage settings patch",d=>{object(d.spec.stages[0]).settingsPatch=[]}],
    ["comparison list",d=>{object(d.spec).comparisons={}}],
    ["comparison reason",d=>{d.spec.comparisons![0].reason=""}],
    ["comparison stage",d=>{d.spec.comparisons![0].left={stageID:"unknown"}}],
    ["comparison reference",d=>{object(d.spec.comparisons![0]).left={}}],
    ["ambiguous reference",d=>{object(d.spec.comparisons![0]).right={stageID:"baseline",historical:{publicationID:"past-id",stageID:"baseline"}}}],
    ["historical publication ID",d=>{object(d.spec.comparisons![0]).right={historical:{publicationID:"",stageID:"baseline"}}}],
    ["historical stage ID",d=>{object(d.spec.comparisons![0]).right={historical:{publicationID:"past-id",stageID:""}}}],
    ["obsolete historical reference",d=>{object(d.spec.comparisons![0]).right={historical:{runUID:"past-uid",stageID:"baseline",resultRef:evidenceRef}}}],
    ["scenarios",d=>{d.loadScenarios=[]}],
    ["duplicate scenario UID",d=>{d.loadScenarios.push({...d.loadScenarios[0]})}],
    ["parts",d=>{d.loadScenarios[0].spec.parts=[]}],
    ["part ID",d=>{d.loadScenarios[0].spec.parts[0].id=""}],
    ["part duration",d=>{d.loadScenarios[0].spec.parts[0].durationSeconds=0}],
    ["missing load",d=>{delete object(d.loadScenarios[0].spec.parts[0]).rates}],
    ["empty rates",d=>{d.loadScenarios[0].spec.parts[0].rates={}}],
    ["negative rates",d=>{d.loadScenarios[0].spec.parts[0].rates={camera:-1}}],
    ["nonnumeric rates",d=>{object(d.loadScenarios[0].spec.parts[0]).rates={camera:"10"}}],
    ["workloads",d=>{d.workloads=[]}],
    ["workload UID",d=>{d.workloads[0].uid=""}],
    ["workload generation",d=>{d.workloads[0].generation=0}],
    ["duplicate workload UID",d=>{d.workloads.push({...d.workloads[0]})}],
    ["workload SLO",d=>{delete object(d.workloads[0]).slo}],
    ["SLO percentile",d=>{d.workloads[0].slo.percentile=101}],
    ["SLO latency",d=>{d.workloads[0].slo.targetLatencyNanos=-1}],
  ]
  for(const [field,change] of invalid) {
    const definition=JSON.parse(JSON.stringify(run.definition)) as EvaluationDefinition
    change(definition)
    const decoded=decodeExperiment(publishedText({...run,definition}))
    assert.equal(decoded.definition,undefined,field)
    assert.equal(definitionPolicy(decoded.stages[0],decoded),"미기록",field)
    assert.equal(definitionComparisonReason(decoded),"미기록",field)
    assert.deepEqual(definitionComparisons(decoded),[],field)
    assert.deepEqual(decoded.stages,run.stages,field)
    const list=decodeExperiments(publishedText({items:[{...run,definition}]}))
    assert.equal(list.items[0].definition,undefined,field)
  }
})

test("settings patches expose recorded key paths with facts present and inherit only absent stage patches",()=>{
  const run=withDefinition(definedRun([{...stage,id:"full-e5-1"},{...stage,id:"static-e5-1"},{...stage,id:"switches"}]),d=>{
    d.spec.stages[2].settingsPatch={controls:{power:false}}
  })
  assert.equal(definitionPolicy(run.stages[0],run),"변경 없음")
  assert.equal(definitionPolicy(run.stages[1],run),"변경 없음")
  assert.equal(definitionPolicy(run.stages[2],run),"전력모드")
  const global=withDefinition(run,d=>{d.spec.settingsPatch={controls:{routing:false}}})
  assert.equal(definitionPolicy(global.stages[0],global),"요청 분배")
  assert.equal(definitionPolicy(global.stages[2],global),"전력모드")
  const empty=withDefinition(global,d=>{d.spec.stages[0].settingsPatch={}})
  assert.equal(definitionPolicy(empty.stages[0],empty),"변경 없음")
  const inherited=withDefinition(global,d=>{d.spec.stages[0].settingsPatch=null})
  assert.equal(definitionPolicy(inherited.stages[0],inherited),"요청 분배")
  const detailed=decodeExperiment(publishedText({...global,facts:{name:"목록 사실",stages:[{id:global.stages[0].id,settingsPatchKeys:["controls.routing"]},{id:global.stages[2].id,settingsPatchKeys:["controls.power"]}]}}))
  assert.equal(definitionPolicy(detailed.stages[0],detailed),"요청 분배")
  assert.equal(definitionPolicy(detailed.stages[2],detailed),"전력모드")
  const projected=decodeExperiment(publishedText({...global,facts:{stages:[{id:global.stages[0].id,settingsPatchKeys:[]},{id:global.stages[2].id,settingsPatchKeys:null}]}}))
  assert.equal(definitionPolicy(projected.stages[0],projected),"변경 없음")
  assert.equal(definitionPolicy(projected.stages[2],projected),"미기록")
})

test("row rename and declaration reordering preserve id-only stage and comparison joins",()=>{
  const run=definedRun([{...stage,id:"candidate"},{...stage,id:"baseline"}],[["candidate","baseline","원래 이름"]])
  const renamed=withDefinition(run,d=>{
    d.spec.stages[0].row="접미사 없는 새 표시 이름"
    d.spec.stages[1].row="완전히 다른 표시 이름"
    d.spec.stages.reverse()
  })
  assert.equal(definitionStage(renamed,renamed.stages[0])?.row,"접미사 없는 새 표시 이름")
  assert.equal(stageLabel(renamed.stages[0],renamed),"candidate / 접미사 없는 새 표시 이름 / 변경 없음")
  assert.deepEqual(definitionComparisons(renamed).map(pair=>[pair.chill.id,pair.control.id]),[["candidate","baseline"]])
  assert.equal(definitionStage(renamed,{...renamed.stages[0],id:"접미사 없는 새 표시 이름"}),undefined)
  assert.equal(definitionPolicy({...renamed.stages[0],id:"unlisted"},renamed),"미기록")
  const noComparison=withDefinition(run,d=>{delete d.spec.comparisons})
  assert.deepEqual(definitionComparisons(noComparison),[])
  assert.equal(definitionComparisonReason(noComparison),"비교 없음")
})


test("target rate is duration-weighted declared direct rates, never observed offered divided by time",()=>{
  const run=withDefinition(definedRun([{...stage,id:"candidate",kpis:{requests:{camera:{offered:999999}}}}]),d=>{
    d.loadScenarios[0].spec.parts=[
      {id:"idle",durationSeconds:10,rates:{camera:0,other:0}},
      {id:"steady",durationSeconds:30,rates:{camera:12,other:4}},
    ]
  })
  assert.equal(stageRate(run.stages[0],run),12)
  assert.equal(stageRate({...run.stages[0],to:"invalid",kpis:{}},run),12)
  const zero=withDefinition(run,d=>{d.loadScenarios[0].spec.parts=[{id:"idle",durationSeconds:10,rates:{camera:0}}]})
  assert.equal(stageRate(zero.stages[0],zero),0)
  assert.equal(stageRate(run.stages[0],recorded.publication),null)
})

test("same-load validity requires frozen scenario UID and the complete load generator, not observed rate similarity",()=>{
  const a={...stage,id:"candidate",kpis:{requests:{camera:{offered:1}}}},b={...stage,id:"baseline",kpis:{requests:{camera:{offered:1000000}}}}
  const run=definedRun([a,b],[["candidate","baseline"]])
  assert.equal(comparisonValidity(a,b,run,run).conditions.load,"같음")
  assert.equal(comparisonValidity(a,b).conditions.load,"미기록")
  const changedScenario=withDefinition(run,d=>{
    d.loadScenarios.push({...d.loadScenarios[0],uid:"other-scenario-uid"})
    d.spec.stages[1].loadScenarioRef.uid="other-scenario-uid"
  })
  assert.equal(comparisonValidity(a,b,changedScenario,changedScenario).conditions.load,"다름")
  const renamedScenario=withDefinition(run,d=>{d.spec.stages[1].loadScenarioRef.name="renamed"})
  assert.equal(comparisonValidity(a,b,renamedScenario,renamedScenario).conditions.load,"같음")
  const changedGenerator=withDefinition(run,d=>{d.spec.loadGenerator.sourceSHA="different-source"})
  assert.equal(comparisonValidity(a,b,run,changedGenerator).conditions.load,"다름")
  for(const sourceSHA of [undefined,""]) {
    const optionalSource=withDefinition(run,d=>{
      if(sourceSHA===undefined)delete d.spec.loadGenerator.sourceSHA
      else d.spec.loadGenerator.sourceSHA=sourceSHA
      d.spec.loadGenerator.image="registry.invalid/evaluation:tag"
    })
    assert.equal(stageRate(a,optionalSource),stageRate(a,run))
    assert.equal(comparisonValidity(a,b,optionalSource,optionalSource).conditions.load,"같음")
  }
})

test("historical comparisons join publication ID and stage ID and carry both definitions through every projection",()=>{
  const make=(id:string,energy:number,p95:number):ExperimentStage=>({id,from:stage.from,to:stage.to,kpis:{wholePoolJoules:energy,measurementRules:{population:"offered"},requests:{camera:{workloadUID:"camera",definitionID:"camera/1/model/2/preprocessor/3",identitySource:evidenceRef,offered:100,deadlineSuccessFraction:1,completedLatencyNS:{p95:p95*1e6}}}}})
  const historical=withDefinition({...definedRun([make("baseline",100,10)]),id:"control-222-223-first-20261030"},d=>{
    d.run={name:"same-display-name",uid:"different-run-uid"}
    d.workloads[0].slo.targetLatencyNanos=80000000
  })
  const run=withDefinition(definedRun([make("candidate",90,20)]),d=>{
    d.run.name="same-display-name"
    d.spec.stages[0].settingsPatch={controls:{power:false}}
    d.spec.comparisons=[{left:{stageID:"candidate"},right:{historical:{publicationID:historical.id,stageID:"baseline"}},reason:"고정 구성과 과거 자율 운영의 선언된 비교"}]
  })
  const decoy={...historical,id:"different-run-uid"}
  const runs=[run,decoy,historical],pair=definitionComparisons(run,runs)[0]
  assert.equal(pair.chill,run.stages[0])
  assert.equal(pair.control,historical.stages[0])
  assert.equal(pair.leftRun,run)
  assert.equal(pair.rightRun,historical)
  assert.equal(pair.reason,run.definition!.spec.comparisons![0].reason)
  assert.deepEqual(historicalPublicationIDs(run),[historical.id])
  assert.deepEqual(historicalPublicationIDs(recorded.publication),[])
  assert.deepEqual(definitionComparisons(run,runs),[pair])
  assert.equal(campaignScoreboard(run,runs).coverageCounts.unpaired,0)
  const heatmap=experimentHeatmap(run,runs)[0]
  assert.equal(heatmap.leftRun,run)
  assert.equal(heatmap.rightRun,historical)
  assert.equal(heatmap.reason,pair.reason)
  assert.equal(heatmap.energyPercent,-10)
  const chart=experimentChartData(run,"camera",runs)[0]
  assert.deepEqual([chart.leftP95,chart.rightP95,chart.leftSLO,chart.rightSLO],[20,10,50,80])
  assert.match(chart.leftLabel,/전력모드/)
  assert.equal(typeof chart.rightLabel,"string")
  assert.match(chart.rightLabel!,/변경 없음/)
  for(const unavailable of [[run],[run,decoy],[run,{...historical,stages:[]}],[run,historical,historical]]) {
    assert.deepEqual(definitionComparisons(run,unavailable),[])
    assert.deepEqual(definitionComparisons(run,unavailable),[])
    assert.match(definitionComparisonReason(run,unavailable),/대상 게시 없음/)
    assert.equal(campaignScoreboard(run,unavailable).coverageCounts.pairs,0)
    assert.match(experimentHeatmap(run,unavailable)[0].reason,/대상 게시 없음/)
    assert.equal(experimentChartData(run,"camera",unavailable)[0].rightSLO,null)
  }
  const reverse=withDefinition(run,d=>{
    const comparison=d.spec.comparisons![0]
    ;[comparison.left,comparison.right]=[comparison.right,comparison.left]
  })
  assert.equal(experimentHeatmap(reverse,[reverse,historical])[0].energyPercent,100*10/90)
  assert.deepEqual(experimentChartData(reverse,"camera",[reverse,historical]).find(row=>row.id==="baseline")?.leftSLO,80)
})

test("list comparisons join local and historical KPI without definitions and do not invent frozen metadata",()=>{
  const detail=definedRun([{...stage,id:"candidate"},{...stage,id:"baseline"}],[["candidate","baseline"]])
  const historical={...recorded.publication,id:"control-222-223-first-20261030"}
  const comparisons=[
    ...detail.definition!.spec.comparisons!,
    {left:{stageID:"candidate"},right:{historical:{publicationID:historical.id,stageID:historical.stages[0].id}},reason:"목록의 과거 비교"},
  ]
  const list=decodeExperiments(publishedText({items:[{...detail,definition:undefined,comparisons},historical]}))
  const run=list.items[0],pairs=definitionComparisons(run,list.items)
  assert.equal(run.definition,undefined)
  assert.deepEqual(run.comparisons,comparisons)
  assert.equal(pairs.length,2)
  assert.equal(pairs[0].chill.id,"candidate")
  assert.equal(pairs[0].control.id,"baseline")
  assert.equal(pairs[1].rightRun,list.items[1])
  assert.equal(pairs[1].control.id,historical.stages[0].id)
  assert.deepEqual(historicalPublicationIDs(run),[historical.id])
  assert.equal(definitionPolicy(run.stages[0],run),"미기록")
  assert.equal(comparisonValidity(pairs[0].chill,pairs[0].control,run,run).conditions.load,"미기록")
  assert.ok(experimentRows(run.stages[0]).every(row=>frozenP95SLO(row,run)===null))
  assert.match(definitionComparisonReason(run,[run]),/대상 게시 없음/)
  const absent=decodeExperiments(publishedText({items:[{...detail,definition:undefined}]})).items[0]
  assert.deepEqual(definitionComparisons(absent),[])
  assert.equal(definitionComparisonReason(absent),"미기록")
  const overridden=decodeExperiment(publishedText({...detail,comparisons:[comparisons[1]]}))
  assert.equal(definitionComparisons(overridden,[overridden,historical]).length,1)
  assert.equal(definitionComparisons(overridden,[overridden,historical])[0].control.id,"baseline")
})

test("outcome preserves independent execution, measurement and cleanup axes at run and stage scope",()=>{
  const base=definedRun([{...stage,id:"candidate"},{...stage,id:"baseline"}])
  const outcome={
    execution:{status:"Complete",reason:"ExecutionComplete"},
    measurement:{verdict:"FAIL",reason:"RequestTargetFailed"},
    cleanup:{status:"Failed",reason:"CleanupFailed"},
    stages:[
      {id:"baseline",execution:{status:"Failed",reason:"NotStarted"},measurement:{verdict:"NOT_EVALUATED",reason:"NotStarted"},cleanup:{status:"Complete",reason:"NoResources"}},
      {id:"candidate",execution:{status:"Complete",reason:"StageComplete"},measurement:{verdict:"INVALID",reason:"CoverageGap"},cleanup:{status:"Failed",reason:"StageCleanupFailed"}},
    ],
  }
  const run=decodeExperiment(publishedText({...base,outcome}))
  assert.deepEqual(run.outcome,outcome)
  assert.deepEqual(decodeExperiments(publishedText({items:[{...base,definition:undefined,outcome}]})).items[0].outcome,outcome)
  assert.deepEqual(experimentOutcome(run),{
    execution:{value:"Complete",reason:"ExecutionComplete"},
    measurement:{value:"FAIL",reason:"RequestTargetFailed"},
    cleanup:{value:"Failed",reason:"CleanupFailed"},
  })
  assert.deepEqual(experimentOutcome(run,run.stages[0]),{
    execution:{value:"Complete",reason:"StageComplete"},
    measurement:{value:"INVALID",reason:"CoverageGap"},
    cleanup:{value:"Failed",reason:"StageCleanupFailed"},
  })
  const missing={execution:{value:"미기록",reason:"미기록"},measurement:{value:"미기록",reason:"미기록"},cleanup:{value:"미기록",reason:"미기록"}}
  assert.deepEqual(experimentOutcome(recorded.publication),missing)
  assert.deepEqual(experimentOutcome(run,{...run.stages[0],id:"absent"}),missing)
  assert.deepEqual(experimentOutcome(decodeExperiment(publishedText({...base,outcome:{execution:outcome.execution}}))),{
    ...missing,execution:{value:"Complete",reason:"ExecutionComplete"},
  })
})

test("injection results join planned IDs despite reordered records and retain unmatched plans and results",()=>{
  const base=withDefinition(definedRun([{...stage,id:"candidate"},{...stage,id:"baseline"}]),d=>{
    d.spec.stages[0].failureInjection=[
      {id:"kill-1",action:"delete-serving-pod",offsetSeconds:120},
      {id:"kill-2",action:"delete-serving-pod",offsetSeconds:180},
    ]
    d.spec.stages.reverse()
  })
  const injections=[
    {id:"orphan",action:"delete-serving-pod",outcome:"Invalid",reason:"NoTarget",scheduledOffsetSeconds:240},
    {id:"kill-1",action:"delete-serving-pod",outcome:"Executed",reason:"PodDeleted",scheduledOffsetSeconds:121,actualOffsetSeconds:121.4},
  ]
  const run=decodeExperiment(publishedText({...base,outcome:{stages:[{id:"baseline",injections:[{id:"kill-2",outcome:"Cancelled"}]},{id:"candidate",injections}]}}))
  assert.deepEqual(injectionRows(run,run.stages[0]),[
    {id:"kill-1",action:"delete-serving-pod",outcome:"Executed",reason:"PodDeleted",scheduledOffsetSeconds:121,actualOffsetSeconds:121.4},
    {id:"kill-2",action:"delete-serving-pod",outcome:"미기록",reason:"미기록",scheduledOffsetSeconds:180,actualOffsetSeconds:null},
    {id:"orphan",action:"delete-serving-pod",outcome:"Invalid",reason:"NoTarget",scheduledOffsetSeconds:240,actualOffsetSeconds:null},
  ])
  assert.deepEqual(injectionRows(base,base.stages[0]).map(row=>[row.id,row.outcome,row.scheduledOffsetSeconds]),[["kill-1","미기록",120],["kill-2","미기록",180]])
  assert.deepEqual(injectionRows(recorded.publication,stage),[])
})

test("S1-1: real demo and #213 publication shapes preserve values, denominators, populations, sources and missing reasons",()=>{
  const rows=experimentRows(stage),image=rows.find(row=>row.name==="image-classification-tight-slo")!
  const success=image.metrics.find(metric=>metric.key==="deadlineSuccessFraction")!
  assert.equal(success.value,0.7957909383424646)
  assert.equal(success.numerator,45754)
  assert.equal(success.denominator,57495)
  assert.match(success.source,/kpis.requests.image-classification-tight-slo.deadlineSuccessFraction/)
  assert.match(String(success.population),/Frozen offered/)
  assert.equal(success.reason,null)
  assert.equal(image.metrics.find(metric=>metric.key==="rawPopulation.uniqueRequests")!.value,57495)
  assert.equal(image.metrics.find(metric=>metric.key==="actualGatewayIdentities")!.value,57493)
  assert.notEqual(image.metrics.find(metric=>metric.key==="offered")!.value,57493)
  const idle=experimentRows(demo.stages[0])[0].metrics.find(metric=>metric.key==="successFraction")!
  assert.equal(idle.value,null)
  assert.equal(idle.denominator,0)
  assert.equal(idle.reason,"No offered requests")
  assert.equal(publishedText(idle.value),"")
  assert.equal(publishedText(0),"0")
  assert.deepEqual(experimentRows({...stage,kpis:{other:{offered:1}}}),[])
  assert.ok(rows.every(row=>row.metrics.every(metric=>!metric.label.includes("라이브"))))
})

test("S1-2: #213 retained energy bound renders ± unchanged; different rule identifiers never hide numbers",()=>{
  assert.equal(energyText(stage),"173062.73965024325 ± 516.5136059839 J")
  assert.equal(energyText(stage,"activePoolJoules"),"148610.0577813059 ± 516.5136059839 J")
  const other:ExperimentStage={...stage,kpis:{...stage.kpis,measurementRules:{energyCoverage:{maxSingleGapSeconds:31,maxGapFraction:.01},evidenceSource:"api"}}}
  assert.deepEqual(differentMeasurementRules(stage,other),["energyCoverage","evidenceSource"])
  assert.equal(energyText(other),energyText(stage))
  assert.deepEqual(differentMeasurementRules(stage,{...stage,kpis:{...stage.kpis,measurementRules:{energyCoverage:{maxSingleGapSeconds:30,maxGapFraction:.01}}}}),[])
  assert.equal(energyText({...stage,kpis:{wholePoolJoules:null,gapErrorBoundJoules:1}}),"")
  assert.equal(energyText({...stage,kpis:{wholePoolJoules:0,gapErrorBoundJoules:0}}),"0 ± 0 J")
})

test("S1-3: retained names, UID and exact definitions stay independent of same-name registration",()=>{
  const old={...stage,kpis:{requests:{"same-name":{uid:"old-uid",definitionID:"old-definition",offered:2,deadlineSuccessFraction:.5,successNumerator:1,successDenominator:2}}}}
  const replacement={...stage,kpis:{requests:{"same-name":{uid:"new-uid",definitionID:"new-definition",offered:0}}}}
  const before=experimentRows(old),after=experimentRows(replacement)
  assert.equal(before[0].uid,"old-uid");assert.equal(before[0].definition,"old-definition")
  assert.equal(after[0].uid,"new-uid");assert.equal(after[0].definition,"new-definition")
  assert.equal(before[0].name,after[0].name)
  // Old actual publications have no identity. Do not invent it from their name.
  assert.equal(experimentRows(stage)[0].uid,undefined)
})


test("S1-5: rendered unbound serving-manager grants measurement reads and occupancy writes, never raw",()=>{
  const rendered=execFileSync("helm",["template","s1-fixture","../charts/chill","--set","servingEntrypoint.enabled=true","--set","servingEntrypoint.tls.secretName=fixture-tls","--set","evidenceStore.connection.bucket=fixture","--set","evidenceStore.connection.region=local"],{encoding:"utf8",maxBuffer:16*1024*1024})
  const roles=rendered.split(/\n---\n/).filter(doc=>/name: s1-fixture-chill-serving-manager\s/.test(doc))
  assert.ok(roles.length>=2)
  const manager=roles.join("\n")
  for(const path of ["/chill/api/v1/experiments","/chill/api/v1/experiments/*","operation-cost-measurements","dispatch-occupancy-measurements"])assert.ok(manager.includes(path),path)
  const grants=Array.from(manager.matchAll(/resources:\s*\[([^\]]+)\]\s+verbs:\s*\[([^\]]+)\]/g),match=>({resources:match[1].split(",").map(value=>value.trim()),verbs:match[2].split(",").map(value=>value.trim())}))
  for(const resource of ["operationcostmeasurements","dispatchoccupancymeasurements"])for(const verb of ["get","list"])assert.ok(grants.some(grant=>grant.resources.includes(resource)&&grant.verbs.includes(verb)),`${verb} ${resource}`)
  for(const verb of ["create","delete"])assert.ok(grants.some(grant=>grant.resources.includes("dispatchoccupancymeasurements")&&grant.verbs.includes(verb)),`${verb} dispatchoccupancymeasurements`)
  assert.doesNotMatch(manager,/observations\/raw/)
  const config=serverConfig({CHILL_BASE_URL:"https://fixture.invalid",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"fixture"})
  for(const path of ["v1/experiments","v1/experiments/control-213-full-sample","v1/namespaces/research/operation-cost-measurements","v1/namespaces/research/dispatch-occupancy-measurements"]){
    assert.equal(relayPlan("GET",path.split("/"),"",config).kind,"query")
    if(path.endsWith("/dispatch-occupancy-measurements"))assert.equal(relayPlan("POST",path.split("/"),"",config).kind,"mutation")
    else assert.throws(()=>relayPlan("POST",path.split("/"),"",config),/DashboardMethodNotAllowed/)
  }
})

test("S3: name matching preserves changed/missing identity and explicit ambiguous candidates",()=>{
  const a:ExperimentStage={id:"A",from:"2026-10-30T00:00:00Z",to:"2026-10-30T00:00:10Z",kpis:{requests:{same:{uid:"old",offered:10},onlyA:{offered:0}}}}
  const b:ExperimentStage={...a,kpis:{requests:{same:{uid:"new",offered:20},onlyB:{offered:0}},workloads:{same:{offered:20}}}}
  const rows=comparisonRows(a,b)
  assert.equal(rows.length,3)
  assert.equal(rows[0].a[0].uid,"old")
  assert.equal(rows[0].b[0].uid,undefined)
  assert.equal(rows[0].b[1].uid,"new")
  assert.equal(rows[0].b.length,2)
  assert.equal(rows.find(row=>row.name==="onlyB")!.a.length,0)
  assert.equal(stageRate(a,recorded.publication),null)
  assert.equal(stageRate(b,recorded.publication),null)
})

test("S3: publisher units, ratios, differences and invalid values stay explicit",()=>{
  const row=experimentRows(stage)[0]
  assert.equal(resultMetric(row,"p95").value,Number(object(row.original.completedLatencyNS).p95)/1e6)
  assert.equal(resultMetric(row,"p95").rawValue,object(row.original.completedLatencyNS).p95)
  assert.equal(ratio(undefined,1,4),.25)
  assert.equal(ratio(.5,0,0),null)
  assert.equal(ratio(undefined,5,4),null)
  assert.equal(formatResult(.7957909383424646,"%"),"79.58 %")
  assert.equal(formatResult(.09261,"J"),"0 J")
  assert.equal(formatResult(-1,"ms"),"미제공")
  assert.equal(formatResult(NaN,"J"),"미제공")
  assert.equal(formatResult(Infinity,"ms"),"미제공")
  assert.equal(difference(.8,.7,"%"),"+10.00 %p")
  assert.equal(difference(12,15,"ms"),"-3.00 ms")
  assert.equal(difference(.80000001,.8,"%"),"0.01 %p 미만 · 방향 없음")
  assert.equal(difference(.8,.7,"%",false),"계산 불가")
  assert.equal(relativeEnergy(1,0),"계산 불가")
  assert.equal(relativeEnergy(90,100),"-10.00 %")
  assert.equal(relativeEnergy(undefined,100),"계산 불가")
})

test("F4: both retained publisher shapes expose historical identity and ordered producer-relative plans losslessly",()=>{
  for(const [shape,original] of [["requests",stage],["workloads",demo.stages[0]]] as const) {
    const existing=experimentRows(original)[0],source={store:"archive",key:"raw/frozen/scheduler.json"},phase={store:"archive",key:"phase.json"}
    const metadata=parseSchedulerJSON(`{"workloadUID":"historical-uid","definitionID":"historical-definition","identitySource":{"store":"archive","key":"raw/frozen/scheduler.json"},"substages":[{"id":"zero","alpha":0,"startOffsetNanos":0,"endOffsetNanos":9007199254740993,"from":"2026-10-30T00:00:02.123456789Z","to":"2027-02-11T05:59:21.378197782Z","source":{"store":"archive","key":"phase.json"}},{"id":"next","alpha":0.7,"startOffsetNanos":9007199254740993,"endOffsetNanos":9007199254740994,"from":"2027-02-11T05:59:21.378197782Z","to":"2027-02-11T05:59:21.378197783Z","source":{"store":"archive","key":"phase.json"}}],"missingReasons":{}}`) as Record<string,unknown>
    const published:ExperimentStage={...original,kpis:{[shape]:{[existing.name]:{...existing.original,...metadata,missingReasons:existing.original.missingReasons}}}}
    const row=experimentRows(published)[0]
    assert.equal(row.name,existing.name)
    assert.equal(row.source,`kpis.${shape}.${existing.name}`)
    assert.equal(row.uid,"historical-uid")
    assert.equal(row.definition,"historical-definition")
    assert.deepEqual(row.identitySource,source)
    assert.deepEqual(row.metrics,existing.metrics)
    assert.deepEqual(row.substages?.map(part=>[part.id,part.alpha]),[["zero",0],["next",.7]])
    assert.equal(row.substages?.[0].endOffsetNanos,9007199254740993n)
    assert.equal(row.substages?.[1].startOffsetNanos,9007199254740993n)
    assert.equal(row.substages?.[1].endOffsetNanos,9007199254740994n)
    assert.equal(row.substages?.[0].from,"2026-10-30T00:00:02.123456789Z")
    assert.notEqual(row.substages?.[0].from,published.from)
    assert.equal(row.substages?.[1].to,"2027-02-11T05:59:21.378197783Z")
    assert.deepEqual(row.substages?.[0].source,phase)
    assert.match(publishedText(row.original),/9007199254740993/)
    assert.deepEqual(comparisonRows(published,published)[0].b[0].substages,row.substages)
  }
})

test("F4: null/missing alpha and mixed identities never borrow legacy identity, rate or another producer anchor",()=>{
  for(const shape of ["requests","workloads"] as const) {
    const missingReasons={identity:"Mixed historical definitions",alpha:"Direct rates: alpha not configured",producerAnchor:"Producer not started",substageSource:"Phase not published"}
    const published:ExperimentStage={...stage,kpis:{[shape]:{same:{offered:100,uid:"legacy-uid",definition:"legacy-definition",workloadUID:null,definitionID:null,identitySource:null,missingReasons,substages:[{id:"alpha-is-not-inferred-9",alpha:null,startOffsetNanos:0,endOffsetNanos:1000000000,from:null,to:null,source:null},{id:"missing-alpha",startOffsetNanos:1000000000,endOffsetNanos:2000000000,from:null,to:null,source:null}]}}}}
    const row=experimentRows(published)[0]
    assert.equal(row.uid,null)
    assert.equal(row.definition,null)
    assert.equal(row.identitySource,null)
    assert.deepEqual(row.missingReasons,missingReasons)
    assert.deepEqual(row.substages?.map(part=>part.alpha),[null,null])
    assert.equal(row.substages?.[0].from,null)
    assert.equal(row.substages?.[0].to,null)
    assert.equal(row.substages?.[0].startOffsetNanos,0)
    assert.equal(row.substages?.[0].source,null)
    assert.equal(stageRate(published,recorded.publication),null)
    const absent=experimentRows({...stage,kpis:{[shape]:{same:{offered:100}}}})[0]
    assert.equal(absent.uid,undefined)
    assert.equal(absent.definition,undefined)
    assert.equal(absent.identitySource,undefined)
    assert.equal(absent.substages,null)
    assert.deepEqual(absent.missingReasons,{})
  }
  for(const original of [stage,demo.stages[0]]) {
    assert.ok(experimentRows(original).every(row=>row.substages===null&&row.identitySource===undefined))
  }
})


test("declared comparisons alone define pairs, independent of names or observed rates",()=>{
  const left={...stage,id:"candidate"},right={...stage,id:"baseline"},unlisted={...stage,id:"full-steady-mixed"}
  const run=definedRun([left,right,unlisted],[["candidate","baseline"]])
  assert.deepEqual(definitionComparisons(run).map(pair=>[pair.chill.id,pair.control.id]),[["candidate","baseline"]])
  assert.equal(definitionComparisons({...run,stages:[left,left,right]}).length,0)
  assert.deepEqual(definitionComparisons(demo),[])
  assert.equal(definitionComparisonReason(demo),"미기록")
})


test("service deltas preserve published precision without inventing missing values",()=>{
  const left=experimentRows({...stage,kpis:{requests:{same:{deadlineSuccessFraction:.9737,successDenominator:100,completedLatencyNS:{p95:299300000}}}}})[0]
  const right=experimentRows({...stage,kpis:{requests:{same:{deadlineSuccessFraction:1,successDenominator:100,completedLatencyNS:{p95:142100000}}}}})[0]
  assert.match(serviceDelta(left,right),/성공 -2.63 %p; p95 \+157.20 ms \(×2.11\)/)
  assert.match(serviceDelta(undefined,right),/계산 불가/)
})

test("R5: fixed unit decimals preserve zeros and original numeric export precision",()=>{
  for(const [value,unit,expected] of [
    [12.6,"J","13 J"],[12,"건","12 건"],[0,"J","0 J"],[12,"W","12.0 W"],
    [0,"W","0.0 W"],[.5,"%","50.00 %"],[0,"%","0.00 %"],[2,"%p","2.00 %p"],
    [2,"ms","2.00 ms"],[.01234567,"J/건","0.0123 J/건"],[0,"J/item","0.0000 J/item"],
  ] as const)assert.equal(formatResult(value,unit),expected)
  assert.equal(formatResult(9007199254740993n,"건"),"9007199254740993 건")
  const original=parseSchedulerJSON('{"wholePoolJoules":173062.73965024325,"offered":9007199254740993}')
  const before=publishedText(original)
  formatResult(object(original).wholePoolJoules,"J")
  formatResult(object(original).offered,"건")
  assert.equal(publishedText(original),before)
  assert.match(before,/173062\.73965024325/)
  assert.match(before,/9007199254740993/)
})

test("R5: zero B allows absolute differences and percentage points, not relative percent or multiplier",()=>{
  assert.equal(difference(12.3,0,"J"),"+12 J")
  assert.equal(difference(.25,0,"%"),"+25.00 %p")
  assert.equal(difference(0,0,"ms"),"0.00 ms")
  assert.equal(relativeEnergy(12,0),"계산 불가")
  assert.equal(relativeEnergy(0,0),"계산 불가")
  const row=(p95:number)=>experimentRows({...stage,kpis:{requests:{same:{deadlineSuccessFraction:0,successDenominator:10,completedLatencyNS:{p95}}}}})[0]
  assert.match(serviceDelta(row(1000000),row(0)),/p95 \+1.00 ms \(배율 계산 불가 · B=0\)/)
})

test("R5: every nonzero subunit delta is directionless even when rounding would show one display unit",()=>{
  for(const [a,b,unit,expected] of [
    [10.9,10,"J","1 J 미만 · 방향 없음"],[10,10.9,"J","1 J 미만 · 방향 없음"],
    [1.099,1,"W","0.1 W 미만 · 방향 없음"],[1,1.099,"W","0.1 W 미만 · 방향 없음"],
    [.009,0,"ms","0.01 ms 미만 · 방향 없음"],[0,.009,"ms","0.01 ms 미만 · 방향 없음"],
    [.00009,0,"%","0.01 %p 미만 · 방향 없음"],[0,.00009,"%","0.01 %p 미만 · 방향 없음"],
    [.00009,0,"J/건","0.0001 J/건 미만 · 방향 없음"],
  ] as const) {
    assert.equal(difference(a,b,unit),expected)
    assert.equal(comparisonDirection(a,b,unit,"A","B"),"표시단위 미만 · 방향 없음")
  }
  assert.equal(relativeEnergy(100.009,100),"0.01 % 미만 · 방향 없음")
  assert.equal(relativeEnergy(100,100.009),"0.01 % 미만 · 방향 없음")
  // Calculating before rounding is observable: displayed endpoints differ by 0.02 ms.
  assert.equal(formatResult(1.016,"ms"),"1.02 ms")
  assert.equal(formatResult(1.004,"ms"),"1.00 ms")
  assert.equal(difference(1.016,1.004,"ms"),"+0.01 ms")
})

test("R5: direct A/B direction names the actual policy in both orders without claiming an advantage",()=>{
  assert.equal(comparisonDirection(90,100,"J","정적 비교군","CHILL"),"정적 비교군 값이 CHILL 값보다 낮음")
  assert.equal(comparisonDirection(100,90,"J","CHILL","정적 비교군"),"CHILL 값이 정적 비교군 값보다 높음")
  assert.equal(comparisonDirection(.9,.8,"%","정적 비교군","CHILL"),"정적 비교군 값이 CHILL 값보다 높음")
  assert.equal(comparisonDirection(12,12,"ms","고정 정책","수동 정책"),"고정 정책 / 수동 정책: 같음")
  assert.equal(comparisonDirection(undefined,12,"ms","A","B"),"비교 불가")
})

test("p95 SLO joins the frozen workload UID, never a name, legacy UID or retained KPI SLO",()=>{
  const run=definedRun([{...stage,id:"baseline"}])
  const row=(original:Record<string,unknown>,name="camera")=>experimentRows({...stage,kpis:{workloads:{[name]:original}}})[0]
  assert.equal(frozenP95SLO(row({workloadUID:"camera",measurementSLO:{percentile:95,targetLatencyNanos:999999999}}),run),null)
  assert.equal(frozenP95SLO(row({workloadUID:"camera"},"renamed-camera"),run),null)
  assert.equal(frozenP95SLO(row({uid:"camera"}),run),null)
  assert.equal(frozenP95SLO(row({measurementSLO:{percentile:95,targetLatencyNanos:50000000}}),run),null)
  assert.equal(frozenP95SLO(row({workloadUID:"replacement-camera"}),run),null)
  assert.equal(frozenP95SLO(row({measurementSLO:{percentile:95,targetLatencyNanos:123456789},sloPercentile:95,sloMS:50})),null)
  assert.equal(frozenP95SLO(row({workloadUID:"camera"}),withDefinition(run,definition=>{definition.workloads[0].slo.percentile=99})),null)
})

test("heatmap retains all stages without fabricated missing service values",()=>{
  const rows=experimentHeatmap(recorded.publication)
  const represented=rows.flatMap(row=>[row.stage,...(row.other?[row.other]:[])])
  assert.equal(represented.length,recorded.publication.stages.length)
  assert.ok(recorded.publication.stages.every(stage=>represented.includes(stage)))
  const missing={...stage,id:"manual",kpis:{requests:{camera:{offered:0}}}}
  const row=experimentHeatmap({...recorded.publication,stages:[missing]})[0]
  assert.equal(row.other,null)
  assert.equal(row.energyPercent,null)
  assert.equal(experimentChartData({...recorded.publication,stages:[missing]},"camera")[0].leftP95,null)
})

test("coverage includes every recorded paired stage",()=>{
  const stage=(id:string,energy:number,from:string):ExperimentStage=>({id,from,to:"2026-10-30T00:01:00Z",kpis:{wholePoolJoules:energy}} as ExperimentStage)
  const run=definedRun([stage("candidate-1",90,"2026-10-30T00:00:00Z"),stage("baseline-1",100,"2026-10-30T00:00:00Z"),stage("candidate-2",120,"2026-10-30T00:00:00Z"),stage("baseline-2",100,"2026-10-30T00:00:00Z")],[["candidate-1","baseline-1"],["candidate-2","baseline-2"]])
  const score=campaignScoreboard(run)
  assert.equal(score.coverageCounts.stages,4)
})


test("declared mismatched stage names retain selected values and numeric energy changes",()=>{
  const make=(id:string,energy:number,success:number,p95:number,offered:number):ExperimentStage=>({id,from:"2026-10-30T00:00:00Z",to:"2026-10-30T00:01:00Z",kpis:{wholePoolJoules:energy,requests:{camera:{offered,deadlineSuccessFraction:success,successDenominator:offered,completedLatencyNS:{p95:p95*1e6}}}}})
  const run=definedRun([make("low",90,.99,10,600),make("low-reference",100,1,9,600),make("high",90,.847,1655,6120),make("different-row-name",100,.892,282,7000)],[["low","low-reference"],["high","different-row-name"]])
  const comparison=definitionComparisons(run).find(pair=>pair.chill.id==="high")!
  assert.equal(comparison.basis,"정의 비교")
  assert.equal(comparison.control.id,"different-row-name")
  const row=experimentHeatmap(run).find(row=>row.stage.id==="high")!
  assert.equal(row.stage,comparison.chill)
  assert.equal(row.other,comparison.control)
  assert.equal(row.energyPercent,-10)
  assert.match(serviceDelta(experimentRows(row.stage)[0],experimentRows(row.other!)[0]),/-4.5/)
})


test("R8: campaign coverage retains paired, unmatched and unavailable stages",()=>{
  const make=(id:string,energy:number|null,success:number|null,p95:number|null):ExperimentStage=>({
    id,from:"2026-10-30T00:00:00Z",to:"2026-10-30T00:01:00Z",
    kpis:{wholePoolJoules:energy,measurementRules:{population:"offered"},requests:{camera:{offered:600,deadlineSuccessFraction:success,completedLatencyNS:{p95:p95===null?null:p95*1e6}}}},
  })
  const run=definedRun([
    make("full-steady-mixed",90,.8,20),make("static-baseline-steady-mixed",100,.9,10),
    make("full-burst",110,1,5),make("static-baseline-burst",100,1,5),
    make("full-ramp-up",null,null,null),make("static-baseline-ramp-up",100,1,10),
    make("manual",50,1,5),
  ],[["full-steady-mixed","static-baseline-steady-mixed"],["full-burst","static-baseline-burst"],["full-ramp-up","static-baseline-ramp-up"]])
  const scoreboard=campaignScoreboard(run)
  assert.equal(scoreboard.coverageCounts.stages,7)
  assert.equal(scoreboard.coverageCounts.pairs,3)
  assert.equal(scoreboard.coverageCounts.unpaired,1)
  const unavailable=campaignScoreboard({...run,stages:[make("manual",50,1,5)]})
  assert.equal(unavailable.coverageCounts.unpaired,1)
})

test("declared comparison order is retained while heatmap intervals follow recorded time without mutating the publication",()=>{
  const make=(id:string,minute:number):ExperimentStage=>({...stage,id,from:`2026-10-30T00:0${minute}:00Z`,to:`2026-10-30T00:0${minute+1}:00Z`})
  const run=definedRun([make("later",4),make("later-reference",5),make("early",0),make("early-reference",1)],[["later","later-reference"],["early","early-reference"]])
  assert.deepEqual(definitionComparisons(run).map(pair=>pair.chill.id),["later","early"])
  assert.deepEqual(experimentHeatmap(run).map(row=>row.stage.id),["early","later"])
  assert.equal(run.stages[0].id,"later")
})

test("declared comparison reasons retain missing targets without inventing counterparts",()=>{
  const paired={...definedRun([{...stage,id:"candidate"},{...stage,id:"baseline"}],[["candidate","baseline"]]),from:"2026-10-29T00:00:00Z"}
  const unpaired={...paired,id:"unpaired",from:"2026-10-31T00:00:00Z",stages:paired.stages.slice(0,1)}
  assert.match(definitionComparisonReason(unpaired),/대상 게시 없음/)
  assert.equal(definitionComparisonReason(paired),paired.definition!.spec.comparisons![0].reason)
})

test("R9: published FAIL means a failed request target, never a failed result query",()=>{
  assert.equal(publishedVerdict({...stage,kpis:{...stage.kpis,status:"FAIL"}}),"요청 목표 미달")
  assert.equal(publishedVerdict({...stage,kpis:{status:"NOT_READY"}}),"판정 근거 부족")
  assert.equal(publishedVerdict({...stage,kpis:{status:"NEW_STATUS"}}),"NEW_STATUS")
  const run={...recorded.publication,id:"current",stages:[{...stage,id:"selected"}]}
  assert.equal(selectedExperimentStage(run,"current","selected"),run.stages[0])
  assert.equal(selectedExperimentStage(run,"previous","selected"),undefined)
  assert.equal(selectedExperimentStage({...run,stages:[...run.stages,...run.stages]},"current","selected"),undefined)
  assert.equal(selectedExperimentStage(undefined,"current","selected"),undefined)
})



test("R10: comparison limits retain missing rules, unequal durations and uncertain identity with declared load",()=>{
  const make=(id:string):ExperimentStage=>({id,from:"2026-10-30T00:00:00Z",to:"2026-10-30T00:01:00Z",kpis:{measurementRules:{population:"offered"},requests:{camera:{uid:"camera",definitionID:"v1",offered:600,deadlineSuccessFraction:1,completedLatencyNS:{p95:1000000}}}}})
  const a=make("candidate"),b=make("baseline"),run=definedRun([a,b],[["candidate","baseline"]])
  assert.equal(comparisonValidity(a,b,run,run).load,"같음")
  assert.match(comparisonValidity(a,b,run,run).identity,/같은 등록·정의/)
  assert.match(comparisonValidity(a,b,run,run).rules,/차이 없음/)
  assert.equal(comparisonValidity(a,b,run,run).limited,false)
  assert.deepEqual(comparisonValidity(a,b,run,run).conditions,{load:"같음",identity:"같음",rules:"같음"})
  assert.equal(comparisonValidity(a,{...b,kpis:{}}).limited,true)
  const uncertain={...b,to:"2026-10-30T00:02:00Z",kpis:{requests:{camera:{offered:600}}}}
  const limits=comparisonValidity(a,uncertain)
  assert.equal(limits.load,"미기록")
  assert.match(limits.duration,/길이 다름/)
  assert.match(limits.rules,/미기록/)
  assert.match(limits.identity,/미확인 1/)
  assert.match(comparisonValidity(a,{...b,kpis:{...b.kpis,measurementRules:{}}}).rules,/미기록/)
  assert.match(comparisonValidity(a,{...b,kpis:{...b.kpis,measurementRules:{population:"received"}}}).rules,/차이 있음/)
  assert.match(comparisonValidity(a,{...b,kpis:{...b.kpis,requests:{camera:{uid:"camera",definitionID:"v2",offered:600}}}}).identity,/다름 1/)
  assert.equal(campaignScoreboard({...run,stages:[{...a,kpis:{...a.kpis,measurementRules:null}},b]}).coverageCounts.limited,1)
  assert.equal(campaignScoreboard({...run,stages:[{...a,kpis:{...a.kpis,requests:{camera:{offered:600,deadlineSuccessFraction:1,completedLatencyNS:{p95:1000000}}}}},b]}).coverageCounts.limited,1)
})

test("R10: duration validity and scoreboard use the same precision as displayed seconds",()=>{
  const make=(id:string,to:string):ExperimentStage=>({id,from:"2026-10-30T00:00:00Z",to,kpis:{measurementRules:{population:"offered"},requests:{camera:{uid:"camera",definitionID:"v1",offered:18000}}}})
  const a=make("candidate","2026-10-30T00:30:00.001Z"),b=make("baseline","2026-10-30T00:30:00.499Z"),run=definedRun([a,b],[["candidate","baseline"]])
  assert.match(comparisonValidity(a,b,run,run).duration,/구간 길이 같음 · A 1,800 s \/ B 1,800 s/)
  assert.equal(comparisonValidity(a,b,run,run).limited,false)
  assert.equal(campaignScoreboard(run).coverageCounts.durationDifferences,0)
  const different={...b,to:"2026-10-30T00:30:00.501Z"}
  assert.match(comparisonValidity(a,different).duration,/구간 길이 다름/)
  assert.equal(comparisonValidity(a,different).limited,true)
  assert.match(comparisonValidity(a,{...b,to:"invalid"}).duration,/미제공/)
})

test("R10: coverage projects numeric pair and stage counts without sentence parsing",()=>{
  const make=(id:string,energy:number,success:number,p95:number):ExperimentStage=>({...stage,id,kpis:{wholePoolJoules:energy,requests:{camera:{offered:100,deadlineSuccessFraction:success,completedLatencyNS:{p95:p95*1e6}}}}})
  const score=campaignScoreboard(definedRun([make("full-steady-mixed",90,.8,20),make("static-baseline-steady-mixed",100,.9,10)],[["full-steady-mixed","static-baseline-steady-mixed"]]))
  assert.equal(score.coverageCounts.pairs,1)
  assert.equal(score.coverageCounts.stages,2)
})

test("duration icon ignores rounding differences and appears only beyond one percent",()=>{
  const make=(seconds:number):ExperimentStage=>({...stage,from:"2026-10-30T00:00:00Z",to:new Date(Date.parse("2026-10-30T00:00:00Z")+seconds*1000).toISOString()})
  assert.equal(durationMismatch(make(1800.001),make(1800.499)),false)
  assert.equal(durationMismatch(make(99),make(100)),false)
  assert.equal(durationMismatch(make(98.999),make(100)),true)
  assert.equal(durationMismatch(make(100),make(98.999)),true)
  assert.equal(durationMismatch(make(101),make(100)),false)
  assert.equal(durationMismatch(make(101.001),make(100)),true)
  assert.equal(durationMismatch(make(0),make(100)),false)
  assert.equal(durationMismatch({...make(100),to:"invalid"},make(100)),false)
})

test("selected-service chart and heatmap share ordered intervals and frozen targets",()=>{
  const make=(id:string,minute:number,energy:number,p95:number|null):ExperimentStage=>({id,from:`2026-10-30T00:0${minute}:00Z`,to:`2026-10-30T00:0${minute+1}:00Z`,kpis:{wholePoolJoules:energy,requests:{camera:{workloadUID:"camera",definitionID:"camera/1/model/2/preprocessor/3",identitySource:evidenceRef,offered:600,deadlineSuccessFraction:.9,completedLatencyNS:{p95:p95===null?null:p95*1e6}}}}})
  const later=make("candidate-later",4,120,20),fixedLater=make("baseline-later",5,100,10),early=make("candidate-early",0,90,10),fixedEarly=make("baseline-early",1,100,null)
  const run=definedRun([later,fixedLater,early,fixedEarly],[["candidate-later","baseline-later"],["candidate-early","baseline-early"]])
  const rows=experimentHeatmap(run),chart=experimentChartData(run,"camera")
  assert.deepEqual(rows.map(row=>row.id),[early.id,later.id])
  assert.deepEqual(rows.map(row=>row.energyPercent),[-10,20])
  assert.deepEqual(chart.map(row=>[row.id,row.interval,row.rate,row.leftP95,row.rightP95,row.leftSLO,row.rightSLO]),[[early.id,stageLabel(early,run),10,10,null,50,50],[later.id,stageLabel(later,run),10,20,10,50,50]])
  assert.equal(run.stages[0].id,later.id)
  const zero=experimentHeatmap({...run,stages:[early,{...fixedEarly,kpis:{...fixedEarly.kpis,wholePoolJoules:0}}]})[0]
  assert.equal(zero.energyPercent,null)
  const duplicates=experimentHeatmap({...run,stages:[early,early,fixedEarly]})
  assert.equal(new Set(duplicates.map(row=>row.key)).size,3)
  assert.ok(duplicates.every(row=>row.other===null))
})

test("R13: coverage separates matched definitions and rules from limited comparisons",()=>{
  const make=(id:string,energy:number,definition="same"):ExperimentStage=>({id,from:"2026-10-30T00:00:00Z",to:"2026-10-30T00:00:10Z",kpis:{wholePoolJoules:energy,measurementRules:{window:10},requests:{camera:{uid:"camera",definitionID:definition,offered:10,deadlineSuccessFraction:1,completedLatencyNS:{p95:1000000}}}}})
  const run=definedRun([make("full-steady-mixed",90),make("static-baseline-steady-mixed",100),make("full-burst",1,"changed"),make("static-baseline-burst",100)],[["full-steady-mixed","static-baseline-steady-mixed"],["full-burst","static-baseline-burst"]])
  const score=campaignScoreboard(run)
  assert.equal(score.coverageCounts.matched,1)
  assert.equal(score.coverageCounts.limited,1)
  const limited=campaignScoreboard({...run,stages:run.stages.slice(2)})
  assert.equal(limited.coverageCounts.matched,0)
  assert.equal(limited.coverageCounts.limited,1)
})

test("IA: measured quality differences remain facts, never threshold-based SLO conclusions",()=>{
  const make=(id:string,success:number,p95:number):ExperimentStage=>({...stage,id,kpis:{wholePoolJoules:100,measurementRules:{window:10},requests:{camera:{uid:"camera",definitionID:"same",offered:10,deadlineSuccessFraction:success,completedLatencyNS:{p95:p95*1e6}}}}})
  const run=definedRun([make("candidate",.9,10),make("baseline",.8,20)],[["candidate","baseline"]])
  const score=campaignScoreboard(run),pair=definitionComparisons(run)[0]
  assert.equal(Object.hasOwn(score,"conclusion"),false)
  assert.match(serviceDelta(experimentRows(pair.chill)[0],experimentRows(pair.control)[0]),/10/)
  assert.match(serviceDelta(experimentRows(pair.chill)[0],experimentRows(pair.control)[0]),/×0.50/)
})

test("R14: comparison limits count each affected pair by its recorded cause",()=>{
  const make=(id:string):ExperimentStage=>({id,from:"2026-10-30T00:00:00Z",to:"2026-10-30T00:00:10Z",kpis:{wholePoolJoules:100,measurementRules:{window:10},requests:{camera:{uid:"camera",definitionID:"same",offered:10,deadlineSuccessFraction:1,completedLatencyNS:{p95:1000000}}}}})
  const stages=[make("full-steady-mixed"),make("static-baseline-steady-mixed"),make("full-burst"),make("static-baseline-burst"),make("full-ramp-up"),make("static-baseline-ramp-up")]
  stages[0].kpis.measurementRules={window:20}
  stages[2].kpis.requests={camera:{uid:"camera",offered:10}}
  const run=withDefinition(definedRun(stages,[["full-steady-mixed","static-baseline-steady-mixed"],["full-burst","static-baseline-burst"],["full-ramp-up","static-baseline-ramp-up"]]),definition=>{
    definition.loadScenarios.push({...definition.loadScenarios[0],name:"different-load",uid:"different-load-uid"})
    definition.spec.stages[4].loadScenarioRef={name:"different-load",uid:"different-load-uid"}
  }),counts=campaignScoreboard(run).coverageCounts
  assert.equal(counts.ruleDifferences,1)
  assert.equal(counts.identityUnknown,1)
  assert.equal(counts.loadDifferences,1)
  assert.equal(counts.limited,3)
})

test("R14: rendered experiment judgment exposes one status and cause counts without repeated warning prose",()=>{
  const source=readFileSync(new URL("../features/experiments-conclusion.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const require=createRequire(import.meta.url),exports:Record<string,ComponentType<{run:Experiment}>>={}
  new Function("require","exports",compiled)((name:string)=>name==="@/lib/experiment-comparison"?comparisonHelpers:name==="@/lib/dashboard-routes"?routeHelpers:name==="@/lib/experiments"?experimentHelpers:name==="@/lib/measurements"?measurementHelpers:name==="@/lib/client-transport"?transport:name==="@/lib/client"?{}:name==="@/components/ui/button"?{Button:(props:Record<string,unknown>)=>createElement("button",props)}:name==="lucide-react"?{TriangleAlert:(props:Record<string,unknown>)=>createElement("svg",props)}:require(name),exports)
  const run=definedRun([{...stage,id:"candidate",kpis:{...stage.kpis,measurementRules:{window:10},requests:{camera:{offered:10}}}},{...stage,id:"baseline",kpis:{...stage.kpis,measurementRules:{window:20},requests:{camera:{offered:10}}}}],[["candidate","baseline"]])
  const judgment=renderToStaticMarkup(createElement(exports.ExperimentJudgment,{run}))
  assert.match(judgment,/참고/)
  assert.match(judgment,/aria-label="비교 상태"/)
  const limits=renderToStaticMarkup(createElement(exports.ExperimentLimits,{run}))
  assert.match(limits,/규칙 다름/)
  assert.match(limits,/정의 미기록/)
  assert.match(limits,/<table[^>]*aria-label="비교 제한 원인"/)
  assert.match(limits,/<th[^>]*>제한 원인<\/th>/)
  assert.match(limits,/<th[^>]*>구간 수 \(개\)<\/th>/)
  assert.doesNotMatch(limits,/role="tooltip"|invisible|\bw-max\b/)
  assert.match(limits,/<td[^>]*>1<\/td>/)
  const outcome=renderToStaticMarkup(createElement(exports.ExperimentOutcome,{run}))
  assert.match(outcome,/aria-label="실험 판정 3축"/)
  assert.doesNotMatch(outcome,/SLO 개선|SLO 악화|참고 · 제한 비교/)
  const coverage=renderToStaticMarkup(createElement(exports.ExperimentCoverage,{run}))
  assert.match(coverage,/<table[^>]*aria-label="비교 구간 수"/)
  assert.match(coverage,/<th[^>]*>참고<\/th><td[^>]*>1<\/td>/)
})


test("R17: legacy publications retain observed rates and service latency without a comparison definition",()=>{
  const run=decodeExperiment(publishedText(recorded.publication))
  const chart=experimentChartData(run,"")
  assert.equal(chart.length,run.stages.length)
  assert.ok(chart.some(row=>row.observedRate!==null&&row.observedRate>0))
  assert.ok(chart.every(row=>row.rate===null))
  const name=experimentRows(run.stages[0])[0].name
  const service=experimentChartData(run,name)
  assert.equal(service[0].leftP95,resultMetric(experimentRows(run.stages[0])[0],"p95").value)
  assert.ok(service.every(row=>row.rightP95===null&&row.leftSLO===null&&row.rightSLO===null))
})

test("R17: legacy result rows render absolute stage KPIs instead of an empty delta matrix",()=>{
  const source=readFileSync(new URL("../features/experiments-conclusion.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const require=createRequire(import.meta.url),exports:Record<string,ComponentType<{run:Experiment;runs:Experiment[];pending:boolean;error:boolean}>>={}
  new Function("require","exports",compiled)((name:string)=>name==="@/lib/experiment-comparison"?comparisonHelpers:name==="@/lib/dashboard-routes"?routeHelpers:name==="@/lib/experiments"?experimentHelpers:name==="@/lib/measurements"?measurementHelpers:name==="@/lib/client-transport"?transport:name==="@/lib/client"?{}:name==="@/components/ui/button"?{Button:(props:Record<string,unknown>)=>createElement("button",props)}:name==="next/link"?{__esModule:true,default:(props:Record<string,unknown>)=>createElement("a",props)}:require(name),exports)
  const run=decodeExperiment(publishedText(recorded.publication))
  const markup=renderToStaticMarkup(createElement(exports.ExperimentResultRow,{run,runs:[run],pending:false,error:false}))
  assert.equal((markup.match(/<tr\b/g)??[]).length,1)
  assert.doesNotMatch(markup,/단계별 원값|발행 요청률|<table|성공률 Δ|p95 \(배\)/)
  assert.match(markup,new RegExp(run.id))
})

test("R17b: planned and issued chart rates share the selected service and duration weighting",()=>{
  const run=withDefinition(definedRun([{...stage,id:"candidate"}]),definition=>{
    definition.loadScenarios[0].spec.parts=[{id:"early",durationSeconds:10,rates:{camera:2,plc:30}},{id:"late",durationSeconds:30,rates:{camera:6,plc:10}}]
  })
  const average=(service:string)=>experimentChartData(run,service)[0].rate
  assert.equal(average("camera"),5)
  assert.equal(average("plc"),15)
  assert.equal(average(""),20)
  assert.ok(experimentChartData(run,"absent").every(row=>row.rate===null))
})

test("IA: half/high remain planned offsets and parent measurements occur exactly once",()=>{
  const run=withDefinition(definedRun([{...stage,id:"candidate",kpis:{wholePoolJoules:100,requests:{camera:{offered:600,completedLatencyNS:{p95:20000000}}}}}]),d=>{
    d.loadScenarios[0].spec.parts=[{id:"half",durationSeconds:180,rates:{camera:53.075340806062236}},{id:"high",durationSeconds:300,rates:{camera:106.15068161212447}}]
  })
  const chart=experimentChartData(run,"camera")
  assert.equal(chart.length,1)
  assert.deepEqual(chart[0].plans.map(part=>[part.id,part.from,part.to,part.rate]),[["half",0,180,53.075340806062236],["high",180,480,106.15068161212447]])
  assert.equal(chart[0].leftP95,20)
  assert.equal(chart[0].leftEnergy,100)
  assert.equal(chart[0].observedRate,600/stageSeconds(stage)!)
  assert.ok(chart[0].plans.every(part=>!Object.hasOwn(part,"leftP95")&&!Object.hasOwn(part,"leftEnergy")&&!Object.hasOwn(part,"observedRate")))
  assert.equal(experimentChartData(run,"")[0].leftP95,null)
  assert.equal(experimentChartData(run,"")[0].leftSLO,null)
})

test("R18: solo measured values, SLO verdict and frozen metadata render real objects",()=>{
  const run=definedRun([{...stage,id:"candidate",kpis:{wholePoolJoules:54776.5,measurementRules:{harnessRepositoryCommit:"abc123"},requests:{camera:{workloadUID:"camera",offered:100,successNumerator:25,successDenominator:100,completedLatencyNS:{p95:1986480000,samples:25}}}}}])
  run.outcome={execution:{status:"Complete"},measurement:{verdict:"FAIL",reason:"SLOFailed"}}
  run.definition!.loadScenarios[0].spec.parts=[{id:"half",durationSeconds:180,rates:{camera:53.075340806062236}},{id:"high",durationSeconds:300,rates:{camera:106.15068161212447}}]
  const source=readFileSync(new URL("../features/experiments-conclusion.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  type RenderProps={run:Experiment;stage?:ExperimentStage;runs?:Experiment[];pending?:boolean;error?:boolean}
  const require=createRequire(import.meta.url),exports={} as {useHistoricalExperiments:(runs:Experiment[])=>{runs:Experiment[]};ExperimentResultRow:ComponentType<RenderProps>;ExperimentMetadata:ComponentType<RenderProps>}
  new Function("require","exports",compiled)((name:string)=>name==="@/lib/experiment-comparison"?comparisonHelpers:name==="@/lib/dashboard-routes"?routeHelpers:name==="@/lib/experiments"?experimentHelpers:name==="@/lib/measurements"?measurementHelpers:name==="@/lib/client-transport"?transport:name==="@/lib/client"?{useChillQueries:()=>[{data:run,isPending:false}]}:name==="@/components/ui/button"?{Button:(props:Record<string,unknown>)=>createElement("button",props)}:name==="next/link"?{__esModule:true,default:(props:Record<string,unknown>)=>createElement("a",props)}:require(name),exports)
  const listed={...run,definition:undefined,comparisons:[]}
  const markup=renderToStaticMarkup(createElement(exports.ExperimentResultRow,{run,runs:[run],pending:false,error:false}))
  assert.match(markup,/변경 없음/)
  assert.doesNotMatch(markup,/단계별 원값|54,777|<table/)
  assert.match(markup,/SLO 미충족/)
  assert.doesNotMatch(markup,/측정 실패|53\.075340806062236|에너지 Δ/)
  const metadata=renderToStaticMarkup(createElement(exports.ExperimentMetadata,{run,stage:run.stages[0]}))
  assert.match(metadata,/half/)
  assert.match(metadata,/high/)
  assert.match(metadata,/53\.08/)
  assert.match(metadata,/실행기 소스 버전/)
  assert.match(metadata,/abc123/)
  const historic=renderToStaticMarkup(createElement(exports.ExperimentMetadata,{run:listed,stage:listed.stages[0]}))
  assert.match(historic,/미기록/)
  assert.doesNotMatch(historic,/자동 운영|53\.08/)
})

test("R18 review: frozen rules render Korean item/value/unit rows without policy or SLO duplication",()=>{
  const source=readFileSync(new URL("../features/experiments-conclusion.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const require=createRequire(import.meta.url),exports={} as {ExperimentMetadata:ComponentType<{run:Experiment;stage:ExperimentStage}>}
  new Function("require","exports",compiled)((name:string)=>name==="@/lib/experiment-comparison"?comparisonHelpers:name==="@/lib/dashboard-routes"?routeHelpers:name==="@/lib/experiments"?experimentHelpers:name==="@/lib/measurements"?measurementHelpers:name==="@/lib/client-transport"?transport:name==="@/lib/client"?{}:name==="@/components/ui/button"?{Button:(props:Record<string,unknown>)=>createElement("button",props)}:require(name),exports)
  const rules={harnessRepositoryCommit:"abc123",energyCoverage:object(stage.kpis.measurementRules).energyCoverage,evidenceSource:"api",gcMeaning:"runtime-metrics-delta"}
  const run=definedRun([{...stage,id:"candidate",kpis:{measurementRules:rules,requests:{camera:{workloadUID:"camera",definitionID:"camera-v1"}}}}])
  const markup=renderToStaticMarkup(createElement(exports.ExperimentMetadata,{run,stage:run.stages[0]}))
  assert.match(markup,/<table aria-label="측정 규칙"/)
  for(const label of ["항목","값","단위","실행기 소스 버전","전력 결손 비율 상한","단일 전력 공백 상한","근거 출처","GC 집계 방식"])assert.ok(markup.includes(label),label)
  assert.match(markup,/전력 결손 비율 상한<\/th><td[^>]*>1<\/td><td[^>]*>%<\/td>/)
  assert.match(markup,/단일 전력 공백 상한<\/th><td[^>]*>30<\/td><td[^>]*>s<\/td>/)
  assert.match(markup,/abc123/)
  assert.match(markup,/공개 API/)
  assert.match(markup,/런타임 누적값 차이/)
  assert.match(markup,/aria-label="서비스 식별자·정의"/)
  assert.doesNotMatch(markup.match(/<table aria-label="측정 규칙"[\s\S]*?<\/table>/)?.[0]??"",/harnessRepositoryCommit|energyCoverage|maxGapFraction|runtime-metrics-delta|자동 운영|SLO/)
  assert.deepEqual(run.stages[0].kpis.measurementRules,rules)
  const missing=renderToStaticMarkup(createElement(exports.ExperimentMetadata,{run,stage:{...run.stages[0],kpis:{measurementRules:{energyCoverage:{maxGapFraction:0,maxSingleGapSeconds:null}}}}}))
  assert.match(missing,/전력 결손 비율 상한<\/th><td[^>]*>0<\/td>/)
  assert.match(missing,/단일 전력 공백 상한<\/th><td[^>]*>미기록<\/td>/)
})

test("R18 review: interval Sheet renders each stage policy and service SLO only once",()=>{
  const require=createRequire(import.meta.url)
  const load=(file:string,dependencies:Record<string,unknown>,extra="")=>{
    const source=readFileSync(new URL(file,import.meta.url),"utf8")
    const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
    const exports:Record<string,unknown>={}
    new Function("require","exports",compiled+extra)((name:string)=>name in dependencies?dependencies[name]:require(name),exports)
    return exports
  }
  const element=(tag:string)=>function Element({children}:{children?:ReactNode}){return createElement(tag,{},children)}
  const shared={"@/lib/experiment-comparison":comparisonHelpers,"@/lib/dashboard-routes":routeHelpers,"@/lib/experiments":experimentHelpers,"@/lib/measurements":measurementHelpers,"@/lib/client":{},"@/lib/client-transport":transport,"@/components/ui/button":{Button:element("button")},"next/link":{__esModule:true,default:element("a")}}
  Object.assign(shared,{"@/components/unreadable-experiments":load("../components/unreadable-experiments.tsx",shared)})
  const conclusion=load("../features/experiments-conclusion.tsx",shared)
  const features=load("../features/experiments.tsx",{...shared,"@/lib/scheduler":{parseSchedulerJSON},"next/navigation":{},"lucide-react":icons,"recharts":{},"@/components/ui/dialog":{},"@/components/ui/tooltip":{},"@/components/read-region":{SourceJSON:()=>null,SourceExport:()=>null},"@/components/screen":{},"@/components/ui/sheet":Object.fromEntries(["Sheet","SheetContent","SheetHeader","SheetTitle"].map(name=>[name,element("div")])),"@/features/experiments-conclusion":conclusion,"@/features/scheduler-evidence":{SchedulerIntervalEvents:()=>null},"@/features/evaluation-request":{}},"\nexports.IntervalDrawer=IntervalDrawer;")
  const run=definedRun([{...stage,id:"candidate",kpis:{requests:{camera:{workloadUID:"camera",definitionID:"camera/1/model/2/preprocessor/3",identitySource:evidenceRef,offered:100,deadlineSuccessFraction:1,completedLatencyNS:{p95:1000000}}}}}])
  const Drawer=features.IntervalDrawer as ComponentType<{run:Experiment;runs:Experiment[];row:ExperimentHeatmapRow;service:string;onClose:()=>void;restoreFocus:()=>void}>
  const markup=renderToStaticMarkup(createElement(Drawer,{run,runs:[run],row:experimentHeatmap(run)[0],service:"",onClose:()=>{},restoreFocus:()=>{}}))
  assert.equal((markup.match(/변경 없음/g)??[]).length,1)
  assert.ok(markup.includes(`p95 · ${measurementHelpers.formatMeasurementNumber(50,"ms")} ms`))
  assert.ok(markup.includes(`적용 확인: p95 · ${measurementHelpers.formatMeasurementNumber(50,"ms")} ms`))
  assert.match(markup,/서비스 식별자·정의/)
})

function reviewExperimentUI(params:URLSearchParams,identity:{name:string;uid:string}) {
  const require=createRequire(import.meta.url),exports:Record<string,ComponentType<Record<string,unknown>>>={}
  const compiled=transpileModule(readFileSync(new URL("../features/experiments.tsx",import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const modules:Record<string,unknown>={
    "@/lib/experiment-comparison":comparisonHelpers,"@/lib/dashboard-routes":routeHelpers,"@/lib/experiments":experimentHelpers,"@/lib/measurements":measurementHelpers,
    "@/lib/client":{useChillQuery:()=>({data:identity})},"next/navigation":{useSearchParams:()=>params},
    "@/lib/client-transport":transport,
    "next/link":{__esModule:true,default:({children,href}:{children:ReactNode;href:string})=>createElement("a",{href},children)},
    "@/components/ui/button":{Button:({children}:{children:ReactNode})=>createElement("span",{},children)},
    recharts:{ResponsiveContainer:()=>null},
  }
  new Function("require","exports",`${compiled}\nexports.PublicationRunLink=PublicationRunLink;exports.ExperimentCharts=ExperimentCharts;`)((name:string)=>name in modules?modules[name]:name.startsWith("@/")?{}:require(name),exports)
  return exports
}

test("P2 results: progress round trip preserves every result selection parameter",()=>{
  const run=definedRun([{...stage,id:"chosen"}]),params=new URLSearchParams({a:run.id,al:"other",as:"chosen",b:"baseline",bs:"base",sheet:"closed",eventSide:"baseline",offset:"5",service:"camera",uid:"obsolete"})
  const identity=run.definition!.run,ui=reviewExperimentUI(params,identity)
  const markup=renderToStaticMarkup(createElement(ui.PublicationRunLink,{run}))
  const href=markup.match(/href="([^"]+)"/)![1].replaceAll("&amp;","&"),url=new URL(href,"https://dashboard.invalid")
  assert.equal(url.pathname,`/experiments/runs/${identity.name}`)
  assert.equal(url.searchParams.get("uid"),identity.uid)
  for(const [key,value] of params)if(key!=="uid")assert.equal(url.searchParams.get(key),value,key)
})

test("P2 results: recorded substages do not imply missing interval measurements",()=>{
  const base=withDefinition(definedRun([{...stage,id:"candidate",kpis:{wholePoolJoules:100,requests:{camera:{offered:600,completedLatencyNS:{p95:20000000}}}}}]),definition=>{
    definition.loadScenarios[0].spec.parts=[{id:"half",durationSeconds:10,rates:{camera:5}},{id:"high",durationSeconds:10,rates:{camera:10}}]
  })
  const ui=reviewExperimentUI(new URLSearchParams(),base.definition!.run)
  const render=(run:Experiment)=>renderToStaticMarkup(createElement(ui.ExperimentCharts,{run,runs:[run],service:"camera",rows:experimentHeatmap(run),selected:undefined,onSelect:()=>{}}))
  assert.match(render(base),/계획 오프셋 · 구간 실측 미제공/)
  const observed={...base,stages:base.stages.map(item=>({...item,kpis:{...item.kpis,requests:{camera:{offered:600,completedLatencyNS:{p95:20000000},substages:[{id:"half",offered:200,completedLatencyNS:{p95:10000000}}]}}}}))}
  const markup=render(observed)
  assert.match(markup,/계획 오프셋 · 차트 실측은 단계 집계/)
  assert.doesNotMatch(markup,/구간 실측 미제공/)
})

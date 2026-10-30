import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { execFileSync } from "node:child_process"
import { object, comparisonRows, difference, formatResult, ratio, relativeEnergy, resultMetric, stageRate, experimentRows, energyText, differentMeasurementRules, decisionRange, decisionIdentity, evidenceError, publishedText, type Experiment, type ExperimentStage } from "./experiments.ts"
import type { SchedulerObservation } from "./read-types.ts"
import { relayPlan } from "./relay.ts"
import { serverConfig } from "./server-config.ts"

const recorded=JSON.parse(readFileSync(new URL("./fixtures/experiment-213.json",import.meta.url),"utf8")) as {publication:Experiment;decision:SchedulerObservation}
const demo=JSON.parse(readFileSync(new URL("./fixtures/experiment-demo.json",import.meta.url),"utf8")) as Experiment
const stage=recorded.publication.stages[0]

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

test("S1-4: archive judgment lookup pins nanosecond decision.at plus identity, never latest; absence differs from deadline",()=>{
  const at=recorded.decision.decision!.at,range=decisionRange(at)!
  assert.deepEqual(range,{from:"2026-10-28T01:34:28.365665918Z",to:"2026-10-28T01:34:28.365665919Z"})
  assert.deepEqual(decisionRange("2026-10-28T10:34:28.365665918+09:00"),range)
  assert.equal(decisionRange("2026-10-28T01:34:28.999999999Z")!.to,"2026-10-28T01:34:29.000000000Z")
  assert.equal(decisionRange("2026-02-30T01:00:00Z"),null)
  assert.equal(decisionRange("latest"),null)
  const identity=decisionIdentity(recorded.decision)
  const newer={...recorded.decision,decision:{...recorded.decision.decision!,at:"2026-10-28T01:34:29Z"}}
  const sameTime={...recorded.decision,decision:{...recorded.decision.decision!,status:"Keep"}}
  assert.notEqual(identity,decisionIdentity(newer));assert.notEqual(identity,decisionIdentity(sameTime))
  assert.equal(evidenceError({status:504,data:{code:"DashboardRelayTimeout"}}),"조회 시간 초과")
  assert.equal(evidenceError({status:408,data:{code:"RequestTimeout"}}),"조회 시간 초과")
  assert.equal(evidenceError({status:403,data:{code:"Forbidden"}}),"근거 조회 실패")
  const config=serverConfig({CHILL_BASE_URL:"https://fixture.invalid",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"fixture"})
  const plan=relayPlan("GET",["v1","observations","scheduler"],`?${new URLSearchParams({...range,limit:"20"})}`,config)
  assert.match(plan.path,/365665918Z/);assert.match(plan.path,/365665919Z/)
  const source=readFileSync(new URL("../features/experiments.tsx",import.meta.url),"utf8")
  assert.match(source,/기록 없음/);assert.match(source,/decisionIdentity\(event\)===identity&&offset===params.get\("record"\)/)
  assert.doesNotMatch(source,/observations\/raw|v1\/scheduler\/state/)
})

test("S1-5: rendered unbound serving-manager caller grants include experiment and both measurement reads, never raw",()=>{
  const rendered=execFileSync("helm",["template","s1-fixture","../charts/chill","--set","servingEntrypoint.enabled=true","--set","servingEntrypoint.tls.secretName=fixture-tls","--set","evidenceStore.connection.bucket=fixture","--set","evidenceStore.connection.region=local"],{encoding:"utf8",maxBuffer:16*1024*1024})
  const roles=rendered.split(/\n---\n/).filter(doc=>/name: s1-fixture-chill-serving-manager\s/.test(doc))
  assert.ok(roles.length>=2)
  const manager=roles.join("\n")
  for(const path of ["/chill/api/v1/experiments","/chill/api/v1/experiments/*","operation-cost-measurements","dispatch-occupancy-measurements"])assert.ok(manager.includes(path),path)
  assert.match(manager,/resources: \[operationcostmeasurements, dispatchoccupancymeasurements\]\s+verbs: \[get, list\]/)
  assert.doesNotMatch(manager,/observations\/raw/)
  const config=serverConfig({CHILL_BASE_URL:"https://fixture.invalid",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"fixture"})
  for(const path of ["v1/experiments","v1/experiments/control-213-full-sample","v1/namespaces/research/operation-cost-measurements","v1/namespaces/research/dispatch-occupancy-measurements"]){
    assert.equal(relayPlan("GET",path.split("/"),"",config).kind,"query")
    assert.throws(()=>relayPlan("POST",path.split("/"),"",config),/DashboardMethodNotAllowed/)
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
  assert.equal(stageRate(a),1)
  assert.equal(stageRate(b),null)
})

test("S3: publisher units, ratios, differences and invalid values stay explicit",()=>{
  const row=experimentRows(stage)[0]
  assert.equal(resultMetric(row,"p95").value,Number(object(row.original.completedLatencyNS).p95)/1e6)
  assert.equal(resultMetric(row,"p95").rawValue,object(row.original.completedLatencyNS).p95)
  assert.equal(ratio(undefined,1,4),.25)
  assert.equal(ratio(.5,0,0),null)
  assert.equal(ratio(undefined,5,4),null)
  assert.equal(formatResult(.7957909383424646,"%"),"79.58 %")
  assert.equal(formatResult(.09261,"J"),"0.09261 J")
  assert.equal(formatResult(-1,"ms"),"미제공")
  assert.equal(formatResult(NaN,"J"),"미제공")
  assert.equal(formatResult(Infinity,"ms"),"미제공")
  assert.equal(difference(.8,.7,"%"),"+10.00 %p")
  assert.equal(difference(12,15,"ms"),"-3 ms")
  assert.equal(difference(.80000001,.8,"%"),"+0.00 %p · 표시 정밀도 미만")
  assert.equal(difference(.8,.7,"%",false),"계산 불가")
  assert.equal(relativeEnergy(1,0),"계산 불가")
  assert.equal(relativeEnergy(90,100),"-10.00 %")
  assert.equal(relativeEnergy(undefined,100),"계산 불가")
})

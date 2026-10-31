import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { execFileSync } from "node:child_process"
import { campaignGroups, campaignPairs, commitExperimentSelection, comparisonDirection, experimentSelection, frozenP95SLO, stagePairing, policyStageGroups, serviceDelta, stageChartData, alphaText, object, comparisonRows, difference, formatResult, ratio, relativeEnergy, resultMetric, stageRate, experimentRows, energyText, differentMeasurementRules, decisionRange, decisionIdentity, evidenceError, publishedText, type Experiment, type ExperimentStage } from "./experiments.ts"
import { campaignConclusion, campaignScoreboard, campaignTradeoffs, policyChartStyle, stageComparison, stageHistoryLink, stageActivity, stageLoadLabel } from "./experiments.ts"
import type { SchedulerObservation } from "./read-types.ts"
import { relayPlan } from "./relay.ts"
import { serverConfig } from "./server-config.ts"
import { parseSchedulerJSON } from "./scheduler.ts"
import { schedulerHistoryKindMatches } from "./scheduler-history.ts"

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
  assert.match(source,/stageHistoryLink\(stage\)/)
  assert.doesNotMatch(source,/RFC3339Nano|<Input/)
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
  assert.equal(alphaText(0,undefined),"α 0")
  assert.equal(alphaText(null,"Direct request rates configured; alpha not recorded"),"직접 요청률 설정 — α 없음")
  assert.equal(alphaText(null,undefined),"α 미제공")
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
    assert.ok(stageRate(published)!>0)
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

test("R3: rapid A/B selections preserve the preceding URL selection and clear only dependent stages",()=>{
  const location=new URL("https://dashboard.invalid/experiments?a=old&as=old-stage&b=old-b&bs=keep&at=stale")
  const history={replaceState(_data:unknown,_unused:string,url?:string|URL|null){location.href=new URL(String(url),location).href}}
  commitExperimentSelection(location,history,{a:"new"})
  commitExperimentSelection(location,history,{b:"new-b"})
  const selected=location.searchParams
  assert.equal(selected.get("a"),"new")
  assert.equal(selected.get("b"),"new-b")
  assert.equal(selected.has("as"),false)
  assert.equal(selected.has("bs"),false)
  assert.equal(selected.has("at"),false)
  assert.equal(experimentSelection("a=one&as=chosen&b=two&bs=keep",{as:"next"}).get("bs"),"keep")
})

test("R7: campaign grouping uses exact stages first and unique same-load comparisons second",()=>{
  const full={...stage,id:"full-E5-1"},staticStage={...stage,id:"static-E5-1"}
  const run={...recorded.publication,stages:[full,{...full,id:"full-E5-2"},staticStage,{...staticStage,id:"static-E5-3"}]}
  assert.deepEqual(campaignPairs(run).map(pair=>[pair.chill.id,pair.control.id]),[["full-E5-1","static-E5-1"],["full-E5-2","static-E5-3"]])
  assert.equal(campaignPairs({...run,stages:[full,full,staticStage]}).length,0)
  const groups=campaignGroups([run,demo])
  assert.equal(groups.length,2)
  assert.equal(groups.find(group=>group.run.id===run.id)!.pairs.length,2)
  assert.equal(groups.find(group=>group.run.id===demo.id)!.pairs.length,0)
  assert.ok(groups.every(group=>group.title===group.run.id),"each card identifies its publication instead of repeating a generic title")
})

test("R3: chart points retain per-service success and p95 without averaging or inventing missing values",()=>{
  const data=stageChartData(recorded.publication)
  assert.equal(data.length,recorded.publication.stages.length)
  assert.equal(data[0].rate,stageRate(stage))
  const row=experimentRows(stage)[0]
  assert.equal(data[0].services[row.name].p95,resultMetric(row,"p95").value)
  assert.equal(data[0].services[row.name].success,Number(resultMetric(row,"success").value)*100)
  const missing=stageChartData({...recorded.publication,stages:[{...stage,kpis:{wholePoolJoules:0,requests:{same:{offered:0}}}}]})[0]
  assert.equal(missing.energy,0)
  assert.equal(missing.services.same.p95,null)
  assert.equal(missing.services.same.success,null)
})

test("R3fix: policies share exact stage axes and normalize shared energy by offered requests and actual duration",()=>{
  const make=(id:string,seconds:number,offered:unknown=10):ExperimentStage=>({id,from:"2026-10-30T00:00:00Z",to:`2026-10-30T00:00:${String(seconds).padStart(2,"0")}Z`,kpis:{wholePoolJoules:100,gapErrorBoundJoules:5,requests:{same:{offered}}}})
  const run={...recorded.publication,stages:[make("full-E5-2",20),make("static-E5-2",10),make("full-E5-3-repeat",10,0)]}
  const points=stageChartData(run)
  assert.deepEqual(points.map(p=>p.policy),["자동 운영","고정 구성","자동 운영"])
  assert.deepEqual(points.map(p=>p.axis),["E5-2","E5-2","E5-3-repeat"])
  assert.equal(points[0].joulesPerRequest,10)
  assert.equal(points[0].watts,5)
  assert.equal(points[0].wattError,.25)
  assert.equal(points[1].watts,10)
  assert.equal(points[2].joulesPerRequest,null)
  assert.equal(stageChartData({...run,stages:[make("unknown",10,null)]})[0].joulesPerRequest,null)
  const grouped=policyStageGroups(run)
  assert.deepEqual(grouped.policies,["자동 운영","고정 구성"])
  assert.deepEqual(grouped.groups.map(group=>group.axis),["E5-2","E5-3-repeat"])
  assert.equal(grouped.groups[0].series[0]?.watts,5)
  assert.equal(grouped.groups[0].series[1]?.watts,10)
  assert.equal(grouped.groups[1].series[1],null)
  const duplicateGroups=policyStageGroups({...run,stages:[run.stages[0],run.stages[0]]}).groups
  assert.equal(duplicateGroups.length,2)
  assert.ok(duplicateGroups.every(group=>group.series[0]?.paired===false))
  const duplicates={...run.stages[0],kpis:{wholePoolJoules:100,requests:{same:{offered:10}},workloads:{same:{offered:10}}}}
  assert.equal(stageChartData({...run,stages:[duplicates]})[0].joulesPerRequest,null)
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

test("R7: unique load comparisons preserve exact pairing status, duplicate stages and unknown policies",()=>{
  const make=(id:string):ExperimentStage=>({...stage,id,from:"2026-10-30T00:00:00Z",to:"2026-10-30T00:00:10Z",kpis:{wholePoolJoules:100,requests:{same:{offered:100}}}})
  const stages=[make("full-E5-1-a"),make("static-E5-1-b"),make("full-E5-2"),make("static-E5-2"),make("full-E5-3"),make("full-E5-3"),make("static-E5-3"),make("manual")]
  const run={...recorded.publication,stages}
  assert.ok(stages.every(stage=>stageRate(stage)===10))
  assert.deepEqual(campaignPairs(run).map(pair=>pair.key),["E5-1-a","E5-2"])
  assert.match(stagePairing(run,stages[0]).reason,/static-E5-1-a 없음/)
  assert.match(stagePairing(run,stages[1]).reason,/full-E5-1-b 없음/)
  assert.equal(stagePairing(run,stages[4]).reason,"동일 단계 식별자 중복")
  assert.equal(stagePairing(run,stages[7]).paired,false)
  const points=stageChartData(run)
  assert.equal(points.length,stages.length)
  assert.equal(points.filter(point=>point.paired).length,2)
  const grouped=policyStageGroups(run).groups.flatMap(group=>group.series.filter(point=>point!==null))
  assert.equal(grouped.length,stages.length)
  assert.equal(grouped.filter(point=>point.reason==="동일 단계 식별자 중복").length,3)
})

test("R5: p95 SLO comes only from each frozen stage KPI and never from another percentile or live settings",()=>{
  const row=(original:Record<string,unknown>)=>experimentRows({...stage,kpis:{workloads:{same:original}}})[0]
  assert.equal(frozenP95SLO(row({measurementSLO:{percentile:95,targetLatencyNanos:123456789}})),123.456789)
  assert.equal(frozenP95SLO(row({sloPercentile:95,sloMS:50})),50)
  assert.equal(frozenP95SLO(row({measurementSLO:{percentile:99,targetLatencyNanos:1000000}})),null)
  assert.equal(frozenP95SLO(row({schedulerSLO:{percentile:95,targetLatencyNanos:1000000}})),null)
  assert.equal(frozenP95SLO(row({})),null)
})

test("R5: all-service charts and all-stage table are initially rendered, with original source exports retained",()=>{
  const source=readFileSync(new URL("../features/experiments.tsx",import.meta.url),"utf8")
  assert.doesNotMatch(source,/차트로 볼 서비스|자세히 볼 서비스|const \[chosen|const \[selected|연구|논문/)
  assert.match(source,/md:grid-cols-2 xl:grid-cols-3/)
  assert.match(source,/h-\[180px\]/)
  assert.match(source,/names\.map\(name=><article/)
  assert.match(source,/<tbody>\{rows\.map\(row=><ServiceRows/)
  assert.match(source,/aq\.data&&<><StageCharts/)
  assert.doesNotMatch(source,/pairs\.length>0&&<StageSummary/)
  assert.match(source,/connectNulls=\{false\}/)
  assert.match(source,/dataKey=\{`u\$\{index\}`\}/)
  assert.match(source,/<SourceExport filename="experiment-comparison\.json" value=\{\{a:aq\.data,/)
})

test("operator conclusion includes every paired stage and reports energy losses alongside gains",()=>{
  const stage=(id:string,energy:number,from:string):ExperimentStage=>({id,from,to:"2026-10-30T00:01:00Z",kpis:{wholePoolJoules:energy}} as ExperimentStage)
  const run={stages:[stage("full-E5-1",90,"2026-10-30T00:00:00Z"),stage("static-E5-1",100,"2026-10-30T00:00:00Z"),stage("full-E5-2",120,"2026-10-30T00:00:00Z"),stage("static-E5-2",100,"2026-10-30T00:00:00Z")]} as Experiment
  assert.match(campaignConclusion(run),/전체 4단계/)
  assert.match(campaignConclusion(run),/감소 1 \/ 증가 1구간/)
  assert.equal(stageLoadLabel(run.stages[0],run),"요청률 미기록")
})

test("operator conclusion names the worst service loss and does not treat missing success as zero",()=>{
  const make=(id:string,success:unknown,p95:number):ExperimentStage=>({id,from:"2026-10-30T00:00:00Z",to:"2026-10-30T00:01:00Z",kpis:{wholePoolJoules:100,requests:{camera:{deadlineSuccessFraction:success,completedLatencyNS:{p95:p95*1e6}}}}})
  const run={stages:[make("full-E5-1",.8,20),make("static-E5-1",.9,10)]} as Experiment
  assert.match(campaignConclusion(run),/최대 성공률 하락 -10.00%p: camera/)
  assert.match(campaignConclusion(run),/최대 p95 증가 10.00 ms: camera/)
  assert.match(campaignConclusion({...run,stages:[make("full-E5-1",null,20),run.stages[1]]}),/확인이 부족/)
})

test("R7: suffix-mismatched high load remains in gains/losses, worst quality loss and shared chart axis",()=>{
  const make=(id:string,success:number,p95:number,offered=6120):ExperimentStage=>({id,from:"2026-10-30T00:00:00Z",to:"2026-10-30T00:01:00Z",kpis:{wholePoolJoules:id.startsWith("full")?90:100,requests:{camera:{offered,deadlineSuccessFraction:success,successDenominator:offered,completedLatencyNS:{p95:p95*1e6}}}}})
  const high=make("full-E5-3",.847,1655),fixed=make("static-E5-3-223",.892,282)
  const run={...recorded.publication,stages:[make("full-E5-1",.99,10,600),make("static-E5-1",1,9,600),high,fixed]}
  const comparison=stageComparison(run,high)
  assert.equal(comparison.basis,"같은 부하 비교")
  assert.equal(comparison.paired,false)
  assert.equal(comparison.counterpart,fixed)
  const row=campaignTradeoffs(run).find(row=>row.chill===high)!
  assert.equal(row.energyDelta,-10)
  assert.ok(Math.abs(row.services[0].successDelta!-(-4.5))<1e-10)
  assert.equal(row.services[0].p95Ratio,1655/282)
  assert.equal(row.services[0].worstSuccess,true)
  assert.equal(row.services[0].worstP95,true)
  assert.match(campaignConclusion(run),/102 req\/s/)
  assert.equal(policyStageGroups(run).groups.length,2)
  // A nearly identical actual duration is not a different load plan.
  assert.equal(stageComparison({...run,stages:[high,{...fixed,to:"2026-10-30T00:01:00.005Z"}]},high).counterpart?.id,fixed.id)
  assert.equal(stageComparison({...run,stages:[high,fixed,{...fixed,id:"static-E5-4"}]},high).counterpart,null)
  assert.equal(stageComparison({...run,stages:[high,{...fixed,kpis:{requests:{other:{offered:6120}}}}]},high).counterpart,null)
  assert.equal(stageComparison({...run,stages:[high,{...fixed,kpis:{requests:{camera:{offered:7000}}}}]},high).counterpart,null)
})

test("R7: stage record links retain the exact range and activity counts never claim a partial page is complete",()=>{
  const params=new URL(stageHistoryLink(stage),"https://dashboard.invalid").searchParams
  assert.equal(params.get("from"),stage.from)
  assert.equal(params.get("to"),stage.to)
  assert.equal(params.get("limit"),"100")
  assert.equal(params.get("filter"),"decision-action")
  assert.deepEqual(["decision","action","power","snapshot","settings"].filter(kind=>schedulerHistoryKindMatches(params.get("filter")!,kind)),["decision","action"])
  const page={from:stage.from,to:stage.to,events:[recorded.decision,{at:stage.from,action:{id:"a",kind:"prepare",node:{name:"node",uid:"node-uid"},phase:"Started",startedAt:stage.from}}],nextCursor:"100"}
  assert.deepEqual(stageActivity(page),{decisions:1,actions:1,partial:true,statuses:[recorded.decision.decision!.status],kinds:["prepare"]})
  assert.equal(stageActivity({...page,nextCursor:undefined,gapUntil:stage.to}).partial,true)
  assert.deepEqual(stageActivity({from:stage.from,to:stage.to,events:null}),{decisions:0,actions:0,partial:false,statuses:[],kinds:[]})
})

test("R8: campaign scoreboard separates energy, worst service losses, missing metrics and incomparable stages",()=>{
  const make=(id:string,energy:number|null,success:number|null,p95:number|null):ExperimentStage=>({
    id,from:"2026-10-30T00:00:00Z",to:"2026-10-30T00:01:00Z",
    kpis:{wholePoolJoules:energy,measurementRules:{population:"offered"},requests:{camera:{offered:600,deadlineSuccessFraction:success,completedLatencyNS:{p95:p95===null?null:p95*1e6}}}},
  })
  const run={...recorded.publication,stages:[
    make("full-E5-1",90,.8,20),make("static-E5-1",100,.9,10),
    make("full-E5-2",110,1,5),make("static-E5-2",100,1,5),
    make("full-E5-3",null,null,null),make("static-E5-3",100,1,10),
    make("manual",50,1,5),
  ]}
  const scoreboard=campaignScoreboard(run)
  assert.deepEqual(scoreboard.metrics.map(metric=>metric.label),["전체 POOL 에너지","최대 성공률 하락","최대 p95 증가"])
  assert.equal(scoreboard.metrics[0].value,"감소 1 / 증가 1 / 같음 0쌍")
  assert.match(scoreboard.metrics[0].detail,/값 미제공 1쌍/)
  assert.match(scoreboard.metrics[1].value,/-10.00 %p/)
  assert.match(scoreboard.metrics[1].detail,/camera.*E5-1.*비교 불가·미제공 1건/)
  assert.match(scoreboard.metrics[2].value,/\+10.00 ms.*×2.00/)
  assert.match(scoreboard.metrics[2].detail,/camera.*E5-1.*비교 불가·미제공 1건/)
  assert.match(scoreboard.coverage,/전체 7단계/)
  assert.match(scoreboard.coverage,/같은 부하 3쌍.*비교 불가 1단계/)
  const unavailable=campaignScoreboard({...run,stages:[make("manual",50,1,5)]})
  assert.ok(unavailable.metrics.every(metric=>metric.value==="비교 불가"))
  assert.match(unavailable.coverage,/비교 불가 1단계/)
  const missing=campaignScoreboard({...run,stages:[make("full-E5-1",null,null,null),make("static-E5-1",100,1,10)]})
  assert.ok(missing.metrics.every(metric=>metric.value==="미제공"))
})

test("R8: comparisons and chart axes follow recorded time rather than severity, suffix or input order",()=>{
  const make=(id:string,minute:number):ExperimentStage=>({...stage,id,from:`2026-10-30T00:0${minute}:00Z`,to:`2026-10-30T00:0${minute+1}:00Z`})
  const run={...recorded.publication,stages:[make("full-E5-1",4),make("static-E5-1",5),make("full-E5-3",0),make("static-E5-3",1)]}
  assert.deepEqual(campaignPairs(run).map(pair=>pair.key),["E5-3","E5-1"])
  assert.deepEqual(campaignTradeoffs(run).map(pair=>pair.key),["E5-3","E5-1"])
  assert.deepEqual(policyStageGroups(run).groups.map(group=>group.axis),["E5-3","E5-1"])
  assert.equal(run.stages[0].id,"full-E5-1","sorting never mutates the publication")
})

test("R8: policy markers distinguish square fixed from circle automatic without converting missing chart points to zero",()=>{
  assert.deepEqual(policyChartStyle("고정 구성"),{color:"var(--color-slate-500, #64748b)",shape:"square"})
  assert.deepEqual(policyChartStyle("자동 운영"),{color:"var(--color-teal-600, #0d9488)",shape:"circle"})
  const run={...recorded.publication,stages:[{...stage,id:"full-E5-1",kpis:{requests:{camera:{offered:0}}}}]}
  assert.equal(stageChartData(run)[0].services.camera.success,null)
  assert.equal(stageChartData(run)[0].services.camera.p95,null)
  assert.equal(policyStageGroups(run).groups[0].series[0]?.services.camera.p95,null)
})

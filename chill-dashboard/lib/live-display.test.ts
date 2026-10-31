import assert from "node:assert/strict"
import { test } from "node:test"
import type { LiveAction, LiveInstance, LiveMetric, LiveSnapshot, LiveWorkload } from "./live.ts"
import { actionProgress, actionOrigin, scopeSnapshot, scopeEvents, recentEvents, liveFresh, belongsToPool, displayNumber, duration, elapsed, instanceSymbol, isStale, ratio, receivedLabel, routingShare, terminalCount, workloadLabel, workloadQueue, servicePriority, mergeKeepEvents, trendSegments, boardVerdict, decisionText } from "./live-display.ts"

const at="2026-10-30T12:00:00.123456789Z",now=Date.parse(at)
const instance:LiveInstance={id:"shared",node:{uid:"n",name:"Node"},ownerWorkloadUID:"a",profileID:"p",ready:true,stopped:false,routable:{a:true,b:true},running:{a:1,b:0},waiting:{a:0,b:2},observedAt:at}
const metric:LiveMetric={received:9,completed:3,failed:1,withinSLO:2,latencySamples:4,p50ms:10,p95ms:20,p99ms:30}
const workload:LiveWorkload={uid:"a",name:"same-name",model:"arbitrary",definitionID:"d",slo:{percentile:95,targetLatencyNanos:100000000},autonomous:false,admission:true,pending:2,readiness:{ready:true,reason:"",observedAt:at}}

test("terminal ratios never divide by received and distinguish missing from observed zero",()=>{
  assert.equal(terminalCount(metric),4)
  assert.equal(ratio(metric.completed,terminalCount(metric)),"3/4 (75.0%)")
  assert.equal(ratio(metric.withinSLO,terminalCount(metric)),"2/4 (50.0%)")
  assert.equal(ratio(0,0),"비율 없음")
  assert.equal(ratio(undefined,0),"")
  assert.equal(receivedLabel(undefined),"")
  assert.equal(receivedLabel({...metric,received:0}),"0 req/s · 무유입")
  assert.equal(displayNumber(null," W"),"")
  assert.equal(displayNumber(0," W",1),"0.0 W")
})
test("shared instances belong to every matching UID but never match by workload name",()=>{
  assert.equal(belongsToPool(instance,"a"),true)
  assert.equal(belongsToPool(instance,"b"),true)
  assert.equal(belongsToPool({...instance,routable:{},running:{c:2}},"c"),true)
  assert.equal(belongsToPool(instance,"same-name"),false)
  assert.equal(belongsToPool({...instance,routable:{},running:{},ownerWorkloadUID:""},"a"),false)
})
test("six symbols follow documented precedence, not binding count or operation completion",()=>{
  assert.equal(instanceSymbol({...instance,stopped:true},"a")[0],"×")
  assert.equal(instanceSymbol({...instance,routable:{}},"a")[0],"◌")
  assert.equal(instanceSymbol(instance,"a")[0],"●")
  assert.equal(instanceSymbol(instance,"b")[0],"◐")
  assert.equal(instanceSymbol({...instance,routable:{},running:{}},"a")[0],"○")
  assert.equal(instanceSymbol({...instance,ready:false,routable:{},running:{}},"a")[0],"◇")
})
test("state labels require independent facts and mark stale native execution",()=>{
  assert.equal(workloadLabel(workload,[],undefined,now),"등록됨")
  assert.equal(workloadLabel(workload,[],metric,now),"요청 받음")
  assert.equal(workloadLabel(workload,[{...instance,routable:{},running:{}}],undefined,now),"실행 준비됨")
  assert.equal(workloadLabel(workload,[instance],undefined,now),"실제 처리 중")
  assert.equal(workloadLabel(workload,[instance],undefined,now+5001),"마지막 관측 · 실제 처리 중")
  assert.equal(workloadLabel({...workload,definitionID:null},[{...instance,running:{}}],undefined,now),"등록됨")
  assert.equal(isStale(at,now+5000),false)
  assert.equal(isStale(at,now+5001),true)
  assert.equal(isStale(null,now),true)
  assert.equal(elapsed(at,now+2500),"2초 전")
})
test("routing shares preserve absence and normalize supplied weights only",()=>{
  assert.equal(routingShare({shared:3,other:1},"shared"),75)
  assert.equal(routingShare({shared:0,other:1},"shared"),0)
  assert.equal(routingShare({shared:0},"shared"),undefined)
  assert.equal(routingShare({},"shared"),undefined)
})

test("risk and work precede traffic; idle never hides missing metrics or pending work",()=>{
  assert.equal(servicePriority(workload,metric,0),0)
  assert.equal(servicePriority(workload,{...metric,failed:0,withinSLO:3},0),2)
  assert.equal(servicePriority({...workload,pending:0},{...metric,failed:0,withinSLO:3},1),3)
  assert.equal(servicePriority({...workload,pending:0},{...metric,received:0,completed:0,failed:0,withinSLO:0},0),5)
  assert.equal(servicePriority({...workload,pending:0},undefined,0),4)
})
test("verdict prioritizes lost data, zero serving, SLO, delayed changes, progress, then normal",()=>{
  const input={fresh:true,terminals:3,failed:0,sloMisses:0,queued:0,changes:0,delayed:0,serving:1,off:0,total:1,admissionBlocked:false}
  assert.equal(boardVerdict({...input,fresh:false,serving:0,sloMisses:1,delayed:1}).label,"데이터 끊김")
  assert.equal(boardVerdict({...input,serving:0,sloMisses:1,delayed:1}).label,"서빙 0")
  assert.equal(boardVerdict({...input,sloMisses:1,delayed:1}).label,"SLO 위반")
  assert.equal(boardVerdict({...input,delayed:1,changes:1}).label,"정체")
  assert.equal(boardVerdict({...input,changes:1}).label,"진행")
  assert.equal(boardVerdict(input).label,"정상")
  assert.equal(boardVerdict({...input,terminals:0}).label,"표본 없음")
})
test("only consecutive unchanged Keep events merge; changes remain independently visible",()=>{
  const decision=(seq:number,reason="")=>({type:"event" as const,kind:"decision" as const,seq,at,decision:{at,state:"Keep",reason,explanation:""}})
  const rows=mergeKeepEvents([decision(1),decision(2),decision(3,"ReservedScope:plan-a"),decision(4)])
  assert.deepEqual(rows.map(row=>row.count),[2,1,1])
  assert.equal(rows[0].event.seq,2)
  assert.equal(decisionText({at,state:"Keep",reason:"ReservedScope:plan-a",explanation:"예약된 자원은 유지해요"}),"예약된 자원은 유지해요")
})
test("five-minute sparkline does not join missing seconds or absent workload samples",()=>{
  const end=now+4000
  const points=[{at:now,value:1},{at:now+1000,value:2},{at:now+3000,value:3},{at:now+4000,value:null}]
  assert.equal(trendSegments(points,end).length,2)
  assert.equal(trendSegments([{at:now-301000,value:9}],now).length,0)
})

test("queue totals include native waiting even when routing is already removed",()=>{
  const draining={...instance,ownerWorkloadUID:"other",routable:{},running:{},waiting:{a:3}}
  assert.equal(belongsToPool(draining,"a"),true)
  assert.equal(workloadQueue(workload,[draining]),5)
  assert.equal(servicePriority({...workload,admission:false},undefined,0,5),0)
})

test("SLO verdict respects the configured percentile instead of demanding every response be in time",()=>{
  const observed={...metric,received:100,completed:100,failed:0,withinSLO:96,latencySamples:100}
  assert.equal(servicePriority({...workload,pending:0},observed,0),4)
  assert.equal(servicePriority({...workload,pending:0}, {...observed,withinSLO:94},0),1)
  assert.equal(boardVerdict({fresh:true,terminals:100,failed:0,sloMisses:0,queued:0,changes:0,delayed:0,serving:1,off:0,total:1,admissionBlocked:false}).label,"정상")
})

test("delayed changes are not counted as ordinary progress; completion always wins",()=>{
  const action:LiveAction={id:"manual-unroute:a",kind:"unroute",node:{uid:"n",name:"N"},workloadUID:"a",status:"running",startedAt:at,expectedSeconds:10,expectedSource:null}
  assert.equal(actionProgress(action,now+30000),"progress")
  assert.equal(actionProgress(action,now+30001),"delayed")
  assert.equal(actionProgress({...action,expectedSeconds:null},now+300001),"delayed")
  assert.equal(actionProgress({...action,expectedSeconds:134},now+300001),"progress")
  assert.equal(actionProgress({...action,expectedSeconds:134},now+402001),"delayed")
  assert.equal(actionProgress({...action,completedAt:at},now+6600000),"terminal")
  assert.equal(actionOrigin(action),"수동")
  assert.equal(actionOrigin({...action,id:"prepare:a"}),"자동")
  assert.equal(duration(6600),"1시간 50분")
  assert.equal(elapsed(at,now+6600000),"1시간 50분 전")
})
test("service scope applies to metrics inputs, changes, reservations and events without cluster decisions",()=>{
  const action:LiveAction={id:"a",kind:"prepare",node:{uid:"n",name:"N"},workloadUID:"a",status:"running",startedAt:at,expectedSeconds:null,expectedSource:null}
  const snapshot:LiveSnapshot={workloads:[workload,{...workload,uid:"b"}],nodes:[],instances:[instance],routing:{a:{shared:1},b:{shared:1}},decision:{at,state:"Keep",reason:"",explanation:"cluster"},actions:[action,{...action,id:"b",workloadUID:"b"}],reservations:[{owner:"a",nodes:[],workloadUIDs:["a"],podUIDs:[],instances:[],reason:"",explanation:""}]}
  const scoped=scopeSnapshot(snapshot,"a")
  assert.deepEqual(scoped.workloads.map(row=>row.uid),["a"])
  assert.deepEqual(scoped.actions,[action])
  assert.equal(scoped.decision,null)
  const events=[{type:"event" as const,kind:"action" as const,seq:1,at,action:{...action,phase:"started" as const}},{type:"event" as const,kind:"decision" as const,seq:2,at,decision:snapshot.decision!}]
  assert.equal(scopeEvents(events,"a").length,1)
  assert.equal(recentEvents(events,now).length,1)
  assert.equal(recentEvents(events,now+30001).length,0)
})
test("heartbeat or reconnect never makes old metrics live",()=>{
  const state={status:"live",lastObservedAt:at,metrics:[{bucketAt:at}]} as Parameters<typeof liveFresh>[0]
  assert.equal(liveFresh(state,now),true)
  assert.equal(liveFresh(state,now+5001),false)
  assert.equal(liveFresh({...state,status:"resuming"},now),false)
  assert.equal(liveFresh({...state,metrics:[]},now),false)
})

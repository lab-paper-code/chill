import assert from "node:assert/strict"
import { test } from "node:test"
import type { LiveAction, LiveInstance, LiveMetric, LiveMetrics, LiveSnapshot, LiveWorkload } from "./live.ts"
import { changeListStatus, actionProgress, actionOrigin, scopeSnapshot, scopeEvents, recentEvents, liveFresh, belongsToPool, displayNumber, duration, elapsed, instanceSymbol, isStale, ratio, receivedLabel, routingShare, terminalCount, workloadLabel, workloadQueue, servicePriority, mergeKeepEvents, trendSegments, boardVerdict, decisionText } from "./live-display.ts"
import { retainedEvents, sloLatency, minuteMetrics, incidentFlow, incidentSummary, serviceRows, powerBreakdown, powerWindow, decisionReason, gapEpisodes, liveReason, displayTime } from "./live-display.ts"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { createElement, type ComponentType, type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import { compareAt, initialLiveState, type LiveState } from "./live.ts"
import * as displayHelpers from "./live-display.ts"
import { decisionRange } from "./experiments.ts"

const at="2026-10-30T12:00:00.123456789Z",now=Date.parse(at)
const instance:LiveInstance={id:"shared",node:{uid:"n",name:"Node"},ownerWorkloadUID:"a",profileID:"p",ready:true,stopped:false,routable:{a:true,b:true},running:{a:1,b:0},waiting:{a:0,b:2},observedAt:at}
const metric:LiveMetric={received:9,completed:3,failed:1,withinSLO:2,latencySamples:4,p50ms:10,p90ms:15,p95ms:20,p99ms:30}
const workload:LiveWorkload={uid:"a",name:"same-name",model:"arbitrary",definitionID:"d",slo:{percentile:95,targetLatencyNanos:100000000},autonomous:false,admission:true,pending:2,readiness:{ready:true,reason:"",observedAt:at}}

test("one incident flow includes gaps by their start and counts them alongside changes",()=>{
  const gaps=[{from:at,to:new Date(now+1000).toISOString(),scope:"node:n",reason:"power sample unavailable"}]
  const flow=incidentFlow([],gaps,now+1000)
  assert.equal(flow.length,1)
  assert.equal(flow[0].kind,"gap")
  assert.equal(flow[0].at,at)
  assert.equal(incidentFlow([],gaps,now-1).length,0)
})
test("incident flow preserves nanoseconds and breaks equal timestamps by descending sequence",()=>{
  const event=(seq:number,time:string)=>({type:"event" as const,kind:"decision" as const,seq,at:time,decision:{at:time,state:"Changed",reason:"",explanation:""}})
  const events=[event(1,"2026-10-30T12:00:00.1234Z"),event(2,"2026-10-30T12:00:00.123456789Z"),event(3,"2026-10-30T12:00:00.123456789Z")]
  const gaps=[{from:"2026-10-30T12:00:00.123456790Z",to:at,scope:"all",reason:"gap"}]
  assert.deepEqual(incidentFlow(events,gaps,now).map(row=>row.key),[
    "gap:all:2026-10-30T12:00:00.123456790Z:gap","event:3","event:2","event:1",
  ])
})
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
test("headline aggregates service states: anomaly, caution, normal, then no load",()=>{
  const pool={...workload,pending:0}
  const snapshot:LiveSnapshot={workloads:[pool],nodes:[],instances:[{...instance,waiting:{}}],routing:{},decision:null,actions:[],reservations:[]}
  const observed={...metric,received:100,completed:100,failed:0,withinSLO:100,latencySamples:100}
  const verdict=(samples:Record<string,LiveMetric>,fresh=true,gap=false)=>boardVerdict(serviceRows(snapshot,samples,now),fresh,gap)
  assert.equal(verdict({a:observed}).label,"정상")
  assert.equal(verdict({a:{...observed,received:0,completed:0,latencySamples:0}}).label,"정상 · 무부하")
  assert.equal(verdict({a:{...observed,completed:0,withinSLO:0,latencySamples:0}}).label,"주의")
  assert.equal(serviceRows(snapshot,{a:{...observed,latencySamples:0}},now)[0].status.label,"표본 부족")
  assert.equal(verdict({a:{...observed,withinSLO:94}}).label,"이상")
  assert.equal(verdict({a:observed},true,true).label,"정상")
  assert.equal(verdict({a:observed},true,true).observation.label,"관측 공백")
  assert.equal(verdict({a:observed},false).label,"미확인")
  assert.equal(verdict({a:observed},false).observation.label,"오래된 관측")
  assert.equal(verdict({}).label,"주의")
  const mixed={...snapshot,workloads:[pool,{...pool,uid:"b"}]}
  assert.equal(boardVerdict(serviceRows(mixed,{a:observed,b:{...observed,received:0}},now),true,false).label,"정상")
})
test("whole-board changes and delays warn after service anomalies, including UID-less power actions",()=>{
  const snapshot:LiveSnapshot={workloads:[{...workload,pending:0}],nodes:[],instances:[{...instance,waiting:{}}],routing:{},decision:null,actions:[],reservations:[]}
  const observed={...metric,failed:0,withinSLO:3}
  const rows=serviceRows(snapshot,{a:observed},now)
  assert.equal(boardVerdict(rows,true,false,1,0).label,"주의")
  assert.equal(boardVerdict(rows,true,false,1,0).reason,"변경 중")
  assert.equal(boardVerdict(rows,true,false,0,1).label,"주의")
  assert.equal(boardVerdict(rows,true,false,0,1).reason,"예상 대비 지연")
  assert.equal(boardVerdict(serviceRows(snapshot,{a:metric},now),true,false,1,1).label,"이상")
  assert.equal(boardVerdict(rows,true,true,1,1).label,"주의")
  const concurrent=boardVerdict(serviceRows(snapshot,{a:metric},now),true,true)
  assert.equal(concurrent.axis,"서비스 영향")
  assert.equal(concurrent.label,"이상")
  assert.equal(concurrent.reason,"요청 실패")
  assert.equal(concurrent.observation.axis,"관측 상태")
  assert.equal(concurrent.observation.label,"관측 공백")
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
  const pool={...workload,pending:0},snapshot:LiveSnapshot={workloads:[pool],nodes:[],instances:[{...instance,waiting:{}}],routing:{},decision:null,actions:[],reservations:[]}
  assert.equal(boardVerdict(serviceRows(snapshot,{a:observed},now),true,false).label,"정상")
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

test("cached changes never claim current progress or absence while disconnected or stale",()=>{
  const state={status:"live",lastObservedAt:at,metrics:[{bucketAt:at}],snapshot:{actions:[]}} as unknown as Parameters<typeof changeListStatus>[0]
  assert.equal(changeListStatus(state,now).title,"변경 진행 0건 / 정체 0건")
  assert.equal(changeListStatus(state,now).emptyLabel,"진행·정체 변경이 없어요.")
  for(const stale of [{...state,status:"reconnecting"},{...state,status:"paused-hidden"},{...state,status:"disconnected"},state]){
    const view=changeListStatus(stale,now+6000)
    assert.equal(view.fresh,false)
    assert.match(view.title,/현재 상태 미확인.*마지막 관측 21:00:00 KST/)
    assert.match(view.emptyLabel,/현재 상태.*미확인/)
  }
  assert.match(changeListStatus({...state,status:"reconnecting"},now).title,/현재 상태 미확인/)
})

test("automation and placement do not turn observed no-load services into an alarm",()=>{
  const snapshot:LiveSnapshot={workloads:[{...workload,pending:0}],nodes:[],instances:[],routing:{},decision:null,actions:[],reservations:[]}
  const idle={...metric,received:0,completed:0,failed:0,withinSLO:0,latencySamples:0}
  assert.equal(boardVerdict(serviceRows(snapshot,{a:idle},now),true,false).label,"정상 · 무부하")
  assert.equal(boardVerdict(serviceRows(snapshot,{a:{...idle,received:1}},now),true,false).label,"이상")
  assert.equal(boardVerdict(serviceRows(snapshot,{a:{...idle,failed:1}},now),true,false).label,"이상")
  assert.equal(decisionReason("",[false,false]),"자동제어 대상 없음")
  assert.equal(decisionReason("",[true,false]),"미제공")
  assert.equal(decisionReason("",[]),"미제공")
  assert.equal(decisionReason("Keep",[true]),"유지")
})

test("latency uses the exact SLO percentile and retained events outlive the new-event window",()=>{
  assert.equal(sloLatency({...workload,slo:{...workload.slo,percentile:90}},metric),15)
  assert.equal(sloLatency({...workload,slo:{...workload.slo,percentile:99}},metric),30)
  assert.equal(sloLatency({...workload,slo:{...workload.slo,percentile:92}},metric),undefined)
  assert.equal(sloLatency(workload,{...metric,latencySamples:0}),null)
  const event={type:"event" as const,kind:"decision" as const,seq:1,at,decision:{at,state:"Changed",reason:"",explanation:"변경"}}
  assert.equal(recentEvents([event],now+60000).length,0)
  assert.equal(retainedEvents([event],now+60000).length,1)
  assert.equal(retainedEvents([...Array(35)].map((_,seq)=>({...event,seq})),now).length,30)
  assert.equal(retainedEvents([{...event,decision:{...event.decision,state:"Keep"}}],now).length,0)
})

test("minute summary includes exactly sixty completed second buckets at aligned and fractional clocks",()=>{
  const end=Date.parse("2026-10-30T12:01:00Z")
  const rows:LiveMetrics[]=Array.from({length:62},(_,i)=>({type:"metrics",seq:i+1,at:new Date(end-61000+i*1000).toISOString(),bucketAt:new Date(end-61000+i*1000).toISOString(),workloads:{a:metric},nodes:{},pool:{wholeWatts:null,activeWatts:null}}))
  for(const clock of [end,end+750]) {
    const selected=minuteMetrics(rows,"a",clock)
    assert.equal(selected.length,60)
    assert.equal(selected.reduce((sum,row)=>sum+row.received,0),540)
  }
})

test("board counters and timeline share one retained view across desktop and 390px remounts",()=>{
  const events=Array.from({length:32},(_,seq)=>({type:"event" as const,kind:"decision" as const,seq,at,decision:{at,state:"Keep",reason:"",explanation:""}}))
  const gaps=Array.from({length:35},(_,i)=>({from:new Date(now-i).toISOString(),to:at,scope:`node:${i}`,reason:"power sample unavailable"}))
  const desktop=incidentSummary(events,gaps,now),mobile=incidentSummary(events,gaps,now)
  assert.equal(desktop.newCount,desktop.rows.filter(row=>Date.parse(row.at)>=now-30000).length)
  assert.deepEqual(mobile,desktop)
  assert.equal(desktop.rows.some(row=>row.kind==="event"&&row.event.kind==="decision"&&row.event.decision.state==="Keep"),false)
  assert.equal(incidentSummary(events,[],now).newCount,0)
})
test("idle collapse excludes missing metrics, active instances, reservations and attention",()=>{
  const idleMetric={...metric,received:0,completed:0,failed:0,withinSLO:0},idleWorkload={...workload,pending:0}
  const snapshot:LiveSnapshot={workloads:[idleWorkload],nodes:[],instances:[],routing:{},decision:null,actions:[],reservations:[]}
  assert.equal(serviceRows(snapshot,{a:idleMetric},now)[0].idle,true)
  assert.equal(serviceRows(snapshot,{},now)[0].idle,false)
  assert.equal(serviceRows({...snapshot,instances:[instance]},{a:idleMetric},now)[0].idle,false)
  assert.equal(serviceRows({...snapshot,reservations:[{owner:"a",nodes:[],workloadUIDs:["a"],podUIDs:[],instances:[],reason:"",explanation:""}]},{a:idleMetric},now)[0].idle,false)
  assert.equal(serviceRows(snapshot,{a:{...idleMetric,failed:1}},now)[0].idle,false)
})
test("power remainder uses the same bucket and never invents missing node power",()=>{
  const metrics:LiveMetrics={type:"metrics",seq:1,at,bucketAt:at,workloads:{},nodes:{a:{watts:40,observedAt:at},outside:{watts:60,observedAt:at}},pool:{wholeWatts:100,activeWatts:40}}
  assert.equal(powerBreakdown(["a"],metrics).other,60)
  assert.equal(powerBreakdown(["a"],{...metrics,nodes:{a:{watts:null,observedAt:null}}}).other,null)
  assert.equal(powerBreakdown(["a"],{...metrics,pool:{...metrics.pool,wholeWatts:null}}).other,null)
  assert.equal(powerBreakdown(["a"],{...metrics,pool:{...metrics.pool,wholeWatts:30}}).other,null)
})
test("contiguous device gaps form one incident rather than one incident per second",()=>{
  const time=(seconds:number)=>new Date(now+seconds*1000).toISOString()
  const gaps=Array.from({length:6},(_,i)=>({from:time(i),to:time(i+1),scope:"node:n",reason:"power sample unavailable"}))
  const flow=incidentFlow([],gaps,now+6000)
  assert.equal(flow.length,1)
  assert.equal(flow[0].kind,"gap")
  if(flow[0].kind==="gap")assert.equal(flow[0].gap.to,time(6))
})
test("only recovered short gaps leave the flow; completeness keeps the original intervals",()=>{
  const time=(seconds:number)=>new Date(now+seconds*1000).toISOString()
  const gaps=[{from:time(0),to:time(1),scope:"node:n",reason:"power sample unavailable"},{from:time(1),to:time(3),scope:"node:n",reason:"power sample unavailable"},{from:time(0),to:time(6),scope:"node:other",reason:"power sample unavailable"}]
  const metrics:LiveMetrics[]=[{type:"metrics",seq:1,at:time(6),bucketAt:time(6),workloads:{},nodes:{n:{watts:0,observedAt:time(6)},other:{watts:10,observedAt:time(6)}},pool:{wholeWatts:10,activeWatts:0}}]
  const episodes=gapEpisodes(gaps,now+7000,metrics)
  assert.deepEqual(episodes.map(row=>[row.scope,row.seconds,row.recovered]),[["node:n",3,true],["node:other",6,true]])
  assert.equal(incidentFlow([],gaps,now+7000,false,metrics).length,1)
  assert.equal(incidentSummary([],gaps,now+7000,metrics).newCount,1)
  assert.equal(gaps[0].to,time(1))
  assert.equal(gapEpisodes(gaps,now+7000,[])[0].recovered,false)
  assert.equal(gapEpisodes(gaps,now+5000,metrics)[0].recovered,false)
  assert.equal(gapEpisodes(gaps,now+7000,[{...metrics[0],nodes:{other:metrics[0].nodes.other}}])[0].recovered,false)
})
test("zero arrivals cannot claim an SLO result and product reasons use Korean labels",()=>{
  assert.equal(sloLatency(workload,{...metric,received:0}),null)
  assert.equal(liveReason("ExcludedNode"),"제외 노드")
  assert.equal(liveReason("Keep"),"유지")
  assert.equal(liveReason(""),"미제공")
  assert.equal(liveReason("UnregisteredCode"),"UnregisteredCode")
  assert.equal(liveReason("MissingEvidence"),"측정 근거 없음")
  assert.equal(liveReason("Preparing"),"배치 준비 중")
  assert.equal(displayTime("2026-10-30T15:02:03Z","KST"),"00:02:03 KST")
  assert.equal(displayTime(at,"KST"),"21:00:00 KST")
})
test("event history loss cannot recover from metrics or disappear as a short gap",()=>{
  const gap={from:new Date(now-1000).toISOString(),to:at,scope:"events",reason:"event history limit"}
  const metrics:LiveMetrics[]=[{type:"metrics",seq:1,at,bucketAt:at,workloads:{a:metric},nodes:{},pool:{wholeWatts:10,activeWatts:0}}]
  assert.equal(gapEpisodes([gap],now,metrics)[0].recovered,false)
  assert.equal(incidentFlow([],[gap],now,false,metrics).length,1)
})
test("power windows use retained completed buckets, not component clocks or mount time",()=>{
  const end=Date.parse("2026-10-30T12:01:00Z")
  const rows:LiveMetrics[]=Array.from({length:60},(_,i)=>({type:"metrics",seq:i+1,at:new Date(end-59000+i*1000).toISOString(),bucketAt:new Date(end-60000+i*1000).toISOString(),workloads:{},nodes:{n:{watts:i===10?null:20,observedAt:at}},pool:{wholeWatts:i===10?null:20,activeWatts:null}}))
  const desktop=powerWindow(rows),remounted=powerWindow([...rows])
  assert.deepEqual(remounted,desktop)
  assert.equal(desktop.expected,60)
  assert.equal(desktop.nodes.n,59)
  assert.equal(desktop.observed,59)
  assert.equal(desktop.segments.length,2)
  const sparse=powerWindow([rows[0],rows[59]])
  assert.equal(sparse.expected,60)
  assert.equal(sparse.observed,2)
  assert.deepEqual(powerWindow([]),{end:0,expected:0,observed:0,nodes:{},segments:[]})
})
test("operations board keeps independent axes, idle summary, service placement and selected-time evidence",()=>{
  const pool={...workload,pending:0},zero={...metric,received:0,completed:0,failed:0,withinSLO:0,latencySamples:0}
  const snapshot:LiveSnapshot={workloads:Array.from({length:29},(_,i)=>({...pool,uid:`service-${i}`,name:`서비스-${i}`})),nodes:[{uid:"n",name:"장치",powerMode:"고정",ready:true,measurement:false,observedAt:at}],instances:[],routing:{},decision:{at,state:"Keep",reason:"",explanation:""},actions:[],reservations:[]}
  const sample:LiveMetrics={type:"metrics",seq:1,at,bucketAt:"2026-10-30T12:00:00Z",workloads:Object.fromEntries(snapshot.workloads.map(row=>[row.uid,zero])),nodes:{n:{watts:40,observedAt:at}},pool:{wholeWatts:40,activeWatts:0}}
  const state:LiveState={...initialLiveState(),snapshot,metrics:[sample],lastReceivedAt:now,lastObservedAt:at,status:"fixture"}
  const require=createRequire(import.meta.url)
  let query=new URLSearchParams(),experiment="활성 없음"
  const compile=(path:string,modules:Record<string,unknown>)=>{
    const exports:Record<string,ComponentType<Record<string,unknown>>>={}
    const source=readFileSync(new URL(path,import.meta.url),"utf8")
    const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
    new Function("require","exports",compiled)((name:string)=>modules[name]??require(name),exports)
    return exports
  }
  const wrap=({children}:{children?:ReactNode})=>createElement("div",{},children)
  const status=compile("../components/status-cell.tsx",{
    "@/components/read-region":{StatusBadge:({children}:{children:ReactNode})=>createElement("strong",{},children)},
    "@/components/ui/tooltip":{Tooltip:wrap,TooltipTrigger:wrap,TooltipContent:wrap},
    "@/lib/status":{normalizeStatusLabel:(label:string)=>label},
  })
  const modules:Record<string,unknown>={
    "next/link":{default:({children,...props}:Record<string,unknown>)=>createElement("a",props,children as string)},
    "next/navigation":{usePathname:()=>"/",useSearchParams:()=>query,useRouter:()=>({push:(url:string)=>{query=new URL(url,"http://local").searchParams},replace:(url:string)=>{query=new URL(url,"http://local").searchParams}})},
    "@/lib/client":{useSession:()=>({epoch:1,config:{liveFixture:false}}),useChillQuery:()=>({})},
    "@/lib/use-live":{useLive:()=>({state,status:"fixture",reconnect:()=>{}})},
    "@/lib/live-display":displayHelpers,"@/lib/experiments":{decisionRange},
    "@/lib/live":{compareAt},
    "@/components/status-cell":status,
    "@/lib/status":{STATUS_AXES:{serviceImpact:"서비스 영향",performance:"성능 판정",observation:"관측 상태"}},
    "@/components/ui/sheet":{Sheet:({open,children}:{open:boolean;children:ReactNode})=>open?createElement("div",{"data-sheet":true},children):null,SheetContent:wrap,SheetHeader:wrap,SheetTitle:wrap},
    "./evaluation-active":{EvaluationStatusCell:()=>createElement(status.StatusCell,{axis:"실험 상태",label:experiment,tone:"info"})},
    "@/lib/evaluation-active":{evaluationInjectionMatch:()=>false},
    "@/lib/scheduler":{stringifySchedulerJSON:JSON.stringify},"@/lib/source-export":{safeSource:(value:unknown)=>value},
    "./scheduler":{judgmentLabels:{Keep:"구성 유지"},actionKinds:{power:"전력모드 변경"},actionStatuses:{},actionNodeName:(node:{name:string})=>node.name,SchedulerEvidence:({kind}:{kind:string})=>createElement("section",{"data-evidence":kind},kind)},
    "./live-board.css":{},
  }
  const board=compile("../features/live-board.tsx",modules)
  const render=()=>renderToStaticMarkup(createElement(board.LiveBoardPage,{}))
  const idle=render()
  assert.match(idle,/유휴 서비스 집계/)
  assert.match(idle,/접수 허용 개/)
  assert.match(idle,/자동제어 개/)
  assert.match(idle,/성능 판정<\/span><strong>판정 불가/)
  assert.doesNotMatch(idle,/>서비스-\d+<\/button>|live-node-power|data-sheet/)
  query=new URLSearchParams({services:"확인 필요"})
  assert.match(render(),/해당 서비스 없음/)
  assert.doesNotMatch(render(),/유휴 서비스 집계/)
  query=new URLSearchParams({view:"individual"})
  assert.equal((render().match(/>서비스-\d+<\/button>/g)??[]).length,29)
  query=new URLSearchParams({services:"확인 필요"})
  sample.workloads["service-0"]={...zero,received:10,completed:10,withinSLO:10,latencySamples:10,p95ms:20}
  snapshot.instances=[{...instance,ownerWorkloadUID:"service-0",routable:{"service-0":true},running:{"service-0":1},waiting:{}}]
  assert.match(render(),/해당 서비스 없음/)
  query=new URLSearchParams({services:"가동"})
  const active=render()
  assert.match(active,/>서비스-0<\/button>/)
  assert.doesNotMatch(active,/>서비스-1<\/button>/)
  assert.match(active,/operations\/devices\?node=n/)
  assert.match(active,/p95 20\.0/)
  snapshot.workloads[0].slo.percentile=90
  assert.match(render(),/p90 15\.0/)
  state.gaps=[{from:at,to:new Date(Date.now()+1000).toISOString(),scope:"node:n",reason:"power sample unavailable"}]
  const gap=render()
  assert.match(gap,/서비스 영향<\/span><strong>정상/)
  assert.match(gap,/관측 상태<\/span><strong>관측 공백/)
  assert.match(gap,/aria-live="polite"[^>]*>서비스 영향 정상/)
  assert.match(gap,/관측 상태 관측 공백/)
  assert.match(gap,/전력 표본 누락/)
  snapshot.actions=[{id:"shared-power",kind:"power",node:{uid:"n",name:"장치"},workloadUID:"",status:"running",startedAt:new Date(Date.now()).toISOString(),expectedSeconds:null,expectedSource:null}]
  snapshot.reservations=[{owner:"a",nodes:[],workloadUIDs:["service-1"],podUIDs:[],instances:[],reason:"ReservedScope",explanation:""}]
  const changing=render()
  assert.match(changing,/변경 건/)
  assert.match(changing,/예약 건/)
  assert.match(changing,/전력모드 변경/)
  assert.match(changing,/>진행<\/td>/)
  assert.match(changing,/예약 상세/)
  experiment="실행 중"
  assert.match(render(),/실험 상태<\/span><strong>실행 중/)
  query=new URLSearchParams({open:"service:service-0"})
  const detail=render()
  assert.equal((detail.match(/data-sheet="true"/g)??[]).length,1)
  assert.ok(detail.indexOf("서비스 SLO")<detail.indexOf("근거 조회 중"))
  assert.match(detail,/선택 시각 KST/)
  query=new URLSearchParams({at})
  assert.match(render(),/21:00:00 KST/)
  const exactRange=decisionRange(at)!
  assert.ok(render().includes(`from=${encodeURIComponent(exactRange.from)}&amp;to=${encodeURIComponent(exactRange.to)}`))
  query=new URLSearchParams({evidence:"capacity",workload:"service-0",row:"original"})
  assert.match(render(),/data-evidence="capacity"/)
  query=new URLSearchParams({open:"terms"})
  assert.match(render(),/용어 도움말/)
  assert.match(render(),/후속 유효 관측으로만 회복 확인/)
  query=new URLSearchParams()
  sample.workloads["service-1"]={...zero,failed:1}
  const stable=render()
  assert.ok(stable.indexOf(">서비스-0</button>")<stable.indexOf(">서비스-1</button>"))
  state.status="disconnected"
  query=new URLSearchParams()
  const disconnected=render()
  assert.match(disconnected,/서비스 영향<\/span><strong>미확인/)
  assert.match(disconnected,/성능 판정<\/span><strong>판정 불가/)
})

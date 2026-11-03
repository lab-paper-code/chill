import assert from "node:assert/strict"
import { test } from "node:test"
import * as display from "./live-display.ts"
import type { LiveEvent, LiveSnapshot, LiveWorkload } from "./live.ts"
import type { SchedulerCandidate } from "./read-types.ts"

const at="2026-11-03T00:00:00Z",now=Date.parse(at)
const workload:LiveWorkload={uid:"w",name:"서비스",model:"m",definitionID:"d",slo:{percentile:95,targetLatencyNanos:1000000},autonomous:false,admission:true,pending:0,readiness:{ready:false,reason:"Preparing",observedAt:at}}
const snapshot:LiveSnapshot={workloads:[workload],nodes:[],instances:[],routing:{},decision:null,actions:[],reservations:[]}
const candidate:SchedulerCandidate={id:"keep",keep:true,status:"Feasible",actions:[],reasons:[],slo:[],expectedJoules:0,worstExceedance:0}

test("R18 zero evaluation is not SLO success and identical keep targets appear once",()=>{
  assert.equal(display.liveReason("Feasible"),"실행 가능")
  assert.equal(display.candidateEvaluation(candidate),"평가 대상 없음")
  assert.equal(display.candidatePlans(candidate,structuredClone(candidate)).length,1)
  assert.equal(display.candidatePlans(candidate,{...candidate,id:"change",keep:false}).length,2)
  assert.equal(display.candidateEvaluation({...candidate,slo:[{uid:"w",definition:"d",samples:3,percentile:95,expectedNanos:1,targetNanos:2,normalizedExceedance:0,predictedOutcomes:null}]}),"평가 대상 1개")
})
test("R18 undeployed disabled Preparing has one factual state without hiding real preparation",()=>{
  assert.equal(display.workloadReadiness(workload,[]),"미배치 · 자동제어 꺼짐")
  assert.equal(display.workloadReadiness({...workload,autonomous:true},[]),"배치 준비 중")
  assert.equal(display.workloadReadiness(workload,[{id:"i",node:{uid:"n",name:"장치"},ownerWorkloadUID:"w",profileID:"p",ready:false,stopped:false,routable:{},running:{},waiting:{},observedAt:at}]),"배치 준비 중")
})
test("R18 fixed power warnings are omitted but exclusions and inventory reasons survive",()=>{
  const node={uid:"fixed",name:"고정 장치",powerMode:"fixed",ready:true,measurement:false,observedAt:at}
  const nodes=[{...node,reasons:["PowerModeUnavailable"]},{...node,uid:"g2",name:"genesis2",reasons:["ExcludedNode","PlacementInventoryMissing"]},{...node,uid:"orin",name:"orin-nano",reasons:["ExcludedNode"]},{...node,uid:"jetson",powerMode:"MAXN",reasons:["PowerModeUnavailable"]}]
  const alerts=display.deviceAlerts({...snapshot,nodes},[])
  assert.deepEqual(alerts.map(row=>row.uid),["g2","orin","jetson"])
  assert.equal(alerts[0].reason,"제외 노드 / 배치 인벤토리 없음")
  assert.equal(display.liveReason("PlacementInventoryMissing"),"배치 인벤토리 없음")
})
test("device alerts prefer detailed power observation and transition reasons without fixed-mode false alarms",()=>{
  const node={uid:"n",name:"장치",powerMode:"MAXN",ready:true,measurement:false,observedAt:at}
  const nodes=[
    {...node,uid:"observation",powerMode:"",reasons:["PowerModeUnavailable","PowerModeObservationUnavailable"]},
    {...node,uid:"transition",reasons:["PowerModeUnavailable","PowerTransitionUnavailable"]},
    {...node,uid:"fixed",powerMode:"fixed",reasons:["PowerModeUnavailable","PowerTransitionUnavailable"]},
    {...node,uid:"excluded",powerMode:"fixed",reasons:["ExcludedNode","PowerModeUnavailable","PowerTransitionUnavailable"]},
  ]
  assert.deepEqual(display.deviceAlerts({...snapshot,nodes},[]).map(row=>[row.uid,row.reason]),[
    ["observation","전력모드 관측 불가"],["transition","전력 전환 근거 없음"],["excluded","제외 노드"],
  ])
})
test("R18 five-minute decision totals count only retained distinct server judgments",()=>{
  const events:LiveEvent[]=Array.from({length:31},(_,i)=>({type:"event",kind:"decision",seq:i+1,at:new Date(now-300000+i*10000).toISOString(),decision:{at:new Date(now-300000+i*10000).toISOString(),state:"Keep",reason:"",explanation:""}}))
  const summary=display.decisionActivity([...events,events[30]],now)
  assert.deepEqual(summary,{count:31,kept:31,lastAt:events[30].decision!.at})
  assert.deepEqual(display.decisionActivity(events,now+300001),{count:0,kept:0,lastAt:null})
  assert.equal(display.decisionActivity([...events,{...events[30],at:new Date(now+1000).toISOString(),decision:{...events[30].decision!,at:new Date(now+1000).toISOString(),state:"Changed"}} as LiveEvent],now).count,31)
})
test("R18 grouped pod facts show readiness transitions instead of an unspecified reason",()=>{
  const event=(seq:number,ready:string):LiveEvent=>({type:"event",kind:"cluster",seq,at:new Date(now+seq*7000).toISOString(),cluster:{kind:"pod",at:new Date(now+seq*7000).toISOString(),event:"changed",pod:{uid:"p",name:"agent",phase:"Running",ready}}})
  const events=[event(0,"False"),event(1,"True")]
  assert.equal(display.clusterResult(events),"준비 아니오 → 예 (7초)")
  assert.equal(display.clusterResult([events[1]]),"실행 중 · 준비 예")
})

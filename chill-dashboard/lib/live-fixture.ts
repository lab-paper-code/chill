import type { LiveMetric } from "./live.ts"

// Synthetic contract scenes, never an experiment result or recorded measurement.
export const liveFixtureScenes = ["empty", "first", "expand", "shrink"] as const

export function fixtureMessages(scene:string, at = new Date().toISOString()):unknown[] {
  const end = Math.floor(Date.parse(at) / 1000) * 1000
  const time = (seconds:number) => new Date(end + seconds * 1000).toISOString()
  const nodeA = {uid:"node-a", name:"검사용 노드 A"}, nodeB = {uid:"node-b", name:"검사용 노드 B"}
  const workload = (uid:string, name:string) => ({uid, name, model:"검사용 범용 모델", definitionID:`fixture-${uid}`, slo:{percentile:95, targetLatencyNanos:800000000}, autonomous:true, admission:true, pending:0, readiness:{ready:true, reason:"Ready", observedAt:time(-4)}})
  const workloads = [workload("pool-alpha", "검사용 서비스 A"), workload("pool-beta", "검사용 서비스 B")]
  const node = (ref:typeof nodeA) => ({...ref, powerMode:"default", ready:true, measurement:false, reasons:[], observedAt:time(-4)})
  const instance = (id:string, ref:typeof nodeA, ready:boolean, routable:Record<string,boolean>, running:Record<string,number>) => ({id, node:ref, ownerWorkloadUID:"pool-alpha", profileID:"fixture-profile", ready, stopped:false, routable, running, waiting:{"pool-alpha":0,"pool-beta":0}, observedAt:time(-4)})
  const a = instance("inst-a", nodeA, true, {"pool-alpha":true,"pool-beta":true}, {"pool-alpha":2,"pool-beta":1})
  const b = instance("inst-b", nodeB, false, {"pool-alpha":false}, {"pool-alpha":0})
  const decision = {at:time(-4), state:"TransitionInProgress", reason:"검사용 확장: 새 인스턴스 준비"}
  const action = {id:"fixture-prepare", kind:"prepare", node:nodeB, workloadUID:"pool-alpha", status:"running", startedAt:time(-3), expectedSeconds:null, expectedSource:null}
  const metric = (received:number, completed:number, failed:number, withinSLO:number):LiveMetric => ({received, completed, failed, withinSLO, latencySamples:completed, p50ms:completed?220:null, p95ms:completed?620:null, p99ms:completed?790:null})
  const metrics = (seq:number, second:number, alpha:LiveMetric, beta:LiveMetric) => ({type:"metrics", seq, at:time(second+1), bucketAt:time(second), workloads:{"pool-alpha":alpha,"pool-beta":beta}, nodes:{"node-a":{watts:42.5,observedAt:time(second)},"node-b":{watts:null,observedAt:null}}, pool:{wholeWatts:null,activeWatts:42.5}, gaps:[{from:time(second),to:time(second+1),scope:"node:node-b",reason:"검사용 전력 결손"}]})
  const reservation = {owner:"fixture-prepare", nodes:[nodeB], workloadUIDs:["pool-alpha"], podUIDs:["fixture-pod"], instances:["inst-b"], reason:"미확인"}
  const messages:unknown[] = [{type:"snapshot",seq:0,at:time(-6),workloads:[],nodes:[],instances:[],routing:{},decision:null,actions:[],reservations:[]}]
  if(scene === "empty") return messages
  messages.push(
    {type:"delta",seq:1,at:time(-5),workloads,nodes:[node(nodeA)],instances:[a],routing:{"pool-alpha":{"inst-a":1},"pool-beta":{"inst-a":1}},decision:{at:time(-5),state:"Ready",reason:"검사용 첫 구성"},reservations:[]},
    {type:"event",seq:2,at:time(-5),kind:"decision",decision:{at:time(-5),state:"Ready",reason:"검사용 첫 구성"}},
    metrics(3,-5,metric(0,0,0,0),metric(0,0,0,0)),
  )
  if(scene === "first") return messages
  messages.push(
    {type:"delta",seq:4,at:time(-3),workloads:[{...workloads[0],pending:3},workloads[1]],nodes:[node(nodeA),node(nodeB)],instances:[b],decision,actions:[action],reservations:[reservation]},
    {type:"event",seq:5,at:time(-3),kind:"action",action:{...action,phase:"started"}},
    metrics(6,-3,metric(60,38,2,36),metric(17,16,1,15)),
  )
  if(scene === "expand") return messages
  messages.push(
    {type:"delta",seq:7,at:time(-1),workloads,instances:[{...a,routable:{"pool-alpha":false,"pool-beta":true},running:{"pool-alpha":1,"pool-beta":1},observedAt:time(-1)}],removed:{instances:["inst-b"]},routing:{"pool-alpha":{},"pool-beta":{"inst-a":1}},decision:{at:time(-1),state:"TransitionInProgress",reason:"검사용 축소: 잔여 실행을 기다려요"},actions:[{...action,status:"complete",completedAt:time(-1)}],reservations:[reservation]},
    {type:"event",seq:8,at:time(-1),kind:"action",action:{...action,status:"complete",completedAt:time(-1),phase:"completed",result:{status:"complete"}}},
    metrics(9,-1,metric(0,4,1,3),metric(0,0,0,0)),
    {type:"event",seq:10,at:time(0),kind:"cluster",cluster:{kind:"issue",at:time(0),event:"issue",source:"observer",reason:"검사용 E5-7 관측 장애",previousReason:"검사용 이전 사유"}},
  )
  return messages
}

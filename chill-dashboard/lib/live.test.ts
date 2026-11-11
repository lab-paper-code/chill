import assert from "node:assert/strict"
import { test, type TestContext } from "node:test"
import { applyLiveMessage, connectLive, initialLiveState, isLiveActionActive, liveActionKey, parseLiveMessage, type LiveMetric, type LiveSnapshot, type LiveState } from "./live.ts"
import { fixtureMessages } from "./live-fixture.ts"
import * as live from "./live.ts"

const at="2026-10-30T12:00:00.123456789Z"
const time=(seconds:number)=>new Date(Date.parse("2026-10-30T12:00:00Z")+seconds*1000).toISOString()
const node={uid:"node-a",name:"Node A"}
const workload={uid:"pool-a",name:"Pool A",model:"generic",definitionID:"definition-a",slo:{percentile:95,targetLatencyNanos:800_000_000},autonomous:true,admission:true,pending:2,readiness:{ready:true,reason:"Ready",observedAt:at}}
const instance={id:"instance-a",node,ownerWorkloadUID:"pool-a",profileID:"profile-a",ready:true,stopped:false,routable:{"pool-a":true,"pool-b":true},running:{"pool-a":2,"pool-b":1},waiting:{"pool-a":3,"pool-b":0},observedAt:at}
const action={id:"action-a",kind:"prepare",node,workloadUID:"pool-a",status:"running",startedAt:at,expectedSeconds:null,expectedSource:null}
const reservation={owner:"action-a",nodes:[node],workloadUIDs:["pool-a"],podUIDs:[],instances:["instance-a"],reason:"",explanation:"이전 변경이 자원을 예약하고 있어요."}
const metric:LiveMetric={received:4,completed:2,failed:1,withinSLO:1,latencySamples:3,p50ms:15,p90ms:35,p95ms:40,p99ms:60}
function snapshot(seq=0,changes:Partial<LiveSnapshot>={},stamp=at) {
  return {type:"snapshot",seq,at:stamp,workloads:[workload],nodes:[{...node,powerMode:"default",ready:false,measurement:true,reasons:["MeasurementInProgress"],observedAt:at}],instances:[instance],routing:{"pool-a":{"instance-a":1},"pool-b":{"instance-a":1}},decision:{at,state:"TransitionInProgress",reason:"prepare",explanation:"인스턴스를 준비하고 있어요."},actions:[action],reservations:[reservation],...changes}
}
function metrics(seq:number,second:number,changes:Record<string,unknown>={}) {
  return {type:"metrics",chill_obslog_queue_length:0,chill_obslog_oldest_wait_seconds:0,seq,at:time(second+1),bucketAt:time(second),workloads:{"pool-a":metric},nodes:{"node-a":{watts:null,observedAt:null}},pool:{wholeWatts:null,activeWatts:null},...changes}
}
test("metrics JSON preserves current observation-log queue gauges",()=>{
  for(const [length,wait] of [[3,1.5],[0,0]]) {
    const message=parseLiveMessage(JSON.stringify(metrics(1,0,{chill_obslog_queue_length:length,chill_obslog_oldest_wait_seconds:wait})))
    assert.equal(message?.type,"metrics")
    if(message?.type!=="metrics")throw new Error("metrics rejected")
    assert.equal(message.chill_obslog_queue_length,length)
    assert.equal(message.chill_obslog_oldest_wait_seconds,wait)
    assert.deepEqual(message.workloads,{"pool-a":metric})
    assert.deepEqual(message.pool,{wholeWatts:null,activeWatts:null})
  }
  for(const changes of [{chill_obslog_queue_length:undefined},{chill_obslog_oldest_wait_seconds:undefined},{chill_obslog_queue_length:-1},{chill_obslog_queue_length:.5},{chill_obslog_oldest_wait_seconds:-1}]) {
    assert.equal(parseLiveMessage(metrics(1,0,changes)),null)
  }
})
function apply(state:LiveState,input:unknown,receivedAt=1234) {
  const message=parseLiveMessage(input)
  assert.ok(message,"test frame must satisfy the live DTO")
  return applyLiveMessage(state,message,receivedAt)
}
test("last valid shared power outlives the metric buffer and page consumers, but not the session",()=>{
  let state=apply(initialLiveState(),snapshot())
  state=apply(state,metrics(1,0,{pool:{wholeWatts:0,activeWatts:0}}))
  for(let second=1;second<=601;second++)state=apply(state,metrics(second+1,second))
  assert.equal(state.metrics.length,600)
  assert.equal(state.metrics.some(row=>row.pool.wholeWatts!=null),false)
  const remountedConsumer={...state}
  assert.deepEqual(remountedConsumer.lastPower,{bucketAt:time(0),watts:0})
  state=apply(state,metrics(603,602,{pool:{wholeWatts:50,activeWatts:10}}))
  state=apply(state,metrics(604,600,{pool:{wholeWatts:20,activeWatts:10}}))
  assert.deepEqual(state.lastPower,{bucketAt:time(602),watts:50})
  state=apply(state,metrics(605,602))
  assert.deepEqual(state.lastPower,{bucketAt:time(600),watts:20})
  assert.equal(initialLiveState().lastPower,null)
})
test("device power scales grow only at session maxima and survive buffer expiry and page consumers",()=>{
  let state=apply(initialLiveState(),snapshot())
  state=apply(state,metrics(1,0,{nodes:{"node-a":{watts:40,observedAt:time(0)}},pool:{wholeWatts:100,activeWatts:40}}))
  assert.equal(state.powerScale,40)
  state=apply(state,metrics(2,1,{nodes:{"node-a":{watts:20,observedAt:time(1)}},pool:{wholeWatts:50,activeWatts:20}}))
  assert.equal(state.powerScale,40)
  for(let second=2;second<=601;second++)state=apply(state,metrics(second+1,second))
  assert.equal({...state}.powerScale,40)
  state=apply(state,metrics(603,602,{nodes:{"node-a":{watts:80,observedAt:time(602)}},pool:{wholeWatts:150,activeWatts:80}}))
  assert.equal(state.powerScale,80)
  assert.equal(initialLiveState().powerScale,1)
})
test("one shared scale includes every metered device and never shrinks on corrections",()=>{
  let state=apply(initialLiveState(),snapshot())
  state=apply(state,metrics(1,0,{nodes:{"node-a":{watts:40,observedAt:time(0)},outside:{watts:120,observedAt:time(0)}},pool:{wholeWatts:160,activeWatts:40}}))
  assert.equal(state.powerScale,120)
  state=apply(state,metrics(2,0,{nodes:{"node-a":{watts:10,observedAt:time(0)}}}))
  assert.equal(state.powerScale,120)
})

test("active changes exclude terminal history and not-yet-started plans on both screens",()=>{
  for (const status of ["starting","running"]) assert.equal(isLiveActionActive({status}),true)
  for (const status of ["planned","complete","failed","partial","cancel"]) assert.equal(isLiveActionActive({status}),false)
  assert.equal(isLiveActionActive({status:"running",completedAt:at}),false)
})

test("parser accepts the server DTO and only normalizes nullable Go collections",()=>{
  const message=parseLiveMessage(snapshot(0,{instances:[{...instance,routable:null,running:null,waiting:null} as unknown as typeof instance],reservations:[{...reservation,workloadUIDs:null,podUIDs:null,instances:null} as unknown as typeof reservation]}))
  assert.ok(message?.type === "snapshot")
  assert.deepEqual(message.instances[0].routable,{})
  assert.deepEqual(message.instances[0].running,{})
  assert.deepEqual(message.instances[0].waiting,{})
  assert.deepEqual(message.reservations[0].podUIDs,[])
  assert.equal(message.nodes[0].ready,false)
  assert.deepEqual(message.nodes[0].reasons,["MeasurementInProgress"])
  assert.equal(message.workloads[0].pending,2)
  assert.equal(message.at,at)
  assert.ok(parseLiveMessage(JSON.stringify(snapshot())))
})

test("trust boundary rejects invalid envelopes, missing consumed fields and invalid numbers",()=>{
  for (const input of [null,[],"{",{type:"raw",seq:1,at},{...snapshot(),seq:2**53},{...snapshot(),seq:-1},{...snapshot(),at:"2026-02-30T12:00:00Z"},{...snapshot(),at:"2026-10-30T12:00:00+00:00"},{...snapshot(),nodes:null},{...snapshot(),instances:[{...instance,waiting:undefined}]},{...snapshot(),instances:[{...instance,running:{"pool-a":-1}}]},{...snapshot(),workloads:[{...workload,pending:0.5}]},{...snapshot(),actions:[{...action,expectedSeconds:Infinity}]},{...metrics(1,0),workloads:{"pool-a":{...metric,withinSLO:3}}},{...metrics(1,0),bucketAt:at},{...metrics(1,0),nodes:{"node-a":{watts:NaN,observedAt:null}}},{type:"event",seq:1,at,kind:"cluster",cluster:{kind:"pod",at,event:"pod",pod:{uid:"pod-a",ready:true}}},{type:"event",seq:1,at,kind:"decision",decision:{at,state:"Ready",reason:""},cluster:{kind:"issue",at,event:"issue"}},{type:"hello",seq:1,at,protocol:"chill-live/2",serverAt:at,retainFrom:1}]) {
    assert.equal(parseLiveMessage(input),null)
  }
  assert.equal(parseLiveMessage(" ".repeat((1<<20)+1)),null)
})

test("hello cannot advance checkpoint; heartbeat is reception, not observation",()=>{
  let state=apply(initialLiveState(),{type:"hello",seq:500,at,protocol:"chill-live/1",serverAt:at,retainFrom:1},1)
  assert.equal(state.checkpoint,null)
  assert.equal(state.lastObservedAt,null)
  assert.equal(state.status,"resuming")
  state=apply(state,snapshot(8),2)
  state=apply(state,{type:"heartbeat",seq:9,at:time(20),serverAt:time(20)},3)
  assert.deepEqual(state.checkpoint,{seq:9,at:time(20)})
  assert.equal(state.lastReceivedAt,3)
  assert.equal(state.lastObservedAt,at)
  assert.equal(state.snapshot?.instances[0].observedAt,at)
})

test("delta replaces full collections, upserts full rows, then removes identities",()=>{
  const initial=apply(initialLiveState(),snapshot())
  let state=apply(initial,{type:"delta",seq:1,at,workloads:[],nodes:[],routing:{},reservations:[],decision:null,instances:[{...instance,ready:false,running:{"pool-b":4}}],actions:[{...action,status:"complete",completedAt:at}]})
  assert.deepEqual(state.snapshot?.workloads,[])
  assert.deepEqual(state.snapshot?.nodes,[])
  assert.deepEqual(state.snapshot?.routing,{})
  assert.deepEqual(state.snapshot?.reservations,[])
  assert.equal(state.snapshot?.decision,null)
  assert.deepEqual(state.snapshot?.instances[0].running,{"pool-b":4})
  assert.equal(state.snapshot?.actions[0].status,"complete")
  assert.ok(state.definitions[JSON.stringify(["pool-a","definition-a"])])
  assert.equal(initial.snapshot?.instances[0].ready,true)
  state=apply(state,{type:"delta",seq:2,at,instances:[instance],actions:[action],removed:{instances:[instance.id],actions:[liveActionKey(action)]}})
  assert.deepEqual(state.snapshot?.instances,[])
  assert.deepEqual(state.snapshot?.actions,[])
  assert.ok(state.definitions[JSON.stringify(["pool-a","definition-a"])])
})

test("action identity includes node and original nanosecond start, not only operation id",()=>{
  const another={...action,node:{uid:"node-b",name:"Node B"},startedAt:"2026-10-30T12:00:00.123456790Z",expectedSeconds:4,expectedSource:{costID:"cost-a",reference:{kind:"OperationCost",name:"measured-a"},statistic:"mean" as const,trialCount:3}}
  let state=apply(initialLiveState(),snapshot())
  state=apply(state,{type:"delta",seq:1,at,actions:[another]})
  assert.equal(state.snapshot?.actions.length,2)
  state=apply(state,{type:"delta",seq:2,at,removed:{actions:[liveActionKey(action)]}})
  assert.deepEqual(state.snapshot?.actions,[another])
  assert.equal(state.snapshot?.reservations.length,1)
})

test("a shared instance preserves independent routing/running/waiting for every pool",()=>{
  let state=apply(initialLiveState(),snapshot())
  state=apply(state,{type:"delta",seq:1,at,instances:[{...instance,routable:{"pool-a":false,"pool-b":true},running:{"pool-a":1,"pool-b":2},waiting:{"pool-a":0,"pool-b":5}}]})
  assert.deepEqual(state.snapshot?.instances[0].routable,{"pool-a":false,"pool-b":true})
  assert.deepEqual(state.snapshot?.instances[0].running,{"pool-a":1,"pool-b":2})
  assert.deepEqual(state.snapshot?.instances[0].waiting,{"pool-a":0,"pool-b":5})
  assert.deepEqual(state.snapshot?.routing,{"pool-a":{"instance-a":1},"pool-b":{"instance-a":1}})
  assert.equal(state.snapshot?.reservations.length,1)
})

test("same-second corrections replace the whole bucket and its gaps without manufacturing zeros",()=>{
  let state=apply(initialLiveState(),snapshot())
  state=apply(state,metrics(1,0,{gaps:[{from:time(0),to:time(1),scope:"node:node-a",reason:"missing power"}]}))
  state=apply(state,metrics(2,0,{workloads:{"pool-b":{...metric,received:0}},nodes:{},pool:{wholeWatts:0,activeWatts:null}}))
  assert.equal(state.metrics.length,1)
  assert.equal(state.metrics[0].workloads["pool-a"],undefined)
  assert.equal(state.metrics[0].workloads["pool-b"].received,0)
  assert.deepEqual(state.metrics[0].nodes,{})
  assert.equal(state.metrics[0].pool.wholeWatts,0)
  assert.equal(state.metrics[0].pool.activeWatts,null)
  assert.deepEqual(state.gaps,[])
  state=apply(state,metrics(3,5,{workloads:{},gaps:[{from:time(5),to:time(6),scope:"workloads",reason:"unavailable"}]}))
  assert.deepEqual(state.metrics.map(row=>row.bucketAt),[time(0),time(5)])
  assert.deepEqual(state.metrics[1].workloads,{})
  assert.equal(state.gaps[0].reason,"unavailable")
})

test("metric retention is 600 recent seconds, not 600 sparse frames",()=>{
  let state=apply(initialLiveState(),snapshot())
  for (let second=0;second<=600;second++) state=apply(state,metrics(second+1,second))
  assert.equal(state.metrics.length,600)
  assert.equal(state.metrics[0].bucketAt,time(1))
  state=apply(state,metrics(602,1500))
  assert.equal(state.metrics.length,1)
  assert.equal(state.metrics[0].bucketAt,time(1500))
})

test("duplicates are ignored, seq jumps request resync and snapshots preserve lost-history gaps",()=>{
  let state=apply(initialLiveState(),snapshot(10))
  const baseline=state.snapshot
  state=apply(state,snapshot(10,{workloads:[]}))
  assert.equal(state.snapshot,baseline,"duplicate snapshot checkpoint cannot replace state")
  state=apply(state,metrics(11,0))
  const before=state.snapshot
  state=apply(state,{type:"delta",seq:11,at,workloads:[]})
  assert.equal(state.snapshot,before)
  state=apply(state,{type:"delta",seq:14,at:time(30),workloads:[]})
  assert.equal(state.checkpoint?.seq,11)
  assert.equal(state.snapshot,before)
  assert.equal(state.resyncRequired,true)
  state=apply(state,{type:"delta",seq:12,at,workloads:[]})
  assert.equal(state.snapshot,before)
  state=apply(state,snapshot(20,{instances:[]},time(30)))
  assert.equal(state.checkpoint?.seq,20)
  assert.deepEqual(state.snapshot?.instances,[])
  assert.equal(state.resyncRequired,false)
  assert.equal(state.gaps[0].scope,"all")
  assert.equal(state.gaps[0].reason,"resume history unavailable")
  assert.equal(state.metrics.length,1)
  state=apply(state,snapshot(20,{},time(31)))
  assert.equal(state.checkpoint?.at,time(31),"same seq with a different original at is a new baseline")
})

test("cluster events need no delta, event identities deduplicate, and events never mutate current state",()=>{
  let state=apply(initialLiveState(),snapshot())
  const current=state.snapshot
  const event={type:"event",seq:1,at:time(2),kind:"cluster",cluster:{kind:"pod",at:time(2),event:"pod",pod:{uid:"pod-a",ready:"False"},source:"observer",reason:"NotReady"}}
  state=apply(state,event)
  state=apply(state,{...event,seq:2})
  assert.equal(state.events.length,1)
  assert.equal(state.snapshot,current)
  assert.equal(state.lastObservedAt,time(2))
  state=apply(state,{type:"event",seq:3,at:time(3),kind:"action",action:{...action,status:"complete",completedAt:time(3),phase:"completed",result:{status:"partial",error:"native failed"}}})
  assert.equal(state.snapshot?.actions[0].status,"running")
  assert.equal(state.snapshot?.reservations.length,1)
})

test("event cap records omitted history and retains 1000 events",()=>{
  let state=apply(initialLiveState(),snapshot())
  for (let seq=1;seq<=1002;seq++) state=apply(state,{type:"event",seq,at:time(seq),kind:"cluster",cluster:{kind:"issue",at:time(seq),event:"issue",source:"observer",reason:`issue-${seq}`}})
  assert.equal(state.events.length,1000)
  assert.equal(state.events[0].seq,3)
  assert.deepEqual(state.gaps.filter(row=>row.scope==="events"),[{from:time(1),to:time(3),scope:"events",reason:"event history limit"}])
})

test("unchanged Keep traffic cannot evict the last actual change",()=>{
  let state=apply(initialLiveState(),snapshot())
  state=apply(state,{type:"event",seq:1,at:time(1),kind:"cluster",cluster:{kind:"issue",at:time(1),event:"issue",source:"observer",reason:"remember"}})
  for(let seq=2;seq<=1002;seq++) state=apply(state,{type:"event",seq,at:time(seq),kind:"decision",decision:{at:time(seq),state:"Keep",reason:"",explanation:"유지"}})
  assert.equal(state.events.length,1000)
  assert.equal(state.events[0].seq,1)
})

test("all synthetic scenes use the real DTO parser/reducer",()=>{
  for (const scene of ["empty","first","expand","shrink"]) {
    let state=initialLiveState()
    for (const message of fixtureMessages(scene,time(0))) state=apply(state,message)
    assert.ok(state.snapshot)
    assert.equal(state.resyncRequired,false)
    if (scene === "empty") assert.equal(state.snapshot.workloads.length,0)
    if (scene === "shrink") {
      assert.equal(state.snapshot.instances[0].running["pool-alpha"],1)
      assert.equal(state.snapshot.instances[0].routable["pool-beta"],true)
      assert.equal(state.snapshot.reservations.length,1)
      assert.equal(state.events.at(-1)?.kind,"cluster")
    }
  }
})

class FakeSocket {
  static sockets:FakeSocket[]=[]
  url:string;sent:string[]=[];closed=false
  onopen:((event:unknown)=>void)|null=null
  onmessage:((event:{data:string})=>void)|null=null
  onclose:((event:{code:number})=>void)|null=null
  onerror:((event:unknown)=>void)|null=null
  constructor(url:URL) { this.url=String(url);FakeSocket.sockets.push(this) }
  send(value:string) { this.sent.push(value) }
  open() { this.onopen?.({}) }
  message(value:unknown) { this.onmessage?.({data:JSON.stringify(value)}) }
  close(code=1000) { this.closed=true;this.onclose?.({code}) }
}
function browser(t:TestContext,initial=initialLiveState(),visibility="visible") {
  const descriptors=new Map(["WebSocket","document","location"].map(key=>[key,Object.getOwnPropertyDescriptor(globalThis,key)]))
  const document=new EventTarget() as EventTarget & {visibilityState:string}
  document.visibilityState=visibility
  Object.defineProperty(globalThis,"WebSocket",{configurable:true,value:FakeSocket})
  Object.defineProperty(globalThis,"document",{configurable:true,value:document})
  Object.defineProperty(globalThis,"location",{configurable:true,value:{href:"https://dashboard.example/pools/pool-a?open=inst:a"}})
  FakeSocket.sockets=[]
  t.mock.timers.enable({apis:["setTimeout"]})
  const store={state:initial}
  const connection=connectLive("memory-only-token",()=>store.state,change=>{store.state=change(store.state)})
  t.after(()=>{
    connection.close()
    t.mock.timers.reset()
    for (const [key,descriptor] of descriptors) {
      if (descriptor) Object.defineProperty(globalThis,key,descriptor)
      else Reflect.deleteProperty(globalThis,key)
    }
  })
  return {store,connection,document}
}
function hello(seq=0) { return {type:"hello",seq,at,protocol:"chill-live/1",serverAt:at,retainFrom:1} }

test("browser transport authenticates first, resumes after hello with original checkpoint, and cleans up",t=>{
  const {store,connection}=browser(t,apply(initialLiveState(),snapshot(8)))
  const first=FakeSocket.sockets[0]
  assert.equal(first.url,"wss://dashboard.example/api/live")
  first.open()
  assert.deepEqual(first.sent.map(value=>JSON.parse(value)),[{type:"auth",token:"memory-only-token"}])
  first.message(hello(9))
  assert.deepEqual(JSON.parse(first.sent[1]),{type:"resume",from:8,at})
  assert.equal(store.state.checkpoint?.seq,8)
  first.message({type:"heartbeat",seq:9,at:time(1),serverAt:time(1)})
  first.close(1006)
  t.mock.timers.tick(600)
  const second=FakeSocket.sockets[1]
  second.open();second.message(hello(10))
  assert.deepEqual(JSON.parse(second.sent[1]),{type:"resume",from:9,at:time(1)})
  const lateMessage=second.onmessage
  const beforeClose=store.state
  connection.close()
  assert.equal(second.closed,true)
  lateMessage?.({data:JSON.stringify(snapshot(100))})
  assert.equal(store.state,beforeClose,"late callback from disposed identity cannot repopulate data")
  t.mock.timers.tick(60_000)
  connection.reconnect()
  assert.equal(FakeSocket.sockets.length,2)
})

test("transport requests a baseline for a seq gap, not REST data or partial future state",t=>{
  const {store}=browser(t)
  const socket=FakeSocket.sockets[0]
  socket.open();socket.message(hello())
  assert.deepEqual(JSON.parse(socket.sent[1]),{type:"resume",from:0})
  socket.message(snapshot(2))
  socket.message({type:"delta",seq:6,at:time(6),workloads:[]})
  assert.equal(store.state.snapshot?.workloads.length,1)
  assert.deepEqual(JSON.parse(socket.sent.at(-1)!),{type:"resume",from:0})
  const sent=socket.sent.length
  socket.message({type:"delta",seq:7,at:time(7),workloads:[]})
  assert.equal(socket.sent.length,sent)
  socket.message(snapshot(7,{workloads:[]},time(7)))
  assert.equal(store.state.status,"live")
  assert.equal(store.state.snapshot?.workloads.length,0)
  assert.equal(store.state.gaps[0].reason,"resume history unavailable")
})

for (const code of [4401,4403]) test(`auth close ${code} hides data and stops retries including visibility/manual reconnect`,t=>{
  const {store,connection,document}=browser(t,apply(initialLiveState(),snapshot(8)))
  const socket=FakeSocket.sockets[0]
  socket.open();socket.close(code)
  assert.equal(store.state.snapshot,null)
  assert.deepEqual(store.state.metrics,[])
  assert.deepEqual(store.state.definitions,{})
  assert.equal(store.state.status,code===4401 ? "unauthorized" : "forbidden")
  t.mock.timers.tick(60_000)
  connection.reconnect();document.dispatchEvent(new Event("visibilitychange"))
  assert.equal(FakeSocket.sockets.length,1)
})

test("hidden tabs close without polling, visible tabs resume, and reconnect delay stays bounded",t=>{
  t.mock.method(Math,"random",()=>0)
  const {document,store}=browser(t,apply(initialLiveState(),snapshot(8)))
  const first=FakeSocket.sockets[0]
  first.open();first.message(hello(8))
  document.visibilityState="hidden";document.dispatchEvent(new Event("visibilitychange"))
  assert.equal(first.closed,true)
  assert.equal(store.state.status,"paused-hidden")
  t.mock.timers.tick(120_000)
  assert.equal(FakeSocket.sockets.length,1)
  document.visibilityState="visible";document.dispatchEvent(new Event("visibilitychange"))
  let socket=FakeSocket.sockets.at(-1)!
  socket.open();socket.message(hello(8))
  assert.deepEqual(JSON.parse(socket.sent[1]),{type:"resume",from:8,at})
  for (let retry=0;retry<10;retry++) {
    const count:number=FakeSocket.sockets.length
    socket.close(1013)
    const delay=Math.min(30_000,500*2**Math.min(retry,6))
    t.mock.timers.tick(delay-1)
    assert.equal(FakeSocket.sockets.length,count)
    t.mock.timers.tick(1)
    assert.equal(FakeSocket.sockets.length,count+1)
    socket=FakeSocket.sockets.at(-1)!
  }
})

test("an initially hidden tab is paused rather than disconnected or connecting",t=>{
  const {store}=browser(t,initialLiveState(),"hidden")
  assert.equal(store.state.status,"paused-hidden")
  assert.equal(FakeSocket.sockets.length,0)
})

test("additive response metadata keeps the live connection and consumed projections current",t=>{
  const {store}=browser(t)
  const socket=FakeSocket.sockets[0]
  const extension={version:2,trace:"new-server"}
  socket.open();socket.message({...hello(),extension})
  const initial=snapshot()
  socket.message({...initial,extension,
    workloads:[{...workload,extension,slo:{...workload.slo,extension},readiness:{...workload.readiness,extension}}],
    nodes:initial.nodes.map(row=>({...row,extension})),
    instances:[{...instance,extension,node:{...node,extension}}],
    decision:{...initial.decision,extension},
    actions:[{...action,extension,node:{...node,extension}}],
    reservations:[{...reservation,extension,nodes:[{...node,extension}]}],
  })
  socket.message({...metrics(1,0),extension,pool:{wholeWatts:50,activeWatts:20,extension},nodes:{"node-a":{watts:20,observedAt:time(0),extension}},workloads:{"pool-a":{...metric,extension}}})
  socket.message({type:"delta",seq:2,at,extension,workloads:[{...workload,pending:7,extension}],removed:{instances:[],extension}})
  socket.message({type:"event",seq:3,at,kind:"cluster",extension,cluster:{kind:"pod",at,event:"pod",extension,pod:{uid:"pod-a",name:"new-pod",extension}}})
  assert.equal(socket.closed,false)
  assert.equal(store.state.status,"live")
  assert.equal(store.state.snapshot?.workloads[0].pending,7)
  assert.equal(store.state.metrics.at(-1)?.pool.wholeWatts,50)
  assert.equal(store.state.events.at(-1)?.cluster?.pod?.name,"new-pod")
})

for (const {name,payload,reason} of [
  {name:"collection",payload:{...snapshot(),workloads:"not an array"},reason:/snapshot: workloads: Expected array/},
  {name:"nested field",payload:{...snapshot(),actions:[{...action,expectedSource:{costID:"cost-a",reference:null,statistic:"mean",trialCount:1}}]},reason:/snapshot: actions: 0: expectedSource.reference: Expected object/},
  {name:"unsafe counter",payload:{...metrics(1,0),workloads:{"pool-a":{...metric,received:2**53}}},reason:/metrics: workloads: pool-a: received: Expected safe integer/},
  {name:"envelope",payload:{...snapshot(),seq:-1},reason:/snapshot: seq: Expected finite nonnegative number/},
  {name:"hello contract",payload:{...hello(),protocol:"chill-live/2"},reason:/hello: protocol: Expected chill-live\/1/},
  {name:"unknown type",payload:{type:"future",seq:1,at},reason:/future: type: Unknown live frame type/},
  {name:"invalid JSON",payload:"{",reason:/unknown: .+/},
]) test(`malformed ${name} closes with an application code and retains the parser rejection`,t=>{
  const {store,connection,document}=browser(t)
  const socket=FakeSocket.sockets[0],close=t.mock.method(socket,"close")
  socket.open();socket.message(hello())
  socket.onmessage?.({data:typeof payload==="string" ? payload : JSON.stringify(payload)})
  const code=close.mock.calls[0]?.arguments[0]
  assert.ok(code!==undefined && code>=4000 && code<=4999)
  assert.notEqual(code,1002)
  assert.equal(socket.closed,true)
  assert.equal(store.state.snapshot,null)
  assert.equal(store.state.status,"protocol-error")
  assert.match(store.state.protocolError??"",reason)
  t.mock.timers.tick(60_000)
  document.visibilityState="hidden";document.dispatchEvent(new Event("visibilitychange"))
  document.visibilityState="visible";document.dispatchEvent(new Event("visibilitychange"))
  assert.equal(FakeSocket.sockets.length,1)
  assert.equal(store.state.status,"protocol-error")
  assert.match(store.state.protocolError??"",reason)
  connection.reconnect()
  assert.equal(FakeSocket.sockets.length,2)
})

for (const duplicate of [false,true]) test(`hello ordering rejection is retained (${duplicate ? "duplicate" : "missing"})`,t=>{
  const {store}=browser(t)
  const socket=FakeSocket.sockets[0],close=t.mock.method(socket,"close")
  socket.open()
  if (duplicate) socket.message(hello())
  socket.message(duplicate ? hello() : snapshot())
  assert.equal(close.mock.calls[0]?.arguments[0],4002)
  assert.equal(store.state.status,"protocol-error")
  assert.match(store.state.protocolError??"",duplicate ? /hello: Unexpected duplicate hello/ : /snapshot: Expected hello before other frames/)
})

test("snapshot history preserves fresh/unmatched retention metadata without seeding observations",()=>{
  const baseline=apply(initialLiveState(),snapshot(10)).snapshot
  const empty={reason:"fresh",replayed:false,requested:{afterSeq:0,throughSeq:0},retained:{events:null,power:null},gaps:[]}
  const unmatched={reason:"checkpoint-unmatched",replayed:false,requested:{afterSeq:2,throughSeq:10,at},retained:{events:{fromSeq:5,toSeq:7},power:null},gaps:[{fromSeq:3,toSeq:4,scope:"events",reason:"NoRetainedFrames"},{fromSeq:3,toSeq:10,scope:"power",reason:"NoRetainedFrames"}]}
  for(const history of [empty,unmatched,{...unmatched,requested:{afterSeq:11,throughSeq:10,at},gaps:[]}]){
    const message=parseLiveMessage({...snapshot(10),history})
    assert.ok(message?.type==="snapshot")
    assert.deepEqual(message.history,history)
    const state=applyLiveMessage(initialLiveState(),message)
    assert.deepEqual(state.resumeHistory,history)
    assert.deepEqual(state.snapshot,baseline)
    assert.deepEqual(state.events,[])
    assert.deepEqual(state.metrics,[])
    assert.deepEqual(state.gaps,[],"inclusive sequence bounds are not observation-time gaps")
  }
  assert.ok(parseLiveMessage(snapshot()),"history remains optional")
})

test("history validates nested types and inclusive ranges without treating bounds as event counts",()=>{
  const history={reason:"fresh",replayed:false,requested:{afterSeq:0,throughSeq:10},retained:{events:{fromSeq:2,toSeq:2},power:null},gaps:[{fromSeq:1,toSeq:1,scope:"events",reason:"NoRetainedFrames"}]}
  assert.ok(parseLiveMessage({...snapshot(10),history}))
  const extended=parseLiveMessage({...snapshot(10),history:{...history,unknown:true,requested:{...history.requested,extension:"new"},retained:{...history.retained,extension:"new"}}})
  assert.ok(extended)
  const current=applyLiveMessage(initialLiveState(),extended)
  assert.equal(current.resumeHistory?.retained.events?.fromSeq,2)
  assert.equal(current.resumeHistory?.gaps[0].toSeq,1)
  for(const invalid of [
    {...history,reason:"unknown"},{...history,replayed:true},{...history,retained:{events:null}},
    {...history,requested:{afterSeq:-1,throughSeq:10}},{...history,requested:{afterSeq:0,throughSeq:10,at:"bad"}},
    {...history,retained:{events:{fromSeq:3,toSeq:2},power:null}},
    {...history,retained:{events:{fromSeq:0,toSeq:2},power:null}},
    {...history,retained:{events:{fromSeq:2,toSeq:11},power:null}},
    {...history,gaps:[{fromSeq:1,toSeq:11,scope:"events",reason:"NoRetainedFrames"}]},
    {...history,gaps:[{fromSeq:2,toSeq:1,scope:"events",reason:"NoRetainedFrames"}]},
    {...history,gaps:[{fromSeq:1,toSeq:1,scope:"events",reason:"invented"}]},
    {...history,gaps:[{fromSeq:1,toSeq:1,scope:"unknown",reason:"NoRetainedFrames"}]},
    {...history,gaps:null},
  ])assert.equal(parseLiveMessage({...snapshot(10),history:invalid}),null)
})

test("recorded events reuse payload validation without inventing a websocket sequence",()=>{
  assert.equal(typeof live.parseRecordedLiveEvent,"function")
  const recorded={type:"event",at,kind:"cluster",cluster:{kind:"pod",at,event:"changed",pod:{uid:"p",ready:"True"}}}
  const parsed=live.parseRecordedLiveEvent(recorded)
  assert.ok(parsed)
  assert.equal(Object.hasOwn(parsed,"seq"),false)
  assert.equal(parseLiveMessage(recorded),null,"WS events still require the envelope sequence")
  const page=live.parseRecordedLivePage({events:[{...recorded,seq:99,extension:true}],from:time(0),to:time(1),coverage:{from:null,to:null,sources:[],gaps:[]},extension:true})
  assert.ok(page)
  const current=apply(initialLiveState(),snapshot(8))
  const archived=live.applyRecordedLivePage(current,page,true)
  assert.equal(archived.events.at(-1)?.cluster?.pod?.uid,"p")
  assert.equal(Object.hasOwn(archived.events.at(-1)!,"seq"),false)
  assert.equal(archived.checkpoint,current.checkpoint)
  assert.equal(live.parseRecordedLiveEvent({...recorded,cluster:{...recorded.cluster,pod:{uid:"p",ready:true}}}),null)
  assert.equal(live.parseRecordedLiveEvent({type:"event",at,kind:"action",action:{...action,phase:"completed",result:{status:"complete"}}})?.kind,"action")
})

test("retention metadata suppresses the legacy inferred time gap even on a changed checkpoint",()=>{
  const history={reason:"checkpoint-unmatched",replayed:false,requested:{afterSeq:2,throughSeq:20,at},retained:{events:null,power:null},gaps:[{fromSeq:3,toSeq:20,scope:"events",reason:"NoRetainedFrames"}]}
  let state=apply(initialLiveState(),snapshot(2))
  state=apply(state,{...snapshot(20,{},time(30)),history})
  assert.deepEqual(state.historyGaps,[])
  assert.deepEqual(state.gaps,[])
  assert.deepEqual(state.resumeHistory,history)
  const baseline=state.snapshot
  state=apply(state,{...snapshot(20,{workloads:[]},time(30)),history:{...history,gaps:[]}})
  assert.equal(state.snapshot,baseline,"duplicate metadata cannot replace current snapshot data")
  assert.deepEqual(state.resumeHistory,{...history,gaps:[]})
})

test("payload event identity ignores transport sequence, arrival time and object insertion order",()=>{
  const left=live.parseRecordedLiveEvent({type:"event",at,kind:"action",action:{...action,phase:"started",expectedSeconds:3,expectedSource:{costID:"c",reference:{kind:"Cost",name:"sample"},statistic:"mean",trialCount:1}}})!
  const right=live.parseRecordedLiveEvent({type:"event",at:time(1),kind:"action",action:{...action,phase:"started",expectedSeconds:3,expectedSource:{costID:"c",reference:{name:"sample",kind:"Cost"},statistic:"mean",trialCount:1}}})!
  assert.equal(live.eventKey(left),live.eventKey({...right,seq:9}))
  if(right.kind==="action")assert.notEqual(live.eventKey(left),live.eventKey({...right,action:{...right.action,status:"failed"}}))
})

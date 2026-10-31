import assert from "node:assert/strict"
import { test, type TestContext } from "node:test"
import { applyLiveMessage, connectLive, initialLiveState, isLiveActionActive, liveActionKey, parseLiveMessage, type LiveMetric, type LiveSnapshot, type LiveState } from "./live.ts"
import { fixtureMessages } from "./live-fixture.ts"

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
  return {type:"metrics",seq,at:time(second+1),bucketAt:time(second),workloads:{"pool-a":metric},nodes:{"node-a":{watts:null,observedAt:null}},pool:{wholeWatts:null,activeWatts:null},...changes}
}
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
  assert.deepEqual(state.powerScale,{nodes:{"node-a":40},other:60})
  state=apply(state,metrics(2,1,{nodes:{"node-a":{watts:20,observedAt:time(1)}},pool:{wholeWatts:50,activeWatts:20}}))
  assert.deepEqual(state.powerScale,{nodes:{"node-a":40},other:60})
  for(let second=2;second<=601;second++)state=apply(state,metrics(second+1,second))
  assert.deepEqual({...state}.powerScale,{nodes:{"node-a":40},other:60})
  state=apply(state,metrics(603,602,{nodes:{"node-a":{watts:80,observedAt:time(602)}},pool:{wholeWatts:150,activeWatts:80}}))
  assert.deepEqual(state.powerScale,{nodes:{"node-a":80},other:70})
  assert.deepEqual(initialLiveState().powerScale,{nodes:{},other:1})
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

test("trust boundary rejects invalid envelopes, old DTOs, missing fields and invalid numbers",()=>{
  for (const input of [null,[],"{",{type:"raw",seq:1,at},{...snapshot(),seq:2**53},{...snapshot(),seq:-1},{...snapshot(),at:"2026-02-30T12:00:00Z"},{...snapshot(),at:"2026-10-30T12:00:00+00:00"},{...snapshot(),nodes:null},{...snapshot(),instances:[{...instance,state:"running"}]},{...snapshot(),instances:[{...instance,waiting:undefined}]},{...snapshot(),instances:[{...instance,running:{"pool-a":-1}}]},{...snapshot(),workloads:[{...workload,pending:0.5}]},{...snapshot(),actions:[{...action,expectedSeconds:Infinity}]},{...metrics(1,0),workloads:{"pool-a":{...metric,withinSLO:3}}},{...metrics(1,0),bucketAt:at},{...metrics(1,0),nodes:{"node-a":{watts:NaN,observedAt:null}}},{type:"event",seq:1,at,kind:"cluster",cluster:{kind:"pod",at,event:"pod",pod:{uid:"pod-a",ready:true}}},{type:"event",seq:1,at,kind:"decision",decision:{at,state:"Ready",reason:""},cluster:{kind:"issue",at,event:"issue"}},{type:"hello",seq:1,at,protocol:"chill-live/2",serverAt:at,retainFrom:1}]) {
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

test("malformed payload closes the connection before data can enter the cache",t=>{
  const {store,connection}=browser(t)
  const socket=FakeSocket.sockets[0]
  socket.open();socket.message(hello());socket.message({...snapshot(),workloads:"not an array"})
  assert.equal(socket.closed,true)
  assert.equal(store.state.snapshot,null)
  assert.equal(store.state.status,"protocol-error")
  t.mock.timers.tick(60_000)
  assert.equal(FakeSocket.sockets.length,1)
  connection.reconnect()
  assert.equal(FakeSocket.sockets.length,2)
})

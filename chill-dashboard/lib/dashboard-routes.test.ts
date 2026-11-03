import assert from "node:assert/strict"
import test from "node:test"
import { dashboardDestination, dashboardRedirects } from "./dashboard-routes.ts"

test("legacy routes preserve duplicate filters, exact windows and selections at canonical destinations",()=>{
  const original=new URLSearchParams("from=2026-10-31T01%3A02%3A03.123456789Z&to=2026-10-31T02%3A02%3A03Z&uid=u%2Fid&filter=failed&filter=late&namespace=ns&a=publication&as=stage&b=baseline&bs=baseline-stage")
  for(const route of dashboardRedirects) {
    const result=new URL(dashboardDestination(route.source,original)!,"https://dashboard.invalid")
    const expected=new URL(route.source==="/evaluations"?"/experiments/results":route.destination,"https://dashboard.invalid")
    assert.equal(result.pathname,expected.pathname,route.source)
    for(const key of new Set(original.keys()))if(!expected.searchParams.has(key))assert.deepEqual(result.searchParams.getAll(key),original.getAll(key),`${route.source}: ${key}`)
    for(const [key,value] of expected.searchParams)assert.equal(result.searchParams.get(key),value)
  }
  assert.equal(dashboardDestination("/experiments",new URLSearchParams("a=pub&as=stage&al=left&b=base&bs=other")),"/experiments/results?a=pub&as=stage&al=left&b=base&bs=other")
  assert.equal(dashboardDestination("/experiments",new URLSearchParams()),null)
  assert.equal(dashboardDestination("/experiments/results",original),null)
  assert.equal(dashboardDestination("/operations/devices",original),null)
  assert.equal(dashboardDestination("/measurements/results",original),null)
})

test("legacy pool and measurement detail map identities without truncating or replacing UID",()=>{
  const pool=new URL(dashboardDestination("/pools/u%2Fid",new URLSearchParams("fixture=expand&from=exact"))!,"https://dashboard.invalid")
  assert.equal(pool.pathname,"/")
  assert.equal(pool.searchParams.get("open"),"service:u/id")
  assert.equal(pool.searchParams.get("fixture"),"expand")
  for(const [path,kind] of [["operation-cost-measurements","operation-cost"],["dispatch-occupancy-measurements","dispatch-occupancy"]]) {
    const result=new URL(dashboardDestination(`/measurements/${path}/ns%20a/name%2Fa`,new URLSearchParams("uid=old-uid&node=node-a&namespace=old"))!,"https://dashboard.invalid")
    assert.equal(result.pathname,"/measurements/results")
    assert.equal(result.searchParams.get("kind"),kind)
    assert.equal(result.searchParams.get("namespace"),"ns a")
    assert.equal(result.searchParams.get("measurement"),"name/a")
    assert.equal(result.searchParams.get("uid"),"old-uid")
    assert.equal(result.searchParams.get("node"),"node-a")
  }
  const request=new URL(dashboardDestination("/measurements/batch-energy-optimizations/ns%20a/request%2Fa",new URLSearchParams("uid=request-uid"))!,"https://dashboard.invalid")
  assert.equal(request.pathname,"/measurements/requests/request%2Fa")
  assert.equal(request.searchParams.get("namespace"),"ns a")
  assert.equal(request.searchParams.get("uid"),"request-uid")
  const run=new URL(dashboardDestination("/measurements/profiling-runs/ns/run-a",new URLSearchParams("uid=run-uid&requestUID=parent"))!,"https://dashboard.invalid")
  assert.equal(run.pathname,"/measurements/requests")
  assert.equal(run.searchParams.get("run"),"run-a")
  assert.equal(run.searchParams.get("uid"),"run-uid")
  assert.equal(run.searchParams.get("requestUID"),"parent")
})

test("legacy pool details retain service scope and selected node or reservation",()=>{
  const node=new URL(dashboardDestination("/pools/service%2Fuid",new URLSearchParams("open=node%3Anode%2Fuid&fixture=expand&filter=one&filter=two"))!,"https://dashboard.invalid")
  assert.equal(node.pathname,"/operations/devices")
  assert.equal(node.searchParams.get("node"),"node/uid")
  assert.equal(node.searchParams.get("workload"),"service/uid")
  assert.equal(node.searchParams.has("open"),false)
  assert.deepEqual(node.searchParams.getAll("filter"),["one","two"])
  assert.equal(node.searchParams.get("fixture"),"expand")
  const reservations=new URL(dashboardDestination("/pools/service%2Fuid",new URLSearchParams("open=reservations&at=original"))!,"https://dashboard.invalid")
  assert.equal(reservations.pathname,"/")
  assert.equal(reservations.searchParams.get("open"),"reservations")
  assert.equal(reservations.searchParams.get("workload"),"service/uid")
  assert.equal(reservations.searchParams.get("at"),"original")
})

test("experiment legacy addresses reach their final destination without redirect loops",()=>{
  const cases:[string,string,string|null][]=[
    ["/experiments","",null],
    ["/experiments","view=published&offset=5",null],
    ["/experiments","name=run%2Fone&uid=old&service=w&filter=a&filter=b","/experiments/runs/run%2Fone?uid=old&service=w&filter=a&filter=b"],
    ["/experiments","name=run","/experiments/runs/run"],
    ["/evaluations","name=run&uid=old","/experiments/runs/run?uid=old"],
    ["/evaluations","","/experiments"],
    ["/experiments/results","","/experiments?view=published&offset=0"],
    ["/experiments/results","offset=5&service=w","/experiments?offset=5&service=w&view=published"],
    ["/experiments/results","a=p&al=l&as=s&b=r&bs=t&service=w",null],
    ["/experiments","a=p&al=l&as=s&b=r&bs=t&service=w","/experiments/results?a=p&al=l&as=s&b=r&bs=t&service=w"],
    ["/experiments/pub%2Fone","offset=5&as=s","/experiments/results?offset=5&as=s&a=pub%2Fone"],
    ["/experiments/runs","",null],
    ["/experiments/runs/run","uid=old",null],
    ["/experiments/new","copy=run",null],
    ["/experiments/new","publication=pub",null],
    ["/experiments/run/edit","uid=old","/experiments/runs/run/edit?uid=old"],
    ["/experiments/run%2Fone/edit","uid=old&service=w","/experiments/runs/run%2Fone/edit?uid=old&service=w"],
    ["/experiments/runs/run/edit","uid=old",null],
    ["/experiments/runs/runs/edit","uid=old",null],
    // The new execution namespace wins over the old editor for a run named "runs".
    ["/experiments/runs/edit","uid=old",null],
    ["/experiments/results","name=run&service=w","/experiments?name=run&service=w&view=published&offset=0"],
    ["/evaluations/load-scenarios","name=load&uid=old","/experiments/load-scenarios?name=load&uid=old"],
    ["/experiments/load-scenarios","name=load&uid=old",null],
    ["/experiments/load-scenarios/new","copy=load",null],
    ["/experiments/load-scenarios/load/edit","uid=old",null],
  ]
  for(const [path,search,expected] of cases) {
    const destination=dashboardDestination(path,new URLSearchParams(search))
    assert.equal(destination,expected,`${path}?${search}`)
    if(destination) {
      const final=new URL(destination,"https://dashboard.invalid")
      assert.equal(dashboardDestination(final.pathname,final.searchParams),null,`redirect loop: ${destination}`)
    }
  }
  for(const query of ["as=stage&service=w","a=&as=stage&offset=5"]) {
    const destination=dashboardDestination("/experiments",new URLSearchParams(query))!
    const final=new URL(destination,"https://dashboard.invalid")
    assert.equal(final.pathname,"/experiments")
    assert.equal(final.searchParams.get("view"),"published")
    assert.equal(dashboardDestination(final.pathname,final.searchParams),null)
  }
})

import { commitExperimentSelection, experimentSelection } from "./dashboard-routes.ts"

test("IA: closing a shared interval keeps its identity independently from the Sheet",()=>{
  const params=experimentSelection("?a=run&al=historical&as=full-steady-mixed&b=run&bs=static-baseline-steady-mixed",{sheet:"closed"})
  assert.equal(params.get("as"),"full-steady-mixed")
  assert.equal(params.get("bs"),"static-baseline-steady-mixed")
  assert.equal(params.get("sheet"),"closed")
})

test("IA: opening a shared interval pushes once, service changes replace and closing preserves list context",()=>{
  const location=new URL("https://dashboard.invalid/experiments/results?a=publication&offset=5&service=camera&returnTo=%2Fexperiments%3Fview%3Dpublished")
  const calls:string[]=[],history={state:{__NA:true},replaceState(data:unknown,_unused:string,url?:string|URL|null){assert.equal(data,null,"Next must observe external history updates");calls.push("replace");location.href=new URL(String(url),location).href},pushState(data:unknown,_unused:string,url?:string|URL|null){assert.equal(data,null,"Next must observe external history updates");calls.push("push");location.href=new URL(String(url),location).href},back(){calls.push("back")}}
  commitExperimentSelection(location,history,{al:"publication",as:"stage"},"push")
  commitExperimentSelection(location,history,{service:"other"})
  assert.deepEqual(calls,["push","replace"])
  assert.equal(location.searchParams.get("offset"),"5")
  assert.equal(location.searchParams.get("returnTo"),"/experiments?view=published")
  commitExperimentSelection(location,history,{sheet:"closed"})
  assert.deepEqual(calls,["push","replace","replace"])
  const direct={replaceState(_data:unknown,_unused:string,url?:string|URL|null){location.href=new URL(String(url),location).href}}
  commitExperimentSelection(location,direct,{sheet:"closed"})
  assert.equal(location.searchParams.get("as"),"stage")
  assert.equal(location.searchParams.get("service"),"other")
  assert.equal(location.searchParams.get("offset"),"5")
})

test("R3: rapid A/B selections preserve the preceding URL selection and clear only dependent stages",()=>{
  const location=new URL("https://dashboard.invalid/experiments?a=old&al=historical&as=old-stage&b=old-b&bs=keep&at=stale")
  const history={replaceState(_data:unknown,_unused:string,url?:string|URL|null){location.href=new URL(String(url),location).href}}
  commitExperimentSelection(location,history,{a:"new"})
  commitExperimentSelection(location,history,{b:"new-b"})
  const selected=location.searchParams
  assert.equal(location.pathname,"/experiments/results")
  assert.equal(selected.get("a"),"new")
  assert.equal(selected.get("b"),"new-b")
  assert.equal(selected.has("as"),false)
  assert.equal(selected.has("al"),false)
  assert.equal(selected.has("bs"),false)
  assert.equal(selected.has("at"),false)
  assert.equal(experimentSelection("a=one&as=chosen&b=two&bs=keep",{as:"next"}).get("bs"),"keep")
})


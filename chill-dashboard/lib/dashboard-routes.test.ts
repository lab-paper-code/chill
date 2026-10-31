import assert from "node:assert/strict"
import test from "node:test"
import { dashboardDestination, dashboardRedirects } from "./dashboard-routes.ts"

test("legacy routes preserve duplicate filters, exact windows and selections at canonical destinations",()=>{
  const original=new URLSearchParams("from=2026-10-31T01%3A02%3A03.123456789Z&to=2026-10-31T02%3A02%3A03Z&uid=u%2Fid&filter=failed&filter=late&namespace=ns&a=publication&as=stage&b=baseline&bs=baseline-stage")
  for(const route of dashboardRedirects) {
    const result=new URL(dashboardDestination(route.source,original)!,"https://dashboard.invalid")
    const expected=new URL(route.destination,"https://dashboard.invalid")
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

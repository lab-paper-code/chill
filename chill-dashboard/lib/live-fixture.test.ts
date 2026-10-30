import test from "node:test"
import assert from "node:assert/strict"
import { fixtureMessages, liveFixtureScenes } from "./live-fixture.ts"
import { initialLiveState, parseLiveMessage, applyLiveMessage } from "./live.ts"
import { serverConfig } from "./server-config.ts"

const at="2026-10-30T12:00:00.000Z"
function scene(name:string) {
  let state=initialLiveState()
  for(const frame of fixtureMessages(name,at)) {
    const message=parseLiveMessage(frame)
    assert.ok(message,`${name} uses the real live boundary`)
    state=applyLiveMessage(state,message,Date.parse(at))
  }
  return state
}

test("S2 synthetic scenes preserve empty / first / expansion / residual execution facts",()=>{
  assert.deepEqual(liveFixtureScenes,["empty","first","expand","shrink"])
  assert.equal(scene("empty").snapshot?.workloads.length,0)
  const first=scene("first"),expanded=scene("expand"),shrink=scene("shrink")
  assert.equal(first.snapshot?.instances.length,1)
  const shared=first.snapshot!.instances[0]
  assert.equal(shared.routable["pool-alpha"],true)
  assert.equal(shared.routable["pool-beta"],true)
  assert.equal(first.metrics.at(-1)?.workloads["pool-alpha"].received,0)
  assert.equal(first.metrics.at(-1)?.workloads["pool-alpha"].completed,0)
  assert.equal(expanded.snapshot?.instances.length,2)
  assert.equal(expanded.snapshot?.workloads[0].pending,3)
  assert.equal(expanded.snapshot?.instances[1].waiting["pool-alpha"],0)
  assert.equal(expanded.snapshot?.reservations[0].reason,"미확인")
  assert.equal(shrink.snapshot?.instances.length,1,"shrink visibly removes the added instance")
  assert.equal(shrink.snapshot?.instances[0].routable["pool-alpha"],false)
  assert.equal(shrink.snapshot?.instances[0].running["pool-alpha"],1)
  assert.equal(shrink.snapshot?.actions[0].status,"complete")
  assert.equal(shrink.snapshot?.reservations.length,1,"completed action does not release reservation")
  assert.equal(shrink.events.at(-1)?.kind,"cluster")
  assert.equal(shrink.snapshot?.decision?.reason,"검사용 축소: 잔여 실행을 기다려요")
})

test("S2 numerator denominator and absence use distinct source facts",()=>{
  const expanded=scene("expand"),metric=expanded.metrics.at(-1)!.workloads["pool-alpha"]
  assert.equal(metric.completed/(metric.completed+metric.failed),0.95)
  assert.equal(metric.withinSLO/(metric.completed+metric.failed),0.9)
  assert.equal(metric.received,60)
  assert.equal(expanded.metrics.at(-1)?.pool.wholeWatts,null)
  assert.equal(expanded.metrics.at(-1)?.nodes["node-b"].watts,null)
  assert.ok(expanded.gaps.some(gap=>gap.reason==="검사용 전력 결손"))
  assert.equal(expanded.metrics.length,2,"unreceived time is not padded with zero buckets")
})

test("synthetic scenes require an explicit server flag, not a URL flag",()=>{
  const env={CHILL_BASE_URL:"https://localhost:3446",CHILL_NAMESPACE:"fixture",CHILL_SYSTEM_NAME:"fixture"}
  assert.equal(serverConfig(env).liveFixture,false)
  assert.equal(serverConfig({...env,CHILL_DEMO_REPLAY:"1"}).liveFixture,true)
  assert.equal(serverConfig({...env,CHILL_DEMO_REPLAY:"true"}).liveFixture,false)
})

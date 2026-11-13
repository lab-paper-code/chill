import assert from "node:assert/strict"
import { test } from "node:test"
import * as display from "./live-display.ts"

test("observed unconfirmed and idle-removal compounds deduplicate labels without hiding unknown codes",()=>{
  const reason="UnconfirmedInstance:pod/containerd://executor:NotReady; ReservedScope:w; IdleInstanceRemoved:idle-a; IdleInstanceRemoved:idle-b"
  assert.equal(display.liveReason(reason),"인스턴스 미확인; 변경 자원 예약 중; 유휴 인스턴스 제거")
  assert.equal(display.incidentDiagnosis(reason).reason,display.liveReason(reason))
  assert.equal(display.liveReason("UnconfirmedInstance:pod/containerd://executor; NewReason:actual-detail; NewReason:actual-detail"),"인스턴스 미확인; NewReason:actual-detail")
})

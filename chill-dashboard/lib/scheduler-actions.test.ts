import assert from "node:assert/strict"
import test from "node:test"
import { actionElapsedNanos, actionErrorLabel, clusterRow, groupActions, phaseStatus, planVsActual, reservationText } from "./scheduler-actions.ts"

const node = { uid: "", name: "" }
const rec = (id: string, phase: string, at: string) => ({ at, action: { id, kind: "prepare", node, phase, startedAt: "2026-01-01T00:00:00Z" } })

test("groupActions keeps the newest record per id", () => {
  const rows = groupActions([rec("a", "complete", "2026-01-01T00:00:30Z"), rec("a", "running", "2026-01-01T00:00:01Z"), rec("b", "running", "2026-01-01T00:00:02Z")])
  assert.deepEqual(rows.map(r => [r.id, r.phase]), [["a", "complete"], ["b", "running"]])
  assert.equal(actionElapsedNanos(rows[0]), 30e9)
  assert.equal(actionElapsedNanos(rows[1]), null)
})

test("phaseStatus covers measurement phases and live codes", () => {
  assert.equal(phaseStatus("Accepted").label, "접수됨")
  assert.equal(phaseStatus("failed").tone, "fail")
})

test("plan uses the recorded expected time; errors, reservations and issues get labels", () => {
  const action = { id: "a", kind: "prepare", node, phase: "complete", startedAt: "2026-01-01T00:00:00Z", completedAt: "2026-01-01T00:01:28Z", expectedSeconds: 90 }
  assert.deepEqual(planVsActual(action), [{ label: "소요", plan: "1분 30초", actual: "1분 28초" }])
  assert.equal(actionErrorLabel("PreparationFailed: readiness probe timed out after 30s"), "준비 실패")
  assert.equal(actionErrorLabel("something odd"), null)
  assert.equal(reservationText({ owner: "measurement-lease/x", reason: "" }), "측정 실행 중")
  assert.equal(reservationText({ owner: "decision/2026-01-01T00:00:00Z", reason: "" }), "판단에 따른 변경")
  const issue = clusterRow({ kind: "issue", at: "", event: "resolved", source: "chill-system/chill-node-agent-x", reason: "", previousReason: "node-agent power sample gap exceeded 5s" })
  assert.deepEqual(issue, { kind: "문제 해소", target: "chill-node-agent-x", content: "전력 표본 공백", detail: "node-agent power sample gap exceeded 5s" })
})

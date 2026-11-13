import assert from "node:assert/strict"
import test from "node:test"
import { actionElapsedNanos, groupActions, phaseStatus } from "./scheduler-actions.ts"

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

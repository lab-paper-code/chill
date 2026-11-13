import test from "node:test"
import assert from "node:assert/strict"
import { breadcrumbs } from "./routes.ts"

test("breadcrumbs hide ids and mark parameters", () => {
  const trail = breadcrumbs("/experiments/results/5f0c2a9e-41b7-4d3e-9a8f-1c2b3d4e5f60")
  assert.deepEqual(trail.map(crumb => crumb.label), ["실험", "결과", "상세"])
  assert.equal(trail[2].dynamic, true)
  assert.deepEqual(breadcrumbs("/services/new").map(crumb => crumb.label), ["서비스", "새로 만들기"])
  assert.equal(breadcrumbs("/services/vqa/edit").at(-1)?.dynamic, undefined)
})

import assert from "node:assert/strict"
import { test } from "node:test"
import type { SchedulerSettings } from "./read-types.ts"
import { changeRows, fieldInput, formOf, parseField, rebase, settingsOf } from "./scheduler-settings-form.ts"

const controls = { placement: true, resources: true, replicas: true, routing: true, batching: true, power: true, membership: true }
const base = {
  observationWindowNanos: 60000000000, controlIntervalNanos: 10000000000, comparisonPeriodNanos: 600000000000, transitionReserveFraction: 0, scaleDownHeadroomFraction: 0.25,
  maxReplicas: 8, maxInstances: 16, servingMemoryBytes: 18446744073709551615n, completionMarginMS: 29, capacityWindowNanos: 60000000000, capacityWindows: 20, capacityPassingWindows: 19,
  capacityMinimumRequests: 1000, capacityGridSteps: 20, capacityBisectionWidth: 0.005, capacityTailSamples: 100, planSearchWorkers: 3,
  defaultOperationCost: { prepare: { seconds: 1, joules: 1 }, remove: { seconds: 1, joules: 1 }, power: { seconds: 1, joules: 1 } }, defaultIdlePower: { nodeWatts: 1, perInstanceWatts: 1 }, defaultDispatchOccupancyNanos: 1,
  autonomousWorkloads: ["a", "b"], minimumDemand: { a: 0.3 }, excludedNodes: ["n1"], controls,
} satisfies SchedulerSettings

test("untouched form keeps exact stored values and absent optional fields", () => {
  const { settings, errors } = settingsOf(base, formOf(base))
  assert.deepEqual(errors, {})
  assert.equal(settings.servingMemoryBytes, 18446744073709551615n)
  assert.equal("planSearchEvaluations" in settings, false)
  assert.equal(changeRows(base, settings, uid => uid).length, 0)
  assert.equal(fieldInput("controlIntervalNanos", base), "10000")
  assert.equal(fieldInput("planSearchEvaluations", base), "500000")
})

test("inputs convert units exactly and reject invalid values", () => {
  assert.equal(parseField("controlIntervalNanos", "0.5"), 500000n)
  assert.equal(parseField("servingMemoryBytes", "128"), 134217728n)
  assert.equal(parseField("servingMemoryBytes", "0.5"), 524288n)
  assert.equal(parseField("planPolishActive", "0"), 0n)
  for (const [key, text] of [["maxReplicas", "0"], ["controlIntervalNanos", "0"], ["capacityBisectionWidth", "0"], ["transitionReserveFraction", "1.5"], ["servingMemoryBytes", "0.0000001"], ["planSearchStrategy", "v9"]] as const)
    assert.throws(() => parseField(key, text))
  const form = formOf(base)
  form.fields.capacityPassingWindows = "21"
  form.demand.b = "x"
  assert.deepEqual(Object.keys(settingsOf(base, form).errors).sort(), ["capacityPassingWindows", "demand:b"])
})

test("rebase applies only my changes onto the latest settings", () => {
  const form = formOf(base)
  form.autonomous = ["a"]
  form.fields.maxReplicas = "10"
  form.demand.a = ""
  const mine = settingsOf(base, form).settings
  assert.deepEqual(changeRows(base, mine, uid => uid).map(row => row.id), ["auto:b", "demand:a", "maxReplicas"])
  const latest = { ...base, autonomousWorkloads: ["a", "b", "c"], maxInstances: 4, controls: { ...controls, power: false } }
  const merged = rebase(latest, base, mine)
  assert.deepEqual(merged.autonomousWorkloads, ["a", "c"])
  assert.equal(merged.maxInstances, 4)
  assert.equal(merged.maxReplicas, 10n)
  assert.equal(merged.controls.power, false)
  assert.deepEqual(merged.minimumDemand, {})
})

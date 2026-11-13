import test from "node:test"
import assert from "node:assert/strict"
import { formatDurationNanos, formatRelative, niceTicks, secondTicks, timeTicks } from "./format.ts"

test("formatDurationNanos: one decimal of seconds under a minute, then minutes and seconds", () => {
  assert.equal(formatDurationNanos(55.77e9), "55.8초")
  assert.equal(formatDurationNanos(31e9), "31.0초")
  assert.equal(formatDurationNanos(59.97e9), "1분")
  assert.equal(formatDurationNanos(88e9), "1분 28초")
})

test("formatRelative says 방금 around now, either side", () => {
  const now = 1_000_000_000
  assert.equal(formatRelative(now + 1500, now), "방금")
  assert.equal(formatRelative(now - 4000, now), "방금")
  assert.equal(formatRelative(now - 12_000, now), "12초 전")
  assert.equal(formatRelative(now + 30_000, now), "곧")
})

test("niceTicks keeps low ranges apart", () => {
  const low = niceTicks(0, 1.2)
  assert.deepEqual(low.ticks, [0, 0.5, 1, 1.5])
  assert.equal(low.digits, 1)
  assert.deepEqual(niceTicks(0, 0).ticks, [0, 0.25, 0.5, 0.75, 1])
  assert.equal(niceTicks(0, 480).digits, 0)
  assert.equal(niceTicks(0, 0.03).digits, 2)
})

test("timeTicks never repeats a label", () => {
  for (const span of [30_000, 15 * 60_000, 3_600_000, 86_400_000, 7 * 86_400_000]) {
    const from = Date.parse("2026-11-13T05:00:07Z"), { ticks, label } = timeTicks(from, from + span)
    const labels = ticks.map(label)
    assert.equal(new Set(labels).size, labels.length, `${span}: ${labels}`)
    assert.ok(ticks.length >= 2)
  }
})

test("secondTicks starts at zero", () => {
  const { ticks, label } = secondTicks(600)
  assert.equal(ticks[0], 0)
  assert.equal(label(90), "1분 30초")
})

import test from "node:test"
import assert from "node:assert/strict"
import { formatDetailTime, formatDurationNanos, formatListTime, formatMs, formatRate, formatRelative, formatSeconds, formatTarget, formatWatts, goDurationNanos, niceTicks, percentNumber, rateNumber, secondTicks, timeTicks, wattsNumber } from "./format.ts"

test("formatDurationNanos: one decimal of seconds under a minute, then minutes and seconds; whole units without decimals", () => {
  assert.equal(formatDurationNanos(55.77e9), "55.8초")
  assert.equal(formatDurationNanos(31e9), "31초")
  assert.equal(formatDurationNanos(10e9), "10초")
  assert.equal(formatDurationNanos(59.97e9), "1분")
  assert.equal(formatDurationNanos(88e9), "1분 28초")
  assert.equal(formatDurationNanos(948e6), "948 ms")
  assert.equal(formatDurationNanos(12.34e6), "12.3 ms")
  assert.equal(formatSeconds(10), "10초")
  assert.equal(formatSeconds(0.5), "500 ms")
  assert.equal(formatSeconds(450), "7분 30초")
})

test("latency targets read in ms with separators, never as a Go duration", () => {
  assert.equal(goDurationNanos("1s"), 1e9)
  assert.equal(goDurationNanos("1m30s"), 90e9)
  assert.equal(goDurationNanos("7.034s"), 7.034e9)
  assert.equal(goDurationNanos("soon"), undefined)
  assert.equal(formatTarget(95, 1e9), "p95 1,000 ms")
  assert.equal(formatTarget(99.5, 7.034e9), "p99.5 7,034 ms")
  assert.equal(formatMs(948), "948 ms")
  assert.equal(formatMs(12.34), "12.3 ms")
  assert.equal(formatMs(1000.4), "1,000 ms")
})

test("power, rate and success rate precision", () => {
  assert.equal(wattsNumber(45.26), "45.3")
  assert.equal(wattsNumber(306.4), "306")
  assert.equal(formatWatts(410), "410 W")
  assert.equal(rateNumber(4.26), "4.3")
  assert.equal(rateNumber(31.8), "32")
  assert.equal(formatRate(1482), "1,482 req/s")
  assert.equal(percentNumber(0.99667), "99.7")
  assert.equal(formatWatts(null), "—")
})

test("list and detail times in KST", () => {
  const at = "2026-11-13T05:03:22Z"
  assert.equal(formatListTime(at), "11-13 14:03:22")
  assert.equal(formatDetailTime(at), "2026-11-13 14:03:22")
  assert.equal(formatListTime(undefined), "—")
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

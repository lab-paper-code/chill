import assert from "node:assert/strict"
import test from "node:test"
import type { RequestRow } from "./read-types.ts"
import { managementResult, requestError, requestMs, requestMarks, requestResult, requestStages } from "./services-records.ts"

const row = (extra: Partial<RequestRow>): RequestRow => ({ kind: "final", requestID: "r", receivedAt: "2026-01-01T00:00:00Z", ...extra })

test("request result", () => {
  assert.equal(requestResult(row({ outcome: "completed" })).label, "완료")
  assert.equal(requestResult(row({ kind: "assigned" })).label, "배정됨")
  assert.equal(requestResult(row({ kind: "" })).label, "처리 중")
  assert.deepEqual(requestResult(row({ outcome: "mystery" })), { label: "—", tone: "idle", code: "mystery" })
})

test("stages and marks keep only what the server sent", () => {
  assert.equal(requestStages(row({})), null)
  assert.deepEqual(requestStages(row({ queueMS: 2, backendMS: 5 }))?.map(stage => stage.label), ["대기", "추론"])
  assert.deepEqual(requestStages(row({ backendMS: 5 }), "답 생성")?.map(stage => stage.label), ["답 생성"])
  assert.deepEqual(requestMarks(row({ acceptedNanos: 1n, exchangeNanos: 9 })).map(mark => mark.label), ["접수", "응답 완료"])
})

test("management result", () => {
  assert.equal(managementResult({ phase: "Completed" }).tone, "ok")
  assert.equal(managementResult({ phase: "Draining", error: "x" }).tone, "fail")
  assert.equal(managementResult({ phase: "Applying" }).tone, "progress")
})

test("request error codes", () => {
  assert.equal(requestError("QueueFull"), "대기열 가득 참")
  assert.equal(requestError("RequestTimeout: deadline"), "응답 시간 초과")
  assert.equal(requestError("Mystery"), null)
  assert.equal(requestError(undefined), null)
})

test("request times are always ms", () => {
  assert.equal(requestMs(363_189), "0.4 ms")
  assert.equal(requestMs(3_000_000_000n), "3,000 ms")
  assert.equal(requestMs(undefined), "—")
})

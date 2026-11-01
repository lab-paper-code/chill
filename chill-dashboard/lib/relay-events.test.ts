import assert from "node:assert/strict"
import test from "node:test"
import { relayPlan } from "./relay.ts"
import { serverConfig } from "./server-config.ts"

test("event recovery allows one exact timestamp on its read-only route", () => {
  const config = serverConfig({ CHILL_BASE_URL: "https://chill.example", CHILL_NAMESPACE: "chill-system", CHILL_SYSTEM_NAME: "chill" })
  const path = ["v1", "observations", "events"]
  const at = "2026-11-01T01:16:44.827689578Z"
  const search = `?${new URLSearchParams({ at })}`
  assert.deepEqual(relayPlan("GET", path, search, config), {
    path: `/api/v1/observations/events${search}`, kind: "query", timeoutMs: config.queryTimeoutMs, bodyAllowed: false,
  })
  for (const query of ["", "?at=bad", "?at=2026-02-30T00:00:00Z", `${search}&at=${at}`, `${search}&limit=1`, `${search}&from=${at}`]) {
    assert.throws(() => relayPlan("GET", path, query, config), /DashboardQueryInvalid/)
  }
  for (const method of ["POST", "PUT", "DELETE", "PATCH", "HEAD"]) {
    assert.throws(() => relayPlan(method, path, search, config), /DashboardMethodNotAllowed/)
  }
  assert.throws(() => relayPlan("GET", [...path, "extra"], search, config), /DashboardRouteNotAllowed/)
})

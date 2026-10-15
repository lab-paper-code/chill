import assert from "node:assert/strict"
import test from "node:test"
import { serverConfig, publicConfig } from "./server-config.ts"
import { boundedBody, relay, relayPlan } from "./relay.ts"

test("fixed-owner relay enforces path, origin, body and credential boundaries", async () => {
  const env = {
    CHILL_BASE_URL: "https://owner.example:8443", CHILL_NAMESPACE: "research", CHILL_SYSTEM_NAME: "chill",
    CHILL_QUERY_TIMEOUT_MS: "10000", CHILL_MUTATION_TIMEOUT_MS: "60000", CHILL_INFER_TIMEOUT_MS: "30000",
    CHILL_MAX_REQUEST_BYTES: "8388608", CHILL_PAGE_SIZE: "50", CHILL_MAX_PAGE_SIZE: "200",
    CHILL_MAX_WINDOW_HOURS: "24", CHILL_POLL_INTERVAL_MS: "5000",
  }
  const config = serverConfig(env)
  for (const value of ["http://owner.example", "https://secret@owner.example", "https://owner.example/api", "https://owner.example/?token=secret", "https://owner.example/#secret", "https://owner.example\\evil", "https://owner.example?", ""]) {
    assert.throws(() => serverConfig({ ...env, CHILL_BASE_URL: value }), /DashboardConfigInvalid/)
  }
  for (const changes of [{ CHILL_QUERY_TIMEOUT_MS: "NaN" }, { CHILL_INFER_TIMEOUT_MS: "0" }, { CHILL_MAX_REQUEST_BYTES: "999999999" }, { CHILL_PAGE_SIZE: "51", CHILL_MAX_PAGE_SIZE: "50" }, { CHILL_POLL_INTERVAL_MS: "1" }, { CHILL_NAMESPACE: "" }, { CHILL_SYSTEM_NAME: "" }]) {
    assert.throws(() => serverConfig({ ...env, ...changes }), /DashboardConfigInvalid/)
  }

  assert.equal(relayPlan("POST", "v1/namespaces/research/batch-energy-optimizations/preflight".split("/"), "", config).kind, "query")
  assert.equal(relayPlan("PUT", ["v1", "models", "preflight"], "", config).kind, "mutation")
  for (const [method, path, query] of [
    ["GET", "v1/https://evil.example", ""], ["GET", "v1/models/..", ""], ["GET", "v1/models/a%2fb", ""],
    ["PATCH", "v1/models/demo", ""], ["POST", "v1/systems", ""], ["GET", "v1/models", "?url=https://evil.example"],
    ["GET", "v1/systems", "?limit=201"], ["GET", "v1/inventory", "?offset=1000001"],
    ["GET", "v1/systems", "?limit=1&limit=2"], ["GET", "v1/systems", "?q=%zz"],
    ["GET", "v1/systems", `?q=${encodeURIComponent("한".repeat(86))}`],
    ["GET", "v1/namespaces/research/profiling-runs", "?phase=Completed"],
    ["GET", "v1/namespaces/research/profiling-runs", "?condition=EvidenceVerified=maybe"],
    ["GET", "v1/observations/workloads/uid", "?from=2026-01-01T00:00:00Z&to=2026-01-03T00:00:00Z"],
    ["GET", "v1/observations/workloads/uid", "?from=2026-02-30T00:00:00Z"],
    ["GET", "v1/observations/workloads/uid", "?from=2026-01-01T00:00:00.000000002Z&to=2026-01-01T00:00:00.000000001Z"],
  ]) assert.throws(() => relayPlan(method, path.split("/"), query, config), /Dashboard/)
  assert.throws(() => relayPlan("GET", ["v1", "observations", "workloads", "uid"], "", config), /DashboardQueryInvalid/)

  const bytes = Buffer.from([0, 255, 13, 10, 123, 125])

  const signal = new AbortController().signal
  await assert.rejects(boundedBody(new Response(bytes).body, bytes.length - 1, signal), /DashboardRequestTooLarge/)
  const controller = new AbortController()
  const pending = boundedBody(new ReadableStream<Uint8Array>(), 10, controller.signal)
  controller.abort()
  await assert.rejects(pending, /DashboardRelayCanceled/)

  const previous = Object.fromEntries(Object.keys(env).map(key => [key, process.env[key]]))
  Object.assign(process.env, env)
  try {
    const safe = publicConfig()
    assert.equal("owner" in safe, false)
    assert.equal((await relay(new Request("https://dashboard.example/api/chill/v1/models"), ["v1", "models"])).status, 401)
    const foreign = new Request("https://dashboard.example/api/chill/v1/models", { method: "POST", headers: { authorization: "Bearer secret", origin: "https://evil.example" } })
    assert.equal((await relay(foreign, ["v1", "models"])).status, 403)
    const proxied = new Request("http://0.0.0.0:3000/api/chill/v1/models", {
      method: "POST",
      headers: { authorization: "Bearer secret", host: "dashboard.example", origin: "https://dashboard.example", "content-length": String(config.maxRequestBytes + 1) },
    })
    assert.equal((await relay(proxied, ["v1", "models"])).status, 413)
  } finally {
    for (const [key, value] of Object.entries(previous)) {
      if (value === undefined) delete process.env[key]
      else process.env[key] = value
    }
  }
})

test("scheduler consumer routes enforce exact methods and bounded observation queries", () => {
  const config = serverConfig({
    CHILL_BASE_URL: "https://owner.example", CHILL_NAMESPACE: "research", CHILL_SYSTEM_NAME: "chill",
    CHILL_PAGE_SIZE: "7", CHILL_MAX_PAGE_SIZE: "20", CHILL_MAX_WINDOW_HOURS: "1",
  })
  const plan = (method: string, path: string, search = "") => relayPlan(method, path.split("/"), search, config)
  const from = "2026-01-01T00:00:00.000000001Z"
  const to = "2026-01-01T01:00:00.000000001Z"
  const window = new URLSearchParams({ from, to })
  for (const path of ["v1/scheduler", "v1/scheduler/state", "v1/scheduler/settings"]) {
    assert.deepEqual(plan("GET", path), { path: `/api/${path}`, kind: "query", timeoutMs: config.queryTimeoutMs, bodyAllowed: false })
    assert.throws(() => plan("GET", path, "?limit=1"), /DashboardQueryInvalid/)
    for (const method of ["POST", "DELETE", "PATCH", "HEAD", "OPTIONS", ...(path.endsWith("/settings") ? [] : ["PUT"])]) {
      assert.throws(() => plan(method, path), /DashboardMethodNotAllowed/)
    }
  }
  assert.deepEqual(plan("PUT", "v1/scheduler/settings"), {
    path: "/api/v1/scheduler/settings", kind: "mutation", timeoutMs: config.mutationTimeoutMs, bodyAllowed: true,
  })
  assert.throws(() => plan("PUT", "v1/scheduler/settings", "?from=now"), /DashboardQueryInvalid/)
  assert.deepEqual(plan("GET", "v1/scheduler/profiles"), { path: "/api/v1/scheduler/profiles", kind: "query", timeoutMs: config.mutationTimeoutMs, bodyAllowed: false })
  assert.deepEqual(plan("POST", "v1/scheduler/profiles"), { path: "/api/v1/scheduler/profiles", kind: "mutation", timeoutMs: config.mutationTimeoutMs, bodyAllowed: true })
  for (const method of ["PUT", "DELETE", "PATCH"]) assert.throws(() => plan(method, "v1/scheduler/profiles"), /DashboardMethodNotAllowed/)
  assert.throws(() => plan("GET", "v1/scheduler/profiles", "?limit=1"), /DashboardQueryInvalid/)
  for (const path of ["v1/scheduler/other", "v1/scheduler/settings/extra", "v1/scheduler/profiles/extra", "v1/observations/load/extra", "v1/observations/power/extra", "v1/observations/scheduler/extra"]) {
    assert.throws(() => plan("GET", path), /DashboardRouteNotAllowed/)
  }

  assert.equal(plan("GET", "v1/observations/load", `?${window}`).path, `/api/v1/observations/load?${window}`)
  assert.deepEqual(plan("GET", "v1/observations/power", `?${window}`), {
    path: `/api/v1/observations/power?${window}`, kind: "query", timeoutMs: config.queryTimeoutMs, bodyAllowed: false,
  })
  assert.equal(plan("GET", "v1/observations/scheduler", `?${window}`).path, `/api/v1/observations/scheduler?${window}&limit=7`)
  assert.equal(plan("GET", "v1/observations/scheduler", `?${window}&limit=20&cursor=${Number.MAX_SAFE_INTEGER}`).path,
    `/api/v1/observations/scheduler?${window}&limit=20&cursor=${Number.MAX_SAFE_INTEGER}`)
  for (const path of ["v1/observations/load", "v1/observations/power", "v1/observations/scheduler"]) {
    for (const method of ["POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"]) {
      assert.throws(() => plan(method, path, `?${window}`), /DashboardMethodNotAllowed/)
    }
    for (const search of [
      "", `?from=${from}`, `?to=${to}`, `?${window}&from=${from}`, `?${window}&to=${to}`,
      `?${window}&unknown=1`, `?${window}&definition=a&definition=b`, `?${window}&outcome=a`,
      "?from=2026-02-30T00:00:00Z&to=2026-03-01T00:00:00Z",
      "?from=not-a-time&to=2026-01-01T01:00:00Z",
      `?from=${from}&to=${from}`, `?from=${to}&to=${from}`,
      `?from=${from}&to=2026-01-01T01:00:00.000000002Z`,
    ]) assert.throws(() => plan("GET", path, search), /DashboardQueryInvalid/)
  }
  for (const path of ["v1/observations/load", "v1/observations/power"]) {
    for (const search of ["limit=1", "cursor=0", "offset=0"]) {
      assert.throws(() => plan("GET", path, `?${window}&${search}`), /DashboardQueryInvalid/)
    }
  }
  for (const search of [
    "limit=0", "limit=21", "limit=1&limit=2", "cursor=-1", "cursor=1.5",
    "cursor=9007199254740992", "cursor=0&cursor=1", "offset=0",
  ]) assert.throws(() => plan("GET", "v1/observations/scheduler", `?${window}&${search}`), /DashboardQueryInvalid/)

  const workload = new URLSearchParams(plan("GET", "v1/observations/workloads/uid",
    `?${window}&definition=one&definition=two&outcome=complete&outcome=failed`).path.split("?")[1])
  assert.deepEqual(workload.getAll("definition"), ["one", "two"])
  assert.deepEqual(workload.getAll("outcome"), ["complete", "failed"])
  assert.equal(workload.get("limit"), "7")
  assert.equal(workload.get("from"), from)
  assert.equal(workload.get("to"), to)
})

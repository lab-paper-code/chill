import assert from "node:assert/strict"
import test from "node:test"
import { serverConfig, publicConfig } from "./server-config.ts"
import { boundedBody, relay, relayPlan } from "./relay.ts"

test("W8 aggregate occupancy relay permits only GET with an exact bounded time range",()=>{
  const config=serverConfig({CHILL_BASE_URL:"https://chill.example",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"chill"})
  const route=["v1","observations","occupancy"],range="?from=2026-11-01T00:00:00Z&to=2026-11-01T00:01:00Z"
  const plan=relayPlan("GET",route,range,config)
  assert.equal(plan.kind,"query")
  assert.equal(new URL(plan.path,"https://local").searchParams.size,2)
  assert.equal(plan.bodyAllowed,false)
  assert.throws(()=>relayPlan("POST",route,range,config),/DashboardMethodNotAllowed/)
  for(const query of ["","?from=bad&to=bad","?from=2026-11-01T00:00:00Z&to=2026-11-01T00:00:00Z",range+"&limit=1",range+"&node=node",range+"&from=2026-11-01T00:00:00Z"])assert.throws(()=>relayPlan("GET",route,query,config),/DashboardQueryInvalid/)
})

test("power buckets alone accept bounded numeric Go durations and aligned bucket counts",()=>{
  const config=serverConfig({CHILL_BASE_URL:"https://chill.example",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"chill"})
  const range="from=2026-11-01T00:00:00.500Z&to=2026-11-01T00:15:00.500Z"
  for(const bucket of ["5s","1m","0.5m","1h"])assert.equal(new URL(relayPlan("GET",["v1","observations","power"],`?${range}&bucket=${bucket}`,config).path,"https://local").searchParams.get("bucket"),bucket)
  for(const bucket of ["","0s","1ms","2h","NaNs","1s","5s&bucket=5s"])assert.throws(()=>relayPlan("GET",["v1","observations","power"],`?${range}&bucket=${bucket}`,config),/DashboardQueryInvalid/)
  assert.throws(()=>relayPlan("GET",["v1","observations","load"],`?${range}&bucket=5s`,config),/DashboardQueryInvalid/)
  assert.throws(()=>relayPlan("GET",["v1","observations","power"],"?from=2026-11-01T00:00:00Z&to=2026-11-01T01:00:01Z&bucket=5s",config),/DashboardQueryInvalid/)
  assert.doesNotThrow(()=>relayPlan("GET",["v1","observations","power"],`?${range}`,config))
})

test("event history accepts bounded opaque cursors without mixing exact-time reads",()=>{
  const config=serverConfig({CHILL_BASE_URL:"https://chill.example",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"chill"})
  const route=["v1","observations","events"],range="from=2026-11-01T00:00:00Z&to=2026-11-02T00:00:00Z"
  const cursor="opaque+/==:checkpoint"+"x".repeat(700)
  assert.equal(new URL(relayPlan("GET",route,`?${range}&limit=500&cursor=${encodeURIComponent(cursor)}`,config).path,"https://local").searchParams.get("cursor"),cursor)
  assert.doesNotThrow(()=>relayPlan("GET",route,"?at=2026-11-01T00:00:00Z",config))
  for(const suffix of ["&limit=501","&limit=0","&at=2026-11-01T00:00:00Z","&cursor="+ "x".repeat(4097)])assert.throws(()=>relayPlan("GET",route,`?${range}${suffix}`,config),/DashboardQueryInvalid/)
  for(const query of ["?at=2026-11-01T00:00:00Z&limit=1","?from=2026-11-01T00:00:00Z","?from=2026-11-01T00:00:00Z&to=2026-11-02T00:00:01Z"])assert.throws(()=>relayPlan("GET",route,query,config),/DashboardQueryInvalid/)
})
test("fixed-chill-server relay enforces path, origin, body and credential boundaries", async () => {
  const env = {
    CHILL_BASE_URL: "https://chill.example:8443", CHILL_NAMESPACE: "research", CHILL_SYSTEM_NAME: "chill",
    CHILL_QUERY_TIMEOUT_MS: "10000", CHILL_MUTATION_TIMEOUT_MS: "60000", CHILL_INFER_TIMEOUT_MS: "30000",
    CHILL_MAX_REQUEST_BYTES: "8388608", CHILL_PAGE_SIZE: "50", CHILL_MAX_PAGE_SIZE: "200",
    CHILL_MAX_WINDOW_HOURS: "24", CHILL_POLL_INTERVAL_MS: "5000",
  }
  const config = serverConfig(env)
  for (const value of ["http://chill.example", "https://secret@chill.example", "https://chill.example/api", "https://chill.example/?token=secret", "https://chill.example/#secret", "https://chill.example\\evil", "https://chill.example?", ""]) {
    assert.throws(() => serverConfig({ ...env, CHILL_BASE_URL: value }), /DashboardConfigInvalid/)
  }
  for (const changes of [{ CHILL_QUERY_TIMEOUT_MS: "NaN" }, { CHILL_INFER_TIMEOUT_MS: "0" }, { CHILL_MAX_REQUEST_BYTES: "999999999" }, { CHILL_PAGE_SIZE: "51", CHILL_MAX_PAGE_SIZE: "50" }, { CHILL_POLL_INTERVAL_MS: "1" }, { CHILL_NAMESPACE: "" }, { CHILL_SYSTEM_NAME: "" }]) {
    assert.throws(() => serverConfig({ ...env, ...changes }), /DashboardConfigInvalid/)
  }

  assert.equal(relayPlan("POST", "v1/namespaces/research/batch-energy-optimizations/preflight".split("/"), "", config).kind, "query")
  const coverage="v1/namespaces/research/measurement-coverage".split("/")
  assert.equal(relayPlan("GET",coverage,"?batches=1,2,4&node=node-a",config).kind,"query")
  for(const query of ["?batches=2,1","?batches=1,1","?batches=0","?batches=","?node=","?node=a&node=b","?model=x"]) assert.throws(()=>relayPlan("GET",coverage,query,config),/DashboardQueryInvalid/)
  assert.throws(()=>relayPlan("POST",coverage,"",config),/DashboardMethodNotAllowed/)
  for(const route of [coverage,["v1","workloads"],["v1","namespaces","research","batch-energy-optimizations"],["v1","scheduler","profiles"]]) {
    assert.equal(new URL(`https://local${relayPlan("GET",route,"?view=summary",config).path}`).searchParams.get("view"),"summary")
    for(const query of ["?view=","?view=compact","?view=full","?view=summary&view=summary"])assert.throws(()=>relayPlan("GET",route,query,config),/DashboardQueryInvalid/)
  }
  for(const [method,route] of [["POST",["v1","workloads"]],["GET",["v1","workloads","one"]],["POST",["v1","namespaces","research","batch-energy-optimizations"]]] as const)assert.throws(()=>relayPlan(method,[...route],"?view=summary",config),/DashboardQueryInvalid/)
  assert.equal(relayPlan("POST","v1/namespaces/research/batch-energy-optimizations/preflight".split("/"),"?variantId=exact-condition",config).kind,"query")
  const runs="v1/namespaces/research/profiling-runs".split("/")
  const runFilters=new URLSearchParams({model:"m",deviceClass:"d",node:"n",path:"cpu",powerMode:"fixed",status:"problem",group:JSON.stringify({namespace:"research",measurement:{dataset:"images"}}),parentUID:"parent"})
  assert.equal(new URL(`https://local${relayPlan("GET",runs,`?${runFilters}`,config).path}`).searchParams.get("status"),"problem")
  const profiles="v1/scheduler/profiles".split("/")
  assert.match(relayPlan("GET",profiles,"?model=m&deviceClass=d&profile=exact&offset=25&limit=25",config).path,/profile=exact/)
  assert.throws(()=>relayPlan("GET",profiles,"?limit=51",config),/DashboardQueryInvalid/)
  for (const [route, keys] of [[runs, ["name","created","model","powerMode","status"]], [profiles, ["model","deviceClass","node","powerMode","path","created","status"]]] as const) {
    for (const sort of keys) {
      for (const order of ["asc", "desc"]) assert.match(relayPlan("GET",route,`?sort=${sort}&order=${order}`,config).path,new RegExp(`sort=${sort}&order=${order}`))
    }
    for (const query of ["?cpu=2","?sort=cpu","?sort=unknown","?sort=","?sort=model&sort=model","?sort=model&order=DESC","?sort=model&order=","?sort=model&order=asc&order=desc","?order=asc"]) {
      assert.throws(()=>relayPlan("GET",route,query,config),/DashboardQueryInvalid/)
    }
  }
  assert.match(relayPlan("GET",profiles,"?q=model&node=n&powerMode=fixed&path=cpu&status=usable",config).path,/status=usable/)
  assert.equal(relayPlan("PUT", ["v1", "models", "preflight"], "", config).kind, "mutation")
  for (const kind of ["preprocessors", "datasets"]) {
    assert.equal(relayPlan("POST", ["v1", kind], "", config).kind, "mutation")
    assert.equal(relayPlan("DELETE", ["v1", kind, "demo"], "", config).kind, "mutation")
    assert.throws(() => relayPlan("PATCH", ["v1", kind, "demo"], "", config), /Dashboard/)
  }
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
    assert.equal("chillServer" in safe, false)
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

test("approved long inference lifetime keeps delivery without enlarging metadata budgets", () => {
  const config = serverConfig({
    CHILL_BASE_URL: "https://chill.example", CHILL_NAMESPACE: "research", CHILL_SYSTEM_NAME: "chill",
    CHILL_INFER_TIMEOUT_MS: "129000",
  })
  assert.equal(config.inferTimeoutMs, 129000)
  assert.equal(relayPlan("POST", ["v1", "workloads", "smol", "infer"], "", config).timeoutMs, 139000)
  assert.equal(relayPlan("GET", ["v1", "models"], "", config).timeoutMs, 10000)
  assert.equal(relayPlan("PUT", ["v1", "models", "smol"], "", config).timeoutMs, 60000)
})

test("evaluation relay permits only contracted operations with conditional stop", () => {
  const config = serverConfig({
    CHILL_BASE_URL: "https://chill.example", CHILL_NAMESPACE: "research", CHILL_SYSTEM_NAME: "chill",
  })
  const plan = (method: string, path: string, search = "") => relayPlan(method, path.split("/"), search, config)
  for (const path of ["v1/evaluations/active", "v1/evaluations/inputs", "v1/evaluations/template"]) {
    assert.deepEqual(plan("GET", path), { path: `/api/${path}`, kind: "query", timeoutMs: config.queryTimeoutMs, bodyAllowed: false })
    for (const method of ["POST", "PUT", "DELETE", "PATCH"]) assert.throws(() => plan(method, path, "?uid=run-uid"), /DashboardMethodNotAllowed/)
    if (path !== "v1/evaluations/inputs") assert.throws(() => plan("GET", path, "?limit=1"), /DashboardQueryInvalid/)
  }
  for (const path of ["v1/evaluations/preflight", "v1/evaluations/load-scenarios/preflight"]) {
    assert.deepEqual(plan("POST", path), { path: `/api/${path}`, kind: "query", timeoutMs: config.queryTimeoutMs, bodyAllowed: true })
    for (const method of ["GET", "PUT", "DELETE", "PATCH"]) assert.throws(() => plan(method, path), /DashboardMethodNotAllowed/)
    assert.throws(() => plan("POST", path, "?variantId=other"), /DashboardQueryInvalid/)
  }
  for (const path of ["v1/evaluations", "v1/evaluations/load-scenarios", "v1/evaluations/run-a/release"]) {
    assert.deepEqual(plan("POST", path), { path: `/api/${path}`, kind: "mutation", timeoutMs: config.mutationTimeoutMs, bodyAllowed: true })
    if (!path.endsWith("/release")) assert.deepEqual(plan("GET", path), { path: `/api/${path}`, kind: "query", timeoutMs: config.queryTimeoutMs, bodyAllowed: false })
    else assert.throws(() => plan("GET", path), /DashboardMethodNotAllowed/)
    for (const method of ["PUT", "DELETE", "PATCH"]) assert.throws(() => plan(method, path), /DashboardMethodNotAllowed/)
    assert.throws(() => plan("POST", path, "?dryRun=All"), /DashboardQueryInvalid/)
  }
  for (const path of ["v1/evaluations/run-a", "v1/evaluations/load-scenarios/load-a"]) {
    assert.deepEqual(plan("GET", path), { path: `/api/${path}`, kind: "query", timeoutMs: config.queryTimeoutMs, bodyAllowed: false })
    for (const method of ["DELETE", "PUT"]) {
      assert.deepEqual(plan(method, path, "?uid=run-uid"), { path: `/api/${path}?uid=run-uid`, kind: "mutation", timeoutMs: config.mutationTimeoutMs, bodyAllowed: method === "PUT" })
      for (const query of ["", "?uid=", "?uid=one&uid=two", "?uid=run-uid&force=true"]) assert.throws(() => plan(method, path, query), /DashboardQueryInvalid/)
    }
    for (const method of ["POST", "PATCH"]) assert.throws(() => plan(method, path), /DashboardMethodNotAllowed/)
    assert.throws(() => plan("GET", path, "?uid=run-uid"), /DashboardQueryInvalid/)
  }
  for (const path of ["v1/evaluations/run-a/status", "v1/evaluations/run-a/release/extra"]) assert.throws(() => plan("POST", path), /DashboardRouteNotAllowed/)
})

test("evaluation inputs accepts single server filters and bounded explicit pages",()=>{
  for(const maxPageSize of [200,20]) {
  const config=serverConfig({CHILL_BASE_URL:"https://chill.example",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"chill",CHILL_PAGE_SIZE:"20",CHILL_MAX_PAGE_SIZE:String(maxPageSize)})
  const plan=(search:string)=>relayPlan("GET",["v1","evaluations","inputs"],search,config).path
  assert.equal(plan(""),"/api/v1/evaluations/inputs")
  for(const search of ["?kind=seed&set=evaluation-enable-20261101&offset=0&limit=25","?kind=payload","?set=한글","?offset=2","?limit=2","?limit=25","?limit=50","?kind=future-kind",`?set=${"a".repeat(512)}`,`?set=${"한".repeat(170)}`]) assert.equal(plan(search),`/api/v1/evaluations/inputs?${new URLSearchParams(search)}`)
  for(const search of ["?kind=seed&kind=payload","?set=a&set=b","?offset=0&offset=1","?limit=2&limit=3","?limit=0","?limit=51","?offset=-1","?offset=1000001","?offset=1.5","?kind=","?set=","?kind=%00","?unknown=a",`?set=${"a".repeat(513)}`,`?set=${"한".repeat(171)}`,`?kind=${"a".repeat(257)}`]) assert.throws(()=>plan(search),/DashboardQueryInvalid/)
  }
})

test("scheduler consumer routes enforce exact methods and bounded observation queries", () => {
  const config = serverConfig({
    CHILL_BASE_URL: "https://chill.example", CHILL_NAMESPACE: "research", CHILL_SYSTEM_NAME: "chill",
    CHILL_PAGE_SIZE: "7", CHILL_MAX_PAGE_SIZE: "20", CHILL_MAX_WINDOW_HOURS: "1",
  })
  const plan = (method: string, path: string, search = "") => relayPlan(method, path.split("/"), search, config)
  const from = "2026-01-01T00:00:00.000000001Z"
  const to = "2026-01-01T01:00:00.000000001Z"
  const window = new URLSearchParams({ from, to })
  for (const path of ["v1/scheduler", "v1/scheduler/state", "v1/scheduler/settings"]) {
    const compact = path.endsWith("/settings") ? "" : "?view=compact"
    assert.deepEqual(plan("GET", path), { path: `/api/${path}${compact}`, kind: "query", timeoutMs: config.queryTimeoutMs, bodyAllowed: false })
    if (compact) assert.equal(plan("GET", path, "?view=compact").path, `/api/${path}${compact}`)
    assert.throws(() => plan("GET", path, compact ? "?view=full" : "?view=compact"), /DashboardQueryInvalid/)
    assert.throws(() => plan("GET", path, "?limit=1"), /DashboardQueryInvalid/)
    for (const method of ["POST", "DELETE", "PATCH", "HEAD", "OPTIONS", ...(path.endsWith("/settings") ? [] : ["PUT"])]) {
      assert.throws(() => plan(method, path), /DashboardMethodNotAllowed/)
    }
  }
  assert.deepEqual(plan("PUT", "v1/scheduler/settings"), {
    path: "/api/v1/scheduler/settings", kind: "mutation", timeoutMs: config.mutationTimeoutMs, bodyAllowed: true,
  })
  assert.throws(() => plan("PUT", "v1/scheduler/settings", "?from=now"), /DashboardQueryInvalid/)
  assert.deepEqual(plan("POST", "v1/scheduler/operations"), {
    path: "/api/v1/scheduler/operations", kind: "mutation", timeoutMs: config.mutationTimeoutMs, bodyAllowed: true,
  })
  for (const method of ["GET", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"]) {
    assert.throws(() => plan(method, "v1/scheduler/operations"), { message: "DashboardMethodNotAllowed", status: 405 })
  }
  assert.throws(() => plan("POST", "v1/scheduler/operations", "?dryRun=All"), /DashboardQueryInvalid/)
  assert.deepEqual(plan("GET", "v1/scheduler/profiles"), { path: "/api/v1/scheduler/profiles?limit=7", kind: "query", timeoutMs: config.queryTimeoutMs, bodyAllowed: false })
  for (const method of ["POST", "PUT", "DELETE", "PATCH"]) assert.throws(() => plan(method, "v1/scheduler/profiles"), /DashboardMethodNotAllowed/)
  assert.equal(plan("GET", "v1/scheduler/profiles", "?limit=1").path,"/api/v1/scheduler/profiles?limit=1")
  const tasks = "v1/namespaces/chill-system/batch-energy-optimizations"
  assert.deepEqual(plan("GET", tasks), { path: `/api/${tasks}`, kind: "query", timeoutMs: config.queryTimeoutMs, bodyAllowed: false })
  assert.deepEqual(plan("POST", tasks), { path: `/api/${tasks}`, kind: "mutation", timeoutMs: config.mutationTimeoutMs, bodyAllowed: true })
  assert.deepEqual(plan("POST", tasks, "?dryRun=All"), { path: `/api/${tasks}?dryRun=All`, kind: "query", timeoutMs: config.queryTimeoutMs, bodyAllowed: true })
  for (const search of ["?dryRun=Server", "?dryRun=All&dryRun=All", "?limit=1"]) assert.throws(() => plan("POST", tasks, search), /DashboardQueryInvalid/)
  assert.throws(() => plan("GET", tasks, "?dryRun=All"), /DashboardQueryInvalid/)
  assert.deepEqual(plan("DELETE", `${tasks}/old-run`, "?uid=u-1"), { path: `/api/${tasks}/old-run?uid=u-1`, kind: "mutation", timeoutMs: config.mutationTimeoutMs, bodyAllowed: false })
  for (const search of ["", "?uid=", "?uid=a&uid=b", "?uid=u-1&force=true"]) assert.throws(() => plan("DELETE", `${tasks}/old-run`, search), /DashboardQueryInvalid/)
  assert.throws(() => plan("GET", `${tasks}/old-run`), /DashboardMethodNotAllowed/)
  assert.throws(() => plan("DELETE", `${tasks}/preflight`, "?uid=u-1"), /DashboardMethodNotAllowed/)
  assert.throws(() => plan("DELETE", "v1/scheduler/profiles/chill-system/old-run"), /DashboardRouteNotAllowed/)
  for (const path of ["v1/scheduler/other", "v1/scheduler/settings/extra", "v1/scheduler/profiles/extra", "v1/observations/load/extra", "v1/observations/power/extra", "v1/observations/scheduler/extra"]) {
    assert.throws(() => plan("GET", path), /DashboardRouteNotAllowed/)
  }

  assert.equal(plan("GET", "v1/observations/load", `?${window}`).path, `/api/v1/observations/load?${window}`)
  assert.deepEqual(plan("GET", "v1/observations/power", `?${window}`), {
    path: `/api/v1/observations/power?${window}`, kind: "query", timeoutMs: config.queryTimeoutMs, bodyAllowed: false,
  })
  assert.equal(plan("GET", "v1/observations/scheduler", `?${window}`).path, `/api/v1/observations/scheduler?${window}&limit=7&view=compact`)
  assert.equal(plan("GET", "v1/observations/scheduler", `?${window}&limit=20&cursor=${Number.MAX_SAFE_INTEGER}`).path,
    `/api/v1/observations/scheduler?${window}&limit=20&cursor=${Number.MAX_SAFE_INTEGER}&view=compact`)
  assert.equal(plan("GET", "v1/observations/scheduler", `?${window}&limit=1&cursor=21&view=compact`).path,
    `/api/v1/observations/scheduler?${window}&limit=1&cursor=21&view=compact`)
  assert.equal(plan("GET", "v1/observations/scheduler", `?${window}&view=summary&order=desc&filter=decision-action&uid=workload-uid`).path,
    `/api/v1/observations/scheduler?${window}&view=summary&order=desc&filter=decision-action&uid=workload-uid&limit=7`)
  assert.throws(() => plan("GET", "v1/scheduler", "?view=summary"), /DashboardQueryInvalid/)
  assert.throws(() => plan("GET", "v1/observations/scheduler", `?${window}&view=summary&view=compact`), /DashboardQueryInvalid/)
  for (const order of ["asc", "desc"]) {
    const search = `?${window}&order=${order}&filter=decision-action&uid=workload-uid`
    assert.equal(plan("GET", "v1/observations/scheduler", search).path,
      `/api/v1/observations/scheduler?${window}&order=${order}&filter=decision-action&uid=workload-uid&limit=7&view=compact`)
  }
  for (const filter of ["all", "decision", "action", "snapshot", "settings", "power", "other"]) {
    assert.match(plan("GET", "v1/observations/scheduler", `?${window}&filter=${filter}`).path, new RegExp(`filter=${filter}`))
  }
  for (const invalid of ["order=", "order=DESC", "order=desc&order=asc", "filter=", "filter=unknown", "filter=action&filter=decision", "uid=", "uid=a&uid=b", "uid=a%20b", "uid=a%2Fb", "uid=a%5Cb", `uid=${"a".repeat(257)}`]) {
    assert.throws(() => plan("GET", "v1/observations/scheduler", `?${window}&${invalid}`), /DashboardQueryInvalid/)
  }
  for (const path of ["v1/observations/load", "v1/observations/power"]) {
    for (const extra of ["order=desc", "filter=decision-action", "uid=workload-uid"]) {
      assert.throws(() => plan("GET", path, `?${window}&${extra}`), /DashboardQueryInvalid/)
    }
  }
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

test("model compatibility relay permits only the exact GET and four single required selection queries", () => {
  const config = serverConfig({CHILL_BASE_URL:"https://chill.example",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"chill"})
  const path = ["v1","model-compatibility"]
  const selection = {modelSpec:"model-1",executionPath:"onnx-cpu",deviceClass:"class-1",node:"node-1"}
  const search = `?${new URLSearchParams(selection)}`
  assert.deepEqual(relayPlan("GET",path,search,config), {
    path:`/api/v1/model-compatibility${search}`,kind:"query",timeoutMs:config.queryTimeoutMs,bodyAllowed:false,
  })
  for (const method of ["POST","PUT","DELETE","PATCH","HEAD","OPTIONS"]) {
    assert.throws(() => relayPlan(method,path,search,config),{message:"DashboardMethodNotAllowed",status:405})
  }
  for (const route of [["v1","model-compatibility","extra"],["v2","model-compatibility"],["v1","model-compatibilities"]]) {
    assert.throws(() => relayPlan("GET",route,search,config),{message:"DashboardRouteNotAllowed",status:404})
  }
  assert.throws(() => relayPlan("GET",path,"",config),/DashboardQueryInvalid/)
  for (const key of Object.keys(selection)) {
    const missing = new URLSearchParams(selection)
    missing.delete(key)
    assert.throws(() => relayPlan("GET",path,`?${missing}`,config),/DashboardQueryInvalid/)
    for (const value of ["","%00","%zz"]) {
      const invalid = new URLSearchParams(selection)
      invalid.delete(key)
      assert.throws(() => relayPlan("GET",path,`?${invalid}&${key}=${value}`,config),/DashboardQueryInvalid/)
    }
    assert.throws(() => relayPlan("GET",path,`${search}&${key}=${selection[key as keyof typeof selection]}`,config),/DashboardQueryInvalid/)
  }
  for (const extra of ["limit=1","offset=0","q=model","view=compact","namespace=other","powerMode=low","url=https://evil.example"]) {
    assert.throws(() => relayPlan("GET",path,`${search}&${extra}`,config),/DashboardQueryInvalid/)
  }
})

test("compatibility matrix is a selection-free read-only projection",()=>{
  const config=serverConfig({CHILL_BASE_URL:"https://chill.example",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"chill"})
  const path=["v1","model-compatibility-matrix"]
  assert.equal(relayPlan("GET",path,"",config).path,"/api/v1/model-compatibility-matrix")
  assert.throws(()=>relayPlan("GET",path,"?node=node",config),/DashboardQueryInvalid/)
  assert.throws(()=>relayPlan("POST",path,"",config),/DashboardMethodNotAllowed/)
})

test("R19 new contracts: relay permits only bounded experiment pages and columnar bucket power",()=>{
  const config=serverConfig({CHILL_BASE_URL:"https://chill.example",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"chill"})
  const experiments=["v1","experiments"],power=["v1","observations","power"]
  assert.equal(relayPlan("GET",experiments,"?offset=25&limit=25",config).path,"/api/v1/experiments?offset=25&limit=25")
  assert.equal(relayPlan("GET",experiments,"",config).path,"/api/v1/experiments")
  for(const query of ["?limit=51","?limit=0","?offset=1000001","?offset=-1","?limit=25&limit=25","?q=name"])assert.throws(()=>relayPlan("GET",experiments,query,config),/DashboardQueryInvalid/)
  assert.throws(()=>relayPlan("GET",["v1","experiments","selected"],"?offset=0&limit=1",config),/DashboardQueryInvalid/)
  const range="?from=2026-11-03T03:00:00Z&to=2026-11-03T03:15:00Z"
  assert.match(relayPlan("GET",power,range+"&bucket=5s&format=columns",config).path,/format=columns/)
  for(const suffix of ["&format=columns","&bucket=5s&format=rows","&bucket=5s&format=columns&format=columns"])assert.throws(()=>relayPlan("GET",power,range+suffix,config),/DashboardQueryInvalid/)
})

test("occupancy mutations reject uncontracted queries and keep dry-run read lifetime",()=>{
  const config=serverConfig({CHILL_BASE_URL:"https://chill.example",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"chill"})
  const list=["v1","namespaces","research","dispatch-occupancy-measurements"],detail=[...list,"occ-one"]
  for(const search of ["?dryRun=","?dryRun=true","?dryRun=All&dryRun=All","?uid=one","?workers=4","?dryRun=All&duration=30"])assert.throws(()=>relayPlan("POST",list,search,config),/DashboardQueryInvalid/)
  for(const search of ["","?uid=","?uid=one&uid=two","?uid=one&dryRun=All"])assert.throws(()=>relayPlan("DELETE",detail,search,config),/DashboardQueryInvalid/)
  for(const method of ["PUT","PATCH"])assert.throws(()=>relayPlan(method,detail,"?uid=one",config),/DashboardMethodNotAllowed/)
  assert.throws(()=>relayPlan("DELETE",list,"?uid=one",config),/DashboardMethodNotAllowed/)
  assert.throws(()=>relayPlan("POST",detail,"",config),/DashboardMethodNotAllowed/)
  assert.throws(()=>relayPlan("GET",detail,"?uid=one",config),/DashboardQueryInvalid/)
  assert.equal(relayPlan("POST",list,"?dryRun=All",config).kind,"query")
  assert.equal(relayPlan("POST",list,"?dryRun=All",config).timeoutMs,config.queryTimeoutMs)
  assert.equal(relayPlan("POST",list,"",config).kind,"mutation")
  assert.equal(relayPlan("DELETE",detail,"?uid=one",config).path,"/api/v1/namespaces/research/dispatch-occupancy-measurements/occ-one?uid=one")
  assert.equal(relayPlan("DELETE",detail,"?uid=one",config).bodyAllowed,false)
  assert.equal(relayPlan("GET",list,"",config).kind,"query")
})

test("control323 review: real relay accepts bounded BEO pages and only negotiated measurement v2",()=>{
  const config=serverConfig({CHILL_BASE_URL:"https://chill.example",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"chill",CHILL_MAX_PAGE_SIZE:"200"})
  const beo="v1/namespaces/research/batch-energy-optimizations".split("/")
  for(const search of ["?view=summary&limit=50&offset=0","?view=summary&summary=v2&limit=50&offset=1000000"]) {
    assert.equal(relayPlan("GET",beo,search,config).path,`/api/${beo.join("/")}${search}`)
  }
  for(const suffix of ["limit=0","limit=51","offset=-1","offset=1000001","limit=2&limit=3","offset=0&offset=1","summary=v1","summary=v2&summary=v2"]) {
    assert.throws(()=>relayPlan("GET",beo,`?view=summary&${suffix}`,config),/DashboardQueryInvalid/)
  }
  for(const route of [beo,"v1/namespaces/research/measurement-coverage".split("/"),"v1/scheduler/profiles".split("/")]) {
    assert.equal(new URL(relayPlan("GET",route,"?view=summary&summary=v2",config).path,"https://local.invalid").searchParams.get("summary"),"v2")
    assert.throws(()=>relayPlan("GET",route,"?summary=v2",config),/DashboardQueryInvalid/)
  }
  assert.throws(()=>relayPlan("GET",["v1","workloads"],"?view=summary&summary=v2",config),/DashboardQueryInvalid/)
})

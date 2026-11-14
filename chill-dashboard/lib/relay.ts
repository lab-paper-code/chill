import { request as httpsRequest } from "node:https"
import type { IncomingMessage } from "node:http"
import { serverConfig, type PublicConfig } from "./server-config.ts"
import { timestampNanos } from "./date-time.ts"

class RelayFailure extends Error {
  readonly code: string
  readonly status: number
  readonly responseHeaders: IncomingMessage["headers"]
  constructor(code: string, status: number, responseHeaders: IncomingMessage["headers"] = {}) {
    super(code)
    this.code = code
    this.status = status
    this.responseHeaders = responseHeaders
  }
}

export function relayFailure(code: string, status: number, unknownOutcome = false): Response {
  return Response.json({ code, unknownOutcome }, { status, headers: {
    "Cache-Control": "no-store", "X-Chill-Outcome-Unknown": String(unknownOutcome),
  } })
}


function queryTimestampNanos(value: string): bigint {
  const nanos = timestampNanos(value)
  if (nanos === null) throw new RelayFailure("DashboardQueryInvalid", 400)
  return nanos
}

type RelayPlan = { path: string; kind: "query" | "mutation" | "infer"; timeoutMs: number; bodyAllowed: boolean }

export function relayPlan(method: string, segments: string[], search: string, config: PublicConfig): RelayPlan {
  if (!segments.length || segments.some(part => part.length > 253 || !/^[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(part))) throw new RelayFailure("DashboardRouteNotAllowed", 404)
  const route = segments.join("/")
  let methods: string[] = []
  let query: "none" | "summary" | "measurementPlans" | "page" | "experimentPage" | "inputs" | "profiles" | "runs" | "observations" | "event" | "load" | "power" | "schedulerHistory" | "dryRun" | "uid" | "compatibility" | "coverage" | "preflight" = "none"
  let kind: RelayPlan["kind"] = "query"
  if (/^v1\/(models|workloads|preprocessors|datasets)$/.test(route)) { methods = ["GET", "POST"]; if(route==="v1/workloads"&&method==="GET")query="summary" }
  else if (/^v1\/(models|workloads|preprocessors|datasets)\/[^/]+$/.test(route)) methods = ["GET", "PUT", "DELETE"]
  else if (/^v1\/workloads\/[^/]+\/infer$/.test(route)) { methods = ["POST"]; kind = "infer" }
  else if (/^v1\/pools(?:\/[^/]+)?$/.test(route)) methods = ["GET"]
  else if (/^v1\/observations\/workloads\/[^/]+$/.test(route)) { methods = ["GET"]; query = "observations" }
  else if (route === "v1/observations/load") { methods = ["GET"]; query = "load" }
  else if (route === "v1/observations/power") { methods = ["GET"]; query = "power" }
  else if (route === "v1/observations/occupancy") { methods = ["GET"]; query = "load" }
  else if (route === "v1/observations/scheduler") { methods = ["GET"]; query = "schedulerHistory" }
  else if (route === "v1/observations/events") { methods = ["GET"]; query = "event" }
  else if (route === "v1/scheduler/capacity") methods = ["GET"]
  else if (route === "v1/model-compatibility") { methods = ["GET"]; query = "compatibility" }
  else if (route === "v1/model-compatibility-matrix") methods = ["GET"]
  else if (route === "v1/experiments") { methods = ["GET"]; query = "experimentPage" }
  else if (/^v1\/experiments\/[^/]+$/.test(route)) methods = ["GET"]
  else if (route === "v1/evaluations/inputs") { methods = ["GET"]; query = "inputs" }
  else if (route === "v1/evaluations/active" || route === "v1/evaluations/template") methods = ["GET"]
  else if (route === "v1/evaluations/preflight" || route === "v1/evaluations/load-scenarios/preflight") methods = ["POST"]
  else if (route === "v1/evaluations" || route === "v1/evaluations/load-scenarios") methods = ["GET", "POST"]
  else if (/^v1\/evaluations\/load-scenarios\/[^/]+$/.test(route)) { methods = ["GET", "PUT", "DELETE"]; if (method !== "GET") query = "uid" }
  else if (/^v1\/evaluations\/[^/]+\/release$/.test(route)) methods = ["POST"]
  else if (/^v1\/evaluations\/[^/]+$/.test(route)) { methods = ["GET", "PUT", "DELETE"]; if (method !== "GET") query = "uid" }
  else if (/^v1\/namespaces\/[^/]+\/dispatch-occupancy-measurements$/.test(route)) { methods = ["GET", "POST"]; if (method === "POST") query = "dryRun" }
  else if (/^v1\/namespaces\/[^/]+\/dispatch-occupancy-measurements\/[^/]+$/.test(route)) { methods = ["GET", "DELETE"]; if (method === "DELETE") query = "uid" }
  else if (/^v1\/namespaces\/[^/]+\/operation-cost-measurements(?:\/[^/]+)?$/.test(route)) methods = ["GET"]
  else if (route === "v1/scheduler" || route === "v1/scheduler/state") methods = ["GET"]
  else if (route === "v1/scheduler/settings") methods = ["GET", "PUT"]
  else if (route === "v1/scheduler/operations") methods = ["POST"]
  else if (route === "v1/scheduler/profiles") { methods = ["GET"]; query = "profiles" }
  else if (/^v1\/(systems|inventory)$/.test(route)) { methods = ["GET"]; query = "page" }
  else if (/^v1\/namespaces\/[^/]+\/measurement-coverage$/.test(route)) { methods = ["GET"]; query = "coverage" }
  else if (/^v1\/namespaces\/[^/]+\/profiling-runs$/.test(route)) { methods = ["GET"]; query = "runs" }
  else if (/^v1\/namespaces\/[^/]+\/profiling-runs\/[^/]+\/evidence-passport$/.test(route) || /^v1\/namespaces\/[^/]+\/batch-energy-optimizations\/[^/]+\/result$/.test(route)) methods = ["GET"]
  else if (/^v1\/namespaces\/[^/]+\/batch-energy-optimizations\/preflight$/.test(route)) { methods = ["POST"]; query = "preflight" }
  else if (/^v1\/namespaces\/[^/]+\/batch-energy-optimizations$/.test(route)) { methods = ["GET", "POST"]; query = method === "POST" ? "dryRun" : "measurementPlans" }
  else if (/^v1\/namespaces\/[^/]+\/batch-energy-optimizations\/[^/]+$/.test(route)) { methods = ["DELETE"]; query = "uid" }
  if (!methods.length) throw new RelayFailure("DashboardRouteNotAllowed", 404)
  if (!methods.includes(method)) throw new RelayFailure("DashboardMethodNotAllowed", 405)
  if (Buffer.byteLength(search) > 4096 || /%(?![0-9a-fA-F]{2})/.test(search)) throw new RelayFailure("DashboardQueryInvalid", 400)
  const params = new URLSearchParams(search)
  let powerBucket:bigint|undefined
  // A server dry-run creates nothing, so it keeps the query lifetime like preflight instead of an unknown mutation outcome.
  const dryRun = query === "dryRun" && params.has("dryRun")
  if (kind !== "infer" && method !== "GET" && !dryRun && !/^v1\/evaluations\/(?:load-scenarios\/)?preflight$/.test(route) && !/^v1\/namespaces\/[^/]+\/batch-energy-optimizations\/preflight$/.test(route)) kind = "mutation"
  // History lists default to summaries; selected records explicitly request compact evidence.
  const compact = method === "GET" && (query === "schedulerHistory" || route === "v1/scheduler" || route === "v1/scheduler/state")
  const allowed = compact ? ["view"] : []
  if (query === "summary" || query === "measurementPlans" || query === "coverage" || query === "profiles") allowed.push("view")
  if (query === "measurementPlans" || query === "coverage" || query === "profiles") allowed.push("summary")
  if (query === "measurementPlans") allowed.push("limit","offset")
  if (query === "coverage") allowed.push("batches","node")
  if (query === "inputs") allowed.push("kind","set","offset","limit")
  if (query === "experimentPage") allowed.push("offset","limit")
  if (query === "event") allowed.push("at","from","to","limit","cursor")
  if (query === "power") allowed.push("from","to","bucket","format")
  if (query === "profiles") allowed.push("limit","offset","model","deviceClass","profile","q","node","powerMode","path","status","sort","order")
  if (query === "runs") allowed.push("model","deviceClass","node","path","powerMode","status","group","parentUID","sort","order")
  allowed.push(...(query === "compatibility" ? ["modelSpec", "executionPath", "deviceClass", "node"] : query === "observations" ? ["from", "to", "limit", "cursor", "order", "definition", "outcome"] : query === "load" ? ["from", "to"] : query === "schedulerHistory" ? ["from", "to", "limit", "cursor", "order", "filter", "uid"] : query === "page" ? ["limit", "offset", "q"] : query === "runs" ? ["limit", "offset", "q", "condition"] : query === "dryRun" ? ["dryRun"] : query === "uid" ? ["uid"] : []))
  for (const key of new Set(params.keys())) {
    const values = params.getAll(key)
    if (!allowed.includes(key) || (values.length > 1 && key !== "definition" && key !== "outcome") || values.length > 32 || values.some(value => /[\x00-\x1f\x7f\ufffd]/.test(value))) throw new RelayFailure("DashboardQueryInvalid", 400)
    for (const value of values) {
      if (key === "cursor" && query === "event") {
        if (!value || Buffer.byteLength(value)>4096) throw new RelayFailure("DashboardQueryInvalid",400)
      } else if (key === "format") {
        if(value!=="columns")throw new RelayFailure("DashboardQueryInvalid",400)
      } else if (key === "bucket") {
        const match=/^(\d+(?:\.\d{1,9})?|\.\d{1,9})(s|m|h)$/.exec(value)
        if (!match) throw new RelayFailure("DashboardQueryInvalid",400)
        const [whole,fraction=""]=match[1].split(".")
        powerBucket=(BigInt(whole||"0")*1000000000n+BigInt(fraction.padEnd(9,"0")))*(match[2]==="h"?3600n:match[2]==="m"?60n:1n)
        if (powerBucket<1000000000n||powerBucket>3600000000000n) throw new RelayFailure("DashboardQueryInvalid",400)
      } else if (["limit", "offset", "cursor"].includes(key)) {
        const max = key === "limit" ? (query === "event" ? 500 : query === "measurementPlans" || query === "profiles" || query === "inputs" || query === "experimentPage" ? 50 : config.maxPageSize) : key === "offset" ? 1000000 : Number.MAX_SAFE_INTEGER
        if (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value)) || Number(value) > max || (key === "limit" && Number(value) < 1)) throw new RelayFailure("DashboardQueryInvalid", 400)
      } else if (key === "sort") {
        const keys = query === "profiles" ? ["model","deviceClass","node","powerMode","path","created","status"] : ["name","created","model","powerMode","status"]
        if (!keys.includes(value)) throw new RelayFailure("DashboardQueryInvalid", 400)
      } else if (key === "order") {
        if (value !== "asc" && value !== "desc") throw new RelayFailure("DashboardQueryInvalid", 400)
      } else if (key === "filter") {
        if (!["all", "decision-action", "decision-change", "decision", "action", "snapshot", "settings", "power", "other"].includes(value)) throw new RelayFailure("DashboardQueryInvalid", 400)
      } else if (key === "uid" && query === "schedulerHistory") {
        if (!value || Buffer.byteLength(value) > 256 || /[ /\\]/.test(value)) throw new RelayFailure("DashboardQueryInvalid", 400)
      } else if (key === "batches") {
        const batches=value.split(",").map(Number)
        if (!/^[1-9]\d*(,[1-9]\d*)*$/.test(value) || batches.some((n,i)=>!Number.isSafeInteger(n)||(i>0&&n<=batches[i-1]))) throw new RelayFailure("DashboardQueryInvalid",400)
      } else if (key === "condition") {
        if (Buffer.byteLength(value) > 128 || !/^[^=]+=(True|False|Unknown)$/.test(value)) throw new RelayFailure("DashboardQueryInvalid", 400)
      } else if (key === "summary") {
        if(value!=="v2"||params.get("view")!=="summary")throw new RelayFailure("DashboardQueryInvalid",400)
      } else if (key === "view") {
        const summary=route==="v1/scheduler/state"||query==="schedulerHistory"||query==="summary"||query==="measurementPlans"||query==="coverage"||query==="profiles"
        if (!(value==="summary"&&summary) && !(value==="compact"&&compact)) throw new RelayFailure("DashboardQueryInvalid", 400)
      } else if (key === "dryRun") {
        if (value !== "All") throw new RelayFailure("DashboardQueryInvalid", 400)
      } else if (Buffer.byteLength(value) > (key === "group" ? 16384 : key === "outcome" ? 64 : query === "inputs" && key === "set" ? 512 : 256) || (key !== "q" && !value)) throw new RelayFailure("DashboardQueryInvalid", 400)
    }
  }
  if (params.has("order") && query !== "schedulerHistory" && query !== "observations" && !params.has("sort")) throw new RelayFailure("DashboardQueryInvalid", 400)
  if (query === "uid" && !params.get("uid")) throw new RelayFailure("DashboardQueryInvalid", 400)
  if (query === "compatibility" && allowed.some(key => !params.get(key))) throw new RelayFailure("DashboardQueryInvalid", 400)
  if (query === "event" && params.has("at")) {
    if ([...params.keys()].some(key=>key!=="at"&&key!=="cursor")) throw new RelayFailure("DashboardQueryInvalid",400)
    queryTimestampNanos(params.get("at") ?? "")
    if (params.has("cursor")) queryTimestampNanos(params.get("cursor") ?? "")
  } else if (query === "event") {
    const from=queryTimestampNanos(params.get("from")??""),to=queryTimestampNanos(params.get("to")??"")
    if (to<=from||to-from>86400000000000n) throw new RelayFailure("DashboardQueryInvalid",400)
    if (!params.has("limit")) params.set("limit",String(Math.min(100,config.pageSize)))
  }
  if (["page", "profiles", "runs", "observations", "schedulerHistory"].includes(query) && !params.has("limit")) params.set("limit", String(query==="profiles"?Math.min(50,config.pageSize):config.pageSize))
  if (query === "observations" || query === "load" || query === "power" || query === "schedulerHistory") {
    if(query==="power"&&params.has("format")&&!params.has("bucket"))throw new RelayFailure("DashboardQueryInvalid",400)
    const from = params.get("from"), to = params.get("to")
    if (!from || !to) throw new RelayFailure("DashboardQueryInvalid", 400)
    const window = queryTimestampNanos(to) - queryTimestampNanos(from)
    if (window <= 0n || window > BigInt(config.maxWindowHours * 3600000) * 1000000n) throw new RelayFailure("DashboardQueryInvalid", 400)
    if (query === "power" && params.has("bucket")) {
      const bucket=powerBucket!
      // Go Time.Truncate measures duration multiples from year 1, not the Unix epoch.
      const start=queryTimestampNanos(from)+62135596800000000000n,remainder=((start%bucket)+bucket)%bucket
      if (window>3600000000000n||(window+remainder+bucket-1n)/bucket>900n) throw new RelayFailure("DashboardQueryInvalid",400)
    }
  }
  if (compact && !params.has("view")) params.set("view", "compact")
  const suffix = params.toString()
  // Native inference may consume its entire cap; keep a bounded delivery margin.
  return { path: `/api/${route}${suffix ? `?${suffix}` : ""}`, kind, timeoutMs: kind === "infer" ? config.inferTimeoutMs + config.queryTimeoutMs : kind === "mutation" ? config.mutationTimeoutMs : config.queryTimeoutMs, bodyAllowed: method === "POST" || method === "PUT" }
}

export async function boundedBody(stream: ReadableStream<Uint8Array> | null, limit: number, signal: AbortSignal): Promise<Buffer> {
  if (!stream) return Buffer.alloc(0)
  const reader = stream.getReader()
  const chunks: Uint8Array[] = []
  let size = 0
  const abort = () => { void reader.cancel().catch(() => {}) }
  signal.addEventListener("abort", abort, { once: true })
  try {
    if (signal.aborted) throw new RelayFailure("DashboardRelayCanceled", 499)
    while (true) {
      const { done, value } = await reader.read()
      if (signal.aborted) throw new RelayFailure("DashboardRelayCanceled", 499)
      if (done) break
      size += value.byteLength
      if (size > limit) {
        void reader.cancel().catch(() => {})
        throw new RelayFailure("DashboardRequestTooLarge", 413)
      }
      chunks.push(value)
    }
    return Buffer.concat(chunks, size)
  } finally {
    signal.removeEventListener("abort", abort)
    reader.releaseLock()
  }
}

function upstream(chillServer: URL, path: string, method: string, headers: Record<string, string>, body: Buffer, limit: number, signal: AbortSignal): Promise<{ status: number; headers: IncomingMessage["headers"]; body: Buffer }> {
  const { promise, resolve, reject } = Promise.withResolvers<{ status: number; headers: IncomingMessage["headers"]; body: Buffer }>()
  let responseHeaders: IncomingMessage["headers"] = {}
  // Native Node trust includes NODE_EXTRA_CA_CERTS; never a custom trust-any agent.
  const req = httpsRequest(chillServer, { method, path, headers, signal, rejectUnauthorized: true, maxHeaderSize: 16384 }, res => {
    responseHeaders = res.headers
    const chunks: Buffer[] = []
    let bytes = 0
    const declared = res.headers["content-length"]
    if (declared && (!/^\d+$/.test(declared) || Number(declared) > limit)) {
      reject(new RelayFailure("DashboardResponseTooLarge", 502, res.headers))
      res.destroy()
      req.destroy()
      return
    }
    res.on("data", (chunk: Buffer) => {
      bytes += chunk.length
      if (bytes > limit) {
        reject(new RelayFailure("DashboardResponseTooLarge", 502, res.headers))
        res.destroy()
        req.destroy()
      } else chunks.push(chunk)
    })
    res.on("end", () => resolve({ status: res.statusCode!, headers: res.headers, body: Buffer.concat(chunks, bytes) }))
    res.on("error", () => reject(new RelayFailure("DashboardRelayUnavailable", 502, res.headers)))
    res.on("aborted", () => reject(new RelayFailure("DashboardRelayUnavailable", 502, res.headers)))
  })
  req.on("error", () => reject(new RelayFailure("DashboardRelayUnavailable", 502, responseHeaders)))
  req.end(body)
  return promise
}


export async function relay(request: Request, segments: string[]): Promise<Response> {
  let config
  try { config = serverConfig() } catch { return relayFailure("DashboardConfigInvalid", 503) }
  let plan: RelayPlan
  const url = new URL(request.url)
  try { plan = relayPlan(request.method, segments, url.search, config) } catch (error) {
    if (error instanceof RelayFailure) return relayFailure(error.code, error.status)
    return relayFailure("DashboardQueryInvalid", 400)
  }
  const authorization = request.headers.get("authorization")
  if (!authorization || authorization.length > 16384 || !/^Bearer [A-Za-z0-9._~+\/-]+=*$/i.test(authorization)) return relayFailure("DashboardAuthenticationRequired", 401)
  const origin = request.headers.get("origin")
  // TLS ingress preserves Host; Next's request URL may name the private HTTP listener.
  if (request.method !== "GET" && origin !== null && origin !== `https://${request.headers.get("host") ?? url.host}`) return relayFailure("DashboardOriginDenied", 403)
  const headers: Record<string, string> = { Authorization: authorization }
  for (const name of ["if-match", "content-type"]) {
    const value = request.headers.get(name)
    if (value !== null) {
      if (value.length > 1024 || /[\x00-\x1f\x7f]/.test(value)) return relayFailure("DashboardHeaderInvalid", 400)
      headers[name] = value
    }
  }
  const nativeTimeout = request.headers.get("x-chill-request-timeout")
  if (nativeTimeout !== null) {
    if (plan.kind !== "infer" || !nativeTimeout || nativeTimeout.length > 128 || /[\x00-\x1f\x7f]/.test(nativeTimeout)) return relayFailure("DashboardHeaderInvalid", 400)
    headers["X-Chill-Request-Timeout"] = nativeTimeout
  }
  const declared = request.headers.get("content-length")
  if (declared !== null && (!/^\d+$/.test(declared) || Number(declared) > config.maxRequestBytes)) return relayFailure("DashboardRequestTooLarge", 413)
  const controller = new AbortController()
  let timedOut = false
  const timer = setTimeout(() => { timedOut = true; controller.abort() }, plan.timeoutMs)
  const abort = () => controller.abort()
  request.signal.addEventListener("abort", abort, { once: true })
  if (request.signal.aborted) controller.abort()
  let sent = false
  const started = performance.now()
  try {
    const body = await boundedBody(request.body, config.maxRequestBytes, controller.signal)
    if (body.length && !plan.bodyAllowed) return relayFailure("DashboardBodyNotAllowed", 400)
    if (controller.signal.aborted) throw new RelayFailure("DashboardRelayCanceled", 499)
    sent = true
    const result = await upstream(config.chillServer, plan.path, request.method, headers, body, config.maxRequestBytes, controller.signal)
    const responseHeaders = new Headers({ "Cache-Control": "no-store", "X-Chill-Relay-Timing": (performance.now() - started).toFixed(1) })
    for (const name of ["content-type", "content-encoding", "x-chill-request-id", "x-chill-workload-uid", "if-match", "retry-after"]) {
      const value = result.headers[name]
      if (typeof value === "string") responseHeaders.set(name, value)
    }
    if(plan.path==="/api/v1/scheduler/settings"&&typeof result.headers.etag==="string")responseHeaders.set("ETag",result.headers.etag)
    const bytes = new Uint8Array(result.body.buffer as ArrayBuffer, result.body.byteOffset, result.body.byteLength)
    return new Response([204, 205, 304].includes(result.status) ? null : bytes, { status: result.status, headers: responseHeaders })
  } catch (error) {
    const unknown = sent && plan.kind !== "query"
    const response = timedOut ? relayFailure("DashboardRelayTimeout", 504, unknown)
      : request.signal.aborted ? relayFailure("DashboardRelayCanceled", 499, unknown)
      : error instanceof RelayFailure ? relayFailure(error.code, error.status, unknown)
      : relayFailure("DashboardRelayUnavailable", 502, unknown)
    if (error instanceof RelayFailure) {
      for (const name of ["x-chill-request-id", "x-chill-workload-uid", "if-match"]) {
        const value = error.responseHeaders[name]
        if (typeof value === "string") response.headers.set(name, value)
      }
    }
    return response
  } finally {
    clearTimeout(timer)
    request.signal.removeEventListener("abort", abort)
  }
}

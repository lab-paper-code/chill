import { request as httpsRequest } from "node:https"
import type { IncomingMessage } from "node:http"
import { serverConfig, type PublicConfig } from "./server-config.ts"

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


function timestampNanos(value: string): bigint {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/.exec(value)
  if (!match) throw new RelayFailure("DashboardQueryInvalid", 400)
  const [, year, month, day, hour, minute, second, fraction = "", zone] = match
  const date = new Date(`${year}-${month}-${day}T${hour}:${minute}:${second}Z`)
  if (!Number.isFinite(date.getTime()) || date.toISOString().slice(0, 19) !== `${year}-${month}-${day}T${hour}:${minute}:${second}`) throw new RelayFailure("DashboardQueryInvalid", 400)
  let offset = 0
  if (zone !== "Z") {
    const h = Number(zone.slice(1, 3)), m = Number(zone.slice(4, 6))
    if (h > 23 || m > 59) throw new RelayFailure("DashboardQueryInvalid", 400)
    offset = (h * 60 + m) * (zone[0] === "+" ? 1 : -1)
  }
  return BigInt(date.getTime() - offset * 60000) * 1000000n + BigInt(fraction.padEnd(9, "0"))
}

type RelayPlan = { path: string; kind: "query" | "mutation" | "infer"; timeoutMs: number; bodyAllowed: boolean }

export function relayPlan(method: string, segments: string[], search: string, config: PublicConfig): RelayPlan {
  if (!segments.length || segments.some(part => part.length > 253 || !/^[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(part))) throw new RelayFailure("DashboardRouteNotAllowed", 404)
  const route = segments.join("/")
  let methods: string[] = []
  let query: "none" | "page" | "runs" | "observations" | "load" | "schedulerHistory" = "none"
  let kind: RelayPlan["kind"] = "query"
  if (/^v1\/(models|workloads)$/.test(route)) methods = ["GET", "POST"]
  else if (/^v1\/(models|workloads)\/[^/]+$/.test(route)) methods = ["GET", "PUT", "DELETE"]
  else if (/^v1\/workloads\/[^/]+\/infer$/.test(route)) { methods = ["POST"]; kind = "infer" }
  else if (/^v1\/pools(?:\/[^/]+)?$/.test(route)) methods = ["GET"]
  else if (/^v1\/observations\/workloads\/[^/]+$/.test(route)) { methods = ["GET"]; query = "observations" }
  else if (route === "v1/observations/load") { methods = ["GET"]; query = "load" }
  else if (route === "v1/observations/scheduler") { methods = ["GET"]; query = "schedulerHistory" }
  else if (route === "v1/scheduler" || route === "v1/scheduler/state") methods = ["GET"]
  else if (route === "v1/scheduler/settings") methods = ["GET", "PUT"]
  else if (/^v1\/(systems|inventory)$/.test(route)) { methods = ["GET"]; query = "page" }
  else if (/^v1\/namespaces\/[^/]+\/profiling-runs$/.test(route)) { methods = ["GET"]; query = "runs" }
  else if (/^v1\/namespaces\/[^/]+\/profiling-runs\/[^/]+\/evidence-passport$/.test(route) || /^v1\/namespaces\/[^/]+\/batch-energy-optimizations\/[^/]+\/result$/.test(route)) methods = ["GET"]
  else if (/^v1\/namespaces\/[^/]+\/batch-energy-optimizations\/preflight$/.test(route)) methods = ["POST"] // Read-only producer; query lifetime and no unknown mutation outcome.
  if (!methods.length) throw new RelayFailure("DashboardRouteNotAllowed", 404)
  if (!methods.includes(method)) throw new RelayFailure("DashboardMethodNotAllowed", 405)
  if (kind !== "infer" && method !== "GET" && !/^v1\/namespaces\/[^/]+\/batch-energy-optimizations\/preflight$/.test(route)) kind = "mutation"
  if (Buffer.byteLength(search) > 4096 || /%(?![0-9a-fA-F]{2})/.test(search)) throw new RelayFailure("DashboardQueryInvalid", 400)
  const params = new URLSearchParams(search)
  const allowed = query === "observations" ? ["from", "to", "limit", "cursor", "definition", "outcome"] : query === "load" ? ["from", "to"] : query === "schedulerHistory" ? ["from", "to", "limit", "cursor"] : query === "page" ? ["limit", "offset", "q"] : query === "runs" ? ["limit", "offset", "q", "condition"] : []
  for (const key of new Set(params.keys())) {
    const values = params.getAll(key)
    if (!allowed.includes(key) || (values.length > 1 && key !== "definition" && key !== "outcome") || values.length > 32 || values.some(value => /[\x00-\x1f\x7f\ufffd]/.test(value))) throw new RelayFailure("DashboardQueryInvalid", 400)
    for (const value of values) {
      if (["limit", "offset", "cursor"].includes(key)) {
        const max = key === "limit" ? config.maxPageSize : key === "offset" ? 1000000 : Number.MAX_SAFE_INTEGER
        if (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value)) || Number(value) > max || (key === "limit" && Number(value) < 1)) throw new RelayFailure("DashboardQueryInvalid", 400)
      } else if (key === "condition") {
        if (Buffer.byteLength(value) > 128 || !/^[^=]+=(True|False|Unknown)$/.test(value)) throw new RelayFailure("DashboardQueryInvalid", 400)
      } else if (Buffer.byteLength(value) > (key === "outcome" ? 64 : 256) || (key !== "q" && !value)) throw new RelayFailure("DashboardQueryInvalid", 400)
    }
  }
  if (query !== "none" && query !== "load" && !params.has("limit")) params.set("limit", String(config.pageSize))
  if (query === "observations" || query === "load" || query === "schedulerHistory") {
    const from = params.get("from"), to = params.get("to")
    if (!from || !to) throw new RelayFailure("DashboardQueryInvalid", 400)
    const window = timestampNanos(to) - timestampNanos(from)
    if (window <= 0n || window > BigInt(config.maxWindowHours * 3600000) * 1000000n) throw new RelayFailure("DashboardQueryInvalid", 400)
  }
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

function upstream(owner: URL, path: string, method: string, headers: Record<string, string>, body: Buffer, limit: number, signal: AbortSignal): Promise<{ status: number; headers: IncomingMessage["headers"]; body: Buffer }> {
  const { promise, resolve, reject } = Promise.withResolvers<{ status: number; headers: IncomingMessage["headers"]; body: Buffer }>()
  let responseHeaders: IncomingMessage["headers"] = {}
  // Native Node trust includes NODE_EXTRA_CA_CERTS; never a custom trust-any agent.
  const req = httpsRequest(owner, { method, path, headers, signal, rejectUnauthorized: true, maxHeaderSize: 16384 }, res => {
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
    const result = await upstream(config.owner, plan.path, request.method, headers, body, config.maxRequestBytes, controller.signal)
    const responseHeaders = new Headers({ "Cache-Control": "no-store", "X-Chill-Relay-Timing": (performance.now() - started).toFixed(1) })
    for (const name of ["content-type", "content-encoding", "x-chill-request-id", "x-chill-workload-uid", "if-match"]) {
      const value = result.headers[name]
      if (typeof value === "string") responseHeaders.set(name, value)
    }
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

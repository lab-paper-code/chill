// Local replay of a captured ChillServer for UI work. Never imported by the application or image entrypoint.
// node --experimental-strip-types server/replay.mjs --capture <dir> [--port 3600] [--scene captured|active] [--no-next]
import https from 'node:https'
import net from 'node:net'
import { once } from 'node:events'
import { execFileSync, spawn } from 'node:child_process'
import { createHash, randomUUID } from 'node:crypto'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseArgs } from 'node:util'
import { WebSocketServer, WebSocket } from 'ws'
import { createFront } from './live-proxy.mjs'
import { loadCapture, nanos, pageOf, shiftTimes } from './replay-capture.mjs'
import { capturedJournal, queryJournal } from './replay-journal.mjs'
import { activeScene } from './replay-scene.mjs'
import { activeMeasurements } from './replay-measure.mjs'
import { measurementRequests } from './replay-requests.mjs'
import { parseMeasurementJSON } from '../lib/measurement-summary.ts'
import { parseSchedulerJSON, stringifySchedulerJSON } from '../lib/scheduler.ts'

const SCENES = ['captured', 'active']
const { values: args } = parseArgs({ options: { capture: { type: 'string' }, port: { type: 'string', default: '3600' }, scene: { type: 'string', default: 'captured' }, 'no-next': { type: 'boolean', default: false }, 'self-check': { type: 'boolean', default: false } } })
if (!args.capture || !SCENES.includes(args.scene)) { console.error('usage: replay.mjs --capture <dir> [--port 3600] [--scene captured|active] [--no-next] [--self-check]'); process.exit(2) }
const port = Number(args.port), upstreamPort = port + 1, nextPort = port + 2
const startedAt = Date.now()
// One time shift for the life of the process: captured records keep their timestamps and age.
const capture = loadCapture(args.capture, startedAt)
const ns = capture.meta.namespace, NS = `v1/namespaces/${ns}/`
// The prepare action of the first cycle started 40 s before the server did.
const epoch = startedAt - 40_480
const measure = activeMeasurements(capture, epoch)
const active = activeScene(capture, epoch, { profileFor: measure.profileFor, supportedInputs: measure.supportedInputs })
const capturedRecords = capturedJournal(capture)
const inventoryNodes = parsed => Object.fromEntries(parsed.classes.flatMap(cls => cls.nodes.map(node => [node.name, node.uid])))
const requests = measurementRequests({ capture, startedAt, ns, nodeUID: inventoryNodes(capture.json('v1/inventory', '?limit=200&offset=0')), measure })
let defaultScene = args.scene
const mutations = []

const directory = mkdtempSync(join(tmpdir(), 'chill-replay-tls-'))
execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', join(directory, 'key.pem'), '-out', join(directory, 'cert.pem'), '-days', '7', '-subj', '/CN=localhost', '-addext', 'subjectAltName=IP:127.0.0.1,DNS:localhost'], { stdio: 'ignore' })
const cert = readFileSync(join(directory, 'cert.pem')), key = readFileSync(join(directory, 'key.pem'))

const iso = ms => new Date(ms).toISOString().replace(/\.?0+Z$/, 'Z')
const seconds = ms => new Date(ms).toISOString().replace(/\.\d+Z$/, 'Z')
const error = (status, code, message) => [status, { code, message }]
const dns = name => typeof name === 'string' && name.length <= 253 && /^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/.test(name)
const hash = text => createHash('sha256').update(text).digest('hex')

// Mutable per-scene state, copied lazily from the capture.
const KINDS = { workloads: 'Workload', models: 'ModelSpec', preprocessors: 'Preprocessor', datasets: 'Dataset' }
const capturedSettings = capture.json('v1/scheduler/settings')
const capturedETag = Number(JSON.parse(capture.match('v1/scheduler/settings').etag))
const state = Object.fromEntries(SCENES.map(name => {
  const settings = name === 'active' ? active.settings(capturedSettings) : capturedSettings
  const history = name === 'active' ? [active.settingsChange(capturedSettings, settings)] : []
  return [name, { settings, etag: capturedETag, history, catalog: {}, inferRows: [], beo: { added: [], removed: new Set() }, dom: { added: [], removed: new Set() },
    evaluations: null, loadScenarios: null, released: null, stopRequested: null }]
}))

const tokenOf = header => /^Bearer (\S+)$/.exec(header ?? '')?.[1]
const sceneOf = token => SCENES.includes(token) ? token : defaultScene
const autonomous = scene => state[scene].settings.autonomousWorkloads ?? []

function send(res, status, body, headers = {}) {
  res.writeHead(status, { 'content-type': 'application/json', 'cache-control': 'no-store', ...headers })
  res.end(body === undefined ? undefined : typeof body === 'string' ? body : stringifySchedulerJSON(body))
}
async function readBody(req) {
  let raw = ''
  req.setEncoding('utf8')
  for await (const chunk of req) { raw += chunk; if (raw.length > 1 << 20) throw Object.assign(new Error('too large'), { status: 413 }) }
  if (!raw) return undefined
  try { return parseSchedulerJSON(raw) } catch { throw Object.assign(new Error('invalid JSON'), { status: 400 }) }
}
const version = req => req.headers['if-match']?.trim().replace(/^"(.*)"$/, '$1')
const parsed = (path, search = '') => capture.json(path, search)
const paging = (q, fallback, maximum) => ({ offset: Number(q.get('offset') ?? 0) || 0, limit: Math.min(maximum, Number(q.get('limit') ?? fallback) || fallback) })
const window = (q, now, span) => { const to = q.has('to') ? Date.parse(q.get('to')) : now; return { from: q.has('from') ? Date.parse(q.get('from')) : to - span, to } }

// Catalog kinds (workloads, models, preprocessors, datasets): per-scene copies of the captured lists.
function catalog(scene, kind) {
  const store = state[scene].catalog
  if (!store[kind]) {
    const list = parsed(`v1/${kind}`)
    store[kind] = { meta: Array.isArray(list) ? null : list.metadata, items: new Map((Array.isArray(list) ? list : list.items).map(item => [(item.desired ?? item).metadata.name, item])) }
  }
  return store[kind]
}
const summary = view => ({ ...view, supportedInputs: view.supportedInputs.map(input => ({ executionPath: input.executionPath, artifact: input.artifact, profiles: input.profiles.map(profile => ({ id: profile.id })) })) })

// Evaluations: captured runs plus, in the active scene, the running, blocked and pending runs.
function evaluations(scene, now) {
  const own = state[scene]
  if (!own.evaluations) {
    own.evaluations = new Map(parsed('v1/evaluations').items.map(item => [item.name, item]))
    if (scene === 'active') for (const view of [active.blockedEvaluation(null), active.pendingEvaluation()]) own.evaluations.set(view.name, view)
    if (scene === 'active') own.evaluations.set(active.evaluationNames.running, 'running')
  }
  return [...own.evaluations.entries()].map(([name, view]) => view === 'running' ? { ...active.runningEvaluation(now), ...(own.stopRequested ? { stopRequested: own.stopRequested } : {}) }
    : name === active.evaluationNames.blocked && scene === 'active' ? active.blockedEvaluation(own.released) : view)
}
function loadScenarios(scene) {
  const own = state[scene]
  if (!own.loadScenarios) {
    const inUse = scene === 'active' ? active.loadScenariosInUse() : []
    own.loadScenarios = new Map(parsed('v1/evaluations/load-scenarios').items.map(item => [item.name, inUse.includes(item.name) ? { ...item, inUse: [active.evaluationNames.running] } : item]))
  }
  return own.loadScenarios
}

// Occupancy list: submissions, active overlays, captured items, deletions. BEO requests and runs live in replay-requests.mjs.
function domItems(scene, now) {
  const own = state[scene].dom
  const items = [...own.added, ...(scene === 'active' ? measure.occupancy(now) : []), ...parsed(`${NS}dispatch-occupancy-measurements`).items]
  return items.filter(item => !own.removed.has(item.metadata.name))
}

// Captured power columns re-bucketed onto the requested window; buckets outside the capture are empty.
function capturedPower(q) {
  const from = Date.parse(q.get('from')), to = Date.parse(q.get('to'))
  if (!q.has('bucket')) {
    const body = parsed('v1/observations/power')
    return [200, { ...body, from: q.get('from'), to: q.get('to') }]
  }
  const match = /^(\d+(?:\.\d+)?)(s|m|h)$/.exec(q.get('bucket')), bucket = match ? Math.round(Number(match[1]) * { s: 1000, m: 60_000, h: 3_600_000 }[match[2]]) : 0
  const start = Math.floor(from / bucket) * bucket, count = Math.ceil((to - start) / bucket)
  if (!bucket || bucket < 1000 || bucket > 3_600_000 || to - from > 3_600_000 || count > 900) return error(400, 'InvalidQuery', 'power series requires a positive from/to range of at most 1h and bucket between 1s and 1h')
  const series = capture.entries.filter(entry => entry.path === 'v1/observations/power' && entry.params.some(([key]) => key === 'bucket')).map(entry => parsed(entry.path, entry.key.slice(entry.path.length)))
    .find(body => body.bucketNanos === bucket * 1e6)
  const windowKeys = ['scope', 'wholePoolJoules', 'wholePoolMeanWatts', 'activePoolJoules', 'activePoolMeanWatts', 'complete', 'readFailures', 'maximumSampleGapSeconds', 'reasons', 'gapUntil', 'coverage']
  const nodeKeys = series ? Object.keys(series.nodes) : []
  const buckets = Array.from({ length: count }, (_, i) => {
    const left = Math.max(from, start + i * bucket), right = Math.min(to, start + (i + 1) * bucket)
    const j = series ? (start + i * bucket - Math.floor(Date.parse(series.from) / bucket) * bucket) / bucket : -1
    if (series && j >= 0 && j < series.windows.scope.length) return { from: iso(left), to: iso(right), ...Object.fromEntries(windowKeys.map(key => [key, series.windows[key][j]])),
      nodes: series.nodes.node[j].map((_, n) => Object.fromEntries(nodeKeys.map(key => [key, series.nodes[key][j][n]]))) }
    return { from: iso(left), to: iso(right), scope: 'whole-pool-wall', nodes: [], wholePoolJoules: null, wholePoolMeanWatts: null, activePoolJoules: null, activePoolMeanWatts: null, complete: false,
      readFailures: 0, maximumSampleGapSeconds: 0, reasons: ['raw history for selected interval is incomplete'], gapUntil: null, coverage: { from: iso(left), to: iso(right), sources: [], gaps: [] } }
  })
  const head = { from: q.get('from'), to: q.get('to'), bucketNanos: bucket * 1e6 }
  if (q.get('format') !== 'columns') return [200, { ...head, buckets }]
  const keys = nodeKeys.length ? nodeKeys : ['node', 'meter', 'scope', 'joules', 'meanWatts', 'samples', 'reasons']
  return [200, { ...head, format: 'columns', windows: Object.fromEntries(windowKeys.map(key => [key, buckets.map(row => row[key] ?? null)])),
    nodes: Object.fromEntries(keys.map(key => [key, buckets.map(row => row.nodes.map(node => node[key] ?? null))])) }]
}

// v1/observations/events: either `at` (+cursor) or `from,to` (+limit, cursor); numeric cursor over the ascending window.
function events(scene, q, now) {
  const base = parsed('v1/observations/events') ?? {}
  const rows = (from, to) => scene === 'active' ? active.events(from, Math.min(to, now)) : (base.events ?? []).filter(row => Date.parse(row.at) >= from && Date.parse(row.at) < to)
  const cursor = Number(q.get('cursor') ?? 0) || 0
  if (q.has('at')) {
    const at = nanos(q.get('at'))
    if (at === null) return error(400, 'InvalidQuery', 'one RFC3339Nano event timestamp is required')
    const ms = Number(at / 1_000_000n), all = rows(ms - 1, ms + 1).filter(row => nanos(row.at) === at), page = all.slice(cursor, cursor + 100)
    return [200, { events: page, ...(cursor + 100 < all.length ? { nextCursor: String(cursor + 100) } : {}), coverage: base.coverage }]
  }
  const from = Date.parse(q.get('from') ?? ''), to = Date.parse(q.get('to') ?? '')
  if (!Number.isFinite(from) || !Number.isFinite(to) || from >= to) return error(400, 'InvalidQuery', 'positive event interval is required')
  const fromN = nanos(q.get('from')), toN = nanos(q.get('to'))
  const all = rows(from, to + 1).filter(row => nanos(row.at) >= fromN && nanos(row.at) < toN), limit = Number(q.get('limit') ?? 100) || 100
  const page = all.slice(cursor, cursor + limit)
  return [200, { events: page, from: q.get('from'), to: q.get('to'), ...(cursor + limit < all.length ? { nextCursor: String(cursor + limit) } : {}),
    coverage: { from: q.get('from'), to: q.get('to'), sources: [{ from: q.get('from'), to: q.get('to'), stream: 'cluster', source: 'local' }, { from: q.get('from'), to: q.get('to'), stream: 'scheduler', source: 'local' }], gaps: [] } }]
}

// GET model-compatibility from the captured matrix; an unknown combination has the Unknown verdict.
let matrix
function compatibility(q, now) {
  matrix ??= parsed('v1/model-compatibility-matrix')
  const keys = ['modelSpec', 'executionPath', 'deviceClass', 'node']
  if (keys.some(key => q.getAll(key).length !== 1 || !q.get(key)) || [...q.keys()].length !== 4) return error(400, 'InvalidQuery', 'InvalidQuery')
  const selection = Object.fromEntries(keys.map(key => [key, q.get(key)]))
  const found = [...matrix.nodeRows, ...matrix.rows].find(row => keys.every(key => row.selection[key] === selection[key]))
  if (found) return [200, found]
  const inventory = parsed('v1/inventory', '?limit=200&offset=0'), cls = inventory.classes.find(row => row.name === selection.deviceClass)
  const model = inventory.models.find(row => row.name === selection.modelSpec), node = cls?.nodes.find(row => row.name === selection.node)
  const object = (row, name) => ({ name, uid: row?.uid ?? '', resourceVersion: row?.resourceVersion ?? '' })
  return [200, { readAt: iso(now), selection, inputs: { modelSpec: object(model, selection.modelSpec), deviceClass: object(cls, selection.deviceClass), node: object(node, selection.node),
    runtimeDeclaration: matrix.rows[0]?.inputs.runtimeDeclaration ?? null }, verdict: 'Unknown',
  reasons: [{ kind: 'Unknown', code: !model ? 'ModelSpecMissing' : !cls ? 'DeviceClassMissing' : !node ? 'NodeMissing' : 'ExecutionPathMissing', message: '선택한 모델, 장치 종류, 실행 경로와 Node의 지원 입력을 확인할 수 없습니다' }], diagnostics: [] }]
}

function get(scene, path, url, now) {
  const q = url.searchParams, act = scene === 'active'
  if (path === 'v1/scheduler/settings') return [200, state[scene].settings, { ETag: `"${state[scene].etag}"` }]
  const kind = /^v1\/(workloads|models|preprocessors|datasets)(?:\/([^/]+))?$/.exec(path)
  if (kind) {
    const store = catalog(scene, kind[1]), project = item => kind[1] === 'workloads' && act ? active.workloadView(item, now) : item
    if (kind[2]) { const item = store.items.get(decodeURIComponent(kind[2])); return item ? [200, project(item)] : error(404, 'NotFound', `${kind[1]} ${kind[2]} not found`) }
    const items = [...store.items.values()].map(project)
    return [200, kind[1] === 'workloads' ? (q.get('view') === 'summary' ? items.map(summary) : items) : { ...(store.meta ? { metadata: store.meta } : {}), items }]
  }
  if (path === 'v1/observations/scheduler') {
    const history = state[scene].history.map(row => ({ at: iso(row.at), settings: row.change }))
    // Synthetic records never lie in the future: a window reaching past now is cut at now.
    const nowN = BigInt(now) * 1_000_000n
    const records = act ? (fromN, toN) => active.journal(fromN, toN < nowN ? toN : nowN, autonomous(scene), state[scene].settings, state[scene].history)
      : () => [...capturedRecords, ...history.map(row => ({ at: row.at, atN: nanos(row.at), kind: 'settings', summary: { at: row.at, settings: { actorUsername: row.settings.actorUsername, actorUID: row.settings.actorUID, appliedAt: row.settings.appliedAt } }, compact: () => row, full: () => row }))]
        .sort((a, b) => a.atN < b.atN ? -1 : a.atN > b.atN ? 1 : 0)
    const result = queryJournal(records, q, now)
    return [result.status, result.body]
  }
  if (path === 'v1/observations/events') return events(scene, q, now)
  if (path.startsWith('v1/observations/workloads/')) {
    if (q.has('order') && !['asc', 'desc'].includes(q.get('order'))) return error(400, 'InvalidQuery', 'invalid observation query')
    const base = parsed(path, url.search)
    if (!base) return undefined
    const result = act ? active.observations(base, q, now, state[scene].inferRows) : undefined
    if (result) return [result.status, result.body]
    const { nextCursor: _cursor, ...rest } = base
    return [200, { ...rest, ...(q.has('from') ? { from: q.get('from'), to: q.get('to') } : {}), requests: [], total: 0, selected: 0, outcomes: {} }]
  }
  if (path === 'v1/observations/power') { if (!q.has('from') || !q.has('to')) return error(400, 'InvalidQuery', 'from and to are required'); if (act) { const r = active.power(q, now); return [r.status, r.body] } return capturedPower(q) }
  if (path === 'v1/observations/load') {
    const span = window(q, now, 60_000)
    return act ? [200, active.load(parsed(path, url.search), span.from, span.to)] : [200, { ...parsed(path, url.search), from: iso(span.from), to: iso(span.to), durationNanos: (span.to - span.from) * 1e6 }]
  }
  if (path === 'v1/model-compatibility') return compatibility(q, now)
  // Paged lists.
  if (path === 'v1/experiments') {
    const known = capture.known(path, url.search), { offset, limit } = paging(q, 25, 50)
    const items = known.items.map(item => act ? active.experiment(item) : item), slice = pageOf(items, offset, limit)
    return [200, { ...known.body, items: slice.items, page: { ...slice.page, total: known.complete ? known.body.page.total : items.length } }]
  }
  const experiment = /^v1\/experiments\/([^/]+)$/.exec(path)
  if (experiment) {
    const body = parsed(path) ?? capture.known('v1/experiments').items.find(item => item.id === decodeURIComponent(experiment[1]))
    return body ? [200, act ? active.experiment(body) : body] : error(404, 'NotFound', `experiment ${experiment[1]} not found`)
  }
  if (path === 'v1/evaluations/inputs') {
    const known = capture.known(path, url.search), { offset, limit } = paging(q, 25, 50)
    const items = (act ? active.evaluationInputs() : known?.items ?? []).filter(item => (!q.get('kind') || item.kind === q.get('kind')) && (!q.get('set') || item.set === q.get('set')))
    const slice = pageOf(items, offset, limit)
    return [200, { items: slice.items, page: slice.page }]
  }
  if (path === 'v1/systems') { const known = capture.known(path), { offset, limit } = paging(q, 50, 200), slice = pageOf(known.items, offset, limit); return [200, { ...known.body, systems: slice.items, page: slice.page }] }
  if (path === 'v1/inventory') {
    const body = parsed(path, '?limit=200&offset=0'), { offset, limit } = paging(q, 50, 200), classes = pageOf(body.classes, offset, limit), models = pageOf(body.models, offset, limit)
    return [200, { ...body, classes: classes.items, models: models.items, classPage: classes.page, modelPage: models.page }]
  }
  if (path === `${NS}batch-energy-optimizations`) {
    const { apiVersion, kind, metadata } = parsed(path, '?limit=50&offset=0')
    return requests.list(scene, state[scene].beo, q, now, { apiVersion, kind, metadata })
  }
  if (path === `${NS}profiling-runs`) return requests.runs(scene, state[scene].beo, q, now)
  const named = /^(batch-energy-optimizations|profiling-runs)\/([^/]+)\/(result|evidence-passport)$/.exec(path.startsWith(NS) ? path.slice(NS.length) : '')
  if (named?.[1] === 'batch-energy-optimizations' && named[3] === 'result') return requests.result(scene, state[scene].beo, decodeURIComponent(named[2]), now)
  if (named?.[1] === 'profiling-runs' && named[3] === 'evidence-passport') return requests.passport(scene, state[scene].beo, decodeURIComponent(named[2]), now)
  if (path === 'v1/scheduler/profiles') {
    const base = parsed(path, '?view=summary&summary=v2&limit=50&offset=0')
    if (act) return [200, measure.profiles(base, q)]
    const { offset, limit } = paging(q, 25, 50)
    return [200, { ...base, profiles: [], page: { offset, limit, total: 0 } }]
  }
  // Evaluations and load scenarios.
  if (path === 'v1/evaluations') return [200, { items: evaluations(scene, now) }]
  if (path === 'v1/evaluations/load-scenarios') return [200, { items: [...loadScenarios(scene).values()] }]
  const loadScenario = /^v1\/evaluations\/load-scenarios\/([^/]+)$/.exec(path)
  if (loadScenario && loadScenario[1] !== 'preflight') { const item = loadScenarios(scene).get(decodeURIComponent(loadScenario[1])); return item ? [200, item] : error(404, 'NotFound', `load scenario ${loadScenario[1]} not found`) }
  const evaluation = /^v1\/evaluations\/([^/]+)$/.exec(path)
  if (evaluation && !['active', 'template', 'inputs', 'load-scenarios'].includes(evaluation[1])) {
    const item = evaluations(scene, now).find(view => view.name === decodeURIComponent(evaluation[1]))
    return item ? [200, item] : error(404, 'NotFound', `evaluation ${evaluation[1]} not found`)
  }
  // Occupancy and operation-cost measurements.
  if (path === `${NS}dispatch-occupancy-measurements`) return [200, { items: domItems(scene, now) }]
  const dom = /^dispatch-occupancy-measurements\/([^/]+)$/.exec(path.startsWith(NS) ? path.slice(NS.length) : '')
  if (dom) { const item = domItems(scene, now).find(row => row.metadata.name === decodeURIComponent(dom[1])); return item ? [200, item] : error(404, 'NotFound', `dispatch-occupancy-measurement ${dom[1]} not found`) }
  // Active-scene projections.
  if (act) {
    if (path === 'v1/pools') return [200, active.pools(parsed(path), now)]
    if (path === 'v1/scheduler/state' && q.get('view') === 'summary') return [200, active.schedulerSummary(parsed(path, url.search), now, autonomous(scene))]
    if (path === 'v1/evaluations/active') return [200, active.activeEvaluations(now)]
    if (path === `${NS}measurement-coverage`) return [200, measure.coverage(parsed(path, url.search), now)]
  }
  const raw = capture.match(path, url.search)
  if (raw) {
    let body = capture.text(raw)
    if (raw.status === 200 && q.has('from') && q.has('to') && /^v1\/observations\//.test(path)) { const value = parseSchedulerJSON(body); if (typeof value?.from === 'string') body = { ...value, from: q.get('from'), to: q.get('to') } }
    return [raw.status, body, raw.etag ? { ETag: raw.etag } : {}]
  }
  // A single object not captured on its own is answered from its captured list.
  const parent = path.slice(0, path.lastIndexOf('/')), name = decodeURIComponent(path.slice(path.lastIndexOf('/') + 1)), list = capture.match(parent, '') && parsed(parent)
  const items = list && (Array.isArray(list) ? list : list.items ?? list.runs)
  const item = items?.find(row => (row.metadata?.name ?? row.name ?? row.id) === name)
  return item ? [200, item] : undefined
}

// Real-shape answers for every mutation the dashboard relay allows.
async function mutate(scene, req, path, url) {
  const q = url.searchParams, own = state[scene], now = Date.now(), act = scene === 'active'
  const infer = /^v1\/workloads\/([^/]+)\/infer$/.exec(path)
  if (infer && req.method === 'POST') {
    req.resume()
    const name = decodeURIComponent(infer[1]), view = catalog(scene, 'workloads').items.get(name), id = randomUUID()
    if (!view) return error(404, 'WorkloadNotFound', `workload ${name} not found`)
    await new Promise(resolve => setTimeout(resolve, 400))
    const result = act ? active.infer(name, now, Date.now()) : undefined
    if (!result) return [503, { code: 'MissingEvidence', message: 'new requests are stopped: MissingEvidence' }, { 'X-Chill-Request-ID': id, 'X-Chill-Workload-UID': view.desired.metadata.uid }]
    own.inferRows.push(result.row)
    if (own.inferRows.length > 500) own.inferRows.shift()
    return [200, result.body, { 'X-Chill-Request-ID': result.row.requestID, 'X-Chill-Workload-UID': result.uid, 'X-Chill-Request-Lifetime': result.lifetime }]
  }
  const body = await readBody(req)
  if (path === 'v1/scheduler/settings' && req.method === 'PUT') {
    if (!req.headers['if-match']) return error(428, 'SettingsPreconditionRequired', 'GET settings and supply its ETag in If-Match')
    if (version(req) !== String(own.etag)) return error(409, 'SettingsConflict', 'Scheduler settings changed; reload and retry')
    if (!body || typeof body !== 'object' || Array.isArray(body)) return error(400, 'InvalidBody', 'Settings must be a JSON object')
    const before = own.settings
    own.settings = { ...before, ...body }
    own.etag++
    const change = { actorUsername: 'replay-operator', actorUID: 'replay-operator-uid', before, after: own.settings, validatedAt: iso(now), appliedAt: iso(now), affected: [] }
    own.history.push({ at: now, change })
    return [200, change, { ETag: `"${own.etag}"` }]
  }
  if (path === 'v1/scheduler/operations' && req.method === 'POST') {
    if (!body?.kind || !body?.node?.name) return error(400, 'InvalidBody', 'manual operation kind and node are required')
    const node = { name: body.node.name, uid: body.node.uid ?? '' }, empty = at => ({ at: iso(at), node, powerMode: '', instances: [], native: {}, pods: [], states: [] })
    return [200, { operation: { id: `manual/${body.kind}/${node.name}/${iso(now)}`, kind: body.kind, node, ...(body.instance ? { instance: body.instance } : {}), policyInstances: [], workloadUIDs: body.workloadUIDs ?? (body.workloadUID ? [body.workloadUID] : []),
      ...(body.workloadUID ? { workloadUID: body.workloadUID } : {}), reason: body.reason ?? '', actorUsername: 'replay-operator', actorUID: 'replay-operator-uid', expectedSeconds: null, expectedSource: null },
    before: empty(now), actual: empty(now + 1200), startedAt: iso(now), endedAt: iso(now + 1200), status: 'complete', progress: [`${body.kind} requested`, `${body.kind} complete`] }]
  }
  // Catalog kinds.
  const kind = /^v1\/(workloads|models|preprocessors|datasets)(?:\/([^/]+))?$/.exec(path)
  if (kind) {
    const store = catalog(scene, kind[1]), stamp = iso(now)
    const meta = item => (item.desired ?? item).metadata
    if (req.method === 'POST' && !kind[2]) {
      const name = body?.name
      if (!dns(name)) return error(400, 'InvalidInput', 'name is required')
      if (store.items.has(name)) return error(409, 'AlreadyExists', `${kind[1]} ${name} already exists`)
      const object = { kind: KINDS[kind[1]], apiVersion: 'edge.dacs.io/v1alpha1', metadata: { name, uid: randomUUID(), resourceVersion: String(now), generation: 1, creationTimestamp: seconds(now) }, spec: body.spec ?? {} }
      const item = kind[1] === 'workloads' ? { desired: object, supportedInputs: [], admission: false, readiness: 'Waiting', reason: 'AppliedDefinitionUnavailable', pending: 0, assigned: 0, observedAt: stamp } : object
      store.items.set(name, item)
      return [201, item]
    }
    const name = kind[2] && decodeURIComponent(kind[2]), item = name && store.items.get(name)
    if (!item) return error(404, 'NotFound', `${kind[1]} ${name} not found`)
    if (!version(req) || version(req) === '*') return error(400, 'ResourceVersionRequired', 'If-Match must contain the queried resourceVersion')
    if (version(req) !== meta(item).resourceVersion) return error(409, 'ResourceVersionConflict', `${kind[1]} ${name} changed; reload and retry`)
    if (req.method === 'PUT') {
      const object = { ...(item.desired ?? item), metadata: { ...meta(item), resourceVersion: String(now), generation: (meta(item).generation ?? 1) + 1 }, spec: body?.spec ?? (item.desired ?? item).spec }
      const next = item.desired ? { ...item, desired: object, observedAt: stamp } : object
      store.items.set(name, next)
      return [200, next]
    }
    if (req.method === 'DELETE') { store.items.delete(name); return kind[1] === 'workloads' ? [202, item] : [204] }
  }
  // Evaluations.
  if (path === 'v1/evaluations/preflight' && req.method === 'POST') {
    const spec = body?.spec ?? {}, captured = parsed('v1/evaluations').items.find(item => item.liveTarget?.length)?.liveTarget ?? []
    const checks = ['spec', 'name', 'evidenceStore', ...Object.keys(spec.settingsPatch ?? {}).map(id => `settingsPatch/${id}`),
      ...(spec.stages ?? []).flatMap(stage => [...(stage.loadScenarioRef ? [`loadScenario/${stage.id}`] : []), `coverage/${stage.id}`]), 'pool', 'profiling'].map(id => ({ id, passed: true, reason: '' }))
    return [200, { checks, liveTarget: (spec.target?.components ?? []).map(component => ({ component: component.name, resource: component.resource,
      liveImageIDs: captured.find(row => row.component === component.name)?.liveImageIDs ?? [], ready: true })) }]
  }
  const release = /^v1\/evaluations\/([^/]+)\/release$/.exec(path)
  if (release && req.method === 'POST') {
    const name = decodeURIComponent(release[1]), view = evaluations(scene, now).find(row => row.name === name)
    if (!view) return error(404, 'NotFound', `evaluation ${name} not found`)
    if (view.uid !== body?.uid || view.phase !== 'Blocked') return error(409, 'ResourceVersionConflict', view.uid !== body?.uid ? 'EvaluationRun UID changed' : 'release requires a finished, blocked evaluation')
    if (!body.reason) return error(400, 'InvalidInput', 'release reason required')
    if (act && name === active.evaluationNames.blocked) own.released = { at: now, reason: body.reason }
    else own.evaluations.set(name, { ...view, phase: 'Done', finishedAt: view.finishedAt ?? seconds(now), conditions: view.conditions.map(c => c.type === 'PoolAcquired' ? { ...c, status: 'False', reason: 'OperatorReleased', lastTransitionTime: seconds(now) } : c) })
    return [202, { name, uid: body.uid }]
  }
  if (/^v1\/evaluations(\/[^/]+)?$/.test(path) && !['v1/evaluations/load-scenarios'].includes(path)) {
    evaluations(scene, now)
    const name = path === 'v1/evaluations' ? body?.name : decodeURIComponent(path.split('/')[2]), store = own.evaluations
    if (req.method === 'POST' && path === 'v1/evaluations') {
      if (!dns(name)) return error(400, 'InvalidInput', 'valid evaluation name required')
      if (store.has(name)) return error(409, 'AlreadyExists', `evaluation ${name} already exists`)
      const uid = randomUUID()
      store.set(name, { name, uid, spec: body.spec ?? {}, conditions: [], procedures: [], stages: [], phase: 'Pending', liveTarget: [] })
      return [202, { name, uid }]
    }
    const view = evaluations(scene, now).find(row => row.name === name)
    if (!view) return error(404, 'NotFound', `evaluation ${name} not found`)
    if (q.get('uid') !== view.uid) return error(409, 'ResourceVersionConflict', 'EvaluationRun UID changed')
    if (req.method === 'PUT') {
      if (view.phase !== 'Pending') return error(409, 'ResourceVersionConflict', 'only a pending evaluation can be replaced')
      store.set(name, { ...view, spec: body?.spec ?? view.spec })
      return [202, { name, uid: view.uid }]
    }
    if (req.method === 'DELETE') {
      if (store.get(name) === 'running') own.stopRequested = { at: seconds(now), by: 'replay-operator' }
      else store.delete(name)
      return [202, { name, uid: view.uid }]
    }
  }
  // Load scenarios.
  if (path === 'v1/evaluations/load-scenarios/preflight' && req.method === 'POST') {
    const workloads = catalog(scene, 'workloads').items, refs = body?.spec?.workloads ?? []
    const checks = [{ id: 'name', passed: dns(body?.name), ...(dns(body?.name) ? {} : { reason: 'valid name required' }) }, { id: 'spec', passed: true },
      ...refs.flatMap(ref => [{ id: `workload:${ref.name}`, passed: workloads.has(ref.name), ...(workloads.has(ref.name) ? {} : { reason: `workloads.edge.dacs.io "${ref.name}" not found` }) }, { id: `dataset:${ref.name}`, passed: true }])]
    return [200, { checks, liveWorkloads: Object.fromEntries(refs.filter(ref => workloads.has(ref.name)).map(ref => { const w = workloads.get(ref.name).desired; return [ref.name, { uid: w.metadata.uid, generation: w.metadata.generation }] })) }]
  }
  if (/^v1\/evaluations\/load-scenarios(\/[^/]+)?$/.test(path)) {
    const store = loadScenarios(scene), name = path.endsWith('load-scenarios') ? body?.name : decodeURIComponent(path.split('/')[3])
    if (req.method === 'POST' && path.endsWith('load-scenarios')) {
      if (!dns(name)) return error(400, 'InvalidInput', 'valid name required')
      if (store.has(name)) return error(409, 'AlreadyExists', `load scenario ${name} already exists`)
      const uid = randomUUID()
      store.set(name, { name, uid, spec: body.spec ?? {}, inUse: [], createdAt: seconds(now) })
      return [200, { name, uid }]
    }
    const item = store.get(name)
    if (!item) return error(404, 'NotFound', `load scenario ${name} not found`)
    if (q.get('uid') !== item.uid) return error(409, 'ResourceVersionConflict', 'LoadScenario UID changed')
    if (item.inUse?.length) return error(409, 'ResourceVersionConflict', `load scenario is in use by ${item.inUse.join(', ')}`)
    if (req.method === 'PUT') { store.set(name, { ...item, spec: body?.spec ?? item.spec }); return [200, { name, uid: item.uid }] }
    if (req.method === 'DELETE') { store.delete(name); return [200, { name, uid: item.uid }] }
  }
  // Batch energy optimizations.
  if (path === `${NS}batch-energy-optimizations/preflight` && req.method === 'POST') {
    if (body?.kind && body.kind !== 'BatchEnergyOptimization') return error(400, 'BatchEnergyOptimizationPreflightRequestInvalid', 'BatchEnergyOptimizationPreflightRequestInvalid')
    return [200, measure.preflight(body, q.get('variantId'), now)]
  }
  if (path === `${NS}batch-energy-optimizations` && req.method === 'POST') {
    if (q.has('dryRun') && q.get('dryRun') !== 'All') return error(400, 'InvalidQuery', 'dryRun must be All when supplied')
    const name = body?.metadata?.name
    if (body?.apiVersion !== 'edge.dacs.io/v1alpha1' || body?.kind !== 'BatchEnergyOptimization' || !dns(name) || (body.metadata.namespace && body.metadata.namespace !== ns)) return error(400, 'InvalidBody', 'native BatchEnergyOptimization identity must match the requested namespace')
    const preflight = measure.preflight(body, null, now)
    if (!preflight.resolves) return error(400, 'MeasurementPreflightRejected', preflight.reason)
    if (requests.item(scene, own.beo, name, now)) return error(409, 'AlreadyExists', `batchenergyoptimizations.edge.dacs.io "${name}" already exists`)
    const task = { apiVersion: body.apiVersion, kind: body.kind, metadata: { ...body.metadata, namespace: ns }, spec: body.spec }
    if (q.get('dryRun') === 'All') return [200, { request: 'NOT_SUBMITTED', task }]
    const created = { ...task, metadata: { ...task.metadata, uid: randomUUID(), resourceVersion: String(now), generation: 1, creationTimestamp: seconds(now) }, status: {} }
    own.beo.added.unshift(created)
    return [202, { request: 'Accepted', task: created }]
  }
  const beo = /^batch-energy-optimizations\/([^/]+)$/.exec(path.startsWith(NS) ? path.slice(NS.length) : '')
  if (beo && req.method === 'DELETE') {
    const name = decodeURIComponent(beo[1]), item = requests.item(scene, own.beo, name, now)
    if (!version(req) || !q.get('uid')) return error(400, 'ResourceVersionRequired', 'native UID and resourceVersion are required')
    if (!item) return error(404, 'NotFound', `batchenergyoptimizations.edge.dacs.io "${name}" not found`)
    if (item.metadata.uid !== q.get('uid') || item.metadata.resourceVersion !== version(req)) return error(409, 'ResourceVersionConflict', 'native UID or resourceVersion changed')
    if (item.status?.phase === 'Measuring') return error(409, 'MeasurementInUse', 'an active measurement cannot be deleted')
    own.beo.removed.add(name); own.beo.added = own.beo.added.filter(row => row.metadata.name !== name)
    return [202, { request: 'DeletionRequested', task: item }]
  }
  // Dispatch occupancy measurements.
  if (path === `${NS}dispatch-occupancy-measurements` && req.method === 'POST') {
    if (q.has('dryRun') && q.get('dryRun') !== 'All') return error(400, 'InvalidQuery', 'dryRun must be All when supplied')
    if ((body?.name && !dns(body.name)) || !dns(body?.node) || !dns(body?.batchEnergyOptimization) || !dns(body?.workload) || !(body?.batch >= 1)) return error(400, 'InvalidBody', 'valid node, BEO, Workload and positive batch are required')
    if (!act) return error(503, 'OccupancyPolicyUnavailable', 'dispatchOccupancy block is required')
    const profile = measure.beoProfile(body.batchEnergyOptimization)
    if (!profile || profile.node !== body.node) return error(400, 'OccupancyInfeasible', profile ? 'BEO must be Complete on the selected Node' : 'BEO has no current accepted profile')
    if (!profile.batches.includes(body.batch)) return error(400, 'OccupancyInfeasible', 'selected batch has no measured service population')
    const workload = catalog(scene, 'workloads').items.get(body.workload)
    if (!workload) return error(404, 'NotFound', `workloads.edge.dacs.io "${body.workload}" not found`)
    const policy = { aggregationWindowSeconds: 120, refreshIntervalSeconds: 60, scanTimeoutSeconds: 30, maximumRequests: 250000, warmupSeconds: 5, burstSeconds: 15, workers: 8, minimumPairs: 100,
      requestLifetimeSeconds: 8, deliveryMarginMillis: 25, preparationSeconds: 2, jobSetupSeconds: 10, collectionSeconds: 10, plannedElapsedCapSeconds: 120, seed: 1 }
    const tail = profile.serviceSeconds(body.batch) * 1.3, outstanding = Math.ceil(policy.workers / body.batch), drain = Math.ceil(policy.requestLifetimeSeconds + tail + 0.025), extension = policy.requestLifetimeSeconds + 1
    const elapsed = policy.preparationSeconds + policy.jobSetupSeconds + policy.collectionSeconds + policy.warmupSeconds + policy.burstSeconds + extension + drain
    if (policy.workers < body.batch) return error(400, 'OccupancyInfeasible', 'invalid occupancy burst, workers or minimum pairs')
    const inputs = { dispatchOccupancy: policy, configuration: { namespace: ns, name: 'chill-scheduler', resourceVersion: String(state[scene].etag), key: 'scheduler.json' }, node: { name: profile.node, uid: profile.nodeUID }, coefficients: policy,
      profile: { profileID: profile.id, runtimeImage: profile.runtimeImage, batch: body.batch, meanBatchSeconds: profile.serviceSeconds(body.batch), maxObservedSeconds: tail,
        workload: { name: body.workload, uid: workload.desired.metadata.uid, generation: workload.desired.metadata.generation } },
      computed: { tailBatchSeconds: tail, minimumRequestLifetimeSeconds: Math.max(1, Math.ceil(outstanding * tail + 0.025)), queueRequestCap: 256, queueBytesCap: 67108864, supplyExtensionSeconds: extension } }
    const spec = { node: body.node, batchEnergyOptimization: body.batchEnergyOptimization, workload: body.workload, batch: body.batch, warmupSeconds: policy.warmupSeconds, durationSeconds: policy.burstSeconds,
      minimumPairs: policy.minimumPairs, workers: policy.workers, supplyMode: 'closed-loop', seed: policy.seed, requestLifetimeSeconds: policy.requestLifetimeSeconds, deliveryMarginMillis: policy.deliveryMarginMillis,
      drainSeconds: drain, preparationSeconds: policy.preparationSeconds, jobSetupSeconds: policy.jobSetupSeconds, collectionSeconds: policy.collectionSeconds, plannedElapsedSeconds: elapsed,
      conditions: { profileID: profile.id, inputsJSON: JSON.stringify(inputs), waitingBatches: outstanding - 1 } }
    const name = body.name || `occ-${body.node.slice(0, 40)}-${body.batchEnergyOptimization.slice(0, 40)}-b${body.batch}-${hash(JSON.stringify(spec)).slice(0, 10)}`
    const task = { apiVersion: 'edge.dacs.io/v1alpha1', kind: 'DispatchOccupancyMeasurement', metadata: { namespace: ns, name }, spec }
    if (q.get('dryRun') === 'All') return [200, { request: 'NOT_SUBMITTED', task }]
    if (domItems(scene, now).some(item => item.metadata.name === name)) return error(409, 'AlreadyExists', `dispatchoccupancymeasurements.edge.dacs.io "${name}" already exists`)
    const uid = randomUUID(), created = { ...task, metadata: { ...task.metadata, uid, resourceVersion: String(now), generation: 1, creationTimestamp: seconds(now) } }
    own.dom.added.unshift({ kind: 'DispatchOccupancyMeasurement', metadata: { namespace: ns, name, uid, resourceVersion: String(now), creationTimestamp: seconds(now) }, node: body.node,
      conditions: { workload: body.workload, batchEnergyOptimization: body.batchEnergyOptimization, batch: body.batch, supplyMode: 'closed-loop', warmupSeconds: spec.warmupSeconds, durationSeconds: spec.durationSeconds, workers: spec.workers },
      status: {}, references: { cr: { apiVersion: 'edge.dacs.io/v1alpha1', kind: 'DispatchOccupancyMeasurement', namespace: ns, name, uid }, evidence: [] },
      assessment: { status: 'running', displayStatus: 'running', label: '진행', reasons: [], execution: { phase: 'Pending', generation: 1, observedGeneration: 0 }, evidence: { available: false, accepted: false, reasons: [] },
        checklist: { criteriaMet: null, version: 'result-checklist.v1', checks: [] }, scope: { schedulerUsed: null, reused: false, facts: [] } },
      name, uid, batchEnergyOptimization: body.batchEnergyOptimization, workload: body.workload, batch: body.batch, profileID: profile.id, phase: null, reason: '', createdAt: seconds(now),
      measurementWindow: null, d: null, evidence: null, schemaVersion: '', estimate: null, verdict: '', minimumPairs: spec.minimumPairs, saturation: null, idleFraction: null })
    return [202, { request: 'Accepted', task: created }]
  }
  const domDelete = /^dispatch-occupancy-measurements\/([^/]+)$/.exec(path.startsWith(NS) ? path.slice(NS.length) : '')
  if (domDelete && req.method === 'DELETE') {
    const name = decodeURIComponent(domDelete[1]), item = domItems(scene, now).find(row => row.metadata.name === name)
    if (!version(req) || !q.get('uid')) return error(400, 'ResourceVersionRequired', 'native UID and resourceVersion are required')
    if (!item) return error(404, 'NotFound', `dispatchoccupancymeasurements.edge.dacs.io "${name}" not found`)
    if (item.metadata.uid !== q.get('uid') || item.metadata.resourceVersion !== version(req)) return error(409, 'ResourceVersionConflict', 'native UID or resourceVersion changed')
    own.dom.removed.add(name); own.dom.added = own.dom.added.filter(row => row.metadata.name !== name)
    return [202, { request: 'DeletionRequested', task: { apiVersion: 'edge.dacs.io/v1alpha1', kind: 'DispatchOccupancyMeasurement', metadata: item.metadata } }]
  }
  return error(404, 'ReplayMissing', `${req.method} /api/${path}`)
}

// Every GET goes through here: a replay bug is logged and answered 500 ReplayError without its JS message.
function answer(scene, path, url, now) {
  try { return get(scene, path, url, now) ?? [404, { code: 'ReplayMissing', message: `/api/${path}` }] } catch (failure) {
    console.error(`replay: GET /api/${path}${url.search}`, failure)
    return [500, { code: 'ReplayError' }]
  }
}

const upstream = https.createServer({ key, cert }, async (req, res) => {
  const token = tokenOf(req.headers.authorization)
  if (!token) return send(res, 401, { code: 'Unauthenticated', message: 'Bearer token required' })
  const url = new URL(req.url, 'https://replay')
  if (!url.pathname.startsWith('/api/v1/')) return send(res, 404, { code: 'ReplayMissing', message: url.pathname })
  const path = url.pathname.slice('/api/'.length), scene = sceneOf(token)
  if (req.method === 'GET') return send(res, ...answer(scene, path, url, Date.now()))
  try {
    const [status, body, headers] = await mutate(scene, req, path, url)
    mutations.push({ at: iso(Date.now()), scene, method: req.method, path: `${path}${url.search}`, status })
    if (mutations.length > 200) mutations.shift()
    send(res, status, body, headers)
  } catch (failure) {
    if (!failure.status) console.error(`replay: ${req.method} /api/${path}${url.search}`, failure)
    send(res, failure.status ?? 500, failure.status ? { code: 'InvalidBody', message: failure.message } : { code: 'ReplayError' })
  }
})

// Self-consistency sweep over both scenes: every list the dashboard reads resolves through every detail path
// it builds, every body decodes with lib/ parsers, no synthetic record lies in the future. Returns failures.
function selfCheck() {
  const failures = [], now = Date.now()
  for (const scene of SCENES) {
    const fail = message => failures.push(`[${scene}] ${message}`)
    const read = (pathQuery, expect = [200]) => {
      const url = new URL(`https://replay/api/${pathQuery}`), [status, body] = answer(scene, url.pathname.slice('/api/'.length), url, now)
      let value
      try { value = parseMeasurementJSON(typeof body === 'string' ? body : stringifySchedulerJSON(body)) } catch (error) { fail(`${pathQuery}: body does not decode (${error.message})`) }
      if (!expect.includes(status)) fail(`${pathQuery}: status ${status}${value?.code ? ` ${value.code}` : ''}`)
      return { status, body: value }
    }
    const future = (what, at) => { if (Date.parse(at) > now + 1000) fail(`${what}: ${at} is in the future`) }
    const enc = encodeURIComponent
    // Requests: list (full and paged), /result, child runs; failure-summary runs exist and failed.
    const list = read(`${NS}batch-energy-optimizations?view=summary&summary=v2`).body?.items ?? []
    const pagedNames = []
    for (let offset = 0; offset !== undefined;) { const page = read(`${NS}batch-energy-optimizations?view=summary&summary=v2&limit=50&offset=${offset}`).body; pagedNames.push(...(page?.items ?? []).map(row => row.metadata.name)); offset = page?.page?.nextOffset }
    if (pagedNames.join() !== list.map(row => row.metadata.name).join()) fail('batch-energy-optimizations pages differ from the full list')
    const names = new Map(list.map(row => [row.metadata.name, row]))
    const runsOf = uid => read(`${NS}profiling-runs?parentUID=${enc(uid)}&limit=200`).body?.runs ?? []
    for (const item of list) {
      const { name, uid } = item.metadata, st = item.status ?? {}
      future(`request ${name} creationTimestamp`, item.metadata.creationTimestamp)
      for (const c of st.conditions ?? []) future(`request ${name} condition`, c.lastTransitionTime)
      const result = read(`${NS}batch-energy-optimizations/${enc(name)}/result`, [200, 502])
      if (result.body?.campaign?.name !== name || result.body?.campaign?.uid !== uid) fail(`${name}/result: campaign does not name the request`)
      if (st.phase === 'Complete' && result.status === 200 && !result.body.resultRows?.length) fail(`${name}/result: complete without rows`)
      if (scene === 'active' && st.phase === 'Complete' && result.status === 200 && item.assessment?.displayStatus === 'usable' && result.body.assessment?.displayStatus !== 'usable') fail(`${name}/result: assessment differs from the list`)
      const children = runsOf(uid)
      if (!children.length && (st.completedTrials || st.phase === 'Measuring' || item.assessment?.failureSummary?.some(row => row.runName))) fail(`${name}: profiling-runs?parentUID returns no runs`)
      for (const row of item.assessment?.failureSummary ?? []) if (row.runName && !children.some(run => run.name === row.runName && run.assessment.displayStatus === 'problem')) fail(`${name}: failure-summary run ${row.runName} is not a problem child run`)
    }
    // Runs: every run in the list resolves its passport the way the real handler does.
    const runs = []
    for (let offset = 0; offset !== undefined;) { const page = read(`${NS}profiling-runs?limit=200&offset=${offset}`).body; runs.push(...(page?.runs ?? [])); offset = page?.page?.nextOffset }
    for (const run of runs) {
      future(`run ${run.name} creationTimestamp`, run.creationTimestamp)
      for (const c of run.status.conditions ?? []) future(`run ${run.name} condition`, c.lastTransitionTime)
      if (!names.has(run.ownerReferences[0].name)) fail(`run ${run.name}: owner ${run.ownerReferences[0].name} is not listed`)
      const passport = read(`${NS}profiling-runs/${enc(run.name)}/evidence-passport`, [200, 202, 502])
      if (passport.status === 200 && !passport.body?.measurement) fail(`${run.name}/evidence-passport: 200 without measurement`)
      if (passport.status === 202 && passport.body?.code !== 'EvidencePassportPending') fail(`${run.name}/evidence-passport: 202 without EvidencePassportPending`)
      if (passport.status === 502 && passport.body?.run?.name !== run.name) fail(`${run.name}/evidence-passport: 502 without the Run state`)
    }
    const byQ = read(`${NS}profiling-runs?q=${enc('98BAD44F9CED-active')}&limit=200`).body?.runs ?? []
    if (!byQ.length || byQ.some(run => !run.name.includes('98bad44f9ced-active'))) fail('profiling-runs?q does not select by name')
    if (!runs.some(run => run.status.conditions?.some(c => c.type === 'ExecutionComplete' && c.status === 'False'))) fail('no run with a failed ExecutionComplete condition')
    // Coverage: every request a cell names is listed with the same uid.
    const coverage = read(`${NS}measurement-coverage?view=summary&summary=v2`).body
    for (const cell of coverage?.cells ?? []) {
      const ids = [cell.currentMeasurement?.id, ...cell.history.map(row => row.requestId), ...cell.failureSummary.map(row => row.requestId), ...cell.variants.flatMap(v => v.requestIds)].filter(Boolean)
      for (const id of new Set(ids)) { const [, name, uid] = id.split('/'); if (names.get(name)?.metadata.uid !== uid) fail(`coverage ${cell.id}: request ${id} is not listed`) }
      for (const row of cell.failureSummary) if (row.runName && !runs.some(run => run.name === row.runName)) fail(`coverage ${cell.id}: failure run ${row.runName} is not listed`)
    }
    // Profiles, operation costs, occupancy, evaluations, experiments, load scenarios, catalog, workloads.
    const profiles = []
    for (let offset = 0; offset !== undefined;) { const page = read(`v1/scheduler/profiles?limit=50&offset=${offset}`).body; profiles.push(...(page?.profiles ?? [])); offset = page?.page?.nextOffset }
    for (const p of profiles) {
      if (read(`v1/scheduler/profiles?profile=${enc(p.id)}&limit=1`).body?.profiles?.[0]?.id !== p.id) fail(`profile ${p.id}: profile= does not resolve`)
      const [, name, uid] = p.id.split('/'); if (names.get(name)?.metadata.uid !== uid) fail(`profile ${p.id}: request is not listed`)
    }
    const nodes = new Set(read('v1/inventory?limit=200').body?.classes?.flatMap(cls => cls.nodes.map(node => node.name)) ?? [])
    for (const row of read(`${NS}operation-cost-measurements`).body?.items ?? []) read(`${NS}operation-cost-measurements/${enc(row.metadata.name)}`)
    for (const row of read(`${NS}dispatch-occupancy-measurements`).body?.items ?? []) {
      read(`${NS}dispatch-occupancy-measurements/${enc(row.metadata.name)}`)
      if (!nodes.has(row.node)) fail(`occupancy ${row.metadata.name}: node ${row.node} not in inventory`)
    }
    for (const row of read('v1/evaluations').body?.items ?? []) read(`v1/evaluations/${enc(row.name)}`)
    for (const row of read('v1/evaluations/load-scenarios').body?.items ?? []) read(`v1/evaluations/load-scenarios/${enc(row.name)}`)
    for (let offset = 0; offset !== undefined;) { const page = read(`v1/experiments?limit=50&offset=${offset}`).body; for (const row of page?.items ?? []) read(`v1/experiments/${enc(row.id)}`); offset = page?.page?.nextOffset }
    for (const kind of ['models', 'preprocessors', 'datasets']) for (const row of read(`v1/${kind}`).body?.items ?? []) read(`v1/${kind}/${enc(row.metadata.name)}`)
    const span = `from=${enc(new Date(now - 600_000).toISOString())}&to=${enc(new Date(now).toISOString())}`
    for (const view of read('v1/workloads?view=summary').body ?? []) {
      const { name, uid } = view.desired.metadata
      read(`v1/workloads/${enc(name)}`)
      const asc = read(`v1/observations/workloads/${uid}?${span}`).body?.requests ?? [], desc = []
      for (let cursor = ''; cursor !== undefined;) { const page = read(`v1/observations/workloads/${uid}?${span}&order=desc&limit=7${cursor ? `&cursor=${cursor}` : ''}`).body; desc.push(...(page?.requests ?? [])); cursor = page?.nextCursor }
      if (desc.map(row => row.requestID).join() !== asc.map(row => row.requestID).reverse().join()) fail(`observations/workloads/${uid}: order=desc pages are not the asc rows reversed`)
    }
    read(`v1/observations/workloads/${read('v1/workloads?view=summary').body?.[0]?.desired.metadata.uid}?${span}&order=up`, [400])
    // Nodes named by pools, requests and runs exist in the inventory.
    const pools = read('v1/pools').body
    for (const node of [...(pools?.available ?? []), ...(pools?.measurement ?? [])].map(row => row.node.name).concat((pools?.workloads ?? []).flatMap(w => w.nodes.map(n => n.name)))) if (!nodes.has(node)) fail(`pools: node ${node} not in inventory`)
    for (const node of new Set([...list.map(row => row.spec.state.targetNode), ...runs.map(run => run.spec.targetNode)])) if (!nodes.has(node)) fail(`node ${node} is named by requests or runs but not in inventory`)
    // No synthetic decision or event after now, whatever window is asked.
    const ahead = `from=${enc(new Date(now - 120_000).toISOString())}&to=${enc(new Date(now + 86_400_000).toISOString())}`
    for (const row of read(`v1/observations/scheduler?${ahead}&filter=decision&view=summary`).body?.events ?? []) future('scheduler decision', row.at)
    for (const row of read(`v1/observations/events?${ahead}&limit=500`).body?.events ?? []) future('event', row.at)
  }
  return failures
}

// chill-live/1 hub: hello, resume, snapshot, then scene frames with a contiguous per-connection seq.
const wss = new WebSocketServer({ noServer: true, perMessageDeflate: false })
const captureLoop = capture.frames.slice(capture.frames.findIndex(frame => frame.type === 'snapshot') + 1).filter(frame => frame.type !== 'heartbeat')
const loopRestart = captureLoop.findIndex(frame => frame.type === 'metrics')
const at = frame => Date.parse(frame.at)
// Captured frame content keeps the process-wide shift; envelope and metric times are re-stamped to now.
const shifted = frame => parseSchedulerJSON(shiftTimes(JSON.stringify(frame), capture.shift))
function restamp(frame, now) {
  if (frame.type !== 'metrics') return frame
  const bucket = Math.floor(now / 1000) * 1000 - 1000
  return { ...frame, bucketAt: seconds(bucket), nodes: Object.fromEntries(Object.entries(frame.nodes).map(([uid, node]) => [uid, { ...node, observedAt: node.observedAt && iso(bucket + 970) }])) }
}

function serve(socket, scene) {
  let seq = capture.frames[0].seq, loop, heartbeat, started = false, snapshotAt = 0
  const emit = frame => { if (socket.readyState === WebSocket.OPEN) socket.send(stringifySchedulerJSON({ ...frame, seq: ++seq, at: frame.at ?? iso(Date.now()) })) }
  socket.on('close', () => { clearTimeout(loop); clearInterval(heartbeat) })
  const hello = iso(Date.now())
  socket.send(JSON.stringify({ type: 'hello', seq, at: hello, protocol: 'chill-live/1', serverAt: hello, retainFrom: seq + 1 }))
  // Every resume (first or resync) gets a fresh snapshot at the current seq; frames continue from seq + 1.
  socket.on('message', data => {
    let resume
    try { resume = JSON.parse(data.toString()) } catch { /* rejected below */ }
    if (resume?.type !== 'resume' || !Number.isSafeInteger(resume.from) || resume.from < 0) { socket.close(1002); return }
    const t0 = Date.now()
    snapshotAt = t0
    const history = resume.from > 0 ? { history: { reason: 'checkpoint-unmatched', replayed: false, requested: { afterSeq: resume.from, throughSeq: seq, ...(resume.at ? { at: resume.at } : {}) }, retained: { events: null, power: null }, gaps: [] } } : {}
    const { type: _type, seq: _seq, at: _at, ...body } = scene === 'active' ? active.snapshot(t0, autonomous(scene)) : shifted(capture.frames[1])
    socket.send(stringifySchedulerJSON({ type: 'snapshot', seq, at: iso(t0), ...body, ...history }))
    if (started) return
    started = true
    heartbeat = setInterval(() => { const now = iso(Date.now()); emit({ type: 'heartbeat', at: now, serverAt: now }) }, 5000)
    if (scene === 'active') {
      let last = t0
      const tick = () => {
        const now = Date.now()
        for (const row of active.timeline(last, now, autonomous(scene))) emit({ ...row.frame, at: iso(row.at) })
        emit({ type: 'metrics', ...active.metrics(Math.floor(now / 1000) * 1000 - 1000, now - snapshotAt < 8000), at: iso(now) })
        last = now
        loop = setTimeout(tick, 1005 - (Date.now() % 1000))
      }
      loop = setTimeout(tick, 1005 - (Date.now() % 1000))
      return
    }
    // Replay the capture with its own spacing, looping from the first metrics frame.
    const pass = index => {
      const anchor = at(captureLoop[index]), began = Date.now()
      const step = i => {
        if (i >= captureLoop.length) { loop = setTimeout(() => pass(loopRestart), 1000); return }
        const now = Date.now()
        emit({ ...restamp(shifted(captureLoop[i]), now), at: iso(now) })
        loop = setTimeout(() => step(i + 1), Math.max(0, began + at(captureLoop[i + 1] ?? captureLoop[i]) - anchor - Date.now()))
      }
      step(index)
    }
    pass(0)
  })
}

upstream.on('upgrade', (req, socket, head) => {
  const token = tokenOf(req.headers.authorization)
  if (new URL(req.url, 'https://replay').pathname !== '/api/v1/live' || !token) { socket.end('HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n'); return }
  wss.handleUpgrade(req, socket, head, client => {
    client.token = token
    client.scene = sceneOf(token)
    serve(client, client.scene)
  })
})

const front = createFront({ baseURL: `https://127.0.0.1:${upstreamPort}`, ca: cert, nextPort })
const browser = https.createServer({ key, cert }, (req, res) => {
  const url = new URL(req.url, 'https://replay')
  if (url.pathname === '/__replay/status' && req.method === 'GET') {
    const connections = Object.fromEntries(SCENES.map(name => [name, [...wss.clients].filter(client => client.scene === name).length]))
    return send(res, 200, { defaultScene, shiftSeconds: capture.shift / 1000, connections, settingsETag: Object.fromEntries(SCENES.map(name => [name, state[name].etag])), mutations })
  }
  if (url.pathname === '/__replay/scene' && req.method === 'POST') {
    const name = url.searchParams.get('name')
    if (!SCENES.includes(name)) return send(res, 400, { code: 'UnknownScene', message: SCENES.join('|') })
    defaultScene = name
    for (const client of wss.clients) if (!SCENES.includes(client.token)) client.close(1013)
    return send(res, 200, { defaultScene })
  }
  front.emit('request', req, res)
})
browser.on('upgrade', (req, socket, head) => {
  if (req.url === '/api/live') { front.emit('upgrade', req, socket, head); return }
  // Next dev HMR and any other upgrade: raw TCP pipe to the dev server.
  const target = net.connect(nextPort, '127.0.0.1', () => {
    const lines = [`${req.method} ${req.url} HTTP/${req.httpVersion}`]
    for (let i = 0; i < req.rawHeaders.length; i += 2) lines.push(`${req.rawHeaders[i]}: ${req.rawHeaders[i + 1]}`)
    target.write(`${lines.join('\r\n')}\r\n\r\n`)
    if (head.length) target.write(head)
    target.pipe(socket).pipe(target)
  })
  target.on('error', () => socket.destroy())
  socket.on('error', () => target.destroy())
})

// The sweep runs at every start (failures printed); --self-check exits with its verdict instead of serving.
{
  const checkedAt = Date.now(), failures = selfCheck()
  for (const line of failures) console.error(`self-check: ${line}`)
  console.log(`self-check: ${failures.length ? `${failures.length} failure(s)` : 'ok'} in ${Date.now() - checkedAt} ms`)
  if (args['self-check']) { rmSync(directory, { recursive: true, force: true }); process.exit(failures.length ? 1 : 0) }
}
upstream.listen(upstreamPort, '127.0.0.1'); await once(upstream, 'listening')
browser.listen(port, '127.0.0.1'); await once(browser, 'listening')
const root = fileURLToPath(new URL('..', import.meta.url))
const next = args['no-next'] ? null : spawn(join(root, 'node_modules/.bin/next'), ['dev', '--hostname', '127.0.0.1', '--port', String(nextPort)], {
  cwd: root, stdio: 'inherit', detached: true,
  env: { ...process.env, CHILL_BASE_URL: `https://127.0.0.1:${upstreamPort}`, NODE_EXTRA_CA_CERTS: join(directory, 'cert.pem'), CHILL_NAMESPACE: ns, CHILL_SYSTEM_NAME: 'chill', CHILL_POLL_INTERVAL_MS: '5000' },
})
console.log(`replay ready https://localhost:${port} scene=${defaultScene}`)

let stopping = false
function stop() {
  if (stopping) return
  stopping = true
  for (const client of wss.clients) client.terminate()
  front.shutdown(); browser.close(); upstream.close()
  browser.closeAllConnections(); upstream.closeAllConnections()
  rmSync(directory, { recursive: true, force: true })
  if (!next || next.exitCode !== null) process.exit(0)
  try { process.kill(-next.pid, 'SIGTERM') } catch { process.exit(0) }
  next.on('exit', () => process.exit(0))
  setTimeout(() => { try { process.kill(-next.pid, 'SIGKILL') } catch { /* gone */ } process.exit(0) }, 5000).unref()
}
process.on('SIGINT', stop)
process.on('SIGTERM', stop)
next?.on('exit', code => { if (!stopping) { console.error(`next dev exited (${code})`); stop() } })

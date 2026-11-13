// Local replay of a captured ChillServer for UI work. Never imported by the application or image entrypoint.
// node --experimental-strip-types server/replay.mjs --capture <dir> [--port 3600] [--scene captured|active] [--no-next]
import https from 'node:https'
import net from 'node:net'
import { once } from 'node:events'
import { execFileSync, spawn } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseArgs } from 'node:util'
import { WebSocketServer, WebSocket } from 'ws'
import { createFront } from './live-proxy.mjs'
import { loadCapture, shiftTimes } from './replay-capture.mjs'
import { activeScene } from './replay-scene.mjs'
import { activeMeasurements } from './replay-measure.mjs'
import { parseSchedulerJSON, stringifySchedulerJSON } from '../lib/scheduler.ts'
import { parseMeasurementJSON } from '../lib/measurement-summary.ts'

const SCENES = ['captured', 'active']
const { values: args } = parseArgs({ options: { capture: { type: 'string' }, port: { type: 'string', default: '3600' }, scene: { type: 'string', default: 'captured' }, 'no-next': { type: 'boolean', default: false } } })
if (!args.capture || !SCENES.includes(args.scene)) { console.error('usage: replay.mjs --capture <dir> [--port 3600] [--scene captured|active] [--no-next]'); process.exit(2) }
const port = Number(args.port), upstreamPort = port + 1, nextPort = port + 2
const capture = loadCapture(args.capture)
const startedAt = Date.now()
// The prepare action of the first cycle started 40 s before the server did.
const measure = activeMeasurements(capture, startedAt - 40_480)
const active = activeScene(capture, startedAt - 40_480, measure.profileFor)
let defaultScene = args.scene
const mutations = []

const directory = mkdtempSync(join(tmpdir(), 'chill-replay-tls-'))
execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', join(directory, 'key.pem'), '-out', join(directory, 'cert.pem'), '-days', '7', '-subj', '/CN=localhost', '-addext', 'subjectAltName=IP:127.0.0.1,DNS:localhost'], { stdio: 'ignore' })
const cert = readFileSync(join(directory, 'cert.pem')), key = readFileSync(join(directory, 'key.pem'))

// Mutable per-scene state: scheduler settings and lazily copied catalog lists.
const KINDS = { workloads: 'Workload', models: 'ModelSpec', preprocessors: 'Preprocessor', datasets: 'Dataset' }
const capturedSettings = capture.json('v1/scheduler/settings')
const capturedETag = Number(JSON.parse(capture.match('v1/scheduler/settings').etag))
const state = Object.fromEntries(SCENES.map(name => {
  const settings = name === 'active' ? active.settings(capturedSettings) : capturedSettings
  const history = name === 'active' ? [active.settingsChange(capturedSettings, settings)] : []
  return [name, { settings, etag: capturedETag, history, catalog: {} }]
}))

const tokenOf = header => /^Bearer (\S+)$/.exec(header ?? '')?.[1]
const sceneOf = token => SCENES.includes(token) ? token : defaultScene
const autonomous = scene => state[scene].settings.autonomousWorkloads ?? []
const iso = ms => new Date(ms).toISOString()

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

// Captured body for path?query, time-shifted to now; null when the capture lacks it.
function captured(path, search) {
  const entry = capture.match(path, search)
  return entry && { entry, text: shiftTimes(capture.text(entry), capture.shiftMs()) }
}
// parseMeasurementJSON also expands measurement-summary readProjection bodies; other bodies pass through.
const parsed = (path, search) => { const hit = captured(path, search); return hit && parseMeasurementJSON(hit.text) }

function catalog(scene, kind) {
  const store = state[scene].catalog
  if (!store[kind]) {
    const list = parsed(`v1/${kind}`, '')
    store[kind] = { meta: Array.isArray(list) ? null : list.metadata, items: new Map((Array.isArray(list) ? list : list.items).map(item => [(item.desired ?? item).metadata.name, item])) }
  }
  return store[kind]
}
const summary = view => ({ ...view, supportedInputs: view.supportedInputs.map(input => ({ executionPath: input.executionPath, artifact: input.artifact, profiles: input.profiles.map(profile => ({ id: profile.id })) })) })
// decision-change = a decision with a target that is not the keep plan. Summary rows carry no target,
// so the captured compact/full decision reads supply it by capture-time instant; unknown rows are not changes.
let capturedTargets
function changedDecision(decision, at) {
  capturedTargets ??= new Map(['?filter=decision&view=compact', '?filter=decision&limit=3'].flatMap(search => capture.json('v1/observations/scheduler', search)?.events ?? [])
    .filter(row => row.decision && 'target' in row.decision).map(row => [Date.parse(row.decision.at), row.decision]))
  const full = decision && 'target' in decision ? decision : capturedTargets.get(Date.parse(at) - capture.shiftMs())
  return !!full?.target && !full.target.keep && full.target.id !== full.keep?.id
}

// Overlays shared by both scenes plus the active-scene REST projection. Returns undefined to fall through.
function overlay(scene, path, url, now) {
  const q = url.searchParams, from = Date.parse(q.get('from') ?? ''), to = Date.parse(q.get('to') ?? '')
  const windowed = Number.isFinite(from) && Number.isFinite(to)
  const kind = /^v1\/(workloads|models|preprocessors|datasets)(?:\/([^/]+))?$/.exec(path)
  if (kind && state[scene].catalog[kind[1]]) {
    const store = state[scene].catalog[kind[1]], project = item => kind[1] === 'workloads' && scene === 'active' ? active.workloadView(item, now) : item
    if (kind[2]) { const item = store.items.get(decodeURIComponent(kind[2])); return item ? [200, project(item)] : [404, { code: 'NotFound', message: `${kind[1]} ${kind[2]} not found` }] }
    const items = [...store.items.values()].map(project)
    return [200, kind[1] === 'workloads' ? (q.get('view') === 'summary' ? items.map(summary) : items) : { ...(store.meta ? { metadata: store.meta } : {}), items }]
  }
  if (path === 'v1/scheduler/settings') return [200, state[scene].settings, { ETag: `"${state[scene].etag}"` }]
  if (path === 'v1/observations/scheduler' && q.get('filter') === 'decision-change') {
    const search = new URLSearchParams(q); search.set('filter', 'decision')
    const base = parsed(path, `?${search}`) ?? { events: [] }, summaryView = q.get('view') === 'summary'
    const events = scene === 'active' ? active.decisionChanges(windowed ? from : now - 3_600_000, windowed ? to : now, autonomous(scene), summaryView) : base.events.filter(row => changedDecision(row.decision, row.at))
    const { nextCursor: _cursor, profiles: _profiles, ...rest } = base
    return [200, { ...rest, events: events.slice(0, Number(q.get('limit')) || events.length) }]
  }
  if (scene === 'active' && path === 'v1/observations/scheduler' && q.get('filter') === 'settings') {
    const base = parsed(path, url.search) ?? {}, compact = q.get('view') === 'summary'
    const events = state.active.history.filter(row => (!windowed || (row.at >= from && row.at < to)))
      .map((row, index) => ({ at: iso(row.at), offset: 100 + index, settings: compact ? { actorUsername: row.change.actorUsername, actorUID: row.change.actorUID, appliedAt: row.change.appliedAt } : row.change }))
    if (q.get('order') !== 'asc') events.reverse()
    return [200, { ...base, events }]
  }
  if (path === 'v1/observations/events') {
    const base = parsed(path, url.search) ?? {}, limit = Number(q.get('limit')) || 100
    const lo = windowed ? from : now - 900_000, hi = windowed ? to : now
    const events = scene === 'active' ? active.events(lo, hi) : (base.events ?? []).filter(row => Date.parse(row.at) >= lo && Date.parse(row.at) < hi)
    const { nextCursor: _cursor, ...rest } = base
    return [200, { ...rest, events: events.slice(-limit) }]
  }
  if (scene !== 'active') return undefined
  if (path === 'v1/workloads') { const views = parsed(path, url.search).map(view => active.workloadView(view, now)); return [200, views] }
  if (kind?.[1] === 'workloads' && kind[2]) { const view = parsed(path, ''); return view && [200, active.workloadView(view, now)] }
  if (path === 'v1/pools') return [200, active.pools(parsed(path, ''), now)]
  if (path === 'v1/scheduler/state' && q.get('view') === 'summary') return [200, active.schedulerSummary(parsed(path, url.search), now, autonomous(scene))]
  if (path.startsWith('v1/observations/workloads/')) {
    const base = parsed(path, url.search)
    return base && [200, active.observations(base, now, windowed ? from : now - 3_600_000, windowed ? to : now, Number(q.get('limit')) || 50)]
  }
  if (path === 'v1/observations/load') return [200, active.load(parsed(path, url.search), windowed ? from : now - 60_000, windowed ? to : now)]
  if (path === 'v1/evaluations/active') return [200, active.activeEvaluations(now)]
  const ns = `v1/namespaces/${capture.meta.namespace}/`
  if (path === `${ns}measurement-coverage`) return [200, measure.coverage(parsed(path, url.search), now)]
  if (path === 'v1/scheduler/profiles') return [200, measure.profiles(parsed(path, '?view=summary&summary=v2&limit=50&offset=0'), q)]
  if (path === `${ns}batch-energy-optimizations`) return [200, measure.beoList(parsed(path, url.search), now)]
  const named = /^([a-z-]+)\/([^/]+)(?:\/([a-z-]+))?$/.exec(path.startsWith(ns) ? path.slice(ns.length) : '')
  if (named?.[1] === 'batch-energy-optimizations' && named[3] === 'result') { const body = measure.beoResult(decodeURIComponent(named[2]), now); return body && [200, body] }
  if (path === `${ns}profiling-runs`) return [200, measure.runs(parsed(path, url.search), q, now)]
  if (named?.[1] === 'profiling-runs' && named[3] === 'evidence-passport') { const body = measure.passport(decodeURIComponent(named[2]), now); return body && [200, body] }
  if (path === `${ns}dispatch-occupancy-measurements`) { const base = parsed(path, ''); return [200, { ...base, items: [...measure.occupancy(now), ...base.items] }] }
  if (named?.[1] === 'dispatch-occupancy-measurements' && !named[3]) { const body = measure.occupancy(now).find(row => row.name === decodeURIComponent(named[2])); return body && [200, body] }
  return undefined
}

async function mutate(scene, req, path, url) {
  const q = url.searchParams
  // Inference input is raw bytes, not JSON.
  if (req.method === 'POST' && /^v1\/workloads\/[^/]+\/infer$/.test(path)) {
    req.resume()
    await new Promise(resolve => setTimeout(resolve, 400))
    return [200, { outputs: [{ label: 'tabby cat', score: 0.912 }, { label: 'tiger cat', score: 0.061 }] }, { 'X-Chill-Request-ID': randomUUID() }]
  }
  const body = await readBody(req)
  if (path === 'v1/scheduler/settings' && req.method === 'PUT') {
    const current = state[scene]
    if (!req.headers['if-match']) return [428, { code: 'SettingsPreconditionRequired', message: 'GET settings and supply its ETag in If-Match' }]
    if (version(req) !== String(current.etag)) return [409, { code: 'SettingsConflict', message: 'Scheduler settings changed; reload and retry' }]
    if (!body || typeof body !== 'object' || Array.isArray(body)) return [400, { code: 'InvalidBody', message: 'Settings must be a JSON object' }]
    const before = current.settings, at = Date.now()
    current.settings = { ...before, ...body }
    current.etag++
    const change = { actorUsername: 'replay-operator', actorUID: 'replay-operator-uid', before, after: current.settings, validatedAt: iso(at), appliedAt: iso(at), affected: [] }
    current.history.push({ at, change })
    return [200, change, { ETag: `"${current.etag}"` }]
  }
  if (req.method === 'POST' && (q.get('dryRun') === 'All' || path.endsWith('/preflight'))) {
    if (path === 'v1/evaluations/preflight') return [200, { checks: [], liveTarget: [] }]
    if (path === 'v1/evaluations/load-scenarios/preflight') return [200, { checks: [], liveWorkloads: {} }]
    if (path.endsWith('/batch-energy-optimizations/preflight')) return [200, { apiVersion: 'edge.dacs.io/v1alpha1', kind: 'BatchEnergyOptimizationPreflight', readAt: iso(Date.now()),
      campaign: { namespace: capture.meta.namespace, name: body?.metadata?.name ?? '', spec: body?.spec ?? {} }, resolves: true, batchPlan: {} }]
    return [200, { dryRun: true, request: 'Accepted', task: body ?? null }]
  }
  const kind = /^v1\/(workloads|models|preprocessors|datasets)(?:\/([^/]+))?$/.exec(path)
  if (kind) {
    const store = catalog(scene, kind[1]), now = iso(Date.now())
    const meta = item => (item.desired ?? item).metadata
    if (req.method === 'POST' && !kind[2]) {
      const name = body?.name
      if (typeof name !== 'string' || !name) return [400, { code: 'InvalidBody', message: 'name is required' }]
      if (store.items.has(name)) return [409, { code: 'AlreadyExists', message: `${kind[1]} ${name} already exists` }]
      const object = { kind: KINDS[kind[1]], apiVersion: 'edge.dacs.io/v1alpha1', metadata: { name, uid: randomUUID(), resourceVersion: String(Date.now()), generation: 1, creationTimestamp: now.replace(/\.\d+Z$/, 'Z') }, spec: body.spec ?? {} }
      const item = kind[1] === 'workloads' ? { desired: object, supportedInputs: [], admission: false, readiness: 'Waiting', reason: 'AppliedDefinitionUnavailable', pending: 0, assigned: 0, observedAt: now } : object
      store.items.set(name, item)
      return [201, item]
    }
    const name = kind[2] && decodeURIComponent(kind[2]), item = name && store.items.get(name)
    if (!item) return [404, { code: 'NotFound', message: `${kind[1]} ${name} not found` }]
    if (!version(req) || version(req) === '*') return [428, { code: 'ResourceVersionRequired', message: 'If-Match must contain the queried resourceVersion' }]
    if (version(req) !== meta(item).resourceVersion) return [409, { code: 'Conflict', message: `${kind[1]} ${name} changed; reload and retry` }]
    if (req.method === 'PUT') {
      const object = { ...(item.desired ?? item), metadata: { ...meta(item), resourceVersion: String(Date.now()), generation: (meta(item).generation ?? 1) + 1 }, spec: body?.spec ?? (item.desired ?? item).spec }
      const next = item.desired ? { ...item, desired: object, observedAt: now } : object
      store.items.set(name, next)
      return [200, next]
    }
    if (req.method === 'DELETE') { store.items.delete(name); return kind[1] === 'workloads' ? [202, item] : [204] }
  }
  const last = decodeURIComponent(path.split('/').at(-1))
  return [202, { name: body?.name ?? body?.metadata?.name ?? last, uid: body?.uid ?? body?.metadata?.uid ?? q.get('uid') ?? randomUUID() }]
}

const upstream = https.createServer({ key, cert }, async (req, res) => {
  const token = tokenOf(req.headers.authorization)
  if (!token) return send(res, 401, { code: 'Unauthenticated', message: 'Bearer token required' })
  const url = new URL(req.url, 'https://replay')
  if (!url.pathname.startsWith('/api/v1/')) return send(res, 404, { code: 'ReplayMissing', message: url.pathname })
  const path = url.pathname.slice('/api/'.length), scene = sceneOf(token)
  try {
    if (req.method !== 'GET') {
      const [status, body, headers] = await mutate(scene, req, path, url)
      mutations.push({ at: iso(Date.now()), scene, method: req.method, path: `${path}${url.search}`, status })
      if (mutations.length > 200) mutations.shift()
      return send(res, status, body, headers)
    }
    const hit = overlay(scene, path, url, Date.now()) ?? (raw => raw && [raw.entry.status, raw.text, raw.entry.etag ? { ETag: raw.entry.etag } : {}])(captured(path, url.search))
    if (!hit) return send(res, 404, { code: 'ReplayMissing', message: url.pathname })
    // Shifted capture windows differ from the requested window; window-checked reads echo the request.
    const q = url.searchParams
    if (hit[0] === 200 && q.has('from') && q.has('to') && /^v1\/observations\/(events|load|scheduler|workloads\/)/.test(path)) {
      const body = typeof hit[1] === 'string' ? parseSchedulerJSON(hit[1]) : hit[1]
      if (typeof body?.from === 'string' && typeof body.to === 'string') hit[1] = { ...body, from: q.get('from'), to: q.get('to') }
    }
    send(res, ...hit)
  } catch (error) {
    send(res, error.status ?? 500, { code: error.status ? 'InvalidBody' : 'ReplayError', message: error.message })
  }
})

// chill-live/1 hub: hello, resume, snapshot, then scene frames with a contiguous per-connection seq.
const wss = new WebSocketServer({ noServer: true, perMessageDeflate: false })
const captureLoop = capture.frames.slice(capture.frames.findIndex(frame => frame.type === 'snapshot') + 1).filter(frame => frame.type !== 'heartbeat')
const loopRestart = captureLoop.findIndex(frame => frame.type === 'metrics')
const at = frame => Date.parse(frame.at)

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
    const { type: _type, seq: _seq, at: _at, ...body } = scene === 'active' ? active.snapshot(t0, autonomous(scene)) : parseSchedulerJSON(shiftTimes(JSON.stringify(capture.frames[1]), capture.shiftMs(t0)))
    socket.send(stringifySchedulerJSON({ type: 'snapshot', seq, at: iso(t0), ...body, ...history }))
    if (started) return
    started = true
    heartbeat = setInterval(() => { const at = iso(Date.now()); emit({ type: 'heartbeat', at, serverAt: at }) }, 5000)
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
    // Replay the capture with its own spacing; each pass shifts the frames (whole seconds) so they land at now.
    const pass = index => {
      const anchor = at(captureLoop[index]), began = Date.now(), shift = Math.round((began - anchor) / 1000) * 1000
      const step = i => {
        if (i >= captureLoop.length) { loop = setTimeout(() => pass(loopRestart), 1000); return }
        emit({ ...parseSchedulerJSON(shiftTimes(JSON.stringify(captureLoop[i]), shift)), at: iso(Date.now()) })
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
    return send(res, 200, { defaultScene, connections, settingsETag: Object.fromEntries(SCENES.map(name => [name, state[name].etag])), mutations })
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

upstream.listen(upstreamPort, '127.0.0.1'); await once(upstream, 'listening')
browser.listen(port, '127.0.0.1'); await once(browser, 'listening')
const root = fileURLToPath(new URL('..', import.meta.url))
const next = args['no-next'] ? null : spawn(join(root, 'node_modules/.bin/next'), ['dev', '--hostname', '127.0.0.1', '--port', String(nextPort)], {
  cwd: root, stdio: 'inherit', detached: true,
  env: { ...process.env, CHILL_BASE_URL: `https://127.0.0.1:${upstreamPort}`, NODE_EXTRA_CA_CERTS: join(directory, 'cert.pem'), CHILL_NAMESPACE: capture.meta.namespace, CHILL_SYSTEM_NAME: 'chill', CHILL_POLL_INTERVAL_MS: '5000' },
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

// Measurement requests (BatchEnergyOptimizations) and their ProfilingRuns for server/replay.mjs.
// The captured scene and the active scene share one request set (the union of every captured list
// page, plus requests the coverage still references) and one run set (every captured run, plus the
// child runs a request's status and failure summaries imply). Reads follow the real handlers:
// cmd/chill-query/measurement_http.go (list), internal/queryapi/readapi (runs list, /result,
// /evidence-passport) and internal/queryapi (assessments, failure summaries).
import { createHash } from 'node:crypto'
import { shiftTimes } from './replay-capture.mjs'
import { parseMeasurementJSON } from '../lib/measurement-summary.ts'

const API = 'console.chill.dacs.io/v1alpha1', GROUP = 'edge.dacs.io/v1alpha1', STORE = 'chill-evidence-store', CHECKLIST = 'result-checklist.v1'
const RESULT_SCHEMA = 'chill.dacs.io/batch-energy-optimization-result.v1alpha5', EVIDENCE_SCHEMA = 'chill.dacs.io/profiling-execution-evidence.v1alpha18'
const CAPTURED_EVIDENCE = 'chill.dacs.io/profiling-execution-evidence.v1alpha16'
const BATCHES = [1, 2, 4, 8, 16, 32, 64, 128]
const RUNNING = ['Pending', 'Granted', 'Running', 'Measuring', 'Waiting']
const DISPLAY = { criteria_met: ['usable', '사용 가능'], complete: ['usable', '사용 가능'], review: ['review', '검토 필요'], partial: ['insufficient', '부족'], warning: ['problem', '문제'], failed: ['problem', '문제'],
  running: ['running', '진행'], unmeasured: ['unmeasured', '미측정'], unsupported: ['unsupported', '지원 안 함'], unknown: ['unknown', '확인 불가'] }
// Typed terminations behind the failure codes of internal/queryapi/measurement_failures.go.
const TERMINATION = { MeasurementBudgetExceeded: { class: 'ExpectedStop', container: 'runtime', stage: 'Execution', failureReason: 'MeasurementBudgetExceeded' },
  MemoryAllocationFailed: { class: 'ExpectedStop', container: 'runtime', stage: 'Execution', failureReason: 'MemoryAllocationFailed' }, UnknownFailure: { class: 'UnknownFailure' },
  RetryableInfrastructureFailure: { class: 'RetryableFailure' }, EvidenceInvalid: { class: 'FatalFailure', failureReason: 'IntegrityError' } }
const LOG = { GPUMemoryExhausted: 'ERROR runner failed: CUDA failure 2: out of memory', PowerObservationTimeout: 'ERROR metric observation timed out after 500ms', RuntimeFailed: 'ERROR execute firstfloor measurement: runtime failed' }

const seconds = ms => new Date(ms).toISOString().replace(/\.\d+Z$/, 'Z')
const hash = text => createHash('sha256').update(text).digest('hex')
const uuid = seed => { const h = hash(seed); return `${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-a${h.slice(17, 20)}-${h.slice(20, 32)}` }
const byName = (a, b) => a.name < b.name ? -1 : a.name > b.name ? 1 : 0
const cellOf = c => [c.modelSpec, c.deviceClass, c.executionPath, c.powerMode].join('/')
const invalid = message => [400, { code: 'InvalidQuery', message }]

// MeasurementAssessment of internal/queryapi for one execution (no children).
export function assess(status, extra = {}) {
  const [displayStatus, label] = DISPLAY[status]
  return { status, displayStatus, label, reasons: [], execution: null, evidence: { available: false, accepted: false, reasons: [] },
    checklist: { criteriaMet: null, version: CHECKLIST, checks: [] }, scope: { schedulerUsed: null, reused: false, facts: [] }, ...extra }
}
// assessSingleMeasurement for a request execution without accepted evidence.
function requestAssessment(item, extra = {}) {
  const st = item.status ?? {}, execution = { phase: st.phase ?? '', generation: item.metadata.generation ?? 1, observedGeneration: st.observedGeneration ?? 0 }
  const current = execution.generation === execution.observedGeneration
  const status = current && execution.phase === 'Failed' ? 'failed' : current && RUNNING.includes(execution.phase) ? 'running' : 'unknown'
  return assess(status, { execution, ...extra })
}

export function measurementRequests({ capture, startedAt, ns, nodeUID, measure }) {
  const NS = `v1/namespaces/${ns}/`
  const known = (path, search) => capture.known(`${NS}${path}`, search)
  const summaryList = known('batch-energy-optimizations', '?view=summary&summary=v2'), plainList = known('batch-energy-optimizations', ''), runList = known('profiling-runs', '')
  const coverage = capture.json(`${NS}measurement-coverage`, '?view=summary&summary=v2')

  // List pages were read after capturedAt: move them back by whole minutes so nothing lies in the future.
  const latest = [...summaryList.items, ...runList.items].flatMap(row => [row.metadata?.creationTimestamp ?? row.creationTimestamp, ...(row.status?.conditions ?? []).map(c => c.lastTransitionTime)])
    .reduce((max, at) => Math.max(max, Date.parse(at ?? '') || 0), 0)
  const lag = Math.max(0, Math.ceil((latest - startedAt + 60_000) / 60_000) * 60_000)
  const settle = value => lag ? parseMeasurementJSON(shiftTimes(JSON.stringify(value), -lag)) : value
  const dedupe = (rows, key) => [...new Map(rows.map(row => [key(row), row])).values()]

  const summaries = new Map(dedupe(summaryList.items, row => row.metadata.uid).map(row => { const item = settle(row); return [item.metadata.name, item] }))
  const plains = new Map(dedupe(plainList.items, row => row.metadata.uid).map(row => { const item = settle(row); return [item.metadata.name, item] }))
  const capturedRuns = dedupe(runList.items, row => row.uid).map(settle)

  // Failure summaries name runs and requests; collect them by request name from every captured source.
  const failures = new Map(), references = new Map()
  const addFailure = row => { if (!row.runName) return; const name = row.requestId.split('/')[1]; if (!failures.has(name)) failures.set(name, new Map()); failures.get(name).set(row.runName, row) }
  for (const item of summaries.values()) for (const row of item.assessment?.failureSummary ?? []) addFailure(row)
  for (const cell of coverage.cells) {
    const refer = (id, status) => { if (!id) return; const row = references.get(id); if (!row) references.set(id, { cell, status }); else row.status ??= status }
    refer(cell.currentMeasurement?.id)
    for (const row of cell.history) refer(row.requestId, row.status)
    for (const row of cell.failureSummary) { refer(row.requestId, 'failed'); addFailure(row) }
    for (const variant of cell.variants) { for (const id of variant.requestIds) refer(id); for (const row of variant.assessment?.failureSummary ?? []) addFailure(row) }
  }
  measure?.attach(name => summaries.get(name))
  for (const [name, rows] of failures) for (const row of rows.values()) if (!references.has(row.requestId)) references.set(row.requestId, { status: 'failed' })

  // Requests the coverage still names but no captured page lists: rebuilt from the cell and a sibling.
  for (const [id, { cell, status }] of references) {
    const [, name, uid] = id.split('/')
    if (summaries.has(name)) continue
    const sibling = [...summaries.values()].find(row => cell && cellOf(row.spec.state.candidate) === cell.id) ?? [...summaries.values()].find(row => row.metadata.name.startsWith(name.replace(/-[^-]+$/, '')))
    if (!cell && !sibling) continue
    const [model, deviceClass, executionPath, powerMode] = (cell?.id ?? cellOf(sibling.spec.state.candidate)).split('/')
    const created = Date.parse(sibling?.metadata.creationTimestamp ?? seconds(startedAt)) - 3_600_000, ended = created + 1_800_000
    const phase = status === 'running' ? 'Measuring' : status === 'failed' ? 'Failed' : 'Complete'
    const spec = { ...(sibling?.spec ?? {}), state: { candidate: { ...(sibling?.spec.state.candidate ?? {}), modelSpec: model, deviceClass, executionPath, powerMode }, targetNode: sibling?.spec.state.targetNode ?? cell?.support?.node?.name } }
    const condition = { Measuring: ['Unknown', 'TrialInFlight', 'batch-energy-optimization measurements are in progress'], Failed: ['False', 'FatalFailure', 'batch-energy-optimization measurement failed'],
      Complete: ['True', 'ResultWritten', 'batch-energy-optimization result is complete'] }[phase]
    const item = { apiVersion: GROUP, kind: 'BatchEnergyOptimization',
      metadata: { name, namespace: ns, uid, resourceVersion: String(370000000 + parseInt(uid.slice(0, 5), 16)), generation: 1, creationTimestamp: seconds(created), annotations: { 'chill.dacs.io/coverage-cell': cell?.id ?? '' } },
      spec, status: { completedTrials: 0, observedGeneration: 1, phase, ...(phase === 'Failed' ? { outcome: 'FatalFailure' } : {}),
        conditions: [{ lastTransitionTime: seconds(phase === 'Measuring' ? Math.min(startedAt, created + 600_000) : ended), message: condition[2], observedGeneration: 1, reason: condition[1], status: condition[0], type: 'MeasurementComplete' }] } }
    item.assessment = requestAssessment(item, { reasons: [{ code: 'AcceptedEvidenceUnavailable', message: 'No verified catalog profile for this request identity', sourceIds: [id] }] })
    summaries.set(name, item); plains.set(name, item)
  }

  // Child runs a request implies: completed trials in campaign order, the in-flight trial, and named failures.
  const trialOrder = n => [['idle', 1], ...BATCHES.map(b => ['active', b])].flatMap(([mode, batch]) => Array.from({ length: n }, (_, i) => ({ mode, batch, attempt: i + 1 })))
  function childRuns(item) {
    const { name, uid } = item.metadata, spec = item.spec, st = item.status ?? {}, n = spec.trialsPerBatch ?? 3
    const done = st.completedTrials ?? 0, order = trialOrder(n), named = failures.get(name) ?? new Map()
    const trials = order.slice(0, done).map(trial => ({ ...trial, state: 'complete' }))
    if (RUNNING.includes(st.phase) && order[done]) trials.push({ ...order[done], state: 'running' })
    for (const row of named.values()) {
      const match = /-(idle|active)-b(\d+)-a(\d+)$/.exec(row.runName)
      const trial = { mode: match?.[1] ?? 'active', batch: Number(match?.[2] ?? row.batch ?? 1), attempt: Number(match?.[3] ?? 1), state: 'failed', failure: row }
      const at = trials.findIndex(t => `${name}-${t.mode}-b${t.batch}-a${t.attempt}` === row.runName)
      if (at >= 0) trials[at] = trial; else trials.push(trial)
    }
    const start = Date.parse(item.metadata.creationTimestamp) + 60_000
    const end = Math.min(startedAt - 5_000, Date.parse(st.conditions?.[0]?.lastTransitionTime ?? '') || startedAt - 5_000)
    const step = Math.max(1_000, (end - start) / (trials.length + 1))
    return trials.map((trial, index) => {
      const created = Math.min(startedAt - 5_000, start + index * step), runName = trial.failure?.runName ?? `${name}-${trial.mode}-b${trial.batch}-a${trial.attempt}`
      const runUID = trial.failure?.runId || uuid(`${uid}/${runName}`), job = `profilingrun-${hash(runUID).slice(0, 32)}`, finished = seconds(Math.min(startedAt - 1_000, created + step * 0.8))
      const protocol = trial.mode === 'idle' ? spec.idleProtocol ?? {} : spec.activeProtocol ?? {}
      const measurement = trial.mode === 'idle'
        ? { batch: 1, warmupIterations: 0, settlingDurationSeconds: protocol.settlingDurationSeconds, durationSeconds: protocol.durationSeconds, durationToleranceSeconds: protocol.durationToleranceSeconds, workloadMode: 'idle-model-loaded' }
        : { batch: trial.batch, warmupIterations: protocol.warmupIterations, durationSeconds: protocol.durationSeconds, durationToleranceSeconds: protocol.durationToleranceSeconds, workloadMode: 'saturated-serial-fixed-batch', dataset: protocol.dataset }
      const node = spec.state.targetNode, execution = phase => ({ phase, generation: 1, observedGeneration: 1 })
      const base = { observedGeneration: 1, job: { name: job, uid: uuid(job) } }
      let status, assessment
      if (trial.state === 'complete') {
        const key = `profiling-runs/${runUID}/execution.json`
        status = { ...base, conditions: [{ type: 'ExecutionComplete', status: 'True', observedGeneration: 1, lastTransitionTime: finished, reason: 'JobSucceeded', message: 'measurement Job completed successfully' },
          { type: 'EvidenceVerified', status: 'True', observedGeneration: 1, lastTransitionTime: finished, reason: 'EvidenceVerified', message: 'immutable execution evidence was independently verified' }],
        actualExecution: { pod: { name: `${job}-${hash(job).slice(0, 5)}`, uid: uuid(`${job}/pod`) }, node: { name: node, uid: nodeUID[node] ?? '' } },
        evidence: { store: STORE, key, schemaVersion: CAPTURED_EVIDENCE, size: 9200 + parseInt(runUID.slice(0, 3), 16) }, jobCleanup: 'Done' }
        assessment = assess('complete', { execution: execution('Complete'), evidence: { available: true, accepted: true, reasons: [] }, scope: { schedulerUsed: null, reused: false, facts: [`Retained evidence: ${STORE}/${key}`] } })
      } else if (trial.state === 'running') {
        status = { ...base, conditions: [{ type: 'ExecutionComplete', status: 'Unknown', observedGeneration: 1, lastTransitionTime: seconds(created + 5_000), reason: 'JobRunning', message: 'measurement Job is running' }] }
        assessment = assess('running', { execution: execution('Running') })
      } else {
        const row = trial.failure, termination = row.exitCode == null ? undefined
          : { exitCode: row.exitCode, ...(TERMINATION[row.code] ?? {}), logTail: row.firstErrorLine ?? `${row.firstErrorTime ?? ''} ${LOG[row.code] ?? `ERROR measurement exited with code ${row.exitCode}`}`.trim() }
        const invalidEvidence = row.code === 'EvidenceInvalid'
        status = { ...base, conditions: [{ type: 'ExecutionComplete', status: 'False', observedGeneration: 1, lastTransitionTime: finished, reason: 'JobFailed', message: 'measurement Job failed' },
          ...(invalidEvidence ? [{ type: 'EvidenceVerified', status: 'False', observedGeneration: 1, lastTransitionTime: finished, reason: 'EvidenceInvalid', message: 'execution evidence failed integrity verification' }] : [])],
        jobCleanup: 'Done', ...(termination ? { runtimeTermination: termination } : {}) }
        const reason = { code: row.code, label: row.label, message: row.message, sourceIds: [runUID] }
        assessment = assess(invalidEvidence ? 'warning' : 'failed', { reasons: invalidEvidence ? [reason, reason] : [reason], execution: execution('Failed'),
          failureSummary: [{ requestId: '', runId: runUID, runNamespace: ns, runName, batch: trial.batch, code: row.code, label: row.label, exitCode: row.exitCode ?? null, message: row.message,
            ...(row.firstErrorLine ? { firstErrorLine: row.firstErrorLine, firstErrorTime: row.firstErrorTime } : {}), relation: 'unresolved' }] })
      }
      return { namespace: ns, name: runName, uid: runUID, resourceVersion: String(Number(item.metadata.resourceVersion) - 1 - index), generation: 1, creationTimestamp: seconds(created),
        ownerReferences: [{ apiVersion: GROUP, kind: 'BatchEnergyOptimization', name, uid, controller: true, blockOwnerDeletion: true }],
        labels: { 'edge.dacs.io/attempt': String(trial.attempt), 'edge.dacs.io/batch': String(trial.batch), 'edge.dacs.io/batch-energy-optimization': name, 'edge.dacs.io/workload-mode': trial.mode },
        spec: { candidate: spec.state.candidate, targetNode: node, measurement, campaignTrial: { batch: trial.batch, attempt: trial.attempt }, metricObservation: protocol.metricObservation }, status, assessment }
    })
  }
  const runs = new Map(capturedRuns.map(run => [run.name, run]))
  for (const item of summaries.values()) for (const run of childRuns(item)) if (!runs.has(run.name)) runs.set(run.name, run)
  const baseRuns = [...runs.values()].sort(byName)
  const capturedRaw = path => { const entry = capture.match(path); return entry && entry.path === path ? [entry.status, parseMeasurementJSON(shiftTimes(capture.text(entry), -lag))] : undefined }

  // Per-scene views. `own` = { added: [], removed: Set } from the mutation state.
  const overlaid = (scene, name) => scene === 'active' && measure.overlays(name)
  function items(scene, own, summary, t) {
    const added = own.added.map(item => summary ? { ...item, assessment: requestAssessment(item) } : item)
    const base = [...(summary ? summaries : plains).values()].map(item => overlaid(scene, item.metadata.name) ? measure.beoItem(item, t) : item)
    const sessions = scene === 'active' ? measure.sessions(t) : []
    return [...added, ...sessions, ...base].filter(item => !own.removed.has(item.metadata.name)).sort((a, b) => byName(a.metadata, b.metadata))
  }
  const item = (scene, own, name, t) => items(scene, own, true, t).find(row => row.metadata.name === name)
  function runSet(scene, own, t) {
    const removed = name => own.removed.has(name) || overlaid(scene, name)
    const synthetic = scene === 'active' ? measure.runs(t).filter(run => !own.removed.has(run.ownerReferences[0].name)) : []
    return [...synthetic, ...baseRuns.filter(run => !removed(run.ownerReferences[0].name))].sort(byName)
  }
  function runNamed(scene, own, name, t) {
    if (scene === 'active') { const run = measure.runs(t).find(row => row.name === name); if (run) return own.removed.has(run.ownerReferences[0].name) ? undefined : run }
    const run = runs.get(name)
    return run && !own.removed.has(run.ownerReferences[0].name) && !overlaid(scene, run.ownerReferences[0].name) ? run : undefined
  }
  // MeasurementRequestFailures: problem runs owned by the request, newest first.
  function requestFailures(scene, own, request, t) {
    const owned = runSet(scene, own, t).filter(run => run.ownerReferences.some(o => o.controller && o.kind === 'BatchEnergyOptimization' && o.name === request.metadata.name && o.uid === request.metadata.uid))
      .filter(run => run.assessment.displayStatus === 'problem').sort((a, b) => a.creationTimestamp === b.creationTimestamp ? (a.name < b.name ? 1 : -1) : a.creationTimestamp < b.creationTimestamp ? 1 : -1)
    return owned.map(run => ({ ...run.assessment.failureSummary[0], requestId: `${ns}/${request.metadata.name}/${request.metadata.uid}` }))
  }
  const fallbackFailure = request => [{ requestId: `${ns}/${request.metadata.name}/${request.metadata.uid}`, runId: '', runNamespace: '', runName: '', batch: null, code: 'Other', label: '기타 오류',
    exitCode: null, message: '측정을 완료하지 못했습니다. 실행 상세에서 상태를 확인해 주세요.', relation: 'unresolved' }]

  return {
    lagSeconds: lag / 1000,
    capturedRunCount: capturedRuns.length,

    // GET batch-energy-optimizations: MeasurementSummaryVersion + MeasurementListPage, name order, page only when asked.
    list(scene, own, q, t, meta) {
      const keys = [...q.keys()]
      if (keys.some(key => q.getAll(key).length !== 1 || !['view', 'summary', 'limit', 'offset'].includes(key)) || (q.has('view') && q.get('view') !== 'summary')
        || (q.has('summary') && (q.get('summary') !== 'v2' || q.get('view') !== 'summary'))) return invalid('invalid measurement list query')
      for (const key of ['limit', 'offset']) if (q.has(key)) {
        const n = Number(q.get(key))
        if (String(n) !== q.get(key) || !Number.isInteger(n) || (key === 'limit' ? n < 1 || n > 50 : n < 0 || n > 1_000_000)) return invalid('invalid measurement list query')
      }
      const all = items(scene, own, q.get('view') === 'summary', t), body = { ...meta, items: all }
      if (!q.has('limit') && !q.has('offset')) return [200, body]
      const offset = Number(q.get('offset') ?? 0), limit = Number(q.get('limit') ?? 25), start = Math.min(offset, all.length), end = start + Math.min(limit, all.length - start)
      return [200, { ...body, items: all.slice(start, end), page: { offset, limit, total: all.length, ...(end < all.length ? { nextOffset: end } : {}) } }]
    },
    items: (scene, own, t) => items(scene, own, true, t),
    item,

    // GET profiling-runs: parseListQuery(runs=true), matchesRunFilters, sortRuns, pageBounds.
    runs(scene, own, q, t) {
      const filters = {}, search = { q: '' }
      if (q.toString().length > 4096) return [400, { code: 'InvalidQuery' }]
      for (const key of new Set(q.keys())) {
        const values = q.getAll(key), value = values[0]
        if (values.length !== 1) return [400, { code: 'InvalidQuery' }]
        if (key === 'limit' || key === 'offset') {
          const n = Number(value)
          if (!/^[+-]?\d+$/.test(value) || (key === 'limit' ? n <= 0 || n > 200 : n < 0 || n > 1_000_000)) return [400, { code: 'InvalidQuery' }]
          search[key] = n
        } else if (key === 'q') { if (value.length > 256) return [400, { code: 'InvalidQuery' }]; search.q = value.toLowerCase() }
        else if (key === 'sort') { if (!['name', 'created', 'model', 'powerMode', 'status'].includes(value)) return [400, { code: 'InvalidQuery' }]; search.sort = value }
        else if (key === 'order') { if (!['asc', 'desc'].includes(value)) return [400, { code: 'InvalidQuery' }]; search.order = value }
        else if (key === 'condition') {
          const [kind, status] = [value.slice(0, value.indexOf('=')), value.slice(value.indexOf('=') + 1)]
          if (!value.includes('=') || !kind || value.length > 128 || !['True', 'False', 'Unknown'].includes(status)) return [400, { code: 'InvalidQuery' }]
          search.condition = [kind, status]
        } else if (['model', 'deviceClass', 'node', 'path', 'powerMode', 'status', 'parentUID', 'group'].includes(key)) {
          if (value.length > 2048) return [400, { code: 'InvalidQuery' }]
          if (key === 'group' && value) { const group = canonicalGroup(value); if (group === null) return [400, { code: 'InvalidQuery' }]; filters.group = group } else filters[key] = value
        } else return [400, { code: 'InvalidQuery' }]
      }
      if (search.order && !search.sort) return [400, { code: 'InvalidQuery' }]
      const fields = { model: run => run.spec.candidate.modelSpec, deviceClass: run => run.spec.candidate.deviceClass, node: run => run.spec.targetNode, path: run => run.spec.candidate.executionPath, powerMode: run => run.spec.candidate.powerMode }
      const selected = runSet(scene, own, t).filter(run => run.namespace === ns && run.name.toLowerCase().includes(search.q)
        && (!search.condition || (run.status.conditions ?? []).some(c => c.type === search.condition[0] && c.status === search.condition[1]))
        && Object.entries(fields).every(([key, get]) => !filters[key] || filters[key] === get(run))
        && (!filters.status || run.assessment.displayStatus === filters.status)
        && (!filters.parentUID || run.ownerReferences.some(o => o.controller && o.apiVersion === GROUP && o.kind === 'BatchEnergyOptimization' && o.uid === filters.parentUID))
        && (!filters.group || runGroup(run) === filters.group))
      if (search.sort) {
        const key = { name: run => run.name, created: run => run.creationTimestamp, model: run => run.spec.candidate.modelSpec, powerMode: run => run.spec.candidate.powerMode, status: run => run.assessment.displayStatus }[search.sort]
        const sign = search.order === 'desc' ? -1 : 1, cmp = (a, b) => a < b ? -1 : a > b ? 1 : 0
        selected.sort((a, b) => sign * cmp(key(a), key(b)) || cmp(a.name, b.name) || cmp(a.uid, b.uid))
      }
      const offset = search.offset ?? 0, limit = search.limit ?? 50, start = Math.min(offset, selected.length), end = Math.min(start + limit, selected.length)
      return [200, { readAt: new Date(t).toISOString(), resourceVersion: String(385500963 + Math.floor((t - startedAt) / 1000)), namespace: ns, allNamespaces: false,
        runs: selected.slice(start, end), page: { offset, limit, total: selected.length, ...(end < selected.length ? { nextOffset: end } : {}) } }]
    },
    run: runNamed,

    // GET batch-energy-optimizations/{name}/result: BuildBatchEnergyOptimizationResult or its error projection.
    // A request without session settings whose result is published answers PreviousFormat (BuildBatchEnergyOptimizationResult).
    result(scene, own, name, t) {
      const [code, body] = this.storedResult(scene, own, name, t)
      const request = item(scene, own, name, t)
      return [code, request?.status?.result && !request.spec.session && body.campaign
        ? { ...body, resultFormat: 'PreviousFormat', resultFormatMessage: 'This result was stored by the previous measurement method and cannot be read; delete the request and measure again.' } : body]
    },
    storedResult(scene, own, name, t) {
      const request = item(scene, own, name, t)
      if (!request) return [404, { code: 'BatchEnergyOptimizationResultUnavailable' }]
      if (scene === 'active' && request.spec.session) return [200, measure.sessionResult(name, t)]
      if (overlaid(scene, name)) return [200, measure.beoResult(request, t)]
      if (!own.added.some(row => row.metadata.name === name)) { const raw = capturedRaw(`${NS}batch-energy-optimizations/${name}/result`); if (raw) return [raw[0], { ...raw[1], readAt: new Date(t).toISOString() }] }
      const st = request.status ?? {}, ref = st.result
      const campaign = { namespace: ns, name, uid: request.metadata.uid, resourceVersion: request.metadata.resourceVersion, generation: request.metadata.generation ?? 1, spec: request.spec,
        ...(st.phase ? { phase: st.phase } : {}), ...(st.outcome ? { outcome: st.outcome } : {}), completedTrials: st.completedTrials ?? 0, ...(st.conditions?.length ? { conditions: st.conditions } : {}),
        storedResult: ref ? { store: ref.store, key: ref.key, size: ref.size } : null }
      const listsFailedRuns = st.phase === 'Failed' || (st.phase === 'Complete' && st.outcome === 'BatchDiscoveryComplete')
      const head = { apiVersion: API, kind: 'BatchEnergyOptimizationResult', readAt: new Date(t).toISOString(), campaign, result: null, statistics: null }
      const withFailures = assessment => {
        const failureSummary = listsFailedRuns ? requestFailures(scene, own, request, t) : []
        return { ...assessment, scope: { ...assessment.scope, schedulerUsed: false }, ...(failureSummary.length ? { failureSummary } : assessment.displayStatus === 'problem' ? { failureSummary: fallbackFailure(request) } : {}) }
      }
      if (!ref) {
        const reason = st.phase === 'Failed' ? 'RequestFailed' : 'NoPublishedResult'
        return [200, { ...head, statisticsReason: reason, measurementFacts: null, measurementFactsReason: reason, assessment: withFailures(requestAssessment(request)), resultRows: [] }]
      }
      const curve = request.assessment?.reasons?.find(row => row.code === 'InvalidEvidence' && /curve-plan/.test(row.message))?.message ?? 'unsupported curve-plan schema "chill.dacs.io/curve-plan.v1alpha12"'
      const reason = ref.schemaVersion !== RESULT_SCHEMA ? `decode BatchEnergyOptimization ${ns}/${name} result: unsupported batch-energy-optimization result schema "${ref.schemaVersion}"`
        : `read BatchEnergyOptimization ${ns}/${name} statistics: ${curve}`
      const assessment = requestAssessment(request)
      return [502, { ...head, statisticsReason: 'ResultUnavailable', measurementFacts: null, measurementFactsReason: 'ResultUnavailable', assessment: withFailures(assessment), resultRows: [],
        code: 'BatchEnergyOptimizationResultUnavailable', reason, evidenceError: { code: 'BatchEnergyOptimizationResultUnavailable', status: 502 } }]
    },

    // GET profiling-runs/{name}/evidence-passport: pending 202, unavailable 502 with the Run state, 404 without a Run.
    passport(scene, own, name, t) {
      const run = runNamed(scene, own, name, t)
      if (!run) return [404, { code: 'EvidencePassportUnavailable' }]
      if (scene === 'active' && run.status.evidence?.schemaVersion === EVIDENCE_SCHEMA) { const body = measure.passport(name, t); if (body) return [200, body] }
      const state = (code, reason) => ({ apiVersion: API, kind: 'EvidencePassport', readAt: new Date(t).toISOString(), code, ...(reason ? { reason } : {}),
        run: { namespace: run.namespace, name, uid: run.uid, resourceVersion: run.resourceVersion, generation: run.generation, spec: run.spec, ...(run.status.conditions?.length ? { conditions: run.status.conditions } : {}),
          ...(run.status.job ? { job: run.status.job } : {}), ...(run.status.actualExecution ? { actualExecution: run.status.actualExecution } : {}) },
        assessment: run.assessment, evidence: null, measurement: null })
      const evidence = run.status.evidence
      if (!evidence && run.assessment.displayStatus === 'running') return [202, state('EvidencePassportPending')]
      const raw = capturedRaw(`${NS}profiling-runs/${name}/evidence-passport`)
      if (raw && runs.get(name) === run) return [raw[0], { ...raw[1], readAt: new Date(t).toISOString() }]
      const problem = !evidence ? 'verified evidence is not available' : evidence.schemaVersion !== EVIDENCE_SCHEMA ? `unsupported evidence schemaVersion "${evidence.schemaVersion}"` : null
      return [502, state('EvidencePassportUnavailable', problem ? `read ProfilingRun ${ns}/${name} evidence reference: ${problem}` : `load ProfilingRun ${ns}/${name} evidence: evidence object not found`)]
    },

    // Every request id the coverage names, and the failure-summary runs, for the self-check.
    references: () => [...references.keys()],
    failureRuns: () => [...failures.values()].flatMap(rows => [...rows.values()]),
  }
}

// readapi canonicalRunGroup and the run side of matchesRunFilters: sorted-key JSON of the spec without batch.
const sortedJSON = value => JSON.stringify(value, (_key, item) => item && typeof item === 'object' && !Array.isArray(item) ? Object.fromEntries(Object.keys(item).sort().map(k => [k, item[k]])) : item)
function canonicalGroup(raw) { try { const value = JSON.parse(raw); return value && typeof value === 'object' && !Array.isArray(value) ? sortedJSON(value) : null } catch { return null } }
function runGroup(run) { const { batch: _batch, ...measurement } = run.spec.measurement ?? {}; return sortedJSON({ ...run.spec, measurement, namespace: run.namespace }) }

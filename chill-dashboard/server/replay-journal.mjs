// Scheduler journal for server/replay.mjs: one record set per scene, served in every view of
// v1/observations/scheduler with the window, filter, order and cursor rules of
// internal/serving/observation/observation_log_scheduler.go (SchedulerObservationPage).
import { nanos } from './replay-capture.mjs'

// RFC3339Nano text of BigInt Unix nanoseconds, trailing zeros trimmed as Go formats it.
export function fmtNanos(value) {
  const seconds = value / 1_000_000_000n, fraction = value % 1_000_000_000n
  const base = new Date(Number(seconds) * 1000).toISOString().slice(0, 19)
  return fraction ? `${base}.${fraction.toString().padStart(9, '0').replace(/0+$/, '')}Z` : `${base}Z`
}

// Record kind with the precedence of schedulerObservationEnvelope.kind().
export function recordKind(row) {
  if (row.decision) return 'decision'
  if (row.snapshot) return 'snapshot'
  if (row.settings) return 'settings'
  if (row.action) return 'action'
  if (row.meter || row.power || row.powerJoules !== undefined) return 'power'
  return 'other'
}
const identity = row => row.action?.id ?? row.node?.uid ?? row.control?.scope ?? ''

// decision-change: a decision whose target exists and is not the keep plan.
export const changedDecision = decision => !!decision?.target && !decision.target.keep && decision.target.id !== decision.keep?.id

// schedulerHistoryObservations projection; decision rows carry the optional reasons, targetKeep and evaluationElapsedNanos.
export function summaryRow(row) {
  const out = { at: row.at }
  if (row.control) out.control = row.control
  if (row.decision) {
    const d = row.decision
    out.decision = { at: d.at, status: d.status, workloads: d.workloads ?? null }
    if (d.reasons?.length) out.decision.reasons = d.reasons
    if (d.target !== undefined && d.target !== null) out.decision.targetKeep = d.targetKeep ?? (!!d.target.keep || d.target.id === d.keep?.id)
    else if (d.targetKeep !== undefined) out.decision.targetKeep = d.targetKeep
    if (d.evaluationElapsedNanos !== undefined) out.decision.evaluationElapsedNanos = d.evaluationElapsedNanos
  }
  if (row.snapshot) out.snapshot = { at: row.snapshot.at }
  if (row.settings) out.settings = { actorUsername: row.settings.actorUsername, actorUID: row.settings.actorUID, ...(row.settings.appliedAt ? { appliedAt: row.settings.appliedAt } : {}), ...(row.settings.error ? { error: row.settings.error } : {}) }
  if (row.action) out.action = { id: row.action.id, kind: row.action.kind, node: row.action.node, phase: row.action.phase, startedAt: row.action.startedAt }
  if (row.power) out.power = { startedAt: row.power.startedAt }
  if (row.node) out.node = row.node
  if (row.powerJoules !== undefined) out.powerJoules = row.powerJoules
  return out
}

// Captured journal: every captured scheduler record, one entry per record identity, keeping its
// richest form (full > compact > summary). Decision summaries gain fields from the richer form.
export function capturedJournal(capture) {
  const records = new Map()
  for (const entry of capture.entries.filter(row => row.path === 'v1/observations/scheduler' && row.status === 200)) {
    const view = entry.params.find(([key]) => key === 'view')?.[1] ?? 'full'
    const body = capture.json(entry.path, entry.key.slice(entry.path.length))
    for (const event of body.events ?? []) {
      const { offset: _offset, omitted: _omitted, projection: _projection, ...row } = event
      const kind = recordKind(row), key = `${kind}|${nanos(row.at)}|${identity(row)}`
      const record = records.get(key) ?? { at: row.at, atN: nanos(row.at), kind, forms: {} }
      record.forms[view] ??= row
      records.set(key, record)
    }
  }
  const rows = [...records.values()]
  for (const record of rows) {
    const rich = record.forms.full ?? record.forms.compact
    record.summary = summaryRow(rich ?? record.forms.summary)
    if (record.forms.summary && rich?.decision) record.summary = { ...record.forms.summary, decision: summaryRow(rich).decision }
    else if (record.forms.summary) record.summary = record.forms.summary
    // Records known only in summary form answer richer views with their summary fields.
    record.compact = () => record.forms.compact ?? record.forms.full ?? record.summary
    record.full = () => record.forms.full ?? record.forms.compact ?? record.summary
  }
  // Every decision has a tick control row joined by decisionAt (synthesized where the capture lacks it).
  const ticks = new Set(rows.filter(row => row.kind === 'other' && row.forms.summary?.control?.scope === 'tick').map(row => row.summary.control.decisionAt))
  for (const record of rows.filter(row => row.kind === 'decision' && !ticks.has(row.at))) {
    const evaluate = BigInt(record.summary.decision.evaluationElapsedNanos ?? 110_000_000 + Number(record.atN % 90_000_000n))
    const control = tickControl(record.atN, evaluate, record.summary.decision.status, record.summary.decision.targetKeep === false ? 'candidate' : record.summary.decision.targetKeep ? 'keep' : '')
    const row = { at: control.finishedAt, control }
    rows.push({ at: row.at, atN: nanos(row.at), kind: 'other', summary: row, compact: () => row, full: () => row, forms: { summary: row } })
  }
  return rows.sort((a, b) => a.atN < b.atN ? -1 : a.atN > b.atN ? 1 : 0)
}

// A tick SchedulerControlObservation that evaluated `evaluate` ns and finished just after decision `atN`.
export function tickControl(atN, evaluate, status, targetID) {
  const capture = 180_000_000n + atN % 60_000_000n, window = 400_000n + atN % 300_000n, publish = 27_000n, record = 1_500_000n
  const tickAt = atN - capture - window - evaluate, finished = atN + publish + record + 90_000_000n
  return { scope: 'tick', tickAt: fmtNanos(tickAt), decisionAt: fmtNanos(atN), startedAt: fmtNanos(tickAt + 600n), finishedAt: fmtNanos(finished),
    elapsedNanos: Number(finished - tickAt), phase: 'launch', ...(status ? { status } : {}), ...(targetID ? { targetID } : {}),
    lockWaitNanos: 650, admitNanos: 0, tickNanos: 0, captureNanos: Number(capture), windowNanos: Number(window), refreshListNanos: Number(capture) - 1_700_000,
    sourceProbeNanos: 336_897, restNanos: 916_780, evaluateNanos: Number(evaluate), publishNanos: Number(publish), recordNanos: Number(record), launchNanos: 2434,
    evaluation: { preparationNanos: Math.round(Number(evaluate) * 0.08), searchNanos: Math.round(Number(evaluate) * 0.7), paybackNanos: Math.round(Number(evaluate) * 0.05),
      ratesNanos: Math.round(Number(evaluate) * 0.1), projectionNanos: Math.round(Number(evaluate) * 0.05), surrogateElapsedNanos: Math.round(Number(evaluate) * 0.4), exactElapsedNanos: Math.round(Number(evaluate) * 0.25) },
    refreshNodesNanos: 1_200_000, refreshPodsNanos: 3_400_000, refreshResourcesNanos: 900_000, refreshDeviceClassesNanos: 400_000, refreshServicesNanos: 600_000,
    windowBarrierNanos: 20_000, windowScanNanos: Number(window) / 2, windowDecodeNanos: 60_000, windowJoinNanos: 30_000, windowSortNanos: 10_000, windowCacheNanos: 5_000, windowBudgetFallbacks: 0 }
}

const FILTERS = ['', 'all', 'decision', 'snapshot', 'settings', 'action', 'power', 'other', 'decision-action', 'decision-change']
const invalid = message => ({ status: 400, body: { code: 'InvalidQuery', message } })

// One SchedulerObservationPage over `records(fromN, toN)` (ascending, all kinds).
export function queryJournal(records, q, now, maxEvents = 500) {
  const toN = q.has('to') ? nanos(q.get('to')) : BigInt(now) * 1_000_000n
  const fromN = q.has('from') ? nanos(q.get('from')) : toN === null ? null : toN - 60_000_000_000n
  if (fromN === null || toN === null || fromN >= toN) return invalid('positive RFC3339 observation interval is required')
  const filter = q.get('filter') ?? '', order = q.get('order') ?? 'asc', view = q.get('view') ?? ''
  if (!FILTERS.includes(filter) || !['asc', 'desc'].includes(order) || !['', 'summary', 'compact'].includes(view)) return invalid('invalid scheduler observation selection')
  const limit = q.has('limit') ? Number(q.get('limit')) : maxEvents, cursor = q.has('cursor') ? Number(q.get('cursor')) : 0
  if (!Number.isSafeInteger(limit) || limit <= 0 || limit > maxEvents || !Number.isSafeInteger(cursor) || cursor < 0) return invalid('bounded limit and nonnegative cursor are required')
  const window = records(fromN, toN).filter(row => row.atN >= fromN && row.atN < toN).map((row, offset) => ({ row, offset }))
  const uid = q.get('uid')
  let selected = window.filter(({ row }) => (filter === '' || filter === 'all' || filter === row.kind
    || (filter === 'decision-action' && (row.kind === 'decision' || row.kind === 'action'))
    || (filter === 'decision-change' && row.kind === 'decision' && changedDecision(row.full().decision)))
    && (!uid || JSON.stringify(row.full()).includes(uid)))
  if (cursor > selected.length) return invalid('cursor exceeds selected observation count')
  if (order === 'desc') selected = [...selected].sort((a, b) => a.row.atN === b.row.atN ? b.offset - a.offset : a.row.atN > b.row.atN ? -1 : 1)
  const end = Math.min(selected.length, cursor + limit)
  const events = selected.slice(cursor, end).map(({ row, offset }) => ({ ...(view === 'summary' ? row.summary : view === 'compact' ? row.compact() : row.full()), offset }))
  const from = fmtNanos(fromN), to = fmtNanos(toN)
  const body = { from, to, events, ...(end < selected.length ? { nextCursor: String(end) } : {}),
    coverage: { from, to, sources: [{ from, to, stream: 'scheduler', source: 'local' }], gaps: [] } }
  return { status: 200, body: view === 'compact' ? { ...body, profiles: {} } : body }
}

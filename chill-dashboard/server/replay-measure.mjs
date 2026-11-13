// Synthetic healthy measurement evidence for the replay 'active' scene. Every overlay is derived from
// captured names (models, device classes, nodes, paths, power modes, requests) and follows the Query
// wire types in internal/queryapi (coverage, result rows, BEO result, evidence passport, occupancy).
import { createHash } from 'node:crypto'
import { MEASUREMENT_NODE } from './replay-scene.mjs'

const STORE = 'chill-evidence-store', BATCHES = [1, 2, 4, 8], TRIALS = 3, CHECKLIST = 'result-checklist.v1'
const MODELS = ['mobilenet-v2-050', 'plc-lstm-ae', 'smolvlm2-500m-video-instruct']
const MODEL_MS = { 'mobilenet-v2-050': 3.8, 'plc-lstm-ae': 2.4, 'smolvlm2-500m-video-instruct': 560 }
const MEMORY = { 'mobilenet-v2-050': [182e6, 9e6], 'plc-lstm-ae': [96e6, 2e6], 'smolvlm2-500m-video-instruct': [1.92e9, 140e6] }
// idle watts and [batch-1 slowdown, active delta watts] per execution kind.
const CLASS = {
  'edge-server-rtx-2080-ti-64g': { idle: 54, gpu: [1, 62], cpu: [3.2, 38] },
  'jetson-agx-orin-64g': { idle: 13.5, gpu: [2.3, 17], cpu: [5.5, 9] },
  'jetson-orin-nano-8g': { idle: 4.1, gpu: [4.2, 5.2], cpu: [11, 3.4] },
  'jetson-xavier-nx-8g': { idle: 5.6, gpu: [5, 6.1], cpu: [13, 3.8] },
  'jetson-nano-4g': { idle: 1.9, gpu: [9.5, 3.4], cpu: [24, 2.3] },
  'lattepanda-3-delta-8g': { idle: 8.2, cpu: [8.5, 6.4] },
  'orangepi-5-plus-16g': { idle: 4, cpu: [7.5, 4.6], npu: [3, 2.1] },
  'raspberry-pi-5-8g': { idle: 3.1, cpu: [10.5, 4.1] },
}
// Serving placements of the active scene: [node, model, path, power mode].
export const PLACEMENTS = [
  ['genesis2', 'mobilenet-v2-050', 'firstfloor-ort-cuda-amd64', 'GPU_1500'],
  ['jetsono', 'mobilenet-v2-050', 'firstfloor-ort-cuda-arm64', 'MAXN'],
  ['jetson-nano', 'plc-lstm-ae', 'firstfloor-ort-cuda-rnn-arm64', 'MAXN'],
  ['genesis3', 'smolvlm2-500m-video-instruct', 'firstfloor-ort-cuda-amd64', 'GPU_1500'],
]
const PREPARED = ['orin-nano', 'mobilenet-v2-050', 'firstfloor-ort-cuda-arm64', '15W']
// Captured requests that become healthy: two complete, one still measuring. The captured failed one stays.
const COMPLETE = ['coverage-694507bcc68d-06b30e6db9c5', 'coverage-4de3c8086996-8277c899db90']
const MEASURING = 'coverage-69d06d5c6f97-6de8b857ed79'
const USABLE = 20, RUNNING = 3, INSUFFICIENT = 2

const iso = ms => new Date(ms).toISOString()
const seconds = ms => iso(ms).replace(/\.\d+Z$/, 'Z')
const hash = text => createHash('sha256').update(text).digest('hex')
const uuid = seed => { const h = hash(seed); return `${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-a${h.slice(17, 20)}-${h.slice(20, 32)}` }
const jitter = (seed, spread) => 1 + (parseInt(hash(seed).slice(0, 8), 16) / 0xffffffff - 0.5) * 2 * spread
const round = (value, digits = 3) => Math.round(value * 10 ** digits) / 10 ** digits
const pass = (code, message, sourceIds) => ({ code, result: 'pass', message, sourceIds })
const assessment = (status, extra = {}) => {
  const [displayStatus, label] = { criteria_met: ['usable', '사용 가능'], complete: ['usable', '사용 가능'], partial: ['insufficient', '부족'], running: ['running', '진행'] }[status]
  return { status, displayStatus, label, reasons: [], execution: null, evidence: { available: status !== 'running', accepted: status !== 'running', reasons: [] },
    checklist: { criteriaMet: status === 'criteria_met' ? true : null, version: CHECKLIST, checks: [] }, scope: { schedulerUsed: null, reused: false, facts: [] }, ...extra }
}
const cellID = (model, cls, path, mode) => [model, cls, path, mode].join('/')

export function activeMeasurements(capture, epoch) {
  const ns = capture.meta.namespace
  const coverage = capture.json(`v1/namespaces/${ns}/measurement-coverage`, '?view=summary&summary=v2')
  const beos = capture.known(`v1/namespaces/${ns}/batch-energy-optimizations`, '?view=summary&summary=v2').items
  const classes = capture.json('v1/inventory', '?limit=200&offset=0').classes
  const nodeClass = Object.fromEntries(classes.flatMap(cls => cls.nodes.map(node => [node.name, cls.name])))
  const nodeUID = Object.fromEntries(classes.flatMap(cls => cls.nodes.map(node => [node.name, node.uid])))
  const paths = Object.fromEntries(capture.json('v1/workloads', '').map(view => [view.appliedDefinition.model.name, { workload: view.desired.metadata.name, preprocessor: view.appliedDefinition.preprocessor?.name ?? '', paths: view.appliedDefinition.model.paths }]))
  const cells = Object.fromEntries(coverage.cells.map(cell => [cell.id, cell]))
  const beoByName = Object.fromEntries(beos.map(item => [item.metadata.name, item]))
  const requestID = item => `${ns}/${item.metadata.name}/${item.metadata.uid}`
  const candidateCell = item => { const c = item.spec.state.candidate; return cellID(c.modelSpec, c.deviceClass, c.executionPath, c.powerMode) }

  // Cell selection: serving and prepared cells, the complete requests' cells, then compatible problem cells round-robin by device class.
  const used = new Set(PLACEMENTS.map(([node, ...rest]) => cellID(rest[0], nodeClass[node], rest[1], rest[2])))
  const nodeOf = {}
  for (const [node, model, path, mode] of [...PLACEMENTS, PREPARED]) nodeOf[cellID(model, nodeClass[node], path, mode)] = node
  const fixed = [...new Set([...used, cellID(PREPARED[1], nodeClass[PREPARED[0]], PREPARED[2], PREPARED[3]), ...COMPLETE.map(name => candidateCell(beoByName[name]))])]
  // Pool cells must name a captured Complete request, which the overlay then turns healthy or measuring.
  const pool = coverage.cells.filter(cell => MODELS.includes(cell.model.name) && cell.support.verdict === 'Compatible' && cell.assessment.displayStatus === 'problem' && cell.variants.length && !fixed.includes(cell.id)
    && beoByName[cell.currentMeasurement?.id.split('/')[1]]?.status?.phase === 'Complete')
  // Interleave models, and device classes within a model, so every model and class gets cells.
  const interleave = groups => { const out = []; for (let i = 0; out.length < groups.flat().length; i++) for (const group of groups) if (group[i]) out.push(group[i]); return out }
  const spread = interleave(MODELS.map(model => interleave(Object.values(Object.groupBy(pool.filter(cell => cell.model.name === model), cell => cell.deviceClass.name))))).map(cell => cell.id)
  const measuringCell = candidateCell(beoByName[MEASURING])
  const rest = spread.filter(id => id !== measuringCell), free = USABLE - fixed.length
  // The device holding the scene's measurement lease runs one measuring request of its own (the lease holder).
  const leaseCell = rest.find(id => beoByName[cells[id].currentMeasurement.id.split('/')[1]].spec.state.targetNode === MEASUREMENT_NODE)
  const others = rest.filter(id => id !== leaseCell), extra = RUNNING - 1 - (leaseCell ? 1 : 0)
  const usableIDs = fixed.concat(others.slice(0, free))
  const runningIDs = [measuringCell, ...(leaseCell ? [leaseCell] : []), ...others.slice(free, free + extra)]
  const partialIDs = others.slice(free + extra, free + extra + INSUFFICIENT)
  const completeCell = Object.fromEntries(COMPLETE.map(name => [candidateCell(beoByName[name]), beoByName[name]]))
  const measuredAt = id => seconds(epoch - 3_600_000 * (3 + parseInt(hash(id).slice(0, 4), 16) % 70))

  // Physical model: per-call time grows with batch, J per item falls with batch.
  function perf(id, batch) {
    const [model, cls, path, mode] = id.split('/')
    const device = CLASS[cls] ?? { idle: 5, cpu: [10, 4] }
    const kind = path.includes('cuda') ? 'gpu' : path.includes('rknn') ? 'npu' : 'cpu'
    const [slow, delta] = device[kind] ?? device.cpu
    const [speed, power] = /MAXN|GPU_1500|fixed|50W|25W|20W/.test(mode) ? [1, 1] : /GPU_1200|30W|15W/.test(mode) ? [1.25, 0.8] : [1.6, 0.6]
    const callMs = MODEL_MS[model] * slow * speed * (path.includes('int8') ? 0.7 : 1) * (1 + 0.55 * (batch - 1)) * jitter(`${id}/${batch}`, 0.01)
    const idle = device.idle * jitter(id, 0.02), active = idle + delta * power * (0.9 + 0.1 * Math.log2(batch) / 3) * jitter(`${id}/${batch}/w`, 0.01)
    const throughput = batch * 1000 / callMs, calls = Math.round(TRIALS * 60_000 / callMs)
    return { node: nodeOf[id], idle, active, callMs, throughput, calls, p95: callMs * 1.16, total: active / throughput, incremental: (active - idle) / throughput, memory: MEMORY[model][0] + MEMORY[model][1] * batch }
  }
  const nodeFor = cell => nodeOf[cell.id] ?? cell.support.node?.name ?? classes.find(cls => cls.name === cell.deviceClass.name)?.nodes[0]?.name
  const profileOf = cell => completeCell[cell.id] ? requestID(completeCell[cell.id]) : cell.currentMeasurement?.id ?? `${ns}/replay-${hash(cell.id).slice(0, 12)}/${uuid(cell.id)}`
  const runUID = (profile, mode, batch, attempt) => uuid(`${profile}/${mode}/${batch}/${attempt}`)
  const runEvidence = uid => ({ store: STORE, key: `profiling-runs/${uid}/execution.json`, size: 9300 + parseInt(uid.slice(0, 3), 16) })

  function curvePlan(cell, node) {
    const [model, cls, path, mode] = cell.id.split('/'), info = paths[model], spec = info?.paths.find(row => row.name === path) ?? {}
    return { name: `replay-${hash(cell.id).slice(0, 10)}`, logicalModel: model, deviceClass: cls, nodeUID: nodeUID[node] ?? '', runtimeFamily: spec.runtimeFamily ?? 'firstfloor', backend: spec.backend ?? 'ort-cpu',
      ...(spec.firstfloor ? { firstfloor: spec.firstfloor } : {}), runtimeImage: spec.image ? spec.image.replace(/\/[^/]+@.*/, '/chill-firstfloor-runtime:v0.19.2') : '',
      actor: { backend: spec.backend, artifact: spec.artifact, powerMode: mode }, powerMode: mode,
      metricSource: { sourceKind: 'edge-metrics', nodeName: node, endpoint: `http://${node}:9101/metrics`, powerMetric: 'node_wall_power_watts' },
      activeProtocol: { workloadMode: 'saturated-serial-fixed-batch', warmupIterations: 1, settlingDurationSeconds: 0, durationSeconds: 60, durationToleranceSeconds: 0.5, metricIntervalMilliseconds: 1000, metricTimeoutMilliseconds: 500, metricDurationSeconds: 64, metricObserverImage: '', workloadObserverImage: '', workloadSchemaVersion: '' },
      idleProtocol: { workloadMode: 'idle-model-loaded', warmupIterations: 0, settlingDurationSeconds: 2, durationSeconds: 10, durationToleranceSeconds: 0.5, metricIntervalMilliseconds: 1000, metricTimeoutMilliseconds: 500, metricDurationSeconds: 12, metricObserverImage: '', workloadObserverImage: '', workloadSchemaVersion: '' },
      conversion: {}, artifact: { executionPath: path, name: spec.artifact ?? '', files: spec.files ?? [], image: spec.image ?? '' },
      preprocessor: { name: info?.preprocessor ?? '', image: spec.preprocessor?.image ?? '', architecture: spec.preprocessor?.architecture ?? '', path: spec.preprocessor?.path ?? '', inputs: [], outputs: [] },
      plannedBatchSizes: BATCHES }
  }

  // Batch facts as MeasurementBatchFacts; result rows as queryapi.MeasurementResultRow (compacted).
  function facts(cell, profile, node, batch) {
    const p = perf(cell.id, batch)
    const sources = [1, 2, 3].flatMap(a => [runEvidence(runUID(profile, 'active', batch, a)), runEvidence(runUID(profile, 'idle', 1, a))]).map(({ store, key, size }) => ({ store, key, size }))
    return { batch, p95Milliseconds: round(p.p95, 2), latencySampleCount: p.calls, idleWatts: round(p.idle, 2), activeWatts: round(p.active, 2), totalJoulesPerItem: round(p.total, 5), incrementalJoulesPerItem: round(p.incremental, 5),
      confidence: { estimatorVersion: 'welch-t.v1', method: 'welch-t', level: 0.95, halfWidth: round(p.incremental * 0.04, 6) }, activeTrialCount: TRIALS, idleTrialCount: TRIALS,
      completedItems: p.calls * batch, completedCalls: p.calls, powerSource: { kind: 'edge-metrics', nodeName: node, endpoint: `http://${node}:9101/metrics`, metric: 'node_wall_power_watts' },
      quality: [], sources, missingReasons: [], phasePower: [], dispatchOccupancyNanosPerInput: [] }
  }
  const rowOf = (profile, condition, f, schedulerUsed) => ({ id: `${profile}/direct/${f.batch}`, rowKind: 'direct', profileId: profile, sourceProfileId: null, batch: f.batch, condition,
    p95Milliseconds: f.p95Milliseconds, latencySampleCount: f.latencySampleCount, idleWatts: f.idleWatts, activeWatts: f.activeWatts, totalJoulesPerItem: f.totalJoulesPerItem, incrementalJoulesPerItem: f.incrementalJoulesPerItem,
    confidence: { level: 0.95, lower: round(f.incrementalJoulesPerItem - f.confidence.halfWidth, 6), upper: round(f.incrementalJoulesPerItem + f.confidence.halfWidth, 6), halfWidth: f.confidence.halfWidth },
    activeTrialCount: TRIALS, idleTrialCount: TRIALS, completedItems: f.completedItems, completedCalls: f.completedCalls, powerSource: f.powerSource,
    assessment: assessment('criteria_met', { scope: { schedulerUsed, reused: false, facts: [] } }), sources: f.sources.map(s => `${s.store}/${s.key}`), missingReasons: [], reuse: null })
  const checks = rows => {
    const ids = rows.map(row => row.id)
    return [pass('ExactConditionLineage', '동결 조건과 원시 근거 연결', ids), pass('TargetBatch', '해당 batch 실측', ids), pass('ActiveTrials', '독립 활성 반복 수', ids), pass('IdleTrials', '공유 유휴 반복 수', ids),
      pass('IncrementalConfidence', '기존 증분 에너지 신뢰구간', ids), pass('ClientLatency', '통합 client p95와 표본 수', ids), pass('PowerSource', '실제 전력 출처', ids), pass('FrozenPowerWindowPolicy', '모든 활성·유휴 창의 동결 표본·gap·실패 정책', ids)]
  }
  const progress = (measured, extra = {}) => ({ basis: 'requested', target: BATCHES, measured, criteriaMet: measured, missing: BATCHES.filter(b => !measured.includes(b) && !(extra.inProgress ?? []).includes(b)), inProgress: [], unknown: [], ...extra })

  // Measuring request: 3 idle trials then 3 per batch; one trial completes every 45 s, looping.
  const measuringTrials = t => 4 + Math.floor((t - epoch) / 45_000) % 10
  const measuringBatches = t => BATCHES.slice(0, Math.max(0, Math.floor((measuringTrials(t) - TRIALS) / TRIALS)))

  // One measured profile per usable/insufficient cell.
  const profiles = []
  for (const id of [...usableIDs, ...partialIDs]) {
    const cell = cells[id], node = nodeFor(cell), profile = profileOf(cell), condition = curvePlan(cell, node), at = completeCell[id] ? seconds(epoch - 5_400_000) : measuredAt(id)
    const measured = partialIDs.includes(id) ? [1, 2] : BATCHES, schedulerUsed = used.has(id)
    const batchFacts = measured.map(batch => facts(cell, profile, node, batch))
    profiles.push({ id, cell, node, profile, condition, at, measured, schedulerUsed, batchFacts, rows: batchFacts.map(f => rowOf(profile, condition, f, schedulerUsed)), partial: partialIDs.includes(id) })
  }
  const profileByCell = Object.fromEntries(profiles.map(p => [p.id, p]))

  function healthyCell(p) {
    const variantID = hash(JSON.stringify({ ...p.condition, name: undefined, plannedBatchSizes: undefined }))
    const scope = { schedulerUsed: p.schedulerUsed, reused: false, facts: [] }
    const base = p.partial ? assessment('partial', { reasons: [{ code: 'TargetBatchMissing', message: '목표 배치의 측정이 부족합니다', sourceIds: [] }], scope }) : assessment('criteria_met', { scope })
    const execution = { phase: 'Complete', generation: 1, observedGeneration: 1 }
    const batchProgress = progress(p.measured)
    const best = p.rows.reduce((a, b) => b.incrementalJoulesPerItem < a.incrementalJoulesPerItem ? b : a)
    const runIds = p.measured.flatMap(b => [1, 2, 3].map(a => runUID(p.profile, 'active', b, a))).concat([1, 2, 3].map(a => runUID(p.profile, 'idle', 1, a)))
    return { ...p.cell, support: p.cell.support.verdict === 'Compatible' && p.cell.support.node?.name === p.node ? p.cell.support : { ...p.cell.support, verdict: 'Compatible', node: { name: p.node, uid: nodeUID[p.node] }, reasons: [] },
      assessment: { ...base, execution }, variantCount: 1, batchProgress, failureSummary: [],
      variants: [{ id: variantID, condition: p.condition, assessment: { ...base, execution, checklist: { ...base.checklist, checks: checks(p.rows) } }, batchProgress, resultRows: p.rows,
        requestIds: [p.profile], runIds, profileIds: [p.profile], measuredAt: p.at }],
      representative: { variantId: variantID, batch: best.batch, incrementalJoulesPerItem: best.incrementalJoulesPerItem },
      currentMeasurement: { id: p.profile, model: p.cell.model.name, deviceClass: p.cell.deviceClass.name, executionPath: p.cell.executionPath, powerMode: p.cell.powerMode, measuredAt: p.at },
      history: [{ requestId: p.profile, status: p.partial ? 'partial' : 'criteria_met' }, ...p.cell.history.filter(row => row.requestId !== p.profile)] }
  }
  function runningCell(cell, t) {
    const measured = cell.id === measuringCell ? measuringBatches(t) : [1, 2]
    const inProgress = BATCHES.slice(measured.length, measured.length + 1)
    const base = { ...assessment('running'), execution: { phase: 'Measuring', generation: 1, observedGeneration: 1 } }
    const batchProgress = progress(measured, { inProgress, missing: BATCHES.slice(measured.length + 1) })
    const request = runningRequest(cell), item = requestItem(request.split('/')[1]), runIds = item ? runsOf(item, t).map(run => run.uid) : []
    return { ...cell, assessment: base, batchProgress, failureSummary: [], variantCount: 1,
      variants: [{ ...cell.variants[0], assessment: base, batchProgress, resultRows: [], requestIds: [request], runIds, measuredAt: '0001-01-01T00:00:00Z' }],
      history: [{ requestId: request, status: 'running' }, ...cell.history.filter(row => row.requestId !== request)] }
  }
  function runningRequest(cell) { return cell.id === measuringCell ? requestID(beoByName[MEASURING]) : cell.currentMeasurement?.id ?? cell.variants[0].requestIds[0] }

  // summary and nextActions follow SummarizeMeasurementCoverage / AddCoverageActions.
  function summarize(base, rows) {
    const counts = Object.fromEntries(['usable', 'review', 'insufficient', 'problem', 'running', 'unmeasured', 'unsupported', 'unknown'].map(key => [key, 0]))
    const summary = { ...base.summary, countsByDisplayStatus: counts, supportedCellCount: 0, unsupportedCellCount: 0, unknownSupportCount: 0, completeCellCount: 0, criteriaMetCellCount: 0, reusedCellCount: 0, failedCellCount: 0, profileCount: profiles.length }
    const nextActions = [], actions = { failure: 0, 'missing-batches': 0, evidence: 0 }
    for (const cell of rows) {
      const { status, displayStatus, reasons, scope } = cell.assessment
      counts[displayStatus]++
      summary[{ Compatible: 'supportedCellCount', Incompatible: 'unsupportedCellCount' }[cell.support.verdict] ?? 'unknownSupportCount']++
      if (displayStatus === 'usable') summary.completeCellCount++
      if (status === 'criteria_met') summary.criteriaMetCellCount++
      if (scope.reused && displayStatus === 'usable') summary.reusedCellCount++
      if (displayStatus === 'problem') summary.failedCellCount++
      if (displayStatus === 'problem' && !cell.failureSummary.length && actions.failure < 3) { nextActions.push({ kind: 'failure', cellId: cell.id, variantId: null, entityId: '', reasonCode: reasons[0]?.code ?? 'MeasurementProblem', message: reasons[0]?.message ?? '문제 조건의 원인을 확인하세요' }); actions.failure++ }
      for (const failure of cell.failureSummary) if (displayStatus !== 'usable' && actions.failure < 3) { nextActions.push({ kind: 'failure', cellId: cell.id, variantId: null, entityId: failure.requestId, reasonCode: failure.code, message: failure.message }); actions.failure++ }
      for (const variant of cell.variants) if (variant.batchProgress.missing.length && actions['missing-batches'] < 3) { nextActions.push({ kind: 'missing-batches', cellId: cell.id, variantId: variant.id, entityId: '', reasonCode: 'TargetBatchMissing', message: '목표 batch의 부족 범위를 확인하세요' }); actions['missing-batches']++ }
      if (['unknown', 'review'].includes(status) && cell.support.verdict !== 'Incompatible' && actions.evidence < 3) { nextActions.push({ kind: 'evidence', cellId: cell.id, variantId: null, entityId: '', reasonCode: 'EvidenceUnconfirmed', message: status === 'review' ? '입력 계약·품질의 확인된 불일치를 상세에서 확인하세요' : '지원 입력과 동결 근거를 확인하세요' }); actions.evidence++ }
    }
    return { summary, nextActions }
  }

  // BEO overlays by request name: the profile request of every measured cell (complete), and the request of
  // every running cell (measuring: MEASURING advances one trial every 45 s, the others hold batches 1 and 2).
  const overlay = new Map(profiles.map(p => [p.profile.split('/')[1], { p }]))
  for (const id of runningIDs) {
    const name = runningRequest(cells[id]).split('/')[1]
    overlay.set(name, id === measuringCell ? { trials: measuringTrials, measured: measuringBatches } : { trials: () => TRIALS * 3, measured: () => [1, 2] })
  }
  let requestItem = name => beoByName[name]
  const execution = phase => ({ phase, generation: 1, observedGeneration: 1 })
  const beoAssessment = item => {
    const o = overlay.get(item.metadata.name)
    if (!o.p) return { ...assessment('running'), execution: execution('Measuring') }
    return { ...assessment(o.p.partial ? 'partial' : 'criteria_met', { scope: { schedulerUsed: o.p.schedulerUsed, reused: false, facts: [] } }), execution: execution('Complete') }
  }
  function beoStatus(item, t) {
    const o = overlay.get(item.metadata.name)
    if (!o.p) return { completedTrials: o.trials(t), conditions: [{ lastTransitionTime: seconds(epoch - 900_000), message: 'batch-energy-optimization measurements are in progress', observedGeneration: 1, reason: 'TrialInFlight', status: 'Unknown', type: 'MeasurementComplete' }], observedGeneration: 1, phase: 'Measuring' }
    return { completedTrials: TRIALS * (o.p.measured.length + 1), conditions: [{ lastTransitionTime: o.p.at, message: 'batch-energy-optimization result is complete', observedGeneration: 1, reason: 'ResultWritten', status: 'True', type: 'MeasurementComplete' }],
      observedGeneration: 1, outcome: 'MinimumFound/SearchSpaceExhausted', phase: 'Complete',
      result: { key: `batch-energy-optimizations/${item.metadata.uid}/result.json`, schemaVersion: 'chill.dacs.io/batch-energy-optimization-result.v1alpha5', size: 11284, store: STORE } }
  }
  const runsOf = (item, t) => {
    const name = item.metadata.name, o = overlay.get(name), profile = requestID(item), c = item.spec.state.candidate, node = o.p?.node ?? item.spec.state.targetNode
    const done = o.p ? TRIALS * (o.p.measured.length + 1) : o.trials(t), measuring = !o.p
    const trials = [[1, 'idle'], ...BATCHES.map(b => [b, 'active'])].flatMap(([batch, mode]) => [1, 2, 3].map(attempt => ({ batch, mode, attempt })))
    const first = measuring ? epoch - 900_000 : Date.parse(o.p.at) - (done + 1) * 45_000
    return trials.slice(0, Math.min(trials.length, done + (measuring ? 1 : 0))).map(({ batch, mode, attempt }, index) => {
      const uid = runUID(profile, mode, batch, attempt), running = index >= done, created = first + index * 45_000
      const runName = `${name}-${mode}-b${batch}-a${attempt}`
      const measurement = mode === 'idle' ? { batch, warmupIterations: 0, settlingDurationSeconds: 2, durationSeconds: 10, durationToleranceSeconds: 0.5, workloadMode: 'idle-model-loaded' }
        : { batch, warmupIterations: 1, durationSeconds: 60, durationToleranceSeconds: 0.5, workloadMode: 'saturated-serial-fixed-batch', dataset: item.spec.activeProtocol?.dataset }
      const job = `profilingrun-${hash(uid).slice(0, 32)}`
      const conditions = running ? [{ type: 'ExecutionComplete', status: 'Unknown', observedGeneration: 1, lastTransitionTime: seconds(created + 5_000), reason: 'JobRunning', message: 'measurement Job is running' }]
        : [{ type: 'ExecutionComplete', status: 'True', observedGeneration: 1, lastTransitionTime: seconds(created + 40_000), reason: 'JobSucceeded', message: 'measurement Job completed successfully' },
          { type: 'EvidenceVerified', status: 'True', observedGeneration: 1, lastTransitionTime: seconds(created + 40_000), reason: 'EvidenceVerified', message: 'immutable execution evidence was independently verified' }]
      const evidence = runEvidence(uid)
      return { namespace: ns, name: runName, uid, resourceVersion: String(386000000 + index), generation: 1, creationTimestamp: seconds(created),
        ownerReferences: [{ apiVersion: 'edge.dacs.io/v1alpha1', kind: 'BatchEnergyOptimization', name, uid: item.metadata.uid, controller: true, blockOwnerDeletion: true }],
        labels: { 'edge.dacs.io/attempt': String(attempt), 'edge.dacs.io/batch': String(batch), 'edge.dacs.io/batch-energy-optimization': name, 'edge.dacs.io/workload-mode': mode },
        spec: { candidate: c, targetNode: node, measurement, campaignTrial: { batch, attempt }, metricObservation: { intervalMilliseconds: 1000, timeoutMilliseconds: 500, durationSeconds: mode === 'idle' ? 12 : 64 } },
        status: { observedGeneration: 1, conditions, job: { name: job, uid: uuid(job) }, actualExecution: { pod: { name: `${job}-${hash(job).slice(0, 5)}`, uid: uuid(`${job}/pod`) }, node: { name: node, uid: nodeUID[node] } },
          ...(running ? {} : { evidence: { store: STORE, key: evidence.key, schemaVersion: 'chill.dacs.io/profiling-execution-evidence.v1alpha18', size: evidence.size }, jobCleanup: 'Done' }) },
        assessment: running ? { ...assessment('running'), execution: execution('Running') }
          : { ...assessment('complete', { scope: { schedulerUsed: null, reused: false, facts: [`Retained evidence: ${STORE}/${evidence.key}`] } }), execution: execution('Complete'), checklist: { criteriaMet: null, version: CHECKLIST, checks: [] } } }
    })
  }
  const synthRuns = t => [...overlay.keys()].flatMap(name => requestItem(name) ? runsOf(requestItem(name), t) : [])

  return {
    // Instance profile identity for a serving placement; undefined outside the selected cells.
    profileFor: (node, model, path, mode) => profileByCell[cellID(model, nodeClass[node], path, mode)]?.profile,

    coverage(base, t) {
      const rows = base.cells.map(cell => profileByCell[cell.id] ? healthyCell({ ...profileByCell[cell.id], cell }) : runningIDs.includes(cell.id) ? runningCell(cell, t) : cell)
      return { ...base, freshness: 'fresh', dataAsOf: iso(t - 4000), cells: rows, ...summarize(base, rows) }
    },

    profiles(base, q) {
      const filters = { models: 'model', deviceClasses: 'deviceClass', nodes: 'node', powerModes: 'powerMode', paths: 'path' }
      const all = profiles.map(p => ({ id: p.profile, reference: { store: STORE, key: `batch-energy-optimizations/${p.profile.split('/')[2]}/result.json`, size: 11284 },
        evidence: p.batchFacts.flatMap(f => f.sources), nodeName: p.node, condition: p.condition,
        batches: p.batchFacts.map(f => ({ batch: f.batch, energyPerInputJoules: f.incrementalJoulesPerItem, memoryBytes: Math.round(perf(p.id, f.batch).memory), memoryBasis: { observation: 'peak-working-set', evidence: f.sources[0] }, meanWallWatts: f.activeWatts })),
        confidenceLevel: 0.95, trialsPerBatch: TRIALS, idleBaselineWatts: p.batchFacts[0].idleWatts, measuredAt: p.at,
        metricSource: { kind: 'edge-metrics', nodeName: p.node, endpoint: `http://${p.node}:9101/metrics`, metric: 'node_wall_power_watts' },
        assessment: { ...healthyCell(p).variants[0].assessment }, resultRows: p.rows,
        facets: { model: p.cell.model.name, deviceClass: p.cell.deviceClass.name, node: p.node, powerMode: p.cell.powerMode, path: p.cell.executionPath } }))
      const selected = all.filter(row => (!q.get('profile') || row.id === q.get('profile')) && Object.values(filters).every(key => !q.get(key) || row.facets[key] === q.get(key))
        && (!q.get('status') || row.assessment.displayStatus === q.get('status')) && (!q.get('q') || row.id.includes(q.get('q'))))
      const offset = Number(q.get('offset')) || 0, limit = Math.min(50, Number(q.get('limit')) || 25), page = selected.slice(offset, offset + limit)
      const names = new Set(overlay.keys())
      return { ...base, insufficiencies: (base.insufficiencies ?? []).filter(row => !names.has(row.source?.Name)), preparations: {},
        profiles: page.map(({ facets: _facets, ...row }) => row),
        page: { offset, limit, total: selected.length, ...(offset + limit < selected.length ? { nextOffset: offset + limit } : {}) },
        filters: Object.fromEntries([...Object.entries(filters).map(([key, facet]) => [key, [...new Set(all.map(row => row.facets[facet]))].sort()]), ['statuses', [...new Set(all.map(row => row.assessment.displayStatus))].sort()]]) }
    },

    // Overlaid requests by name; `attach` gives the overlay the request items the caller lists.
    overlays: name => overlay.has(name),
    // UID of the measuring request that holds the MEASUREMENT_NODE lease (the real lease owner is the request UID).
    leaseHolder: () => leaseCell && runningRequest(cells[leaseCell]).split('/')[2],
    attach(lookup) { requestItem = lookup },
    // One BEO list item (plain or summary) with the active-scene status overlay.
    beoItem(item, t) {
      return overlay.has(item.metadata?.name) ? { ...item, status: beoStatus(item, t), assessment: beoAssessment(item) } : item
    },

    // Workload supportedInputs from the usable profiles of a model, grouped by execution path.
    supportedInputs(model, paths) {
      const groups = new Map()
      for (const p of profiles.filter(row => row.cell.model.name === model && !row.partial)) {
        const path = p.cell.executionPath
        if (!groups.has(path)) groups.set(path, { executionPath: path, artifact: paths.find(row => row.name === path)?.artifact ?? '', profiles: [] })
        groups.get(path).profiles.push({ id: p.profile, reference: { store: STORE, key: `batch-energy-optimizations/${p.profile.split('/')[2]}/result.json`, size: 11284 },
          evidence: p.batchFacts.flatMap(f => f.sources), condition: p.condition })
      }
      return [...groups.values()]
    },

    // BatchEnergyOptimizationPreflight for a campaign body (internal/queryapi/batch_energy_optimization_preflight_view.go).
    preflight(campaign, variantId, t) {
      const c = campaign?.spec?.state?.candidate ?? {}, node = campaign?.spec?.state?.targetNode ?? ''
      const id = cellID(c.modelSpec, c.deviceClass, c.executionPath, c.powerMode), cell = cells[id]
      const head = { apiVersion: 'console.chill.dacs.io/v1alpha1', kind: 'BatchEnergyOptimizationPreflight', readAt: iso(t), campaign: { namespace: ns, name: campaign?.metadata?.name ?? '', spec: campaign?.spec ?? {} } }
      const unresolved = reason => ({ ...head, resolves: false, reason, batchPlan: { variantId: null, conditionMatch: 'unknown', plannedBatches: [], measuredBatches: [], inProgressBatches: [], missingBatches: [], searchMayStopEarly: false, reasons: [{ code: 'PlanUnresolved', message: reason }] } })
      if (!cell) return unresolved(`no catalog support row for ${id}`)
      if (cell.support.verdict === 'Incompatible') return unresolved(`execution path ${c.executionPath} is not supported on ${c.deviceClass}`)
      if (!nodeUID[node] || nodeClass[node] !== c.deviceClass) return unresolved(`target Node ${node} is not of DeviceClass ${c.deviceClass}`)
      const plan = curvePlan(cell, node), planned = [...BATCHES, 16], p = profileByCell[id]
      const inProgress = runningIDs.includes(id) ? (id === measuringCell ? BATCHES.slice(measuringBatches(t).length, measuringBatches(t).length + 1) : [4]) : []
      const measured = p ? p.measured : runningIDs.includes(id) ? (id === measuringCell ? measuringBatches(t) : [1, 2]) : []
      const exact = measured.length > 0 || inProgress.length > 0, variant = hash(JSON.stringify({ ...plan, name: undefined, plannedBatchSizes: undefined }))
      return { ...head, resolves: true, plan: { schemaVersion: 'chill.dacs.io/curve-plan.v1alpha14', shared: { ...plan, plannedBatchSizes: planned } },
        batchPlan: { variantId: exact && !variantId ? variant : null, conditionMatch: exact ? 'exact' : 'planned-only', plannedBatches: planned, measuredBatches: measured, inProgressBatches: inProgress,
          missingBatches: planned.filter(b => !measured.includes(b) && !inProgress.includes(b)), searchMayStopEarly: true,
          reasons: [{ code: 'AutomaticBatchDiscovery', message: 'The plan bounds automatic batch discovery; completed measurements may cover only a prefix.' },
            ...(exact ? [{ code: 'DeclarationUIDsNotRetained', message: 'Measured evidence retains declaration names and generations, not their UIDs.' }] : [])] } }
    },

    // Accepted profile of a BEO by name (profile ID ns/name/uid), with its node and measured batches.
    beoProfile(name) {
      const p = profiles.find(row => row.profile.split('/')[1] === name && !row.partial)
      return p && { id: p.profile, node: p.node, nodeUID: nodeUID[p.node], batches: p.measured, model: p.cell.model.name, path: p.cell.executionPath, powerMode: p.cell.powerMode,
        runtimeImage: p.condition.runtimeImage, serviceSeconds: batch => perf(p.id, batch).callMs / 1000 }
    },

    beoResult(item, t) {
      const name = item.metadata.name, o = overlay.get(name)
      const status = beoStatus(item, t)
      const campaign = { namespace: ns, name, uid: item.metadata.uid, resourceVersion: item.metadata.resourceVersion, generation: 1, spec: item.spec, phase: status.phase, ...(status.outcome ? { outcome: status.outcome } : {}),
        completedTrials: status.completedTrials, conditions: status.conditions, storedResult: status.result ? { store: STORE, key: status.result.key, size: status.result.size } : null }
      const readAt = iso(t)
      if (!o.p) return { apiVersion: 'console.chill.dacs.io/v1alpha1', kind: 'BatchEnergyOptimizationResult', readAt, campaign, result: null, statistics: null, statisticsReason: 'NoPublishedResult',
        measurementFacts: null, measurementFactsReason: 'NoPublishedResult', assessment: { ...beoAssessment(item), scope: { schedulerUsed: false, reused: false, facts: [] } }, resultRows: [] }
      const p = o.p, c = item.spec.state.candidate
      const runs = runsOf(item, t)
      const factsBody = { request: { namespace: ns, name, uid: item.metadata.uid }, resultReference: { store: STORE, key: status.result.key, size: status.result.size }, condition: p.condition,
        pointPolicy: { version: 'point-policy.v1', minimumTrials: TRIALS, confidenceLevel: 0.95 }, batches: p.batchFacts }
      return { apiVersion: 'console.chill.dacs.io/v1alpha1', kind: 'BatchEnergyOptimizationResult', readAt, campaign,
        result: { schemaVersion: 'chill.dacs.io/batch-energy-optimization-result.v1alpha5', outcome: status.outcome,
          measurementState: { targetNode: p.node, modelSpec: c.modelSpec, deviceClass: c.deviceClass, executionPath: c.executionPath, powerMode: c.powerMode },
          curvePlan: { store: STORE, key: `batch-energy-optimizations/${item.metadata.uid}/curve-plan.json`, schemaVersion: 'chill.dacs.io/curve-plan.v1alpha14', size: 6120 },
          completedTrials: runs.map(run => ({ run: { namespace: ns, name: run.name, uid: run.uid }, measurement: { workloadMode: run.spec.measurement.workloadMode, batch: run.spec.measurement.batch },
            evidence: { store: STORE, key: run.status.evidence.key, schemaVersion: run.status.evidence.schemaVersion, size: run.status.evidence.size } })),
          excludedTrials: [], detail: { minimumBatch: p.rows.reduce((a, b) => b.incrementalJoulesPerItem < a.incrementalJoulesPerItem ? b : a).batch, searchedBatches: BATCHES } },
        statistics: { batches: p.batchFacts.map(f => ({ batch: f.batch, incrementalWorkJoulesPerItem: f.incrementalJoulesPerItem, activeTrialCount: TRIALS, idleTrialCount: TRIALS, confidence: f.confidence,
          latencySampleCount: f.latencySampleCount, latencyP95Milliseconds: f.p95Milliseconds, sources: f.sources })) },
        statisticsReason: null, measurementFacts: factsBody, measurementFactsReason: null,
        assessment: { ...beoAssessment(item), checklist: { criteriaMet: p.partial ? null : true, version: CHECKLIST, checks: checks(p.rows) } }, resultRows: p.rows }
    },

    // Synthetic child runs of every overlaid request.
    runs: synthRuns,

    // Evidence passport for a synthetic child run with evidence; undefined for other names.
    passport(name, t) {
      const run = synthRuns(t).find(row => row.name === name)
      if (!run || !run.status.evidence) return undefined
      const c = run.spec.candidate, batch = run.spec.measurement.batch, idle = run.spec.measurement.workloadMode === 'idle-model-loaded'
      const f = perf(cellID(c.modelSpec, c.deviceClass, c.executionPath, c.powerMode), batch), started = Date.parse(run.creationTimestamp) + 25_000, duration = idle ? 10 : 60, ended = started + duration * 1000
      const calls = idle ? 0 : Math.round(f.calls / TRIALS), mean = idle ? 0 : f.callMs, watts = idle ? f.idle : f.active
      const gpu = c.executionPath.includes('cuda'), node = run.spec.targetNode
      const nativeSamples = idle ? [] : Array.from({ length: 6 }, (_, i) => {
        const at = started + Math.round(i * f.callMs * 7), service = f.callMs * jitter(`${name}/${i}`, 0.05), ids = Array.from({ length: batch }, (_, k) => `${run.uid.slice(0, 8)}-${i}-${k}`)
        return { batchID: 1000 + i * 7, batchSize: batch, requestIDs: ids, recordIDs: ids.map(id => `rec-${id}`), units: ids.map(() => 1), serviceDurationNanos: Math.round(service * 1e6), precedingIdleNanos: Math.round(f.callMs * 0.04e6), trialID: run.uid,
          runnerServiceMs: round(service, 3), preprocessMs: round(service * 0.12, 3), backendCallMs: round(service * 0.8, 3), postprocessMs: round(service * 0.05, 3), nativeRequestMs: round(service * 1.03, 3),
          startedAt: iso(at), finishedAt: iso(at + service), latestEnqueuedAt: iso(at - 0.4), earliestEnqueuedAt: iso(at - 1.1) }
      })
      return { apiVersion: 'console.chill.dacs.io/v1alpha1', kind: 'EvidencePassport', readAt: iso(t),
        run: { namespace: run.namespace, name, uid: run.uid, resourceVersion: run.resourceVersion, generation: 1, spec: run.spec, conditions: run.status.conditions, job: run.status.job, actualExecution: run.status.actualExecution },
        evidence: { finalizedAt: iso(ended + 3000), final: { store: STORE, key: run.status.evidence.key, size: run.status.evidence.size }, raw: { store: STORE, key: `profiling-runs/${run.uid}/raw.json`, size: 182_000 + calls * 24 } },
        measurement: {
          runtime: { family: 'firstfloor', version: 'onnxruntime 1.20.1', provider: gpu ? 'CUDAExecutionProvider' : 'CPUExecutionProvider', architecture: c.executionPath.endsWith('amd64') ? 'amd64' : 'arm64', batch,
            warmupIterations: run.spec.measurement.warmupIterations, measurementStartedAt: iso(started), measurementEndedAt: iso(ended), elapsedSeconds: duration, completedCalls: calls, completedItems: calls * batch,
            throughputItemsPerSecond: idle ? 0 : round(calls * batch / duration, 2), latencyMilliseconds: { count: calls, mean: round(mean, 3), p50: round(mean * 0.97, 3), p95: round(f.p95, 3), p99: round(f.p95 * 1.09, 3) }, nativeSamples },
          power: { source: { kind: 'edge-metrics', nodeName: node, metric: 'node_wall_power_watts' }, startedAt: iso(started - 1000), endedAt: iso(ended + 3000), observationSeconds: duration + 4, samples: duration + 4, failures: 0,
            watts: { minimum: round(watts * 0.94, 2), mean: round(watts, 2), maximum: round(watts * 1.07, 2) }, meanRequestLatencySeconds: round(mean / 1000, 5), p95RequestLatencySeconds: round(f.p95 / 1000, 5), maximumSampleGapSeconds: 1.03, sourceTimestampAbsent: false },
          thermal: { samples: duration + 4, observationAvailableSamples: duration + 4, activeSamples: 0, latchedSamples: 0, maximumTemperatureCelsius: { samples: duration + 4, minimum: gpu ? 51 : 44, maximum: gpu ? 63 : 52 }, issues: 0 } },
        assessment: run.assessment }
    },

    // Dispatch occupancy: one complete short-burst item and one measuring item, prepended to the capture.
    occupancy(t) {
      const ic = PLACEMENTS[0], plc = PLACEMENTS[2], uidA = uuid('dom/burst/complete'), uidB = uuid('dom/burst/measuring')
      const pA = profileByCell[cellID(ic[1], nodeClass[ic[0]], ic[2], ic[3])], pB = profileByCell[cellID(plc[1], nodeClass[plc[0]], plc[2], plc[3])]
      const per = perf(pA.id, 4).callMs * 1e6 / 4, start = epoch - 2_700_000, end = start + 120_000
      const key = `serving/dispatch-occupancy-observations/${nodeUID[ic[0]]}/${uidA}.json`
      const statistics = { median: Math.round(per), p25: Math.round(per * 0.88), p75: Math.round(per * 1.13), iqr: Math.round(per * 0.25), p10: Math.round(per * 0.71), p90: Math.round(per * 1.31), mean: Math.round(per * 1.02), negativeFraction: 0.012 }
      const item = (name, uid, node, workload, profile, batch, status, extra) => ({ kind: 'DispatchOccupancyMeasurement',
        metadata: { namespace: ns, name, uid, resourceVersion: String(386100000 + batch), creationTimestamp: seconds(extra.created) }, node,
        conditions: { workload, batchEnergyOptimization: profile.split('/')[1], batch, supplyMode: 'closed-loop', warmupSeconds: 30, durationSeconds: 120, workers: batch * 2 }, status,
        references: { cr: { apiVersion: 'edge.dacs.io/v1alpha1', kind: 'DispatchOccupancyMeasurement', namespace: ns, name, uid }, evidence: extra.evidence ? [{ store: STORE, key: extra.evidence.key }] : [] },
        assessment: extra.assessment, name, uid, batchEnergyOptimization: profile.split('/')[1], workload, batch, profileID: profile, phase: status.phase, reason: '', createdAt: seconds(extra.created),
        measurementWindow: extra.window ?? null, d: null, evidence: extra.evidence ?? null, schemaVersion: extra.evidence ? 'chill.dacs.io/dispatch-occupancy-measurement.v1alpha2' : '',
        estimate: extra.estimate ?? null, verdict: extra.estimate ? 'Observed' : '', minimumPairs: 200, saturation: null, idleFraction: null })
      const pairs = 1840
      return [
        item('genesis2-burst-d40-occ-mnv2-cuda', uidA, ic[0], 'image-classification-tight-slo', pA.profile, 4, { phase: 'Complete', powerMode: ic[3], result: { store: STORE, key } }, {
          created: start - 60_000, window: { start: iso(start), end: iso(end) }, evidence: { store: STORE, key, size: 52_840 },
          estimate: { node: { name: ic[0], uid: nodeUID[ic[0]] }, workloadUID: '', model: ic[1], powerMode: ic[3], executionPath: ic[2], runtimeImage: pA.condition.runtimeImage, batch: 4,
            profile: { profileID: pA.profile, reference: { store: STORE, key: `batch-energy-optimizations/${pA.profile.split('/')[2]}/result.json`, size: 11284 }, evidence: [], meanNanos: Math.round(per * 1.04) },
            pairs, batches: 3712, completeBatches: 3698, requests: 14848, completeRequests: 14792, rawRows: 29696, completeRawRows: 29584, enrichedCompleteRawRows: 29584, finalRows: 14848, completeFinalRows: 14792,
            statistics, window: { start: iso(start), end: iso(end) } },
          assessment: { ...assessment('complete', { scope: { schedulerUsed: null, reused: false, facts: [`Observed signed ns/request estimate: ${pairs} adjacent pairs; minimumPairs: 200`] } }), execution: { phase: 'Complete', generation: 1, observedGeneration: 0 }, evidence: { available: true, accepted: false, reasons: [] } } }),
        item('jetson-nano-burst-d41-occ-plc-cuda', uidB, plc[0], 'plc-anomaly', pB.profile, 2, { phase: 'Measuring', powerMode: plc[3] }, {
          created: t - 70_000, assessment: { ...assessment('running'), execution: { phase: 'Measuring', generation: 1, observedGeneration: 0 } } }),
      ]
    },
  }
}

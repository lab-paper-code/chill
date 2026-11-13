// Synthetic 'active' scene for server/replay.mjs. Pure functions of wall-clock `t` (ms) over the
// captured data: every record (decision, action, request row, power sample) is a deterministic
// function of its own time, so the list, the single-record read, live frames and summaries agree.
// `epoch` anchors every cycle: a changed decision and a prepare action start each CYCLE.
import { fmtNanos, summaryRow, tickControl } from './replay-journal.mjs'
import { nanos } from './replay-capture.mjs'

const CYCLE = 120_000, DECIDE = 20_000, DECISION_OFFSET = 230, ACTION_OFFSET = 480, ACTION_RUN = 88_000
const PERIOD = 600_000, EVAL_CYCLE = 900_000, EVAL_STAGE = 300_000, SLO_WINDOW = 60, ROW_SLOT = 30_000, POWER_ROW = 60_000
const KEEP = '이번 판단에서는 새 변경을 선택하지 않았어요. 이전에 시작한 변경은 계속될 수 있어요.'
const RESERVATION = '이전 변경이 장치와 서비스 자원을 예약하고 있어요. 실행과 자원 반환 확인이 끝날 때까지 다른 변경에서 사용하지 않아요.'
const REASON_TEXT = { PartialObservationWindow: '유입 관측 시간이 아직 부족해 다음 관측을 기다려요.', NoPlanMeetsSLO: '검토한 구성으로는 서비스 응답 목표를 충족하지 못해요.',
  TransitionGainWithinUncertainty: '예상 절감량이 오차 범위 안이어서 현재 구성을 유지해요.' }
const SERVING = [
  { name: 'image-classification-tight-slo', k: 1, rate: 120, swing: 0.12, period: 300, p95: 0.62, p95Swing: 0.04, p95Period: 170, success: 0.996, pending: 0, path: 'firstfloor-ort-cuda-amd64',
    placements: [['genesis2', 70, 'firstfloor-ort-cuda-amd64', 16, 'GPU-3f9a1c2e-58d1-4c0b-9a51-6c1f0e2d7b44'], ['jetsono', 30, 'firstfloor-ort-cuda-arm64', 8]], answer: ['tabby cat', 'golden retriever', 'sports car', 'espresso'] },
  { name: 'plc-anomaly', k: 2, rate: 35, swing: 0.1, period: 240, p95: 1.0, p95Swing: 0.15, p95Period: 150, success: 0.985, pending: 2,
    placements: [['jetson-nano', 100, 'firstfloor-ort-cuda-rnn-arm64', 4]] },
  { name: 'smolvlm2-vqa', k: 3, rate: 0.4, swing: 0.2, period: 200, p95: 4.1, p95Swing: 0.25, p95Period: 130, success: 1, pending: 0,
    placements: [['genesis3', 100, 'firstfloor-ort-cuda-amd64', 1, 'GPU-8b2d47e0-1c3a-4f6e-b7d9-02a4c5e8f113']], answer: ['A red car is parked in front of a brick building.', 'Two people are walking a dog on the beach.'] },
]
const AUTONOMOUS = ['image-classification-tight-slo', 'plc-anomaly', 'smolvlm2-vqa', 'okdong-ae-anomaly']
// [idle, busy] watts per device class; unlisted meters keep their captured reading.
const WATTS = { genesis2: [91, 142], genesis3: [54, 112], jetsono: [31.7, 37.5], 'jetson-nano': [2.4, 6.8], 'orin-nano': [5.5, 8.6], lattepanda: [9.5, 15.2] }
const PREPARE_NODE = 'orin-nano', MEASUREMENT_NODE = 'lattepanda', FAILED_NODE = 'rasp5', GAP_NODE = 'orangepi', GAP = [1800, 1815]
const LEASE = '6f0d3b8e-2a41-4c7f-9e15-d8b3a0c47e92'
const EVALUATION = { name: 'evaluation-replay-energy-sweep', uid: '4c1d9e2a-7b35-4f08-9e61-2a8d5c0b3f17', source: 'evaluation-wbs7-shadow-20261108' }
const BLOCKED = { name: 'evaluation-replay-cleanup-blocked', uid: '9b3e61d4-2f07-4a5c-8e19-c40d7b5a2e68', source: 'eval-e5-control380-fidelity-reject' }
const PENDING = { name: 'evaluation-replay-pending', uid: 'd28c4f17-6e9a-4b30-a5d2-7f1e09c3b846', source: 'eval-e5-control379-admission-delay' }
const COMPARED_EXPERIMENT = '70974720-4177-4819-92b7-2c6340aced55'

// Go RFC3339Nano for whole milliseconds (trailing zeros trimmed).
const iso = ms => new Date(ms).toISOString().replace(/\.?0+Z$/, 'Z')
const seconds = ms => new Date(ms).toISOString().replace(/\.\d+Z$/, 'Z')
const rand = (seed, k) => { const x = Math.sin(seed * 12.9898 + k * 78.233) * 43758.5453; return x - Math.floor(x) }
const wave = (t, period) => Math.sin(2 * Math.PI * t / 1000 / period)
const round = (value, digits = 1) => Math.round(value * 10 ** digits) / 10 ** digits
const hex = (seed, k, n) => Array.from({ length: n }, (_, i) => Math.floor(rand(seed + i, k) * 16).toString(16)).join('')
const uuid = (seed, k) => `${hex(seed, k, 8)}-${hex(seed, k + 1, 4)}-4${hex(seed, k + 2, 3)}-a${hex(seed, k + 3, 3)}-${hex(seed, k + 4, 12)}`
const b64 = text => Buffer.from(text, 'utf8').toString('base64')
const quantile = (sorted, p) => sorted.length ? sorted[Math.max(0, Math.ceil(p * sorted.length) - 1)] : null

// options.profileFor(node, model, path, mode) and options.supportedInputs(model, paths) come from replay-measure.mjs.
export function activeScene(capture, epoch, options = {}) {
  const profileFor = options.profileFor ?? (() => undefined), supportedInputs = options.supportedInputs ?? (() => [])
  const snapshot = capture.frames.find(frame => frame.type === 'snapshot')
  const firstMetrics = capture.frames.find(frame => frame.type === 'metrics')
  const nodes = Object.fromEntries(snapshot.nodes.map(node => [node.name, node]))
  const ref = name => ({ uid: nodes[name].uid, name })
  const workloads = Object.fromEntries(snapshot.workloads.map(row => [row.name, row]))
  const views = Object.fromEntries(capture.json('v1/workloads', '').map(view => [view.desired.metadata.name, view]))
  const observed = Object.fromEntries(Object.values(workloads).map(row => [row.uid, capture.json(`v1/observations/workloads/${row.uid}`, '?limit=50')]))
  const services = SERVING.map(svc => {
    const row = workloads[svc.name], definition = observed[row.uid].definitions[0].definition
    return { ...svc, uid: row.uid, model: row.model, definitionID: row.definitionID, definition, percentile: row.slo.percentile, target: row.slo.targetLatencyNanos / 1e9,
      output: views[svc.name].appliedDefinition.preprocessor.output,
      instances: svc.placements.map(([node, weight, path, batch, gpu]) => ({
        id: `${svc.name}-${node}-${hex(svc.k, weight, 5)}`, node, weight, batch, gpu, path, podUID: uuid(svc.k, weight),
        profileID: profileFor(node, row.model, path, nodes[node].powerMode) ?? `${row.model}/${node}/${path}/${nodes[node].powerMode}/b${batch}`,
      })) }
  })
  const byUID = Object.fromEntries(services.map(svc => [svc.uid, svc])), byName = Object.fromEntries(services.map(svc => [svc.name, svc]))
  const ic = services[0], plc = services[1]
  const servingNodes = new Set(services.flatMap(svc => svc.instances.map(inst => inst.node)))
  // image-classification was updated (admission queue 1024 to 2048) 20 minutes before the server started.
  const updateAt = epoch - 1_200_000
  const icNext = { ...ic.definition, generation: 2, admission: { ...ic.definition.admission, maxQueuedRequests: 2048 } }
  const identity = d => `${d.uid}/${d.generation}/${d.model.uid}/${d.model.generation}/${d.preprocessor.uid}/${d.preprocessor.generation}`
  const definitionFor = (svc, t) => svc === ic && t >= updateAt ? icNext : svc.definition
  const definitionID = (svc, t) => identity(definitionFor(svc, t))

  // Decisions: changed every CYCLE, a few Pending (PartialObservationWindow), an occasional Overload, Keep otherwise.
  const decisionAt = k => epoch + k * DECIDE + DECISION_OFFSET
  const lastDecision = t => Math.floor((t - epoch - DECISION_OFFSET) / DECIDE)
  const mod = (k, n) => ((k % n) + n) % n
  const changed = k => mod(k, CYCLE / DECIDE) === 0
  const status = k => changed(k) ? 'Feasible' : [7, 8].includes(mod(k, 41)) ? 'Pending' : mod(k, 53) === 20 ? 'Overload' : 'Keep'
  const reasons = k => ({ Pending: ['PartialObservationWindow'], Overload: ['NoPlanMeetsSLO'], Feasible: [], Keep: mod(k, 4) === 1 ? ['TransitionGainWithinUncertainty'] : [] })[status(k)]
  const evaluation = k => mod(k, 90) === 45 ? 11_300_000_000 + Math.round(rand(k, 3) * 4e8) : Math.round(2e8 + rand(k, 5) * 2.3e9)
  const targetID = k => ({ Feasible: `candidate-${k}`, Pending: '', Overload: 'keep', Keep: 'keep' })[status(k)]
  const explanation = k => {
    const head = { Keep: KEEP, Pending: '운영 판단에 필요한 관측을 기다려요.', Overload: '예상 부하가 서비스 응답 목표를 넘어 확인이 필요해요.' }[status(k)] ?? `최근 판단 상태: ${status(k)}.`
    return [head, ...reasons(k).map(code => REASON_TEXT[code])].join(' ')
  }
  const decision = k => ({ at: iso(decisionAt(k)), state: status(k), reason: reasons(k).join('; '), explanation: explanation(k) })
  const actionStart = c => epoch + c * CYCLE + ACTION_OFFSET
  const cycle = t => Math.floor((t - epoch - ACTION_OFFSET) / CYCLE)
  const running = t => t < actionStart(cycle(t)) + ACTION_RUN
  const action = (c, t) => {
    const startedAt = actionStart(c), done = t >= startedAt + ACTION_RUN
    return { id: `prepare/${PREPARE_NODE}/${iso(startedAt)}`, kind: 'prepare', node: ref(PREPARE_NODE), workloadUID: ic.uid, status: done ? 'complete' : 'running',
      startedAt: iso(startedAt), ...(done ? { completedAt: iso(startedAt + ACTION_RUN) } : {}), expectedSeconds: 90, expectedSource: null }
  }
  const actionKey = row => JSON.stringify([row.id, row.node.uid, row.startedAt])
  const lease = { owner: LEASE, nodes: [ref(MEASUREMENT_NODE)], workloadUIDs: [], podUIDs: [], instances: [], reason: `measurement-lease/${LEASE}: profiling run in progress`,
    explanation: `${RESERVATION} 제공된 사유: measurement-lease/${LEASE}: profiling run in progress` }
  const reservations = t => [lease, ...(running(t) ? [{ owner: `decision/${iso(decisionAt(cycle(t) * (CYCLE / DECIDE)))}`, nodes: [ref(PREPARE_NODE)], workloadUIDs: [ic.uid], podUIDs: [], instances: [],
    reason: '', explanation: RESERVATION }] : [])]
  const workloadStates = autonomous => snapshot.workloads.map(w => !autonomous.includes(w.uid) ? { uid: w.uid, state: 'Disabled', reason: 'AutonomousControlOff' }
    : byUID[w.uid] ? { uid: w.uid, state: 'Controlled' } : { uid: w.uid, state: 'MeasurementEvidenceMissing', reason: 'MissingEvidence' })

  // Per-second request outcome for one service; withinSLO crosses the percentile exactly at the target.
  function load(svc, second) {
    const t = second * 1000, base = svc.rate * (1 + svc.swing * wave(t, svc.period))
    const received = svc.rate < 2 ? (rand(second, svc.k) < base ? 1 : 0) : Math.max(0, Math.round(base + (rand(second, svc.k) - 0.5) * 2 * Math.sqrt(base)))
    const failed = Math.min(received, Math.floor(received * (1 - svc.success) + rand(second, svc.k + 3)))
    const completed = received - failed
    const p95 = svc.p95 + svc.p95Swing * wave(t, svc.p95Period) + (rand(second, svc.k + 7) - 0.5) * svc.p95 * 0.02
    const share = Math.min(0.998, Math.max(0.6, svc.percentile / 100 + 0.4 * (1 - p95 / svc.target)))
    const withinSLO = Math.min(completed, Math.round(completed * share))
    const ms = factor => completed ? round(p95 * factor * 1000) : null
    return { received, completed, failed, withinSLO, latencySamples: completed, p50ms: ms(0.55), p90ms: ms(0.86), p95ms: ms(1), p99ms: ms(1.32), p95 }
  }
  // Window by finish time; `pending` mimics a window not yet covered (no quantileMs/met).
  function slo(svc, second, pending) {
    let samples = 0, withinTarget = 0, weighted = 0
    for (let s = second - SLO_WINDOW + 1; s <= second; s++) {
      const row = load(svc, s)
      samples += row.latencySamples; withinTarget += row.withinSLO; weighted += row.p95 * row.latencySamples
    }
    const base = { windowSeconds: SLO_WINDOW, percentile: svc.percentile, targetMs: svc.target * 1000, samples, withinTarget }
    if (!samples || pending) return base
    const quantileMs = round(weighted / samples * 1000)
    return { ...base, quantileMs, met: quantileMs <= base.targetMs }
  }
  const occupancy = (svc, inst, t) => {
    const second = Math.floor(t / 1000), share = inst.weight / 100
    const busy = Math.max(svc.rate < 2 ? 0 : 1, Math.round(svc.rate * share * svc.p95 * 0.5 / inst.batch + rand(second, svc.k + inst.weight)))
    const waiting = svc === plc ? 2 + Math.round(3 * rand(second, 11)) : Math.round(rand(second, svc.k + 5) * share * 2)
    return { running: { [svc.uid]: svc.rate < 2 ? (rand(second, 13) < 0.6 ? 1 : 0) : busy }, waiting: { [svc.uid]: waiting } }
  }

  function liveWorkloads(t, autonomous) {
    return snapshot.workloads.map(row => {
      const svc = byUID[row.uid]
      return { ...row, ...(svc ? { definitionID: definitionID(svc, t) } : {}), autonomous: autonomous.includes(row.uid), admission: !!svc, pending: svc?.pending ?? 0,
        readiness: svc ? { ready: true, reason: 'Ready', observedAt: iso(t - 2000) } : { ...row.readiness, observedAt: iso(t - 2000) } }
    })
  }
  const liveNodes = t => snapshot.nodes.map(node => ({ ...node, measurement: node.name === MEASUREMENT_NODE, observedAt: iso(decisionAt(lastDecision(t))) }))
  const liveInstances = t => services.flatMap(svc => svc.instances.map(inst => ({
    id: inst.id, node: ref(inst.node), ownerWorkloadUID: svc.uid, profileID: inst.profileID, ready: true, stopped: false,
    routable: { [svc.uid]: true }, ...occupancy(svc, inst, t), observedAt: iso(t - 700),
  })))
  const routing = () => Object.fromEntries(snapshot.workloads.map(row => [row.uid, Object.fromEntries((byUID[row.uid]?.instances ?? []).map(inst => [inst.id, inst.weight]))]))

  // Node watts per second, shared by live metrics and every power read. GAP_NODE misses GAP seconds each hour.
  const meterNames = Object.keys(firstMetrics.nodes)
  const uidName = Object.fromEntries([...snapshot.nodes.map(node => [node.uid, node.name])])
  function watts(name, uid, second) {
    const range = WATTS[name], base = firstMetrics.nodes[uid]?.watts ?? null
    if (base === null) return null
    const jitter = 0.03 * wave(second * 1000, 23 + name.length) + 0.01 * (rand(second, name.length) - 0.5)
    if (!range) return round(base * (1 + jitter), 2)
    const active = servingNodes.has(name) || name === MEASUREMENT_NODE || (name === PREPARE_NODE && running(second * 1000))
    const level = active ? range[1] * (name === 'genesis2' ? 0.9 + 0.1 * load(ic, second).received / ic.rate : 1) : range[0]
    return round(level * (1 + jitter), 2)
  }
  const missing = (name, second) => name === GAP_NODE && mod(second, 3600) >= GAP[0] && mod(second, 3600) < GAP[1]
  const activeAt = second => new Set([...servingNodes, ...(running(second * 1000) ? [PREPARE_NODE] : [])])

  // Archive events repeat every PERIOD so any recent window holds some of each kind.
  function periodic(n) {
    const at = offset => epoch + n * PERIOD + offset
    const failedStart = at(200_000), issue = 'node-agent power sample gap exceeded 5s'
    const failed = { id: `prepare/${FAILED_NODE}/${iso(failedStart)}`, kind: 'prepare', node: ref(FAILED_NODE), workloadUID: plc.uid, status: 'failed', startedAt: iso(failedStart),
      completedAt: iso(failedStart + 31_000), expectedSeconds: 45, expectedSource: null }
    const plcInst = plc.instances[0]
    return [
      { at: at(70_000), kind: 'cluster', cluster: { kind: 'issue', at: iso(at(70_000)), event: 'raised', source: `chill-system/chill-node-agent-${MEASUREMENT_NODE}`, reason: issue } },
      { at: at(130_000), kind: 'cluster', cluster: { kind: 'issue', at: iso(at(130_000)), event: 'resolved', source: `chill-system/chill-node-agent-${MEASUREMENT_NODE}`, reason: '', previousReason: issue } },
      { at: failedStart + 31_000, kind: 'action', action: { ...failed, phase: 'failed', result: { status: 'failed', error: 'PreparationFailed: readiness probe timed out after 30s' } } },
      { at: at(320_000), kind: 'cluster', cluster: { kind: 'pod', at: iso(at(320_000)), event: 'changed', pod: { namespace: 'chill-system', name: plcInst.id, uid: plcInst.podUID, node: plcInst.node, phase: 'Running', ready: 'True' } } },
      { at: at(450_000), kind: 'cluster', cluster: { kind: 'pod', at: iso(at(450_000)), event: 'changed', pod: { namespace: 'chill-system', name: `profilingrun-replay-${hex(n, 9, 6)}`, uid: uuid(n, 9), node: MEASUREMENT_NODE, phase: 'Running', ready: 'True' } } },
    ]
  }

  // Expected joules of decision k's keep and target candidates (target absent while Pending; a keep target is the keep plan).
  const energies = k => {
    const s = Math.floor(decisionAt(k) / 1000), keep = round(sumWatts(s - 60, s, () => true) * 10, 1)
    return { keep, target: status(k) === 'Feasible' ? round(keep * 0.972, 1) : status(k) === 'Pending' ? undefined : keep }
  }
  // Full serving.SchedulerDecision for decision k.
  function fullDecision(k, autonomous, settings) {
    const t = decisionAt(k), s = Math.floor(t / 1000), st = status(k)
    const estimate = (svc, factor) => ({ uid: svc.uid, definition: definitionID(svc, t), samples: 400 + Math.round(rand(k, svc.k) * 600), percentile: svc.percentile,
      expectedNanos: Math.round(load(svc, s).p95 * factor * 1e9), targetNanos: svc.target * 1e9, normalizedExceedance: Math.max(0, round(load(svc, s).p95 * factor / svc.target - 1, 4)), predictedOutcomes: {} })
    const instances = services.flatMap(svc => svc.instances.map(inst => ({ id: inst.id, node: ref(inst.node), ownerWorkloadUID: svc.uid, workloadUID: svc.uid, profileID: inst.profileID, ready: true, stopped: false, routable: { [svc.uid]: true } })))
    const { keep: joules, target: targetJoules } = energies(k)
    const keep = { id: 'keep', keep: true, nodes: [], instances, routing: routing(), actions: [], status: st === 'Overload' ? 'Overload' : 'Feasible', reasons: [],
      slo: services.map(svc => estimate(svc, st === 'Overload' && svc === plc ? 1.25 : 1)), expectedJoules: joules, worstExceedance: st === 'Overload' ? 0.25 : 0 }
    const target = st === 'Feasible' ? { ...keep, id: `candidate-${k}`, keep: false, actions: [{ id: `prepare/${PREPARE_NODE}/${iso(actionStart(Math.floor(k / (CYCLE / DECIDE))))}`, kind: 'prepare',
      node: ref(PREPARE_NODE), workloadUID: ic.uid, dependsOn: [], expectedSeconds: 90 }], slo: services.map(svc => estimate(svc, svc === ic ? 0.94 : 1)), expectedJoules: targetJoules }
      : st === 'Pending' ? null : keep
    const loads = services.map(svc => { let received = 0; for (let i = s - 60; i < s; i++) received += load(svc, i).received; const accepted = Math.round(received * svc.success)
      return { uid: svc.uid, received, accepted, computeInputs: accepted, inputsPerSecond: round(accepted / 60, 3), recentComputeInputs: Math.round(accepted / 6) } })
    const evaluate = evaluation(k)
    return { at: iso(t), status: st, keep, target, reasons: reasons(k).length ? reasons(k) : null, settings,
      search: { examined: 40 + mod(k, 30), bounds: settings, windowStart: iso(t - 60_000), windowEnd: iso(t), recentWindowStart: iso(t - 10_000),
        received: loads.reduce((a, b) => a + b.received, 0), accepted: loads.reduce((a, b) => a + b.accepted, 0), computeInputs: loads.reduce((a, b) => a + b.computeInputs, 0),
        population: 'received events in [windowStart,windowEnd); validated compute arrivals (including capacity/evidence rejections) replayed; outstanding retained independently',
        loads, comparedNodes: [...servingNodes, PREPARE_NODE].map(ref) },
      workloads: workloadStates(autonomous), references: [],
      capacity: { periodNanos: 600e9, routeNanos: 60e9, search: { strategy: settings.planSearchStrategy ?? 'v2', neighborhood: 2, evaluated: 40 + mod(k, 30), durationNanos: Math.round(evaluate * 0.7), budgetCut: false,
        surrogateElapsedNanos: Math.round(evaluate * 0.4), exactElapsedNanos: Math.round(evaluate * 0.25) },
        workloads: services.map(svc => ({ uid: svc.uid, rate: round(svc.rate * (1 + svc.swing * wave(t, svc.period)), 3), minimumDemand: settings.minimumDemand?.[svc.uid] ?? 0, queued: svc.pending,
          routingRate: round(svc.rate * 1.2, 3), retainedRoutingRate: round(svc.rate * 0.6, 3), tests: [], keepTests: [], weights: Object.fromEntries(svc.instances.map(inst => [inst.id, inst.weight])) })),
        plans: snapshot.nodes.map(node => { const eligible = servingNodes.has(node.name) || node.name === PREPARE_NODE
          return { node: ref(node.name), key: `${node.name}/${node.powerMode}`, eligible, ...(eligible ? {} : { reason: node.name === MEASUREMENT_NODE ? 'ReservedScope' : 'NotSelected' }),
            preNanos: 0, postNanos: 0, joules: round(watts(node.name, node.uid, s) * 600, 1) } }),
        energyComparison: { status: 'decidable' } },
      evaluationElapsedNanos: evaluate,
      evaluationTiming: { preparationNanos: Math.round(evaluate * 0.08), searchNanos: Math.round(evaluate * 0.7), paybackNanos: Math.round(evaluate * 0.05), ratesNanos: Math.round(evaluate * 0.1),
        projectionNanos: Math.round(evaluate * 0.05), surrogateElapsedNanos: Math.round(evaluate * 0.4), exactElapsedNanos: Math.round(evaluate * 0.25) } }
  }
  function sumWatts(from, to, include) {
    let total = 0
    for (let s = from; s < to; s++) for (const uid of meterNames) { const name = uidName[uid] ?? 'genesis1'; if (include(name, s)) total += watts(name, uid, s) ?? 0 }
    return total / Math.max(1, to - from)
  }

  // Scheduler journal records in [fromN, toN): decisions (+ tick and round control), actions, settings, power.
  function journal(fromN, toN, autonomous, settings, history) {
    const from = Number(fromN / 1_000_000n), to = Number(toN / 1_000_000n) + 1, out = []
    const record = (atMs, kind, full, summary = summaryRow(full), atText = iso(atMs)) => out.push({ at: atText, atN: nanos(atText), kind, summary, compact: () => full, full: () => full })
    for (let k = lastDecision(from - 15_000); decisionAt(k) < to + 15_000; k++) {
      const at = iso(decisionAt(k)), atN = nanos(at)
      let full
      const get = () => full ??= { at, decision: fullDecision(k, autonomous, settings), snapshot: { at: iso(decisionAt(k) - 120), workloads: liveWorkloads(decisionAt(k), autonomous), nodes: liveNodes(decisionAt(k)),
        instances: liveInstances(decisionAt(k)), routing: routing(), reservations: reservations(decisionAt(k)), actions: [action(cycle(decisionAt(k)), decisionAt(k))] } }
      const energy = energies(k)
      const summaryDecision = { at, status: status(k), workloads: workloadStates(autonomous), ...(reasons(k).length ? { reasons: reasons(k) } : {}),
        ...(status(k) === 'Pending' ? {} : { targetKeep: status(k) !== 'Feasible' }), keepExpectedJoules: energy.keep, ...(energy.target !== undefined ? { targetExpectedJoules: energy.target } : {}), evaluationElapsedNanos: evaluation(k) }
      out.push({ at, atN, kind: 'decision', summary: { at, decision: summaryDecision, snapshot: { at: iso(decisionAt(k) - 120) } }, compact: get, full: get })
      const tick = { at: '', control: tickControl(atN, BigInt(evaluation(k)), status(k), targetID(k)) }
      tick.at = tick.control.finishedAt
      out.push({ at: tick.at, atN: nanos(tick.at), kind: 'other', summary: tick, compact: () => tick, full: () => tick })
      const roundAt = nanos(tick.at) + 2_000_000n
      const roundRow = { at: fmtNanos(roundAt), control: { ...tick.control, scope: 'round', decisionAt: '0001-01-01T00:00:00Z', startedAt: fmtNanos(nanos(tick.control.tickAt) - 530_000_000n),
        finishedAt: fmtNanos(roundAt), elapsedNanos: Number(roundAt - nanos(tick.control.tickAt)) + 530_000_000, phase: 'complete', status: undefined, targetID: undefined, admitNanos: 532_434_105,
        tickNanos: Number(nanos(tick.at) - nanos(tick.control.tickAt)), captureNanos: 0, windowNanos: 0, evaluateNanos: 0 } }
      out.push({ at: roundRow.at, atN: roundAt, kind: 'other', summary: roundRow, compact: () => roundRow, full: () => roundRow })
    }
    for (let c = cycle(from - ACTION_RUN); actionStart(c) < to; c++) {
      // `expected` is the recorded state.Operation; the summary row takes its workload UIDs.
      const a = action(c, actionStart(c) + ACTION_RUN), instance = `${ic.name}-${PREPARE_NODE}-${hex(c, 4, 5)}`
      const expected = { id: a.id, kind: 'prepare', node: a.node, instance, policyInstances: null, workloadUID: ic.uid, workloadUIDs: [ic.uid], expectedSeconds: 90, expectedSource: null }
      const base = { id: a.id, kind: 'prepare', node: a.node, instance, startedAt: a.startedAt, expected, expectedSeconds: 90, expectedSource: null }
      record(actionStart(c), 'action', { at: a.startedAt, action: { ...base, phase: 'running' } })
      record(actionStart(c) + ACTION_RUN, 'action', { at: a.completedAt, action: { ...base, phase: 'complete', completedAt: a.completedAt } })
    }
    for (let n = Math.floor((from - epoch) / PERIOD) - 1; epoch + n * PERIOD < to; n++) for (const row of periodic(n).filter(row => row.kind === 'action')) {
      const a = row.action
      const expected = { id: a.id, kind: a.kind, node: a.node, policyInstances: null, workloadUID: a.workloadUID, workloadUIDs: [a.workloadUID], expectedSeconds: a.expectedSeconds, expectedSource: null }
      record(row.at, 'action', { at: iso(row.at), action: { id: a.id, kind: a.kind, node: a.node, phase: 'failed', startedAt: a.startedAt, completedAt: a.completedAt, expected, error: a.result.error, expectedSeconds: a.expectedSeconds, expectedSource: null } })
    }
    for (const row of history) record(row.at, 'settings', { at: iso(row.at), settings: row.change })
    const meters = powerMeters()
    for (let at = Math.floor(from / POWER_ROW) * POWER_ROW + 703; at < to; at += POWER_ROW) for (const meter of meters) {
      const s = Math.floor(at / 1000), name = meter.node.name
      if (missing(name, s)) continue
      let joules = 0
      for (let i = s - 5; i < s; i++) joules += watts(name, meter.node.uid, i) ?? 0
      record(at, 'power', { at: iso(at), power: { startedAt: iso(at - 5000) }, node: meter.node, meter: meter.meter, powerScope: meter.scope, powerJoules: round(joules, 6) })
    }
    return out.filter(row => row.atN >= fromN && row.atN < toN).sort((a, b) => a.atN < b.atN ? -1 : a.atN > b.atN ? 1 : 0)
  }

  // Power: captured meter identities, synthesized readings.
  let meterCache
  const powerMeters = () => meterCache ??= capture.json('v1/observations/power', '').nodes.map(node => ({ node: node.node, meter: node.meter, scope: node.scope, sourceIdentity: node.sourceIdentity }))
  // One OperatingPowerWindow over [from, to) ms; seconds at or after `now - 1 s` are not yet retained.
  function powerWindow(from, to, now, stride = 1) {
    const horizon = Math.floor((now - 1000) / 1000), first = Math.ceil(from / 1000), last = Math.ceil(to / 1000)
    // One sample per whole second in [from, to); energy and mean power use that same span.
    const covered = last <= horizon + 1, duration = Math.max(1, last - first)
    let whole = 0, activeJ = 0, wholeOk = covered, activeOk = covered
    const nodesOut = powerMeters().map(meter => {
      const name = meter.node.name, uid = meter.node.uid
      let joules = 0, samples = 0, gapStart = null, gapSeconds = 0, gapCount = 0, maxGap = 1.0, firstAt = null, lastAt = null
      for (let s = first; s < Math.min(last, horizon + 1); s += stride) {
        if (missing(name, s)) { if (gapStart === null) { gapStart = s; gapCount++ } gapSeconds += stride; continue }
        if (gapStart !== null) { maxGap = Math.max(maxGap, s - gapStart + 1); gapStart = null }
        const w = watts(name, uid, s) ?? 0
        joules += w * stride; samples++; firstAt ??= s; lastAt = s
        if (activeAt(s).has(name)) activeJ += w * stride
      }
      if (gapStart !== null) maxGap = Math.max(maxGap, Math.min(last, horizon + 1) - gapStart + 1)
      const gapFails = gapSeconds > 10 && stride === 1, complete = covered && !gapFails
      const reasons = !covered ? ['raw history for selected interval is incomplete'] : gapFails ? ['maximum sample gap exceeds policy'] : []
      if (!complete) { wholeOk = false; if (activeAt(first).has(name)) activeOk = false }
      whole += joules
      const value = complete ? round(joules, 3) : null
      return { node: meter.node, meter: meter.meter, scope: meter.scope, meterJoules: value, meterComplete: complete, gapCount, gapSeconds: round(gapSeconds, 3), gapFraction: round(gapSeconds / Math.max(1, duration), 6),
        sourceIdentity: meter.sourceIdentity, joules: value, meanWatts: complete ? round(joules / duration, 3) : null, samples, readFailures: 0, maximumSampleGapSeconds: round(maxGap + 0.03, 3),
        sourceTimestampAbsent: true, observedSamples: samples, ...(firstAt !== null ? { firstObservedAt: iso(firstAt * 1000 + 970), lastObservedAt: iso(lastAt * 1000 + 970) } : {}), reasons }
    })
    const windowReasons = nodesOut.filter(node => node.reasons.length).map(node => `Node ${node.node.name} (${node.node.uid}): ${node.reasons[0]}`)
    const fromText = iso(from), toText = iso(to)
    return { from: fromText, to: toText, scope: 'whole-pool-wall', nodes: nodesOut, wholePoolJoules: wholeOk ? round(whole, 3) : null, wholePoolMeanWatts: wholeOk ? round(whole / duration, 3) : null,
      activePoolJoules: activeOk ? round(activeJ, 3) : null, activePoolMeanWatts: activeOk ? round(activeJ / duration, 3) : null, complete: wholeOk, readFailures: 0,
      maximumSampleGapSeconds: Math.max(...nodesOut.map(node => node.maximumSampleGapSeconds)), reasons: windowReasons,
      coverage: { from: fromText, to: toText, sources: [{ from: fromText, to: toText, stream: 'scheduler', source: 'local' }], gaps: [] } }
  }

  // Request rows: one per service every ROW_SLOT, plus rows recorded by inference calls.
  function requestRow(svc, receivedAt, seed, outcome) {
    const t = receivedAt, s = Math.floor(t / 1000), d = definitionFor(svc, t), inst = svc.instances.find((_i, index, all) => rand(seed, 31) * 100 < all.slice(0, index + 1).reduce((sum, row) => sum + row.weight, 0)) ?? svc.instances[0]
    const id = `${svc.uid.slice(0, 8)}-${Math.round(receivedAt).toString(36)}-${hex(seed, 3, 4)}`
    const lifetime = Math.round(svc.target * 3e9), base = { kind: 'final', requestID: id, workloadUID: svc.uid, definitionID: identity(d), receivedAt: iso(t), lifetimeNanos: lifetime, completionMarginMS: 29, admissionPolicy: d.admission.policy }
    const validated = 310_000 + Math.round(rand(seed, 2) * 60_000), accepted = validated + 210_000
    const timing = (stages, offsets) => stages.map((stage, i) => ({ stage, elapsedNanos: offsets[i] }))
    if (outcome === 'rejected') {
      const code = svc === plc && rand(seed, 9) < 0.5 ? 'DeadlineInfeasible' : 'QueueFull', elapsed = accepted + 400_000
      return { ...base, outcome: 'rejected', error: code, rejection: { code, stage: code === 'QueueFull' ? 'gateway' : 'executor', policy: d.admission.policy, ...(code === 'DeadlineInfeasible' ? { predictedCompletionNanos: Math.round(svc.target * 1.4e9) } : {}), remainingSLONanos: Math.round(svc.target * 1e9) - elapsed },
        elapsedNanos: elapsed, validatedNanos: validated, executionComplete: false, serverTiming: timing(['receiveDone', 'authDone', 'bodyDecodeDone', 'inputValidateDone', 'validatedEventDone', 'acceptDone', 'responseWriteStart', 'responseWriteDone'],
          [40_000, 290_000, validated - 60_000, validated - 20_000, validated, accepted, accepted + 100_000, elapsed - 50_000]) }
    }
    const z = Math.sqrt(-2 * Math.log(rand(seed, 47) || 1e-9)) * Math.cos(2 * Math.PI * rand(seed, 53)), sigma = Math.log(1 / 0.55) / 1.645
    const p95 = load(svc, s).p95, dispatched = accepted + 880_000, queued = dispatched + 600_000
    const elapsed = outcome === 'timeout' ? lifetime : Math.max(dispatched + 4_000_000, Math.round(p95 * 0.55 * Math.exp(z * sigma) * 1e9))
    const wait = Math.round((elapsed - dispatched) * (0.15 + 0.12 * rand(seed, 59))), runningAt = queued + wait
    const service = Math.max(1_000_000, elapsed - runningAt - 1_100_000), executed = runningAt + service, exchange = executed + 400_000
    const pre = service * 0.12, post = service * 0.05, backend = service - pre - post, batchSize = Math.max(1, Math.round(inst.batch * (0.3 + 0.7 * rand(seed, 61))))
    const ms = value => round(value / 1e6, 3), at = offset => iso(t + offset / 1e6)
    const common = { ...base, node: ref(inst.node), instance: inst.id, profileID: inst.profileID, decisionID: iso(decisionAt(lastDecision(t))), dispatchedAt: at(dispatched),
      validatedNanos: validated, acceptedNanos: accepted, dispatchedNanos: dispatched, queuedNanos: queued, gatewayWaitNanos: dispatched - accepted, executorWaitNanos: wait,
      batchID: 1_000_000 + Math.floor(t / 7), batchSize, nativeQueuedAt: at(queued), nativeRunningAt: at(runningAt) }
    if (outcome === 'timeout') return { ...common, outcome: 'timeout', error: 'RequestTimeout', elapsedNanos: elapsed, runningNanos: runningAt, executionComplete: false, queueMS: ms(wait),
      serverTiming: timing(['receiveDone', 'authDone', 'inputValidateDone', 'validatedEventDone', 'acceptDone', 'assigned', 'executeStart', 'workerHTTPStart', 'responseWriteStart', 'responseWriteDone'],
        [40_000, 290_000, validated - 20_000, validated, accepted, dispatched - 100_000, dispatched, dispatched + 120_000, elapsed - 200_000, elapsed - 50_000]) }
    if (outcome === 'error') return { ...common, outcome: 'error', error: 'BackendError', elapsedNanos: runningAt + 2_000_000, runningNanos: runningAt, executionComplete: false, nativeOutcome: 'error', nativeError: 'runner exited: CUDA error: out of memory',
      nativeExecutionComplete: false, queueMS: ms(wait), serverTiming: timing(['receiveDone', 'authDone', 'inputValidateDone', 'validatedEventDone', 'acceptDone', 'assigned', 'executeStart', 'workerHTTPStart', 'workerHTTPDone', 'replyReceived', 'responseWriteDone'],
        [40_000, 290_000, validated - 20_000, validated, accepted, dispatched - 100_000, dispatched, dispatched + 120_000, runningAt + 1_500_000, runningAt + 1_600_000, runningAt + 1_900_000]) }
    return { ...common, outcome: 'completed', executionComplete: true, elapsedNanos: elapsed, runningNanos: runningAt, executedNanos: executed, exchangeNanos: exchange,
      nativeFinishedAt: at(executed), nativeExecutionComplete: true, nativeOutcome: 'completed', queueMS: ms(wait), serviceMS: ms(service), backendMS: ms(backend), preprocessMS: ms(pre), postprocessMS: ms(post),
      ...(svc.name === 'smolvlm2-vqa' ? { firstTokenMS: ms(pre + backend * 0.35) } : {}), units: 1, nativeElapsedMS: ms(executed - dispatched), serviceDurationNanos: Math.round(service), precedingIdleNanos: Math.round(rand(seed, 71) * 3e6),
      serverTiming: timing(['receiveDone', 'authDone', 'bodyDecodeDone', 'inputValidateDone', 'validatedEventDone', 'acceptDone', 'assigned', 'executeStart', 'workerHTTPStart', 'workerHTTPDone', 'replyReceived', 'responseWriteStart', 'responseWriteDone'],
        [40_000, 290_000, validated - 60_000, validated - 20_000, validated, accepted, dispatched - 100_000, dispatched, dispatched + 120_000, exchange, exchange + 90_000, elapsed - 300_000, elapsed - 60_000]) }
  }
  function slotRow(svc, slot) {
    const receivedAt = slot * ROW_SLOT + Math.floor(rand(slot, svc.k) * (ROW_SLOT - 1000)), u = rand(slot, svc.k + 13)
    const outcome = u < (svc === plc ? 0.05 : 0.012) ? 'rejected' : u < (svc === plc ? 0.08 : 0.018) ? 'timeout' : u < (svc === plc ? 0.095 : 0.024) ? 'error' : 'completed'
    return requestRow(svc, receivedAt, slot * 7 + svc.k, outcome)
  }
  // Management events for image-classification's admission update.
  function management() {
    const before = ic.definition, after = icNext, actor = { actorUsername: 'operator@chill.local', actorUID: '2b7c9e41-5d08-4f3a-a6e1-9c0d47b2f815' }
    return [
      { workloadUID: ic.uid, kind: 'Update', phase: 'Accepted', ...actor, before, target: after, at: iso(updateAt) },
      { workloadUID: ic.uid, kind: 'Update', phase: 'Draining', ...actor, after: before, at: iso(updateAt + 2100) },
      { workloadUID: ic.uid, kind: 'Update', phase: 'Preparing', ...actor, after: before, at: iso(updateAt + 9400) },
      { workloadUID: ic.uid, kind: 'Update', phase: 'Applied', ...actor, before, target: after, after, at: iso(updateAt + 41_800) },
      { workloadUID: ic.uid, kind: 'Update', phase: 'Completed', ...actor, target: after, after, at: iso(updateAt + 42_300) },
    ]
  }

  // Evaluations: one running (the active evaluation), one blocked on cleanup, one pending.
  let evaluationSources
  const evaluationSource = name => (evaluationSources ??= Object.fromEntries(capture.json('v1/evaluations', '').items.map(item => [item.name, item])))[name]
  const runStages = () => evaluationSource(EVALUATION.source).spec.stages.slice(0, 3)
  const evalStart = t => { const anchor = epoch + 40_000 - EVAL_STAGE - 240_000; return anchor + Math.floor((t - anchor) / EVAL_CYCLE) * EVAL_CYCLE }
  const condition = (type, statusValue, reason, at, message = '') => ({ type, status: statusValue, observedGeneration: 1, lastTransitionTime: seconds(at), reason, message })
  function runningEvaluation(t) {
    const source = evaluationSource(EVALUATION.source), start = evalStart(t), index = Math.floor((t - start) / EVAL_STAGE), stages = runStages()
    const provisional = source.stages.find(stage => stage.provisionalD1)?.provisionalD1
    return { name: EVALUATION.name, uid: EVALUATION.uid, spec: { ...source.spec, stages, question: 'Energy sweep across three load rows with autonomous control on.' },
      conditions: [condition('PoolAcquired', 'True', 'Acquired', start)], currentStage: stages[index].id, currentProcedure: 'stage',
      procedures: [{ step: 'prepare', startedAt: seconds(start), finishedAt: seconds(start + 14_000), reason: 'ProcedureComplete' }, { step: 'stage', startedAt: seconds(start + 15_000), finishedAt: null }],
      stages: stages.map((stage, i) => {
        const begin = start + i * EVAL_STAGE, go = begin + 5000, done = i < index, current = i === index, name = `eval-${EVALUATION.uid.replace(/-/g, '')}-s00${i}`
        const phases = [['loadStarted', go], ['loadEnded', go + 270_000], ['drainStarted', go + 270_000], ['drainEnded', go + 280_000], ['collectStarted', go + 280_000], ['collectEnded', go + 290_000]]
        const progress = done ? phases : current ? phases.filter(([, at]) => at <= t) : []
        return { id: stage.id, name, uid: uuid(i, 77), conditions: done ? [condition('ExecutionComplete', 'True', 'Complete', go + 290_000), condition('MeasurementPassed', 'True', 'MeasurementComplete', go + 292_000), condition('StageCleanupComplete', 'True', 'ProcedureComplete', go + 294_000)] : [],
          ...(done ? { measurementVerdict: 'PASS', currentProcedure: 'cleanup-jobs' } : current ? { currentProcedure: 'stage' } : {}),
          procedures: done ? [{ step: 'factor', startedAt: seconds(begin), finishedAt: seconds(go), reason: 'ProcedureComplete' }, { step: 'stage', startedAt: seconds(go), finishedAt: seconds(go + 290_000), reason: 'ProcedureComplete' },
            { step: 'judge', startedAt: seconds(go + 290_000), finishedAt: seconds(go + 292_000), reason: 'ProcedureComplete', measurementVerdict: 'PASS' }, { step: 'cleanup-jobs', startedAt: seconds(go + 292_000), finishedAt: seconds(go + 294_000), reason: 'ProcedureComplete' }]
            : current ? [{ step: 'factor', startedAt: seconds(begin), finishedAt: seconds(go), reason: 'ProcedureComplete' }, { step: 'stage', startedAt: seconds(go), finishedAt: null }] : [],
          startedAt: done || current ? seconds(begin) : null, finishedAt: done ? seconds(go + 294_000) : null, goAt: done || current ? seconds(go) : null,
          ...(current && provisional ? { provisionalD1: provisionalD1(provisional, (t - go) / 270_000, t) } : {}),
          progress: progress.map(([phase, at]) => ({ phase, at: iso(at) })), ...(progress.length ? { lastProgressAt: iso(progress.at(-1)[1]) } : {}),
          loadJobs: done || current ? [{ name: `${name}-load`, uid: uuid(i, 79), status: done ? 'Succeeded' : 'Active', startedAt: seconds(go), ...(done ? { completedAt: seconds(go + 280_000), reason: 'Completed' } : {}) }] : [] }
      }),
      startedAt: seconds(start), phase: 'Running', liveTarget: source.liveTarget ?? [] }
  }
  function blockedEvaluation(released) {
    const source = evaluationSource(BLOCKED.source), start = epoch - 3_000_000, end = start + 1_900_000
    return { ...source, name: BLOCKED.name, uid: BLOCKED.uid, publicationID: undefined, phase: released ? 'Done' : 'Blocked', startedAt: seconds(start), finishedAt: released ? seconds(released.at) : undefined,
      conditions: [released ? condition('PoolAcquired', 'False', 'OperatorReleased', released.at, `released by replay-operator: ${released.reason}`) : condition('PoolAcquired', 'True', 'Acquired', start),
        condition('ExecutionComplete', 'True', 'Complete', end), condition('MeasurementPassed', 'True', 'MeasurementComplete', end + 4000),
        condition('CleanupComplete', 'False', 'ResidualState', end + 60_000, `load Job ${BLOCKED.name}-s001-load still has a running Pod on genesis2; a replay instance keeps 1 GPU allocation`)],
      currentProcedure: 'cleanup-jobs',
      stages: (source.stages ?? []).map((stage, i) => ({ ...stage, uid: uuid(i, 83), name: `eval-${BLOCKED.uid.replace(/-/g, '')}-s00${i}`,
        loadJobs: [{ name: `${BLOCKED.name}-s00${i}-load`, uid: uuid(i, 85), status: i === (source.stages.length - 1) ? 'Active' : 'Succeeded', startedAt: seconds(start + i * 900_000),
          ...(i === source.stages.length - 1 ? {} : { completedAt: seconds(start + i * 900_000 + 880_000), reason: 'Completed' }) }] })) }
  }
  function pendingEvaluation() {
    const source = evaluationSource(PENDING.source)
    return { name: PENDING.name, uid: PENDING.uid, spec: { ...source.spec, question: 'Pending replay run awaiting the evaluation pool.' }, conditions: [], procedures: [], stages: [], phase: 'Pending', liveTarget: [] }
  }
  // EvaluationProvisionalD1 of the running stage: the captured snapshot's per-service shape, counts scaled to
  // the elapsed share of the load (offered = scheduled arrivals so far), rates and latencies kept.
  let provisionalTemplate
  function provisionalD1(source, share, t) {
    provisionalTemplate ??= capture.json('v1/evaluations', '').items.flatMap(item => (item.stages ?? []).map(stage => stage.provisionalD1)).find(p => p && Object.values(p.workloads ?? {}).some(w => w.outcomeClasses?.onTime > 0))
    const template = provisionalTemplate ?? source, f = Math.min(1, Math.max(0.02, share)), scale = n => Math.round(n * f)
    const counts = value => Object.fromEntries(Object.entries(value).map(([key, n]) => [key, typeof n === 'number' ? scale(n) : n]))
    const stats = value => ({ ...value, samples: scale(value.samples) })
    const metrics = w => ({ ...w, outcomeClasses: counts(w.outcomeClasses), timeoutCauses: counts(w.timeoutCauses), loadLengthSeconds: round(w.loadLengthSeconds * f, 3),
      completedLatencyNS: stats(w.completedLatencyNS), gatewayWaitNS: stats(w.gatewayWaitNS), executorWaitNS: stats(w.executorWaitNS),
      rejections: (w.rejections ?? []).map(r => ({ ...r, count: scale(r.count), gatewayWaitNS: stats(r.gatewayWaitNS), executorWaitNS: stats(r.executorWaitNS) })),
      ...('offered' in w ? { offered: scale(w.offered), deadlineSuccess: scale(w.deadlineSuccess), requiredSuccessfulRank: scale(w.requiredSuccessfulRank) } : {}) })
    const workloads = Object.fromEntries(Object.keys(source.workloads ?? template.workloads).map(name => [name, template.workloads[name] ?? Object.values(template.workloads)[0]]).map(([name, w]) => [name, {
      ...metrics(w), outcomes: counts(w.outcomes ?? {}), unfinishedOrUnsent: scale(w.unfinishedOrUnsent ?? 0), substages: (w.substages ?? []).map(metrics) }]))
    const generatorQuality = Object.fromEntries(Object.keys(workloads).map(name => { const g = template.generatorQuality?.[name] ?? Object.values(template.generatorQuality ?? {})[0]
      return g ? [name, { ...g, offered: scale(g.offered), issued: scale(g.issued), unissued: scale(g.unissued), durationNS: Math.round(g.durationNS * f), sendLatenessNS: stats(g.sendLatenessNS), actualGapNS: stats(g.actualGapNS), plannedGapNS: stats(g.plannedGapNS),
        generatorQuality: { ...g.generatorQuality, denominator: scale(g.generatorQuality?.denominator ?? 0), exceeding: scale(g.generatorQuality?.exceeding ?? 0) } }] : null }).filter(Boolean))
    return { asOf: iso(t - 4000), closedThrough: seconds(Math.floor((t - 4000) / 60_000) * 60_000), provisional: true, workloads, generatorQuality }
  }

  return {
    services: () => services.map(svc => ({ name: svc.name, uid: svc.uid })),
    settings: settings => ({ ...settings, autonomousWorkloads: AUTONOMOUS.map(name => workloads[name].uid) }),
    // The settings change that turned autonomous control on, eight minutes before the server started.
    settingsChange: (before, after) => ({ at: epoch - 480_000, change: { actorUsername: 'operator@chill.local', actorUID: '2b7c9e41-5d08-4f3a-a6e1-9c0d47b2f815', before, after,
      validatedAt: iso(epoch - 480_000), appliedAt: iso(epoch - 480_000 + 37), affected: [] } }),
    journal,

    snapshot(t, autonomous) {
      return { workloads: liveWorkloads(t, autonomous), nodes: liveNodes(t), instances: liveInstances(t), routing: routing(),
        decision: decision(lastDecision(t)), actions: [action(cycle(t), t)], reservations: reservations(t) }
    },

    // sloPending: true for the first seconds after a snapshot; smolvlm2-vqa then has no verdict yet.
    metrics(bucketMs, sloPending = false) {
      const second = bucketMs / 1000
      const rows = Object.fromEntries(snapshot.workloads.map(row => {
        const svc = byUID[row.uid]
        if (!svc) return [row.uid, { received: 0, completed: 0, failed: 0, withinSLO: 0, latencySamples: 0, p50ms: null, p90ms: null, p95ms: null, p99ms: null }]
        const { p95: _p95, ...metric } = load(svc, second)
        return [row.uid, { ...metric, slo: slo(svc, second, sloPending && svc.name === 'smolvlm2-vqa') }]
      }))
      const meters = Object.fromEntries(meterNames.map(uid => [uid, { watts: missing(uidName[uid] ?? 'genesis1', second) ? null : watts(uidName[uid] ?? 'genesis1', uid, second), observedAt: iso(bucketMs + 970 + Math.round(rand(second, uid.length) * 25)) }]))
      const active = new Set([...activeAt(second)].map(name => nodes[name].uid))
      const sum = rows => round(rows.reduce((total, [, meter]) => total + (meter.watts ?? 0), 0), 2)
      return { bucketAt: iso(bucketMs), workloads: rows, nodes: meters, pool: { wholeWatts: sum(Object.entries(meters)), activeWatts: sum(Object.entries(meters).filter(([uid]) => active.has(uid))) },
        chill_obslog_queue_length: Math.round(rand(second, 17) * 6), chill_obslog_oldest_wait_seconds: round(rand(second, 19) * 0.04, 3) }
    },

    // Delta/event payloads for decisions and action phases in (from, to].
    timeline(from, to, autonomous) {
      const out = []
      for (let k = lastDecision(from) + 1; decisionAt(k) <= to; k++) {
        const at = decisionAt(k), row = decision(k)
        out.push({ at, frame: { type: 'delta', workloads: liveWorkloads(at, autonomous), nodes: liveNodes(at), reservations: reservations(at + ACTION_OFFSET), decision: row } })
        out.push({ at, frame: { type: 'event', kind: 'decision', decision: row } })
      }
      for (let c = cycle(from - ACTION_RUN); actionStart(c) <= to; c++) {
        const start = actionStart(c), end = start + ACTION_RUN
        if (start > from) {
          const started = action(c, start)
          out.push({ at: start, frame: { type: 'delta', actions: [started], removed: { actions: [actionKey(action(c - 1, start))] } } })
          out.push({ at: start, frame: { type: 'event', kind: 'action', action: { ...started, phase: 'started' } } })
        }
        if (end > from && end <= to) {
          const done = action(c, end)
          out.push({ at: end, frame: { type: 'delta', actions: [done], reservations: reservations(end) } })
          out.push({ at: end, frame: { type: 'event', kind: 'action', action: { ...done, phase: 'completed', result: { status: 'complete' } } } })
        }
      }
      return out.sort((a, b) => a.at - b.at)
    },

    // v1/observations/events rows in [from, to) ascending: changed decisions, prepare phases, periodic cluster/failed rows.
    events(from, to) {
      const rows = []
      for (let k = Math.ceil((from - epoch - DECISION_OFFSET) / CYCLE) * (CYCLE / DECIDE); decisionAt(k) < to; k += CYCLE / DECIDE) rows.push({ at: decisionAt(k), kind: 'decision', decision: decision(k) })
      for (let c = cycle(from - ACTION_RUN); actionStart(c) < to; c++) {
        rows.push({ at: actionStart(c), kind: 'action', action: { ...action(c, actionStart(c)), phase: 'started' } })
        const end = actionStart(c) + ACTION_RUN
        rows.push({ at: end, kind: 'action', action: { ...action(c, end), phase: 'completed', result: { status: 'complete' } } })
      }
      for (let n = Math.floor((from - epoch) / PERIOD) - 1; epoch + n * PERIOD < to; n++) rows.push(...periodic(n))
      return rows.filter(row => row.at >= from && row.at < to).sort((a, b) => a.at - b.at).map(({ at, ...row }) => ({ type: 'event', at: iso(at), ...row }))
    },

    // Serving workloads: Ready, measured supportedInputs, image-classification at generation 2.
    workloadView(view, t) {
      const svc = byUID[view.desired?.metadata?.uid]
      if (!svc) return view
      const assigned = svc.instances.reduce((sum, inst) => sum + occupancy(svc, inst, t).running[svc.uid], 0)
      const paths = view.appliedDefinition?.model?.paths ?? []
      let next = { ...view, supportedInputs: supportedInputs(svc.model, paths), admission: true, readiness: 'Ready', reason: 'Ready', pending: svc.pending, assigned, observedAt: iso(t - 300) }
      if (svc === ic) {
        const admission = icNext.admission, desired = view.desired
        next = { ...next, desired: { ...desired, metadata: { ...desired.metadata, generation: 2 }, spec: { ...desired.spec, admission },
          status: { ...desired.status, appliedDefinition: { ...desired.status.appliedDefinition, workloadGeneration: 2, admission } } },
          appliedDefinition: { ...view.appliedDefinition, generation: 2, admission } }
      }
      return next
    },

    pools(base, t) {
      const instance = (svc, inst) => ({ id: inst.id, podUID: inst.podUID, allocation: { ...(inst.gpu ? { gpuUUID: inst.gpu } : {}), memoryBytes: 134217728 },
        attemptedAt: iso(epoch - 1_800_000), ownerWorkloadUID: svc.uid, node: ref(inst.node), profile: inst.profileID, ready: true, stopped: false,
        routable: { [svc.uid]: true }, workloads: [{ name: svc.name, uid: svc.uid }], ...occupancy(svc, inst, t), assignedUnknown: {}, observedAt: iso(t - 700) })
      const poolNode = node => ({ node: ref(node.name), ready: node.ready, powerMode: node.powerMode, reasons: node.reasons ?? [] })
      const used = new Set([...servingNodes, MEASUREMENT_NODE, ...(running(t) ? [PREPARE_NODE] : [])])
      return { ...base,
        workloads: services.map(svc => ({ workload: { name: svc.name, uid: svc.uid },
          nodes: [...svc.instances.map(inst => ref(inst.node)), ...(svc === ic && running(t) ? [ref(PREPARE_NODE)] : [])], instances: svc.instances.map(inst => instance(svc, inst)) })),
        measurement: [{ node: ref(MEASUREMENT_NODE), instances: [] }],
        available: snapshot.nodes.filter(node => !used.has(node.name)).map(poolNode),
        other: [], observedAt: iso(t - 400) }
    },

    schedulerSummary(base, t, autonomous) {
      const k = lastDecision(t), c = cycle(t), act = action(c, t), at = iso(decisionAt(k))
      return { ...base, at: iso(decisionAt(k) - 120),
        nodes: snapshot.nodes.map(node => ({ node: ref(node.name), powerMode: node.powerMode, ready: node.ready, measurement: node.name === MEASUREMENT_NODE, reasons: node.reasons ?? null })),
        instances: services.flatMap(svc => svc.instances.map(inst => ({ id: inst.id, targetID: 'keep', node: ref(inst.node), ownerWorkloadUID: svc.uid, workloadUID: svc.uid,
          profileID: inst.profileID, ready: true, routable: { [svc.uid]: true }, reasons: null, stopped: false, podName: inst.id, podUID: inst.podUID }))),
        routing: routing(),
        decision: { id: at, at, status: status(k), reasons: reasons(k).length ? reasons(k) : null, targetID: targetID(k), workloads: workloadStates(autonomous) },
        actions: running(t) ? [{ id: act.id, kind: act.kind, status: 'running', node: act.node, instance: `${ic.name}-${PREPARE_NODE}-${hex(c, 4, 5)}`, dependsOn: [],
          elapsedNanos: (t - actionStart(c)) * 1e6, expectedSeconds: 90, expectedSource: null }] : [],
        poolReturn: { reservationCount: reservations(t).length, outstandingCount: 0, activeActionCount: running(t) ? 1 : 0,
          latestResultEndedAt: iso(actionStart(running(t) ? c - 1 : c) + ACTION_RUN) } }
    },

    // ObservationResult over [from, to) with the definition/outcome filters and a numeric cursor.
    observations(base, q, t, inferRows) {
      const svc = byUID[base.uid]
      if (!svc) return undefined
      const from = Date.parse(q.get('from')), to = Date.parse(q.get('to'))
      const definitionFilter = q.getAll('definition'), outcomeFilter = q.getAll('outcome')
      const rows = []
      for (let slot = Math.floor(from / ROW_SLOT) - 1; slot * ROW_SLOT < to; slot++) {
        const row = slotRow(svc, slot), receivedAt = Date.parse(row.receivedAt)
        if (receivedAt >= from && receivedAt < to && receivedAt + (row.elapsedNanos ?? 0) / 1e6 <= t) rows.push(row)
      }
      rows.push(...inferRows.filter(row => row.workloadUID === svc.uid && Date.parse(row.receivedAt) >= from && Date.parse(row.receivedAt) < to))
      rows.sort((a, b) => Date.parse(a.receivedAt) - Date.parse(b.receivedAt) || (a.requestID < b.requestID ? -1 : 1))
      const definitions = svc === ic ? [ic.definition, icNext] : [svc.definition]
      const outcomes = {}, cohorts = new Map(definitions.map(d => [identity(d), { definition: identity(d), total: 0, outcomes: {}, sampleCount: 0, latencyNanos: [], completionResidualNanos: [], percentile: d.slo.percentile, quantileNanos: null, targetLatencyNanos: d.slo.targetLatencyNanos }]))
      let total = 0
      const selected = []
      for (const row of rows) {
        if (definitionFilter.length && !definitionFilter.includes(row.definitionID)) continue
        total++; outcomes[row.outcome] = (outcomes[row.outcome] ?? 0) + 1
        if (outcomeFilter.length && !outcomeFilter.includes(row.outcome)) continue
        selected.push(row)
        const cohort = cohorts.get(row.definitionID)
        cohort.total++; cohort.outcomes[row.outcome] = (cohort.outcomes[row.outcome] ?? 0) + 1
        if (row.outcome === 'completed') { cohort.latencyNanos.push(row.elapsedNanos); cohort.completionResidualNanos.push(row.elapsedNanos - row.dispatchedNanos - Math.round(row.nativeElapsedMS * 1e6)) }
      }
      for (const cohort of cohorts.values()) {
        cohort.latencyNanos.sort((a, b) => a - b); cohort.completionResidualNanos.sort((a, b) => a - b)
        cohort.sampleCount = cohort.latencyNanos.length
        cohort.quantileNanos = quantile(cohort.latencyNanos, cohort.percentile / 100)
        if (!cohort.sampleCount) { cohort.latencyNanos = null; cohort.completionResidualNanos = null }
      }
      // order=desc pages newest first; the cursor counts rows in that order. Aggregates are order-free.
      if (q.get('order') === 'desc') selected.reverse()
      const limit = q.has('limit') ? Number(q.get('limit')) : 0, cursor = Number(q.get('cursor') ?? 0)
      if (!Number.isSafeInteger(cursor) || cursor < 0 || cursor > selected.length) return { status: 400, body: { code: 'InvalidQuery', message: 'observation cursor is out of range' } }
      const end = limit > 0 && limit < selected.length - cursor ? cursor + limit : selected.length
      const changes = svc === ic ? management().filter(event => Date.parse(event.at) >= from && Date.parse(event.at) < to
        && (!definitionFilter.length || [event.before, event.target, event.after].some(d => d && definitionFilter.includes(identity(d))))) : []
      const { nextCursor: _cursor, ...rest } = base
      return { status: 200, body: { ...rest, from: q.get('from'), to: q.get('to'), definitionFilter: definitionFilter.length ? definitionFilter : null, outcomeFilter: outcomeFilter.length ? outcomeFilter : null,
        total, selected: selected.length, outcomes, cohorts: [...cohorts.values()].sort((a, b) => a.definition < b.definition ? -1 : 1),
        definitions: definitions.map(d => ({ identity: identity(d), definition: d })).sort((a, b) => a.identity < b.identity ? -1 : 1),
        requests: selected.slice(cursor, end), management: changes.length ? changes : null, ...(end < selected.length ? { nextCursor: String(end) } : {}),
        coverage: { ...(base.coverage ?? {}), from: q.get('from'), to: q.get('to') } } }
    },

    // POST v1/workloads/{name}/infer for a serving service: firstfloor Response body and the completion row.
    infer(name, receivedAt, finishedAt) {
      const svc = byName[name]
      if (!svc) return undefined
      const seed = receivedAt / 997 + svc.k, row = requestRow(svc, receivedAt, seed, 'completed')
      const elapsed = Math.round((finishedAt - receivedAt) * 1e6), scale = elapsed / row.elapsedNanos
      const scaled = Object.fromEntries(Object.entries(row).map(([key, value]) => [key, /Nanos$/.test(key) && typeof value === 'number' && key !== 'lifetimeNanos' ? Math.round(value * scale) : /MS$/.test(key) && typeof value === 'number' && key !== 'completionMarginMS' ? round(value * scale, 3) : value]))
      scaled.serverTiming = row.serverTiming.map(stage => ({ ...stage, elapsedNanos: Math.round(stage.elapsedNanos * scale) }))
      const values = { label: svc.answer?.[Math.floor(rand(seed, 3) * svc.answer.length)] ?? 'tabby cat', answer: svc.answer?.[Math.floor(rand(seed, 3) * svc.answer.length)] ?? '', score: String(round(0.02 + rand(seed, 5) * 0.2, 4)), anomaly: 'false', threshold: '0.35', anomaly_alert: 'false', largest_score_feature: 'barrel_temp_zone2' }
      const body = { model_name: svc.model, id: row.requestID, outputs: svc.output.map(field => ({ name: field.name, data: [b64(values[field.name] ?? '')] })),
        progress: { requestID: row.requestID, workloadUID: svc.uid, definition: row.definitionID, phase: 'finished', at: iso(finishedAt), batchID: row.batchID, batchSize: 1, profileID: row.profileID, decisionID: row.decisionID,
          queueMS: scaled.queueMS, serviceMS: scaled.serviceMS, backendMS: scaled.backendMS, preprocessMS: scaled.preprocessMS, postprocessMS: scaled.postprocessMS, ...(scaled.firstTokenMS ? { firstTokenMS: scaled.firstTokenMS } : {}),
          units: 1, nativeElapsedMS: scaled.nativeElapsedMS, executionComplete: true, outcome: 'completed' }, executionComplete: true }
      return { row: { ...scaled, batchSize: 1 }, body, uid: svc.uid, lifetime: `${svc.target * 3}s` }
    },

    load(base, from, to) {
      const window = (to - from) / 1000
      return { ...base, from: iso(from), to: iso(to), durationNanos: (to - from) * 1e6,
        workloads: services.map(svc => {
          const received = Math.round(svc.rate * window), accepted = Math.round(received * svc.success), completed = Math.max(0, accepted - Math.round(svc.rate * 0.3))
          return { uid: svc.uid, received, accepted, assigned: accepted, completed, receivedInputsPerSecond: round(received / window, 3), acceptedInputsPerSecond: round(accepted / window, 3),
            assignedInputsPerSecond: round(accepted / window, 3), completionEventsPerSecond: round(completed / window, 3),
            instanceAssignments: Object.fromEntries(svc.instances.map(inst => [inst.id, Math.round(accepted * inst.weight / 100)])) }
        }) }
    },

    // Power reads: no bucket = one OperatingPowerWindow; bucket = OperatingPowerSeries or OperatingPowerColumns.
    power(q, t) {
      const from = Date.parse(q.get('from')), to = Date.parse(q.get('to'))
      if (!q.has('bucket')) return { status: 200, body: powerWindow(from, to, t, Math.max(1, Math.ceil((to - from) / 3_600_000))) }
      const match = /^(\d+(?:\.\d+)?)(s|m|h)$/.exec(q.get('bucket')), bucket = match ? Math.round(Number(match[1]) * { s: 1000, m: 60_000, h: 3_600_000 }[match[2]]) : 0
      const start = Math.floor(from / bucket) * bucket, count = Math.ceil((to - start) / bucket)
      if (!bucket || bucket < 1000 || bucket > 3_600_000 || to - from > 3_600_000 || count > 900) return { status: 400, body: { code: 'InvalidQuery', message: 'power series requires a positive from/to range of at most 1h and bucket between 1s and 1h' } }
      const buckets = Array.from({ length: count }, (_, i) => powerWindow(Math.max(from, start + i * bucket), Math.min(to, start + (i + 1) * bucket), t))
      const head = { from: q.get('from'), to: q.get('to'), bucketNanos: bucket * 1e6 }
      if (q.get('format') !== 'columns') return { status: 200, body: { ...head, buckets } }
      const windowKeys = ['scope', 'wholePoolJoules', 'wholePoolMeanWatts', 'activePoolJoules', 'activePoolMeanWatts', 'complete', 'readFailures', 'maximumSampleGapSeconds', 'reasons', 'gapUntil', 'coverage']
      const nodeKeys = ['node', 'meter', 'scope', 'meterJoules', 'meterComplete', 'gapCount', 'gapSeconds', 'gapFraction', 'sourceIdentity', 'joules', 'meanWatts', 'samples', 'readFailures', 'maximumSampleGapSeconds', 'sourceTimestampAbsent', 'observedSamples', 'firstObservedAt', 'lastObservedAt', 'reasons']
      return { status: 200, body: { ...head, format: 'columns', windows: Object.fromEntries(windowKeys.map(key => [key, buckets.map(row => row[key] ?? null)])),
        nodes: Object.fromEntries(nodeKeys.map(key => [key, buckets.map(row => row.nodes.map(node => node[key] ?? null))])) } }
    },

    // One running evaluation: 3 stages of 300 s, at stage 2 about 4 minutes in when the server starts.
    activeEvaluations(t) {
      const start = evalStart(t), index = Math.floor((t - start) / EVAL_STAGE), stageAt = start + index * EVAL_STAGE
      return { items: [{ name: EVALUATION.name, uid: EVALUATION.uid, startedAt: seconds(start), stageIndex: index + 1, stagesTotal: 3,
        stage: { id: runStages()[index].id, startedAt: seconds(stageAt), goAt: seconds(stageAt + 5000), plannedSeconds: 300, injections: [] } }] }
    },
    runningEvaluation, blockedEvaluation, pendingEvaluation,
    evaluationNames: { running: EVALUATION.name, blocked: BLOCKED.name, pending: PENDING.name },
    // Load scenarios referenced by the running evaluation.
    loadScenariosInUse: () => runStages().map(stage => stage.loadScenarioRef?.name).filter(Boolean),
    evaluationInputs: () => ['e5-7-7', 'e5-3-7', 'e5-5-7', 'residual'].map((set, i) => ({ store: 'chill-evidence-store', key: `evaluation-inputs/trace/${set}/arrivals-r${i + 1}.jsonl.gz`, size: 412_000 + i * 37_118, kind: 'trace', set, name: `arrivals-r${i + 1}.jsonl.gz` })),
    // Experiment detail/list item with a recorded comparison between its two stages.
    experiment(body) {
      if (body?.id !== COMPARED_EXPERIMENT || (body.stages ?? []).length < 2) return body
      const comparisons = [{ left: { stageID: body.stages[1].id }, right: { stageID: body.stages[0].id }, reason: '같은 부하에서 workload 우선순위와 excess 우선순위를 비교' }]
      return body.definition ? { ...body, definition: { ...body.definition, spec: { ...body.definition.spec, comparisons } } } : { ...body, comparisons }
    },
  }
}


// Synthetic 'active' scene for server/replay.mjs. Pure functions of wall-clock `t` (ms) over the
// captured live snapshot, so REST overlays and live frames describe the same state.
// `epoch` anchors every cycle: a changed decision and a prepare action start each CYCLE.
const CYCLE = 120_000, DECIDE = 20_000, DECISION_OFFSET = 230, ACTION_OFFSET = 480, ACTION_RUN = 88_000
const PERIOD = 600_000, EVAL_CYCLE = 900_000, EVAL_STAGE = 300_000, SLO_WINDOW = 60
const KEEP = '이번 판단에서는 새 변경을 선택하지 않았어요. 이전에 시작한 변경은 계속될 수 있어요.'
const RESERVATION = '이전 변경이 장치와 서비스 자원을 예약하고 있어요. 실행과 자원 반환 확인이 끝날 때까지 다른 변경에서 사용하지 않아요.'
const SERVING = [
  { name: 'image-classification-tight-slo', k: 1, rate: 120, swing: 0.12, period: 300, p95: 0.62, p95Swing: 0.04, p95Period: 170, success: 0.996, pending: 0,
    placements: [['genesis2', 70, 'firstfloor-ort-cuda-amd64', 16, 'GPU-3f9a1c2e-58d1-4c0b-9a51-6c1f0e2d7b44'], ['jetsono', 30, 'firstfloor-ort-cuda-arm64', 8]] },
  { name: 'plc-anomaly', k: 2, rate: 35, swing: 0.1, period: 240, p95: 1.0, p95Swing: 0.15, p95Period: 150, success: 0.985, pending: 2,
    placements: [['jetson-nano', 100, 'firstfloor-ort-cuda-rnn-arm64', 4]] },
  { name: 'smolvlm2-vqa', k: 3, rate: 0.4, swing: 0.2, period: 200, p95: 4.1, p95Swing: 0.25, p95Period: 130, success: 1, pending: 0,
    placements: [['genesis3', 100, 'firstfloor-ort-cuda-amd64', 1, 'GPU-8b2d47e0-1c3a-4f6e-b7d9-02a4c5e8f113']] },
]
const AUTONOMOUS = ['image-classification-tight-slo', 'plc-anomaly', 'smolvlm2-vqa', 'okdong-ae-anomaly']
// [idle, busy] watts per device class; unlisted meters keep their captured reading.
const WATTS = { genesis2: [91, 142], genesis3: [54, 112], jetsono: [31.7, 37.5], 'jetson-nano': [2.4, 6.8], 'orin-nano': [5.5, 8.6], lattepanda: [9.5, 15.2] }
const PREPARE_NODE = 'orin-nano', MEASUREMENT_NODE = 'lattepanda', FAILED_NODE = 'rasp5'
const LEASE = '6f0d3b8e-2a41-4c7f-9e15-d8b3a0c47e92'
const EVALUATION = { name: 'evaluation-replay-energy-sweep', uid: '4c1d9e2a-7b35-4f08-9e61-2a8d5c0b3f17', stages: ['baseline', 'autonomous', 'recovery'] }

const iso = ms => new Date(ms).toISOString()
const seconds = ms => iso(ms).replace(/\.\d+Z$/, 'Z')
const rand = (seed, k) => { const x = Math.sin(seed * 12.9898 + k * 78.233) * 43758.5453; return x - Math.floor(x) }
const wave = (t, period) => Math.sin(2 * Math.PI * t / 1000 / period)
const round = (value, digits = 1) => Math.round(value * 10 ** digits) / 10 ** digits
const hex = (seed, k, n) => Array.from({ length: n }, (_, i) => Math.floor(rand(seed + i, k) * 16).toString(16)).join('')
const uuid = (seed, k) => `${hex(seed, k, 8)}-${hex(seed, k + 1, 4)}-4${hex(seed, k + 2, 3)}-a${hex(seed, k + 3, 3)}-${hex(seed, k + 4, 12)}`

// profileFor(node, model, path, powerMode) names the measured profile each instance runs (replay-measure.mjs).
export function activeScene(capture, epoch, profileFor = () => undefined) {
  const snapshot = capture.frames.find(frame => frame.type === 'snapshot')
  const firstMetrics = capture.frames.find(frame => frame.type === 'metrics')
  const nodes = Object.fromEntries(snapshot.nodes.map(node => [node.name, node]))
  const ref = name => ({ uid: nodes[name].uid, name })
  const workloads = Object.fromEntries(snapshot.workloads.map(row => [row.name, row]))
  const services = SERVING.map(svc => {
    const row = workloads[svc.name]
    return { ...svc, uid: row.uid, model: row.model, definitionID: row.definitionID, percentile: row.slo.percentile, target: row.slo.targetLatencyNanos / 1e9,
      instances: svc.placements.map(([node, weight, path, batch, gpu]) => ({
        id: `${svc.name}-${node}-${hex(svc.k, weight, 5)}`, node, weight, batch, gpu, podUID: uuid(svc.k, weight),
        profileID: profileFor(node, row.model, path, nodes[node].powerMode) ?? `${row.model}/${node}/${path}/${nodes[node].powerMode}/b${batch}`,
      })) }
  })
  const byUID = Object.fromEntries(services.map(svc => [svc.uid, svc]))
  const ic = services[0], plc = services[1]
  const servingNodes = new Set(services.flatMap(svc => svc.instances.map(inst => inst.node)))

  const decisionAt = k => epoch + k * DECIDE + DECISION_OFFSET
  const lastDecision = t => Math.floor((t - epoch - DECISION_OFFSET) / DECIDE)
  const changed = k => k % (CYCLE / DECIDE) === 0
  const decision = k => ({ at: iso(decisionAt(k)), state: changed(k) ? 'Feasible' : 'Keep', reason: '', explanation: changed(k) ? '최근 판단 상태: Feasible.' : KEEP })
  const actionStart = c => epoch + c * CYCLE + ACTION_OFFSET
  const cycle = t => Math.floor((t - epoch - ACTION_OFFSET) / CYCLE)
  const running = t => t < actionStart(cycle(t)) + ACTION_RUN
  const action = (c, t) => {
    const startedAt = actionStart(c), done = t >= startedAt + ACTION_RUN
    return { id: `prepare/${PREPARE_NODE}/${iso(startedAt)}`, kind: 'prepare', node: ref(PREPARE_NODE), workloadUID: ic.uid, status: done ? 'complete' : 'running',
      startedAt: iso(startedAt), ...(done ? { completedAt: iso(startedAt + ACTION_RUN) } : {}), expectedSeconds: 90, expectedSource: null }
  }
  const actionKey = row => JSON.stringify([row.id, row.node.uid, row.startedAt])
  // The measurement lease on lattepanda is permanent; the prepare reservation lives while its action runs.
  const lease = { owner: LEASE, nodes: [ref(MEASUREMENT_NODE)], workloadUIDs: [], podUIDs: [], instances: [], reason: `measurement-lease/${LEASE}: profiling run in progress`,
    explanation: `${RESERVATION} 제공된 사유: measurement-lease/${LEASE}: profiling run in progress` }
  const reservations = t => [lease, ...(running(t) ? [{ owner: `decision/${iso(decisionAt(cycle(t) * (CYCLE / DECIDE)))}`, nodes: [ref(PREPARE_NODE)], workloadUIDs: [ic.uid], podUIDs: [], instances: [],
    reason: '', explanation: RESERVATION }] : [])]
  const workloadStates = autonomous => snapshot.workloads.map(w => !autonomous.includes(w.uid) ? { uid: w.uid, state: 'Disabled', reason: 'AutonomousControlOff' }
    : byUID[w.uid] ? { uid: w.uid, state: 'Controlled' } : { uid: w.uid, state: 'MeasurementEvidenceMissing', reason: 'MissingEvidence' })

  // Per-second request outcome for one service; deterministic so REST and live agree.
  // withinSLO is linear in p95/target and crosses the percentile exactly at the target.
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
      return { ...row, autonomous: autonomous.includes(row.uid), admission: !!svc, pending: svc?.pending ?? 0,
        readiness: svc ? { ready: true, reason: 'Ready', observedAt: iso(t - 2000) } : { ...row.readiness, observedAt: iso(t - 2000) } }
    })
  }
  const liveNodes = t => snapshot.nodes.map(node => ({ ...node, measurement: node.name === MEASUREMENT_NODE, observedAt: iso(decisionAt(lastDecision(t))) }))
  const liveInstances = t => services.flatMap(svc => svc.instances.map(inst => ({
    id: inst.id, node: ref(inst.node), ownerWorkloadUID: svc.uid, profileID: inst.profileID, ready: true, stopped: false,
    routable: { [svc.uid]: true }, ...occupancy(svc, inst, t), observedAt: iso(t - 700),
  })))
  const routing = () => Object.fromEntries(snapshot.workloads.map(row => [row.uid, Object.fromEntries((byUID[row.uid]?.instances ?? []).map(inst => [inst.id, inst.weight]))]))

  function watts(name, uid, second) {
    const range = WATTS[name], base = firstMetrics.nodes[uid]?.watts ?? null
    if (base === null) return null
    const jitter = 0.03 * wave(second * 1000, 23 + name.length) + 0.01 * (rand(second, name.length) - 0.5)
    if (!range) return round(base * (1 + jitter), 2)
    const active = servingNodes.has(name) || name === MEASUREMENT_NODE || (name === PREPARE_NODE && running(second * 1000))
    const level = active ? range[1] * (name === 'genesis2' ? 0.9 + 0.1 * load(ic, second).received / ic.rate : 1) : range[0]
    return round(level * (1 + jitter), 2)
  }

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

  return {
    settings: settings => ({ ...settings, autonomousWorkloads: AUTONOMOUS.map(name => workloads[name].uid) }),
    // The settings change that turned autonomous control on, eight minutes before the server started.
    settingsChange: (before, after) => ({ at: epoch - 480_000, change: { actorUsername: 'operator@chill.local', actorUID: '2b7c9e41-5d08-4f3a-a6e1-9c0d47b2f815', before, after,
      validatedAt: iso(epoch - 480_000), appliedAt: iso(epoch - 480_000 + 37), affected: [] } }),

    snapshot(t, autonomous) {
      return { workloads: liveWorkloads(t, autonomous), nodes: liveNodes(t), instances: liveInstances(t), routing: routing(),
        decision: decision(lastDecision(t)), actions: [action(cycle(t), t)], reservations: reservations(t) }
    },

    // sloPending: true for the first seconds after a snapshot; smolvlm2-vqa then has no verdict yet.
    metrics(bucketMs, sloPending = false) {
      const second = bucketMs / 1000, names = Object.fromEntries(snapshot.nodes.map(node => [node.uid, node.name]))
      const rows = Object.fromEntries(snapshot.workloads.map(row => {
        const svc = byUID[row.uid]
        if (!svc) return [row.uid, { received: 0, completed: 0, failed: 0, withinSLO: 0, latencySamples: 0, p50ms: null, p90ms: null, p95ms: null, p99ms: null }]
        const { p95: _p95, ...metric } = load(svc, second)
        return [row.uid, { ...metric, slo: slo(svc, second, sloPending && svc.name === 'smolvlm2-vqa') }]
      }))
      const meters = Object.fromEntries(Object.keys(firstMetrics.nodes).map(uid => [uid, { watts: watts(names[uid] ?? uid, uid, second), observedAt: iso(bucketMs + 970 + Math.round(rand(second, uid.length) * 25)) }]))
      const active = new Set([...servingNodes, ...(running(bucketMs) ? [PREPARE_NODE] : [])].map(name => nodes[name].uid))
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

    // v1/observations/events rows in [from, to): changed decisions, prepare phases, periodic cluster/failed rows.
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

    // v1/observations/scheduler filter=decision-change rows in [from, to), newest first: decisions whose target is not the keep plan.
    // The summary view carries no target, as in schedulerHistoryDecision.
    decisionChanges(from, to, autonomous, summary) {
      const rows = []
      for (let k = Math.floor((to - epoch - DECISION_OFFSET) / CYCLE) * (CYCLE / DECIDE); decisionAt(k) >= from; k -= CYCLE / DECIDE) {
        if (decisionAt(k) >= to) continue
        const at = iso(decisionAt(k)), workloads = workloadStates(autonomous)
        const decision = summary ? { at, status: 'Feasible', workloads } : { at, status: 'Feasible', keep: { id: 'keep', keep: true }, target: { id: `candidate-${k}`, keep: false }, reasons: null, workloads }
        rows.push({ at, offset: 4000 + k, decision, snapshot: { at: iso(decisionAt(k) - 120) } })
      }
      return rows
    },

    workloadView(view, t) {
      const svc = byUID[view.desired?.metadata?.uid]
      if (!svc) return view
      const assigned = svc.instances.reduce((sum, inst) => sum + occupancy(svc, inst, t).running[svc.uid], 0)
      return { ...view, admission: true, readiness: 'Ready', reason: 'Ready', pending: svc.pending, assigned, observedAt: iso(t - 300) }
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
      const k = lastDecision(t), row = decision(k), c = cycle(t), act = action(c, t)
      return { ...base, at: iso(decisionAt(k) - 120),
        nodes: snapshot.nodes.map(node => ({ node: ref(node.name), powerMode: node.powerMode, ready: node.ready, measurement: node.name === MEASUREMENT_NODE, reasons: node.reasons ?? null })),
        instances: services.flatMap(svc => svc.instances.map(inst => ({ id: inst.id, targetID: 'keep', node: ref(inst.node), ownerWorkloadUID: svc.uid, workloadUID: svc.uid,
          profileID: inst.profileID, ready: true, routable: { [svc.uid]: true }, reasons: null, stopped: false, podName: inst.id, podUID: inst.podUID }))),
        routing: routing(),
        decision: { id: row.at, at: row.at, status: row.state, reasons: null, targetID: row.state === 'Keep' ? 'keep' : `candidate-${k}`, workloads: workloadStates(autonomous) },
        actions: running(t) ? [{ id: act.id, kind: act.kind, status: 'running', node: act.node, instance: `${ic.name}-${PREPARE_NODE}-pending`, dependsOn: [],
          elapsedNanos: (t - actionStart(c)) * 1e6, expectedSeconds: 90, expectedSource: null }] : [],
        poolReturn: { reservationCount: reservations(t).length, outstandingCount: 0, activeActionCount: running(t) ? 1 : 0,
          latestResultEndedAt: iso(actionStart(running(t) ? c - 1 : c) + ACTION_RUN) } }
    },

    // About 40 request rows per serving service plus cohort totals over [from, to).
    observations(base, t, from, to, limit) {
      const svc = byUID[base.uid]
      if (!svc) return base
      const window = Math.max(1, (to - from) / 1000), total = Math.round(svc.rate * window), failed = Math.round(total * (1 - svc.success))
      const p95 = load(svc, Math.floor(t / 1000)).p95, sigma = Math.log(1 / 0.55) / 1.645
      const rows = Array.from({ length: Math.min(40, limit) }, (_, i) => {
        const receivedAt = t - (40 - i) * 1500 - Math.round(rand(i, svc.k) * 900), seed = receivedAt / 1000
        const inst = svc.instances.find((_inst, index, all) => rand(seed, 31) * 100 < all.slice(0, index + 1).reduce((sum, row) => sum + row.weight, 0)) ?? svc.instances[0]
        const id = `${svc.uid.slice(0, 8)}-${Math.round(receivedAt).toString(36)}`
        if (svc.success < 0.99 && rand(seed, 41) < 0.06) return { kind: 'final', requestID: id, workloadUID: svc.uid, definitionID: svc.definitionID, outcome: 'rejected', error: 'AdmissionRejected',
          admissionPolicy: 'Reject', receivedAt: iso(receivedAt), elapsedNanos: 900_000 + Math.round(rand(seed, 43) * 400_000), validatedNanos: 310_000, executionComplete: false }
        const z = Math.sqrt(-2 * Math.log(rand(seed, 47) || 1e-9)) * Math.cos(2 * Math.PI * rand(seed, 53))
        const elapsed = Math.round(p95 * 0.55 * Math.exp(z * sigma) * 1e9), queue = Math.round(elapsed * (0.18 + 0.1 * rand(seed, 59)))
        const dispatched = 1_400_000, queued = dispatched + 2_100_000, run = queued + queue, executed = elapsed - 3_600_000
        return { kind: 'final', requestID: id, workloadUID: svc.uid, definitionID: svc.definitionID, node: ref(inst.node), instance: inst.id, profileID: inst.profileID,
          decisionID: iso(decisionAt(lastDecision(receivedAt))), outcome: 'completed', executionComplete: true, receivedAt: iso(receivedAt), lifetimeNanos: Math.round(svc.target * 3e9),
          dispatchedAt: iso(receivedAt + 1.4), elapsedNanos: elapsed, validatedNanos: 310_000, acceptedNanos: 520_000, dispatchedNanos: dispatched, queuedNanos: queued,
          runningNanos: run, executedNanos: executed, exchangeNanos: elapsed - 900_000, batchSize: Math.max(1, Math.round(inst.batch * rand(seed, 61))), queueMS: round(queue / 1e6, 3) }
      })
      const latencies = rows.filter(row => row.outcome === 'completed').map(row => row.elapsedNanos).sort((a, b) => a - b)
      const outcomes = failed ? { completed: total - failed, rejected: failed } : { completed: total }
      return { ...base, from: iso(from), to: iso(to), total, selected: total, outcomes,
        cohorts: base.cohorts.map(cohort => ({ ...cohort, total, outcomes, sampleCount: total - failed, latencyNanos: latencies, quantileNanos: Math.round(p95 * 1e9) })), requests: rows }
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

    // One running evaluation: 3 stages of 300 s, at stage 2 about 4 minutes in when the server starts.
    activeEvaluations(t) {
      const anchor = epoch + 40_000 - EVAL_STAGE - 240_000
      const start = anchor + Math.floor((t - anchor) / EVAL_CYCLE) * EVAL_CYCLE, index = Math.floor((t - start) / EVAL_STAGE), stageAt = start + index * EVAL_STAGE
      return { items: [{ name: EVALUATION.name, uid: EVALUATION.uid, startedAt: seconds(start), stageIndex: index + 1, stagesTotal: 3,
        stage: { id: EVALUATION.stages[index], startedAt: seconds(stageAt), goAt: seconds(stageAt + 5000), plannedSeconds: 300, injections: [] } }] }
    },
  }
}

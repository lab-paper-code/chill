// Display helpers for the device screens. Unit conversion and labels only; no statistics.
import type { LiveReservation } from "./live.ts"
import type { PowerPoint } from "./power-series.ts"
import type { PowerModeActuator, ProfilingRun } from "./read-types.ts"
import type { StatusInfo } from "./status-labels.ts"

export type Ref = { name: string; uid: string }
/** What holds a device under measurement: the lease holder UID, its measurement request and the run, as far as known. */
export type MeasurementHold = { holder: string | null; request: Ref | null; run: Ref | null }

const LEASE = "measurement-lease/"
/** The measurement request (BatchEnergyOptimization) that controls a run. */
export function runRequest(run: ProfilingRun): Ref | null {
  const owner = run.ownerReferences?.find(item => item.kind === "BatchEnergyOptimization" && item.controller !== false)
  return owner ? { name: owner.name, uid: owner.uid } : null
}

/**
 * Measuring devices to what holds them, from data the screen already has: the live reservations
 * (a lease is owned by the measurement's UID, reason "measurement-lease/<uid>: …") and the running runs.
 * A lease names its holder; the newest running run whose request is the holder gives request and run.
 * A measuring device without a lease takes the newest running run aimed at it.
 */
export function measurementHolds(measuring: readonly string[], reservations: readonly LiveReservation[], runs: readonly ProfilingRun[]): Map<string, MeasurementHold> {
  const holders = new Map<string, string>()
  for (const r of reservations) {
    if (!r.reason.startsWith(LEASE) && !r.owner.startsWith(LEASE)) continue
    const holder = r.reason.startsWith(LEASE) ? r.reason.slice(LEASE.length).split(":", 1)[0] : r.owner.slice(LEASE.length)
    for (const node of r.nodes) holders.set(node.name, holder || r.owner)
  }
  const newest = [...runs].sort((a, b) => b.creationTimestamp.localeCompare(a.creationTimestamp))
  const holds = new Map<string, MeasurementHold>()
  for (const node of measuring) {
    const holder = holders.get(node) ?? null
    const run = holder ? newest.find(item => runRequest(item)?.uid === holder || item.uid === holder) : newest.find(item => item.spec.targetNode === node)
    holds.set(node, { holder, request: run ? runRequest(run) : null, run: run ? { name: run.name, uid: run.uid } : null })
  }
  return holds
}

/**
 * One role per device. `roles` are the `DeviceRow.roles` labels from `deviceRows`.
 * Exclusion wins: the planner ignores an excluded device whatever else it runs.
 */
export function deviceRole(roles: readonly string[], excluded: boolean): StatusInfo | null {
  if (excluded) return { label: "제외", tone: "warn" }
  if (roles.includes("측정 중")) return { label: "측정", tone: "progress" }
  if (roles.includes("서비스 배치")) return { label: "서비스", tone: "ok" }
  if (roles.includes("가용")) return { label: "가용", tone: "idle" }
  return null
}

export const readiness = (ready: boolean | null): StatusInfo | null =>
  ready == null ? null : ready ? { label: "준비됨", tone: "ok" } : { label: "준비 안 됨", tone: "fail" }

const factors: Record<string, number> = { "": 1, k: 1e3, M: 1e6, G: 1e9, T: 1e12, P: 1e15, Ki: 2 ** 10, Mi: 2 ** 20, Gi: 2 ** 30, Ti: 2 ** 40, Pi: 2 ** 50 }
/** Kubernetes quantity ("64Gi", "4G", "512Mi") in bytes; null when it is not a plain size. */
export function quantityBytes(quantity: string | null | undefined): number | null {
  const match = /^(\d+(?:\.\d+)?)(Ki|Mi|Gi|Ti|Pi|k|M|G|T|P)?$/.exec(quantity?.trim() ?? "")
  return match ? Number(match[1]) * factors[match[2] ?? ""] : null
}

/** How a power mode is applied, and its setting. */
export function actuatorText(actuator: PowerModeActuator): { method: string; setting: string | null } {
  switch (actuator.kind) {
    case "Fixed": return { method: "고정", setting: null }
    case "JetsonNVPModel": return { method: "Jetson 전력 모드", setting: `모드 ${actuator.jetsonNVPModel.id}` }
    case "NvidiaLockedGPUClock": {
      const { minMHz, maxMHz } = actuator.nvidiaLockedGPUClock
      return { method: "GPU 클럭 고정", setting: minMHz === maxMHz ? `${minMHz} MHz` : `${minMHz}–${maxMHz} MHz` }
    }
  }
}

/** Joules as a value and unit for a tile: kJ from 10 000 J. */
export function energyParts(joules: number | null | undefined): { value: number | null; unit: "J" | "kJ" } {
  if (joules == null || !Number.isFinite(joules)) return { value: null, unit: "J" }
  return Math.abs(joules) >= 10_000 ? { value: joules / 1000, unit: "kJ" } : { value: joules, unit: "J" }
}

/**
 * Chart rows at each interval start. Missing time between intervals becomes a null row,
 * so the line breaks there as it does at a null sample.
 */
export function chartRows(points: readonly PowerPoint[], values: (point: PowerPoint) => Record<string, number | null>): ({ t: number } & Record<string, number | null>)[] {
  const rows: ({ t: number } & Record<string, number | null>)[] = []
  let previous: PowerPoint | undefined
  for (const point of points) {
    const row = values(point)
    if (previous && previous.to !== point.from) rows.push({ t: previous.to, ...Object.fromEntries(Object.keys(row).map(key => [key, null])) })
    rows.push({ t: point.from, ...row })
    previous = point
  }
  return rows
}

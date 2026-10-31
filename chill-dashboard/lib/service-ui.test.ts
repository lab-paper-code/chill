import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as domain from "./domain.ts"
import * as labels from "./labels.ts"
import * as scheduler from "./scheduler.ts"
import type { WorkloadView } from "./domain.ts"

type Element = { type: unknown; props: Record<string, unknown> }
type Component = (props: Record<string, unknown>) => Element
const require = createRequire(import.meta.url)
function load(file: string, modules: Record<string, unknown>) {
  const compiled = transpileModule(readFileSync(new URL(file, import.meta.url), "utf8"), { compilerOptions: { target: ScriptTarget.ES2022, module: ModuleKind.CommonJS, jsx: JsxEmit.ReactJSX } }).outputText
  const exports: Record<string, Component> = {}
  new Function("require", "exports", compiled)((name: string) => Object.hasOwn(modules, name) ? modules[name] : name.startsWith("@/") ? {} : require(name), exports)
  return exports
}
function elements(node: unknown): Element[] {
  if (Array.isArray(node)) return node.flatMap(elements)
  if (!node || typeof node !== "object" || !("props" in node)) return []
  const element = node as Element
  return [element, ...elements(element.props.children)]
}
const idle = { desired: { metadata: { name: "idle", uid: "service-uid" }, spec: { modelRef: { name: "model" }, preprocessorRef: { name: "input" }, slo: { percentile: 95, targetLatency: "1s" } } }, admission: true, readiness: "Ready", pending: 0, assigned: 0, reason: "", supportedInputs: [], observedAt: "2026-11-01T00:00:00Z" } as unknown as WorkloadView
function harness() {
  const paths: string[] = []
  const modules: Record<string, unknown> = {
    react: { useState: (value: unknown) => [typeof value === "function" ? value() : value, () => {}] },
    "next/link": { default: "a" },
    "next/navigation": { useSearchParams: () => new URLSearchParams() },
    "@tanstack/react-query": { useQueryClient: () => ({}) },
    "@/lib/domain": domain, "@/lib/labels": labels, "@/lib/scheduler": scheduler,
    "@/lib/client": { useChillQuery: (path: string) => { paths.push(path); return { data: path === "v1/workloads" ? [idle] : path.endsWith("/idle") ? idle : undefined, error: null } }, useChillMutation: () => ({ isPending: false }) },
    "@/components/status-cell": { StatusCell: "status-cell" },
    "@/components/data-table": { DataTable: "data-table" },
    "@/components/screen": { Screen: "main", Rows: "rows", RowDialog: "row-dialog", Facts: "facts" },
    "@/components/read-region": { QueryGate: "query-gate" },
    "@/components/ui/button": { Button: "button" },
  }
  const hub = load("../features/service-hub.tsx", modules)
  modules["@/features/service-hub"] = hub
  return { hub, management: load("../features/management.tsx", modules), paths }
}

test("idle admission remains permission, not actual traffic, in service list and detail", () => {
  const h = harness()
  const admission = h.hub.ServiceAdmission({ view: idle })
  assert.equal(admission.props.axis, "접수 허용")
  assert.equal(admission.props.label, "허용")
  assert.equal(h.hub.ServiceAdmission({ view: { ...idle, admission: false } }).props.label, "차단")
  for (const tree of [h.management.ServicesPage({}), h.management.ServicePage({ name: "idle" })]) {
    const table = elements(tree).find(element => element.type === "data-table")!
    const columns = table.props.columns as { key: string; value: (view: WorkloadView) => Element }[]
    const cell = columns.find(column => column.key === "admission")!.value(idle)
    assert.equal(cell.type, h.hub.ServiceAdmission)
    assert.equal(h.hub.ServiceAdmission(cell.props).props.label, "허용")
  }
  assert.ok(h.paths.includes("v1/workloads"))
  assert.ok(h.paths.includes("v1/workloads/idle"))
})

test("integrated detail retains three regions and UID-specific current observation", () => {
  const h = harness(), tree = h.management.ServicePage({ name: "idle" })
  assert.equal(tree.props.title, "idle")
  assert.equal(tree.props.lede, undefined)
  assert.equal(elements(tree).filter(element => element.props.id === "application").length, 1)
  assert.equal(elements(tree).filter(element => element.type === h.hub.ServiceDefinition).length, 1)
  const placement = h.hub.ServicePlacement({ view: idle, signals: { pools: {}, scheduler: {}, instances: [] } })
  assert.ok(elements(placement).some(element => element.props.href === "/?open=service%3Aservice-uid"))
  assert.ok(elements(tree.props.action).some(element => element.props.href === "/workloads/idle/infer?uid=service-uid"))
})

test("distribution rows and weights come from one scheduler snapshot despite a later pools observation", () => {
  const h = harness()
  const snapshotAt = "2026-10-31T23:59:00Z"
  const instance = { id: "instance", workloadUID: "service-uid", node: { name: "scheduler-device" }, ready: true }
  const shared = { id: "shared", workloadUID: "another-service", node: { name: "shared-device" }, ready: true }
  const unrelated = { id: "unrelated", workloadUID: "another-service", node: { name: "other-device" }, ready: true }
  const unweighted = { id: "unweighted", workloadUID: "service-uid", node: { name: "unweighted-device" }, ready: false }
  const poolInstance = { id: "pool-only", node: { name: "pools-device" }, ready: true }
  const placement = h.hub.ServicePlacement({ view: idle, signals: {
    pools: { data: { observedAt: idle.observedAt } },
    scheduler: { data: { state: { snapshot: { at: snapshotAt, instances: [instance, shared, unrelated, unweighted], routing: { "service-uid": { instance: 2, shared: 0 } } } } } },
    instances: [poolInstance],
  } })
  const table = elements(placement).find(element => element.type === "data-table")!
  assert.deepEqual(table.props.data, [instance, shared, unweighted])
  const columns = table.props.columns as {key: string; value: (row: unknown) => unknown}[]
  const weight = columns.find(column => column.key === "weight")!
  assert.equal(weight.value(instance), 2)
  assert.equal(weight.value(shared), 0)
  assert.equal(weight.value(unweighted), "미제공")
  const poolTable = elements(placement).filter(element => element.type === "data-table")[1]
  assert.deepEqual(poolTable.props.data, [poolInstance])
  const facts = elements(placement).filter(element => element.type === "facts").map(element => element.props.items)
  assert.deepEqual(facts, [
    [["분배 기준 시각 (KST)", new Date(snapshotAt).toLocaleString("ko-KR", { timeZone: "Asia/Seoul" })]],
    [["배치 관측 시각 (KST)", new Date(idle.observedAt).toLocaleString("ko-KR", { timeZone: "Asia/Seoul" })]],
  ])
})

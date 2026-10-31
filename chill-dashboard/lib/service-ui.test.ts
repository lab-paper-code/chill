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

test("distribution joins require the same UID and observation time", () => {
  const h = harness(), instance = { id: "instance", node: { name: "device" }, ready: true }
  function weight(uid: string, at: string) {
    const placement = h.hub.ServicePlacement({ view: idle, signals: { pools: { data: { observedAt: idle.observedAt } }, scheduler: { data: { state: { snapshot: { at, workloads: [{ uid }], routing: { "service-uid": { instance: 2 } } } } } }, instances: [instance] } })
    const table = elements(placement).find(element => element.type === "data-table")!
    const columns = table.props.columns as {key: string; value: (row: unknown) => unknown}[]
    return columns.find(column => column.key === "weight")!.value(instance)
  }
  assert.equal(weight("service-uid", idle.observedAt), 2)
  assert.equal(weight("replacement", idle.observedAt), "미확인")
  assert.equal(weight("service-uid", "2026-10-31T23:59:00Z"), "미확인")
})

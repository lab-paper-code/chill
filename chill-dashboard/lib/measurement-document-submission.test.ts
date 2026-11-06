import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as requestHelpers from "./measurement-request.ts"
import * as documentHelpers from "./measurement-document.ts"
import * as guidance from "./measurement-guidance.ts"

type Tree = { type: unknown; props: Record<string, unknown> }
type Component = (props: Record<string, unknown>) => unknown
type Request = { path: string; method: string; body: documentHelpers.MeasurementRequestDocument }
type Response = { status: number; body: string; data: unknown }
type Failure = Error & { status: number; body: string; data: unknown; unknownOutcome?: boolean }
type Callbacks = { onSuccess?: (response: Response) => void; onError?: (error: Failure) => void }
type ConsumerActions = { view: () => unknown; paste: (value: string) => void; review: () => void; click: (name: RegExp, allowDisabled?: boolean) => void; mutations: { resolve: (data: unknown) => void }[] }
const require = createRequire(import.meta.url)
const jsx = (type: unknown, props: Record<string, unknown>): Tree => ({ type, props })
function text(value: unknown): string {
  if (Array.isArray(value)) return value.map(text).join(" ")
  if (value && typeof value === "object" && "props" in value) return text((value as Tree).props.children)
  return typeof value === "string" || typeof value === "number" ? String(value) : ""
}

function consumer() {
  const states = new Map<Component, unknown[]>()
  let values: unknown[] = [], cursor = 0, mutationCursor = 0, refetches = 0, namespace = "lab"
  const sent: Request[] = [], routes: string[] = []
  function boundary() {
    let callbacks: Callbacks = {}
    const mutation = {
      isPending: false, isSuccess: false, data: undefined as Response | undefined, error: null as Failure | null,
      reset() { mutation.isPending = false; mutation.isSuccess = false; mutation.data = undefined; mutation.error = null },
      mutate(input: Request, handlers: Callbacks = {}) {
        mutation.reset(); mutation.isPending = true; callbacks = handlers; sent.push(input)
      },
      resolve(data: unknown) {
        mutation.isPending = false; mutation.isSuccess = true; mutation.data = { status: 200, body: "", data }
        callbacks.onSuccess?.(mutation.data)
      },
      reject(error: Failure) { mutation.isPending = false; mutation.error = error; callbacks.onError?.(error) },
    }
    return mutation
  }
  const mutations = [boundary(), boundary(), boundary()]
  const react = {
    useId: () => "document-input",
    useRef(current: unknown) {
      const index = cursor++
      if (!Object.hasOwn(values, index)) values[index] = {current}
      return values[index]
    },
    useState(initial: unknown) {
      const index = cursor++
      if (!Object.hasOwn(values, index)) values[index] = typeof initial === "function" ? initial() : initial
      const state = values
      return [state[index], (next: unknown) => { state[index] = typeof next === "function" ? next(state[index]) : next }]
    },
  }
  const modules: Record<string, unknown> = {
    react, "react/jsx-runtime": { jsx, jsxs: jsx, Fragment: "fragment" },
    "next/link": { __esModule: true, default: "a" },
    "next/navigation": { useRouter: () => ({ replace: (href: string) => routes.push(href) }) },
    "@/lib/client": { ApiError: Error, useChillMutation: () => ({...mutations[mutationCursor++]}) },
    "@/hooks/use-measurement-data": { useMeasurementPlans: () => ({ data: { items: [] }, isFetching: false, refetch: async () => { refetches++; return { error: null } } }) },
    "@/lib/measurement-request": requestHelpers, "@/lib/measurement-document": documentHelpers, "@/lib/measurement-guidance": guidance,
    "@/components/ui/button": { Button: "button" }, "@/components/ui/input": { Input: "input" },
    "@/components/screen": {
      Screen: ({ children }: Record<string, unknown>) => jsx("main", { children }),
      Facts: ({ items }: { items: unknown[][] }) => jsx("dl", { children: items.map(item => jsx("dd", { children: item[1] })) }),
    },
    "@/components/read-region": { SourceJSON: ({ value }: Record<string, unknown>) => jsx("pre", { children: JSON.stringify(value) }), SourceExport: () => null },
    "@/components/measurement-request-source": { MeasurementRequestSource: ({ value }: Record<string, unknown>) => jsx("pre", { children: JSON.stringify(value) }) },
    "@/components/measurement-navigation": { MeasurementNavigation: () => null },
    "@/features/read-pages": { preflightConclusion: () => "", PreflightAnswer: ({ answer }: Record<string, unknown>) => jsx("pre", { children: JSON.stringify(answer) }) },
  }
  function load(path: string): Record<string, Component> {
    const compiled = transpileModule(readFileSync(new URL(path, import.meta.url), "utf8"), { compilerOptions: { target: ScriptTarget.ES2022, module: ModuleKind.CommonJS, jsx: JsxEmit.ReactJSX } }).outputText
    const exports = {}
    new Function("require", "exports", compiled)((name: string) => {
      if (Object.hasOwn(modules, name)) return modules[name]
      if (name.startsWith("@/")) return modules[name] = load(`../${name.slice(2)}${name.includes("hooks/") ? ".ts" : ".tsx"}`)
      return require(name)
    }, exports)
    return exports
  }
  const { MeasurementDocumentRequestPage } = load("../features/measurement-document-request.tsx")
  function render(Component: Component, props: Record<string, unknown>) {
    cursor = 0
    if (!states.has(Component)) states.set(Component, [])
    values = states.get(Component)!
    return Component(props)
  }
  let nodes: Tree[] = []
  function expand(value: unknown, disabled = false): unknown {
    if (Array.isArray(value)) return value.map(item => expand(item, disabled))
    if (!value || typeof value !== "object" || !("props" in value)) return value
    const node = value as Tree
    if (typeof node.type === "function") return expand(render(node.type as Component, node.props), disabled)
    const inherited = disabled || (node.type === "fieldset" && !!node.props.disabled)
    const expanded = { ...node, props: { ...node.props, ...(["button", "input", "select", "textarea"].includes(String(node.type)) && disabled ? { disabled: true } : {}), children: expand(node.props.children, inherited) } }
    nodes.push(expanded)
    return expanded
  }
  function view() { nodes = []; mutationCursor = 0; return expand(render(MeasurementDocumentRequestPage, { namespace })) }
  function button(name: RegExp) {
    const found = nodes.find(node => node.type === "button" && name.test(text(node)))
    assert.ok(found, `Missing action ${name}`)
    return found
  }
  function click(name: RegExp, allowDisabled = false) {
    const found = button(name)
    if (!allowDisabled) assert.ok(!found.props.disabled)
    return (found.props.onClick as () => void)()
  }
  function paste(value: string) {
    const field = nodes.find(node => node.type === "textarea")!
    ;(field.props.onChange as (event: unknown) => void)({ target: { value } })
  }
  function review() { (nodes.find(node => node.type === "form")!.props.onSubmit as (event: unknown) => void)({ preventDefault() {} }) }
  return { sent, routes, mutations, view, button, click, paste, review, nodes: () => nodes, refetches: () => refetches, selectNamespace: (value: string) => { namespace = value } }
}

const source = "apiVersion: edge.dacs.io/v1alpha1\nkind: BatchEnergyOptimization\nmetadata: {name: operator-request, namespace: lab, labels: {campaign: operator}, annotations: {source: producer}}\nspec:\n  activeProtocol: {workload: provided-service, durationSeconds: 17}\n  retryLimit: 2\n  producerExtension: {enabled: false, limit: 0}\n"
const check = /선택 사전검사|검사 중/
const submit = /측정 요청 제출/
const lookup = /접수 기록 조회/
function prepare(c: ConsumerActions) { c.view(); c.paste(source); c.view(); c.review(); c.view() }
function pass(c: ConsumerActions) {
  c.click(check)
  c.mutations[0].resolve({ request: "NOT_SUBMITTED", task: null })
  c.mutations[1].resolve({ resolves: true })
  c.view()
}
function failure(status: number, body: string, unknownOutcome = false): Failure {
  return Object.assign(new Error(body), { status, body, data: { code: "InvalidDefinition" }, unknownOutcome })
}

test("local review enables authoritative submission without optional checks", () => {
  const c = consumer()
  prepare(c)
  assert.deepEqual(c.sent, [])
  assert.equal(c.button(submit).props.disabled, false)
  c.click(submit)
  const request = c.sent.at(-1)!
  assert.equal(request.path, "v1/namespaces/lab/batch-energy-optimizations")
  assert.equal(request.method, "POST")
  c.mutations[2].resolve({ request: "Accepted", task: { metadata: { name: "operator-request", namespace: "lab", uid: "accepted-uid" } } })
  assert.ok(c.routes.at(-1)?.includes("uid=accepted-uid"))
})

test("lost, rejected or still-pending diagnostics do not block the final server decision", () => {
  for (const diagnostic of ["lost", "rejected", "pending"]) {
    const c = consumer()
    prepare(c); c.click(check); c.view()
    if (diagnostic === "lost") c.mutations[0].reject(failure(0, "check response lost"))
    if (diagnostic === "rejected") c.mutations[0].reject(failure(422, "previous conditions rejected"))
    if (diagnostic !== "pending") c.mutations[1].resolve({ resolves: false, reason: "previous plan unavailable" })
    c.view()
    assert.equal(c.button(submit).props.disabled, false, diagnostic)
    c.click(submit)
    assert.equal(c.sent.at(-1)?.path, "v1/namespaces/lab/batch-energy-optimizations")
    c.mutations[2].resolve({ request: "Accepted", task: { metadata: { name: "operator-request", namespace: "lab", uid: "server-decided" } } })
    assert.ok(c.routes.at(-1)?.includes("uid=server-decided"), diagnostic)
  }
})

test("editing requires local review, then submits the new conditions without another diagnostic", () => {
  const c = consumer()
  prepare(c); pass(c)
  c.paste(source.replace("durationSeconds: 17", "durationSeconds: 23")); c.view()
  assert.equal(c.button(submit).props.disabled, true)
  c.review(); c.view()
  assert.equal(c.button(submit).props.disabled, false)
  c.click(submit)
  assert.equal((c.sent.at(-1)!.body.spec.activeProtocol as Record<string, unknown>).durationSeconds, 23)
})

test("actual submission errors retain the draft and server diagnostics", () => {
  for (const [status, message] of [[400, "unknown field spec.batchSizes"], [401, "authentication required"], [403, "permission denied"], [422, "conditions rejected"]] as const) {
    const c = consumer()
    const draft = status === 400 ? source + "  batchSizes: [1, 2]\n" : source
    c.view(); c.paste(draft); c.view(); c.review(); c.view(); c.click(submit)
    c.mutations[2].reject(failure(status, message)); c.view()
    assert.equal(c.nodes().find(node => node.type === "textarea")!.props.value, draft)
    assert.ok(c.nodes().some(node => node.props.role === "alert" && text(node).includes(message)))
    assert.deepEqual(c.routes, [])
  }
})

test("namespace changes cannot send a reviewed document across the namespace boundary", () => {
  const c = consumer()
  prepare(c); c.selectNamespace("other-lab"); c.view()
  assert.equal(c.button(submit).props.disabled, true)
  assert.equal(c.nodes().find(node => node.type === "textarea")!.props.value, source)
  assert.deepEqual(c.sent, [])
})

test("an in-flight final POST blocks a second write, diagnostics and editing", () => {
  const c = consumer()
  prepare(c); c.click(submit); c.click(submit); c.view()
  assert.equal(c.button(submit).props.disabled, true)
  assert.equal(c.button(check).props.disabled, true)
  assert.equal(c.nodes().find(node => node.type === "textarea")!.props.disabled, true)
  c.click(submit, true); c.click(check, true)
  assert.deepEqual(c.sent.map(request => request.path), ["v1/namespaces/lab/batch-energy-optimizations"])
})

test("unknown final outcome blocks further writes and editing while read-only lookup remains usable", async () => {
  const c = consumer()
  prepare(c); c.click(submit)
  c.mutations[2].reject(failure(0, "response lost", true)); c.view()
  assert.equal(c.button(check).props.disabled, true)
  assert.equal(c.button(submit).props.disabled, true)
  assert.equal(c.nodes().find(node => node.type === "textarea")!.props.disabled, true)
  assert.equal(c.button(lookup).props.disabled, false)
  c.click(check, true); c.click(submit, true); c.paste("replacement draft"); c.review(); c.view()
  assert.deepEqual(c.sent.map(request => request.path), ["v1/namespaces/lab/batch-energy-optimizations"])
  assert.equal(c.nodes().find(node => node.type === "textarea")!.props.value, source)
  c.click(lookup)
  await Promise.resolve()
  c.view()
  assert.equal(c.refetches(), 1)
  assert.equal(c.button(submit).props.disabled, true)
})

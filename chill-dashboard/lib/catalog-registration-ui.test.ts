import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as domain from "./domain.ts"
import * as manifest from "./catalog-manifest.ts"
import * as labels from "./labels.ts"

type Tree = { type: unknown; props: Record<string, unknown> }
type Component = (props: Record<string, unknown>) => unknown
const require = createRequire(import.meta.url)
const jsx = (type: unknown, props: Record<string, unknown>): Tree => ({ type, props })
function text(value: unknown): string {
  if (Array.isArray(value)) return value.map(text).join(" ")
  if (value && typeof value === "object" && "props" in value) return text((value as Tree).props.children)
  return typeof value === "string" || typeof value === "number" ? String(value) : ""
}
function consumer(kind: manifest.CatalogKind) {
  const states = new Map<unknown, unknown[]>()
  let values: unknown[] = [], cursor = 0
  const react = { useState(initial: unknown) {
    const index = cursor++
    if (!Object.hasOwn(values, index)) values[index] = typeof initial === "function" ? initial() : initial
    const state = values
    return [state[index], (next: unknown) => { state[index] = typeof next === "function" ? next(state[index]) : next }]
  } }
  function render(Component: Component, props: Record<string, unknown>) {
    cursor = 0
    if (!states.has(Component)) states.set(Component, [])
    values = states.get(Component)!
    return Component(props)
  }
  const sent: { path: string; method: string; body: { name: string; spec: Record<string, unknown> } }[] = []
  const mutation = { isPending: false, error: null as null | Error, mutateAsync: async (input: typeof sent[number]) => {
    sent.push(input)
    if (mutation.error) throw mutation.error
    return { status: 201, body: "{}", data: { metadata: { name: input.body.name, uid: "registered" }, spec: input.body.spec } }
  } }
  const modules: Record<string, unknown> = {
    react, "react/jsx-runtime": { jsx, jsxs: jsx, Fragment: "fragment" },
    "next/link": { __esModule: true, default: "a" }, "next/navigation": { useSearchParams: () => new URLSearchParams() },
    "@tanstack/react-query": { useQueryClient: () => ({ invalidateQueries: async () => {} }) },
    "lucide-react": { ChevronLeft: "svg", ChevronRight: "svg" },
    "@/lib/domain": domain, "@/lib/catalog-manifest": manifest, "@/lib/labels": labels,
    "@/lib/client": { ApiError: Error, useChillMutation: () => mutation, useChillQuery: (path: string) => ({ data: path.includes("workloads") ? [] : { items: [] } }) },
    "@/components/ui/button": { Button: "button" }, "@/components/ui/input": { Input: "input" }, "@/components/ui/dialog": {},
    "@/components/data-table": { DataTable: () => null }, "@/features/service-hub": {}, "@/lib/measurement-request": {},
    "@/components/read-region": { SourceJSON: ({ value }: { value: unknown }) => jsx("pre", { children: JSON.stringify(value) }), StatusBadge: "span" },
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
  const { ResourceEditor } = load("../features/catalog-editor.tsx")
  let nodes: Tree[] = []
  function expand(value: unknown): unknown {
    if (Array.isArray(value)) return value.map(expand)
    if (!value || typeof value !== "object" || !("props" in value)) return value
    const node = value as Tree
    if (typeof node.type === "function") return expand(render(node.type as Component, node.props))
    const expanded = { ...node, props: { ...node.props, children: expand(node.props.children) } }
    nodes.push(expanded)
    return expanded
  }
  function view() { nodes = []; return expand(render(ResourceEditor, { kind })) }
  function button(name: RegExp) {
    const found = nodes.find(node => node.type === "button" && name.test(text(node)))
    assert.ok(found, `Missing action ${name}`)
    return found
  }
  function click(name: RegExp) { const node = button(name); assert.ok(!node.props.disabled); return (node.props.onClick as () => unknown)() }
  function paste(value: string) { const input = nodes.find(node => node.type === "textarea")!; (input.props.onChange as (event: unknown) => void)({ target: { value } }) }
  function review() { (nodes.find(node => node.type === "form")!.props.onSubmit as (event: unknown) => void)({ preventDefault() {} }) }
  return { sent, mutation, view, click, paste, review, nodes: () => nodes }
}

const specs: Record<manifest.CatalogKind, Record<string, unknown>> = {
  models: { artifacts: [{ name: "cpu", format: "onnx", source: { image: "registry.example/multiarch:v3", architecture: "arm64", files: ["/model/a.onnx"] } }, { name: "cpu-x86", format: "onnx", source: { image: "registry.example/multiarch:v3", architecture: "amd64", files: ["/model/a.onnx"] } }], executionPaths: [], producerExtension: { enabled: true, limit: 0 } },
  preprocessors: { input: { fields: [] }, output: { fields: [] }, artifacts: [], maxNewTokens: 128, producerExtension: { note: "retain" } },
  datasets: { source: { image: "registry.example/data:release", path: "/data" }, fields: [], producerExtension: { weights: [0.1, 0.9] } },
}

test("producer import reviews all catalog kinds and only explicit registration POSTs the provided spec", async () => {
  for (const kind of Object.keys(specs) as manifest.CatalogKind[]) {
    const c = consumer(kind)
    c.view(); c.click(/YAML\/JSON/); c.view()
    const source = JSON.stringify({ ...manifest.buildCatalogManifest(kind, "producer-entry", specs[kind]), metadata: { name: "producer-entry", uid: "source-uid", resourceVersion: "88" }, status: { ready: true } })
    c.paste(source); c.view(); c.review()
    const reviewed = c.view()
    assert.equal(c.sent.length, 0)
    assert.match(text(reviewed), /producer-entry/)
    assert.match(text(reviewed), /producerExtension/)
    assert.match(text(reviewed), new RegExp(manifest.catalogManifestKinds[kind]))
    await c.click(/등록하기/)
    assert.equal(c.sent.length, 1)
    const request = c.sent[0]
    assert.equal(request.path, `v1/${kind}`)
    assert.equal(request.method, "POST")
    assert.equal(request.body.name, "producer-entry")
    assert.equal(Object.keys(request.body).length, 2)
    const extension = request.body.spec.producerExtension as Record<string, unknown>
    assert.ok(extension)
    if (kind === "models") {
      const files = request.body.spec.artifacts as { source: { image: string; architecture: string } }[]
      assert.equal(files.length, 2)
      assert.equal(files[1].source.architecture, "amd64")
      assert.equal(files[0].source.image, "registry.example/multiarch:v3")
      assert.equal(extension.limit, 0)
    } else if (kind === "preprocessors") assert.equal(request.body.spec.maxNewTokens, 128)
    else assert.equal((extension.weights as number[])[1], 0.9)
    c.view()
    assert.ok(!c.nodes().some(node => node.type === "button" && /등록하기/.test(text(node))))
    assert.equal(c.sent.length, 1)
  }
})

test("local YAML file supports multiarch references without import-time requests", async () => {
  const c = consumer("models")
  c.view(); c.click(/YAML\/JSON/); c.view()
  const yaml = readFileSync(new URL("../../config/samples/catalog/edge_v1alpha1_modelspec.yaml", import.meta.url), "utf8")
  const file = c.nodes().find(node => node.type === "input" && node.props.type === "file")!
  await (file.props.onChange as (event: unknown) => Promise<void>)({ target: { files: [{ text: async () => yaml }] } })
  c.view(); c.review(); c.view()
  assert.equal(c.sent.length, 0)
  await c.click(/등록하기/)
  const artifacts = c.sent[0].body.spec.artifacts as { source: { architecture: string; image: string } }[]
  assert.ok(artifacts.some(a => a.source.architecture === "arm64"))
  assert.ok(artifacts.some(a => a.source.architecture === "amd64"))
  c.view()
  assert.ok(!c.nodes().some(node => node.type === "button" && /등록하기/.test(text(node))))
  assert.equal(c.sent.length, 1)
})

test("existing API registration JSON is accepted without replacing supplied fields", async () => {
  const c = consumer("datasets")
  c.view(); c.click(/YAML\/JSON/); c.view()
  c.paste('{"name":"api-entry","spec":{"source":{"image":"registry.example/data:current","path":"/producer"},"fields":[],"future":false}}')
  c.view(); c.review(); c.view()
  assert.equal(c.sent.length, 0)
  await c.click(/등록하기/)
  assert.equal(c.sent[0].body.name, "api-entry")
  assert.equal(c.sent[0].body.spec.future, false)
  const source = c.sent[0].body.spec.source
  assert.ok(source && typeof source === "object" && "path" in source)
  assert.equal(source.path, "/producer")
})

test("invalid documents and mismatched kinds cannot register; editing invalidates review", () => {
  const c = consumer("datasets")
  c.view(); c.click(/YAML\/JSON/); c.view()
  for (const invalid of ["spec: [", "name: x\nspec: {}\n---\nname: y\nspec: {}", JSON.stringify(manifest.buildCatalogManifest("models", "wrong", {})), "apiVersion: edge.dacs.io/v2\nkind: Dataset\nmetadata: {name: x}\nspec: {}", "name: x\nspec: []", "name: x\nspec: {large: 9007199254740993}", '{"name":"x","spec":{"large":9007199254740993}}', "name: x\nspec: {limit: .inf}", "name: x\nspec: &self {nested: *self}", "name: x\nspec: {field: a, field: b}"]) {
    c.paste(invalid); c.view(); c.review(); c.view()
    assert.ok(c.nodes().some(node => node.props.role === "alert"))
    assert.ok(!c.nodes().some(node => node.type === "button" && /등록하기/.test(text(node))))
  }
  c.paste('{"name":"api-entry","spec":{"fields":[],"future":true}}'); c.view(); c.review(); c.view()
  assert.ok(c.nodes().some(node => node.type === "button" && /등록하기/.test(text(node))))
  c.paste("name: changed\nspec: {}"); c.view()
  assert.ok(!c.nodes().some(node => node.type === "button" && /등록하기/.test(text(node))))
  assert.equal(c.sent.length, 0)
})

test("server rejection retains original input and error without retrying or defaulting", async () => {
  const c = consumer("preprocessors")
  c.view(); c.click(/YAML\/JSON/); c.view()
  const source = "name: rejected\nspec:\n  artifacts: []\n  producerSetting: keep-this\n"
  c.paste(source); c.view(); c.review(); c.view()
  c.mutation.error = Object.assign(new Error("server rejection"), { status: 422, data: { code: "InvalidDefinition", field: "spec.artifacts", message: "producer target does not exist" }, body: "producer target does not exist" })
  await c.click(/등록하기/)
  const result = c.view()
  assert.equal(c.sent.length, 1)
  assert.equal(c.sent[0].body.spec.producerSetting, "keep-this")
  assert.equal(c.nodes().find(node => node.type === "textarea")?.props.value, source)
  assert.match(text(result), /producer target does not exist/)
  assert.ok(c.nodes().some(node => node.props.role === "alert"))
  c.view()
  assert.equal(c.sent.length, 1)
})

test("response loss blocks resubmission and method switching until the registration is checked", async () => {
  const c = consumer("datasets")
  c.view(); c.click(/YAML\/JSON/); c.view()
  c.paste('{"name":"uncertain","spec":{"fields":[]}}'); c.view(); c.review(); c.view()
  c.mutation.error = Object.assign(new Error("response lost"), { unknownOutcome: true })
  await c.click(/등록하기/)
  c.view()
  assert.equal(c.sent.length, 1)
  const register = c.nodes().find(node => node.type === "button" && /등록하기/.test(text(node)))!
  assert.equal(register.props.disabled, true)
  const method = c.nodes().find(node => node.type === "button" && /등록 방법/.test(text(node)))!
  assert.equal(method.props.disabled, true)
  await (register.props.onClick as () => Promise<void>)()
  assert.equal(c.sent.length, 1)
})

test("manual registration remains available at the original name step", () => {
  for (const kind of Object.keys(specs) as manifest.CatalogKind[]) {
    const c = consumer(kind)
    c.view(); c.click(/직접 작성/); c.view()
    assert.ok(c.nodes().some(node => node.type === "input" && node.props.required))
    assert.ok(c.nodes().some(node => node.type === "form"))
    assert.equal(c.sent.length, 0)
  }
})

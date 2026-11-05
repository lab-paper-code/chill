import assert from "node:assert/strict"
import test from "node:test"
import { elements, hookRunner, load, type Tree } from "../lib/evaluation-author-test-helpers.ts"
import * as document from "../lib/measurement-document.ts"

const source = "apiVersion: edge.dacs.io/v1alpha1\nkind: BatchEnergyOptimization\nmetadata: {name: operator-request, namespace: lab}\nspec: {producerExtension: keep}\n"

function consumer() {
  const hooks = hookRunner()
  let body: document.MeasurementRequestDocument | undefined
  let reviews = 0
  const props = { namespace: "lab", locked: false, reviewed: false, example: source,
    onReview(value: document.MeasurementRequestDocument) { body = value; reviews++; props.reviewed = true },
    onEdit() { body = undefined; props.reviewed = false },
  }
  const { MeasurementDocumentInput } = load("../components/measurement-document-input.tsx", {
    react: { ...hooks.react, useId: () => "measurement-document" },
    "@/components/ui/button": { Button: "button" }, "@/components/ui/input": { Input: "input" },
    "@/lib/measurement-document": document,
  })
  let nodes: Tree[] = []
  function view() { nodes = elements(hooks.render(MeasurementDocumentInput, props)); return nodes }
  function node(type: string) { const found = nodes.find(node => node.type === type); assert.ok(found); return found }
  function paste(value: string) { (node("textarea").props.onChange as (event: unknown) => void)({ target: { value } }) }
  function review() { (node("form").props.onSubmit as (event: unknown) => void)({ preventDefault() {} }) }
  function insert() { const button = nodes.find(node => node.type === "button" && node.props.type === "button")!; (button.props.onClick as () => void)() }
  async function file(text: () => Promise<string>) { await (node("input").props.onChange as (event: unknown) => Promise<void>)({ target: { files: [{ text }] } }) }
  return { props, view, node, paste, review, insert, file, body: () => body, reviews: () => reviews }
}

test("paste and file selection only prepare local drafts; explicit review returns the provided document", async () => {
  const c = consumer()
  c.view(); c.paste(source); c.view()
  assert.equal(c.reviews(), 0)
  c.review(); c.view()
  assert.equal(c.body()?.spec.producerExtension, "keep")
  c.paste(source.replace("keep", "edited")); c.view()
  assert.equal(c.body(), undefined)
  c.review(); c.view()
  assert.equal(c.body()?.spec.producerExtension, "edited")
  await c.file(async () => source.replace("keep", "from-file")); c.view()
  assert.equal(c.body(), undefined)
  assert.equal(c.reviews(), 2)
  c.review(); c.view()
  assert.equal(c.body()?.spec.producerExtension, "from-file")
  c.props.namespace = "other"; c.view()
  c.review(); c.view()
  assert.equal(c.body(), undefined)
  assert.ok(c.view().some(node => node.props.role === "alert"))
  c.props.namespace = "lab"; c.view()
  c.paste(source.replace("BatchEnergyOptimization", "Dataset")); c.view(); c.review(); c.view()
  assert.equal(c.body(), undefined)
  assert.ok(c.view().some(node => node.props.role === "alert"))
})

test("example never replaces a draft and locked handlers cannot change or review input", async () => {
  const c = consumer()
  c.view(); c.insert(); c.view()
  assert.equal(c.node("textarea").props.value, source)
  assert.equal(c.reviews(), 0)
  const draft = source.replace("keep", "operator-draft")
  c.paste(draft); c.view(); c.review(); c.view(); c.insert(); c.view()
  assert.equal(c.node("textarea").props.value, draft)
  assert.equal(c.body()?.spec.producerExtension, "operator-draft")
  c.props.locked = true; c.view()
  assert.equal(c.node("fieldset").props.disabled, true)
  c.paste("overwritten"); c.insert(); c.review(); await c.file(async () => "overwritten"); c.view()
  assert.equal(c.node("textarea").props.value, draft)
  assert.equal(c.body()?.spec.producerExtension, "operator-draft")
  assert.equal(c.reviews(), 1)
})

test("file reads lock draft controls and read failures preserve the previous draft with a readable error", async () => {
  const c = consumer()
  c.view(); c.paste(source); c.view(); c.review(); c.view()
  const { promise, reject: rejectRead } = Promise.withResolvers<string>()
  const pending = c.file(() => promise)
  c.view()
  assert.equal(c.node("fieldset").props.disabled, true)
  assert.equal(c.body(), undefined)
  c.paste("overwritten"); c.review()
  rejectRead(new Error("local read failed"))
  await pending
  c.view()
  assert.equal(c.node("textarea").props.value, source)
  assert.equal(c.reviews(), 1)
  c.props.locked = true
  const nodes = c.view()
  const error = nodes.find(node => node.props.role === "alert")
  assert.ok(error)
  assert.ok(!elements(c.node("fieldset")).includes(error))
  assert.equal(c.node("textarea").props["aria-invalid"], true)
})

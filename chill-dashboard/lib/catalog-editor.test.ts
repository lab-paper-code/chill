import assert from "node:assert/strict"
import test from "node:test"
import { blankDraft, diff, draftFrom, summarize, validate } from "./catalog-editor.ts"

test("blank drafts need names; filled model passes and bad backend is flagged", () => {
  const draft = blankDraft("models")
  assert.ok(validate(draft, "", true).name)
  assert.ok(validate(draft, "m", true)["artifacts.0.name"])
  if (draft.kind !== "models") throw new Error("kind")
  draft.spec.artifacts[0] = { name: "a", format: "onnx", source: { image: "r@sha256:1", architecture: "amd64", files: ["/m.onnx"] } }
  draft.spec.executionPaths[0] = { ...draft.spec.executionPaths[0], name: "p", artifact: "a" }
  assert.deepEqual(validate(draft, "m", true), {})
  draft.spec.executionPaths[0].runtime.backend = "rknnlite"
  assert.ok(validate(draft, "m", false)["executionPaths.0.backend"])
})

test("diff lists changed, added and removed leaves; draftFrom rejects other kinds", () => {
  const before = { artifacts: [{ name: "a", source: { files: ["/x"] } }] }
  const after = { artifacts: [{ name: "b", source: { files: ["/x", "/y"] } }, { name: "c" }] }
  assert.deepEqual(diff(before, after).map(d => [d.label, d.before, d.after]), [["파일 1 이름", "a", "b"], ["파일 1 보관 경로", "/x", "/x, /y"], ["파일 2 이름", "—", "c"]])
  assert.equal(draftFrom("datasets", { artifacts: [], executionPaths: [] }), null)
  assert.deepEqual(summarize("datasets", { source: { path: "/d" }, fields: [{ name: "image" }] }), [["필드", "image"], ["경로", "/d"]])
})

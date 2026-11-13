import assert from "node:assert/strict"
import test from "node:test"
import { checkLabel, checkReason, patchDefault, runNameError, scenarioSeconds, specChanges } from "./experiments-run-editor.ts"

test("preflight check ids map to labels; unknown ids keep the code", () => {
  assert.deepEqual(checkLabel("pool"), { label: "장치 풀" })
  assert.deepEqual(checkLabel("settingsPatch/run"), { label: "공통 설정 변경" })
  assert.deepEqual(checkLabel("loadScenario/e5-a"), { label: "부하 시나리오 e5-a" })
  assert.deepEqual(checkLabel("coverage/x/y"), { label: "서비스 포함 x/y" })
  assert.equal(checkReason("another EvaluationRun occupies the pool"), "다른 실험이 사용 중")
  assert.equal(checkReason("evidenceStore must be chill-store"), "지정 저장소만 가능")
  assert.equal(checkReason("spec.stages[0]: bad"), "spec.stages[0]: bad")
  assert.deepEqual(checkLabel("future/x"), { label: "기타 점검", code: "future/x" })
})

test("run names follow DNS-1123 and must differ from the copied origin", () => {
  assert.equal(runNameError("eval-a.b"), "")
  assert.equal(runNameError(""), "이름 필요")
  assert.equal(runNameError("Eval"), "소문자, 숫자, -, .만")
  assert.equal(runNameError("eval-a-"), "소문자, 숫자, -, .만")
  assert.equal(runNameError("eval", "eval"), "원본과 다른 이름 필요")
})

test("defaults come from current settings, else neutral values", () => {
  assert.equal(patchDefault("controlIntervalNanos", { controlIntervalNanos: 5n } as never), 5n)
  assert.deepEqual(patchDefault("excludedNodes", undefined), [])
  assert.equal(patchDefault("planExactFinalists", undefined), 16)
  assert.equal((patchDefault("controls", undefined) as Record<string, boolean>).power, true)
})

test("scenario length sums parts; spec changes list differing top-level fields", () => {
  assert.equal(scenarioSeconds({ parts: [{ durationSeconds: 60 }, { durationSeconds: 30 }, {}] }), 90)
  const rows = specChanges({ question: "a", stages: [1], judgment: { x: 1 } }, { question: "a", stages: [1, 2], judgment: { x: 2 } })
  assert.deepEqual(rows, [{ label: "단계", before: "1개", after: "2개" }, { label: "판정 기준", before: "있음", after: "변경됨" }])
})

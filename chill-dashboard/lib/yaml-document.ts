import { parse } from "yaml"

export function documentObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

export function parseYAMLDocument(text: string): unknown {
  try { return parse(text, { schema: "core" }) }
  catch (error) { throw new Error(`YAML/JSON 문서 하나를 확인해 주세요. ${(error as Error).message}`) }
}

// YAML aliases and non-finite numbers must not change or disappear during JSON transport.
export function checkJSON(value: unknown, ancestors = new Set<object>()): void {
  if (typeof value === "number" && Number.isInteger(value) && !Number.isSafeInteger(value)) throw new Error("정확히 전달할 수 없는 큰 정수 있음, 값 표현 확인 필요")
  if (value === null || typeof value === "string" || typeof value === "boolean" || (typeof value === "number" && Number.isFinite(value))) return
  if (typeof value !== "object" || ancestors.has(value) || (!Array.isArray(value) && Object.getPrototypeOf(value) !== Object.prototype && Object.getPrototypeOf(value) !== null)) throw new Error("JSON으로 전달할 수 없는 값이나 순환 참조 있음")
  ancestors.add(value)
  for (const child of Object.values(value)) checkJSON(child, ancestors)
  ancestors.delete(value)
}

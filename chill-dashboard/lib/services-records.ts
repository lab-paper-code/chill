// Display mapping for the service 요청 and 정의 tabs: server outcome and management codes to Korean words.
import type { RequestRow, WireInteger } from "./read-types.ts"
import type { StatusInfo } from "./status-labels.ts"

const outcome: Record<string, StatusInfo> = {
  completed: { label: "완료", tone: "ok" },
  rejected: { label: "접수 거부", tone: "warn" },
  error: { label: "오류", tone: "fail" },
  timeout: { label: "시간 초과", tone: "fail" },
  canceled: { label: "취소", tone: "idle" },
  incomplete: { label: "미완료", tone: "warn" },
  delivery_failed: { label: "전달 실패", tone: "fail" },
  preprocess_failed: { label: "전처리 실패", tone: "fail" },
}
export const outcomeCodes = Object.keys(outcome)

/** Outcome of a request row; a row without outcome is still open (assigned or running). */
export function requestResult(row: Pick<RequestRow, "outcome" | "kind">): StatusInfo {
  if (row.outcome) return Object.hasOwn(outcome, row.outcome) ? outcome[row.outcome] : { label: "—", tone: "idle", code: row.outcome }
  return row.kind === "assigned" ? { label: "배정됨", tone: "progress" } : { label: "처리 중", tone: "progress" }
}
export const outcomeLabel = (code: string) => (Object.hasOwn(outcome, code) ? outcome[code].label : "—")

export const nanosToMs = (value: WireInteger | null | undefined) => (value == null ? null : Number(value) / 1e6)

/** The batch stage durations the server measured; null until the execution record arrived. */
export function requestStages(row: RequestRow): { key: string; label: string; ms: number }[] | null {
  const stages = [
    { key: "queue", label: "대기", ms: row.queueMS },
    { key: "preprocess", label: "전처리", ms: row.preprocessMS },
    { key: "backend", label: "추론", ms: row.backendMS },
    { key: "postprocess", label: "후처리", ms: row.postprocessMS },
  ]
  const known = stages.filter((stage): stage is { key: string; label: string; ms: number } => stage.ms !== undefined)
  return known.length ? known : null
}

/** Offsets from the receive time, in order. Absent stages were not reached. */
export function requestMarks(row: RequestRow): { label: string; nanos: WireInteger }[] {
  const marks: [string, WireInteger | undefined][] = [
    ["입력 검사", row.validatedNanos], ["접수", row.acceptedNanos], ["배정", row.dispatchedNanos], ["대기열 진입", row.queuedNanos],
    ["실행 시작", row.runningNanos], ["실행 완료", row.executedNanos], ["응답 완료", row.exchangeNanos],
  ]
  return marks.filter((mark): mark is [string, WireInteger] => mark[1] !== undefined).map(([label, nanos]) => ({ label, nanos }))
}

const managementKinds: Record<string, string> = { Update: "수정", Delete: "삭제", ModelUpdate: "모델 변경", PreprocessorUpdate: "전처리기 변경" }
const managementPhases: Record<string, string> = { Stopping: "중지 중", Draining: "비우는 중", Detaching: "분리 중", Applying: "적용 중", Cleaning: "정리 중", Completed: "완료", Failed: "실패" }
export const managementKind = (code: string) => managementKinds[code] ?? "—"
export const managementPhase = (code: string) => managementPhases[code] ?? "—"

/** Result of one management event: an error or the Failed phase is a failure, Completed is done, anything else is in progress. */
export function managementResult(event: { phase: string; error?: string }): StatusInfo {
  if (event.error || event.phase === "Failed") return { label: "실패", tone: "fail" }
  if (event.phase === "Completed") return { label: "완료", tone: "ok" }
  return { label: "진행 중", tone: "progress" }
}

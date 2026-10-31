import { statusLabel } from "./labels.ts"

export const ABSENCE_LABELS = ["미기록", "미제공", "미확인", "판정 불가", "해당 없음", "지원 안 함", "결과 미확인", "권한 없음", "조회 실패", "오래된 관측"] as const
export type AbsenceLabel = typeof ABSENCE_LABELS[number]

export const STATUS_AXES = {
  serviceImpact: "서비스 영향", performance: "성능 판정", observation: "관측 상태", experiment: "실험 상태",
  admission: "접수 허용", placement: "배치", arrivals: "실제 유입", currentUse: "현재 사용",
  quality: "품질 판정", execution: "실행", measurement: "측정", cleanup: "정리",
} as const
export type StatusAxis = typeof STATUS_AXES[keyof typeof STATUS_AXES]
export type StatusTone = "ok" | "warn" | "fail" | "info" | "idle"
export type StatusValue = {axis: StatusAxis; label: string; tone: StatusTone; reason?: string}

// Display synonyms only. Callers still distinguish an empty list, zero, and a missing field.
const absenceSynonyms: Record<string, AbsenceLabel> = {
  "기록 없음": "미기록", "기록되지 않음": "미기록",
  "제공 없음": "미제공", "제공되지 않음": "미제공", "계산값 없음": "미제공", "사유 미제공": "미제공", "사유 제공 없음": "미제공",
  "확인 불가": "미확인", "확인 안 됨": "미확인", "정보 없음": "미확인", "확인 중": "미확인", "확인이 필요해요": "미확인",
  "평가 불가": "판정 불가", "미지원": "지원 안 함", "결과 불명": "결과 미확인",
  "접근 권한 없음": "권한 없음", "불러오기 실패": "조회 실패", "관측 지연": "오래된 관측",
}

export function normalizeStatusLabel(value: string | null | undefined): string {
  if (!value?.trim()) return "미확인"
  const label = statusLabel(value.trim())
  return absenceSynonyms[label] ?? label
}

/** Lift a shared state to the header only when axis, state, tone AND reason match. Never removes a column. */
export function commonStatus(values: readonly StatusValue[]): StatusValue | undefined {
  const first = values[0]
  if (!first) return undefined
  const label = normalizeStatusLabel(first.label)
  return values.every(value => value.axis === first.axis && normalizeStatusLabel(value.label) === label && value.tone === first.tone && value.reason === first.reason)
    ? {...first, label} : undefined
}

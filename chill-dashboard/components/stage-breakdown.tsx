import type { BackendProgress } from "@/lib/read-types"

// Plain Korean duration: 0초, 1밀리초 미만, 350밀리초, 9.4초, 1분 30초, 2시간 15분.
export function duration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return "0초"
  if (ms < 1) return "1밀리초 미만"
  if (ms < 1000) return `${Math.round(ms)}밀리초`
  if (ms < 59950) return `${Number((ms / 1000).toFixed(1))}초`
  if (ms < 3570000) { const s = Math.round(ms / 1000), m = Math.floor(s / 60); return s % 60 ? `${m}분 ${s % 60}초` : `${m}분` }
  const m = Math.round(ms / 60000), h = Math.floor(m / 60)
  return m % 60 ? `${h}시간 ${m % 60}분` : `${h}시간`
}

type Stages = Pick<BackendProgress, "queueMS" | "preprocessMS" | "backendMS" | "postprocessMS" | "firstTokenMS" | "units" | "batchSize">

// One request's stages on one scale. Batch stage spans are shared by the batch; units belong to this request.
export function StageBreakdown({ progress, generative = progress.firstTokenMS !== undefined }: { progress: Stages; generative?: boolean }) {
  const stages: [string, number][] = [["대기", progress.queueMS], ["전처리", progress.preprocessMS], [generative ? "답 생성" : "추론", progress.backendMS], ["후처리", progress.postprocessMS]]
  const total = stages.reduce((sum, [, value]) => sum + Math.max(value, 0), 0)
  const notes = [
    progress.firstTokenMS !== undefined && `첫 토큰까지 ${duration(progress.firstTokenMS)}`,
    progress.units !== undefined && `처리 단위 ${progress.units}개`,
    (progress.batchSize ?? 0) > 1 && `같은 배치 ${progress.batchSize}건 · 단계 시간은 같은 배치가 함께 쓴 시간이에요`,
  ].filter(Boolean)
  return <div className="grid gap-3">
    <dl className="energy-rows" aria-label="단계별 시간">{stages.map(([label, value]) => <div key={label} className="energy-row"><dt>{label}</dt>
      <div className="energy-track" aria-hidden="true">{total > 0 && value > 0 && <div className="energy-fill" style={{ width: `${Math.max(value / total * 100, 2)}%` }} />}</div>
      <dd>{duration(value)}</dd></div>)}</dl>
    {notes.map(note => <p key={note as string} className="text-sm text-muted-foreground">{note}</p>)}
  </div>
}

"use client"

import * as React from "react"
import { Inbox, Lock, RefreshCw, ServerCog, TriangleAlert } from "lucide-react"
import { cn } from "@/lib/utils"
import { ApiError, isCoverageWarming } from "@/lib/client-transport"
import Button from "@/components/custom/blocks/Button"
import { SkeletonBlock } from "@/components/custom/blocks/SkeletonBlock"
import { LoaderBlock } from "@/components/custom/blocks/LoaderBlock"

/** Empty region with an optional next action. `title` is a short label, never a sentence of explanation. */
export function EmptyState({ title, action, icon: Icon = Inbox, compact = false, className }: { title: string; action?: React.ReactNode; icon?: React.ElementType; compact?: boolean; className?: string }) {
  return (
    <div role="status" className={cn("flex flex-col items-center justify-center gap-3 text-center", compact ? "py-8" : "min-h-56 rounded-xl border border-dashed border-border py-12", className)}>
      <span className="grid size-10 place-items-center rounded-full bg-muted text-muted-foreground"><Icon className="size-5" aria-hidden="true" /></span>
      <p className="text-sm font-medium text-foreground">{title}</p>
      {action}
    </div>
  )
}

/** The subset of a react-query result (from useChillQuery) the boundary reads. */
export type BoundaryQuery<T> = {
  data: T | undefined
  error: ApiError | null
  isPending: boolean
  failureReason?: unknown
  refetch: () => unknown
}

const httpLabels: Record<number, string> = { 404: "찾을 수 없음", 409: "충돌", 429: "요청 과다", 502: "서버 연결 실패", 503: "서버 사용 불가", 504: "응답 지연" }

function Skeleton({ kind }: { kind: "table" | "tiles" | "block" }) {
  if (kind === "tiles") return <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4" aria-hidden="true">{[0, 1, 2, 3].map(i => <SkeletonBlock key={i} animation="pulse" height={88} radius={10} />)}</div>
  if (kind === "block") return <SkeletonBlock animation="pulse" height={160} radius={10} aria-hidden="true" />
  return (
    <div className="overflow-hidden rounded-xl border border-border bg-card" aria-hidden="true">
      <SkeletonBlock animation="pulse" height={36} radius={0} />
      {[0, 1, 2, 3, 4].map(i => <div key={i} className="border-t border-border px-3 py-2.5"><SkeletonBlock animation="pulse" height={20} radius={4} /></div>)}
    </div>
  )
}

/**
 * Renders a query region: skeleton while loading, "초기화 중" while ChillServer warms up,
 * a no-permission block for 401/403, an error block with retry, the empty state, or the children.
 */
export function QueryBoundary<T>({ query, children, skeleton = "table", isEmpty, empty, className }: {
  query: BoundaryQuery<T>
  children: (data: T) => React.ReactNode
  skeleton?: "table" | "tiles" | "block" | React.ReactNode
  /** When it returns true the `empty` node is shown instead of the children. */
  isEmpty?: (data: T) => boolean
  empty?: React.ReactNode
  className?: string
}) {
  const { data, error, isPending, failureReason } = query
  let body: React.ReactNode
  if (isCoverageWarming(error) || (data === undefined && isCoverageWarming(failureReason))) {
    body = (
      <div role="status" className="flex min-h-40 flex-col items-center justify-center gap-3 rounded-xl border border-border bg-card py-10">
        <LoaderBlock variant="spinner" size="sm" />
        <p className="text-sm font-medium">초기화 중</p>
      </div>
    )
  } else if (error && [401, 403].includes(error.status)) {
    body = <EmptyState icon={Lock} title={error.status === 401 ? "접속 정보 확인 필요" : "권한 없음"} />
  } else if (error && data === undefined) {
    const code = (error.data as { code?: unknown } | null)?.code
    const detail = typeof code === "string" ? code : error.status ? `HTTP ${error.status}` : "응답 없음"
    body = (
      <div role="alert" className="flex min-h-40 flex-col items-center justify-center gap-3 rounded-xl border border-border bg-card py-10 text-center">
        <span className="grid size-10 place-items-center rounded-full bg-fail-soft text-fail"><TriangleAlert className="size-5" aria-hidden="true" /></span>
        <p className="text-sm font-medium">조회 실패{httpLabels[error.status] ? `: ${httpLabels[error.status]}` : ""}</p>
        <p className="font-mono text-xs text-muted-foreground">{detail}</p>
        <Button size="sm" variant="outline" iconLeft={<RefreshCw />} onClick={() => void query.refetch()}>다시 시도</Button>
      </div>
    )
  } else if (isPending || data === undefined) {
    body = skeleton === "table" || skeleton === "tiles" || skeleton === "block" ? <Skeleton kind={skeleton as "table" | "tiles" | "block"} /> : skeleton
  } else if (isEmpty?.(data)) {
    body = empty ?? <EmptyState title="항목 없음" />
  } else {
    body = children(data)
  }
  return <div className={cn("min-w-0", className)} aria-busy={isPending || undefined}>{body}</div>
}

/** Placeholder for screens whose content is not built yet. */
export function NotReady() {
  return <EmptyState icon={ServerCog} title="준비 중" />
}

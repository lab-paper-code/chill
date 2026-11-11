"use client"

import * as React from "react"
import { useSession } from "@/lib/client"
import { ApiError, errorDetails, isCoverageWarming } from "@/lib/client-transport"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { safeSource } from "@/lib/source-export"
import { stringifySchedulerJSON } from "@/lib/scheduler"
import { explain, statusLabel } from "@/lib/labels"
import { Braces, CircleCheck, CircleDashed, CircleX, Download, Info, RefreshCw, TriangleAlert } from "lucide-react"

export type Tone = "ok" | "warn" | "fail" | "info" | "idle"
const tones: Record<string, Tone> = {
  Ready: "ok", Applied: "ok", Completed: "ok", True: "ok", completed: "ok",
  Waiting: "warn", Degraded: "warn", MissingEvidence: "warn", MissingDataset: "warn", MissingRuntime: "warn", NativeNotReady: "warn", Draining: "warn", False: "warn", deferred: "warn", timeout: "warn", incomplete: "warn", Preparing: "warn",
  Failed: "fail", error: "fail", rejected: "fail", delivery_failed: "fail",
  Accepted: "info", Applying: "info", Changing: "info", Deleting: "info", Registration: "info", Update: "info", Delete: "info", ModelUpdate: "info", PreprocessorUpdate: "info", pending: "info", assigned: "info", queued: "info", running: "info", returning: "info",
}
const toneIcons = {ok: CircleCheck, warn: TriangleAlert, fail: CircleX, info: Info, idle: CircleDashed}
export const toneOf = (value?: string): Tone => value ? tones[value] ?? "idle" : "idle"
// Tone is never the only signal: every badge carries an icon and a text label.
export function StatusBadge({ value, tone, children }: { value?: string; tone?: Tone; children?: React.ReactNode }) {
  const resolved = tone ?? toneOf(value)
  const Icon = toneIcons[resolved]
  return <span className="badge" data-tone={resolved}><Icon aria-hidden="true" />{children ?? statusLabel(value)}</span>
}

const errorLabels: Record<number, string> = {
  401: "접속 정보를 다시 확인해 주세요.", 403: "이 정보를 볼 권한이 없어요.",
  404: "항목을 찾을 수 없어요.", 409: "다른 변경이 있어 다시 확인해야 해요.",
  429: "요청이 많아요. 잠시 후 다시 시도해 주세요.",
  502: "연결하지 못했어요.", 503: "지금은 정보를 확인할 수 없어요.", 504: "응답이 늦어지고 있어요.",
}
export function DetailsDialog({ label, title, children, description, icon }: {
  label: string; title: string; children: React.ReactNode; description?: string; icon?: React.ReactNode
}) {
  return <Dialog><DialogTrigger asChild><Button type="button" variant="ghost" size="sm" className="detail-trigger self-start justify-self-start">{icon}{label}</Button></DialogTrigger>
    <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-xl">
      <DialogHeader><DialogTitle>{title}</DialogTitle>{description ? <DialogDescription>{description}</DialogDescription> : <DialogDescription className="sr-only">{title}의 상세 정보</DialogDescription>}</DialogHeader>
      <div className="min-w-0 space-y-4">{children}</div>
    </DialogContent>
  </Dialog>
}
type GateQuery = {data?:unknown;error:ApiError|null;errorContext?:string;failureReason?:unknown;isPending:boolean;isFetching:boolean;dataUpdatedAt:number;refetch:()=>unknown}
function QueryState({query,children,inlineSource=false}:{query:GateQuery;children:React.ReactNode;inlineSource?:boolean}) {
  const {token} = useSession()
  const denied = query.error?.status === 401 || query.error?.status === 403
  const code = query.error?.data && typeof query.error.data === "object" && "code" in query.error.data && typeof query.error.data.code === "string" ? query.error.data.code : undefined
  const decodeError=code==="DashboardPrecisionUnsupported"||code==="MeasurementSummaryInvalid"
  const warming=isCoverageWarming(query.error??query.failureReason)
  const detail={query:query.errorContext,...errorDetails(query.error)}
  // Body codes read as a sentence and next action; status-only errors keep the status sentence. The code stays in 문제 자세히 보기.
  const [sentence, next] = code ? explain(code) : [errorLabels[query.error?.status ?? 0] ?? "정보를 불러오지 못했어요."]
  return <>
    {warming?query.error?<p role="status">초기화 중</p>:null:!token ? <p>먼저 접속해 주세요.</p> : query.isPending && !query.error ? <div className="skeleton" role="status"><span className="sr-only">불러오고 있어요.</span><span /><span /><span /></div> : null}
    {warming&&query.error?<Button type="button" variant="outline" size="sm" disabled={query.isFetching} onClick={()=>void query.refetch()}>다시 조회</Button>:query.error&&!warming&&<div role="alert" className="notice"><p className="flex items-start gap-2 font-medium"><TriangleAlert className="mt-0.5 size-4 shrink-0 text-fail" aria-hidden="true" />{query.error.unknownOutcome ? "결과를 확인하지 못했어요. 다시 보내기 전에 상태를 확인해 주세요." : decodeError ? query.error.body : sentence}</p>{!query.error.unknownOutcome && !decodeError && next && <p>{next}</p>}<div className="flex flex-wrap gap-2"><Button type="button" variant="outline" size="sm" onClick={() => void query.refetch()} disabled={!token || query.isFetching}><RefreshCw className={query.isFetching ? "animate-spin" : undefined} aria-hidden="true" />다시 불러오기</Button><SourceJSON inline={inlineSource} title="문제 자세히 보기" value={detail} /></div>{query.data !== undefined && !denied && <p>아래는 이전에 확인한 정보예요.</p>}</div>}
    {token && !denied && query.data !== undefined && children}
  </>
}
// One screen, one query state: loading, error and denial without panel chrome.
export function QueryGate({query,children}:{query:GateQuery;children?:React.ReactNode}) {
  return <div className="query-gate"><QueryState query={query}>{children}</QueryState></div>
}
export function SourceJSON({value,title="원본 자료 보기",inline=false}:{value:unknown;title?:string;inline?:boolean}) {
  const body=<pre className="source-json">{stringifySchedulerJSON(safeSource(value),2)}</pre>
  return inline?<details><summary>{title}</summary>{body}</details>:<DetailsDialog label={title} title={title} icon={<Braces aria-hidden="true" />}>{body}</DetailsDialog>
}
export function SourceExport({value,filename}:{value:unknown;filename:string}) {
  return <Button variant="outline" size="sm" className="self-start" onClick={() => {
    const url = URL.createObjectURL(new Blob([stringifySchedulerJSON(safeSource(value),2)],{type:"application/json"}))
    const link = document.createElement("a"); link.href=url; link.download=filename.replace(/[^a-zA-Z0-9._-]/g,"_"); link.click(); URL.revokeObjectURL(url)
  }}><Download aria-hidden="true" />자료 다운로드</Button>
}

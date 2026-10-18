"use client"

import * as React from "react"
import { ApiError, useSession } from "@/lib/client"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { safeSource } from "@/lib/source-export"
import { stringifySchedulerJSON } from "@/lib/scheduler"
import { explain } from "@/lib/labels"
import { Braces, CircleCheck, CircleDashed, CircleX, Download, Info, RefreshCw, TriangleAlert } from "lucide-react"

export type Tone = "ok" | "warn" | "fail" | "info" | "idle"
const tones: Record<string, Tone> = {
  Ready: "ok", Applied: "ok", Completed: "ok", True: "ok", completed: "ok", response_finished: "ok",
  Waiting: "warn", Degraded: "warn", MissingEvidence: "warn", MissingDataset: "warn", MissingRuntime: "warn", NativeNotReady: "warn", Draining: "warn", False: "warn", deferred: "warn", timeout: "warn", incomplete: "warn", Preparing: "warn",
  Failed: "fail", error: "fail", rejected: "fail", delivery_failed: "fail",
  Accepted: "info", Applying: "info", Changing: "info", Deleting: "info", Registration: "info", Update: "info", Delete: "info", ModelUpdate: "info", PreprocessorUpdate: "info", pending: "info", assigned: "info", queued: "info", running: "info", returning: "info", request_received: "info",
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
export function statusLabel(value?: string): string {
  const labels: Record<string, string> = {
    Ready: "사용 가능", Waiting: "준비 중", Preparing: "실행 준비 중", Changing: "변경 중", Deleting: "삭제 중", Degraded: "확인 필요",
    MissingEvidence: "측정 결과가 필요해요", MissingDataset: "데이터셋이 필요해요", MissingRuntime: "실행 환경이 필요해요",
    NativeNotReady: "실행 준비 중", Registration: "등록", Update: "수정", Delete: "삭제", ModelUpdate: "모델 변경", PreprocessorUpdate: "전처리기 변경",
    Accepted: "접수됨", Applying: "적용 중", Applied: "적용됨", Draining: "기존 요청 마무리 중",
    Completed: "완료", Failed: "실패", True: "정상", False: "확인 필요", Unknown: "확인 중",
    completed: "완료", error: "오류", rejected: "거절", timeout: "시간 초과",
    canceled: "취소", incomplete: "처리 중", delivery_failed: "응답 전달 실패",
    pending: "배정 대기", assigned: "배정됨", queued: "실행 대기", running: "실행 중", returning: "응답 보내는 중",
    deferred: "목표 시간을 넘겨 뒤로 미뤄짐",
    request_received: "요청 도착", response_finished: "응답 완료",
    AppliedSnapshotUnavailable: "적용 결과 확인 대기",
    Running: "실행 중", Pending: "대기 중", Succeeded: "완료", Complete: "완료", Planned: "계획됨",
  }
  // Internal codes (CamelCase or "Code: detail") never reach the main screen; the raw value stays in 자세히.
  return !value ? "정보 없음" : labels[value] ?? (/^[A-Z][A-Za-z]+(:|$)/.test(value) ? "확인이 필요해요" : value)
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
type GateQuery = {data?:unknown;error:ApiError|null;isPending:boolean;isFetching:boolean;dataUpdatedAt:number;refetch:()=>unknown}
function QueryState({query,children,inlineSource=false}:{query:GateQuery;children:React.ReactNode;inlineSource?:boolean}) {
  const {token} = useSession()
  const denied = query.error?.status === 401 || query.error?.status === 403
  const code = query.error?.data && typeof query.error.data === "object" && "code" in query.error.data && typeof query.error.data.code === "string" ? query.error.data.code : undefined
  // Body codes read as a sentence and next action; status-only errors keep the status sentence. The code stays in 문제 자세히 보기.
  const [sentence, next] = code ? explain(code) : [errorLabels[query.error?.status ?? 0] ?? "정보를 불러오지 못했어요."]
  return <>
    {!token ? <p>먼저 접속해 주세요.</p> : query.isPending && !query.error ? <div className="skeleton" role="status"><span className="sr-only">불러오고 있어요.</span><span /><span /><span /></div> : null}
    {query.error && <div role="alert" className="notice"><p className="flex items-start gap-2 font-medium"><TriangleAlert className="mt-0.5 size-4 shrink-0 text-fail" aria-hidden="true" />{query.error.unknownOutcome ? "결과를 확인하지 못했어요. 다시 보내기 전에 상태를 확인해 주세요." : code === "DashboardPrecisionUnsupported" ? query.error.body : sentence}</p>{!query.error.unknownOutcome && code !== "DashboardPrecisionUnsupported" && next && <p>{next}</p>}<div className="flex flex-wrap gap-2"><Button type="button" variant="outline" size="sm" onClick={() => void query.refetch()} disabled={!token || query.isFetching}><RefreshCw className={query.isFetching ? "animate-spin" : undefined} aria-hidden="true" />다시 불러오기</Button><SourceJSON inline={inlineSource} title="문제 자세히 보기" value={{status:query.error.status,error:query.error.data ?? query.error.body}} /></div>{query.data !== undefined && !denied && <p>아래는 이전에 확인한 정보예요.</p>}</div>}
    {token && !denied && query.data !== undefined && children}
  </>
}
// One screen, one query state: loading, error and denial without panel chrome.
export function QueryGate({query,children}:{query:GateQuery;children:React.ReactNode}) {
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

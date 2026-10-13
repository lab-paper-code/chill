"use client"

import * as React from "react"
import { ApiError, useSession } from "@/lib/client"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { safeSource } from "@/lib/source-export"
import { stringifySchedulerJSON } from "@/lib/scheduler"

const errorLabels: Record<number, string> = {
  401: "접속 정보를 다시 확인해 주세요.", 403: "이 정보를 볼 권한이 없어요.",
  404: "항목을 찾을 수 없어요.", 409: "다른 변경이 있어 다시 확인해야 해요.",
  429: "요청이 많아요. 잠시 후 다시 시도해 주세요.",
  502: "연결하지 못했어요.", 503: "지금은 정보를 확인할 수 없어요.", 504: "응답이 늦어지고 있어요.",
}
export function statusLabel(value?: string): string {
  const labels: Record<string, string> = {
    Ready: "사용 가능", Waiting: "준비 중", Deleting: "삭제 중", Degraded: "확인 필요",
    MissingEvidence: "측정 결과가 필요해요", MissingRuntime: "실행 환경이 필요해요",
    NativeNotReady: "실행 준비 중", Registration: "등록", Update: "수정", Delete: "삭제",
    Accepted: "접수됨", Applying: "적용 중", Applied: "적용됨", Draining: "기존 요청 마무리 중",
    Completed: "완료", Failed: "실패", True: "정상", False: "확인 필요", Unknown: "확인 중",
    completed: "완료", error: "오류", rejected: "거절", timeout: "시간 초과",
    canceled: "취소", incomplete: "처리 중", delivery_failed: "응답 전달 실패",
    pending: "미배정", assigned: "실행 단계 확인 대기", queued: "실행기 큐 대기", running: "실행 중", returning: "응답 반환 중",
    request_received: "요청 도착", response_finished: "응답 완료",
  }
  return value ? labels[value] ?? value : "정보 없음"
}
export function DetailsDialog({ label, title, children, description }: {
  label: string; title: string; children: React.ReactNode; description?: string
}) {
  return <Dialog><DialogTrigger asChild><Button type="button" variant="ghost" className="detail-trigger self-start">{label}</Button></DialogTrigger>
    <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-xl">
      <DialogHeader><DialogTitle>{title}</DialogTitle>{description ? <DialogDescription>{description}</DialogDescription> : <DialogDescription className="sr-only">{title}의 상세 정보</DialogDescription>}</DialogHeader>
      <div className="min-w-0 space-y-4">{children}</div>
    </DialogContent>
  </Dialog>
}
export function ReadRegion({title,query,children,inlineSource=false}: {title:string;query:{data?:unknown;error:ApiError|null;isPending:boolean;isFetching:boolean;dataUpdatedAt:number;refetch:()=>unknown};children:React.ReactNode;inlineSource?:boolean}) {
  const {token} = useSession()
  const denied = query.error?.status === 401 || query.error?.status === 403
  const code = query.error?.data && typeof query.error.data === "object" && "code" in query.error.data ? query.error.data.code : undefined
  return <section className="read-region" aria-label={title}>
    <div className="read-region-heading"><h2>{title}</h2><div className="toolbar">
      {query.dataUpdatedAt > 0 && <span className="text-xs text-muted-foreground">확인 {new Date(query.dataUpdatedAt).toLocaleTimeString("ko-KR", {timeZone:"UTC",hour:"2-digit",minute:"2-digit"})} UTC</span>}
      <Button type="button" variant="ghost" onClick={() => void query.refetch()} disabled={!token || query.isFetching}>새로고침</Button>
    </div></div>
    {!token ? <p>먼저 접속해 주세요.</p> : query.isPending && !query.error ? <p role="status">불러오고 있어요.</p> : null}
    {query.isFetching && query.data !== undefined && <p role="status" className="text-sm text-muted-foreground">새로운 정보를 확인하고 있어요.</p>}
    {query.error && <div role="alert" className="notice"><p>{query.error.unknownOutcome ? "결과를 확인하지 못했어요. 다시 보내기 전에 상태를 확인해 주세요." : code === "SchedulerUnavailable" ? "운영 판단 API는 배포 설정에서 연결합니다. 현재는 스케줄러 설정이 연결되지 않았습니다." : code === "DashboardPrecisionUnsupported" ? query.error.body : errorLabels[query.error.status] ?? "정보를 불러오지 못했어요."}</p><SourceJSON inline={inlineSource} title="문제 자세히 보기" value={{status:query.error.status,error:query.error.data ?? query.error.body}} />{query.data !== undefined && !denied && <p>아래는 이전에 확인한 정보예요.</p>}</div>}
    {token && !denied && query.data !== undefined && children}
  </section>
}
export function SourceJSON({value,title="원본 자료 보기",inline=false}:{value:unknown;title?:string;inline?:boolean}) {
  const body=<pre className="source-json">{stringifySchedulerJSON(safeSource(value),2)}</pre>
  return inline?<details><summary>{title}</summary>{body}</details>:<DetailsDialog label={title} title={title}>{body}</DetailsDialog>
}
export function SourceExport({value,filename}:{value:unknown;filename:string}) {
  return <Button variant="outline" onClick={() => {
    const url = URL.createObjectURL(new Blob([stringifySchedulerJSON(safeSource(value),2)],{type:"application/json"}))
    const link = document.createElement("a"); link.href=url; link.download=filename.replace(/[^a-zA-Z0-9._-]/g,"_"); link.click(); URL.revokeObjectURL(url)
  }}>자료 다운로드</Button>
}
export function PendingFeature({title,description}:{title:string;description:string}) {
  return <section className="empty-state"><span className="status-pill">연결 준비 중</span><h2>{title}</h2><p>{description}</p><Button disabled>아직 사용할 수 없어요</Button></section>
}

"use client"

import * as React from "react"
import { useRouter } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { ApiError, useChillMutation, useChillQuery, useSession } from "@/lib/client"
import type { ProfileSelection, ProfileSelectionsView, SchedulerView } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, SourceJSON, StatusBadge, type Tone } from "@/components/read-region"
import { Empty, Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"

const profilesPath = "v1/scheduler/profiles"

// HTTP 200 alone is not adoption: only Persisted counts, and loaded is a separate later step.
export function selectionOutcome(selection:ProfileSelection):{tone:Tone;badge:string;sentence:string} {
  if (selection.status === "Rejected") return {tone:"fail", badge:"채택 안 됨", sentence:"이 측정 결과는 채택되지 않았어요."}
  if (selection.status === "Pending") return {tone:"warn", badge:"적용 대기", sentence:"채택 요청이 아직 끝나지 않았어요."}
  if (selection.status === "Persisted" && selection.loaded) return {tone:"ok", badge:"운영 판단에 반영됨", sentence:"운영 판단이 이 프로파일을 불러왔어요."}
  if (selection.status === "Persisted") return {tone:"warn", badge:"저장됨 · 반영 대기", sentence:"설정에는 저장됐지만 운영 판단이 아직 불러오지 않았어요."}
  return {tone:"idle", badge:"확인 필요", sentence:"채택 상태를 확인할 수 없어요."}
}
const loadedNote = "운영 판단에 반영됐다는 건 판단에 쓸 수 있다는 뜻이에요. GPU 서빙 준비나 실제 서빙, 자동 제어가 끝났다는 뜻은 아니에요."
const resultHref = (selection:ProfileSelection) => `/measurements/batch-energy-optimizations/${encodeURIComponent(selection.campaign.namespace)}/${encodeURIComponent(selection.campaign.name)}`
const same = (selection:ProfileSelection, namespace:string, name:string) => selection.campaign.namespace === namespace && selection.campaign.name === name

export function useProfileSelections() {
  return useChillQuery<ProfileSelectionsView>(profilesPath, {decoder:parseSchedulerJSON})
}
function AutoControl() {
  const view = useChillQuery<SchedulerView>("v1/scheduler", {decoder:parseSchedulerJSON})
  const enabled = view.data?.settings.controls.enabled
  return enabled === undefined ? null : enabled ? <StatusBadge tone="info">자동 적용 켜짐</StatusBadge> : <StatusBadge tone="idle">자동 적용 꺼짐</StatusBadge>
}
function SelectionFacts({selection}:{selection:ProfileSelection}) {
  const outcome = selectionOutcome(selection)
  return <>
    <Facts items={[
      ["상태", <StatusBadge key="s" tone={outcome.tone}>{outcome.badge}</StatusBadge>],
      ["설정에 저장", selection.persisted ? "저장됨" : "저장 안 됨"],
      ["운영 판단 반영", selection.loaded ? "불러옴" : "아직 안 불러옴"],
    ]}/>
    {selection.reason && <p className="text-sm text-muted-foreground">{selection.reason}</p>}
    <p className="text-sm text-muted-foreground">{loadedNote}</p>
    <SourceJSON value={selection} title="원본 자료 보기"/>
  </>
}

// /scheduler/profiles: which measurement results the operating judgment uses.
export function SchedulerProfilesPage() {
  const query = useProfileSelections(), router = useRouter(), {config} = useSession()
  const [name, setName] = React.useState(""), [open, setOpen] = React.useState(false)
  const selections = query.data?.selections ?? []
  const loaded = selections.filter(selection => selection.status === "Persisted" && selection.loaded).length
  return <Screen back={["/scheduler","운영"]} status={<AutoControl/>}
    title={!query.data ? "측정 프로파일" : selections.length ? `운영 판단이 측정 프로파일 ${loaded}개를 쓰고 있어요` : "아직 채택한 측정 프로파일이 없어요"}
    lede="측정 결과를 채택하면 운영 판단이 그 결과로 구성을 비교해요. 자동 적용이 꺼져 있으면 구성은 바뀌지 않아요.">
    <QueryGate query={query}>
      {selections.length ? <Rows label="채택한 측정 결과">{selections.map(selection => {
        const outcome = selectionOutcome(selection)
        return <Row key={`${selection.campaign.namespace}/${selection.campaign.name}`} href={resultHref(selection)} label={selection.campaign.name}
          hint={outcome.sentence} value={<StatusBadge tone={outcome.tone}>{outcome.badge}</StatusBadge>}/>
      })}</Rows> : query.data && <Empty title="측정 결과 화면에서 채택할 수 있어요"/>}
      {query.data && <p className="px-1 text-sm text-muted-foreground">{loadedNote}</p>}
    </QueryGate>
    <Rows label="더 하기">
      <RowDialog open={open} onOpenChange={setOpen} label="다른 측정 결과 채택하기" hint="측정 계획 이름으로 결과를 열어요" title="측정 결과 열기" description="결과를 확인한 뒤 그 화면에서 채택해요.">
        <form className="grid gap-4" onSubmit={event => { event.preventDefault(); if (name.trim()) { setOpen(false); router.push(`/measurements/batch-energy-optimizations/${encodeURIComponent(config.namespace)}/${encodeURIComponent(name.trim())}`) } }}>
          <label className="grid gap-1.5">측정 계획 이름<Input value={name} onChange={event => setName(event.target.value)} maxLength={253} required/></label>
          <Button type="submit">결과 열기</Button>
        </form>
      </RowDialog>
      {query.data && <RowDialog label="원본 자료" title="채택 상태 원본"><SourceJSON inline value={query.data}/></RowDialog>}
    </Rows>
  </Screen>
}

// Shown on a measurement plan result: its current adoption and the explicit adopt action.
export function ProfileAdoption({namespace, name, ready}:{namespace:string;name:string;ready:boolean}) {
  const query = useProfileSelections(), client = useQueryClient(), mutation = useChillMutation()
  const [confirm, setConfirm] = React.useState(false), [result, setResult] = React.useState<ProfileSelection|null>(null), [unknown, setUnknown] = React.useState(false)
  const current = query.data?.selections?.find(selection => same(selection, namespace, name))
  const shown = result ?? current
  async function adopt() {
    setResult(null); setUnknown(false)
    try {
      const response = await mutation.mutateAsync({path:profilesPath, method:"POST", body:{campaign:{namespace, name}}})
      setResult(parseSchedulerJSON(response.body) as ProfileSelection)
    } catch (error) {
      if (error instanceof ApiError && (error.unknownOutcome || error.status === 0)) setUnknown(true)
    } finally {
      setConfirm(false)
      await client.invalidateQueries({predicate:q => typeof q.queryKey[1] === "string" && q.queryKey[1].startsWith("v1/scheduler")})
    }
  }
  const error = mutation.error && !unknown ? mutation.error : null
  return <>
    <Rows label="운영 판단 채택">
      {shown ? <RowDialog label={selectionOutcome(shown).badge} hint={selectionOutcome(shown).sentence} title="채택 상태" value={<StatusBadge tone={selectionOutcome(shown).tone}>{shown.persisted ? "채택됨" : "미채택"}</StatusBadge>}>
        <SelectionFacts selection={shown}/>
      </RowDialog> : <Row href="/scheduler/profiles" label="아직 채택하지 않았어요" hint={query.isPending ? "채택 상태를 확인하고 있어요" : "채택한 측정 프로파일 목록 보기"}/>}
      {shown && <Row href="/scheduler/profiles" label="채택한 측정 프로파일 전체"/>}
    </Rows>
    {unknown && <div role="alert" className="notice"><p>채택 결과를 확인하지 못했어요. 다시 보내기 전에 현재 상태를 확인해 주세요.</p><Button variant="outline" onClick={() => { setUnknown(false); void query.refetch() }}>현재 채택 상태 확인하기</Button></div>}
    {error && <div role="alert" className="notice"><p>{error.status === 403 ? "채택하려면 운영 설정 변경 권한이 필요해요." : "채택 요청을 처리하지 못했어요."}</p><SourceJSON value={error.data ?? error.body} title="문제 자세히 보기"/></div>}
    <div className="screen-action"><Button disabled={!ready || mutation.isPending || query.isPending} onClick={() => setConfirm(true)}>{shown?.persisted ? "다시 채택하기" : "운영 판단에 채택하기"}</Button></div>
    <Dialog open={confirm} onOpenChange={setConfirm}><DialogContent>
      <DialogHeader><DialogTitle>{shown?.persisted ? "이 측정 결과를 다시 채택할까요?" : "이 측정 결과를 채택할까요?"}</DialogTitle><DialogDescription>운영 판단이 이 결과를 프로파일로 써서 구성을 비교해요.</DialogDescription></DialogHeader>
      <Facts items={[["측정 계획", name], ["범위", namespace]]}/>
      <p className="text-sm text-muted-foreground">채택은 운영 설정에 저장돼요. 자동 적용이 꺼져 있으면 실제 구성은 바뀌지 않고, GPU 서빙이나 자동 제어가 시작되지도 않아요.</p>
      <Button disabled={mutation.isPending} onClick={() => void adopt()}>{mutation.isPending ? "채택하는 중" : "채택하기"}</Button>
    </DialogContent></Dialog>
  </>
}

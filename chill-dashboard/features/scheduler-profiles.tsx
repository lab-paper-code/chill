"use client"

import * as React from "react"
import { useRouter } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { ApiError, useChillMutation, useChillQuery, useSession } from "@/lib/client"
import type { CurvePlanSpec, ProfileSelection, ProfileSelectionsView, SchedulerProfile, SchedulerView } from "@/lib/read-types"
import { explain } from "@/lib/labels"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, SourceJSON, StatusBadge, type Tone } from "@/components/read-region"
import { Empty, Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"

const profilesPath = "v1/scheduler/profiles"

// HTTP 200 alone is not adoption or removal: Persisted counts as adopted, Removed with loaded=false as fully removed.
export function selectionOutcome(selection:ProfileSelection):{tone:Tone;badge:string;sentence:string} {
  if (selection.status === "Rejected") return {tone:"fail", badge:"채택 안 됨", sentence:"이 측정 결과는 채택되지 않았어요."}
  if (selection.code === "ProfileUnloadFailed" || selection.code === "ProfileUnloadPending" || (selection.status === "Removed" && selection.loaded))
    return {tone:"warn", badge:"해제 반영 대기", sentence:"채택 해제는 저장됐지만 운영 판단이 아직 이 프로파일을 불러온 상태예요."}
  if (selection.status === "Pending") return {tone:"warn", badge:"적용 대기", sentence:"요청이 저장됐지만 운영 판단에 아직 반영되지 않았어요."}
  if (selection.status === "Removed") return {tone:"ok", badge:"채택 해제됨", sentence:"운영 판단이 더 이상 이 프로파일을 쓰지 않아요."}
  if (selection.status === "Unselected") return {tone:"idle", badge:"채택 안 됨", sentence:"채택되지 않은 측정이에요."}
  if (selection.status === "Persisted" && selection.loaded) return {tone:"ok", badge:"운영 판단에 반영됨", sentence:"운영 판단이 이 프로파일을 불러왔어요."}
  if (selection.status === "Persisted") return {tone:"warn", badge:"저장됨 · 반영 대기", sentence:"설정에는 저장됐지만 운영 판단이 아직 불러오지 않았어요."}
  return {tone:"idle", badge:"확인 필요", sentence:"채택 상태를 확인할 수 없어요."}
}
const loadedNote = "운영 판단에 반영됐다는 건 판단에 쓸 수 있다는 뜻이에요. GPU 서빙 준비나 실제 서빙, 자동 제어가 끝났다는 뜻은 아니에요."
const resultHref = (selection:ProfileSelection) => `/measurements/batch-energy-optimizations/${encodeURIComponent(selection.campaign.namespace)}/${encodeURIComponent(selection.campaign.name)}`
const same = (selection:ProfileSelection, namespace:string, name:string) => selection.campaign.namespace === namespace && selection.campaign.name === name
// The four measurement conditions of a profile (CurvePlanSpec), in plain words. Raw values stay in the 원본 view.
export const conditionRows = (condition:CurvePlanSpec):[string,string][] => [
  ["모델 파일", `${condition.logicalModel} / ${condition.artifact.name}`],
  ["실행 방식", [condition.runtimeFamily, condition.backend].map(code => explain(code)[0]).join(" · ")],
  ["전처리기", condition.preprocessor.name || "없음"],
  ["데이터셋", condition.activeProtocol.dataset?.name ?? "없음"],
]
// Loaded profiles reach the client only inside the state: as options (with the services they fit) and as running instances.
function loadedProfiles(view?:ProfileSelectionsView) {
  const snapshot = view?.state.snapshot, names = new Map((snapshot?.workloads ?? []).flatMap(item => item ? [[item.uid, item.name] as const] : []))
  const found = new Map<string,{profile:SchedulerProfile;services:Set<string>}>()
  const add = (profile:SchedulerProfile|null, uids:string[]|null) => {
    if (!profile) return
    const entry = found.get(profile.id) ?? {profile, services:new Set<string>()}
    for (const uid of uids ?? []) entry.services.add(names.get(uid) ?? "이전 등록 서비스")
    found.set(profile.id, entry)
  }
  for (const option of snapshot?.options ?? []) add(option.instance.profile, option.compatibleUIDs)
  for (const instance of snapshot?.instances ?? []) add(instance.profile, instance.workloadUIDs)
  return found
}

export function useProfileSelections() {
  return useChillQuery<ProfileSelectionsView>(profilesPath, {decoder:parseSchedulerJSON})
}
function AutoControl() {
  const view = useChillQuery<SchedulerView>("v1/scheduler", {decoder:parseSchedulerJSON})
  const count = view.data?.settings.autonomousWorkloads?.length
  return count === undefined ? null : count ? <StatusBadge tone="info">자동 제어 서비스 {count}개</StatusBadge> : <StatusBadge tone="idle">자동 제어 서비스 없음</StatusBadge>
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
  const selections = query.data?.selections ?? [], profiles = loadedProfiles(query.data)
  const loaded = selections.filter(selection => selection.status === "Persisted" && selection.loaded).length
  return <Screen back={["/scheduler","운영"]} status={<AutoControl/>}
    title={!query.data ? "측정 프로파일" : selections.length ? `운영 판단이 측정 프로파일 ${loaded}개를 쓰고 있어요` : "아직 채택한 측정 프로파일이 없어요"}
    lede="측정 결과를 채택하면 운영 판단이 그 결과로 구성을 비교해요. 자동 적용이 꺼져 있으면 구성은 바뀌지 않아요.">
    <QueryGate query={query}>
      {selections.length ? <Rows label="채택한 측정 결과">{selections.map(selection => {
        const outcome = selectionOutcome(selection), found = selection.profileID ? profiles.get(selection.profileID) : undefined
        const services = found?.services.size ? [...found.services].join(", ") : "맞는 서비스가 없어요"
        return <RowDialog key={`${selection.campaign.namespace}/${selection.campaign.name}`} label={selection.campaign.name}
          hint={found ? `${services} · ${conditionRows(found.profile.condition).map(([,value]) => value).join(" · ")}` : outcome.sentence} value={<StatusBadge tone={outcome.tone}>{outcome.badge}</StatusBadge>} title={selection.campaign.name}>
          {found ? <Facts label="측정 조건" items={[["서비스", services], ["장비", found.profile.nodeName], ...conditionRows(found.profile.condition)]}/> : <p>운영 판단이 이 결과를 불러오면 측정 조건을 보여줘요.</p>}
          <SelectionFacts selection={selection}/>
          {found && <SourceJSON inline value={found.profile.condition} title="측정 조건 원본 보기"/>}
          <Rows><Row href={resultHref(selection)} label="측정 결과 보기"/></Rows>
        </RowDialog>
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

// Shown on a measurement plan result: its current adoption, the explicit adopt action and unadoption.
// The primary action is rendered only when the result can be adopted; the caller owns it otherwise.
export function ProfileAdoption({namespace, name, ready, children}:{namespace:string;name:string;ready:boolean;children?:React.ReactNode}) {
  const query = useProfileSelections(), client = useQueryClient(), adoptMutation = useChillMutation(), removeMutation = useChillMutation()
  const [dialog, setDialog] = React.useState<"adopt"|"remove"|null>(null), [result, setResult] = React.useState<ProfileSelection|null>(null), [unknown, setUnknown] = React.useState<"adopt"|"remove"|null>(null)
  const current = query.data?.selections?.find(selection => same(selection, namespace, name))
  const shown = result ?? current
  const adopted = !!shown && (shown.persisted || shown.loaded)
  const refresh = () => client.invalidateQueries({predicate:q => typeof q.queryKey[1] === "string" && q.queryKey[1].startsWith("v1/scheduler")})
  async function adopt() {
    setResult(null); setUnknown(null); removeMutation.reset()
    try {
      const response = await adoptMutation.mutateAsync({path:profilesPath, method:"POST", body:{campaign:{namespace, name}}})
      setResult(parseSchedulerJSON(response.body) as ProfileSelection)
    } catch (error) {
      if (error instanceof ApiError && (error.unknownOutcome || error.status === 0)) setUnknown("adopt")
    } finally { setDialog(null); await refresh() }
  }
  async function remove() {
    setResult(null); setUnknown(null); adoptMutation.reset()
    try {
      const response = await removeMutation.mutateAsync({path:`${profilesPath}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`, method:"DELETE", version:query.data?.configVersion})
      setResult(parseSchedulerJSON(response.body) as ProfileSelection)
    } catch (error) {
      if (error instanceof ApiError && (error.unknownOutcome || error.status === 0)) setUnknown("remove")
    } finally { setDialog(null); await refresh() }
  }
  const adoptError = adoptMutation.error && unknown !== "adopt" ? adoptMutation.error : null
  const removeError = removeMutation.error && unknown !== "remove" ? removeMutation.error : null
  const removeCode = removeError?.data && typeof removeError.data === "object" && "code" in removeError.data ? String(removeError.data.code) : undefined
  const removeReason = removeError?.data && typeof removeError.data === "object" && "reason" in removeError.data ? String(removeError.data.reason) : undefined
  return <>
    <Rows label="운영 판단 채택">
      {shown ? <RowDialog label={selectionOutcome(shown).badge} hint={selectionOutcome(shown).sentence} title="채택 상태" value={<StatusBadge tone={selectionOutcome(shown).tone}>{shown.persisted ? "채택됨" : shown.loaded ? "해제 대기" : "미채택"}</StatusBadge>}>
        <SelectionFacts selection={shown}/>
      </RowDialog> : <Row href="/scheduler/profiles" label="아직 채택하지 않았어요" hint={query.isPending ? "채택 상태를 확인하고 있어요" : "채택한 측정 프로파일 목록 보기"}/>}
      {adopted && <Row onClick={() => setDialog("remove")} label="채택 해제" hint="운영 판단이 이 프로파일을 더 이상 쓰지 않게 해요"/>}
      {shown && <Row href="/scheduler/profiles" label="채택한 측정 프로파일 전체"/>}
    </Rows>
    {unknown && <div role="alert" className="notice"><p>{unknown === "adopt" ? "채택" : "채택 해제"} 결과를 확인하지 못했어요. 다시 보내기 전에 현재 상태를 확인해 주세요.</p><Button variant="outline" onClick={() => { setUnknown(null); void query.refetch() }}>현재 채택 상태 확인하기</Button></div>}
    {adoptError && <div role="alert" className="notice"><p>{adoptError.status === 403 ? "채택하려면 운영 설정 변경 권한이 필요해요." : "채택 요청을 처리하지 못했어요."}</p><SourceJSON value={adoptError.data ?? adoptError.body} title="문제 자세히 보기"/></div>}
    {removeError && <div role="alert" className="notice"><p>{removeError.status === 403 ? "채택을 해제하려면 운영 설정 변경 권한이 필요해요."
      : removeCode === "ProfileInUse" ? "지금 실행 중인 서비스나 진행 중인 변경이 이 프로파일을 쓰고 있어서 해제할 수 없어요."
      : removeCode === "ProfileControlsEnabled" ? "자동 제어가 켜져 있어서 해제할 수 없어요. 운영 설정에서 자동 제어를 끈 뒤 다시 시도해 주세요."
      : removeError.status === 409 ? "그 사이 채택 목록이 바뀌었어요. 최신 상태를 확인한 뒤 다시 시도해 주세요."
      : "채택 해제 요청을 처리하지 못했어요."}</p>{removeReason && <p className="text-sm">{removeReason}</p>}
      {removeError.status === 409 && !removeCode?.startsWith("Profile") && <Button variant="outline" onClick={() => { removeMutation.reset(); void query.refetch() }}>최신 상태 확인하기</Button>}
      <SourceJSON value={removeError.data ?? removeError.body} title="문제 자세히 보기"/></div>}
    {children}
    {ready && <div className="screen-action"><Button disabled={adoptMutation.isPending || removeMutation.isPending || query.isPending} onClick={() => setDialog("adopt")}>{adopted ? "다시 채택하기" : "운영 판단에 채택하기"}</Button></div>}
    <Dialog open={dialog === "adopt"} onOpenChange={open => setDialog(open ? "adopt" : null)}><DialogContent>
      <DialogHeader><DialogTitle>{adopted ? "이 측정 결과를 다시 채택할까요?" : "이 측정 결과를 채택할까요?"}</DialogTitle><DialogDescription>운영 판단이 이 결과를 프로파일로 써서 구성을 비교해요.</DialogDescription></DialogHeader>
      <Facts items={[["측정", name], ["범위", namespace]]}/>
      <p className="text-sm text-muted-foreground">채택은 운영 설정에 저장돼요. 자동 적용이 꺼져 있으면 실제 구성은 바뀌지 않고, GPU 서빙이나 자동 제어가 시작되지도 않아요.</p>
      <Button disabled={adoptMutation.isPending} onClick={() => void adopt()}>{adoptMutation.isPending ? "채택하는 중" : "채택하기"}</Button>
    </DialogContent></Dialog>
    <Dialog open={dialog === "remove"} onOpenChange={open => setDialog(open ? "remove" : null)}><DialogContent>
      <DialogHeader><DialogTitle>채택을 해제할까요?</DialogTitle><DialogDescription>운영 판단이 이 프로파일을 더 이상 쓰지 않아요.</DialogDescription></DialogHeader>
      <Facts items={[["측정", name], ["범위", namespace]]}/>
      <p className="text-sm text-muted-foreground">측정 결과와 근거 파일은 지워지지 않아요. 실행 중인 서비스나 진행 중인 변경이 이 프로파일을 쓰고 있거나 자동 제어가 켜져 있으면 해제되지 않아요.</p>
      <Button variant="destructive" disabled={removeMutation.isPending || !query.data} onClick={() => void remove()}>{removeMutation.isPending ? "해제하는 중" : "채택 해제하기"}</Button>
    </DialogContent></Dialog>
  </>
}

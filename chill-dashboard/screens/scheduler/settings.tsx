"use client"

import * as React from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { sectionTabs } from "@/lib/routes"
import { cn } from "@/lib/utils"
import { useChillMutation, useChillQuery } from "@/lib/client"
import { ApiError } from "@/lib/client-transport"
import type { WorkloadSummary } from "@/lib/domain"
import type { PoolsView, SchedulerSettings } from "@/lib/read-types"
import { parseSchedulerJSON, settingsEqual, stringifySchedulerJSON } from "@/lib/scheduler"
import { writableSettingKeys } from "@/lib/scheduler-settings"
import { changeRows, fieldMeta, formOf, rebase, settingsGroups, settingsOf, type FieldKey, type SettingsForm, type SettingsGroup } from "@/lib/scheduler-settings-form"
import { Button, ConfirmDialog, Fields, FormActions, LeaveGuard, Page, PageHeader, PageTabs, QueryBoundary, RawDialog, Section, StatusBadge, useToast } from "@/components/kit"
import { SettingsGroupView, type Service } from "./settings-fields"
import { SettingsHistory } from "./settings-history"

type Read = { settings: SchedulerSettings; etag: string | null }

export function SchedulerSettingsScreen() {
  const search = useSearchParams(), router = useRouter(), pathname = usePathname()
  const group = settingsGroups.find(item => item.value === search.get("group"))?.value ?? "targets"
  const setParam = (key: string, value: string | null) => {
    const next = new URLSearchParams(search.toString())
    if (value === null) next.delete(key); else next.set(key, value)
    const query = next.toString()
    router.replace(`${pathname}${query ? `?${query}` : ""}`, { scroll: false })
  }

  const etag = React.useRef<string | null>(null)
  const settings = useChillQuery<SchedulerSettings>("v1/scheduler/settings", { decoder: parseSchedulerJSON, poll: true, onResponse: response => { etag.current = response.etag ?? null } })
  const workloads = useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary")
  const pools = useChillQuery<PoolsView>("v1/pools")
  const services = React.useMemo<Service[] | string>(() => workloads.data ? workloads.data.map(item => ({ uid: item.desired.metadata.uid, name: item.desired.metadata.name })).sort((a, b) => a.name.localeCompare(b.name))
    : workloads.error ? "조회 실패" : "조회 중", [workloads.data, workloads.error])
  const nodes = React.useMemo<string[] | string>(() => {
    const view = pools.data
    if (!view) return pools.error ? "조회 실패" : "조회 중"
    return [...(view.workloads ?? []).flatMap(pool => (pool.nodes ?? []).map(node => node.name)), ...(view.measurement ?? []).map(item => item.node.name),
      ...(view.available ?? []).map(item => item.node.name), ...(view.other ?? []).map(item => item.node.name)]
  }, [pools.data, pools.error])
  const name = React.useCallback((uid: string) => (typeof services === "string" ? undefined : services.find(service => service.uid === uid)?.name) ?? `서비스 ${uid.slice(0, 8)}`, [services])

  // The first edit pins the read (settings + ETag) the change is based on.
  const [draft, setDraft] = React.useState<{ base: Read; form: SettingsForm } | null>(null)
  const [review, setReview] = React.useState(false)
  const [conflict, setConflict] = React.useState<{ base: Read; latest: Read; mine: SchedulerSettings } | null>(null)
  const [unknown, setUnknown] = React.useState<SchedulerSettings | null>(null)
  const [raw, setRaw] = React.useState(false)
  const mutation = useChillMutation(), client = useQueryClient(), { toast } = useToast()

  const result = draft ? settingsOf(draft.base.settings, draft.form) : null
  const rows = draft && result ? changeRows(draft.base.settings, result.settings, name) : []
  const errorCount = result ? Object.keys(result.errors).length : 0
  const changed = new Set(rows.map(row => row.id))

  function update(change: (form: SettingsForm) => SettingsForm) {
    const base = draft?.base ?? (settings.data ? { settings: settings.data, etag: etag.current } : null)
    if (!base) return
    const form = change(draft?.form ?? formOf(base.settings))
    const next = settingsOf(base.settings, form)
    setDraft(Object.keys(next.errors).length || changeRows(base.settings, next.settings, name).length ? { base, form } : null)
  }
  function discard() { setDraft(null); setUnknown(null); mutation.reset() }

  async function reread(): Promise<Read | null> {
    const latest = await settings.refetch()
    if (!latest.data) { toast({ title: "설정 조회 실패", type: "error" }); return null }
    return { settings: latest.data, etag: etag.current }
  }

  async function apply(base: Read, next: SchedulerSettings) {
    const body = Object.fromEntries(writableSettingKeys.filter(key => Object.hasOwn(next, key)).map(key => [key, next[key]]))
    try {
      await mutation.mutateAsync({ path: "v1/scheduler/settings", method: "PUT", body: stringifySchedulerJSON(body), ...(base.etag ? { version: base.etag } : {}) })
      setReview(false); setConflict(null); setDraft(null)
      toast({ title: "설정 적용됨", description: `변경 ${changeRows(base.settings, next, name).length}건`, type: "success" })
    } catch (error) {
      setReview(false); setConflict(null)
      const status = error instanceof ApiError ? error.status : 0
      if (!(error instanceof ApiError) || error.unknownOutcome) {
        setUnknown(next)
        toast({ title: "결과 미확인", description: "현재 값 확인 필요", type: "warning" })
      } else if (status === 409 || status === 428) {
        const latest = await reread()
        if (!latest) return
        const mine = rebase(latest.settings, base.settings, next)
        if (status === 428) {
          setDraft({ base: latest, form: formOf(mine) })
          toast({ title: "버전 없음", description: "최신 값으로 다시 검토", type: "warning" })
        } else if (changeRows(latest.settings, mine, name).length === 0) {
          setDraft(null)
          toast({ title: "이미 같은 값", type: "info" })
        } else setConflict({ base, latest, mine })
      } else {
        const data = error.data as { field?: unknown } | null
        const field = typeof data?.field === "string" ? fieldMeta[data.field as FieldKey]?.label : undefined
        toast({ title: status === 403 ? "변경 권한 없음" : "적용 실패", description: field ?? `HTTP ${status}`, type: "error" })
      }
    } finally {
      void client.invalidateQueries({ predicate: query => typeof query.queryKey[1] === "string" && /^v1\/(scheduler|observations\/scheduler)/.test(query.queryKey[1]) })
    }
  }

  async function confirmOutcome() {
    const latest = await reread()
    if (!latest || !unknown) return
    if (settingsEqual(latest.settings, unknown)) { discard(); toast({ title: "적용 확인됨", type: "success" }); return }
    setUnknown(null); mutation.reset()
    toast({ title: "적용 안 됨", description: "변경 유지", type: "info" })
  }

  const groupCount = (value: SettingsGroup) => rows.filter(row => row.group === value).length
  const history = search.get("history")

  return (
    <Page>
      <PageHeader title="스케줄러"
        actions={<Button variant="outline" onClick={() => setParam("history", "")}>변경 이력</Button>}
        more={[{ label: "원본 보기", onSelect: () => setRaw(true), disabled: !settings.data }]} />
      <PageTabs tabs={sectionTabs.scheduler} />
      <QueryBoundary query={settings} skeleton="block">
        {current => {
          const form = draft?.form ?? formOf(current)
          return (
            <div className="grid gap-6 lg:grid-cols-[11rem_minmax(0,1fr)]">
              <nav aria-label="설정 묶음" className="min-w-0 lg:sticky lg:top-4 lg:self-start">
                <ul className="-mx-1 flex gap-1 overflow-x-auto px-1 [scrollbar-width:none] lg:mx-0 lg:flex-col lg:px-0">
                  {settingsGroups.map(item => {
                    const active = item.value === group, count = groupCount(item.value)
                    return (
                      <li key={item.value}>
                        <button type="button" aria-current={active || undefined} onClick={() => setParam("group", item.value === "targets" ? null : item.value)}
                          className={cn("flex h-9 w-full items-center justify-between gap-2 whitespace-nowrap rounded-md px-3 text-sm transition-colors",
                            active ? "bg-muted font-medium text-foreground" : "text-muted-foreground hover:bg-muted/60 hover:text-foreground")}>
                          {item.label}
                          {count > 0 && <span className="rounded bg-primary-soft px-1.5 text-xs tabular-nums text-primary" aria-label={`변경 ${count}건`}>{count}</span>}
                        </button>
                      </li>
                    )
                  })}
                </ul>
              </nav>
              <SettingsGroupView group={group} form={form} errors={result?.errors ?? {}} changed={changed} update={update} services={services} nodes={nodes} />
            </div>
          )
        }}
      </QueryBoundary>

      {(draft || unknown) && (
        <FormActions changes={rows.length}>
          {unknown ? <StatusBadge tone="warn" label="결과 미확인" /> : errorCount > 0 && <StatusBadge tone="fail" label={`입력 오류 ${errorCount}건`} />}
          <Button variant="outline" onClick={discard} disabled={mutation.isPending}>되돌리기</Button>
          {unknown
            ? <Button key="check" variant="default" onClick={() => void confirmOutcome()}>현재 값 확인</Button>
            : <Button key="review" variant="default" disabled={errorCount > 0 || rows.length === 0} onClick={() => setReview(true)}>검토 후 적용</Button>}
        </FormActions>
      )}

      <ConfirmDialog open={review} onOpenChange={setReview} title={`설정 변경 ${rows.length}건 적용`} target="스케줄러 설정" confirmLabel="적용"
        changes={rows.map(row => ({ label: row.label, before: row.before, after: row.after }))} pending={mutation.isPending}
        onConfirm={() => { if (draft && result) void apply(draft.base, result.settings) }} />
      <ConfirmDialog open={!!conflict} onOpenChange={open => { if (!open) setConflict(null) }} title="다른 변경이 먼저 적용됨" confirmLabel="최신 값에 내 변경 적용"
        changes={conflict ? changeRows(conflict.latest.settings, conflict.mine, name).map(row => ({ label: row.label, before: row.before, after: row.after })) : []} pending={mutation.isPending}
        onConfirm={() => { if (!conflict) return; setDraft({ base: conflict.latest, form: formOf(conflict.mine) }); void apply(conflict.latest, conflict.mine) }}>
        {conflict && <Section title="먼저 적용된 변경">
          <Fields columns={1} items={changeRows(conflict.base.settings, conflict.latest.settings, name).map(row => ({ label: row.label, value: `${row.before} → ${row.after}` }))} />
        </Section>}
      </ConfirmDialog>
      <RawDialog open={raw} onOpenChange={setRaw} title="스케줄러 설정 원본" value={settings.data} filename="scheduler-settings" />
      <SettingsHistory open={history !== null} selected={history ?? ""} onSelect={at => setParam("history", at)} onClose={() => setParam("history", null)} name={name} />
      <LeaveGuard when={!!draft} />
    </Page>
  )
}

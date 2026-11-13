"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { Button, ConfirmDialog, EmptyState, Hint, Page, PageHeader, PageTabs, QueryBoundary, RawDialog, StatusBadge, buttonClassName, useToast } from "@/components/kit"
import { Alert } from "@/components/custom/blocks/Alerts"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { useChillMutation, useChillQuery } from "@/lib/client"
import type { WorkloadView } from "@/lib/domain"
import type { SchedulerSettings } from "@/lib/read-types"
import { parseSchedulerJSON, stringifySchedulerJSON } from "@/lib/scheduler"
import { writableSettingKeys } from "@/lib/scheduler-settings"
import { admissionStatus, autonomyEnabled, serviceReadiness } from "@/lib/status-labels"
import { ServiceDefinitionTab } from "./detail-definition"
import { ServiceRequestsTab } from "./detail-requests"
import { ServiceStatusTab } from "./detail-status"
import { ServiceTestTab } from "./detail-test"
import { failureToast } from "./routing-dialog"

const tabs = [{ value: "overview", label: "현황" }, { value: "requests", label: "요청" }, { value: "definition", label: "정의" }, { value: "test", label: "테스트" }]
const archivedTabs = tabs.slice(1, 3)
const touchesScheduler = (query: { queryKey: readonly unknown[] }) => typeof query.queryKey[1] === "string" && query.queryKey[1].startsWith("v1/scheduler")

export function ServiceDetailScreen({ name }: { name: string }) {
  const params = useSearchParams()
  const urlUID = params.get("uid") ?? undefined
  const tab = params.get("tab") ?? "overview"
  const router = useRouter()
  const client = useQueryClient()
  const { toast } = useToast()
  const path = `v1/workloads/${encodeURIComponent(name)}`
  const detail = useChillQuery<WorkloadView>(path, { poll: (_data, error) => error?.status !== 404 })
  const workload = detail.data
  const gone = detail.error?.status === 404
  const current = workload?.desired.metadata.uid
  const wrongUID = !!urlUID && !!current && urlUID !== current

  // The ETag of the last settings read through this observer; PUT sends it as If-Match.
  const etag = React.useRef<string | null>(null)
  const settings = useChillQuery<SchedulerSettings>("v1/scheduler/settings", { decoder: parseSchedulerJSON, onResponse: response => { etag.current = response.etag ?? null } })
  const mutation = useChillMutation()
  const [autonomy, setAutonomy] = React.useState<{ next: boolean; base: SchedulerSettings; version: string } | null>(null)
  const [deleting, setDeleting] = React.useState(false)
  const [raw, setRaw] = React.useState(false)

  if (gone && urlUID) {
    const props = { name, uid: urlUID, workload: undefined }
    return (
      <Page>
        <PageHeader title={name} status={{ label: "삭제됨", tone: "idle" }} />
        <PageTabs param="tab" defaultValue="requests" tabs={archivedTabs} />
        {tab === "definition" ? <ServiceDefinitionTab {...props} /> : <ServiceRequestsTab {...props} />}
      </Page>
    )
  }
  if (gone) {
    return (
      <Page>
        <PageHeader title={name} />
        <EmptyState title="서비스 없음" action={<Link href="/services" className={buttonClassName({ variant: "outline", size: "sm" })}>서비스 목록</Link>} />
      </Page>
    )
  }

  const autonomous = !!current && !!settings.data?.autonomousWorkloads?.includes(current)
  const settingsDenied = !!settings.error && [401, 403].includes(settings.error.status)
  const switchHint = settingsDenied ? "권한 없음" : settings.error && !settings.data ? "설정 조회 실패" : wrongUID ? "다른 등록" : undefined
  const reason = workload && workload.reason !== workload.readiness ? serviceReadiness(workload.reason) : undefined

  async function askAutonomy(next: boolean) {
    const fresh = await settings.refetch()
    if (!fresh.data || !etag.current) { toast({ title: "설정 조회 실패", type: "error" }); return }
    setAutonomy({ next, base: fresh.data, version: etag.current })
  }
  function applyAutonomy() {
    if (!autonomy || !current) return
    const { next, base, version } = autonomy
    const list = (base.autonomousWorkloads ?? []).filter(item => item !== current)
    const after = next ? [...list, current] : list
    const body = Object.fromEntries(writableSettingKeys.filter(key => key === "autonomousWorkloads" || Object.hasOwn(base, key)).map(key => [key, key === "autonomousWorkloads" ? after : base[key]]))
    mutation.mutate({ path: "v1/scheduler/settings", method: "PUT", body: stringifySchedulerJSON(body), version }, {
      onSuccess: () => toast({ title: next ? "자동 제어 켜짐" : "자동 제어 꺼짐", type: "success" }),
      onError: error => toast(failureToast(error)),
      onSettled: () => { setAutonomy(null); void client.invalidateQueries({ predicate: touchesScheduler }) },
    })
  }
  function remove() {
    if (!workload) return
    mutation.mutate({ path, method: "DELETE", version: workload.desired.metadata.resourceVersion }, {
      onSuccess: () => {
        setDeleting(false)
        toast({ title: "삭제 요청됨", type: "success" })
        void client.invalidateQueries({ queryKey: ["chill", "v1/workloads?view=summary"] })
        router.push("/services")
      },
      onError: error => {
        setDeleting(false)
        toast(failureToast(error))
        if (error.status === 409) void detail.refetch()
      },
    })
  }

  const editable = !!current && !wrongUID
  return (
    <Page>
      <PageHeader
        title={name}
        badges={workload && <>
          <StatusBadge status={serviceReadiness(workload.readiness)} reason={reason && (reason.code ? `코드 ${reason.code}` : reason.label)} />
          <StatusBadge status={admissionStatus(workload.admission)} />
        </>}
        actions={<>
          <Hint text={switchHint}>
            <Label className="h-9 px-1">
              <Switch checked={autonomous} disabled={!editable || !settings.data || mutation.isPending} onCheckedChange={next => void askAutonomy(next)} />
              자동 제어
            </Label>
          </Hint>
          {editable
            ? <Link href={`/services/${encodeURIComponent(name)}/edit?uid=${encodeURIComponent(current)}`} className={buttonClassName({ variant: "default" })}>수정</Link>
            : <Button disabled>수정</Button>}
        </>}
        more={[
          { label: "원본 보기", disabled: !workload, onSelect: () => setRaw(true) },
          { label: "삭제", destructive: true, disabled: !editable || mutation.isPending, onSelect: () => setDeleting(true) },
        ]}
      />
      {wrongUID && (
        <Alert type="warning" icon="AlertTriangle" title="주소와 다른 등록">
          <Link href={`/services/${encodeURIComponent(name)}?uid=${encodeURIComponent(current)}`} className={buttonClassName({ variant: "link", size: "sm", className: "h-auto px-0" })}>현재 등록 보기</Link>
        </Alert>
      )}
      <PageTabs param="tab" defaultValue="overview" tabs={tabs} />
      {/* 현황 reads the live state, which is usually connected already; it does not wait for the definition read. */}
      {tab === "overview" || !["requests", "definition", "test"].includes(tab) ? <ServiceStatusTab name={name} uid={urlUID ?? current} workload={workload} /> : (
        <QueryBoundary query={detail} skeleton="tiles">
          {view => {
            const props = { name, uid: urlUID ?? view.desired.metadata.uid, workload: view }
            return tab === "requests" ? <ServiceRequestsTab {...props} />
              : tab === "definition" ? <ServiceDefinitionTab {...props} />
              : <ServiceTestTab {...props} />
          }}
        </QueryBoundary>
      )}

      <ConfirmDialog
        open={!!autonomy} onOpenChange={open => { if (!open) setAutonomy(null) }}
        title={autonomy?.next ? "자동 제어 켜기" : "자동 제어 끄기"} target={name} confirmLabel="적용" pending={mutation.isPending}
        changes={autonomy ? [{ label: "자동 제어", before: autonomyEnabled(autonomy.base.autonomousWorkloads?.includes(current ?? "") ?? false).label, after: autonomyEnabled(autonomy.next).label }] : []}
        onConfirm={applyAutonomy}
      />
      <ConfirmDialog open={deleting} onOpenChange={setDeleting} title="서비스 삭제" target={name} tone="destructive" confirmLabel="삭제" pending={mutation.isPending} onConfirm={remove}>
        새 요청 접수를 멈추고 배치를 정리함. 요청과 정의 기록은 남음.
      </ConfirmDialog>
      <RawDialog open={raw} onOpenChange={setRaw} value={workload} filename={`workload-${name}`} />
    </Page>
  )
}

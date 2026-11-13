"use client"

import * as React from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import {
  Button, ConfirmDialog, DataTable, EmptyState, Fields, Page, PageHeader, PageTabs, QueryBoundary, RawDialog, Section,
  StatRow, StatTile, StatusBadge, StatusDot, TimeRange, buttonClassName, useTimeRangeParam, useToast, type Column, type TimeRangeValue,
} from "@/components/kit"
import { useChillMutation } from "@/lib/client"
import { actuatorText, deviceRole, energyParts, quantityBytes, readiness } from "@/lib/devices-view"
import { formatNumber, formatPercent } from "@/lib/format"
import { routingShare } from "@/lib/live-display"
import type { PowerPoint } from "@/lib/power-series"
import type { InventoryClass, SchedulerSettings, WorkloadRef } from "@/lib/read-types"
import type { LiveState } from "@/lib/live"
import { deviceInstances, type DeviceRow } from "@/lib/resources-data"
import { stringifySchedulerJSON } from "@/lib/scheduler"
import { writableSettingKeys } from "@/lib/scheduler-settings"
import type { StatusInfo } from "@/lib/status-labels"
import { PowerChart, useDevices, usePower } from "./data"

const HOUR = 3_600_000
const RECENT: TimeRangeValue = { preset: "15m" }
const nodeSeries = [{ key: "w", label: "전력", color: 1 as const }]
const tabs = [{ value: "overview", label: "현황" }, { value: "power", label: "전력" }, { value: "spec", label: "사양" }]

export function DeviceDetailScreen({ name }: { name: string }) {
  const { state, inventory, rows, excluded, settings, etag } = useDevices()
  const tab = useSearchParams().get("tab") ?? "overview"
  const [raw, setRaw] = React.useState(false)
  const row = rows.find(item => item.name === name)

  if (!row) {
    return (
      <Page>
        <PageHeader title={name} />
        <QueryBoundary query={inventory} isEmpty={() => true} empty={<EmptyState title="장치 없음" action={<Link href="/devices" className={buttonClassName({ variant: "outline" })}>장치 목록</Link>} />}>
          {() => null}
        </QueryBoundary>
      </Page>
    )
  }
  const role = deviceRole(row.roles, excluded.has(row.name))
  const cls = row.classes[0]
  const ready = readiness(row.ready)
  return (
    <Page>
      <PageHeader
        title={row.name} status={ready ?? undefined} badges={role && <StatusBadge status={role} />}
        actions={<PlanAction name={row.name} services={row.services} excluded={excluded.has(row.name)} settings={settings.data} etag={etag} fetching={settings.isFetching} refetch={() => void settings.refetch()} />}
        more={[{ label: "원본 보기", onSelect: () => setRaw(true) }]}
      />
      <StatRow className="xl:grid-cols-3">
        <StatTile label="현재 전력" value={row.watts == null ? null : formatNumber(row.watts, 1)} unit="W" reason={state.metrics.length ? "전력 미관측" : "실시간 수신 대기"} />
        <StatTile label="전력 모드" value={row.powerMode || null} reason="전력 모드 미확인" />
        <StatTile label="인스턴스" value={String(row.instances.length)} unit="개" sub={`서비스 ${row.services.length}개`} />
      </StatRow>
      <PageTabs param="tab" defaultValue="overview" tabs={tabs} />
      {tab === "power" ? <PowerTab row={row} /> : tab === "spec" ? <SpecTab row={row} cls={cls} /> : <OverviewTab row={row} state={state} />}
      <RawDialog
        open={raw} onOpenChange={setRaw} filename={`device-${row.name}`}
        value={{ deviceClass: cls ?? null, node: state.snapshot?.nodes.find(node => node.uid === row.uid) ?? null, instances: row.poolInstances }}
      />
    </Page>
  )
}

type InstanceRow = { key: string; service: WorkloadRef | null; status: StatusInfo | null; share: number | undefined; running: number | undefined; waiting: number | undefined }

function instanceStatus(ready: boolean | null, stopped: boolean | null): StatusInfo | null {
  if (stopped) return { label: "중지", tone: "idle" }
  if (ready == null) return null
  return ready ? { label: "준비됨", tone: "ok" } : { label: "준비 중", tone: "progress" }
}

const instanceColumns: Column<InstanceRow>[] = [
  {
    key: "service", header: "서비스", width: "40%",
    cell: row => (
      <span className="inline-flex min-w-0 items-center gap-2">
        {row.status && <StatusDot status={row.status} />}
        {row.service?.name ? <Link href={`/services/${encodeURIComponent(row.service.name)}?uid=${row.service.uid}`} className="truncate hover:underline">{row.service.name}</Link> : "—"}
      </span>
    ),
    sortValue: row => row.service?.name,
  },
  { key: "share", header: "분배 비율", unit: "%", numeric: true, cell: row => row.share == null ? null : formatNumber(row.share, 0), sortValue: row => row.share, missingReason: "분배 미관측" },
  { key: "running", header: "처리 중", numeric: true, cell: row => row.running == null ? null : formatNumber(row.running), sortValue: row => row.running, missingReason: "관측 없음" },
  { key: "waiting", header: "대기", numeric: true, cell: row => row.waiting == null ? null : formatNumber(row.waiting), sortValue: row => row.waiting, missingReason: "관측 없음" },
]

function OverviewTab({ row, state }: { row: DeviceRow; state: LiveState }) {
  const power = usePower(RECENT)
  const values = React.useCallback((point: PowerPoint) => ({ w: point.nodes[row.uid] ?? null }), [row.uid])
  const instances: InstanceRow[] = deviceInstances(row, state).flatMap(instance => {
    const status = instanceStatus(instance.ready, instance.stopped)
    const services: (WorkloadRef | null)[] = instance.services.length ? instance.services : [null]
    return services.map(service => ({
      key: `${instance.id}/${service?.uid ?? ""}`, service, status,
      share: service ? routingShare(state.snapshot?.routing[service.uid], instance.id) : undefined,
      running: service ? instance.live?.running[service.uid] ?? instance.pool?.running[service.uid] : undefined,
      waiting: service ? instance.live?.waiting[service.uid] ?? instance.pool?.waiting[service.uid] : undefined,
    }))
  })
  return (
    <>
      <Section title="최근 15분 전력" card>
        <QueryBoundary query={power.series} skeleton="block">
          {() => <PowerChart points={power.points} domain={power.domain} series={nodeSeries} values={values} label={`${row.name} 전력`} height={200} />}
        </QueryBoundary>
      </Section>
      <DataTable
        label="인스턴스" title="인스턴스" rows={instances} rowKey={item => item.key} columns={instanceColumns}
        defaultSort={{ key: "service", desc: false }} pageSize={0} empty={<EmptyState title="인스턴스 없음" compact />}
      />
    </>
  )
}

function PowerTab({ row }: { row: DeviceRow }) {
  const [range, setRange] = useTimeRangeParam(["15m", "1h"], "15m", HOUR)
  const power = usePower(range)
  const values = React.useCallback((point: PowerPoint) => ({ w: point.nodes[row.uid] ?? null }), [row.uid])
  const node = power.window.data?.nodes?.find(item => item.node.uid === row.uid)
  const energy = energyParts(node?.joules)
  const absent = power.window.error ? "조회 실패" : power.window.data ? (node ? "구간 계측 불완전" : "계측 없음") : "조회 중"
  return (
    <>
      <TimeRange value={range} onChange={setRange} presets={["15m", "1h"]} custom={{ maxSpanMs: HOUR }} />
      <StatRow className="xl:grid-cols-3">
        <StatTile label="구간 에너지" value={energy.value == null ? null : formatNumber(energy.value, 1)} unit={energy.unit} reason={absent} info="선택 구간에서 서버가 적분한 이 장치의 에너지" />
        <StatTile label="평균 전력" value={node?.meanWatts == null ? null : formatNumber(node.meanWatts, 1)} unit="W" reason={absent} />
        <StatTile
          label="공백" value={node ? formatNumber(node.gapSeconds, 1) : null} unit="s" reason={absent}
          sub={node && `구간의 ${formatPercent(node.gapFraction, 2)}`} info="전력 표본이 없는 시간의 합"
        />
      </StatRow>
      <Section title="전력" card>
        <QueryBoundary query={power.series} skeleton="block">
          {() => <PowerChart points={power.points} domain={power.domain} series={nodeSeries} values={values} label={`${row.name} 전력`} />}
        </QueryBoundary>
      </Section>
    </>
  )
}

type ModeRow = InventoryClass["powerModes"][number]

function SpecTab({ row, cls }: { row: DeviceRow; cls: InventoryClass | undefined }) {
  if (!cls) return <EmptyState title="장비 사양 없음" />
  const bytes = quantityBytes(cls.memory)
  const gib = bytes == null ? null : bytes / 2 ** 30
  const columns: Column<ModeRow>[] = [
    {
      key: "name", header: "전력 모드",
      cell: mode => <span className="inline-flex items-center gap-2">{mode.name}{mode.name === row.powerMode && <StatusBadge tone="ok" label="사용 중" />}</span>,
    },
    { key: "method", header: "제어", cell: mode => actuatorText(mode.actuator).method },
    { key: "setting", header: "설정", cell: mode => actuatorText(mode.actuator).setting, missingReason: "설정값 없음" },
  ]
  return (
    <>
      <Section title="장비" card>
        <Fields items={[
          { label: "종류", value: cls.name },
          { label: "아키텍처", value: cls.architecture },
          { label: "메모리", value: gib == null ? cls.memory : `${formatNumber(gib, Number.isInteger(gib) ? 0 : 1)} GiB` },
          { label: "가속기", value: cls.accelerator === "none" ? null : cls.accelerator, reason: "가속기 없음" },
        ]} columns={2} />
      </Section>
      <DataTable label="전력 모드" title="전력 모드" rows={cls.powerModes} rowKey={mode => mode.name} columns={columns} pageSize={0} />
    </>
  )
}

/** Header action: add or remove this device in `excludedNodes`, read-modify-write under the settings ETag. */
function PlanAction({ name, services, excluded, settings, etag, fetching, refetch }: {
  name: string; services: WorkloadRef[]; excluded: boolean
  settings: SchedulerSettings | undefined; etag: string | null; fetching: boolean; refetch: () => void
}) {
  const [open, setOpen] = React.useState(false)
  const mutation = useChillMutation()
  const { toast } = useToast()
  const label = excluded ? "계획에 복귀" : "계획에서 제외"
  const submit = () => {
    const latest = settings
    if (!latest) return
    const current = latest.excludedNodes ?? []
    const body: Record<string, unknown> = Object.fromEntries(writableSettingKeys.filter(key => Object.hasOwn(latest, key)).map(key => [key, latest[key]]))
    body.excludedNodes = excluded ? current.filter(item => item !== name) : [...current, name]
    mutation.mutate({ path: "v1/scheduler/settings", method: "PUT", body: stringifySchedulerJSON(body), version: etag ?? undefined }, {
      onSuccess: () => {
        setOpen(false)
        toast({ title: excluded ? "계획에 복귀함" : "계획에서 제외함", description: name, type: "success" })
        refetch()
      },
      onError: error => {
        setOpen(false)
        refetch()
        if (error.unknownOutcome || error.status === 0) toast({ title: "결과 미확인", description: name, type: "warning" })
        else if (error.status === 409) toast({ title: "다른 설정 변경과 충돌", description: "최신 설정을 다시 읽음", type: "error" })
        else if (error.status === 428) toast({ title: "설정 버전 확인 필요", description: "최신 설정을 다시 읽음", type: "error" })
        else toast({ title: "적용 실패", description: `HTTP ${error.status}`, type: "error" })
      },
    })
  }
  return (
    <>
      <Button variant="default" disabled={!settings} onClick={() => { setOpen(true); refetch() }}>{label}</Button>
      <ConfirmDialog
        open={open} onOpenChange={setOpen} title={label} target={name} confirmLabel={excluded ? "복귀" : "제외"}
        changes={[{ label: "스케줄러 계획", before: excluded ? "제외" : "포함", after: excluded ? "포함" : "제외" }]}
        pending={mutation.isPending || fetching} onConfirm={submit}
      >
        <Fields columns={1} items={[{ label: "실행 중인 서비스", value: services.length ? services.map(service => service.name).join(", ") : "없음" }]} />
      </ConfirmDialog>
    </>
  )
}

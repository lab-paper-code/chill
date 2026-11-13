"use client"

import * as React from "react"
import { ArrowDown, ArrowUp, Info, Plus, X } from "lucide-react"
import { Button, FormSection, Hint, IdText, StatusDot } from "@/components/kit"
import { InputBlock } from "@/components/custom/blocks/InputBlock"
import { ToggleGroup, ToggleGroupItem } from "@/components/custom/blocks/ToggleGroupBlock"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { settingControls } from "@/lib/scheduler-settings"
import { schedulerOptions } from "@/lib/scheduler-selection"
import { fieldUnit, fields, settingsGroups, type FieldKey, type SettingsForm, type SettingsGroup } from "@/lib/scheduler-settings-form"

export type Service = { uid: string; name: string }
type Props = {
  group: SettingsGroup
  form: SettingsForm
  errors: Record<string, string>
  /** Change row ids (field key, `controls:<key>`, `auto:<uid>`, `demand:<uid>`, `excluded:<node>`, ...). */
  changed: Set<string>
  update: (change: (form: SettingsForm) => SettingsForm) => void
  /** Known services, or the short state label ("조회 중", "조회 실패") while the list is unavailable. */
  services: Service[] | string
  /** Observed device names, or the state label while unavailable. */
  nodes: string[] | string
}

/** The editors of one settings group. */
export function SettingsGroupView(props: Props) {
  const { group } = props
  const title = settingsGroups.find(item => item.value === group)!.label
  if (group === "targets") return <Targets {...props} />
  if (group === "controls") return (
    <FormSection title={title}>
      <div className="grid gap-x-8 gap-y-3 sm:grid-cols-2">
        {settingControls.map(control => (
          <SwitchRow key={control.key} label={control.label} changed={props.changed.has(`controls:${control.key}`)} checked={props.form.controls[control.key]}
            onChange={on => props.update(form => ({ ...form, controls: { ...form.controls, [control.key]: on } }))} />
        ))}
      </div>
    </FormSection>
  )
  // The scheduler choice decides which search values matter, so it leads its group.
  const items = fields.filter(field => field.group === group).sort((a, b) => Number(b.kind === "strategy") - Number(a.kind === "strategy"))
  return (
    <FormSection title={title}>
      <div className="grid gap-x-10 gap-y-3 sm:grid-cols-2">
        {items.map(field => {
          const value = props.form.fields[field.key], changed = props.changed.has(field.key)
          const set = (next: string | boolean) => props.update(form => ({ ...form, fields: { ...form.fields, [field.key]: next } }))
          if (field.kind === "boolean") return <SwitchRow key={field.key} label={field.label} help={field.help} changed={changed} checked={value === true} onChange={set} />
          if (field.kind === "strategy") return (
            <div key={field.key} className="flex min-h-9 items-center justify-between gap-4">
              <FieldLabel label={field.label} help={field.help} changed={changed} />
              <ToggleGroup type="single" variant="segmented" size="sm" aria-label={field.label} value={String(value)} onValueChange={next => { if (next) set(next) }}>
                {schedulerOptions.map(option => <ToggleGroupItem key={option.value} value={option.value}>{option.label}</ToggleGroupItem>)}
              </ToggleGroup>
            </div>
          )
          return <NumberField key={field.key} label={field.label} help={field.help} unit={fieldUnit(field.key as FieldKey)} changed={changed} value={String(value)} error={props.errors[field.key]} onChange={set} />
        })}
      </div>
    </FormSection>
  )
}

function FieldLabel({ label, help, changed, htmlFor }: { label: React.ReactNode; help?: string; changed: boolean; htmlFor?: string }) {
  return (
    <div className="flex min-h-5 min-w-0 items-center gap-1.5">
      <Label htmlFor={htmlFor} className="min-w-0"><span className="truncate">{label}</span></Label>
      {help && <Hint text={help} focusable><Info className="size-3.5 text-muted-foreground" aria-label="설명" /></Hint>}
      {changed && <StatusDot tone="progress" label="변경됨" />}
    </div>
  )
}

/** Label on the left, a fixed-width input with its unit on the right. */
function NumberField({ label, help, unit, changed, value, error, onChange, placeholder }: {
  label: React.ReactNode; help?: string; unit?: string; changed: boolean; value: string; error?: string; onChange: (value: string) => void; placeholder?: string
}) {
  const id = React.useId()
  return (
    <div className="flex min-w-0 items-start justify-between gap-4">
      <div className="flex min-h-9 min-w-0 items-center"><FieldLabel htmlFor={id} label={label} help={help} changed={changed} /></div>
      <InputBlock id={id} className="w-36 shrink-0" inputMode="decimal" autoComplete="off" spellCheck={false} value={value} placeholder={placeholder} error={error}
        rightIcon={unit ? <span className="text-xs">{unit}</span> : undefined} onChange={event => onChange(event.target.value)} />
    </div>
  )
}

function SwitchRow({ label, help, changed, checked, onChange }: { label: string; help?: string; changed: boolean; checked: boolean; onChange: (on: boolean) => void }) {
  const id = React.useId()
  return (
    <div className="flex min-h-9 items-center justify-between gap-3">
      <FieldLabel htmlFor={id} label={label} help={help} changed={changed} />
      <Switch id={id} checked={checked} onCheckedChange={onChange} />
    </div>
  )
}

function CheckRow({ checked, onChange, changed, children }: { checked: boolean; onChange: (on: boolean) => void; changed: boolean; children: React.ReactNode }) {
  return (
    <div className="flex min-h-9 min-w-0 items-center gap-2">
      <Label className="min-w-0 font-normal">
        <Checkbox checked={checked} onCheckedChange={value => onChange(value === true)} />
        <span className="truncate">{children}</span>
      </Label>
      {changed && <StatusDot tone="progress" label="변경됨" />}
    </div>
  )
}

const help = {
  autonomous: "선택한 서비스의 구성을 스케줄러가 바꿈",
  demand: "서비스별 계획 수요의 하한, 비우면 미설정",
  excluded: "스케줄러가 배치, 제거, 전력 모드를 바꾸지 않는 장치",
  priority: "목표를 동시에 지킬 수 없을 때 먼저 지킬 서비스를 고르는 기준",
}

function Targets({ form, errors, changed, update, services: serviceState, nodes: nodeState }: Props) {
  const services = typeof serviceState === "string" ? [] : serviceState, nodes = typeof nodeState === "string" ? [] : nodeState
  const known = new Map(services.map(service => [service.uid, service.name]))
  const label = (uid: string) => known.get(uid) ?? <IdText value={uid} copy={false} />
  // Stored UIDs no longer in the service list stay visible so they can be removed.
  const uniqueUIDs = [...new Set([...services.map(service => service.uid), ...form.autonomous, ...Object.keys(form.demand)])]
  const nodeNames = [...new Set([...nodes, ...form.excluded])].sort()
  const toggle = (list: string[], item: string, on: boolean) => on ? [...list, item] : list.filter(value => value !== item)
  const move = (index: number, offset: number) => update(current => {
    const priority = [...current.priority]
    ;[priority[index], priority[index + offset]] = [priority[index + offset], priority[index]]
    return { ...current, priority }
  })
  const unlisted = services.filter(service => !form.priority.includes(service.uid))
  const empty = (what: string, state: unknown[] | string) => <p className="text-sm text-muted-foreground">{typeof state === "string" ? `${what} ${state}` : `${what} 없음`}</p>
  return (
    <div className="grid gap-4">
      <FormSection title="자동 제어 서비스" info={help.autonomous}>
        {uniqueUIDs.length ? (
          <div className="grid gap-x-6 gap-y-1 sm:grid-cols-2">
            {uniqueUIDs.map(uid => (
              <CheckRow key={uid} checked={form.autonomous.includes(uid)} changed={changed.has(`auto:${uid}`)}
                onChange={on => update(current => ({ ...current, autonomous: toggle(current.autonomous, uid, on) }))}>{label(uid)}</CheckRow>
            ))}
          </div>
        ) : empty("서비스", serviceState)}
      </FormSection>

      <FormSection title="최소 수요" info={help.demand}>
        {uniqueUIDs.length ? (
          <div className="grid gap-x-10 gap-y-3 sm:grid-cols-2">
            {uniqueUIDs.map(uid => (
              <NumberField key={uid} label={label(uid)} unit="req/s" placeholder="미설정" value={form.demand[uid] ?? ""} error={errors[`demand:${uid}`]} changed={changed.has(`demand:${uid}`)}
                onChange={text => update(current => ({ ...current, demand: { ...current.demand, [uid]: text } }))} />
            ))}
          </div>
        ) : empty("서비스", serviceState)}
      </FormSection>

      <FormSection title="제외 장치" info={help.excluded}>
        {nodeNames.length ? (
          <div className="grid grid-cols-2 gap-x-6 gap-y-1 sm:grid-cols-3">
            {nodeNames.map(node => (
              <CheckRow key={node} checked={form.excluded.includes(node)} changed={changed.has(`excluded:${node}`)}
                onChange={on => update(current => ({ ...current, excluded: toggle(current.excluded, node, on) }))}>{node}</CheckRow>
            ))}
          </div>
        ) : empty("장치", nodeState)}
      </FormSection>

      <FormSection title="우선순위" info={help.priority}>
        <div className="grid gap-x-10 sm:grid-cols-2"><div className="flex min-h-9 items-center justify-between gap-4">
          <FieldLabel label="기준" changed={changed.has("sloPriorityStrategy")} />
          <ToggleGroup type="single" variant="segmented" size="sm" aria-label="우선순위 기준" value={form.priorityStrategy}
            onValueChange={next => { if (next) update(current => ({ ...current, priorityStrategy: next as SettingsForm["priorityStrategy"] })) }}>
            <ToggleGroupItem value="excess">목표 초과 정도</ToggleGroupItem>
            <ToggleGroupItem value="workload">서비스 순위</ToggleGroupItem>
          </ToggleGroup>
        </div></div>
        {form.priorityStrategy === "workload" && (
          <div className="grid gap-2 sm:w-1/2 sm:pr-5">
            <FieldLabel label="서비스 순위" changed={changed.has("workloadPriority")} />
            {form.priority.length > 0 && (
              <ol className="divide-y divide-border rounded-md border border-border" aria-label="서비스 순위">
                {form.priority.map((uid, index) => (
                  <li key={uid} className="flex min-h-10 items-center gap-2 px-3">
                    <span className="w-5 text-sm tabular-nums text-muted-foreground">{index + 1}</span>
                    <span className="min-w-0 flex-1 truncate text-sm">{label(uid)}</span>
                    <Button type="button" size="icon" variant="ghost" aria-label="위로" disabled={index === 0} onClick={() => move(index, -1)}><ArrowUp /></Button>
                    <Button type="button" size="icon" variant="ghost" aria-label="아래로" disabled={index === form.priority.length - 1} onClick={() => move(index, 1)}><ArrowDown /></Button>
                    <Button type="button" size="icon" variant="ghost" aria-label="빼기" onClick={() => update(current => ({ ...current, priority: current.priority.filter(item => item !== uid) }))}><X /></Button>
                  </li>
                ))}
              </ol>
            )}
            {unlisted.length > 0 && (
              <div className="flex flex-wrap gap-2">
                {unlisted.map(service => (
                  <Button key={service.uid} type="button" size="sm" variant="outline" iconLeft={<Plus />}
                    onClick={() => update(current => ({ ...current, priority: [...current.priority, service.uid] }))}>{service.name}</Button>
                ))}
              </div>
            )}
          </div>
        )}
      </FormSection>
    </div>
  )
}

"use client"

import * as React from "react"
import { ArrowDown, ArrowUp, X } from "lucide-react"
import { Button, Hint, Select } from "@/components/kit"
import { InputBlock } from "@/components/custom/blocks/InputBlock"
import { ToggleGroup, ToggleGroupItem } from "@/components/custom/blocks/ToggleGroupBlock"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import type { SchedulerSettings } from "@/lib/read-types"
import { settingControls, settingFields } from "@/lib/scheduler-settings"
import { schedulerOptions } from "@/lib/scheduler-selection"
import { fieldInput, fieldUnit, parseField, type FieldKey } from "@/lib/scheduler-settings-form"
import { object, type Spec } from "@/lib/evaluation-author"
import { isPatchKey, patchDefault, patchKeyLabel, patchKeys, type PatchKey } from "@/lib/experiments-run-editor"

/** Inputs whose text does not parse report here; the editor blocks the preflight while any is invalid. */
export const InvalidContext = React.createContext<(id: string, bad: boolean) => void>(() => {})
function useInvalid(id: string, bad: boolean) {
  const report = React.useContext(InvalidContext)
  React.useEffect(() => { report(id, bad); return () => report(id, false) }, [id, bad, report])
}

/** Text input that writes the parsed value on every valid keystroke and keeps the text while it is invalid. */
export function CodecInput({ id, label, initial, parse, onValue, unit, hideLabel = false }: {
  id: string; label: string; initial: string; parse: (text: string) => unknown; onValue: (value: unknown) => void; unit?: string; hideLabel?: boolean
}) {
  const [text, setText] = React.useState(initial)
  const [error, setError] = React.useState("")
  useInvalid(id, !!error)
  return (
    <InputBlock
      label={hideLabel ? undefined : label} aria-label={hideLabel ? label : undefined} inputMode="decimal" autoComplete="off" spellCheck={false}
      value={text} error={error} aria-invalid={!!error} rightIcon={unit ? <span className="text-xs">{unit}</span> : undefined}
      onChange={event => {
        const next = event.target.value
        setText(next)
        try { onValue(parse(next)); setError("") } catch (failure) { setError(failure instanceof Error ? failure.message : "형식 오류") }
      }}
    />
  )
}

/** A live service, or a retired registration a stored definition still names (from the load scenario snapshots). */
export type Service = { uid: string; name: string; retired?: boolean }
type Context = { services: Service[]; nodes: string[]; current?: SchedulerSettings }

function serviceName(services: Service[], uid: string): string {
  const service = services.find(item => item.uid === uid)
  return !service ? "미확인 서비스" : service.retired ? `${service.name} (이전 등록)` : service.name
}
const fieldText = (key: FieldKey, value: unknown) => {
  const text = fieldInput(key, { [key]: value } as unknown as SchedulerSettings)
  return typeof text === "string" ? text : ""
}
const rate = (text: string) => {
  const value = Number(text.trim())
  if (!/^\d+(?:\.\d+)?$/.test(text.trim()) || !Number.isFinite(value)) throw new Error("0 이상 숫자")
  return value
}

/** Settings changes of a run or a stage: one typed input per added scheduler setting key. */
export function SettingsPatchEditor({ id, patch, onChange, context }: { id: string; patch: Spec; onChange: (patch: Spec) => void; context: Context }) {
  const keys = Object.keys(patch)
  const set = (key: string, value: unknown) => onChange({ ...patch, [key]: value })
  const remove = (key: string) => { const next = { ...patch }; delete next[key]; onChange(next) }
  const free = patchKeys.filter(key => !keys.includes(key))
  return (
    <div className="grid gap-3">
      {keys.length > 0 && (
        <ul className="grid divide-y divide-border rounded-lg border border-border">
          {keys.map(key => (
            <li key={key} className="grid gap-2 px-3 py-3">
              <div className="flex min-h-7 items-center gap-2">
                {isPatchKey(key)
                  ? <span className="min-w-0 truncate text-sm font-medium">{patchKeyLabel(key)}</span>
                  : <Hint text={`코드 ${key}`} focusable className="min-w-0 truncate text-sm font-medium">기타 설정</Hint>}
                <Button type="button" size="icon" variant="ghost" className="ml-auto size-7" aria-label={`${isPatchKey(key) ? patchKeyLabel(key) : "기타 설정"} 제거`} onClick={() => remove(key)}><X /></Button>
              </div>
              {isPatchKey(key) && <PatchValue id={`${id}:${key}`} name={key} value={patch[key]} onChange={value => set(key, value)} context={context} />}
            </li>
          ))}
        </ul>
      )}
      {free.length > 0 && (
        <div className="sm:w-64">
          <Select inputSize="sm" searchable placeholder="설정 추가" value="" options={free.map(key => ({ value: key, label: patchKeyLabel(key) }))}
            onChange={key => { if (isPatchKey(key)) set(key, patchDefault(key, context.current)) }} />
        </div>
      )}
    </div>
  )
}

function PatchValue({ id, name, value, onChange, context }: { id: string; name: PatchKey; value: unknown; onChange: (value: unknown) => void; context: Context }) {
  const label = patchKeyLabel(name)
  switch (name) {
    case "autonomousWorkloads": case "excludedNodes": {
      const chosen = Array.isArray(value) ? value.map(String) : []
      const options = name === "excludedNodes"
        ? [...new Set([...context.nodes, ...chosen])].map(node => ({ value: node, label: node }))
        : [...new Set([...context.services.filter(service => !service.retired).map(service => service.uid), ...chosen])].map(uid => ({ value: uid, label: serviceName(context.services, uid) }))
      return (
        <div role="group" aria-label={label} className={`grid gap-x-4 gap-y-1 sm:grid-cols-2 xl:grid-cols-3 ${name === "excludedNodes" ? "grid-cols-2" : "grid-cols-1"}`}>
          {options.map(option => (
            <Label key={option.value} className="min-h-8 min-w-0 font-normal">
              <Checkbox checked={chosen.includes(option.value)} onCheckedChange={on => onChange(on === true ? [...chosen, option.value] : chosen.filter(item => item !== option.value))} />
              <span className="truncate">{option.label}</span>
            </Label>
          ))}
        </div>
      )
    }
    case "controls": {
      const controls = object(value)
      return (
        <div role="group" aria-label={label} className="grid grid-cols-1 gap-x-6 gap-y-1 sm:grid-cols-2 xl:grid-cols-3">
          {settingControls.map(control => (
            <Label key={control.key} className="min-h-8 justify-between font-normal">
              <span className="truncate">{control.label}</span>
              <Switch checked={controls[control.key] === true} onCheckedChange={on => onChange({ ...controls, [control.key]: on })} />
            </Label>
          ))}
        </div>
      )
    }
    case "sloPriorityStrategy":
      return (
        <ToggleGroup type="single" variant="segmented" size="sm" aria-label={label} className="justify-self-start" value={String(value ?? "excess")} onValueChange={next => { if (next) onChange(next) }}>
          <ToggleGroupItem value="excess">목표 초과 정도</ToggleGroupItem>
          <ToggleGroupItem value="workload">서비스 순위</ToggleGroupItem>
        </ToggleGroup>
      )
    case "minimumDemand": return <DemandEditor id={id} value={object(value)} onChange={onChange} services={context.services} />
    case "workloadPriority": return <PriorityEditor value={Array.isArray(value) ? value.map(String) : []} onChange={onChange} services={context.services} />
  }
  const field = settingFields.find(item => item.key === name)!
  if (field.kind === "boolean") return (
    <Label className="min-h-8 font-normal"><Switch checked={value === true} onCheckedChange={on => onChange(on)} />{value === true ? "켜짐" : "꺼짐"}</Label>
  )
  if (field.kind === "strategy") return (
    <ToggleGroup type="single" variant="segmented" size="sm" aria-label={label} className="justify-self-start" value={String(value ?? "")} onValueChange={next => { if (next) onChange(next) }}>
      {schedulerOptions.map(option => <ToggleGroupItem key={option.value} value={option.value}>{option.label}</ToggleGroupItem>)}
    </ToggleGroup>
  )
  return (
    <div className="sm:w-64">
      <CodecInput id={id} label={label} hideLabel initial={fieldText(name, value)} unit={fieldUnit(name)} parse={text => parseField(name, text)} onValue={onChange} />
    </div>
  )
}

function DemandEditor({ id, value, onChange, services }: { id: string; value: Spec; onChange: (value: Spec) => void; services: Service[] }) {
  const uids = Object.keys(value)
  const free = services.filter(service => !service.retired && !uids.includes(service.uid))
  return (
    <div className="grid gap-2">
      {uids.map(uid => (
        <div key={uid} className="grid grid-cols-[minmax(0,1fr)_9rem_auto] items-center gap-2">
          <span className="truncate text-sm">{serviceName(services, uid)}</span>
          <CodecInput id={`${id}:${uid}`} label={`${serviceName(services, uid)} 최소 수요`} hideLabel initial={String(value[uid] ?? "")} unit="req/s" parse={rate} onValue={rate => onChange({ ...value, [uid]: rate })} />
          <Button type="button" size="icon" variant="ghost" className="size-7" aria-label={`${serviceName(services, uid)} 제거`} onClick={() => { const next = { ...value }; delete next[uid]; onChange(next) }}><X /></Button>
        </div>
      ))}
      {free.length > 0 && (
        <div className="sm:w-64">
          <Select inputSize="sm" searchable placeholder="서비스 추가" value="" options={free.map(service => ({ value: service.uid, label: service.name }))} onChange={uid => onChange({ ...value, [uid]: 0 })} />
        </div>
      )}
    </div>
  )
}

function PriorityEditor({ value, onChange, services }: { value: string[]; onChange: (value: string[]) => void; services: Service[] }) {
  const free = services.filter(service => !service.retired && !value.includes(service.uid))
  const move = (from: number, to: number) => { const next = [...value]; [next[from], next[to]] = [next[to], next[from]]; onChange(next) }
  return (
    <div className="grid gap-2">
      {value.length > 0 && (
        <ol className="grid gap-1">
          {value.map((uid, index) => (
            <li key={uid} className="flex min-h-8 items-center gap-2 text-sm">
              <span className="w-5 text-right tabular-nums text-muted-foreground">{index + 1}</span>
              <span className="min-w-0 flex-1 truncate">{serviceName(services, uid)}</span>
              <Button type="button" size="icon" variant="ghost" className="size-7" aria-label="위로" disabled={index === 0} onClick={() => move(index, index - 1)}><ArrowUp /></Button>
              <Button type="button" size="icon" variant="ghost" className="size-7" aria-label="아래로" disabled={index === value.length - 1} onClick={() => move(index, index + 1)}><ArrowDown /></Button>
              <Button type="button" size="icon" variant="ghost" className="size-7" aria-label={`${serviceName(services, uid)} 제거`} onClick={() => onChange(value.filter(item => item !== uid))}><X /></Button>
            </li>
          ))}
        </ol>
      )}
      {free.length > 0 && (
        <div className="sm:w-64">
          <Select inputSize="sm" searchable placeholder="서비스 추가" value="" options={free.map(service => ({ value: service.uid, label: service.name }))} onChange={uid => onChange([...value, uid])} />
        </div>
      )}
    </div>
  )
}

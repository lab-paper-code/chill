"use client"

import * as React from "react"
import { ArrowDown, ArrowUp, ChevronDown, Plus, Trash2 } from "lucide-react"
import { Button, FormSection, Section, Select } from "@/components/kit"
import { InputBlock } from "@/components/custom/blocks/InputBlock"
import { ToggleGroup, ToggleGroupItem } from "@/components/custom/blocks/ToggleGroupBlock"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { formatDurationNanos } from "@/lib/format"
import { millisecondsToNanos, nanosToMilliseconds } from "@/lib/scheduler"
import { object, records, specValue, type LoadScenario, type Spec } from "@/lib/evaluation-author"
import { scenarioSeconds } from "@/lib/experiments-run-editor"
import { CodecInput, SettingsPatchEditor, type Service } from "./run-editor-settings"
import type { SchedulerSettings } from "@/lib/read-types"

export type Path = (string | number)[]
export type Published = { id: string; name: string; stages: string[] }
export type EditorContext = { services: Service[]; nodes: string[]; current?: SchedulerSettings; scenarios: LoadScenario[]; published: Published[] }
type Props = { spec: Spec; edit: (path: Path, value: unknown) => void; context: EditorContext }

const text = (value: unknown) => typeof value === "string" ? value : ""
const STORED = "~stored"

export function BasicSection({ spec, edit, context, name, setName, nameError, locked }: Props & { name: string; setName: (name: string) => void; nameError: string; locked: boolean }) {
  const [touched, setTouched] = React.useState(false)
  const questionId = React.useId()
  const node = text(specValue(spec, ["loadGenerator", "nodeName"]))
  const patch = object(spec.settingsPatch)
  return (
    <div className="grid gap-4">
      <FormSection title="기본">
        <InputBlock label="실험 이름" value={name} disabled={locked} autoComplete="off" spellCheck={false}
          error={touched || name ? nameError || undefined : undefined} aria-invalid={(touched || !!name) && !!nameError}
          onBlur={() => setTouched(true)} onChange={event => setName(event.target.value.trim())} />
        <div className="grid gap-1.5">
          <Label htmlFor={questionId}>질문</Label>
          <Textarea id={questionId} rows={3} value={text(spec.question)} onChange={event => edit(["question"], event.target.value)} />
        </div>
        <div className="sm:w-80">
          <Select label="부하 발생 장치" searchable placeholder="선택" value={node}
            options={[...new Set([...context.nodes, ...(node ? [node] : [])])].map(item => ({ value: item, label: item }))}
            onChange={value => edit(["loadGenerator", "nodeName"], value)} />
        </div>
      </FormSection>
      <FormSection title="공통 설정 변경" info="단계 설정이 없는 단계에 적용">
        <SettingsPatchEditor id="patch:run" patch={patch} context={context} onChange={next => edit(["settingsPatch"], Object.keys(next).length ? next : undefined)} />
      </FormSection>
    </div>
  )
}

export function StagesSection({ spec, edit, context, restructure }: Props & { restructure: (stages: Spec[]) => void }) {
  const stages = records(spec.stages)
  const ids = stages.map(stage => text(stage.id))
  const move = (from: number, to: number) => { const next = [...stages]; [next[from], next[to]] = [next[to], next[from]]; restructure(next) }
  return (
    <div className="grid gap-4">
      {stages.map((stage, index) => {
        const path: Path = ["stages", index]
        const ref = object(stage.loadScenarioRef)
        const uid = text(ref.uid)
        const scenario = context.scenarios.find(item => item.uid === uid)
        const id = text(stage.id)
        const duplicate = !!id && ids.indexOf(id) !== index
        return (
          <Section key={index} card title={`${index + 1}. ${id || "새 단계"}`} actions={<>
            <Button type="button" size="icon" variant="ghost" className="size-8" aria-label="위로" disabled={index === 0} onClick={() => move(index, index - 1)}><ArrowUp /></Button>
            <Button type="button" size="icon" variant="ghost" className="size-8" aria-label="아래로" disabled={index === stages.length - 1} onClick={() => move(index, index + 1)}><ArrowDown /></Button>
            <Button type="button" size="icon" variant="ghost" className="size-8" aria-label="단계 삭제" onClick={() => restructure(stages.filter((_, i) => i !== index))}><Trash2 /></Button>
          </>}>
            <div className="grid gap-4">
              <div className="grid gap-4 sm:grid-cols-2">
                <InputBlock label="단계 ID" value={id} autoComplete="off" spellCheck={false} error={duplicate ? "중복" : undefined} aria-invalid={duplicate} onChange={event => edit([...path, "id"], event.target.value.trim())} />
                <InputBlock label="구분" value={text(stage.row)} autoComplete="off" onChange={event => edit([...path, "row"], event.target.value)} />
              </div>
              <Select label="부하 시나리오" searchable placeholder="선택" value={scenario || !uid ? uid : STORED}
                helperText={scenario ? `구간 ${records(scenario.spec.parts).length}개, ${formatDurationNanos(scenarioSeconds(scenario.spec) * 1e9)}` : uid ? "목록에 없는 시나리오" : undefined}
                options={[...(uid && !scenario ? [{ value: STORED, label: `${text(ref.name) || "이름 없음"} (저장된 참조)` }] : []), ...context.scenarios.map(item => ({ value: item.uid, label: item.name }))]}
                onChange={value => { const item = context.scenarios.find(entry => entry.uid === value); if (item) edit([...path, "loadScenarioRef"], { ...ref, name: item.name, uid: item.uid }) }} />
              <StagePatch id={`patch:${index}`} patch={stage.settingsPatch} context={context} onChange={next => edit([...path, "settingsPatch"], next)} />
            </div>
          </Section>
        )
      })}
      <Button type="button" variant="outline" className="justify-self-start" iconLeft={<Plus />} onClick={() => restructure([...stages, { id: "", row: "" }])}>단계 추가</Button>
    </div>
  )
}

// A stage either inherits the run's settings changes or replaces them with its own (folded: the list is long).
function StagePatch({ id, patch, context, onChange }: { id: string; patch: unknown; context: EditorContext; onChange: (patch: Spec | undefined) => void }) {
  const own = patch !== undefined && patch !== null
  const [open, setOpen] = React.useState(false)
  const count = Object.keys(object(patch)).length
  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <span className="text-sm font-semibold">설정 변경</span>
        <ToggleGroup type="single" variant="segmented" size="sm" aria-label="설정 변경" value={own ? "own" : "inherit"} onValueChange={value => { if (!value) return; onChange(value === "own" ? {} : undefined); setOpen(value === "own") }}>
          <ToggleGroupItem value="inherit">공통 설정</ToggleGroupItem>
          <ToggleGroupItem value="own">단계 설정</ToggleGroupItem>
        </ToggleGroup>
        {own && (
          <Button type="button" size="sm" variant="ghost" aria-expanded={open} iconRight={<ChevronDown className={open ? "rotate-180" : undefined} />} onClick={() => setOpen(!open)}>
            {count ? `${count}건` : "없음"}
          </Button>
        )}
      </div>
      {own && <div hidden={!open}><SettingsPatchEditor id={id} patch={object(patch)} context={context} onChange={onChange} /></div>}
    </div>
  )
}

export function ComparisonsSection({ spec, edit, context, restructure }: Props & { restructure: (comparisons: Spec[]) => void }) {
  const comparisons = records(spec.comparisons)
  const stageIds = records(spec.stages).map(stage => text(stage.id)).filter(Boolean)
  return (
    <div className="grid gap-4">
      {comparisons.map((comparison, index) => (
        <Section key={index} card title={`비교 ${index + 1}`} actions={
          <Button type="button" size="icon" variant="ghost" className="size-8" aria-label="비교 삭제" onClick={() => restructure(comparisons.filter((_, i) => i !== index))}><Trash2 /></Button>
        }>
          <div className="grid gap-4">
            <div className="grid gap-4 md:grid-cols-2">
              <ComparisonSide label="후보" value={object(comparison.left)} stageIds={stageIds} published={context.published} onChange={value => edit(["comparisons", index, "left"], value)} />
              <ComparisonSide label="기준" value={object(comparison.right)} stageIds={stageIds} published={context.published} onChange={value => edit(["comparisons", index, "right"], value)} />
            </div>
            <InputBlock label="비교 사유" value={text(comparison.reason)} onChange={event => edit(["comparisons", index, "reason"], event.target.value)} />
          </div>
        </Section>
      ))}
      <Button type="button" variant="outline" className="justify-self-start" iconLeft={<Plus />}
        onClick={() => restructure([...comparisons, { left: { stageID: "" }, right: { stageID: "" }, reason: "" }])}>비교 추가</Button>
    </div>
  )
}

function ComparisonSide({ label, value, stageIds, published, onChange }: { label: string; value: Spec; stageIds: string[]; published: Published[]; onChange: (value: Spec) => void }) {
  const historical = value.historical !== undefined
  const past = object(value.historical)
  const stage = text(historical ? past.stageID : value.stageID)
  const publicationID = text(past.publicationID)
  const source = published.find(item => item.id === publicationID)
  const stageOptions = (historical ? source?.stages ?? [] : stageIds)
  const withStored = (options: string[]) => [...new Set([...options, ...(stage ? [stage] : [])])].map(item => ({ value: item, label: item }))
  return (
    <fieldset className="grid min-w-0 content-start gap-3 rounded-lg border border-border p-3">
      <legend className="px-1 text-sm font-semibold">{label}</legend>
      <ToggleGroup type="single" variant="segmented" size="sm" aria-label={`${label} 출처`} className="justify-self-start" value={historical ? "past" : "same"}
        onValueChange={next => { if (next) onChange(next === "past" ? { historical: { publicationID: "", stageID: "" } } : { stageID: "" }) }}>
        <ToggleGroupItem value="same">이 실험</ToggleGroupItem>
        <ToggleGroupItem value="past">과거 결과</ToggleGroupItem>
      </ToggleGroup>
      {historical && (
        <Select label="결과" searchable placeholder="선택" value={publicationID}
          options={[...(publicationID && !source ? [{ value: publicationID, label: `${publicationID.slice(0, 8)} (저장된 참조)` }] : []), ...published.map(item => ({ value: item.id, label: item.name }))]}
          onChange={id => onChange({ historical: { publicationID: id, stageID: "" } })} />
      )}
      <Select label="단계" placeholder="선택" value={stage} disabled={historical && !publicationID} options={withStored(stageOptions)}
        onChange={id => onChange(historical ? { historical: { ...past, stageID: id } } : { stageID: id })} />
    </fieldset>
  )
}

const percentText = (value: unknown) => typeof value === "number" ? String(Number((value * 100).toPrecision(12))) : ""
function percent(input: string): number | undefined {
  if (!input.trim()) return undefined
  const value = Number(input)
  if (!/^\d+(?:\.\d+)?$/.test(input.trim()) || value > 100) throw new Error("0 이상 100 이하")
  return value / 100
}
function seconds(input: string): number | undefined {
  if (!input.trim()) return undefined
  const value = Number(input)
  if (!/^\d+(?:\.\d+)?$/.test(input.trim()) || !Number.isFinite(value)) throw new Error("0 이상 숫자")
  return value
}
function nanos(input: string): bigint | undefined {
  if (!input.trim()) return undefined
  try { return millisecondsToNanos(input.trim(), 0n) } catch { throw new Error("0 이상, 소수 6자리까지") }
}
const nanosText = (value: unknown) => typeof value === "bigint" || (typeof value === "number" && Number.isSafeInteger(value)) ? nanosToMilliseconds(value) : ""

export function CriteriaSection({ spec, edit }: Omit<Props, "context">) {
  const generator: Path = ["judgment", "generatorQuality"], energy: Path = ["judgment", "energyCoverage"]
  return (
    <div className="grid gap-4">
      <FormSection title="부하 발생 품질">
        <div className="grid gap-4 sm:grid-cols-2">
          <CodecInput id="criteria:lateness" label="발행 지연 허용" unit="ms" initial={nanosText(specValue(spec, [...generator, "latenessToleranceNanos"]))} parse={nanos} onValue={value => edit([...generator, "latenessToleranceNanos"], value)} />
          <CodecInput id="criteria:exceeding" label="지연 초과 허용 비율" unit="%" initial={percentText(specValue(spec, [...generator, "maxExceedingFraction"]))} parse={percent} onValue={value => edit([...generator, "maxExceedingFraction"], value)} />
        </div>
      </FormSection>
      <FormSection title="전력 계측">
        <div className="grid gap-4 sm:grid-cols-2">
          <CodecInput id="criteria:gap" label="결손 허용 비율" unit="%" initial={percentText(specValue(spec, [...energy, "maxGapFraction"]))} parse={percent} onValue={value => edit([...energy, "maxGapFraction"], value)} />
          <CodecInput id="criteria:single" label="단일 공백 허용" unit="s" initial={String(specValue(spec, [...energy, "maxSingleGapSeconds"]) ?? "")} parse={seconds} onValue={value => edit([...energy, "maxSingleGapSeconds"], value)} />
        </div>
      </FormSection>
    </div>
  )
}

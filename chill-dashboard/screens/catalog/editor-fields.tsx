"use client"

import { useState, type ReactNode } from "react"
import { Plus, Trash2 } from "lucide-react"
import { Button } from "@/components/kit"
import { InputBlock } from "@/components/custom/blocks/InputBlock"
import { SelectBlock } from "@/components/custom/blocks/SelectBlock"
import type { Errors } from "@/lib/catalog-editor"


export function Text({ label, value, onChange, error, placeholder, disabled }: { label: string; value: string; onChange: (value: string) => void; error?: string; placeholder?: string; disabled?: boolean }) {
  return <InputBlock label={label} value={value} onChange={event => onChange(event.target.value)} error={error} placeholder={placeholder} disabled={disabled} />
}

/** A number box; an empty box is NaN and fails validation. */
export function Num({ label, value, onChange, error, min = 1 }: { label: string; value: number | undefined; onChange: (value: number) => void; error?: string; min?: number }) {
  return <InputBlock type="number" label={label} min={min} step={1} value={value === undefined || Number.isNaN(value) ? "" : value} onChange={event => onChange(event.target.valueAsNumber)} error={error} />
}

export function Pick({ label, value, options, onChange, error, placeholder = "선택" }: { label: string; value: string; options: { value: string; label?: string }[]; onChange: (value: string) => void; error?: string; placeholder?: string }) {
  return <SelectBlock label={label} value={value} options={options.map(o => ({ value: o.value, label: o.label ?? o.value }))} onChange={onChange} error={error} placeholder={placeholder} />
}

/** Positive integers typed as "1, 224, 224"; an unreadable text reports an empty list. */
export function IntList({ label, value, onChange, error }: { label: string; value: number[]; onChange: (value: number[]) => void; error?: string }) {
  const [text, setText] = useState(value.join(", "))
  const parse = (t: string) => { const parts = t.split(/[\s,]+/).filter(Boolean); return parts.length && parts.every(p => /^\d+$/.test(p)) ? parts.map(Number) : [] }
  // Follow outside changes (row removed or replaced) without fighting the user's typing.
  if (parse(text).join(",") !== value.join(",") && value.length) setText(value.join(", "))
  return <InputBlock label={label} value={text} onChange={event => { setText(event.target.value); onChange(parse(event.target.value)) }} error={error} placeholder="1, 224, 224" />
}

/** Repeated items: numbered cards with remove, then an add button. */
export function RowList<T>({ title, items, onChange, blank, render, error, min = 1, addLabel }: {
  title: string; items: T[]; onChange: (items: T[]) => void; blank: () => T
  render: (item: T, index: number, set: (patch: Partial<T>) => void) => ReactNode
  error?: string; min?: number; addLabel?: string
}) {
  return (
    <div className="grid gap-3">
      {items.map((item, index) => (
        <div key={index} role="group" aria-label={`${title} ${index + 1}`} className="grid gap-3 rounded-lg border border-border p-3">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-muted-foreground">{title} {index + 1}</span>
            <Button type="button" size="sm" variant="ghost" iconLeft={<Trash2 />} disabled={items.length <= min} aria-label={`${title} ${index + 1} 삭제`} onClick={() => onChange(items.filter((_, i) => i !== index))}>삭제</Button>
          </div>
          {render(item, index, patch => onChange(items.map((x, i) => i === index ? { ...x, ...patch } : x)))}
        </div>
      ))}
      {error && <p role="alert" className="text-xs text-fail">{error}</p>}
      <Button type="button" size="sm" variant="outline" className="w-fit" iconLeft={<Plus />} onClick={() => onChange([...items, blank()])}>{addLabel ?? `${title} 추가`}</Button>
    </div>
  )
}

/** Paths as a list of text rows without a card per row. */
export function PathList({ label, items, onChange, errors, prefix }: { label: string; items: string[]; onChange: (items: string[]) => void; errors: Errors; prefix: string }) {
  return (
    <div className="grid gap-2">
      <span className="text-sm font-medium">{label}</span>
      {items.map((path, i) => (
        <div key={i} className="flex items-start gap-2">
          <div className="min-w-0 flex-1"><InputBlock aria-label={`${label} ${i + 1}`} value={path} placeholder="/artifact/model.onnx" onChange={event => onChange(items.map((x, j) => j === i ? event.target.value : x))} error={errors[`${prefix}.${i}`]} /></div>
          <Button type="button" size="icon" variant="ghost" aria-label={`${label} ${i + 1} 삭제`} disabled={items.length <= 1} onClick={() => onChange(items.filter((_, j) => j !== i))}><Trash2 /></Button>
        </div>
      ))}
      {errors[prefix] && <p role="alert" className="text-xs text-fail">{errors[prefix]}</p>}
      <Button type="button" size="sm" variant="outline" className="w-fit" iconLeft={<Plus />} onClick={() => onChange([...items, ""])}>{label} 추가</Button>
    </div>
  )
}

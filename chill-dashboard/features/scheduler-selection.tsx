"use client"

import * as React from "react"
import { Button } from "@/components/ui/button"
import { schedulerLabel, schedulerOptions } from "@/lib/scheduler-selection"

export function SchedulerSelection({current,value,disabled,save}:{current?:string;value?:string;disabled:boolean;save:(value:string)=>void}) {
  const [next,setNext]=React.useState<string|null>(null)
  const selected=next??value??""
  return <section aria-label="스케줄러 선택" className="grid gap-3 rounded-md border p-4">
    <h2 className="text-lg font-semibold">스케줄러 선택</h2>
    <p className="text-sm">전체 노드 계획에 공통으로 적용해요. 서비스별 CHILL 자동 운영은 계획 참여 여부를 정해요.</p>
    <p className="text-sm">현재: {schedulerLabel(current)}{value!==current&&<> · 적용 전: {schedulerLabel(value)}</>}</p>
    <form className="grid gap-3" onSubmit={event=>{event.preventDefault();if(schedulerOptions.some(option=>option.value===selected)){save(selected);setNext(null)}}}>
      <label className="grid max-w-sm gap-2">사용할 스케줄러<select className="min-h-11 rounded-md border bg-background px-3" value={selected} disabled={disabled} onChange={event=>setNext(event.target.value)}>
        {!schedulerOptions.some(option=>option.value===selected)&&<option value={selected} disabled>{schedulerLabel(value)}</option>}
        {schedulerOptions.map(option=><option key={option.value} value={option.value}>{option.label}</option>)}
      </select></label>
      {schedulerOptions.map(option=><p key={option.value} className="text-sm text-muted-foreground">{option.label}: {option.description}</p>)}
      <Button className="justify-self-start" type="submit" disabled={disabled||selected===value||!schedulerOptions.some(option=>option.value===selected)}>변경 목록에 담기</Button>
    </form>
  </section>
}

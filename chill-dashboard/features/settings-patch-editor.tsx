"use client"

import { useState } from "react"
import { AuthorSelect, AuthorJSON } from "@/components/evaluation-author-fields"
import { Button } from "@/components/ui/button"
import { object, specValue, patchSpec, specJSON, type Spec } from "@/lib/evaluation-author"
import { millisecondsToNanos, nanosToMilliseconds, parseSchedulerJSON } from "@/lib/scheduler"
import { editableSettingKeys, settingFields, settingTargets } from "@/lib/scheduler-settings"
export function SettingsPatchEditor({spec,path,onChange,onValidityChange}:{spec:Spec;path:(string|number)[];onChange:(spec:Spec)=>void;onValidityChange:(valid:boolean)=>void}) {
  const raw=specValue(spec,path),patch=object(raw),stage=path[0]==="stages",[key,setKey]=useState(""),[text,setText]=useState(""),[error,setError]=useState(""),[pending,setPending]=useState(false),[rawInvalid,setRawInvalid]=useState(false)
  const field=settingFields.find(field=>field.key===key),nanos=!!field?.key.endsWith("Nanos"),unit=nanos?"ms":key==="servingMemoryBytes"?"bytes":field?.unit
  const labels=new Map<string,string>([...settingFields,...settingTargets].map(field=>[field.key,field.label]))
  function replace(value:Spec|undefined) {
    onChange(patchSpec(spec,path,value));setKey("");setText("");setPending(false);setError("");onValidityChange(true)
  }
  return <fieldset className="grid min-w-0 gap-3 rounded-md border p-3"><legend className="text-sm">운영 설정 패치</legend>
    <span role="status" className="text-sm">{stage?(raw==null?"전체 실험 설정 상속":"단계 패치로 전체 대체"):raw==null?"전체 실험 패치 미설정":"전체 실험 패치 설정"}</span>
    <AuthorSelect label="설정 항목" value={key} disabled={pending||rawInvalid} onChange={value=>{
      setKey(value);setText("");setError("")
      const current=patch[value]
      if(Object.hasOwn(patch,value)) {
        if(value.endsWith("Nanos")) {
          if(typeof current!=="bigint"&&(typeof current!=="number"||!Number.isFinite(current)||!Number.isInteger(current))) {
            setError("시간 값은 유한한 정수 ns로 입력해 주세요.");onValidityChange(false);return
          }
          setText(nanosToMilliseconds(current))
        } else setText(specJSON(current))
      }
      onValidityChange(true)
    }}><option value="">항목 선택</option>{editableSettingKeys.map(value=><option key={value} value={value}>{labels.get(value)??(value==="controls"?"제어 항목":value)}</option>)}</AuthorSelect>
    {key&&<><label className="grid gap-1 text-sm">{`항목 값 (${unit??"JSON"})`}<textarea disabled={rawInvalid} className="min-h-24 w-full rounded-md border bg-background p-3 font-mono text-xs" value={text} aria-invalid={!!error} onChange={event=>{setText(event.target.value);setPending(true);setError("");onValidityChange(false)}}/></label>
      <Button type="button" variant="outline" disabled={rawInvalid} onClick={()=>{try {
        const value=nanos?millisecondsToNanos(text,["scaleDownStabilizationNanos","replacementDwellNanos","bootstrapCapacityWaitNanos"].includes(key)?0n:1n):parseSchedulerJSON(text)
        if(key==="bootstrapCapacityWaitNanos"&&typeof value==="bigint"&&value>3600000000000n)throw new Error("초기 용량 대기는 0~3600000 ms로 적어 주세요.")
        onChange(patchSpec(spec,path,patchSpec(patch,[key],value)));setPending(false);setError("");onValidityChange(true)
      } catch(error) {setError(error instanceof Error?error.message:"JSON 값 형식 확인");onValidityChange(false)}}}>항목 반영</Button>
      <Button type="button" variant="outline" disabled={rawInvalid} onClick={()=>replace(patchSpec(patch,[key],undefined))}>항목 제거</Button></>}
    {error&&<span role="alert" className="text-destructive">{error}</span>}
    <AuthorJSON label="설정 패치 원문 (ns)" value={patch} disabled={pending} onChange={value=>replace(value)} onValidityChange={valid=>{setRawInvalid(!valid);onValidityChange(valid)}}/>
    <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={rawInvalid} onClick={()=>replace(undefined)}>패치 미설정</Button>{stage&&<Button type="button" variant="outline" disabled={rawInvalid} onClick={()=>replace({})}>빈 단계 패치로 전체 대체</Button>}</div>
  </fieldset>
}

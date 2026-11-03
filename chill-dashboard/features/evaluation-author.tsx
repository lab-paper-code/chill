"use client"

import { useEffect, useId, useRef, useState, type ReactNode } from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { Screen } from "@/components/screen"
import { useChillQuery, useSession } from "@/lib/client"
import { QueryGate } from "@/components/read-region"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { LoadEditor } from "@/features/load-scenario-author"
import { EvaluationPermission, EvaluationSubmit } from "@/features/evaluation-request"
import { object, records, specValue, patchSpec, parseSpec, specJSON, copyAuthorDraft, type Spec, type LoadScenario, type EvidenceInput } from "@/lib/evaluation-author"
import { millisecondsToNanos, nanosToMilliseconds, parseSchedulerJSON } from "@/lib/scheduler"
import { decodeExperimentPage, type ExperimentPage } from "@/lib/experiments"
import { editableSettingKeys, settingFields, settingTargets } from "@/lib/scheduler-settings"


export function useUnsavedAuthor(dirty:boolean) {
  useEffect(()=>{
    if(!dirty)return
    const warn=(event:BeforeUnloadEvent)=>{event.preventDefault();event.returnValue=""}
    window.addEventListener("beforeunload",warn)
    return ()=>window.removeEventListener("beforeunload",warn)
  },[dirty])
}
const selectClass="min-h-11 w-full min-w-0 max-w-full rounded-md border bg-background px-3 text-sm"
export function AuthorSelect({label,value,onChange,children,disabled=false,compact=false}:{label:string;value:string;onChange:(value:string)=>void;children:ReactNode;disabled?:boolean;compact?:boolean}) {
  return <label className="grid min-w-0 gap-1 text-sm"><span className={compact?"sr-only":undefined}>{label}</span><select className={selectClass} value={value} disabled={disabled} onChange={event=>onChange(event.target.value)}>{children}</select></label>
}
export function AuthorField({label,spec,path,onChange,numeric=false,compact=false}:{label:string;spec:Spec;path:(string|number)[];onChange:(spec:Spec)=>void;numeric?:boolean;compact?:boolean}) {
  const value=specValue(spec,path)
  return <label className="grid min-w-0 gap-1 text-sm"><span className={compact?"sr-only":undefined}>{label}</span><Input type={numeric?"number":"text"} step={numeric?"any":undefined} value={value===undefined||value===null?"":String(value)} onChange={event=>{
    const text=event.target.value
    onChange(patchSpec(spec,path,text===""?undefined:numeric?parseSchedulerJSON(text):text))
  }}/></label>
}
export function AuthorJSON({label,value,onChange,onValidityChange,disabled=false}:{label:string;value:Spec;onChange:(value:Spec)=>void;onValidityChange?:(valid:boolean)=>void;disabled?:boolean}) {
  const canonical=specJSON(value),[draft,setDraft]=useState<{text:string;base:string}|null>(null),[error,setError]=useState("")
  return <div className="grid gap-2 text-sm"><label className="grid gap-2">{label}<textarea disabled={disabled} className="min-h-48 w-full rounded-md border bg-background p-3 font-mono text-xs" spellCheck={false} value={error?draft?.text??canonical:draft?.base===canonical?draft.text:canonical} aria-invalid={!!error} onChange={event=>{
    const text=event.target.value
    try {const next=parseSpec(text);setDraft({text,base:specJSON(next)});setError("");onChange(next);onValidityChange?.(true)}
    catch {setDraft({text,base:canonical});setError("JSON 객체 형식 확인");onValidityChange?.(false)}
  }}/></label>{error&&<><span role="alert" className="text-destructive">{error}</span><Button type="button" variant="outline" onClick={()=>{setDraft(null);setError("");onValidityChange?.(true)}}>JSON 변경 취소</Button></>}</div>
}
export function EvidencePicker({label,value,onChange}:{label:string;value:unknown;onChange:(value:Spec|undefined)=>void}) {
  const [set,setSet]=useState(""),[offsets,setOffsets]=useState([0]),[chosen,setChosen]=useState<EvidenceInput|null>(null),setList=useId()
  const {config}=useSession()
  const params=new URLSearchParams({limit:String(Math.min(25,config.maxPageSize)),offset:String(offsets.at(-1)??0)})
  params.set("kind","trace")
  if(set)params.set("set",set)
  const query=useChillQuery<{items:EvidenceInput[];page:{offset:number;limit:number;total:number;nextOffset?:number}}>(`v1/evaluations/inputs?${params}`,{decoder:parseSchedulerJSON})
  const items=query.data?.items??[],current=object(value)
  const identity=(item:Spec)=>JSON.stringify([item.store,item.key])
  const selected=current.key?identity(current):"",next=query.data?.page.nextOffset
  const selectedItem=items.find(item=>identity(item)===selected)??(chosen&&identity(chosen)===selected?chosen:current)
  return <div className="grid min-w-0 gap-2 sm:grid-cols-2">
    <label className="grid min-w-0 gap-1 text-sm">부하 설정 이름<Input list={setList} value={set} onChange={event=>{setSet(event.target.value);setOffsets([0])}}/><datalist id={setList}>{[...new Set(items.map(item=>item.set).filter(Boolean))].map(value=><option key={value} value={value}/>)}</datalist></label>
    <div className="min-w-0 sm:col-span-2"><AuthorSelect label={label} value={selected} onChange={key=>{const item=items.find(item=>identity(item)===key);setChosen(item??null);onChange(item?{...current,store:item.store,key:item.key,size:item.size}:undefined)}}>
      <option value="">미선택</option>{selected&&!items.some(item=>identity(item)===selected)&&<option value={selected}>{String(selectedItem.name||current.key)}</option>}
      {[...new Set(items.map(item=>item.set??""))].map(group=><optgroup key={group} label={group||"묶음 미기록"}>{items.filter(item=>(item.set??"")===group).map(item=><option key={identity(item)} value={identity(item)}>{item.name||item.key}</option>)}</optgroup>)}
    </AuthorSelect></div>
    <div className="flex gap-2 sm:col-span-2"><Button type="button" variant="outline" disabled={offsets.length===1||query.isFetching} onClick={()=>setOffsets(offsets.slice(0,-1))}>이전</Button><Button type="button" variant="outline" disabled={next===undefined||query.isFetching} onClick={()=>{if(next!==undefined)setOffsets([...offsets,next])}}>다음</Button></div>
    <div className="min-w-0 sm:col-span-2"><QueryGate query={query}>{null}</QueryGate>{query.data&&<table className="w-full text-left text-sm tabular-nums"><tbody><tr><th className="font-normal">전체 입력</th><td className="text-right">{query.data.page.total}</td></tr><tr><th className="font-normal">현재 입력</th><td className="text-right">{items.length}</td></tr></tbody></table>}{selected&&<><table className="w-full text-left text-sm tabular-nums"><tbody><tr><th className="font-normal">선택 입력 크기 (B)</th><td className="text-right">{String(current.size??"미기록")}</td></tr></tbody></table><details className="min-w-0"><summary className="min-h-11 cursor-pointer text-sm">선택 입력 원문</summary><pre className="max-w-full overflow-x-auto whitespace-pre-wrap break-all text-xs">{specJSON(current)}</pre></details></>}</div>
  </div>
}
export function AuthorHelp() {
  return <TooltipProvider><Tooltip><TooltipTrigger asChild><Button type="button" variant="ghost">편집 도움말</Button></TooltipTrigger><TooltipContent className="max-w-sm"><table className="w-full text-left text-xs"><caption className="sr-only">작성 제약</caption><tbody>{[["점검 기준","서버 사전점검"],["게시 복사","새 이름"],["복사에서 제외됨","옛 계약에서 삭제된 필드 경로만 표시·제외해요. stages[0]은 첫 단계예요. 원본 실행·게시·부하는 변경하지 않아요. 누락된 현재 필수 입력은 직접 채운 뒤 재점검해요."],["수정 식별자","같은 이름 · 새 UID"],["사용 중 부하","수정·삭제 제한"],["현재 식별자 반영","선택 사항 · 반영 후 재점검"],["원문 편집","기존 필드·정밀 정수 보존"],["운영 설정 패치","운영 설정과 같은 항목·단위예요. 시간 항목은 ms로 입력하고 원문 JSON에는 ns로 저장해요."],["단계 패치","미설정·null은 전체 실험 설정 상속, 빈 객체 {}도 단계 패치로 전체 대체해요. 항목을 모두 제거해도 빈 대체 패치는 유지돼요. 상속하려면 패치 미설정을 선택해요."],["패치 원문","JSON 객체를 그대로 전달하며 서버 사전점검으로 검증해요."],["미리보기 폭","구간 길이"],["미리보기 높이","절대 요청률 (req/s)"]].map(([label,value])=><tr key={label}><th className="p-1 font-normal">{label}</th><td className="p-1">{value}</td></tr>)}</tbody></table></TooltipContent></Tooltip></TooltipProvider>
}
export function HistoricalComparison({spec,path,onChange}:{spec:Spec;path:(string|number)[];onChange:(spec:Spec)=>void}) {
  const ref=object(specValue(spec,path)),publicationID=String(ref.publicationID??""),stageID=String(ref.stageID??"")
  const {config}=useSession(),[offsets,setOffsets]=useState([0])
  const page=useChillQuery<ExperimentPage>(`v1/experiments?${new URLSearchParams({offset:String(offsets.at(-1)??0),limit:String(Math.min(5,config.maxPageSize))})}`,{decoder:decodeExperimentPage})
  const selected=useChillQuery<{stages?:{id:string}[]}>(`v1/experiments/${encodeURIComponent(publicationID||"_")}`,{enabled:!!publicationID,decoder:parseSchedulerJSON})
  const stages=selected.data?.stages??[],next=page.data?.page?.nextOffset
  return <div className="grid min-w-0 gap-2">
    <AuthorSelect label="과거 게시" value={publicationID} onChange={value=>onChange(patchSpec(spec,path,{...ref,publicationID:value,stageID:""}))}><option value="">미선택</option>{publicationID&&!page.data?.items.some(item=>item.id===publicationID)&&<option value={publicationID}>{publicationID} · 저장된 참조</option>}{page.data?.items.map(item=><option key={item.id} value={item.id}>{item.id}</option>)}</AuthorSelect>
    <nav aria-label="과거 게시 페이지" className="flex gap-2"><Button type="button" variant="outline" disabled={offsets.length===1||page.isFetching} onClick={()=>setOffsets(offsets.slice(0,-1))}>이전</Button><Button type="button" variant="outline" disabled={next===undefined||page.isFetching} onClick={()=>{if(next!==undefined)setOffsets([...offsets,next])}}>다음</Button></nav>
    <QueryGate query={page}>{null}</QueryGate>
    {publicationID&&<>{selected.error?.status===404?<span role="status">대상 게시 없음</span>:<QueryGate query={selected}>{null}</QueryGate>}<AuthorSelect label="과거 단계" value={stageID} onChange={value=>onChange(patchSpec(spec,[...path,"stageID"],value))}><option value="">미선택</option>{stageID&&!stages.some(stage=>stage.id===stageID)&&<option value={stageID}>{stageID} · 저장된 참조</option>}{stages.map(stage=><option key={stage.id} value={stage.id}>{stage.id}</option>)}</AuthorSelect></>}
  </div>
}
export function SettingsPatchEditor({spec,path,onChange,onValidityChange}:{spec:Spec;path:(string|number)[];onChange:(spec:Spec)=>void;onValidityChange:(valid:boolean)=>void}) {
  const raw=specValue(spec,path),patch=object(raw),stage=path[0]==="stages",[key,setKey]=useState(""),[text,setText]=useState(""),[error,setError]=useState(""),[pending,setPending]=useState(false),[rawInvalid,setRawInvalid]=useState(false)
  const field=settingFields.find(field=>field.key===key),nanos=!!field?.key.endsWith("Nanos"),unit=nanos?"ms":key==="servingMemoryBytes"?"bytes":field?.unit
  const labels=new Map<string,string>([...settingFields,...settingTargets].map(field=>[field.key,field.label]))
  function replace(value:Spec|undefined) {
    onChange(patchSpec(spec,path,value));setKey("");setText("");setPending(false);setError("");onValidityChange(true)
  }
  return <fieldset className="grid min-w-0 gap-3 rounded-md border p-3"><legend className="text-sm">운영 설정 패치</legend>
    <span role="status" className="text-sm">{stage?(raw==null?"전체 실험 설정 상속":"단계 패치로 전체 대체"):raw==null?"전체 실험 패치 미설정":"전체 실험 패치 설정"}</span>
    <AuthorSelect label="설정 항목" value={key} disabled={pending||rawInvalid} onChange={value=>{setKey(value);const current=patch[value];setText(Object.hasOwn(patch,value)?value.endsWith("Nanos")&&(typeof current==="number"||typeof current==="bigint")?nanosToMilliseconds(current):specJSON(current):"")}}><option value="">항목 선택</option>{editableSettingKeys.map(value=><option key={value} value={value}>{labels.get(value)??(value==="controls"?"제어 항목":value)}</option>)}</AuthorSelect>
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
function ComparisonSide({label,spec,path,onChange,stages}:{label:string;spec:Spec;path:(string|number)[];onChange:(spec:Spec)=>void;stages:Spec[]}) {
  const ref=object(specValue(spec,path)),historical=ref.historical!==undefined
  return <fieldset className="grid gap-2"><legend className="text-sm font-medium">{label}</legend>
    <AuthorSelect label="참조" value={historical?"historical":"stage"} onChange={kind=>{const next={...ref};delete next.historical;delete next.stageID;onChange(patchSpec(spec,path,kind==="historical"?{...next,historical:{}}:{...next,stageID:""}))}}><option value="stage">같은 실험 단계</option><option value="historical">과거 게시</option></AuthorSelect>
    {historical?<HistoricalComparison spec={spec} path={[...path,"historical"]} onChange={onChange}/>:<AuthorSelect label="단계" value={String(ref.stageID??"")} onChange={value=>onChange(patchSpec(spec,[...path,"stageID"],value))}><option value="">미선택</option>{ref.stageID&&!stages.some(s=>s.id===ref.stageID)?<option value={String(ref.stageID)}>{String(ref.stageID)} · 대상 없음</option>:null}{stages.map((s,i)=><option key={i} value={String(s.id??"")}>{String(s.id??"")}</option>)}</AuthorSelect>}
  </fieldset>
}
export function EvaluationSpecEditor({spec,onChange,onValidityChange}:{spec:Spec;onChange:(spec:Spec)=>void;onValidityChange?:(valid:boolean)=>void}) {
  const loads=useChillQuery<{items:LoadScenario[]}>("v1/evaluations/load-scenarios",{decoder:parseSchedulerJSON}),stages=records(spec.stages),comparisons=records(spec.comparisons)
  const [tab,setTab]=useState("stages"),[invalid,setInvalid]=useState<string[]>([])
  const [selectedStage,setSelectedStage]=useState(0),[newLoad,setNewLoad]=useState<{stage:number;session:symbol}|null>(null)
  const [stageRevision,setStageRevision]=useState(0)
  const activeLoad=useRef<symbol|null>(null),draft=useRef({spec,onChange})
  useEffect(()=>{draft.current={spec,onChange}},[spec,onChange])
  function reportValidity(key:string,valid:boolean) {
    const next=valid?invalid.filter(item=>item!==key):[...new Set([...invalid,key])]
    setInvalid(next);onValidityChange?.(next.length===0)
  }
  return <div className="grid min-w-0 gap-4">
    <div className="flex flex-wrap gap-2" role="group" aria-label="편집 항목">{[["stages","단계"],["comparisons","비교"],["criteria","측정 기준"],["json","원문"]].map(([key,label])=><Button type="button" key={key} disabled={invalid.length>0&&key!==tab} variant={tab===key?"default":"outline"} aria-pressed={tab===key} onClick={()=>setTab(key)}>{label}</Button>)}</div>
    {tab==="json"?<AuthorJSON label="실험 정의 원문" value={spec} onChange={onChange} onValidityChange={valid=>reportValidity("spec",valid)}/>:<>
      {tab==="stages"&&<div className="grid gap-4"><QueryGate query={loads}>{null}</QueryGate>{stages.map((stage,index)=>{const p:(string|number)[]=["stages",index],ref=object(stage.loadScenarioRef),selected=index===Math.min(selectedStage,stages.length-1);return <section key={index} className="rounded-md border p-3"><Button type="button" variant="ghost" className="min-h-11 w-full justify-start whitespace-normal text-left" aria-expanded={selected} disabled={invalid.length>0&&!selected} onClick={()=>setSelectedStage(index)}>{String(stage.id||`단계 ${index+1}`)} · {String(ref.name??"부하 미선택")}</Button>{selected&&<div className="mt-3 grid gap-4">
        <div className="grid gap-3 sm:grid-cols-2"><AuthorField label="단계 ID" spec={spec} path={[...p,"id"]} onChange={onChange}/><AuthorField label="행 이름" spec={spec} path={[...p,"row"]} onChange={onChange}/></div>
        <AuthorSelect label="부하 설정" value={String(ref.uid??"")} onChange={uid=>{const selected=loads.data?.items.find(x=>x.uid===uid);if(selected)onChange(patchSpec(spec,[...p,"loadScenarioRef"],{...ref,name:selected.name,uid:selected.uid}))}}><option value="">미선택</option>{ref.uid&&!loads.data?.items.some(x=>x.uid===ref.uid)?<option value={String(ref.uid)}>{String(ref.name)} · 저장된 참조</option>:null}{loads.data?.items.map(item=><option key={item.uid} value={item.uid}>{item.name}</option>)}</AuthorSelect>
        <Button type="button" variant="outline" disabled={invalid.length>0} onClick={()=>{const session=Symbol();activeLoad.current=session;setNewLoad({stage:index,session})}}>새 부하</Button>
        <SettingsPatchEditor key={`${stageRevision}-${index}`} spec={spec} path={[...p,"settingsPatch"]} onChange={onChange} onValidityChange={valid=>reportValidity(`patch-${index}`,valid)}/>
        <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={invalid.length>0||index===0} onClick={()=>{const next=[...stages];[next[index-1],next[index]]=[next[index],next[index-1]];setSelectedStage(index-1);setStageRevision(v=>v+1);onChange(patchSpec(spec,["stages"],next))}}>앞으로 이동</Button><Button type="button" variant="outline" disabled={invalid.length>0} onClick={()=>{setStageRevision(v=>v+1);onChange(patchSpec(spec,["stages"],stages.filter((_,i)=>i!==index)))}}>단계 삭제</Button></div>
      </div>}</section>})}<Button type="button" variant="outline" disabled={invalid.length>0} onClick={()=>{setSelectedStage(stages.length);onChange(patchSpec(spec,["stages"],[...stages,{id:"",row:""}]))}}>단계 추가</Button></div>}
      {tab==="comparisons"&&<div className="grid gap-4">{comparisons.map((comparison,index)=><fieldset key={index} className="grid gap-3 rounded-md border p-3"><legend className="text-sm">비교 {index+1}</legend><div className="grid gap-4 sm:grid-cols-2"><ComparisonSide label="후보" spec={spec} path={["comparisons",index,"left"]} stages={stages} onChange={onChange}/><ComparisonSide label="기준" spec={spec} path={["comparisons",index,"right"]} stages={stages} onChange={onChange}/></div><AuthorField label="비교 사유" spec={spec} path={["comparisons",index,"reason"]} onChange={onChange}/><Button type="button" variant="outline" onClick={()=>onChange(patchSpec(spec,["comparisons"],comparisons.filter((_,i)=>i!==index)))}>비교 삭제</Button></fieldset>)}<Button type="button" variant="outline" onClick={()=>onChange(patchSpec(spec,["comparisons"],[...comparisons,{left:{stageID:""},right:{stageID:""},reason:""}]))}>비교 추가</Button></div>}
      {tab==="criteria"&&<div className="grid gap-4"><AuthorField label="부하 발생 장비" spec={spec} path={["loadGenerator","nodeName"]} onChange={onChange}/><SettingsPatchEditor spec={spec} path={["settingsPatch"]} onChange={onChange} onValidityChange={valid=>reportValidity("patch-global",valid)}/><table className="w-full text-left text-sm"><caption className="sr-only">측정 기준</caption><thead><tr><th>항목</th><th>단위</th><th>값</th></tr></thead><tbody>{[["generatorQuality","latenessToleranceNanos","발행 지연 허용","ns"],["generatorQuality","maxExceedingFraction","지연 초과 비율","비율"],["energyCoverage","maxGapFraction","전력 결손 비율","비율"],["energyCoverage","maxSingleGapSeconds","단일 전력 공백","s"]].map(([section,key,label,unit])=><tr key={key}><th className="font-normal">{label}</th><td>{unit}</td><td className="py-1"><AuthorField label={label} compact spec={spec} path={["judgment",section,key]} numeric onChange={onChange}/></td></tr>)}</tbody></table></div>}
    </>}
    <Sheet open={newLoad!==null} onOpenChange={open=>{if(!open){activeLoad.current=null;setNewLoad(null)}}}><SheetContent className="w-full overflow-y-auto sm:max-w-3xl" aria-describedby={undefined}><SheetHeader><SheetTitle>새 부하</SheetTitle></SheetHeader>{newLoad!==null&&<div className="p-4"><LoadEditor editing={false} onSaved={()=>loads.refetch()} onConfirmed={value=>{if(activeLoad.current!==newLoad.session)return;const path=["stages",newLoad.stage,"loadScenarioRef"],current=draft.current;current.onChange(patchSpec(current.spec,path,{...object(specValue(current.spec,path)),...value}));activeLoad.current=null;setNewLoad(null)}}/></div>}</SheetContent></Sheet>
  </div>
}
export function EvaluationAuthor({initialSpec,initialName,existing,onSubmitted}:{initialSpec?:Spec;initialName?:string;existing?:{name:string;uid:string};onSubmitted?:(value:{name:string;uid:string})=>void}) {
  const [draft]=useState(()=>initialSpec?existing?{spec:structuredClone(initialSpec),removedFields:[] as string[]}:copyAuthorDraft({spec:initialSpec},"evaluation"):{spec:{},removedFields:[] as string[]})
  const [spec,setSpec]=useState<Spec>(draft.spec),[removedFields,setRemovedFields]=useState(draft.removedFields),[name,setName]=useState(existing?.name??""),[origin,setOrigin]=useState(initialName??""),[ready,setReady]=useState(!!initialSpec),[valid,setValid]=useState(true),[source,setSource]=useState(""),[sources,setSources]=useState<Spec>({}),[version,setVersion]=useState(0)
  const [sourcePublication,setSourcePublication]=useState<{uid:string;name:string}>()
  const [baseline]=useState(()=>specJSON({name:existing?.name??"",spec:draft.spec}))
  useUnsavedAuthor(!valid||ready&&specJSON({name,spec})!==baseline)
  const {config}=useSession(),[publicationOffsets,setPublicationOffsets]=useState([0])
  const publications=useChillQuery<ExperimentPage>(`v1/experiments?${new URLSearchParams({offset:String(publicationOffsets.at(-1)??0),limit:String(Math.min(5,config.maxPageSize))})}`,{enabled:!initialSpec,decoder:decodeExperimentPage}),publication=useChillQuery<{definition?:{run?:{name?:string};spec?:Spec}}>(`v1/experiments/${encodeURIComponent(source)}`,{enabled:!!source,decoder:parseSchedulerJSON})
  const template=useChillQuery<{spec:Spec;sources:Spec;sourcePublication?:{uid:string;name:string}}>("v1/evaluations/template",{enabled:false,decoder:parseSchedulerJSON})
  async function loadTemplate() {
    const result=await template.refetch()
    if(!result.error&&result.data){setSpec(structuredClone(result.data.spec));setRemovedFields([]);setSources(result.data.sources);setSourcePublication(result.data.sourcePublication);setOrigin("");setReady(true);setValid(true);setVersion(v=>v+1)}
  }
  const labels:Record<string,string>={question:"질문",target:"대상 구성요소",harness:"실행기",loadGenerator:"부하 발생기",powerMeasurement:"전력 측정",settingsPatch:"운영 설정 패치",judgment:"판정 기준",evidenceStore:"증거 저장소",stages:"단계",comparisons:"비교"}
  return <div className="grid min-w-0 gap-5">
    {!ready&&<EvaluationPermission/>}
    <div className="flex justify-end"><AuthorHelp/></div>
    {!initialSpec&&<details open={!ready} className="grid gap-3"><summary className="min-h-11 cursor-pointer text-sm">정의 출처 · {origin||sourcePublication?.name||(ready?"빈 템플릿":"미선택")}</summary><section className="grid gap-3" aria-label="정의 출처">
      <AuthorSelect label="게시에서 복사" value={source} onChange={setSource}><option value="">게시 선택</option>{source&&!publications.data?.items.some(item=>item.id===source)&&<option value={source}>{source}</option>}{publications.data?.items.map(item=><option key={item.id} value={item.id}>{item.id}</option>)}</AuthorSelect>
      {publications.data?.page&&<nav aria-label="복사할 게시 페이지" className="flex gap-2"><Button type="button" variant="outline" disabled={publicationOffsets.length===1||publications.isFetching} onClick={()=>setPublicationOffsets(publicationOffsets.slice(0,-1))}>이전</Button><Button type="button" variant="outline" disabled={publications.data.page.nextOffset===undefined||publications.isFetching} onClick={()=>{const next=publications.data?.page?.nextOffset;if(next!==undefined)setPublicationOffsets([...publicationOffsets,next])}}>다음</Button></nav>}
      {publications.data&&<table aria-label="복사할 게시 수" className="w-full text-sm tabular-nums"><tbody><tr><th className="text-left font-normal">전체 게시 (개)</th><td className="text-right">{publications.data.page?.total??publications.data.items.length}</td></tr><tr><th className="text-left font-normal">현재 게시 (개)</th><td className="text-right">{publications.data.items.length}</td></tr></tbody></table>}
      <QueryGate query={publications}>{null}</QueryGate>{source&&<QueryGate query={publication}>{null}</QueryGate>}{(template.isFetching||template.error)&&<QueryGate query={template}>{null}</QueryGate>}
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!publication.data?.definition?.spec} onClick={()=>{const definition=publication.data?.definition;if(definition?.spec){const draft=copyAuthorDraft({spec:definition.spec},"evaluation");setSpec(draft.spec);setRemovedFields(draft.removedFields);setSources({});setSourcePublication(undefined);setName("");setOrigin(definition.run?.name??"");setReady(true);setValid(true);setVersion(v=>v+1)}}}>게시 복사</Button>
        <Button type="button" variant="outline" disabled={template.isFetching} onClick={()=>void loadTemplate()}>빈 템플릿</Button>{publication.data&&!publication.data.definition?.spec&&<span role="status" className="text-sm">정의 미기록</span>}</div>
    </section></details>}
    {ready&&<><section className="grid gap-4" aria-label="실험 정의">
      {removedFields.length>0&&<section aria-label="복사에서 제외된 필드" className="grid gap-1 text-sm"><h3>복사에서 제외됨</h3><ul className="list-inside list-disc break-all">{removedFields.map(path=><li key={path}>{path}</li>)}</ul></section>}
      {sourcePublication&&<dl className="grid gap-1 text-sm"><dt>출처 게시 이름</dt><dd>{sourcePublication.name||"미제공"}</dd><dt>출처 게시 식별자</dt><dd className="break-all">{sourcePublication.uid}</dd></dl>}
      {Object.values(sources).some(source=>source==="none")&&<table aria-label="입력 필요 항목" className="w-full text-left text-sm"><thead><tr><th>입력 필요</th><th>상태</th></tr></thead><tbody>{Object.entries(sources).filter(([,source])=>source==="none").map(([field])=><tr key={field}><th className="py-1 font-normal">{labels[field]??"기타 입력"}</th><td>출처 미제공</td></tr>)}</tbody></table>}
      {Object.keys(sources).length>0&&<details><summary className="cursor-pointer text-sm">입력 출처</summary><table className="w-full text-left text-sm"><thead><tr><th>필드</th><th>출처</th><th>상태</th></tr></thead><tbody>{Object.entries(sources).map(([field,source])=><tr key={field} className={source==="none"?"bg-destructive/10":""}><th className="py-2">{labels[field]??field}</th><td>{{live:"현재 구성",config:"운영 설정",latestPublication:"최근 게시",none:"없음"}[String(source)]??"원문 확인"}</td><td>{source==="none"?"입력 필요":"확인됨"}</td></tr>)}</tbody></table></details>}
      <label className="grid gap-1 text-sm">실험 이름<Input value={name} disabled={!!existing} onChange={event=>setName(event.target.value)}/></label>{!existing&&origin&&name===origin&&<span role="alert" className="text-sm text-destructive">새 이름 필요</span>}
      {origin?<details><summary className="min-h-11 cursor-pointer text-sm">질문 원문</summary><AuthorField label="질문 · 저장된 원문" spec={spec} path={["question"]} onChange={setSpec}/></details>:<AuthorField label="질문" spec={spec} path={["question"]} onChange={setSpec}/>}<EvaluationSpecEditor key={version} spec={spec} onChange={setSpec} onValidityChange={setValid}/>
    </section><EvaluationSubmit name={name} spec={spec} existing={existing} disabled={!valid||!name.trim()||(!existing&&name===origin)} onSubmitted={onSubmitted}/></>}
  </div>
}

export function EvaluationAuthorPage({name}:{name?:string}) {
  const params=useSearchParams(),router=useRouter(),copy=params.get("copy")??"",publication=params.get("publication")??"",source=name??copy
  const run=useChillQuery<{name:string;uid:string;phase:string;spec:Spec}>(`v1/evaluations/${encodeURIComponent(source||"_")}`,{enabled:!!source,decoder:parseSchedulerJSON})
  const result=useChillQuery<{definition?:{run:{name:string};spec:Spec}}>(`v1/experiments/${encodeURIComponent(publication||"_")}`,{enabled:!source&&!!publication,decoder:parseSchedulerJSON})
  const identityChanged=!!name&&!!params.get("uid")&&run.data?.uid!==params.get("uid")
  const submitted=(value:{name:string;uid:string})=>router.replace(`/experiments/runs/${encodeURIComponent(value.name)}?${new URLSearchParams({uid:value.uid})}`)
  return <Screen title={name?"실험 수정":"새 실험"} width="data" back={["/experiments","실험"]}>
    {source?<QueryGate query={run}>{run.data&&(name&&(run.data.phase!=="Pending"||identityChanged)?<strong role="alert">{identityChanged?"실험 교체 확인":"시작 전 수정만 허용"}</strong>:<EvaluationAuthor onSubmitted={submitted} key={run.data.uid} initialSpec={run.data.spec} initialName={run.data.name} existing={name?{name:run.data.name,uid:run.data.uid}:undefined}/>)}</QueryGate>:publication?<QueryGate query={result}>{result.data&&(result.data.definition?<EvaluationAuthor onSubmitted={submitted} key={publication} initialSpec={result.data.definition.spec} initialName={result.data.definition.run.name}/>:<div className="flex flex-wrap items-center gap-3"><strong role="status">정의 미기록</strong><Button variant="outline" asChild><Link href="/experiments/new">다른 출처 선택</Link></Button></div>)}</QueryGate>:<EvaluationAuthor onSubmitted={submitted}/>}
  </Screen>
}

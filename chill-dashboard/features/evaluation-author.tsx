"use client"

import { useEffect, useId, useState, type ReactNode } from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { Screen } from "@/components/screen"
import { useChillQuery, useSession } from "@/lib/client"
import { QueryGate } from "@/components/read-region"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { EvaluationPermission, EvaluationSubmit } from "@/features/evaluation-request"
import { object, records, specValue, patchSpec, parseSpec, specJSON, copyAuthorDraft, type Spec, type LoadScenario, type EvidenceInput } from "@/lib/evaluation-author"
import { parseSchedulerJSON } from "@/lib/scheduler"


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
  const defaultKind=label.includes("난수")?"seed":label.includes("번들")?"payload":label.includes("요청률")?"rateBasis":label.includes("분배")?"kappa":label.includes("요청 시각")?"other":""
  const [kind,setKind]=useState(defaultKind),[set,setSet]=useState(""),[offsets,setOffsets]=useState([0]),[chosen,setChosen]=useState<EvidenceInput|null>(null),setList=useId()
  const {config}=useSession()
  const params=new URLSearchParams({limit:String(Math.min(25,config.maxPageSize)),offset:String(offsets.at(-1)??0)})
  if(kind)params.set("kind",kind)
  if(set)params.set("set",set)
  const query=useChillQuery<{items:EvidenceInput[];page:{offset:number;limit:number;total:number;nextOffset?:number}}>(`v1/evaluations/inputs?${params}`,{decoder:parseSchedulerJSON})
  const items=query.data?.items??[],current=object(value)
  const identity=(item:Spec)=>JSON.stringify([item.store,item.key])
  const selected=current.key?identity(current):"",next=query.data?.page.nextOffset
  const selectedItem=items.find(item=>identity(item)===selected)??(chosen&&identity(chosen)===selected?chosen:current)
  return <div className="grid min-w-0 gap-2 sm:grid-cols-2">
    <AuthorSelect label="입력 종류" value={kind} onChange={value=>{setKind(value);setOffsets([0])}}><option value="">전체</option>{Object.entries({seed:"난수",payload:"입력 번들",rateBasis:"요청률 기준",kappa:"분배 계수",other:"기타"}).map(([key,name])=><option key={key} value={key}>{name}</option>)}</AuthorSelect>
    <label className="grid min-w-0 gap-1 text-sm">입력 묶음<Input list={setList} value={set} onChange={event=>{setSet(event.target.value);setOffsets([0])}}/><datalist id={setList}>{[...new Set(items.map(item=>item.set).filter(Boolean))].map(value=><option key={value} value={value}/>)}</datalist></label>
    <div className="min-w-0 sm:col-span-2"><AuthorSelect label={label} value={selected} onChange={key=>{const item=items.find(item=>identity(item)===key);setChosen(item??null);onChange(item?{...current,store:item.store,key:item.key,size:item.size}:undefined)}}>
      <option value="">미선택</option>{selected&&!items.some(item=>identity(item)===selected)&&<option value={selected}>{String(selectedItem.name||current.key)}</option>}
      {[...new Set(items.map(item=>item.set??""))].map(group=><optgroup key={group} label={group||"묶음 미기록"}>{items.filter(item=>(item.set??"")===group).map(item=><option key={identity(item)} value={identity(item)}>{item.name||item.key}</option>)}</optgroup>)}
    </AuthorSelect></div>
    <div className="flex gap-2 sm:col-span-2"><Button type="button" variant="outline" disabled={offsets.length===1||query.isFetching} onClick={()=>setOffsets(offsets.slice(0,-1))}>이전</Button><Button type="button" variant="outline" disabled={next===undefined||query.isFetching} onClick={()=>{if(next!==undefined)setOffsets([...offsets,next])}}>다음</Button></div>
    <div className="min-w-0 sm:col-span-2"><QueryGate query={query}>{null}</QueryGate>{query.data&&<table className="w-full text-left text-sm tabular-nums"><tbody><tr><th className="font-normal">전체 입력</th><td className="text-right">{query.data.page.total}</td></tr><tr><th className="font-normal">현재 입력</th><td className="text-right">{items.length}</td></tr></tbody></table>}{selected&&<><table className="w-full text-left text-sm tabular-nums"><tbody><tr><th className="font-normal">선택 입력 크기 (B)</th><td className="text-right">{String(current.size??"미기록")}</td></tr></tbody></table><details className="min-w-0"><summary className="min-h-11 cursor-pointer text-sm">선택 입력 원문</summary><pre className="max-w-full overflow-x-auto whitespace-pre-wrap break-all text-xs">{specJSON(current)}</pre></details></>}</div>
  </div>
}
export function AuthorHelp() {
  return <TooltipProvider><Tooltip><TooltipTrigger asChild><Button type="button" variant="ghost">편집 도움말</Button></TooltipTrigger><TooltipContent className="max-w-sm"><table className="w-full text-left text-xs"><caption className="sr-only">작성 제약</caption><tbody>{[["점검 기준","서버 사전점검"],["게시 복사","새 이름"],["수정 식별자","같은 이름 · 새 UID"],["사용 중 부하","수정·삭제 제한"],["현재 식별자 반영","선택 사항 · 반영 후 재점검"],["원문 편집","확장 필드·정밀 정수 보존"],["미리보기 폭","구간 길이"],["미리보기 높이","요청률 또는 α"]].map(([label,value])=><tr key={label}><th className="p-1 font-normal">{label}</th><td className="p-1">{value}</td></tr>)}</tbody></table></TooltipContent></Tooltip></TooltipProvider>
}
const switches=[["placement","배치 위치"],["resources","자원"],["replicas","복제 수"],["routing","라우팅"],["batching","배치 크기"],["power","전력"],["membership","풀 구성"]] as const
function ControlFields({spec,path,onChange,onValidityChange}:{spec:Spec;path:(string|number)[];onChange:(spec:Spec)=>void;onValidityChange?:(valid:boolean)=>void}) {
  const registered=useChillQuery<{desired:{metadata:{name:string;uid:string}}}[]>("v1/workloads?view=summary"),services=registered.data?.map(item=>item.desired.metadata)??[],demand=object(specValue(spec,[...path,"minimumDemand"]))
  return <div className="grid gap-3"><table className="w-full text-left text-sm"><thead><tr><th>제어 기준</th><th>비율</th></tr></thead><tbody><tr><th className="font-normal">축소 여유</th><td><AuthorField label="축소 여유 비율" compact spec={spec} path={[...path,"scaleDownHeadroomFraction"]} numeric onChange={onChange}/></td></tr></tbody></table>
    <div className="flex flex-wrap gap-4">{switches.map(([key,label])=><label key={key} className="flex min-h-11 items-center gap-2 text-sm"><input type="checkbox" checked={specValue(spec,[...path,"controls",key])===true} onChange={event=>onChange(patchSpec(spec,[...path,"controls",key],event.target.checked))}/>{label}</label>)}</div>
    <div className="grid min-w-0 gap-3 sm:grid-cols-2">{Object.keys(demand).map((uid,index)=><AuthorField key={uid} label={`${services.find(item=>item.uid===uid)?.name??`저장된 서비스 ${index+1}`} 최소 요청/s`} spec={spec} path={[...path,"minimumDemand",uid]} numeric onChange={onChange}/>)}</div>
    <AuthorSelect label="최소 수요 서비스 추가" value="" onChange={uid=>{if(uid)onChange(patchSpec(spec,[...path,"minimumDemand",uid],0))}}><option value="">서비스 선택</option>{services.filter(item=>!Object.hasOwn(demand,item.uid)).map(item=><option key={item.uid} value={item.uid}>{item.name}</option>)}</AuthorSelect>
    <details><summary className="cursor-pointer text-sm">최소 수요 원문</summary><AuthorJSON label="최소 수요 원문" value={demand} onChange={value=>onChange(patchSpec(spec,[...path,"minimumDemand"],value))} onValidityChange={onValidityChange}/></details>
  </div>
}
function InjectionEditor({spec,path,onChange}:{spec:Spec;path:(string|number)[];onChange:(spec:Spec)=>void}) {
  const plans=records(specValue(spec,path))
  const registered=useChillQuery<{desired:{metadata:{name:string;uid:string}}}[]>("v1/workloads?view=summary"),services=registered.data?.map(item=>item.desired.metadata)??[]
  return <div className="grid gap-3">{plans.map((plan,index)=>{const p=[...path,index];return <fieldset key={index} className="grid gap-3 rounded-md border p-3"><legend className="px-1 text-sm">장애 주입 {index+1}</legend>
    <div className="grid gap-3 sm:grid-cols-3"><AuthorField label="주입 ID" spec={spec} path={[...p,"id"]} onChange={onChange}/><AuthorField label="시작 오프셋 (s)" spec={spec} path={[...p,"offsetSeconds"]} numeric onChange={onChange}/>
    <AuthorSelect label="동작" value={String(plan.action??"")} onChange={action=>{let next=patchSpec(spec,[...p,"action"],action);next=patchSpec(next,[...p,action==="uncordon"?"selection":"priorInjectionID"],undefined);onChange(next)}}><option value="">미선택</option><option value="delete-serving-pod">서비스 Pod 삭제</option><option value="cordon-delete">노드 차단·Pod 삭제</option><option value="uncordon">노드 차단 해제</option></AuthorSelect></div>
    {plan.action==="uncordon"?<AuthorField label="앞선 차단 주입 ID" spec={spec} path={[...p,"priorInjectionID"]} onChange={onChange}/>:<div className="grid gap-3 sm:grid-cols-3">
      <fieldset className="grid gap-2"><legend className="text-sm">대상 서비스</legend>{[...new Set([...services.map(item=>item.uid),...(Array.isArray(object(plan.selection).workloadUIDs)?object(plan.selection).workloadUIDs as string[]:[])])].map((uid,i)=><label key={uid} className="flex min-h-11 items-center gap-2 text-sm"><input type="checkbox" checked={Array.isArray(object(plan.selection).workloadUIDs)&&(object(plan.selection).workloadUIDs as string[]).includes(uid)} onChange={event=>{const selected=Array.isArray(object(plan.selection).workloadUIDs)?object(plan.selection).workloadUIDs as string[]:[];onChange(patchSpec(spec,[...p,"selection","workloadUIDs"],event.target.checked?[...selected,uid]:selected.filter(value=>value!==uid)))}}/>{services.find(item=>item.uid===uid)?.name??`저장된 서비스 ${i+1}`}</label>)}</fieldset>
      <AuthorField label="다른 노드 · 앞선 주입 ID" spec={spec} path={[...p,"selection","differentNodeFrom"]} onChange={onChange}/>
      <fieldset className="grid gap-2"><legend className="text-sm">선택 순서</legend>{[0,1,2].map(i=><AuthorSelect key={i} label={`우선순위 ${i+1}`} value={String((Array.isArray(object(plan.selection).orderBy)?object(plan.selection).orderBy as string[]:[])[i]??"")} onChange={value=>{const order=Array.isArray(object(plan.selection).orderBy)?[...object(plan.selection).orderBy as string[]]:[];order[i]=value;onChange(patchSpec(spec,[...p,"selection","orderBy"],order.filter(Boolean)))}}><option value="">미선택</option><option value="node">장치</option><option value="pod">파드</option><option value="instance">인스턴스</option></AuthorSelect>)}</fieldset></div>}
    <Button type="button" variant="outline" onClick={()=>onChange(patchSpec(spec,path,plans.filter((_,i)=>i!==index)))}>주입 삭제</Button>
  </fieldset>})}<Button type="button" variant="outline" onClick={()=>onChange(patchSpec(spec,path,[...plans,{id:""}]))}>장애 주입 추가</Button></div>
}
function ComparisonSide({label,spec,path,onChange,stages}:{label:string;spec:Spec;path:(string|number)[];onChange:(spec:Spec)=>void;stages:Spec[]}) {
  const ref=object(specValue(spec,path)),historical=ref.historical!==undefined
  return <fieldset className="grid gap-2"><legend className="text-sm font-medium">{label}</legend>
    <AuthorSelect label="참조" value={historical?"historical":"stage"} onChange={kind=>{const next={...ref};delete next.historical;delete next.stageID;onChange(patchSpec(spec,path,kind==="historical"?{...next,historical:{}}:{...next,stageID:""}))}}><option value="stage">같은 실험 단계</option><option value="historical">과거 게시</option></AuthorSelect>
    {historical?<><AuthorField label="게시 ID" spec={spec} path={[...path,"historical","publicationID"]} onChange={onChange}/><AuthorField label="단계 ID" spec={spec} path={[...path,"historical","stageID"]} onChange={onChange}/></>:<AuthorSelect label="단계" value={String(ref.stageID??"")} onChange={value=>onChange(patchSpec(spec,[...path,"stageID"],value))}><option value="">미선택</option>{ref.stageID&&!stages.some(s=>s.id===ref.stageID)?<option value={String(ref.stageID)}>{String(ref.stageID)} · 대상 없음</option>:null}{stages.map((s,i)=><option key={i} value={String(s.id??"")}>{String(s.id??"")}</option>)}</AuthorSelect>}
  </fieldset>
}
export function EvaluationSpecEditor({spec,onChange,onValidityChange}:{spec:Spec;onChange:(spec:Spec)=>void;onValidityChange?:(valid:boolean)=>void}) {
  const loads=useChillQuery<{items:LoadScenario[]}>("v1/evaluations/load-scenarios",{decoder:parseSchedulerJSON}),stages=records(spec.stages),comparisons=records(spec.comparisons)
  const [tab,setTab]=useState("stages"),[invalid,setInvalid]=useState<string[]>([])
  function reportValidity(key:string,valid:boolean) {
    const next=valid?invalid.filter(item=>item!==key):[...new Set([...invalid,key])]
    setInvalid(next);onValidityChange?.(next.length===0)
  }
  return <div className="grid min-w-0 gap-4">
    <div className="flex flex-wrap gap-2" role="group" aria-label="편집 항목">{[["stages","단계"],["comparisons","비교"],["criteria","판정·제어"],["context","실행 환경"],["json","원문"]].map(([key,label])=><Button type="button" key={key} disabled={invalid.length>0&&key!==tab} variant={tab===key?"default":"outline"} aria-pressed={tab===key} onClick={()=>setTab(key)}>{label}</Button>)}</div>
    {tab==="json"?<AuthorJSON label="실험 정의 원문" value={spec} onChange={onChange} onValidityChange={valid=>reportValidity("spec",valid)}/>:<>
      {tab==="stages"&&<div className="grid gap-4"><QueryGate query={loads}>{null}</QueryGate>{stages.map((stage,index)=>{const p:(string|number)[]=["stages",index],ref=object(stage.loadScenarioRef),start=object(stage.startState);return <details key={index} className="rounded-md border p-3" open={stages.length===1?true:undefined}><summary className="cursor-pointer font-medium">{String(stage.id||`단계 ${index+1}`)} · {{autonomous:"자율",fixed:"고정","controls-disabled":"제어요소 끔"}[String(stage.mode)]??"미선택"}</summary><div className="mt-3 grid gap-4">
        <div className="grid gap-3 sm:grid-cols-3"><AuthorField label="단계 ID" spec={spec} path={[...p,"id"]} onChange={onChange}/><AuthorField label="행 이름" spec={spec} path={[...p,"row"]} onChange={onChange}/><AuthorSelect label="모드" value={String(stage.mode??"")} onChange={mode=>onChange(patchSpec(spec,[...p,"mode"],mode))}><option value="">미선택</option><option value="autonomous">자율</option><option value="fixed">고정</option><option value="controls-disabled">제어요소 끔</option></AuthorSelect></div>
        <AuthorSelect label="부하 설정" value={String(ref.uid??"")} onChange={uid=>{const selected=loads.data?.items.find(x=>x.uid===uid);if(selected)onChange(patchSpec(spec,[...p,"loadScenarioRef"],{...ref,name:selected.name,uid:selected.uid}))}}><option value="">미선택</option>{ref.uid&&!loads.data?.items.some(x=>x.uid===ref.uid)?<option value={String(ref.uid)}>{String(ref.name)} · 대상 없음</option>:null}{loads.data?.items.map(item=><option key={item.uid} value={item.uid}>{item.name}</option>)}</AuthorSelect>
        <AuthorSelect label="시작 상태" value={start.stageResultRef!==undefined?"stage":"evidence"} onChange={kind=>{const next={...start};delete next.evidenceRef;delete next.stageResultRef;onChange(patchSpec(spec,[...p,"startState"],{...next,[kind==="stage"?"stageResultRef":"evidenceRef"]:{}}))}}><option value="evidence">증거 객체</option><option value="stage">앞선 단계 출력</option></AuthorSelect>
        {start.stageResultRef!==undefined?<div className="grid gap-3 sm:grid-cols-2"><AuthorField label="앞선 단계 ID" spec={spec} path={[...p,"startState","stageResultRef","stageID"]} onChange={onChange}/><AuthorField label="출력 이름" spec={spec} path={[...p,"startState","stageResultRef","output"]} onChange={onChange}/></div>:<EvidencePicker label="시작 상태 증거" value={start.evidenceRef} onChange={value=>onChange(patchSpec(spec,[...p,"startState","evidenceRef"],value))}/>}
        <details><summary className="cursor-pointer text-sm">단계 제어 · {stage.control===undefined?"기본 상속":"전체 대체"}</summary><div className="mt-3 grid gap-3"><label className="flex min-h-11 items-center gap-2 text-sm"><input type="checkbox" checked={stage.control!==undefined} onChange={event=>{onChange(patchSpec(spec,[...p,"control"],event.target.checked?structuredClone(object(spec.control)):undefined));reportValidity(`stage-${index}`,true)}}/>단계 제어 사용</label>{stage.control!==undefined&&<ControlFields spec={spec} path={[...p,"control"]} onChange={onChange} onValidityChange={valid=>reportValidity(`stage-${index}`,valid)}/>}</div></details>
        <InjectionEditor spec={spec} path={[...p,"failureInjection"]} onChange={onChange}/>
        <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={invalid.length>0||index===0} onClick={()=>{const next=[...stages];[next[index-1],next[index]]=[next[index],next[index-1]];onChange(patchSpec(spec,["stages"],next))}}>앞으로 이동</Button><Button type="button" variant="outline" disabled={invalid.length>0} onClick={()=>onChange(patchSpec(spec,["stages"],stages.filter((_,i)=>i!==index)))}>단계 삭제</Button></div>
      </div></details>})}<Button type="button" variant="outline" onClick={()=>onChange(patchSpec(spec,["stages"],[...stages,{id:"",row:"",startState:{}}]))}>단계 추가</Button><Link className="text-sm text-primary underline" href="/experiments/load-scenarios">부하 설정</Link></div>}
      {tab==="comparisons"&&<div className="grid gap-4">{comparisons.map((comparison,index)=><fieldset key={index} className="grid gap-3 rounded-md border p-3"><legend className="text-sm">비교 {index+1}</legend><div className="grid gap-4 sm:grid-cols-2"><ComparisonSide label="후보" spec={spec} path={["comparisons",index,"left"]} stages={stages} onChange={onChange}/><ComparisonSide label="기준" spec={spec} path={["comparisons",index,"right"]} stages={stages} onChange={onChange}/></div><AuthorField label="비교 사유" spec={spec} path={["comparisons",index,"reason"]} onChange={onChange}/><Button type="button" variant="outline" onClick={()=>onChange(patchSpec(spec,["comparisons"],comparisons.filter((_,i)=>i!==index)))}>비교 삭제</Button></fieldset>)}<Button type="button" variant="outline" onClick={()=>onChange(patchSpec(spec,["comparisons"],[...comparisons,{left:{stageID:""},right:{stageID:""},reason:""}]))}>비교 추가</Button></div>}
      {tab==="criteria"&&<div className="grid gap-4"><table className="w-full text-left text-sm"><caption className="sr-only">판정 기준</caption><thead><tr><th>항목</th><th>단위</th><th>값</th></tr></thead><tbody>{[["generatorQuality","latenessToleranceNanos","발행 지연 허용","ns"],["generatorQuality","maxExceedingFraction","지연 초과 비율","비율"],["energyCoverage","maxGapFraction","전력 결손 비율","비율"],["energyCoverage","maxSingleGapSeconds","단일 전력 공백","s"]].map(([section,key,label,unit])=><tr key={key}><th className="font-normal">{label}</th><td>{unit}</td><td className="py-1"><AuthorField label={label} compact spec={spec} path={["judgment",section,key]} numeric onChange={onChange}/></td></tr>)}</tbody></table><ControlFields spec={spec} path={["control"]} onChange={onChange} onValidityChange={valid=>reportValidity("control",valid)}/></div>}
      {tab==="context"&&<div className="grid min-w-0 gap-4"><AuthorField label="증거 저장소" spec={spec} path={["evidenceStore"]} onChange={onChange}/>
        {(["harness","loadGenerator"] as const).map(key=><fieldset key={key} className="grid min-w-0 gap-3 rounded border p-3"><legend className="px-1 text-sm">{key==="harness"?"실행기":"부하 발생기"}</legend><div className="grid min-w-0 gap-3 sm:grid-cols-2"><AuthorField label="이미지 주소" spec={spec} path={[key,"image"]} onChange={onChange}/><AuthorField label="소스 버전" spec={spec} path={[key,"sourceSHA"]} onChange={onChange}/></div>
          {key==="loadGenerator"&&<><div className="grid gap-3 sm:grid-cols-2">{[["requests","요청"],["limits","한도"]].map(([kind,label])=><AuthorField key={kind} label={`${label} 메모리`} spec={spec} path={[key,"resources",kind,"memory"]} onChange={onChange}/>)}</div><div className="grid gap-3 sm:grid-cols-2"><AuthorField label="최대 시작 편차 (ns)" spec={spec} path={[key,"barrier","maxStartSkewNanos"]} numeric onChange={onChange}/><AuthorField label="최소 준비 표본 (개)" spec={spec} path={[key,"barrier","minimumReadySamples"]} numeric onChange={onChange}/></div>{Object.keys(object(object(spec.loadGenerator).shards)).map(name=><AuthorField key={name} label={`${name} 분할 수`} spec={spec} path={[key,"shards",name]} numeric onChange={onChange}/>)}</>}
        </fieldset>)}
        <fieldset className="grid min-w-0 gap-3 rounded border p-3"><legend className="px-1 text-sm">대상 구성요소</legend>{records(object(spec.target).components).map((component,index)=><div key={index} className="grid min-w-0 gap-3 sm:grid-cols-2">{[["name","이름"],["image","이미지 주소"],["sourceSHA","소스 버전"]].map(([key,label])=><AuthorField key={key} label={label} spec={spec} path={["target","components",index,key]} onChange={onChange}/>)}{[["apiVersion","자원 API 버전"],["kind","자원 종류"],["namespace","네임스페이스"],["name","자원 이름"]].map(([key,label])=><AuthorField key={key} label={label} spec={spec} path={["target","components",index,"resource",key]} onChange={onChange}/>)}</div>)}<Button type="button" variant="outline" onClick={()=>onChange(patchSpec(spec,["target","components"],[...records(object(spec.target).components),{}]))}>구성요소 추가</Button></fieldset>
        <fieldset className="grid min-w-0 gap-3 rounded border p-3"><legend className="px-1 text-sm">전력 측정</legend><AuthorField label="측정 계열" spec={spec} path={["powerMeasurement","series"]} onChange={onChange}/><AuthorField label="표본 간격 (ns)" spec={spec} path={["powerMeasurement","sampling","intervalNanos"]} numeric onChange={onChange}/>
          {records(object(spec.powerMeasurement).meters).map((meter,index)=><div key={index} className="grid min-w-0 gap-3 sm:grid-cols-2"><AuthorField label="계측기 이름" spec={spec} path={["powerMeasurement","meters",index,"id"]} onChange={onChange}/><AuthorField label="관측 원천 식별자" spec={spec} path={["powerMeasurement","meters",index,"sourceIdentity"]} onChange={onChange}/></div>)}<Button type="button" variant="outline" onClick={()=>onChange(patchSpec(spec,["powerMeasurement","meters"],[...records(object(spec.powerMeasurement).meters),{}]))}>계측기 추가</Button>
          {records(object(spec.powerMeasurement).nodes).map((node,index)=><div key={index} className="grid min-w-0 gap-3 sm:grid-cols-2"><AuthorField label="장치 이름" spec={spec} path={["powerMeasurement","nodes",index,"name"]} onChange={onChange}/><AuthorField label="장치 식별자" spec={spec} path={["powerMeasurement","nodes",index,"uid"]} onChange={onChange}/></div>)}<Button type="button" variant="outline" onClick={()=>onChange(patchSpec(spec,["powerMeasurement","nodes"],[...records(object(spec.powerMeasurement).nodes),{}]))}>측정 장치 추가</Button>
        </fieldset>
        <details><summary className="cursor-pointer text-sm">실행 환경 원문</summary>{[["target","대상 구성요소"],["harness","실행기"],["loadGenerator","부하 발생기"],["powerMeasurement","전력 측정"]].map(([key,label])=><AuthorJSON key={key} disabled={invalid.length>0&&!invalid.includes(key)} label={label} value={object(spec[key])} onChange={value=>onChange(patchSpec(spec,[key],value))} onValidityChange={valid=>reportValidity(key,valid)}/>)}</details>
      </div>}
    </>}
  </div>
}
export function EvaluationAuthor({initialSpec,initialName,existing,onSubmitted}:{initialSpec?:Spec;initialName?:string;existing?:{name:string;uid:string};onSubmitted?:(value:{name:string;uid:string})=>void}) {
  const [spec,setSpec]=useState<Spec>(()=>initialSpec?copyAuthorDraft({spec:initialSpec}).spec:{}),[name,setName]=useState(existing?.name??""),[origin,setOrigin]=useState(initialName??""),[ready,setReady]=useState(!!initialSpec),[valid,setValid]=useState(true),[source,setSource]=useState(""),[sources,setSources]=useState<Spec>({}),[version,setVersion]=useState(0)
  const [sourcePublication,setSourcePublication]=useState<{uid:string;name:string}>()
  const [baseline]=useState(()=>specJSON({name:existing?.name??"",spec:initialSpec?copyAuthorDraft({spec:initialSpec}).spec:{}}))
  useUnsavedAuthor(!valid||ready&&specJSON({name,spec})!==baseline)
  const publications=useChillQuery<{items:{id:string}[]}>("v1/experiments",{enabled:!initialSpec}),publication=useChillQuery<{definition?:{run?:{name?:string};spec?:Spec}}>(`v1/experiments/${encodeURIComponent(source)}`,{enabled:!!source,decoder:parseSchedulerJSON})
  const template=useChillQuery<{spec:Spec;sources:Spec;sourcePublication?:{uid:string;name:string}}>("v1/evaluations/template",{enabled:false,decoder:parseSchedulerJSON})
  async function loadTemplate() {
    const result=await template.refetch()
    if(!result.error&&result.data){setSpec(copyAuthorDraft(result.data).spec);setSources(result.data.sources);setSourcePublication(result.data.sourcePublication);setOrigin("");setReady(true);setValid(true);setVersion(v=>v+1)}
  }
  const labels:Record<string,string>={question:"질문",target:"대상 구성요소",harness:"실행기",loadGenerator:"부하 발생기",powerMeasurement:"전력 측정",control:"제어",judgment:"판정 기준",evidenceStore:"증거 저장소",stages:"단계",comparisons:"비교"}
  return <div className="grid min-w-0 gap-5">
    {!ready&&<EvaluationPermission/>}
    <div className="flex justify-end"><AuthorHelp/></div>
    {!initialSpec&&<section className="grid gap-3" aria-label="정의 출처">
      <AuthorSelect label="게시에서 복사" value={source} onChange={setSource}><option value="">게시 선택</option>{publications.data?.items.map(item=><option key={item.id} value={item.id}>{item.id}</option>)}</AuthorSelect>
      <QueryGate query={publications}>{null}</QueryGate>{source&&<QueryGate query={publication}>{null}</QueryGate>}{(template.isFetching||template.error)&&<QueryGate query={template}>{null}</QueryGate>}
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!publication.data?.definition?.spec} onClick={()=>{const definition=publication.data?.definition;if(definition?.spec){setSpec(copyAuthorDraft({spec:definition.spec}).spec);setSources({});setSourcePublication(undefined);setName("");setOrigin(definition.run?.name??"");setReady(true);setValid(true);setVersion(v=>v+1)}}}>게시 복사</Button>
        <Button type="button" variant="outline" disabled={template.isFetching} onClick={()=>void loadTemplate()}>빈 템플릿</Button>{publication.data&&!publication.data.definition?.spec&&<span role="status" className="text-sm">정의 미기록</span>}</div>
    </section>}
    {ready&&<><section className="grid gap-4" aria-label="실험 정의">
      {sourcePublication&&<dl className="grid gap-1 text-sm"><dt>출처 게시 이름</dt><dd>{sourcePublication.name||"미제공"}</dd><dt>출처 게시 식별자</dt><dd className="break-all">{sourcePublication.uid}</dd></dl>}
      {Object.values(sources).some(source=>source==="none")&&<table aria-label="입력 필요 항목" className="w-full text-left text-sm"><thead><tr><th>입력 필요</th><th>상태</th></tr></thead><tbody>{Object.entries(sources).filter(([,source])=>source==="none").map(([field])=><tr key={field}><th className="py-1 font-normal">{labels[field]??"기타 입력"}</th><td>출처 미제공</td></tr>)}</tbody></table>}
      {Object.keys(sources).length>0&&<details><summary className="cursor-pointer text-sm">입력 출처</summary><table className="w-full text-left text-sm"><thead><tr><th>필드</th><th>출처</th><th>상태</th></tr></thead><tbody>{Object.entries(sources).map(([field,source])=><tr key={field} className={source==="none"?"bg-destructive/10":""}><th className="py-2">{labels[field]??field}</th><td>{{live:"현재 구성",config:"운영 설정",latestPublication:"최근 게시",none:"없음"}[String(source)]??"원문 확인"}</td><td>{source==="none"?"입력 필요":"확인됨"}</td></tr>)}</tbody></table></details>}
      <label className="grid gap-1 text-sm">실험 이름<Input value={name} disabled={!!existing} onChange={event=>setName(event.target.value)}/></label>{!existing&&origin&&name===origin&&<span role="alert" className="text-sm text-destructive">새 이름 필요</span>}
      {origin?<details><summary className="min-h-11 cursor-pointer text-sm">질문 원문</summary><AuthorField label="질문 · 저장된 원문" spec={spec} path={["question"]} onChange={setSpec}/></details>:<AuthorField label="질문" spec={spec} path={["question"]} onChange={setSpec}/>}<EvaluationSpecEditor key={version} spec={spec} onChange={setSpec} onValidityChange={setValid}/>
    </section><EvaluationSubmit name={name} spec={spec} existing={existing} disabled={!valid||!name.trim()||(!existing&&name===origin)} onSpecChange={setSpec} onSubmitted={onSubmitted}/></>}
  </div>
}

export function EvaluationAuthorPage({name}:{name?:string}) {
  const params=useSearchParams(),router=useRouter(),copy=params.get("copy")??"",publication=params.get("publication")??"",source=name??copy
  const run=useChillQuery<{name:string;uid:string;phase:string;spec:Spec}>(`v1/evaluations/${encodeURIComponent(source||"_")}`,{enabled:!!source,decoder:parseSchedulerJSON})
  const result=useChillQuery<{definition?:{run:{name:string};spec:Spec}}>(`v1/experiments/${encodeURIComponent(publication||"_")}`,{enabled:!source&&!!publication,decoder:parseSchedulerJSON})
  const identityChanged=!!name&&!!params.get("uid")&&run.data?.uid!==params.get("uid")
  const submitted=(value:{name:string;uid:string})=>router.replace(`/experiments?${new URLSearchParams(value)}`)
  return <Screen title={name?"실험 수정":"새 실험"} width="data" back={["/experiments","실험"]}>
    {source?<QueryGate query={run}>{run.data&&(name&&(run.data.phase!=="Pending"||identityChanged)?<strong role="alert">{identityChanged?"실험 교체 확인":"시작 전 수정만 허용"}</strong>:<EvaluationAuthor onSubmitted={submitted} key={run.data.uid} initialSpec={run.data.spec} initialName={run.data.name} existing={name?{name:run.data.name,uid:run.data.uid}:undefined}/>)}</QueryGate>:publication?<QueryGate query={result}>{result.data&&(result.data.definition?<EvaluationAuthor onSubmitted={submitted} key={publication} initialSpec={result.data.definition.spec} initialName={result.data.definition.run.name}/>:<div className="flex flex-wrap items-center gap-3"><strong role="status">정의 미기록</strong><Button variant="outline" asChild><Link href="/experiments/new">다른 출처 선택</Link></Button></div>)}</QueryGate>:<EvaluationAuthor onSubmitted={submitted}/>}
  </Screen>
}

"use client"

import { useRef, useState, type Dispatch, type SetStateAction } from "react"
import { useRouter } from "next/navigation"
import { ApiError, useChillMutation } from "@/lib/client"
import type { MeasurementMutation, MeasurementPreflight } from "@/lib/read-types"
import { updateDraft, manifest, requestHref, steps, view, type Context, type Draft } from "@/lib/measurement-request"
import type { Compatibility } from "@/lib/measurement-coverage"
import type { MeasurementRequestDocument } from "@/lib/measurement-document"

export function failure(error: ApiError) {
  const data = (error.data && typeof error.data === "object" ? error.data : {}) as { code?: string }
  return { code:data.code, message:data.code==="DashboardOriginDenied"?"현재 접속 주소에서 검사할 수 없어요. 관리자에게 허용 주소 설정을 요청하세요.":error.status===403?"측정 요청 권한이 없어요. 계정의 권한을 확인하세요.":error.status===401?"로그인이 만료됐어요. 다시 로그인하세요.":error.status===409?"같은 이름의 요청이나 조건이 변경됐어요. 목록을 확인하고 다시 검사하세요.":error.status===400||error.status===422?"측정 조건을 처리할 수 없어요. 모델·장치·측정 방식 값을 확인하세요.":"서버 응답을 확인하지 못했어요. 연결 상태를 확인한 뒤 다시 검사하세요." }
}

export function useMeasurementSubmission({ d, setD, context, namespace, variantId, verdict, refetchPlans }: {
  d:Draft; setD:Dispatch<SetStateAction<Draft>>; context:Context; namespace:string; variantId:string;
  verdict?:Compatibility; refetchPlans:()=>Promise<{error:ApiError|null}>;
}) {
  const v=view(d,context)
  const submission=useMeasurementDocumentSubmission({
    body:manifest(d,context,namespace),namespace,plans:context.plans,refetchPlans,variantId,verdict,
    validate:()=>steps.filter(step=>step!=="review").map(step=>v.problem(step)).find(Boolean)||"",
    onInvalid:():void=>{submission.setStage(v.problem("workload")?0:1)},
    onSubmit:()=>setD({...d,name:v.name}),
  })
  const update=(patch:Partial<Draft>)=>{
    if(submission.unknown||submission.pending||submission.send.isPending)return
    setD(current=>updateDraft(current,patch,context))
    submission.invalidate()
  }
  function next() {
    const issue=submission.stage===0?v.problem("workload"):steps.filter(step=>step!=="workload"&&step!=="review").map(step=>v.problem(step)).find(Boolean)
    if(issue){submission.setError(issue);return}
    submission.setError("");submission.setStage(submission.stage+1)
  }
  return {...submission,update,next}
}

export function useMeasurementDocumentSubmission({body,namespace,plans,refetchPlans,variantId="",verdict,validate,onInvalid,onSubmit}:{
  body:MeasurementRequestDocument|null;namespace:string;plans:Context["plans"];
  refetchPlans:()=>Promise<{error:ApiError|null}>;variantId?:string;
  verdict?:Compatibility;validate?:()=>string;onInvalid?:()=>void;onSubmit?:()=>void;
}) {
  const router=useRouter()
  const dry=useChillMutation(),pre=useChillMutation(),send=useChillMutation()
  const [error,setError]=useState(""),[unknown,setUnknown]=useState(false),[checked,setChecked]=useState(false)
  const [showUnknown,setShowUnknown]=useState(false)
  const [stage,setStage]=useState(0)
  const sending=useRef(false)
  const listPath=`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`
  const name=body?.metadata.name??""
  const answer=pre.data?.data as MeasurementPreflight|undefined
  const admitted=dry.isSuccess&&(dry.data?.data as MeasurementMutation|null)?.request==="NOT_SUBMITTED"
  const issue=!body?"먼저 요청 문서를 검토하세요.":body.metadata.namespace!==namespace?"선택한 네임스페이스에서 문서를 다시 검토하세요.":validate?.()||""
  const canSubmit=!issue
  const pending=dry.isPending||pre.isPending
  function invalidate() {
    if(unknown||pending||send.isPending)return
    setError("");dry.reset();pre.reset();send.reset()
  }
  function checkInput() {
    if(!issue)return true
    setError(issue);onInvalid?.();requestAnimationFrame(()=>document.getElementById("measurement-errors")?.focus())
    return false
  }
  function verify() {
    if(unknown||pending||send.isPending)return
    if(!checkInput())return
    if(verdict?.state==="unsupported"){setError(verdict.reason);return}
    if(verdict?.state==="unknown"&&!showUnknown){setError("지원 미확인 조합을 검사하려면 명시 검사 항목을 선택하세요.");return}
    setError("")
    dry.mutate({path:`${listPath}?dryRun=All`,method:"POST",body})
    pre.mutate({path:`${listPath}/preflight${variantId?`?${new URLSearchParams({variantId})}`:""}`,method:"POST",body})
  }
  function submit() {
    if(sending.current||unknown||send.isPending||!checkInput())return
    sending.current=true
    onSubmit?.();setError("")
    send.mutate({path:listPath,method:"POST",body},{
      onSuccess:response=>{const task=(response.data as MeasurementMutation|null)?.task;router.replace(requestHref(task?.metadata.namespace??namespace,task?.metadata.name??name,task?.metadata.uid))},
      onError:e=>{if(e.unknownOutcome||e.status===0||failure(e).code==="MeasurementAuditIncomplete")setUnknown(true);else sending.current=false},
    })
  }
  const found=checked?plans.find(plan=>plan.metadata.namespace===namespace&&plan.metadata.name===name):undefined
  function lookup() {
    void refetchPlans().then(result=>{setChecked(!result.error);if(result.error)setError("목록 조회 실패: 다시 확인하세요.")})
  }
  return {dry,pre,send,error,setError,unknown,checked,showUnknown,setShowUnknown,stage,setStage,answer,admitted,canSubmit,pending,invalidate,verify,submit,found,lookup}
}

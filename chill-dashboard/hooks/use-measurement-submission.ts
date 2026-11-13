"use client"
import { useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { useChillMutation } from "@/lib/client"
import { ApiError } from "@/lib/client-transport"
import type { MeasurementMutation, MeasurementPreflight } from "@/lib/read-types"
import { requestHref, type Context } from "@/lib/measurement-request"
import type { Compatibility } from "@/lib/measurement-coverage"
import type { MeasurementRequestDocument } from "@/lib/measurement-document"

export function failure(error: ApiError) {
  const data = (error.data && typeof error.data === "object" ? error.data : {}) as { code?: string }
  return { code:data.code, message:data.code==="DashboardOriginDenied"?"허용되지 않은 접속 주소":error.status===403?"측정 요청 권한 없음":error.status===401?"로그인 만료":error.status===409?"같은 이름 또는 조건 변경됨":error.status===400||error.status===422?"측정 조건 거부":"서버 응답 확인 불가" }
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

"use client"

import { useState, type Dispatch, type SetStateAction } from "react"
import { useRouter } from "next/navigation"
import { ApiError, useChillMutation } from "@/lib/client"
import type { MeasurementMutation, MeasurementPreflight } from "@/lib/read-types"
import { fillDefaults, manifest, requestCheckCurrent, requestHref, steps, view, type Context, type Draft } from "@/lib/measurement-request"
import type { Compatibility } from "@/lib/measurement-coverage"

export function failure(error: ApiError) {
  const data = (error.data && typeof error.data === "object" ? error.data : {}) as { code?: string }
  return { code:data.code, message:data.code==="DashboardOriginDenied"?"현재 접속 주소에서 검사할 수 없어요. 관리자에게 허용 주소 설정을 요청하세요.":error.status===403?"측정 요청 권한이 없어요. 계정의 권한을 확인하세요.":error.status===401?"로그인이 만료됐어요. 다시 로그인하세요.":error.status===409?"같은 이름의 요청이나 조건이 변경됐어요. 목록을 확인하고 다시 검사하세요.":error.status===400||error.status===422?"측정 조건을 처리할 수 없어요. 모델·장치·측정 방식 값을 확인하세요.":"서버 응답을 확인하지 못했어요. 연결 상태를 확인한 뒤 다시 검사하세요." }
}

export function useMeasurementSubmission({ d, setD, context, namespace, source, copiedIdentity, variantId, verdict, refetchPlans }: {
  d:Draft; setD:Dispatch<SetStateAction<Draft>>; context:Context; namespace:string; source:string;
  copiedIdentity?:Context["plans"][number]["metadata"]; variantId:string;
  verdict?:Compatibility; refetchPlans:()=>Promise<{error:ApiError|null}>;
}) {
  const router=useRouter()
  const dry = useChillMutation(), pre = useChillMutation(), send = useChillMutation()
  const [error,setError]=useState(""),[unknown,setUnknown]=useState(false),[checked,setChecked]=useState(false)
  const [showUnknown,setShowUnknown]=useState(false)
  const [stage,setStage]=useState(0),[checkedInput,setCheckedInput]=useState<string|null>(null)
  const v=view(d,context), listPath=`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`
  const answer=pre.data?.data as MeasurementPreflight|undefined
  const admitted=dry.isSuccess&&(dry.data?.data as MeasurementMutation|null)?.request==="NOT_SUBMITTED"
  const identities={source,copiedIdentity,model:v.model?.metadata,device:v.cls&&{uid:v.cls.uid,resourceVersion:v.cls.resourceVersion},node:v.cls?.nodes.find(node=>node.name===d.node),preprocessor:context.preprocessors.find(item=>item.metadata.name===d.preprocessor)?.metadata,dataset:context.datasets.find(item=>item.metadata.name===d.dataset)?.metadata,showUnknown}
  const inputKey=JSON.stringify({body:manifest(d,context,namespace),...identities})
  const passed=requestCheckCurrent(checkedInput,inputKey,admitted,answer?.resolves)
  const pending=dry.isPending||pre.isPending
  const update=(patch:Partial<Draft>)=>{
    setD(current=>fillDefaults({...current,...patch,...(patch.preprocessor!==undefined?{explicitPreprocessor:!!patch.preprocessor}:{}),...(patch.dataset!==undefined?{explicitDataset:!!patch.dataset}:{})},context))
    setError("");setCheckedInput(null);dry.reset();pre.reset();send.reset()
  }
  function verify() {
    const issue=steps.filter(step=>step!=="review").map(step=>v.problem(step)).find(Boolean)
    if(issue){setError(issue);setStage(v.problem("workload")?0:1);requestAnimationFrame(()=>document.getElementById("measurement-errors")?.focus());return}
    if(verdict?.state==="unsupported"){setError(verdict.reason);return}
    if(verdict?.state==="unknown"&&!showUnknown){setError("지원 미확인 조합을 검사하려면 명시 검사 항목을 선택하세요.");return}
    // Keep the checked identity through success, transport uncertainty and manual identity lookup.
    const pinned={...d,name:v.name};setD(pinned);setError("")
    const body=manifest(pinned,context,namespace)
    setCheckedInput(JSON.stringify({body,...identities}))
    dry.mutate({path:`${listPath}?dryRun=All`,method:"POST",body})
    pre.mutate({path:`${listPath}/preflight${variantId?`?${new URLSearchParams({variantId})}`:""}`,method:"POST",body})
  }
  function submit() {
    if(!passed||unknown)return
    send.mutate({path:listPath,method:"POST",body:manifest(d,context,namespace)},{
      onSuccess:response=>{const task=(response.data as MeasurementMutation|null)?.task;router.replace(requestHref(task?.metadata.namespace??namespace,task?.metadata.name??v.name,task?.metadata.uid))},
      onError:e=>{if(e.unknownOutcome||e.status===0||failure(e).code==="MeasurementAuditIncomplete")setUnknown(true)},
    })
  }
  const found=checked?context.plans.find(plan=>plan.metadata.name===v.name):undefined
  function lookup() {
    void refetchPlans().then(result=>{setChecked(!result.error);if(result.error)setError("목록 조회 실패: 다시 확인하세요.")})
  }
  function next() {
    const issue=stage===0?v.problem("workload"):steps.filter(step=>step!=="workload"&&step!=="review").map(step=>v.problem(step)).find(Boolean)
    if(issue){setError(issue);return}
    setError("");setStage(stage+1)
  }
  return {dry,pre,send,error,unknown,checked,showUnknown,setShowUnknown,stage,setStage,setCheckedInput,answer,admitted,passed,pending,update,verify,submit,found,lookup,next}
}

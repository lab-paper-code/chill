"use client"

import Link from "next/link"
import { useState } from "react"
import { Facts, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { SourceJSON } from "@/components/read-region"
import { MeasurementRequestSource } from "@/components/measurement-request-source"
import { MeasurementNavigation } from "@/components/measurement-navigation"
import { MeasurementDocumentInput } from "@/components/measurement-document-input"
import { MeasurementPreparationGuide } from "@/components/measurement-preparation-guide"
import { useMeasurementPlans } from "@/hooks/use-measurement-data"
import { useMeasurementSubmission } from "@/hooks/use-measurement-submission"
import { emptyDraft, requestHref, type Context } from "@/lib/measurement-request"
import type { MeasurementRequestDocument } from "@/lib/measurement-document"
import { measurementRequestExample } from "@/lib/measurement-guidance"
import { PreflightAnswer, preflightConclusion } from "@/features/read-pages"

const emptyContext:Context={models:[],workloads:[],classes:[],plans:[],preprocessors:[],datasets:[],profiles:[]}
const object=(value:unknown):Record<string,unknown>=>value!==null&&typeof value==="object"&&!Array.isArray(value)?value as Record<string,unknown>:{}
const display=(value:unknown)=>typeof value==="string"||typeof value==="number"?String(value):"미제공"

export function MeasurementDocumentRequestPage({namespace}:{namespace:string}) {
  const plans=useMeasurementPlans(namespace)
  const [draft,setDraft]=useState(emptyDraft)
  const [body,setBody]=useState<MeasurementRequestDocument|null>(null)
  const submission=useMeasurementSubmission({d:draft,setD:setDraft,context:{...emptyContext,plans:plans.data?.items??[]},namespace,source:"document",variantId:"",refetchPlans:plans.refetch,document:{body}})
  const {dry,pre,send,answer,admitted,passed,pending,unknown,checked,found,error,invalidate,verify,submit,lookup}=submission
  const locked=pending||send.isPending||unknown
  const state=object(body?.spec.state),candidate=object(state.candidate),active=object(body?.spec.activeProtocol),idle=object(body?.spec.idleProtocol)
  return <Screen width="data" title="측정 요청 문서 가져오기" back={["/measurements/requests","측정 요청"]}>
    <MeasurementNavigation active="requests" namespace={namespace}/>
    <p className="max-w-prose text-sm text-muted-foreground">준비된 측정 조건을 읽고 검토한 뒤, 서버 검사와 최종 제출을 진행하세요.</p>
    <details><summary className="min-h-11 cursor-pointer py-2 font-medium">준비 안내·예시</summary><MeasurementPreparationGuide/></details>
    <MeasurementDocumentInput namespace={namespace} locked={locked} reviewed={body!==null} example={measurementRequestExample} onEdit={()=>{if(!locked){setBody(null);invalidate()}}} onReview={value=>{if(!locked){setBody(value);invalidate()}}}/>
    {body&&<section aria-label="측정 요청 내용 검토" className="grid min-w-0 gap-5 border-t pt-5">
      <h2 className="font-semibold">요청 내용</h2>
      <div className="grid gap-5 md:grid-cols-2">
        <Facts label="측정 대상" items={[["요청 이름",body.metadata.name],["서비스 (Workload)",display(active.workload)],["실행 장비",display(state.targetNode)]]}/>
        <Facts label="측정 조건" items={[["배치 (건)",Array.isArray(body.spec.batchSizes)?body.spec.batchSizes.map(display).join(", "):"모델 선언 범위 · 서버 검사에서 확인"],["배치별 반복 (회)",display(body.spec.trialsPerBatch)],["실행 / 유휴 측정 (초)",`${display(active.durationSeconds)} / ${display(idle.durationSeconds)}`]]}/>
      </div>
      <details><summary className="min-h-11 cursor-pointer py-2 font-medium">연결 자산·실행 방식·전력 기록</summary><div className="grid gap-5 md:grid-cols-2">
        <Facts label="자산 연결" items={[["네임스페이스",namespace],["모델",display(candidate.modelSpec)],["모델 입출력 처리",display(candidate.preprocessor)],["데이터셋",display(active.dataset)],["장치 종류",display(candidate.deviceClass)],["실행 경로",display(candidate.executionPath)],["전력 모드",display(candidate.powerMode)]]}/>
        <Facts label="기록 조건" items={[["실행 전력 기록 (초)",display(object(active.metricObservation).durationSeconds)],["유휴 전력 기록 (초)",display(object(idle.metricObservation).durationSeconds)],["예열 (회)",display(active.warmupIterations)],["유휴 안정 대기 (초)",display(idle.settlingDurationSeconds)]]}/>
      </div></details>
      <MeasurementRequestSource value={body}/>
      <p className="text-sm text-muted-foreground">이름·설정의 유효성과 실행 가능 여부는 서버 검사에서 확인해요. 최종 제출하면 실제 장비에서 측정이 시작될 수 있어요.</p>
    </section>}
    <div id="measurement-errors" role={error?"alert":undefined} className="space-y-2" tabIndex={-1}>{error&&<p className="text-sm text-destructive">{error}</p>}</div>
    {(dry.data||dry.error||pre.data||pre.error)&&<section aria-label="측정 요청 서버 검사" className="grid gap-3 border-t pt-5">
      <h2 className="font-semibold">서버 검사</h2>
      <Facts items={[["접수 형식 검사",dry.error?`HTTP ${dry.error.status||"응답 미수신"}`:dry.data?(admitted?"미제출 · 형식 검사 통과":"응답 확인"):"검사 중"],["실행 계획 검사",pre.error?`HTTP ${pre.error.status||"응답 미수신"}`:pre.data?(answer?.resolves?"계획 확인":"계획 미확정"):"검사 중"]]}/>
      {answer&&<><h3 className="font-medium">{preflightConclusion(answer)}</h3><PreflightAnswer answer={answer}/></>}
    </section>}
    {[dry,pre,send].map((mutation,index)=>mutation.error?<section key={index} role="alert" className="notice space-y-2"><p>{["접수 검사","실행 계획 검사","제출"][index]} · {mutation.error.status?`HTTP ${mutation.error.status}`:"응답 미수신"}</p><p className="whitespace-pre-wrap break-words text-sm">{mutation.error.body}</p><SourceJSON inline value={{status:mutation.error.status,data:mutation.error.data}} title="오류 상세"/></section>:null)}
    {unknown&&<section role="alert" className="notice grid gap-3"><h2 className="font-semibold">접수 결과 미확인</h2><p className="text-sm">{body?.metadata.name} 요청이 이미 생성됐을 수 있어요. 같은 이름의 접수 기록을 확인하세요.</p><Button variant="outline" className="w-fit" disabled={plans.isFetching} onClick={lookup}>같은 이름의 접수 기록 조회</Button>{found?<Link className="underline" href={requestHref(namespace,found.metadata.name,found.metadata.uid)}>접수된 요청 열기</Link>:checked&&<p className="text-sm">접수 기록 미확인 · 재제출 차단 유지</p>}</section>}
    <div className="flex flex-wrap gap-3"><Button variant="outline" disabled={!body||locked} onClick={verify}>{pending?"검사 중":"서버에서 요청 검사"}</Button><Button disabled={!body||!passed||locked} onClick={submit}>{send.isPending?"제출 중":"측정 요청 제출"}</Button></div>
  </Screen>
}

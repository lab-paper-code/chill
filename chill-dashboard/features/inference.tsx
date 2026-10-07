"use client"

import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useState, type FormEvent } from "react"
import { useChillMutation, useChillQuery, useSession, type RelayResponse } from "@/lib/client"
import { DetailsDialog, ReadRegion, SourceJSON, statusLabel } from "@/components/read-region"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { inferenceBody, tensorCount, type SupportedInput, type WorkloadView } from "@/lib/domain"
import { WorkloadState } from "@/features/management"

function InferenceForm({ name, view, wrongUID }: { name: string; view: WorkloadView; wrongUID: boolean }) {
  const { config } = useSession()
  const mutation = useChillMutation()
  const [selected, setSelected] = useState("")
  const [data, setData] = useState<string[]>([])
  const [timeout, setTimeout] = useState(`${config.inferTimeoutMs}ms`)
  const [validation, setValidation] = useState("")
  const [result, setResult] = useState<RelayResponse | null>(null)
  const [sentBody, setSentBody] = useState<string | null>(null)
  const [submittedAt, setSubmittedAt] = useState<string | null>(null)
  const [observationTo, setObservationTo] = useState<string | null>(null)
  // Keep the selected contract's data draft when observations refresh. Never auto-fill/replay.
  const contract: SupportedInput | undefined = view.supportedInputs.find(input => `${input.executionPath}/${input.artifact}` === selected)
  let preview = ""
  let previewError = ""
  try { if (contract) preview = inferenceBody(contract.tensors, data) } catch (error) { previewError = (error as Error).message }
  const bf16 = contract?.tensors.some(t => t.datatype === "BF16")
  function sample() {
    if (!contract) return
    try {
      const counts = contract.tensors.map(tensorCount)
      // A sample is optional assistance, never allocate an unbounded declared tensor.
      if (counts.reduce((sum, count) => sum + count, 0) * 8 > config.maxRequestBytes) throw new Error("예시 생성은 요청 크기 한도 안에서만 가능합니다. 직접 평면 데이터를 입력하세요.")
      setData(contract.tensors.map((tensor, index) => JSON.stringify(Array(counts[index]).fill(tensor.datatype === "BOOL" ? false : tensor.datatype === "BYTES" ? "" : 0))))
      setValidation("")
    } catch (error) { setValidation((error as Error).message) }
  }
  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setValidation("")
    try {
      if (!contract) throw new Error("측정이 완료된 입력을 선택하세요.")
      if (!view.admission || wrongUID) throw new Error("현재 서비스가 요청을 받을 수 없어요. 준비 상태를 확인하세요.")
      if (!timeout.trim()) throw new Error("응답을 기다릴 제한 시간과 단위를 입력하세요.")
      const body = inferenceBody(contract.tensors, data)
      if (new TextEncoder().encode(body).byteLength > config.maxRequestBytes) throw new Error(`요청이 허용 크기(${config.maxRequestBytes}바이트)를 넘었어요.`)
      setResult(null); setSentBody(body); setSubmittedAt(new Date().toISOString()); setObservationTo(null)
      try {
        const response = await mutation.mutateAsync({ path: `v1/workloads/${encodeURIComponent(name)}/infer`, method: "POST", body, timeout })
        setResult(response)
      } finally { setObservationTo(new Date(Date.now() + 1000).toISOString()) }
    } catch (error) {
      // Mutation failures retain the response body/IDs and unknown-outcome classification.
      if (!mutation.isPending && !(error && typeof error === "object" && "unknownOutcome" in error)) setValidation((error as Error).message)
    }
  }
  const received = result ?? mutation.error
  const requestID = received?.requestID
  const workloadUID = received?.workloadUID ?? view.desired.metadata.uid
  const observationParams = new URLSearchParams({ from: submittedAt ?? "", to: observationTo ?? "", limit: String(config.pageSize) })
  const observationLinkParams = new URLSearchParams(observationParams)
  observationLinkParams.set("workload", name)
  observationLinkParams.set("uid", workloadUID)
  if (requestID) observationLinkParams.set("requestID", requestID)
  const observation = useChillQuery<{ requests: { requestID: string; workloadUID?: string; kind: string; elapsedNanos: number; outcome?: string; error?: string; at: string; receivedAt: string }[]; nextCursor?: string; incompleteWindows?: string[] }>(
    `v1/observations/workloads/${encodeURIComponent(workloadUID)}?${observationParams}`,
    { enabled: !!requestID && !!submittedAt && !!observationTo && !mutation.isPending },
  )
  const completion = observation.data?.requests.find(event => event.requestID === requestID && event.workloadUID === workloadUID && event.kind === "response_finished")
  return <section className="mx-auto grid w-full max-w-2xl gap-6">
    {!view.admission && <div role="status" className="grid gap-2 rounded-xl bg-muted p-5"><h2 className="font-semibold">추론을 시작하려면 준비가 필요해요</h2><p className="text-sm">{view.supportedInputs.length ? "지금은 새 요청을 받을 수 없어요. 준비 상태를 확인해 주세요." : "이 모델의 실행 조건에 맞는 측정 결과가 아직 없어요. 측정이 완료되고 서비스가 준비되면 입력을 선택할 수 있어요."}</p><Link className="text-sm" href="/measurements">측정 결과 확인</Link></div>}
    <div className="flex flex-wrap gap-3"><DetailsDialog label="모델과 준비 상태" title={`${name} 모델과 준비 상태`}><WorkloadState view={view} /></DetailsDialog>{contract && <DetailsDialog label="선택한 입력 자세히" title={`${contract.artifact} 입력과 측정 근거`}><p>아래 크기는 한 번의 입력 크기예요. 여러 요청을 묶는 축은 보내지 않아요.</p><SourceJSON title="지원 입력과 측정 원본" value={contract} /><Link href="/measurements">연결된 측정 결과 확인</Link></DetailsDialog>}</div>
    <form className="grid gap-5" onSubmit={handleSubmit}>
      <fieldset disabled={mutation.isPending || wrongUID} className="grid gap-5">
        <label className="grid gap-2 text-sm">사용할 입력<select required aria-label="사용할 입력" value={selected} className="rounded border bg-background p-2" onChange={event => {
          const choice = view.supportedInputs.find(input => `${input.executionPath}/${input.artifact}` === event.target.value)
          setSelected(event.target.value); setData(choice?.tensors.map(() => "") ?? []); setValidation("")
        }}><option value="">{view.supportedInputs.length ? "측정이 완료된 입력 선택" : "선택할 수 있는 입력이 없어요"}</option>{view.supportedInputs.map((input, index) => <option key={index} value={`${input.executionPath}/${input.artifact}`}>{input.artifact} · {input.tensors.map(t => t.name).join(", ")} ({input.executionPath})</option>)}</select></label>
        {!view.supportedInputs.length && view.admission && <p className="text-sm">사용 가능한 입력이 확인되지 않아 제출할 수 없어요. <Link href="/measurements">측정 결과 확인</Link></p>}
        {selected && !contract && <p role="alert">선택한 입력이 최신 목록에서 없어졌어요. 작성 내용은 유지했어요. 다른 입력을 선택해야 제출할 수 있어요.</p>}
        {contract?.tensors.map((tensor, index) => <fieldset key={`${selected}/${tensor.name}`} className="grid gap-3 rounded-lg border p-4"><legend>입력 {index + 1} · {tensor.name}</legend>
          <p className="text-sm text-muted-foreground">값 종류 {tensor.datatype} · 크기 {tensor.sampleShape.join(" × ")}</p>
          <label className="grid gap-2 text-sm">{`${tensor.name} 입력값`}<textarea aria-label={`${tensor.name} 입력값`} required rows={5} className="w-full rounded border bg-background p-3 font-mono" value={data[index] ?? ""} onChange={event => setData(previous => previous.map((value, i) => i === index ? event.target.value : value))} /></label>
          <p className="text-sm text-muted-foreground">{tensor.datatype === "BYTES" ? '문자열을 목록으로 입력하세요. 예: ["text", "value"]' : tensor.datatype === "BOOL" ? "참·거짓 값을 목록으로 입력하세요. 예: [true, false]" : tensor.datatype === "BF16" ? "이 값 종류는 현재 웹에서 전송할 수 없어요. 다른 지원 입력을 선택하세요." : "숫자를 한 줄의 목록으로 입력하세요. 예: [0, 1, 2]. 큰 64비트 정수도 입력한 숫자 그대로 전송해요."} 위에 표시된 값 개수를 맞춰 주세요.</p>
        </fieldset>)}
        {contract && <Button type="button" variant="outline" disabled={bf16} onClick={sample}>입력 예시 채우기</Button>}
        <details><summary className="cursor-pointer text-sm text-muted-foreground">응답 대기 시간 설정</summary><div className="mt-4 grid gap-3"><label className="grid gap-2 text-sm">응답 대기 제한 시간<Input aria-label="응답 대기 제한 시간" required value={timeout} onChange={event => setTimeout(event.target.value)} /></label><p className="text-sm text-muted-foreground">예: 100ms, 1.5s. 0보다 크고 최대 {config.inferTimeoutMs}밀리초까지 기다릴 수 있어요. 서비스의 응답 목표({view.desired.spec.slo.percentile}% · {view.desired.spec.slo.targetLatency})와는 별개예요. 요청 크기 한도는 {config.maxRequestBytes}바이트예요.</p></div></details>
        <Button type="submit" disabled={!contract || !view.admission || bf16 || !!previewError}>{mutation.isPending ? "결과를 기다리고 있어요" : "추론 실행"}</Button>
      </fieldset>
      {(validation || previewError) && <p role="alert" className="text-sm">{validation || (bf16 ? "이 입력의 값 종류는 웹 전송을 지원하지 않아요." : "입력값의 종류와 개수를 확인해 주세요.")}</p>}
    </form>
    {contract && <DetailsDialog label="보낼 데이터 확인" title="실제 요청 데이터"><p className="text-sm">선택한 입력 한 건을 보냅니다. 모델과 응답 목표는 서비스 설정을 사용해요. 입력 선택은 데이터 형식 선택이며 실행 장비를 지정하지 않아요.</p><pre className="overflow-auto whitespace-pre-wrap rounded border p-3">{preview || "입력값을 작성하면 전송할 데이터를 확인할 수 있어요."}</pre>{previewError && <p>{previewError}</p>}<SourceJSON title="전송 설정" value={{ path: `POST /api/v1/workloads/${name}/infer`, timeout }} /></DetailsDialog>}
    {(received || sentBody) && <section className="grid gap-4 rounded-xl border p-5" aria-live="polite"><h2 className="font-semibold">{mutation.isPending ? "결과를 기다리고 있어요" : mutation.error?.unknownOutcome ? "실행 결과를 확인하지 못했어요" : mutation.error ? "실행을 완료하지 못했어요" : received ? "실행 결과" : "아직 응답을 받지 못했어요"}</h2>
      <p className="text-sm">화면에서 기다린 시간: {received?.browserRTT == null ? "확인되지 않음" : `${received.browserRTT.toFixed(2)}밀리초`}</p>
      <p className="text-sm">서비스 내부 처리 시간: {completion ? `${(completion.elapsedNanos / 1000000).toFixed(2)}밀리초 · ${statusLabel(completion.outcome)}` : "완료 기록이 아직 확인되지 않았어요."}</p>
      {mutation.error && <p role="alert" className="text-sm">{mutation.error.unknownOutcome ? "요청이 처리됐을 수도 있어요. 요청 기록과 서비스 상태를 먼저 확인하세요. 자동으로 다시 실행하지 않아요." : "자세한 오류를 확인하세요. 작성한 입력은 유지했어요."}</p>}
      {!mutation.error && received && <pre aria-label="추론 결과" className="overflow-auto whitespace-pre-wrap rounded-lg bg-muted p-4">{received.body}</pre>}
      {requestID && submittedAt && observationTo && <Link className="text-sm" href={`/observations?${observationLinkParams}`}>이 요청의 처리 기록 확인</Link>}
      <DetailsDialog label="응답과 시간 측정 자세히" title="실제 응답과 시간 측정">
        <p>화면 대기 시간은 브라우저 왕복이에요. 웹 중계 왕복은 {received?.relayTiming ? `${received.relayTiming}밀리초` : "제공되지 않았어요"}. 이 시간에는 서비스 대기와 실행이 포함되므로 중계 자체 비용이나 서비스 내부 처리 시간으로 사용하지 않아요.</p>
        <p>서비스 내부 처리 시간은 요청 완료 기록으로만 확인해요. 기록이 없으면 0으로 추정하지 않아요.</p>
        <SourceJSON title="응답과 요청 식별 정보" value={{ status: received?.status, submittedAt, requestID, workloadUID: received?.workloadUID, queriedUID: view.desired.metadata.uid, browserRTT: received?.browserRTT, relayTiming: received?.relayTiming, completion }} />
        {requestID && observationTo && <ReadRegion title="요청 완료 기록" query={observation}><p>조회 구간 {submittedAt} ~ {observationTo}. 원본 {config.pageSize}개 안에서 해당 요청과 등록 건의 완료 기록을 연결해요.</p>{!completion && <p>현재 조회한 자료에 완료 기록이 없어요. {observation.data?.nextCursor ? "다음 자료가 있으니 요청 기록 화면에서 더 확인하세요." : "자료를 다시 조회하세요."}</p>}{observation.data?.incompleteWindows?.length ? <p>일부 관측 구간이 불완전해요: {observation.data.incompleteWindows.join(", ")}</p> : null}<SourceJSON title="해당 요청의 원본 이벤트" value={observation.data?.requests.filter(event => event.requestID === requestID)} /></ReadRegion>}
        <h3>실제 응답 본문</h3><pre className="overflow-auto whitespace-pre-wrap">{received?.body ?? "응답 미수신"}</pre><h3>실제 제출 본문 · 큰 정수 원문 유지</h3><pre className="overflow-auto whitespace-pre-wrap">{sentBody}</pre>
      </DetailsDialog>
    </section>}
  </section>
}

export function InferencePage({ name }: { name: string }) {
  const { token } = useSession()
  const expectedUID = useSearchParams().get("uid")
  const query = useChillQuery<WorkloadView>(`v1/workloads/${encodeURIComponent(name)}`, { poll: true })
  const wrongUID = !!expectedUID && !!query.data && expectedUID !== query.data.desired.metadata.uid
  return <div className="page grid gap-6"><header className="grid gap-3"><Link className="text-sm text-muted-foreground" href={`/workloads/${encodeURIComponent(name)}${expectedUID ? `?uid=${encodeURIComponent(expectedUID)}` : ""}`}>서비스 상세로 돌아가기</Link><h1 className="text-2xl font-semibold">{name} 추론</h1><p className="text-muted-foreground">입력 한 건을 보내고 결과를 확인하세요.</p></header>
    <ReadRegion title="추론 입력" query={query}>{token && query.data && <>
      {wrongUID && <p role="alert">이름이 같지만 다른 등록 건이에요. 잘못된 서비스로 보내지 않도록 실행을 멈췄어요. <Link href={`/workloads/${encodeURIComponent(name)}/infer?uid=${encodeURIComponent(query.data.desired.metadata.uid)}`}>현재 등록에서 추론하기</Link></p>}
      <InferenceForm key={query.data.desired.metadata.uid} name={name} view={query.data} wrongUID={wrongUID} />
    </>}</ReadRegion>
  </div>
}

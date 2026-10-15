"use client"

import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useState } from "react"
import { useChillMutation, useChillQuery, useSession, type RelayResponse } from "@/lib/client"
import { QueryGate, SourceJSON, StatusBadge, statusLabel } from "@/components/read-region"
import { Empty, Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { inferenceBody, resourceHref, tensorCount, type SupportedInput, type WorkloadView } from "@/lib/domain"
import { WorkloadState } from "@/features/management"

type Stage = "pick" | "fill" | "confirm" | "result"
type RequestEvents = { requests: { requestID: string; workloadUID?: string; kind: string; elapsedNanos: number; outcome?: string; error?: string; at: string; receivedAt: string }[]; nextCursor?: string; incompleteWindows?: string[] }

// Choose an input → fill and confirm → see the result. Nothing is ever resent automatically.
function InferenceFlow({ name, view, wrongUID }: { name: string; view: WorkloadView; wrongUID: boolean }) {
  const { config } = useSession()
  const mutation = useChillMutation()
  const [stage, setStage] = useState<Stage>("pick")
  const [selected, setSelected] = useState("")
  const [data, setData] = useState<string[]>([])
  const [timeout, setTimeout] = useState(`${config.inferTimeoutMs}ms`)
  const [validation, setValidation] = useState("")
  const [result, setResult] = useState<RelayResponse | null>(null)
  const [sentBody, setSentBody] = useState<string | null>(null)
  const [submittedAt, setSubmittedAt] = useState<string | null>(null)
  const [observationTo, setObservationTo] = useState<string | null>(null)
  const service = resourceHref("workloads", name, view.desired.metadata.uid)
  const back: [string, string] = [service, name]
  // Keep the selected contract's draft when the view refreshes. Never auto-fill or replay.
  const contract: SupportedInput | undefined = view.supportedInputs.find(input => `${input.executionPath}/${input.artifact}` === selected)
  let preview = ""
  let previewError = ""
  try { if (contract) preview = inferenceBody(contract.tensors, data) } catch (error) { previewError = (error as Error).message }
  const bf16 = contract?.tensors.some(t => t.datatype === "BF16")
  const received = result ?? mutation.error
  const requestID = received?.requestID
  const workloadUID = received?.workloadUID ?? view.desired.metadata.uid
  const observationParams = new URLSearchParams({ from: submittedAt ?? "", to: observationTo ?? "", limit: String(config.pageSize) })
  const observationLinkParams = new URLSearchParams(observationParams)
  observationLinkParams.set("workload", name)
  observationLinkParams.set("uid", workloadUID)
  if (requestID) observationLinkParams.set("requestID", requestID)
  const observation = useChillQuery<RequestEvents>(`v1/observations/workloads/${encodeURIComponent(workloadUID)}?${observationParams}`, { enabled: !!requestID && !!submittedAt && !!observationTo && !mutation.isPending })
  const completion = observation.data?.requests.find(event => event.requestID === requestID && event.workloadUID === workloadUID && event.kind === "response_finished")

  function sample() {
    if (!contract) return
    try {
      const counts = contract.tensors.map(tensorCount)
      // A sample is optional assistance, never allocate an unbounded declared tensor.
      if (counts.reduce((sum, count) => sum + count, 0) * 8 > config.maxRequestBytes) throw new Error("예시는 요청 크기 한도 안에서만 만들 수 있어요. 값을 직접 적어 주세요.")
      setData(contract.tensors.map((tensor, index) => JSON.stringify(Array(counts[index]).fill(tensor.datatype === "BOOL" ? false : tensor.datatype === "BYTES" ? "" : 0))))
      setValidation("")
    } catch (error) { setValidation((error as Error).message) }
  }
  async function send() {
    setValidation("")
    try {
      if (!contract) throw new Error("측정이 끝난 입력을 골라 주세요.")
      if (!view.admission || wrongUID) throw new Error("지금 이 서비스가 요청을 받을 수 없어요. 준비 상태를 확인해 주세요.")
      if (!timeout.trim()) throw new Error("응답을 기다릴 시간과 단위를 적어 주세요.")
      const body = inferenceBody(contract.tensors, data)
      if (new TextEncoder().encode(body).byteLength > config.maxRequestBytes) throw new Error(`요청이 허용 크기(${config.maxRequestBytes}바이트)를 넘었어요.`)
      setResult(null); setSentBody(body); setSubmittedAt(new Date().toISOString()); setObservationTo(null); setStage("result")
      try {
        setResult(await mutation.mutateAsync({ path: `v1/workloads/${encodeURIComponent(name)}/infer`, method: "POST", body, timeout }))
      } finally { setObservationTo(new Date(Date.now() + 1000).toISOString()) }
    } catch (error) {
      // Mutation failures keep the response body/IDs and unknown-outcome classification in mutation.error.
      if (!(error && typeof error === "object" && "unknownOutcome" in error)) setValidation((error as Error).message)
    }
  }
  const problem = (validation || (stage === "fill" && previewError && data.some(Boolean))) && <p role="alert" className="text-sm">{validation || (bf16 ? "이 입력의 값 종류는 웹에서 보낼 수 없어요." : previewError)}</p>
  const lost = selected && !contract && <p role="alert">고른 입력이 최신 목록에서 없어졌어요. 적은 내용은 그대로 있어요. 다른 입력을 골라야 보낼 수 있어요.</p>

  if (stage === "result") {
    const unknown = mutation.error?.unknownOutcome
    return <Screen back={back} status={<StatusBadge tone={mutation.isPending ? "info" : mutation.error ? unknown ? "warn" : "fail" : "ok"}>{mutation.isPending ? "기다리는 중" : unknown ? "결과 미확인" : mutation.error ? "실패" : "완료"}</StatusBadge>}
      title={mutation.isPending ? "결과를 기다리고 있어요" : unknown ? "실행 결과를 확인하지 못했어요" : mutation.error ? "실행을 끝내지 못했어요" : received ? "추론이 끝났어요" : "아직 응답을 받지 못했어요"}
      lede={unknown ? "요청이 처리됐을 수도 있어요. 요청 기록과 서비스 상태를 먼저 확인해 주세요. 자동으로 다시 실행하지 않아요." : mutation.error ? "자세한 오류를 확인해 주세요. 적은 입력은 그대로 있어요." : undefined}
      action={!mutation.isPending && <Button variant="outline" onClick={() => { mutation.reset(); setResult(null); setSentBody(null); setStage("fill") }}>입력 고쳐서 다시 보내기</Button>}>
      <Facts items={[
        ["화면에서 기다린 시간", received?.browserRTT == null ? "확인되지 않음" : `${received.browserRTT.toFixed(2)}밀리초`],
        ["서비스 내부 처리 시간", completion ? `${(completion.elapsedNanos / 1000000).toFixed(2)}밀리초 · ${statusLabel(completion.outcome)}` : "완료 기록을 아직 못 찾았어요"],
      ]} />
      <Rows>
        {!mutation.error && received && <RowDialog label="결과 데이터 보기" title="추론 결과"><pre aria-label="추론 결과" className="source-json">{received.body}</pre></RowDialog>}
        {requestID && submittedAt && observationTo && <Row href={`/observations?${observationLinkParams}`} label="이 요청의 처리 기록 확인" />}
        <RowDialog label="응답과 시간 측정 자세히" title="실제 응답과 시간 측정">
          <p>화면 대기 시간은 브라우저 왕복이에요. 웹 중계 왕복은 {received?.relayTiming ? `${received.relayTiming}밀리초` : "제공되지 않았어요"}. 이 시간에는 서비스 대기와 실행이 들어 있어서 중계 자체 비용이나 서비스 내부 처리 시간으로 쓰지 않아요.</p>
          <p>서비스 내부 처리 시간은 요청 완료 기록으로만 확인해요. 기록이 없으면 0으로 짐작하지 않아요.</p>
          <SourceJSON inline title="응답과 요청 식별 정보" value={{ status: received?.status, submittedAt, requestID, workloadUID: received?.workloadUID, queriedUID: view.desired.metadata.uid, browserRTT: received?.browserRTT, relayTiming: received?.relayTiming, completion }} />
          {requestID && observationTo && <QueryGate query={observation}>
            <p>조회 구간 {submittedAt} ~ {observationTo}. 원본 {config.pageSize}개 안에서 이 요청과 등록 건의 완료 기록을 연결해요.</p>
            {!completion && <p>지금 조회한 자료에 완료 기록이 없어요. {observation.data?.nextCursor ? "다음 자료가 있으니 요청 기록 화면에서 더 확인해 주세요." : "잠시 뒤 다시 조회해 주세요."}</p>}
            {observation.data?.incompleteWindows?.length ? <p>일부 관측 구간이 불완전해요: {observation.data.incompleteWindows.join(", ")}</p> : null}
            <SourceJSON inline title="이 요청의 원본 이벤트" value={observation.data?.requests.filter(event => event.requestID === requestID)} />
          </QueryGate>}
          <h3>실제 응답 본문</h3><pre className="source-json">{received?.body ?? "응답 미수신"}</pre>
          <h3>실제 보낸 본문 · 큰 정수 원문 유지</h3><pre className="source-json">{sentBody}</pre>
        </RowDialog>
      </Rows>
    </Screen>
  }

  if (stage === "confirm" && contract) return <Screen back={back} status={<span className="text-sm text-muted-foreground">3 / 3</span>} title="이대로 보낼까요?" lede="입력 한 건을 보내요. 모델과 응답 목표는 서비스 설정을 따르고, 실행할 장비는 운영 판단이 정해요."
    action={<div className="flex gap-3"><Button type="button" variant="outline" className="flex-1" disabled={mutation.isPending} onClick={() => { setValidation(""); setStage("fill") }}>이전</Button><Button className="flex-[2]" disabled={mutation.isPending || !view.admission || wrongUID || !!bf16 || !!previewError} onClick={() => void send()}>추론 보내기</Button></div>}>
    <Facts items={[["서비스", name], ["입력", contract.artifact], ["값 개수", `${contract.tensors.reduce((sum, t) => sum + tensorCount(t), 0)}개`], ["응답 대기 시간", timeout], ["요청 크기", `${new TextEncoder().encode(preview).byteLength}바이트`]]} />
    <Rows><RowDialog label="보낼 데이터 확인" title="실제 요청 데이터"><pre className="source-json">{preview}</pre><SourceJSON inline title="전송 설정" value={{ path: `POST /api/v1/workloads/${name}/infer`, timeout }} /></RowDialog></Rows>
    {!view.admission && <p role="alert">지금은 이 서비스가 새 요청을 받을 수 없어요.</p>}
    {problem}
  </Screen>

  if (stage !== "pick" && contract) return <Screen back={back} status={<span className="text-sm text-muted-foreground">2 / 3</span>} title="보낼 값을 채워 주세요" lede="아래 크기는 한 번의 입력 크기예요. 여러 요청을 묶는 축은 보내지 않아요."
    action={<div className="flex gap-3"><Button type="button" variant="outline" className="flex-1" onClick={() => { setValidation(""); setStage("pick") }}>이전</Button><Button className="flex-[2]" disabled={!!bf16} onClick={() => {
      try { inferenceBody(contract.tensors, data); setValidation(""); setStage("confirm") } catch (error) { setValidation((error as Error).message) }
    }}>다음</Button></div>}>
    <div className="grid gap-5">
      {contract.tensors.map((tensor, index) => <label key={`${selected}/${tensor.name}`} className="grid gap-2 text-sm">
        <span className="font-semibold">{tensor.name} · {tensor.datatype === "BOOL" ? "참·거짓" : tensor.datatype === "BYTES" ? "문자열" : /INT/.test(tensor.datatype) ? "정수" : "소수"} {tensorCount(tensor)}개 (크기 {tensor.sampleShape.join(" × ")})</span>
        <textarea aria-label={`${tensor.name} 입력값`} required rows={5} className="w-full rounded border bg-background p-3 font-mono" value={data[index] ?? ""} onChange={event => setData(previous => previous.map((value, i) => i === index ? event.target.value : value))} />
        <span className="text-muted-foreground">{tensor.datatype === "BYTES" ? '문자열을 목록으로 적어 주세요. 예: ["text", "value"]' : tensor.datatype === "BOOL" ? "참·거짓 값을 목록으로 적어 주세요. 예: [true, false]" : tensor.datatype === "BF16" ? "이 값 종류는 지금 웹에서 보낼 수 없어요. 다른 입력을 골라 주세요." : "숫자를 한 줄 목록으로 적어 주세요. 예: [0, 1, 2]. 큰 64비트 정수도 적은 그대로 보내요."}</span>
      </label>)}
      <Button type="button" variant="outline" className="self-start" disabled={bf16} onClick={sample}>입력 예시 채우기</Button>
    </div>
    <Rows>
      <RowDialog label="응답 대기 시간" value={timeout} title="응답 대기 시간"><label className="grid gap-2 text-sm">응답 대기 제한 시간<Input aria-label="응답 대기 제한 시간" required value={timeout} onChange={event => setTimeout(event.target.value)} /></label><p className="text-sm text-muted-foreground">예: 100ms, 1.5s. 0보다 크고 최대 {config.inferTimeoutMs}밀리초까지 기다릴 수 있어요. 서비스의 응답 목표({view.desired.spec.slo.percentile}% · {view.desired.spec.slo.targetLatency})와는 별개예요. 요청 크기 한도는 {config.maxRequestBytes}바이트예요.</p></RowDialog>
      <RowDialog label="고른 입력 자세히" title={`${contract.artifact} 입력과 측정 근거`}><SourceJSON inline title="지원 입력과 측정 원본" value={contract} /><Link href={resourceHref("workloads", name, view.desired.metadata.uid, "evidence")}>측정 근거 보기</Link></RowDialog>
    </Rows>
    {problem}
  </Screen>

  const ready = view.admission && !wrongUID && view.supportedInputs.length > 0
  return <Screen back={back} status={<span className="text-sm text-muted-foreground">1 / 3</span>} title={ready ? "어떤 입력으로 보낼까요?" : "추론을 시작하려면 준비가 필요해요"}
    lede={ready ? "측정이 끝난 입력만 고를 수 있어요." : view.supportedInputs.length ? "지금은 새 요청을 받을 수 없어요. 준비 상태를 확인해 주세요." : "이 모델의 실행 조건에 맞는 측정 결과가 아직 없어요. 측정이 끝나고 서비스가 준비되면 입력을 고를 수 있어요."}
    action={!ready && <Button asChild><Link href={view.supportedInputs.length ? service : "/measurements"}>{view.supportedInputs.length ? "서비스 상태 보기" : "측정 결과 찾기"}</Link></Button>}>
    {lost}
    {ready ? <Rows>{view.supportedInputs.map((input, index) => {
      const key = `${input.executionPath}/${input.artifact}`
      return <Row key={index} label={input.artifact} hint={`${input.tensors.map(t => t.name).join(", ")} · ${input.executionPath}`} value={key === selected ? "선택됨" : undefined} onClick={() => {
        if (key !== selected) { setSelected(key); setData(input.tensors.map(() => "")) }
        setValidation(""); setStage("fill")
      }} />
    })}</Rows> : <Empty title={view.supportedInputs.length ? "서비스가 아직 요청을 받지 않아요" : "고를 수 있는 입력이 없어요"} />}
    <Rows><RowDialog label="모델과 준비 상태" title={`${name} 모델과 준비 상태`}><WorkloadState view={view} /></RowDialog></Rows>
  </Screen>
}

export function InferencePage({ name }: { name: string }) {
  const expectedUID = useSearchParams().get("uid")
  const query = useChillQuery<WorkloadView>(`v1/workloads/${encodeURIComponent(name)}`, { poll: true })
  const view = [401, 403].includes(query.error?.status ?? 0) ? undefined : query.data
  const wrongUID = !!expectedUID && !!view && expectedUID !== view.desired.metadata.uid
  const back: [string, string] = [`/workloads/${encodeURIComponent(name)}${expectedUID ? `?uid=${encodeURIComponent(expectedUID)}` : ""}`, name]
  if (view && wrongUID) return <Screen back={back} status={<StatusBadge tone="warn">작업 멈춤</StatusBadge>} title="이름이 같은 다른 등록이에요" lede="잘못된 서비스로 보내지 않도록 실행을 멈췄어요." action={<Button asChild><Link href={resourceHref("workloads", name, view.desired.metadata.uid, "infer")}>현재 등록에서 추론하기</Link></Button>} />
  if (!view) return <Screen back={back} title={`${name} 추론`}><QueryGate query={query}>{null}</QueryGate></Screen>
  return <InferenceFlow key={view.desired.metadata.uid} name={name} view={view} wrongUID={wrongUID} />
}

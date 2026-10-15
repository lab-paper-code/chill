"use client"

import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useMemo, useState } from "react"
import { useChillMutation, useChillQuery, useSession, type RelayResponse } from "@/lib/client"
import { QueryGate, SourceJSON, StatusBadge, statusLabel } from "@/components/read-region"
import { Empty, Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { rawInferenceBody, rawOutputs, resourceHref, type WorkloadView } from "@/lib/domain"
import { WorkloadState } from "@/features/management"

type Stage = "fill" | "confirm" | "result"
type RequestEvents = { requests: { requestID: string; workloadUID?: string; kind: string; elapsedNanos: number; outcome?: string; error?: string; at: string; receivedAt: string }[]; nextCursor?: string; incompleteWindows?: string[] }
const encoder = new TextEncoder(), decoder = new TextDecoder()

// Fill the applied preprocessor's raw fields → confirm → see the result. Nothing is ever resent automatically.
function InferenceFlow({ name, view, wrongUID }: { name: string; view: WorkloadView; wrongUID: boolean }) {
  const { config } = useSession()
  const mutation = useChillMutation()
  const [stage, setStage] = useState<Stage>("fill")
  const [values, setValues] = useState<Record<string, Uint8Array>>({})
  const [timeout, setTimeout] = useState(`${config.inferTimeoutMs}ms`)
  const [validation, setValidation] = useState("")
  const [result, setResult] = useState<RelayResponse | null>(null)
  const [submittedAt, setSubmittedAt] = useState<string | null>(null)
  const [observationTo, setObservationTo] = useState<string | null>(null)
  const service = resourceHref("workloads", name, view.desired.metadata.uid)
  const back: [string, string] = [service, name]
  // The raw contract is the applied preprocessor's; the draft is kept by field name when the view refreshes.
  const preprocessor = view.appliedDefinition?.preprocessor
  const fields = useMemo(() => preprocessor?.input ?? [], [preprocessor])
  const [preview, previewError] = useMemo(() => {
    try { return [rawInferenceBody(fields, values), ""] } catch (error) { return ["", (error as Error).message] }
  }, [fields, values])
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

  const sent = { path: `POST /api/v1/workloads/${name}/infer`, timeout, inputs: fields.map(field => ({ name: field.name, datatype: "BYTES", bytes: values[field.name]?.length ?? 0 })) }
  async function send() {
    setValidation("")
    try {
      if (!preprocessor) throw new Error("적용된 전처리기가 아직 없어요.")
      if (!view.admission || wrongUID) throw new Error("지금 이 서비스가 요청을 받을 수 없어요. 준비 상태를 확인해 주세요.")
      if (!timeout.trim()) throw new Error("응답을 기다릴 시간과 단위를 적어 주세요.")
      const body = rawInferenceBody(fields, values)
      if (encoder.encode(body).byteLength > config.maxRequestBytes) throw new Error(`요청이 허용 크기(${config.maxRequestBytes}바이트)를 넘었어요.`)
      setResult(null); setSubmittedAt(new Date().toISOString()); setObservationTo(null); setStage("result")
      try {
        setResult(await mutation.mutateAsync({ path: `v1/workloads/${encodeURIComponent(name)}/infer`, method: "POST", body, timeout }))
      } finally { setObservationTo(new Date(Date.now() + 1000).toISOString()) }
    } catch (error) {
      // Mutation failures keep the response body/IDs and unknown-outcome classification in mutation.error.
      if (!(error && typeof error === "object" && "unknownOutcome" in error)) setValidation((error as Error).message)
    }
  }
  const problem = (validation || (stage === "fill" && previewError && Object.keys(values).length > 0)) && <p role="alert" className="text-sm">{validation || previewError}</p>

  if (stage === "result") {
    const unknown = mutation.error?.unknownOutcome
    return <Screen back={back} status={<StatusBadge tone={mutation.isPending ? "info" : mutation.error ? unknown ? "warn" : "fail" : "ok"}>{mutation.isPending ? "기다리는 중" : unknown ? "결과 미확인" : mutation.error ? "실패" : "완료"}</StatusBadge>}
      title={mutation.isPending ? "결과를 기다리고 있어요" : unknown ? "실행 결과를 확인하지 못했어요" : mutation.error ? "실행을 끝내지 못했어요" : received ? "추론이 끝났어요" : "아직 응답을 받지 못했어요"}
      lede={unknown ? "요청이 처리됐을 수도 있어요. 요청 기록과 서비스 상태를 먼저 확인해 주세요. 자동으로 다시 실행하지 않아요." : mutation.error ? "자세한 오류를 확인해 주세요. 적은 입력은 그대로 있어요." : undefined}
      action={!mutation.isPending && <Button variant="outline" onClick={() => { mutation.reset(); setResult(null); setStage("fill") }}>입력 고쳐서 다시 보내기</Button>}>
      <Facts items={[
        ["화면에서 기다린 시간", received?.browserRTT == null ? "확인되지 않음" : `${received.browserRTT.toFixed(2)}밀리초`],
        ["서비스 내부 처리 시간", completion ? `${(completion.elapsedNanos / 1000000).toFixed(2)}밀리초 · ${statusLabel(completion.outcome)}` : "완료 기록을 아직 못 찾았어요"],
      ]} />
      {!mutation.error && received && <Facts label="결과" items={rawOutputs(preprocessor?.output ?? [], received.data).map(output => [output.name,
        // eslint-disable-next-line @next/next/no-img-element -- a response data URL; next/image optimizes nothing here.
        output.value === undefined ? "응답에 없어요" : output.format === "JPEG" ? <img alt={`${output.name} 결과 이미지`} className="max-w-full rounded border" src={`data:image/jpeg;base64,${output.value}`} /> : <span className="whitespace-pre-wrap">{output.value}</span>])} />}
      <Rows>
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
          <SourceJSON inline title="실제 보낸 요청" value={sent} />
        </RowDialog>
      </Rows>
    </Screen>
  }

  const ready = view.admission && !wrongUID && !!fields.length && view.supportedInputs.length > 0
  if (!ready) return <Screen back={back} title="추론을 시작하려면 준비가 필요해요"
    lede={view.supportedInputs.length ? "지금은 새 요청을 받을 수 없어요. 준비 상태를 확인해 주세요." : "이 모델의 실행 조건에 맞는 측정 결과가 아직 없어요. 측정이 끝나고 서비스가 준비되면 입력을 보낼 수 있어요."}
    action={<Button asChild><Link href={view.supportedInputs.length ? service : "/measurements"}>{view.supportedInputs.length ? "서비스 상태 보기" : "측정 결과 찾기"}</Link></Button>}>
    <Empty title={view.supportedInputs.length ? "서비스가 아직 요청을 받지 않아요" : "보낼 수 있는 입력이 없어요"} />
    <Rows><RowDialog label="모델과 준비 상태" title={`${name} 모델과 준비 상태`}><WorkloadState view={view} /></RowDialog></Rows>
  </Screen>

  if (stage === "confirm") return <Screen back={back} status={<span className="text-sm text-muted-foreground">2 / 2</span>} title="이대로 보낼까요?" lede="입력 한 건을 보내요. 모델, 전처리기, 응답 목표는 서비스 설정을 따르고, 실행할 장비는 운영 판단이 정해요."
    action={<div className="flex gap-3"><Button type="button" variant="outline" className="flex-1" disabled={mutation.isPending} onClick={() => { setValidation(""); setStage("fill") }}>이전</Button><Button className="flex-[2]" disabled={mutation.isPending || !view.admission || wrongUID || !!previewError} onClick={() => void send()}>추론 보내기</Button></div>}>
    <Facts items={[["서비스", name], ["전처리기", preprocessor!.name], ...fields.map((field): [string, string] => [field.name, `${field.format} · ${values[field.name]?.length ?? 0}바이트`]), ["응답 대기 시간", timeout], ["요청 크기", `${encoder.encode(preview).byteLength}바이트`]]} />
    <Rows><RowDialog label="보낼 데이터 확인" title="실제 요청 데이터"><SourceJSON inline title="전송 설정" value={sent} /></RowDialog></Rows>
    {!view.admission && <p role="alert">지금은 이 서비스가 새 요청을 받을 수 없어요.</p>}
    {problem}
  </Screen>

  return <Screen back={back} status={<span className="text-sm text-muted-foreground">1 / 2</span>} title="보낼 값을 채워 주세요" lede={`${preprocessor!.name} 전처리기가 받는 원본 입력 한 건이에요. 여러 요청을 묶는 일은 서비스가 해요.`}
    action={<Button className="w-full" onClick={() => {
      try { rawInferenceBody(fields, values); setValidation(""); setStage("confirm") } catch (error) { setValidation((error as Error).message) }
    }}>다음</Button>}>
    <div className="grid gap-5">
      {fields.map(field => <label key={field.name} className="grid gap-2 text-sm">
        <span className="font-semibold">{field.name} · {field.format === "JPEG" ? "JPEG 이미지" : "UTF-8 글"} (최대 {field.maxBytes}바이트)</span>
        {field.format === "JPEG"
          ? <Input type="file" accept="image/jpeg" aria-label={`${field.name} 파일`} onChange={async event => { const file = event.currentTarget.files?.[0]; if (!file) return; const bytes = new Uint8Array(await file.arrayBuffer()); setValues(previous => ({ ...previous, [field.name]: bytes })) }} />
          : <textarea aria-label={`${field.name} 입력값`} required rows={3} className="w-full rounded border bg-background p-3" value={values[field.name] ? decoder.decode(values[field.name]) : ""} onChange={event => { const bytes = encoder.encode(event.target.value); setValues(previous => ({ ...previous, [field.name]: bytes })) }} />}
        {field.format === "JPEG" && values[field.name] && <span className="text-muted-foreground">{values[field.name].length}바이트 파일을 골랐어요</span>}
      </label>)}
    </div>
    <Rows>
      <RowDialog label="응답 대기 시간" value={timeout} title="응답 대기 시간"><label className="grid gap-2 text-sm">응답 대기 제한 시간<Input aria-label="응답 대기 제한 시간" required value={timeout} onChange={event => setTimeout(event.target.value)} /></label><p className="text-sm text-muted-foreground">예: 100ms, 1.5s. 0보다 크고 최대 {config.inferTimeoutMs}밀리초까지 기다릴 수 있어요. 서비스의 응답 목표({view.desired.spec.slo.percentile}% · {view.desired.spec.slo.targetLatency})와는 별개예요. 요청 크기 한도는 {config.maxRequestBytes}바이트예요.</p></RowDialog>
      <RowDialog label="입력 계약과 측정 근거" title={`${name} 입력 계약과 측정 근거`}><SourceJSON inline title="적용된 전처리기와 측정된 실행 경로" value={{ preprocessor, supportedInputs: view.supportedInputs }} /><Link href={resourceHref("workloads", name, view.desired.metadata.uid, "evidence")}>측정 근거 보기</Link></RowDialog>
      <RowDialog label="모델과 준비 상태" title={`${name} 모델과 준비 상태`}><WorkloadState view={view} /></RowDialog>
    </Rows>
    {problem}
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

"use client"

import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { ApiError, useChillMutation, useChillQuery, type RelayResponse } from "@/lib/client"
import { DetailsDialog, QueryGate, SourceJSON, SourceExport, StatusBadge, statusLabel } from "@/components/read-region"
import { Empty, Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { datatypes, positiveIntegers, resourceHref as href, sloTarget as target, workloadSpec, type ModelResource, type ModelList, type ModelSpec, type ModelArtifact, type ExecutionPath, type TensorContract, type WorkloadResource, type WorkloadSpec, type WorkloadView } from "@/lib/domain"
import { ServiceSection, serviceSections, useServiceSignals, type ServiceSectionId } from "@/features/service-hub"

type Kind = "models" | "workloads"
type Resource = ModelResource | WorkloadView
const unwrap = (resource: Resource): ModelResource | WorkloadResource => "desired" in resource ? resource.desired : resource
const kindLabel = (kind: Kind) => kind === "models" ? "모델" : "서비스"
const emptyTensor = (): TensorContract => ({ name: "", datatype: "FP32", sampleShape: [1] })
const emptyArtifact = (): ModelArtifact => ({ name: "", format: "", source: { image: "", architecture: "", path: "" }, input: { tensors: [emptyTensor()] } })
const emptyPath = (): ExecutionPath => ({ name: "", artifact: "", runtime: { family: "", backend: "" } })
// Services that use a model by name, by applied definition, or as an in-flight change target.
const connected = (list: WorkloadView[] | undefined, name: string, uid?: string) => list?.filter(w => w.desired.spec.modelRef.name === name || (!!uid && [w.appliedDefinition?.model.uid, w.desired.status?.appliedDefinition?.modelRef.uid, w.operation?.targetModelUID, w.desired.status?.operation?.targetModelUID].includes(uid)))
const select = "rounded border bg-background p-2"

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="grid gap-2 text-sm">{label}{children}</label>
}
function Text({ label, value, set, required = true }: { label: string; value: string; set: (value: string) => void; required?: boolean }) {
  return <Field label={label}><Input aria-label={label} value={value} onChange={event => set(event.target.value)} required={required} /></Field>
}
// The native contract allows other declarations too; suggestions never restrict existing values.
function Choice({ label, value, set, options }: { label: string; value: string; set: (value: string) => void; options: readonly string[] }) {
  const [custom, setCustom] = useState(!!value && !options.includes(value))
  const isCustom = custom || (!!value && !options.includes(value))
  return <div className="grid gap-2"><Field label={label}><select required aria-label={label} className={select} value={isCustom ? "__custom" : value} onChange={event => {
    const other = event.target.value === "__custom"; setCustom(other); if (!other) set(event.target.value)
  }}><option value="">선택하세요</option>{options.map(option => <option key={option}>{option}</option>)}<option value="__custom">다른 값 직접 입력</option></select></Field>{isCustom && <Text label={`${label} 직접 입력`} value={value} set={set} />}</div>
}
// Pick one of the existing items instead of typing it.
function Choices({ label, value, set, options }: { label: string; value: string; set: (value: string) => void; options: { value: string; label: ReactNode; hint?: ReactNode }[] }) {
  return <fieldset className="rows-group"><legend className="rows-label">{label}</legend><ul className="rows">{options.map(option => <li key={option.value}><label className="row cursor-pointer">
    <input type="radio" required name={label} value={option.value} checked={value === option.value} onChange={() => set(option.value)} />
    <span className="row-main"><span className="row-text">{option.label}</span>{option.hint && <span className="row-hint">{option.hint}</span>}</span>
  </label></li>)}</ul></fieldset>
}
function IntegerList({ label, value, set }: { label: string; value: number[]; set: (value: number[]) => void }) {
  const serialized = value.join(", ")
  const [text, setText] = useState(serialized)
  const [previous, setPrevious] = useState(serialized)
  const input = useRef<HTMLInputElement>(null)
  if (previous !== serialized) {
    setPrevious(serialized)
    try { if (positiveIntegers(text).join(",") !== value.join(",")) setText(serialized) } catch { setText(serialized) }
  }
  useEffect(() => { input.current?.setCustomValidity("") }, [serialized])
  return <Field label={label}><Input ref={input} aria-label={label} required value={text} onChange={event => {
    setText(event.target.value)
    try { set(positiveIntegers(event.target.value)); event.target.setCustomValidity("") } catch (error) { event.target.setCustomValidity((error as Error).message) }
  }} /></Field>
}
function Integer({ label, value, set }: { label: string; value: number; set: (value: number) => void }) {
  return <Field label={label}><Input aria-label={label} type="number" required min={1} max={2147483647} step={1} value={Number.isNaN(value) ? "" : value} onChange={event => set(event.target.valueAsNumber)} /></Field>
}
function DurationField({ value, set }: { value: string; set: (value: string) => void }) {
  // This only splits ordinary display values; compound durations remain untouched for the API.
  const simple = /^(\d+(?:\.\d+)?)(ns|us|µs|μs|ms|s|m|h)$/.exec(value)
  const [advanced, setAdvanced] = useState(!simple)
  const unit = simple?.[2] ?? "ms"
  return <div className="grid gap-3">{advanced ? <Text label="응답 목표 시간 · 원래 표현" value={value} set={set} /> : <div className="grid grid-cols-[1fr_8rem] gap-3"><Field label="응답 목표 시간"><Input aria-label="응답 목표 시간" type="number" min="0" step="any" required value={simple?.[1] ?? ""} onChange={event => set(`${event.target.value}${unit}`)} /></Field><Field label="시간 단위"><select aria-label="시간 단위" className={select} value={unit} onChange={event => set(`${simple?.[1] ?? ""}${event.target.value}`)}>{[["ms", "밀리초"], ["s", "초"], ["m", "분"], ["h", "시간"], ["us", "마이크로초"], ["ns", "나노초"], ["µs", "마이크로초 (µs)"], ["μs", "마이크로초 (μs)"]].map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field></div>}
    <label className="flex gap-2 text-sm text-muted-foreground"><input type="checkbox" checked={advanced} disabled={!simple && advanced} onChange={event => setAdvanced(event.target.checked)} />복합 시간 직접 입력 (예: 1m30s)</label>
    <p className="text-sm text-muted-foreground">입력한 시간은 그대로 저장해요. 0보다 커야 하고, 최종 허용 범위는 저장할 때 확인해요.</p>
  </div>
}
function Confirm({ title, children, busy, disabled = false, accept, cancel }: { title: string; children: ReactNode; busy: boolean; disabled?: boolean; accept: () => void; cancel: () => void }) {
  return <Dialog open onOpenChange={open => { if (!open && !busy) cancel() }}><DialogContent showCloseButton={!busy} className="max-h-[85dvh] overflow-y-auto sm:max-w-2xl" onEscapeKeyDown={event => { if (busy) event.preventDefault() }} onPointerDownOutside={event => { if (busy) event.preventDefault() }}>
    <DialogHeader><DialogTitle>{title}</DialogTitle><DialogDescription>지울 내용과 영향을 확인한 뒤 확정해 주세요.</DialogDescription></DialogHeader><div className="grid gap-4">{children}</div>
    <div className="mt-4 flex gap-3"><Button autoFocus variant="outline" disabled={busy} onClick={cancel}>취소</Button><Button variant="destructive" disabled={busy || disabled} onClick={accept}>{busy ? "삭제하고 있어요" : "삭제하기"}</Button></div>
  </DialogContent></Dialog>
}

type ModelStep = "artifacts" | "inputs" | "paths"
function ModelFields({ spec, set, step, models }: { spec: ModelSpec; set: (spec: ModelSpec) => void; step: ModelStep; models: ModelResource[] }) {
  const [artifactIndex, setArtifactIndex] = useState(0)
  const [pathIndex, setPathIndex] = useState(0)
  const index = Math.min(artifactIndex, Math.max(0, spec.artifacts.length - 1))
  const pi = Math.min(pathIndex, Math.max(0, spec.executionPaths.length - 1))
  const row = spec.artifacts[index]
  const execution = spec.executionPaths[pi]
  function artifact(change: Partial<ModelArtifact>) { set({ ...spec, artifacts: spec.artifacts.map((a, i) => i === index ? { ...a, ...change } : a) }) }
  function path(change: Partial<ExecutionPath>) { set({ ...spec, executionPaths: spec.executionPaths.map((p, i) => i === pi ? { ...p, ...change } : p) }) }
  return <div className="grid gap-5">
    {step !== "paths" && <><Field label="설정할 모델 파일"><select aria-label="설정할 모델 파일" className={select} value={index} onChange={event => setArtifactIndex(Number(event.target.value))}>{spec.artifacts.map((a, i) => <option key={i} value={i}>{a.name || `파일 ${i + 1}`}</option>)}</select></Field>{!row && <p>모델 파일을 추가해 주세요.</p>}</>}
    {step === "artifacts" && <>
      {models.some(m => m.spec.artifacts.length) && <Field label="등록된 모델의 파일 가져오기"><select aria-label="등록된 모델의 파일 가져오기" className={select} value="" onChange={event => {
        const [mi, ai] = event.target.value.split("/").map(Number)
        const copied = models[mi]?.spec.artifacts[ai]
        if (copied) { setArtifactIndex(spec.artifacts.length); set({ ...spec, artifacts: [...spec.artifacts, structuredClone(copied)] }) }
      }}><option value="">가져올 파일 고르기</option>{models.flatMap((m, mi) => m.spec.artifacts.map((a, ai) => <option key={`${mi}/${ai}`} value={`${mi}/${ai}`}>{m.metadata.name} · {a.name}</option>))}</select></Field>}
      {row && <div key={index} className="grid gap-4"><Text label="파일 이름" value={row.name} set={name => artifact({ name })} /><Choice label="파일 형식" value={row.format} set={format => artifact({ format })} options={["onnx", "rknn"]} /><Text label="모델 파일 보관 주소" value={row.source.image} set={image => artifact({ source: { ...row.source, image } })} /><p className="text-sm text-muted-foreground">변하지 않는 정확한 버전의 주소를 적어 주세요. 예: registry.example/model@sha256:…</p><Choice label="대상 장비 종류" value={row.source.architecture} set={architecture => artifact({ source: { ...row.source, architecture } })} options={["amd64", "arm64"]} /><Text label="보관 이미지 안의 파일 위치" value={row.source.path} set={path => artifact({ source: { ...row.source, path } })} /><p className="text-sm text-muted-foreground">예: /model/model.onnx</p>
        <label className="flex gap-2 text-sm"><input type="checkbox" checked={!!row.derivation} onChange={event => artifact({ derivation: event.target.checked ? { sourceArtifact: "", targetPlatform: "", declaredBatchSizes: [1], conversion: { tool: "", version: "" } } : undefined })} />다른 파일에서 변환했어요</label>
        {row.derivation && <><Field label="변환 전 파일"><select required aria-label="변환 전 파일" className={select} value={row.derivation.sourceArtifact} onChange={event => artifact({ derivation: { ...row.derivation!, sourceArtifact: event.target.value } })}><option value="">원본 파일 선택</option>{spec.artifacts.filter((_, i) => i !== index).map((a, i) => <option key={i} value={a.name}>{a.name || "이름 없는 파일"}</option>)}</select></Field><Text label="변환 대상 장비" value={row.derivation.targetPlatform} set={targetPlatform => artifact({ derivation: { ...row.derivation!, targetPlatform } })} /><IntegerList label="지원 묶음 크기 · 개수, 쉼표로 구분" value={row.derivation.declaredBatchSizes} set={declaredBatchSizes => artifact({ derivation: { ...row.derivation!, declaredBatchSizes } })} /><Text label="변환 도구" value={row.derivation.conversion.tool} set={tool => artifact({ derivation: { ...row.derivation!, conversion: { ...row.derivation!.conversion, tool } } })} /><Text label="변환 도구 버전" value={row.derivation.conversion.version} set={version => artifact({ derivation: { ...row.derivation!, conversion: { ...row.derivation!.conversion, version } } })} /></>}
        <Button type="button" variant="outline" onClick={() => set({ ...spec, artifacts: spec.artifacts.filter((_, i) => i !== index) })}>이 파일 빼기</Button></div>}
      <Button type="button" variant="outline" onClick={() => { setArtifactIndex(spec.artifacts.length); set({ ...spec, artifacts: [...spec.artifacts, emptyArtifact()] }) }}>새 모델 파일 추가</Button>
    </>}
    {step === "inputs" && row && <>
      <p className="text-sm text-muted-foreground">한 번의 입력 크기만 적고, 여러 요청을 묶는 축은 빼 주세요. 크기 예: 3, 224, 224 (채널, 높이, 너비).</p>
      {(row.input?.tensors ?? []).map((tensor, ti) => {
        const tensorSet = (change: Partial<TensorContract>) => artifact({ input: { tensors: row.input!.tensors.map((t, i) => i === ti ? { ...t, ...change } : t) } })
        return <fieldset key={`${index}/${ti}`} className="grid gap-4 rounded-lg border p-4"><legend>입력 {ti + 1}</legend><Text label={`입력 ${ti + 1} 이름`} value={tensor.name} set={name => tensorSet({ name })} /><Field label={`입력 ${ti + 1} 값 종류`}><select aria-label={`입력 ${ti + 1} 값 종류`} className={select} value={tensor.datatype} onChange={event => tensorSet({ datatype: event.target.value as TensorContract["datatype"] })}>{datatypes.map(type => <option key={type} value={type}>{type === "BYTES" ? "문자열 (BYTES)" : type === "BOOL" ? "참·거짓 (BOOL)" : type}</option>)}</select></Field><IntegerList label={`입력 ${ti + 1} 크기 · 각 축의 값 개수`} value={tensor.sampleShape} set={sampleShape => tensorSet({ sampleShape })} /><Button type="button" variant="outline" onClick={() => artifact({ input: { tensors: row.input!.tensors.filter((_, i) => i !== ti) } })}>이 입력 빼기</Button></fieldset>
      })}<Button type="button" variant="outline" onClick={() => artifact({ input: { tensors: [...(row.input?.tensors ?? []), emptyTensor()] } })}>입력 추가</Button>
    </>}
    {step === "paths" && <><Field label="설정할 실행 방식"><select aria-label="설정할 실행 방식" className={select} value={pi} onChange={event => setPathIndex(Number(event.target.value))}>{spec.executionPaths.map((p, i) => <option key={i} value={i}>{p.name || `실행 방식 ${i + 1}`}</option>)}</select></Field>
      {execution && <div key={pi} className="grid gap-4"><Text label="실행 방식 이름" value={execution.name} set={name => path({ name })} /><Field label="사용할 모델 파일"><select required aria-label="사용할 모델 파일" className={select} value={execution.artifact} onChange={event => path({ artifact: event.target.value })}><option value="">파일 선택</option>{spec.artifacts.map((a, i) => <option key={i} value={a.name}>{a.name || "이름 없는 파일"}</option>)}</select></Field><Choice label="실행 소프트웨어" value={execution.runtime.family} set={family => path({ runtime: { ...execution.runtime, family } })} options={["firstfloor", "onnxruntime", "rknnlite"]} /><Choice label="처리 방식" value={execution.runtime.backend} set={backend => path({ runtime: { ...execution.runtime, backend } })} options={execution.runtime.family === "firstfloor" ? ["ort-cpu", "ort-cuda", "rknnlite"] : execution.runtime.family === "rknnlite" ? ["not-applicable"] : ["CPUExecutionProvider", "CUDAExecutionProvider", "TensorrtExecutionProvider"]} />
        <label className="flex gap-2 text-sm"><input type="checkbox" checked={!!execution.runtime.settings?.firstfloor} onChange={event => path({ runtime: { ...execution.runtime, settings: event.target.checked ? { firstfloor: { maxBatchSize: 1, maxQueueSize: 1, deadlinePolicy: "keep" } } : undefined } })} />firstfloor 요청 처리 설정 사용</label>
        <p className="text-sm text-muted-foreground">firstfloor에는 이 설정이 필요해요. ONNX 파일은 ort-cpu 또는 ort-cuda, RKNN 파일은 rknnlite를 고르세요. 대기 한도는 서비스마다 따로 적용돼요. 다른 실행 소프트웨어에서는 설정을 꺼 주세요. 최종 조합은 저장할 때 확인해요.</p>
        {execution.runtime.settings?.firstfloor && <>{([["maxBatchSize", "최대 묶음 크기 · 건"], ["maxQueueSize", "서비스별 대기 한도 · 건"]] as const).map(([key, label]) => <Integer key={key} label={label} value={execution.runtime.settings!.firstfloor![key]} set={value => path({ runtime: { ...execution.runtime, settings: { firstfloor: { ...execution.runtime.settings!.firstfloor!, [key]: value } } } })} />)}<Field label="응답 목표를 지키기 어려운 요청"><select required aria-label="응답 목표를 지키기 어려운 요청" className={select} value={execution.runtime.settings.firstfloor.deadlinePolicy ?? "keep"} onChange={event => path({ runtime: { ...execution.runtime, settings: { firstfloor: { ...execution.runtime.settings!.firstfloor!, deadlinePolicy: event.target.value as "drop" | "keep" | "defer" } } } })}><option value="keep">계속 처리 (기본값)</option><option value="drop">실행 전 거절</option><option value="defer">정상 대기 요청 뒤로 미루기</option></select></Field><p className="text-sm text-muted-foreground">이미 실행 중인 요청은 끝까지 처리해요. 정책을 바꾸면 새 실행 인스턴스로 교체돼요. 고정 묶음 측정에서는 거절하거나 미루지 않아요.</p></>}
        <Button type="button" variant="outline" onClick={() => set({ ...spec, executionPaths: spec.executionPaths.filter((_, i) => i !== pi) })}>이 실행 방식 빼기</Button></div>}
      <Button type="button" variant="outline" onClick={() => { setPathIndex(spec.executionPaths.length); set({ ...spec, executionPaths: [...spec.executionPaths, emptyPath()] }) }}>실행 방식 추가</Button>
    </>}
  </div>
}

// Readiness and connection summary for dialogs outside the service screens (inference uses it).
export function WorkloadState({ view }: { view: WorkloadView }) {
  const { name, uid } = view.desired.metadata
  return <>
    <Facts items={[
      ["설정한 모델", view.desired.spec.modelRef.name], ["응답 목표", target(view.desired.spec)],
      ["적용된 모델", view.appliedDefinition?.model.name ?? "아직 적용되지 않았어요"],
      ["준비 상태", `${statusLabel(view.readiness)} · ${statusLabel(view.reason)}`], ["새 요청", view.admission ? "받을 수 있어요" : "아직 받을 수 없어요"],
      ["처리 중인 요청", `배정 대기 ${view.pending}건 · 배정됨 ${view.assigned}건`], ["측정이 끝난 입력", `${view.supportedInputs.length}개`],
    ]} />
    <Link href={href("workloads", name, uid, "evidence")}>측정 근거 보기</Link>
    <SourceJSON inline title="전체 상태와 관측 시각" value={view} /><SourceExport value={view} filename={`workload-${name}.json`} />
  </>
}
function brief(spec: ModelSpec | WorkloadSpec): [string, string][] {
  return "modelRef" in spec ? [["모델", spec.modelRef.name], ["응답 목표", target(spec)]] : [["모델 파일", spec.artifacts.map(a => a.name).join(", ") || "없음"], ["실행 방식", spec.executionPaths.map(p => p.name).join(", ") || "없음"]]
}
function DefinitionSummary({ spec }: { spec: ModelSpec | WorkloadSpec }) {
  return "modelRef" in spec ? <Facts items={brief(spec)} /> : <Facts items={[
    ...spec.artifacts.map((a, i): [string, string] => [`파일 ${a.name || i + 1}`, `${a.format} · 입력 ${a.input?.tensors.map(t => `${t.name} (${t.sampleShape.join(" × ")})`).join(", ") || "없음"}`]),
    ...spec.executionPaths.map((p, i): [string, string] => [`실행 방식 ${p.name || i + 1}`, `${p.artifact} · ${p.runtime.family} / ${p.runtime.backend}`]),
  ]} />
}
function serviceRow(w: WorkloadView) {
  return <Row key={w.desired.metadata.uid} href={href("workloads", w.desired.metadata.name, w.desired.metadata.uid)} label={w.desired.metadata.name} hint={`${w.desired.spec.modelRef.name} · ${target(w.desired.spec)}`} value={w.admission ? "요청 받는 중" : statusLabel(w.reason || w.readiness)} />
}
function Impact({ affected }: { affected: WorkloadView[] | undefined }) {
  return <>
    {affected === undefined ? <p>관련 서비스 정보를 불러오지 못해 영향 범위를 확인할 수 없어요.</p> : affected.length ? <Rows label={`연결된 서비스 ${affected.length}개`}>{affected.map(serviceRow)}</Rows> : <p>연결된 서비스가 없어요.</p>}
    <p className="text-sm text-muted-foreground">모델을 바꾸면 연결된 서비스는 새 요청을 잠시 멈추고 기존 요청을 마무리해요. 쓰는 서비스가 있는 모델은 삭제할 수 없어요.</p>
  </>
}

// One registration: owner read, If-Match version, conflict, unknown outcome and deletion tracking.
function useResource(kind: Kind, name?: string) {
  const queryClient = useQueryClient()
  const expectedUID = useSearchParams().get("uid")
  const path = `v1/${kind}${name ? `/${encodeURIComponent(name)}` : ""}`
  const workloads = useChillQuery<WorkloadView[]>("v1/workloads", { poll: !!name && kind === "models" })
  const detail = useChillQuery<Resource>(path, { enabled: !!name, poll: !!name })
  const mutation = useChillMutation()
  const [baseline, setBaseline] = useState<Resource | undefined>()
  const [version, setVersion] = useState<string | undefined>()
  const [conflict, setConflict] = useState(false)
  const [deletedUID, setDeletedUID] = useState<string | null>(null)
  const [result, setResult] = useState<RelayResponse | null>(null)
  if (name && detail.data && !baseline && ![401, 403].includes(detail.error?.status ?? 0)) {
    setBaseline(detail.data); setVersion(unwrap(detail.data).metadata.resourceVersion)
  }
  const source = baseline && unwrap(baseline)
  const current = name && detail.data ? unwrap(detail.data) : undefined
  const identityChanged = !!source && !!current && source.metadata.uid !== current.metadata.uid
  const wrongUID = !!expectedUID && !!current && expectedUID !== current.metadata.uid
  const missing = detail.error?.status === 404
  const denied = !!name && [401, 403].includes(detail.error?.status ?? 0)
  const blocked = wrongUID || identityChanged || missing || !!deletedUID || denied || mutation.isPending
  const affected = workloads.error ? undefined : connected(workloads.data, name ?? "", source?.metadata.uid)
  async function submit(method: "POST" | "PUT" | "DELETE", body?: unknown) {
    if (blocked || conflict) return
    setResult(null)
    try {
      const response = await mutation.mutateAsync({ path: method === "POST" ? `v1/${kind}` : path, method, body, version: method === "POST" ? undefined : version })
      setResult(response); setConflict(false)
      if (method === "DELETE") setDeletedUID(source?.metadata.uid ?? null)
      if (method === "PUT" && response.data && typeof response.data === "object") {
        const metadata = unwrap(response.data as Resource).metadata
        if (metadata?.uid === source?.metadata.uid) { setBaseline(response.data as Resource); setVersion(metadata.resourceVersion) }
      }
      await Promise.all([queryClient.invalidateQueries({ queryKey: ["chill", "v1/models"] }), queryClient.invalidateQueries({ queryKey: ["chill", "v1/workloads"] }), name ? detail.refetch() : Promise.resolve()])
    } catch (error) {
      if (error instanceof ApiError && error.unknownOutcome && method === "DELETE") setDeletedUID(source?.metadata.uid ?? null)
      if (error instanceof ApiError && error.status === 409 && (error.data as { code?: string } | null)?.code === "ResourceVersionConflict") { setConflict(true); await detail.refetch() }
    }
  }
  function adopt() {
    if (blocked || !detail.data || !current || current.metadata.uid !== source?.metadata.uid) return
    setBaseline(detail.data); setVersion(current.metadata.resourceVersion); setConflict(false); mutation.reset()
  }
  function reviewDelete() {
    if (!current) return
    setDeletedUID(null); setVersion(current.metadata.resourceVersion); mutation.reset(); setResult(null)
  }
  return { name, expectedUID, detail, workloads, mutation, baseline, source, current, identityChanged, wrongUID, missing, denied, blocked, conflict, deletedUID, result, affected, submit, adopt, reviewDelete, clearResult: () => setResult(null) }
}
type ResourceState = ReturnType<typeof useResource>

function Mismatch({ kind, name, uid }: { kind: Kind; name: string; uid: string }) {
  return <Screen back={[`/${kind}`, kindLabel(kind)]} status={<StatusBadge tone="warn">작업 멈춤</StatusBadge>} title="이름이 같은 다른 등록이에요" lede="잘못된 대상을 바꾸지 않도록 작업을 멈췄어요." action={<Button asChild><Link href={href(kind, name, uid)}>현재 등록 열기</Link></Button>} />
}
// Feedback for the last write on this screen; the draft is never discarded and never resent automatically.
function Problem({ r, kind }: { r: ResourceState; kind: Kind }) {
  if (r.conflict) return <div role="alert" className="notice">
    <p className="font-medium">다른 변경이 먼저 저장됐어요. 최신 설정을 확인한 뒤 다시 삭제를 검토해 주세요.</p>
    <div className="flex flex-wrap gap-2"><DetailsDialog label="최신 설정 보기" title="최신 설정">{r.current && <DefinitionSummary spec={r.current.spec} />}<SourceJSON inline title="삭제 기준과 최신 원본" value={{ baseline: r.baseline, latest: r.detail.data }} /></DetailsDialog><Button size="sm" disabled={r.blocked || !r.current || r.current.metadata.uid !== r.source?.metadata.uid} onClick={r.adopt}>최신 상태 확인했어요</Button></div>
  </div>
  const error = r.mutation.error
  if (!error) return null
  const failure = error.data as { code?: string; field?: string; message?: string } | null
  return <div role="alert" className="notice">
    <p className="font-medium">{error.unknownOutcome ? "저장 결과를 확인하지 못했어요" : "변경을 저장하지 못했어요"}</p>
    <p>작성한 내용은 그대로 있어요. {failure?.field?.includes("slo") ? "응답 목표를 확인해 주세요." : failure?.field?.includes("model") ? "선택한 모델을 확인해 주세요." : failure?.field?.includes("artifact") ? "모델 파일과 입력 정보를 확인해 주세요." : failure?.field?.includes("execution") ? "실행 방식을 확인해 주세요." : "자세한 이유를 확인하고 다시 시도해 주세요."} 결과가 불확실하면 현재 상태를 먼저 확인해 주세요. 자동으로 다시 보내지 않아요.</p>
    <div className="flex flex-wrap gap-2"><SourceJSON title="변경 오류 자세히" value={{ status: error.status, error: failure, body: error.body }} />{r.name ? <Button size="sm" variant="outline" onClick={() => void r.detail.refetch()}>최신 상태 확인</Button> : <Button asChild size="sm" variant="outline"><Link href={`/${kind}`}>목록에서 확인</Link></Button>}</div>
  </div>
}
function DeleteConfirm({ kind, name, r, close }: { kind: Kind; name: string; r: ResourceState; close: () => void }) {
  if (!r.source) return null
  return <Confirm title={`${name} 등록을 삭제할까요?`} busy={r.mutation.isPending} disabled={r.blocked || r.conflict} cancel={close} accept={() => { close(); void r.submit("DELETE") }}>
    <p>삭제한 등록은 되돌릴 수 없어요. 진행 중인 요청을 마무리한 뒤 삭제해요. 삭제하면 이 등록으로 새 요청을 받을 수 없고, 같은 이름으로 다시 등록해도 이전 요청 기록과는 별개예요.</p>
    <DefinitionSummary spec={r.source.spec} />
    {kind === "models" && <Impact affected={r.affected} />}
    <SourceJSON inline title="삭제 대상 원본" value={r.source} />
  </Confirm>
}
function Deleted({ kind, name, uid, r }: { kind: Kind; name: string; uid: string; r: ResourceState }) {
  const { missing, current, detail, mutation } = r
  const replaced = !missing && !!current && current.metadata.uid !== uid
  const operation = detail.data && "desired" in detail.data ? detail.data.operation : undefined
  const canReview = !missing && current?.metadata.uid === uid && !detail.error && !(operation && !["Completed", "Failed"].includes(operation.phase))
  const unknown = !!mutation.error?.unknownOutcome
  return <Screen back={[`/${kind}`, kindLabel(kind)]} status={<StatusBadge tone={missing ? "ok" : "info"}>{missing ? "삭제 완료" : "삭제 확인 중"}</StatusBadge>}
    title={missing ? "삭제를 마쳤어요" : replaced ? "같은 이름으로 새 등록이 생겼어요" : unknown ? "삭제 결과를 확인하지 못했어요" : "삭제 요청을 보냈어요"}
    lede={missing ? "다시 조회해서 삭제된 걸 확인했어요." : replaced ? "이전 등록과는 별개예요. 따로 확인해 주세요." : unknown ? "삭제됐을 수도 있어요. 자동으로 다시 보내지 않으니 상태를 다시 확인해 주세요." : "아직 실제로 삭제가 끝났는지는 확인하지 못했어요."}
    action={missing ? <Button asChild><Link href={`/${kind}`}>목록으로 가기</Link></Button> : <Button variant="outline" disabled={detail.isFetching} onClick={() => void detail.refetch()}>삭제 상태 다시 확인</Button>}>
    <Rows>
      {kind === "workloads" && <Row href={`/observations?${new URLSearchParams({ workload: name, uid })}`} label="삭제한 서비스의 요청 기록" />}
      <RowDialog label="삭제 확인 대상" title="삭제 확인 대상"><SourceJSON inline value={{ name, uid, latest: current, status: detail.error?.status, response: r.result && { status: r.result.status, body: r.result.body }, error: mutation.error && { status: mutation.error.status, body: mutation.error.body } }} /></RowDialog>
      {canReview && <Row label="현재 상태 확인 후 삭제 다시 검토" onClick={() => { if (!detail.isFetching && !mutation.isPending) r.reviewDelete() }} />}
    </Rows>
  </Screen>
}

export function ServicesPage() {
  const workloads = useChillQuery<WorkloadView[]>("v1/workloads")
  const list = workloads.data
  const waiting = list?.filter(w => !w.admission).length ?? 0
  return <Screen title={!list ? "서비스" : !list.length ? "아직 등록한 서비스가 없어요" : waiting ? `서비스 ${list.length}개 중 ${waiting}개가 준비를 기다려요` : `서비스 ${list.length}개 모두 요청을 받고 있어요`} lede="서비스를 누르면 상태와 다음 할 일을 볼 수 있어요." action={<Button asChild><Link href="/workloads/new">서비스 추가</Link></Button>}>
    <QueryGate query={workloads}>{list?.length ? <>
      <Rows>{list.map(serviceRow)}</Rows>
      <Rows><RowDialog label="목록 원본 보기" title="서비스 목록 원본"><SourceJSON inline value={list} /></RowDialog></Rows>
    </> : <Empty title="첫 서비스를 추가해 보세요"><p>모델을 고르고 응답 목표를 정하면 돼요.</p></Empty>}</QueryGate>
  </Screen>
}

export function ModelsPage() {
  const models = useChillQuery<ModelList>("v1/models")
  const workloads = useChillQuery<WorkloadView[]>("v1/workloads")
  const items = models.data?.items
  return <Screen back={["/settings", "설정"]} title={!items ? "모델" : items.length ? `모델 ${items.length}개가 등록돼 있어요` : "아직 등록한 모델이 없어요"} lede="등록과 실제 실행 준비는 달라요. 서비스에서 준비 상태를 확인해 주세요." action={<Button asChild><Link href="/models/new">모델 추가</Link></Button>}>
    <QueryGate query={models}>{items?.length ? <>
      <Rows>{items.map(m => <Row key={m.metadata.uid} href={href("models", m.metadata.name, m.metadata.uid)} label={m.metadata.name} hint={`파일 ${m.spec.artifacts.length}개 · 실행 방식 ${m.spec.executionPaths.length}개`} value={workloads.error ? "확인 불가" : workloads.data ? `서비스 ${connected(workloads.data, m.metadata.name, m.metadata.uid)?.length}개` : "확인 중"} />)}</Rows>
      <Rows><RowDialog label="목록 원본 보기" title="모델 목록 원본"><SourceJSON inline value={models.data} /></RowDialog></Rows>
    </> : <Empty title="첫 모델을 추가해 보세요"><p>모델 파일과 실행 방식을 알려 주면 돼요.</p></Empty>}</QueryGate>
  </Screen>
}

// One service: the plain conclusion, its next step, and a row per section.
export function ServicePage({ name }: { name: string }) {
  const r = useResource("workloads", name)
  const view = r.detail.data && "desired" in r.detail.data && !r.denied ? r.detail.data : undefined
  const s = useServiceSignals(view)
  const [deleting, setDeleting] = useState(false)
  if (r.deletedUID) return <Deleted kind="workloads" name={name} uid={r.deletedUID} r={r} />
  if ((r.wrongUID || r.identityChanged) && r.current) return <Mismatch kind="workloads" name={name} uid={r.current.metadata.uid} />
  const uid = view?.desired.metadata.uid ?? ""
  const reason = view ? view.reason || view.readiness : ""
  const failed = view?.operation?.phase === "Failed"
  const measured = !!view?.supportedInputs.length && reason !== "MissingEvidence"
  const title = !view ? name : view.admission ? "추론을 시작할 수 있어요" : failed ? "설정 변경이 실패했어요" : s.operating ? "설정을 적용하고 있어요" : !measured ? "측정 결과가 있어야 요청을 받을 수 있어요" : "아직 요청을 받지 못해요"
  const next = !view ? null : view.admission ? [href("workloads", name, uid, "infer"), "추론하기"] : failed ? [href("workloads", name, uid, "edit"), "설정 수정"] : s.operating ? [href("workloads", name, uid, "definition"), "적용 진행 보기"] : !measured ? ["/measurements", "측정 결과 찾기"] : ["MissingRuntime", "NativeNotReady"].includes(reason) ? [href("workloads", name, uid, "placement"), "배치 확인하기"] : [href("workloads", name, uid, "edit"), "설정 수정"]
  return <Screen back={["/workloads", "서비스"]} status={view && <StatusBadge value={view.admission ? "Ready" : reason}>{view.admission ? "요청 받는 중" : undefined}</StatusBadge>} title={title} lede={view && `${name} · ${view.desired.spec.modelRef.name} 모델 · ${target(view.desired.spec)}`} action={next && <Button asChild><Link href={next[0]}>{next[1]}</Link></Button>}>
    <QueryGate query={r.detail}>{view && <>
      <Problem r={r} kind="workloads" />
      <Rows label="자세히">{(Object.keys(serviceSections) as ServiceSectionId[]).map(id => <Row key={id} href={href("workloads", name, uid, id)} label={serviceSections[id][0]} hint={serviceSections[id][1]} value={s.values[id]} />)}</Rows>
      <Rows label="관리">
        <Row href={href("workloads", name, uid, "edit")} label="설정 수정" hint="모델이나 응답 목표 바꾸기" />
        <Row label="삭제" hint="되돌릴 수 없어요" onClick={() => { if (!r.blocked && !r.conflict) setDeleting(true) }} />
      </Rows>
      {deleting && <DeleteConfirm kind="workloads" name={name} r={r} close={() => setDeleting(false)} />}
    </>}</QueryGate>
  </Screen>
}

export function ServiceSectionPage({ name, section }: { name: string; section: ServiceSectionId }) {
  const expectedUID = useSearchParams().get("uid")
  const detail = useChillQuery<WorkloadView>(`v1/workloads/${encodeURIComponent(name)}`, { poll: true })
  const actual = detail.data?.desired.metadata.uid
  if (actual && expectedUID && actual !== expectedUID) return <Mismatch kind="workloads" name={name} uid={actual} />
  return <ServiceSection name={name} uid={expectedUID ?? actual ?? ""} query={detail} section={section} />
}

export function ModelPage({ name }: { name: string }) {
  const r = useResource("models", name)
  const model = r.detail.data && !("desired" in r.detail.data) && !r.denied ? r.detail.data : undefined
  const [deleting, setDeleting] = useState(false)
  if (r.deletedUID) return <Deleted kind="models" name={name} uid={r.deletedUID} r={r} />
  if ((r.wrongUID || r.identityChanged) && r.current) return <Mismatch kind="models" name={name} uid={r.current.metadata.uid} />
  const uid = model?.metadata.uid ?? ""
  const count = r.affected?.length
  return <Screen back={["/models", "모델"]} title={!model ? name : count === undefined ? "등록된 모델이에요" : count ? `서비스 ${count}개가 이 모델을 써요` : "아직 이 모델을 쓰는 서비스가 없어요"} lede={model && `${name} · 등록과 실제 실행 준비는 달라요. 연결된 서비스에서 준비 상태를 확인해 주세요.`}>
    <QueryGate query={r.detail}>{model && <>
      <Problem r={r} kind="models" />
      <Rows label="자세히">
        <Row href={href("models", name, uid, "files")} label="파일과 실행 방식" hint="모델 파일, 입력, 실행 소프트웨어" value={`파일 ${model.spec.artifacts.length}개 · 실행 ${model.spec.executionPaths.length}개`} />
        <Row href={href("models", name, uid, "services")} label="연결된 서비스" hint="이 모델을 쓰는 서비스" value={count === undefined ? r.workloads.error ? "확인 불가" : "확인 중" : `${count}개`} />
      </Rows>
      <Rows label="관리">
        <Row href={href("models", name, uid, "edit")} label="설정 수정" hint="파일과 실행 방식 바꾸기" />
        <Row label="삭제" hint={count ? "쓰는 서비스가 있으면 삭제할 수 없어요" : "되돌릴 수 없어요"} onClick={() => { if (!r.blocked && !r.conflict) setDeleting(true) }} />
        <RowDialog label="원본 자료 보기" title="모델 전체 원본"><SourceJSON inline value={model} /><SourceExport value={model} filename={`model-${name}.json`} /></RowDialog>
      </Rows>
      {deleting && <DeleteConfirm kind="models" name={name} r={r} close={() => setDeleting(false)} />}
    </>}</QueryGate>
  </Screen>
}

export function ModelSectionPage({ name, section }: { name: string; section: "files" | "services" }) {
  const r = useResource("models", name)
  const model = r.detail.data && !("desired" in r.detail.data) && !r.denied ? r.detail.data : undefined
  if ((r.wrongUID || r.identityChanged) && r.current) return <Mismatch kind="models" name={name} uid={r.current.metadata.uid} />
  const back: [string, string] = [href("models", name, model?.metadata.uid ?? r.expectedUID ?? ""), name]
  if (section === "services") {
    const affected = r.affected
    return <Screen back={back} title={affected === undefined ? r.workloads.error ? "연결된 서비스를 확인하지 못했어요" : "연결된 서비스" : affected.length ? `연결된 서비스가 ${affected.length}개 있어요` : "연결된 서비스가 없어요"} lede="모델을 바꾸면 연결된 서비스는 새 요청을 잠시 멈추고 기존 요청을 마무리해요. 쓰는 서비스가 있는 모델은 삭제할 수 없어요.">
      <QueryGate query={r.detail}><QueryGate query={r.workloads}>{affected?.length ? <Rows>{affected.map(serviceRow)}</Rows> : <Empty title="이 모델을 쓰는 서비스가 없어요" />}</QueryGate></QueryGate>
    </Screen>
  }
  const spec = model?.spec
  return <Screen back={back} title={spec ? `파일 ${spec.artifacts.length}개, 실행 방식 ${spec.executionPaths.length}개가 있어요` : "파일과 실행 방식"} lede="실행 방식마다 측정 결과가 있어야 서비스에서 쓸 수 있어요.">
    <QueryGate query={r.detail}>{spec && <>
      <Rows label="모델 파일">{spec.artifacts.map((a, i) => <RowDialog key={i} label={a.name || `파일 ${i + 1}`} hint={`${a.format} 파일 · ${a.source.architecture}`} value={`입력 ${a.input?.tensors.length ?? 0}개`} title={`${a.name || `파일 ${i + 1}`} 파일`}>
        <Facts items={[
          ["형식", a.format], ["보관 주소", a.source.image], ["대상 장비", a.source.architecture], ["파일 위치", a.source.path],
          ...(a.input?.tensors ?? []).map((t): [string, string] => [`입력 ${t.name}`, `${t.datatype} · 크기 ${t.sampleShape.join(" × ")}`]),
          ["변환", a.derivation ? `${a.derivation.sourceArtifact}에서 ${a.derivation.conversion.tool} ${a.derivation.conversion.version}로 변환 · ${a.derivation.targetPlatform} · 묶음 ${a.derivation.declaredBatchSizes.join(", ")}` : "원본 파일"],
        ]} />
      </RowDialog>)}</Rows>
      <Rows label="실행 방식">{spec.executionPaths.map((p, i) => {
        const ff = p.runtime.settings?.firstfloor
        return <RowDialog key={i} label={p.name || `실행 방식 ${i + 1}`} hint={`${p.artifact} 파일 사용`} title={`${p.name || `실행 방식 ${i + 1}`} 실행 방식`}>
          <Facts items={[["사용하는 파일", p.artifact], ["실행 소프트웨어", p.runtime.family], ["처리 방식", p.runtime.backend], ["요청 처리 설정", ff ? `최대 묶음 ${ff.maxBatchSize}건 · 대기 한도 ${ff.maxQueueSize}건 · ${ff.deadlinePolicy === "drop" ? "실행 전 거절" : ff.deadlinePolicy === "defer" ? "뒤로 미루기" : "계속 처리"}` : "사용 안 함"]]} />
        </RowDialog>
      })}</Rows>
      <Rows><RowDialog label="원본 자료 보기" title="파일과 실행 설정 원본"><SourceJSON inline value={spec} /></RowDialog></Rows>
    </>}</QueryGate>
  </Screen>
}

type Step = "name" | "model" | "slo" | ModelStep | "review"
function Editor({ kind, r, models }: { kind: Kind; r: ResourceState; models: ReturnType<typeof useChillQuery<ModelList>> }) {
  const original = r.source
  const available = models.data?.items ?? []
  const [name, setName] = useState(original?.metadata.name ?? "")
  const [model, setModel] = useState(original && "modelRef" in original.spec ? original.spec.modelRef.name : "")
  const [percentile, setPercentile] = useState(original && "slo" in original.spec ? String(original.spec.slo.percentile) : "99.5")
  const [latency, setLatency] = useState(original && "slo" in original.spec ? original.spec.slo.targetLatency : "100ms")
  const [spec, setSpec] = useState<ModelSpec>(original && "artifacts" in original.spec ? structuredClone(original.spec) : { artifacts: [emptyArtifact()], executionPaths: [emptyPath()] })
  const [reuse, setReuse] = useState("")
  const [index, setIndex] = useState(0)
  const [validation, setValidation] = useState("")
  const steps: Step[] = [...(original ? [] : ["name" as const]), ...(kind === "models" ? ["artifacts", "inputs", "paths"] as const : ["model", "slo"] as const), "review"]
  const step = steps[index]
  const busy = r.mutation.isPending
  const locked = r.blocked || r.conflict
  const back: [string, string] = original ? [href(kind, original.metadata.name, r.expectedUID || original.metadata.uid), original.metadata.name] : [`/${kind}`, kindLabel(kind)]
  function draft(): ModelSpec | WorkloadSpec {
    if (kind !== "models") return workloadSpec(model, percentile, latency)
    if (!spec.artifacts.length || !spec.executionPaths.length || spec.artifacts.some(a => !a.input?.tensors.length)) throw new Error("모델 파일, 각 파일의 입력, 실행 방식을 하나 이상 설정해 주세요.")
    return spec
  }
  function next(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setValidation("")
    try {
      if (step === "review") { if (!locked) void r.submit(original ? "PUT" : "POST", original ? { spec: draft() } : { name, spec: draft() }); return }
      if (step === "artifacts" && (!spec.artifacts.length || spec.artifacts.some(a => !a.name.trim() || !a.format.trim() || !a.source.image.trim() || !a.source.architecture.trim() || !a.source.path.trim()))) throw new Error("파일마다 이름, 형식, 보관 주소, 대상 장비와 파일 위치를 적어 주세요. 위에서 설정할 파일을 바꿀 수 있어요.")
      if (step === "inputs" && spec.artifacts.some(a => !a.input?.tensors.length || a.input.tensors.some(t => !t.name.trim()))) throw new Error("모든 모델 파일에 입력 이름과 크기를 설정해 주세요. 위에서 설정할 파일을 바꿀 수 있어요.")
      if (step === "paths" && (!spec.executionPaths.length || spec.executionPaths.some(p => !p.name.trim() || !p.artifact || !p.runtime.family.trim() || !p.runtime.backend.trim()))) throw new Error("실행 방식마다 이름, 파일, 실행 소프트웨어와 처리 방식을 설정해 주세요.")
      if (steps[index + 1] === "review") draft()
      setIndex(index + 1)
    } catch (error) { setValidation((error as Error).message) }
  }

  if (r.conflict) return <Screen back={back} status={<StatusBadge tone="warn">저장 전 확인</StatusBadge>} title="다른 변경이 먼저 저장됐어요" lede="작성한 내용은 그대로 있어요. 최신 설정을 보고 그 기준으로 계속 작성할지 정해 주세요. 자동으로 다시 저장하지 않아요." action={<Button disabled={!r.current || busy || r.current.metadata.uid !== original?.metadata.uid} onClick={r.adopt}>최신 상태를 기준으로 계속 작성</Button>}>
    <Rows>
      {original && <RowDialog label="작성을 시작한 설정" title="작성을 시작한 설정"><DefinitionSummary spec={original.spec} /></RowDialog>}
      {r.current && <RowDialog label="최신 설정" title="최신 설정"><DefinitionSummary spec={r.current.spec} /></RowDialog>}
      <RowDialog label="원본 자료 보기" title="작성 기준과 최신 원본"><SourceJSON inline value={{ before: original, latest: r.detail.data }} /></RowDialog>
    </Rows>
  </Screen>

  if (r.result) {
    const status = r.result.status
    const saved = r.result.data && typeof r.result.data === "object" ? unwrap(r.result.data as Resource).metadata : undefined
    return <Screen back={back} status={<StatusBadge tone="ok">저장됨</StatusBadge>} title={status === 201 ? "등록했어요" : status === 202 ? "변경 요청을 받았어요" : status === 200 ? "변경 응답을 받았어요" : "응답을 받았어요"} lede={status === 201 ? "실제 준비 상태를 확인해 주세요." : status === 202 ? "적용이 진행되는지 확인해 주세요." : "현재 설정을 확인해 주세요."} action={<Button asChild><Link href={!original && saved ? href(kind, saved.name, saved.uid) : back[0]}>{original ? "상세로 가기" : "등록 상세와 준비 상태 확인"}</Link></Button>}>
      <Rows>
        <RowDialog label="저장 응답 원본" title="저장 응답 원본"><SourceJSON inline value={{ status, body: r.result.body }} /></RowDialog>
        {original && <Row label="계속 수정하기" onClick={r.clearResult} />}
      </Rows>
    </Screen>
  }

  let reviewed: ModelSpec | WorkloadSpec | undefined
  try { if (step === "review") reviewed = draft() } catch { reviewed = undefined }
  const titles: Record<Step, string> = {
    name: `${kindLabel(kind)} 이름을 정해 주세요`, model: "어떤 모델을 쓸까요?", slo: "응답 목표를 정해 주세요",
    artifacts: "모델 파일을 알려 주세요", inputs: "모델 입력을 알려 주세요", paths: "어떻게 실행할까요?",
    review: original ? "이렇게 바꿀까요?" : "이렇게 등록할까요?",
  }
  const ledes: Record<Step, string> = {
    name: "등록한 이름은 나중에 바꿀 수 없어요.", model: "등록된 모델 중에서 골라 주세요.",
    slo: "예: 99.5%라면 1,000건 중 995건이 정한 시간 안에 응답하도록 목표를 정해요. 요청을 기다리는 제한 시간과는 달라요. 바꾼 목표는 새로 받는 요청부터 적용돼요.",
    artifacts: "새 파일은 담당자가 알려 준 보관 주소와 파일 위치가 필요해요. 외부 저장소의 파일 목록은 여기서 조회할 수 없어요.",
    inputs: "모델 파일이 요구하는 입력 이름과 크기를 옮겨 적어 주세요.",
    paths: "모델을 실행할 소프트웨어를 정해요. 정하는 것만으로 장비가 준비되지는 않고, 측정과 준비 확인이 필요해요.",
    review: kind === "workloads" && original ? "모델을 바꾸면 새 요청을 잠시 멈추고 기존 요청을 마무리해요. 응답 목표만 바꾸면 새 요청부터 적용돼요." : "저장한 뒤 측정 결과와 서비스 준비 상태를 확인해 주세요.",
  }
  const selected = available.find(m => m.metadata.name === model)
  return <Screen back={back} status={<span className="text-sm text-muted-foreground">{original ? `${original.metadata.name} 수정` : `${kindLabel(kind)} 추가`} · {index + 1} / {steps.length}</span>} title={titles[step]} lede={ledes[step]}
    action={<div className="flex gap-3">{index > 0 && <Button type="button" variant="outline" className="flex-1" disabled={busy} onClick={() => { setIndex(index - 1); setValidation("") }}>이전</Button>}<Button type="submit" form="resource-editor" className="flex-[2]" disabled={locked}>{step !== "review" ? "다음" : busy ? "저장하고 있어요" : original ? "변경 저장하기" : `${kindLabel(kind)} 등록하기`}</Button></div>}>
    <form id="resource-editor" aria-describedby={validation ? "editor-validation" : undefined} onSubmit={next}><fieldset disabled={locked} className="grid gap-5">
      {step === "name" && <>
        <Field label={`${kindLabel(kind)} 이름`}><Input aria-label={`${kindLabel(kind)} 이름`} required value={name} onChange={event => setName(event.target.value)} /></Field>
        {kind === "models" && available.length > 0 && <Choices label="기존 모델 설정 가져오기" value={reuse} set={uid => { setReuse(uid); const existing = available.find(m => m.metadata.uid === uid); setSpec(existing ? structuredClone(existing.spec) : { artifacts: [emptyArtifact()], executionPaths: [emptyPath()] }) }} options={[{ value: "", label: "새 파일로 등록", hint: "파일과 실행 방식을 직접 알려 줘요" }, ...available.map(m => ({ value: m.metadata.uid, label: m.metadata.name, hint: `파일 ${m.spec.artifacts.length}개 · 실행 방식 ${m.spec.executionPaths.length}개를 가져와요` }))]} />}
      </>}
      {step === "model" && <QueryGate query={models}>
        {available.length || model ? <Choices label="사용할 모델" value={model} set={setModel} options={[...(model && !selected ? [{ value: model, label: model, hint: "지금 목록에서 확인되지 않아요" }] : []), ...available.map(m => ({ value: m.metadata.name, label: m.metadata.name, hint: `파일 ${m.spec.artifacts.length}개 · 실행 방식 ${m.spec.executionPaths.length}개` }))]} /> : <Empty title="고를 수 있는 모델이 없어요"><Button asChild variant="outline"><Link href="/models/new">모델 추가</Link></Button></Empty>}
        {selected && <Rows><RowDialog label="선택한 모델 살펴보기" value={selected.metadata.name} title={selected.metadata.name}><DefinitionSummary spec={selected.spec} /><Impact affected={r.workloads.error ? undefined : connected(r.workloads.data, selected.metadata.name, selected.metadata.uid)} /><Link href={href("models", selected.metadata.name, selected.metadata.uid)}>모델 상세 열기</Link></RowDialog></Rows>}
      </QueryGate>}
      {step === "slo" && <><Field label="목표 비율 · %"><Input aria-label="목표 비율 · %" type="number" step="any" min="0" max="100" required value={percentile} onChange={event => setPercentile(event.target.value)} /></Field><p className="text-sm text-muted-foreground">0보다 크고 100 이하여야 해요.</p><DurationField value={latency} set={setLatency} /></>}
      {(step === "artifacts" || step === "inputs" || step === "paths") && <ModelFields spec={spec} set={setSpec} step={step} models={available} />}
      {step === "review" && reviewed && <>
        <Facts items={[["이름", name], ...brief(reviewed).map(([label, value], i): [string, string] => { const before = original && brief(original.spec)[i]?.[1]; return [label, before === undefined || before === value ? value : `${before} → ${value}`] })]} />
        <Rows>
          {original && <RowDialog label="변경 전 자세히" title="변경 전"><DefinitionSummary spec={original.spec} /></RowDialog>}
          <RowDialog label={original ? "변경 후 자세히" : "등록 내용 자세히"} title={original ? "변경 후" : "등록 내용"}><DefinitionSummary spec={reviewed} /></RowDialog>
          {kind === "models" && original && <RowDialog label="영향 받는 서비스" value={r.affected ? `${r.affected.length}개` : "확인 불가"} title="영향 받는 서비스"><Impact affected={r.affected} /></RowDialog>}
          <RowDialog label="원본 자료 보기" title="저장 원본과 비교 기준"><SourceJSON inline value={{ original, submitted: reviewed }} /></RowDialog>
        </Rows>
        <Problem r={r} kind={kind} />
      </>}
    </fieldset></form>
    {validation && <p id="editor-validation" role="alert">{validation}</p>}
  </Screen>
}

export function ResourceEditor({ kind, name }: { kind: Kind; name?: string }) {
  const r = useResource(kind, name)
  const models = useChillQuery<ModelList>("v1/models")
  if (name && (r.wrongUID || r.identityChanged) && r.current) return <Mismatch kind={kind} name={name} uid={r.current.metadata.uid} />
  if (name && (!r.baseline || r.denied)) return <Screen back={[`/${kind}`, kindLabel(kind)]} title={`${name} 수정`}><QueryGate query={r.detail}>{null}</QueryGate></Screen>
  return <Editor key={`${kind}/${name ?? "new"}/${r.source?.metadata.uid ?? "new"}`} kind={kind} r={r} models={models} />
}

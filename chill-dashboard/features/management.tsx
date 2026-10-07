"use client"

import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useEffect, useRef, useState, type ReactNode } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { ApiError, useChillMutation, useChillQuery, type RelayResponse } from "@/lib/client"
import { DataTable } from "@/components/data-table"
import { DetailsDialog, ReadRegion, SourceJSON, SourceExport, statusLabel } from "@/components/read-region"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { datatypes, positiveIntegers, workloadSpec, type ModelResource, type ModelList, type ModelSpec, type ModelArtifact, type ExecutionPath, type TensorContract, type WorkloadSpec, type WorkloadView } from "@/lib/domain"

const emptyTensor = (): TensorContract => ({ name: "", datatype: "FP32", sampleShape: [1] })
const emptyArtifact = (): ModelArtifact => ({ name: "", format: "", source: { image: "", architecture: "", path: "" }, input: { tensors: [emptyTensor()] } })
const emptyPath = (): ExecutionPath => ({ name: "", artifact: "", runtime: { family: "", backend: "" } })
const href = (kind: string, name: string, uid: string, action = "") => `/${kind}/${encodeURIComponent(name)}${action ? `/${action}` : ""}?uid=${encodeURIComponent(uid)}`
const target = (spec: WorkloadSpec) => `${spec.slo.percentile}%의 요청을 ${spec.slo.targetLatency} 안에 처리`

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
  return <div className="grid gap-2"><Field label={label}><select required aria-label={label} className="rounded border bg-background p-2" value={isCustom ? "__custom" : value} onChange={event => {
    const other = event.target.value === "__custom"; setCustom(other); if (!other) set(event.target.value)
  }}><option value="">선택하세요</option>{options.map(option => <option key={option}>{option}</option>)}<option value="__custom">다른 값 직접 입력</option></select></Field>{isCustom && <Text label={`${label} 직접 입력`} value={value} set={set} />}</div>
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
  return <div className="grid gap-3">{advanced ? <Text label="응답 목표 시간 · 원래 표현" value={value} set={set} /> : <div className="grid grid-cols-[1fr_8rem] gap-3"><Field label="응답 목표 시간"><Input aria-label="응답 목표 시간" type="number" min="0" step="any" required value={simple?.[1] ?? ""} onChange={event => set(`${event.target.value}${unit}`)} /></Field><Field label="시간 단위"><select aria-label="시간 단위" className="rounded border bg-background p-2" value={unit} onChange={event => set(`${simple?.[1] ?? ""}${event.target.value}`)}>{[["ms", "밀리초"], ["s", "초"], ["m", "분"], ["h", "시간"], ["us", "마이크로초"], ["ns", "나노초"], ["µs", "마이크로초 (µs)"], ["μs", "마이크로초 (μs)"]].map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field></div>}
    <label className="flex gap-2 text-sm text-muted-foreground"><input type="checkbox" checked={advanced} disabled={!simple && advanced} onChange={event => setAdvanced(event.target.checked)} />복합 시간 직접 입력 (예: 1m30s)</label>
    <p className="text-sm text-muted-foreground">입력한 시간은 그대로 저장합니다. 0보다 커야 하며 최종 허용 범위는 저장할 때 확인합니다.</p>
  </div>
}
function Confirm({ title, children, busy, disabled = false, accept, cancel }: { title: string; children: ReactNode; busy: boolean; disabled?: boolean; accept: () => void; cancel: () => void }) {
  return <Dialog open onOpenChange={open => { if (!open && !busy) cancel() }}><DialogContent showCloseButton={!busy} className="max-h-[85dvh] overflow-y-auto sm:max-w-2xl" onEscapeKeyDown={event => { if (busy) event.preventDefault() }} onPointerDownOutside={event => { if (busy) event.preventDefault() }}>
    <DialogHeader><DialogTitle>{title}</DialogTitle><DialogDescription>변경 내용과 영향을 확인한 뒤 확정하세요.</DialogDescription></DialogHeader><div className="grid gap-4">{children}</div>
    <div className="mt-4 flex gap-3"><Button autoFocus variant="outline" disabled={busy} onClick={cancel}>취소</Button><Button disabled={busy || disabled} onClick={accept}>{busy ? "저장 중" : "확인했어요"}</Button></div>
  </DialogContent></Dialog>
}

function ModelFields({ spec, set, step }: { spec: ModelSpec; set: (spec: ModelSpec) => void; step: number }) {
  const [artifactIndex, setArtifactIndex] = useState(0)
  const [pathIndex, setPathIndex] = useState(0)
  const index = Math.min(artifactIndex, Math.max(0, spec.artifacts.length - 1))
  const pi = Math.min(pathIndex, Math.max(0, spec.executionPaths.length - 1))
  const row = spec.artifacts[index]
  const execution = spec.executionPaths[pi]
  function artifact(change: Partial<ModelArtifact>) { set({ ...spec, artifacts: spec.artifacts.map((a, i) => i === index ? { ...a, ...change } : a) }) }
  function path(change: Partial<ExecutionPath>) { set({ ...spec, executionPaths: spec.executionPaths.map((p, i) => i === pi ? { ...p, ...change } : p) }) }
  return <div className="grid gap-5">
    {step !== 3 && <><Field label="설정할 모델 파일"><select aria-label="설정할 모델 파일" className="rounded border bg-background p-2" value={index} onChange={event => setArtifactIndex(Number(event.target.value))}>{spec.artifacts.map((a, i) => <option key={i} value={i}>{a.name || `파일 ${i + 1}`}</option>)}</select></Field>{!row && <p>모델 파일을 추가하세요.</p>}</>}
    {step === 1 && <>
      <p className="text-sm text-muted-foreground">새 파일은 담당자가 제공한 보관 주소와 파일 위치가 필요합니다. 외부 저장소의 파일 목록은 여기서 조회할 수 없습니다.</p>
      {row && <div key={index} className="grid gap-4"><Text label="파일 이름" value={row.name} set={name => artifact({ name })} /><Choice label="파일 형식" value={row.format} set={format => artifact({ format })} options={["onnx", "rknn"]} /><Text label="모델 파일 보관 주소" value={row.source.image} set={image => artifact({ source: { ...row.source, image } })} /><p className="text-sm text-muted-foreground">변하지 않는 정확한 버전의 주소를 입력하세요. 예: registry.example/model@sha256:…</p><Choice label="대상 장비 종류" value={row.source.architecture} set={architecture => artifact({ source: { ...row.source, architecture } })} options={["amd64", "arm64"]} /><Text label="보관 이미지 안의 파일 위치" value={row.source.path} set={path => artifact({ source: { ...row.source, path } })} /><p className="text-sm text-muted-foreground">예: /model/model.onnx</p>
        <details className="grid gap-3"><summary className="cursor-pointer text-sm">변환한 파일의 추가 설정</summary><div className="mt-4 grid gap-3"><label className="flex gap-2"><input type="checkbox" checked={!!row.derivation} onChange={event => artifact({ derivation: event.target.checked ? { sourceArtifact: "", targetPlatform: "", declaredBatchSizes: [1], conversion: { tool: "", version: "" } } : undefined })} />다른 파일에서 변환했어요</label>{row.derivation && <><Field label="변환 전 파일"><select required aria-label="변환 전 파일" className="rounded border bg-background p-2" value={row.derivation.sourceArtifact} onChange={event => artifact({ derivation: { ...row.derivation!, sourceArtifact: event.target.value } })}><option value="">원본 파일 선택</option>{spec.artifacts.filter((_, i) => i !== index).map((a, i) => <option key={i} value={a.name}>{a.name || "이름 없는 파일"}</option>)}</select></Field><Text label="변환 대상 장비" value={row.derivation.targetPlatform} set={targetPlatform => artifact({ derivation: { ...row.derivation!, targetPlatform } })} /><IntegerList label="지원 묶음 크기 · 개수, 쉼표로 구분" value={row.derivation.declaredBatchSizes} set={declaredBatchSizes => artifact({ derivation: { ...row.derivation!, declaredBatchSizes } })} /><Text label="변환 도구" value={row.derivation.conversion.tool} set={tool => artifact({ derivation: { ...row.derivation!, conversion: { ...row.derivation!.conversion, tool } } })} /><Text label="변환 도구 버전" value={row.derivation.conversion.version} set={version => artifact({ derivation: { ...row.derivation!, conversion: { ...row.derivation!.conversion, version } } })} /></>}</div></details>
        <Button type="button" variant="outline" onClick={() => set({ ...spec, artifacts: spec.artifacts.filter((_, i) => i !== index) })}>이 파일 빼기</Button></div>}
      <Button type="button" variant="outline" onClick={() => { setArtifactIndex(spec.artifacts.length); set({ ...spec, artifacts: [...spec.artifacts, emptyArtifact()] }) }}>모델 파일 추가</Button>
    </>}
    {step === 2 && row && <>
      <p className="text-sm text-muted-foreground">모델 파일에서 요구하는 입력 이름과 크기를 옮겨 적으세요. 한 번의 입력 크기만 적고, 여러 요청을 묶는 축은 제외합니다. 크기 예: 3, 224, 224 (채널, 높이, 너비).</p>
      {(row.input?.tensors ?? []).map((tensor, ti) => {
        const tensorSet = (change: Partial<TensorContract>) => artifact({ input: { tensors: row.input!.tensors.map((t, i) => i === ti ? { ...t, ...change } : t) } })
        return <fieldset key={`${index}/${ti}`} className="grid gap-4 rounded-lg border p-4"><legend>입력 {ti + 1}</legend><Text label={`입력 ${ti + 1} 이름`} value={tensor.name} set={name => tensorSet({ name })} /><Field label={`입력 ${ti + 1} 값 종류`}><select aria-label={`입력 ${ti + 1} 값 종류`} className="rounded border bg-background p-2" value={tensor.datatype} onChange={event => tensorSet({ datatype: event.target.value as TensorContract["datatype"] })}>{datatypes.map(type => <option key={type}>{type === "BYTES" ? "문자열 (BYTES)" : type === "BOOL" ? "참·거짓 (BOOL)" : type}</option>)}</select></Field><IntegerList label={`입력 ${ti + 1} 크기 · 각 축의 값 개수`} value={tensor.sampleShape} set={sampleShape => tensorSet({ sampleShape })} /><Button type="button" variant="outline" onClick={() => artifact({ input: { tensors: row.input!.tensors.filter((_, i) => i !== ti) } })}>이 입력 빼기</Button></fieldset>
      })}<Button type="button" variant="outline" onClick={() => artifact({ input: { tensors: [...(row.input?.tensors ?? []), emptyTensor()] } })}>입력 추가</Button>
    </>}
    {step === 3 && <><Field label="설정할 실행 방식"><select aria-label="설정할 실행 방식" className="rounded border bg-background p-2" value={pi} onChange={event => setPathIndex(Number(event.target.value))}>{spec.executionPaths.map((p, i) => <option key={i} value={i}>{p.name || `실행 방식 ${i + 1}`}</option>)}</select></Field><p className="text-sm text-muted-foreground">모델에 필요한 실행 소프트웨어를 지정합니다. 선택만으로 장비가 준비되는 것은 아니며 측정과 실제 준비 확인이 필요합니다.</p>
      {execution && <div key={pi} className="grid gap-4"><Text label="실행 방식 이름" value={execution.name} set={name => path({ name })} /><Field label="사용할 모델 파일"><select required aria-label="사용할 모델 파일" className="rounded border bg-background p-2" value={execution.artifact} onChange={event => path({ artifact: event.target.value })}><option value="">파일 선택</option>{spec.artifacts.map((a, i) => <option key={i} value={a.name}>{a.name || "이름 없는 파일"}</option>)}</select></Field><Choice label="실행 소프트웨어" value={execution.runtime.family} set={family => path({ runtime: { ...execution.runtime, family } })} options={["onnxruntime", "triton", "rknnlite"]} /><Choice label="처리 방식" value={execution.runtime.backend} set={backend => path({ runtime: { ...execution.runtime, backend } })} options={execution.runtime.family === "triton" ? ["python"] : execution.runtime.family === "rknnlite" ? ["not-applicable"] : ["CPUExecutionProvider", "CUDAExecutionProvider", "TensorrtExecutionProvider"]} />
        <details open={execution.runtime.family === "triton" || undefined}><summary className="cursor-pointer text-sm">Triton 요청 처리 설정</summary><div className="mt-4 grid gap-3"><label className="flex gap-2"><input type="checkbox" checked={!!execution.runtime.settings?.triton} onChange={event => path({ runtime: { ...execution.runtime, settings: event.target.checked ? { triton: { maxBatchSize: 1, priorityLevels: 1, maxQueueSize: 1 } } : undefined } })} />Triton 설정 사용</label><p className="text-sm text-muted-foreground">Triton과 Python 처리 방식에는 아래 설정이 필요합니다. 다른 실행 소프트웨어에서는 끄세요.</p>{execution.runtime.settings?.triton && ([['maxBatchSize', '최대 묶음 크기 · 건'], ['priorityLevels', '우선순위 단계 · 개'], ['maxQueueSize', '단계별 대기 한도 · 건']] as const).map(([key, label]) => <Integer key={key} label={label} value={execution.runtime.settings!.triton![key]} set={value => path({ runtime: { ...execution.runtime, settings: { triton: { ...execution.runtime.settings!.triton!, [key]: value } } } })} />)}</div></details><Button type="button" variant="outline" onClick={() => set({ ...spec, executionPaths: spec.executionPaths.filter((_, i) => i !== pi) })}>이 실행 방식 빼기</Button></div>}
      <Button type="button" variant="outline" onClick={() => { setPathIndex(spec.executionPaths.length); set({ ...spec, executionPaths: [...spec.executionPaths, emptyPath()] }) }}>실행 방식 추가</Button>
    </>}
  </div>
}

export function WorkloadState({ view }: { view: WorkloadView }) {
  const applied = view.appliedDefinition
  return <section className="grid gap-5" aria-label="서비스 준비와 연결 정보"><dl className="grid gap-4 text-sm"><div><dt className="text-muted-foreground">설정한 모델과 응답 목표</dt><dd><Link href={`/models/${encodeURIComponent(view.desired.spec.modelRef.name)}`}>{view.desired.spec.modelRef.name}</Link> · {target(view.desired.spec)}</dd></div><div><dt className="text-muted-foreground">현재 적용된 모델과 응답 목표</dt><dd>{applied ? <><Link href={href("models", applied.model.name, applied.model.uid)}>{applied.model.name}</Link> · {applied.slo.percentile}%의 요청을 {applied.slo.targetLatencyNanos / 1000000}밀리초 안에 처리</> : "아직 적용되지 않았어요"}</dd></div><div><dt className="text-muted-foreground">실제 준비 상태</dt><dd>{statusLabel(view.readiness)} · {statusLabel(view.reason)}</dd></div><div><dt className="text-muted-foreground">새 요청</dt><dd>{view.admission ? "받을 수 있어요" : "아직 받을 수 없어요"}</dd></div><div><dt className="text-muted-foreground">처리 중인 요청</dt><dd>배정 대기 {view.pending}건 · 배정됨 {view.assigned}건</dd></div><div><dt className="text-muted-foreground">설정 변경 진행</dt><dd>{view.operation ? `${statusLabel(view.operation.kind)} · ${statusLabel(view.operation.phase)}` : "진행 중인 변경 없음"}</dd></div></dl>
    {view.operation && <SourceJSON title="변경 작업 기록" value={view.operation} />}
    <section className="grid gap-3"><h3 className="font-semibold">측정이 완료된 입력</h3>{view.supportedInputs.length ? view.supportedInputs.map((input, index) => <div key={index} className="grid gap-2 rounded-lg border p-4"><p>{input.artifact} · {input.executionPath}</p>{input.tensors.map(t => <p key={t.name}>{t.name} · {t.datatype} · 크기 {t.sampleShape.join(" × ")}</p>)}<p>연결된 측정 {input.profiles.length}개</p><SourceJSON title="입력과 측정 근거" value={input} /></div>) : <p>이 모델의 실행 조건에 맞는 측정 결과가 필요해요. 파일이나 실행 방식을 등록하는 것만으로 추론을 시작할 수는 없어요.</p>}{applied?.model.paths.filter(path => !view.supportedInputs.some(input => input.executionPath === path.name)).map(path => <p key={path.name}>{path.name} · {path.artifact}: 측정 결과가 필요해요</p>)}<Link href="/measurements">필요한 측정 확인</Link></section>
    <SourceJSON title="전체 상태와 관측 시각" value={view} /><SourceExport value={view} filename={`workload-${view.desired.metadata.name}.json`} />
  </section>
}
function DefinitionSummary({ spec }: { spec: ModelSpec | WorkloadSpec }) {
  return "modelRef" in spec ? <dl className="grid gap-2 text-sm"><div><dt>모델</dt><dd>{spec.modelRef.name}</dd></div><div><dt>응답 목표</dt><dd>{target(spec)}</dd></div></dl> : <div className="grid gap-2 text-sm"><p>모델 파일 {spec.artifacts.length}개 · 실행 방식 {spec.executionPaths.length}개</p>{spec.artifacts.map((a, i) => <p key={i}>{a.name || "이름 없는 파일"} · {a.format} · 입력 {a.input?.tensors.map(t => `${t.name} (${t.sampleShape.join(" × ")})`).join(", ") || "없음"}</p>)}{spec.executionPaths.map((p, i) => <p key={i}>{p.name || "이름 없는 실행 방식"}: {p.artifact} · {p.runtime.family} / {p.runtime.backend}</p>)}<SourceJSON title="파일과 실행 설정 자세히" value={spec} /></div>
}
function Impact({ affected }: { affected: WorkloadView[] | undefined }) {
  return <div className="grid gap-3 text-sm"><p>{affected === undefined ? "관련 서비스 정보를 불러오지 못해 영향 범위를 확인할 수 없어요." : `연결된 서비스 ${affected.length}개`}</p>{affected?.map(w => <p key={w.desired.metadata.uid}><Link href={href("workloads", w.desired.metadata.name, w.desired.metadata.uid)}>{w.desired.metadata.name}</Link> · {target(w.desired.spec)} · {w.admission ? "요청 접수 중" : "요청 접수 중지"}</p>)}<p>모델을 변경하면 연결된 서비스는 새 요청을 잠시 멈추고 기존 요청을 마무리해요. 사용 중인 모델은 삭제할 수 없어요.</p></div>
}

function Editor({ kind, resource, models, workloads, latest, conflict, adopt, onSave, busy, blocked }: {
  kind: "models" | "workloads"; resource?: ModelResource | WorkloadView; models: ModelResource[]; workloads?: WorkloadView[]
  latest?: ModelResource | WorkloadView; conflict: boolean; adopt: () => void; onSave: (name: string, spec: ModelSpec | WorkloadSpec) => void; busy: boolean; blocked: boolean
}) {
  const original = resource && ("desired" in resource ? resource.desired : resource)
  const [name, setName] = useState(original?.metadata.name ?? "")
  const [model, setModel] = useState(original && "modelRef" in original.spec ? original.spec.modelRef.name : "")
  const [percentile, setPercentile] = useState(original && "slo" in original.spec ? String(original.spec.slo.percentile) : "99.5")
  const [latency, setLatency] = useState(original && "slo" in original.spec ? original.spec.slo.targetLatency : "100ms")
  const [spec, setSpec] = useState<ModelSpec>(original && "artifacts" in original.spec ? structuredClone(original.spec) : { artifacts: [emptyArtifact()], executionPaths: [emptyPath()] })
  const [reuse, setReuse] = useState("")
  const [step, setStep] = useState(0)
  const [draft, setDraft] = useState<ModelSpec | WorkloadSpec | null>(null)
  const [validation, setValidation] = useState("")
  const latestResource = latest && ("desired" in latest ? latest.desired : latest)
  const selectedModel = models.find(m => m.metadata.name === model)
  const steps = kind === "models" ? ["기본 정보", "모델 파일", "입력 정보", "실행 방식", "등록 내용 확인"] : ["서비스와 모델", "응답 목표", "등록 내용 확인"]
  function buildDraft() {
    if (kind !== "models") return workloadSpec(model, percentile, latency)
    if (!spec.artifacts.length || !spec.executionPaths.length || spec.artifacts.some(a => !a.input?.tensors.length)) throw new Error("모델 파일, 각 파일의 입력, 실행 방식을 하나 이상 설정하세요.")
    return spec
  }
  return <section className="mx-auto grid w-full max-w-2xl gap-6">
    <p className="text-sm text-muted-foreground">{step + 1} / {steps.length} 단계</p><h2 className="text-xl font-semibold">{steps[step]}</h2>
    {conflict && <section role="alert" className="grid gap-3 rounded-lg border p-4"><h3 className="font-semibold">다른 변경이 먼저 저장됐어요</h3><p>작성한 내용은 유지했어요. 기존 설정과 최신 설정을 비교하고, 최신 상태를 기준으로 저장할지 선택하세요. 자동으로 다시 저장하지 않아요.</p>{original && <DefinitionSummary spec={original.spec} />}{latestResource && <><h4>최신 설정</h4><DefinitionSummary spec={latestResource.spec} /></>}<SourceJSON title="작성 기준과 최신 원본" value={{ before: original, latest }} /><Button disabled={!latestResource || busy || latestResource.metadata.uid !== original?.metadata.uid} onClick={adopt}>최신 상태를 기준으로 계속 작성</Button></section>}
    <form aria-describedby={validation ? "management-validation" : undefined} className="grid gap-5" onSubmit={event => {
      event.preventDefault(); setValidation("")
      try {
        if (step < steps.length - 1) {
          if (kind === "models" && step === 1 && (!spec.artifacts.length || spec.artifacts.some(a => !a.name.trim() || !a.format.trim() || !a.source.image.trim() || !a.source.architecture.trim() || !a.source.path.trim()))) throw new Error("파일마다 이름, 형식, 보관 주소, 대상 장비와 파일 위치를 입력하세요. 위에서 설정할 파일을 바꿀 수 있어요.")
          if (kind === "models" && step === 2 && spec.artifacts.some(a => !a.input?.tensors.length || a.input.tensors.some(t => !t.name.trim()))) throw new Error("모든 모델 파일에 입력 이름과 크기를 설정하세요. 위에서 설정할 파일을 바꿀 수 있어요.")
          if (kind === "models" && step === 3 && (!spec.executionPaths.length || spec.executionPaths.some(p => !p.name.trim() || !p.artifact || !p.runtime.family.trim() || !p.runtime.backend.trim()))) throw new Error("실행 방식마다 이름, 파일, 실행 소프트웨어와 처리 방식을 설정하세요.")
          if (step === steps.length - 2) buildDraft()
          setStep(step + 1)
        } else setDraft(buildDraft())
      } catch (error) { setValidation((error as Error).message) }
    }}><fieldset disabled={busy || blocked || conflict} className="grid gap-5">
      {step === 0 && <><Field label={kind === "models" ? "모델 이름" : "서비스 이름"}><Input aria-label={kind === "models" ? "모델 이름" : "서비스 이름"} required readOnly={!!original} value={name} onChange={event => setName(event.target.value)} /></Field>{original && <p className="text-sm text-muted-foreground">등록 이름은 바꿀 수 없어요.</p>}
        {kind === "models" && !original && <><Field label="기존 모델 설정 가져오기"><select aria-label="기존 모델 설정 가져오기" className="rounded border bg-background p-2" value={reuse} onChange={event => { setReuse(event.target.value); const existing = models.find(m => m.metadata.uid === event.target.value); setSpec(existing ? structuredClone(existing.spec) : { artifacts: [emptyArtifact()], executionPaths: [emptyPath()] }) }}><option value="">새 파일로 등록</option>{models.map(m => <option key={m.metadata.uid} value={m.metadata.uid}>{m.metadata.name}</option>)}</select></Field><p className="text-sm text-muted-foreground">등록된 모델의 실제 파일과 입력·실행 설정을 가져옵니다. 새 이름으로 등록하며 다음 단계에서 바꿀 수 있어요.</p></>}
        {kind === "workloads" && <><Field label="사용할 모델"><select required aria-label="사용할 모델" className="rounded border bg-background p-2" value={model} onChange={event => setModel(event.target.value)}><option value="">등록된 모델 선택</option>{model && !selectedModel && <option value={model}>{model} (현재 목록에서 확인되지 않음)</option>}{models.map(m => <option key={m.metadata.uid} value={m.metadata.name}>{m.metadata.name}</option>)}</select></Field>{!models.length && <p className="text-sm">선택할 모델을 불러오지 못했어요. <Link href="/models">모델 목록 확인</Link></p>}{selectedModel && <DetailsDialog label="선택한 모델 살펴보기" title={selectedModel.metadata.name}><DefinitionSummary spec={selectedModel.spec} /><Impact affected={workloads?.filter(w => w.desired.spec.modelRef.name === selectedModel.metadata.name || w.appliedDefinition?.model.uid === selectedModel.metadata.uid || w.operation?.targetModelUID === selectedModel.metadata.uid || w.desired.status?.appliedDefinition?.modelRef.uid === selectedModel.metadata.uid || w.desired.status?.operation?.targetModelUID === selectedModel.metadata.uid)} /><Link href={href("models", selectedModel.metadata.name, selectedModel.metadata.uid)}>모델 상세 열기</Link></DetailsDialog>}</>}
      </>}
      {kind === "models" && step > 0 && step < 4 && <ModelFields spec={spec} set={setSpec} step={step} />}
      {kind === "workloads" && step === 1 && <><Field label="목표 비율 · %"><Input aria-label="목표 비율 · %" type="number" step="any" min="0" max="100" required value={percentile} onChange={event => setPercentile(event.target.value)} /></Field><p className="text-sm text-muted-foreground">예: 99.5%라면 1,000건 중 995건이 아래 시간 안에 응답하도록 목표를 정합니다. 0보다 크고 100 이하여야 해요.</p><DurationField value={latency} set={setLatency} /><p className="text-sm text-muted-foreground">응답 속도 목표이며, 요청을 기다리는 제한 시간과는 달라요. 변경한 목표는 새로 받는 요청부터 적용돼요.</p></>}
      {step === steps.length - 1 && <><p className="font-medium">{name}</p><DefinitionSummary spec={kind === "models" ? spec : { modelRef: { name: model }, slo: { percentile: Number(percentile), targetLatency: latency } }} /><p className="text-sm text-muted-foreground">저장한 뒤 측정 결과와 서비스 준비 상태를 확인하세요.</p></>}
      <div className="flex gap-3">{step > 0 && <Button type="button" variant="outline" onClick={() => { setStep(step - 1); setValidation("") }}>이전</Button>}<Button type="submit">{step < steps.length - 1 ? "다음" : original ? "변경 내용 확인" : "등록 내용 확인"}</Button></div>
    </fieldset>{validation && <p id="management-validation" role="alert">{validation}</p>}</form>
    {draft && <Confirm title={original ? "변경 내용을 저장할까요?" : "등록할까요?"} busy={busy} disabled={blocked || conflict} cancel={() => setDraft(null)} accept={() => { if (!blocked && !conflict) { const submitted = draft; setDraft(null); onSave(name, submitted) } }}><p className="font-medium">{name}</p>{original && <><h3>변경 전</h3><DefinitionSummary spec={original.spec} /></>}<h3>{original ? "변경 후" : "등록 내용"}</h3><DefinitionSummary spec={draft} />{kind === "models" && original && <Impact affected={workloads?.filter(w => w.desired.spec.modelRef.name === original.metadata.name || w.appliedDefinition?.model.uid === original.metadata.uid || w.operation?.targetModelUID === original.metadata.uid || w.desired.status?.appliedDefinition?.modelRef.uid === original.metadata.uid || w.desired.status?.operation?.targetModelUID === original.metadata.uid)} />}{kind === "workloads" && original && <p className="text-sm">모델을 바꾸면 새 요청을 잠시 멈추고 기존 요청을 마무리해요. 응답 목표만 바꾸면 새 요청부터 적용돼요.</p>}<SourceJSON title="저장 원본과 비교 기준" value={{ original, submitted: draft }} /></Confirm>}
  </section>
}

export function ManagementPage({ kind, name, newResource = false, editResource = false }: { kind: "models" | "workloads"; name?: string; newResource?: boolean; editResource?: boolean }) {
  const queryClient = useQueryClient()
  const expectedUID = useSearchParams().get("uid")
  const path = `v1/${kind}${name ? `/${encodeURIComponent(name)}` : ""}`
  const models = useChillQuery<ModelList>("v1/models")
  const workloads = useChillQuery<WorkloadView[]>("v1/workloads", { poll: !!name && kind === "models" })
  const detail = useChillQuery<ModelResource | WorkloadView>(path, { enabled: !!name, poll: !!name })
  const mutation = useChillMutation()
  const [baseline, setBaseline] = useState<ModelResource | WorkloadView | undefined>()
  const [version, setVersion] = useState<string | undefined>()
  const [conflict, setConflict] = useState(false)
  const [deletedUID, setDeletedUID] = useState<string | null>(null)
  const [deleteConfirm, setDeleteConfirm] = useState(false)
  const [result, setResult] = useState<RelayResponse | null>(null)
  if (name && detail.data && !baseline && ![401, 403].includes(detail.error?.status ?? 0)) {
    const resource = "desired" in detail.data ? detail.data.desired : detail.data
    setBaseline(detail.data); setVersion(resource.metadata.resourceVersion)
  }
  const source = baseline && ("desired" in baseline ? baseline.desired : baseline)
  const current = name && detail.data ? ("desired" in detail.data ? detail.data.desired : detail.data) : undefined
  const identityChanged = !!source && !!current && source.metadata.uid !== current.metadata.uid
  const wrongUID = !!expectedUID && !!current && expectedUID !== current.metadata.uid
  const affected = workloads.error ? undefined : workloads.data?.filter(w => w.desired.spec.modelRef.name === (name ?? "") || (!!source?.metadata.uid && [w.appliedDefinition?.model.uid, w.desired.status?.appliedDefinition?.modelRef.uid, w.operation?.targetModelUID, w.desired.status?.operation?.targetModelUID].includes(source.metadata.uid)))
  const missing = detail.error?.status === 404
  const denied = !!name && [401, 403].includes(detail.error?.status ?? 0)
  const availableModels = [401, 403].includes(models.error?.status ?? 0) ? [] : models.data?.items ?? []
  const blocked = wrongUID || identityChanged || missing || !!deletedUID || denied || mutation.isPending
  const failure = mutation.error?.data as { code?: string; field?: string; message?: string } | null | undefined
  async function submit(method: "POST" | "PUT" | "DELETE", body?: unknown) {
    if (blocked || conflict) return
    setResult(null)
    try {
      const response = await mutation.mutateAsync({ path: method === "POST" ? `v1/${kind}` : path, method, body, version: method === "POST" ? undefined : version })
      setResult(response); setConflict(false)
      if (method === "DELETE") setDeletedUID(source?.metadata.uid ?? null)
      if (method === "PUT" && response.data && typeof response.data === "object") {
        const accepted = response.data as ModelResource | WorkloadView
        const metadata = ("desired" in accepted ? accepted.desired : accepted).metadata
        if (metadata?.uid === source?.metadata.uid) { setBaseline(accepted); setVersion(metadata.resourceVersion) }
      }
      await Promise.all([queryClient.invalidateQueries({ queryKey: ["chill", "v1/models"] }), queryClient.invalidateQueries({ queryKey: ["chill", "v1/workloads"] }), name ? detail.refetch() : Promise.resolve()])
    } catch (error) {
      if (error instanceof ApiError && error.unknownOutcome && method === "DELETE") setDeletedUID(source?.metadata.uid ?? null)
      if (error instanceof ApiError && error.status === 409 && (error.data as {code?:string}|null)?.code === "ResourceVersionConflict") { setConflict(true); await detail.refetch() }
    }
  }
  function adoptLatest() {
    if (blocked || !detail.data || !current || current.metadata.uid !== source?.metadata.uid) return
    setBaseline(detail.data)
    setVersion(current.metadata.resourceVersion)
    setConflict(false)
    mutation.reset()
  }
  const title = kind === "models" ? "모델" : "서비스"
  const back = name && editResource ? href(kind, name, expectedUID ?? source?.metadata.uid ?? "") : `/${kind}`
  return <div className="page grid gap-6"><header className="flex flex-wrap items-center justify-between gap-3"><div className="grid gap-3">{(name || newResource) && <Link className="text-sm text-muted-foreground" href={back}>{editResource ? "상세로 돌아가기" : "목록으로 돌아가기"}</Link>}<h1 className="text-2xl font-semibold">{newResource ? `${title} 등록` : editResource ? `${name} 수정` : name ?? `${title} 목록`}</h1></div>{!name && !newResource && <Button asChild><Link href={`/${kind}/new`}>{title} 등록</Link></Button>}</header>
    {!name && !newResource && (kind === "models" ? <ReadRegion title="등록된 모델" query={models}><DataTable data={models.data?.items ?? []} columns={[
      { key: "name", label: "모델 이름", value: m => <Link href={href("models", m.metadata.name, m.metadata.uid)}>{m.metadata.name}</Link>, sortValue: m => m.metadata.name },
      { key: "artifacts", label: "모델 파일", value: m => m.spec.artifacts.map(a => a.name).join(", ") },
      { key: "services", label: "연결 서비스", value: m => workloads.error ? "확인 불가" : workloads.data ? workloads.data.filter(w => w.desired.spec.modelRef.name === m.metadata.name || w.appliedDefinition?.model.uid === m.metadata.uid).length : "조회 중" },
    ]} /><SourceJSON title="목록 원본" value={models.data} /></ReadRegion> : <ReadRegion title="등록된 서비스" query={workloads}><DataTable data={workloads.data ?? []} columns={[
      { key: "name", label: "서비스 이름", value: w => <Link href={href("workloads", w.desired.metadata.name, w.desired.metadata.uid)}>{w.desired.metadata.name}</Link>, sortValue: w => w.desired.metadata.name },
      { key: "model", label: "모델", value: w => w.desired.spec.modelRef.name },
      { key: "ready", label: "서비스 준비", value: w => w.admission ? "요청을 받을 수 있어요" : statusLabel(w.reason || w.readiness) },
      { key: "slo", label: "응답 목표", value: w => target(w.desired.spec) },
    ]} /><SourceJSON title="목록 원본" value={workloads.data} /></ReadRegion>)}
    {name && !editResource && <ReadRegion title={`${title} 상태`} query={detail}>{detail.data && <section className="grid gap-5">{"desired" in detail.data ? <><p className="text-xl font-medium">{detail.data.admission ? "추론을 시작할 수 있어요" : "아직 추론을 시작할 수 없어요"}</p><p className="text-muted-foreground">{detail.data.admission ? `${detail.data.desired.spec.modelRef.name} · ${target(detail.data.desired.spec)}` : statusLabel(detail.data.reason || detail.data.readiness)}</p><div className="flex flex-wrap gap-3"><Button asChild><Link href={href("workloads", name, detail.data.desired.metadata.uid, "infer")}>추론하기</Link></Button><DetailsDialog label="설정과 준비 과정" title={`${name} 설정과 준비 과정`}><WorkloadState view={detail.data} /></DetailsDialog></div></> : <><p className="text-xl font-medium">등록된 모델</p><p className="text-muted-foreground">모델 파일 {detail.data.spec.artifacts.length}개 · 실행 방식 {detail.data.spec.executionPaths.length}개</p><p className="text-sm text-muted-foreground">등록과 실제 실행 준비는 달라요. 연결된 서비스에서 측정 결과와 준비 상태를 확인하세요.</p><DetailsDialog label="파일과 연결 서비스" title={`${name} 파일과 연결 서비스`}><DefinitionSummary spec={detail.data.spec} /><ReadRegion title="연결 서비스" query={workloads}><Impact affected={affected} /></ReadRegion><SourceJSON title="모델 전체 원본" value={detail.data} /><SourceExport value={detail.data} filename={`model-${name}.json`} /></DetailsDialog></>}
      {current && <div className="flex flex-wrap gap-3 border-t pt-4"><Button asChild variant="outline"><Link href={href(kind, name, expectedUID ?? current.metadata.uid, "edit")}>설정 수정</Link></Button><Button variant="ghost" disabled={blocked || conflict} onClick={() => setDeleteConfirm(true)}>삭제</Button></div>}
    </section>}</ReadRegion>}
    {name && editResource && !baseline && <ReadRegion title="수정할 설정" query={detail}>{null}</ReadRegion>}
    {!denied && <>
      {(wrongUID || identityChanged) && <p role="alert">이름이 같지만 다른 등록 건이에요. 잘못된 대상을 변경하지 않도록 작업을 멈췄어요. <Link href={href(kind, name!, current!.metadata.uid)}>현재 등록 열기</Link></p>}
      {deletedUID && <section role="status" className="grid gap-3"><h2 className="font-semibold">삭제 진행</h2><p>{missing ? "대상을 다시 조회해 삭제 완료를 확인했어요." : current && current.metadata.uid !== deletedUID ? "같은 이름으로 새 등록이 생겼어요. 이전 등록과 별개로 확인하세요." : "삭제 요청을 보냈어요. 아직 실제 삭제 완료는 확인되지 않았어요."}</p>{kind === "workloads" && <Link href={`/observations?workload=${encodeURIComponent(name ?? "")}&uid=${encodeURIComponent(deletedUID)}`}>삭제한 서비스의 요청 기록</Link>}<SourceJSON title="삭제 확인 대상" value={{ name, uid: deletedUID, latest: current, status: detail.error?.status }} /><Button variant="outline" onClick={() => void detail.refetch()}>삭제 상태 다시 확인</Button></section>}
      {deletedUID && current?.metadata.uid === deletedUID && !detail.error && <Button variant="outline" disabled={detail.isFetching || mutation.isPending || (!!detail.data && "desired" in detail.data && !!detail.data.operation && !["Completed", "Failed"].includes(detail.data.operation.phase))} onClick={() => { setDeletedUID(null); setVersion(current.metadata.resourceVersion); mutation.reset(); setResult(null) }}>현재 상태 확인 후 삭제 다시 검토</Button>}
      {(newResource || (editResource && baseline)) && <Editor key={`${kind}/${name ?? "new"}/${source?.metadata.uid ?? "new"}`} kind={kind} resource={baseline ? ("desired" in baseline ? { ...baseline, desired: { ...baseline.desired, metadata: { ...baseline.desired.metadata, resourceVersion: version ?? baseline.desired.metadata.resourceVersion } } } : { ...baseline, metadata: { ...baseline.metadata, resourceVersion: version ?? baseline.metadata.resourceVersion } }) : undefined} models={availableModels} workloads={workloads.error ? undefined : workloads.data} latest={detail.data} conflict={conflict} busy={mutation.isPending} blocked={blocked} adopt={adoptLatest} onSave={(resourceName, spec) => void submit(newResource ? "POST" : "PUT", newResource ? { name: resourceName, spec } : { spec })} />}
      {deleteConfirm && source && <Confirm title={`${name} 삭제할까요?`} busy={mutation.isPending} disabled={blocked || conflict} cancel={() => setDeleteConfirm(false)} accept={() => { if (!blocked && !conflict) { setDeleteConfirm(false); void submit("DELETE") } }}><p>삭제한 등록은 되돌릴 수 없어요. 진행 중인 요청을 마무리한 뒤 삭제합니다.</p><h3>현재 설정</h3><DefinitionSummary spec={source.spec} /><h3>삭제 후</h3><p>이 등록으로 새 요청을 받을 수 없어요. 같은 이름으로 다시 등록해도 이전 요청 기록과는 별개예요.</p>{kind === "models" && <Impact affected={affected} />}<SourceJSON title="삭제 대상 원본" value={source} /></Confirm>}
      {conflict && !editResource && <section role="alert" className="grid gap-3"><p>다른 변경이 먼저 저장됐어요. 최신 설정을 확인한 뒤 다시 삭제를 검토하세요.</p>{current && <DefinitionSummary spec={current.spec} />}<SourceJSON title="삭제 기준과 최신 원본" value={{ baseline, latest: detail.data }} /><Button disabled={blocked || !current || current.metadata.uid !== source?.metadata.uid} onClick={adoptLatest}>최신 상태 확인했어요</Button></section>}
      {mutation.error && <section id="management-api-error" role="alert" className="grid gap-3"><h2 className="font-semibold">{mutation.error.unknownOutcome ? "저장 결과를 확인하지 못했어요" : "변경을 저장하지 못했어요"}</h2><p>작성한 내용은 유지했어요. {failure?.field?.includes("slo") ? "응답 목표를 확인해 주세요." : failure?.field?.includes("model") ? "선택한 모델을 확인해 주세요." : failure?.field?.includes("artifact") ? "모델 파일과 입력 정보를 확인해 주세요." : failure?.field?.includes("execution") ? "실행 방식을 확인해 주세요." : "자세한 이유를 확인하고 다시 시도하세요."}</p><SourceJSON title="변경 오류 자세히" value={{ status: mutation.error.status, error: failure, body: mutation.error.body }} /><p>결과가 불확실하면 현재 상태를 먼저 확인하세요. 자동으로 다시 제출하지 않아요.</p>{name && <Button variant="outline" onClick={() => void detail.refetch()}>최신 상태 확인</Button>}</section>}
      {result && <section role="status" className="grid gap-3"><h2 className="font-semibold">{result.status === 201 ? "등록했어요. 실제 준비 상태를 확인하세요." : result.status === 202 ? "변경 요청을 받았어요. 적용 진행을 확인하세요." : result.status === 204 ? "삭제 요청 응답을 받았어요. 삭제 완료를 확인 중이에요." : result.status === 200 ? "변경 응답을 받았어요. 현재 설정을 확인하세요." : "응답을 받았어요."}</h2><SourceJSON title="저장 응답 원본" value={{ status: result.status, body: result.body }} />{newResource && result.data !== null && typeof result.data === "object" && <CreatedLink kind={kind} data={result.data as ModelResource | WorkloadView} />}</section>}
    </>}
  </div>
}
function CreatedLink({ kind, data }: { kind: string; data: ModelResource | WorkloadView }) {
  const metadata = ("desired" in data ? data.desired : data).metadata
  return metadata ? <Link href={href(kind, metadata.name, metadata.uid)}>등록 상세와 준비 상태 확인</Link> : null
}

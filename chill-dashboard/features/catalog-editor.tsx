"use client"

import Link from "next/link"
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react"
import { useChillQuery } from "@/lib/client"
import { DetailsDialog, QueryGate, SourceJSON, StatusBadge } from "@/components/read-region"
import { Empty, Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { backendsByFormat, datatypes, formatText, positiveIntegers, resourceHref as href, sloTarget as target, workloadSpec, type ModelResource, type ModelList, type ModelSpec, type ModelArtifact, type ExecutionPath, type TensorContract, fieldFormats, type RawField, type PreprocessorArtifact, type PreprocessorResource, type PreprocessorSpec, type DatasetResource, type DatasetSpec, type WorkloadSummary } from "@/lib/domain"
import { explain } from "@/lib/labels"
import { ServiceAdmission } from "@/features/service-hub"
import { DataTable } from "@/components/data-table"
import { datasetGap } from "@/lib/measurement-request"
import { connected, unwrap, useResource, type Kind, type Referenced, type Resource, type ResourceState, type Spec } from "@/hooks/use-catalog-resource"
import { CatalogRegistration } from "@/components/catalog-registration"

export const kindLabel = (kind: Kind) => ({ models: "모델", workloads: "서비스", preprocessors: "모델 입출력 처리", datasets: "데이터셋" })[kind]
const emptyField = (): RawField => ({ name: "", format: "JPEG", maxBytes: 1048576 })
const emptyTarget = (): PreprocessorArtifact => ({ target: { model: "", artifact: "" }, source: { image: "", architecture: "", path: "" } })
const subsets = (path: string) => [`${path.replace(/\/+$/, "")}/profiling`, `${path.replace(/\/+$/, "")}/evaluation`]
const emptyTensor = (): TensorContract => ({ name: "", datatype: "FP32", sampleShape: [1] })
const emptyArtifact = (): ModelArtifact => ({ name: "", format: "", source: { image: "", architecture: "", files: [""] } })
const emptyPath = (): ExecutionPath => ({ name: "", artifact: "", runtime: { family: "firstfloor", backend: "", settings: { firstfloor: { maxBatchSize: 1, maxQueueSize: 1, deadlinePolicy: "keep" } } } })
export const select = "rounded border bg-background p-2"
// 4194304 → "4 MiB"; exact byte counts stay in the editor inputs and the 원본 dialogs.
function size(bytes: number) {
  const units = ["B", "KiB", "MiB", "GiB"]
  let value = bytes, unit = 0
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++ }
  return `${Number(value.toFixed(1))} ${units[unit]}`
}
// Particle by the last syllable's final consonant; Latin names are read by their last letter (l, m, n end in one).
function josa(word: string, [after, plain]: [string, string]) {
  const c = word.charCodeAt(word.length - 1) - 0xac00
  return word + ((c >= 0 && c < 11172 ? c % 28 !== 0 : /[lmn013678]$/i.test(word)) ? after : plain)
}
const and = (words: string[]) => words.map((w, i) => i < words.length - 1 ? josa(w, ["과", "와"]) : w).join(" ")
// Contract names as written, each with its format in plain words: "image(사진), question(글)".
const named = (fields: { name: string; format: RawField["format"] }[]) => fields.map(f => `${f.name}(${formatText(f.format)})`).join(", ")
// The plain conclusion of a catalog contract: what it takes and returns, or which records it holds.
const conclusion = (spec: PreprocessorSpec | DatasetSpec) => "input" in spec
  ? `${named(spec.input.fields)} 입력을 받아 ${named(spec.output.fields)} 출력을 만들어요`
  : `${named(spec.fields)} 필드가 있는 기록을 담고 있어요`
// Preview of serving/kube snapshot(): a model path runs only when the preprocessor targets its existing artifact.
function preprocessorGap(p: PreprocessorSpec, model: ModelResource | undefined) {
  if (!model) return ""
  const targets = p.artifacts.filter(a => a.target.model === model.metadata.name && model.spec.artifacts.some(m => m.name === a.target.artifact)).map(a => a.target.artifact)
  if (!targets.length) return "이 모델 파일을 위한 전처리 파일이 없어요"
  return model.spec.executionPaths.some(path => targets.includes(path.artifact)) ? "" : "이 전처리 파일로 실행할 방식이 모델에 없어요"
}

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
function Choices({ label, value, set, options }: { label: string; value: string; set: (value: string) => void; options: { value: string; label: ReactNode; hint?: ReactNode; disabled?: boolean }[] }) {
  return <fieldset className="rows-group"><legend className="rows-label">{label}</legend><ul className="rows">{options.map(option => <li key={option.value}><label className={option.disabled ? "row cursor-not-allowed opacity-60" : "row cursor-pointer"}>
    <input type="radio" required name={label} value={option.value} checked={value === option.value} disabled={option.disabled} onChange={() => set(option.value)} />
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
function Integer({ label, value, set, note }: { label: string; value: number; set: (value: number) => void; note?: ReactNode }) {
  return <Field label={label}><Input aria-label={label} type="number" required min={1} max={2147483647} step={1} value={Number.isNaN(value) ? "" : value} onChange={event => set(event.target.valueAsNumber)} />{note && <span className="text-xs text-muted-foreground">{note}</span>}</Field>
}
// Raw record fields: input/output of a preprocessor and the records of a dataset. Output fields carry no maxBytes.
function RawFields<T extends { name: string; format: RawField["format"]; maxBytes?: number }>({ label, fields, set, blank }: { label: string; fields: T[]; set: (fields: T[]) => void; blank: () => T }) {
  const change = (index: number, value: Partial<T>) => set(fields.map((f, i) => i === index ? { ...f, ...value } : f))
  return <fieldset className="grid gap-4 rounded-lg border p-4"><legend>{label}</legend>{fields.map((f, i) => <div key={i} className="grid items-end gap-3 sm:grid-cols-4">
    <Text label={`${label} ${i + 1} 이름`} value={f.name} set={name => change(i, { name } as Partial<T>)} /><Field label={`${label} ${i + 1} 형식`}><select required aria-label={`${label} ${i + 1} 형식`} className={select} value={f.format} onChange={event => change(i, { format: event.target.value } as Partial<T>)}>{fieldFormats.map(format => <option key={format}>{format}</option>)}</select></Field>{f.maxBytes !== undefined && <Integer label={`${label} ${i + 1} 최대 크기 · 바이트`} value={f.maxBytes} set={maxBytes => change(i, { maxBytes } as Partial<T>)} note={f.maxBytes >= 1 ? `= ${size(f.maxBytes)}` : undefined} />}<Button type="button" variant="outline" onClick={() => set(fields.filter((_, j) => j !== i))}>이 필드 빼기</Button>
  </div>)}<Button type="button" variant="outline" onClick={() => set([...fields, blank()])}>{label} 추가</Button></fieldset>
}
function DurationField({ value, set }: { value: string; set: (value: string) => void }) {
  // This only splits ordinary display values; compound durations remain untouched for the API.
  const simple = /^(\d+(?:\.\d+)?)(ns|us|µs|μs|ms|s|m|h)$/.exec(value)
  const [advanced, setAdvanced] = useState(!simple)
  const unit = simple?.[2] ?? "ms"
  return <div className="grid gap-3">{advanced ? <Text label="응답 목표 시간 · 원래 표현" value={value} set={set} /> : <div className="grid grid-cols-[1fr_8rem] gap-3"><Field label="응답 목표 시간"><Input aria-label="응답 목표 시간" type="number" min="0" step="any" required value={simple?.[1] ?? ""} onChange={event => set(`${event.target.value}${unit}`)} /></Field><Field label="시간 단위"><select aria-label="시간 단위" className={select} value={unit} onChange={event => set(`${simple?.[1] ?? ""}${event.target.value}`)}>{[["ms", "밀리초"], ["s", "초"], ["m", "분"], ["h", "시간"], ["us", "마이크로초"], ["ns", "나노초"], ["µs", "마이크로초 (µs)"], ["μs", "마이크로초 (μs)"]].map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field></div>}
    <label className="flex gap-2 text-sm text-muted-foreground"><input type="checkbox" checked={advanced} disabled={!simple && advanced} onChange={event => setAdvanced(event.target.checked)} />복합 시간 직접 입력 (예: 1m30s)</label>
    <details><summary className="min-h-11 cursor-pointer py-2">도움말</summary>입력한 시간은 그대로 저장해요. 0보다 커야 하고, 최종 허용 범위는 저장할 때 확인해요.</details>
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
  const format = spec.artifacts.find(a => a.name === execution?.artifact)?.format ?? ""
  const backends = backendsByFormat[format] ?? []
  // A format change clears the backend of every path on this file that the new format no longer allows.
  function artifact(change: Partial<ModelArtifact>) {
    const allowed = backendsByFormat[change.format ?? row.format] ?? []
    set({ ...spec, artifacts: spec.artifacts.map((a, i) => i === index ? { ...a, ...change } : a), executionPaths: spec.executionPaths.map(p => p.artifact === row.name && !allowed.includes(p.runtime.backend) ? { ...p, runtime: { ...p.runtime, backend: "" } } : p) })
  }
  function path(change: Partial<ExecutionPath>) { set({ ...spec, executionPaths: spec.executionPaths.map((p, i) => i === pi ? { ...p, ...change } : p) }) }
  return <div className="grid gap-5">
    {step !== "paths" && <><Field label="설정할 모델 파일"><select aria-label="설정할 모델 파일" className={select} value={index} onChange={event => setArtifactIndex(Number(event.target.value))}>{spec.artifacts.map((a, i) => <option key={i} value={i}>{a.name || `파일 ${i + 1}`}</option>)}</select></Field>{!row && <p>모델 파일을 추가해 주세요.</p>}</>}
    {step === "artifacts" && <>
      {models.some(m => m.spec.artifacts.length) && <Field label="등록된 모델의 파일 가져오기"><select aria-label="등록된 모델의 파일 가져오기" className={select} value="" onChange={event => {
        const [mi, ai] = event.target.value.split("/").map(Number)
        const copied = models[mi]?.spec.artifacts[ai]
        if (copied) { setArtifactIndex(spec.artifacts.length); set({ ...spec, artifacts: [...spec.artifacts, structuredClone(copied)] }) }
      }}><option value="">가져올 파일 고르기</option>{models.flatMap((m, mi) => m.spec.artifacts.map((a, ai) => <option key={`${mi}/${ai}`} value={`${mi}/${ai}`}>{m.metadata.name} · {a.name}</option>))}</select></Field>}
      {row && <div key={index} className="grid gap-4"><Text label="파일 이름" value={row.name} set={name => artifact({ name })} /><Choice label="파일 형식" value={row.format} set={format => artifact(format === "rknn" ? { format } : { format, input: undefined })} options={["onnx", "rknn"]} /><Text label="모델 파일 보관 주소" value={row.source.image} set={image => artifact({ source: { ...row.source, image } })} /><details><summary className="min-h-11 cursor-pointer py-2">도움말</summary>변하지 않는 정확한 버전의 주소를 적어 주세요. 예: registry.example/model@sha256:…</details><Choice label="대상 장비 종류" value={row.source.architecture} set={architecture => artifact({ source: { ...row.source, architecture } })} options={["amd64", "arm64"]} /><Text label="보관 이미지 안의 모델 파일 위치 · 여러 개면 쉼표로 구분" value={row.source.files.join(", ")} set={text => artifact({ source: { ...row.source, files: text.split(",").map(file => file.trim()) } })} /><details><summary className="min-h-11 cursor-pointer py-2">도움말</summary>예: /artifact/vision_encoder.onnx, /artifact/decoder_model_merged.onnx. 모든 파일을 함께 불러와요.</details>
        <label className="flex gap-2 text-sm"><input type="checkbox" checked={!!row.derivation} onChange={event => artifact({ derivation: event.target.checked ? { sourceArtifact: "", targetPlatform: "", declaredBatchSizes: [1], conversion: { tool: "", version: "" } } : undefined })} />다른 파일에서 변환했어요</label>
        {row.derivation && <><Field label="변환 전 파일"><select required aria-label="변환 전 파일" className={select} value={row.derivation.sourceArtifact} onChange={event => artifact({ derivation: { ...row.derivation!, sourceArtifact: event.target.value } })}><option value="">원본 파일 선택</option>{spec.artifacts.filter((_, i) => i !== index).map((a, i) => <option key={i} value={a.name}>{a.name || "이름 없는 파일"}</option>)}</select></Field><Text label="변환 대상 장비" value={row.derivation.targetPlatform} set={targetPlatform => artifact({ derivation: { ...row.derivation!, targetPlatform } })} /><IntegerList label="지원 묶음 크기 · 개수, 쉼표로 구분" value={row.derivation.declaredBatchSizes} set={declaredBatchSizes => artifact({ derivation: { ...row.derivation!, declaredBatchSizes } })} /><Text label="변환 도구" value={row.derivation.conversion.tool} set={tool => artifact({ derivation: { ...row.derivation!, conversion: { ...row.derivation!.conversion, tool } } })} /><Text label="변환 도구 버전" value={row.derivation.conversion.version} set={version => artifact({ derivation: { ...row.derivation!, conversion: { ...row.derivation!.conversion, version } } })} /></>}
        <Button type="button" variant="outline" onClick={() => set({ ...spec, artifacts: spec.artifacts.filter((_, i) => i !== index) })}>이 파일 빼기</Button></div>}
      <Button type="button" variant="outline" onClick={() => { setArtifactIndex(spec.artifacts.length); set({ ...spec, artifacts: [...spec.artifacts, emptyArtifact()] }) }}>새 모델 파일 추가</Button>
    </>}
    {step === "inputs" && row && row.format !== "rknn" && <details><summary className="min-h-11 cursor-pointer py-2">도움말</summary>{row.format || "이"} 파일의 입력은 이 파일을 대상으로 하는 전처리기가 정해요. RKNN 파일만 입력을 적어요.</details>}
    {step === "inputs" && row && row.format === "rknn" && <>
      <details><summary className="min-h-11 cursor-pointer py-2">도움말</summary>한 번의 입력 크기만 적고, 여러 요청을 묶는 축은 빼 주세요. 크기 예: 3, 224, 224 (채널, 높이, 너비).</details>
      {(row.input?.tensors ?? []).map((tensor, ti) => {
        const tensorSet = (change: Partial<TensorContract>) => artifact({ input: { tensors: row.input!.tensors.map((t, i) => i === ti ? { ...t, ...change } : t) } })
        return <fieldset key={`${index}/${ti}`} className="grid gap-4 rounded-lg border p-4"><legend>입력 {ti + 1}</legend><Text label={`입력 ${ti + 1} 이름`} value={tensor.name} set={name => tensorSet({ name })} /><Field label={`입력 ${ti + 1} 값 종류`}><select aria-label={`입력 ${ti + 1} 값 종류`} className={select} value={tensor.datatype} onChange={event => tensorSet({ datatype: event.target.value as TensorContract["datatype"] })}>{datatypes.map(type => <option key={type} value={type}>{type === "BYTES" ? "문자열 (BYTES)" : type === "BOOL" ? "참·거짓 (BOOL)" : type}</option>)}</select></Field><IntegerList label={`입력 ${ti + 1} 크기 · 각 축의 값 개수`} value={tensor.sampleShape} set={sampleShape => tensorSet({ sampleShape })} /><Button type="button" variant="outline" onClick={() => artifact({ input: { tensors: row.input!.tensors.filter((_, i) => i !== ti) } })}>이 입력 빼기</Button></fieldset>
      })}<Button type="button" variant="outline" onClick={() => artifact({ input: { tensors: [...(row.input?.tensors ?? []), emptyTensor()] } })}>입력 추가</Button>
    </>}
    {step === "paths" && <><Field label="설정할 실행 방식"><select aria-label="설정할 실행 방식" className={select} value={pi} onChange={event => setPathIndex(Number(event.target.value))}>{spec.executionPaths.map((p, i) => <option key={i} value={i}>{p.name || `실행 방식 ${i + 1}`}</option>)}</select></Field>
      {execution && <div key={pi} className="grid gap-4"><Text label="실행 방식 이름" value={execution.name} set={name => path({ name })} /><Field label="사용할 모델 파일"><select required aria-label="사용할 모델 파일" className={select} value={execution.artifact} onChange={event => {
        // The backend follows the artifact format; a kept pick survives only when the new format still allows it.
        const allowed = backendsByFormat[spec.artifacts.find(a => a.name === event.target.value)?.format ?? ""] ?? []
        path({ artifact: event.target.value, runtime: { ...execution.runtime, family: "firstfloor", backend: allowed.includes(execution.runtime.backend) ? execution.runtime.backend : allowed.length === 1 ? allowed[0] : "" } })
      }}><option value="">파일 선택</option>{spec.artifacts.map((a, i) => <option key={i} value={a.name}>{a.name || "이름 없는 파일"}</option>)}</select></Field>
        <p className="text-sm">실행기는 {explain("firstfloor")[0]}로 정해져 있어요.</p>
        {format === "rknn" && <p className="notice text-sm">RKNN 파일로 실행하려면 Rockchip RK3588 장비용으로 변환한 파일이어야 해요. 변환 정보에 지원 묶음 크기를 적고, 모델 입력은 실수(FP32) 값으로 된 3축 입력 하나여야 해요.</p>}
        {backends.length ? <Choices label="처리 방식" value={execution.runtime.backend} set={backend => path({ runtime: { ...execution.runtime, family: "firstfloor", backend, settings: execution.runtime.settings ?? emptyPath().runtime.settings } })} options={backends.map(backend => ({ value: backend, label: explain(backend)[0] }))} />
          : <details><summary className="min-h-11 cursor-pointer py-2">도움말</summary>{execution.artifact ? "이 파일 형식으로는 실행할 수 없어요. ONNX나 RKNN 파일을 골라 주세요." : "사용할 모델 파일을 먼저 골라 주세요. 처리 방식은 파일 형식에 맞는 것만 보여요."}</details>}
        <details><summary className="min-h-11 cursor-pointer py-2">도움말</summary>한 번에 묶어 실행할 요청 수와 대기 한도를 정해요. 대기 한도는 서비스마다 따로 적용돼요. 최종 조합은 저장할 때 확인해요.</details>
        {execution.runtime.settings?.firstfloor && <>{([["maxBatchSize", "최대 묶음 크기 · 건"], ["maxQueueSize", "서비스별 대기 한도 · 건"]] as const).map(([key, label]) => <Integer key={key} label={label} value={execution.runtime.settings!.firstfloor![key]} set={value => path({ runtime: { ...execution.runtime, settings: { firstfloor: { ...execution.runtime.settings!.firstfloor!, [key]: value } } } })} />)}<Field label="응답 목표를 지키기 어려운 요청"><select required aria-label="응답 목표를 지키기 어려운 요청" className={select} value={execution.runtime.settings.firstfloor.deadlinePolicy ?? "keep"} onChange={event => path({ runtime: { ...execution.runtime, settings: { firstfloor: { ...execution.runtime.settings!.firstfloor!, deadlinePolicy: event.target.value as "drop" | "keep" | "defer" } } } })}><option value="keep">계속 처리 (기본값)</option><option value="drop">실행 전 거절</option><option value="defer">정상 대기 요청 뒤로 미루기</option></select></Field><details><summary className="min-h-11 cursor-pointer py-2">도움말</summary>이미 실행 중인 요청은 끝까지 처리해요. 정책을 바꾸면 새 실행 인스턴스로 교체돼요. 고정 묶음 측정에서는 거절하거나 미루지 않아요.</details></>}
        <Button type="button" variant="outline" onClick={() => set({ ...spec, executionPaths: spec.executionPaths.filter((_, i) => i !== pi) })}>이 실행 방식 빼기</Button></div>}
      <Button type="button" variant="outline" onClick={() => { setPathIndex(spec.executionPaths.length); set({ ...spec, executionPaths: [...spec.executionPaths, emptyPath()] }) }}>실행 방식 추가</Button>
    </>}
  </div>
}

export const fieldList = (fields: { name: string; format: string; maxBytes?: number }[]) => fields.map(f => `${f.name} (${f.format}${f.maxBytes ? ` · 최대 ${size(f.maxBytes)}` : ""})`).join(", ") || "없음"
function brief(spec: Spec): [string, string][] {
  if ("input" in spec) return [["입력", fieldList(spec.input.fields)], ["출력", fieldList(spec.output.fields)], ["대상 모델 파일", spec.artifacts.map(a => `${a.target.model} · ${a.target.artifact}`).join(", ") || "없음"]]
  if ("source" in spec) return [["데이터 위치", `${spec.source.image} · ${spec.source.path}`], ["기록 필드", fieldList(spec.fields)]]
  return "modelRef" in spec ? [["모델", spec.modelRef.name], ["전처리기", spec.preprocessorRef.name], ["데이터셋", spec.datasetRef?.name ?? "정하지 않음"], ["응답 목표", target(spec)]] : [["모델 파일", spec.artifacts.map(a => a.name).join(", ") || "없음"], ["실행 방식", spec.executionPaths.map(p => p.name).join(", ") || "없음"]]
}
export function DefinitionSummary({ spec }: { spec: Spec }) {
  const items: [string, string][] = "input" in spec ? [
    ...spec.input.fields.map((f): [string, string] => [`입력 ${f.name}`, `${formatText(f.format)} · 최대 ${size(f.maxBytes)}`]),
    ...spec.output.fields.map((f): [string, string] => [`출력 ${f.name}`, formatText(f.format)]),
    ["최대 생성 토큰", spec.maxNewTokens ? `${spec.maxNewTokens}개` : "정하지 않음"],
    ...spec.artifacts.map((a, i): [string, string] => [`대상 ${i + 1}`, `${a.target.model} · ${a.target.artifact} · ${a.source.architecture}`]),
  ] : "source" in spec ? [
    ...spec.fields.map((f): [string, string] => [`필드 ${f.name}`, `${formatText(f.format)} · 최대 ${size(f.maxBytes)}`]),
    ["용도", "측정용 profiling · 평가용 evaluation"],
  ] : "modelRef" in spec ? brief(spec) : [
    ...spec.artifacts.map((a): [string, string] => [`모델 파일 ${a.name}`, `${a.format.toUpperCase()} · ${a.source.architecture}`]),
    ...spec.executionPaths.map((p): [string, string] => [`실행 방식 ${p.name}`, `${explain(p.runtime.backend)[0]} · ${p.artifact}`]),
  ]
  const sources: [string, string][] = "input" in spec ? spec.artifacts.flatMap((a, i): [string, string][] => [
    [`대상 ${i + 1} 보관 이미지`, a.source.image], [`대상 ${i + 1} 파일 위치`, a.source.path],
  ]) : "source" in spec ? [
    ["보관 이미지", spec.source.image], ["데이터 위치", spec.source.path], ["측정용 묶음", subsets(spec.source.path)[0]], ["평가용 묶음", subsets(spec.source.path)[1]],
  ] : "modelRef" in spec ? [] : spec.artifacts.flatMap((a): [string, string][] => [
    [`${a.name} 보관 이미지`, a.source.image], [`${a.name} 파일 위치`, a.source.files.join(", ")],
  ])
  return <div className="grid gap-4"><DataTable searchable={false} data={items} columns={[{key:"name",label:"항목",value:row=>row[0]},{key:"value",label:"정의",value:row=>row[1]}]}/>{sources.length > 0 && <details><summary className="min-h-11 cursor-pointer py-2">보관 이미지와 파일 위치</summary><Facts items={sources} /></details>}</div>
}
const impactNotes: Record<Referenced, string> = {
  models: "모델을 바꾸면 연결된 서비스는 새 요청을 잠시 멈추고 기존 요청을 마무리해요. 쓰는 서비스가 있는 모델은 삭제할 수 없어요. 모델 입출력 처리가 대상으로 삼은 모델은 삭제할 수 없고, 대상이 된 모델 파일도 뺄 수 없어요.",
  preprocessors: "모델 입출력 처리를 바꾸면 연결된 서비스는 새 요청을 잠시 멈추고 기존 요청을 마무리한 뒤 새 설정을 적용해요. 연결된 서비스의 데이터셋과 필드가 맞지 않게 되는 변경과 쓰는 서비스가 있는 모델 입출력 처리의 삭제는 거절돼요.",
  datasets: "데이터셋을 바꿔도 서비스는 멈추지 않고, 이후 측정부터 새 내용을 써요. 연결된 서비스의 모델 입출력 처리와 필드가 맞지 않게 되는 변경과 쓰는 서비스가 있는 데이터셋의 삭제는 거절돼요.",
}
export function Impact({ kind, affected }: { kind: Referenced; affected: WorkloadSummary[] | undefined }) {
  return <>
    {affected === undefined ? <p role="status">연결 서비스 미확인</p> : <DataTable searchable={false} data={affected} columns={[{key:"name",label:"연결 서비스",value:view=><Link href={href("workloads",view.desired.metadata.name,view.desired.metadata.uid)}>{view.desired.metadata.name}</Link>},{key:"admission",label:"접수 허용",value:view=><ServiceAdmission view={view}/>}]}/>}
    <details><summary className="min-h-11 cursor-pointer py-2">변경·삭제 제약</summary>{impactNotes[kind]}</details>
  </>
}
// Who still uses a registration: services, and for models the Preprocessors targeting it. An unknown uid stays "".
type Users = { workloads: { name: string; uid: string }[]; preprocessors: { name: string; uid: string }[] }
export const usersOf = (affected: WorkloadSummary[] | undefined, preprocessors: PreprocessorResource[] = []): Users => ({
  workloads: (affected ?? []).map(w => ({ name: w.desired.metadata.name, uid: w.desired.metadata.uid })),
  preprocessors: preprocessors.map(p => ({ name: p.metadata.name, uid: p.metadata.uid })),
})
function who(users: Users) {
  const groups: [Users["workloads"], string][] = [[users.workloads, "서비스"], [users.preprocessors, "전처리기"]]
  return and(groups.filter(([list]) => list.length).map(([list, label]) => list.length === 1 ? `${list[0].name} ${label}` : `${list.length}개 ${label}`))
}
const unblock = (kind: Kind, users: Users) => [
  users.workloads.length ? `서비스 설정에서 다른 ${josa(kindLabel(kind), ["을", "를"])} 고르거나 서비스를 지운 뒤 다시 시도해 주세요.` : "",
  users.preprocessors.length ? "전처리기의 대상에서 이 모델을 뺀 뒤 다시 시도해 주세요." : "",
].filter(Boolean).join(" ")
function UserRows({ users }: { users: Users }) {
  return <>
    {users.workloads.length > 0 && <Rows label="쓰는 서비스">{users.workloads.map(w => <Row key={w.name} href={href("workloads", w.name, w.uid)} label={w.name} hint="서비스 보기" />)}</Rows>}
    {users.workloads.length > 0 && <Rows label="서비스 설정 바꾸기">{users.workloads.map(w => <Row key={w.name} href={href("workloads", w.name, w.uid, "edit")} label={`${w.name} 설정 수정`} />)}</Rows>}
    {users.preprocessors.length > 0 && <Rows label="대상으로 삼은 전처리기">{users.preprocessors.map(p => <Row key={p.name} href={href("preprocessors", p.name, p.uid)} label={p.name} hint="전처리기 보기" />)}</Rows>}
  </>
}
// Known users block deletion without a request; ChillServer still answers 409 when this view is stale.
export function DeleteRow({ kind, r, users, open }: { kind: Kind; r: ResourceState; users: Users; open: () => void }) {
  if (!users.workloads.length && !users.preprocessors.length) return <Row label="삭제" hint="되돌릴 수 없어요" onClick={() => { if (!r.blocked && !r.conflict) open() }} />
  return <RowDialog label="삭제" hint="쓰는 곳이 있어서 지금은 지울 수 없어요" title={`${josa(who(users), ["이", "가"])} 쓰고 있어서 지울 수 없어요`} description={unblock(kind, users)}><UserRows users={users} /></RowDialog>
}
// 409 reference refusals from serving/kube manager.go carry Go %v name lists: "Workloads [a b] and Preprocessors [c]".
function refusal(failure: { code?: string; message?: string } | null, workloads: WorkloadSummary[] | undefined): Users | undefined {
  const code = failure?.code ?? ""
  const expected = code === "ModelReferenced" ? 2 : ["PreprocessorReferenced", "DatasetReferenced", "ModelArtifactReferenced"].includes(code) ? 1 : 0
  const lists = (failure?.message?.match(/\[[^\]]*\]/g) ?? []).map(list => list.slice(1, -1).split(" ").filter(Boolean))
  if (!expected || lists.length !== expected || !lists.flat().length) return undefined
  const [names, targeting]: string[][] = code === "ModelArtifactReferenced" ? [[], lists[0]] : [lists[0], lists[1] ?? []]
  const uid = (name: string) => workloads?.find(w => w.desired.metadata.name === name)?.desired.metadata.uid ?? ""
  return { workloads: names.map(name => ({ name, uid: uid(name) })), preprocessors: targeting.map(name => ({ name, uid: "" })) }
}
export function Mismatch({ kind, name, uid }: { kind: Kind; name: string; uid: string }) {
  return <Screen back={[`/${kind}`, kindLabel(kind)]} status={<StatusBadge tone="warn">작업 멈춤</StatusBadge>} title="등록 식별자 불일치" action={<Button asChild><Link href={href(kind, name, uid)}>현재 등록 열기</Link></Button>} />
}
// Feedback for the last write on this screen; the draft is never discarded and never resent automatically.
export function Problem({ r, kind }: { r: ResourceState; kind: Kind }) {
  if (r.conflict) return <div role="alert" className="notice">
    <p className="font-medium">다른 변경이 먼저 저장됐어요. 최신 설정을 확인한 뒤 다시 삭제를 검토해 주세요.</p>
    <div className="flex flex-wrap gap-2"><DetailsDialog label="최신 설정 보기" title="최신 설정">{r.current && <DefinitionSummary spec={r.current.spec} />}<SourceJSON inline title="삭제 기준과 최신 원본" value={{ baseline: r.baseline, latest: r.detail.data }} /></DetailsDialog><Button size="sm" disabled={r.blocked || !r.current || r.current.metadata.uid !== r.source?.metadata.uid} onClick={r.adopt}>최신 상태 확인했어요</Button></div>
  </div>
  const error = r.mutation.error
  if (!error) return null
  const failure = error.data as { code?: string; field?: string; message?: string } | null
  const users = error.status === 409 ? refusal(failure, r.workloads.data) : undefined
  if (users) {
    const artifacts = failure?.code === "ModelArtifactReferenced"
    return <div role="alert" className="notice">
      <p className="font-medium">{artifacts ? `${josa(who(users), ["이", "가"])} 대상으로 삼은 모델 파일은 뺄 수 없어요` : `${josa(who(users), ["이", "가"])} 이 ${josa(kindLabel(kind), ["을", "를"])} 쓰고 있어서 지울 수 없어요`}</p>
      <p>{artifacts ? "작성한 내용은 그대로 있어요. 그 파일을 남겨 두거나, 전처리기의 대상에서 먼저 빼 주세요." : unblock(kind, users)}</p>
      <UserRows users={users} />
      <SourceJSON title="거절 이유 자세히" value={{ status: error.status, error: failure, body: error.body }} />
    </div>
  }
  // A body code reads as its sentence and next action; the code itself stays in 변경 오류 자세히.
  const [meaning, step] = failure?.code ? explain(failure.code) : ["변경을 저장하지 못했어요."]
  return <div role="alert" className="notice">
    <p className="font-medium">{error.unknownOutcome ? "저장 결과를 확인하지 못했어요" : meaning}</p>
    <p>작성한 내용은 그대로 있어요. {failure?.field?.includes("slo") ? "응답 목표를 확인해 주세요." : failure?.field?.includes("preprocessorRef") ? "선택한 전처리기를 확인해 주세요." : failure?.field?.includes("datasetRef") ? "선택한 데이터셋이 있고 전처리기 입력 필드를 담는지 확인해 주세요." : failure?.field?.includes("target") ? "대상 모델과 모델 파일을 확인해 주세요." : failure?.field?.includes("model") ? "선택한 모델을 확인해 주세요." : failure?.field?.includes("source") ? "보관 주소와 위치를 확인해 주세요." : failure?.field?.includes("artifact") ? "모델 파일과 입력 정보를 확인해 주세요." : failure?.field?.includes("execution") ? "실행 방식을 확인해 주세요." : /input|output|fields/.test(failure?.field ?? "") ? "입력과 출력 필드를 확인해 주세요. 연결된 서비스의 전처리기와 데이터셋 필드가 맞아야 해요." : step ?? "자세한 이유를 확인하고 다시 시도해 주세요."} 결과가 불확실하면 현재 상태를 먼저 확인해 주세요. 자동으로 다시 보내지 않아요.</p>
    <div className="flex flex-wrap gap-2"><SourceJSON title="변경 오류 자세히" value={{ status: error.status, error: failure, body: error.body }} />{r.name ? <Button size="sm" variant="outline" onClick={() => void r.detail.refetch()}>최신 상태 확인</Button> : <Button asChild size="sm" variant="outline"><Link href={`/${kind}`}>목록에서 확인</Link></Button>}</div>
  </div>
}
export function DeleteConfirm({ kind, name, r, close }: { kind: Kind; name: string; r: ResourceState; close: () => void }) {
  if (!r.source) return null
  return <Confirm title={`${name} ${josa(kindLabel(kind), ["을", "를"])} 지울까요?`} busy={r.mutation.isPending} disabled={r.blocked || r.conflict} cancel={close} accept={() => { close(); void r.submit("DELETE") }}>
    <p>{kind === "workloads" ? "진행 중인 요청을 마무리한 뒤 지워요. 지우면 새 요청을 받을 수 없고, 같은 이름으로 다시 등록해도 이전 요청 기록과는 별개예요." : `${name} 등록을 지워요.`} 되돌릴 수 없어요.</p>
    {kind !== "workloads" && <Impact kind={kind} affected={r.affected} />}
  </Confirm>
}
export function Deleted({ kind, name, uid, r }: { kind: Kind; name: string; uid: string; r: ResourceState }) {
  const { missing, current, detail, mutation } = r
  const replaced = !missing && !!current && current.metadata.uid !== uid
  const operation = detail.data && "desired" in detail.data ? detail.data.operation : undefined
  const canReview = !missing && current?.metadata.uid === uid && !detail.error && !(operation && !["Completed", "Failed"].includes(operation.phase))
  const unknown = !!mutation.error?.unknownOutcome
  return <Screen back={[`/${kind}`, kindLabel(kind)]} status={<StatusBadge tone={missing ? "ok" : "info"}>{missing ? "삭제 완료" : "삭제 확인 중"}</StatusBadge>}
    title={missing ? "삭제 완료" : replaced ? "동일 이름 신규 등록" : unknown ? "삭제 결과 미확인" : "삭제 요청 접수"}
    action={missing ? <Button asChild><Link href={`/${kind}`}>목록으로 가기</Link></Button> : <Button variant="outline" disabled={detail.isFetching} onClick={() => void detail.refetch()}>삭제 상태 다시 확인</Button>}>
    <Rows>
      {kind === "workloads" && <Row href={`/records?${new URLSearchParams({ kind: "requests", workload: name, uid })}`} label="삭제한 서비스의 요청 기록" />}
      <RowDialog label="삭제 확인 대상" title="삭제 확인 대상"><SourceJSON inline value={{ name, uid, latest: current, status: detail.error?.status, response: r.result && { status: r.result.status, body: r.result.body }, error: mutation.error && { status: mutation.error.status, body: mutation.error.body } }} /></RowDialog>
      {canReview && <Row label="현재 상태 확인 후 삭제 다시 검토" onClick={() => { if (!detail.isFetching && !mutation.isPending) r.reviewDelete() }} />}
    </Rows>
  </Screen>
}
type Step = "name" | "model" | "preprocessor" | "datasetRef" | "slo" | ModelStep | "fields" | "targets" | "dataset" | "review"
const kindSteps = { models: ["artifacts", "inputs", "paths"], workloads: ["model", "preprocessor", "datasetRef", "slo"], preprocessors: ["fields", "targets"], datasets: ["dataset"] } as const
function Editor({ kind, r, models }: { kind: Kind; r: ResourceState; models: ReturnType<typeof useChillQuery<ModelList>> }) {
  const original = r.source
  const available = models.data?.items ?? []
  const preprocessors = useChillQuery<{ items: PreprocessorResource[] }>("v1/preprocessors", { enabled: kind === "workloads" })
  const datasets = useChillQuery<{ items: DatasetResource[] }>("v1/datasets", { enabled: kind === "workloads" })
  const [name, setName] = useState(original?.metadata.name ?? "")
  const [model, setModel] = useState(original && "modelRef" in original.spec ? original.spec.modelRef.name : "")
  const [preprocessorRef, setPreprocessorRef] = useState(original && "preprocessorRef" in original.spec ? original.spec.preprocessorRef.name : "")
  const [datasetRef, setDatasetRef] = useState(original && "modelRef" in original.spec ? original.spec.datasetRef?.name ?? "" : "")
  const [percentile, setPercentile] = useState(original && "slo" in original.spec ? String(original.spec.slo.percentile) : "99.5")
  const [latency, setLatency] = useState(original && "slo" in original.spec ? original.spec.slo.targetLatency : "100ms")
  const [spec, setSpec] = useState<ModelSpec>(original && "executionPaths" in original.spec ? structuredClone(original.spec) : { artifacts: [emptyArtifact()], executionPaths: [emptyPath()] })
  const [pre, setPre] = useState<PreprocessorSpec>(original && "input" in original.spec ? structuredClone(original.spec) : { input: { fields: [emptyField()] }, output: { fields: [{ name: "", format: "UTF8" }] }, artifacts: [emptyTarget()] })
  const [dataset, setDataset] = useState<DatasetSpec>(original && "source" in original.spec ? structuredClone(original.spec) : { source: { image: "", path: "" }, fields: [emptyField()] })
  const [reuse, setReuse] = useState("")
  const [index, setIndex] = useState(0)
  const [validation, setValidation] = useState("")
  const [manual, setManual] = useState(!!original || kind === "workloads")
  const selected = available.find(m => m.metadata.name === model)
  const savedPreprocessor = original && "preprocessorRef" in original.spec ? original.spec.preprocessorRef.name : ""
  const chosenPreprocessor = preprocessors.data?.items.find(p => p.metadata.name === preprocessorRef)
  const chosenDataset = datasets.data?.items.find(d => d.metadata.name === datasetRef)
  // The saved preprocessor stays selectable: ChillServer accepts it, the service just has no runnable path.
  const preprocessorProblem = chosenPreprocessor && preprocessorRef !== savedPreprocessor ? preprocessorGap(chosenPreprocessor.spec, selected) : ""
  const datasetProblem = chosenDataset ? datasetGap(chosenDataset.spec, chosenPreprocessor?.spec) : ""
  const steps: Step[] = [...(original ? [] : ["name" as const]), ...kindSteps[kind], "review"]
  const step = steps[index]
  const busy = r.mutation.isPending
  const locked = r.blocked || r.conflict
  const back: [string, string] = original ? [href(kind, original.metadata.name, r.expectedUID || original.metadata.uid), original.metadata.name] : [`/${kind}`, kindLabel(kind)]
  function draft(): Spec {
    if (kind === "workloads") return workloadSpec(model, preprocessorRef, datasetRef, percentile, latency)
    if (kind === "preprocessors") { if (!pre.input.fields.length || !pre.output.fields.length || !pre.artifacts.length) throw new Error("입력 필드, 출력 필드, 대상 모델 파일을 하나 이상 설정해 주세요."); return pre }
    if (kind === "datasets") { if (!dataset.fields.length) throw new Error("기록 필드를 하나 이상 설정해 주세요."); return dataset }
    if (!spec.artifacts.length || !spec.executionPaths.length || spec.artifacts.some(a => a.format === "rknn" && !a.input?.tensors.length)) throw new Error("모델 파일, RKNN 파일의 입력, 실행 방식을 하나 이상 설정해 주세요.")
    return spec
  }
  function next(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setValidation("")
    try {
      if (step === "review") { if (!locked) void r.submit(original ? "PUT" : "POST", original ? { spec: draft() } : { name, spec: draft() }); return }
      if (step === "artifacts" && (!spec.artifacts.length || spec.artifacts.some(a => !a.name.trim() || !a.format.trim() || !a.source.image.trim() || !a.source.architecture.trim() || !a.source.files.length || a.source.files.some(file => !file)))) throw new Error("파일마다 이름, 형식, 보관 주소, 대상 장비와 모델 파일 위치를 적어 주세요. 위에서 설정할 파일을 바꿀 수 있어요.")
      if (step === "inputs" && spec.artifacts.some(a => a.format === "rknn" && (!a.input?.tensors.length || a.input.tensors.some(t => !t.name.trim())))) throw new Error("RKNN 모델 파일에 입력 이름과 크기를 설정해 주세요. 위에서 설정할 파일을 바꿀 수 있어요.")
      if (step === "paths" && (!spec.executionPaths.length || spec.executionPaths.some(p => !p.name.trim() || !p.artifact || !p.runtime.backend.trim()))) throw new Error("실행 방식마다 이름, 파일과 처리 방식을 정해 주세요.")
      // Same pairs as ChillServer (internal/modelcatalog/catalog.go:107-123): firstfloor with a backend its artifact format allows.
      const invalid = step === "paths" ? spec.executionPaths.find(p => p.runtime.family !== "firstfloor" || !(backendsByFormat[spec.artifacts.find(a => a.name === p.artifact)?.format ?? ""] ?? []).includes(p.runtime.backend)) : undefined
      if (invalid) throw new Error(`${invalid.name}: 고른 모델 파일 형식에서 쓸 수 없는 처리 방식이에요. 처리 방식을 다시 골라 주세요.`)
      if (step === "preprocessor" && preprocessorProblem) throw new Error(`${preprocessorRef}: ${preprocessorProblem}. 다른 전처리기를 골라 주세요.`)
      if (step === "datasetRef" && datasetProblem) throw new Error(`${datasetRef}: ${datasetProblem}. 다른 데이터셋을 고르거나 데이터셋 없이 진행해 주세요.`)
      if (steps[index + 1] === "review") draft()
      setIndex(index + 1)
    } catch (error) { setValidation((error as Error).message) }
  }

  if (r.conflict) return <Screen back={back} status={<StatusBadge tone="warn">저장 전 확인</StatusBadge>} title="동시 변경 충돌" action={<Button disabled={!r.current || busy || r.current.metadata.uid !== original?.metadata.uid} onClick={r.adopt}>최신 상태를 기준으로 계속 작성</Button>}>
    <Rows>
      {original && <RowDialog label="작성을 시작한 설정" title="작성을 시작한 설정"><DefinitionSummary spec={original.spec} /></RowDialog>}
      {r.current && <RowDialog label="최신 설정" title="최신 설정"><DefinitionSummary spec={r.current.spec} /></RowDialog>}
      <RowDialog label="원본 자료 보기" title="작성 기준과 최신 원본"><SourceJSON inline value={{ before: original, latest: r.detail.data }} /></RowDialog>
    </Rows>
  </Screen>

  if (r.result) {
    const status = r.result.status
    const saved = r.result.data && typeof r.result.data === "object" ? unwrap(r.result.data as Resource).metadata : undefined
    return <Screen back={back} status={<StatusBadge tone="ok">저장됨</StatusBadge>} title={status === 201 ? "등록 완료" : status === 202 ? "변경 접수" : "변경 응답"} action={<Button asChild><Link href={!original && saved ? href(kind, saved.name, saved.uid) : back[0]}>{original ? "상세" : "등록 상세"}</Link></Button>}>
      <Rows>
        <RowDialog label="저장 응답 원본" title="저장 응답 원본"><SourceJSON inline value={{ status, body: r.result.body }} /></RowDialog>
        {original && <Row label="계속 수정하기" onClick={r.clearResult} />}
      </Rows>
    </Screen>
  }
  if (!manual && kind !== "workloads") return <CatalogRegistration kind={kind} label={kindLabel(kind)} r={r} problem={<Problem r={r} kind={kind} />} manual={() => setManual(true)} />

  let reviewed: Spec | undefined
  try { if (step === "review") reviewed = draft() } catch { reviewed = undefined }
  const titles: Record<Step, string> = {
    name: `${kindLabel(kind)} 이름`, model: "모델 선택", preprocessor: "모델 입출력 처리 선택", datasetRef: "데이터셋 선택", slo: "응답 목표",
    artifacts: "모델 파일", inputs: "모델 입력", paths: "실행 방식",
    fields: "입출력 필드", targets: "대상 모델 파일", dataset: "데이터 위치·필드",
    review: original ? "변경 검토" : "등록 검토",
  }
  const ledes: Record<Step, string> = {
    name: "", model: "",
    preprocessor: "전처리기는 원본 입력을 모델 입력으로 바꿔요. 고른 모델의 파일에 맞춘 전처리기만 고를 수 있어요.",
    datasetRef: "측정은 데이터셋에서 표본을 뽑아요. 전처리기 입력을 같은 이름과 형식으로 모두 담고, 크기가 입력 상한 이하인 데이터셋만 고를 수 있어요. 최종 확인은 저장할 때 해요.",
    slo: "예: 99.5%라면 1,000건 중 995건이 정한 시간 안에 응답하도록 목표를 정해요. 요청을 기다리는 제한 시간과는 달라요. 바꾼 목표는 새로 받는 요청부터 적용돼요.",
    artifacts: "새 파일은 담당자가 알려 준 보관 주소와 파일 위치가 필요해요. 외부 저장소의 파일 목록은 여기서 조회할 수 없어요.",
    inputs: "RKNN 모델 파일이 요구하는 입력 이름과 크기를 옮겨 적어 주세요. ONNX 파일의 입력은 전처리기가 정해요.",
    paths: "모델 파일마다 어떤 장치에서 실행할지 정해요. 고를 수 있는 처리 방식은 파일 형식이 정해요. 정하는 것만으로 장비가 준비되지는 않고, 측정과 준비 확인이 필요해요.",
    fields: "필드 이름과 형식은 요청과 데이터셋에서 그대로 써요. 최대 크기는 바이트로 적어요 (1 MiB = 1048576). 최대 생성 토큰은 글을 만드는 모델에만 정해요.",
    targets: "대상 모델 파일마다 그 파일에 맞는 전처리 파일을 하나씩 정해요. 전처리기가 대상으로 삼은 모델 파일은 모델에서 뺄 수 없어요.",
    dataset: "데이터 위치 아래 profiling 폴더는 측정용, evaluation 폴더는 평가용이에요. 각 폴더는 기록 ID/필드 이름 파일로 이뤄져요.",
    review: !original ? "저장한 뒤 측정 결과와 서비스 준비 상태를 확인해 주세요." : kind === "workloads" ? "모델이나 전처리기를 바꾸면 새 요청을 잠시 멈추고 기존 요청을 마무리한 뒤 새 설정을 적용해요. 응답 목표만 바꾸면 새 요청부터, 데이터셋만 바꾸면 이후 측정부터 멈추지 않고 적용돼요." : kind === "models" || kind === "preprocessors" ? `${impactNotes[kind]} 적용 진행은 연결된 서비스에서 확인해 주세요.` : impactNotes[kind],
  }
  return <Screen back={back} status={<span className="text-sm text-muted-foreground">{original ? `${original.metadata.name} 수정` : `${kindLabel(kind)} 추가`} · {index + 1} / {steps.length}</span>} title={titles[step]}
    action={<div className="flex gap-3">{index > 0 && <Button type="button" variant="outline" className="flex-1" disabled={busy} onClick={() => { setIndex(index - 1); setValidation("") }}>이전</Button>}<Button type="submit" form="resource-editor" className="flex-[2]" disabled={locked}>{step !== "review" ? "다음" : busy ? "저장하고 있어요" : original ? "변경 저장하기" : `${kindLabel(kind)} 등록하기`}</Button></div>}>
    {ledes[step] && <details><summary className="min-h-11 cursor-pointer py-2">입력 도움말</summary>{ledes[step]}</details>}
    <form id="resource-editor" aria-describedby={validation ? "editor-validation" : undefined} onSubmit={next}><fieldset disabled={locked} className="grid gap-5">
      {step === "name" && <>
        <Field label={`${kindLabel(kind)} 이름`}><Input aria-label={`${kindLabel(kind)} 이름`} required value={name} onChange={event => setName(event.target.value)} /><span className="w-fit rounded border px-2 py-1 text-xs text-muted-foreground">등록 후 이름 변경 불가</span></Field>
        {kind === "models" && available.length > 0 && <Choices label="기존 모델 설정 가져오기" value={reuse} set={uid => { setReuse(uid); const existing = available.find(m => m.metadata.uid === uid); setSpec(existing ? structuredClone(existing.spec) : { artifacts: [emptyArtifact()], executionPaths: [emptyPath()] }) }} options={[{ value: "", label: "새 파일로 등록", hint: "파일과 실행 방식을 직접 알려 줘요" }, ...available.map(m => ({ value: m.metadata.uid, label: m.metadata.name, hint: `파일 ${m.spec.artifacts.length}개 · 실행 방식 ${m.spec.executionPaths.length}개를 가져와요` }))]} />}
      </>}
      {step === "model" && <QueryGate query={models}>
        {available.length || model ? <Choices label="사용할 모델" value={model} set={setModel} options={[...(model && !selected ? [{ value: model, label: model, hint: "지금 목록에서 확인되지 않아요" }] : []), ...available.map(m => ({ value: m.metadata.name, label: m.metadata.name, hint: `파일 ${m.spec.artifacts.length}개 · 실행 방식 ${m.spec.executionPaths.length}개` }))]} /> : <Empty title="고를 수 있는 모델이 없어요"><Button asChild variant="outline"><Link href="/models/new">모델 추가</Link></Button></Empty>}
        {selected && <Rows><RowDialog label="선택한 모델 살펴보기" value={selected.metadata.name} title={selected.metadata.name}><DefinitionSummary spec={selected.spec} /><Impact kind="models" affected={r.workloads.error ? undefined : connected(r.workloads.data, "models", selected.metadata.name, selected.metadata.uid)} /><Link href={href("models", selected.metadata.name, selected.metadata.uid)}>모델 상세 열기</Link></RowDialog></Rows>}
      </QueryGate>}
      {step === "preprocessor" && <QueryGate query={preprocessors}>
        {preprocessors.data?.items.length || preprocessorRef ? <Choices label="사용할 전처리기" value={preprocessorRef} set={name => {
          setPreprocessorRef(name)
          // A new service starts with the first compatible dataset; registering without one leaves it unable to accept requests.
          if (!original && !datasetRef) { const spec = preprocessors.data?.items.find(p => p.metadata.name === name)?.spec; const ok = datasets.data?.items.find(d => !datasetGap(d.spec, spec)); if (ok) setDatasetRef(ok.metadata.name) }
        }} options={[
          ...(preprocessorRef && !chosenPreprocessor ? [{ value: preprocessorRef, label: preprocessorRef, hint: "지금 목록에서 확인되지 않아요" }] : []),
          ...(preprocessors.data?.items ?? []).map(p => {
            const gap = preprocessorGap(p.spec, selected)
            return { value: p.metadata.name, label: p.metadata.name, hint: gap || conclusion(p.spec), disabled: !!gap && p.metadata.name !== savedPreprocessor }
          }),
        ]} /> : <Empty title="고를 수 있는 전처리기가 없어요"><Button asChild variant="outline"><Link href="/preprocessors/new">전처리기 추가</Link></Button></Empty>}
      </QueryGate>}
      {step === "datasetRef" && <QueryGate query={datasets}>
        <Choices label="측정에 쓸 데이터셋" value={datasetRef} set={setDatasetRef} options={[
          { value: "", label: original ? "데이터셋 정하지 않기" : "데이터셋 없이 등록", hint: "정할 때까지 측정할 수 없어서 요청을 받지 않아요 (데이터셋 없음)" },
          ...(datasetRef && !chosenDataset ? [{ value: datasetRef, label: datasetRef, hint: "지금 목록에서 확인되지 않아요" }] : []),
          ...(datasets.data?.items ?? []).map(d => {
            const gap = datasetGap(d.spec, chosenPreprocessor?.spec)
            return { value: d.metadata.name, label: d.metadata.name, hint: gap || conclusion(d.spec), disabled: !!gap }
          }),
        ]} />
      </QueryGate>}
      {step === "slo" && <><Field label="목표 비율 · %"><Input aria-label="목표 비율 · %" type="number" step="any" min="0" max="100" required value={percentile} onChange={event => setPercentile(event.target.value)} /></Field><details><summary className="min-h-11 cursor-pointer py-2">도움말</summary>0보다 크고 100 이하여야 해요.</details><DurationField value={latency} set={setLatency} /></>}
      {step === "fields" && <><RawFields label="입력 필드" fields={pre.input.fields} set={fields => setPre({ ...pre, input: { fields } })} blank={emptyField} /><RawFields label="출력 필드" fields={pre.output.fields} set={fields => setPre({ ...pre, output: { fields } })} blank={() => ({ name: "", format: "UTF8" as const })} /><Field label="최대 생성 토큰 · 선택"><Input aria-label="최대 생성 토큰 · 선택" type="number" min={1} max={2147483647} step={1} value={pre.maxNewTokens ?? ""} onChange={event => setPre({ ...pre, maxNewTokens: Number.isNaN(event.target.valueAsNumber) ? undefined : event.target.valueAsNumber })} /></Field></>}
      {step === "targets" && <QueryGate query={models}>{pre.artifacts.map((a, i) => {
        const change = (value: Partial<PreprocessorArtifact>) => setPre({ ...pre, artifacts: pre.artifacts.map((x, j) => j === i ? { ...x, ...value } : x) })
        const files = available.find(m => m.metadata.name === a.target.model)?.spec.artifacts ?? []
        return <fieldset key={i} className="grid gap-4 rounded-lg border p-4"><legend>대상 {i + 1}</legend>
          <Field label={`대상 ${i + 1} 모델`}><select required aria-label={`대상 ${i + 1} 모델`} className={select} value={a.target.model} onChange={event => change({ target: { model: event.target.value, artifact: "" } })}><option value="">모델 선택</option>{a.target.model && !available.some(m => m.metadata.name === a.target.model) && <option value={a.target.model}>{a.target.model} (목록에서 확인되지 않아요)</option>}{available.map(m => <option key={m.metadata.uid} value={m.metadata.name}>{m.metadata.name}</option>)}</select></Field>
          <Field label={`대상 ${i + 1} 모델 파일`}><select required aria-label={`대상 ${i + 1} 모델 파일`} className={select} value={a.target.artifact} onChange={event => change({ target: { ...a.target, artifact: event.target.value } })}><option value="">파일 선택</option>{a.target.artifact && !files.some(f => f.name === a.target.artifact) && <option value={a.target.artifact}>{a.target.artifact} (목록에서 확인되지 않아요)</option>}{files.map(f => <option key={f.name} value={f.name}>{f.name} · {f.format} · {f.source.architecture}</option>)}</select></Field>
          <Text label={`대상 ${i + 1} 전처리 파일 보관 주소`} value={a.source.image} set={image => change({ source: { ...a.source, image } })} /><Choice label={`대상 ${i + 1} 대상 장비 종류`} value={a.source.architecture} set={architecture => change({ source: { ...a.source, architecture } })} options={["amd64", "arm64"]} /><Text label={`대상 ${i + 1} 보관 이미지 안의 파일 위치`} value={a.source.path} set={path => change({ source: { ...a.source, path } })} />
          <Button type="button" variant="outline" onClick={() => setPre({ ...pre, artifacts: pre.artifacts.filter((_, j) => j !== i) })}>이 대상 빼기</Button></fieldset>
      })}<Button type="button" variant="outline" onClick={() => setPre({ ...pre, artifacts: [...pre.artifacts, emptyTarget()] })}>대상 모델 파일 추가</Button></QueryGate>}
      {step === "dataset" && <><Text label="데이터 보관 주소" value={dataset.source.image} set={image => setDataset({ ...dataset, source: { ...dataset.source, image } })} /><details><summary className="min-h-11 cursor-pointer py-2">도움말</summary>변하지 않는 정확한 버전의 주소를 적어 주세요. 예: registry.example/dataset@sha256:…</details><Text label="보관 이미지 안의 데이터 위치" value={dataset.source.path} set={path => setDataset({ ...dataset, source: { ...dataset.source, path } })} /><details><summary className="min-h-11 cursor-pointer py-2">도움말</summary>예: /data · 측정용 {subsets(dataset.source.path || "/data")[0]}, 평가용 {subsets(dataset.source.path || "/data")[1]}</details><RawFields label="기록 필드" fields={dataset.fields} set={fields => setDataset({ ...dataset, fields })} blank={emptyField} /></>}
      {(step === "artifacts" || step === "inputs" || step === "paths") && <ModelFields spec={spec} set={setSpec} step={step} models={available} />}
      {step === "review" && reviewed && <>
        <Facts items={[["이름", name], ...brief(reviewed).map(([label, value], i): [string, string] => { const before = original && brief(original.spec)[i]?.[1]; return [label, before === undefined || before === value ? value : `${before} → ${value}`] })]} />
        <Rows>
          {original && <RowDialog label="변경 전 자세히" title="변경 전"><DefinitionSummary spec={original.spec} /></RowDialog>}
          <RowDialog label={original ? "변경 후 자세히" : "등록 내용 자세히"} title={original ? "변경 후" : "등록 내용"}><DefinitionSummary spec={reviewed} /></RowDialog>
          {kind !== "workloads" && original && <RowDialog label="영향 받는 서비스" value={r.affected ? `${r.affected.length}개` : "확인 불가"} title="영향 받는 서비스"><Impact kind={kind} affected={r.affected} /></RowDialog>}
          <RowDialog label="원본 자료 보기" title="저장 원본과 비교 기준"><SourceJSON inline value={{ original, submitted: reviewed }} /></RowDialog>
        </Rows>
      </>}
    </fieldset></form>
    <Problem r={r} kind={kind} />
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
